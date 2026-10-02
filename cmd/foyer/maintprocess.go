package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/store"
	"github.com/kiennt048/foyer/internal/version"
)

// `foyer maintenance`: the same binary, a different job.
//
// If the control plane's container held every database login, the stale-session
// reaper's and the retention role's included, a compromise of the process that
// parses every console request and every portal form would also be a way to change
// FreeRADIUS's accounting and erase the audit trail.
// PacketFence runs its maintenance as `pfcron`, a service of its own beside the
// portal and the admin; this is that shape. `deploy/compose.yaml` runs this mode as
// the `maintenance` service, which alone is given the two logins, and the control
// plane is told where it is (FOYER_MAINTENANCE_URL) and holds neither.
// internal/jobs/remote.go has the door between them.
//
// It serves no console, no portal and no Policy API, and it is given no KEK:
// nothing it runs unseals anything (`config.MaintenanceProcessError`).

// maintenanceMode is the argument that selects it.
const maintenanceMode = "maintenance"

// maintenanceDoorConns bounds the door's connections: its one caller is the
// control plane, and an erasure is an administrator's click.
const maintenanceDoorConns = 16

// maintenanceAppConns is the application pool of the maintenance container.
const maintenanceAppConns = 4

func runMaintenance() error {
	slog.Default().Info("the Foyer maintenance process is starting", "version", version.String(), "revision", version.Revision())
	cfg := config.Load()
	if err := cfg.MaintenanceProcessError(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A small pool, and that is PostgreSQL's arithmetic rather than a taste: the
	// deployment's max_connections of 80 was budgeted with one control-plane pool
	// of 25 (deploy/compose.yaml, the postgres block), and a second 25 here would
	// be past it. The jobs run one at a time per kind.
	app, err := store.Open(ctx, store.Config{URL: cfg.DatabaseURL, MaxConns: maintenanceAppConns})
	if err != nil {
		return fmt.Errorf("database unavailable, refusing to start: %w", err)
	}
	defer app.Close()
	if err := store.RequireLeastPrivilege(ctx, app, store.DefaultAppRole); err != nil {
		return err
	}

	maint, err := openMaintenance(ctx, cfg, app)
	if err != nil {
		return err
	}
	defer maint.Stop()

	door, err := maintenanceDoor(cfg, app, maint)
	if err != nil {
		return err
	}
	s := &namedServer{name: "maintenance", maxConns: maintenanceDoorConns, srv: &http.Server{
		Addr: cfg.MaintenanceAddr, Handler: door,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}}
	errs := make(chan error, 1)
	go func() {
		slog.Default().Info("the listener is serving", "listener", s.name, "address", s.srv.Addr, "scheme", s.scheme(), "maxConnections", s.maxConns)
		if err := s.serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	maint.Start(ctx)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errs:
		shutdown([]*namedServer{s})
		return err
	case <-sigCh:
	}
	slog.Default().Info("shutting the Foyer maintenance process down")
	shutdown([]*namedServer{s})
	return nil
}

// maintenanceDoor is the handler the control plane calls: erasure's retention half
// and "the settings changed". Split out so the wiring is testable without a
// listener.
func maintenanceDoor(cfg config.Config, app *store.Store, maint *maintenance) (http.Handler, error) {
	var eraser store.RetentionEraser
	if r := maint.Retention(); r != nil {
		eraser = r
	}
	return jobs.NewServer(jobs.ServerConfig{
		Token: cfg.MaintenanceToken.Value, App: app, Retention: eraser, Logger: slog.Default(),
		SettingsChanged: func(ctx context.Context) {
			v, err := jobs.FromStore(app)(ctx)
			if err != nil {
				slog.Default().Warn("re-reading the maintenance settings after a change failed; the next cycle re-reads them",
					"error", err.Error())
				return
			}
			maint.SettingsChanged(v)
		},
	})
}

// maintenanceFor is the control plane's choice: with FOYER_MAINTENANCE_URL the
// jobs are the maintenance container's and this process opens no maintenance pool
// (maint is nil — its methods are nil-safe); without it they run here, as on a
// developer's machine.
func maintenanceFor(ctx context.Context, cfg config.Config, app *store.Store) (*maintenance, store.RetentionEraser, func(store.MaintenanceValues), error) {
	if cfg.MaintenanceURL != "" {
		eraser, changed, err := remoteMaintenance(cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		slog.Default().Info("the jobs and the retention half of every erasure run in the maintenance container", "url", cfg.MaintenanceURL)
		return nil, eraser, changed, nil
	}
	maint, err := openMaintenance(ctx, cfg, app)
	if err != nil {
		return nil, nil, nil, err
	}
	// Only a connection that opened: a nil *Retention in the interface would be a
	// non-nil eraser that fails every erasure for the wrong reason.
	var eraser store.RetentionEraser
	if r := maint.Retention(); r != nil {
		eraser = r
	}
	return maint, eraser, maint.SettingsChanged, nil
}

// remoteMaintenance is the control plane's side when FOYER_MAINTENANCE_URL is set:
// erasure's retention half and the settings notification go to the maintenance
// container, and this process opens neither role's pool and schedules nothing.
func remoteMaintenance(cfg config.Config) (store.RetentionEraser, func(store.MaintenanceValues), error) {
	c, err := jobs.NewRemoteClient(cfg.MaintenanceURL, cfg.MaintenanceToken.Value)
	if err != nil {
		return nil, nil, err
	}
	changed := func(store.MaintenanceValues) {
		// Not on the request's goroutine: the operator's save has already
		// succeeded, and a slow maintenance container must not hold its answer.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c.SettingsChanged(ctx, slog.Default())
		}()
	}
	return c, changed, nil
}
