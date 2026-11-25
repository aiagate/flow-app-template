package batcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSink is a thread-safe port.BatchSink that records every committed batch.
type fakeSink struct {
	mu       sync.Mutex
	payloads []port.BatchPayload
	commits  int
	err      error
}

func (s *fakeSink) WriteBatch(_ context.Context, payload port.BatchPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.commits++
	s.payloads = append(s.payloads, payload)
	return nil
}

func (s *fakeSink) commitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

func (s *fakeSink) payloadSnapshot() []port.BatchPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]port.BatchPayload, len(s.payloads))
	copy(out, s.payloads)
	return out
}

func (s *fakeSink) billingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, payload := range s.payloads {
		total += len(payload.BillingEvents)
	}
	return total
}

func (s *fakeSink) totalEventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, payload := range s.payloads {
		total += len(payload.BillingEvents) + len(payload.Quotas) + len(payload.RequestLogs)
	}
	return total
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestBatchSizeTriggersImmediateCommit(t *testing.T) {
	sink := &fakeSink{}
	b, err := New(sink, Options{FlushInterval: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	for i := 0; i < BatchSize; i++ {
		if err := b.EnqueueBilling(domain.BillingEvent{
			ID:         domain.NewEventID(),
			AccountID:  "acct",
			Amount:     -1,
			Reason:     "request",
			RequestID:  fmt.Sprintf("req_%d", i),
			OccurredAt: time.Now(),
		}); err != nil {
			t.Fatalf("EnqueueBilling %d: %v", i, err)
		}
	}

	// The 5s timer cannot have fired; only the batch-size threshold can
	// produce this commit.
	waitFor(t, 2*time.Second, func() bool { return sink.commitCount() == 1 })
	if got := sink.billingCount(); got != BatchSize {
		t.Fatalf("billing events = %d, want %d", got, BatchSize)
	}
	if payloads := sink.payloadSnapshot(); len(payloads) != 1 || len(payloads[0].BillingEvents) != BatchSize {
		t.Fatalf("payloads = %+v", payloads)
	}
}

func TestPartialBatchFlushesOnTimer(t *testing.T) {
	sink := &fakeSink{}
	b, err := New(sink, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	const count = 10
	for i := 0; i < count; i++ {
		if err := b.EnqueueBilling(domain.BillingEvent{
			ID:         domain.NewEventID(),
			AccountID:  "acct",
			Amount:     -1,
			Reason:     "request",
			RequestID:  fmt.Sprintf("req_%d", i),
			OccurredAt: time.Now(),
		}); err != nil {
			t.Fatalf("EnqueueBilling %d: %v", i, err)
		}
	}

	waitFor(t, 2*time.Second, func() bool { return sink.billingCount() == count })
	if sink.commitCount() < 1 {
		t.Fatal("no commit recorded")
	}
	if stats := b.Stats(); stats.Written != count {
		t.Fatalf("Stats().Written = %d, want %d", stats.Written, count)
	} else if stats.QueueCapacity != QueueCapacity || stats.FlushCommits == 0 {
		t.Fatalf("Stats() capacity/commits = %+v", stats)
	}
}

func TestQuotaDeduplicationKeepsLatestSnapshot(t *testing.T) {
	sink := &fakeSink{}
	b, err := New(sink, Options{BatchSize: 4, FlushInterval: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	if err := b.EnqueueQuotaSync(domain.AccountQuota{AccountID: "acct_a", Balance: 10, RateLimitRPM: 10}); err != nil {
		t.Fatalf("EnqueueQuotaSync A1: %v", err)
	}
	if err := b.EnqueueQuotaSync(domain.AccountQuota{AccountID: "acct_b", Balance: 5, RateLimitRPM: 20}); err != nil {
		t.Fatalf("EnqueueQuotaSync B: %v", err)
	}
	if err := b.EnqueueQuotaSync(domain.AccountQuota{AccountID: "acct_a", Balance: 30, RateLimitRPM: 30}); err != nil {
		t.Fatalf("EnqueueQuotaSync A2: %v", err)
	}
	if err := b.EnqueueRequestLog(domain.RequestLog{ID: "log_1", AccountID: "acct_a", Path: "/x"}); err != nil {
		t.Fatalf("EnqueueRequestLog: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return sink.commitCount() == 1 })
	payloads := sink.payloadSnapshot()
	if len(payloads) != 1 {
		t.Fatalf("commits = %d, want 1", len(payloads))
	}
	payload := payloads[0]
	if len(payload.RequestLogs) != 1 || payload.RequestLogs[0].ID != "log_1" {
		t.Fatalf("request logs = %+v", payload.RequestLogs)
	}
	if len(payload.Quotas) != 2 {
		t.Fatalf("deduped quotas = %+v, want 2 accounts", payload.Quotas)
	}
	quotas := make(map[string]domain.AccountQuota, len(payload.Quotas))
	for _, quota := range payload.Quotas {
		quotas[quota.AccountID] = quota
	}
	if got := quotas["acct_a"]; got.Balance != 30 || got.RateLimitRPM != 30 {
		t.Fatalf("acct_a quota = %+v, want latest snapshot 30/30", got)
	}
	if got := quotas["acct_b"]; got.Balance != 5 || got.RateLimitRPM != 20 {
		t.Fatalf("acct_b quota = %+v", got)
	}
}

func TestBurstThenDrainPersistsEveryEvent(t *testing.T) {
	const (
		total     = 2000
		producers = 10
	)
	sink := &fakeSink{}
	b, err := New(sink, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	events := make([]domain.BillingEvent, total)
	for i := range events {
		events[i] = domain.BillingEvent{
			ID:         domain.NewEventID(),
			AccountID:  fmt.Sprintf("acct_%d", i%7),
			Amount:     -1,
			Reason:     "request",
			RequestID:  fmt.Sprintf("req_%d", i),
			OccurredAt: time.Now(),
		}
	}

	var wg sync.WaitGroup
	for producer := 0; producer < producers; producer++ {
		wg.Add(1)
		go func(producer int) {
			defer wg.Done()
			for i := producer; i < total; i += producers {
				if err := b.EnqueueBilling(events[i]); err != nil {
					t.Errorf("EnqueueBilling %d: %v", i, err)
					return
				}
			}
		}(producer)
	}
	wg.Wait()

	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if got := sink.totalEventCount(); got != total {
		t.Fatalf("persisted events = %d, want %d", got, total)
	}

	seen := make(map[string]struct{}, total)
	for _, payload := range sink.payloadSnapshot() {
		for _, event := range payload.BillingEvents {
			id := event.ID.String()
			if _, duplicate := seen[id]; duplicate {
				t.Fatalf("duplicate event %s persisted", id)
			}
			seen[id] = struct{}{}
		}
	}
	if len(seen) != total {
		t.Fatalf("unique persisted events = %d, want %d", len(seen), total)
	}
	for _, event := range events {
		if _, ok := seen[event.ID.String()]; !ok {
			t.Fatalf("event %s lost", event.ID)
		}
	}

	stats := b.Stats()
	if stats.Enqueued != total || stats.Written != total {
		t.Fatalf("Stats() = %+v, want enqueued=written=%d", stats, total)
	}
	if stats.Dropped != 0 || stats.QueueDepth != 0 {
		t.Fatalf("Stats() = %+v, want no drops and empty queue", stats)
	}
	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
}

func TestEnqueueRacingDrainReturnsQueueClosed(t *testing.T) {
	sink := &fakeSink{}
	b, err := New(sink, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const producers = 20
	start := make(chan struct{})
	var (
		wg          sync.WaitGroup
		closedCount atomic.Int64
		unexpected  atomic.Int64
	)

	for producer := 0; producer < producers; producer++ {
		wg.Add(1)
		go func(producer int) {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				err := b.EnqueueRequestLog(domain.RequestLog{
					ID:         fmt.Sprintf("log_%d_%d", producer, i),
					AccountID:  "acct",
					RequestID:  fmt.Sprintf("req_%d_%d", producer, i),
					Method:     "GET",
					Path:       "/x",
					StatusCode: 200,
				})
				switch {
				case err == nil:
				case errors.Is(err, port.ErrQueueClosed):
					closedCount.Add(1)
				default:
					unexpected.Add(1)
				}
			}
		}(producer)
	}

	close(start)
	drainResult := make(chan error, 1)
	go func() { drainResult <- b.Drain(context.Background()) }()
	wg.Wait()

	if err := <-drainResult; err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := unexpected.Load(); got != 0 {
		t.Fatalf("unexpected enqueue errors = %d", got)
	}

	// The queue is definitively closed now, so every later enqueue must report
	// it rather than panic on a closed channel.
	for i := 0; i < 10; i++ {
		if err := b.EnqueueBilling(domain.BillingEvent{ID: domain.NewEventID()}); !errors.Is(err, port.ErrQueueClosed) {
			t.Fatalf("post-drain EnqueueBilling error = %v, want ErrQueueClosed", err)
		}
	}
	if closedCount.Load() == 0 {
		t.Log("no producer observed the closed window; post-drain check still verified closure")
	}

	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("idempotent Drain: %v", err)
	}
}

func TestQueueFullDropsWithoutBlocking(t *testing.T) {
	sink := &blockingSink{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	b, err := New(sink, Options{
		QueueCapacity: 4,
		BatchSize:     1,
		FlushInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	firstErr := make(chan error, 1)
	go func() {
		firstErr <- b.EnqueueBilling(domain.BillingEvent{ID: domain.NewEventID(), AccountID: "acct"})
	}()
	if err := <-firstErr; err != nil {
		t.Fatalf("first enqueue: %v", err)
	}

	// The writer is now blocked inside WriteBatch, so the channel can fill
	// deterministically.
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter WriteBatch")
	}

	full := false
	for i := 0; i < 100; i++ {
		err := b.EnqueueBilling(domain.BillingEvent{ID: domain.NewEventID(), AccountID: "acct"})
		if errors.Is(err, port.ErrQueueFull) {
			full = true
			break
		}
		if err != nil {
			t.Fatalf("unexpected enqueue error: %v", err)
		}
	}
	if !full {
		t.Fatal("queue never reported ErrQueueFull")
	}

	stats := b.Stats()
	if stats.Dropped == 0 {
		t.Fatalf("Dropped = 0, want > 0; stats = %+v", stats)
	}
	if stats.QueueDepth != 4 {
		t.Fatalf("QueueDepth = %d, want 4", stats.QueueDepth)
	}

	close(sink.release)
	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats := b.Stats(); stats.QueueDepth != 0 {
		t.Fatalf("QueueDepth after Drain = %d, want 0", stats.QueueDepth)
	}
}

// blockingSink blocks every WriteBatch until release is closed and signals the
// first entry so a test can fill the queue deterministically.
type blockingSink struct {
	entered chan struct{}
	release chan struct{}

	mu      sync.Mutex
	batches int
}

func (s *blockingSink) WriteBatch(ctx context.Context, _ port.BatchPayload) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.batches++
	s.mu.Unlock()
	return nil
}

func TestQueueFullCounterDoesNotBlockEnqueue(t *testing.T) {
	sink := &blockingSink{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	b, err := New(sink, Options{QueueCapacity: 1, BatchSize: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		close(sink.release)
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	if err := b.EnqueueBilling(domain.BillingEvent{ID: domain.NewEventID()}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter WriteBatch")
	}
	// One slot is available, so this send succeeds; the next one is full.
	if err := b.EnqueueBilling(domain.BillingEvent{ID: domain.NewEventID()}); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if err := b.EnqueueBilling(domain.BillingEvent{ID: domain.NewEventID()}); !errors.Is(err, port.ErrQueueFull) {
		t.Fatalf("third enqueue error = %v, want ErrQueueFull", err)
	}
	if got := b.Stats().Dropped; got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}
}

// flakySink fails the next N WriteBatch calls, then records payloads.
type flakySink struct {
	failures atomic.Int64
	attempts atomic.Int64

	mu       sync.Mutex
	payloads []port.BatchPayload
}

func (s *flakySink) WriteBatch(_ context.Context, payload port.BatchPayload) error {
	s.attempts.Add(1)
	if s.failures.Add(-1) >= 0 {
		return errors.New("sink unavailable")
	}
	s.mu.Lock()
	s.payloads = append(s.payloads, payload)
	s.mu.Unlock()
	return nil
}

func (s *flakySink) billingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, payload := range s.payloads {
		total += len(payload.BillingEvents)
	}
	return total
}

func testBillingEvent(index int) domain.BillingEvent {
	return domain.BillingEvent{
		ID:         domain.NewEventID(),
		AccountID:  "acct",
		Amount:     -1,
		Reason:     "request",
		RequestID:  fmt.Sprintf("req_%d", index),
		OccurredAt: time.Now(),
	}
}

func TestReservedCommitBillingWithQuotaCommitsBothTogether(t *testing.T) {
	sink := &fakeSink{}
	b, err := New(sink, Options{BatchSize: 2, FlushInterval: 5 * time.Second, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	event := domain.BillingEvent{
		ID:         domain.NewEventID(),
		AccountID:  "acct",
		Amount:     10,
		Reason:     "credit",
		RequestID:  "req_1",
		OccurredAt: time.Now(),
	}
	reservation, err := b.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := reservation.CommitBillingWithQuota(event, domain.AccountQuota{AccountID: "acct", Balance: 10, RateLimitRPM: 60, Seq: 1}); err != nil {
		t.Fatalf("CommitBillingWithQuota: %v", err)
	}
	// One queue slot holds both values.
	if stats := b.Stats(); stats.Enqueued != 1 {
		t.Fatalf("Enqueued = %d after one combined enqueue, want 1", stats.Enqueued)
	}
	// A settled reservation cannot be settled a second time.
	if err := reservation.CommitQuotaSync(domain.AccountQuota{AccountID: "acct", Balance: 99}); err == nil {
		t.Fatal("second settlement of the same reservation succeeded")
	}
	reservation.Release()

	// A later quota for the same account replaces the embedded snapshot while
	// the billing event is preserved.
	if err := b.EnqueueQuotaSync(domain.AccountQuota{AccountID: "acct", Balance: 4, RateLimitRPM: 30, Seq: 2}); err != nil {
		t.Fatalf("EnqueueQuotaSync: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return sink.commitCount() == 1 })
	payloads := sink.payloadSnapshot()
	if len(payloads) != 1 {
		t.Fatalf("commits = %d, want 1", len(payloads))
	}
	payload := payloads[0]
	if len(payload.BillingEvents) != 1 || payload.BillingEvents[0].ID != event.ID {
		t.Fatalf("billing events = %+v, want the combined event", payload.BillingEvents)
	}
	if len(payload.Quotas) != 1 {
		t.Fatalf("quotas = %+v, want a single deduplicated quota", payload.Quotas)
	}
	if got := payload.Quotas[0]; got.AccountID != "acct" || got.Balance != 4 || got.RateLimitRPM != 30 || got.Seq != 2 {
		t.Fatalf("quota = %+v, want latest snapshot 4/30 seq 2", got)
	}
}

func TestEnqueueQuotaSyncBlockingWaitsForSpace(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	b, err := New(sink, Options{
		QueueCapacity: 1,
		BatchSize:     1,
		FlushInterval: time.Hour,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	defer func() {
		release()
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	if err := b.EnqueueBilling(testBillingEvent(0)); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter WriteBatch")
	}
	// The writer is blocked and the single channel slot is taken.
	if err := b.EnqueueBilling(testBillingEvent(1)); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		result <- b.EnqueueQuotaSyncBlocking(context.Background(), domain.AccountQuota{AccountID: "acct", Balance: 5})
	}()
	select {
	case err := <-result:
		t.Fatalf("blocking enqueue returned while full: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("blocking enqueue: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocking enqueue did not complete after space freed")
	}
}

func TestEnqueueQuotaSyncBlockingHonorsCancellation(t *testing.T) {
	sink := &blockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	b, err := New(sink, Options{
		QueueCapacity: 1,
		BatchSize:     1,
		FlushInterval: time.Hour,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	defer func() {
		release()
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	if err := b.EnqueueBilling(testBillingEvent(0)); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter WriteBatch")
	}
	if err := b.EnqueueBilling(testBillingEvent(1)); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- b.EnqueueQuotaSyncBlocking(ctx, domain.AccountQuota{AccountID: "acct", Balance: 5})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocking enqueue error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocking enqueue ignored cancellation")
	}
}

func TestEnqueueQuotaSyncBlockingAfterDrain(t *testing.T) {
	b, err := New(&fakeSink{}, Options{Logger: discardLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := b.EnqueueQuotaSyncBlocking(context.Background(), domain.AccountQuota{AccountID: "acct"}); !errors.Is(err, port.ErrQueueClosed) {
		t.Fatalf("blocking enqueue after Drain error = %v, want ErrQueueClosed", err)
	}
}

func TestFailingSinkRetriesWithExponentialBackoffAndRecovers(t *testing.T) {
	const (
		total     = 40
		batchSize = 8
	)
	sink := &flakySink{}
	sink.failures.Store(3)
	b, err := New(sink, Options{
		QueueCapacity:   128,
		BatchSize:       batchSize,
		FlushInterval:   5 * time.Millisecond,
		MaxRetryBackoff: 20 * time.Millisecond,
		Logger:          discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	for i := 0; i < total; i++ {
		if err := b.EnqueueBilling(testBillingEvent(i)); err != nil {
			t.Fatalf("EnqueueBilling %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	var stats domain.BatcherStats
	for time.Now().Before(deadline) {
		stats = b.Stats()
		// While retrying, the writer must not pull more events into pending.
		if stats.Pending > batchSize {
			t.Fatalf("Pending = %d while retrying, want <= %d", stats.Pending, batchSize)
		}
		if stats.Written == total {
			break
		}
		time.Sleep(time.Millisecond)
	}

	stats = b.Stats()
	if stats.Written != total {
		t.Fatalf("Written = %d, want %d; stats = %+v", stats.Written, total, stats)
	}
	if stats.Errors == 0 {
		t.Fatalf("Errors = 0, want the failed attempts counted")
	}
	if stats.Pending != 0 {
		t.Fatalf("Pending after recovery = %d, want 0", stats.Pending)
	}
	if got := sink.billingCount(); got != total {
		t.Fatalf("persisted billing events = %d, want %d", got, total)
	}
}

func TestDrainWakesDuringRetryBackoff(t *testing.T) {
	const batchSize = 4
	sink := &flakySink{}
	sink.failures.Store(1_000)
	b, err := New(sink, Options{
		QueueCapacity:   16,
		BatchSize:       batchSize,
		FlushInterval:   5 * time.Millisecond,
		MaxRetryBackoff: time.Hour,
		Logger:          discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < batchSize; i++ {
		if err := b.EnqueueBilling(testBillingEvent(i)); err != nil {
			t.Fatalf("EnqueueBilling %d: %v", i, err)
		}
	}
	waitFor(t, 2*time.Second, func() bool { return b.Stats().Errors > 0 })

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- b.Drain(context.Background()) }()

	select {
	case err := <-done:
		// The sink never recovers, so the drain reports the joined commit
		// failures instead of blocking on the long retry timer.
		if err == nil {
			t.Fatal("Drain error = nil, want the persistent sink failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not wake the writer during retry backoff")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Drain took %s, want it to wake immediately", elapsed)
	}

	stats := b.Stats()
	if stats.Errors == 0 || stats.Pending != 0 {
		t.Fatalf("Stats() = %+v, want errors > 0 and pending 0", stats)
	}
}

func TestReserveReleaseIsIdempotentAndReturnsCapacity(t *testing.T) {
	b, err := New(&fakeSink{}, Options{QueueCapacity: 2, BatchSize: 4, FlushInterval: time.Hour, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := b.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	res.Release()
	res.Release()
	if got := len(b.tokens); got != 2 {
		t.Fatalf("tokens after double Release = %d, want 2", got)
	}
	if stats := b.Stats(); stats.Dropped != 0 || stats.Enqueued != 0 {
		t.Fatalf("Stats() = %+v, want no drops or enqueues", stats)
	}

	held := make([]port.WriteReservation, 0, 2)
	for i := 0; i < 2; i++ {
		res, err := b.Reserve()
		if err != nil {
			t.Fatalf("Reserve %d: %v", i, err)
		}
		held = append(held, res)
	}
	if _, err := b.Reserve(); !errors.Is(err, port.ErrQueueFull) {
		t.Fatalf("Reserve beyond capacity error = %v, want ErrQueueFull", err)
	}
	if got := b.Stats().Dropped; got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}
	for _, res := range held {
		res.Release()
	}
	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if _, err := b.Reserve(); !errors.Is(err, port.ErrQueueClosed) {
		t.Fatalf("Reserve after Drain error = %v, want ErrQueueClosed", err)
	}
}

func TestReservationCapacitySaturationStaysBounded(t *testing.T) {
	const capacity = 4
	sink := &blockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	b, err := New(sink, Options{
		QueueCapacity: capacity,
		BatchSize:     1,
		FlushInterval: time.Hour,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(sink.release) }) }
	defer func() {
		release()
		if err := b.Drain(context.Background()); err != nil {
			t.Errorf("Drain: %v", err)
		}
	}()

	// Park the writer inside WriteBatch so the buffer cannot drain itself.
	if err := b.EnqueueBilling(testBillingEvent(0)); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter WriteBatch")
	}

	held := make([]port.WriteReservation, 0, capacity)
	for i := 0; i < capacity; i++ {
		res, err := b.Reserve()
		if err != nil {
			t.Fatalf("Reserve %d: %v", i, err)
		}
		held = append(held, res)
	}
	if got := len(b.ch) + len(held); got != capacity {
		t.Fatalf("len(ch)+reservations = %d, want %d", got, capacity)
	}
	if _, err := b.Reserve(); !errors.Is(err, port.ErrQueueFull) {
		t.Fatalf("over-capacity Reserve error = %v, want ErrQueueFull", err)
	}

	// Committing a reservation consumes its token and fills one buffer slot;
	// the total outstanding capacity use must stay at capacity.
	if err := held[0].CommitQuotaSync(domain.AccountQuota{AccountID: "acct", Balance: 1, Seq: 1}); err != nil {
		t.Fatalf("CommitQuotaSync: %v", err)
	}
	if err := held[1].CommitBillingWithQuota(testBillingEvent(1), domain.AccountQuota{AccountID: "acct", Balance: 2, Seq: 2}); err != nil {
		t.Fatalf("CommitBillingWithQuota: %v", err)
	}
	if got := len(b.ch) + (len(held) - 2); got != capacity {
		t.Fatalf("outstanding capacity = %d, want %d", got, capacity)
	}
	// All tokens are still consumed, so an unreserved enqueue must be rejected
	// instead of exceeding the channel capacity.
	if err := b.EnqueueBilling(testBillingEvent(2)); !errors.Is(err, port.ErrQueueFull) {
		t.Fatalf("unreserved enqueue at saturation error = %v, want ErrQueueFull", err)
	}

	held[2].Release()
	held[3].Release()
	if got := len(b.ch) + 2; got != capacity {
		t.Fatalf("buffer after releases = %d, want %d capacity use", got, capacity)
	}
}

func TestDrainWaitsForInFlightReservationCommit(t *testing.T) {
	sink := &fakeSink{}
	b, err := New(sink, Options{QueueCapacity: 2, BatchSize: 4, FlushInterval: time.Hour, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	reservation, err := b.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	drainResult := make(chan error, 1)
	go func() { drainResult <- b.Drain(context.Background()) }()

	// Drain must wait for the outstanding reservation instead of closing the
	// channel underneath its eventual commit.
	select {
	case err := <-drainResult:
		t.Fatalf("Drain returned before the reservation settled: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	event := testBillingEvent(0)
	quota := domain.AccountQuota{AccountID: "acct", Balance: 9, Seq: 1}
	if err := reservation.CommitBillingWithQuota(event, quota); err != nil {
		t.Fatalf("CommitBillingWithQuota: %v", err)
	}

	select {
	case err := <-drainResult:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not finish after the reservation committed")
	}

	payloads := sink.payloadSnapshot()
	if len(payloads) != 1 || len(payloads[0].BillingEvents) != 1 || payloads[0].BillingEvents[0].ID != event.ID {
		t.Fatalf("payloads = %+v, want the reserved billing event persisted", payloads)
	}
	if len(payloads[0].Quotas) != 1 || payloads[0].Quotas[0].Seq != 1 {
		t.Fatalf("payloads = %+v, want the reserved quota persisted", payloads)
	}
}

func TestDrainTimeoutWithUnreleasedReservationDoesNotPanic(t *testing.T) {
	b, err := New(&fakeSink{}, Options{QueueCapacity: 2, BatchSize: 4, FlushInterval: time.Hour, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	reservation, err := b.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := b.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain error = %v, want context.DeadlineExceeded", err)
	}

	// The channel was not closed on timeout, so the leaked reservation can
	// still be settled safely.
	if err := reservation.CommitQuotaSync(domain.AccountQuota{AccountID: "acct", Balance: 3, Seq: 1}); err != nil {
		t.Fatalf("CommitQuotaSync after timed-out Drain: %v", err)
	}
	reservation.Release()
}

// TestRepeatedDrainAfterTimeoutWithUnreleasedReservation guards the shutdown
// path that used to strand the writer: an early Drain timeout must not be the
// only chance to close the queue. Once the outstanding reservation settles,
// the background closer has to wake the writer, and a later Drain must finish.
func TestRepeatedDrainAfterTimeoutWithUnreleasedReservation(t *testing.T) {
	b, err := New(&fakeSink{}, Options{QueueCapacity: 2, BatchSize: 4, FlushInterval: time.Hour, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	reservation, err := b.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := b.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Drain error = %v, want context.DeadlineExceeded", err)
	}

	// Settling the reservation must let the closer finish the shutdown even
	// though the drain context has already expired.
	reservation.Release()

	second := make(chan error, 1)
	go func() { second <- b.Drain(context.Background()) }()
	select {
	case err := <-second:
		// The recorded outcome may reflect the expired first drain context,
		// but it must never block.
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second Drain error = %v, want nil or the timeout outcome", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Drain hung after the reservation was released")
	}

	select {
	case <-b.done:
	default:
		t.Fatal("writer done channel did not close after the reservation was released")
	}
}

// TestRepeatedDrainAfterCancelledContextWithoutReservation guards the second
// stranding path: with no reservations the closer still has to run, so a
// cancelled first Drain followed by a background Drain can never block.
func TestRepeatedDrainAfterCancelledContextWithoutReservation(t *testing.T) {
	for round := 0; round < 100; round++ {
		b, err := New(&fakeSink{}, Options{QueueCapacity: 2, BatchSize: 4, FlushInterval: time.Hour, Logger: discardLogger()})
		if err != nil {
			t.Fatalf("round %d: New: %v", round, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := b.Drain(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("round %d: first Drain error = %v, want context.Canceled", round, err)
		}

		second := make(chan error, 1)
		go func() { second <- b.Drain(context.Background()) }()
		select {
		case <-second:
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: second Drain hung after a cancelled first Drain", round)
		}

		select {
		case <-b.done:
		default:
			t.Fatalf("round %d: writer done channel did not close", round)
		}
	}
}

func TestBuildPayloadKeepsHighestSeqQuota(t *testing.T) {
	event := testBillingEvent(0)
	payload := buildPayload([]writeEvent{
		{kind: quotaEvent, quota: domain.AccountQuota{AccountID: "acct", Balance: 30, Seq: 3}},
		{kind: quotaEvent, quota: domain.AccountQuota{AccountID: "acct", Balance: 10, Seq: 1}},
		{kind: billingWithQuotaEvent, billing: event, quota: domain.AccountQuota{AccountID: "acct", Balance: 20, Seq: 2}},
		{kind: quotaEvent, quota: domain.AccountQuota{AccountID: "other", Balance: 5, Seq: 1}},
		{kind: quotaEvent, quota: domain.AccountQuota{AccountID: "acct", Balance: 33, Seq: 3}},
	})

	if len(payload.BillingEvents) != 1 || payload.BillingEvents[0].ID != event.ID {
		t.Fatalf("billing events = %+v, want the embedded event preserved", payload.BillingEvents)
	}
	if len(payload.Quotas) != 2 {
		t.Fatalf("quotas = %+v, want two accounts", payload.Quotas)
	}
	quotas := make(map[string]domain.AccountQuota, len(payload.Quotas))
	for _, quota := range payload.Quotas {
		quotas[quota.AccountID] = quota
	}
	if got := quotas["acct"]; got.Seq != 3 || got.Balance != 33 {
		t.Fatalf("acct quota = %+v, want the highest-Seq snapshot 33/3", got)
	}
	if got := quotas["other"]; got.Seq != 1 || got.Balance != 5 {
		t.Fatalf("other quota = %+v", got)
	}
}

func TestFinishStopsCommittingAfterPersistentFailure(t *testing.T) {
	sink := &flakySink{}
	sink.failures.Store(1_000)

	// Build the shutdown path directly so the test controls exactly which
	// events are pending and which are already buffered.
	b := &Batcher{
		sink:            sink,
		batchSize:       2,
		flushInterval:   time.Millisecond,
		maxRetryBackoff: time.Millisecond,
		logger:          discardLogger(),
		ch:              make(chan writeEvent, 8),
		done:            make(chan struct{}),
		stopCh:          make(chan struct{}),
		tokens:          make(chan struct{}, 8),
	}
	for i := 0; i < 8; i++ {
		b.ch <- writeEvent{kind: billingEvent, billing: testBillingEvent(i)}
	}
	close(b.ch)

	b.finish(nil)

	// One chunk with its bounded retries: 1 attempt + 3 retries. The three
	// later chunks must not be attempted at all.
	if got := sink.attempts.Load(); got != 4 {
		t.Fatalf("commit attempts = %d, want 4 (one failed chunk plus retries)", got)
	}
	if err := b.result(); err == nil {
		t.Fatal("drain error = nil, want the persistent sink failure")
	}
	if got := b.Stats().Pending; got != 0 {
		t.Fatalf("Pending = %d, want 0", got)
	}
	if got := b.Stats().QueueDepth; got != 0 {
		t.Fatalf("QueueDepth = %d, want 0", got)
	}
}
