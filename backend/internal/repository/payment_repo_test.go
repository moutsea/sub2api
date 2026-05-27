package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPaymentRepositoryCreditStripePaymentOrderAllowsDiscountedAmount(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &paymentRepository{db: db}
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, user_id, amount, amount_cents").
		WithArgs("cs_discount").
		WillReturnRows(paymentOrderRows().AddRow(
			"order_1", int64(7), 100.0, int64(10000), service.PaymentCurrencyCNY,
			service.PaymentProviderStripe, service.PaymentMethodWechat, "cs_discount", "", "",
			service.PaymentStatusPending, "", nil, nil, nil, now, now,
		))
	mock.ExpectExec("UPDATE payment_orders\\s+SET status = \\$2").
		WithArgs("order_1", service.PaymentStatusPaid, "pi_discount", "evt_discount", sqlmock.AnyArg(), nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("UPDATE users").
		WithArgs(int64(7), 100.0).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(125.0))
	mock.ExpectExec("UPDATE payment_orders\\s+SET credited_at").
		WithArgs("order_1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	order, credited, err := repo.CreditStripePaymentOrder(context.Background(), service.StripeCheckoutCompletion{
		SessionID:       "cs_discount",
		PaymentIntentID: "pi_discount",
		AmountCents:     7000,
		Currency:        service.PaymentCurrencyCNY,
		EventID:         "evt_discount",
	})

	require.NoError(t, err)
	require.True(t, credited)
	require.NotNil(t, order)
	require.Equal(t, service.PaymentStatusPaid, order.Status)
	require.Equal(t, 25.0, order.BalanceBefore)
	require.Equal(t, 125.0, order.BalanceAfter)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPaymentRepositoryCreditStripePaymentOrderRejectsOverAmount(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &paymentRepository{db: db}
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, user_id, amount, amount_cents").
		WithArgs("cs_over").
		WillReturnRows(paymentOrderRows().AddRow(
			"order_1", int64(7), 100.0, int64(10000), service.PaymentCurrencyCNY,
			service.PaymentProviderStripe, service.PaymentMethodWechat, "cs_over", "", "",
			service.PaymentStatusPending, "", nil, nil, nil, now, now,
		))
	mock.ExpectExec("UPDATE payment_orders\\s+SET status = \\$2").
		WithArgs("order_1", service.PaymentStatusMismatch, "pi_over", "evt_over", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	order, credited, err := repo.CreditStripePaymentOrder(context.Background(), service.StripeCheckoutCompletion{
		SessionID:       "cs_over",
		PaymentIntentID: "pi_over",
		AmountCents:     12000,
		Currency:        service.PaymentCurrencyCNY,
		EventID:         "evt_over",
	})

	require.Error(t, err)
	require.False(t, credited)
	require.NotNil(t, order)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPaymentRepositoryCreditStripePaymentOrderRejectsBelowMinimumPaidRatio(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &paymentRepository{db: db}
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, user_id, amount, amount_cents").
		WithArgs("cs_low_discount").
		WillReturnRows(paymentOrderRows().AddRow(
			"order_1", int64(7), 100.0, int64(10000), service.PaymentCurrencyCNY,
			service.PaymentProviderStripe, service.PaymentMethodWechat, "cs_low_discount", "", "",
			service.PaymentStatusPending, "", nil, nil, nil, now, now,
		))
	mock.ExpectExec("UPDATE payment_orders\\s+SET status = \\$2").
		WithArgs("order_1", service.PaymentStatusMismatch, "pi_low_discount", "evt_low_discount", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	order, credited, err := repo.CreditStripePaymentOrder(context.Background(), service.StripeCheckoutCompletion{
		SessionID:       "cs_low_discount",
		PaymentIntentID: "pi_low_discount",
		AmountCents:     6900,
		Currency:        service.PaymentCurrencyCNY,
		EventID:         "evt_low_discount",
	})

	require.Error(t, err)
	require.False(t, credited)
	require.NotNil(t, order)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPaymentRepositoryCreditStripePaymentOrderRejectsZeroAmount(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &paymentRepository{db: db}
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, user_id, amount, amount_cents").
		WithArgs("cs_zero").
		WillReturnRows(paymentOrderRows().AddRow(
			"order_1", int64(7), 100.0, int64(10000), service.PaymentCurrencyCNY,
			service.PaymentProviderStripe, service.PaymentMethodWechat, "cs_zero", "", "",
			service.PaymentStatusPending, "", nil, nil, nil, now, now,
		))
	mock.ExpectExec("UPDATE payment_orders\\s+SET status = \\$2").
		WithArgs("order_1", service.PaymentStatusMismatch, "pi_zero", "evt_zero", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	order, credited, err := repo.CreditStripePaymentOrder(context.Background(), service.StripeCheckoutCompletion{
		SessionID:       "cs_zero",
		PaymentIntentID: "pi_zero",
		AmountCents:     0,
		Currency:        service.PaymentCurrencyCNY,
		EventID:         "evt_zero",
	})

	require.Error(t, err)
	require.False(t, credited)
	require.NotNil(t, order)
	require.NoError(t, mock.ExpectationsWereMet())
}

func paymentOrderRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"user_id",
		"amount",
		"amount_cents",
		"currency",
		"provider",
		"payment_method",
		"stripe_session_id",
		"stripe_payment_intent_id",
		"stripe_event_id",
		"status",
		"checkout_url",
		"expires_at",
		"paid_at",
		"credited_at",
		"created_at",
		"updated_at",
	})
}
