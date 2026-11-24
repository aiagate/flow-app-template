// Package usecase implements the application services. It depends only on the
// domain model and the contract ports; persistence, transport and infrastructure
// details stay outside this package.
package usecase

import (
	"errors"
	"fmt"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// ErrorCode is the transport-neutral classification of a use-case failure.
type ErrorCode string

const (
	CodeValidation           ErrorCode = "VALIDATION_ERROR"
	CodeInsufficientBalance  ErrorCode = "INSUFFICIENT_BALANCE"
	CodeNotFound             ErrorCode = "NOT_FOUND"
	CodeConflict             ErrorCode = "CONFLICT"
	CodeConcurrencyConflict  ErrorCode = "CONCURRENCY_CONFLICT"
	CodeRateLimited          ErrorCode = "RATE_LIMITED"
	CodeRouteDisabled        ErrorCode = "ROUTE_DISABLED"
	CodeUnauthorized         ErrorCode = "UNAUTHORIZED"
	CodeForbidden            ErrorCode = "FORBIDDEN"
	CodeServiceUnavailable   ErrorCode = "SERVICE_UNAVAILABLE"
	CodeUnsupportedMediaType ErrorCode = "UNSUPPORTED_MEDIA_TYPE"
	CodeUnexpected           ErrorCode = "UNEXPECTED"
)

// UseCaseError carries a stable error code and a user-facing message. The
// underlying cause stays available through errors.Is/errors.As.
type UseCaseError struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *UseCaseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Code)
}

func (e *UseCaseError) Unwrap() error { return e.Err }

// NewError builds a classified error without a wrapped cause.
func NewError(code ErrorCode, message string) *UseCaseError {
	return &UseCaseError{Code: code, Message: message}
}

// WrapError builds a classified error that keeps the underlying cause.
func WrapError(code ErrorCode, message string, err error) *UseCaseError {
	return &UseCaseError{Code: code, Message: message, Err: err}
}

// ClassifyError maps domain and repository sentinels to a stable application
// error. Unknown causes are deliberately hidden behind a generic message.
func ClassifyError(err error) *UseCaseError {
	if err == nil {
		return nil
	}
	var useCaseErr *UseCaseError
	if errors.As(err, &useCaseErr) {
		return useCaseErr
	}

	switch {
	case errors.Is(err, domain.ErrValidation):
		return WrapError(CodeValidation, err.Error(), err)
	case errors.Is(err, domain.ErrInvalidTransition):
		return WrapError(CodeConflict, err.Error(), err)
	case errors.Is(err, domain.ErrInsufficientBalance):
		return WrapError(CodeInsufficientBalance, "insufficient balance", err)
	case errors.Is(err, domain.ErrRateLimited):
		return WrapError(CodeRateLimited, "rate limit exceeded", err)
	case errors.Is(err, domain.ErrRouteDisabled):
		return WrapError(CodeRouteDisabled, "route is disabled", err)
	case errors.Is(err, domain.ErrUnauthorized):
		return WrapError(CodeUnauthorized, "authentication required", err)
	case errors.Is(err, domain.ErrForbidden):
		return WrapError(CodeForbidden, "access denied", err)
	case errors.Is(err, domain.ErrTooManyAccounts):
		return WrapError(CodeServiceUnavailable, "account capacity reached", err)
	case errors.Is(err, port.ErrQueueFull):
		return WrapError(CodeServiceUnavailable, "write queue is full", err)
	case errors.Is(err, port.ErrQueueClosed):
		return WrapError(CodeServiceUnavailable, "service is shutting down", err)
	case errors.Is(err, port.ErrNotFound):
		return WrapError(CodeNotFound, "resource not found", err)
	case errors.Is(err, port.ErrAlreadyExists):
		return WrapError(CodeConflict, "resource already exists", err)
	case errors.Is(err, port.ErrVersionConflict):
		return WrapError(CodeConcurrencyConflict, "version conflict", err)
	default:
		return WrapError(CodeUnexpected, "unexpected error", err)
	}
}

func validationError(field, reason string) *UseCaseError {
	return NewError(CodeValidation, fmt.Sprintf("%s: %s", field, reason))
}
