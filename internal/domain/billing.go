package domain

import "time"

// AccountQuota is the persisted balance and rate limit for an account.
type AccountQuota struct {
	AccountID    string
	Balance      int64
	RateLimitRPM int
	UpdatedAt    time.Time
}

// BillingEvent is an append-only balance movement.
type BillingEvent struct {
	ID         EventID
	AccountID  string
	Amount     int64
	Reason     string
	RequestID  string
	OccurredAt time.Time
}

// RequestLog is an append-only record of an API request.
type RequestLog struct {
	ID         string
	RequestID  string
	AccountID  string
	Method     string
	Path       string
	StatusCode int
	LatencyUS  int64
	OccurredAt time.Time
}

// RoutePolicy is the per-route hot-path configuration.
type RoutePolicy struct {
	Enabled      bool
	Cost         int64
	RateLimitRPM int
}

// RuntimeConfig is the immutable snapshot read by the hot path.
type RuntimeConfig struct {
	DefaultRateLimitRPM int
	DefaultRequestCost  int64
	MaintenanceMode     bool
	Routes              map[string]RoutePolicy
}

// Clone returns a deep copy so a snapshot can be handed to readers without
// sharing the mutable Routes map.
func (c *RuntimeConfig) Clone() *RuntimeConfig {
	if c == nil {
		return nil
	}
	var routes map[string]RoutePolicy
	if c.Routes != nil {
		routes = make(map[string]RoutePolicy, len(c.Routes))
		for name, policy := range c.Routes {
			routes[name] = policy
		}
	}
	return &RuntimeConfig{
		DefaultRateLimitRPM: c.DefaultRateLimitRPM,
		DefaultRequestCost:  c.DefaultRequestCost,
		MaintenanceMode:     c.MaintenanceMode,
		Routes:              routes,
	}
}

// AccountState is the in-memory view of an account held by the hot path.
type AccountState struct {
	AccountID    string
	Balance      int64
	RateLimitRPM int
	UpdatedAt    time.Time
}

// HotPathDecision reports the result of a hot-path admission attempt.
type HotPathDecision struct {
	Allowed          bool
	Reason           string
	Cost             int64
	RemainingBalance int64
	RemainingTokens  int64
	// RateLimitRPM is the effective requests-per-minute limit enforced for the
	// request. Zero means unlimited.
	RateLimitRPM int
}

// HotPathStats is a snapshot of hot-path counters.
type HotPathStats struct {
	Requests            int64
	Allowed             int64
	RateLimited         int64
	InsufficientBalance int64
	RouteDisabled       int64
	MaintenanceRejected int64
}

// BatcherStats is a snapshot of asynchronous write-queue counters.
type BatcherStats struct {
	Enqueued      int64
	Written       int64
	Dropped       int64
	Pending       int
	Errors        int64
	QueueDepth    int
	QueueCapacity int
	FlushCommits  int64
}

// DBHealth is the startup/runtime health view built from actual PRAGMAs.
type DBHealth struct {
	OK                bool
	JournalMode       string
	WALAutocheckpoint int64
	WALBytes          int64
	WALWarnBytes      int64
	WALHealthy        bool
	ReadQueryOnly     bool
	ReadConnections   int
}
