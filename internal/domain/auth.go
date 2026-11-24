package domain

import (
	"fmt"
	"strings"
)

const (
	// MaxAccountIDLength is the maximum byte length of an account identifier.
	MaxAccountIDLength = 64
	// MaxRequestIDLength is the maximum byte length of a request identifier.
	MaxRequestIDLength = 128
)

// Principal is an authenticated caller of the API.
type Principal struct {
	Subject   string
	AccountID string
	IsAdmin   bool
}

// ValidateAccountID trims raw and checks that it is a non-empty identifier of
// at most MaxAccountIDLength bytes. Only letters, digits, '.', '_', ':' and
// '-' are allowed.
func ValidateAccountID(raw string) (string, error) {
	return validateIdentifier("account_id", raw, MaxAccountIDLength)
}

// ValidateRequestID trims raw and checks that it is a non-empty identifier of
// at most MaxRequestIDLength bytes. Only letters, digits, '.', '_', ':' and
// '-' are allowed.
func ValidateRequestID(raw string) (string, error) {
	return validateIdentifier("request_id", raw, MaxRequestIDLength)
}

// validateIdentifier applies the shared identifier rules: trim surrounding
// whitespace, require a non-empty value within the byte limit, and restrict
// every byte to the allowed character set.
func validateIdentifier(field, raw string, maxLength int) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", validationError(field, "must not be empty")
	}
	if len(value) > maxLength {
		return "", validationError(field, fmt.Sprintf("must not exceed %d bytes", maxLength))
	}
	for i := 0; i < len(value); i++ {
		if !isIdentifierByte(value[i]) {
			return "", validationError(field, "must contain only letters, digits, '.', '_', ':' or '-'")
		}
	}
	return value, nil
}

// isIdentifierByte reports whether b belongs to the allowed identifier
// character set. It works on bytes so non-ASCII input is rejected.
func isIdentifierByte(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z':
		return true
	case b >= 'a' && b <= 'z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '.', b == '_', b == ':', b == '-':
		return true
	default:
		return false
	}
}
