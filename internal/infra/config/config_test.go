package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var configKeys = []string{
	"HTTP_ADDR",
	"DB_PATH",
	"DB_READ_CONNS",
	"DEFAULT_INITIAL_BALANCE",
	"DEFAULT_RATE_LIMIT_RPM",
	"DEFAULT_REQUEST_COST",
	"SHUTDOWN_TIMEOUT",
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
}

func TestProcessEnvironmentWinsOverEnvFiles(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, dir, ".env.local", "HTTP_ADDR=:9000\nDB_PATH=./local.db\n")
	writeFile(t, dir, ".env", "HTTP_ADDR=:7000\nDB_PATH=./env.db\n")
	t.Setenv("HTTP_ADDR", ":1234")

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
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			isolateConfig(t)
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
			writeFile(t, dir, ".env", contents)
			if _, err := Load(); err == nil {
				t.Fatal("Load() succeeded, want parse error")
			}
		})
	}
}
