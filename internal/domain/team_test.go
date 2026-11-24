package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTeamNameValidation(t *testing.T) {
	valid := []string{"A", "Engineering", "プロダクト", strings.Repeat("t", 100)}
	for _, raw := range valid {
		t.Run("valid/"+raw, func(t *testing.T) {
			name, err := NewTeamName(raw)
			if err != nil {
				t.Fatalf("NewTeamName(%q): %v", raw, err)
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
		strings.Repeat("t", 101),
		strings.Repeat("あ", 101),
		" Engineering",
		"Engineering ",
		"\nEngineering",
	}
	for _, raw := range invalid {
		t.Run("invalid/"+raw, func(t *testing.T) {
			if _, err := NewTeamName(raw); !errors.Is(err, ErrValidation) {
				t.Fatalf("NewTeamName(%q) error = %v, want ErrValidation", raw, err)
			}
		})
	}

	if !(TeamName{}).IsZero() {
		t.Fatal("zero TeamName must report zero")
	}
}

func TestTeamAggregate(t *testing.T) {
	before := time.Now().UTC()
	team := FormTeam(mustTeamName(t, "Engineering"))
	after := time.Now().UTC()

	if team.ID().IsZero() {
		t.Fatal("formed team has zero ID")
	}
	if team.Version().Int64() != 0 {
		t.Fatalf("version = %d, want 0", team.Version().Int64())
	}
	if team.Name().String() != "Engineering" {
		t.Fatalf("name = %q", team.Name().String())
	}
	if team.CreatedAt().Before(before) || team.CreatedAt().After(after) {
		t.Fatalf("created_at %s outside [%s, %s]", team.CreatedAt(), before, after)
	}
	if team.UpdatedAt() != team.CreatedAt() {
		t.Fatal("updated_at must equal created_at on creation")
	}

	oldUpdated := team.UpdatedAt()
	team.ChangeName(mustTeamName(t, "Platform"))
	if team.Name().String() != "Platform" {
		t.Fatalf("name after change = %q", team.Name().String())
	}
	if team.UpdatedAt().Before(oldUpdated) {
		t.Fatal("updated_at moved backwards")
	}
	if team.Version().Int64() != 0 {
		t.Fatal("domain mutation must leave persistence version untouched")
	}

	restored := RestoreTeam(
		team.ID(),
		mustTeamName(t, "Engineering"),
		mustVersion(t, 3),
		team.CreatedAt(),
		team.UpdatedAt(),
	)
	if restored.Version().Int64() != 3 {
		t.Fatalf("restored version = %d", restored.Version().Int64())
	}
	if !restored.Equal(team) {
		t.Fatal("restored team must keep identity")
	}
	if team.Equal(FormTeam(mustTeamName(t, "Engineering"))) {
		t.Fatal("different IDs must not compare equal")
	}
	if team.Equal(nil) {
		t.Fatal("nil must not compare equal")
	}
	var nilTeam *Team
	if nilTeam.Equal(nil) {
		t.Fatal("nil receiver must not compare equal")
	}
}
