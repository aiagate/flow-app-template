package port

import "github.com/aiagate/flow-app-template/internal/domain"

// HotPathGuard is the lock-free admission path. Implementations must complete
// every operation in memory; no method performs disk or network I/O.
type HotPathGuard interface {
	CurrentConfig() *domain.RuntimeConfig
	UpdateConfig(config *domain.RuntimeConfig)
	AcquireAndDebit(accountID, route string) (domain.HotPathDecision, error)
	// Authorize enforces maintenance mode, route policy and the per-account
	// rate limit without moving balance. It exists for routes whose handler
	// performs one explicit, amount-specific debit.
	Authorize(accountID, route string) (domain.HotPathDecision, error)
	// Debit deducts an explicit amount after Authorize. It checks the balance
	// and moves funds but does not apply route policy or rate limiting again.
	Debit(accountID string, amount int64) (domain.HotPathDecision, error)
	Refund(accountID string, amount int64)
	// Credit adds funds after a successful top-up. An unknown account is
	// created with exactly the credited amount. It reports an error for an
	// invalid account ID, when the account capacity limit is reached, or when
	// the resulting balance would overflow int64.
	Credit(accountID string, amount int64) error
	// RollbackCredit reverses a Credit when the corresponding persistence
	// operation fails.
	RollbackCredit(accountID string, amount int64)
	GetAccountState(accountID string) (domain.AccountState, bool)
	Hydrate(quotas []domain.AccountQuota)
	Stats() domain.HotPathStats
}
