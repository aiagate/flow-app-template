package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

func openTestStore(t *testing.T, readConns int) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flow.db")
	store, err := Open(context.Background(), path, readConns)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return store
}

func mustDisplayName(t *testing.T, raw string) domain.DisplayName {
	t.Helper()
	value, err := domain.NewDisplayName(raw)
	if err != nil {
		t.Fatalf("NewDisplayName(%q): %v", raw, err)
	}
	return value
}

func mustEmail(t *testing.T, raw string) domain.Email {
	t.Helper()
	value, err := domain.NewEmail(raw)
	if err != nil {
		t.Fatalf("NewEmail(%q): %v", raw, err)
	}
	return value
}

func mustTeamName(t *testing.T, raw string) domain.TeamName {
	t.Helper()
	value, err := domain.NewTeamName(raw)
	if err != nil {
		t.Fatalf("NewTeamName(%q): %v", raw, err)
	}
	return value
}

func mustUser(t *testing.T, name, email string) *domain.User {
	t.Helper()
	return domain.RegisterUser(mustDisplayName(t, name), mustEmail(t, email))
}

func mustTeam(t *testing.T, name string) *domain.Team {
	t.Helper()
	return domain.FormTeam(mustTeamName(t, name))
}

func assertQuota(t *testing.T, got domain.AccountQuota, wantBalance int64, wantRPM int, wantSeq int64) {
	t.Helper()
	if got.Balance != wantBalance || got.RateLimitRPM != wantRPM || got.Seq != wantSeq {
		t.Fatalf("quota = %+v, want balance %d rpm %d seq %d", got, wantBalance, wantRPM, wantSeq)
	}
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readPragma(t *testing.T, queryer rowQuerier, name string) string {
	t.Helper()
	var value any
	if err := queryer.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&value); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return fmt.Sprint(value)
}

func assertPragma(t *testing.T, queryer rowQuerier, name, want string) {
	t.Helper()
	if got := readPragma(t, queryer, name); got != want {
		t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
	}
}

func TestRequiredWritePragmas(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()

	expected := map[string]string{
		"journal_mode":       "wal",
		"synchronous":        "1",
		"mmap_size":          "134217728",
		"cache_size":         "-32000",
		"foreign_keys":       "1",
		"wal_autocheckpoint": "0",
		"busy_timeout":       "5000",
	}
	for name, want := range expected {
		assertPragma(t, store.WriteDB, name, want)
	}

	var journalMode string
	if err := store.WriteDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	t.Logf("darwin/arm64 observed PRAGMA mmap_size on WriteDB = %s", readPragma(t, store.WriteDB, "mmap_size"))
}

func TestReadPragmasAcrossConcurrentConnections(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()

	const connectionCount = 5
	connections := make([]*sql.Conn, 0, connectionCount)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	for range connectionCount {
		connection, err := store.ReadDB.Conn(ctx)
		if err != nil {
			t.Fatalf("acquire read connection: %v", err)
		}
		connections = append(connections, connection)
	}

	expected := map[string]string{
		"query_only":   "1",
		"synchronous":  "1",
		"mmap_size":    "134217728",
		"cache_size":   "-32000",
		"foreign_keys": "1",
		"busy_timeout": "5000",
	}
	for index, connection := range connections {
		for name, want := range expected {
			if got := readPragma(t, connection, name); got != want {
				t.Errorf("connection %d PRAGMA %s = %q, want %q", index, name, got, want)
			}
		}
	}
	t.Logf("darwin/arm64 observed PRAGMA mmap_size on ReadDB connections = %s",
		readPragma(t, connections[0], "mmap_size"))
}

func TestReadPoolIsReadOnly(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()

	var count int
	if err := store.ReadDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("read from read pool: %v", err)
	}

	insertErr := func() error {
		_, err := store.ReadDB.ExecContext(ctx,
			`INSERT INTO users (id, display_name, email, version, created_at, updated_at)
			 VALUES (?, ?, ?, 0, ?, ?)`,
			domain.NewUserID().String(), "Alice", "alice@example.com",
			formatTimestamp(time.Now()), formatTimestamp(time.Now()))
		return err
	}()
	if insertErr == nil {
		t.Fatal("INSERT on read pool succeeded, want read-only error")
	}
	if !isReadOnlyError(insertErr) {
		t.Fatalf("INSERT error = %v, want read-only SQLITE_READONLY", insertErr)
	}
	t.Logf("observed read-only INSERT error: %v", insertErr)

	createErr := func() error {
		_, err := store.ReadDB.ExecContext(ctx, `CREATE TABLE should_fail (id TEXT)`)
		return err
	}()
	if createErr == nil {
		t.Fatal("CREATE TABLE on read pool succeeded, want read-only error")
	}
	if !isReadOnlyError(createErr) {
		t.Fatalf("CREATE TABLE error = %v, want read-only SQLITE_READONLY", createErr)
	}
	t.Logf("observed read-only CREATE TABLE error: %v", createErr)
}

func isReadOnlyError(err error) bool {
	var sqliteErr *sqliteDriver.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_READONLY {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "readonly") || strings.Contains(message, "read-only")
}

func TestHealth(t *testing.T) {
	store := openTestStore(t, 5)
	health, err := store.Health(context.Background())
	if err != nil {
		t.Fatalf("Health(): %v", err)
	}
	if !health.OK {
		t.Fatalf("Health() = %+v, want OK", health)
	}
	if !strings.EqualFold(health.JournalMode, "wal") {
		t.Errorf("JournalMode = %q", health.JournalMode)
	}
	if health.WALAutocheckpoint != 0 {
		t.Errorf("WALAutocheckpoint = %d", health.WALAutocheckpoint)
	}
	if !health.ReadQueryOnly {
		t.Error("ReadQueryOnly = false")
	}
	if health.ReadConnections != 5 {
		t.Errorf("ReadConnections = %d, want 5", health.ReadConnections)
	}
	if health.WALBytes < 0 {
		t.Errorf("WALBytes = %d, want non-negative", health.WALBytes)
	}
	if health.WALWarnBytes != 64<<20 {
		t.Errorf("WALWarnBytes = %d, want %d", health.WALWarnBytes, int64(64<<20))
	}
	if !health.WALHealthy {
		t.Errorf("WALHealthy = false, WALBytes = %d, WALWarnBytes = %d", health.WALBytes, health.WALWarnBytes)
	}
}

func TestMigrationSchemaAndIdempotence(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()

	if err := store.migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var migrationCount int
	if err := store.ReadDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if migrationCount != 3 {
		t.Fatalf("schema_migrations count = %d, want 3", migrationCount)
	}

	tables := []string{
		"users", "teams", "team_memberships", "account_quotas", "billing_events", "request_logs",
		"runtime_config",
	}
	for _, table := range tables {
		var name string
		err := store.ReadDB.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	indexes := []string{
		"idx_users_display_name",
		"idx_teams_name",
		"idx_team_memberships_team_id",
		"idx_team_memberships_user_id",
		"uq_team_memberships_current_period",
		"idx_billing_events_account_occurred",
		"idx_request_logs_occurred",
	}
	for _, index := range indexes {
		var name, ddl string
		err := store.ReadDB.QueryRowContext(ctx,
			`SELECT name, sql FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&name, &ddl)
		if err != nil {
			t.Fatalf("index %s missing: %v", index, err)
		}
		if index == "uq_team_memberships_current_period" && !strings.Contains(ddl, "WHERE status IN") {
			t.Fatalf("partial index DDL = %q, want WHERE status IN", ddl)
		}
	}
}

func TestOptimisticLocking(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	users := NewUserRepository(store)
	teams := NewTeamRepository(store)
	memberships := NewMembershipRepository(store)

	t.Run("user", func(t *testing.T) {
		user := mustUser(t, "Alice", "alice@example.com")
		if err := users.Create(ctx, user); err != nil {
			t.Fatalf("create user: %v", err)
		}
		first, err := users.GetByID(ctx, user.ID())
		if err != nil {
			t.Fatalf("get first user: %v", err)
		}
		second, err := users.GetByID(ctx, user.ID())
		if err != nil {
			t.Fatalf("get second user: %v", err)
		}
		first.ChangeEmail(mustEmail(t, "new@example.com"))
		if err := users.Update(ctx, first); err != nil {
			t.Fatalf("first update: %v", err)
		}
		second.ChangeEmail(mustEmail(t, "stale@example.com"))
		if err := users.Update(ctx, second); !errors.Is(err, port.ErrVersionConflict) {
			t.Fatalf("stale update error = %v, want ErrVersionConflict", err)
		}
		reloaded, err := users.GetByID(ctx, user.ID())
		if err != nil {
			t.Fatalf("reload user: %v", err)
		}
		if reloaded.Version().Int64() != 1 {
			t.Fatalf("version = %d, want 1", reloaded.Version().Int64())
		}
		if reloaded.Email().String() != "new@example.com" {
			t.Fatalf("email = %q, want first update to win", reloaded.Email().String())
		}
		ghost := mustUser(t, "Ghost", "ghost@example.com")
		if err := users.Update(ctx, ghost); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("missing user update error = %v, want ErrNotFound", err)
		}
	})

	t.Run("team", func(t *testing.T) {
		team := mustTeam(t, "Engineering")
		if err := teams.Create(ctx, team); err != nil {
			t.Fatalf("create team: %v", err)
		}
		first, err := teams.GetByID(ctx, team.ID())
		if err != nil {
			t.Fatalf("get first team: %v", err)
		}
		second, err := teams.GetByID(ctx, team.ID())
		if err != nil {
			t.Fatalf("get second team: %v", err)
		}
		first.ChangeName(mustTeamName(t, "Platform"))
		if err := teams.Update(ctx, first); err != nil {
			t.Fatalf("first update: %v", err)
		}
		second.ChangeName(mustTeamName(t, "Stale"))
		if err := teams.Update(ctx, second); !errors.Is(err, port.ErrVersionConflict) {
			t.Fatalf("stale update error = %v, want ErrVersionConflict", err)
		}
		reloaded, err := teams.GetByID(ctx, team.ID())
		if err != nil {
			t.Fatalf("reload team: %v", err)
		}
		if reloaded.Version().Int64() != 1 {
			t.Fatalf("version = %d, want 1", reloaded.Version().Int64())
		}
		if reloaded.Name().String() != "Platform" {
			t.Fatalf("name = %q, want Platform", reloaded.Name().String())
		}
		ghost := mustTeam(t, "Ghost")
		if err := teams.Update(ctx, ghost); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("missing team update error = %v, want ErrNotFound", err)
		}
	})

	t.Run("membership", func(t *testing.T) {
		team := mustTeam(t, "Membership")
		if err := teams.Create(ctx, team); err != nil {
			t.Fatalf("create team: %v", err)
		}
		user := mustUser(t, "Bob", "bob@example.com")
		if err := users.Create(ctx, user); err != nil {
			t.Fatalf("create user: %v", err)
		}
		membership := domain.JoinTeam(team.ID(), user.ID())
		if err := memberships.Create(ctx, membership); err != nil {
			t.Fatalf("create membership: %v", err)
		}
		first, err := memberships.GetByID(ctx, membership.ID())
		if err != nil {
			t.Fatalf("get first membership: %v", err)
		}
		second, err := memberships.GetByID(ctx, membership.ID())
		if err != nil {
			t.Fatalf("get second membership: %v", err)
		}
		if err := first.ChangeRole(domain.RoleAdmin); err != nil {
			t.Fatalf("change role: %v", err)
		}
		if err := memberships.Update(ctx, first); err != nil {
			t.Fatalf("first update: %v", err)
		}
		if err := second.ChangeRole(domain.RoleOwner); err != nil {
			t.Fatalf("change stale role: %v", err)
		}
		if err := memberships.Update(ctx, second); !errors.Is(err, port.ErrVersionConflict) {
			t.Fatalf("stale update error = %v, want ErrVersionConflict", err)
		}
		reloaded, err := memberships.GetByID(ctx, membership.ID())
		if err != nil {
			t.Fatalf("reload membership: %v", err)
		}
		if reloaded.Role() != domain.RoleAdmin {
			t.Fatalf("role = %q, want ADMIN", reloaded.Role())
		}
		if reloaded.Version().Int64() != 1 {
			t.Fatalf("version = %d, want 1", reloaded.Version().Int64())
		}
	})
}

func TestMembershipPartialUniqueIndexAndRejoin(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	teams := NewTeamRepository(store)
	users := NewUserRepository(store)
	memberships := NewMembershipRepository(store)

	team := mustTeam(t, "Engineering")
	if err := teams.Create(ctx, team); err != nil {
		t.Fatalf("create team: %v", err)
	}
	user := mustUser(t, "Alice", "alice@example.com")
	if err := users.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	first := domain.JoinTeam(team.ID(), user.ID())
	if err := memberships.Create(ctx, first); err != nil {
		t.Fatalf("create active membership: %v", err)
	}

	duplicateActive := domain.RequestJoinTeam(team.ID(), user.ID())
	if err := memberships.Create(ctx, duplicateActive); !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("duplicate active/pending create error = %v, want ErrAlreadyExists", err)
	}

	if err := first.Leave(); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if err := memberships.Update(ctx, first); err != nil {
		t.Fatalf("persist leave: %v", err)
	}

	pending := domain.RequestJoinTeam(team.ID(), user.ID())
	if err := memberships.Create(ctx, pending); err != nil {
		t.Fatalf("rejoin pending: %v", err)
	}
	duplicatePending := domain.JoinTeam(team.ID(), user.ID())
	if err := memberships.Create(ctx, duplicatePending); !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("duplicate pending create error = %v, want ErrAlreadyExists", err)
	}

	if err := pending.Leave(); err != nil {
		t.Fatalf("leave pending: %v", err)
	}
	if err := memberships.Update(ctx, pending); err != nil {
		t.Fatalf("persist pending leave: %v", err)
	}

	rejoined := domain.JoinTeam(team.ID(), user.ID())
	if err := memberships.Create(ctx, rejoined); err != nil {
		t.Fatalf("rejoin active: %v", err)
	}
	if rejoined.ID() == first.ID() || rejoined.ID() == pending.ID() {
		t.Fatal("rejoin must allocate a new membership ID")
	}

	history, err := memberships.ListByTeam(ctx, team.ID())
	if err != nil {
		t.Fatalf("list memberships: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	statuses := make(map[domain.MembershipID]domain.MembershipStatus, len(history))
	for _, membership := range history {
		statuses[membership.ID()] = membership.Status()
	}
	if statuses[first.ID()] != domain.StatusLeaved {
		t.Errorf("first membership status = %q, want LEAVED", statuses[first.ID()])
	}
	if statuses[pending.ID()] != domain.StatusLeaved {
		t.Errorf("pending membership status = %q, want LEAVED", statuses[pending.ID()])
	}
	if statuses[rejoined.ID()] != domain.StatusActive {
		t.Errorf("rejoined membership status = %q, want ACTIVE", statuses[rejoined.ID()])
	}
}

func TestWriteBatch(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	quotas := NewQuotaRepository(store)
	audit := NewAuditQuery(store)

	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	eventOne := domain.BillingEvent{
		ID:         domain.NewEventID(),
		AccountID:  "acct_a",
		Amount:     -1,
		Reason:     "request",
		RequestID:  "req_1",
		OccurredAt: base,
	}
	eventTwo := domain.BillingEvent{
		ID:         domain.NewEventID(),
		AccountID:  "acct_b",
		Amount:     50,
		Reason:     "top_up",
		RequestID:  "req_2",
		OccurredAt: base.Add(time.Minute),
	}
	logOne := domain.RequestLog{
		ID: "log_1", RequestID: "req_1", AccountID: "acct_a",
		Method: "GET", Path: "/users", StatusCode: 200, LatencyUS: 120,
		OccurredAt: base,
	}
	logTwo := domain.RequestLog{
		ID: "log_2", RequestID: "req_2", AccountID: "acct_b",
		Method: "POST", Path: "/teams", StatusCode: 201, LatencyUS: 250,
		OccurredAt: base.Add(time.Minute),
	}
	payload := port.BatchPayload{
		BillingEvents: []domain.BillingEvent{eventOne, eventTwo},
		Quotas: []domain.AccountQuota{
			{AccountID: "acct_a", Balance: 100, RateLimitRPM: 10, Seq: 1, UpdatedAt: base},
			{AccountID: "acct_b", Balance: 50, RateLimitRPM: 20, Seq: 1, UpdatedAt: base},
			{AccountID: "acct_a", Balance: 999, RateLimitRPM: 30, Seq: 2, UpdatedAt: base.Add(time.Second)},
		},
		RequestLogs: []domain.RequestLog{logOne, logTwo},
	}
	if err := store.WriteBatch(ctx, payload); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	quotaA, err := quotas.GetByAccountID(ctx, "acct_a")
	if err != nil {
		t.Fatalf("get quota A: %v", err)
	}
	if quotaA.Balance != 999 || quotaA.RateLimitRPM != 30 || quotaA.Seq != 2 {
		t.Fatalf("quota A = %+v, want latest deduplicated value with seq 2", quotaA)
	}
	allQuotas, err := quotas.ListAll(ctx)
	if err != nil {
		t.Fatalf("list quotas: %v", err)
	}
	if len(allQuotas) != 2 {
		t.Fatalf("quota count = %d, want 2", len(allQuotas))
	}

	events, err := audit.ListBillingEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("list billing events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("billing event count = %d, want 2", len(events))
	}
	if events[0].ID != eventTwo.ID {
		t.Fatalf("billing order = %s, want newest %s first", events[0].ID, eventTwo.ID)
	}
	accountEvents, err := audit.ListBillingEvents(ctx, "acct_a", 10)
	if err != nil {
		t.Fatalf("list account billing events: %v", err)
	}
	if len(accountEvents) != 1 || accountEvents[0].ID != eventOne.ID {
		t.Fatalf("account billing events = %+v", accountEvents)
	}
	limitedEvents, err := audit.ListBillingEvents(ctx, "", 1)
	if err != nil {
		t.Fatalf("list limited billing events: %v", err)
	}
	if len(limitedEvents) != 1 || limitedEvents[0].ID != eventTwo.ID {
		t.Fatalf("limited billing events = %+v", limitedEvents)
	}

	logs, err := audit.ListRequestLogs(ctx, "", 10)
	if err != nil {
		t.Fatalf("list request logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("request log count = %d, want 2", len(logs))
	}
	if logs[0].ID != "log_2" {
		t.Fatalf("request log order = %s, want log_2 first", logs[0].ID)
	}
	accountLogs, err := audit.ListRequestLogs(ctx, "acct_a", 10)
	if err != nil {
		t.Fatalf("list account request logs: %v", err)
	}
	if len(accountLogs) != 1 || accountLogs[0].ID != "log_1" {
		t.Fatalf("account request logs = %+v", accountLogs)
	}

	// A later batch updates the existing quota row instead of inserting a
	// duplicate account.
	if err := store.WriteBatch(ctx, port.BatchPayload{
		Quotas: []domain.AccountQuota{
			{AccountID: "acct_a", Balance: 555, RateLimitRPM: 60, Seq: 3, UpdatedAt: base.Add(2 * time.Second)},
		},
	}); err != nil {
		t.Fatalf("second WriteBatch: %v", err)
	}
	quotaA, err = quotas.GetByAccountID(ctx, "acct_a")
	if err != nil {
		t.Fatalf("get updated quota A: %v", err)
	}
	if quotaA.Balance != 555 || quotaA.RateLimitRPM != 60 || quotaA.Seq != 3 {
		t.Fatalf("quota A after upsert = %+v, want balance 555 rpm 60 seq 3", quotaA)
	}
	allQuotas, err = quotas.ListAll(ctx)
	if err != nil {
		t.Fatalf("list quotas after upsert: %v", err)
	}
	if len(allQuotas) != 2 {
		t.Fatalf("quota count after upsert = %d, want 2", len(allQuotas))
	}

	// A failing event must roll back the whole payload.
	duplicateID := domain.NewEventID()
	failing := port.BatchPayload{
		BillingEvents: []domain.BillingEvent{
			{ID: duplicateID, AccountID: "acct_c", Amount: -1, Reason: "request", RequestID: "req_3", OccurredAt: base},
			{ID: duplicateID, AccountID: "acct_c", Amount: -1, Reason: "request", RequestID: "req_4", OccurredAt: base},
		},
		Quotas: []domain.AccountQuota{
			{AccountID: "acct_c", Balance: 123, RateLimitRPM: 1, UpdatedAt: base},
		},
		RequestLogs: []domain.RequestLog{
			{ID: "log_3", RequestID: "req_3", AccountID: "acct_c", Method: "GET", Path: "/fail", StatusCode: 500, OccurredAt: base},
		},
	}
	if err := store.WriteBatch(ctx, failing); err == nil {
		t.Fatal("WriteBatch with duplicate billing ID succeeded, want failure")
	} else if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("WriteBatch error = %v, want ErrAlreadyExists", err)
	}

	if _, err := quotas.GetByAccountID(ctx, "acct_c"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("quota C after rollback error = %v, want ErrNotFound", err)
	}
	eventsAfter, err := audit.ListBillingEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("list billing after rollback: %v", err)
	}
	if len(eventsAfter) != 2 {
		t.Fatalf("billing count after rollback = %d, want 2", len(eventsAfter))
	}
	logsAfter, err := audit.ListRequestLogs(ctx, "", 10)
	if err != nil {
		t.Fatalf("list logs after rollback: %v", err)
	}
	if len(logsAfter) != 2 {
		t.Fatalf("log count after rollback = %d, want 2", len(logsAfter))
	}
}

func TestWriteBatchKeepsHighestSeqWithinBatch(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	quotas := NewQuotaRepository(store)
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	payload := port.BatchPayload{Quotas: []domain.AccountQuota{
		{AccountID: "acct_seq", Balance: 70, RateLimitRPM: 10, Seq: 2, UpdatedAt: base},
		{AccountID: "acct_seq", Balance: 90, RateLimitRPM: 20, Seq: 1, UpdatedAt: base.Add(time.Second)},
	}}

	deduped := dedupeQuotas(payload.Quotas)
	if len(deduped) != 1 {
		t.Fatalf("dedupeQuotas length = %d, want 1", len(deduped))
	}
	assertQuota(t, deduped[0], 70, 10, 2)

	if err := store.WriteBatch(ctx, payload); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	got, err := quotas.GetByAccountID(ctx, "acct_seq")
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	assertQuota(t, got, 70, 10, 2)
}

func TestWriteBatchIgnoresStaleSeqAcrossBatches(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	quotas := NewQuotaRepository(store)
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	if err := store.WriteBatch(ctx, port.BatchPayload{Quotas: []domain.AccountQuota{
		{AccountID: "acct_seq", Balance: 70, RateLimitRPM: 10, Seq: 2, UpdatedAt: base},
	}}); err != nil {
		t.Fatalf("first WriteBatch: %v", err)
	}
	// A delayed snapshot with a lower Seq must not roll the balance back.
	if err := store.WriteBatch(ctx, port.BatchPayload{Quotas: []domain.AccountQuota{
		{AccountID: "acct_seq", Balance: 90, RateLimitRPM: 20, Seq: 1, UpdatedAt: base.Add(time.Second)},
	}}); err != nil {
		t.Fatalf("second WriteBatch: %v", err)
	}

	got, err := quotas.GetByAccountID(ctx, "acct_seq")
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	assertQuota(t, got, 70, 10, 2)
}

func TestWriteBatchAppliesSameSeqUpdate(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	quotas := NewQuotaRepository(store)
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	if err := store.WriteBatch(ctx, port.BatchPayload{Quotas: []domain.AccountQuota{
		{AccountID: "acct_seq", Balance: 70, RateLimitRPM: 10, Seq: 2, UpdatedAt: base},
	}}); err != nil {
		t.Fatalf("first WriteBatch: %v", err)
	}
	// Replaying the same Seq is idempotent for the balance and still applies a
	// corrected rate limit.
	if err := store.WriteBatch(ctx, port.BatchPayload{Quotas: []domain.AccountQuota{
		{AccountID: "acct_seq", Balance: 70, RateLimitRPM: 60, Seq: 2, UpdatedAt: base.Add(time.Second)},
	}}); err != nil {
		t.Fatalf("second WriteBatch: %v", err)
	}

	got, err := quotas.GetByAccountID(ctx, "acct_seq")
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	assertQuota(t, got, 70, 60, 2)
}

func TestQuotaRepositoryPreservesSeqAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	ctx := context.Background()
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	quota := domain.AccountQuota{
		AccountID: "acct_seq", Balance: 70, RateLimitRPM: 45, Seq: 7, UpdatedAt: base,
	}

	first, err := Open(ctx, path, 5)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := first.WriteBatch(ctx, port.BatchPayload{
		Quotas: []domain.AccountQuota{quota},
	}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	got, err := NewQuotaRepository(first).GetByAccountID(ctx, quota.AccountID)
	if err != nil {
		t.Fatalf("get quota before reopen: %v", err)
	}
	assertQuota(t, got, 70, 45, 7)
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := Open(ctx, path, 5)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
	repo := NewQuotaRepository(second)
	got, err = repo.GetByAccountID(ctx, quota.AccountID)
	if err != nil {
		t.Fatalf("get quota after reopen: %v", err)
	}
	assertQuota(t, got, 70, 45, 7)

	listed, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll after reopen: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListAll length = %d, want 1", len(listed))
	}
	assertQuota(t, listed[0], 70, 45, 7)
	if !listed[0].UpdatedAt.Equal(base) {
		t.Fatalf("UpdatedAt = %s, want %s", listed[0].UpdatedAt, base)
	}
}

func TestHighConcurrencyReadersAndWriters(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	teams := NewTeamRepository(store)
	audit := NewAuditQuery(store)

	const (
		writerCount = 20
		readerCount = 20
		iterations  = 10
		readerLoops = 50
	)

	seeded := make([]*domain.Team, 0, writerCount)
	for index := range writerCount {
		team := mustTeam(t, fmt.Sprintf("Team %02d", index))
		if err := teams.Create(ctx, team); err != nil {
			t.Fatalf("seed team %d: %v", index, err)
		}
		seeded = append(seeded, team)
	}

	start := make(chan struct{})
	done := make(chan struct{})
	errCh := make(chan error, writerCount*iterations+readerCount*readerLoops*3)

	var writers sync.WaitGroup
	writers.Add(writerCount)
	for index := range writerCount {
		go func(index int) {
			defer writers.Done()
			<-start
			team := seeded[index]
			for iteration := range iterations {
				current, err := teams.GetByID(ctx, team.ID())
				if err != nil {
					errCh <- fmt.Errorf("writer %d get: %w", index, err)
					return
				}
				current.ChangeName(mustTeamName(t, fmt.Sprintf("Team %02d v%02d", index, iteration)))
				if err := teams.Update(ctx, current); err != nil {
					errCh <- fmt.Errorf("writer %d update: %w", index, err)
					return
				}
			}
		}(index)
	}

	var readers sync.WaitGroup
	readers.Add(readerCount)
	for index := range readerCount {
		go func(index int) {
			defer readers.Done()
			<-start
			for range readerLoops {
				select {
				case <-done:
					return
				default:
				}
				if _, err := teams.List(ctx); err != nil {
					errCh <- fmt.Errorf("reader %d list: %w", index, err)
					return
				}
				if _, err := teams.GetByID(ctx, seeded[index%writerCount].ID()); err != nil {
					errCh <- fmt.Errorf("reader %d get: %w", index, err)
					return
				}
				if _, err := audit.ListRequestLogs(ctx, "", 10); err != nil {
					errCh <- fmt.Errorf("reader %d audit: %w", index, err)
					return
				}
			}
		}(index)
	}

	close(start)
	writers.Wait()
	close(done)
	readers.Wait()
	close(errCh)

	busyErrors := 0
	for err := range errCh {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "busy") || strings.Contains(message, "locked") {
			busyErrors++
		}
		t.Errorf("concurrent operation failed: %v", err)
	}
	if busyErrors != 0 {
		t.Fatalf("SQLITE_BUSY count = %d, want 0", busyErrors)
	}

	all, err := teams.List(ctx)
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	if len(all) != writerCount {
		t.Fatalf("team count = %d, want %d", len(all), writerCount)
	}
	for _, team := range all {
		if team.Version().Int64() != iterations {
			t.Fatalf("team %s version = %d, want %d", team.ID(), team.Version().Int64(), iterations)
		}
	}
}

func TestRepositoriesRoundTripAndNotFound(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	users := NewUserRepository(store)
	teams := NewTeamRepository(store)
	memberships := NewMembershipRepository(store)
	quotas := NewQuotaRepository(store)

	user := mustUser(t, "Alice", "alice@example.com")
	if err := users.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := users.Create(ctx, user); !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("duplicate user error = %v, want ErrAlreadyExists", err)
	}
	duplicateEmail := mustUser(t, "Another Alice", "alice@example.com")
	if err := users.Create(ctx, duplicateEmail); !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("duplicate email error = %v, want ErrAlreadyExists", err)
	}
	team := mustTeam(t, "Engineering")
	if err := teams.Create(ctx, team); err != nil {
		t.Fatalf("create team: %v", err)
	}
	membership := domain.JoinTeam(team.ID(), user.ID())
	if err := memberships.Create(ctx, membership); err != nil {
		t.Fatalf("create membership: %v", err)
	}

	listedUsers, err := users.List(ctx)
	if err != nil || len(listedUsers) != 1 {
		t.Fatalf("List users = %d, %v", len(listedUsers), err)
	}
	listedTeams, err := teams.List(ctx)
	if err != nil || len(listedTeams) != 1 {
		t.Fatalf("List teams = %d, %v", len(listedTeams), err)
	}
	listedMemberships, err := memberships.ListByTeam(ctx, team.ID())
	if err != nil || len(listedMemberships) != 1 {
		t.Fatalf("ListByTeam = %d, %v", len(listedMemberships), err)
	}

	if _, err := users.GetByID(ctx, domain.NewUserID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("missing user error = %v, want ErrNotFound", err)
	}
	if _, err := teams.GetByID(ctx, domain.NewTeamID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("missing team error = %v, want ErrNotFound", err)
	}
	if _, err := memberships.GetByID(ctx, domain.NewMembershipID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("missing membership error = %v, want ErrNotFound", err)
	}
	if _, err := quotas.GetByAccountID(ctx, "missing"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("missing quota error = %v, want ErrNotFound", err)
	}
}

func TestOpenClampsReadConnections(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{0, 5},
		{1, 4},
		{4, 4},
		{5, 5},
		{6, 6},
		{100, 6},
	} {
		t.Run(fmt.Sprintf("%d", tc.in), func(t *testing.T) {
			store := openTestStore(t, tc.in)
			if store.readConns != tc.want {
				t.Fatalf("readConns = %d, want %d", store.readConns, tc.want)
			}
			stats := store.ReadDB.Stats()
			if stats.MaxOpenConnections != tc.want {
				t.Fatalf("MaxOpenConnections = %d, want %d", stats.MaxOpenConnections, tc.want)
			}
		})
	}
}

func TestWithWriteTxRollbackAndPanic(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	users := NewUserRepository(store)
	teams := NewTeamRepository(store)

	t.Run("error rolls back every repository", func(t *testing.T) {
		user := mustUser(t, "Rollback", "rollback@example.com")
		team := mustTeam(t, "Rollback")
		boom := errors.New("boom")
		err := store.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
			if err := repos.Users().Create(ctx, user); err != nil {
				return err
			}
			if err := repos.Teams().Create(ctx, team); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("WithWriteTx error = %v, want boom", err)
		}
		if _, err := users.GetByID(ctx, user.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("user after rollback error = %v, want ErrNotFound", err)
		}
		if _, err := teams.GetByID(ctx, team.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("team after rollback error = %v, want ErrNotFound", err)
		}
	})

	t.Run("panic rolls back and releases the write lock", func(t *testing.T) {
		user := mustUser(t, "Panic", "panic@example.com")
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_ = store.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
				if err := repos.Users().Create(ctx, user); err != nil {
					return err
				}
				panic("boom")
			})
		}()
		if recovered != "boom" {
			t.Fatalf("recovered = %v, want boom", recovered)
		}
		if _, err := users.GetByID(ctx, user.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("user after panic error = %v, want ErrNotFound", err)
		}

		after := mustUser(t, "After", "after@example.com")
		if err := users.Create(ctx, after); err != nil {
			t.Fatalf("write after panic: %v", err)
		}
	})
}

func TestOpenCreatesParentAndReopensExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "flow.db")
	ctx := context.Background()

	first, err := Open(ctx, path, 5)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	user := mustUser(t, "Persisted", "persisted@example.com")
	if err := NewUserRepository(first).Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := Open(ctx, path, 5)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
	reloaded, err := NewUserRepository(second).GetByID(ctx, user.ID())
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if reloaded.Email().String() != "persisted@example.com" {
		t.Fatalf("email = %q", reloaded.Email().String())
	}
	var migrationsApplied int
	if err := second.ReadDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationsApplied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationsApplied != 3 {
		t.Fatalf("schema_migrations count = %d, want 3", migrationsApplied)
	}
}

func TestWithWriteTxCommitsTxScopedRepositories(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	users := NewUserRepository(store)
	teams := NewTeamRepository(store)
	memberships := NewMembershipRepository(store)

	user := mustUser(t, "Tx", "tx@example.com")
	team := mustTeam(t, "Tx Team")
	membership := domain.JoinTeam(team.ID(), user.ID())

	err := store.WithWriteTx(ctx, func(ctx context.Context, repos port.WriteRepos) error {
		if err := repos.Users().Create(ctx, user); err != nil {
			return err
		}
		if err := repos.Teams().Create(ctx, team); err != nil {
			return err
		}
		if err := repos.Memberships().Create(ctx, membership); err != nil {
			return err
		}
		// Reads inside the transaction must observe the uncommitted writes.
		reloaded, err := repos.Users().GetByID(ctx, user.ID())
		if err != nil {
			return err
		}
		if reloaded.Email().String() != "tx@example.com" {
			return fmt.Errorf("unexpected in-tx email %q", reloaded.Email().String())
		}
		listed, err := repos.Teams().List(ctx)
		if err != nil {
			return err
		}
		if len(listed) != 1 {
			return fmt.Errorf("in-tx team count = %d, want 1", len(listed))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithWriteTx: %v", err)
	}

	if _, err := users.GetByID(ctx, user.ID()); err != nil {
		t.Fatalf("committed user: %v", err)
	}
	if _, err := teams.GetByID(ctx, team.ID()); err != nil {
		t.Fatalf("committed team: %v", err)
	}
	if _, err := memberships.GetByID(ctx, membership.ID()); err != nil {
		t.Fatalf("committed membership: %v", err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), "   ", 5); err == nil {
		t.Fatal("Open with blank path succeeded, want error")
	}
	if _, err := Open(context.Background(), "", 5); err == nil {
		t.Fatal("Open with empty path succeeded, want error")
	}
}

func TestTimestampOrderingUsesFixedWidthUTC(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	audit := NewAuditQuery(store)

	base := time.Date(2026, time.November, 24, 6, 56, 0, 0, time.UTC)
	half := base.Add(500 * time.Millisecond)
	beforeEvent := domain.BillingEvent{
		ID: domain.NewEventID(), AccountID: "acct_order", Amount: -1,
		Reason: "request", RequestID: "req_before", OccurredAt: base,
	}
	afterEvent := domain.BillingEvent{
		ID: domain.NewEventID(), AccountID: "acct_order", Amount: -1,
		Reason: "request", RequestID: "req_after", OccurredAt: half,
	}
	if err := store.WriteBatch(ctx, port.BatchPayload{
		BillingEvents: []domain.BillingEvent{beforeEvent, afterEvent},
		RequestLogs: []domain.RequestLog{
			{
				ID: "log_before", RequestID: "req_before", AccountID: "acct_order",
				Method: "GET", Path: "/before", StatusCode: 200, OccurredAt: base,
			},
			{
				ID: "log_after", RequestID: "req_after", AccountID: "acct_order",
				Method: "GET", Path: "/after", StatusCode: 200, OccurredAt: half,
			},
		},
	}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	events, err := audit.ListBillingEvents(ctx, "acct_order", 10)
	if err != nil {
		t.Fatalf("ListBillingEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("billing event count = %d, want 2", len(events))
	}
	if events[0].ID != afterEvent.ID {
		t.Fatalf("billing order = %s first, want .5s event %s", events[0].ID, afterEvent.ID)
	}
	if events[1].ID != beforeEvent.ID {
		t.Fatalf("billing order = %s second, want .0s event %s", events[1].ID, beforeEvent.ID)
	}

	logs, err := audit.ListRequestLogs(ctx, "acct_order", 10)
	if err != nil {
		t.Fatalf("ListRequestLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("request log count = %d, want 2", len(logs))
	}
	if logs[0].ID != "log_after" {
		t.Fatalf("request log order = %s first, want log_after", logs[0].ID)
	}
	if logs[1].ID != "log_before" {
		t.Fatalf("request log order = %s second, want log_before", logs[1].ID)
	}
}

func TestRuntimeConfigRepositoryLoadMissing(t *testing.T) {
	store := openTestStore(t, 5)
	config, found, err := NewRuntimeConfigRepository(store).Load(context.Background())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if found {
		t.Fatal("Load() found = true, want false")
	}
	if config != nil {
		t.Fatalf("Load() config = %+v, want nil", config)
	}
}

func TestRuntimeConfigRepositoryRoundTripAndOverwrite(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()
	repo := NewRuntimeConfigRepository(store)

	first := &domain.RuntimeConfig{
		DefaultRateLimitRPM: 120,
		DefaultRequestCost:  3,
		MaintenanceMode:     true,
		Routes: map[string]domain.RoutePolicy{
			"GET /api/v1/users": {Enabled: true, Cost: 2, RateLimitRPM: 30},
			"GET /api/v1/teams": {Enabled: false, Cost: 1, RateLimitRPM: 0},
		},
	}
	if err := repo.Save(ctx, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	loaded, found, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load(first): %v", err)
	}
	if !found {
		t.Fatal("Load(first) found = false, want true")
	}
	if !reflect.DeepEqual(loaded, first) {
		t.Fatalf("Load(first) = %+v, want %+v", loaded, first)
	}

	second := &domain.RuntimeConfig{
		DefaultRateLimitRPM: 60,
		DefaultRequestCost:  1,
		MaintenanceMode:     false,
	}
	if err := repo.Save(ctx, second); err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	loaded, found, err = repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load(second): %v", err)
	}
	if !found {
		t.Fatal("Load(second) found = false, want true")
	}
	if !reflect.DeepEqual(loaded, second) {
		t.Fatalf("Load(second) = %+v, want %+v", loaded, second)
	}

	if err := repo.Save(ctx, nil); err == nil {
		t.Fatal("Save(nil) succeeded, want error")
	}
}

func TestRuntimeConfigRepositoryPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	ctx := context.Background()

	first, err := Open(ctx, path, 5)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	config := &domain.RuntimeConfig{
		DefaultRateLimitRPM: 90,
		DefaultRequestCost:  2,
		MaintenanceMode:     true,
		Routes: map[string]domain.RoutePolicy{
			"POST /api/v1/teams": {Enabled: true, Cost: 4, RateLimitRPM: 15},
		},
	}
	if err := NewRuntimeConfigRepository(first).Save(ctx, config); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := Open(ctx, path, 5)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
	loaded, found, err := NewRuntimeConfigRepository(second).Load(ctx)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if !found {
		t.Fatal("Load() found = false, want true")
	}
	if !reflect.DeepEqual(loaded, config) {
		t.Fatalf("Load() = %+v, want %+v", loaded, config)
	}
}

func TestHealthReportsWALSizeAndThreshold(t *testing.T) {
	store := openTestStore(t, 5)
	ctx := context.Background()

	if err := NewUserRepository(store).Create(ctx, mustUser(t, "WAL", "wal@example.com")); err != nil {
		t.Fatalf("create user: %v", err)
	}

	store.SetWALWarnBytes(1)
	health, err := store.Health(ctx)
	if err != nil {
		t.Fatalf("Health(): %v", err)
	}
	if health.WALBytes <= 1 {
		t.Fatalf("WALBytes = %d, want > 1 after a write", health.WALBytes)
	}
	if health.WALWarnBytes != 1 {
		t.Errorf("WALWarnBytes = %d, want 1", health.WALWarnBytes)
	}
	if health.WALHealthy {
		t.Errorf("WALHealthy = true, WALBytes = %d, WALWarnBytes = %d", health.WALBytes, health.WALWarnBytes)
	}
	if health.OK {
		t.Errorf("OK = true with a WAL over the threshold: %+v", health)
	}

	store.SetWALWarnBytes(0)
	health, err = store.Health(ctx)
	if err != nil {
		t.Fatalf("Health() with disabled threshold: %v", err)
	}
	if health.WALWarnBytes != 0 {
		t.Errorf("WALWarnBytes = %d, want 0", health.WALWarnBytes)
	}
	if !health.WALHealthy {
		t.Errorf("WALHealthy = false with the threshold disabled: %+v", health)
	}
	if !health.OK {
		t.Errorf("OK = false with the threshold disabled: %+v", health)
	}

	store.SetWALWarnBytes(-1)
	if got := store.walWarnBytes.Load(); got != 0 {
		t.Errorf("WALWarnBytes after negative set = %d, want 0", got)
	}
}

func TestFormatTimestampIsFixedWidthUTC(t *testing.T) {
	base := time.Date(2026, time.November, 24, 6, 56, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"zero nanoseconds", base, "2026-11-24T06:56:00.000000000Z"},
		{"half second", base.Add(500 * time.Millisecond), "2026-11-24T06:56:00.500000000Z"},
		{
			"non-UTC offset",
			base.Add(123456789 * time.Nanosecond).In(time.FixedZone("JST", 9*60*60)),
			"2026-11-24T06:56:00.123456789Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTimestamp(tc.in)
			if got != tc.want {
				t.Fatalf("formatTimestamp() = %q, want %q", got, tc.want)
			}
			if len(got) != 30 {
				t.Fatalf("formatTimestamp() length = %d, want 30", len(got))
			}
			parsed, err := parseTimestamp("test", got)
			if err != nil {
				t.Fatalf("parseTimestamp(%q): %v", got, err)
			}
			if !parsed.Equal(tc.in) {
				t.Fatalf("parsed = %s, want %s", parsed, tc.in)
			}
		})
	}
}
