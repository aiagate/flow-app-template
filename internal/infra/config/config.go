// Package config loads and validates the process configuration from the
// environment, after applying local .env files that do not override variables
// already present in the process environment.
package config

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr        = ":8080"
	defaultDBPath          = "./data/flow.db"
	defaultDBReadConns     = 5
	minDBReadConns         = 4
	maxDBReadConns         = 6
	defaultInitialBalance  = int64(1000)
	defaultRateLimitRPM    = 120
	defaultRequestCost     = int64(1)
	defaultShutdownTimeout = 10 * time.Second
)

// Config is the validated application configuration consumed by the
// composition root.
type Config struct {
	HTTPAddr              string
	DBPath                string
	DBReadConns           int
	DefaultInitialBalance int64
	DefaultRateLimitRPM   int
	DefaultRequestCost    int64
	ShutdownTimeout       time.Duration
}

// Load reads .env.local then .env from the project root (or the current
// directory when no project root can be found), then validates the effective
// environment. Existing process environment variables always win.
func Load() (Config, error) {
	root := projectRoot()
	for _, name := range []string{".env.local", ".env"} {
		if err := loadEnvFile(filepath.Join(root, name)); err != nil {
			return Config{}, err
		}
	}

	httpAddr := envValue("HTTP_ADDR", defaultHTTPAddr)
	if httpAddr == "" {
		return Config{}, fmt.Errorf("HTTP_ADDR must not be empty")
	}
	_, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be a host:port address: %w", err)
	}
	if port == "" {
		return Config{}, fmt.Errorf("HTTP_ADDR must include a port")
	}

	dbPath := envValue("DB_PATH", defaultDBPath)
	if dbPath == "" {
		return Config{}, fmt.Errorf("DB_PATH must not be empty")
	}

	readConns, err := envInt("DB_READ_CONNS", defaultDBReadConns)
	if err != nil {
		return Config{}, err
	}
	if readConns < minDBReadConns {
		readConns = minDBReadConns
	}
	if readConns > maxDBReadConns {
		readConns = maxDBReadConns
	}

	initialBalance, err := envInt64("DEFAULT_INITIAL_BALANCE", defaultInitialBalance)
	if err != nil {
		return Config{}, err
	}
	if initialBalance < 0 {
		return Config{}, fmt.Errorf("DEFAULT_INITIAL_BALANCE must be non-negative")
	}

	rateLimitRPM, err := envInt("DEFAULT_RATE_LIMIT_RPM", defaultRateLimitRPM)
	if err != nil {
		return Config{}, err
	}
	if rateLimitRPM <= 0 {
		return Config{}, fmt.Errorf("DEFAULT_RATE_LIMIT_RPM must be positive")
	}

	requestCost, err := envInt64("DEFAULT_REQUEST_COST", defaultRequestCost)
	if err != nil {
		return Config{}, err
	}
	if requestCost < 0 {
		return Config{}, fmt.Errorf("DEFAULT_REQUEST_COST must be non-negative")
	}

	timeout, err := envDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	if timeout <= 0 {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be positive")
	}

	return Config{
		HTTPAddr:              httpAddr,
		DBPath:                dbPath,
		DBReadConns:           readConns,
		DefaultInitialBalance: initialBalance,
		DefaultRateLimitRPM:   rateLimitRPM,
		DefaultRequestCost:    requestCost,
		ShutdownTimeout:       timeout,
	}, nil
}

func envValue(key, fallback string) string {
	if raw, ok := os.LookupEnv(key); ok {
		if value := strings.TrimSpace(raw); value != "" {
			return value
		}
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := envValue(key, strconv.Itoa(fallback))
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return value, nil
}

func envInt64(key string, fallback int64) (int64, error) {
	raw := envValue(key, strconv.FormatInt(fallback, 10))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return value, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := envValue(key, fallback.String())
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return value, nil
}

// projectRoot walks up from the working directory to the first directory
// containing go.mod. Running from a subdirectory therefore behaves like
// running from the repository root.
func projectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export"))
		}
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			return fmt.Errorf("%s:%d: invalid variable name", path, lineNo)
		}
		value, err := parseEnvValue(rawValue)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s:%d: set %s: %w", path, lineNo, key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func parseEnvValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '"':
		end := closingQuote(value, '"')
		if end < 0 {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		if err := requireOnlyComment(value[end+1:]); err != nil {
			return "", err
		}
		unquoted, err := strconv.Unquote(value[:end+1])
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		return unquoted, nil
	case '\'':
		end := closingQuote(value, '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		if err := requireOnlyComment(value[end+1:]); err != nil {
			return "", err
		}
		return value[1:end], nil
	default:
		if index := strings.Index(value, " #"); index >= 0 {
			value = strings.TrimSpace(value[:index])
		}
		return value, nil
	}
}

// closingQuote finds the index of the closing quote, honoring backslash
// escapes for double-quoted values.
func closingQuote(value string, quote byte) int {
	escaped := false
	for index := 1; index < len(value); index++ {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && value[index] == '\\':
			escaped = true
		case value[index] == quote:
			return index
		}
	}
	return -1
}

func requireOnlyComment(trailing string) error {
	trimmed := strings.TrimSpace(trailing)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	return fmt.Errorf("unexpected content after quoted value")
}
