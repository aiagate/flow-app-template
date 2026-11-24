package usecase

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

const maxBillingReasonRunes = 200

// AccountView is the balance snapshot returned to callers.
type AccountView struct {
	AccountID    string
	Balance      int64
	RateLimitRPM int
	UpdatedAt    time.Time
}

// BillingUseCase owns the explicit ledger operations. Metered request costs are
// pre-debited by the hot-path guard before the handler runs; credit and charge
// move funds here and enqueue their durable history.
type BillingUseCase struct {
	guard  port.HotPathGuard
	quotas port.QuotaRepository
	audit  port.AuditQuery
	queue  port.WriteEventQueue
}

func NewBillingUseCase(
	guard port.HotPathGuard,
	quotas port.QuotaRepository,
	audit port.AuditQuery,
	queue port.WriteEventQueue,
) *BillingUseCase {
	return &BillingUseCase{guard: guard, quotas: quotas, audit: audit, queue: queue}
}

type CreditAccountInput struct {
	AccountID string
	Amount    int64
	Reason    string
	RequestID string
}

type ChargeAccountInput struct {
	AccountID string
	Amount    int64
	Reason    string
	RequestID string
}

// GetAccount returns the authoritative in-memory balance for an account. The
// persisted quota supplies updated_at when it is available.
func (b *BillingUseCase) GetAccount(ctx context.Context, accountID string) (AccountView, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AccountView{}, validationError("account_id", "must not be empty")
	}

	state, ok := b.guard.GetAccountState(accountID)
	if ok {
		updatedAt := time.Now().UTC()
		quota, err := b.quotas.GetByAccountID(ctx, accountID)
		switch {
		case err == nil:
			updatedAt = quota.UpdatedAt
		case errors.Is(err, port.ErrNotFound):
		default:
			return AccountView{}, ClassifyError(err)
		}
		return AccountView{
			AccountID:    state.AccountID,
			Balance:      state.Balance,
			RateLimitRPM: state.RateLimitRPM,
			UpdatedAt:    updatedAt,
		}, nil
	}

	quota, err := b.quotas.GetByAccountID(ctx, accountID)
	if err != nil {
		return AccountView{}, ClassifyError(err)
	}
	return AccountView{
		AccountID:    quota.AccountID,
		Balance:      quota.Balance,
		RateLimitRPM: quota.RateLimitRPM,
		UpdatedAt:    quota.UpdatedAt,
	}, nil
}

// CreditAccount adds balance immediately and queues the billing event plus the
// latest quota snapshot for the asynchronous group commit.
func (b *BillingUseCase) CreditAccount(ctx context.Context, input CreditAccountInput) (AccountView, error) {
	accountID, reason, err := validateLedgerInput(input.AccountID, input.Amount, input.Reason)
	if err != nil {
		return AccountView{}, err
	}

	b.guard.Credit(accountID, input.Amount)
	state, ok := b.guard.GetAccountState(accountID)
	if !ok {
		return AccountView{}, NewError(CodeUnexpected, "account state is unavailable")
	}

	now := time.Now().UTC()
	view := AccountView{
		AccountID:    state.AccountID,
		Balance:      state.Balance,
		RateLimitRPM: state.RateLimitRPM,
		UpdatedAt:    now,
	}
	if err := b.enqueueMovement(domain.BillingEvent{
		ID:         domain.NewEventID(),
		AccountID:  accountID,
		Amount:     input.Amount,
		Reason:     reason,
		RequestID:  input.RequestID,
		OccurredAt: now,
	}, view); err != nil {
		// Undo the in-memory credit so the caller can safely retry.
		_, _ = b.guard.Debit(accountID, input.Amount)
		return AccountView{}, err
	}
	return view, nil
}

// ChargeAccount performs the single explicit debit for the charge route. The
// middleware for that route only authorizes and rate-limits, so the amount is
// never deducted twice.
func (b *BillingUseCase) ChargeAccount(ctx context.Context, input ChargeAccountInput) (AccountView, error) {
	accountID, reason, err := validateLedgerInput(input.AccountID, input.Amount, input.Reason)
	if err != nil {
		return AccountView{}, err
	}

	_, err = b.guard.Debit(accountID, input.Amount)
	if err != nil {
		return AccountView{}, ClassifyError(err)
	}
	state, ok := b.guard.GetAccountState(accountID)
	if !ok {
		b.guard.Refund(accountID, input.Amount)
		return AccountView{}, NewError(CodeUnexpected, "account state is unavailable")
	}

	now := time.Now().UTC()
	view := AccountView{
		AccountID:    state.AccountID,
		Balance:      state.Balance,
		RateLimitRPM: state.RateLimitRPM,
		UpdatedAt:    now,
	}
	if err := b.enqueueMovement(domain.BillingEvent{
		ID:         domain.NewEventID(),
		AccountID:  accountID,
		Amount:     -input.Amount,
		Reason:     reason,
		RequestID:  input.RequestID,
		OccurredAt: now,
	}, view); err != nil {
		b.guard.Refund(accountID, input.Amount)
		return AccountView{}, err
	}
	return view, nil
}

// ListBillingEvents reads append-only ledger history, optionally filtered by
// account.
func (b *BillingUseCase) ListBillingEvents(ctx context.Context, accountID string, limit int) ([]domain.BillingEvent, error) {
	events, err := b.audit.ListBillingEvents(ctx, strings.TrimSpace(accountID), limit)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return events, nil
}

// ListRequestLogs reads append-only request history.
func (b *BillingUseCase) ListRequestLogs(ctx context.Context, limit int) ([]domain.RequestLog, error) {
	logs, err := b.audit.ListRequestLogs(ctx, "", limit)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return logs, nil
}

func (b *BillingUseCase) enqueueMovement(event domain.BillingEvent, view AccountView) error {
	if err := b.queue.EnqueueBilling(event); err != nil {
		return ClassifyError(err)
	}
	if err := b.queue.EnqueueQuotaSync(domain.AccountQuota{
		AccountID:    view.AccountID,
		Balance:      view.Balance,
		RateLimitRPM: view.RateLimitRPM,
		UpdatedAt:    view.UpdatedAt,
	}); err != nil {
		return ClassifyError(err)
	}
	return nil
}

func validateLedgerInput(accountID string, amount int64, reason string) (string, string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", "", validationError("account_id", "must not be empty")
	}
	if amount <= 0 {
		return "", "", validationError("amount", "must be positive")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", "", validationError("reason", "must not be empty")
	}
	if utf8.RuneCountInString(reason) > maxBillingReasonRunes {
		return "", "", validationError("reason", "must not exceed 200 characters")
	}
	return accountID, reason, nil
}
