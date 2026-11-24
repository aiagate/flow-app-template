package httpapi

import (
	"net/http"

	"github.com/aiagate/flow-app-template/internal/domain"
)

type dbHealthResponse struct {
	OK                bool   `json:"ok"`
	JournalMode       string `json:"journal_mode"`
	WALAutocheckpoint int64  `json:"wal_autocheckpoint"`
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
		ReadQueryOnly:     health.ReadQueryOnly,
		ReadConnections:   health.ReadConnections,
	}
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	health, err := h.deps.Runtime.Health(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	status := "degraded"
	if health.OK {
		status = "ok"
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: status, DB: newDBHealthResponse(health)})
}
