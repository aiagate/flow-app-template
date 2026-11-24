package httpapi

import (
	"net/http"

	"github.com/aiagate/flow-app-template/internal/domain"
)

type dbHealthResponse struct {
	OK                bool   `json:"ok"`
	JournalMode       string `json:"journal_mode"`
	WALAutocheckpoint int64  `json:"wal_autocheckpoint"`
	WALBytes          int64  `json:"wal_bytes"`
	WALWarnBytes      int64  `json:"wal_warn_bytes"`
	WALHealthy        bool   `json:"wal_healthy"`
	ReadQueryOnly     bool   `json:"read_query_only"`
	ReadConnections   int    `json:"read_connections"`
}

type healthResponse struct {
	Status string           `json:"status"`
	DB     dbHealthResponse `json:"db"`
}

func newDBHealthResponse(health domain.DBHealth) dbHealthResponse {
	return dbHealthResponse{
		OK:                health.OK,
		JournalMode:       health.JournalMode,
		WALAutocheckpoint: health.WALAutocheckpoint,
		WALBytes:          health.WALBytes,
		WALWarnBytes:      health.WALWarnBytes,
		WALHealthy:        health.WALHealthy,
		ReadQueryOnly:     health.ReadQueryOnly,
		ReadConnections:   health.ReadConnections,
	}
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	health, err := h.deps.Runtime.Health(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	status := "degraded"
	code := http.StatusServiceUnavailable
	if health.OK {
		status = "ok"
		code = http.StatusOK
	}
	writeJSON(w, code, healthResponse{Status: status, DB: newDBHealthResponse(health)})
}
