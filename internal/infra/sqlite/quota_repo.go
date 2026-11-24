package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

type quotaRepo struct {
	store *Store
}

func (r *quotaRepo) ListAll(ctx context.Context) ([]domain.AccountQuota, error) {
	rows, err := r.store.ReadDB.QueryContext(ctx, `
		SELECT account_id, balance, rate_limit_rpm, updated_at
		FROM account_quotas
		ORDER BY account_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list account quotas: %w", err)
	}
	defer rows.Close()

	quotas := make([]domain.AccountQuota, 0)
	for rows.Next() {
		quota, err := scanQuota(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account quota: %w", err)
		}
		quotas = append(quotas, quota)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account quotas: %w", err)
	}
	return quotas, nil
}

func (r *quotaRepo) GetByAccountID(ctx context.Context, accountID string) (domain.AccountQuota, error) {
	quota, err := scanQuota(r.store.ReadDB.QueryRowContext(ctx, `
		SELECT account_id, balance, rate_limit_rpm, updated_at
		FROM account_quotas
		WHERE account_id = ?`, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccountQuota{}, notFound("get account quota")
	}
	if err != nil {
		return domain.AccountQuota{}, fmt.Errorf("get account quota %s: %w", accountID, err)
	}
	return quota, nil
}

func scanQuota(row scanner) (domain.AccountQuota, error) {
	var (
		accountID string
		balance   int64
		rateLimit int64
		updated   string
	)
	if err := row.Scan(&accountID, &balance, &rateLimit, &updated); err != nil {
		return domain.AccountQuota{}, err
	}
	updatedAt, err := parseTimestamp("account_quota.updated_at", updated)
	if err != nil {
		return domain.AccountQuota{}, err
	}
	return domain.AccountQuota{
		AccountID:    accountID,
		Balance:      balance,
		RateLimitRPM: int(rateLimit),
		UpdatedAt:    updatedAt,
	}, nil
}

var _ port.QuotaRepository = (*quotaRepo)(nil)
