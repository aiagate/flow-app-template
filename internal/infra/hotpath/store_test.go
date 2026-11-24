package hotpath

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
)

// fakeClock is a race-safe injectable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testConfig(rpm int, cost int64) *domain.RuntimeConfig {
	return &domain.RuntimeConfig{
		DefaultRateLimitRPM: rpm,
		DefaultRequestCost:  cost,
	}
}

func TestNewDefaultsAndInitialConfig(t *testing.T) {
	store := New(Options{})
	if store.CurrentConfig() == nil {
		t.Fatal("CurrentConfig() = nil, want an empty snapshot")
	}
	if _, ok := store.GetAccountState("unknown"); ok {
		t.Fatal("unknown account reported as present")
	}
	if stats := store.Stats(); stats != (domain.HotPathStats{}) {
		t.Fatalf("initial Stats() = %+v, want zero", stats)
	}
}

func TestUpdateConfigSnapshotsAndIsolates(t *testing.T) {
	store := New(Options{InitialBalance: 100, DefaultRateLimitRPM: 60, Clock: newFakeClock(time.Now()).Now})

	first := &domain.RuntimeConfig{
		DefaultRateLimitRPM: 60,
		DefaultRequestCost:  1,
		Routes: map[string]domain.RoutePolicy{
			"GET /users": {Enabled: true, Cost: 1, RateLimitRPM: 60},
		},
	}
	store.UpdateConfig(first)

	snapshot := store.CurrentConfig()
	if snapshot == first {
		t.Fatal("UpdateConfig stored the caller's pointer instead of a clone")
	}
	if snapshot.DefaultRateLimitRPM != 60 || snapshot.DefaultRequestCost != 1 {
		t.Fatalf("snapshot scalars = %+v", snapshot)
	}
	if got := snapshot.Routes["GET /users"]; got.Cost != 1 || !got.Enabled {
		t.Fatalf("snapshot route = %+v", got)
	}

	// Mutating the caller-owned config after the swap must not leak into live
	// readers.
	first.DefaultRateLimitRPM = 1
	first.MaintenanceMode = true
	first.Routes["GET /users"] = domain.RoutePolicy{Enabled: false, Cost: 99}
	first.Routes["GET /new"] = domain.RoutePolicy{Enabled: true, Cost: 7}

	snapshot = store.CurrentConfig()
	if snapshot.DefaultRateLimitRPM != 60 || snapshot.MaintenanceMode {
		t.Fatalf("snapshot changed after caller mutation: %+v", snapshot)
	}
	if route := snapshot.Routes["GET /users"]; !route.Enabled || route.Cost != 1 {
		t.Fatalf("route changed after caller mutation: %+v", route)
	}
	if _, ok := snapshot.Routes["GET /new"]; ok {
		t.Fatal("new caller route leaked into the live snapshot")
	}

	// A second update swaps the whole snapshot atomically.
	second := &domain.RuntimeConfig{DefaultRateLimitRPM: 7, DefaultRequestCost: 3}
	store.UpdateConfig(second)
	if got := store.CurrentConfig(); got.DefaultRateLimitRPM != 7 || got.DefaultRequestCost != 3 {
		t.Fatalf("second snapshot = %+v", got)
	}

	// A nil update is a no-op rather than clearing the live configuration.
	store.UpdateConfig(nil)
	if got := store.CurrentConfig(); got.DefaultRateLimitRPM != 7 {
		t.Fatalf("nil UpdateConfig changed snapshot: %+v", got)
	}
}

func TestConfigSwapIsAtomicForConcurrentReaders(t *testing.T) {
	store := New(Options{DefaultRateLimitRPM: 1})
	store.UpdateConfig(testConfig(1, 1))

	const readers = 20
	stop := make(chan struct{})
	var (
		wg  sync.WaitGroup
		bad atomic.Int64
	)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				config := store.CurrentConfig()
				if config == nil {
					bad.Add(1)
					continue
				}
				if rpm := config.DefaultRateLimitRPM; rpm != 1 && rpm != 2 {
					bad.Add(1)
				}
				if cost := config.DefaultRequestCost; cost != 1 && cost != 2 {
					bad.Add(1)
				}
			}
		}()
	}

	for i := 0; i < 2000; i++ {
		rpm := 1
		cost := int64(1)
		if i%2 == 1 {
			rpm = 2
			cost = 2
		}
		store.UpdateConfig(testConfig(rpm, cost))
	}
	close(stop)
	wg.Wait()

	if got := bad.Load(); got != 0 {
		t.Fatalf("observed %d torn or nil config snapshots", got)
	}
}

func TestRouteDisabledAndMaintenanceMode(t *testing.T) {
	store := New(Options{InitialBalance: 100, DefaultRateLimitRPM: 100})
	store.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 100,
		DefaultRequestCost:  1,
		Routes: map[string]domain.RoutePolicy{
			"GET /disabled": {Enabled: false, Cost: 1, RateLimitRPM: 100},
			"GET /enabled":  {Enabled: true, Cost: 1, RateLimitRPM: 100},
		},
	})

	if _, err := store.AcquireAndDebit("acct", "GET /disabled"); !errors.Is(err, domain.ErrRouteDisabled) {
		t.Fatalf("disabled route error = %v, want ErrRouteDisabled", err)
	}
	if stats := store.Stats(); stats.RouteDisabled != 1 || stats.MaintenanceRejected != 0 {
		t.Fatalf("stats after disabled route = %+v", stats)
	}
	if _, ok := store.GetAccountState("acct"); ok {
		t.Fatal("rejected request created account state")
	}

	if _, err := store.AcquireAndDebit("acct", "GET /enabled"); err != nil {
		t.Fatalf("enabled route: %v", err)
	}

	enabled := store.CurrentConfig().Clone()
	enabled.MaintenanceMode = true
	store.UpdateConfig(enabled)

	if _, err := store.AcquireAndDebit("acct", "GET /enabled"); !errors.Is(err, domain.ErrRouteDisabled) {
		t.Fatalf("maintenance error = %v, want ErrRouteDisabled", err)
	}
	// Maintenance is global and is checked before route policy.
	if _, err := store.AcquireAndDebit("acct", "GET /disabled"); !errors.Is(err, domain.ErrRouteDisabled) {
		t.Fatalf("maintenance disabled route error = %v, want ErrRouteDisabled", err)
	}
	stats := store.Stats()
	if stats.MaintenanceRejected != 2 {
		t.Fatalf("MaintenanceRejected = %d, want 2", stats.MaintenanceRejected)
	}
	if stats.RouteDisabled != 1 {
		t.Fatalf("RouteDisabled = %d, want 1 (maintenance must not increment it)", stats.RouteDisabled)
	}
	if stats.Allowed != 1 {
		t.Fatalf("Allowed = %d, want 1", stats.Allowed)
	}

	enabled = store.CurrentConfig().Clone()
	enabled.MaintenanceMode = false
	enabled.Routes["GET /disabled"] = domain.RoutePolicy{Enabled: true, Cost: 2, RateLimitRPM: 100}
	store.UpdateConfig(enabled)
	decision, err := store.AcquireAndDebit("acct", "GET /disabled")
	if err != nil {
		t.Fatalf("re-enabled route: %v", err)
	}
	if decision.Cost != 2 {
		t.Fatalf("re-enabled route cost = %d, want 2", decision.Cost)
	}
}

func TestRateLimitWindowAndReset(t *testing.T) {
	start := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	store := New(Options{InitialBalance: 100, Clock: clock.Now})
	store.UpdateConfig(testConfig(2, 1))

	first, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if !first.Allowed || first.RemainingTokens != 1 || first.RemainingBalance != 99 {
		t.Fatalf("first decision = %+v", first)
	}

	second, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if second.RemainingTokens != 0 || second.RemainingBalance != 98 {
		t.Fatalf("second decision = %+v", second)
	}

	third, err := store.AcquireAndDebit("acct", "GET /x")
	if !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("third request error = %v, want ErrRateLimited", err)
	}
	if third.Allowed || third.RemainingBalance != 98 || third.RemainingTokens != 0 {
		t.Fatalf("rate-limited decision = %+v", third)
	}

	// Requests for another account are independent of acct's window.
	if _, err := store.AcquireAndDebit("other", "GET /x"); err != nil {
		t.Fatalf("other account request: %v", err)
	}

	clock.Advance(30 * time.Second)
	if _, err := store.AcquireAndDebit("acct", "GET /x"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("request after 30s error = %v, want ErrRateLimited", err)
	}

	clock.Advance(31 * time.Second)
	afterReset, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("request after window reset: %v", err)
	}
	if afterReset.RemainingTokens != 1 || afterReset.RemainingBalance != 97 {
		t.Fatalf("after-reset decision = %+v", afterReset)
	}

	stats := store.Stats()
	if stats.Requests != 6 || stats.Allowed != 4 || stats.RateLimited != 2 {
		t.Fatalf("Stats() = %+v, want requests=6 allowed=4 rateLimited=2", stats)
	}
	if stats.InsufficientBalance != 0 {
		t.Fatalf("InsufficientBalance = %d, want 0", stats.InsufficientBalance)
	}
}

func TestRouteAndAccountRateLimitPrecedence(t *testing.T) {
	clock := newFakeClock(time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC))
	store := New(Options{InitialBalance: 1000, DefaultRateLimitRPM: 50, Clock: clock.Now})
	store.Hydrate([]domain.AccountQuota{
		{AccountID: "acct", Balance: 1000, RateLimitRPM: 2},
	})
	store.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 100,
		DefaultRequestCost:  1,
		Routes: map[string]domain.RoutePolicy{
			"GET /route-limited": {Enabled: true, Cost: 3, RateLimitRPM: 1},
			"GET /inherits":      {Enabled: true, Cost: 1},
		},
	})

	// Route policy wins over the account-specific rate limit.
	decision, err := store.AcquireAndDebit("acct", "GET /route-limited")
	if err != nil {
		t.Fatalf("first route-limited request: %v", err)
	}
	if decision.Cost != 3 || decision.RemainingTokens != 0 {
		t.Fatalf("route-limited decision = %+v", decision)
	}
	if _, err := store.AcquireAndDebit("acct", "GET /route-limited"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("second route-limited request error = %v, want ErrRateLimited", err)
	}

	// A route without its own RPM inherits the account-specific value (2),
	// not the runtime default (100). The window is account-wide, so the
	// route-limited request above already consumed one of the two tokens and
	// this request is the last allowed one.
	decision, err = store.AcquireAndDebit("acct", "GET /inherits")
	if err != nil {
		t.Fatalf("inherited-rate request: %v", err)
	}
	if decision.RemainingTokens != 0 {
		t.Fatalf("inherited decision = %+v, want 0 remaining account tokens", decision)
	}
	if _, err := store.AcquireAndDebit("acct", "GET /inherits"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("third account request error = %v, want ErrRateLimited", err)
	}

	// An account without an explicit limit uses the runtime default.
	for i := 0; i < 100; i++ {
		if _, err := store.AcquireAndDebit("lazy", "GET /inherits"); err != nil {
			t.Fatalf("lazy request %d: %v", i, err)
		}
	}
	if _, err := store.AcquireAndDebit("lazy", "GET /inherits"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("lazy 101st request error = %v, want ErrRateLimited", err)
	}
}

func TestInsufficientBalanceDoesNotDebit(t *testing.T) {
	store := New(Options{InitialBalance: 5, DefaultRateLimitRPM: 10})
	store.UpdateConfig(testConfig(10, 2))

	first, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if first.RemainingBalance != 3 {
		t.Fatalf("first RemainingBalance = %d, want 3", first.RemainingBalance)
	}
	second, err := store.AcquireAndDebit("acct", "GET /x")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if second.RemainingBalance != 1 {
		t.Fatalf("second RemainingBalance = %d, want 1", second.RemainingBalance)
	}

	third, err := store.AcquireAndDebit("acct", "GET /x")
	if !errors.Is(err, domain.ErrInsufficientBalance) {
		t.Fatalf("third request error = %v, want ErrInsufficientBalance", err)
	}
	if third.Allowed || third.RemainingBalance != 1 || third.Cost != 2 {
		t.Fatalf("insufficient decision = %+v", third)
	}
	// The rejected request consumed the third rate-limit slot even though the
	// balance was not debited.
	if third.RemainingTokens != 7 {
		t.Fatalf("RemainingTokens = %d, want 7", third.RemainingTokens)
	}

	state, ok := store.GetAccountState("acct")
	if !ok || state.Balance != 1 {
		t.Fatalf("account state = %+v, ok=%v; want balance 1", state, ok)
	}
	stats := store.Stats()
	if stats.Requests != 3 || stats.Allowed != 2 || stats.InsufficientBalance != 1 {
		t.Fatalf("Stats() = %+v", stats)
	}
	if stats.RouteDisabled != 0 || stats.RateLimited != 0 {
		t.Fatalf("unexpected rejection counters: %+v", stats)
	}
	if got := store.totalDebited.Load(); got != 4 {
		t.Fatalf("totalDebited = %d, want 4", got)
	}
}

func TestCreditAndRefund(t *testing.T) {
	store := New(Options{InitialBalance: 10, DefaultRateLimitRPM: 100})
	store.UpdateConfig(testConfig(100, 1))

	if _, ok := store.GetAccountState("acct"); ok {
		t.Fatal("account exists before first operation")
	}

	store.Credit("acct", 5)
	state, ok := store.GetAccountState("acct")
	if !ok || state.Balance != 15 || state.RateLimitRPM != 100 {
		t.Fatalf("state after credit = %+v, ok=%v", state, ok)
	}
	store.Refund("acct", 3)
	state, _ = store.GetAccountState("acct")
	if state.Balance != 18 {
		t.Fatalf("balance after refund = %d, want 18", state.Balance)
	}

	// Non-positive movements are ignored.
	store.Credit("acct", 0)
	store.Credit("acct", -4)
	store.Refund("acct", -1)
	state, _ = store.GetAccountState("acct")
	if state.Balance != 18 {
		t.Fatalf("balance after no-op movements = %d, want 18", state.Balance)
	}

	// Credit lazily creates an unknown account from the configured defaults.
	store.Credit("new", 7)
	state, ok = store.GetAccountState("new")
	if !ok || state.Balance != 17 || state.RateLimitRPM != 100 {
		t.Fatalf("lazy credited account = %+v, ok=%v", state, ok)
	}

	if got := store.totalCredited.Load(); got != 12 {
		t.Fatalf("totalCredited = %d, want 12", got)
	}
	if got := store.totalRefunded.Load(); got != 3 {
		t.Fatalf("totalRefunded = %d, want 3", got)
	}
}

func TestHydrateAndLazyDefaults(t *testing.T) {
	clock := newFakeClock(time.Date(2026, time.April, 2, 8, 0, 0, 0, time.UTC))
	store := New(Options{InitialBalance: 10, DefaultRateLimitRPM: 3, Clock: clock.Now})
	store.Hydrate([]domain.AccountQuota{
		{AccountID: "hydrated", Balance: 50, RateLimitRPM: 5},
		{AccountID: "unlimited", Balance: 20, RateLimitRPM: 0},
		{AccountID: "", Balance: 999, RateLimitRPM: 1},
	})

	state, ok := store.GetAccountState("hydrated")
	if !ok || state.Balance != 50 || state.RateLimitRPM != 5 {
		t.Fatalf("hydrated state = %+v, ok=%v", state, ok)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.AcquireAndDebit("hydrated", "GET /x"); err != nil {
			t.Fatalf("hydrated request %d: %v", i, err)
		}
	}
	if _, err := store.AcquireAndDebit("hydrated", "GET /x"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("hydrated 6th request error = %v, want ErrRateLimited", err)
	}

	// An explicitly hydrated zero RPM means unlimited even though the store
	// default and runtime default are positive.
	for i := 0; i < 10; i++ {
		if _, err := store.AcquireAndDebit("unlimited", "GET /x"); err != nil {
			t.Fatalf("unlimited request %d: %v", i, err)
		}
	}

	// Unknown accounts get the lazy defaults.
	for i := 0; i < 3; i++ {
		if _, err := store.AcquireAndDebit("lazy", "GET /x"); err != nil {
			t.Fatalf("lazy request %d: %v", i, err)
		}
	}
	if _, err := store.AcquireAndDebit("lazy", "GET /x"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("lazy 4th request error = %v, want ErrRateLimited", err)
	}
	state, ok = store.GetAccountState("lazy")
	if !ok || state.Balance != 10 || state.RateLimitRPM != 3 {
		t.Fatalf("lazy state = %+v, ok=%v", state, ok)
	}

	// Changing the runtime default affects accounts that never had an explicit
	// limit, while hydrated accounts keep their own limit.
	store.UpdateConfig(testConfig(2, 0))
	state, _ = store.GetAccountState("lazy")
	if state.RateLimitRPM != 2 {
		t.Fatalf("lazy state RPM after config update = %d, want 2", state.RateLimitRPM)
	}
	clock.Advance(61 * time.Second)
	for i := 0; i < 2; i++ {
		if _, err := store.AcquireAndDebit("lazy", "GET /x"); err != nil {
			t.Fatalf("lazy runtime-default request %d: %v", i, err)
		}
	}
	if _, err := store.AcquireAndDebit("lazy", "GET /x"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("lazy runtime-default 3rd request error = %v, want ErrRateLimited", err)
	}

	// Re-hydrating replaces the stored values and resets the window.
	store.Hydrate([]domain.AccountQuota{{AccountID: "lazy", Balance: 77, RateLimitRPM: 9}})
	state, _ = store.GetAccountState("lazy")
	if state.Balance != 77 || state.RateLimitRPM != 9 {
		t.Fatalf("rehydrated state = %+v", state)
	}
}

func TestConcurrentAccountsConfigAndConservation(t *testing.T) {
	const (
		accountCount = 4
		producers    = 25
		attempts     = 200
		initial      = int64(1_000_000)
	)
	accounts := make([]string, accountCount)
	for i := range accounts {
		accounts[i] = fmt.Sprintf("acct_%d", i)
	}

	start := time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)
	store := New(Options{InitialBalance: initial, DefaultRateLimitRPM: 1_000_000, Clock: newFakeClock(start).Now})
	store.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 1_000_000,
		DefaultRequestCost:  1,
	})

	var (
		wg         sync.WaitGroup
		unexpected atomic.Int64
	)
	routeNames := []string{"GET /a", "GET /b", "GET /c"}

	// A concurrent writer continuously swaps immutable snapshots. Every
	// snapshot keeps cost and admission behavior identical so the final
	// balance is exactly predictable.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			store.UpdateConfig(&domain.RuntimeConfig{
				DefaultRateLimitRPM: 1_000_000,
				DefaultRequestCost:  1,
				Routes: map[string]domain.RoutePolicy{
					routeNames[i%len(routeNames)]: {Enabled: true, Cost: 1, RateLimitRPM: 1_000_000},
				},
			})
		}
	}()

	for producer := 0; producer < producers*accountCount; producer++ {
		account := accounts[producer%accountCount]
		wg.Add(1)
		go func(account string, offset int) {
			defer wg.Done()
			for i := 0; i < attempts; i++ {
				route := routeNames[(offset+i)%len(routeNames)]
				decision, err := store.AcquireAndDebit(account, route)
				if err != nil {
					unexpected.Add(1)
					continue
				}
				if !decision.Allowed || decision.Cost != 1 {
					unexpected.Add(1)
				}
			}
		}(account, producer)
	}

	wg.Wait()

	if got := unexpected.Load(); got != 0 {
		t.Fatalf("unexpected admission failures = %d, want 0", got)
	}

	perAccount := int64(producers * attempts)
	for _, account := range accounts {
		state, ok := store.GetAccountState(account)
		if !ok {
			t.Fatalf("account %s missing", account)
		}
		want := initial - perAccount
		if state.Balance != want {
			t.Fatalf("account %s balance = %d, want %d", account, state.Balance, want)
		}
	}

	wantTotal := int64(producers * accountCount * attempts)
	stats := store.Stats()
	if stats.Requests != wantTotal || stats.Allowed != wantTotal {
		t.Fatalf("Stats() = %+v, want requests=allowed=%d", stats, wantTotal)
	}
	if stats.RateLimited != 0 || stats.InsufficientBalance != 0 || stats.RouteDisabled != 0 {
		t.Fatalf("unexpected rejection stats: %+v", stats)
	}
	if got := store.totalDebited.Load(); got != wantTotal {
		t.Fatalf("totalDebited = %d, want %d", got, wantTotal)
	}
}

func TestRefundConcurrentConservation(t *testing.T) {
	const (
		initial = int64(1000)
		credits = int64(37)
		workers = 20
	)
	store := New(Options{InitialBalance: initial, DefaultRateLimitRPM: 1_000_000})
	store.UpdateConfig(testConfig(1_000_000, 0))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := int64(0); j < credits; j++ {
				store.Credit("acct", 2)
				store.Refund("acct", 1)
			}
		}()
	}
	wg.Wait()

	want := initial + int64(workers)*credits*3
	state, ok := store.GetAccountState("acct")
	if !ok || state.Balance != want {
		t.Fatalf("balance = %+v, ok=%v; want %d", state, ok, want)
	}
	if got := store.totalCredited.Load(); got != int64(workers)*credits*2 {
		t.Fatalf("totalCredited = %d", got)
	}
	if got := store.totalRefunded.Load(); got != int64(workers)*credits {
		t.Fatalf("totalRefunded = %d", got)
	}
}
