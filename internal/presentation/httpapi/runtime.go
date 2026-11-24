package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

type routePolicyResponse struct {
	Enabled      bool  `json:"enabled"`
	Cost         int64 `json:"cost"`
	RateLimitRPM int   `json:"rate_limit_rpm"`
}

type runtimeConfigResponse struct {
	DefaultRateLimitRPM int                            `json:"default_rate_limit_rpm"`
	DefaultRequestCost  int64                          `json:"default_request_cost"`
	MaintenanceMode     bool                           `json:"maintenance_mode"`
	Routes              map[string]routePolicyResponse `json:"routes"`
}

type runtimeConfigRequest struct {
	DefaultRateLimitRPM int           `json:"default_rate_limit_rpm"`
	DefaultRequestCost  int64         `json:"default_request_cost"`
	MaintenanceMode     bool          `json:"maintenance_mode"`
	Routes              routesPayload `json:"routes"`
}

// routesPayload accepts both the object shape the console writes and an array
// of route entries.
type routesPayload struct {
	policies map[string]domain.RoutePolicy
}

func (p *routesPayload) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		p.policies = nil
		return nil
	}

	switch trimmed[0] {
	case '{':
		var raw map[string]struct {
			Enabled      bool  `json:"enabled"`
			Cost         int64 `json:"cost"`
			RateLimitRPM int   `json:"rate_limit_rpm"`
		}
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		p.policies = make(map[string]domain.RoutePolicy, len(raw))
		for name, policy := range raw {
			p.policies[name] = domain.RoutePolicy{
				Enabled:      policy.Enabled,
				Cost:         policy.Cost,
				RateLimitRPM: policy.RateLimitRPM,
			}
		}
		return nil
	case '[':
		var raw []struct {
			Route        string `json:"route"`
			Name         string `json:"name"`
			Path         string `json:"path"`
			Enabled      bool   `json:"enabled"`
			Cost         int64  `json:"cost"`
			RateLimitRPM int    `json:"rate_limit_rpm"`
		}
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		p.policies = make(map[string]domain.RoutePolicy, len(raw))
		seen := make(map[string]struct{}, len(raw))
		for _, entry := range raw {
			name := entry.Route
			if name == "" {
				name = entry.Name
			}
			if name == "" {
				name = entry.Path
			}
			name = strings.TrimSpace(name)
			if _, exists := seen[name]; exists {
				return fmt.Errorf("routes must not contain duplicate names")
			}
			seen[name] = struct{}{}
			p.policies[name] = domain.RoutePolicy{
				Enabled:      entry.Enabled,
				Cost:         entry.Cost,
				RateLimitRPM: entry.RateLimitRPM,
			}
		}
		return nil
	default:
		return fmt.Errorf("routes must be an object or an array")
	}
}

type runtimeStatsResponse struct {
	HotPath hotPathStatsResponse `json:"hotpath"`
	Batcher batcherStatsResponse `json:"batcher"`
	DB      dbHealthResponse     `json:"db"`
}

type hotPathStatsResponse struct {
	Requests            int64 `json:"requests"`
	Allowed             int64 `json:"allowed"`
	RateLimited         int64 `json:"rate_limited"`
	InsufficientBalance int64 `json:"insufficient_balance"`
	RouteDisabled       int64 `json:"route_disabled"`
	MaintenanceRejected int64 `json:"maintenance_rejected"`
}

type batcherStatsResponse struct {
	Enqueued      int64 `json:"enqueued"`
	Written       int64 `json:"written"`
	Dropped       int64 `json:"dropped"`
	Pending       int   `json:"pending"`
	Errors        int64 `json:"errors"`
	QueueDepth    int   `json:"queue_depth"`
	QueueCapacity int   `json:"queue_capacity"`
	FlushCommits  int64 `json:"flush_commits"`
}

func (h *Handler) getRuntimeConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, newRuntimeConfigResponse(h.deps.Runtime.GetRuntimeConfig()))
}

func (h *Handler) updateRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	var request runtimeConfigRequest
	if err := decodeJSON(r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	config, err := h.deps.Runtime.UpdateRuntimeConfig(r.Context(), usecase.UpdateRuntimeConfigInput{
		DefaultRateLimitRPM: request.DefaultRateLimitRPM,
		DefaultRequestCost:  request.DefaultRequestCost,
		MaintenanceMode:     request.MaintenanceMode,
		Routes:              request.Routes.policies,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newRuntimeConfigResponse(config))
}

func (h *Handler) getRuntimeStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.deps.Runtime.GetRuntimeStats(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runtimeStatsResponse{
		HotPath: hotPathStatsResponse{
			Requests:            stats.HotPath.Requests,
			Allowed:             stats.HotPath.Allowed,
			RateLimited:         stats.HotPath.RateLimited,
			InsufficientBalance: stats.HotPath.InsufficientBalance,
			RouteDisabled:       stats.HotPath.RouteDisabled,
			MaintenanceRejected: stats.HotPath.MaintenanceRejected,
		},
		Batcher: batcherStatsResponse{
			Enqueued:      stats.Batcher.Enqueued,
			Written:       stats.Batcher.Written,
			Dropped:       stats.Batcher.Dropped,
			Pending:       stats.Batcher.Pending,
			Errors:        stats.Batcher.Errors,
			QueueDepth:    stats.Batcher.QueueDepth,
			QueueCapacity: stats.Batcher.QueueCapacity,
			FlushCommits:  stats.Batcher.FlushCommits,
		},
		DB: newDBHealthResponse(stats.DB),
	})
}

func newRuntimeConfigResponse(config *domain.RuntimeConfig) runtimeConfigResponse {
	response := runtimeConfigResponse{
		Routes: make(map[string]routePolicyResponse, len(config.Routes)),
	}
	if config == nil {
		return response
	}
	response.DefaultRateLimitRPM = config.DefaultRateLimitRPM
	response.DefaultRequestCost = config.DefaultRequestCost
	response.MaintenanceMode = config.MaintenanceMode
	for name, policy := range config.Routes {
		response.Routes[name] = routePolicyResponse{
			Enabled:      policy.Enabled,
			Cost:         policy.Cost,
			RateLimitRPM: policy.RateLimitRPM,
		}
	}
	return response
}
