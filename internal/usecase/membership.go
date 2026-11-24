package usecase

import (
	"context"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// MembershipUseCase applies membership-period operations. A user who leaves and
// rejoins gets a fresh period rather than reusing the previous row.
type MembershipUseCase struct {
	tx          port.WriteTxManager
	teams       port.TeamRepository
	users       port.UserRepository
	memberships port.MembershipRepository
}

func NewMembershipUseCase(
	tx port.WriteTxManager,
	teams port.TeamRepository,
	users port.UserRepository,
	memberships port.MembershipRepository,
) *MembershipUseCase {
	return &MembershipUseCase{tx: tx, teams: teams, users: users, memberships: memberships}
}

type JoinTeamInput struct {
	TeamID string
	UserID string
}

// JoinTeam creates an immediately ACTIVE membership after verifying both
// aggregates exist. The partial unique index turns a second current period into
// an already-exists conflict.
func (m *MembershipUseCase) JoinTeam(ctx context.Context, input JoinTeamInput) (*domain.TeamMembership, error) {
	teamID, userID, err := m.parsePair(input.TeamID, input.UserID)
	if err != nil {
		return nil, err
	}

	var created *domain.TeamMembership
	err = m.tx.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		if _, err := repos.Teams().GetByID(ctx, teamID); err != nil {
			return err
		}
		if _, err := repos.Users().GetByID(ctx, userID); err != nil {
			return err
		}
		membership := domain.JoinTeam(teamID, userID)
		if err := repos.Memberships().Create(ctx, membership); err != nil {
			return err
		}
		created = membership
		return nil
	})
	if err != nil {
		return nil, ClassifyError(err)
	}
	return created, nil
}

// RequestJoinTeam creates a PENDING membership after verifying both aggregates
// exist.
func (m *MembershipUseCase) RequestJoinTeam(ctx context.Context, input JoinTeamInput) (*domain.TeamMembership, error) {
	teamID, userID, err := m.parsePair(input.TeamID, input.UserID)
	if err != nil {
		return nil, err
	}

	var created *domain.TeamMembership
	err = m.tx.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		if _, err := repos.Teams().GetByID(ctx, teamID); err != nil {
			return err
		}
		if _, err := repos.Users().GetByID(ctx, userID); err != nil {
			return err
		}
		membership := domain.RequestJoinTeam(teamID, userID)
		if err := repos.Memberships().Create(ctx, membership); err != nil {
			return err
		}
		created = membership
		return nil
	})
	if err != nil {
		return nil, ClassifyError(err)
	}
	return created, nil
}

// ListTeamMemberships returns every membership period for an existing team,
// including LEAVED history.
func (m *MembershipUseCase) ListTeamMemberships(ctx context.Context, teamID string) ([]*domain.TeamMembership, error) {
	parsedTeamID, err := domain.ParseTeamID(teamID)
	if err != nil {
		return nil, ClassifyError(err)
	}
	if _, err := m.teams.GetByID(ctx, parsedTeamID); err != nil {
		return nil, ClassifyError(err)
	}
	memberships, err := m.memberships.ListByTeam(ctx, parsedTeamID)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return memberships, nil
}

// ApproveJoinRequest moves a PENDING period to ACTIVE.
func (m *MembershipUseCase) ApproveJoinRequest(ctx context.Context, membershipID string) (*domain.TeamMembership, error) {
	return m.transition(ctx, membershipID, func(membership *domain.TeamMembership) error {
		return membership.Approve()
	})
}

// LeaveTeam moves a PENDING or ACTIVE period to LEAVED.
func (m *MembershipUseCase) LeaveTeam(ctx context.Context, membershipID string) (*domain.TeamMembership, error) {
	return m.transition(ctx, membershipID, func(membership *domain.TeamMembership) error {
		return membership.Leave()
	})
}

// ChangeRole updates the role of a non-LEAVED membership.
func (m *MembershipUseCase) ChangeRole(ctx context.Context, membershipID, role string) (*domain.TeamMembership, error) {
	parsedRole, err := domain.ParseMembershipRole(role)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return m.transition(ctx, membershipID, func(membership *domain.TeamMembership) error {
		return membership.ChangeRole(parsedRole)
	})
}

func (m *MembershipUseCase) transition(
	ctx context.Context,
	membershipID string,
	apply func(*domain.TeamMembership) error,
) (*domain.TeamMembership, error) {
	parsedID, err := domain.ParseMembershipID(membershipID)
	if err != nil {
		return nil, ClassifyError(err)
	}

	var updated *domain.TeamMembership
	err = m.tx.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		membership, err := repos.Memberships().GetByID(ctx, parsedID)
		if err != nil {
			return err
		}
		if err := apply(membership); err != nil {
			return err
		}
		if err := repos.Memberships().Update(ctx, membership); err != nil {
			return err
		}
		updated, err = repos.Memberships().GetByID(ctx, parsedID)
		return err
	})
	if err != nil {
		return nil, ClassifyError(err)
	}
	return updated, nil
}

func (m *MembershipUseCase) parsePair(rawTeamID, rawUserID string) (domain.TeamID, domain.UserID, error) {
	teamID, err := domain.ParseTeamID(rawTeamID)
	if err != nil {
		return domain.TeamID{}, domain.UserID{}, ClassifyError(err)
	}
	userID, err := domain.ParseUserID(rawUserID)
	if err != nil {
		return domain.TeamID{}, domain.UserID{}, ClassifyError(err)
	}
	return teamID, userID, nil
}
