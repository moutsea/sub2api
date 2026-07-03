package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const stripeCheckoutSessionsURL = "https://api.stripe.com/v1/checkout/sessions"

type stripePaymentClient struct {
	secretKey  string
	httpClient *http.Client
}

func newStripePaymentClient(cfg *config.Config) stripeCheckoutClient {
	secretKey := ""
	if cfg != nil {
		secretKey = strings.TrimSpace(cfg.Payment.Stripe.SecretKey)
	}
	return &stripePaymentClient{
		secretKey: secretKey,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *stripePaymentClient) CreateCheckoutSession(ctx context.Context, params StripeCheckoutSessionParams) (*StripeCheckoutSession, error) {
	if strings.TrimSpace(c.secretKey) == "" {
		return nil, ErrPaymentNotConfigured.WithMetadata(map[string]string{"field": "payment.stripe.secret_key"})
	}

	form := url.Values{}
	form.Set("mode", "payment")
	paymentMethods := params.PaymentMethods
	if len(paymentMethods) == 0 && params.PaymentMethod != "" {
		paymentMethods = []string{params.PaymentMethod}
	}
	if len(paymentMethods) == 0 {
		paymentMethods = []string{PaymentMethodAlipay, PaymentMethodWechat}
	}
	hasWechatPay := false
	for _, method := range paymentMethods {
		method = strings.TrimSpace(method)
		if method == "" {
			continue
		}
		form.Add("payment_method_types[]", method)
		if method == PaymentMethodWechat {
			hasWechatPay = true
		}
	}
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(params.Currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(params.AmountCents, 10))
	form.Set("line_items[0][price_data][product_data][name]", fmt.Sprintf("账户余额充值 $%.2f", params.Amount))
	form.Set("success_url", params.SuccessURL)
	form.Set("cancel_url", params.CancelURL)
	form.Set("metadata[order_id]", params.OrderID)
	form.Set("metadata[user_id]", strconv.FormatInt(params.UserID, 10))
	form.Set("metadata[amount_cents]", strconv.FormatInt(params.AmountCents, 10))
	form.Set("metadata[site]", stripeCheckoutSite(params.Site))
	form.Set("payment_intent_data[metadata][order_id]", params.OrderID)
	form.Set("payment_intent_data[metadata][user_id]", strconv.FormatInt(params.UserID, 10))
	form.Set("payment_intent_data[metadata][amount_cents]", strconv.FormatInt(params.AmountCents, 10))
	form.Set("payment_intent_data[metadata][site]", stripeCheckoutSite(params.Site))
	if hasWechatPay {
		form.Set("payment_method_options[wechat_pay][client]", "web")
	}
	form.Set("allow_promotion_codes", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stripeCheckoutSessionsURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", "recharge:"+params.OrderID)
	req.SetBasicAuth(c.secretKey, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, stripeServiceError("STRIPE_REQUEST_FAILED", "failed to create Stripe checkout session").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, stripeServiceError("STRIPE_RESPONSE_READ_FAILED", "failed to read Stripe response").WithCause(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, stripeErrorFromResponse(resp.StatusCode, body)
	}

	var session StripeCheckoutSession
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, stripeServiceError("STRIPE_RESPONSE_INVALID", "failed to decode Stripe checkout session").WithCause(err)
	}
	return &session, nil
}

func stripeCheckoutSite(site string) string {
	site = strings.TrimSpace(site)
	if site == "" {
		return stripePaymentSite
	}
	return site
}

func stripeErrorFromResponse(statusCode int, body []byte) error {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	message := "Stripe checkout session creation failed"
	if err := json.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Error.Message) != "" {
		message = parsed.Error.Message
	}
	reason := "STRIPE_API_ERROR"
	if parsed.Error.Code != "" {
		reason = "STRIPE_" + strings.ToUpper(strings.ReplaceAll(parsed.Error.Code, "-", "_"))
	}
	return stripeServiceError(reason, message).WithMetadata(map[string]string{
		"status": strconv.Itoa(statusCode),
		"type":   parsed.Error.Type,
		"param":  parsed.Error.Param,
	})
}
