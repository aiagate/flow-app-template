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

type userRepo struct {
	repoBase
}

const userColumns = `id, display_name, email, version, created_at, updated_at`

func (r *userRepo) Create(ctx context.Context, user *domain.User) error {
	return r.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO users (id, display_name, email, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			user.ID().String(),
			user.DisplayName().String(),
			user.Email().String(),
			user.Version().Int64(),
			formatTimestamp(user.CreatedAt()),
			formatTimestamp(user.UpdatedAt()),
		)
		return mapWriteError("create user", err)
	})
}

func (r *userRepo) GetByID(ctx context.Context, id domain.UserID) (*domain.User, error) {
	user, err := scanUser(r.queryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("get user")
	}
	if err != nil {
		return nil, fmt.Errorf("get user %s: %w", id.String(), err)
	}
	return user, nil
}

func (r *userRepo) List(ctx context.Context) ([]*domain.User, error) {
	rows, err := r.query(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]*domain.User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

func (r *userRepo) Update(ctx context.Context, user *domain.User) error {
	return r.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE users
			SET display_name = ?, email = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?`,
			user.DisplayName().String(),
			user.Email().String(),
			formatTimestamp(time.Now().UTC()),
			user.ID().String(),
			user.Version().Int64(),
		)
		if err != nil {
			return mapWriteError("update user", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("update user rows affected: %w", err)
		}
		if affected == 1 {
			return nil
		}
		exists, err := existsInTx(ctx, tx, `SELECT 1 FROM users WHERE id = ?`, user.ID().String())
		if err != nil {
			return fmt.Errorf("check user existence: %w", err)
		}
		if !exists {
			return notFound("update user")
		}
		return versionConflict("update user")
	})
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (*domain.User, error) {
	var (
		rawID      string
		displayRaw string
		emailRaw   string
		version    int64
		createdRaw string
		updatedRaw string
	)
	if err := row.Scan(&rawID, &displayRaw, &emailRaw, &version, &createdRaw, &updatedRaw); err != nil {
		return nil, err
	}
	id, err := domain.ParseUserID(rawID)
	if err != nil {
		return nil, err
	}
	displayName, err := domain.NewDisplayName(displayRaw)
	if err != nil {
		return nil, err
	}
	email, err := domain.NewEmail(emailRaw)
	if err != nil {
		return nil, err
	}
	versionValue, err := domain.NewVersion(version)
	if err != nil {
		return nil, err
	}
	createdAt, err := parseTimestamp("user.created_at", createdRaw)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTimestamp("user.updated_at", updatedRaw)
	if err != nil {
		return nil, err
	}
	return domain.RestoreUser(id, displayName, email, versionValue, createdAt, updatedAt), nil
}

var _ port.UserRepository = (*userRepo)(nil)
