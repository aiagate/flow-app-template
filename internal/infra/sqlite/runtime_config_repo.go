package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// runtimeConfigRepo persists the single runtime configuration row as JSON.
type runtimeConfigRepo struct {
	store *Store
}

var _ port.RuntimeConfigRepository = (*runtimeConfigRepo)(nil)

// NewRuntimeConfigRepository returns a repository for the runtime config row.
func NewRuntimeConfigRepository(store *Store) port.RuntimeConfigRepository {
	return &runtimeConfigRepo{store: store}
}

type runtimeConfigDTO struct {
	DefaultRateLimitRPM int                       `json:"default_rate_limit_rpm"`
	DefaultRequestCost  int64                     `json:"default_request_cost"`
	MaintenanceMode     bool                      `json:"maintenance_mode"`
	Routes              map[string]routePolicyDTO `json:"routes"`
}

type routePolicyDTO struct {
	Enabled      bool  `json:"enabled"`
	Cost         int64 `json:"cost"`
	RateLimitRPM int   `json:"rate_limit_rpm"`
}

func (r *runtimeConfigRepo) Load(ctx context.Context) (*domain.RuntimeConfig, bool, error) {
	var encoded string
	err := r.store.ReadDB.QueryRowContext(ctx,
		`SELECT config_json FROM runtime_config WHERE id = 1`).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load runtime config: %w", err)
	}

	var dto runtimeConfigDTO
	if err := json.Unmarshal([]byte(encoded), &dto); err != nil {
		return nil, false, fmt.Errorf("decode runtime config: %w", err)
	}

	config := &domain.RuntimeConfig{
		DefaultRateLimitRPM: dto.DefaultRateLimitRPM,
		DefaultRequestCost:  dto.DefaultRequestCost,
		MaintenanceMode:     dto.MaintenanceMode,
	}
	if dto.Routes != nil {
		config.Routes = make(map[string]domain.RoutePolicy, len(dto.Routes))
		for name, policy := range dto.Routes {
			config.Routes[name] = domain.RoutePolicy{
				Enabled:      policy.Enabled,
				Cost:         policy.Cost,
				RateLimitRPM: policy.RateLimitRPM,
			}
		}
	}
	return config, true, nil
}

func (r *runtimeConfigRepo) Save(ctx context.Context, config *domain.RuntimeConfig) error {
	if config == nil {
		return fmt.Errorf("save runtime config: config must not be nil")
	}

	dto := runtimeConfigDTO{
		DefaultRateLimitRPM: config.DefaultRateLimitRPM,
		DefaultRequestCost:  config.DefaultRequestCost,
		MaintenanceMode:     config.MaintenanceMode,
	}
	if config.Routes != nil {
		dto.Routes = make(map[string]routePolicyDTO, len(config.Routes))
		for name, policy := range config.Routes {
			dto.Routes[name] = routePolicyDTO{
				Enabled:      policy.Enabled,
				Cost:         policy.Cost,
				RateLimitRPM: policy.RateLimitRPM,
			}
		}
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		return fmt.Errorf("encode runtime config: %w", err)
	}

	return r.store.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO runtime_config (id, config_json, updated_at)
			VALUES (1, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				config_json = excluded.config_json,
				updated_at = excluded.updated_at`,
			string(encoded), formatTimestamp(time.Now()),
		); err != nil {
			return fmt.Errorf("save runtime config: %w", err)
		}
		return nil
	})
}
