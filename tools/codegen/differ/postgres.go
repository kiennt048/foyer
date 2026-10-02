package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The adapter half of the harness: a real PostgreSQL, migrated with goose, seeded
// from the oracle's world, and served through `internal/apistore`.
//
// ── why this file exists ─────────────────────────────────────────────────────
//
// Without it, `differ` would build its Go server over `internal/api/memory` alone
// and never touch `internal/apistore`, so `make diff-api` would produce a
// byte-identical report with or without the adapter. What the memory mode proves is
// that the *handlers* answer what the oracle answers over a world handed to them.
// What it cannot notice is a binding that reads the wrong column, or a query that
// forgot its site scope, because no query runs.
//
// So this mode removes the fake and puts PostgreSQL where it stood. The world still
// comes from the running mock — a transcribed fixture set would disagree about every
// timestamp, which is the reason `seed.go` reads the oracle rather than copying it —
// but it is written into tables and read back out through SQL.
//
// ── what it costs, stated plainly ────────────────────────────────────────────
//
// The seeder is the harness's own code and it can be wrong. A field it fails to
// carry looks exactly like a field the adapter fails to read. That is why the mode
// ships with `-sabotage`: it breaks one binding in the adapter on purpose and
// requires the run to go red, so the claim "this harness can detect a broken
// adapter" is re-derived rather than asserted. See sabotage.go.

// pgHarness is the container, the schema owner's pool, and the store.
type pgHarness struct {
	container testcontainers.Container
	url       string
	owner     *pgxpool.Pool
	store     *store.Store
	retention *store.Retention
	server    *httptest.Server
	portal    *httptest.Server
}

func (h *pgHarness) Close() {
	if h.portal != nil {
		h.portal.Close()
	}
	if h.server != nil {
		h.server.Close()
	}
	if h.owner != nil {
		h.owner.Close()
	}
	if h.store != nil {
		h.store.Close()
	}
	h.retention.Close()
	if h.container != nil {
		_ = testcontainers.TerminateContainer(h.container)
	}
}

// startPostgres runs PostgreSQL 16 and applies every migration.
//
// The same image and the same wait strategy as `internal/store` and
// `internal/apistore` use, because a harness that migrated a different PostgreSQL
// from the one the suite tests would be answering a question nobody asked.
func startPostgres(ctx context.Context) (*pgHarness, error) {
	// testcontainers narrates container startup on the default logger, and the
	// report this command prints is the artefact. The container's own log is not
	// suppressed — a failure to start still surfaces as the returned error.
	tclog.SetDefault(tclog.NewNoopLogger())

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("foyer"),
		postgres.WithUsername("foyer"),
		postgres.WithPassword(randomSecret()),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return nil, fmt.Errorf("starting PostgreSQL: %w\n"+
			"  the adapter mode compares `internal/apistore` against the oracle over a real\n"+
			"  database and cannot be approximated without one. Run with `-mode memory` to\n"+
			"  get the fake-backed comparison alone — and read the coverage block, which will\n"+
			"  then say the adapter was not compared at all", err)
	}
	h := &pgHarness{container: ctr}

	h.url, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		h.Close()
		return nil, err
	}
	if err := store.Migrate(ctx, h.url); err != nil {
		h.Close()
		return nil, fmt.Errorf("applying migrations: %w", err)
	}

	kr, err := store.NewKeyring("differ:" + base64.StdEncoding.EncodeToString(randomBytes(32)))
	if err != nil {
		h.Close()
		return nil, err
	}
	// The key exists for the life of this process and is written nowhere. Shared
	// secrets and controller credentials are envelope-encrypted columns, so a
	// keyring is required to write one at all; generating it here keeps
	// `security.md` §7 — no static credential anywhere in the repository — true of
	// the harness as well as of the product.
	h.store, err = store.Open(ctx, store.Config{URL: h.url, Keys: kr, MaxConns: 8})
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("opening the store: %w", err)
	}

	// Erasure deletes through foyer_retention, as the product does: the adapter mode
	// must compare the real erasure, not a refusal that the connection is missing.
	h.retention, err = store.OpenRetention(ctx, store.Config{URL: h.url})
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("opening the retention connection: %w", err)
	}

	h.owner, err = ownerPool(ctx, h.url)
	if err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

// ownerPool is the connection the seeder writes accounting through.
//
// It is scoped to the tenant and **not** de-privileged, because `radacct` and
// `radpostauth` belong to FreeRADIUS: `store.Open` sets role `foyer_app`, which
// holds SELECT on both and nothing else, so an INSERT through the store's own pool
// is refused by PostgreSQL rather than by a convention. This pool stands in for the
// only thing allowed to write them.
func ownerPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('foyer.tenant_id', $1, false)`, store.DefaultTenantID)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// serve wires the adapter into the same `internal/api` server the memory mode uses.
//
// Identical above the port seam on purpose: the only variable between the two modes
// is which implementation of `api.Ports` answers, which is what makes a difference
// between them attributable.
func (h *pgHarness) serve(sabotage string, w *memory.World) error {
	adapter, err := apistore.New(apistore.Config{
		Store:     h.store,
		Retention: h.retention,
		// A fake behind the driver seam, and it is labelled one.
		// `docs/rules/nas-drivers.md` is explicit that a hand-written NAS mock
		// encodes the author's assumptions, so nothing this harness prints about a
		// probe is evidence about hardware. The scenario declares the probe
		// operations as not compared in this mode for the same reason.
		Drivers: harnessDrivers{guest: harnessGuestAP(w)},
	})
	if err != nil {
		return err
	}
	ports := adapter.Ports()
	if sabotage != "" {
		// The sabotage is applied at the port seam rather than inside
		// `internal/apistore`, so that a harness proving it can go red never
		// requires an edit to the package under test. See sabotage.go.
		ports, err = applySabotage(ports, sabotage, w)
		if err != nil {
			return err
		}
	}
	// The guest surface over the same database. `Adapter.PortalPorts()` is the
	// wiring an operator gets, not a second fake: the sessions, grants, vouchers
	// and placements are SQL, and the only stand-in is the driver behind
	// `harnessDrivers` — which is where `harnessGuestDriver` puts a strict mock
	// rather than a permissive one. See portal.go.
	srv, err := api.New(api.Config{Ports: ports, Portal: adapter.PortalPorts()})
	if err != nil {
		return err
	}
	h.server = httptest.NewServer(srv.Handler())
	// The guest surface is its own listener here too. See `client.portalBase`.
	h.portal = httptest.NewServer(srv.PortalHandler())
	return nil
}

func (h *pgHarness) client() *client {
	return &client{
		base:       h.server.URL + api.BasePath,
		portalBase: h.portal.URL + api.BasePath,
		http:       harnessHTTPClient(h.server.Client()),
	}
}

// harnessDrivers answers for a controller without reaching one.
//
// It mirrors the controller's recorded reachability so that a refusal is a refusal
// and a reachable controller is reachable, and it invents nothing else.
//
// ── a fake may not be more capable than the real thing ───────────────────────
//
// `nas.NewMockDriver(nas.VendorUniFi)` with its capabilities left as they come
// reports `RotateSecret: true`; `internal/nas/unifi.go` — the only built driver —
// reports false, and so does every vendor in the design, because pushing a shared
// secret to a device is a thing none of them can do. A harness built on it reports
// a `500` on `rotateNasSecret` that **no operator could ever see**.
//
// The other fields are optimistic too. The mock claims
// `WalledGarden: fqdn` where the shipped UniFi driver claims `none`, and the reason
// it carries is specific — UniFi's guest pre-authorization allowance is configured
// on the controller and no documented Integration API endpoint programs it, so the
// *driver* cannot maintain a garden even though the controller has the feature.
// `docs/rules/nas-drivers.md` makes `WalledGarden` three-valued precisely so that
// distinction survives, and a capability decides whether a control **exists at all**.
//
// So the fields are not restated here. They are **copied from the shipped
// driver**, which cannot drift from it and needs nobody to come back when the real
// capabilities change. Building one dials nothing: `Capabilities()` returns
// constants and `NewUniFiDriver` only assembles an HTTP client.
// The placement is carried rather than looked up, because the driver seam is
// asked for a driver and not for a world: `harnessGuestAP` reads the oracle's
// devices once, in `serve`, and the answer travels here.
type harnessDrivers struct{ guest guestPlacement }

func (h harnessDrivers) ForController(c store.Controller, _ []byte, _ string) (nas.NASDriver, error) {
	// The same vendor dispatch the real `VendorDrivers` performs. Omada and Ruckus
	// have drivers in the design and not in `internal/nas`, and answering for one
	// from a UniFi fake would be the defect `devices.go` names in as many words: a
	// probe that reported `reachable` from a driver that does not exist.
	if gen.ControllerVendor(c.Vendor) != gen.ControllerVendorUnifi {
		return nil, fmt.Errorf("%w: %s", apistore.ErrNoDriver, c.Vendor)
	}

	// **Strict, not permissive, and the difference decides a named security
	// test.** `NewMockDriver` answers *present* for every MAC it has not been
	// told about, which is exactly `docs/rules/testing.md`'s "an input the
	// harness cannot be wrong about": with it behind the guest surface, *no grant
	// is created for a MAC the driver did not confirm present* passes whatever
	// the product does. `NewStrictMockDriver` refuses by default and
	// `harnessGuestDriver` tells it about the one client the scenario drives,
	// placed on the access point the oracle's own row names — so a step that
	// claims the wrong AP goes red.
	//
	// Nothing on the console path reads presence, so this narrowing costs the
	// rest of the scenario nothing; the probe operations that would notice are
	// declared not-compared in this mode already.
	d := harnessGuestDriver(h.guest)
	d.Caps = shippedUniFiCapabilities(c.BaseURL)

	if c.Reachability != "ok" {
		d.ProbeOutcome = &nas.ProbeResult{
			Reachable: false,
			Errors:    []string{"the harness's driver did not reach this controller: " + c.Reachability},
		}
	}
	return d, nil
}

// shippedUniFiCapabilities is what the built driver reports, read off the built
// driver rather than copied into a literal here.
func shippedUniFiCapabilities(baseURL string) nas.Capabilities {
	if baseURL == "" {
		baseURL = "https://harness.invalid"
	}
	return nas.NewUniFiDriver(nas.UniFiConfig{BaseURL: baseURL}).Capabilities()
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func randomSecret() string { return base64.RawURLEncoding.EncodeToString(randomBytes(18)) }
