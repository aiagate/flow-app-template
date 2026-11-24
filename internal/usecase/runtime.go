package usecase

import (
	"context"
	"strings"
	"sync"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// RuntimeStats is the combined hot-path, writer-queue and database snapshot.
type RuntimeStats struct {
	HotPath domain.HotPathStats
	Batcher domain.BatcherStats
	DB      domain.DBHealth
}

// RuntimeUseCase owns the control-plane endpoints.
type RuntimeUseCase struct {
	guard      port.HotPathGuard
	queue      port.WriteEventQueue
	db         port.DBHealthChecker
	configRepo port.RuntimeConfigRepository

	// updateMu serializes the persist-then-swap critical section so two
	// concurrent updates cannot commit to SQLite in one order and replace the
	// hot-path snapshot in the reverse order.
	updateMu sync.Mutex
}

func NewRuntimeUseCase(
	guard port.HotPathGuard,
	queue port.WriteEventQueue,
	db port.DBHealthChecker,
	configRepo port.RuntimeConfigRepository,
) *RuntimeUseCase {
	return &RuntimeUseCase{guard: guard, queue: queue, db: db, configRepo: configRepo}
}

// GetRuntimeConfig returns a defensive copy of the immutable hot-path snapshot.
func (r *RuntimeUseCase) GetRuntimeConfig() *domain.RuntimeConfig {
	config := r.guard.CurrentConfig()
	if config == nil {
		return &domain.RuntimeConfig{}
	}
	return config.Clone()
}

type UpdateRuntimeConfigInput struct {
	DefaultRateLimitRPM int
	DefaultRequestCost  int64
	MaintenanceMode     bool
	Routes              map[string]domain.RoutePolicy
}

// UpdateRuntimeConfig validates the candidate snapshot, persists it when a
// repository is configured, and only then atomically swaps it into the hot
// path. A failed save leaves the in-memory snapshot untouched so a restart
// cannot silently roll back a configuration the operator never saw accepted.
func (r *RuntimeUseCase) UpdateRuntimeConfig(ctx context.Context, input UpdateRuntimeConfigInput) (*domain.RuntimeConfig, error) {
	if input.DefaultRateLimitRPM <= 0 {
		return nil, validationError("default_rate_limit_rpm", "must be positive")
	}
	if input.DefaultRequestCost < 0 {
		return nil, validationError("default_request_cost", "must be non-negative")
	}

	routes := make(map[string]domain.RoutePolicy, len(input.Routes))
	for rawName, policy := range input.Routes {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, validationError("routes", "route names must not be empty")
		}
		if _, exists := routes[name]; exists {
			return nil, validationError("routes", "route names must be unique")
		}
		if policy.Cost < 0 {
			return nil, validationError("routes."+name+".cost", "must be non-negative")
		}
		if policy.RateLimitRPM < 0 {
			return nil, validationError("routes."+name+".rate_limit_rpm", "must be non-negative")
		}
		routes[name] = policy
	}

	next := &domain.RuntimeConfig{
		DefaultRateLimitRPM: input.DefaultRateLimitRPM,
		DefaultRequestCost:  input.DefaultRequestCost,
		MaintenanceMode:     input.MaintenanceMode,
		Routes:              routes,
	}

	// Hold the lock across persistence and the snapshot swap. Saving and
	// swapping separately would let two accepted updates interleave: SQLite
	// could keep update B while the hot path ends up with update A.
	r.updateMu.Lock()
	defer r.updateMu.Unlock()

	if r.configRepo != nil {
		if err := r.configRepo.Save(ctx, next); err != nil {
			return nil, ClassifyError(err)
		}
	}
	r.guard.UpdateConfig(next)
	return next.Clone(), nil
}

// GetRuntimeStats reads every counter without touching the write path.
func (r *RuntimeUseCase) GetRuntimeStats(ctx context.Context) (RuntimeStats, error) {
	health, err := r.db.Health(ctx)
	if err != nil {
		return RuntimeStats{}, ClassifyError(err)
	}
	return RuntimeStats{
		HotPath: r.guard.Stats(),
		Batcher: r.queue.Stats(),
		DB:      health,
	}, nil
}

// Health reads the live database PRAGMAs.
func (r *RuntimeUseCase) Health(ctx context.Context) (domain.DBHealth, error) {
	health, err := r.db.Health(ctx)
	if err != nil {
		return domain.DBHealth{}, ClassifyError(err)
	}
	return health, nil
}
