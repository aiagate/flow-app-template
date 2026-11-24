package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

type teamRepo struct {
	repoBase
}

const teamColumns = `id, name, version, created_at, updated_at`

func (r *teamRepo) Create(ctx context.Context, team *domain.Team) error {
	return r.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, name, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`,
			team.ID().String(),
			team.Name().String(),
			team.Version().Int64(),
			formatTimestamp(team.CreatedAt()),
			formatTimestamp(team.UpdatedAt()),
		)
		return mapWriteError("create team", err)
	})
}

func (r *teamRepo) GetByID(ctx context.Context, id domain.TeamID) (*domain.Team, error) {
	team, err := scanTeam(r.queryRow(ctx,
		`SELECT `+teamColumns+` FROM teams WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("get team")
	}
	if err != nil {
		return nil, fmt.Errorf("get team %s: %w", id.String(), err)
	}
	return team, nil
}

func (r *teamRepo) List(ctx context.Context) ([]*domain.Team, error) {
	rows, err := r.query(ctx,
		`SELECT `+teamColumns+` FROM teams ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()

	teams := make([]*domain.Team, 0)
	for rows.Next() {
		team, err := scanTeam(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teams: %w", err)
	}
	return teams, nil
}

func (r *teamRepo) Update(ctx context.Context, team *domain.Team) error {
	return r.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE teams
			SET name = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?`,
			team.Name().String(),
			formatTimestamp(time.Now().UTC()),
			team.ID().String(),
			team.Version().Int64(),
		)
		if err != nil {
			return mapWriteError("update team", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("update team rows affected: %w", err)
		}
		if affected == 1 {
			return nil
		}
		exists, err := existsInTx(ctx, tx, `SELECT 1 FROM teams WHERE id = ?`, team.ID().String())
		if err != nil {
			return fmt.Errorf("check team existence: %w", err)
		}
		if !exists {
			return notFound("update team")
		}
		return versionConflict("update team")
	})
}

func scanTeam(row scanner) (*domain.Team, error) {
	var (
		rawID      string
		nameRaw    string
		version    int64
		createdRaw string
		updatedRaw string
	)
	if err := row.Scan(&rawID, &nameRaw, &version, &createdRaw, &updatedRaw); err != nil {
		return nil, err
	}
	id, err := domain.ParseTeamID(rawID)
	if err != nil {
		return nil, err
	}
	name, err := domain.NewTeamName(nameRaw)
	if err != nil {
		return nil, err
	}
	versionValue, err := domain.NewVersion(version)
	if err != nil {
		return nil, err
	}
	createdAt, err := parseTimestamp("team.created_at", createdRaw)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimestamp("team.updated_at", updatedRaw)
	if err != nil {
		return nil, err
	}
	return domain.RestoreTeam(id, name, versionValue, createdAt, updatedAt), nil
}

var _ port.TeamRepository = (*teamRepo)(nil)
