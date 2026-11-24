package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aiagate/flow-app-template/migrations"
)

// migrate applies every embedded migration that is not yet recorded. The
// tracking table and all pending migrations commit atomically in one write
// transaction.
func (s *Store) migrate(ctx context.Context) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS schema_migrations (
				version TEXT PRIMARY KEY,
				applied_at TEXT NOT NULL
			)`); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}

		entries, err := migrations.FS.ReadDir(".")
		if err != nil {
			return fmt.Errorf("read embedded migrations: %w", err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			names = append(names, entry.Name())
		}
		sort.Strings(names)

		applied := make(map[string]struct{}, len(names))
		rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return fmt.Errorf("read applied migrations: %w", err)
		}
		for rows.Next() {
			var version string
			if err := rows.Scan(&version); err != nil {
				rows.Close()
				return fmt.Errorf("scan applied migration: %w", err)
			}
			applied[version] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate applied migrations: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close applied migrations: %w", err)
		}

		for _, name := range names {
			if _, ok := applied[name]; ok {
				continue
			}
			contents, err := migrations.FS.ReadFile(name)
			if err != nil {
				return fmt.Errorf("read migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				name,
				time.Now().UTC().Format(timestampLayout),
			); err != nil {
				return fmt.Errorf("record migration %s: %w", name, err)
			}
		}
		return nil
	})
}
