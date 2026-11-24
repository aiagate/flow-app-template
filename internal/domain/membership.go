package domain

import (
	"strings"
	"time"
)

// MembershipRole is the role a member holds within a team.
type MembershipRole string

const (
	RoleOwner  MembershipRole = "OWNER"
	RoleAdmin  MembershipRole = "ADMIN"
	RoleMember MembershipRole = "MEMBER"
)

func ParseMembershipRole(raw string) (MembershipRole, error) {
	switch MembershipRole(strings.ToUpper(strings.TrimSpace(raw))) {
	case RoleOwner:
		return RoleOwner, nil
	case RoleAdmin:
		return RoleAdmin, nil
	case RoleMember:
		return RoleMember, nil
	default:
		return "", validationError("membership_role", "must be OWNER, ADMIN or MEMBER")
	}
}

func (r MembershipRole) String() string { return string(r) }

// MembershipStatus is the lifecycle state of a membership period.
type MembershipStatus string

const (
	StatusPending MembershipStatus = "PENDING"
	StatusActive  MembershipStatus = "ACTIVE"
	StatusLeaved  MembershipStatus = "LEAVED"
)

func ParseMembershipStatus(raw string) (MembershipStatus, error) {
	switch MembershipStatus(strings.ToUpper(strings.TrimSpace(raw))) {
	case StatusPending:
		return StatusPending, nil
	case StatusActive:
		return StatusActive, nil
	case StatusLeaved:
		return StatusLeaved, nil
	default:
		return "", validationError("membership_status", "must be PENDING, ACTIVE or LEAVED")
	}
}

func (s MembershipStatus) String() string { return string(s) }

// TeamMembership is the membership-period aggregate. LEAVED rows are retained
// so rejoining creates a fresh period instead of erasing history.
type TeamMembership struct {
	id        MembershipID
	teamID    TeamID
	userID    UserID
	role      MembershipRole
	status    MembershipStatus
	version   Version
	createdAt time.Time
	updatedAt time.Time
}

// JoinTeam creates an immediately active membership with the MEMBER role.
func JoinTeam(teamID TeamID, userID UserID) *TeamMembership {
	return newMembership(teamID, userID, RoleMember, StatusActive)
}

// RequestJoinTeam creates a pending membership with the MEMBER role.
func RequestJoinTeam(teamID TeamID, userID UserID) *TeamMembership {
	return newMembership(teamID, userID, RoleMember, StatusPending)
}

func newMembership(teamID TeamID, userID UserID, role MembershipRole, status MembershipStatus) *TeamMembership {
	now := time.Now().UTC()
	return &TeamMembership{
		id:        NewMembershipID(),
		teamID:    teamID,
		userID:    userID,
		role:      role,
		status:    status,
		version:   Version{},
		createdAt: now,
		updatedAt: now,
	}
}

// RestoreTeamMembership rebuilds an aggregate from persisted state.
func RestoreTeamMembership(
	id MembershipID,
	teamID TeamID,
	userID UserID,
	role MembershipRole,
	status MembershipStatus,
	version Version,
	createdAt time.Time,
	updatedAt time.Time,
) *TeamMembership {
	return &TeamMembership{
		id:        id,
		teamID:    teamID,
		userID:    userID,
		role:      role,
		status:    status,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}
}

func (m *TeamMembership) ID() MembershipID         { return m.id }
func (m *TeamMembership) TeamID() TeamID           { return m.teamID }
func (m *TeamMembership) UserID() UserID           { return m.userID }
func (m *TeamMembership) Role() MembershipRole     { return m.role }
func (m *TeamMembership) Status() MembershipStatus { return m.status }
func (m *TeamMembership) Version() Version         { return m.version }
func (m *TeamMembership) CreatedAt() time.Time     { return m.createdAt }
func (m *TeamMembership) UpdatedAt() time.Time     { return m.updatedAt }

// Approve moves PENDING to ACTIVE. Every other transition is rejected without
// mutating the aggregate.
func (m *TeamMembership) Approve() error {
	if m.status != StatusPending {
		return ErrInvalidTransition
	}
	m.status = StatusActive
	m.touch()
	return nil
}

// Leave moves PENDING or ACTIVE to LEAVED. Leaving an already LEAVED
// membership is rejected without mutating the aggregate.
func (m *TeamMembership) Leave() error {
	if m.status == StatusLeaved {
		return ErrInvalidTransition
	}
	m.status = StatusLeaved
	m.touch()
	return nil
}

// ChangeRole updates the role unless the membership has already ended.
func (m *TeamMembership) ChangeRole(role MembershipRole) error {
	if m.status == StatusLeaved {
		return ErrInvalidTransition
	}
	m.role = role
	m.touch()
	return nil
}

func (m *TeamMembership) touch() { m.updatedAt = time.Now().UTC() }
