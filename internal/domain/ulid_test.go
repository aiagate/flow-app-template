package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestULIDValueObjects(t *testing.T) {
	run := func(
		name string,
		newID func() string,
		parse func(string) (string, error),
		isZero func(string) bool,
		zero string,
	) {
		t.Run(name, func(t *testing.T) {
			generated := newID()
			if len(generated) != 26 {
				t.Fatalf("generated ID length = %d, want 26", len(generated))
			}
			if isZero(generated) {
				t.Fatal("generated ID must not be zero")
			}
			parsed, err := parse(generated)
			if err != nil {
				t.Fatalf("Parse(%q): %v", generated, err)
			}
			if parsed != generated {
				t.Fatalf("round trip = %q, want %q", parsed, generated)
			}

			if !isZero(zero) {
				t.Fatalf("zero value must report zero: %q", zero)
			}

			invalid := []string{
				"",
				"not-a-ulid",
				strings.Repeat("0", 25),
				strings.Repeat("0", 27),
				"01ARZ3NDEKTSV4RRFFQ69G5FAI",
				" 01ARZ3NDEKTSV4RRFFQ69G5FAV",
				"01ARZ3NDEKTSV4RRFFQ69G5FAV ",
			}
			for _, raw := range invalid {
				if _, err := parse(raw); err == nil {
					t.Fatalf("Parse(%q) succeeded, want validation error", raw)
				} else if !errors.Is(err, ErrValidation) {
					t.Fatalf("Parse(%q) error = %v, want ErrValidation", raw, err)
				}
			}
		})
	}

	run("user",
		func() string { return NewUserID().String() },
		func(raw string) (string, error) {
			id, err := ParseUserID(raw)
			return id.String(), err
		},
		func(raw string) bool {
			id, err := ParseUserID(raw)
			if err != nil {
				return false
			}
			return id.IsZero()
		},
		UserID{}.String(),
	)

	run("team",
		func() string { return NewTeamID().String() },
		func(raw string) (string, error) {
			id, err := ParseTeamID(raw)
			return id.String(), err
		},
		func(raw string) bool {
			id, err := ParseTeamID(raw)
			if err != nil {
				return false
			}
			return id.IsZero()
		},
		TeamID{}.String(),
	)

	run("membership",
		func() string { return NewMembershipID().String() },
		func(raw string) (string, error) {
			id, err := ParseMembershipID(raw)
			return id.String(), err
		},
		func(raw string) bool {
			id, err := ParseMembershipID(raw)
			if err != nil {
				return false
			}
			return id.IsZero()
		},
		MembershipID{}.String(),
	)

	run("event",
		func() string { return NewEventID().String() },
		func(raw string) (string, error) {
			id, err := ParseEventID(raw)
			return id.String(), err
		},
		func(raw string) bool {
			id, err := ParseEventID(raw)
			if err != nil {
				return false
			}
			return id.IsZero()
		},
		EventID{}.String(),
	)
}

func TestULIDUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := NewUserID().String()
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate ULID generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewRequestID(t *testing.T) {
	first := NewRequestID()
	second := NewRequestID()
	if len(first) != 26 || len(second) != 26 {
		t.Fatalf("request IDs = %q, %q; want 26-character ULIDs", first, second)
	}
	if first == second {
		t.Fatal("request IDs must be unique")
	}
	if _, err := ulid.ParseStrict(first); err != nil {
		t.Fatalf("request ID is not a canonical ULID: %v", err)
	}
}
