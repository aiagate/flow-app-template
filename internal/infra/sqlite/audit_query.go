package sqlite

import (
	"context"
	"fmt"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

const (
	defaultAuditLimit = 100
	maxAuditLimit     = 1000
)

type auditQuery struct {
	store *Store
}

func (q *auditQuery) ListBillingEvents(ctx context.Context, accountID string, limit int) ([]domain.BillingEvent, error) {
	limit = clampAuditLimit(limit)
	query := `SELECT id, account_id, amount, reason, request_id, occurred_at FROM billing_events`
	args := make([]any, 0, 2)
	if accountID != "" {
		query += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	query += ` ORDER BY occurred_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := q.store.ReadDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list billing events: %w", err)
	}
	defer rows.Close()

	events := make([]domain.BillingEvent, 0)
	for rows.Next() {
		var (
			rawID      string
			account    string
			amount     int64
			reason     string
			requestID  string
			occurredAt string
		)
		if err := rows.Scan(&rawID, &account, &amount, &reason, &requestID, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan billing event: %w", err)
		}
		id, err := domain.ParseEventID(rawID)
		if err != nil {
			return nil, err
		}
		occurred, err := parseTimestamp("billing_event.occurred_at", occurredAt)
		if err != nil {
			return nil, err
		}
		events = append(events, domain.BillingEvent{
			ID:         id,
			AccountID:  account,
			Amount:     amount,
			Reason:     reason,
			RequestID:  requestID,
			OccurredAt: occurred,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate billing events: %w", err)
	}
	return events, nil
}

func (q *auditQuery) ListRequestLogs(ctx context.Context, accountID string, limit int) ([]domain.RequestLog, error) {
	limit = clampAuditLimit(limit)
	query := `SELECT id, request_id, account_id, method, path, status_code, latency_us, occurred_at FROM request_logs`
	args := make([]any, 0, 2)
	if accountID != "" {
		query += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	query += ` ORDER BY occurred_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := q.store.ReadDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list request logs: %w", err)
	}
	defer rows.Close()

	logs := make([]domain.RequestLog, 0)
	for rows.Next() {
		var (
			logRow     domain.RequestLog
			occurredAt string
		)
		if err := rows.Scan(
			&logRow.ID,
			&logRow.RequestID,
			&logRow.AccountID,
			&logRow.Method,
			&logRow.Path,
			&logRow.StatusCode,
			&logRow.LatencyUS,
			&occurredAt,
		); err != nil {
			return nil, fmt.Errorf("scan request log: %w", err)
		}
		occurred, err := parseTimestamp("request_log.occurred_at", occurredAt)
		if err != nil {
			return nil, err
		}
		logRow.OccurredAt = occurred
		logs = append(logs, logRow)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request logs: %w", err)
	}
	return logs, nil
}

func clampAuditLimit(limit int) int {
	if limit <= 0 {
		return defaultAuditLimit
	}
	if limit > maxAuditLimit {
		return maxAuditLimit
	}
	return limit
}

var _ port.AuditQuery = (*auditQuery)(nil)
