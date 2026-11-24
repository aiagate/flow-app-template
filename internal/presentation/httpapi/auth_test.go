package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

func withAuthorization(value string) requestOption {
	return func(r *http.Request) { r.Header.Set(authorizationHeader, value) }
}

func TestPublicRoutesNeedNoAuthentication(t *testing.T) {
	env := newTestEnvironment(t)

	index := call(t, env.handler, http.MethodGet, "/", nil, withoutAuth())
	assertStatus(t, index, http.StatusOK)
	if contentType := index.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("index content type = %q", contentType)
	}
	if got := index.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := index.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
	if got := index.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	csp := index.Header().Get("Content-Security-Policy")
	for _, fragment := range []string{
		"default-src 'self'", "script-src 'self'", "style-src 'self'",
		"frame-ancestors 'none'", "base-uri 'self'",
	} {
		if !strings.Contains(csp, fragment) {
			t.Fatalf("CSP %q does not contain %q", csp, fragment)
		}
	}

	asset := call(t, env.handler, http.MethodGet, "/assets/app.js", nil, withoutAuth())
	assertStatus(t, asset, http.StatusOK)

	health := call(t, env.handler, http.MethodGet, "/healthz", nil, withoutAuth())
	assertStatus(t, health, http.StatusOK)
}

func TestAPIRequiresABearerToken(t *testing.T) {
	env := newTestEnvironment(t)

	assertUnauthorized := func(t *testing.T, recorder *httptest.ResponseRecorder) {
		t.Helper()
		assertErrorCode(t, recorder, "UNAUTHORIZED", http.StatusUnauthorized)
		if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
		}
	}

	t.Run("missing header", func(t *testing.T) {
		assertUnauthorized(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil, withoutAuth()))
	})
	t.Run("wrong token", func(t *testing.T) {
		assertUnauthorized(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil, withAuthToken("not-a-token")))
	})
	t.Run("wrong scheme", func(t *testing.T) {
		assertUnauthorized(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil, withAuthorization("Basic YWRtaW4=")))
	})
	t.Run("empty bearer token", func(t *testing.T) {
		assertUnauthorized(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil, withAuthorization("Bearer   ")))
	})
}

func TestAdminRoutesRejectAccountPrincipals(t *testing.T) {
	env := newTestEnvironment(t)
	token := testAccountToken("acct-a")

	for _, path := range []string{
		"/api/v1/runtime/config",
		"/api/v1/runtime/stats",
		"/api/v1/logs",
	} {
		assertErrorCode(t, call(t, env.handler, http.MethodGet, path, nil, withAuthToken(token)), "FORBIDDEN", http.StatusForbidden)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/default/credit", map[string]any{
		"amount": 10, "reason": "top-up",
	}, withAuthToken(token)), "FORBIDDEN", http.StatusForbidden)
}

func TestAccountPrincipalsArePinnedToTheirAccount(t *testing.T) {
	env := newTestEnvironment(t)
	token := testAccountToken("acct-a")

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil,
		withAuthToken(token), withAccount("acct-b")), "FORBIDDEN", http.StatusForbidden)

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/accounts/acct-b", nil,
		withAuthToken(token)), "FORBIDDEN", http.StatusForbidden)

	own := call(t, env.handler, http.MethodGet, "/api/v1/accounts/acct-a", nil, withAuthToken(token))
	assertStatus(t, own, http.StatusOK)
	if got := own.Header().Get(accountIDHeader); got != "acct-a" {
		t.Fatalf("X-Account-ID = %q, want acct-a", got)
	}

	events := call(t, env.handler, http.MethodGet, "/api/v1/billing/events", nil, withAuthToken(token))
	assertStatus(t, events, http.StatusOK)
	if env.audit.billingAccount != "acct-a" {
		t.Fatalf("billing filter = %q, want the principal account", env.audit.billingAccount)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/billing/events?account_id=acct-b", nil,
		withAuthToken(token)), "FORBIDDEN", http.StatusForbidden)

	admin := call(t, env.handler, http.MethodGet, "/api/v1/billing/events?account_id=acct-b", nil)
	assertStatus(t, admin, http.StatusOK)
	if env.audit.billingAccount != "acct-b" {
		t.Fatalf("admin billing filter = %q, want acct-b", env.audit.billingAccount)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/accounts/"+strings.Repeat("a", domain.MaxAccountIDLength+1), nil),
		"VALIDATION_ERROR", http.StatusBadRequest)
}

func TestAdminCanImpersonateAnAccount(t *testing.T) {
	env := newTestEnvironment(t)

	get := call(t, env.handler, http.MethodGet, "/api/v1/accounts/acct-b", nil, withAccount("acct-b"))
	assertStatus(t, get, http.StatusOK)
	if got := get.Header().Get(accountIDHeader); got != "acct-b" {
		t.Fatalf("X-Account-ID = %q", got)
	}

	before := env.guard.accounts["acct-b"].Balance
	charged := call(t, env.handler, http.MethodPost, "/api/v1/accounts/acct-b/charge", map[string]any{
		"amount": 25, "reason": "manual adjustment",
	}, withAccount("acct-a"))
	assertStatus(t, charged, http.StatusOK)
	if got := charged.Header().Get(accountIDHeader); got != "acct-b" {
		t.Fatalf("path account did not win: X-Account-ID = %q", got)
	}
	if got := env.guard.accounts["acct-b"].Balance; got != before-25 {
		t.Fatalf("path account balance = %d, want %d", got, before-25)
	}
	if got := env.guard.accounts["acct-a"].Balance; got != 1000 {
		t.Fatalf("header account was charged instead: %d", got)
	}
}

func TestPathAccountSetsHeaderAndRequestLog(t *testing.T) {
	env := newTestEnvironment(t)

	// The admin sends no X-Account-ID, so only the path identifies the target.
	get := call(t, env.handler, http.MethodGet, "/api/v1/accounts/acct-b", nil)
	assertStatus(t, get, http.StatusOK)
	if got := get.Header().Get(accountIDHeader); got != "acct-b" {
		t.Fatalf("X-Account-ID = %q, want acct-b", got)
	}

	_, _, logs := env.queue.snapshot()
	var found bool
	for _, entry := range logs {
		if entry.Path != "/api/v1/accounts/acct-b" {
			continue
		}
		found = true
		if entry.AccountID != "acct-b" {
			t.Fatalf("request log account_id = %q, want acct-b", entry.AccountID)
		}
	}
	if !found {
		t.Fatalf("no request log for the path-targeted account call: %+v", logs)
	}
}

func TestChargeUsesThePathAccountForAccountPrincipals(t *testing.T) {
	env := newTestEnvironment(t)
	token := testAccountToken("acct-a")

	charged := call(t, env.handler, http.MethodPost, "/api/v1/accounts/acct-a/charge", map[string]any{
		"amount": 10, "reason": "manual",
	}, withAuthToken(token), withAccount("acct-a"))
	assertStatus(t, charged, http.StatusOK)

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/acct-b/charge", map[string]any{
		"amount": 10, "reason": "manual",
	}, withAuthToken(token)), "FORBIDDEN", http.StatusForbidden)
}

func TestAccountIDHeaderValidation(t *testing.T) {
	env := newTestEnvironment(t)

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil, withAccount("bad id")),
		"VALIDATION_ERROR", http.StatusBadRequest)
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil,
		withAccount(strings.Repeat("a", domain.MaxAccountIDLength+1))),
		"VALIDATION_ERROR", http.StatusBadRequest)
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil,
		withAuthToken(testAccountToken("acct-a")), withAccount("acct b")),
		"VALIDATION_ERROR", http.StatusBadRequest)
}

func TestRequestIDValidation(t *testing.T) {
	env := newTestEnvironment(t)

	for _, requestID := range []string{"bad id", strings.Repeat("a", domain.MaxRequestIDLength+1)} {
		recorder := call(t, env.handler, http.MethodGet, "/healthz", nil, withRequestIDHeader(requestID))
		assertErrorCode(t, recorder, "VALIDATION_ERROR", http.StatusBadRequest)
		effective := recorder.Header().Get(requestIDHeader)
		if effective == requestID {
			t.Fatalf("invalid request ID %q was reflected", requestID)
		}
		if _, err := domain.ValidateRequestID(effective); err != nil {
			t.Fatalf("generated request ID %q is invalid: %v", effective, err)
		}
	}

	valid := call(t, env.handler, http.MethodGet, "/healthz", nil, withRequestIDHeader("  request-42  "))
	assertStatus(t, valid, http.StatusOK)
	if got := valid.Header().Get(requestIDHeader); got != "request-42" {
		t.Fatalf("request ID = %q, want trimmed request-42", got)
	}
}

func TestJSONContentTypeIsRequired(t *testing.T) {
	env := newTestEnvironment(t)
	body := map[string]any{"display_name": "Alice", "email": "alice@example.com"}

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/users", body, withoutContentType()),
		"UNSUPPORTED_MEDIA_TYPE", http.StatusUnsupportedMediaType)
	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/users", body, withContentType("text/plain")),
		"UNSUPPORTED_MEDIA_TYPE", http.StatusUnsupportedMediaType)

	created := call(t, env.handler, http.MethodPost, "/api/v1/users", body, withContentType("application/json; charset=utf-8"))
	assertStatus(t, created, http.StatusCreated)
}

func TestRateLimitHeadersUseTheDecision(t *testing.T) {
	t.Run("default limit", func(t *testing.T) {
		env := newTestEnvironment(t)
		recorder := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
		assertStatus(t, recorder, http.StatusOK)
		if got := recorder.Header().Get("X-RateLimit-Limit"); got != "120" {
			t.Fatalf("X-RateLimit-Limit = %q", got)
		}
		if got := recorder.Header().Get("X-RateLimit-Remaining"); got != "119" {
			t.Fatalf("X-RateLimit-Remaining = %q", got)
		}
	})

	t.Run("route override", func(t *testing.T) {
		env := newTestEnvironment(t)
		assertStatus(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
			"default_rate_limit_rpm": 120,
			"default_request_cost":   1,
			"routes": map[string]any{
				"GET /api/v1/users": map[string]any{"enabled": true, "cost": 1, "rate_limit_rpm": 7},
			},
		}), http.StatusOK)

		recorder := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
		assertStatus(t, recorder, http.StatusOK)
		if got := recorder.Header().Get("X-RateLimit-Limit"); got != "7" {
			t.Fatalf("X-RateLimit-Limit = %q", got)
		}
		if got := recorder.Header().Get("X-RateLimit-Remaining"); got != "6" {
			t.Fatalf("X-RateLimit-Remaining = %q", got)
		}
	})

	t.Run("unlimited omits the headers", func(t *testing.T) {
		env := newTestEnvironment(t)
		env.guard.mu.Lock()
		env.guard.config.DefaultRateLimitRPM = 0
		env.guard.mu.Unlock()

		recorder := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
		assertStatus(t, recorder, http.StatusOK)
		if got := recorder.Header().Get("X-RateLimit-Limit"); got != "" {
			t.Fatalf("unlimited limit header = %q, want empty", got)
		}
		if got := recorder.Header().Get("X-RateLimit-Remaining"); got != "" {
			t.Fatalf("unlimited remaining header = %q, want empty", got)
		}
	})

	t.Run("route disabled omits the balance", func(t *testing.T) {
		env := newTestEnvironment(t)
		assertStatus(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
			"default_rate_limit_rpm": 120,
			"default_request_cost":   1,
			"routes": map[string]any{
				"GET /api/v1/users": map[string]any{"enabled": false, "cost": 1, "rate_limit_rpm": 0},
			},
		}), http.StatusOK)

		recorder := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
		assertErrorCode(t, recorder, "ROUTE_DISABLED", http.StatusServiceUnavailable)
		if got := recorder.Header().Get("X-Account-Balance"); got != "" {
			t.Fatalf("route-disabled balance header = %q, want empty", got)
		}
	})
}

func TestHealthReportsDegradedAs503(t *testing.T) {
	env := newTestEnvironment(t)

	healthy := call(t, env.handler, http.MethodGet, "/healthz", nil, withoutAuth())
	assertStatus(t, healthy, http.StatusOK)

	env.health.health = domain.DBHealth{
		OK: false, JournalMode: "wal", WALBytes: 4096, WALWarnBytes: 2048, WALHealthy: false,
	}
	degraded := call(t, env.handler, http.MethodGet, "/healthz", nil, withoutAuth())
	assertStatus(t, degraded, http.StatusServiceUnavailable)
	payload := decodeMap(t, degraded)
	if payload["status"] != "degraded" {
		t.Fatalf("status = %v", payload["status"])
	}
	db := objectField(t, payload, "db")
	if db["wal_bytes"] != float64(4096) || db["wal_warn_bytes"] != float64(2048) || db["wal_healthy"] != false {
		t.Fatalf("db health = %+v", db)
	}
}

func TestRuntimeStatsExposeBatcherAndWALDetail(t *testing.T) {
	env := newTestEnvironment(t)
	env.health.health = domain.DBHealth{
		OK: true, JournalMode: "wal", WALAutocheckpoint: 0, WALBytes: 8192, WALWarnBytes: 65536,
		WALHealthy: true, ReadQueryOnly: true, ReadConnections: 5,
	}
	env.queue.mu.Lock()
	env.queue.stats = domain.BatcherStats{Pending: 12, Errors: 3, QueueDepth: 4, QueueCapacity: 8192}
	env.queue.mu.Unlock()

	stats := call(t, env.handler, http.MethodGet, "/api/v1/runtime/stats", nil)
	assertStatus(t, stats, http.StatusOK)
	payload := decodeMap(t, stats)
	batcher := objectField(t, payload, "batcher")
	if batcher["pending"] != float64(12) || batcher["errors"] != float64(3) {
		t.Fatalf("batcher = %+v", batcher)
	}
	db := objectField(t, payload, "db")
	if db["wal_bytes"] != float64(8192) || db["wal_warn_bytes"] != float64(65536) || db["wal_healthy"] != true {
		t.Fatalf("db = %+v", db)
	}
}

func TestDuplicateRouteNamesInArrayAreRejected(t *testing.T) {
	env := newTestEnvironment(t)

	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 120,
		"default_request_cost":   1,
		"routes": []any{
			map[string]any{"route": "GET /api/v1/users", "enabled": true, "cost": 1},
			map[string]any{"route": " GET /api/v1/users ", "enabled": true, "cost": 1},
		},
	}), "VALIDATION_ERROR", http.StatusBadRequest)
}

func TestQueueAndCapacityFailuresReturn503(t *testing.T) {
	t.Run("queue full", func(t *testing.T) {
		env := newTestEnvironment(t)
		before := env.guard.accounts["acct-a"].Balance
		env.queue.mu.Lock()
		env.queue.err = port.ErrQueueFull
		env.queue.mu.Unlock()

		assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/acct-a/credit", map[string]any{
			"amount": 10, "reason": "top-up",
		}), "SERVICE_UNAVAILABLE", http.StatusServiceUnavailable)

		if got := env.guard.accounts["acct-a"].Balance; got != before {
			t.Fatalf("credit was not rolled back: balance %d, want %d", got, before)
		}
	})

	t.Run("account capacity", func(t *testing.T) {
		env := newTestEnvironment(t)
		env.guard.mu.Lock()
		env.guard.creditErr = domain.ErrTooManyAccounts
		env.guard.mu.Unlock()

		assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/acct-a/credit", map[string]any{
			"amount": 10, "reason": "top-up",
		}), "SERVICE_UNAVAILABLE", http.StatusServiceUnavailable)
	})

	t.Run("queue closed refunds a charge", func(t *testing.T) {
		env := newTestEnvironment(t)
		before := env.guard.accounts["acct-a"].Balance
		env.queue.mu.Lock()
		env.queue.err = port.ErrQueueClosed
		env.queue.mu.Unlock()

		assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/acct-a/charge", map[string]any{
			"amount": 20, "reason": "manual",
		}), "SERVICE_UNAVAILABLE", http.StatusServiceUnavailable)

		if got := env.guard.accounts["acct-a"].Balance; got != before {
			t.Fatalf("charge was not refunded: balance %d, want %d", got, before)
		}
	})
}

func TestMeteredRequestsReportRateLimitFromTheDecision(t *testing.T) {
	env := newTestEnvironment(t)
	env.guard.mu.Lock()
	env.guard.config.Routes = map[string]domain.RoutePolicy{
		"GET /api/v1/users": {Enabled: true, Cost: 1, RateLimitRPM: 3},
	}
	env.guard.mu.Unlock()

	for index := 0; index < 3; index++ {
		recorder := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
		assertStatus(t, recorder, http.StatusOK)
		if got := recorder.Header().Get("X-RateLimit-Limit"); got != "3" {
			t.Fatalf("request %d limit = %q", index, got)
		}
		if got := recorder.Header().Get("X-RateLimit-Remaining"); got != strconv.Itoa(2-index) {
			t.Fatalf("request %d remaining = %q, want %d", index, got, 2-index)
		}
	}
	limited := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
	assertErrorCode(t, limited, "RATE_LIMITED", http.StatusTooManyRequests)
	if got := limited.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("rate-limited remaining = %q", got)
	}
}
