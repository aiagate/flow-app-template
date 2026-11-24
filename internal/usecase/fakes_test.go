package usecase

import (
	"context"
	"sync"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
)

// fakeRepos is an in-memory implementation of the three aggregate repositories
// plus the write transaction manager. It mirrors the SQLite adapter's version
// increments and conflict sentinels closely enough for use-case tests.
type fakeRepos struct {
	mu sync.Mutex

	users       map[string]*domain.User
	userOrder   []string
	teams       map[string]*domain.Team
	teamOrder   []string
	memberships map[string]*domain.TeamMembership
	memberOrder []string
}

func newFakeRepos() *fakeRepos {
	return &fakeRepos{
		users:       make(map[string]*domain.User),
		teams:       make(map[string]*domain.Team),
		memberships: make(map[string]*domain.TeamMembership),
	}
}

func (f *fakeRepos) WithWriteTx(ctx context.Context, fn func(context.Context, port.WriteRepos) error) error {
	return fn(ctx, f)
}

func (f *fakeRepos) Users() port.UserRepository             { return fakeUserRepo{store: f} }
func (f *fakeRepos) Teams() port.TeamRepository             { return fakeTeamRepo{store: f} }
func (f *fakeRepos) Memberships() port.MembershipRepository { return fakeMembershipRepo{store: f} }

type fakeUserRepo struct{ store *fakeRepos }

func (r fakeUserRepo) Create(ctx context.Context, user *domain.User) error {
	return r.store.Create(ctx, user)
}
func (r fakeUserRepo) GetByID(ctx context.Context, id domain.UserID) (*domain.User, error) {
	return r.store.GetByID(ctx, id)
}
func (r fakeUserRepo) List(ctx context.Context) ([]*domain.User, error) {
	return r.store.List(ctx)
}
func (r fakeUserRepo) Update(ctx context.Context, user *domain.User) error {
	return r.store.Update(ctx, user)
}

type fakeTeamRepo struct{ store *fakeRepos }

func (r fakeTeamRepo) Create(ctx context.Context, team *domain.Team) error {
	return r.store.CreateTeam(ctx, team)
}
func (r fakeTeamRepo) GetByID(ctx context.Context, id domain.TeamID) (*domain.Team, error) {
	return r.store.GetTeamByID(ctx, id)
}
func (r fakeTeamRepo) List(ctx context.Context) ([]*domain.Team, error) {
	return r.store.ListTeams(ctx)
}
func (r fakeTeamRepo) Update(ctx context.Context, team *domain.Team) error {
	return r.store.UpdateTeam(ctx, team)
}

type fakeMembershipRepo struct{ store *fakeRepos }

func (r fakeMembershipRepo) Create(ctx context.Context, membership *domain.TeamMembership) error {
	return r.store.CreateMembership(ctx, membership)
}
func (r fakeMembershipRepo) GetByID(ctx context.Context, id domain.MembershipID) (*domain.TeamMembership, error) {
	return r.store.GetMembershipByID(ctx, id)
}
func (r fakeMembershipRepo) ListByTeam(ctx context.Context, teamID domain.TeamID) ([]*domain.TeamMembership, error) {
	return r.store.ListByTeam(ctx, teamID)
}
func (r fakeMembershipRepo) Update(ctx context.Context, membership *domain.TeamMembership) error {
	return r.store.UpdateMembership(ctx, membership)
}

func (f *fakeRepos) Create(_ context.Context, user *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.users {
		if existing.Email().String() == user.Email().String() {
			return port.ErrAlreadyExists
		}
	}
	f.users[user.ID().String()] = cloneUser(user)
	f.userOrder = append(f.userOrder, user.ID().String())
	return nil
}

func (f *fakeRepos) GetByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, ok := f.users[id.String()]
	if !ok {
		return nil, port.ErrNotFound
	}
	return cloneUser(user), nil
}

func (f *fakeRepos) List(_ context.Context) ([]*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	users := make([]*domain.User, 0, len(f.userOrder))
	for _, id := range f.userOrder {
		users = append(users, cloneUser(f.users[id]))
	}
	return users, nil
}

func (f *fakeRepos) Update(_ context.Context, user *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.users[user.ID().String()]
	if !ok {
		return port.ErrNotFound
	}
	if stored.Version() != user.Version() {
		return port.ErrVersionConflict
	}
	for id, existing := range f.users {
		if id != user.ID().String() && existing.Email().String() == user.Email().String() {
			return port.ErrAlreadyExists
		}
	}
	version, _ := domain.NewVersion(stored.Version().Int64() + 1)
	f.users[user.ID().String()] = domain.RestoreUser(
		user.ID(), user.DisplayName(), user.Email(), version, stored.CreatedAt(), time.Now().UTC(),
	)
	return nil
}

func (f *fakeRepos) CreateTeam(_ context.Context, team *domain.Team) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teams[team.ID().String()] = cloneTeam(team)
	f.teamOrder = append(f.teamOrder, team.ID().String())
	return nil
}

func (f *fakeRepos) GetTeamByID(_ context.Context, id domain.TeamID) (*domain.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	team, ok := f.teams[id.String()]
	if !ok {
		return nil, port.ErrNotFound
	}
	return cloneTeam(team), nil
}

func (f *fakeRepos) ListTeams(_ context.Context) ([]*domain.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	teams := make([]*domain.Team, 0, len(f.teamOrder))
	for _, id := range f.teamOrder {
		teams = append(teams, cloneTeam(f.teams[id]))
	}
	return teams, nil
}

func (f *fakeRepos) UpdateTeam(_ context.Context, team *domain.Team) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.teams[team.ID().String()]
	if !ok {
		return port.ErrNotFound
	}
	if stored.Version() != team.Version() {
		return port.ErrVersionConflict
	}
	version, _ := domain.NewVersion(stored.Version().Int64() + 1)
	f.teams[team.ID().String()] = domain.RestoreTeam(
		team.ID(), team.Name(), version, stored.CreatedAt(), time.Now().UTC(),
	)
	return nil
}

func (f *fakeRepos) CreateMembership(_ context.Context, membership *domain.TeamMembership) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.memberships {
		if existing.TeamID() == membership.TeamID() &&
			existing.UserID() == membership.UserID() &&
			existing.Status() != domain.StatusLeaved {
			return port.ErrAlreadyExists
		}
	}
	f.memberships[membership.ID().String()] = cloneMembership(membership)
	f.memberOrder = append(f.memberOrder, membership.ID().String())
	return nil
}

func (f *fakeRepos) GetMembershipByID(_ context.Context, id domain.MembershipID) (*domain.TeamMembership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	membership, ok := f.memberships[id.String()]
	if !ok {
		return nil, port.ErrNotFound
	}
	return cloneMembership(membership), nil
}

func (f *fakeRepos) ListByTeam(_ context.Context, teamID domain.TeamID) ([]*domain.TeamMembership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	memberships := make([]*domain.TeamMembership, 0)
	for _, id := range f.memberOrder {
		membership := f.memberships[id]
		if membership.TeamID() == teamID {
			memberships = append(memberships, cloneMembership(membership))
		}
	}
	return memberships, nil
}

func (f *fakeRepos) UpdateMembership(_ context.Context, membership *domain.TeamMembership) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.memberships[membership.ID().String()]
	if !ok {
		return port.ErrNotFound
	}
	if stored.Version() != membership.Version() {
		return port.ErrVersionConflict
	}
	version, _ := domain.NewVersion(stored.Version().Int64() + 1)
	f.memberships[membership.ID().String()] = domain.RestoreTeamMembership(
		membership.ID(), membership.TeamID(), membership.UserID(), membership.Role(),
		membership.Status(), version, stored.CreatedAt(), time.Now().UTC(),
	)
	return nil
}

func cloneUser(user *domain.User) *domain.User {
	return domain.RestoreUser(
		user.ID(), user.DisplayName(), user.Email(), user.Version(), user.CreatedAt(), user.UpdatedAt(),
	)
}

func cloneTeam(team *domain.Team) *domain.Team {
	return domain.RestoreTeam(team.ID(), team.Name(), team.Version(), team.CreatedAt(), team.UpdatedAt())
}

func cloneMembership(membership *domain.TeamMembership) *domain.TeamMembership {
	return domain.RestoreTeamMembership(
		membership.ID(), membership.TeamID(), membership.UserID(), membership.Role(),
		membership.Status(), membership.Version(), membership.CreatedAt(), membership.UpdatedAt(),
	)
}

// fakeGuard is a simplified in-memory hot path. It tracks balances and applies
// maintenance, route policy and balance checks; the real rate-limit algorithm
// is covered by the hotpath package tests.
type fakeGuard struct {
	mu       sync.Mutex
	config   *domain.RuntimeConfig
	accounts map[string]domain.AccountState
	stats    domain.HotPathStats
}

func newFakeGuard(balances map[string]int64) *fakeGuard {
	accounts := make(map[string]domain.AccountState, len(balances))
	for accountID, balance := range balances {
		accounts[accountID] = domain.AccountState{
			AccountID: accountID, Balance: balance, RateLimitRPM: 120,
		}
	}
	return &fakeGuard{
		config:   &domain.RuntimeConfig{DefaultRateLimitRPM: 120, DefaultRequestCost: 1},
		accounts: accounts,
	}
}

func (g *fakeGuard) CurrentConfig() *domain.RuntimeConfig {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.config.Clone()
}

func (g *fakeGuard) UpdateConfig(config *domain.RuntimeConfig) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.config = config.Clone()
}

func (g *fakeGuard) AcquireAndDebit(accountID, route string) (domain.HotPathDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Requests++
	if g.config.MaintenanceMode {
		g.stats.MaintenanceRejected++
		return domain.HotPathDecision{}, domain.ErrRouteDisabled
	}
	policy, hasPolicy := g.config.Routes[route]
	if hasPolicy && !policy.Enabled {
		g.stats.RouteDisabled++
		return domain.HotPathDecision{}, domain.ErrRouteDisabled
	}
	cost := g.config.DefaultRequestCost
	if hasPolicy {
		cost = policy.Cost
	}
	state := g.accounts[accountID]
	if state.Balance < cost {
		g.stats.InsufficientBalance++
		return domain.HotPathDecision{
			Allowed: false, Cost: cost, RemainingBalance: state.Balance,
		}, domain.ErrInsufficientBalance
	}
	state.Balance -= cost
	g.accounts[accountID] = state
	g.stats.Allowed++
	return domain.HotPathDecision{
		Allowed: true, Cost: cost, RemainingBalance: state.Balance,
	}, nil
}

func (g *fakeGuard) Authorize(accountID, route string) (domain.HotPathDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Requests++
	if g.config.MaintenanceMode {
		g.stats.MaintenanceRejected++
		return domain.HotPathDecision{}, domain.ErrRouteDisabled
	}
	policy, hasPolicy := g.config.Routes[route]
	if hasPolicy && !policy.Enabled {
		g.stats.RouteDisabled++
		return domain.HotPathDecision{}, domain.ErrRouteDisabled
	}
	g.stats.Allowed++
	return domain.HotPathDecision{
		Allowed: true, RemainingBalance: g.accounts[accountID].Balance,
	}, nil
}

func (g *fakeGuard) Debit(accountID string, amount int64) (domain.HotPathDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.accounts[accountID]
	if state.Balance < amount {
		g.stats.InsufficientBalance++
		return domain.HotPathDecision{Allowed: false, Cost: amount, RemainingBalance: state.Balance}, domain.ErrInsufficientBalance
	}
	state.Balance -= amount
	g.accounts[accountID] = state
	return domain.HotPathDecision{Allowed: true, Cost: amount, RemainingBalance: state.Balance}, nil
}

func (g *fakeGuard) Refund(accountID string, amount int64) {
	if amount <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.accounts[accountID]
	state.Balance += amount
	g.accounts[accountID] = state
}

func (g *fakeGuard) Credit(accountID string, amount int64) {
	if amount <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.accounts[accountID]
	state.AccountID = accountID
	if state.RateLimitRPM == 0 {
		state.RateLimitRPM = 120
	}
	state.Balance += amount
	g.accounts[accountID] = state
}

func (g *fakeGuard) GetAccountState(accountID string) (domain.AccountState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.accounts[accountID]
	return state, ok
}

func (g *fakeGuard) Hydrate(quotas []domain.AccountQuota) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, quota := range quotas {
		g.accounts[quota.AccountID] = domain.AccountState{
			AccountID: quota.AccountID, Balance: quota.Balance, RateLimitRPM: quota.RateLimitRPM,
		}
	}
}

func (g *fakeGuard) Stats() domain.HotPathStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// fakeQueue records enqueued events so tests can assert exactly what the
// asynchronous writer would have persisted.
type fakeQueue struct {
	mu      sync.Mutex
	billing []domain.BillingEvent
	quotas  []domain.AccountQuota
	logs    []domain.RequestLog
	err     error
	stats   domain.BatcherStats
}

func (q *fakeQueue) EnqueueBilling(event domain.BillingEvent) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.billing = append(q.billing, event)
	q.stats.Enqueued++
	return nil
}

func (q *fakeQueue) EnqueueQuotaSync(quota domain.AccountQuota) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.quotas = append(q.quotas, quota)
	q.stats.Enqueued++
	return nil
}

func (q *fakeQueue) EnqueueRequestLog(logRow domain.RequestLog) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.logs = append(q.logs, logRow)
	q.stats.Enqueued++
	return nil
}

func (q *fakeQueue) Stats() domain.BatcherStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}

func (q *fakeQueue) Drain(context.Context) error { return nil }

type fakeQuotas struct {
	mu     sync.Mutex
	quotas map[string]domain.AccountQuota
}

func (q *fakeQuotas) ListAll(context.Context) ([]domain.AccountQuota, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	quotas := make([]domain.AccountQuota, 0, len(q.quotas))
	for _, quota := range q.quotas {
		quotas = append(quotas, quota)
	}
	return quotas, nil
}

func (q *fakeQuotas) GetByAccountID(_ context.Context, accountID string) (domain.AccountQuota, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	quota, ok := q.quotas[accountID]
	if !ok {
		return domain.AccountQuota{}, port.ErrNotFound
	}
	return quota, nil
}

type fakeAudit struct {
	events []domain.BillingEvent
	logs   []domain.RequestLog
	err    error
}

func (a *fakeAudit) ListBillingEvents(context.Context, string, int) ([]domain.BillingEvent, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.events, nil
}

func (a *fakeAudit) ListRequestLogs(context.Context, string, int) ([]domain.RequestLog, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.logs, nil
}

type fakeHealth struct {
	health domain.DBHealth
	err    error
}

func (h fakeHealth) Health(context.Context) (domain.DBHealth, error) {
	return h.health, h.err
}
