package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/aiagate/flow-app-template/internal/usecase"
)

const maxJSONBodyBytes = 1 << 20 // 1 MiB

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// statusForCode is the single status mapping used by every endpoint.
func statusForCode(code usecase.ErrorCode) int {
	switch code {
	case usecase.CodeValidation:
		return http.StatusBadRequest
	case usecase.CodeUnauthorized:
		return http.StatusUnauthorized
	case usecase.CodeForbidden:
		return http.StatusForbidden
	case usecase.CodeInsufficientBalance:
		return http.StatusPaymentRequired
	case usecase.CodeNotFound:
		return http.StatusNotFound
	case usecase.CodeConflict, usecase.CodeConcurrencyConflict:
		return http.StatusConflict
	case usecase.CodeUnsupportedMediaType:
		return http.StatusUnsupportedMediaType
	case usecase.CodeRateLimited:
		return http.StatusTooManyRequests
	case usecase.CodeRouteDisabled, usecase.CodeServiceUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// writeError renders the shared JSON error envelope, adds the bearer challenge
// for 401 responses and logs server-side failures.
func (h *Handler) writeError(w http.ResponseWriter, err error) {
	classified := usecase.ClassifyError(err)
	if classified == nil {
		classified = usecase.NewError(usecase.CodeUnexpected, "unexpected error")
	}
	status := statusForCode(classified.Code)
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status >= http.StatusInternalServerError {
		h.logger.Error("request failed",
			slog.Int("status", status),
			slog.String("code", string(classified.Code)),
			slog.String("message", classified.Message),
			slog.Any("error", classified.Err),
		)
	}
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: errorBody{Code: string(classified.Code), Message: classified.Message},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// decodeJSON reads exactly one JSON object, mapping a missing or wrong media
// type to UNSUPPORTED_MEDIA_TYPE and malformed or oversized bodies to
// VALIDATION_ERROR.
func decodeJSON(r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return usecase.NewError(usecase.CodeUnsupportedMediaType, "Content-Type must be application/json")
	}
	if r.Body == nil {
		return usecase.NewError(usecase.CodeValidation, "request body is required")
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(destination); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return usecase.NewError(usecase.CodeValidation, "request body must not exceed 1 MiB")
		}
		if errors.Is(err, io.EOF) {
			return usecase.NewError(usecase.CodeValidation, "request body must be a JSON object")
		}
		return usecase.NewError(usecase.CodeValidation, "request body must be valid JSON: "+err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return usecase.NewError(usecase.CodeValidation, "request body must contain a single JSON object")
	}
	return nil
}
