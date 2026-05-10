package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestVerifyStripeSignature(t *testing.T) {
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed"}`)
	secret := "whsec_test"
	now := time.Unix(1710000000, 0)
	header := signedStripeHeader(payload, secret, now)

	require.NoError(t, verifyStripeSignature(payload, header, secret, now))
	require.Error(t, verifyStripeSignature([]byte(`{"id":"evt_2"}`), header, secret, now))
	require.Error(t, verifyStripeSignature(payload, header, "wrong_secret", now))
	require.Error(t, verifyStripeSignature(payload, header, secret, now.Add(10*time.Minute)))
}

func TestPaymentServiceCreateCheckoutSession(t *testing.T) {
	repo := &fakePaymentRepo{}
	stripe := &fakeStripeCheckoutClient{
		session: &StripeCheckoutSession{ID: "cs_test", URL: "https://checkout.stripe.com/c/pay/cs_test"},
	}
	svc := NewPaymentService(repo, nil, nil, &config.Config{
		Payment: config.PaymentConfig{Stripe: config.StripePaymentConfig{
			Enabled:           true,
			SecretKey:         "sk_test",
			Currency:          "cny",
			MinRechargeAmount: 0,
		}},
	})
	svc.SetStripeClient(stripe)

	result, err := svc.CreateCheckoutSession(context.Background(), CreateCheckoutSessionRequest{
		UserID:        7,
		Amount:        100,
		PaymentMethod: "wechat",
		SuccessURL:    "https://example.com/recharge?success=1",
		CancelURL:     "https://example.com/recharge?cancel=1",
	})
	require.NoError(t, err)
	require.Equal(t, "cs_test", result.SessionID)
	require.Equal(t, "https://checkout.stripe.com/c/pay/cs_test", result.CheckoutURL)
	require.NotEmpty(t, result.OrderID)
	require.Equal(t, int64(10000), repo.created.AmountCents)
	require.Equal(t, PaymentMethodWechat, repo.created.PaymentMethod)
	require.Equal(t, int64(7), stripe.params.UserID)
	require.Equal(t, PaymentMethodWechat, stripe.params.PaymentMethod)
	require.Equal(t, int64(10000), stripe.params.AmountCents)
	require.Equal(t, "cny", stripe.params.Currency)
}

func TestPaymentServiceCreateCheckoutSessionRejectsInvalidAmount(t *testing.T) {
	svc := NewPaymentService(&fakePaymentRepo{}, nil, nil, &config.Config{
		Payment: config.PaymentConfig{Stripe: config.StripePaymentConfig{Enabled: true, SecretKey: "sk_test", Currency: "cny"}},
	})
	svc.SetStripeClient(&fakeStripeCheckoutClient{})

	_, err := svc.CreateCheckoutSession(context.Background(), CreateCheckoutSessionRequest{
		UserID:        7,
		Amount:        0,
		PaymentMethod: PaymentMethodAlipay,
		SuccessURL:    "https://example.com/success",
		CancelURL:     "https://example.com/cancel",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidPaymentAmount)
}

func TestPaymentServiceHandleStripeWebhookCreditsPaidSession(t *testing.T) {
	now := time.Now()
	payload := []byte(`{"id":"evt_paid","type":"checkout.session.completed","data":{"object":{"id":"cs_paid","amount_total":5000,"currency":"cny","payment_status":"paid","payment_intent":"pi_paid"}}}`)
	repo := &fakePaymentRepo{creditResult: &PaymentOrder{ID: "order_1", UserID: 9}}
	svc := NewPaymentService(repo, nil, nil, &config.Config{
		Payment: config.PaymentConfig{Stripe: config.StripePaymentConfig{
			Enabled:       true,
			SecretKey:     "sk_test",
			WebhookSecret: "whsec_test",
			Currency:      "cny",
		}},
	})

	result, err := svc.HandleStripeWebhook(context.Background(), payload, signedStripeHeader(payload, "whsec_test", now))
	require.NoError(t, err)
	require.Equal(t, "evt_paid", result.EventID)
	require.Equal(t, "cs_paid", result.SessionID)
	require.True(t, result.Credited)
	require.Equal(t, "cs_paid", repo.creditCompletion.SessionID)
	require.Equal(t, int64(5000), repo.creditCompletion.AmountCents)
}

func TestPaymentServiceHandleStripeWebhookDefersUnpaidCompletedSession(t *testing.T) {
	now := time.Now()
	payload := []byte(`{"id":"evt_pending","type":"checkout.session.completed","data":{"object":{"id":"cs_pending","amount_total":5000,"currency":"cny","payment_status":"unpaid"}}}`)
	repo := &fakePaymentRepo{}
	svc := NewPaymentService(repo, nil, nil, &config.Config{
		Payment: config.PaymentConfig{Stripe: config.StripePaymentConfig{
			Enabled:       true,
			SecretKey:     "sk_test",
			WebhookSecret: "whsec_test",
			Currency:      "cny",
		}},
	})

	result, err := svc.HandleStripeWebhook(context.Background(), payload, signedStripeHeader(payload, "whsec_test", now))
	require.NoError(t, err)
	require.Equal(t, "pending_payment", result.Status)
	require.Equal(t, "cs_pending", repo.pendingSessionID)
	require.Empty(t, repo.creditCompletion.SessionID)
}

func signedStripeHeader(payload []byte, secret string, now time.Time) string {
	timestamp := fmt.Sprintf("%d", now.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	return fmt.Sprintf("t=%s,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

type fakeStripeCheckoutClient struct {
	params  StripeCheckoutSessionParams
	session *StripeCheckoutSession
	err     error
}

func (f *fakeStripeCheckoutClient) CreateCheckoutSession(_ context.Context, params StripeCheckoutSessionParams) (*StripeCheckoutSession, error) {
	f.params = params
	if f.err != nil {
		return nil, f.err
	}
	if f.session == nil {
		return &StripeCheckoutSession{ID: "cs_fake", URL: "https://checkout.stripe.com/c/pay/cs_fake"}, nil
	}
	return f.session, nil
}

type fakePaymentRepo struct {
	created          *PaymentOrder
	attachedID       string
	failedOrder      string
	pendingSessionID string
	creditCompletion StripeCheckoutCompletion
	creditResult     *PaymentOrder
	creditErr        error
}

func (f *fakePaymentRepo) CreatePaymentOrder(_ context.Context, order *PaymentOrder) error {
	copy := *order
	f.created = &copy
	return nil
}

func (f *fakePaymentRepo) AttachStripeSession(_ context.Context, orderID, sessionID, checkoutURL string, _ *time.Time) error {
	if f.created == nil || f.created.ID != orderID {
		return errors.New("unknown order")
	}
	f.attachedID = sessionID
	f.created.StripeSessionID = sessionID
	f.created.CheckoutURL = checkoutURL
	return nil
}

func (f *fakePaymentRepo) MarkPaymentOrderFailed(_ context.Context, orderID, _ string) error {
	f.failedOrder = orderID
	return nil
}

func (f *fakePaymentRepo) MarkPaymentOrderFailedBySession(context.Context, string, string, []byte) error {
	return nil
}

func (f *fakePaymentRepo) MarkStripeSessionPending(_ context.Context, sessionID, _ string, _ []byte) error {
	f.pendingSessionID = sessionID
	return nil
}

func (f *fakePaymentRepo) CreditStripePaymentOrder(_ context.Context, completion StripeCheckoutCompletion) (*PaymentOrder, bool, error) {
	f.creditCompletion = completion
	if f.creditErr != nil {
		return nil, false, f.creditErr
	}
	return f.creditResult, f.creditResult != nil, nil
}

func (f *fakePaymentRepo) ListByUserID(_ context.Context, _ int64, params pagination.PaginationParams) ([]PaymentOrder, *pagination.PaginationResult, error) {
	return nil, &pagination.PaginationResult{
		Total:    0,
		Page:     params.Page,
		PageSize: params.Limit(),
		Pages:    0,
	}, nil
}
