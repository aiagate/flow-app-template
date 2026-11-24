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
	EnqueueQuotaSync(quota domain.AccountQuota) error
	EnqueueRequestLog(log domain.RequestLog) error
	Stats() domain.BatcherStats
	Drain(ctx context.Context) error
}
