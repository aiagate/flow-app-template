package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
)

var configKeys = []string{
	"HTTP_ADDR",
	"DB_PATH",
	"DB_READ_CONNS",
	"DEFAULT_INITIAL_BALANCE",
	"DEFAULT_RATE_LIMIT_RPM",
	"DEFAULT_REQUEST_COST",
	"SHUTDOWN_TIMEOUT",
	"ADMIN_API_KEY",
	"API_KEYS",
	"DRAIN_TIMEOUT",
	"WAL_WARN_BYTES",
	"MAX_ACCOUNTS",
}

// isolateConfig moves to an empty directory and removes every configuration
// variable, restoring both the directory and the environment afterwards.
func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	for _, key := range configKeys {
		key := key
		old, existed := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	return dir
}

func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadDefaults(t *testing.T) {
	isolateConfig(t)
	t.Setenv("ADMIN_API_KEY", "admin-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DBPath != "./data/flow.db" {
		t.Errorf("DBPath = %q, want ./data/flow.db", cfg.DBPath)
	}
	if cfg.DBReadConns != 5 {
		t.Errorf("DBReadConns = %d, want 5", cfg.DBReadConns)
	}
	if cfg.DefaultInitialBalance != 1000 {
		t.Errorf("DefaultInitialBalance = %d, want 1000", cfg.DefaultInitialBalance)
	}
	if cfg.DefaultRateLimitRPM != 120 {
		t.Errorf("DefaultRateLimitRPM = %d, want 120", cfg.DefaultRateLimitRPM)
	}
	if cfg.DefaultRequestCost != 1 {
		t.Errorf("DefaultRequestCost = %d, want 1", cfg.DefaultRequestCost)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.AdminAPIKey != "admin-secret" {
		t.Errorf("AdminAPIKey = %q, want admin-secret", cfg.AdminAPIKey)
	}
	if len(cfg.AccountAPIKeys) != 0 {
		t.Errorf("AccountAPIKeys = %v, want empty", cfg.AccountAPIKeys)
	}
	if cfg.DrainTimeout != 10*time.Second {
		t.Errorf("DrainTimeout = %s, want 10s", cfg.DrainTimeout)
	}
	if cfg.WALWarnBytes != 67108864 {
		t.Errorf("WALWarnBytes = %d, want 67108864", cfg.WALWarnBytes)
	}
	if cfg.MaxAccounts != 4096 {
		t.Errorf("MaxAccounts = %d, want 4096", cfg.MaxAccounts)
	}
}

func TestLoadFromEnvFiles(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, dir, ".env.local", `
# local overrides
export HTTP_ADDR=":9000"
DB_PATH='./data/local.db'
`)
	writeFile(t, dir, ".env", `
HTTP_ADDR=:7000
DB_PATH=./data/env.db
DB_READ_CONNS=6
DEFAULT_INITIAL_BALANCE="42" # quoted with comment
DEFAULT_RATE_LIMIT_RPM=30
DEFAULT_REQUEST_COST=3
SHUTDOWN_TIMEOUT=3s # trailing comment
DRAIN_TIMEOUT=5s
WAL_WARN_BYTES=1048576
MAX_ACCOUNTS=32
ADMIN_API_KEY=file-admin-key
API_KEYS=acct_one:file-secret, acct_two:other-secret
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.HTTPAddr != ":9000" {
		t.Errorf("HTTPAddr = %q, want .env.local value :9000", cfg.HTTPAddr)
	}
	if cfg.DBPath != "./data/local.db" {
		t.Errorf("DBPath = %q, want .env.local value", cfg.DBPath)
	}
	if cfg.DBReadConns != 6 || cfg.DefaultInitialBalance != 42 ||
		cfg.DefaultRateLimitRPM != 30 || cfg.DefaultRequestCost != 3 {
		t.Errorf("numeric values not loaded: %+v", cfg)
	}
	if cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 3s", cfg.ShutdownTimeout)
	}
	if cfg.AdminAPIKey != "file-admin-key" {
		t.Errorf("AdminAPIKey = %q, want file-admin-key", cfg.AdminAPIKey)
	}
	wantKeys := map[string]string{"acct_one": "file-secret", "acct_two": "other-secret"}
	if !reflect.DeepEqual(cfg.AccountAPIKeys, wantKeys) {
		t.Errorf("AccountAPIKeys = %v, want %v", cfg.AccountAPIKeys, wantKeys)
	}
	if cfg.DrainTimeout != 5*time.Second {
		t.Errorf("DrainTimeout = %s, want 5s", cfg.DrainTimeout)
	}
	if cfg.WALWarnBytes != 1048576 {
		t.Errorf("WALWarnBytes = %d, want 1048576", cfg.WALWarnBytes)
	}
	if cfg.MaxAccounts != 32 {
		t.Errorf("MaxAccounts = %d, want 32", cfg.MaxAccounts)
	}
}

func TestProcessEnvironmentWinsOverEnvFiles(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, dir, ".env.local", "HTTP_ADDR=:9000\nDB_PATH=./local.db\n")
	writeFile(t, dir, ".env", "HTTP_ADDR=:7000\nDB_PATH=./env.db\n")
	t.Setenv("HTTP_ADDR", ":1234")
	t.Setenv("ADMIN_API_KEY", "admin-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.HTTPAddr != ":1234" {
		t.Fatalf("HTTPAddr = %q, want process value :1234", cfg.HTTPAddr)
	}
	if cfg.DBPath != "./local.db" {
		t.Fatalf("DBPath = %q, want .env.local value", cfg.DBPath)
	}
}

func TestFindProjectRootFromSubdirectory(t *testing.T) {
	dir := isolateConfig(t)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeFile(t, dir, ".env", "HTTP_ADDR=:4567\n")
	subdir := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("create subdir: %v", err)
	}
	t.Chdir(subdir)
	t.Setenv("ADMIN_API_KEY", "admin-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.HTTPAddr != ":4567" {
		t.Fatalf("HTTPAddr = %q, want :4567 from project-root .env", cfg.HTTPAddr)
	}
}

func TestDBReadConnsClamping(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"1", 4},
		{"3", 4},
		{"4", 4},
		{"5", 5},
		{"6", 6},
		{"7", 6},
		{"99", 6},
		{"-10", 4},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			isolateConfig(t)
			t.Setenv("ADMIN_API_KEY", "admin-secret")
			t.Setenv("DB_READ_CONNS", tc.raw)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.DBReadConns != tc.want {
				t.Fatalf("DBReadConns = %d, want %d", cfg.DBReadConns, tc.want)
			}
		})
	}
}

func TestInvalidConfiguration(t *testing.T) {
	cases := []struct {
		key   string
		value string
		want  string
	}{
		{"HTTP_ADDR", "not-an-address", "HTTP_ADDR"},
		{"DB_READ_CONNS", "abc", "DB_READ_CONNS"},
		{"DEFAULT_INITIAL_BALANCE", "not-a-number", "DEFAULT_INITIAL_BALANCE"},
		{"DEFAULT_INITIAL_BALANCE", "-1", "DEFAULT_INITIAL_BALANCE"},
		{"DEFAULT_RATE_LIMIT_RPM", "invalid", "DEFAULT_RATE_LIMIT_RPM"},
		{"DEFAULT_RATE_LIMIT_RPM", "0", "DEFAULT_RATE_LIMIT_RPM"},
		{"DEFAULT_REQUEST_COST", "invalid", "DEFAULT_REQUEST_COST"},
		{"DEFAULT_REQUEST_COST", "-1", "DEFAULT_REQUEST_COST"},
		{"SHUTDOWN_TIMEOUT", "not-a-duration", "SHUTDOWN_TIMEOUT"},
		{"SHUTDOWN_TIMEOUT", "0s", "SHUTDOWN_TIMEOUT"},
		{"DRAIN_TIMEOUT", "not-a-duration", "DRAIN_TIMEOUT"},
		{"DRAIN_TIMEOUT", "0s", "DRAIN_TIMEOUT"},
		{"DRAIN_TIMEOUT", "-1s", "DRAIN_TIMEOUT"},
		{"WAL_WARN_BYTES", "not-a-number", "WAL_WARN_BYTES"},
		{"WAL_WARN_BYTES", "0", "WAL_WARN_BYTES"},
		{"WAL_WARN_BYTES", "-1", "WAL_WARN_BYTES"},
		{"MAX_ACCOUNTS", "not-a-number", "MAX_ACCOUNTS"},
		{"MAX_ACCOUNTS", "0", "MAX_ACCOUNTS"},
		{"MAX_ACCOUNTS", "-1", "MAX_ACCOUNTS"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			isolateConfig(t)
			t.Setenv("ADMIN_API_KEY", "admin-secret")
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want mention of %s", err, tc.want)
			}
		})
	}
}

func TestMalformedEnvFile(t *testing.T) {
	cases := []string{
		"NO_EQUALS_SIGN\n",
		"=value\n",
		"BAD KEY=value\n",
		"HTTP_ADDR=\":unterminated\n",
		"HTTP_ADDR='unterminated\n",
	}
	for _, contents := range cases {
		t.Run(strings.TrimSpace(contents), func(t *testing.T) {
			dir := isolateConfig(t)
			t.Setenv("ADMIN_API_KEY", "admin-secret")
			writeFile(t, dir, ".env", contents)
			if _, err := Load(); err == nil {
				t.Fatal("Load() succeeded, want parse error")
			}
		})
	}
}

func TestLoadRequiresAdminAPIKey(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		isolateConfig(t)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ADMIN_API_KEY") {
			t.Fatalf("Load() error = %v, want mention of ADMIN_API_KEY", err)
		}
	})

	for _, raw := range []string{"", " ", "\t\n"} {
		t.Run(fmt.Sprintf("blank=%q", raw), func(t *testing.T) {
			isolateConfig(t)
			t.Setenv("ADMIN_API_KEY", raw)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ADMIN_API_KEY") {
				t.Fatalf("Load() error = %v, want mention of ADMIN_API_KEY", err)
			}
		})
	}
}

func TestLoadParsesAccountAPIKeys(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"single", "acct_a:secret-a", map[string]string{"acct_a": "secret-a"}},
		{
			"multiple trimmed",
			" acct_b : secret-b , acct_a:secret-a ",
			map[string]string{"acct_a": "secret-a", "acct_b": "secret-b"},
		},
		{"secret with colon", "acct_a:sec:ret", map[string]string{"acct_a": "sec:ret"}},
		{"empty", "", nil},
		{"whitespace", "   ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfig(t)
			t.Setenv("ADMIN_API_KEY", "admin-secret")
			t.Setenv("API_KEYS", tc.raw)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if !reflect.DeepEqual(cfg.AccountAPIKeys, tc.want) {
				t.Fatalf("AccountAPIKeys = %v, want %v", cfg.AccountAPIKeys, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidAPIKeys(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"missing separator", "acct_a"},
		{"empty entry between commas", "acct_a:secret,,acct_b:secret"},
		{"leading empty entry", ",acct_a:secret"},
		{"trailing empty entry", "acct_a:secret,"},
		{"empty account id", ":secret"},
		{"invalid account id", "bad id:secret"},
		{"overlong account id", strings.Repeat("a", domain.MaxAccountIDLength+1) + ":secret"},
		{"empty secret", "acct_a:"},
		{"blank secret", "acct_a:   "},
		{"duplicate account id", "acct_a:one,acct_a:two"},
		{"duplicate token", "acct_a:same,acct_b:same"},
		{"admin key collision", "acct_a:admin-secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfig(t)
			t.Setenv("ADMIN_API_KEY", "admin-secret")
			t.Setenv("API_KEYS", tc.raw)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "API_KEYS") {
				t.Fatalf("Load() error = %v, want mention of API_KEYS", err)
			}
		})
	}
}

func TestLoadAPIKeyErrorDoesNotLeakBareSecret(t *testing.T) {
	const secret = "sk-live-bare-secret-value"

	isolateConfig(t)
	t.Setenv("ADMIN_API_KEY", "admin-secret")
	t.Setenv("API_KEYS", secret)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded with an API_KEYS entry missing its separator")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error leaked the raw API key: %v", err)
	}
	if !strings.Contains(err.Error(), "API_KEYS entry 1 must use account_id:secret format") {
		t.Fatalf("Load() error = %v, want the entry position and expected format", err)
	}
}

func TestLoadRuntimeSettings(t *testing.T) {
	isolateConfig(t)
	t.Setenv("ADMIN_API_KEY", "admin-secret")
	t.Setenv("DRAIN_TIMEOUT", "2s")
	t.Setenv("WAL_WARN_BYTES", "4096")
	t.Setenv("MAX_ACCOUNTS", "7")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DrainTimeout != 2*time.Second {
		t.Errorf("DrainTimeout = %s, want 2s", cfg.DrainTimeout)
	}
	if cfg.WALWarnBytes != 4096 {
		t.Errorf("WALWarnBytes = %d, want 4096", cfg.WALWarnBytes)
	}
	if cfg.MaxAccounts != 7 {
		t.Errorf("MaxAccounts = %d, want 7", cfg.MaxAccounts)
	}
}

func TestStaticAuthenticator(t *testing.T) {
	authenticator := NewStaticAuthenticator("admin-secret", map[string]string{
		"acct_b": "token-b",
		"acct_a": "token-a",
	})

	if len(authenticator.accountKeys) != 2 ||
		authenticator.accountKeys[0].accountID != "acct_a" ||
		authenticator.accountKeys[1].accountID != "acct_b" {
		t.Fatalf("account keys are not sorted by account ID: %+v", authenticator.accountKeys)
	}

	cases := []struct {
		name   string
		token  string
		want   domain.Principal
		wantOK bool
	}{
		{
			name:   "admin",
			token:  "admin-secret",
			want:   domain.Principal{Subject: "admin", IsAdmin: true},
			wantOK: true,
		},
		{
			name:   "account a",
			token:  "token-a",
			want:   domain.Principal{Subject: "account:acct_a", AccountID: "acct_a"},
			wantOK: true,
		},
		{
			name:   "account b",
			token:  "token-b",
			want:   domain.Principal{Subject: "account:acct_b", AccountID: "acct_b"},
			wantOK: true,
		},
		{name: "unknown", token: "unknown"},
		{name: "empty", token: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := authenticator.Authenticate(tc.token)
			if ok != tc.wantOK {
				t.Fatalf("Authenticate(%q) ok = %v, want %v", tc.token, ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("Authenticate(%q) = %+v, want %+v", tc.token, got, tc.want)
			}
		})
	}
}

func TestStaticAuthenticatorWithoutAdminKey(t *testing.T) {
	authenticator := NewStaticAuthenticator("", map[string]string{"acct_a": "token-a"})

	if _, ok := authenticator.Authenticate(""); ok {
		t.Fatal("empty token authenticated without an admin key")
	}
	if _, ok := authenticator.Authenticate("admin"); ok {
		t.Fatal("admin token authenticated without an admin key")
	}
	principal, ok := authenticator.Authenticate("token-a")
	if !ok {
		t.Fatal("account token did not authenticate")
	}
	if principal.AccountID != "acct_a" || principal.IsAdmin {
		t.Fatalf("principal = %+v", principal)
	}
}

func TestStaticAuthenticatorAdminMatchWins(t *testing.T) {
	authenticator := NewStaticAuthenticator("same-token", map[string]string{"acct_a": "same-token"})
	principal, ok := authenticator.Authenticate("same-token")
	if !ok || !principal.IsAdmin || principal.Subject != "admin" {
		t.Fatalf("Authenticate() = %+v, %v; want admin principal", principal, ok)
	}
}
