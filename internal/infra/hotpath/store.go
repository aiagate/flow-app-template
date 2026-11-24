// Package hotpath is the in-memory admission and quota adapter. Every method
// completes without disk or network I/O: configuration is an immutable atomic
// snapshot and account state is protected by per-account mutexes.
package hotpath

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

const (
	// rateLimitWindow is the fixed admission window per account.
	rateLimitWindow = time.Minute
	// defaultMaxAccounts bounds the number of lazily created accounts so an
	// unbounded stream of unknown IDs cannot exhaust memory.
	defaultMaxAccounts = 4096
)

// Options configures a Store. Zero values are valid: an unknown account starts
// with a zero balance and an unlimited rate limit until Hydrate or UpdateConfig
// supplies runtime values.
type Options struct {
	InitialBalance      int64
	DefaultRateLimitRPM int
	// MaxAccounts bounds lazily created accounts. Values <= 0 use
	// defaultMaxAccounts.
	MaxAccounts int
	// Clock is injectable for deterministic window tests. Nil means time.Now.
	Clock func() time.Time
}

// accountBucket is the mutable state for one account. The map lock is never
// held while this mutex is held, so operations on different accounts proceed
// independently.
type accountBucket struct {
	mu sync.Mutex

	balance      int64
	rateLimitRPM int
	// rateLimitSet distinguishes an account-specific limit loaded by Hydrate
	// from the Store default. When false, RuntimeConfig.DefaultRateLimitRPM
	// still applies, so changing the runtime default affects lazy accounts.
	rateLimitSet bool

	windowStart time.Time
	windowCount int64
}

// Store implements port.HotPathGuard.
type Store struct {
	config atomic.Pointer[domain.RuntimeConfig]

	mu       sync.RWMutex
	accounts map[string]*accountBucket

	initialBalance      int64
	defaultRateLimitRPM int
	maxAccounts         int
	clock               func() time.Time

	totalRequests       atomic.Int64
	allowed             atomic.Int64
	rateLimited         atomic.Int64
	insufficientBalance atomic.Int64
	routeDisabled       atomic.Int64
	maintenanceRejected atomic.Int64
	totalDebited        atomic.Int64
	totalCredited       atomic.Int64
	totalRefunded       atomic.Int64
}

var _ port.HotPathGuard = (*Store)(nil)

// New returns a started in-memory guard. The initial runtime snapshot is empty
// and can be replaced with UpdateConfig before serving requests.
func New(opts Options) *Store {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	maxAccounts := opts.MaxAccounts
	if maxAccounts <= 0 {
		maxAccounts = defaultMaxAccounts
	}
	store := &Store{
		accounts:            make(map[string]*accountBucket),
		initialBalance:      opts.InitialBalance,
		defaultRateLimitRPM: opts.DefaultRateLimitRPM,
		maxAccounts:         maxAccounts,
		clock:               clock,
	}
	store.config.Store(&domain.RuntimeConfig{DefaultRateLimitRPM: opts.DefaultRateLimitRPM})
	return store
}

// CurrentConfig returns the current immutable snapshot. Callers must treat the
// returned value, including its Routes map, as read-only.
func (s *Store) CurrentConfig() *domain.RuntimeConfig {
	return s.config.Load()
}

// UpdateConfig clones the supplied configuration before swapping it in so the
// caller cannot mutate the live snapshot through shared map storage.
func (s *Store) UpdateConfig(config *domain.RuntimeConfig) {
	if config == nil {
		return
	}
	s.config.Swap(config.Clone())
}

// Hydrate loads persisted quotas at startup. A later Hydrate replaces the
// balance and rate limit and resets that account's current window. Invalid
// account IDs are skipped. Hydration never drops a persisted account: when the
// persisted set exceeds MaxAccounts, the limit grows to fit it.
func (s *Store) Hydrate(quotas []domain.AccountQuota) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, quota := range quotas {
		accountID, err := domain.ValidateAccountID(quota.AccountID)
		if err != nil {
			continue
		}
		bucket := s.accounts[accountID]
		if bucket == nil {
			bucket = &accountBucket{}
			s.accounts[accountID] = bucket
		}
		bucket.mu.Lock()
		bucket.balance = quota.Balance
		bucket.rateLimitRPM = quota.RateLimitRPM
		bucket.rateLimitSet = true
		bucket.windowStart = time.Time{}
		bucket.windowCount = 0
		bucket.mu.Unlock()
	}
	if len(s.accounts) > s.maxAccounts {
		s.maxAccounts = len(s.accounts)
	}
}

// AcquireAndDebit admits one request and pre-debits its cost. The checks run in
// this order: maintenance mode, route policy, rate limit, balance.
//
// A rejected request never changes the balance. A request that passes the rate
// limit but then fails the balance check still consumed a rate-limit slot: the
// slot is intentionally not rolled back because the attempt reached admission.
// Conversely a rate-limited request consumes no balance. The rate-limit window
// is fixed and anchored at the first request of each minute; the decision carries
// the tokens left in the active window. HotPathDecision has no reset-time field,
// so the reset instant remains internal to the bucket.
func (s *Store) AcquireAndDebit(accountID, route string) (domain.HotPathDecision, error) {
	s.totalRequests.Add(1)

	config := s.config.Load()
	if config != nil && config.MaintenanceMode {
		s.maintenanceRejected.Add(1)
		return rejected(domain.ErrRouteDisabled, 0, 0, 0), domain.ErrRouteDisabled
	}

	var (
		policy      domain.RoutePolicy
		hasPolicy   bool
		defaultRPM  int
		defaultCost int64
	)
	if config != nil {
		policy, hasPolicy = config.Routes[route]
		defaultRPM = config.DefaultRateLimitRPM
		defaultCost = config.DefaultRequestCost
	}
	if hasPolicy && !policy.Enabled {
		s.routeDisabled.Add(1)
		return rejected(domain.ErrRouteDisabled, 0, 0, 0), domain.ErrRouteDisabled
	}

	cost := defaultCost
	if hasPolicy {
		cost = policy.Cost
	}
	if cost < 0 {
		cost = 0
	}

	bucket, err := s.bucketFor(accountID)
	if err != nil {
		return rejected(err, cost, 0, 0), err
	}
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	rpm := effectiveRPM(policy, hasPolicy, bucket, defaultRPM, s.defaultRateLimitRPM)
	remaining, rateOK := bucket.consumeRate(rpm, s.clock())
	if !rateOK {
		s.rateLimited.Add(1)
		return domain.HotPathDecision{
			Allowed:          false,
			Reason:           domain.ErrRateLimited.Error(),
			Cost:             cost,
			RemainingBalance: bucket.balance,
			RemainingTokens:  0,
			RateLimitRPM:     decisionRPM(rpm),
		}, domain.ErrRateLimited
	}

	if bucket.balance < cost {
		s.insufficientBalance.Add(1)
		return domain.HotPathDecision{
			Allowed:          false,
			Reason:           domain.ErrInsufficientBalance.Error(),
			Cost:             cost,
			RemainingBalance: bucket.balance,
			RemainingTokens:  remaining,
			RateLimitRPM:     decisionRPM(rpm),
		}, domain.ErrInsufficientBalance
	}

	bucket.balance -= cost
	s.totalDebited.Add(cost)
	s.allowed.Add(1)

	return domain.HotPathDecision{
		Allowed:          true,
		Cost:             cost,
		RemainingBalance: bucket.balance,
		RemainingTokens:  remaining,
		RateLimitRPM:     decisionRPM(rpm),
	}, nil
}

// Authorize performs the same maintenance-mode, route-policy and rate-limit
// checks as AcquireAndDebit but leaves the balance untouched. It is used by
// routes whose handler makes one explicit, amount-specific debit.
func (s *Store) Authorize(accountID, route string) (domain.HotPathDecision, error) {
	s.totalRequests.Add(1)

	config := s.config.Load()
	if config != nil && config.MaintenanceMode {
		s.maintenanceRejected.Add(1)
		return rejected(domain.ErrRouteDisabled, 0, 0, 0), domain.ErrRouteDisabled
	}

	var (
		policy     domain.RoutePolicy
		hasPolicy  bool
		defaultRPM int
	)
	if config != nil {
		policy, hasPolicy = config.Routes[route]
		defaultRPM = config.DefaultRateLimitRPM
	}
	if hasPolicy && !policy.Enabled {
		s.routeDisabled.Add(1)
		return rejected(domain.ErrRouteDisabled, 0, 0, 0), domain.ErrRouteDisabled
	}

	bucket, err := s.bucketFor(accountID)
	if err != nil {
		return rejected(err, 0, 0, 0), err
	}
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	rpm := effectiveRPM(policy, hasPolicy, bucket, defaultRPM, s.defaultRateLimitRPM)
	remaining, rateOK := bucket.consumeRate(rpm, s.clock())
	if !rateOK {
		s.rateLimited.Add(1)
		return domain.HotPathDecision{
			Allowed:          false,
			Reason:           domain.ErrRateLimited.Error(),
			RemainingBalance: bucket.balance,
			RemainingTokens:  0,
			RateLimitRPM:     decisionRPM(rpm),
		}, domain.ErrRateLimited
	}

	s.allowed.Add(1)
	return domain.HotPathDecision{
		Allowed:          true,
		RemainingBalance: bucket.balance,
		RemainingTokens:  remaining,
		RateLimitRPM:     decisionRPM(rpm),
	}, nil
}

// Debit deducts an explicit amount from an account without applying route
// policy or the rate limit. Authorization is expected to have happened first.
// It refuses unknown accounts instead of creating one with a free initial
// balance.
func (s *Store) Debit(accountID string, amount int64) (domain.HotPathDecision, error) {
	accountID, err := domain.ValidateAccountID(accountID)
	if err != nil {
		return domain.HotPathDecision{}, err
	}
	if amount < 0 {
		amount = 0
	}

	bucket := s.findBucket(accountID)
	if bucket == nil {
		return domain.HotPathDecision{}, port.ErrNotFound
	}
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	if bucket.balance < amount {
		s.insufficientBalance.Add(1)
		return domain.HotPathDecision{
			Allowed:          false,
			Reason:           domain.ErrInsufficientBalance.Error(),
			Cost:             amount,
			RemainingBalance: bucket.balance,
			RemainingTokens:  -1,
		}, domain.ErrInsufficientBalance
	}

	bucket.balance -= amount
	s.totalDebited.Add(amount)
	return domain.HotPathDecision{
		Allowed:          true,
		Cost:             amount,
		RemainingBalance: bucket.balance,
		RemainingTokens:  -1,
	}, nil
}

// Refund returns a previously charged amount to an account. Non-positive
// amounts and unknown accounts are ignored.
func (s *Store) Refund(accountID string, amount int64) {
	if amount <= 0 {
		return
	}
	accountID, err := domain.ValidateAccountID(accountID)
	if err != nil {
		return
	}
	bucket := s.findBucket(accountID)
	if bucket == nil {
		return
	}
	bucket.mu.Lock()
	bucket.balance += amount
	bucket.mu.Unlock()
	s.totalRefunded.Add(amount)
}

// Credit adds funds to an account. Non-positive amounts are ignored. An
// unknown account is created with exactly the credited amount; it does not
// inherit the configured initial balance because that balance is reserved for
// accounts admitted through the request path.
func (s *Store) Credit(accountID string, amount int64) error {
	accountID, err := domain.ValidateAccountID(accountID)
	if err != nil {
		return err
	}
	if amount <= 0 {
		return nil
	}

	s.mu.Lock()
	bucket := s.accounts[accountID]
	if bucket == nil {
		if len(s.accounts) >= s.maxAccounts {
			s.mu.Unlock()
			return domain.ErrTooManyAccounts
		}
		bucket = &accountBucket{
			balance:      amount,
			rateLimitRPM: s.defaultRateLimitRPM,
		}
		s.accounts[accountID] = bucket
		s.mu.Unlock()
		s.totalCredited.Add(amount)
		return nil
	}
	s.mu.Unlock()

	bucket.mu.Lock()
	if bucket.balance > math.MaxInt64-amount {
		bucket.mu.Unlock()
		return fmt.Errorf("%w: balance overflow", domain.ErrValidation)
	}
	bucket.balance += amount
	bucket.mu.Unlock()
	s.totalCredited.Add(amount)
	return nil
}

// RollbackCredit reverses a Credit when the corresponding persistence
// operation fails. It never creates an account. The amount is always removed,
// even when concurrent spending has already reduced the balance below it.
func (s *Store) RollbackCredit(accountID string, amount int64) {
	if amount <= 0 {
		return
	}
	accountID, err := domain.ValidateAccountID(accountID)
	if err != nil {
		return
	}
	bucket := s.findBucket(accountID)
	if bucket == nil {
		return
	}
	bucket.mu.Lock()
	bucket.balance -= amount
	bucket.mu.Unlock()
	s.totalCredited.Add(-amount)
}

// GetAccountState reports the in-memory state of a known account. It never
// creates an account as a side effect.
func (s *Store) GetAccountState(accountID string) (domain.AccountState, bool) {
	s.mu.RLock()
	bucket := s.accounts[accountID]
	s.mu.RUnlock()
	if bucket == nil {
		return domain.AccountState{}, false
	}

	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	rateLimitRPM := bucket.rateLimitRPM
	if !bucket.rateLimitSet {
		if config := s.config.Load(); config != nil && config.DefaultRateLimitRPM > 0 {
			rateLimitRPM = config.DefaultRateLimitRPM
		} else {
			rateLimitRPM = s.defaultRateLimitRPM
		}
	}
	return domain.AccountState{
		AccountID:    accountID,
		Balance:      bucket.balance,
		RateLimitRPM: rateLimitRPM,
	}, true
}

// SnapshotQuotas returns the current quota state for every account. It is used
// once during graceful shutdown to enqueue a final authoritative sync after the
// HTTP server has stopped accepting and running handlers.
func (s *Store) SnapshotQuotas(updatedAt time.Time) []domain.AccountQuota {
	s.mu.RLock()
	buckets := make(map[string]*accountBucket, len(s.accounts))
	for accountID, bucket := range s.accounts {
		buckets[accountID] = bucket
	}
	s.mu.RUnlock()

	config := s.config.Load()
	quotas := make([]domain.AccountQuota, 0, len(buckets))
	for accountID, bucket := range buckets {
		bucket.mu.Lock()
		rateLimitRPM := bucket.rateLimitRPM
		if !bucket.rateLimitSet {
			if config != nil && config.DefaultRateLimitRPM > 0 {
				rateLimitRPM = config.DefaultRateLimitRPM
			} else {
				rateLimitRPM = s.defaultRateLimitRPM
			}
		}
		quotas = append(quotas, domain.AccountQuota{
			AccountID:    accountID,
			Balance:      bucket.balance,
			RateLimitRPM: rateLimitRPM,
			UpdatedAt:    updatedAt,
		})
		bucket.mu.Unlock()
	}
	return quotas
}

// Stats returns a point-in-time snapshot of the hot-path telemetry counters.
func (s *Store) Stats() domain.HotPathStats {
	return domain.HotPathStats{
		Requests:            s.totalRequests.Load(),
		Allowed:             s.allowed.Load(),
		RateLimited:         s.rateLimited.Load(),
		InsufficientBalance: s.insufficientBalance.Load(),
		RouteDisabled:       s.routeDisabled.Load(),
		MaintenanceRejected: s.maintenanceRejected.Load(),
	}
}

// bucketFor returns the bucket for a valid account, creating it lazily with the
// store defaults when capacity allows. Invalid IDs and a full account table are
// reported to the caller instead of failing inside the hot path.
func (s *Store) bucketFor(accountID string) (*accountBucket, error) {
	accountID, err := domain.ValidateAccountID(accountID)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	bucket := s.accounts[accountID]
	s.mu.RUnlock()
	if bucket != nil {
		return bucket, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if bucket = s.accounts[accountID]; bucket != nil {
		return bucket, nil
	}
	if len(s.accounts) >= s.maxAccounts {
		return nil, domain.ErrTooManyAccounts
	}
	bucket = &accountBucket{
		balance:      s.initialBalance,
		rateLimitRPM: s.defaultRateLimitRPM,
	}
	s.accounts[accountID] = bucket
	return bucket, nil
}

// findBucket returns an existing bucket without creating one.
func (s *Store) findBucket(accountID string) *accountBucket {
	s.mu.RLock()
	bucket := s.accounts[accountID]
	s.mu.RUnlock()
	return bucket
}

// decisionRPM normalizes the effective limit for HotPathDecision, where zero
// means unlimited.
func decisionRPM(rpm int) int {
	if rpm < 0 {
		return 0
	}
	return rpm
}

func effectiveRPM(
	policy domain.RoutePolicy,
	hasPolicy bool,
	bucket *accountBucket,
	configDefault int,
	storeDefault int,
) int {
	if hasPolicy && policy.RateLimitRPM > 0 {
		return policy.RateLimitRPM
	}
	if bucket.rateLimitSet {
		return bucket.rateLimitRPM
	}
	if configDefault > 0 {
		return configDefault
	}
	return storeDefault
}

// consumeRate advances the fixed one-minute window. It returns the remaining
// tokens (-1 when the account has no limit) and whether the request fits.
func (b *accountBucket) consumeRate(rpm int, now time.Time) (int64, bool) {
	if b.windowStart.IsZero() || !now.Before(b.windowStart.Add(rateLimitWindow)) {
		b.windowStart = now
		b.windowCount = 0
	}
	if rpm > 0 && b.windowCount >= int64(rpm) {
		return 0, false
	}
	b.windowCount++
	if rpm <= 0 {
		return -1, true
	}
	remaining := int64(rpm) - b.windowCount
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

func rejected(err error, cost, balance, tokens int64) domain.HotPathDecision {
	return domain.HotPathDecision{
		Allowed:          false,
		Reason:           err.Error(),
		Cost:             cost,
		RemainingBalance: balance,
		RemainingTokens:  tokens,
	}
}
