package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDisplayNameValidation(t *testing.T) {
	valid := []string{
		"A",
		"Alice",
		"Alice Smith",
		strings.Repeat("あ", 100),
	}
	for _, raw := range valid {
		t.Run("valid/"+raw, func(t *testing.T) {
			name, err := NewDisplayName(raw)
			if err != nil {
				t.Fatalf("NewDisplayName(%q): %v", raw, err)
			}
			if name.String() != raw {
				t.Fatalf("String() = %q, want %q", name.String(), raw)
			}
			if name.IsZero() {
				t.Fatal("valid name must not be zero")
			}
		})
	}

	invalid := []string{
		"",
		strings.Repeat("a", 101),
		strings.Repeat("あ", 101),
		" Alice",
		"Alice ",
		"\tAlice",
		"Alice\n",
	}
	for _, raw := range invalid {
		t.Run("invalid/"+raw, func(t *testing.T) {
			_, err := NewDisplayName(raw)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("NewDisplayName(%q) error = %v, want ErrValidation", raw, err)
			}
		})
	}

	if !(DisplayName{}).IsZero() {
		t.Fatal("zero DisplayName must report zero")
	}
	if got := utf8.RuneCountInString(strings.Repeat("あ", 100)); got != 100 {
		t.Fatalf("test fixture rune count = %d", got)
	}
}

func TestEmailValidation(t *testing.T) {
	valid := map[string]string{
		"alice@example.com":               "alice@example.com",
		"  alice@example.com  ":           "alice@example.com",
		"a.b+c_d%e-f@sub.example.co.uk":   "a.b+c_d%e-f@sub.example.co.uk",
		"USER@EXAMPLE.COM":                "USER@EXAMPLE.COM",
		"first.last@teams.example.museum": "first.last@teams.example.museum",
	}
	for raw, want := range valid {
		t.Run("valid/"+raw, func(t *testing.T) {
			email, err := NewEmail(raw)
			if err != nil {
				t.Fatalf("NewEmail(%q): %v", raw, err)
			}
			if email.String() != want {
				t.Fatalf("String() = %q, want %q", email.String(), want)
			}
		})
	}

	invalid := []string{
		"",
		"   ",
		"plainaddress",
		"@example.com",
		"alice@",
		"alice@example",
		"alice@example.c",
		"alice..bob@example.com",
		"alice@example..com",
		"alice bob@example.com",
		"alice@exa mple.com",
		"alice@-example.com",
		"alice@example-.com",
		"a@" + strings.Repeat("b", 250) + ".com",
	}
	for _, raw := range invalid {
		t.Run("invalid/"+raw, func(t *testing.T) {
			_, err := NewEmail(raw)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("NewEmail(%q) error = %v, want ErrValidation", raw, err)
			}
		})
	}

	if !(Email{}).IsZero() {
		t.Fatal("zero Email must report zero")
	}
}

func TestVersion(t *testing.T) {
	version, err := NewVersion(0)
	if err != nil {
		t.Fatalf("NewVersion(0): %v", err)
	}
	if !version.IsZero() {
		t.Fatal("version 0 must be zero")
	}
	next := version.Next()
	if next.Int64() != 1 {
		t.Fatalf("Next().Int64() = %d, want 1", next.Int64())
	}
	if version.Int64() != 0 {
		t.Fatal("Next must not mutate the receiver")
	}

	if _, err := NewVersion(-1); !errors.Is(err, ErrValidation) {
		t.Fatalf("NewVersion(-1) error = %v, want ErrValidation", err)
	}
	if !(Version{}).IsZero() {
		t.Fatal("zero Version must report zero")
	}
}

func TestUserAggregate(t *testing.T) {
	before := time.Now().UTC()
	user := RegisterUser(mustDisplayName(t, "Alice"), mustEmail(t, "alice@example.com"))
	after := time.Now().UTC()

	if user.ID().IsZero() {
		t.Fatal("registered user has zero ID")
	}
	if user.Version().Int64() != 0 {
		t.Fatalf("version = %d, want 0", user.Version().Int64())
	}
	if user.DisplayName().String() != "Alice" {
		t.Fatalf("display name = %q", user.DisplayName().String())
	}
	if user.Email().String() != "alice@example.com" {
		t.Fatalf("email = %q", user.Email().String())
	}
	if user.CreatedAt().Before(before) || user.CreatedAt().After(after) {
		t.Fatalf("created_at %s outside [%s, %s]", user.CreatedAt(), before, after)
	}
	if user.UpdatedAt() != user.CreatedAt() {
		t.Fatalf("updated_at %s != created_at %s", user.UpdatedAt(), user.CreatedAt())
	}

	oldUpdated := user.UpdatedAt()
	user.ChangeEmail(mustEmail(t, "new@example.com"))
	if user.Email().String() != "new@example.com" {
		t.Fatalf("email after change = %q", user.Email().String())
	}
	if user.UpdatedAt().Before(oldUpdated) {
		t.Fatal("updated_at moved backwards")
	}
	if user.Version().Int64() != 0 {
		t.Fatal("domain mutation must leave persistence version untouched")
	}

	restored := RestoreUser(
		user.ID(),
		mustDisplayName(t, "Alice"),
		mustEmail(t, "alice@example.com"),
		mustVersion(t, 7),
		user.CreatedAt(),
		user.UpdatedAt(),
	)
	if restored.Version().Int64() != 7 {
		t.Fatalf("restored version = %d", restored.Version().Int64())
	}
	if !restored.Equal(user) {
		t.Fatal("restored aggregate must keep identity")
	}

	sameIdentity := RestoreUser(
		user.ID(),
		mustDisplayName(t, "Someone Else"),
		mustEmail(t, "other@example.com"),
		mustVersion(t, 1),
		time.Now().UTC(),
		time.Now().UTC(),
	)
	if !user.Equal(sameIdentity) {
		t.Fatal("identity comparison must ignore state")
	}
	other := RegisterUser(mustDisplayName(t, "Bob"), mustEmail(t, "bob@example.com"))
	if user.Equal(other) {
		t.Fatal("different IDs must not compare equal")
	}
	if user.Equal(nil) {
		t.Fatal("nil must not compare equal")
	}
	var nilUser *User
	if nilUser.Equal(nil) {
		t.Fatal("nil receiver must not compare equal")
	}
}
