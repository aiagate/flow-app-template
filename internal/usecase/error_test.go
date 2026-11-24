package usecase

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code ErrorCode
	}{
		{"nil", nil, ""},
		{"validation", &domain.ValidationError{Field: "email", Reason: "invalid"}, CodeValidation},
		{"invalid transition", domain.ErrInvalidTransition, CodeConflict},
		{"insufficient balance", domain.ErrInsufficientBalance, CodeInsufficientBalance},
		{"rate limited", domain.ErrRateLimited, CodeRateLimited},
		{"route disabled", domain.ErrRouteDisabled, CodeRouteDisabled},
		{"not found", fmt.Errorf("get user: %w", port.ErrNotFound), CodeNotFound},
		{"already exists", fmt.Errorf("create user: %w", port.ErrAlreadyExists), CodeConflict},
		{"version conflict", fmt.Errorf("update team: %w", port.ErrVersionConflict), CodeConcurrencyConflict},
		{"unknown", errors.New("driver exploded"), CodeUnexpected},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			classified := ClassifyError(testCase.err)
			if testCase.err == nil {
				if classified != nil {
					t.Fatalf("ClassifyError(nil) = %+v, want nil", classified)
				}
				return
			}
			if classified == nil {
				t.Fatal("ClassifyError returned nil")
			}
			if classified.Code != testCase.code {
				t.Fatalf("code = %q, want %q", classified.Code, testCase.code)
			}
			if !errors.Is(classified, testCase.err) {
				t.Fatalf("classified error does not wrap the original: %v", classified)
			}
			if testCase.code == CodeUnexpected && classified.Message == "driver exploded" {
				t.Fatal("unexpected errors must not leak the underlying message")
			}
		})
	}
}

func TestClassifyErrorKeepsUseCaseError(t *testing.T) {
	original := NewError(CodeConflict, "custom conflict")
	classified := ClassifyError(fmt.Errorf("handler: %w", original))
	if classified != original {
		t.Fatalf("ClassifyError returned a different error: %+v", classified)
	}
	if classified.Error() != "custom conflict" {
		t.Fatalf("message = %q", classified.Error())
	}
}

func TestUseCaseErrorMessageFallbacks(t *testing.T) {
	override := &UseCaseError{Code: CodeValidation, Message: "explicit"}
	if override.Error() != "explicit" {
		t.Fatalf("message = %q", override.Error())
	}
	caused := &UseCaseError{Code: CodeUnexpected, Err: errors.New("cause")}
	if caused.Error() != "cause" {
		t.Fatalf("message = %q", caused.Error())
	}
	codeOnly := &UseCaseError{Code: CodeNotFound}
	if codeOnly.Error() != "NOT_FOUND" {
		t.Fatalf("message = %q", codeOnly.Error())
	}
	var nilErr *UseCaseError
	if nilErr.Error() != "" {
		t.Fatalf("nil message = %q", nilErr.Error())
	}
}
