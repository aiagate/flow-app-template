package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/aiagate/flow-app-template/internal/domain"
)

func newRuntimeForTest(health domain.DBHealth, healthErr error) (*RuntimeUseCase, *fakeGuard, *fakeQueue) {
	guard := newFakeGuard(nil)
	queue := &fakeQueue{}
	return NewRuntimeUseCase(guard, queue, fakeHealth{health: health, err: healthErr}), guard, queue
}

func TestGetRuntimeConfigReturnsAClone(t *testing.T) {
	runtime, guard, _ := newRuntimeForTest(domain.DBHealth{OK: true}, nil)
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
	runtime, guard, _ := newRuntimeForTest(domain.DBHealth{OK: true}, nil)

	updated, err := runtime.UpdateRuntimeConfig(UpdateRuntimeConfigInput{
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
		if _, err := runtime.UpdateRuntimeConfig(input); err == nil {
			t.Fatalf("case %d: UpdateRuntimeConfig succeeded, want validation error", index)
		} else if ClassifyError(err).Code != CodeValidation {
			t.Fatalf("case %d: code = %s", index, ClassifyError(err).Code)
		}
	}
}

func TestRuntimeStatsAndHealth(t *testing.T) {
	ctx := context.Background()
	health := domain.DBHealth{OK: true, JournalMode: "wal", ReadConnections: 5}
	runtime, guard, queue := newRuntimeForTest(health, nil)
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

	runtime, _, _ = newRuntimeForTest(domain.DBHealth{}, errors.New("database down"))
	_, err = runtime.GetRuntimeStats(ctx)
	assertCode(t, err, CodeUnexpected)
	_, err = runtime.Health(ctx)
	assertCode(t, err, CodeUnexpected)
}
