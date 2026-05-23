package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignFeishuWebhookMatchesReferenceImplementation(t *testing.T) {
	require.Equal(t, "jWsBkWnzlRKtaP+iZgwraSojMWik4cJR7aysApQZuoA=", signFeishuWebhook(1710000000, "secret"))
}

func TestFeishuPaymentNotifierSendsTextPayload(t *testing.T) {
	var sentPayload feishuTextMessagePayload
	notifier := &feishuPaymentNotifier{
		webhookURL:    "https://open.feishu.cn/open-apis/bot/v2/hook/test",
		webhookSecret: "secret",
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &sentPayload))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"ok"}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
		now: func() time.Time {
			return time.Unix(1710000000, 0)
		},
	}

	err := notifier.NotifyPaymentPaid(context.Background(), &PaymentOrder{
		ID:                    "order_1",
		UserID:                9,
		Amount:                50,
		Currency:              "cny",
		BalanceBefore:         12.5,
		BalanceAfter:          62.5,
		StripePaymentIntentID: "pi_paid",
	})

	require.NoError(t, err)
	require.Equal(t, "text", sentPayload.MsgType)
	require.Equal(t, int64(1710000000), sentPayload.Timestamp)
	require.Equal(t, signFeishuWebhook(1710000000, "secret"), sentPayload.Sign)
	require.Equal(t, "Sub2API 收款 userId: 9, 充值金额: ¥50.00, 应付金额: ¥50.00, 充值前余额: ¥12.50, 充值后余额: ¥62.50", sentPayload.Content.Text)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
