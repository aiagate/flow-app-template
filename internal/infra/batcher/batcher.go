// Package batcher provides the asynchronous group-commit write queue. It
// depends only on port.BatchSink; the composition root supplies a sink such as
// the SQLite store, and this package never imports another infra adapter.
package batcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

const (
	// QueueCapacity is the default buffered channel size.
	QueueCapacity = 8192
	// BatchSize is both the immediate-flush threshold and the drain chunk size.
	BatchSize = 64
	// FlushInterval is the default maximum delay before a partial batch is
	// committed.
	FlushInterval = 15 * time.Millisecond
)

type eventKind uint8

const (
	billingEvent eventKind = iota
	quotaEvent
	requestLogEvent
)

type writeEvent struct {
	kind       eventKind
	billing    domain.BillingEvent
	quota      domain.AccountQuota
	requestLog domain.RequestLog
}

// Options overrides the batch queue defaults. Zero values use the package
// defaults; negative values are rejected by New.
type Options struct {
	QueueCapacity int
	BatchSize     int
	FlushInterval time.Duration
}

// Batcher implements port.WriteEventQueue with one background writer.
type Batcher struct {
	sink          port.BatchSink
	queueCapacity int
	batchSize     int
	flushInterval time.Duration

	// mu guards the channel lifecycle. Enqueue holds RLock while checking
	// closed and sending, so Drain's Lock cannot close the channel underneath a
	// sender. The send itself is non-blocking.
	mu       sync.RWMutex
	closed   bool
	drainCtx context.Context
	drainErr error

	ch   chan writeEvent
	done chan struct{}

	queueCap     atomic.Int64
	queueLen     atomic.Int64
	enqueued     atomic.Int64
	flushed      atomic.Int64
	flushCommits atomic.Int64
	dropped      atomic.Int64
	errors       atomic.Int64
}

var _ port.WriteEventQueue = (*Batcher)(nil)

// New starts the background writer. A nil sink or a negative option is an
// error; zero options use QueueCapacity, BatchSize, and FlushInterval.
func New(sink port.BatchSink, opts Options) (*Batcher, error) {
	if sink == nil {
		return nil, errors.New("batcher: sink must not be nil")
	}

	capacity := opts.QueueCapacity
	if capacity < 0 {
		return nil, fmt.Errorf("batcher: queue capacity must be non-negative, got %d", capacity)
	}
	if capacity == 0 {
		capacity = QueueCapacity
	}

	batchSize := opts.BatchSize
	if batchSize < 0 {
		return nil, fmt.Errorf("batcher: batch size must be non-negative, got %d", batchSize)
	}
	if batchSize == 0 {
		batchSize = BatchSize
	}

	interval := opts.FlushInterval
	if interval < 0 {
		return nil, fmt.Errorf("batcher: flush interval must be non-negative, got %s", interval)
	}
	if interval == 0 {
		interval = FlushInterval
	}

	batcher := &Batcher{
		sink:          sink,
		queueCapacity: capacity,
		batchSize:     batchSize,
		flushInterval: interval,
		ch:            make(chan writeEvent, capacity),
		done:          make(chan struct{}),
	}
	batcher.queueCap.Store(int64(capacity))
	go batcher.run()
	return batcher, nil
}

// EnqueueBilling queues one billing event.
func (b *Batcher) EnqueueBilling(event domain.BillingEvent) error {
	return b.enqueue(writeEvent{kind: billingEvent, billing: event})
}

// EnqueueQuotaSync queues the latest quota snapshot for an account.
func (b *Batcher) EnqueueQuotaSync(quota domain.AccountQuota) error {
	return b.enqueue(writeEvent{kind: quotaEvent, quota: quota})
}

// EnqueueRequestLog queues one request log row.
func (b *Batcher) EnqueueRequestLog(logRow domain.RequestLog) error {
	return b.enqueue(writeEvent{kind: requestLogEvent, requestLog: logRow})
}

// Stats returns a point-in-time snapshot of the queue counters. Written counts
// events successfully committed to the sink; QueueDepth counts events queued
// but not yet moved into the writer's in-memory batch.
func (b *Batcher) Stats() domain.BatcherStats {
	return domain.BatcherStats{
		Enqueued:      b.enqueued.Load(),
		Written:       b.flushed.Load(),
		Dropped:       b.dropped.Load(),
		QueueDepth:    int(b.queueLen.Load()),
		QueueCapacity: int(b.queueCap.Load()),
		FlushCommits:  b.flushCommits.Load(),
	}
}

// Drain closes the queue and waits until the writer has committed every
// accepted event. Repeated calls are safe: only the first one closes the
// channel, and later calls observe the same result. The first caller's context
// is the drain context used for the remaining commits; a waiting caller can
// stop early through its own ctx.Done().
func (b *Batcher) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	b.mu.Lock()
	if !b.closed {
		b.closed = true
		b.drainCtx = ctx
		close(b.ch)
	}
	b.mu.Unlock()

	// Prefer an already-finished drain even when the caller's context is also
	// done, so a completed Drain remains deterministic.
	select {
	case <-b.done:
		return b.result()
	default:
	}

	select {
	case <-b.done:
		return b.result()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Batcher) result() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.drainErr
}

func (b *Batcher) enqueue(event writeEvent) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return port.ErrQueueClosed
	}

	// Count the slot before publishing it so the writer can never decrement a
	// not-yet-counted event and make QueueDepth transiently negative.
	b.queueLen.Add(1)
	select {
	case b.ch <- event:
		b.enqueued.Add(1)
		return nil
	default:
		b.queueLen.Add(-1)
		b.dropped.Add(1)
		return port.ErrQueueFull
	}
}

func (b *Batcher) run() {
	defer close(b.done)
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	pending := make([]writeEvent, 0, b.batchSize)
	retrying := false

	for {
		select {
		case event, ok := <-b.ch:
			if !ok {
				b.finish(pending)
				return
			}
			b.queueLen.Add(-1)
			pending = append(pending, event)

			if len(pending) >= b.batchSize && !retrying {
				if err := b.commit(context.Background(), pending[:b.batchSize]); err != nil {
					b.errors.Add(1)
					retrying = true
					continue
				}
				pending = pending[b.batchSize:]
			}

		case <-ticker.C:
			if len(pending) == 0 {
				retrying = false
				continue
			}
			if err := b.commit(context.Background(), pending); err != nil {
				b.errors.Add(1)
				retrying = true
				continue
			}
			pending = pending[:0]
			retrying = false
		}
	}
}

// finish commits the in-memory batch and every event still buffered in the
// closed channel in chunks of batchSize. It records the first error and stops
// so Drain can report it instead of spinning forever.
func (b *Batcher) finish(pending []writeEvent) {
	b.mu.RLock()
	ctx := b.drainCtx
	b.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}

	batch := pending
	for event := range b.ch {
		b.queueLen.Add(-1)
		batch = append(batch, event)
		if len(batch) >= b.batchSize {
			if err := b.commit(ctx, batch[:b.batchSize]); err != nil {
				b.errors.Add(1)
				b.setDrainError(err)
				return
			}
			batch = batch[b.batchSize:]
		}
	}

	for len(batch) > 0 {
		n := b.batchSize
		if len(batch) < n {
			n = len(batch)
		}
		if err := b.commit(ctx, batch[:n]); err != nil {
			b.errors.Add(1)
			b.setDrainError(err)
			return
		}
		batch = batch[n:]
	}

	b.setDrainError(nil)
}

func (b *Batcher) setDrainError(err error) {
	b.mu.Lock()
	b.drainErr = err
	b.mu.Unlock()
}

func (b *Batcher) commit(ctx context.Context, events []writeEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if len(events) == 0 {
		return nil
	}

	payload := buildPayload(events)
	if err := b.sink.WriteBatch(ctx, payload); err != nil {
		return err
	}
	b.flushed.Add(int64(len(events)))
	b.flushCommits.Add(1)
	return nil
}

// buildPayload packs the batch into one payload and keeps only the latest
// quota snapshot per account. Billing events and request logs preserve queue
// order.
func buildPayload(events []writeEvent) port.BatchPayload {
	var payload port.BatchPayload
	var quotaIndex map[string]int

	for _, event := range events {
		switch event.kind {
		case billingEvent:
			payload.BillingEvents = append(payload.BillingEvents, event.billing)
		case quotaEvent:
			if quotaIndex == nil {
				quotaIndex = make(map[string]int)
			}
			if index, ok := quotaIndex[event.quota.AccountID]; ok {
				payload.Quotas[index] = event.quota
			} else {
				quotaIndex[event.quota.AccountID] = len(payload.Quotas)
				payload.Quotas = append(payload.Quotas, event.quota)
			}
		case requestLogEvent:
			payload.RequestLogs = append(payload.RequestLogs, event.requestLog)
		}
	}
	return payload
}
