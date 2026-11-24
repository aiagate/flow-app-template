CREATE TABLE users (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE,
    version INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_users_display_name ON users(display_name);

CREATE TABLE teams (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_teams_name ON teams(name);

CREATE TABLE team_memberships (
    id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    role TEXT NOT NULL,
    status TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_team_memberships_team_id ON team_memberships(team_id);
CREATE INDEX idx_team_memberships_user_id ON team_memberships(user_id);

CREATE UNIQUE INDEX uq_team_memberships_current_period
    ON team_memberships(team_id, user_id)
    WHERE status IN ('PENDING', 'ACTIVE');

CREATE TABLE account_quotas (
    account_id TEXT PRIMARY KEY,
    balance INTEGER NOT NULL,
    rate_limit_rpm INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE billing_events (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL,
    amount INTEGER NOT NULL,
    reason TEXT NOT NULL,
    request_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);

CREATE INDEX idx_billing_events_account_occurred
    ON billing_events(account_id, occurred_at DESC);

CREATE TABLE request_logs (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    status_code INTEGER NOT NULL,
    latency_us INTEGER NOT NULL,
    occurred_at TEXT NOT NULL
);

CREATE INDEX idx_request_logs_occurred ON request_logs(occurred_at DESC);
