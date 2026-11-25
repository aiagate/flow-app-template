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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/infra/batcher"
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
	if _, err := app.hot.Credit("final", 777, nil); err != nil {
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

// TestEndToEndQueueFullRejectsMeteredRouteWithoutLosingBillingHistory proves
// that a saturated write queue rejects a metered request before the hot path
// debits it, and that the rejection neither records a billing event nor drops
// one that should have been recorded.
func TestEndToEndQueueFullRejectsMeteredRouteWithoutLosingBillingHistory(t *testing.T) {
	const initialBalance = int64(100)

	cfg := baseTestConfig(t)
	cfg.DefaultInitialBalance = initialBalance
	cfg.DefaultRateLimitRPM = 1000

	app, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	client := newE2EClient(t, app, cfg.AdminAPIKey)

	// Hold every batcher slot so the next metered request cannot reserve one.
	held := make([]port.WriteReservation, 0, batcher.QueueCapacity)
	defer func() {
		for _, res := range held {
			res.Release()
		}
	}()
	for len(held) < batcher.QueueCapacity {
		res, err := app.writer.Reserve()
		if err != nil {
			t.Fatalf("Reserve %d/%d: %v", len(held), batcher.QueueCapacity, err)
		}
		held = append(held, res)
	}
	if _, err := app.writer.Reserve(); !errors.Is(err, port.ErrQueueFull) {
		t.Fatalf("Reserve with a full queue error = %v, want ErrQueueFull", err)
	}

	before := client.mustStatus(http.MethodGet, "/api/v1/accounts/default", nil, "", http.StatusOK)
	if before.Body["balance"] != float64(initialBalance) {
		t.Fatalf("balance before rejection = %v, want %d", before.Body["balance"], initialBalance)
	}

	rejected := client.mustDo(http.MethodGet, "/api/v1/users", nil, "", "")
	if rejected.Status != http.StatusServiceUnavailable {
		t.Fatalf("metered status under saturation = %d, want %d; body = %s",
			rejected.Status, http.StatusServiceUnavailable, rejected.Raw)
	}
	if code, _ := object(t, rejected.Body, "error")["code"].(string); code != "SERVICE_UNAVAILABLE" {
		t.Fatalf("metered rejection code = %q, want SERVICE_UNAVAILABLE", code)
	}

	// Admission failed before the debit and before any event was enqueued:
	// the balance is untouched and the ledger has no phantom row.
	afterRejection := client.mustStatus(http.MethodGet, "/api/v1/accounts/default", nil, "", http.StatusOK)
	if afterRejection.Body["balance"] != float64(initialBalance) {
		t.Fatalf("balance after rejection = %v, want %d", afterRejection.Body["balance"], initialBalance)
	}

	var rejectedEvents int64
	if err := app.store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM billing_events WHERE account_id = ?`, defaultAccountID,
	).Scan(&rejectedEvents); err != nil {
		t.Fatalf("count billing events after rejection: %v", err)
	}
	if rejectedEvents != 0 {
		t.Fatalf("billing events after rejected request = %d, want 0", rejectedEvents)
	}

	// Return the slots, then the same metered route must succeed and bill once.
	for _, res := range held {
		res.Release()
	}
	held = held[:0]

	allowed := client.mustStatus(http.MethodGet, "/api/v1/users", nil, "", http.StatusOK)
	if got := allowed.Header.Get("X-Account-Balance"); got != fmt.Sprintf("%d", initialBalance-1) {
		t.Fatalf("balance header after allowed request = %q, want %d", got, initialBalance-1)
	}
	afterAllowed := client.mustStatus(http.MethodGet, "/api/v1/accounts/default", nil, "", http.StatusOK)
	if afterAllowed.Body["balance"] != float64(initialBalance-1) {
		t.Fatalf("balance after allowed request = %v, want %d", afterAllowed.Body["balance"], initialBalance-1)
	}

	shutdownTestApp(t, app)

	reopened, err := sqlite.Open(context.Background(), cfg.DBPath, cfg.DBReadConns)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer reopened.Close()

	quota, err := sqlite.NewQuotaRepository(reopened).GetByAccountID(context.Background(), defaultAccountID)
	if err != nil {
		t.Fatalf("GetByAccountID: %v", err)
	}
	events, err := sqlite.NewAuditQuery(reopened).ListBillingEvents(context.Background(), defaultAccountID, 1000)
	if err != nil {
		t.Fatalf("ListBillingEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("persisted billing events = %d, want exactly 1", len(events))
	}
	var sum int64
	for _, event := range events {
		sum += event.Amount
	}
	if sum != -1 {
		t.Fatalf("billing event sum = %d, want -1", sum)
	}
	if want := initialBalance + sum; quota.Balance != want {
		t.Fatalf("persisted balance = %d, want initialBalance + SUM(events) = %d", quota.Balance, want)
	}
	if quota.Seq != 1 {
		t.Fatalf("persisted seq = %d, want 1", quota.Seq)
	}
}

// TestEndToEndConcurrentChargesAndRestartPreserveMonotonicSeqAndBalance drives
// concurrent explicit charges and metered pre-debits, inspects SQLite before a
// clean shutdown to simulate a crash, then restarts on the same file and proves
// that hydration restores the persisted sequence instead of freezing updates.
func TestEndToEndConcurrentChargesAndRestartPreserveMonotonicSeqAndBalance(t *testing.T) {
	const (
		initialBalance = int64(1000)
		chargeCount    = 20
		chargeAmount   = int64(10)
		meteredCount   = 20
		requestCost    = int64(1)
	)

	cfg := baseTestConfig(t)
	cfg.DefaultInitialBalance = initialBalance
	// 40 requests in one burst stay well below the per-minute limit.
	cfg.DefaultRateLimitRPM = 120

	first, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp first: %v", err)
	}
	if err := first.Start(); err != nil {
		t.Fatalf("Start first: %v", err)
	}

	client := newE2EClient(t, first, cfg.AdminAPIKey)

	var (
		wg       sync.WaitGroup
		failures = make(chan error, chargeCount+meteredCount)
	)
	for range chargeCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := client.do(http.MethodPost, "/api/v1/accounts/default/charge", map[string]any{
				"amount": chargeAmount, "reason": "concurrent-charge",
			}, "", "")
			if err != nil {
				failures <- err
				return
			}
			if response.Status != http.StatusOK {
				failures <- fmt.Errorf("charge status = %d: %s", response.Status, response.Raw)
			}
		}()
	}
	for range meteredCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := client.do(http.MethodGet, "/api/v1/users", nil, "", "")
			if err != nil {
				failures <- err
				return
			}
			if response.Status != http.StatusOK {
				failures <- fmt.Errorf("metered status = %d: %s", response.Status, response.Raw)
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

	// Wait until the writer has committed every accepted event. This inspects
	// SQLite while the process is still running, simulating a crash that never
	// reaches the final SnapshotQuotas/Shutdown path.
	quotaRepo := sqlite.NewQuotaRepository(first.store)
	audit := sqlite.NewAuditQuery(first.store)
	wantEvents := chargeCount + meteredCount
	wantSum := -(chargeAmount*chargeCount + requestCost*meteredCount)
	deadline := time.Now().Add(10 * time.Second)
	var persisted []domain.BillingEvent
	for {
		stats := first.writer.Stats()
		events, err := audit.ListBillingEvents(context.Background(), defaultAccountID, 1000)
		if err != nil {
			t.Fatalf("ListBillingEvents while running: %v", err)
		}
		var sum int64
		for _, event := range events {
			sum += event.Amount
		}
		if stats.QueueDepth == 0 && stats.Pending == 0 && len(events) == wantEvents && sum == wantSum {
			persisted = events
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("writer did not drain: stats = %+v, events = %d, sum = %d", stats, len(events), sum)
		}
		time.Sleep(5 * time.Millisecond)
	}

	quota, err := quotaRepo.GetByAccountID(context.Background(), defaultAccountID)
	if err != nil {
		t.Fatalf("GetByAccountID before shutdown: %v", err)
	}
	if quota.Balance != 780 {
		t.Fatalf("balance before shutdown = %d, want 780", quota.Balance)
	}
	if quota.Seq != 40 {
		t.Fatalf("seq before shutdown = %d, want 40", quota.Seq)
	}
	var persistedSum int64
	for _, event := range persisted {
		persistedSum += event.Amount
	}
	if want := initialBalance + persistedSum; quota.Balance != want {
		t.Fatalf("balance before shutdown = %d, want initialBalance + SUM(events) = %d", quota.Balance, want)
	}

	shutdownTestApp(t, first)

	// Restart on the same file. Hydrate must restore balance and Seq so the
	// `WHERE excluded.seq >= account_quotas.seq` upsert keeps accepting writes.
	second, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp second: %v", err)
	}
	if err := second.Start(); err != nil {
		t.Fatalf("Start second: %v", err)
	}
	restarted := newE2EClient(t, second, cfg.AdminAPIKey)

	account := restarted.mustStatus(http.MethodGet, "/api/v1/accounts/default", nil, "", http.StatusOK)
	if account.Body["balance"] != float64(780) {
		t.Fatalf("balance after restart = %v, want 780", account.Body["balance"])
	}
	charged := restarted.mustStatus(http.MethodPost, "/api/v1/accounts/default/charge", map[string]any{
		"amount": 30, "reason": "post-restart",
	}, "", http.StatusOK)
	if charged.Body["balance"] != float64(750) {
		t.Fatalf("balance after post-restart charge = %v, want 750", charged.Body["balance"])
	}
	if got := charged.Header.Get("X-Account-Balance"); got != "750" {
		t.Fatalf("post-restart balance header = %q, want 750", got)
	}

	shutdownTestApp(t, second)

	reopened, err := sqlite.Open(context.Background(), cfg.DBPath, cfg.DBReadConns)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer reopened.Close()

	finalQuota, err := sqlite.NewQuotaRepository(reopened).GetByAccountID(context.Background(), defaultAccountID)
	if err != nil {
		t.Fatalf("GetByAccountID after restart: %v", err)
	}
	if finalQuota.Balance != 750 {
		t.Fatalf("persisted balance after restart = %d, want 750", finalQuota.Balance)
	}
	if finalQuota.Seq != 41 {
		t.Fatalf("persisted seq after restart = %d, want 41", finalQuota.Seq)
	}
	events, err := sqlite.NewAuditQuery(reopened).ListBillingEvents(context.Background(), defaultAccountID, 1000)
	if err != nil {
		t.Fatalf("ListBillingEvents after restart: %v", err)
	}
	if len(events) != wantEvents+1 {
		t.Fatalf("persisted billing events after restart = %d, want %d", len(events), wantEvents+1)
	}
	var finalSum int64
	for _, event := range events {
		finalSum += event.Amount
	}
	if want := initialBalance + finalSum; finalQuota.Balance != want {
		t.Fatalf("persisted balance = %d, want initialBalance + SUM(events) = %d", finalQuota.Balance, want)
	}
}

// TestEndToEndCreditAndChargeUnderQueueSaturationNeverGoesNegative hammers the
// explicit ledger routes while the write queue oscillates between saturated and
// partially drained. It proves that a rejected credit is always a clean 503 and
// that observed and persisted balances never go negative or invent credit.
func TestEndToEndCreditAndChargeUnderQueueSaturationNeverGoesNegative(t *testing.T) {
	const (
		accountID      = defaultAccountID
		ledgerAmount   = int64(100)
		creditPath     = "/api/v1/accounts/" + accountID + "/credit"
		chargePath     = "/api/v1/accounts/" + accountID + "/charge"
		saturationPair = 4
	)

	cfg := baseTestConfig(t)
	cfg.DefaultInitialBalance = 0
	cfg.DefaultRateLimitRPM = 1000

	app, err := newApp(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	client := newE2EClient(t, app, cfg.AdminAPIKey)

	held := make([]port.WriteReservation, 0, batcher.QueueCapacity)
	releaseHeld := func() {
		for _, res := range held {
			res.Release()
		}
		held = held[:0]
	}
	defer releaseHeld()

	// fill takes every slot that is currently free, which re-acquires capacity
	// after each release round.
	fill := func() {
		for {
			res, err := app.writer.Reserve()
			if err != nil {
				if !errors.Is(err, port.ErrQueueFull) {
					t.Fatalf("Reserve: %v", err)
				}
				return
			}
			held = append(held, res)
		}
	}
	releaseSome := func(n int) {
		if n > len(held) {
			n = len(held)
		}
		for _, res := range held[len(held)-n:] {
			res.Release()
		}
		held = held[:len(held)-n]
	}

	type ledgerOutcome struct {
		kind       string
		status     int
		code       string
		balance    int64
		hasBalance bool
		err        error
	}

	runBatch := func(credits, charges int) []ledgerOutcome {
		outcomes := make([]ledgerOutcome, credits+charges)
		var wg sync.WaitGroup
		launch := func(index int, kind, path string) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				outcome := ledgerOutcome{kind: kind}
				response, err := client.do(http.MethodPost, path, map[string]any{
					"amount": ledgerAmount, "reason": "saturation-" + kind,
				}, "", "")
				if err != nil {
					outcome.err = err
					outcomes[index] = outcome
					return
				}
				outcome.status = response.Status
				if errorValue, ok := response.Body["error"].(map[string]any); ok {
					outcome.code, _ = errorValue["code"].(string)
				}
				if bodyBalance, ok := response.Body["balance"].(float64); ok {
					outcome.balance = int64(bodyBalance)
					outcome.hasBalance = true
				}
				if header := response.Header.Get("X-Account-Balance"); header != "" {
					parsed, parseErr := strconv.ParseInt(header, 10, 64)
					if parseErr != nil {
						outcome.err = fmt.Errorf("%s balance header %q: %w", kind, header, parseErr)
						outcomes[index] = outcome
						return
					}
					if outcome.hasBalance && outcome.balance != parsed {
						outcome.err = fmt.Errorf("%s body balance %d != header balance %d", kind, outcome.balance, parsed)
						outcomes[index] = outcome
						return
					}
					outcome.balance = parsed
					outcome.hasBalance = true
				}
				outcomes[index] = outcome
			}()
		}
		for i := 0; i < credits; i++ {
			launch(i, "credit", creditPath)
		}
		for i := 0; i < charges; i++ {
			launch(credits+i, "charge", chargePath)
		}
		wg.Wait()
		return outcomes
	}

	verifyOutcomes := func(outcomes []ledgerOutcome) (creditOK, chargeOK int) {
		t.Helper()
		for _, outcome := range outcomes {
			if outcome.err != nil {
				t.Fatalf("%s: %v", outcome.kind, outcome.err)
			}
			switch outcome.kind {
			case "credit":
				switch outcome.status {
				case http.StatusOK:
					creditOK++
				case http.StatusServiceUnavailable:
					if outcome.code != "SERVICE_UNAVAILABLE" {
						t.Fatalf("rejected credit code = %q, want SERVICE_UNAVAILABLE", outcome.code)
					}
				default:
					t.Fatalf("credit status = %d, want 200 or 503; body code = %q", outcome.status, outcome.code)
				}
			case "charge":
				switch outcome.status {
				case http.StatusOK:
					chargeOK++
				case http.StatusPaymentRequired:
					// A charge that wins a slot while the balance is zero is
					// rejected without moving funds.
				case http.StatusServiceUnavailable:
					if outcome.code != "SERVICE_UNAVAILABLE" {
						t.Fatalf("rejected charge code = %q, want SERVICE_UNAVAILABLE", outcome.code)
					}
				default:
					t.Fatalf("charge status = %d, want 200, 402 or 503; body code = %q", outcome.status, outcome.code)
				}
			}
			if outcome.hasBalance && outcome.balance < 0 {
				t.Fatalf("%s observed negative balance %d", outcome.kind, outcome.balance)
			}
		}
		return creditOK, chargeOK
	}

	observeBalance := func() int64 {
		t.Helper()
		account := client.mustStatus(http.MethodGet, "/api/v1/accounts/"+accountID, nil, "", http.StatusOK)
		balance, ok := account.Body["balance"].(float64)
		if !ok {
			t.Fatalf("account balance is missing: %s", account.Raw)
		}
		if int64(balance) < 0 {
			t.Fatalf("observed negative balance %d", int64(balance))
		}
		return int64(balance)
	}

	var creditOK, chargeOK int

	// Round 1: the queue is completely full, so every concurrent credit and
	// charge must be rejected before it can move balance.
	fill()
	if len(held) != batcher.QueueCapacity {
		t.Fatalf("held reservations = %d, want %d", len(held), batcher.QueueCapacity)
	}
	full := runBatch(saturationPair, saturationPair)
	for _, outcome := range full {
		if outcome.status != http.StatusServiceUnavailable {
			t.Fatalf("%s status with a full queue = %d, want 503", outcome.kind, outcome.status)
		}
	}
	verifyOutcomes(full)
	if balance := observeBalance(); balance != 0 {
		t.Fatalf("balance after fully saturated round = %d, want 0", balance)
	}

	// Round 2: release exactly one slot so a concurrent batch races for it.
	// The writer may recycle that slot between requests, so the split is not
	// deterministic; every rejection must still be a clean 503 and no observed
	// balance may go negative.
	releaseSome(1)
	mixed := runBatch(saturationPair, saturationPair)
	mixedCreditOK, mixedChargeOK := verifyOutcomes(mixed)
	creditOK += mixedCreditOK
	chargeOK += mixedChargeOK
	observeBalance()

	// Round 3: re-acquire every free slot and repeat the race, proving the
	// rejection path stays clean across release/re-acquire cycles.
	fill()
	releaseSome(2)
	reacquired := runBatch(saturationPair, saturationPair)
	reacquiredCreditOK, reacquiredChargeOK := verifyOutcomes(reacquired)
	creditOK += reacquiredCreditOK
	chargeOK += reacquiredChargeOK
	observeBalance()

	// Drain the queue, then prove the accepted path still moves exactly the
	// requested amounts and never below zero.
	releaseHeld()
	for range 3 {
		credit := client.mustStatus(http.MethodPost, creditPath, map[string]any{
			"amount": ledgerAmount, "reason": "accepted-credit",
		}, "", http.StatusOK)
		if balance, ok := credit.Body["balance"].(float64); !ok || int64(balance) < 0 {
			t.Fatalf("credit observed invalid balance %v", credit.Body["balance"])
		}
		creditOK++
		observeBalance()

		charge := client.mustStatus(http.MethodPost, chargePath, map[string]any{
			"amount": ledgerAmount, "reason": "accepted-charge",
		}, "", http.StatusOK)
		if balance, ok := charge.Body["balance"].(float64); !ok || int64(balance) < 0 {
			t.Fatalf("charge observed invalid balance %v", charge.Body["balance"])
		}
		chargeOK++
		observeBalance()
	}
	if creditOK == 0 || chargeOK == 0 {
		t.Fatalf("accepted ledger operations = (%d credits, %d charges), want both non-zero", creditOK, chargeOK)
	}

	expectedBalance := ledgerAmount * int64(creditOK-chargeOK)
	if balance := observeBalance(); balance != expectedBalance {
		t.Fatalf("balance before shutdown = %d, want %d", balance, expectedBalance)
	}

	shutdownTestApp(t, app)

	reopened, err := sqlite.Open(context.Background(), cfg.DBPath, cfg.DBReadConns)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer reopened.Close()

	quota, err := sqlite.NewQuotaRepository(reopened).GetByAccountID(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetByAccountID: %v", err)
	}
	events, err := sqlite.NewAuditQuery(reopened).ListBillingEvents(context.Background(), accountID, 1000)
	if err != nil {
		t.Fatalf("ListBillingEvents: %v", err)
	}
	if len(events) != creditOK+chargeOK {
		t.Fatalf("persisted billing events = %d, want %d accepted ledger operations", len(events), creditOK+chargeOK)
	}
	var (
		sum          int64
		creditEvents int
		chargeEvents int
	)
	for _, event := range events {
		sum += event.Amount
		switch event.Amount {
		case ledgerAmount:
			creditEvents++
		case -ledgerAmount:
			chargeEvents++
		default:
			t.Fatalf("unexpected billing event amount %d", event.Amount)
		}
	}
	if creditEvents != creditOK || chargeEvents != chargeOK {
		t.Fatalf("persisted events = (%d credits, %d charges), want (%d, %d)",
			creditEvents, chargeEvents, creditOK, chargeOK)
	}
	const startingBalance = int64(0)
	if quota.Balance != startingBalance+sum {
		t.Fatalf("persisted balance = %d, want startingBalance + SUM(events) = %d", quota.Balance, startingBalance+sum)
	}
	if quota.Balance != expectedBalance {
		t.Fatalf("persisted balance = %d, want %d", quota.Balance, expectedBalance)
	}
	if quota.Balance < 0 {
		t.Fatalf("persisted balance is negative: %d", quota.Balance)
	}
}
