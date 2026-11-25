package hotpath

import (
	"errors"
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

	decision, err := store.Debit("acct", 4, nil)
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if !decision.Allowed || decision.Cost != 4 || decision.RemainingBalance != 6 {
		t.Fatalf("decision = %+v", decision)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 6 {
		t.Fatalf("balance = %d, want 6", state.Balance)
	}

	if _, err := store.Debit("acct", 7, nil); !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("overdraft error = %v, want ErrInsufficientBalance", err)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 6 {
		t.Fatalf("failed debit changed balance to %d", state.Balance)
	}

	zero, err := store.Debit("acct", 0, nil)
	if err != nil || !zero.Allowed || zero.RemainingBalance != 6 {
		t.Fatalf("zero debit = %+v, %v", zero, err)
	}
	negative, err := store.Debit("acct", -5, nil)
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
	if _, err := store.Debit("acct", 7, nil); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if state, _ := store.GetAccountState("acct"); state.Balance != 3 {
		t.Fatalf("balance = %d, want a single debit to 3", state.Balance)
	}
	if _, err := store.Debit("acct", 4, nil); !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("second debit error = %v", err)
	}
}

func TestSnapshotQuotas(t *testing.T) {
	store := New(Options{InitialBalance: 5, DefaultRateLimitRPM: 60})
	store.Hydrate([]domain.AccountQuota{
		{AccountID: "acct_a", Balance: 10, RateLimitRPM: 30},
	})
	store.Credit("acct_b", 7, nil)

	updatedAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	snapshot := store.SnapshotQuotas(updatedAt)
	byAccount := make(map[string]domain.AccountQuota, len(snapshot))
	for _, quota := range snapshot {
		byAccount[quota.AccountID] = quota
	}

	if got := byAccount["acct_a"]; got.Balance != 10 || got.RateLimitRPM != 30 || got.Seq != 0 || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("acct_a snapshot = %+v", got)
	}
	if got := byAccount["acct_b"]; got.Balance != 7 || got.RateLimitRPM != 60 || got.Seq != 1 {
		t.Fatalf("acct_b snapshot = %+v", got)
	}
}

func TestDebitUnknownAccountReturnsNotFound(t *testing.T) {
	store := New(Options{InitialBalance: 100, DefaultRateLimitRPM: 10})

	decision, err := store.Debit("ghost", 1, nil)
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
	if _, err := store.Credit("c", 5, nil); !errors.Is(err, domain.ErrTooManyAccounts) {
		t.Fatalf("Credit error = %v, want ErrTooManyAccounts", err)
	}
	if _, ok := store.GetAccountState("c"); ok {
		t.Fatal("rejected account was created")
	}

	// Existing accounts remain usable at the limit.
	if _, err := store.Authorize("a", "GET /x"); err != nil {
		t.Fatalf("Authorize existing account: %v", err)
	}
	if _, err := store.Credit("a", 5, nil); err != nil {
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
		if _, err := store.Debit(accountID, 1, nil); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("Debit(%q) error = %v, want ErrValidation", accountID, err)
		}
		if _, err := store.Credit(accountID, 1, nil); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("Credit(%q) error = %v, want ErrValidation", accountID, err)
		}
	}

	if stats := store.Stats(); stats.Requests != int64(2*len(invalid)) {
		t.Fatalf("Requests = %d, want %d", stats.Requests, 2*len(invalid))
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

	// A route-specific limit wins for enforcement, but the account's own limit
	// is still reported for quota persistence.
	decision, err := store.AcquireAndDebit("account", "GET /route")
	if err != nil || decision.RateLimitRPM != 1 || decision.AccountRateLimitRPM != 20 {
		t.Fatalf("route decision = %+v, %v; want RateLimitRPM 1 and AccountRateLimitRPM 20", decision, err)
	}
	decision, err = store.AcquireAndDebit("account", "GET /route")
	if !errors.Is(err, domain.ErrRateLimited) || decision.RateLimitRPM != 1 || decision.AccountRateLimitRPM != 20 {
		t.Fatalf("rate-limited decision = %+v, %v; want RateLimitRPM 1 and AccountRateLimitRPM 20", decision, err)
	}

	// A route without its own limit inherits the account limit.
	decision, err = store.AcquireAndDebit("account", "GET /other")
	if err != nil || decision.RateLimitRPM != 20 || decision.AccountRateLimitRPM != 20 {
		t.Fatalf("account decision = %+v, %v; want RateLimitRPM 20 and AccountRateLimitRPM 20", decision, err)
	}

	// A fresh account uses the runtime default.
	decision, err = store.AcquireAndDebit("defaulted", "GET /other")
	if err != nil || decision.RateLimitRPM != 30 || decision.AccountRateLimitRPM != 30 {
		t.Fatalf("default decision = %+v, %v; want RateLimitRPM 30 and AccountRateLimitRPM 30", decision, err)
	}

	// An explicitly unlimited account reports zero.
	decision, err = store.AcquireAndDebit("unlimited", "GET /other")
	if err != nil || decision.RateLimitRPM != 0 || decision.AccountRateLimitRPM != 0 {
		t.Fatalf("unlimited decision = %+v, %v; want RateLimitRPM 0 and AccountRateLimitRPM 0", decision, err)
	}

	// A route override never leaks into the account-level limit reported by
	// Debit, which does not apply route policy at all.
	decision, err = store.Debit("account", 1, nil)
	if err != nil || decision.RateLimitRPM != 20 || decision.AccountRateLimitRPM != 20 {
		t.Fatalf("debit decision = %+v, %v; want RateLimitRPM 20 and AccountRateLimitRPM 20", decision, err)
	}

	// Rejected balance decisions still carry the enforced limit.
	broke := New(Options{InitialBalance: 0, DefaultRateLimitRPM: 12, Clock: clock.Now})
	broke.UpdateConfig(testConfig(12, 5))
	decision, err = broke.AcquireAndDebit("account", "GET /other")
	if !errors.Is(err, domain.ErrInsufficientBalance) || decision.RateLimitRPM != 12 || decision.AccountRateLimitRPM != 12 {
		t.Fatalf("insufficient decision = %+v, %v; want RateLimitRPM 12 and AccountRateLimitRPM 12", decision, err)
	}
	decision, err = broke.Authorize("account", "GET /other")
	if err != nil || decision.RateLimitRPM != 12 || decision.AccountRateLimitRPM != 12 {
		t.Fatalf("authorize decision = %+v, %v; want RateLimitRPM 12 and AccountRateLimitRPM 12", decision, err)
	}
}

func TestSeqAdvancesOnlyOnBalanceChanges(t *testing.T) {
	clock := newFakeClock(time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC))
	store := New(Options{InitialBalance: 100, DefaultRateLimitRPM: 10, Clock: clock.Now})
	store.Hydrate([]domain.AccountQuota{{AccountID: "acct", Balance: 100, RateLimitRPM: 10}})
	store.UpdateConfig(&domain.RuntimeConfig{DefaultRateLimitRPM: 10, DefaultRequestCost: 2})

	state, ok := store.GetAccountState("acct")
	if !ok || state.Seq != 0 {
		t.Fatalf("hydrated state = %+v, ok=%v; want Seq 0", state, ok)
	}

	// Authorize consumes a rate-limit slot but never moves balance or Seq.
	authorized, err := store.Authorize("acct", "GET /x")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if authorized.Seq != 0 {
		t.Fatalf("Authorize Seq = %d, want 0", authorized.Seq)
	}

	debited, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("AcquireAndDebit: %v", err)
	}
	if debited.Seq != 1 || debited.RemainingBalance != 98 {
		t.Fatalf("AcquireAndDebit decision = %+v, want Seq 1 and balance 98", debited)
	}

	// A zero-cost admission changes no balance and therefore no Seq.
	store.UpdateConfig(&domain.RuntimeConfig{DefaultRateLimitRPM: 10, DefaultRequestCost: 0})
	free, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("zero-cost AcquireAndDebit: %v", err)
	}
	if free.Seq != 1 {
		t.Fatalf("zero-cost AcquireAndDebit Seq = %d, want 1", free.Seq)
	}

	explicit, err := store.Debit("acct", 5, nil)
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if explicit.Seq != 2 || explicit.RemainingBalance != 93 {
		t.Fatalf("Debit decision = %+v, want Seq 2 and balance 93", explicit)
	}

	// A zero debit is a no-op for Seq as well.
	zero, err := store.Debit("acct", 0, nil)
	if err != nil || zero.Seq != 2 {
		t.Fatalf("zero Debit = %+v, %v; want Seq 2", zero, err)
	}

	store.Refund("acct", 1, nil)
	state, _ = store.GetAccountState("acct")
	if state.Seq != 3 || state.Balance != 94 {
		t.Fatalf("state after Refund = %+v, want Seq 3 and balance 94", state)
	}

	creditState, err := store.Credit("acct", 6, nil)
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if creditState.Seq != 4 || creditState.Balance != 100 {
		t.Fatalf("Credit state = %+v, want Seq 4 and balance 100", creditState)
	}

	// Non-positive credits do not move Seq and do not invoke onCommit.
	unchanged, err := store.Credit("acct", 0, func(domain.AccountState) {
		t.Error("onCommit invoked for a non-positive credit")
	})
	if err != nil || unchanged.Seq != 4 || unchanged.Balance != 100 {
		t.Fatalf("zero Credit state = %+v, %v", unchanged, err)
	}
}

func TestHydratePreservesMonotonicSeq(t *testing.T) {
	store := New(Options{InitialBalance: 10, DefaultRateLimitRPM: 10})

	store.Hydrate([]domain.AccountQuota{{AccountID: "acct", Balance: 50, RateLimitRPM: 10, Seq: 5}})
	state, _ := store.GetAccountState("acct")
	if state.Seq != 5 {
		t.Fatalf("Seq after first hydrate = %d, want 5", state.Seq)
	}

	if _, err := store.Credit("acct", 1, nil); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	state, _ = store.GetAccountState("acct")
	if state.Seq != 6 {
		t.Fatalf("Seq after credit = %d, want 6", state.Seq)
	}

	// An older persisted snapshot replaces the balance but must never rewind
	// the in-memory sequence.
	store.Hydrate([]domain.AccountQuota{{AccountID: "acct", Balance: 60, RateLimitRPM: 10, Seq: 3}})
	state, _ = store.GetAccountState("acct")
	if state.Seq != 6 || state.Balance != 60 {
		t.Fatalf("state after stale hydrate = %+v, want Seq 6 and balance 60", state)
	}

	// A newer snapshot advances the sequence.
	store.Hydrate([]domain.AccountQuota{{AccountID: "acct", Balance: 70, RateLimitRPM: 10, Seq: 9}})
	state, _ = store.GetAccountState("acct")
	if state.Seq != 9 || state.Balance != 70 {
		t.Fatalf("state after newer hydrate = %+v, want Seq 9 and balance 70", state)
	}

	// Negative persisted sequences are clamped to zero on creation.
	store.Hydrate([]domain.AccountQuota{{AccountID: "clamped", Balance: 1, Seq: -4}})
	state, _ = store.GetAccountState("clamped")
	if state.Seq != 0 {
		t.Fatalf("clamped Seq = %d, want 0", state.Seq)
	}

	// A debit continues from the hydrated sequence.
	store.UpdateConfig(&domain.RuntimeConfig{DefaultRateLimitRPM: 10, DefaultRequestCost: 1})
	decision, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("AcquireAndDebit: %v", err)
	}
	if decision.Seq != 10 || decision.RemainingBalance != 69 {
		t.Fatalf("decision = %+v, want Seq 10 and balance 69", decision)
	}
}

func TestCreditOnCommitHoldsAccountLock(t *testing.T) {
	store := New(Options{InitialBalance: 0, DefaultRateLimitRPM: 10})
	store.Hydrate([]domain.AccountQuota{{AccountID: "acct", Balance: 100, RateLimitRPM: 10}})

	var (
		entered  = make(chan struct{})
		release  = make(chan struct{})
		creditOK = make(chan error, 1)
	)
	var committed domain.AccountState
	go func() {
		_, err := store.Credit("acct", 10, func(state domain.AccountState) {
			committed = state
			close(entered)
			<-release
		})
		creditOK <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Credit did not invoke onCommit")
	}

	debitDone := make(chan struct{})
	go func() {
		defer close(debitDone)
		if _, err := store.Debit("acct", 50, nil); err != nil {
			t.Errorf("Debit: %v", err)
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		store.GetAccountState("acct")
	}()

	select {
	case <-debitDone:
		t.Fatal("Debit completed while Credit onCommit still held the account lock")
	case <-readDone:
		t.Fatal("GetAccountState completed while Credit onCommit still held the account lock")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	if err := <-creditOK; err != nil {
		t.Fatalf("Credit: %v", err)
	}
	<-debitDone
	<-readDone

	if committed.Balance != 110 || committed.Seq != 1 || committed.AccountID != "acct" {
		t.Fatalf("committed state = %+v, want balance 110 and Seq 1", committed)
	}
	state, _ := store.GetAccountState("acct")
	if state.Balance != 60 || state.Seq != 2 {
		t.Fatalf("final state = %+v, want balance 60 and Seq 2", state)
	}
}

func TestCreditNewAccountIsInvisibleUntilOnCommitFinishes(t *testing.T) {
	store := New(Options{DefaultRateLimitRPM: 10})

	var (
		entered  = make(chan struct{})
		release  = make(chan struct{})
		creditOK = make(chan error, 1)
	)
	var committed domain.AccountState
	go func() {
		_, err := store.Credit("brand_new", 25, func(state domain.AccountState) {
			committed = state
			close(entered)
			<-release
		})
		creditOK <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Credit did not invoke onCommit for a new account")
	}

	// The bucket is already in the map, but its mutex is held across onCommit,
	// so a concurrent debit or reader cannot observe the new balance yet.
	debitDone := make(chan struct{})
	go func() {
		defer close(debitDone)
		if _, err := store.Debit("brand_new", 5, nil); err != nil {
			t.Errorf("Debit: %v", err)
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		store.GetAccountState("brand_new")
	}()

	select {
	case <-debitDone:
		t.Fatal("Debit completed before the new account's onCommit finished")
	case <-readDone:
		t.Fatal("GetAccountState completed before the new account's onCommit finished")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	if err := <-creditOK; err != nil {
		t.Fatalf("Credit: %v", err)
	}
	<-debitDone
	<-readDone

	if committed.Balance != 25 || committed.Seq != 1 {
		t.Fatalf("committed state = %+v, want balance 25 and Seq 1", committed)
	}
	state, ok := store.GetAccountState("brand_new")
	if !ok || state.Balance != 20 || state.Seq != 2 {
		t.Fatalf("final state = %+v, ok=%v; want balance 20 and Seq 2", state, ok)
	}
}
