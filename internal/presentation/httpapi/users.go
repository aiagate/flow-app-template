package httpapi

import (
	"net/http"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

type userResponse struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type userEnvelope struct {
	User userResponse `json:"user"`
}

type usersEnvelope struct {
	Users []userResponse `json:"users"`
}

type createUserRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

type changeUserEmailRequest struct {
	Email           string `json:"email"`
	ExpectedVersion *int64 `json:"expected_version"`
}

func newUserResponse(user *domain.User) userResponse {
	return userResponse{
		ID:          user.ID().String(),
		DisplayName: user.DisplayName().String(),
		Email:       user.Email().String(),
		Version:     user.Version().Int64(),
		CreatedAt:   user.CreatedAt(),
		UpdatedAt:   user.UpdatedAt(),
	}
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var request createUserRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	user, err := h.deps.Users.CreateUser(r.Context(), usecase.CreateUserInput{
		DisplayName: request.DisplayName,
		Email:       request.Email,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, userEnvelope{User: newUserResponse(user)})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.deps.Users.ListUsers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	response := usersEnvelope{Users: make([]userResponse, 0, len(users))}
	for _, user := range users {
		response.Users = append(response.Users, newUserResponse(user))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	user, err := h.deps.Users.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, userEnvelope{User: newUserResponse(user)})
}

func (h *Handler) changeUserEmail(w http.ResponseWriter, r *http.Request) {
	var request changeUserEmailRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, err)
		return
	}
	user, err := h.deps.Users.ChangeUserEmail(r.Context(), usecase.ChangeUserEmailInput{
		UserID:          r.PathValue("id"),
		Email:           request.Email,
		ExpectedVersion: request.ExpectedVersion,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, userEnvelope{User: newUserResponse(user)})
}
