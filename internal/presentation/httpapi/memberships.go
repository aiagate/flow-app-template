package httpapi

import (
	"net/http"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

type membershipResponse struct {
	ID        string    `json:"id"`
	TeamID    string    `json:"team_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type membershipEnvelope struct {
	Membership membershipResponse `json:"membership"`
}

type membershipsEnvelope struct {
	Memberships []membershipResponse `json:"memberships"`
}

type joinMembershipRequest struct {
	UserID string `json:"user_id"`
}

type changeRoleRequest struct {
	Role string `json:"role"`
}

func newMembershipResponse(membership *domain.TeamMembership) membershipResponse {
	return membershipResponse{
		ID:        membership.ID().String(),
		TeamID:    membership.TeamID().String(),
		UserID:    membership.UserID().String(),
		Role:      membership.Role().String(),
		Status:    membership.Status().String(),
		Version:   membership.Version().Int64(),
		CreatedAt: membership.CreatedAt(),
		UpdatedAt: membership.UpdatedAt(),
	}
}

func (h *Handler) joinTeam(w http.ResponseWriter, r *http.Request) {
	h.createMembership(w, r, false)
}

func (h *Handler) requestJoinTeam(w http.ResponseWriter, r *http.Request) {
	h.createMembership(w, r, true)
}

func (h *Handler) createMembership(w http.ResponseWriter, r *http.Request, pending bool) {
	var request joinMembershipRequest
	if err := decodeJSON(r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	ctx := r.Context()
	input := usecase.JoinTeamInput{
		TeamID: r.PathValue("team_id"),
		UserID: request.UserID,
	}
	var (
		membership *domain.TeamMembership
		err        error
	)
	if pending {
		membership, err = h.deps.Memberships.RequestJoinTeam(ctx, input)
	} else {
		membership, err = h.deps.Memberships.JoinTeam(ctx, input)
	}
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, membershipEnvelope{Membership: newMembershipResponse(membership)})
}

func (h *Handler) listTeamMemberships(w http.ResponseWriter, r *http.Request) {
	memberships, err := h.deps.Memberships.ListTeamMemberships(r.Context(), r.PathValue("team_id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	response := membershipsEnvelope{Memberships: make([]membershipResponse, 0, len(memberships))}
	for _, membership := range memberships {
		response.Memberships = append(response.Memberships, newMembershipResponse(membership))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) approveJoinRequest(w http.ResponseWriter, r *http.Request) {
	membership, err := h.deps.Memberships.ApproveJoinRequest(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipEnvelope{Membership: newMembershipResponse(membership)})
}

func (h *Handler) leaveTeam(w http.ResponseWriter, r *http.Request) {
	membership, err := h.deps.Memberships.LeaveTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipEnvelope{Membership: newMembershipResponse(membership)})
}

func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request) {
	var request changeRoleRequest
	if err := decodeJSON(r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	membership, err := h.deps.Memberships.ChangeRole(r.Context(), r.PathValue("id"), request.Role)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, membershipEnvelope{Membership: newMembershipResponse(membership)})
}
