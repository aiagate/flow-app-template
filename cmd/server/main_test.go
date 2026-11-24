package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/infra/config"
	"github.com/aiagate/flow-app-template/internal/infra/hotpath"
	"github.com/aiagate/flow-app-template/internal/infra/sqlite"
)

type apiResponse struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    string
}

type e2eClient struct {
	t      *testing.T
	base   string
	client *http.Client
	token  string

	apiRequests   atomic.Int64
	billingEvents atomic.Int64
}

// baseTestConfig returns a valid end-to-end configuration using a fresh
// temporary database per test.
func baseTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		HTTPAddr:              "127.0.0.1:0",
		DBPath:                filepath.Join(t.TempDir(), "flow.db"),
		DBReadConns:           5,
		DefaultInitialBalance: 0,
		DefaultRateLimitRPM:   1000,
		DefaultRequestCost:    1,
		ShutdownTimeout:       5 * time.Second,
		AdminAPIKey:           "admin-secret",
		AccountAPIKeys:        nil,
		DrainTimeout:          5 * time.Second,
		WALWarnBytes:          64 << 20,
		MaxAccounts:           4096,
	}
}

func newE2EClient(t *testing.T, app *App, token string) *e2eClient {
	t.Helper()
	return &e2eClient{
		t:    t,
		base: "http://" + app.Addr(),
		// Keep-alive is disabled so the short-lived test never leaves a
		// just-dialed idle connection racing http.Server.Shutdown.
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}},
		token:  token,
	}
}

func shutdownTestApp(t *testing.T, app *App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := app.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func (c *e2eClient) do(method, path string, body any, account, requestID string) (*apiResponse, error) {
	if strings.HasPrefix(path, "/api/") {
		c.apiRequests.Add(1)
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if account != "" {
		request.Header.Set("X-Account-ID", account)
	}
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	result := &apiResponse{Status: response.StatusCode, Header: response.Header, Raw: string(raw)}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		if err := json.Unmarshal(raw, &result.Body); err != nil {
			return nil, fmt.Errorf("decode %s %s (%d): %w: %s", method, path, response.StatusCode, err, string(raw))
		}
	}

	// Every metered pre-debit emits a billing event for any response below
	// 500 except the three guard rejections that never debit. Credit is the
	// one unmetered ledger endpoint that also emits an event.
	if strings.HasSuffix(path, "/credit") {
		if response.StatusCode < 300 {
			c.billingEvents.Add(1)
		}
	} else if response.StatusCode < 500 &&
		response.StatusCode != http.StatusPaymentRequired &&
		response.StatusCode != http.StatusTooManyRequests &&
		response.StatusCode != http.StatusServiceUnavailable &&
		response.Header.Get("X-Account-Balance") != "" {
		c.billingEvents.Add(1)
	}
	return result, nil
}

func (c *e2eClient) mustDo(method, path string, body any, account, requestID string) *apiResponse {
	c.t.Helper()
	response, err := c.do(method, path, body, account, requestID)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

func (c *e2eClient) mustStatus(method, path string, body any, account string, want int) *apiResponse {
	c.t.Helper()
	response := c.mustDo(method, path, body, account, "")
	if response.Status != want {
		c.t.Fatalf("%s %s status = %d, want %d; body = %s", method, path, response.Status, want, response.Raw)
	}
	return response
}

func (c *e2eClient) mustError(method, path string, body any, account, wantCode string, wantStatus int) {
	c.t.Helper()
	response := c.mustDo(method, path, body, account, "")
	if response.Status != wantStatus {
		c.t.Fatalf("%s %s status = %d, want %d; body = %s", method, path, response.Status, wantStatus, response.Raw)
	}
	errorValue, ok := response.Body["error"].(map[string]any)
	if !ok {
		c.t.Fatalf("%s %s has no error envelope: %s", method, path, response.Raw)
	}
	if code, _ := errorValue["code"].(string); code != wantCode {
		c.t.Fatalf("%s %s error code = %q, want %q", method, path, code, wantCode)
	}
	if message, _ := errorValue["message"].(string); message == "" {
		c.t.Fatalf("%s %s error message is empty", method, path)
	}
}

func object(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := payload[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not an object: %v", key, payload[key])
	}
	return value
}

func array(t *testing.T, payload map[string]any, key string) []any {
	t.Helper()
	value, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("field %q is not an array: %v", key, payload[key])
	}
	return value
}

func text(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok || value == "" {
		t.Fatalf("field %q is not a non-empty string: %v", key, object[key])
	}
	return value
}

func TestEndToEndGracefulShutdownAndPersistence(t *testing.T) {
	cfg := baseTestConfig(t)

	app, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	client := newE2EClient(t, app, cfg.AdminAPIKey)

	// Health and embedded console.
	health := client.mustStatus(http.MethodGet, "/healthz", nil, "", http.StatusOK)
	if health.Body["status"] != "ok" {
		t.Fatalf("health = %s", health.Raw)
	}
	if db, ok := health.Body["db"].(map[string]any); !ok || db["journal_mode"] != "wal" {
		t.Fatalf("health db = %s", health.Raw)
	}
	index := client.mustStatus(http.MethodGet, "/", nil, "", http.StatusOK)
	if !strings.Contains(index.Raw, "app.js") {
		t.Fatal("index response does not reference app.js")
	}
	asset := client.mustStatus(http.MethodGet, "/assets/app.js", nil, "", http.StatusOK)
	if !strings.Contains(asset.Raw, "use strict") {
		t.Fatal("app.js asset was not served")
	}
	client.mustStatus(http.MethodGet, "/assets/styles.css", nil, "", http.StatusOK)

	// Fund the default account before metered traffic.
	credit := client.mustStatus(http.MethodPost, "/api/v1/accounts/default/credit", map[string]any{
		"amount": 1000, "reason": "seed",
	}, "", http.StatusOK)
	if credit.Body["balance"] != float64(1000) {
		t.Fatalf("default credit = %s", credit.Raw)
	}

	// Users CRUD and optimistic locking.
	createUser := client.mustStatus(http.MethodPost, "/api/v1/users", map[string]any{
		"display_name": "Alice", "email": "alice@example.com",
	}, "", http.StatusCreated)
	user := object(t, createUser.Body, "user")
	userID := text(t, user, "id")
	if createUser.Header.Get("X-Request-ID") == "" {
		t.Fatal("response did not echo a request ID")
	}

	listUsers := client.mustStatus(http.MethodGet, "/api/v1/users", nil, "", http.StatusOK)
	if len(array(t, listUsers.Body, "users")) != 1 {
		t.Fatalf("users = %s", listUsers.Raw)
	}
	client.mustStatus(http.MethodGet, "/api/v1/users/"+userID, nil, "", http.StatusOK)

	updatedUser := client.mustStatus(http.MethodPut, "/api/v1/users/"+userID+"/email", map[string]any{
		"email": "alice2@example.com", "expected_version": 0,
	}, "", http.StatusOK)
	if object(t, updatedUser.Body, "user")["version"] != float64(1) {
		t.Fatalf("updated user = %s", updatedUser.Raw)
	}
	client.mustError(http.MethodPut, "/api/v1/users/"+userID+"/email", map[string]any{
		"email": "stale@example.com", "expected_version": 0,
	}, "", "CONCURRENCY_CONFLICT", http.StatusConflict)
	client.mustError(http.MethodPost, "/api/v1/users", map[string]any{
		"display_name": "Duplicate", "email": "alice2@example.com",
	}, "", "CONFLICT", http.StatusConflict)
	client.mustError(http.MethodPost, "/api/v1/users", map[string]any{}, "", "VALIDATION_ERROR", http.StatusBadRequest)

	// Teams CRUD and optimistic locking.
	createTeam := client.mustStatus(http.MethodPost, "/api/v1/teams", map[string]any{"name": "Platform"}, "", http.StatusCreated)
	team := object(t, createTeam.Body, "team")
	teamID := text(t, team, "id")
	client.mustStatus(http.MethodGet, "/api/v1/teams", nil, "", http.StatusOK)
	client.mustStatus(http.MethodGet, "/api/v1/teams/"+teamID, nil, "", http.StatusOK)
	updatedTeam := client.mustStatus(http.MethodPut, "/api/v1/teams/"+teamID, map[string]any{
		"name": "Engineering", "expected_version": 0,
	}, "", http.StatusOK)
	if object(t, updatedTeam.Body, "team")["version"] != float64(1) {
		t.Fatalf("updated team = %s", updatedTeam.Raw)
	}
	client.mustError(http.MethodPut, "/api/v1/teams/"+teamID, map[string]any{
		"name": "Stale", "expected_version": 0,
	}, "", "CONCURRENCY_CONFLICT", http.StatusConflict)

	// Memberships: join, duplicate 409, request, approve, role, leave, rejoin.
	bob := client.mustStatus(http.MethodPost, "/api/v1/users", map[string]any{
		"display_name": "Bob", "email": "bob@example.com",
	}, "", http.StatusCreated)
	bobID := text(t, object(t, bob.Body, "user"), "id")

	joined := client.mustStatus(http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/join", map[string]any{
		"user_id": userID,
	}, "", http.StatusCreated)
	aliceMembership := text(t, object(t, joined.Body, "membership"), "id")
	client.mustError(http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/join", map[string]any{
		"user_id": userID,
	}, "", "CONFLICT", http.StatusConflict)

	requested := client.mustStatus(http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/request", map[string]any{
		"user_id": bobID,
	}, "", http.StatusCreated)
	bobMembership := text(t, object(t, requested.Body, "membership"), "id")

	memberships := client.mustStatus(http.MethodGet, "/api/v1/teams/"+teamID+"/memberships", nil, "", http.StatusOK)
	if len(array(t, memberships.Body, "memberships")) != 2 {
		t.Fatalf("memberships = %s", memberships.Raw)
	}
	client.mustStatus(http.MethodPost, "/api/v1/memberships/"+bobMembership+"/approve", map[string]any{}, "", http.StatusOK)
	client.mustStatus(http.MethodPut, "/api/v1/memberships/"+bobMembership+"/role", map[string]any{"role": "ADMIN"}, "", http.StatusOK)
	client.mustStatus(http.MethodPost, "/api/v1/memberships/"+aliceMembership+"/leave", map[string]any{}, "", http.StatusOK)
	client.mustError(http.MethodPost, "/api/v1/memberships/"+aliceMembership+"/leave", map[string]any{}, "", "CONFLICT", http.StatusConflict)

	rejoined := client.mustStatus(http.MethodPost, "/api/v1/teams/"+teamID+"/memberships/join", map[string]any{
		"user_id": userID,
	}, "", http.StatusCreated)
	if text(t, object(t, rejoined.Body, "membership"), "id") == aliceMembership {
		t.Fatal("rejoin reused the old membership period")
	}

	// Runtime config: disable and re-enable a route.
	client.mustStatus(http.MethodGet, "/api/v1/runtime/config", nil, "", http.StatusOK)
	client.mustStatus(http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 1000,
		"default_request_cost":   1,
		"routes": map[string]any{
			"GET /api/v1/users": map[string]any{"enabled": false, "cost": 1, "rate_limit_rpm": 0},
		},
	}, "", http.StatusOK)
	client.mustError(http.MethodGet, "/api/v1/users", nil, "", "ROUTE_DISABLED", http.StatusServiceUnavailable)
	client.mustStatus(http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 1000,
		"default_request_cost":   1,
	}, "", http.StatusOK)
	client.mustStatus(http.MethodGet, "/api/v1/users", nil, "", http.StatusOK)

	// Maintenance mode blocks metered routes but never the control plane.
	client.mustStatus(http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 1000,
		"default_request_cost":   1,
		"maintenance_mode":       true,
	}, "", http.StatusOK)
	client.mustError(http.MethodGet, "/api/v1/users", nil, "", "ROUTE_DISABLED", http.StatusServiceUnavailable)
	client.mustStatus(http.MethodGet, "/healthz", nil, "", http.StatusOK)
	client.mustStatus(http.MethodGet, "/api/v1/runtime/config", nil, "", http.StatusOK)
	client.mustStatus(http.MethodGet, "/api/v1/accounts/default", nil, "", http.StatusOK)
	client.mustStatus(http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 1000,
		"default_request_cost":   1,
	}, "", http.StatusOK)

	// Ledger: exact single debit, insufficient balance and validation.
	ledgerCredit := client.mustStatus(http.MethodPost, "/api/v1/accounts/ledger/credit", map[string]any{
		"amount": 500, "reason": "deposit",
	}, "", http.StatusOK)
	if ledgerCredit.Body["balance"] != float64(500) {
		t.Fatalf("ledger credit = %s", ledgerCredit.Raw)
	}
	ledgerCharge := client.mustStatus(http.MethodPost, "/api/v1/accounts/ledger/charge", map[string]any{
		"amount": 100, "reason": "usage",
	}, "", http.StatusOK)
	if ledgerCharge.Body["balance"] != float64(400) {
		t.Fatalf("ledger charge = %s", ledgerCharge.Raw)
	}
	account := client.mustStatus(http.MethodGet, "/api/v1/accounts/ledger", nil, "", http.StatusOK)
	if account.Body["balance"] != float64(400) {
		t.Fatalf("ledger account = %s", account.Raw)
	}
	client.mustError(http.MethodPost, "/api/v1/accounts/ledger/charge", map[string]any{
		"amount": 1000, "reason": "too much",
	}, "", "INSUFFICIENT_BALANCE", http.StatusPaymentRequired)

	// A zero-balance account is rejected, then a credit makes it usable.
	client.mustError(http.MethodGet, "/api/v1/users", nil, "broke", "INSUFFICIENT_BALANCE", http.StatusPaymentRequired)
	client.mustStatus(http.MethodPost, "/api/v1/accounts/broke/credit", map[string]any{
		"amount": 10, "reason": "funding",
	}, "", http.StatusOK)
	client.mustStatus(http.MethodGet, "/api/v1/users", nil, "broke", http.StatusOK)

	// Route-specific rate limit on a fresh account.
	client.mustStatus(http.MethodPost, "/api/v1/accounts/limited/credit", map[string]any{
		"amount": 10, "reason": "funding",
	}, "", http.StatusOK)
	client.mustStatus(http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 1000,
		"default_request_cost":   1,
		"routes": map[string]any{
			"GET /api/v1/teams": map[string]any{"enabled": true, "cost": 1, "rate_limit_rpm": 2},
		},
	}, "", http.StatusOK)
	first := client.mustDo(http.MethodGet, "/api/v1/teams", nil, "limited", "")
	if first.Header.Get("X-RateLimit-Limit") != "2" || first.Header.Get("X-RateLimit-Remaining") != "1" {
		t.Fatalf("first rate headers = %v", first.Header)
	}
	client.mustStatus(http.MethodGet, "/api/v1/teams", nil, "limited", http.StatusOK)
	client.mustError(http.MethodGet, "/api/v1/teams", nil, "limited", "RATE_LIMITED", http.StatusTooManyRequests)

	// Concurrent metered burst.
	client.mustStatus(http.MethodPost, "/api/v1/accounts/burst/credit", map[string]any{
		"amount": 500, "reason": "funding",
	}, "", http.StatusOK)
	const burstSize = 20
	var (
		wg       sync.WaitGroup
		failures chan error
	)
	failures = make(chan error, burstSize)
	for range burstSize {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := client.do(http.MethodGet, "/api/v1/users", nil, "burst", "")
			if err != nil {
				failures <- err
				return
			}
			if response.Status != http.StatusOK {
				failures <- fmt.Errorf("burst status = %d: %s", response.Status, response.Raw)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	if t.Failed() {
		t.FailNow()
	}

	// Capture the authoritative in-memory state before shutdown.
	accounts := []string{"default", "ledger", "broke", "limited", "burst"}
	finalBalances := make(map[string]int64, len(accounts))
	for _, accountID := range accounts {
		state, ok := app.hot.GetAccountState(accountID)
		if !ok {
			t.Fatalf("account %q missing from the hot path", accountID)
		}
		finalBalances[accountID] = state.Balance
	}
	expectedBilling := client.billingEvents.Load()
	expectedLogs := client.apiRequests.Load()
	if expectedBilling == 0 || expectedLogs == 0 {
		t.Fatalf("expected counters = (%d billing, %d logs)", expectedBilling, expectedLogs)
	}

	// Graceful shutdown order: HTTP -> batcher drain -> store close.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := app.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := app.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	writerStats := app.writer.Stats()
	if writerStats.Dropped != 0 {
		t.Fatalf("batcher dropped %d events", writerStats.Dropped)
	}
	if writerStats.Enqueued != writerStats.Written {
		t.Fatalf("batcher enqueued %d events but wrote %d", writerStats.Enqueued, writerStats.Written)
	}

	// Reopen the database fresh and verify nothing queued by the hot path was
	// lost.
	reopened, err := sqlite.Open(context.Background(), cfg.DBPath, cfg.DBReadConns)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer reopened.Close()

	var billingCount, logCount int64
	if err := reopened.ReadDB.QueryRow(`SELECT COUNT(*) FROM billing_events`).Scan(&billingCount); err != nil {
		t.Fatalf("count billing events: %v", err)
	}
	if billingCount != expectedBilling {
		t.Fatalf("persisted billing events = %d, want %d", billingCount, expectedBilling)
	}
	if err := reopened.ReadDB.QueryRow(`SELECT COUNT(*) FROM request_logs`).Scan(&logCount); err != nil {
		t.Fatalf("count request logs: %v", err)
	}
	if logCount != expectedLogs {
		t.Fatalf("persisted request logs = %d, want %d", logCount, expectedLogs)
	}

	var incompleteLogs, incompleteEvents int64
	if err := reopened.ReadDB.QueryRow(`
		SELECT COUNT(*) FROM request_logs
		WHERE id = '' OR request_id = '' OR account_id = '' OR method = '' OR path = '' OR status_code = 0
	`).Scan(&incompleteLogs); err != nil {
		t.Fatalf("check request log fields: %v", err)
	}
	if incompleteLogs != 0 {
		t.Fatalf("%d request logs are missing required fields", incompleteLogs)
	}
	if err := reopened.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM billing_events WHERE id = '' OR account_id = '' OR reason = '' OR request_id = ''`,
	).Scan(&incompleteEvents); err != nil {
		t.Fatalf("check billing event fields: %v", err)
	}
	if incompleteEvents != 0 {
		t.Fatalf("%d billing events are missing required fields", incompleteEvents)
	}

	for _, accountID := range accounts {
		want := finalBalances[accountID]
		var (
			balance int64
			amount  int64
		)
		if err := reopened.ReadDB.QueryRow(
			`SELECT balance FROM account_quotas WHERE account_id = ?`, accountID,
		).Scan(&balance); err != nil {
			t.Fatalf("load quota %q: %v", accountID, err)
		}
		if balance != want {
			t.Fatalf("persisted balance for %q = %d, want %d", accountID, balance, want)
		}
		if err := reopened.ReadDB.QueryRow(
			`SELECT COALESCE(SUM(amount), 0) FROM billing_events WHERE account_id = ?`, accountID,
		).Scan(&amount); err != nil {
			t.Fatalf("sum billing events for %q: %v", accountID, err)
		}
		if amount != want {
			t.Fatalf("billing event sum for %q = %d, want balance %d", accountID, amount, want)
		}
	}

	// Hydrating a fresh hot path from the reopened read pool must reproduce the
	// exact balances.
	quotas, err := sqlite.NewQuotaRepository(reopened).ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll quotas: %v", err)
	}
	freshHotPath := hotpath.New(hotpath.Options{})
	freshHotPath.Hydrate(quotas)
	for _, accountID := range accounts {
		state, ok := freshHotPath.GetAccountState(accountID)
		if !ok {
			t.Fatalf("fresh hot path has no account %q", accountID)
		}
		if state.Balance != finalBalances[accountID] {
			t.Fatalf("hydrated balance for %q = %d, want %d", accountID, state.Balance, finalBalances[accountID])
		}
	}
}

func TestEndToEndAuthenticationAndAuthorization(t *testing.T) {
	cfg := baseTestConfig(t)
	cfg.AdminAPIKey = "admin-secret"
	cfg.AccountAPIKeys = map[string]string{"acct-a": "acct-a-secret"}
	cfg.DefaultInitialBalance = 1000

	app, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer shutdownTestApp(t, app)

	anonymous := newE2EClient(t, app, "")
	account := newE2EClient(t, app, cfg.AccountAPIKeys["acct-a"])
	admin := newE2EClient(t, app, cfg.AdminAPIKey)

	// Every /api route requires a bearer token, while the console and the
	// probe stay public.
	anonymous.mustError(http.MethodGet, "/api/v1/users", nil, "", "UNAUTHORIZED", http.StatusUnauthorized)
	anonymous.mustStatus(http.MethodGet, "/", nil, "", http.StatusOK)
	anonymous.mustStatus(http.MethodGet, "/healthz", nil, "", http.StatusOK)

	// An account principal is pinned to its own account and cannot touch the
	// control plane or the unmetered funding route.
	account.mustError(http.MethodGet, "/api/v1/runtime/config", nil, "", "FORBIDDEN", http.StatusForbidden)
	account.mustError(http.MethodPost, "/api/v1/accounts/default/credit", map[string]any{
		"amount": 10, "reason": "forbidden",
	}, "", "FORBIDDEN", http.StatusForbidden)
	account.mustStatus(http.MethodGet, "/api/v1/accounts/acct-a", nil, "", http.StatusOK)
	account.mustStatus(http.MethodPost, "/api/v1/accounts/acct-a/charge", map[string]any{
		"amount": 10, "reason": "usage",
	}, "", http.StatusOK)
	account.mustStatus(http.MethodGet, "/api/v1/users", nil, "", http.StatusOK)

	// The admin key reaches every route, including account-scoped ledger
	// operations.
	admin.mustStatus(http.MethodGet, "/api/v1/runtime/config", nil, "", http.StatusOK)
	admin.mustStatus(http.MethodGet, "/api/v1/runtime/stats", nil, "", http.StatusOK)
	admin.mustStatus(http.MethodGet, "/api/v1/logs", nil, "", http.StatusOK)
	admin.mustStatus(http.MethodGet, "/api/v1/accounts/acct-a", nil, "", http.StatusOK)
	admin.mustStatus(http.MethodPost, "/api/v1/accounts/acct-a/credit", map[string]any{
		"amount": 25, "reason": "top-up",
	}, "", http.StatusOK)
	admin.mustStatus(http.MethodPost, "/api/v1/accounts/acct-a/charge", map[string]any{
		"amount": 5, "reason": "usage",
	}, "", http.StatusOK)
	admin.mustStatus(http.MethodGet, "/api/v1/users", nil, "", http.StatusOK)
}

func TestRuntimeConfigPersistsAcrossServerRestart(t *testing.T) {
	cfg := baseTestConfig(t)
	cfg.AdminAPIKey = "admin-secret"
	cfg.AccountAPIKeys = map[string]string{"acct-a": "acct-a-secret"}

	first, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp first: %v", err)
	}
	if err := first.Start(); err != nil {
		t.Fatalf("Start first: %v", err)
	}

	client := newE2EClient(t, first, cfg.AdminAPIKey)
	client.mustStatus(http.MethodPut, "/api/v1/runtime/config", map[string]any{
		"default_rate_limit_rpm": 250,
		"default_request_cost":   3,
		"maintenance_mode":       true,
		"routes": map[string]any{
			"GET /api/v1/users": map[string]any{"enabled": false, "cost": 7, "rate_limit_rpm": 11},
		},
	}, "", http.StatusOK)
	want := client.mustStatus(http.MethodGet, "/api/v1/runtime/config", nil, "", http.StatusOK)
	shutdownTestApp(t, first)

	second, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp second: %v", err)
	}
	if err := second.Start(); err != nil {
		t.Fatalf("Start second: %v", err)
	}
	defer shutdownTestApp(t, second)

	restarted := newE2EClient(t, second, cfg.AdminAPIKey)
	got := restarted.mustStatus(http.MethodGet, "/api/v1/runtime/config", nil, "", http.StatusOK)
	if !reflect.DeepEqual(want.Body, got.Body) {
		t.Fatalf("persisted runtime config changed across restart:\nwant = %#v\ngot  = %#v", want.Body, got.Body)
	}
	if got.Body["maintenance_mode"] != true {
		t.Fatalf("maintenance_mode = %v, want true", got.Body["maintenance_mode"])
	}
	routes := object(t, got.Body, "routes")
	policy := object(t, routes, "GET /api/v1/users")
	if policy["enabled"] != false || policy["cost"] != float64(7) || policy["rate_limit_rpm"] != float64(11) {
		t.Fatalf("persisted route policy = %#v", policy)
	}
}

func TestHealthzReturns503WhenWALExceedsWarnThreshold(t *testing.T) {
	cfg := baseTestConfig(t)
	cfg.WALWarnBytes = 1

	app, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer shutdownTestApp(t, app)

	client := newE2EClient(t, app, cfg.AdminAPIKey)
	client.mustStatus(http.MethodPost, "/api/v1/accounts/wal/credit", map[string]any{
		"amount": 100, "reason": "grow-wal",
	}, "", http.StatusOK)

	// Wait until the batcher has committed at least one write so the WAL file
	// has grown from the configured threshold.
	deadline := time.Now().Add(5 * time.Second)
	for app.writer.Stats().Written == 0 {
		if time.Now().After(deadline) {
			t.Fatal("batcher did not commit a write")
		}
		time.Sleep(5 * time.Millisecond)
	}

	health := client.mustStatus(http.MethodGet, "/healthz", nil, "", http.StatusServiceUnavailable)
	if health.Body["status"] != "degraded" {
		t.Fatalf("health status = %v, want degraded", health.Body["status"])
	}
	db := object(t, health.Body, "db")
	walBytes, _ := db["wal_bytes"].(float64)
	warnBytes, _ := db["wal_warn_bytes"].(float64)
	if warnBytes != 1 {
		t.Fatalf("wal_warn_bytes = %v, want 1", db["wal_warn_bytes"])
	}
	if walBytes <= warnBytes {
		t.Fatalf("wal_bytes = %v, want greater than wal_warn_bytes %v", walBytes, warnBytes)
	}
	if db["wal_healthy"] != false {
		t.Fatalf("wal_healthy = %v, want false", db["wal_healthy"])
	}
}

func TestShutdownWithTimeoutsPersistsFinalSnapshotWithIndependentBudgets(t *testing.T) {
	cfg := baseTestConfig(t)

	app, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Mutate only the hot path so the final SnapshotQuotas is the sole writer
	// of this balance.
	if err := app.hot.Credit("final", 777); err != nil {
		t.Fatalf("seed hot path: %v", err)
	}

	httpCtx, cancelHTTP := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelHTTP()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDrain()

	// An expired HTTP budget may surface a context error, but the valid drain
	// budget must still persist the final snapshot.
	if err := app.ShutdownWithTimeouts(httpCtx, drainCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ShutdownWithTimeouts: %v", err)
	}
	if err := app.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	stats := app.writer.Stats()
	if stats.Dropped != 0 {
		t.Fatalf("batcher dropped %d events", stats.Dropped)
	}
	if stats.Enqueued != stats.Written {
		t.Fatalf("batcher enqueued %d events but wrote %d", stats.Enqueued, stats.Written)
	}

	reopened, err := sqlite.Open(context.Background(), cfg.DBPath, cfg.DBReadConns)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer reopened.Close()

	var balance int64
	if err := reopened.ReadDB.QueryRow(
		`SELECT balance FROM account_quotas WHERE account_id = ?`, "final",
	).Scan(&balance); err != nil {
		t.Fatalf("load final quota: %v", err)
	}
	if balance != 777 {
		t.Fatalf("persisted final balance = %d, want 777", balance)
	}
}
