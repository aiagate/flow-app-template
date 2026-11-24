package hotpath

import (
	"errors"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
)

func TestAuthorizeEnforcesPolicyWithoutDebiting(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := New(Options{InitialBalance: 100, DefaultRateLimitRPM: 2, Clock: clock.Now})
	store.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 2,
		DefaultRequestCost:  5,
	})

	first, err := store.Authorize("acct", "GET /api/v1/users")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !first.Allowed || first.Cost != 0 || first.RemainingBalance != 100 || first.RemainingTokens != 1 {
		t.Fatalf("first decision = %+v", first)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 100 {
		t.Fatalf("Authorize changed the balance to %d", state.Balance)
	}

	second, err := store.Authorize("acct", "GET /api/v1/users")
	if err != nil {
		t.Fatalf("second Authorize: %v", err)
	}
	if second.RemainingTokens != 0 {
		t.Fatalf("second remaining tokens = %d, want 0", second.RemainingTokens)
	}

	if _, err := store.Authorize("acct", "GET /api/v1/users"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("third Authorize error = %v, want ErrRateLimited", err)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 100 {
		t.Fatalf("rate-limited Authorize changed the balance to %d", state.Balance)
	}

	stats := store.Stats()
	if stats.Requests != 3 || stats.Allowed != 2 || stats.RateLimited != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAuthorizeRejectsMaintenanceAndDisabledRoutes(t *testing.T) {
	store := New(Options{InitialBalance: 10, DefaultRateLimitRPM: 10})
	store.Hydrate([]domain.AccountQuota{{AccountID: "acct", Balance: 10, RateLimitRPM: 10}})
	store.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 10,
		DefaultRequestCost:  1,
		MaintenanceMode:     true,
	})
	if _, err := store.Authorize("acct", "GET /api/v1/users"); !errors.Is(err, domain.ErrRouteDisabled) {
		t.Fatalf("maintenance error = %v, want ErrRouteDisabled", err)
	}

	store.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 10,
		DefaultRequestCost:  1,
		Routes: map[string]domain.RoutePolicy{
			"GET /api/v1/users": {Enabled: false, Cost: 1},
		},
	})
	if _, err := store.Authorize("acct", "GET /api/v1/users"); !errors.Is(err, domain.ErrRouteDisabled) {
		t.Fatalf("disabled route error = %v, want ErrRouteDisabled", err)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 10 {
		t.Fatalf("rejected Authorize changed the balance to %d", state.Balance)
	}
}

func TestDebitMovesAnExplicitAmount(t *testing.T) {
	store := New(Options{InitialBalance: 10, DefaultRateLimitRPM: 10})

	decision, err := store.Debit("acct", 4)
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if !decision.Allowed || decision.Cost != 4 || decision.RemainingBalance != 6 {
		t.Fatalf("decision = %+v", decision)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 6 {
		t.Fatalf("balance = %d, want 6", state.Balance)
	}

	if _, err := store.Debit("acct", 7); !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("overdraft error = %v, want ErrInsufficientBalance", err)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 6 {
		t.Fatalf("failed debit changed balance to %d", state.Balance)
	}

	zero, err := store.Debit("acct", 0)
	if err != nil || !zero.Allowed || zero.RemainingBalance != 6 {
		t.Fatalf("zero debit = %+v, %v", zero, err)
	}
	negative, err := store.Debit("acct", -5)
	if err != nil || !negative.Allowed || negative.Cost != 0 || negative.RemainingBalance != 6 {
		t.Fatalf("negative debit = %+v, %v", negative, err)
	}
}

func TestAuthorizeThenDebitChargesOnce(t *testing.T) {
	store := New(Options{InitialBalance: 10, DefaultRateLimitRPM: 10})
	store.UpdateConfig(&domain.RuntimeConfig{DefaultRateLimitRPM: 10})

	if _, err := store.Authorize("acct", "POST /api/v1/accounts/{account_id}/charge"); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if _, err := store.Debit("acct", 7); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 3 {
		t.Fatalf("balance = %d, want a single debit to 3", state.Balance)
	}
	if _, err := store.Debit("acct", 4); !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("second debit error = %v", err)
	}
}

func TestSnapshotQuotas(t *testing.T) {
	store := New(Options{InitialBalance: 5, DefaultRateLimitRPM: 60})
	store.Hydrate([]domain.AccountQuota{
		{AccountID: "acct_a", Balance: 10, RateLimitRPM: 30},
	})
	store.Credit("acct_b", 7)

	updatedAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	snapshot := store.SnapshotQuotas(updatedAt)
	byAccount := make(map[string]domain.AccountQuota, len(snapshot))
	for _, quota := range snapshot {
		byAccount[quota.AccountID] = quota
	}

	if got := byAccount["acct_a"]; got.Balance != 10 || got.RateLimitRPM != 30 || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("acct_a snapshot = %+v", got)
	}
	if got := byAccount["acct_b"]; got.Balance != 12 || got.RateLimitRPM != 60 {
		t.Fatalf("acct_b snapshot = %+v", got)
	}
}
