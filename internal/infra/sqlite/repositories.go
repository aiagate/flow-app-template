package sqlite

import (
	"database/sql"

	"github.com/aiagate/flow-app-template/internal/contract/port"
)

// writeRepos binds the three aggregate repositories to one transaction.
type writeRepos struct {
	users       port.UserRepository
	teams       port.TeamRepository
	memberships port.MembershipRepository
}

func newWriteRepos(tx *sql.Tx) *writeRepos {
	return &writeRepos{
		users:       &userRepo{repoBase: repoBase{tx: tx}},
		teams:       &teamRepo{repoBase: repoBase{tx: tx}},
		memberships: &membershipRepo{repoBase: repoBase{tx: tx}},
	}
}

func (r *writeRepos) Users() port.UserRepository             { return r.users }
func (r *writeRepos) Teams() port.TeamRepository             { return r.teams }
func (r *writeRepos) Memberships() port.MembershipRepository { return r.memberships }

var _ port.WriteRepos = (*writeRepos)(nil)

// NewUserRepository returns a repository backed by the store's read pool for
// reads and the serialized write pool for writes.
func NewUserRepository(store *Store) port.UserRepository {
	return &userRepo{repoBase: repoBase{store: store}}
}

func NewTeamRepository(store *Store) port.TeamRepository {
	return &teamRepo{repoBase: repoBase{store: store}}
}

func NewMembershipRepository(store *Store) port.MembershipRepository {
	return &membershipRepo{repoBase: repoBase{store: store}}
}

func NewQuotaRepository(store *Store) port.QuotaRepository {
	return &quotaRepo{store: store}
}

func NewAuditQuery(store *Store) port.AuditQuery {
	return &auditQuery{store: store}
}
