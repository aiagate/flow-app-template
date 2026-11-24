package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// TeamName is a non-empty, trimmed name of at most 100 runes.
type TeamName struct {
	value string
}

func NewTeamName(raw string) (TeamName, error) {
	if raw == "" {
		return TeamName{}, validationError("team_name", "must not be empty")
	}
	if utf8.RuneCountInString(raw) > nameMaxRunes {
		return TeamName{}, validationError("team_name", "must not exceed 100 characters")
	}
	if raw != strings.TrimSpace(raw) {
		return TeamName{}, validationError("team_name", "must not have leading or trailing whitespace")
	}
	return TeamName{value: raw}, nil
}

func (n TeamName) String() string { return n.value }

func (n TeamName) IsZero() bool { return n.value == "" }

// Team is the team aggregate root.
type Team struct {
	id        TeamID
	name      TeamName
	version   Version
	createdAt time.Time
	updatedAt time.Time
}

// FormTeam creates a new team at version zero.
func FormTeam(name TeamName) *Team {
	now := time.Now().UTC()
	return &Team{
		id:        NewTeamID(),
		name:      name,
		version:   Version{},
		createdAt: now,
		updatedAt: now,
	}
}

// RestoreTeam rebuilds an aggregate from persisted state.
func RestoreTeam(
	id TeamID,
	name TeamName,
	version Version,
	createdAt time.Time,
	updatedAt time.Time,
) *Team {
	return &Team{
		id:        id,
		name:      name,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}
}

func (t *Team) ID() TeamID           { return t.id }
func (t *Team) Name() TeamName       { return t.name }
func (t *Team) Version() Version     { return t.version }
func (t *Team) CreatedAt() time.Time { return t.createdAt }
func (t *Team) UpdatedAt() time.Time { return t.updatedAt }
func (t *Team) ChangeName(name TeamName) {
	t.name = name
	t.updatedAt = time.Now().UTC()
}

// Equal compares aggregate identity, not mutable state.
func (t *Team) Equal(other *Team) bool {
	return t != nil && other != nil && t.id == other.id
}
