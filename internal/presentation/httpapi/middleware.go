package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

const (
	requestIDHeader = "X-Request-ID"
	accountIDHeader = "X-Account-ID"
	defaultAccount  = "default"
)

type contextKey string

const (
	requestIDContextKey contextKey = "request-id"
	accountIDContextKey contextKey = "account-id"
)

// statusRecorder captures the response status without changing the body.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func requestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

func accountIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(accountIDContextKey).(string)
	if value == "" {
		return defaultAccount
	}
	return value
}

func newRequestID() string {
	return domain.NewRequestID()
}

// withRequestID propagates an inbound request ID or allocates a fresh one, and
// echoes the effective ID on the response.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get(requestIDHeader))
		if requestID == "" {
			requestID = newRequestID()
		}
		r.Header.Set(requestIDHeader, requestID)
		w.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withAccountID resolves X-Account-ID and defaults to the shared "default"
// account when the header is absent or blank.
func withAccountID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID := strings.TrimSpace(r.Header.Get(accountIDHeader))
		if accountID == "" {
			accountID = defaultAccount
		}
		w.Header().Set(accountIDHeader, accountID)
		ctx := context.WithValue(r.Context(), accountIDContextKey, accountID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withBodyLimit caps every request body at the JSON limit. The decoder maps the
// resulting MaxBytesError to VALIDATION_ERROR.
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// withPanicRecovery converts a panic into the shared JSON 500 envelope.
func (h *Handler) withPanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recorder, ok := w.(*statusRecorder); ok && recorder.wroteHeader {
					return
				}
				writeError(w, usecase.NewError(usecase.CodeUnexpected, "internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withRequestLog queues one append-only request log for every API call. The
// send is non-blocking and never performs disk I/O.
func (h *Handler) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := newStatusRecorder(w)
		next.ServeHTTP(recorder, r)

		if h.deps.Queue == nil || !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		// The request-ID and account middlewares run inside this one and
		// publish their values on the response headers, so read them there
		// rather than from the outer request context.
		requestID := w.Header().Get(requestIDHeader)
		if requestID == "" {
			requestID = requestIDFrom(r.Context())
		}
		accountID := w.Header().Get(accountIDHeader)
		if accountID == "" {
			accountID = accountIDFrom(r.Context())
		}
		_ = h.deps.Queue.EnqueueRequestLog(domain.RequestLog{
			ID:         newRequestID(),
			RequestID:  requestID,
			AccountID:  accountID,
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: recorder.status,
			LatencyUS:  time.Since(started).Microseconds(),
			OccurredAt: started.UTC(),
		})
	})
}

// withGuard is the hot-path admission middleware. preDebit is false only for
// the charge route, whose handler performs the single amount-specific debit.
func (h *Handler) withGuard(route string, preDebit bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID := accountIDFrom(r.Context())

		var (
			decision domain.HotPathDecision
			err      error
		)
		if preDebit {
			decision, err = h.deps.HotPath.AcquireAndDebit(accountID, route)
		} else {
			decision, err = h.deps.HotPath.Authorize(accountID, route)
		}

		setRateHeaders(w, h.rateLimitFor(route), decision)
		if err != nil {
			writeError(w, err)
			return
		}
		if !preDebit {
			next.ServeHTTP(w, r)
			return
		}

		recorder := newStatusRecorder(w)
		next.ServeHTTP(recorder, r)
		h.settleMeteredRequest(r, accountID, decision, recorder.status)
	})
}

// settleMeteredRequest persists a successful pre-debit or refunds it after a
// 5xx response.
func (h *Handler) settleMeteredRequest(r *http.Request, accountID string, decision domain.HotPathDecision, status int) {
	if decision.Cost <= 0 {
		return
	}

	now := time.Now().UTC()
	if status >= http.StatusInternalServerError {
		h.deps.HotPath.Refund(accountID, decision.Cost)
		h.enqueueQuota(accountID, now)
		return
	}

	if h.deps.Queue != nil {
		_ = h.deps.Queue.EnqueueBilling(domain.BillingEvent{
			ID:         domain.NewEventID(),
			AccountID:  accountID,
			Amount:     -decision.Cost,
			Reason:     "request",
			RequestID:  requestIDFrom(r.Context()),
			OccurredAt: now,
		})
	}
	h.enqueueQuota(accountID, now)
}

func (h *Handler) enqueueQuota(accountID string, updatedAt time.Time) {
	if h.deps.Queue == nil {
		return
	}
	state, ok := h.deps.HotPath.GetAccountState(accountID)
	if !ok {
		return
	}
	_ = h.deps.Queue.EnqueueQuotaSync(domain.AccountQuota{
		AccountID:    state.AccountID,
		Balance:      state.Balance,
		RateLimitRPM: state.RateLimitRPM,
		UpdatedAt:    updatedAt,
	})
}

func (h *Handler) rateLimitFor(route string) int {
	if h.deps.HotPath == nil {
		return 0
	}
	config := h.deps.HotPath.CurrentConfig()
	if config == nil {
		return 0
	}
	if policy, ok := config.Routes[route]; ok && policy.RateLimitRPM > 0 {
		return policy.RateLimitRPM
	}
	return config.DefaultRateLimitRPM
}

func setRateHeaders(w http.ResponseWriter, limit int, decision domain.HotPathDecision) {
	remaining := decision.RemainingTokens
	if remaining < 0 {
		remaining = int64(limit)
	}
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
	w.Header().Set("X-Account-Balance", strconv.FormatInt(decision.RemainingBalance, 10))
}
