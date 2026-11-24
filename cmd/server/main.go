// Command server is the single-binary composition root. It wires configuration,
// SQLite, the in-memory hot path, the asynchronous writer and the HTTP layer.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/aiagate/flow-app-template/internal/domain"
	"github.com/aiagate/flow-app-template/internal/infra/batcher"
	"github.com/aiagate/flow-app-template/internal/infra/config"
	"github.com/aiagate/flow-app-template/internal/infra/hotpath"
	"github.com/aiagate/flow-app-template/internal/infra/sqlite"
	"github.com/aiagate/flow-app-template/internal/presentation/httpapi"
	"github.com/aiagate/flow-app-template/internal/usecase"
)

const (
	defaultAccountID = "default"
	maxHeaderBytes   = 16 << 10
)

// App owns every long-lived dependency of one server process. It is testable
// without starting the process-level signal handler.
type App struct {
	cfg    config.Config
	logger *slog.Logger
	store  *sqlite.Store
	hot    *hotpath.Store
	writer *batcher.Batcher
	server *http.Server

	listener net.Listener
	serveErr chan error

	shutdownOnce sync.Once
	shutdownErr  error
}

// newApp assembles the application in dependency order:
// config -> SQLite (migrated writer, then read-only reader) -> runtime config
// hydration -> hot path hydration -> batcher -> use cases -> HTTP handler.
func newApp(ctx context.Context, cfg config.Config) (*App, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	store, err := sqlite.Open(ctx, cfg.DBPath, cfg.DBReadConns)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store.SetWALWarnBytes(cfg.WALWarnBytes)

	runtimeConfigRepo := sqlite.NewRuntimeConfigRepository(store)
	persistedConfig, foundConfig, err := runtimeConfigRepo.Load(ctx)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("load runtime config: %w", err)
	}

	quotas := sqlite.NewQuotaRepository(store)
	persisted, err := quotas.ListAll(ctx)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("load persisted quotas: %w", err)
	}

	hot := hotpath.New(hotpath.Options{
		InitialBalance:      cfg.DefaultInitialBalance,
		DefaultRateLimitRPM: cfg.DefaultRateLimitRPM,
		MaxAccounts:         cfg.MaxAccounts,
	})
	hot.Hydrate(persisted)

	// Persisted accounts keep their stored balance. Every other configured
	// account is seeded from the process defaults so an API key works on first
	// use and a restart never resets a persisted balance.
	now := time.Now().UTC()
	known := make(map[string]struct{}, len(persisted)+len(cfg.AccountAPIKeys)+1)
	for _, quota := range persisted {
		known[quota.AccountID] = struct{}{}
	}
	seeds := make([]domain.AccountQuota, 0, 1+len(cfg.AccountAPIKeys))
	seed := func(accountID string) {
		if _, exists := known[accountID]; exists {
			return
		}
		known[accountID] = struct{}{}
		seeds = append(seeds, domain.AccountQuota{
			AccountID:    accountID,
			Balance:      cfg.DefaultInitialBalance,
			RateLimitRPM: cfg.DefaultRateLimitRPM,
			UpdatedAt:    now,
		})
	}
	seed(defaultAccountID)

	accountIDs := make([]string, 0, len(cfg.AccountAPIKeys))
	for accountID := range cfg.AccountAPIKeys {
		accountIDs = append(accountIDs, accountID)
	}
	sort.Strings(accountIDs)
	for _, accountID := range accountIDs {
		seed(accountID)
	}
	if len(seeds) > 0 {
		hot.Hydrate(seeds)
	}

	if foundConfig && persistedConfig != nil {
		hot.UpdateConfig(persistedConfig)
	} else {
		hot.UpdateConfig(&domain.RuntimeConfig{
			DefaultRateLimitRPM: cfg.DefaultRateLimitRPM,
			DefaultRequestCost:  cfg.DefaultRequestCost,
		})
	}
	logger.Info("hot path hydrated",
		slog.Int("persisted_accounts", len(persisted)),
		slog.Int("seeded_accounts", len(seeds)),
		slog.Int("accounts", len(known)),
	)

	writer, err := batcher.New(store, batcher.Options{Logger: logger})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("start batcher: %w", err)
	}

	auth := config.NewStaticAuthenticator(cfg.AdminAPIKey, cfg.AccountAPIKeys)

	billing := usecase.NewBillingUseCase(hot, quotas, sqlite.NewAuditQuery(store), writer)
	runtimeUseCase := usecase.NewRuntimeUseCase(hot, writer, store, runtimeConfigRepo)
	handler, err := httpapi.NewHandler(httpapi.Dependencies{
		Users:       usecase.NewUserUseCase(store, sqlite.NewUserRepository(store)),
		Teams:       usecase.NewTeamUseCase(store, sqlite.NewTeamRepository(store)),
		Memberships: usecase.NewMembershipUseCase(store, sqlite.NewTeamRepository(store), sqlite.NewUserRepository(store), sqlite.NewMembershipRepository(store)),
		Billing:     billing,
		Runtime:     runtimeUseCase,
		HotPath:     hot,
		Queue:       writer,
		Auth:        auth,
		Logger:      logger,
	})
	if err != nil {
		drainTimeout := cfg.DrainTimeout
		if drainTimeout <= 0 {
			drainTimeout = cfg.ShutdownTimeout
		}
		if drainTimeout <= 0 {
			drainTimeout = 10 * time.Second
		}
		drainCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		defer cancel()
		_ = writer.Drain(drainCtx)
		_ = store.Close()
		return nil, fmt.Errorf("build http handler: %w", err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	return &App{
		cfg:      cfg,
		logger:   logger,
		store:    store,
		hot:      hot,
		writer:   writer,
		server:   server,
		serveErr: make(chan error, 1),
	}, nil
}

// Start binds the configured address synchronously, then serves in the
// background so callers can learn the effective address before sending traffic.
func (a *App) Start() error {
	listener, err := net.Listen("tcp", a.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.HTTPAddr, err)
	}
	a.listener = listener
	a.server.Addr = listener.Addr().String()
	if a.logger != nil {
		a.logger.Info("http server listening", slog.String("addr", a.Addr()))
	}
	go func() {
		a.serveErr <- a.server.Serve(listener)
	}()
	return nil
}

// Addr reports the effective listen address after Start.
func (a *App) Addr() string {
	if a.listener == nil {
		return a.cfg.HTTPAddr
	}
	return a.listener.Addr().String()
}

// ServeError exposes the background Serve result.
func (a *App) ServeError() <-chan error { return a.serveErr }

// Wait blocks until Serve has returned.
func (a *App) Wait() error {
	err := <-a.serveErr
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops accepting traffic, drains every queued write, and only then
// closes the database using one shared deadline for both phases.
func (a *App) Shutdown(ctx context.Context) error {
	return a.ShutdownWithTimeouts(ctx, ctx)
}

// ShutdownWithTimeouts stops accepting traffic, flushes the final authoritative
// quota snapshot, drains the writer and closes the store. HTTP shutdown and
// write drain have independent budgets so a slow connection drain cannot starve
// the batcher. The order is mandatory: in-flight handlers must be able to
// enqueue before the writer drains, and the writer must finish before the store
// closes.
func (a *App) ShutdownWithTimeouts(httpCtx, drainCtx context.Context) error {
	a.shutdownOnce.Do(func() {
		if a.logger != nil {
			a.logger.Info("shutdown started")
		}

		var errs []error
		if a.server != nil {
			if err := a.server.Shutdown(httpCtx); err != nil {
				errs = append(errs, fmt.Errorf("shutdown http server: %w", err))
			}
		}
		// Handlers have stopped. Queue one final authoritative quota snapshot
		// per account so out-of-order handler completions cannot leave an older
		// balance as the last write.
		if a.writer != nil && a.hot != nil {
			now := time.Now().UTC()
			for _, quota := range a.hot.SnapshotQuotas(now) {
				if err := a.writer.EnqueueQuotaSyncBlocking(drainCtx, quota); err != nil {
					errs = append(errs, fmt.Errorf("enqueue final quota for %s: %w", quota.AccountID, err))
				}
			}
		}
		if a.writer != nil {
			if err := a.writer.Drain(drainCtx); err != nil {
				errs = append(errs, fmt.Errorf("drain write queue: %w", err))
			}
		}
		if a.store != nil {
			if err := a.store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close store: %w", err))
			}
		}
		a.shutdownErr = errors.Join(errs...)

		if a.logger != nil {
			if a.shutdownErr != nil {
				a.logger.Error("shutdown completed with errors", slog.Any("error", a.shutdownErr))
			} else {
				a.logger.Info("shutdown completed")
			}
		}
	})
	return a.shutdownErr
}

// shutdownApp builds independent HTTP and drain deadlines from the validated
// configuration. A missing shutdown timeout falls back to 10s; a missing drain
// timeout follows the shutdown timeout.
func shutdownApp(app *App, cfg config.Config) error {
	httpTimeout := cfg.ShutdownTimeout
	if httpTimeout <= 0 {
		httpTimeout = 10 * time.Second
	}
	drainTimeout := cfg.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = cfg.ShutdownTimeout
	}
	if drainTimeout <= 0 {
		drainTimeout = httpTimeout
	}

	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), httpTimeout)
	defer cancelHTTP()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainTimeout)
	defer cancelDrain()

	return app.ShutdownWithTimeouts(httpCtx, drainCtx)
}

// run starts the app, waits for cancellation or a fatal Serve error, and then
// performs the graceful shutdown sequence.
func run(ctx context.Context, cfg config.Config) error {
	app, err := newApp(ctx, cfg)
	if err != nil {
		return err
	}

	if err := app.Start(); err != nil {
		_ = shutdownApp(app, cfg)
		return err
	}

	select {
	case <-ctx.Done():
	case serveErr := <-app.ServeError():
		shutdownErr := shutdownApp(app, cfg)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return errors.Join(fmt.Errorf("serve: %w", serveErr), shutdownErr)
		}
		return shutdownErr
	}

	if err := shutdownApp(app, cfg); err != nil {
		return err
	}
	return app.Wait()
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", slog.Any("error", err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Once the first signal starts the graceful shutdown, restore the default
	// signal handler so a second SIGINT/SIGTERM terminates the process
	// immediately instead of waiting for TimeoutStopSec.
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := run(ctx, cfg); err != nil {
		logger.Error("server", slog.Any("error", err))
		os.Exit(1)
	}
}
