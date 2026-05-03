package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/google/uuid"
)

const (
	PaymentProviderStripe = "stripe"

	PaymentMethodAlipay   = "alipay"
	PaymentMethodWechat   = "wechat_pay"
	PaymentCurrencyCNY    = "cny"
	PaymentStatusCreated  = "created"
	PaymentStatusPending  = "pending"
	PaymentStatusPaid     = "paid"
	PaymentStatusFailed   = "failed"
	PaymentStatusMismatch = "amount_mismatch"

	stripeSignatureTolerance = 5 * time.Minute
)

var (
	ErrPaymentNotConfigured = infraerrors.ServiceUnavailable("PAYMENT_NOT_CONFIGURED", "payment is not configured")
	ErrInvalidPaymentMethod = infraerrors.BadRequest("INVALID_PAYMENT_METHOD", "invalid payment method")
	ErrInvalidPaymentAmount = infraerrors.BadRequest("INVALID_PAYMENT_AMOUNT", "invalid recharge amount")
	ErrPaymentOrderNotFound = infraerrors.NotFound("PAYMENT_ORDER_NOT_FOUND", "payment order not found")
	ErrInvalidStripeWebhook = infraerrors.BadRequest("INVALID_STRIPE_WEBHOOK", "invalid Stripe webhook")
)

type PaymentOrder struct {
	ID                    string
	UserID                int64
	Amount                float64
	AmountCents           int64
	Currency              string
	Provider              string
	PaymentMethod         string
	StripeSessionID       string
	StripePaymentIntentID string
	StripeEventID         string
	Status                string
	CheckoutURL           string
	ExpiresAt             *time.Time
	PaidAt                *time.Time
	CreditedAt            *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type CreateCheckoutSessionRequest struct {
	UserID        int64
	Amount        float64
	PaymentMethod string
	SuccessURL    string
	CancelURL     string
}

type CreateCheckoutSessionResult struct {
	OrderID     string  `json:"order_id"`
	SessionID   string  `json:"session_id"`
	CheckoutURL string  `json:"checkout_url"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
}

type StripeCheckoutSessionParams struct {
	OrderID       string
	UserID        int64
	Amount        float64
	AmountCents   int64
	Currency      string
	PaymentMethod string
	SuccessURL    string
	CancelURL     string
}

type StripeCheckoutSession struct {
	ID            string            `json:"id"`
	URL           string            `json:"url"`
	AmountTotal   int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
	PaymentStatus string            `json:"payment_status"`
	PaymentIntent string            `json:"payment_intent"`
	ExpiresAt     int64             `json:"expires_at"`
	Metadata      map[string]string `json:"metadata"`
}

type StripeCheckoutCompletion struct {
	SessionID       string
	PaymentIntentID string
	AmountCents     int64
	Currency        string
	EventID         string
	EventType       string
	RawEvent        []byte
	PaidAt          time.Time
}

type PaymentWebhookResult struct {
	EventID   string `json:"event_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status"`
	Credited  bool   `json:"credited"`
	Ignored   bool   `json:"ignored"`
}

type PaymentRepository interface {
	CreatePaymentOrder(ctx context.Context, order *PaymentOrder) error
	AttachStripeSession(ctx context.Context, orderID, sessionID, checkoutURL string, expiresAt *time.Time) error
	MarkPaymentOrderFailed(ctx context.Context, orderID, reason string) error
	MarkPaymentOrderFailedBySession(ctx context.Context, sessionID, stripeEventID string, rawEvent []byte) error
	MarkStripeSessionPending(ctx context.Context, sessionID, stripeEventID string, rawEvent []byte) error
	CreditStripePaymentOrder(ctx context.Context, completion StripeCheckoutCompletion) (*PaymentOrder, bool, error)
	ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams) ([]PaymentOrder, *pagination.PaginationResult, error)
}

type stripeCheckoutClient interface {
	CreateCheckoutSession(ctx context.Context, params StripeCheckoutSessionParams) (*StripeCheckoutSession, error)
}

type PaymentService struct {
	repo                 PaymentRepository
	stripe               stripeCheckoutClient
	billingCacheService  *BillingCacheService
	authCacheInvalidator APIKeyAuthCacheInvalidator
	cfg                  *config.Config
}

func NewPaymentService(
	repo PaymentRepository,
	billingCacheService *BillingCacheService,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	cfg *config.Config,
) *PaymentService {
	svc := &PaymentService{
		repo:                 repo,
		billingCacheService:  billingCacheService,
		authCacheInvalidator: authCacheInvalidator,
		cfg:                  cfg,
	}
	svc.stripe = newStripePaymentClient(cfg)
	return svc
}

func (s *PaymentService) SetStripeClient(client stripeCheckoutClient) {
	s.stripe = client
}

func (s *PaymentService) ListOrders(ctx context.Context, userID int64, params pagination.PaginationParams) ([]PaymentOrder, *pagination.PaginationResult, error) {
	return s.repo.ListByUserID(ctx, userID, params)
}

func (s *PaymentService) CreateCheckoutSession(ctx context.Context, req CreateCheckoutSessionRequest) (*CreateCheckoutSessionResult, error) {
	if err := s.ensureStripeConfigured(); err != nil {
		return nil, err
	}
	if s.repo == nil {
		return nil, infraerrors.InternalServer("PAYMENT_REPOSITORY_MISSING", "payment repository is not configured")
	}
	if s.stripe == nil {
		return nil, infraerrors.InternalServer("STRIPE_CLIENT_MISSING", "Stripe client is not configured")
	}

	paymentMethod, err := normalizeStripePaymentMethod(req.PaymentMethod)
	if err != nil {
		return nil, err
	}
	amount, amountCents, err := s.normalizeRechargeAmount(req.Amount)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SuccessURL) == "" || strings.TrimSpace(req.CancelURL) == "" {
		return nil, infraerrors.BadRequest("INVALID_RETURN_URL", "payment return URL is required")
	}

	currency := s.stripeCurrency()
	order := &PaymentOrder{
		ID:            uuid.NewString(),
		UserID:        req.UserID,
		Amount:        amount,
		AmountCents:   amountCents,
		Currency:      currency,
		Provider:      PaymentProviderStripe,
		PaymentMethod: paymentMethod,
		Status:        PaymentStatusCreated,
	}
	if err := s.repo.CreatePaymentOrder(ctx, order); err != nil {
		log.Printf("[Payment] create order failed: user_id=%d amount=%.2f err=%v", req.UserID, amount, err)
		return nil, fmt.Errorf("create payment order: %w", err)
	}

	session, err := s.stripe.CreateCheckoutSession(ctx, StripeCheckoutSessionParams{
		OrderID:       order.ID,
		UserID:        req.UserID,
		Amount:        amount,
		AmountCents:   amountCents,
		Currency:      currency,
		PaymentMethod: paymentMethod,
		SuccessURL:    req.SuccessURL,
		CancelURL:     req.CancelURL,
	})
	if err != nil {
		_ = s.repo.MarkPaymentOrderFailed(ctx, order.ID, "stripe_create_failed")
		return nil, err
	}
	if session == nil || strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.URL) == "" {
		_ = s.repo.MarkPaymentOrderFailed(ctx, order.ID, "stripe_create_empty_session")
		return nil, infraerrors.ServiceUnavailable("STRIPE_SESSION_INVALID", "Stripe did not return a checkout session")
	}

	var expiresAt *time.Time
	if session.ExpiresAt > 0 {
		t := time.Unix(session.ExpiresAt, 0)
		expiresAt = &t
	}
	if err := s.repo.AttachStripeSession(ctx, order.ID, session.ID, session.URL, expiresAt); err != nil {
		log.Printf("[Payment] attach stripe session failed: order_id=%s session_id=%s err=%v", order.ID, session.ID, err)
		return nil, fmt.Errorf("attach stripe session: %w", err)
	}

	log.Printf("[Payment] checkout session created: order_id=%s session_id=%s user_id=%d amount=%.2f method=%s", order.ID, session.ID, req.UserID, amount, paymentMethod)

	return &CreateCheckoutSessionResult{
		OrderID:     order.ID,
		SessionID:   session.ID,
		CheckoutURL: session.URL,
		Amount:      amount,
		Currency:    currency,
	}, nil
}

func (s *PaymentService) HandleStripeWebhook(ctx context.Context, payload []byte, signatureHeader string) (*PaymentWebhookResult, error) {
	if err := s.ensureStripeConfigured(); err != nil {
		log.Printf("[Payment] webhook rejected: stripe not configured")
		return nil, err
	}
	webhookSecret := strings.TrimSpace(s.cfg.Payment.Stripe.WebhookSecret)
	if webhookSecret == "" {
		log.Printf("[Payment] webhook rejected: webhook_secret is empty")
		return nil, ErrPaymentNotConfigured.WithMetadata(map[string]string{"field": "payment.stripe.webhook_secret"})
	}
	if err := verifyStripeSignature(payload, signatureHeader, webhookSecret, time.Now()); err != nil {
		log.Printf("[Payment] webhook signature verification failed: %v", err)
		return nil, ErrInvalidStripeWebhook.WithCause(err)
	}

	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object StripeCheckoutSession `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		log.Printf("[Payment] webhook payload unmarshal failed: %v", err)
		return nil, ErrInvalidStripeWebhook.WithCause(err)
	}
	session := event.Data.Object
	log.Printf("[Payment] webhook received: event_id=%s type=%s session_id=%s", event.ID, event.Type, session.ID)

	result := &PaymentWebhookResult{
		EventID:   event.ID,
		EventType: event.Type,
		SessionID: session.ID,
		Status:    "received",
	}

	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if strings.TrimSpace(session.ID) == "" {
			log.Printf("[Payment] webhook rejected: empty session_id in event %s", event.ID)
			return nil, ErrInvalidStripeWebhook.WithMetadata(map[string]string{"field": "data.object.id"})
		}
		if event.Type == "checkout.session.completed" && strings.ToLower(session.PaymentStatus) != "paid" {
			if err := s.repo.MarkStripeSessionPending(ctx, session.ID, event.ID, payload); err != nil {
				log.Printf("[Payment] webhook mark pending failed: session_id=%s err=%v", session.ID, err)
				return nil, err
			}
			log.Printf("[Payment] webhook: session %s marked pending (payment_status=%s)", session.ID, session.PaymentStatus)
			result.Status = "pending_payment"
			return result, nil
		}
		paidAt := time.Now()
		order, credited, err := s.repo.CreditStripePaymentOrder(ctx, StripeCheckoutCompletion{
			SessionID:       session.ID,
			PaymentIntentID: session.PaymentIntent,
			AmountCents:     session.AmountTotal,
			Currency:        strings.ToLower(session.Currency),
			EventID:         event.ID,
			EventType:       event.Type,
			RawEvent:        payload,
			PaidAt:          paidAt,
		})
		if err != nil {
			log.Printf("[Payment] webhook credit failed: session_id=%s err=%v", session.ID, err)
			return nil, err
		}
		result.Status = PaymentStatusPaid
		result.Credited = credited
		if credited && order != nil {
			log.Printf("[Payment] webhook: payment credited, user_id=%d amount=%.2f order_id=%s", order.UserID, order.Amount, order.ID)
			s.invalidateBillingCaches(ctx, order.UserID)
		} else {
			log.Printf("[Payment] webhook: payment already credited or order nil, session_id=%s credited=%v", session.ID, credited)
		}
		return result, nil
	case "checkout.session.async_payment_failed":
		if strings.TrimSpace(session.ID) == "" {
			log.Printf("[Payment] webhook rejected: empty session_id in event %s", event.ID)
			return nil, ErrInvalidStripeWebhook.WithMetadata(map[string]string{"field": "data.object.id"})
		}
		if err := s.repo.MarkPaymentOrderFailedBySession(ctx, session.ID, event.ID, payload); err != nil {
			log.Printf("[Payment] webhook mark failed error: session_id=%s err=%v", session.ID, err)
			return nil, err
		}
		log.Printf("[Payment] webhook: payment failed, session_id=%s event_id=%s", session.ID, event.ID)
		result.Status = PaymentStatusFailed
		return result, nil
	default:
		log.Printf("[Payment] webhook: ignored event type=%s event_id=%s", event.Type, event.ID)
		result.Status = "ignored"
		result.Ignored = true
		return result, nil
	}
}

func (s *PaymentService) ensureStripeConfigured() error {
	if s == nil || s.cfg == nil || !s.cfg.Payment.Stripe.Enabled {
		return ErrPaymentNotConfigured
	}
	if strings.TrimSpace(s.cfg.Payment.Stripe.SecretKey) == "" {
		return ErrPaymentNotConfigured.WithMetadata(map[string]string{"field": "payment.stripe.secret_key"})
	}
	return nil
}

func (s *PaymentService) stripeCurrency() string {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.Payment.Stripe.Currency) == "" {
		return PaymentCurrencyCNY
	}
	return strings.ToLower(strings.TrimSpace(s.cfg.Payment.Stripe.Currency))
}

func (s *PaymentService) normalizeRechargeAmount(input float64) (float64, int64, error) {
	if math.IsNaN(input) || math.IsInf(input, 0) || input <= 0 {
		return 0, 0, ErrInvalidPaymentAmount.WithMetadata(map[string]string{"min": "0"})
	}
	minAmount := 0.0
	if s != nil && s.cfg != nil {
		minAmount = s.cfg.Payment.Stripe.MinRechargeAmount
	}
	if input < minAmount {
		return 0, 0, ErrInvalidPaymentAmount.WithMetadata(map[string]string{
			"min": strconv.FormatFloat(minAmount, 'f', 2, 64),
		})
	}
	amountCents := int64(math.Round(input * 100))
	if amountCents <= 0 {
		return 0, 0, ErrInvalidPaymentAmount.WithMetadata(map[string]string{"min": "0"})
	}
	amount := float64(amountCents) / 100
	return amount, amountCents, nil
}

func (s *PaymentService) invalidateBillingCaches(ctx context.Context, userID int64) {
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, userID)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
}

func normalizeStripePaymentMethod(method string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case PaymentMethodAlipay:
		return PaymentMethodAlipay, nil
	case PaymentMethodWechat, "wechat", "weixin", "wxpay":
		return PaymentMethodWechat, nil
	default:
		return "", ErrInvalidPaymentMethod.WithMetadata(map[string]string{
			"allowed": PaymentMethodAlipay + "," + PaymentMethodWechat,
		})
	}
}

func verifyStripeSignature(payload []byte, header string, secret string, now time.Time) error {
	if strings.TrimSpace(header) == "" {
		return fmt.Errorf("missing Stripe-Signature header")
	}
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("missing Stripe webhook secret")
	}

	var timestamp string
	signatures := make([]string, 0, 2)
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "t":
			timestamp = strings.TrimSpace(value)
		case "v1":
			signatures = append(signatures, strings.TrimSpace(value))
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return fmt.Errorf("missing timestamp or v1 signature")
	}
	unixTimestamp, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	eventTime := time.Unix(unixTimestamp, 0)
	if delta := now.Sub(eventTime); delta > stripeSignatureTolerance || delta < -stripeSignatureTolerance {
		return fmt.Errorf("timestamp outside tolerance")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)

	for _, signature := range signatures {
		decoded, err := hex.DecodeString(signature)
		if err != nil {
			continue
		}
		if hmac.Equal(decoded, expected) {
			return nil
		}
	}
	return fmt.Errorf("no matching signature")
}

func stripeServiceError(reason, message string) *infraerrors.ApplicationError {
	return infraerrors.New(http.StatusBadGateway, reason, message)
}
