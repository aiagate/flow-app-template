package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
)

func newRuntimeForTest(health domain.DBHealth, healthErr error) (*RuntimeUseCase, *fakeGuard, *fakeQueue, *fakeRuntimeConfig) {
	guard := newFakeGuard(nil)
	queue := &fakeQueue{}
	configRepo := &fakeRuntimeConfig{}
	return NewRuntimeUseCase(guard, queue, fakeHealth{health: health, err: healthErr}, configRepo), guard, queue, configRepo
}

func TestGetRuntimeConfigReturnsAClone(t *testing.T) {
	runtime, guard, _, _ := newRuntimeForTest(domain.DBHealth{OK: true}, nil)
	guard.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: 60,
		DefaultRequestCost:  2,
		Routes:              map[string]domain.RoutePolicy{"GET /api/v1/users": {Enabled: true, Cost: 1, RateLimitRPM: 30}},
	})

	config := runtime.GetRuntimeConfig()
	if config.DefaultRateLimitRPM != 60 || config.DefaultRequestCost != 2 {
		t.Fatalf("config = %+v", config)
	}
	config.Routes["GET /api/v1/users"] = domain.RoutePolicy{Enabled: false}
	config.DefaultRateLimitRPM = 1

	again := runtime.GetRuntimeConfig()
	if again.DefaultRateLimitRPM != 60 || !again.Routes["GET /api/v1/users"].Enabled {
		t.Fatalf("GetRuntimeConfig did not return a defensive copy: %+v", again)
	}

	guard.config = nil
	empty := runtime.GetRuntimeConfig()
	if empty == nil || empty.DefaultRateLimitRPM != 0 {
		t.Fatalf("nil snapshot = %+v", empty)
	}
}

func TestUpdateRuntimeConfigValidation(t *testing.T) {
	ctx := context.Background()
	runtime, guard, _, _ := newRuntimeForTest(domain.DBHealth{OK: true}, nil)

	updated, err := runtime.UpdateRuntimeConfig(ctx, UpdateRuntimeConfigInput{
		DefaultRateLimitRPM: 30,
		DefaultRequestCost:  3,
		MaintenanceMode:     true,
		Routes: map[string]domain.RoutePolicy{
			" GET /api/v1/users ": {Enabled: false, Cost: 2, RateLimitRPM: 5},
		},
	})
	if err != nil {
		t.Fatalf("UpdateRuntimeConfig: %v", err)
	}
	if updated.DefaultRateLimitRPM != 30 || !updated.MaintenanceMode {
		t.Fatalf("updated config = %+v", updated)
	}
	policy, ok := updated.Routes["GET /api/v1/users"]
	if !ok || policy.Enabled || policy.Cost != 2 || policy.RateLimitRPM != 5 {
		t.Fatalf("routes = %+v", updated.Routes)
	}
	if guard.CurrentConfig().DefaultRateLimitRPM != 30 {
		t.Fatal("hot path did not receive the new snapshot")
	}

	cases := []UpdateRuntimeConfigInput{
		{DefaultRateLimitRPM: 0, DefaultRequestCost: 1},
		{DefaultRateLimitRPM: 1, DefaultRequestCost: -1},
		{DefaultRateLimitRPM: 1, Routes: map[string]domain.RoutePolicy{"  ": {Enabled: true}}},
		{DefaultRateLimitRPM: 1, Routes: map[string]domain.RoutePolicy{"GET /x": {Enabled: true, Cost: -1}}},
		{DefaultRateLimitRPM: 1, Routes: map[string]domain.RoutePolicy{"GET /x": {Enabled: true, RateLimitRPM: -1}}},
	}
	for index, input := range cases {
		if _, err := runtime.UpdateRuntimeConfig(ctx, input); err == nil {
			t.Fatalf("case %d: UpdateRuntimeConfig succeeded, want validation error", index)
		} else if ClassifyError(err).Code != CodeValidation {
			t.Fatalf("case %d: code = %s", index, ClassifyError(err).Code)
		}
	}
}

func TestUpdateRuntimeConfigPersistsBeforeSwapping(t *testing.T) {
	ctx := context.Background()
	runtime, guard, _, configRepo := newRuntimeForTest(domain.DBHealth{OK: true}, nil)
	guard.UpdateConfig(&domain.RuntimeConfig{DefaultRateLimitRPM: 90, DefaultRequestCost: 1})

	var observed int
	configRepo.onSave = func() {
		observed = guard.CurrentConfig().DefaultRateLimitRPM
	}

	updated, err := runtime.UpdateRuntimeConfig(ctx, UpdateRuntimeConfigInput{
		DefaultRateLimitRPM: 45,
		DefaultRequestCost:  2,
	})
	if err != nil {
		t.Fatalf("UpdateRuntimeConfig: %v", err)
	}
	if observed != 90 {
		t.Fatalf("guard was updated before persistence: observed %d", observed)
	}
	if updated.DefaultRateLimitRPM != 45 || guard.CurrentConfig().DefaultRateLimitRPM != 45 {
		t.Fatalf("updated = %+v, guard = %+v", updated, guard.CurrentConfig())
	}
	if configRepo.saved == nil || configRepo.saved.DefaultRateLimitRPM != 45 || configRepo.saved.DefaultRequestCost != 2 {
		t.Fatalf("saved config = %+v", configRepo.saved)
	}

	// Persisted snapshots are defensively copied: mutating the returned value
	// must not change what was stored.
	updated.DefaultRateLimitRPM = 1
	if configRepo.saved.DefaultRateLimitRPM != 45 {
		t.Fatal("Save kept a reference to the caller's snapshot")
	}
}

func TestUpdateRuntimeConfigSerializesSaveAndSwap(t *testing.T) {
	ctx := context.Background()
	guard := newFakeGuard(nil)
	configRepo := &blockingRuntimeConfig{
		firstSaveStarted: make(chan struct{}),
		releaseFirstSave: make(chan struct{}),
	}
	runtime := NewRuntimeUseCase(
		guard,
		&fakeQueue{},
		fakeHealth{health: domain.DBHealth{OK: true}},
		configRepo,
	)

	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.UpdateRuntimeConfig(ctx, UpdateRuntimeConfigInput{DefaultRateLimitRPM: 10})
		firstDone <- err
	}()
	<-configRepo.firstSaveStarted

	secondDone := make(chan error, 1)
	go func() {
		_, err := runtime.UpdateRuntimeConfig(ctx, UpdateRuntimeConfigInput{DefaultRateLimitRPM: 20})
		secondDone <- err
	}()

	// The second update must stay outside Save while the first critical
	// section is open. Without the mutex it persists and swaps here, and the
	// first goroutine's later swap leaves memory and SQLite disagreeing.
	select {
	case err := <-secondDone:
		close(configRepo.releaseFirstSave)
		<-firstDone
		if err != nil {
			t.Fatalf("second UpdateRuntimeConfig: %v", err)
		}
		t.Fatal("second UpdateRuntimeConfig completed before the first released its critical section")
	case <-time.After(200 * time.Millisecond):
	}

	close(configRepo.releaseFirstSave)
	if err := <-firstDone; err != nil {
		t.Fatalf("first UpdateRuntimeConfig: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second UpdateRuntimeConfig: %v", err)
	}

	persisted := configRepo.lastSaved()
	live := guard.CurrentConfig()
	if persisted == nil || live == nil {
		t.Fatalf("persisted = %+v, live = %+v", persisted, live)
	}
	if live.DefaultRateLimitRPM != persisted.DefaultRateLimitRPM {
		t.Fatalf("hot path config = %d, last persisted = %d", live.DefaultRateLimitRPM, persisted.DefaultRateLimitRPM)
	}
	if persisted.DefaultRateLimitRPM != 20 {
		t.Fatalf("last persisted config = %d, want 20", persisted.DefaultRateLimitRPM)
	}
}

// blockingRuntimeConfig persists every snapshot in call order and blocks the
// first Save until the test releases it, so interleaving is observable.
type blockingRuntimeConfig struct {
	mu               sync.Mutex
	saved            *domain.RuntimeConfig
	saves            int
	firstSaveStarted chan struct{}
	releaseFirstSave chan struct{}
}

func (c *blockingRuntimeConfig) Load(context.Context) (*domain.RuntimeConfig, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saved == nil {
		return nil, false, nil
	}
	return c.saved.Clone(), true, nil
}

func (c *blockingRuntimeConfig) Save(_ context.Context, cfg *domain.RuntimeConfig) error {
	c.mu.Lock()
	c.saves++
	call := c.saves
	c.saved = cfg.Clone()
	c.mu.Unlock()

	if call == 1 {
		close(c.firstSaveStarted)
		<-c.releaseFirstSave
	}
	return nil
}

func (c *blockingRuntimeConfig) lastSaved() *domain.RuntimeConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saved.Clone()
}

func TestUpdateRuntimeConfigSaveFailureLeavesMemoryUntouched(t *testing.T) {
	ctx := context.Background()
	runtime, guard, _, configRepo := newRuntimeForTest(domain.DBHealth{OK: true}, nil)
	guard.UpdateConfig(&domain.RuntimeConfig{DefaultRateLimitRPM: 90, DefaultRequestCost: 1})
	configRepo.saveErr = errors.New("disk is full")

	_, err := runtime.UpdateRuntimeConfig(ctx, UpdateRuntimeConfigInput{
		DefaultRateLimitRPM: 45,
		DefaultRequestCost:  2,
	})
	assertCode(t, err, CodeUnexpected)
	if got := guard.CurrentConfig().DefaultRateLimitRPM; got != 90 {
		t.Fatalf("guard config changed after failed save: %d", got)
	}
	if configRepo.saved != nil {
		t.Fatalf("failed save stored a snapshot: %+v", configRepo.saved)
	}
}

func TestUpdateRuntimeConfigWithoutRepository(t *testing.T) {
	ctx := context.Background()
	guard := newFakeGuard(nil)
	runtime := NewRuntimeUseCase(guard, &fakeQueue{}, fakeHealth{health: domain.DBHealth{OK: true}}, nil)

	updated, err := runtime.UpdateRuntimeConfig(ctx, UpdateRuntimeConfigInput{
		DefaultRateLimitRPM: 12,
		DefaultRequestCost:  1,
	})
	if err != nil {
		t.Fatalf("UpdateRuntimeConfig: %v", err)
	}
	if updated.DefaultRateLimitRPM != 12 || guard.CurrentConfig().DefaultRateLimitRPM != 12 {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestRuntimeStatsAndHealth(t *testing.T) {
	ctx := context.Background()
	health := domain.DBHealth{OK: true, JournalMode: "wal", ReadConnections: 5}
	runtime, guard, queue, _ := newRuntimeForTest(health, nil)
	guard.stats = domain.HotPathStats{Requests: 4, Allowed: 3, RateLimited: 1}
	queue.stats = domain.BatcherStats{Enqueued: 10, Written: 9, Dropped: 1, QueueDepth: 1, QueueCapacity: 8192, FlushCommits: 3}

	stats, err := runtime.GetRuntimeStats(ctx)
	if err != nil {
		t.Fatalf("GetRuntimeStats: %v", err)
	}
	if stats.HotPath.Requests != 4 || stats.Batcher.Written != 9 || stats.DB.ReadConnections != 5 {
		t.Fatalf("stats = %+v", stats)
	}

	gotHealth, err := runtime.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !gotHealth.OK || gotHealth.JournalMode != "wal" {
		t.Fatalf("health = %+v", gotHealth)
	}

	runtime, _, _, _ = newRuntimeForTest(domain.DBHealth{}, errors.New("database down"))
	_, err = runtime.GetRuntimeStats(ctx)
	assertCode(t, err, CodeUnexpected)
	_, err = runtime.Health(ctx)
	assertCode(t, err, CodeUnexpected)
}
