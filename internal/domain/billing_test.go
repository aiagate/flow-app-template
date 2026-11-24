package domain

import (
	"testing"
	"time"
)

func TestRuntimeConfigClone(t *testing.T) {
	original := &RuntimeConfig{
		DefaultRateLimitRPM: 120,
		DefaultRequestCost:  1,
		MaintenanceMode:     false,
		Routes: map[string]RoutePolicy{
			"GET /users":  {Enabled: true, Cost: 1, RateLimitRPM: 120},
			"POST /users": {Enabled: false, Cost: 2, RateLimitRPM: 30},
		},
	}

	clone := original.Clone()
	if clone == original {
		t.Fatal("Clone must return a new snapshot")
	}
	if clone.DefaultRateLimitRPM != original.DefaultRateLimitRPM ||
		clone.DefaultRequestCost != original.DefaultRequestCost ||
		clone.MaintenanceMode != original.MaintenanceMode {
		t.Fatal("Clone did not copy scalar settings")
	}
	if len(clone.Routes) != len(original.Routes) {
		t.Fatal("Clone did not copy routes")
	}

	clone.Routes["GET /users"] = RoutePolicy{Enabled: false, Cost: 99, RateLimitRPM: 1}
	clone.Routes["GET /health"] = RoutePolicy{Enabled: true}
	if original.Routes["GET /users"].Cost != 1 {
		t.Fatal("mutating clone changed the original route map")
	}
	if _, ok := original.Routes["GET /health"]; ok {
		t.Fatal("adding to clone changed the original route map")
	}

	var nilConfig *RuntimeConfig
	if nilConfig.Clone() != nil {
		t.Fatal("nil Clone must stay nil")
	}

	empty := (&RuntimeConfig{}).Clone()
	if empty == nil {
		t.Fatal("Clone must not return nil for a non-nil config")
	}
	if empty.Routes != nil {
		t.Fatal("Clone must preserve a nil route map")
	}
}

func TestBillingValueTypes(t *testing.T) {
	occurred := time.Now().UTC()
	event := BillingEvent{
		ID:         NewEventID(),
		AccountID:  "acct_1",
		Amount:     -5,
		Reason:     "request",
		RequestID:  "req_1",
		OccurredAt: occurred,
	}
	if event.ID.IsZero() {
		t.Fatal("event ID must not be zero")
	}
	if event.OccurredAt != occurred {
		t.Fatal("event timestamp was not preserved")
	}

	logRow := RequestLog{
		ID:         "log_1",
		RequestID:  "req_1",
		AccountID:  "acct_1",
		Method:     "GET",
		Path:       "/users",
		StatusCode: 200,
		LatencyUS:  42,
		OccurredAt: occurred,
	}
	if logRow.LatencyUS != 42 {
		t.Fatal("request log was not preserved")
	}

	state := AccountState{AccountID: "acct_1", Balance: 10, RateLimitRPM: 120}
	if state.Balance != 10 {
		t.Fatal("account state was not preserved")
	}
}
