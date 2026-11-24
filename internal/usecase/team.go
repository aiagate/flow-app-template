package usecase

import (
	"context"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// TeamUseCase applies team directory operations.
type TeamUseCase struct {
	tx    port.WriteTxManager
	teams port.TeamRepository
}

func NewTeamUseCase(tx port.WriteTxManager, teams port.TeamRepository) *TeamUseCase {
	return &TeamUseCase{tx: tx, teams: teams}
}

type CreateTeamInput struct {
	Name string
}

// CreateTeam validates the team name and persists a new team aggregate.
func (t *TeamUseCase) CreateTeam(ctx context.Context, input CreateTeamInput) (*domain.Team, error) {
	name, err := domain.NewTeamName(input.Name)
	if err != nil {
		return nil, ClassifyError(err)
	}

	team := domain.FormTeam(name)
	err = t.tx.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		return repos.Teams().Create(ctx, team)
	})
	if err != nil {
		return nil, ClassifyError(err)
	}
	return team, nil
}

// GetTeam loads one team by its canonical ULID.
func (t *TeamUseCase) GetTeam(ctx context.Context, id string) (*domain.Team, error) {
	teamID, err := domain.ParseTeamID(id)
	if err != nil {
		return nil, ClassifyError(err)
	}
	team, err := t.teams.GetByID(ctx, teamID)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return team, nil
}

// ListTeams returns every team ordered by the repository's stable ordering.
func (t *TeamUseCase) ListTeams(ctx context.Context) ([]*domain.Team, error) {
	teams, err := t.teams.List(ctx)
	if err != nil {
		return nil, ClassifyError(err)
	}
	return teams, nil
}

type UpdateTeamInput struct {
	TeamID          string
	Name            string
	ExpectedVersion *int64
}

// UpdateTeam renames a team with optimistic locking. Omitting ExpectedVersion
// uses the version read inside the write transaction; a stale explicit version
// returns CONCURRENCY_CONFLICT.
func (t *TeamUseCase) UpdateTeam(ctx context.Context, input UpdateTeamInput) (*domain.Team, error) {
	teamID, err := domain.ParseTeamID(input.TeamID)
	if err != nil {
		return nil, ClassifyError(err)
	}
	name, err := domain.NewTeamName(input.Name)
	if err != nil {
		return nil, ClassifyError(err)
	}
	if err := validateExpectedVersion(input.ExpectedVersion); err != nil {
		return nil, err
	}

	var updated *domain.Team
	err = t.tx.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		team, err := repos.Teams().GetByID(ctx, teamID)
		if err != nil {
			return err
		}
		if input.ExpectedVersion != nil && team.Version().Int64() != *input.ExpectedVersion {
			return port.ErrVersionConflict
		}
		team.ChangeName(name)
		if err := repos.Teams().Update(ctx, team); err != nil {
			return err
		}
		updated, err = repos.Teams().GetByID(ctx, teamID)
		return err
	})
	if err != nil {
		return nil, ClassifyError(err)
	}
	return updated, nil
}
