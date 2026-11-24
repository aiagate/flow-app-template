package domain

import (
	"testing"
)

func mustDisplayName(t *testing.T, raw string) DisplayName {
	t.Helper()
	value, err := NewDisplayName(raw)
	if err != nil {
		t.Fatalf("NewDisplayName(%q): %v", raw, err)
	}
	return value
}

func mustEmail(t *testing.T, raw string) Email {
	t.Helper()
	value, err := NewEmail(raw)
	if err != nil {
		t.Fatalf("NewEmail(%q): %v", raw, err)
	}
	return value
}

func mustTeamName(t *testing.T, raw string) TeamName {
	t.Helper()
	value, err := NewTeamName(raw)
	if err != nil {
		t.Fatalf("NewTeamName(%q): %v", raw, err)
	}
	return value
}

func mustVersion(t *testing.T, value int64) Version {
	t.Helper()
	version, err := NewVersion(value)
	if err != nil {
		t.Fatalf("NewVersion(%d): %v", value, err)
	}
	return version
}
