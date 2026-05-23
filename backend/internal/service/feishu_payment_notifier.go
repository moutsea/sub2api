package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const feishuPaymentNotifyTimeout = 5 * time.Second

type feishuPaymentNotifier struct {
	webhookURL    string
	webhookSecret string
	httpClient    *http.Client
	now           func() time.Time
}

type feishuTextMessagePayload struct {
	MsgType   string                   `json:"msg_type"`
	Content   feishuTextMessageContent `json:"content"`
	Timestamp int64                    `json:"timestamp"`
	Sign      string                   `json:"sign"`
}

type feishuTextMessageContent struct {
	Text string `json:"text"`
}

func newFeishuPaymentNotifier(cfg *config.Config) paymentNotifier {
	if cfg == nil || !cfg.Payment.Feishu.Enabled {
		return nil
	}
	webhookURL := strings.TrimSpace(cfg.Payment.Feishu.WebhookURL)
	webhookSecret := strings.TrimSpace(cfg.Payment.Feishu.WebhookSecret)
	if webhookURL == "" || webhookSecret == "" {
		return nil
	}
	return &feishuPaymentNotifier{
		webhookURL:    webhookURL,
		webhookSecret: webhookSecret,
		httpClient:    &http.Client{Timeout: feishuPaymentNotifyTimeout},
		now:           time.Now,
	}
}

func (n *feishuPaymentNotifier) NotifyPaymentPaid(ctx context.Context, order *PaymentOrder) error {
	if n == nil || order == nil {
		return nil
	}
	return n.sendText(ctx, formatFeishuPaymentPaidText(order))
}

func (n *feishuPaymentNotifier) sendText(ctx context.Context, text string) error {
	webhookURL := strings.TrimSpace(n.webhookURL)
	webhookSecret := strings.TrimSpace(n.webhookSecret)
	if webhookURL == "" || webhookSecret == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := n.now
	if now == nil {
		now = time.Now
	}
	payload := newFeishuTextMessagePayload(text, now().Unix(), webhookSecret)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Feishu payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Feishu request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := n.httpClient
	if client == nil {
		client = &http.Client{Timeout: feishuPaymentNotifyTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send Feishu request: %w", err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if readErr != nil {
			return fmt.Errorf("Feishu webhook returned HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("Feishu webhook returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if readErr != nil || len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil
	}
	if result.Code != 0 {
		if strings.TrimSpace(result.Msg) == "" {
			return fmt.Errorf("Feishu webhook returned code %d", result.Code)
		}
		return fmt.Errorf("Feishu webhook returned code %d: %s", result.Code, result.Msg)
	}
	return nil
}

func newFeishuTextMessagePayload(text string, timestamp int64, secret string) feishuTextMessagePayload {
	return feishuTextMessagePayload{
		MsgType: "text",
		Content: feishuTextMessageContent{
			Text: text,
		},
		Timestamp: timestamp,
		Sign:      signFeishuWebhook(timestamp, secret),
	}
}

func signFeishuWebhook(timestamp int64, secret string) string {
	stringToSign := strconv.FormatInt(timestamp, 10) + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func formatFeishuPaymentPaidText(order *PaymentOrder) string {
	amount := order.Amount
	if amount <= 0 && order.AmountCents > 0 {
		amount = float64(order.AmountCents) / 100
	}
	symbol := paymentCurrencySymbol(order.Currency)
	parts := []string{
		fmt.Sprintf("Sub2API 收款 userId: %d", order.UserID),
		fmt.Sprintf("充值金额: %s%.2f", symbol, amount),
		fmt.Sprintf("应付金额: %s%.2f", symbol, amount),
		fmt.Sprintf("充值前余额: %s%.2f", symbol, order.BalanceBefore),
		fmt.Sprintf("充值后余额: %s%.2f", symbol, order.BalanceAfter),
	}
	return strings.Join(parts, ", ")
}

func paymentCurrencySymbol(currency string) string {
	switch strings.ToLower(strings.TrimSpace(currency)) {
	case "cny":
		return "¥"
	case "usd":
		return "$"
	case "eur":
		return "€"
	default:
		if currency = strings.ToUpper(strings.TrimSpace(currency)); currency != "" {
			return currency + " "
		}
		return "$"
	}
}
