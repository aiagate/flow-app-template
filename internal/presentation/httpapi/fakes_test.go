package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/contract/port"
	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

const testAdminToken = "admin-token"

func testAccountToken(accountID string) string {
	return accountID + "-token"
}

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
	mu        sync.Mutex
	config    *domain.RuntimeConfig
	accounts  map[string]domain.AccountState
	windows   map[string]*testWindow
	stats     domain.HotPathStats
	creditErr error
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
		return domain.HotPathDecision{Reason: domain.ErrRouteDisabled.Error()}, domain.ErrRouteDisabled
	}
	policy, hasPolicy := g.config.Routes[route]
	if hasPolicy && !policy.Enabled {
		g.stats.RouteDisabled++
		return domain.HotPathDecision{Reason: domain.ErrRouteDisabled.Error()}, domain.ErrRouteDisabled
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
			Allowed: false, Reason: domain.ErrRateLimited.Error(),
			RemainingBalance: g.account(accountID).Balance, RemainingTokens: 0,
			RateLimitRPM: rpm,
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
			RemainingTokens: int64(rpm) - window.count, RateLimitRPM: rpm,
		}, domain.ErrInsufficientBalance
	}
	state.Balance -= cost
	state.Seq++
	state.UpdatedAt = time.Now().UTC()
	g.accounts[accountID] = state
	g.stats.Allowed++
	return domain.HotPathDecision{
		Allowed:             true,
		Cost:                cost,
		RemainingBalance:    state.Balance,
		RemainingTokens:     int64(rpm) - window.count,
		RateLimitRPM:        rpm,
		AccountRateLimitRPM: state.RateLimitRPM,
		Seq:                 state.Seq,
	}, nil
}
func (g *testGuard) Authorize(accountID, route string) (domain.HotPathDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Requests++
	if g.config.MaintenanceMode {
		g.stats.MaintenanceRejected++
		return domain.HotPathDecision{Reason: domain.ErrRouteDisabled.Error()}, domain.ErrRouteDisabled
	}
	policy, hasPolicy := g.config.Routes[route]
	if hasPolicy && !policy.Enabled {
		g.stats.RouteDisabled++
		return domain.HotPathDecision{Reason: domain.ErrRouteDisabled.Error()}, domain.ErrRouteDisabled
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
			Allowed: false, Reason: domain.ErrRateLimited.Error(),
			RemainingBalance: g.account(accountID).Balance, RemainingTokens: 0,
			RateLimitRPM: rpm,
		}, domain.ErrRateLimited
	}
	window.count++
	g.stats.Allowed++
	return domain.HotPathDecision{
		Allowed: true, RemainingBalance: g.account(accountID).Balance,
		RemainingTokens: int64(rpm) - window.count, RateLimitRPM: rpm,
	}, nil
}
func (g *testGuard) Debit(accountID string, amount int64, onCommit func(domain.AccountState)) (domain.HotPathDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.accounts[accountID]
	if !ok {
		return domain.HotPathDecision{}, port.ErrNotFound
	}
	if state.Balance < amount {
		g.stats.InsufficientBalance++
		return domain.HotPathDecision{Allowed: false, Cost: amount, RemainingBalance: state.Balance}, domain.ErrInsufficientBalance
	}
	state.Balance -= amount
	state.Seq++
	state.UpdatedAt = time.Now().UTC()
	g.accounts[accountID] = state
	if onCommit != nil {
		onCommit(state)
	}
	return domain.HotPathDecision{
		Allowed: true, Cost: amount, RemainingBalance: state.Balance,
		AccountRateLimitRPM: state.RateLimitRPM, Seq: state.Seq,
	}, nil
}
func (g *testGuard) Refund(accountID string, amount int64, onCommit func(domain.AccountState)) {
	if amount <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.accounts[accountID]
	if !ok {
		return
	}
	state.Balance += amount
	state.Seq++
	state.UpdatedAt = time.Now().UTC()
	g.accounts[accountID] = state
	if onCommit != nil {
		onCommit(state)
	}
}
func (g *testGuard) Credit(accountID string, amount int64, onCommit func(domain.AccountState)) (domain.AccountState, error) {
	if amount <= 0 {
		return domain.AccountState{}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.creditErr != nil {
		return domain.AccountState{}, g.creditErr
	}
	state, ok := g.accounts[accountID]
	if !ok {
		state = domain.AccountState{AccountID: accountID, RateLimitRPM: g.config.DefaultRateLimitRPM}
	}
	state.AccountID = accountID
	if state.RateLimitRPM == 0 {
		state.RateLimitRPM = 120
	}
	state.Balance += amount
	state.Seq++
	state.UpdatedAt = time.Now().UTC()
	g.accounts[accountID] = state
	if onCommit != nil {
		onCommit(state)
	}
	return state, nil
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
			Seq: quota.Seq, UpdatedAt: quota.UpdatedAt,
		}
	}
}
func (g *testGuard) Stats() domain.HotPathStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// rateCount reports the rate-limit tokens consumed for an account so tests can
// prove a rejected request never touched the limiter.
func (g *testGuard) rateCount(accountID string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if window := g.windows[accountID]; window != nil {
		return window.count
	}
	return 0
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
	mu        sync.Mutex
	billing   []domain.BillingEvent
	quotas    []domain.AccountQuota
	logs      []domain.RequestLog
	stats     domain.BatcherStats
	err       error
	commitErr error

	reserved  int
	active    int
	committed int
	released  int
}

func (q *testQueue) EnqueueBilling(event domain.BillingEvent) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.billing = append(q.billing, event)
	q.stats.Enqueued++
	return nil
}

// Reserve models a pre-allocated write slot. A queue error rejects the request
// before any balance mutation.
func (q *testQueue) Reserve() (port.WriteReservation, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return nil, q.err
	}
	q.reserved++
	q.active++
	return &testReservation{queue: q}, nil
}

func (q *testQueue) EnqueueQuotaSync(quota domain.AccountQuota) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.quotas = append(q.quotas, quota)
	q.stats.Enqueued++
	return nil
}
func (q *testQueue) EnqueueQuotaSyncBlocking(_ context.Context, quota domain.AccountQuota) error {
	return q.EnqueueQuotaSync(quota)
}
func (q *testQueue) EnqueueRequestLog(logRow domain.RequestLog) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
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

func (q *testQueue) reservationCounts() (reserved, active, committed, released int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.reserved, q.active, q.committed, q.released
}

// testReservation is one pre-reserved write slot. Exactly one commit or
// Release settles it; a second settle is a no-op.
type testReservation struct {
	queue   *testQueue
	settled bool
}

func (r *testReservation) CommitBillingWithQuota(event domain.BillingEvent, quota domain.AccountQuota) error {
	q := r.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	if r.settled {
		return nil
	}
	r.settled = true
	q.active--
	if q.commitErr != nil {
		q.released++
		return q.commitErr
	}
	q.billing = append(q.billing, event)
	q.quotas = append(q.quotas, quota)
	q.committed++
	q.stats.Enqueued++
	return nil
}

func (r *testReservation) CommitQuotaSync(quota domain.AccountQuota) error {
	q := r.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	if r.settled {
		return nil
	}
	r.settled = true
	q.active--
	if q.commitErr != nil {
		q.released++
		return q.commitErr
	}
	q.quotas = append(q.quotas, quota)
	q.committed++
	q.stats.Enqueued++
	return nil
}

func (r *testReservation) Release() {
	q := r.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	if r.settled {
		return
	}
	r.settled = true
	q.active--
	q.released++
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
	events         []domain.BillingEvent
	logs           []domain.RequestLog
	billingAccount string
	logsAccount    string
}

func (a *testAudit) ListBillingEvents(_ context.Context, accountID string, _ int) ([]domain.BillingEvent, error) {
	a.billingAccount = accountID
	return a.events, nil
}
func (a *testAudit) ListRequestLogs(_ context.Context, accountID string, _ int) ([]domain.RequestLog, error) {
	a.logsAccount = accountID
	return a.logs, nil
}

type testHealth struct {
	health domain.DBHealth
}

func (h *testHealth) Health(context.Context) (domain.DBHealth, error) { return h.health, nil }

// testAuthenticator mirrors the static authenticator: one admin token and one
// token per configured account.
type testAuthenticator struct {
	principals map[string]domain.Principal
}

func newTestAuthenticator() *testAuthenticator {
	principals := map[string]domain.Principal{
		testAdminToken: {Subject: "admin", IsAdmin: true},
	}
	for _, accountID := range []string{"default", "acct-a", "acct-b"} {
		principals[testAccountToken(accountID)] = domain.Principal{
			Subject:   "account:" + accountID,
			AccountID: accountID,
		}
	}
	return &testAuthenticator{principals: principals}
}

func (a *testAuthenticator) Authenticate(token string) (domain.Principal, bool) {
	principal, ok := a.principals[token]
	return principal, ok
}

type testRuntimeConfig struct {
	mu      sync.Mutex
	saved   *domain.RuntimeConfig
	saveErr error
}

func (c *testRuntimeConfig) Load(context.Context) (*domain.RuntimeConfig, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saved.Clone(), c.saved != nil, nil
}

func (c *testRuntimeConfig) Save(_ context.Context, cfg *domain.RuntimeConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saveErr != nil {
		return c.saveErr
	}
	c.saved = cfg.Clone()
	return nil
}

type testEnvironment struct {
	handler *Handler
	repos   *testRepos
	guard   *testGuard
	queue   *testQueue
	quotas  *testQuotas
	audit   *testAudit
	health  *testHealth
	config  *testRuntimeConfig
	auth    *testAuthenticator
}

func newTestEnvironment(t *testing.T) *testEnvironment {
	t.Helper()
	repos := newTestRepos()
	guard := newTestGuard(map[string]int64{"default": 1000, "acct-a": 1000, "acct-b": 1000})
	queue := &testQueue{}
	quotas := &testQuotas{quotas: make(map[string]domain.AccountQuota)}
	audit := &testAudit{}
	health := &testHealth{health: domain.DBHealth{
		OK: true, JournalMode: "wal", WALAutocheckpoint: 0, WALWarnBytes: 64 << 20,
		WALHealthy: true, ReadQueryOnly: true, ReadConnections: 5,
	}}
	configRepo := &testRuntimeConfig{}
	auth := newTestAuthenticator()

	users := usecase.NewUserUseCase(repos, repos.Users())
	teams := usecase.NewTeamUseCase(repos, repos.Teams())
	memberships := usecase.NewMembershipUseCase(repos, repos.Teams(), repos.Users(), repos.Memberships())
	billing := usecase.NewBillingUseCase(guard, quotas, audit, queue)
	runtime := usecase.NewRuntimeUseCase(guard, queue, health, configRepo)

	handler, err := NewHandler(Dependencies{
		Users: users, Teams: teams, Memberships: memberships,
		Billing: billing, Runtime: runtime, HotPath: guard, Queue: queue,
		Auth: auth, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	typed, ok := handler.(*Handler)
	if !ok {
		t.Fatalf("NewHandler returned %T", handler)
	}
	return &testEnvironment{
		handler: typed, repos: repos, guard: guard, queue: queue, quotas: quotas,
		audit: audit, health: health, config: configRepo, auth: auth,
	}
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
