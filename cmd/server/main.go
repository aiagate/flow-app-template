// Command server is the single-binary composition root. It wires configuration,
// SQLite, the in-memory hot path, the asynchronous writer and the HTTP layer.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
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

const defaultAccountID = "default"

// App owns every long-lived dependency of one server process. It is testable
// without starting the process-level signal handler.
type App struct {
	cfg    config.Config
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
// config -> SQLite (migrated writer, then read-only reader) -> hot path
// hydration -> batcher -> use cases -> HTTP handler.
func newApp(ctx context.Context, cfg config.Config) (*App, error) {
	store, err := sqlite.Open(ctx, cfg.DBPath, cfg.DBReadConns)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
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
	})
	hot.Hydrate(persisted)
	if _, ok := hot.GetAccountState(defaultAccountID); !ok {
		hot.Hydrate([]domain.AccountQuota{{
			AccountID:    defaultAccountID,
			Balance:      cfg.DefaultInitialBalance,
			RateLimitRPM: cfg.DefaultRateLimitRPM,
			UpdatedAt:    time.Now().UTC(),
		}})
	}
	hot.UpdateConfig(&domain.RuntimeConfig{
		DefaultRateLimitRPM: cfg.DefaultRateLimitRPM,
		DefaultRequestCost:  cfg.DefaultRequestCost,
	})

	writer, err := batcher.New(store, batcher.Options{
		QueueCapacity: batcher.QueueCapacity,
		BatchSize:     batcher.BatchSize,
		FlushInterval: batcher.FlushInterval,
	})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("start batcher: %w", err)
	}

	billing := usecase.NewBillingUseCase(hot, quotas, sqlite.NewAuditQuery(store), writer)
	runtimeUseCase := usecase.NewRuntimeUseCase(hot, writer, store)
	handler, err := httpapi.NewHandler(httpapi.Dependencies{
		Users:       usecase.NewUserUseCase(store, sqlite.NewUserRepository(store)),
		Teams:       usecase.NewTeamUseCase(store, sqlite.NewTeamRepository(store)),
		Memberships: usecase.NewMembershipUseCase(store, sqlite.NewTeamRepository(store), sqlite.NewUserRepository(store), sqlite.NewMembershipRepository(store)),
		Billing:     billing,
		Runtime:     runtimeUseCase,
		HotPath:     hot,
		Queue:       writer,
	})
	if err != nil {
		drainCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
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
	}

	return &App{
		cfg:      cfg,
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
// closes the database. The order is mandatory: in-flight handlers must be able
// to enqueue before the writer drains, and the writer must finish before the
// store closes.
func (a *App) Shutdown(ctx context.Context) error {
	a.shutdownOnce.Do(func() {
		var errs []error
		if a.server != nil {
			if err := a.server.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("shutdown http server: %w", err))
			}
		}
		// Handlers have stopped. Queue one final authoritative quota snapshot
		// per account so out-of-order handler completions cannot leave an older
		// balance as the last write.
		if a.writer != nil && a.hot != nil {
			now := time.Now().UTC()
			for _, quota := range a.hot.SnapshotQuotas(now) {
				if err := a.writer.EnqueueQuotaSync(quota); err != nil {
					errs = append(errs, fmt.Errorf("enqueue final quota for %s: %w", quota.AccountID, err))
				}
			}
		}
		if a.writer != nil {
			if err := a.writer.Drain(ctx); err != nil {
				errs = append(errs, fmt.Errorf("drain write queue: %w", err))
			}
		}
		if a.store != nil {
			if err := a.store.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close store: %w", err))
			}
		}
		a.shutdownErr = errors.Join(errs...)
	})
	return a.shutdownErr
}

// run starts the app, waits for cancellation or a fatal Serve error, and then
// performs the graceful shutdown sequence.
func run(ctx context.Context, cfg config.Config) error {
	app, err := newApp(ctx, cfg)
	if err != nil {
		return err
	}

	if err := app.Start(); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
		return err
	}

	select {
	case <-ctx.Done():
	case serveErr := <-app.ServeError():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", serveErr)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := app.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return app.Wait()
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		log.Fatalf("server: %v", err)
	}
}
