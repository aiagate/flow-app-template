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

type membershipRepo struct {
	repoBase
}

const membershipColumns = `id, team_id, user_id, role, status, version, created_at, updated_at`

func (r *membershipRepo) Create(ctx context.Context, membership *domain.TeamMembership) error {
	return r.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_memberships (id, team_id, user_id, role, status, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			membership.ID().String(),
			membership.TeamID().String(),
			membership.UserID().String(),
			membership.Role().String(),
			membership.Status().String(),
			membership.Version().Int64(),
			formatTimestamp(membership.CreatedAt()),
			formatTimestamp(membership.UpdatedAt()),
		)
		return mapWriteError("create team membership", err)
	})
}

func (r *membershipRepo) GetByID(ctx context.Context, id domain.MembershipID) (*domain.TeamMembership, error) {
	membership, err := scanMembership(r.queryRow(ctx,
		`SELECT `+membershipColumns+` FROM team_memberships WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("get team membership")
	}
	if err != nil {
		return nil, fmt.Errorf("get team membership %s: %w", id.String(), err)
	}
	return membership, nil
}

func (r *membershipRepo) ListByTeam(ctx context.Context, teamID domain.TeamID) ([]*domain.TeamMembership, error) {
	rows, err := r.query(ctx,
		`SELECT `+membershipColumns+` FROM team_memberships
		 WHERE team_id = ? ORDER BY created_at ASC, id ASC`,
		teamID.String())
	if err != nil {
		return nil, fmt.Errorf("list team memberships: %w", err)
	}
	defer rows.Close()

	memberships := make([]*domain.TeamMembership, 0)
	for rows.Next() {
		membership, err := scanMembership(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team membership: %w", err)
		}
		memberships = append(memberships, membership)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate team memberships: %w", err)
	}
	return memberships, nil
}

func (r *membershipRepo) Update(ctx context.Context, membership *domain.TeamMembership) error {
	return r.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE team_memberships
			SET team_id = ?, user_id = ?, role = ?, status = ?,
			    version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?`,
			membership.TeamID().String(),
			membership.UserID().String(),
			membership.Role().String(),
			membership.Status().String(),
			formatTimestamp(time.Now().UTC()),
			membership.ID().String(),
			membership.Version().Int64(),
		)
		if err != nil {
			return mapWriteError("update team membership", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("update team membership rows affected: %w", err)
		}
		if affected == 1 {
			return nil
		}
		exists, err := existsInTx(ctx, tx,
			`SELECT 1 FROM team_memberships WHERE id = ?`, membership.ID().String())
		if err != nil {
			return fmt.Errorf("check team membership existence: %w", err)
		}
		if !exists {
			return notFound("update team membership")
		}
		return versionConflict("update team membership")
	})
}

func scanMembership(row scanner) (*domain.TeamMembership, error) {
	var (
		rawID      string
		rawTeamID  string
		rawUserID  string
		roleRaw    string
		statusRaw  string
		version    int64
		createdRaw string
		updatedRaw string
	)
	if err := row.Scan(&rawID, &rawTeamID, &rawUserID, &roleRaw, &statusRaw, &version, &createdRaw, &updatedRaw); err != nil {
		return nil, err
	}
	id, err := domain.ParseMembershipID(rawID)
	if err != nil {
		return nil, err
	}
	teamID, err := domain.ParseTeamID(rawTeamID)
	if err != nil {
		return nil, err
	}
	userID, err := domain.ParseUserID(rawUserID)
	if err != nil {
		return nil, err
	}
	role, err := domain.ParseMembershipRole(roleRaw)
	if err != nil {
		return nil, err
	}
	status, err := domain.ParseMembershipStatus(statusRaw)
	if err != nil {
		return nil, err
	}
	versionValue, err := domain.NewVersion(version)
	if err != nil {
		return nil, err
	}
	createdAt, err := parseTimestamp("team_membership.created_at", createdRaw)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimestamp("team_membership.updated_at", updatedRaw)
	if err != nil {
		return nil, err
	}
	return domain.RestoreTeamMembership(
		id, teamID, userID, role, status, versionValue, createdAt, updatedAt,
	), nil
}

var _ port.MembershipRepository = (*membershipRepo)(nil)
