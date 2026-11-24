// Package httpapi exposes the JSON API and the embedded console. It depends on
// contract ports and use cases only; persistence and hot-path implementations
// are supplied by the composition root.
package httpapi

import (
	"fmt"
	"io/fs"
	"net/http"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/usecase"
	"github.com/aiagate/flow-app-template/web"
)

// Dependencies is the complete set of capabilities the HTTP layer needs.
type Dependencies struct {
	Users       *usecase.UserUseCase
	Teams       *usecase.TeamUseCase
	Memberships *usecase.MembershipUseCase
	Billing     *usecase.BillingUseCase
	Runtime     *usecase.RuntimeUseCase
	HotPath     port.HotPathGuard
	Queue       port.WriteEventQueue
}

// Handler owns the route table and the embedded console assets.
type Handler struct {
	deps     Dependencies
	mux      *http.ServeMux
	index    []byte
	staticFS fs.FS
	assets   http.Handler
	root     http.Handler
}

// ServeHTTP runs the fully wrapped middleware chain.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.root.ServeHTTP(w, r)
}

// NewHandler builds the fully wrapped HTTP handler, including the panic,
// request-ID, body-limit, account, request-log and hot-path middlewares.
func NewHandler(deps Dependencies) (http.Handler, error) {
	if deps.Users == nil || deps.Teams == nil || deps.Memberships == nil ||
		deps.Billing == nil || deps.Runtime == nil || deps.HotPath == nil || deps.Queue == nil {
		return nil, fmt.Errorf("httpapi: every dependency must be provided")
	}

	staticFS, err := web.StaticFS()
	if err != nil {
		return nil, fmt.Errorf("httpapi: open embedded static assets: %w", err)
	}
	index, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		return nil, fmt.Errorf("httpapi: read embedded index.html: %w", err)
	}

	handler := &Handler{
		deps:     deps,
		mux:      http.NewServeMux(),
		index:    index,
		staticFS: staticFS,
		assets:   http.StripPrefix("/assets/", http.FileServerFS(staticFS)),
	}
	handler.register()
	handler.root = handler.wrap()
	return handler, nil
}

func (h *Handler) register() {
	// Embedded console.
	h.mux.HandleFunc("GET /{$}", h.serveIndex)
	h.mux.Handle("GET /assets/{path...}", http.HandlerFunc(h.serveAsset))

	// Unmetered control-plane and ledger routes.
	h.mux.HandleFunc("GET /healthz", h.health)
	h.mux.HandleFunc("GET /api/v1/runtime/config", h.getRuntimeConfig)
	h.mux.HandleFunc("PUT /api/v1/runtime/config", h.updateRuntimeConfig)
	h.mux.HandleFunc("GET /api/v1/runtime/stats", h.getRuntimeStats)
	h.mux.HandleFunc("GET /api/v1/accounts/{account_id}", h.getAccount)
	h.mux.HandleFunc("POST /api/v1/accounts/{account_id}/credit", h.creditAccount)
	h.mux.HandleFunc("GET /api/v1/billing/events", h.listBillingEvents)
	h.mux.HandleFunc("GET /api/v1/logs", h.listRequestLogs)

	// Metered directory routes.
	h.metered("POST /api/v1/users", h.createUser)
	h.metered("GET /api/v1/users", h.listUsers)
	h.metered("GET /api/v1/users/{id}", h.getUser)
	h.metered("PUT /api/v1/users/{id}/email", h.changeUserEmail)

	h.metered("POST /api/v1/teams", h.createTeam)
	h.metered("GET /api/v1/teams", h.listTeams)
	h.metered("GET /api/v1/teams/{id}", h.getTeam)
	h.metered("PUT /api/v1/teams/{id}", h.updateTeam)

	h.metered("POST /api/v1/teams/{team_id}/memberships/join", h.joinTeam)
	h.metered("POST /api/v1/teams/{team_id}/memberships/request", h.requestJoinTeam)
	h.metered("GET /api/v1/teams/{team_id}/memberships", h.listTeamMemberships)
	h.metered("POST /api/v1/memberships/{id}/approve", h.approveJoinRequest)
	h.metered("POST /api/v1/memberships/{id}/leave", h.leaveTeam)
	h.metered("PUT /api/v1/memberships/{id}/role", h.changeRole)

	// The charge handler owns the single explicit debit, so the guard only
	// authorizes and rate-limits this route.
	h.mux.Handle("POST /api/v1/accounts/{account_id}/charge",
		h.withGuard("POST /api/v1/accounts/{account_id}/charge", false, h.withPanicRecovery(http.HandlerFunc(h.chargeAccount))))

	// Unknown paths and unsupported methods still receive the JSON envelope.
	h.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, usecase.NewError(usecase.CodeNotFound, "route not found"))
	}))
}

func (h *Handler) metered(pattern string, endpoint http.HandlerFunc) {
	h.mux.Handle(pattern, h.withGuard(pattern, true, h.withPanicRecovery(endpoint)))
}

func (h *Handler) wrap() http.Handler {
	var handler http.Handler = h.mux
	handler = h.withPanicRecovery(handler)
	handler = withAccountID(handler)
	handler = withBodyLimit(handler)
	handler = withRequestID(handler)
	handler = h.withRequestLog(handler)
	return handler
}

func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("path")
	if name == "" {
		writeError(w, usecase.NewError(usecase.CodeNotFound, "asset not found"))
		return
	}
	if _, err := fs.Stat(h.staticFS, name); err != nil {
		writeError(w, usecase.NewError(usecase.CodeNotFound, "asset not found"))
		return
	}
	h.assets.ServeHTTP(w, r)
}

func (h *Handler) serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.index)
}
