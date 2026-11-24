package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/aiagate/flow-app-template/internal/contract/port"
)

// repoBase gives every repository the same read/write routing. Reads inside a
// write transaction use that transaction, which provides read-your-writes
// semantics; standalone reads use the read-only pool.
type repoBase struct {
	store *Store
	tx    *sql.Tx
}

func (r *repoBase) write(ctx context.Context, fn func(*sql.Tx) error) error {
	if r.tx != nil {
		return fn(r.tx)
	}
	return r.store.withWriteTx(ctx, fn)
}

func (r *repoBase) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if r.tx != nil {
		return r.tx.QueryRowContext(ctx, query, args...)
	}
	return r.store.ReadDB.QueryRowContext(ctx, query, args...)
}

func (r *repoBase) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if r.tx != nil {
		return r.tx.QueryContext(ctx, query, args...)
	}
	return r.store.ReadDB.QueryContext(ctx, query, args...)
}

func existsInTx(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, query, args...).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

func mapWriteError(op string, err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqliteDriver.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return &port.RepositoryError{Op: op, Message: sqliteErr.Error(), Err: port.ErrAlreadyExists}
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func notFound(op string) error {
	return &port.RepositoryError{Op: op, Err: port.ErrNotFound}
}

func versionConflict(op string) error {
	return &port.RepositoryError{Op: op, Err: port.ErrVersionConflict}
}

func parseTimestamp(field, raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s timestamp %q: %w", field, raw, err)
	}
	return parsed.UTC(), nil
}
