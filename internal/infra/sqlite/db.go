// Package sqlite provides the raw-SQL SQLite persistence adapters. It has no
// knowledge of the in-memory hot path or the asynchronous batcher; the
// composition root wires those together.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

const (
	defaultReadConns     = 5
	minReadConns         = 4
	maxReadConns         = 6
	defaultWALWarnBytes  = int64(64 << 20)
	timestampWriteLayout = "2006-01-02T15:04:05.000000000Z"
)

// Store owns the single write pool and the read-only read pool.
type Store struct {
	WriteDB *sql.DB
	ReadDB  *sql.DB

	dbPath       string
	walWarnBytes atomic.Int64
	writeMu      sync.Mutex
	readConns    int
}

// Open builds the database path, opens and migrates the write pool first, and
// only then opens the read-only pool. Opening readers before the database and
// its WAL files exist fails, which is why this order is enforced here.
func Open(ctx context.Context, dbPath string, readConns int) (*Store, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, fmt.Errorf("database path must not be empty")
	}
	readConns = clampReadConns(readConns)

	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	uri := "file:" + filepath.ToSlash(absPath)

	writeDSN := uri + "?_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=mmap_size(134217728)" +
		"&_pragma=cache_size(-32000)" +
		"&_pragma=wal_autocheckpoint(0)"

	writeDB, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		return nil, fmt.Errorf("open write database: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	writeDB.SetConnMaxLifetime(0)

	store := &Store{WriteDB: writeDB, dbPath: absPath, readConns: readConns}
	store.walWarnBytes.Store(defaultWALWarnBytes)
	if err := writeDB.PingContext(ctx); err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("ping write database: %w", err)
	}
	// Litestream owns checkpoints; make the database-level setting explicit in
	// addition to the per-connection DSN pragma.
	if _, err := writeDB.ExecContext(ctx, "PRAGMA wal_autocheckpoint = 0"); err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("disable automatic WAL checkpointing: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		_ = writeDB.Close()
		return nil, err
	}

	readDSN := uri + "?mode=ro&_query_only=on&_txlock=deferred" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=mmap_size(134217728)" +
		"&_pragma=cache_size(-32000)"

	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open read database: %w", err)
	}
	readDB.SetMaxOpenConns(readConns)
	readDB.SetMaxIdleConns(readConns)
	readDB.SetConnMaxLifetime(0)
	if err := readDB.PingContext(ctx); err != nil {
		_ = readDB.Close()
		_ = writeDB.Close()
		return nil, fmt.Errorf("ping read database: %w", err)
	}

	store.ReadDB = readDB
	return store, nil
}

func clampReadConns(readConns int) int {
	if readConns == 0 {
		return defaultReadConns
	}
	if readConns < minReadConns {
		return minReadConns
	}
	if readConns > maxReadConns {
		return maxReadConns
	}
	return readConns
}

// WithWriteTx serializes writes in-process, then commits or rolls back one
// SQLite transaction. The callback receives repositories bound to that
// transaction.
func (s *Store) WithWriteTx(ctx context.Context, fn func(context.Context, port.WriteRepos) error) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return fn(ctx, newWriteRepos(tx))
	})
}

func (s *Store) withWriteTx(ctx context.Context, fn func(*sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.WriteDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write transaction: %w", err)
	}
	committed = true
	return nil
}

// SetWALWarnBytes sets the WAL size threshold reported by Health. A value at or
// below zero disables the threshold.
func (s *Store) SetWALWarnBytes(bytes int64) {
	if bytes > 0 {
		s.walWarnBytes.Store(bytes)
		return
	}
	s.walWarnBytes.Store(0)
}

// Health reads the effective settings from live connections rather than
// trusting the DSN text.
func (s *Store) Health(ctx context.Context) (domain.DBHealth, error) {
	var health domain.DBHealth
	var journalMode string
	var walAutocheckpoint int64
	if err := s.WriteDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return domain.DBHealth{}, fmt.Errorf("read journal_mode: %w", err)
	}
	if err := s.WriteDB.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&walAutocheckpoint); err != nil {
		return domain.DBHealth{}, fmt.Errorf("read wal_autocheckpoint: %w", err)
	}
	var queryOnly int
	if err := s.ReadDB.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return domain.DBHealth{}, fmt.Errorf("read query_only: %w", err)
	}
	walBytes, err := s.walSize()
	if err != nil {
		return domain.DBHealth{}, err
	}
	warnBytes := s.walWarnBytes.Load()
	walHealthy := warnBytes <= 0 || walBytes <= warnBytes

	health.JournalMode = journalMode
	health.WALAutocheckpoint = walAutocheckpoint
	health.WALBytes = walBytes
	health.WALWarnBytes = warnBytes
	health.WALHealthy = walHealthy
	health.ReadQueryOnly = queryOnly == 1
	health.ReadConnections = s.readConns
	health.OK = strings.EqualFold(journalMode, "wal") && walAutocheckpoint == 0 &&
		queryOnly == 1 && walHealthy
	return health, nil
}

func (s *Store) walSize() (int64, error) {
	info, err := os.Stat(s.dbPath + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat WAL file: %w", err)
	}
	return info.Size(), nil
}

// WriteBatch implements port.BatchSink. Every insert in the payload commits in
// one transaction, so a failing event cannot leave quotas or logs behind.
func (s *Store) WriteBatch(ctx context.Context, payload port.BatchPayload) error {
	quotas := dedupeQuotas(payload.Quotas)
	if len(payload.BillingEvents) == 0 && len(quotas) == 0 && len(payload.RequestLogs) == 0 {
		return nil
	}
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		if len(payload.BillingEvents) > 0 {
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO billing_events (id, account_id, amount, reason, request_id, occurred_at)
				VALUES (?, ?, ?, ?, ?, ?)`)
			if err != nil {
				return fmt.Errorf("prepare billing event insert: %w", err)
			}
			defer stmt.Close()
			for _, event := range payload.BillingEvents {
				if _, err := stmt.ExecContext(ctx,
					event.ID.String(),
					event.AccountID,
					event.Amount,
					event.Reason,
					event.RequestID,
					formatTimestamp(event.OccurredAt),
				); err != nil {
					return mapWriteError("insert billing event", err)
				}
			}
		}

		if len(quotas) > 0 {
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO account_quotas (account_id, balance, rate_limit_rpm, seq, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(account_id) DO UPDATE SET
					balance = excluded.balance,
					rate_limit_rpm = excluded.rate_limit_rpm,
					seq = excluded.seq,
					updated_at = excluded.updated_at
				WHERE excluded.seq >= account_quotas.seq`)
			if err != nil {
				return fmt.Errorf("prepare quota upsert: %w", err)
			}
			defer stmt.Close()
			for _, quota := range quotas {
				if _, err := stmt.ExecContext(ctx,
					quota.AccountID,
					quota.Balance,
					quota.RateLimitRPM,
					quota.Seq,
					formatTimestamp(quota.UpdatedAt),
				); err != nil {
					return mapWriteError("upsert account quota", err)
				}
			}
		}

		if len(payload.RequestLogs) > 0 {
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO request_logs (id, request_id, account_id, method, path, status_code, latency_us, occurred_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
			if err != nil {
				return fmt.Errorf("prepare request log insert: %w", err)
			}
			defer stmt.Close()
			for _, logRow := range payload.RequestLogs {
				if _, err := stmt.ExecContext(ctx,
					logRow.ID,
					logRow.RequestID,
					logRow.AccountID,
					logRow.Method,
					logRow.Path,
					logRow.StatusCode,
					logRow.LatencyUS,
					formatTimestamp(logRow.OccurredAt),
				); err != nil {
					return mapWriteError("insert request log", err)
				}
			}
		}
		return nil
	})
}

// dedupeQuotas keeps one snapshot per account. When a payload carries several
// snapshots for the same account, the highest Seq wins regardless of slice
// order, so a delayed lower-Seq snapshot cannot roll the persisted balance
// backward.
func dedupeQuotas(quotas []domain.AccountQuota) []domain.AccountQuota {
	if len(quotas) < 2 {
		return quotas
	}
	index := make(map[string]int, len(quotas))
	deduped := make([]domain.AccountQuota, 0, len(quotas))
	for _, quota := range quotas {
		if existing, ok := index[quota.AccountID]; ok {
			if quota.Seq >= deduped[existing].Seq {
				deduped[existing] = quota
			}
			continue
		}
		index[quota.AccountID] = len(deduped)
		deduped = append(deduped, quota)
	}
	return deduped
}

// Close closes readers before the writer so no in-flight read can race the
// final WAL state.
func (s *Store) Close() error {
	var errs []error
	if s.ReadDB != nil {
		if err := s.ReadDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close read database: %w", err))
		}
	}
	if s.WriteDB != nil {
		if err := s.WriteDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close write database: %w", err))
		}
	}
	return errors.Join(errs...)
}

var (
	_ port.WriteTxManager  = (*Store)(nil)
	_ port.BatchSink       = (*Store)(nil)
	_ port.DBHealthChecker = (*Store)(nil)
)

// formatTimestamp renders t in the canonical fixed-width UTC form so TEXT
// ordering matches chronological ordering. Parsing stays on RFC3339Nano for
// legacy rows.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format(timestampWriteLayout)
}
