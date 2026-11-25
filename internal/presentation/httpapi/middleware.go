package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

const (
	requestIDHeader     = "X-Request-ID"
	accountIDHeader     = "X-Account-ID"
	authorizationHeader = "Authorization"
	defaultAccount      = "default"
)

type contextKey string

const (
	requestIDContextKey contextKey = "request-id"
	accountIDContextKey contextKey = "account-id"
	principalContextKey contextKey = "principal"
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

func principalFrom(ctx context.Context) (domain.Principal, bool) {
	value, ok := ctx.Value(principalContextKey).(domain.Principal)
	return value, ok
}

// withSecurityHeaders applies the baseline hardening headers to every response,
// including static assets and the console document.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// withRequestID propagates a validated inbound request ID or allocates a fresh
// one. An inbound ID that is blank, malformed or oversized is rejected so it can
// never be reflected or persisted as attacker-controlled text.
func (h *Handler) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(requestIDHeader)
		requestID := ""
		if raw == "" {
			requestID = domain.NewRequestID()
		} else {
			validated, err := domain.ValidateRequestID(raw)
			if err != nil {
				requestID = domain.NewRequestID()
				r.Header.Set(requestIDHeader, requestID)
				w.Header().Set(requestIDHeader, requestID)
				h.writeError(w, usecase.NewError(usecase.CodeValidation, "invalid X-Request-ID header"))
				return
			}
			requestID = validated
		}

		r.Header.Set(requestIDHeader, requestID)
		w.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey, requestID)
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

// withAuth authenticates every /api/ request and resolves the effective
// account. Admin principals may impersonate any well-formed account through
// X-Account-ID; account principals are pinned to their own account.
func (h *Handler) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		token, ok := bearerToken(r.Header.Get(authorizationHeader))
		if !ok {
			h.writeError(w, usecase.NewError(usecase.CodeUnauthorized, "authentication required"))
			return
		}
		principal, ok := h.deps.Auth.Authenticate(token)
		if !ok {
			h.writeError(w, usecase.NewError(usecase.CodeUnauthorized, "authentication required"))
			return
		}

		accountID, err := effectiveAccountID(r, principal)
		if err != nil {
			h.writeError(w, err)
			return
		}

		w.Header().Set(accountIDHeader, accountID)
		ctx := context.WithValue(r.Context(), principalContextKey, principal)
		ctx = context.WithValue(ctx, accountIDContextKey, accountID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts a bearer token. The scheme comparison is
// case-insensitive and the token must be non-empty.
func bearerToken(header string) (string, bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
		return "", false
	}
	return fields[1], true
}

// effectiveAccountID resolves X-Account-ID against the authenticated principal.
func effectiveAccountID(r *http.Request, principal domain.Principal) (string, error) {
	raw := r.Header.Get(accountIDHeader)
	if principal.IsAdmin {
		if strings.TrimSpace(raw) == "" {
			return defaultAccount, nil
		}
		accountID, err := domain.ValidateAccountID(raw)
		if err != nil {
			return "", usecase.ClassifyError(err)
		}
		return accountID, nil
	}

	if raw != "" {
		accountID, err := domain.ValidateAccountID(raw)
		if err != nil {
			return "", usecase.ClassifyError(err)
		}
		if accountID != principal.AccountID {
			return "", usecase.NewError(usecase.CodeForbidden, "access denied")
		}
	}
	return principal.AccountID, nil
}

// requireAdmin rejects every principal that is not an administrator.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFrom(r.Context())
		if !ok || !principal.IsAdmin {
			h.writeError(w, usecase.NewError(usecase.CodeForbidden, "access denied"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withPanicRecovery converts a panic into the shared JSON 500 envelope and logs
// the stack without leaking it to the client.
func (h *Handler) withPanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recorder, ok := w.(*statusRecorder); ok && recorder.wroteHeader {
					return
				}
				h.logger.Error("panic recovered",
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())),
					slog.String("request_id", requestIDFrom(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				)
				h.writeError(w, usecase.NewError(usecase.CodeUnexpected, "internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withRequestLog writes one structured access log and queues one append-only
// request log for every API call. Enqueue failures are reported instead of
// being silently dropped.
func (h *Handler) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := newStatusRecorder(w)
		next.ServeHTTP(recorder, r)

		latency := time.Since(started)
		requestID := recorder.Header().Get(requestIDHeader)
		if requestID == "" {
			requestID = requestIDFrom(r.Context())
		}
		accountID := recorder.Header().Get(accountIDHeader)

		attributes := []any{
			slog.String("request_id", requestID),
			slog.String("account_id", accountID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Int64("latency_us", latency.Microseconds()),
		}
		switch {
		case recorder.status >= http.StatusInternalServerError:
			h.logger.Error("http request", attributes...)
		case recorder.status >= http.StatusBadRequest:
			h.logger.Warn("http request", attributes...)
		default:
			h.logger.Info("http request", attributes...)
		}

		if !strings.HasPrefix(r.URL.Path, "/api/") || h.deps.Queue == nil {
			return
		}
		logAccountID := accountID
		if logAccountID == "" {
			logAccountID = accountIDFrom(r.Context())
		}
		if err := h.deps.Queue.EnqueueRequestLog(domain.RequestLog{
			ID:         domain.NewRequestID(),
			RequestID:  requestID,
			AccountID:  logAccountID,
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: recorder.status,
			LatencyUS:  latency.Microseconds(),
			OccurredAt: started.UTC(),
		}); err != nil {
			h.logger.Warn("enqueue request log", slog.Any("error", err), slog.String("request_id", requestID))
		}
	})
}

// withGuard is the hot-path admission middleware. preDebit is false only for
// the charge route, whose handler performs the single amount-specific debit.
// accountFromPath unifies the X-Account-ID header with the account in the URL
// before admission runs.
func (h *Handler) withGuard(route string, preDebit, accountFromPath bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID := accountIDFrom(r.Context())
		if accountFromPath {
			pathAccountID, err := domain.ValidateAccountID(r.PathValue("account_id"))
			if err != nil {
				h.writeError(w, usecase.ClassifyError(err))
				return
			}
			principal, ok := principalFrom(r.Context())
			if !ok || (!principal.IsAdmin && principal.AccountID != pathAccountID) {
				h.writeError(w, usecase.NewError(usecase.CodeForbidden, "access denied"))
				return
			}
			accountID = pathAccountID
			w.Header().Set(accountIDHeader, accountID)
			r = r.WithContext(context.WithValue(r.Context(), accountIDContextKey, accountID))
		}

		if !preDebit {
			decision, err := h.deps.HotPath.Authorize(accountID, route)
			setRateHeaders(w, decision)
			if err != nil {
				h.writeError(w, usecase.ClassifyError(err))
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Reserve the write slot before admission so a full queue rejects the
		// request without consuming balance or rate-limit tokens.
		res, err := h.deps.Queue.Reserve()
		if err != nil {
			h.writeError(w, usecase.ClassifyError(err))
			return
		}
		defer res.Release()

		decision, err := h.deps.HotPath.AcquireAndDebit(accountID, route)
		setRateHeaders(w, decision)
		if err != nil {
			h.writeError(w, usecase.ClassifyError(err))
			return
		}
		if decision.Cost <= 0 {
			// Nothing to settle, so hand the slot back before the handler runs.
			res.Release()
			next.ServeHTTP(w, r)
			return
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		h.settleMeteredRequest(r.Context(), accountID, decision, rec.status, res)
	})
}

// settleMeteredRequest commits a successful pre-debit atomically with the
// quota snapshot captured under the bucket lock, or refunds it after a 5xx
// response. The reservation is always settled by the end of this call.
func (h *Handler) settleMeteredRequest(ctx context.Context, accountID string, decision domain.HotPathDecision, status int, res port.WriteReservation) {
	if status >= http.StatusInternalServerError {
		h.deps.HotPath.Refund(accountID, decision.Cost, func(st domain.AccountState) {
			if err := res.CommitQuotaSync(domain.AccountQuota{
				AccountID:    accountID,
				Balance:      st.Balance,
				RateLimitRPM: st.RateLimitRPM,
				Seq:          st.Seq,
				UpdatedAt:    st.UpdatedAt,
			}); err != nil {
				h.logger.Warn("commit refunded quota",
					slog.Any("error", err),
					slog.String("account_id", accountID),
					slog.String("request_id", requestIDFrom(ctx)),
				)
			}
		})
		return
	}

	now := time.Now().UTC()
	if err := res.CommitBillingWithQuota(
		domain.BillingEvent{
			ID:         domain.NewEventID(),
			AccountID:  accountID,
			Amount:     -decision.Cost,
			Reason:     "request",
			RequestID:  requestIDFrom(ctx),
			OccurredAt: now,
		},
		domain.AccountQuota{
			AccountID:    accountID,
			Balance:      decision.RemainingBalance,
			RateLimitRPM: decision.AccountRateLimitRPM,
			Seq:          decision.Seq,
			UpdatedAt:    now,
		},
	); err != nil {
		h.logger.Warn("enqueue metered billing",
			slog.Any("error", err),
			slog.String("account_id", accountID),
			slog.String("request_id", requestIDFrom(ctx)),
		)
	}
}

// setRateHeaders reports the effective limit carried by the admission decision.
// A non-positive RateLimitRPM means unlimited, so both rate headers are omitted
// rather than inventing a limit.
func setRateHeaders(w http.ResponseWriter, decision domain.HotPathDecision) {
	if decision.RateLimitRPM > 0 {
		remaining := int64(0)
		if decision.RemainingTokens >= 0 {
			remaining = decision.RemainingTokens
		}
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(decision.RateLimitRPM))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
	}
	switch {
	case decision.Reason == domain.ErrRouteDisabled.Error():
		// Maintenance or a disabled route rejected the request before an
		// account was even resolved, so there is no balance worth reporting.
	case decision.Allowed,
		decision.Reason == domain.ErrRateLimited.Error(),
		decision.Reason == domain.ErrInsufficientBalance.Error():
		w.Header().Set("X-Account-Balance", strconv.FormatInt(decision.RemainingBalance, 10))
	}
}
