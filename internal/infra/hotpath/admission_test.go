package hotpath

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
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
	store.Hydrate([]domain.AccountQuota{{AccountID: "acct", Balance: 10, RateLimitRPM: 10}})

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
	if got := byAccount["acct_b"]; got.Balance != 7 || got.RateLimitRPM != 60 {
		t.Fatalf("acct_b snapshot = %+v", got)
	}
}

func TestDebitUnknownAccountReturnsNotFound(t *testing.T) {
	store := New(Options{InitialBalance: 100, DefaultRateLimitRPM: 10})

	decision, err := store.Debit("ghost", 1)
	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("Debit unknown account error = %v, want port.ErrNotFound", err)
	}
	if decision != (domain.HotPathDecision{}) {
		t.Fatalf("Debit unknown account decision = %+v, want zero", decision)
	}
	if _, ok := store.GetAccountState("ghost"); ok {
		t.Fatal("Debit created an unknown account with a free initial balance")
	}
	if stats := store.Stats(); stats.InsufficientBalance != 0 {
		t.Fatalf("InsufficientBalance = %d, want 0", stats.InsufficientBalance)
	}
}

func TestMaxAccountsLimitsLazyCreation(t *testing.T) {
	store := New(Options{InitialBalance: 10, MaxAccounts: 2})
	store.Hydrate([]domain.AccountQuota{
		{AccountID: "a", Balance: 1, RateLimitRPM: 10},
		{AccountID: "b", Balance: 1, RateLimitRPM: 10},
	})

	if _, err := store.AcquireAndDebit("c", "GET /x"); !errors.Is(err, domain.ErrTooManyAccounts) {
		t.Fatalf("AcquireAndDebit error = %v, want ErrTooManyAccounts", err)
	}
	if _, err := store.Authorize("c", "GET /x"); !errors.Is(err, domain.ErrTooManyAccounts) {
		t.Fatalf("Authorize error = %v, want ErrTooManyAccounts", err)
	}
	if err := store.Credit("c", 5); !errors.Is(err, domain.ErrTooManyAccounts) {
		t.Fatalf("Credit error = %v, want ErrTooManyAccounts", err)
	}
	if _, ok := store.GetAccountState("c"); ok {
		t.Fatal("rejected account was created")
	}

	// Existing accounts remain usable at the limit.
	if _, err := store.Authorize("a", "GET /x"); err != nil {
		t.Fatalf("Authorize existing account: %v", err)
	}
	if err := store.Credit("a", 5); err != nil {
		t.Fatalf("Credit existing account: %v", err)
	}
}

func TestHydrateGrowsMaxAccounts(t *testing.T) {
	store := New(Options{MaxAccounts: 2})
	store.Hydrate([]domain.AccountQuota{
		{AccountID: "a", Balance: 1},
		{AccountID: "b", Balance: 2},
		{AccountID: "c", Balance: 3},
	})

	// Hydration grew the limit to the persisted account count instead of
	// dropping anything. The table is now full, so lazy creation is rejected.
	for _, accountID := range []string{"a", "b", "c"} {
		if _, ok := store.GetAccountState(accountID); !ok {
			t.Fatalf("hydrated account %q missing", accountID)
		}
	}
	if _, err := store.Authorize("a", "GET /x"); err != nil {
		t.Fatalf("Authorize hydrated account: %v", err)
	}
	if _, err := store.Authorize("d", "GET /x"); !errors.Is(err, domain.ErrTooManyAccounts) {
		t.Fatalf("Authorize beyond expanded limit error = %v, want ErrTooManyAccounts", err)
	}
}

func TestInvalidAccountIDsRejected(t *testing.T) {
	store := New(Options{InitialBalance: 10})
	invalid := []string{"", "   ", "bad id", "acct!", "acct/1"}

	for _, accountID := range invalid {
		if _, err := store.AcquireAndDebit(accountID, "GET /x"); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("AcquireAndDebit(%q) error = %v, want ErrValidation", accountID, err)
		}
		if _, err := store.Authorize(accountID, "GET /x"); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("Authorize(%q) error = %v, want ErrValidation", accountID, err)
		}
		if _, err := store.Debit(accountID, 1); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("Debit(%q) error = %v, want ErrValidation", accountID, err)
		}
		if err := store.Credit(accountID, 1); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("Credit(%q) error = %v, want ErrValidation", accountID, err)
		}
	}

	if stats := store.Stats(); stats.Requests != int64(2*len(invalid)) {
		t.Fatalf("Requests = %d, want %d", stats.Requests, 2*len(invalid))
	}
}

func TestRollbackCreditAfterConcurrentDebitGoesNegative(t *testing.T) {
	store := New(Options{InitialBalance: 0, DefaultRateLimitRPM: 1_000_000})
	if err := store.Credit("acct", 100); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if _, err := store.Debit("acct", 30); err != nil {
		t.Fatalf("Debit: %v", err)
	}

	var (
		debited atomic.Int64
		wg      sync.WaitGroup
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				decision, err := store.Debit("acct", 1)
				switch {
				case err == nil:
					debited.Add(decision.Cost)
				case errors.Is(err, domain.ErrInsufficientBalance):
				default:
					t.Errorf("unexpected Debit error: %v", err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		store.RollbackCredit("acct", 100)
	}()
	wg.Wait()

	// Rollback is unconditional: it reverses the full credit even though
	// spending already reduced the balance, so the result can be negative.
	want := int64(100) - 30 - debited.Load() - 100
	state, ok := store.GetAccountState("acct")
	if !ok || state.Balance != want {
		t.Fatalf("balance = %+v, ok=%v; want %d", state, ok, want)
	}
	if state.Balance >= 0 {
		t.Fatalf("balance = %d, want a negative balance", state.Balance)
	}
	if got := store.totalCredited.Load(); got != 0 {
		t.Fatalf("totalCredited = %d, want 0", got)
	}
}

func TestDecisionRateLimitRPM(t *testing.T) {
	clock := newFakeClock(time.Date(2026, time.June, 1, 9, 0, 0, 0, time.UTC))
	store := New(Options{InitialBalance: 1000, DefaultRateLimitRPM: 40, Clock: clock.Now})
	store.Hydrate([]domain.AccountQuota{
		{AccountID: "account", Balance: 1000, RateLimitRPM: 20},
		{AccountID: "unlimited", Balance: 1000, RateLimitRPM: 0},
	})
	store.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 30,
		DefaultRequestCost:  1,
		Routes: map[string]domain.RoutePolicy{
			"GET /route": {Enabled: true, Cost: 1, RateLimitRPM: 1},
		},
	})

	// A route-specific limit wins over the account limit.
	decision, err := store.AcquireAndDebit("account", "GET /route")
	if err != nil || decision.RateLimitRPM != 1 {
		t.Fatalf("route decision = %+v, %v; want RateLimitRPM 1", decision, err)
	}
	decision, err = store.AcquireAndDebit("account", "GET /route")
	if !errors.Is(err, domain.ErrRateLimited) || decision.RateLimitRPM != 1 {
		t.Fatalf("rate-limited decision = %+v, %v; want RateLimitRPM 1", decision, err)
	}

	// A route without its own limit inherits the account limit.
	decision, err = store.AcquireAndDebit("account", "GET /other")
	if err != nil || decision.RateLimitRPM != 20 {
		t.Fatalf("account decision = %+v, %v; want RateLimitRPM 20", decision, err)
	}

	// A fresh account uses the runtime default.
	decision, err = store.AcquireAndDebit("defaulted", "GET /other")
	if err != nil || decision.RateLimitRPM != 30 {
		t.Fatalf("default decision = %+v, %v; want RateLimitRPM 30", decision, err)
	}

	// An explicitly unlimited account reports zero.
	decision, err = store.AcquireAndDebit("unlimited", "GET /other")
	if err != nil || decision.RateLimitRPM != 0 {
		t.Fatalf("unlimited decision = %+v, %v; want RateLimitRPM 0", decision, err)
	}

	// Rejected balance decisions still carry the enforced limit.
	broke := New(Options{InitialBalance: 0, DefaultRateLimitRPM: 12, Clock: clock.Now})
	broke.UpdateConfig(testConfig(12, 5))
	decision, err = broke.AcquireAndDebit("account", "GET /other")
	if !errors.Is(err, domain.ErrInsufficientBalance) || decision.RateLimitRPM != 12 {
		t.Fatalf("insufficient decision = %+v, %v; want RateLimitRPM 12", decision, err)
	}
	decision, err = broke.Authorize("account", "GET /other")
	if err != nil || decision.RateLimitRPM != 12 {
		t.Fatalf("authorize decision = %+v, %v; want RateLimitRPM 12", decision, err)
	}
}
