package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAccountID(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trimmed", raw: "  acct-42  ", want: "acct-42"},
		{name: "colon dot dash underscore", raw: "tenant:acct.1_2-3", want: "tenant:acct.1_2-3"},
		{name: "ulid", raw: "01ARZ3NDEKTSV4RRFFQ69G5FAV", want: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{name: "uuid", raw: "018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b", want: "018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b"},
		{name: "exact max length", raw: strings.Repeat("a", MaxAccountIDLength), want: strings.Repeat("a", MaxAccountIDLength)},
	}
	for _, test := range valid {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateAccountID(test.raw)
			if err != nil {
				t.Fatalf("ValidateAccountID(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ValidateAccountID(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "spaces only", raw: "   "},
		{name: "over max length", raw: strings.Repeat("a", MaxAccountIDLength+1)},
		{name: "newline inside", raw: "acct\nid"},
		{name: "carriage return inside", raw: "acct\rid"},
		{name: "null byte inside", raw: "acct\x00id"},
		{name: "space inside", raw: "acct id"},
		{name: "unicode", raw: "acct-é"},
		{name: "non-ascii", raw: "日本語"},
		{name: "slash", raw: "acct/id"},
		{name: "at sign", raw: "acct@id"},
	}
	for _, test := range invalid {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateAccountID(test.raw)
			if err == nil {
				t.Fatalf("ValidateAccountID(%q) = %q, want error", test.raw, got)
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("ValidateAccountID(%q) error = %v, want ErrValidation", test.raw, err)
			}
			if !strings.Contains(err.Error(), "account_id") {
				t.Fatalf("ValidateAccountID(%q) error = %q, want field account_id", test.raw, err)
			}
		})
	}
}

func TestValidateRequestID(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trimmed", raw: "  req-42  ", want: "req-42"},
		{name: "colon dot dash underscore", raw: "req:1.2_3-4", want: "req:1.2_3-4"},
		{name: "ulid", raw: "01ARZ3NDEKTSV4RRFFQ69G5FAV", want: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{name: "uuid", raw: "018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b", want: "018f2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b"},
		{name: "exact max length", raw: strings.Repeat("a", MaxRequestIDLength), want: strings.Repeat("a", MaxRequestIDLength)},
	}
	for _, test := range valid {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateRequestID(test.raw)
			if err != nil {
				t.Fatalf("ValidateRequestID(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("ValidateRequestID(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "spaces only", raw: "   "},
		{name: "over max length", raw: strings.Repeat("a", MaxRequestIDLength+1)},
		{name: "newline inside", raw: "req\nid"},
		{name: "carriage return inside", raw: "req\rid"},
		{name: "null byte inside", raw: "req\x00id"},
		{name: "space inside", raw: "req id"},
		{name: "unicode", raw: "req-é"},
		{name: "non-ascii", raw: "日本語"},
		{name: "slash", raw: "req/id"},
		{name: "at sign", raw: "req@id"},
	}
	for _, test := range invalid {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateRequestID(test.raw)
			if err == nil {
				t.Fatalf("ValidateRequestID(%q) = %q, want error", test.raw, got)
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("ValidateRequestID(%q) error = %v, want ErrValidation", test.raw, err)
			}
			if !strings.Contains(err.Error(), "request_id") {
				t.Fatalf("ValidateRequestID(%q) error = %q, want field request_id", test.raw, err)
			}
		})
	}
}
