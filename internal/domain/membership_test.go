package domain

import (
	"errors"
	"testing"
	"time"
)

func TestMembershipRoleParsing(t *testing.T) {
	cases := map[string]MembershipRole{
		"OWNER":    RoleOwner,
		"admin":    RoleAdmin,
		" Member ": RoleMember,
	}
	for raw, want := range cases {
		got, err := ParseMembershipRole(raw)
		if err != nil {
			t.Fatalf("ParseMembershipRole(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseMembershipRole(%q) = %q, want %q", raw, got, want)
		}
		if got.String() != string(want) {
			t.Fatalf("String() = %q, want %q", got.String(), want)
		}
	}
	for _, raw := range []string{"", "GUEST", "owner!", "OWNER ADMIN"} {
		if _, err := ParseMembershipRole(raw); !errors.Is(err, ErrValidation) {
			t.Fatalf("ParseMembershipRole(%q) error = %v, want ErrValidation", raw, err)
		}
	}
}

func TestMembershipStatusParsing(t *testing.T) {
	cases := map[string]MembershipStatus{
		"PENDING": StatusPending,
		"active":  StatusActive,
		" Leaved": StatusLeaved,
	}
	for raw, want := range cases {
		got, err := ParseMembershipStatus(raw)
		if err != nil {
			t.Fatalf("ParseMembershipStatus(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseMembershipStatus(%q) = %q, want %q", raw, got, want)
		}
		if got.String() != string(want) {
			t.Fatalf("String() = %q, want %q", got.String(), want)
		}
	}
	for _, raw := range []string{"", "JOINED", "active!", "PENDING ACTIVE"} {
		if _, err := ParseMembershipStatus(raw); !errors.Is(err, ErrValidation) {
			t.Fatalf("ParseMembershipStatus(%q) error = %v, want ErrValidation", raw, err)
		}
	}
}

func TestMembershipFactoriesAndRestore(t *testing.T) {
	teamID := NewTeamID()
	userID := NewUserID()

	joined := JoinTeam(teamID, userID)
	if joined.ID().IsZero() {
		t.Fatal("joined membership has zero ID")
	}
	if joined.TeamID() != teamID || joined.UserID() != userID {
		t.Fatal("joined membership lost its identifiers")
	}
	if joined.Role() != RoleMember {
		t.Fatalf("joined role = %q, want MEMBER", joined.Role())
	}
	if joined.Status() != StatusActive {
		t.Fatalf("joined status = %q, want ACTIVE", joined.Status())
	}
	if joined.Version().Int64() != 0 {
		t.Fatalf("joined version = %d, want 0", joined.Version().Int64())
	}
	if joined.CreatedAt().IsZero() || joined.UpdatedAt().IsZero() {
		t.Fatal("joined timestamps must be set")
	}

	requested := RequestJoinTeam(teamID, userID)
	if requested.Status() != StatusPending {
		t.Fatalf("requested status = %q, want PENDING", requested.Status())
	}
	if requested.Role() != RoleMember {
		t.Fatalf("requested role = %q, want MEMBER", requested.Role())
	}
	if requested.ID() == joined.ID() {
		t.Fatal("separate membership periods must have distinct IDs")
	}

	created := time.Now().UTC().Add(-time.Hour)
	updated := created.Add(time.Minute)
	restored := RestoreTeamMembership(
		joined.ID(), teamID, userID, RoleAdmin, StatusPending, mustVersion(t, 4), created, updated,
	)
	if restored.ID() != joined.ID() || restored.TeamID() != teamID || restored.UserID() != userID {
		t.Fatal("restore lost identifiers")
	}
	if restored.Role() != RoleAdmin || restored.Status() != StatusPending {
		t.Fatal("restore lost role or status")
	}
	if restored.Version().Int64() != 4 {
		t.Fatalf("restored version = %d, want 4", restored.Version().Int64())
	}
	if !restored.CreatedAt().Equal(created) || !restored.UpdatedAt().Equal(updated) {
		t.Fatal("restore lost timestamps")
	}
}

func TestMembershipTransitionMatrix(t *testing.T) {
	type operation struct {
		name       string
		apply      func(*TeamMembership) error
		wantErr    bool
		wantStatus MembershipStatus
		wantRole   MembershipRole
	}

	operations := []operation{
		{
			name:       "approve",
			apply:      func(m *TeamMembership) error { return m.Approve() },
			wantStatus: StatusActive,
			wantRole:   RoleMember,
		},
		{
			name:       "leave",
			apply:      func(m *TeamMembership) error { return m.Leave() },
			wantStatus: StatusLeaved,
			wantRole:   RoleMember,
		},
		{
			name:       "change_role",
			apply:      func(m *TeamMembership) error { return m.ChangeRole(RoleAdmin) },
			wantStatus: "",
			wantRole:   RoleAdmin,
		},
	}

	statuses := []MembershipStatus{StatusPending, StatusActive, StatusLeaved}
	allowed := map[MembershipStatus]map[string]bool{
		StatusPending: {"approve": true, "leave": true, "change_role": true},
		StatusActive:  {"approve": false, "leave": true, "change_role": true},
		StatusLeaved:  {"approve": false, "leave": false, "change_role": false},
	}

	for _, status := range statuses {
		for _, op := range operations {
			t.Run(string(status)+"/"+op.name, func(t *testing.T) {
				created := time.Now().UTC().Add(-time.Hour)
				membership := RestoreTeamMembership(
					NewMembershipID(),
					NewTeamID(),
					NewUserID(),
					RoleMember,
					status,
					mustVersion(t, 2),
					created,
					created,
				)
				initialStatus := membership.Status()
				initialRole := membership.Role()
				initialVersion := membership.Version()
				initialUpdated := membership.UpdatedAt()

				err := op.apply(membership)
				if allowed[status][op.name] {
					if err != nil {
						t.Fatalf("transition rejected: %v", err)
					}
					wantStatus := op.wantStatus
					if wantStatus == "" {
						wantStatus = initialStatus
					}
					if membership.Status() != wantStatus {
						t.Fatalf("status = %q, want %q", membership.Status(), wantStatus)
					}
					if membership.Role() != op.wantRole {
						t.Fatalf("role = %q, want %q", membership.Role(), op.wantRole)
					}
					if membership.UpdatedAt().Before(initialUpdated) {
						t.Fatal("successful transition moved updated_at backwards")
					}
				} else {
					if !errors.Is(err, ErrInvalidTransition) {
						t.Fatalf("error = %v, want ErrInvalidTransition", err)
					}
					if membership.Status() != initialStatus {
						t.Fatalf("failed transition changed status to %q", membership.Status())
					}
					if membership.Role() != initialRole {
						t.Fatalf("failed transition changed role to %q", membership.Role())
					}
					if !membership.UpdatedAt().Equal(initialUpdated) {
						t.Fatal("failed transition changed updated_at")
					}
				}
				if membership.Version() != initialVersion {
					t.Fatal("domain transition must not change persistence version")
				}
			})
		}
	}
}

func TestMembershipApproveOnlyFromPending(t *testing.T) {
	membership := JoinTeam(NewTeamID(), NewUserID())
	if err := membership.Approve(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("approve active error = %v, want ErrInvalidTransition", err)
	}
	if membership.Status() != StatusActive {
		t.Fatal("failed approve changed status")
	}

	pending := RequestJoinTeam(NewTeamID(), NewUserID())
	if err := pending.Approve(); err != nil {
		t.Fatalf("approve pending: %v", err)
	}
	if pending.Status() != StatusActive {
		t.Fatalf("status after approve = %q", pending.Status())
	}
}
