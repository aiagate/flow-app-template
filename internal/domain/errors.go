package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by domain rules. Callers should compare with
// errors.Is so that wrapped details remain available.
var (
	ErrValidation          = errors.New("validation failed")
	ErrInvalidTransition   = errors.New("invalid state transition")
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrRateLimited         = errors.New("rate limited")
	ErrRouteDisabled       = errors.New("route disabled")
)

// ValidationError describes which value failed validation and why.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("validation failed: %s", e.Reason)
	}
	return fmt.Sprintf("validation failed for %s: %s", e.Field, e.Reason)
}

// Unwrap makes errors.Is(err, ErrValidation) true for every validation error.
func (e *ValidationError) Unwrap() error { return ErrValidation }

func validationError(field, reason string) error {
	return &ValidationError{Field: field, Reason: reason}
}
