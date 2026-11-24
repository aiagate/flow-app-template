package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

type accountResponse struct {
	AccountID    string    `json:"account_id"`
	Balance      int64     `json:"balance"`
	RateLimitRPM int       `json:"rate_limit_rpm"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ledgerRequest struct {
	Amount int64  `json:"amount"`
	Reason string `json:"reason"`
}

type billingEventsEnvelope struct {
	Events []billingEventResponse `json:"events"`
}

type billingEventResponse struct {
	ID         string    `json:"id"`
	AccountID  string    `json:"account_id"`
	Amount     int64     `json:"amount"`
	Reason     string    `json:"reason"`
	RequestID  string    `json:"request_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

type requestLogsEnvelope struct {
	Logs []requestLogResponse `json:"logs"`
}

type requestLogResponse struct {
	ID         string    `json:"id"`
	RequestID  string    `json:"request_id"`
	AccountID  string    `json:"account_id"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"status_code"`
	LatencyUS  int64     `json:"latency_us"`
	OccurredAt time.Time `json:"occurred_at"`
}

func newAccountResponse(view usecase.AccountView) accountResponse {
	return accountResponse{
		AccountID:    view.AccountID,
		Balance:      view.Balance,
		RateLimitRPM: view.RateLimitRPM,
		UpdatedAt:    view.UpdatedAt,
	}
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	view, err := h.deps.Billing.GetAccount(r.Context(), r.PathValue("account_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAccountResponse(view))
}

func (h *Handler) creditAccount(w http.ResponseWriter, r *http.Request) {
	var request ledgerRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	view, err := h.deps.Billing.CreditAccount(r.Context(), usecase.CreditAccountInput{
		AccountID: r.PathValue("account_id"),
		Amount:    request.Amount,
		Reason:    request.Reason,
		RequestID: requestIDFrom(r.Context()),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAccountResponse(view))
}

func (h *Handler) chargeAccount(w http.ResponseWriter, r *http.Request) {
	var request ledgerRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	view, err := h.deps.Billing.ChargeAccount(r.Context(), usecase.ChargeAccountInput{
		AccountID: r.PathValue("account_id"),
		Amount:    request.Amount,
		Reason:    request.Reason,
		RequestID: requestIDFrom(r.Context()),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	// The guard authorized before the amount-specific debit; overwrite its
	// header with the post-charge balance.
	w.Header().Set("X-Account-Balance", strconv.FormatInt(view.Balance, 10))
	writeJSON(w, http.StatusOK, newAccountResponse(view))
}

func (h *Handler) listBillingEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := h.deps.Billing.ListBillingEvents(r.Context(), r.URL.Query().Get("account_id"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	response := billingEventsEnvelope{Events: make([]billingEventResponse, 0, len(events))}
	for _, event := range events {
		response.Events = append(response.Events, newBillingEventResponse(event))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) listRequestLogs(w http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	logs, err := h.deps.Billing.ListRequestLogs(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	response := requestLogsEnvelope{Logs: make([]requestLogResponse, 0, len(logs))}
	for _, logRow := range logs {
		response.Logs = append(response.Logs, requestLogResponse{
			ID:         logRow.ID,
			RequestID:  logRow.RequestID,
			AccountID:  logRow.AccountID,
			Method:     logRow.Method,
			Path:       logRow.Path,
			StatusCode: logRow.StatusCode,
			LatencyUS:  logRow.LatencyUS,
			OccurredAt: logRow.OccurredAt,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func newBillingEventResponse(event domain.BillingEvent) billingEventResponse {
	return billingEventResponse{
		ID:         event.ID.String(),
		AccountID:  event.AccountID,
		Amount:     event.Amount,
		Reason:     event.Reason,
		RequestID:  event.RequestID,
		OccurredAt: event.OccurredAt,
	}
}

func queryLimit(r *http.Request, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 1000 {
		return 0, usecase.NewError(usecase.CodeValidation, "limit must be an integer between 1 and 1000")
	}
	return limit, nil
}
