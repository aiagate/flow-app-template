// Package batcher provides the asynchronous group-commit write queue. It
// depends only on port.BatchSink; the composition root supplies a sink such as
// the SQLite store, and this package never imports another infra adapter.
package batcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// DefaultMaxRetryBackoff caps the exponential retry delay after a failed
	// commit.
	DefaultMaxRetryBackoff = time.Second
)

type eventKind uint8

const (
	billingEvent eventKind = iota
	quotaEvent
	requestLogEvent
	// billingWithQuotaEvent keeps a billing event and the quota snapshot from
	// the same operation in one queue slot so they commit together.
	billingWithQuotaEvent
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
	QueueCapacity   int
	BatchSize       int
	FlushInterval   time.Duration
	MaxRetryBackoff time.Duration
	Logger          *slog.Logger
}

// Batcher implements port.WriteEventQueue with one background writer.
type Batcher struct {
	sink            port.BatchSink
	queueCapacity   int
	batchSize       int
	flushInterval   time.Duration
	maxRetryBackoff time.Duration
	logger          *slog.Logger

	// mu guards the channel lifecycle. Enqueue holds RLock while checking
	// closed and sending, so Drain's Lock cannot close the channel underneath a
	// sender. The send itself is non-blocking.
	mu       sync.RWMutex
	closed   bool
	drainCtx context.Context
	drainErr error

	ch     chan writeEvent
	done   chan struct{}
	stopCh chan struct{}
	// tokens bounds the total number of accepted-but-unwritten slots across
	// the channel buffer and outstanding reservations. It is pre-filled with
	// queueCapacity tokens and never closed, so returning a token never
	// panics and never blocks.
	tokens chan struct{}
	// activeRes tracks reservations that have not been settled yet. Drain
	// waits for it before closing the channel.
	activeRes sync.WaitGroup

	queueCap     atomic.Int64
	queueLen     atomic.Int64
	pendingLen   atomic.Int64
	enqueued     atomic.Int64
	flushed      atomic.Int64
	flushCommits atomic.Int64
	dropped      atomic.Int64
	errors       atomic.Int64
}

var _ port.WriteEventQueue = (*Batcher)(nil)

// New starts the background writer. A nil sink or a negative option is an
// error; zero options use QueueCapacity, BatchSize, FlushInterval, and
// DefaultMaxRetryBackoff.
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

	maxRetryBackoff := opts.MaxRetryBackoff
	if maxRetryBackoff < 0 {
		return nil, fmt.Errorf("batcher: max retry backoff must be non-negative, got %s", maxRetryBackoff)
	}
	if maxRetryBackoff == 0 {
		maxRetryBackoff = DefaultMaxRetryBackoff
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	batcher := &Batcher{
		sink:            sink,
		queueCapacity:   capacity,
		batchSize:       batchSize,
		flushInterval:   interval,
		maxRetryBackoff: maxRetryBackoff,
		logger:          logger,
		ch:              make(chan writeEvent, capacity),
		done:            make(chan struct{}),
		stopCh:          make(chan struct{}),
		tokens:          make(chan struct{}, capacity),
	}
	for i := 0; i < capacity; i++ {
		batcher.tokens <- struct{}{}
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

// EnqueueQuotaSyncBlocking queues the latest quota snapshot, waiting for a free
// slot instead of dropping it. It gives up with ctx.Err() on cancellation and
// with port.ErrQueueClosed once the queue has been drained.
func (b *Batcher) EnqueueQuotaSyncBlocking(ctx context.Context, quota domain.AccountQuota) error {
	if ctx == nil {
		ctx = context.Background()
	}
	event := writeEvent{kind: quotaEvent, quota: quota}

	select {
	case <-b.tokens:
	case <-b.stopCh:
		return port.ErrQueueClosed
	case <-ctx.Done():
		return ctx.Err()
	}

	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		b.tokens <- struct{}{}
		return port.ErrQueueClosed
	}
	b.enqueued.Add(1)
	b.queueLen.Add(1)
	b.ch <- event
	b.mu.RUnlock()
	return nil
}

// EnqueueRequestLog queues one request log row.
func (b *Batcher) EnqueueRequestLog(logRow domain.RequestLog) error {
	return b.enqueue(writeEvent{kind: requestLogEvent, requestLog: logRow})
}

// Reserve allocates one slot without blocking on the writer goroutine. The
// returned reservation guarantees space in the bounded channel for exactly one
// event, so a caller may commit while holding a per-account lock without doing
// disk I/O or risking a blocking send. Every reservation must be settled with
// CommitBillingWithQuota, CommitQuotaSync, or Release.
func (b *Batcher) Reserve() (port.WriteReservation, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return nil, port.ErrQueueClosed
	}
	select {
	case <-b.tokens:
	default:
		b.dropped.Add(1)
		return nil, port.ErrQueueFull
	}
	b.activeRes.Add(1)
	return &reservation{b: b}, nil
}

// reservation is one pre-allocated queue slot. Settlement is idempotent and
// performed exactly once, either by committing one event or by releasing the
// slot. Neither path takes the lifecycle mutex: the reserved token already
// guarantees channel space and keeps Drain from closing the channel.
type reservation struct {
	b       *Batcher
	settled atomic.Bool
}

var _ port.WriteReservation = (*reservation)(nil)

// CommitBillingWithQuota commits the billing event and its quota snapshot into
// the reserved slot.
func (r *reservation) CommitBillingWithQuota(event domain.BillingEvent, quota domain.AccountQuota) error {
	return r.commit(writeEvent{kind: billingWithQuotaEvent, billing: event, quota: quota})
}

// CommitQuotaSync commits one quota snapshot into the reserved slot.
func (r *reservation) CommitQuotaSync(quota domain.AccountQuota) error {
	return r.commit(writeEvent{kind: quotaEvent, quota: quota})
}

func (r *reservation) commit(event writeEvent) error {
	if !r.settled.CompareAndSwap(false, true) {
		return errors.New("batcher: reservation already settled")
	}
	// The reserved token guarantees len(ch) < cap(ch), and Drain waits for
	// activeRes before closing ch, so this send neither blocks nor panics.
	r.b.enqueued.Add(1)
	r.b.queueLen.Add(1)
	r.b.ch <- event
	r.b.activeRes.Done()
	return nil
}

// Release returns the reserved slot without enqueueing an event. It is
// idempotent and safe to call from a defer after a failed commit attempt.
func (r *reservation) Release() {
	if !r.settled.CompareAndSwap(false, true) {
		return
	}
	r.b.tokens <- struct{}{}
	r.b.activeRes.Done()
}

// Stats returns a point-in-time snapshot of the queue counters. Written counts
// events successfully committed to the sink; QueueDepth counts events queued
// but not yet moved into the writer's in-memory batch; Pending counts the
// events held by the writer for the next commit.
func (b *Batcher) Stats() domain.BatcherStats {
	return domain.BatcherStats{
		Enqueued:      b.enqueued.Load(),
		Written:       b.flushed.Load(),
		Dropped:       b.dropped.Load(),
		Pending:       int(b.pendingLen.Load()),
		Errors:        b.errors.Load(),
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
//
// Shutdown is driven by one background closer rather than by the calling
// goroutine, so a caller with an expired context can return without stranding
// the writer. The closer waits for every reservation taken before the close
// (closing the channel under an unsettled reservation would make its commit
// panic), then always wakes the writer and closes the channel. Consequently a
// later Drain(context.Background()) still completes after an early timeout.
func (b *Batcher) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return b.waitDone(ctx)
	}
	b.closed = true
	b.drainCtx = ctx
	// Exactly one closer is started, under the lifecycle lock. closed is
	// already true, so no new Reserve can call Add after this Wait starts.
	// Setting drainCtx before this goroutine closes stopCh and ch makes the
	// finish path read it without a race.
	go func() {
		b.activeRes.Wait()
		// Wake the writer before closing the channel so a retry backoff cannot
		// delay shutdown until its timer fires.
		close(b.stopCh)
		close(b.ch)
	}()
	b.mu.Unlock()

	return b.waitDone(ctx)
}

// waitDone waits for the writer goroutine to finish, preferring an already
// completed drain over the caller's cancelled context.
func (b *Batcher) waitDone(ctx context.Context) error {
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

	// Acquiring a token reserves channel space for this event, so the send has
	// a free slot and cannot race Drain's channel close (which waits for the
	// lifecycle lock this RLock excludes).
	select {
	case <-b.tokens:
	default:
		b.dropped.Add(1)
		return port.ErrQueueFull
	}

	// Count the slot before publishing it so the writer can never decrement a
	// not-yet-counted event and make QueueDepth transiently negative.
	b.queueLen.Add(1)
	b.ch <- event
	b.enqueued.Add(1)
	return nil
}

func (b *Batcher) run() {
	defer close(b.done)
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	pending := make([]writeEvent, 0, b.batchSize)
	retrying := false
	backoff := b.flushInterval

	var (
		retryTimer *time.Timer
		retryCh    <-chan time.Time
	)
	stopRetry := func() {
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer = nil
		}
		retryCh = nil
	}
	defer stopRetry()

	// recvCh is nil while a failed batch waits for its retry timer. Disabling
	// the receive case bounds pending to one batch and lets QueueDepth and
	// ErrQueueFull express backpressure to producers.
	recvCh := b.ch

	armRetry := func(batchLen int, err error) {
		b.errors.Add(1)
		b.logger.Error("batcher write failed", "err", err, "batch_len", batchLen, "retry_in", backoff)
		retrying = true
		recvCh = nil
		retryTimer = time.NewTimer(backoff)
		retryCh = retryTimer.C
		backoff = growBackoff(backoff, b.maxRetryBackoff)
	}

	// succeed clears the batch after a committed flush and restores normal
	// receiving.
	succeed := func() {
		pending = pending[:0]
		b.pendingLen.Store(0)
		retrying = false
		recvCh = b.ch
		backoff = b.flushInterval
	}

	for {
		select {
		case event, ok := <-recvCh:
			if !ok {
				stopRetry()
				b.finish(pending)
				return
			}
			b.queueLen.Add(-1)
			b.tokens <- struct{}{}
			pending = append(pending, event)
			b.pendingLen.Add(1)

			if !retrying && len(pending) >= b.batchSize {
				if err := b.commit(context.Background(), pending[:b.batchSize]); err != nil {
					armRetry(b.batchSize, err)
					continue
				}
				pending = pending[:0]
				b.pendingLen.Add(-int64(b.batchSize))
			}

		case <-ticker.C:
			if len(pending) == 0 || retrying {
				continue
			}
			if err := b.commit(context.Background(), pending); err != nil {
				armRetry(len(pending), err)
				continue
			}
			succeed()

		case <-retryCh:
			retryTimer = nil
			retryCh = nil
			if err := b.commit(context.Background(), pending); err != nil {
				armRetry(len(pending), err)
				continue
			}
			succeed()

		case <-b.stopCh:
			stopRetry()
			b.finish(pending)
			return
		}
	}
}

// growBackoff doubles current up to max without overflowing.
func growBackoff(current, max time.Duration) time.Duration {
	if current >= max {
		return max
	}
	next := current * 2
	if next <= current || next > max {
		return max
	}
	return next
}

// finish commits the in-memory batch and every event still buffered in the
// closed channel in chunks of batchSize. Each chunk gets a few short retries.
// A chunk that still fails is recorded in the drain error and stops all later
// chunks from being committed, so a later quota snapshot can never be persisted
// while an earlier billing batch failed.
func (b *Batcher) finish(pending []writeEvent) {
	ctx := b.drainContext()
	var (
		drainErr  error
		discarded bool
	)

	// flush returns false when a chunk could not be committed even after its
	// bounded retries. Later chunks must then be skipped: persisting a later
	// quota snapshot after an earlier billing batch failed would make the
	// persisted balances move forward while a charge was lost.
	flush := func(batch []writeEvent) bool {
		if len(batch) == 0 {
			return true
		}
		err := b.commitWithRetry(ctx, batch)
		if err == nil {
			b.pendingLen.Add(-int64(len(batch)))
			return true
		}
		if ctx.Err() == nil {
			b.logger.Error("batcher write failed", "err", err, "batch_len", len(batch))
		}
		drainErr = errors.Join(drainErr, err)
		return false
	}

	halted := !flush(pending)

	batch := make([]writeEvent, 0, b.batchSize)
	for event := range b.ch {
		b.queueLen.Add(-1)
		b.tokens <- struct{}{}
		// Keep draining the closed channel even when halted so every buffered
		// event is accounted for, but commit nothing after the first failure.
		if halted {
			discarded = true
			continue
		}
		b.pendingLen.Add(1)
		batch = append(batch, event)
		if len(batch) < b.batchSize {
			continue
		}
		if ctx.Err() != nil {
			discarded = true
			batch = batch[:0]
			continue
		}
		halted = !flush(batch)
		batch = batch[:0]
	}
	if len(batch) > 0 {
		if halted || ctx.Err() != nil {
			discarded = true
		} else {
			flush(batch)
		}
	}
	// Report cancellation when accepted events could not be committed. If the
	// drain context ended before the writer finished, report that as the
	// outcome even when nothing was left to discard, so every waiting Drain
	// observes the same error whether it wins the select on done or on its own
	// context.
	if discarded && drainErr == nil {
		drainErr = ctx.Err()
	}
	if drainErr == nil {
		drainErr = ctx.Err()
	}

	b.queueLen.Store(0)
	b.pendingLen.Store(0)
	b.setDrainError(drainErr)
}

// commitWithRetry makes one commit attempt and up to three retries with short
// backoff. It stops early when ctx is done.
func (b *Batcher) commitWithRetry(ctx context.Context, batch []writeEvent) error {
	backoffs := [...]time.Duration{15 * time.Millisecond, 30 * time.Millisecond, 60 * time.Millisecond}

	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = b.commit(ctx, batch)
		if lastErr == nil {
			return nil
		}
		b.errors.Add(1)
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt >= len(backoffs) {
			return lastErr
		}

		timer := time.NewTimer(backoffs[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (b *Batcher) drainContext() context.Context {
	b.mu.RLock()
	ctx := b.drainCtx
	b.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
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

	appendQuota := func(quota domain.AccountQuota) {
		if quotaIndex == nil {
			quotaIndex = make(map[string]int)
		}
		if index, ok := quotaIndex[quota.AccountID]; ok {
			if quota.Seq >= payload.Quotas[index].Seq {
				payload.Quotas[index] = quota
			}
			return
		}
		quotaIndex[quota.AccountID] = len(payload.Quotas)
		payload.Quotas = append(payload.Quotas, quota)
	}

	for _, event := range events {
		switch event.kind {
		case billingEvent:
			payload.BillingEvents = append(payload.BillingEvents, event.billing)
		case billingWithQuotaEvent:
			payload.BillingEvents = append(payload.BillingEvents, event.billing)
			appendQuota(event.quota)
		case quotaEvent:
			appendQuota(event.quota)
		case requestLogEvent:
			payload.RequestLogs = append(payload.RequestLogs, event.requestLog)
		}
	}
	return payload
}
