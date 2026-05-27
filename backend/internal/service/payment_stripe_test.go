package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripePaymentClientCreateCheckoutSessionUsesMultiplePaymentMethods(t *testing.T) {
	client := &stripePaymentClient{
		secretKey: "sk_test",
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "recharge:order_1", req.Header.Get("Idempotency-Key"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			form, err := url.ParseQuery(string(body))
			require.NoError(t, err)
			require.Equal(t, []string{PaymentMethodAlipay, PaymentMethodWechat}, form["payment_method_types[]"])
			require.Equal(t, "web", form.Get("payment_method_options[wechat_pay][client]"))
			require.Equal(t, "账户余额充值 $50.00", form.Get("line_items[0][price_data][product_data][name]"))

			responseBody, err := json.Marshal(StripeCheckoutSession{
				ID:  "cs_test",
				URL: "https://checkout.stripe.com/c/pay/cs_test",
			})
			require.NoError(t, err)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(responseBody))),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
	}

	session, err := client.CreateCheckoutSession(context.Background(), StripeCheckoutSessionParams{
		OrderID:        "order_1",
		UserID:         7,
		Amount:         50,
		AmountCents:    5000,
		Currency:       PaymentCurrencyCNY,
		PaymentMethod:  PaymentMethodStripeCheckout,
		PaymentMethods: []string{PaymentMethodAlipay, PaymentMethodWechat},
		SuccessURL:     "https://example.com/success",
		CancelURL:      "https://example.com/cancel",
	})

	require.NoError(t, err)
	require.Equal(t, "cs_test", session.ID)
}
