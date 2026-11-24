package httpapi

import (
	"net/http"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

type teamResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type teamEnvelope struct {
	Team teamResponse `json:"team"`
}

type teamsEnvelope struct {
	Teams []teamResponse `json:"teams"`
}

type createTeamRequest struct {
	Name string `json:"name"`
}

type updateTeamRequest struct {
	Name            string `json:"name"`
	ExpectedVersion *int64 `json:"expected_version"`
}

func newTeamResponse(team *domain.Team) teamResponse {
	return teamResponse{
		ID:        team.ID().String(),
		Name:      team.Name().String(),
		Version:   team.Version().Int64(),
		CreatedAt: team.CreatedAt(),
		UpdatedAt: team.UpdatedAt(),
	}
}

func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	var request createTeamRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	team, err := h.deps.Teams.CreateTeam(r.Context(), usecase.CreateTeamInput{Name: request.Name})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, teamEnvelope{Team: newTeamResponse(team)})
}

func (h *Handler) listTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := h.deps.Teams.ListTeams(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	response := teamsEnvelope{Teams: make([]teamResponse, 0, len(teams))}
	for _, team := range teams {
		response.Teams = append(response.Teams, newTeamResponse(team))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getTeam(w http.ResponseWriter, r *http.Request) {
	team, err := h.deps.Teams.GetTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, teamEnvelope{Team: newTeamResponse(team)})
}

func (h *Handler) updateTeam(w http.ResponseWriter, r *http.Request) {
	var request updateTeamRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	team, err := h.deps.Teams.UpdateTeam(r.Context(), usecase.UpdateTeamInput{
		TeamID:          r.PathValue("id"),
		Name:            request.Name,
		ExpectedVersion: request.ExpectedVersion,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, teamEnvelope{Team: newTeamResponse(team)})
}
