package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

type testRepos struct {
	mu sync.Mutex

	users       map[string]*domain.User
	userOrder   []string
	teams       map[string]*domain.Team
	teamOrder   []string
	memberships map[string]*domain.TeamMembership
	memberOrder []string
}

func newTestRepos() *testRepos {
	return &testRepos{
		users:       make(map[string]*domain.User),
		teams:       make(map[string]*domain.Team),
		memberships: make(map[string]*domain.TeamMembership),
	}
}

func (f *testRepos) WithWriteTx(ctx context.Context, fn func(context.Context, port.WriteRepos) error) error {
	return fn(ctx, f)
}

func (f *testRepos) Users() port.UserRepository             { return testUserRepo{store: f} }
func (f *testRepos) Teams() port.TeamRepository             { return testTeamRepo{store: f} }
func (f *testRepos) Memberships() port.MembershipRepository { return testMembershipRepo{store: f} }

type testUserRepo struct{ store *testRepos }

func (r testUserRepo) Create(_ context.Context, user *domain.User) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, existing := range r.store.users {
		if existing.Email().String() == user.Email().String() {
			return port.ErrAlreadyExists
		}
	}
	r.store.users[user.ID().String()] = cloneTestUser(user)
	r.store.userOrder = append(r.store.userOrder, user.ID().String())
	return nil
}
func (r testUserRepo) GetByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	user, ok := r.store.users[id.String()]
	if !ok {
		return nil, port.ErrNotFound
	}
	return cloneTestUser(user), nil
}
func (r testUserRepo) List(context.Context) ([]*domain.User, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	users := make([]*domain.User, 0, len(r.store.userOrder))
	for _, id := range r.store.userOrder {
		users = append(users, cloneTestUser(r.store.users[id]))
	}
	return users, nil
}
func (r testUserRepo) Update(_ context.Context, user *domain.User) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	stored, ok := r.store.users[user.ID().String()]
	if !ok {
		return port.ErrNotFound
	}
	if stored.Version() != user.Version() {
		return port.ErrVersionConflict
	}
	for id, existing := range r.store.users {
		if id != user.ID().String() && existing.Email().String() == user.Email().String() {
			return port.ErrAlreadyExists
		}
	}
	version, _ := domain.NewVersion(stored.Version().Int64() + 1)
	r.store.users[user.ID().String()] = domain.RestoreUser(
		user.ID(), user.DisplayName(), user.Email(), version, stored.CreatedAt(), time.Now().UTC(),
	)
	return nil
}

type testTeamRepo struct{ store *testRepos }

func (r testTeamRepo) Create(_ context.Context, team *domain.Team) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.store.teams[team.ID().String()] = cloneTestTeam(team)
	r.store.teamOrder = append(r.store.teamOrder, team.ID().String())
	return nil
}
func (r testTeamRepo) GetByID(_ context.Context, id domain.TeamID) (*domain.Team, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	team, ok := r.store.teams[id.String()]
	if !ok {
		return nil, port.ErrNotFound
	}
	return cloneTestTeam(team), nil
}
func (r testTeamRepo) List(context.Context) ([]*domain.Team, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	teams := make([]*domain.Team, 0, len(r.store.teamOrder))
	for _, id := range r.store.teamOrder {
		teams = append(teams, cloneTestTeam(r.store.teams[id]))
	}
	return teams, nil
}
func (r testTeamRepo) Update(_ context.Context, team *domain.Team) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	stored, ok := r.store.teams[team.ID().String()]
	if !ok {
		return port.ErrNotFound
	}
	if stored.Version() != team.Version() {
		return port.ErrVersionConflict
	}
	version, _ := domain.NewVersion(stored.Version().Int64() + 1)
	r.store.teams[team.ID().String()] = domain.RestoreTeam(
		team.ID(), team.Name(), version, stored.CreatedAt(), time.Now().UTC(),
	)
	return nil
}

type testMembershipRepo struct{ store *testRepos }

func (r testMembershipRepo) Create(_ context.Context, membership *domain.TeamMembership) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	for _, existing := range r.store.memberships {
		if existing.TeamID() == membership.TeamID() &&
			existing.UserID() == membership.UserID() &&
			existing.Status() != domain.StatusLeaved {
			return port.ErrAlreadyExists
		}
	}
	r.store.memberships[membership.ID().String()] = cloneTestMembership(membership)
	r.store.memberOrder = append(r.store.memberOrder, membership.ID().String())
	return nil
}
func (r testMembershipRepo) GetByID(_ context.Context, id domain.MembershipID) (*domain.TeamMembership, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	membership, ok := r.store.memberships[id.String()]
	if !ok {
		return nil, port.ErrNotFound
	}
	return cloneTestMembership(membership), nil
}
func (r testMembershipRepo) ListByTeam(_ context.Context, teamID domain.TeamID) ([]*domain.TeamMembership, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	memberships := make([]*domain.TeamMembership, 0)
	for _, id := range r.store.memberOrder {
		membership := r.store.memberships[id]
		if membership.TeamID() == teamID {
			memberships = append(memberships, cloneTestMembership(membership))
		}
	}
	return memberships, nil
}
func (r testMembershipRepo) Update(_ context.Context, membership *domain.TeamMembership) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	stored, ok := r.store.memberships[membership.ID().String()]
	if !ok {
		return port.ErrNotFound
	}
	if stored.Version() != membership.Version() {
		return port.ErrVersionConflict
	}
	version, _ := domain.NewVersion(stored.Version().Int64() + 1)
	r.store.memberships[membership.ID().String()] = domain.RestoreTeamMembership(
		membership.ID(), membership.TeamID(), membership.UserID(), membership.Role(),
		membership.Status(), version, stored.CreatedAt(), time.Now().UTC(),
	)
	return nil
}

func cloneTestUser(user *domain.User) *domain.User {
	return domain.RestoreUser(
		user.ID(), user.DisplayName(), user.Email(), user.Version(), user.CreatedAt(), user.UpdatedAt(),
	)
}
func cloneTestTeam(team *domain.Team) *domain.Team {
	return domain.RestoreTeam(team.ID(), team.Name(), team.Version(), team.CreatedAt(), team.UpdatedAt())
}
func cloneTestMembership(membership *domain.TeamMembership) *domain.TeamMembership {
	return domain.RestoreTeamMembership(
		membership.ID(), membership.TeamID(), membership.UserID(), membership.Role(),
		membership.Status(), membership.Version(), membership.CreatedAt(), membership.UpdatedAt(),
	)
}

type testWindow struct{ count int64 }

type testGuard struct {
	mu       sync.Mutex
	config   *domain.RuntimeConfig
	accounts map[string]domain.AccountState
	windows  map[string]*testWindow
	stats    domain.HotPathStats
}

func newTestGuard(balances map[string]int64) *testGuard {
	accounts := make(map[string]domain.AccountState, len(balances))
	for accountID, balance := range balances {
		accounts[accountID] = domain.AccountState{AccountID: accountID, Balance: balance, RateLimitRPM: 120}
	}
	return &testGuard{
		config:   &domain.RuntimeConfig{DefaultRateLimitRPM: 120, DefaultRequestCost: 1},
		accounts: accounts,
		windows:  make(map[string]*testWindow),
	}
}

func (g *testGuard) CurrentConfig() *domain.RuntimeConfig {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.config.Clone()
}
func (g *testGuard) UpdateConfig(config *domain.RuntimeConfig) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.config = config.Clone()
}
func (g *testGuard) AcquireAndDebit(accountID, route string) (domain.HotPathDecision, error) {
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
	rpm := g.config.DefaultRateLimitRPM
	if hasPolicy && policy.RateLimitRPM > 0 {
		rpm = policy.RateLimitRPM
	}
	window := g.windows[accountID]
	if window == nil {
		window = &testWindow{}
		g.windows[accountID] = window
	}
	if rpm > 0 && window.count >= int64(rpm) {
		g.stats.RateLimited++
		return domain.HotPathDecision{
			Allowed: false, RemainingBalance: g.account(accountID).Balance, RemainingTokens: 0,
		}, domain.ErrRateLimited
	}
	window.count++
	cost := g.config.DefaultRequestCost
	if hasPolicy {
		cost = policy.Cost
	}
	state := g.account(accountID)
	if state.Balance < cost {
		g.stats.InsufficientBalance++
		return domain.HotPathDecision{
			Allowed: false, Cost: cost, RemainingBalance: state.Balance,
			RemainingTokens: int64(rpm) - window.count,
		}, domain.ErrInsufficientBalance
	}
	state.Balance -= cost
	g.accounts[accountID] = state
	g.stats.Allowed++
	return domain.HotPathDecision{
		Allowed: true, Cost: cost, RemainingBalance: state.Balance,
		RemainingTokens: int64(rpm) - window.count,
	}, nil
}
func (g *testGuard) Authorize(accountID, route string) (domain.HotPathDecision, error) {
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
	rpm := g.config.DefaultRateLimitRPM
	if hasPolicy && policy.RateLimitRPM > 0 {
		rpm = policy.RateLimitRPM
	}
	window := g.windows[accountID]
	if window == nil {
		window = &testWindow{}
		g.windows[accountID] = window
	}
	if rpm > 0 && window.count >= int64(rpm) {
		g.stats.RateLimited++
		return domain.HotPathDecision{
			Allowed: false, RemainingBalance: g.account(accountID).Balance, RemainingTokens: 0,
		}, domain.ErrRateLimited
	}
	window.count++
	g.stats.Allowed++
	return domain.HotPathDecision{
		Allowed: true, RemainingBalance: g.account(accountID).Balance,
		RemainingTokens: int64(rpm) - window.count,
	}, nil
}
func (g *testGuard) Debit(accountID string, amount int64) (domain.HotPathDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.account(accountID)
	if state.Balance < amount {
		g.stats.InsufficientBalance++
		return domain.HotPathDecision{Allowed: false, Cost: amount, RemainingBalance: state.Balance}, domain.ErrInsufficientBalance
	}
	state.Balance -= amount
	g.accounts[accountID] = state
	return domain.HotPathDecision{Allowed: true, Cost: amount, RemainingBalance: state.Balance}, nil
}
func (g *testGuard) Refund(accountID string, amount int64) {
	if amount <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.account(accountID)
	state.Balance += amount
	g.accounts[accountID] = state
}
func (g *testGuard) Credit(accountID string, amount int64) {
	if amount <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.account(accountID)
	state.AccountID = accountID
	if state.RateLimitRPM == 0 {
		state.RateLimitRPM = 120
	}
	state.Balance += amount
	g.accounts[accountID] = state
}
func (g *testGuard) GetAccountState(accountID string) (domain.AccountState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.accounts[accountID]
	return state, ok
}
func (g *testGuard) Hydrate(quotas []domain.AccountQuota) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, quota := range quotas {
		g.accounts[quota.AccountID] = domain.AccountState{
			AccountID: quota.AccountID, Balance: quota.Balance, RateLimitRPM: quota.RateLimitRPM,
		}
	}
}
func (g *testGuard) Stats() domain.HotPathStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

func (g *testGuard) account(accountID string) domain.AccountState {
	state, ok := g.accounts[accountID]
	if !ok {
		state = domain.AccountState{AccountID: accountID, RateLimitRPM: g.config.DefaultRateLimitRPM}
		g.accounts[accountID] = state
	}
	return state
}

type testQueue struct {
	mu      sync.Mutex
	billing []domain.BillingEvent
	quotas  []domain.AccountQuota
	logs    []domain.RequestLog
	stats   domain.BatcherStats
}

func (q *testQueue) EnqueueBilling(event domain.BillingEvent) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.billing = append(q.billing, event)
	q.stats.Enqueued++
	return nil
}
func (q *testQueue) EnqueueQuotaSync(quota domain.AccountQuota) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.quotas = append(q.quotas, quota)
	q.stats.Enqueued++
	return nil
}
func (q *testQueue) EnqueueRequestLog(logRow domain.RequestLog) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.logs = append(q.logs, logRow)
	q.stats.Enqueued++
	return nil
}
func (q *testQueue) Stats() domain.BatcherStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}
func (q *testQueue) Drain(context.Context) error { return nil }

func (q *testQueue) snapshot() ([]domain.BillingEvent, []domain.AccountQuota, []domain.RequestLog) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]domain.BillingEvent(nil), q.billing...),
		append([]domain.AccountQuota(nil), q.quotas...),
		append([]domain.RequestLog(nil), q.logs...)
}

type testQuotas struct {
	mu     sync.Mutex
	quotas map[string]domain.AccountQuota
}

func (q *testQuotas) ListAll(context.Context) ([]domain.AccountQuota, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	quotas := make([]domain.AccountQuota, 0, len(q.quotas))
	for _, quota := range q.quotas {
		quotas = append(quotas, quota)
	}
	return quotas, nil
}
func (q *testQuotas) GetByAccountID(_ context.Context, accountID string) (domain.AccountQuota, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	quota, ok := q.quotas[accountID]
	if !ok {
		return domain.AccountQuota{}, port.ErrNotFound
	}
	return quota, nil
}

type testAudit struct {
	events []domain.BillingEvent
	logs   []domain.RequestLog
}

func (a *testAudit) ListBillingEvents(context.Context, string, int) ([]domain.BillingEvent, error) {
	return a.events, nil
}
func (a *testAudit) ListRequestLogs(context.Context, string, int) ([]domain.RequestLog, error) {
	return a.logs, nil
}

type testHealth struct{ health domain.DBHealth }

func (h testHealth) Health(context.Context) (domain.DBHealth, error) { return h.health, nil }

type testEnvironment struct {
	handler *Handler
	repos   *testRepos
	guard   *testGuard
	queue   *testQueue
	quotas  *testQuotas
	audit   *testAudit
}

func newTestEnvironment(t *testing.T) *testEnvironment {
	t.Helper()
	repos := newTestRepos()
	guard := newTestGuard(map[string]int64{"default": 1000})
	queue := &testQueue{}
	quotas := &testQuotas{quotas: make(map[string]domain.AccountQuota)}
	audit := &testAudit{}

	users := usecase.NewUserUseCase(repos, repos.Users())
	teams := usecase.NewTeamUseCase(repos, repos.Teams())
	memberships := usecase.NewMembershipUseCase(repos, repos.Teams(), repos.Users(), repos.Memberships())
	billing := usecase.NewBillingUseCase(guard, quotas, audit, queue)
	runtime := usecase.NewRuntimeUseCase(guard, queue, testHealth{health: domain.DBHealth{
		OK: true, JournalMode: "wal", WALAutocheckpoint: 0, ReadQueryOnly: true, ReadConnections: 5,
	}})

	handler, err := NewHandler(Dependencies{
		Users: users, Teams: teams, Memberships: memberships,
		Billing: billing, Runtime: runtime, HotPath: guard, Queue: queue,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	typed, ok := handler.(*Handler)
	if !ok {
		t.Fatalf("NewHandler returned %T", handler)
	}
	return &testEnvironment{handler: typed, repos: repos, guard: guard, queue: queue, quotas: quotas, audit: audit}
}

func (e *testEnvironment) seedUser(t *testing.T, name, email string) *domain.User {
	t.Helper()
	user, err := usecase.NewUserUseCase(e.repos, e.repos.Users()).
		CreateUser(context.Background(), usecase.CreateUserInput{DisplayName: name, Email: email})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return user
}

func (e *testEnvironment) seedTeam(t *testing.T, name string) *domain.Team {
	t.Helper()
	team, err := usecase.NewTeamUseCase(e.repos, e.repos.Teams()).
		CreateTeam(context.Background(), usecase.CreateTeamInput{Name: name})
	if err != nil {
		t.Fatalf("seed team: %v", err)
	}
	return team
}
