package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type paymentRepository struct {
	db *sql.DB
}

const stripeMinimumPaidRatioPermille int64 = 700

func NewPaymentRepository(db *sql.DB) service.PaymentRepository {
	return &paymentRepository{db: db}
}

func (r *paymentRepository) CreatePaymentOrder(ctx context.Context, order *service.PaymentOrder) error {
	if order == nil {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO payment_orders (
			id, user_id, amount, amount_cents, currency, provider, payment_method, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, order.ID, order.UserID, order.Amount, order.AmountCents, order.Currency, order.Provider, order.PaymentMethod, order.Status)
	return err
}

func (r *paymentRepository) AttachStripeSession(ctx context.Context, orderID, sessionID, checkoutURL string, expiresAt *time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE payment_orders
		SET stripe_session_id = $2,
		    checkout_url = $3,
		    expires_at = $4,
		    status = $5,
		    updated_at = NOW()
		WHERE id = $1
	`, orderID, sessionID, checkoutURL, expiresAt, service.PaymentStatusPending)
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrPaymentOrderNotFound)
}

func (r *paymentRepository) MarkPaymentOrderFailed(ctx context.Context, orderID, reason string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = $2,
		    failure_reason = $3,
		    updated_at = NOW()
		WHERE id = $1
	`, orderID, service.PaymentStatusFailed, reason)
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrPaymentOrderNotFound)
}

func (r *paymentRepository) MarkPaymentOrderFailedBySession(ctx context.Context, sessionID, stripeEventID string, rawEvent []byte) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = $2,
		    stripe_event_id = $3,
		    raw_event = COALESCE($4::jsonb, raw_event),
		    updated_at = NOW()
		WHERE stripe_session_id = $1
	`, sessionID, service.PaymentStatusFailed, stripeEventID, rawEventJSONParam(rawEvent))
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrPaymentOrderNotFound)
}

func (r *paymentRepository) MarkStripeSessionPending(ctx context.Context, sessionID, stripeEventID string, rawEvent []byte) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = $2,
		    stripe_event_id = $3,
		    raw_event = COALESCE($4::jsonb, raw_event),
		    updated_at = NOW()
		WHERE stripe_session_id = $1
	`, sessionID, service.PaymentStatusPending, stripeEventID, rawEventJSONParam(rawEvent))
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrPaymentOrderNotFound)
}

func (r *paymentRepository) CreditStripePaymentOrder(ctx context.Context, completion service.StripeCheckoutCompletion) (*service.PaymentOrder, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	order, err := r.getOrderForUpdate(ctx, tx, completion.SessionID)
	if err != nil {
		return nil, false, err
	}
	// Stripe promotion codes reduce checkout.session.amount_total after the
	// order is created. Allow discounted totals only when the paid amount is
	// still at least 70% of the original order amount.
	if completion.AmountCents > order.AmountCents ||
		completion.AmountCents < minimumAcceptedStripeAmountCents(order.AmountCents) {
		if err := r.markMismatchInTx(ctx, tx, order.ID, completion); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return order, false, service.ErrInvalidStripeWebhook.WithMetadata(map[string]string{"reason": "amount_mismatch"})
	}
	if completion.Currency != "" && completion.Currency != order.Currency {
		if err := r.markMismatchInTx(ctx, tx, order.ID, completion); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return order, false, service.ErrInvalidStripeWebhook.WithMetadata(map[string]string{"reason": "currency_mismatch"})
	}

	paidAt := completion.PaidAt
	if paidAt.IsZero() {
		paidAt = time.Now()
	}
	if err := r.markPaidInTx(ctx, tx, order.ID, completion, paidAt); err != nil {
		return nil, false, err
	}

	if order.CreditedAt != nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return order, false, nil
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE users
		SET balance = balance + $2,
		    updated_at = NOW()
		WHERE id = $1
		  AND deleted_at IS NULL
	`, order.UserID, order.Amount)
	if err != nil {
		return nil, false, err
	}
	if err := requireAffected(res, service.ErrUserNotFound); err != nil {
		return nil, false, err
	}

	now := time.Now()
	res, err = tx.ExecContext(ctx, `
		UPDATE payment_orders
		SET credited_at = $2,
		    updated_at = NOW()
		WHERE id = $1
		  AND credited_at IS NULL
	`, order.ID, now)
	if err != nil {
		return nil, false, err
	}
	if err := requireAffected(res, service.ErrPaymentOrderNotFound); err != nil {
		return nil, false, err
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	order.Status = service.PaymentStatusPaid
	order.PaidAt = &paidAt
	order.CreditedAt = &now
	order.StripePaymentIntentID = completion.PaymentIntentID
	order.StripeEventID = completion.EventID
	return order, true, nil
}

func minimumAcceptedStripeAmountCents(orderAmountCents int64) int64 {
	if orderAmountCents <= 0 {
		return 1
	}
	minimum := (orderAmountCents*stripeMinimumPaidRatioPermille + 999) / 1000
	if minimum < 1 {
		return 1
	}
	return minimum
}

func (r *paymentRepository) getOrderForUpdate(ctx context.Context, tx *sql.Tx, sessionID string) (*service.PaymentOrder, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, user_id, amount, amount_cents, currency, provider, payment_method,
		       COALESCE(stripe_session_id, ''), COALESCE(stripe_payment_intent_id, ''),
		       COALESCE(stripe_event_id, ''), status, COALESCE(checkout_url, ''),
		       expires_at, paid_at, credited_at, created_at, updated_at
		FROM payment_orders
		WHERE stripe_session_id = $1
		FOR UPDATE
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrPaymentOrderNotFound
	}

	var order service.PaymentOrder
	var expiresAt, paidAt, creditedAt sql.NullTime
	if err := rows.Scan(
		&order.ID,
		&order.UserID,
		&order.Amount,
		&order.AmountCents,
		&order.Currency,
		&order.Provider,
		&order.PaymentMethod,
		&order.StripeSessionID,
		&order.StripePaymentIntentID,
		&order.StripeEventID,
		&order.Status,
		&order.CheckoutURL,
		&expiresAt,
		&paidAt,
		&creditedAt,
		&order.CreatedAt,
		&order.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		order.ExpiresAt = &expiresAt.Time
	}
	if paidAt.Valid {
		order.PaidAt = &paidAt.Time
	}
	if creditedAt.Valid {
		order.CreditedAt = &creditedAt.Time
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &order, nil
}

func (r *paymentRepository) markPaidInTx(ctx context.Context, tx *sql.Tx, orderID string, completion service.StripeCheckoutCompletion, paidAt time.Time) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = $2,
		    stripe_payment_intent_id = NULLIF($3, ''),
		    stripe_event_id = $4,
		    paid_at = COALESCE(paid_at, $5),
		    raw_event = COALESCE($6::jsonb, raw_event),
		    updated_at = NOW()
		WHERE id = $1
	`, orderID, service.PaymentStatusPaid, completion.PaymentIntentID, completion.EventID, paidAt, rawEventJSONParam(completion.RawEvent))
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrPaymentOrderNotFound)
}

func (r *paymentRepository) markMismatchInTx(ctx context.Context, tx *sql.Tx, orderID string, completion service.StripeCheckoutCompletion) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE payment_orders
		SET status = $2,
		    stripe_payment_intent_id = NULLIF($3, ''),
		    stripe_event_id = $4,
		    raw_event = COALESCE($5::jsonb, raw_event),
		    updated_at = NOW()
		WHERE id = $1
	`, orderID, service.PaymentStatusMismatch, completion.PaymentIntentID, completion.EventID, rawEventJSONParam(completion.RawEvent))
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrPaymentOrderNotFound)
}

func rawEventJSONParam(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func requireAffected(res sql.Result, notFound error) error {
	if res == nil {
		return nil
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
}

func (r *paymentRepository) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams) ([]service.PaymentOrder, *pagination.PaginationResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_orders WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, nil, err
	}
	if total == 0 {
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return nil, paginationResultFromTotal(0, params), nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, user_id, amount, amount_cents, currency, provider, payment_method,
		       COALESCE(stripe_session_id, ''), COALESCE(stripe_payment_intent_id, ''),
		       COALESCE(stripe_event_id, ''), status, COALESCE(checkout_url, ''),
		       expires_at, paid_at, credited_at, created_at, updated_at
		FROM payment_orders
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, userID, params.Limit(), params.Offset())
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	var orders []service.PaymentOrder
	for rows.Next() {
		var o service.PaymentOrder
		var expiresAt, paidAt, creditedAt sql.NullTime
		if err := rows.Scan(
			&o.ID, &o.UserID, &o.Amount, &o.AmountCents, &o.Currency, &o.Provider, &o.PaymentMethod,
			&o.StripeSessionID, &o.StripePaymentIntentID, &o.StripeEventID,
			&o.Status, &o.CheckoutURL, &expiresAt, &paidAt, &creditedAt, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		if expiresAt.Valid {
			o.ExpiresAt = &expiresAt.Time
		}
		if paidAt.Valid {
			o.PaidAt = &paidAt.Time
		}
		if creditedAt.Valid {
			o.CreditedAt = &creditedAt.Time
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return orders, paginationResultFromTotal(total, params), nil
}

var _ service.PaymentRepository = (*paymentRepository)(nil)
