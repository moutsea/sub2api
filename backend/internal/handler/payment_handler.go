package handler

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type PaymentHandler struct {
	paymentService *service.PaymentService
}

const paymentCheckoutURLExpiryGrace = 5 * time.Minute

func NewPaymentHandler(paymentService *service.PaymentService) *PaymentHandler {
	return &PaymentHandler{paymentService: paymentService}
}

type createCheckoutSessionRequest struct {
	Amount        float64 `json:"amount" binding:"required"`
	PaymentMethod string  `json:"payment_method" binding:"required"`
}

type paymentOrderDTO struct {
	ID            string  `json:"id"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	PaymentMethod string  `json:"payment_method"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
	PaidAt        *string `json:"paid_at"`
	CheckoutURL   *string `json:"checkout_url,omitempty"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
}

func toPaymentOrderDTO(o *service.PaymentOrder) paymentOrderDTO {
	dto := paymentOrderDTO{
		ID:            o.ID,
		Amount:        o.Amount,
		Currency:      o.Currency,
		PaymentMethod: o.PaymentMethod,
		Status:        o.Status,
		CreatedAt:     o.CreatedAt.Format(time.RFC3339),
	}
	if o.PaidAt != nil {
		s := o.PaidAt.Format(time.RFC3339)
		dto.PaidAt = &s
	}
	if (o.Status == service.PaymentStatusPending || o.Status == service.PaymentStatusCreated) && o.CheckoutURL != "" {
		if o.ExpiresAt == nil || o.ExpiresAt.After(time.Now().Add(paymentCheckoutURLExpiryGrace)) {
			dto.CheckoutURL = &o.CheckoutURL
			if o.ExpiresAt != nil {
				s := o.ExpiresAt.Format(time.RFC3339)
				dto.ExpiresAt = &s
			}
		}
	}
	return dto
}

func (h *PaymentHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	page, pageSize := response.ParsePagination(c)
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}

	orders, result, err := h.paymentService.ListOrders(c.Request.Context(), subject.UserID, params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]paymentOrderDTO, 0, len(orders))
	for i := range orders {
		out = append(out, toPaymentOrderDTO(&orders[i]))
	}
	response.Paginated(c, out, result.Total, page, pageSize)
}

func (h *PaymentHandler) CreateCheckoutSession(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req createCheckoutSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	baseURL := paymentReturnBaseURL(c)
	result, err := h.paymentService.CreateCheckoutSession(c.Request.Context(), service.CreateCheckoutSessionRequest{
		UserID:        subject.UserID,
		Amount:        req.Amount,
		PaymentMethod: req.PaymentMethod,
		SuccessURL:    baseURL + "/recharge?status=success&session_id={CHECKOUT_SESSION_ID}",
		CancelURL:     baseURL + "/recharge?status=cancelled",
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

func (h *PaymentHandler) StripeWebhook(c *gin.Context) {
	payload, err := c.GetRawData()
	if err != nil {
		response.Error(c, http.StatusBadRequest, "failed to read request body")
		return
	}
	result, err := h.paymentService.HandleStripeWebhook(c.Request.Context(), payload, c.GetHeader("Stripe-Signature"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func paymentReturnBaseURL(c *gin.Context) string {
	if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" {
		if parsed, err := url.Parse(origin); err == nil && parsed.IsAbs() && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			return strings.TrimRight(origin, "/")
		}
	}

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := firstHeaderValue(c.GetHeader("X-Forwarded-Proto")); forwardedProto == "http" || forwardedProto == "https" {
		scheme = forwardedProto
	}

	host := c.Request.Host
	if forwardedHost := firstHeaderValue(c.GetHeader("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	return scheme + "://" + host
}

func firstHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if first, _, ok := strings.Cut(value, ","); ok {
		return strings.TrimSpace(first)
	}
	return value
}
