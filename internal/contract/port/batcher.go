package port

import (
	"context"
	"errors"

	"github.com/aiagate/flow-app-template/internal/domain"
)

var (
	ErrQueueFull   = errors.New("write event queue is full")
	ErrQueueClosed = errors.New("write event queue is closed")
)

// BatchPayload is one group commit. Quotas carry the latest state per
// account; implementations must additionally deduplicate by AccountID.
type BatchPayload struct {
	BillingEvents []domain.BillingEvent
	Quotas        []domain.AccountQuota
	RequestLogs   []domain.RequestLog
}

// BatchSink persists one payload in a single transaction.
type BatchSink interface {
	WriteBatch(ctx context.Context, payload BatchPayload) error
}

// WriteEventQueue decouples the hot path from SQLite commits.
type WriteEventQueue interface {
	EnqueueBilling(event domain.BillingEvent) error
	// EnqueueBillingWithQuota enqueues a billing event together with the quota
	// snapshot produced by the same operation so they commit atomically.
	EnqueueBillingWithQuota(event domain.BillingEvent, quota domain.AccountQuota) error
	EnqueueQuotaSync(quota domain.AccountQuota) error
	// EnqueueQuotaSyncBlocking enqueues a quota snapshot and waits until it is
	// durable, unless ctx is cancelled first.
	EnqueueQuotaSyncBlocking(ctx context.Context, quota domain.AccountQuota) error
	EnqueueRequestLog(log domain.RequestLog) error
	Stats() domain.BatcherStats
	Drain(ctx context.Context) error
}
