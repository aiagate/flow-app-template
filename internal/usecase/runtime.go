package usecase

import (
	"context"
	"strings"

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
	guard port.HotPathGuard
	queue port.WriteEventQueue
	db    port.DBHealthChecker
}

func NewRuntimeUseCase(guard port.HotPathGuard, queue port.WriteEventQueue, db port.DBHealthChecker) *RuntimeUseCase {
	return &RuntimeUseCase{guard: guard, queue: queue, db: db}
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

// UpdateRuntimeConfig validates the candidate snapshot and atomically swaps it
// into the hot path. Existing route names are replaced, not merged.
func (r *RuntimeUseCase) UpdateRuntimeConfig(input UpdateRuntimeConfigInput) (*domain.RuntimeConfig, error) {
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

	r.guard.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: input.DefaultRateLimitRPM,
		DefaultRequestCost:  input.DefaultRequestCost,
		MaintenanceMode:     input.MaintenanceMode,
		Routes:              routes,
	})
	return r.GetRuntimeConfig(), nil
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
