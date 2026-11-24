// Package port defines the interfaces the application core uses to talk to
// persistence and the in-memory hot path. Implementations live under
// internal/infra and are wired in the composition root.
package port

import (
	"context"

	"github.com/aiagate/flow-app-template/internal/domain"
)

// UserRepository persists user aggregates.
type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id domain.UserID) (*domain.User, error)
	List(ctx context.Context) ([]*domain.User, error)
	Update(ctx context.Context, user *domain.User) error
}

// TeamRepository persists team aggregates.
type TeamRepository interface {
	Create(ctx context.Context, team *domain.Team) error
	GetByID(ctx context.Context, id domain.TeamID) (*domain.Team, error)
	List(ctx context.Context) ([]*domain.Team, error)
	Update(ctx context.Context, team *domain.Team) error
}

// MembershipRepository persists membership-period aggregates.
type MembershipRepository interface {
	Create(ctx context.Context, membership *domain.TeamMembership) error
	GetByID(ctx context.Context, id domain.MembershipID) (*domain.TeamMembership, error)
	ListByTeam(ctx context.Context, teamID domain.TeamID) ([]*domain.TeamMembership, error)
	Update(ctx context.Context, membership *domain.TeamMembership) error
}

// QuotaRepository reads persisted account quotas. Writes happen through the
// asynchronous batch sink, never on the hot path.
type QuotaRepository interface {
	ListAll(ctx context.Context) ([]domain.AccountQuota, error)
	GetByAccountID(ctx context.Context, accountID string) (domain.AccountQuota, error)
}

// AuditQuery reads append-only billing and request history.
type AuditQuery interface {
	ListBillingEvents(ctx context.Context, accountID string, limit int) ([]domain.BillingEvent, error)
	ListRequestLogs(ctx context.Context, accountID string, limit int) ([]domain.RequestLog, error)
}

// WriteRepos exposes repositories bound to one write transaction.
type WriteRepos interface {
	Users() UserRepository
	Teams() TeamRepository
	Memberships() MembershipRepository
}

// WriteTxManager serializes and commits a unit of work. The callback must not
// retain the repositories after it returns.
type WriteTxManager interface {
	WithWriteTx(ctx context.Context, fn func(ctx context.Context, repos WriteRepos) error) error
}

// DBHealthChecker reports the actual database settings after startup.
type DBHealthChecker interface {
	Health(ctx context.Context) (domain.DBHealth, error)
}
