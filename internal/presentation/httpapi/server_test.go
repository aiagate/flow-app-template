package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

type requestOption func(*http.Request)

func withAccount(accountID string) requestOption {
	return func(r *http.Request) { r.Header.Set(accountIDHeader, accountID) }
}

func withRequestIDHeader(requestID string) requestOption {
	return func(r *http.Request) { r.Header.Set(requestIDHeader, requestID) }
}

func call(t *testing.T, handler http.Handler, method, path string, body any, options ...requestOption) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	switch typed := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case []byte:
		reader = bytes.NewReader(typed)
	case string:
		reader = bytes.NewReader([]byte(typed))
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, option := range options {
		option(request)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func assertStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, want, recorder.Body.String())
	}
}

func assertErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, wantCode string, wantStatus int) {
	t.Helper()
	assertStatus(t, recorder, wantStatus)
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content type = %q", contentType)
	}
	payload := decodeMap(t, recorder)
	errorValue, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error envelope: %s", recorder.Body.String())
	}
	if code, _ := errorValue["code"].(string); code != wantCode {
		t.Fatalf("error code = %q, want %q", code, wantCode)
	}
	if message, _ := errorValue["message"].(string); message == "" {
		t.Fatal("error message is empty")
	}
	if len(payload) != 1 {
		t.Fatalf("error envelope has extra top-level fields: %s", recorder.Body.String())
	}
}

func objectField(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := payload[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not an object: %v", key, payload[key])
	}
	return value
}

func arrayField(t *testing.T, payload map[string]any, key string) []any {
	t.Helper()
	value, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("field %q is not an array: %v", key, payload[key])
	}
	return value
}

func stringField(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok || value == "" {
		t.Fatalf("field %q is not a non-empty string: %v", key, object[key])
	}
	return value
}

func numberField(t *testing.T, object map[string]any, key string) float64 {
	t.Helper()
	value, ok := object[key].(float64)
	if !ok {
		t.Fatalf("field %q is not a number: %v", key, object[key])
	}
	return value
}

func TestEmbeddedConsoleHealthAndUnknownRoutes(t *testing.T) {
	env := newTestEnvironment(t)

	t.Run("index", func(t *testing.T) {
		recorder := call(t, env.handler, http.MethodGet, "/", nil)
		assertStatus(t, recorder, http.StatusOK)
		if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
			t.Fatalf("content type = %q", contentType)
		}
		if !strings.Contains(recorder.Body.String(), "app.js") {
			t.Fatal("index.html does not reference the embedded app.js")
		}
	})

	t.Run("assets", func(t *testing.T) {
		for path, want := range map[string]string{
			"/assets/app.js":     "use strict",
			"/assets/styles.css": "--",
		} {
			recorder := call(t, env.handler, http.MethodGet, path, nil)
			assertStatus(t, recorder, http.StatusOK)
			if !strings.Contains(recorder.Body.String(), want) {
				t.Fatalf("%s body does not contain %q", path, want)
			}
		}
		assertErrorCode(t, call(t, env.handler, http.MethodGet, "/assets/missing.js", nil), "NOT_FOUND", http.StatusNotFound)
	})

	t.Run("health", func(t *testing.T) {
		recorder := call(t, env.handler, http.MethodGet, "/healthz", nil)
		assertStatus(t, recorder, http.StatusOK)
		payload := decodeMap(t, recorder)
		if payload["status"] != "ok" {
			t.Fatalf("status = %v", payload["status"])
		}
		db := objectField(t, payload, "db")
		if db["journal_mode"] != "wal" || db["wal_autocheckpoint"] != float64(0) || db["read_query_only"] != true {
			t.Fatalf("db health = %+v", db)
		}
		if db["read_connections"] != float64(5) {
			t.Fatalf("read_connections = %v", db["read_connections"])
		}
	})

	t.Run("unknown route", func(t *testing.T) {
		assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/nope", nil), "NOT_FOUND", http.StatusNotFound)
	})

	t.Run("request id propagation", func(t *testing.T) {
		recorder := call(t, env.handler, http.MethodGet, "/healthz", nil, withRequestIDHeader("request-123"))
		if got := recorder.Header().Get(requestIDHeader); got != "request-123" {
			t.Fatalf("request ID = %q", got)
		}
		generated := call(t, env.handler, http.MethodGet, "/healthz", nil)
		if got := generated.Header().Get(requestIDHeader); len(got) != 26 {
			t.Fatalf("generated request ID = %q", got)
		}
	})
}

func TestUserEndpoints(t *testing.T) {
	env := newTestEnvironment(t)

	empty := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
	assertStatus(t, empty, http.StatusOK)
	if users := arrayField(t, decodeMap(t, empty), "users"); len(users) != 0 {
		t.Fatalf("users = %+v", users)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/users", map[string]any{}), "VALIDATION_ERROR", http.StatusBadRequest)
	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/users", `{"display_name":`), "VALIDATION_ERROR", http.StatusBadRequest)
	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/users", strings.Repeat("a", maxJSONBodyBytes+16)), "VALIDATION_ERROR", http.StatusBadRequest)

	balanceBeforeCreate := env.guard.accounts["default"].Balance
	created := call(t, env.handler, http.MethodPost, "/api/v1/users", map[string]any{
		"display_name": "Alice", "email": "alice@example.com",
	}, withRequestIDHeader("create-user"))
	assertStatus(t, created, http.StatusCreated)
	user := objectField(t, decodeMap(t, created), "user")
	userID := stringField(t, user, "id")
	if user["display_name"] != "Alice" || user["email"] != "alice@example.com" || user["version"] != float64(0) {
		t.Fatalf("created user = %+v", user)
	}
	if created.Header().Get("X-RateLimit-Limit") != "120" {
		t.Fatalf("rate headers = %v", created.Header())
	}
	if created.Header().Get("X-RateLimit-Remaining") == "" {
		t.Fatalf("missing remaining header: %v", created.Header())
	}
	if got := created.Header().Get("X-Account-Balance"); got != strconv.FormatInt(balanceBeforeCreate-1, 10) {
		t.Fatalf("balance header = %q, want %d", got, balanceBeforeCreate-1)
	}

	list := call(t, env.handler, http.MethodGet, "/api/v1/users", nil)
	assertStatus(t, list, http.StatusOK)
	if users := arrayField(t, decodeMap(t, list), "users"); len(users) != 1 {
		t.Fatalf("users = %+v", users)
	}

	got := call(t, env.handler, http.MethodGet, "/api/v1/users/"+userID, nil)
	assertStatus(t, got, http.StatusOK)
	if objectField(t, decodeMap(t, got), "user")["email"] != "alice@example.com" {
		t.Fatal("GET user returned the wrong email")
	}

	updated := call(t, env.handler, http.MethodPut, "/api/v1/users/"+userID+"/email", map[string]any{
		"email": "new@example.com", "expected_version": 0,
	})
	assertStatus(t, updated, http.StatusOK)
	updatedUser := objectField(t, decodeMap(t, updated), "user")
	if updatedUser["email"] != "new@example.com" || updatedUser["version"] != float64(1) {
		t.Fatalf("updated user = %+v", updatedUser)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/users/"+userID+"/email", map[string]any{
		"email": "stale@example.com", "expected_version": 0,
	}), "CONCURRENCY_CONFLICT", http.StatusConflict)

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/users", map[string]any{
		"display_name": "Duplicate", "email": "new@example.com",
	}), "CONFLICT", http.StatusConflict)

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users/not-a-ulid", nil), "VALIDATION_ERROR", http.StatusBadRequest)
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users/01ARZ3NDEKTSV4RRFFQ69G5FAV", nil), "NOT_FOUND", http.StatusNotFound)
	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/users/"+userID+"/email", map[string]any{
		"email": "x@example.com", "expected_version": -1,
	}), "VALIDATION_ERROR", http.StatusBadRequest)

	_, _, logs := env.queue.snapshot()
	if len(logs) == 0 {
		t.Fatal("no request logs were queued")
	}
	last := logs[len(logs)-1]
	if last.Path == "" || last.RequestID == "" || last.ID == "" {
		t.Fatalf("request log = %+v", last)
	}
}

func TestTeamEndpoints(t *testing.T) {
	env := newTestEnvironment(t)

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/teams", map[string]any{"name": ""}), "VALIDATION_ERROR", http.StatusBadRequest)

	created := call(t, env.handler, http.MethodPost, "/api/v1/teams", map[string]any{"name": "Platform"})
	assertStatus(t, created, http.StatusCreated)
	team := objectField(t, decodeMap(t, created), "team")
	teamID := stringField(t, team, "id")
	if team["version"] != float64(0) {
		t.Fatalf("team = %+v", team)
	}

	list := call(t, env.handler, http.MethodGet, "/api/v1/teams", nil)
	assertStatus(t, list, http.StatusOK)
	if teams := arrayField(t, decodeMap(t, list), "teams"); len(teams) != 1 {
		t.Fatalf("teams = %+v", teams)
	}

	got := call(t, env.handler, http.MethodGet, "/api/v1/teams/"+teamID, nil)
	assertStatus(t, got, http.StatusOK)
	if objectField(t, decodeMap(t, got), "team")["name"] != "Platform" {
		t.Fatal("GET team returned the wrong name")
	}

	updated := call(t, env.handler, http.MethodPut, "/api/v1/teams/"+teamID, map[string]any{
		"name": "Engineering", "expected_version": 0,
	})
	assertStatus(t, updated, http.StatusOK)
	updatedTeam := objectField(t, decodeMap(t, updated), "team")
	if updatedTeam["name"] != "Engineering" || updatedTeam["version"] != float64(1) {
		t.Fatalf("updated team = %+v", updatedTeam)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/teams/"+teamID, map[string]any{
		"name": "Stale", "expected_version": 0,
	}), "CONCURRENCY_CONFLICT", http.StatusConflict)

	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/teams/"+teamID, map[string]any{
		"name": "Bad", "expected_version": -1,
	}), "VALIDATION_ERROR", http.StatusBadRequest)

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/teams/01ARZ3NDEKTSV4RRFFQ69G5FAV", nil), "NOT_FOUND", http.StatusNotFound)
}

func TestMembershipEndpoints(t *testing.T) {
	env := newTestEnvironment(t)
	team := env.seedTeam(t, "Platform")
	alice := env.seedUser(t, "Alice", "alice@example.com")
	bob := env.seedUser(t, "Bob", "bob@example.com")
	teamID := team.ID().String()

	joined := call(t, env.handler, http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/join", map[string]any{
		"user_id": alice.ID().String(),
	})
	assertStatus(t, joined, http.StatusCreated)
	membership := objectField(t, decodeMap(t, joined), "membership")
	membershipID := stringField(t, membership, "id")
	if membership["status"] != "ACTIVE" || membership["role"] != "MEMBER" {
		t.Fatalf("joined membership = %+v", membership)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/join", map[string]any{
		"user_id": alice.ID().String(),
	}), "CONFLICT", http.StatusConflict)

	pending := call(t, env.handler, http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/request", map[string]any{
		"user_id": bob.ID().String(),
	})
	assertStatus(t, pending, http.StatusCreated)
	pendingMembership := objectField(t, decodeMap(t, pending), "membership")
	pendingID := stringField(t, pendingMembership, "id")
	if pendingMembership["status"] != "PENDING" {
		t.Fatalf("pending membership = %+v", pendingMembership)
	}

	approved := call(t, env.handler, http.MethodPost, "/api/v1/memberships/"+pendingID+"/approve", map[string]any{})
	assertStatus(t, approved, http.StatusOK)
	if objectField(t, decodeMap(t, approved), "membership")["status"] != "ACTIVE" {
		t.Fatal("approve did not activate the membership")
	}
	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/memberships/"+pendingID+"/approve", map[string]any{}), "CONFLICT", http.StatusConflict)

	role := call(t, env.handler, http.MethodPut, "/api/v1/memberships/"+pendingID+"/role", map[string]any{"role": "ADMIN"})
	assertStatus(t, role, http.StatusOK)
	if objectField(t, decodeMap(t, role), "membership")["role"] != "ADMIN" {
		t.Fatal("role change was not applied")
	}
	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/memberships/"+pendingID+"/role", map[string]any{"role": "GUEST"}), "VALIDATION_ERROR", http.StatusBadRequest)

	list := call(t, env.handler, http.MethodGet, "/api/v1/teams/"+teamID+"/memberships", nil)
	assertStatus(t, list, http.StatusOK)
	if memberships := arrayField(t, decodeMap(t, list), "memberships"); len(memberships) != 2 {
		t.Fatalf("memberships = %+v", memberships)
	}

	left := call(t, env.handler, http.MethodPost, "/api/v1/memberships/"+membershipID+"/leave", map[string]any{})
	assertStatus(t, left, http.StatusOK)
	if objectField(t, decodeMap(t, left), "membership")["status"] != "LEAVED" {
		t.Fatal("leave did not end the membership")
	}
	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/memberships/"+membershipID+"/leave", map[string]any{}), "CONFLICT", http.StatusConflict)

	rejoined := call(t, env.handler, http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/join", map[string]any{
		"user_id": alice.ID().String(),
	})
	assertStatus(t, rejoined, http.StatusCreated)
	if objectField(t, decodeMap(t, rejoined), "membership")["id"] == membershipID {
		t.Fatal("rejoin reused the previous membership ID")
	}

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/teams/01ARZ3NDEKTSV4RRFFQ69G5FAV/memberships/join", map[string]any{
		"user_id": alice.ID().String(),
	}), "NOT_FOUND", http.StatusNotFound)
	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/join", map[string]any{
		"user_id": "not-a-ulid",
	}), "VALIDATION_ERROR", http.StatusBadRequest)
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/teams/not-a-ulid/memberships", nil), "VALIDATION_ERROR", http.StatusBadRequest)
}

func TestBillingEndpoints(t *testing.T) {
	env := newTestEnvironment(t)

	before := env.guard.accounts["default"].Balance
	credited := call(t, env.handler, http.MethodPost, "/api/v1/accounts/default/credit", map[string]any{
		"amount": 50, "reason": "top-up",
	}, withRequestIDHeader("credit-1"))
	assertStatus(t, credited, http.StatusOK)
	account := decodeMap(t, credited)
	if account["balance"] != float64(before+50) || account["account_id"] != "default" {
		t.Fatalf("account after credit = %+v", account)
	}

	balanceBeforeCharge := env.guard.accounts["default"].Balance
	charged := call(t, env.handler, http.MethodPost, "/api/v1/accounts/default/charge", map[string]any{
		"amount": 25, "reason": "manual adjustment",
	})
	assertStatus(t, charged, http.StatusOK)
	if chargedAccount := decodeMap(t, charged); chargedAccount["balance"] != float64(balanceBeforeCharge-25) {
		t.Fatalf("charge debited the wrong amount: %+v", chargedAccount)
	}
	if got := charged.Header().Get("X-Account-Balance"); got != strconv.FormatInt(balanceBeforeCharge-25, 10) {
		t.Fatalf("charge balance header = %q, want %d", got, balanceBeforeCharge-25)
	}

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/default/charge", map[string]any{
		"amount": 1_000_000, "reason": "too much",
	}), "INSUFFICIENT_BALANCE", http.StatusPaymentRequired)

	assertErrorCode(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/default/charge", map[string]any{
		"amount": 0, "reason": "zero",
	}), "VALIDATION_ERROR", http.StatusBadRequest)

	get := call(t, env.handler, http.MethodGet, "/api/v1/accounts/default", nil)
	assertStatus(t, get, http.StatusOK)
	if decodeMap(t, get)["rate_limit_rpm"] != float64(120) {
		t.Fatalf("account = %+v", decodeMap(t, get))
	}

	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/accounts/missing", nil), "NOT_FOUND", http.StatusNotFound)

	env.audit.events = []domain.BillingEvent{{
		ID: domain.NewEventID(), AccountID: "default", Amount: -3, Reason: "request",
		RequestID: "req", OccurredAt: time.Now().UTC(),
	}}
	events := call(t, env.handler, http.MethodGet, "/api/v1/billing/events?account_id=default&limit=20", nil)
	assertStatus(t, events, http.StatusOK)
	eventRows := arrayField(t, decodeMap(t, events), "events")
	if len(eventRows) != 1 {
		t.Fatalf("events = %+v", eventRows)
	}
	event := eventRows[0].(map[string]any)
	if event["amount"] != float64(-3) || event["request_id"] != "req" || event["id"] == "" {
		t.Fatalf("event = %+v", event)
	}
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/billing/events?limit=0", nil), "VALIDATION_ERROR", http.StatusBadRequest)

	env.audit.logs = []domain.RequestLog{{
		ID: "log-1", RequestID: "req", AccountID: "default", Method: "GET",
		Path: "/api/v1/users", StatusCode: 200, LatencyUS: 42, OccurredAt: time.Now().UTC(),
	}}
	logs := call(t, env.handler, http.MethodGet, "/api/v1/logs?limit=20", nil)
	assertStatus(t, logs, http.StatusOK)
	logRows := arrayField(t, decodeMap(t, logs), "logs")
	if len(logRows) != 1 {
		t.Fatalf("logs = %+v", logRows)
	}
	logRow := logRows[0].(map[string]any)
	if logRow["path"] != "/api/v1/users" || logRow["status_code"] != float64(200) || logRow["latency_us"] != float64(42) {
		t.Fatalf("log = %+v", logRow)
	}
}

func TestRuntimeConfigAndGuardEndpoints(t *testing.T) {
	env := newTestEnvironment(t)

	config := call(t, env.handler, http.MethodGet, "/api/v1/runtime/config", nil)
	assertStatus(t, config, http.StatusOK)
	configPayload := decodeMap(t, config)
	if configPayload["default_rate_limit_rpm"] != float64(120) || configPayload["default_request_cost"] != float64(1) {
		t.Fatalf("config = %+v", configPayload)
	}

	updated := call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 60,
		"default_request_cost":   2,
		"maintenance_mode":       false,
		"routes": map[string]any{
			"GET /api/v1/users": map[string]any{"enabled": true, "cost": 3, "rate_limit_rpm": 30},
		},
	})
	assertStatus(t, updated, http.StatusOK)
	updatedPayload := decodeMap(t, updated)
	routes := objectField(t, updatedPayload, "routes")
	policy, ok := routes["GET /api/v1/users"].(map[string]any)
	if !ok || policy["cost"] != float64(3) || policy["rate_limit_rpm"] != float64(30) {
		t.Fatalf("routes = %+v", routes)
	}

	arrayUpdate := call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 120,
		"default_request_cost":   1,
		"maintenance_mode":       false,
		"routes": []any{
			map[string]any{"route": "GET /api/v1/users", "enabled": true, "cost": 1, "rate_limit_rpm": 0},
		},
	})
	assertStatus(t, arrayUpdate, http.StatusOK)

	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 0,
	}), "VALIDATION_ERROR", http.StatusBadRequest)
	assertErrorCode(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", `{"routes": 3}`), "VALIDATION_ERROR", http.StatusBadRequest)

	stats := call(t, env.handler, http.MethodGet, "/api/v1/runtime/stats", nil)
	assertStatus(t, stats, http.StatusOK)
	statsPayload := decodeMap(t, stats)
	objectField(t, statsPayload, "hotpath")
	objectField(t, statsPayload, "batcher")
	if objectField(t, statsPayload, "db")["journal_mode"] != "wal" {
		t.Fatalf("stats = %+v", statsPayload)
	}

	// Route policy disable -> 503, then re-enable.
	assertStatus(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 120,
		"default_request_cost":   1,
		"routes": map[string]any{
			"GET /api/v1/users": map[string]any{"enabled": false, "cost": 1, "rate_limit_rpm": 0},
		},
	}), http.StatusOK)
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil), "ROUTE_DISABLED", http.StatusServiceUnavailable)

	assertStatus(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 120,
		"default_request_cost":   1,
	}), http.StatusOK)
	assertStatus(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil), http.StatusOK)

	// Maintenance mode rejects metered routes but never the control plane.
	assertStatus(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 120,
		"default_request_cost":   1,
		"maintenance_mode":       true,
	}), http.StatusOK)
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil), "ROUTE_DISABLED", http.StatusServiceUnavailable)
	assertStatus(t, call(t, env.handler, http.MethodGet, "/healthz", nil), http.StatusOK)
	assertStatus(t, call(t, env.handler, http.MethodGet, "/api/v1/runtime/config", nil), http.StatusOK)
	assertStatus(t, call(t, env.handler, http.MethodGet, "/api/v1/accounts/default", nil), http.StatusOK)
	assertStatus(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/default/credit", map[string]any{
		"amount": 1, "reason": "maintenance credit",
	}), http.StatusOK)

	assertStatus(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 120,
		"default_request_cost":   1,
	}), http.StatusOK)

	// Insufficient balance on an account with no funds still allows a credit.
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil, withAccount("broke")), "INSUFFICIENT_BALANCE", http.StatusPaymentRequired)
	assertStatus(t, call(t, env.handler, http.MethodPost, "/api/v1/accounts/broke/credit", map[string]any{
		"amount": 5, "reason": "funding",
	}), http.StatusOK)
	assertStatus(t, call(t, env.handler, http.MethodGet, "/api/v1/users", nil, withAccount("broke")), http.StatusOK)

	// Rate limit a fresh account on one route: two succeed, the third is 429.
	env.guard.Credit("limited", 100)
	assertStatus(t, call(t, env.handler, http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 120,
		"default_request_cost":   1,
		"routes": map[string]any{
			"GET /api/v1/teams": map[string]any{"enabled": true, "cost": 1, "rate_limit_rpm": 2},
		},
	}), http.StatusOK)

	first := call(t, env.handler, http.MethodGet, "/api/v1/teams", nil, withAccount("limited"))
	assertStatus(t, first, http.StatusOK)
	if first.Header().Get("X-RateLimit-Limit") != "2" || first.Header().Get("X-RateLimit-Remaining") != "1" {
		t.Fatalf("first rate headers = %v", first.Header())
	}
	second := call(t, env.handler, http.MethodGet, "/api/v1/teams", nil, withAccount("limited"))
	assertStatus(t, second, http.StatusOK)
	if second.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("second rate headers = %v", second.Header())
	}
	assertErrorCode(t, call(t, env.handler, http.MethodGet, "/api/v1/teams", nil, withAccount("limited")), "RATE_LIMITED", http.StatusTooManyRequests)
}

func TestGuardRefundsFiveHundredsAndPanicRecovery(t *testing.T) {
	env := newTestEnvironment(t)
	env.guard.Credit("refund", 10)

	beforeBilling, _, _ := env.queue.snapshot()
	ctx := context.WithValue(context.Background(), accountIDContextKey, "refund")
	ctx = context.WithValue(ctx, requestIDContextKey, "refund-request")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/failing", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	failing := env.handler.withGuard("GET /api/v1/failing", true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	failing.ServeHTTP(recorder, request)
	assertStatus(t, recorder, http.StatusInternalServerError)

	if state, _ := env.guard.GetAccountState("refund"); state.Balance != 10 {
		t.Fatalf("balance after 500 = %d, want the pre-debit refunded", state.Balance)
	}
	afterBilling, afterQuotas, _ := env.queue.snapshot()
	if len(afterBilling) != len(beforeBilling) {
		t.Fatal("a failed request enqueued a billing event")
	}
	if len(afterQuotas) == 0 || afterQuotas[len(afterQuotas)-1].Balance != 10 {
		t.Fatalf("refund did not enqueue a corrected quota: %+v", afterQuotas)
	}

	panicRecorder := httptest.NewRecorder()
	panicking := env.handler.withPanicRecovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	panicking.ServeHTTP(panicRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil))
	assertErrorCode(t, panicRecorder, "UNEXPECTED", http.StatusInternalServerError)
}

func TestStatusForCode(t *testing.T) {
	cases := map[usecase.ErrorCode]int{
		usecase.CodeValidation:          http.StatusBadRequest,
		usecase.CodeInsufficientBalance: http.StatusPaymentRequired,
		usecase.CodeNotFound:            http.StatusNotFound,
		usecase.CodeConflict:            http.StatusConflict,
		usecase.CodeConcurrencyConflict: http.StatusConflict,
		usecase.CodeRateLimited:         http.StatusTooManyRequests,
		usecase.CodeRouteDisabled:       http.StatusServiceUnavailable,
		usecase.CodeUnexpected:          http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := statusForCode(code); got != want {
			t.Fatalf("statusForCode(%s) = %d, want %d", code, got, want)
		}
	}
}
