package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

// The FreeRADIUS side of the console's `network` tag: the tree written from the
// settings an operator saved, and how it reaches the running server. What
// `foyerctl generate-raddb --max-servers`, `apply-raddb --eap-cert-dir` and
// `--restart-command` do from a shell are console actions here.
//
// ── what is the operator's and what ships with Foyer ─────────────────────────
//
// The worker count and EAP-TLS, with its certificate, are the operator's: stored
// in the database and read on every generation, here and at start.
//
// **How FreeRADIUS takes a new tree ships with Foyer, and is no command at all.**
// FOYER_RADDB_TARGET is a handoff directory this process shares with the
// FreeRADIUS container; each generation is written into it whole, and the
// supervisor in the FreeRADIUS image (`lab/blue-green/supervise.sh`) validates it
// with `radiusd -XC`, restarts FreeRADIUS on it, waits for it to report ready and
// rolls back on any failure, then answers. `internal/raddb/handoff.go` has the
// protocol and the survey behind it. The previous mechanism,
// FOYER_RADIUSD_RESTART_COMMAND, was a shell command held in a deployment's
// environment; nothing in deploy/ ever set it, and it is gone.

// eapDir is where the EAP-TLS material is written inside the generated tree, and
// eapDirInTree is the same directory as FreeRADIUS resolves it: `${certdir}` is
// `${confdir}/certs` in the generated radiusd.conf, wherever the tree is mounted.
const (
	eapDir       = "certs/foyer-eap"
	eapDirInTree = "${certdir}/foyer-eap"
)

// radiusApplyWait bounds how long an apply waits for the supervisor: a
// `radiusd -XC`, a stop and a start, each a few seconds on the lab's hardware.
const radiusApplyWait = 90 * time.Second

// radiusStartupWait is longer: at start the FreeRADIUS container may not exist
// yet, and its supervisor answers the request it finds when it does.
const radiusStartupWait = 5 * time.Minute

// raddbApplier is apistore.RadiusApplier over the handoff in FOYER_RADDB_TARGET.
type raddbApplier struct {
	cfg  config.Config
	st   *store.Store
	h    *raddb.Handoff
	mu   sync.Mutex
	wait time.Duration
	poll time.Duration
}

var _ apistore.RadiusApplier = (*raddbApplier)(nil)

func newRaddbApplier(cfg config.Config, st *store.Store) *raddbApplier {
	return &raddbApplier{cfg: cfg, st: st, h: raddb.NewHandoff(cfg.RaddbTarget),
		wait: radiusApplyWait, poll: 250 * time.Millisecond}
}

// Available: a tree is written from here and FreeRADIUS's supervisor has
// announced itself in the handoff directory.
func (a *raddbApplier) Available() bool {
	return a.cfg.RaddbTarget != "" && a.h.SupervisorPresent()
}

// Apply is the console's apply: always a restart, because the operator may be
// asking FreeRADIUS to re-read a client table whose tree has not changed.
//
// Applies are serialised end to end, answer included: two operators pressing
// Apply at once get two answers in order, never one answer and a timeout.
func (a *raddbApplier) Apply(ctx context.Context, s apistore.RadiusGeneratorSettings) (apistore.RadiusApplyResult, error) {
	// The answer is recorded whether or not the browser that asked is still
	// waiting for it: a restart half-reported is worse than one reported late.
	ctx = context.WithoutCancel(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	generation, genCfg, err := a.submit(s, raddb.HandoffApply)
	if err != nil {
		return apistore.RadiusApplyResult{}, err
	}
	if !a.h.SupervisorPresent() {
		// The request stays in place; a supervisor that starts later takes it up.
		return apistore.RadiusApplyResult{
			Outcome: gen.RadiusApplyOutcomeRestartUnavailable,
			Detail:  "The configuration was written, and no FreeRADIUS supervisor has announced itself to take it up.",
		}, nil
	}
	res, err := a.finish(ctx, generation, genCfg, a.wait)
	if err != nil {
		return apistore.RadiusApplyResult{
			Outcome: gen.RadiusApplyOutcomeFailed,
			Detail: fmt.Sprintf("FreeRADIUS's supervisor did not answer within %s, so whether the new configuration is running is not known. "+
				"Check that the FreeRADIUS container is running.", a.wait),
		}, nil
	}
	return res, nil
}

// Startup hands the tree generated from the stored settings to FreeRADIUS when
// this process starts, and records the supervisor's answer. `mode=startup` makes
// it a restart only when the tree differs from the one running, so restarting the
// control plane does not bounce every 802.1X supplicant on site. It replaces the
// `raddb-generate` one-shot, which regenerated the tree from CLI flags on every
// `up` and overwrote what the console had saved.
func (a *raddbApplier) Startup(ctx context.Context, network *apistore.Network) {
	settings, err := network.RadiusSettings(ctx)
	if err != nil {
		slog.Default().Error("the stored FreeRADIUS settings could not be read, so no tree was generated at start", "error", err.Error())
		return
	}
	// Only the submission holds the lock: an operator's apply must not queue
	// behind a start-up that is waiting minutes for a container to exist. A later
	// apply supersedes this generation, and its own answer is the one recorded.
	a.mu.Lock()
	generation, genCfg, err := a.submit(settings, raddb.HandoffStartup)
	a.mu.Unlock()
	if err != nil {
		return
	}
	res, err := a.finish(ctx, generation, genCfg, radiusStartupWait)
	switch {
	case errors.Is(err, raddb.ErrSuperseded):
		return
	case err != nil:
		slog.Default().Warn("FreeRADIUS's supervisor did not answer in time at start; the configuration is written and it will be taken up when it starts", "waited", radiusStartupWait.String(), "target", a.cfg.RaddbTarget)
		return
	}
	if err := network.RecordStartupApply(ctx, res); err != nil {
		slog.Default().Warn("the start-up apply could not be recorded", "error", err.Error())
	}
}

// submit stages one generation from s and requests it.
func (a *raddbApplier) submit(s apistore.RadiusGeneratorSettings, mode raddb.HandoffMode) (uint64, raddb.Config, error) {
	var genCfg raddb.Config
	generation, err := a.h.Stage(func(dir string) error {
		c, err := generateRaddbInto(a.cfg, s, dir)
		genCfg = c
		return err
	})
	if err != nil {
		slog.Default().Error("the FreeRADIUS configuration could not be written", "target", a.cfg.RaddbTarget, "error", err.Error())
		return 0, raddb.Config{}, err
	}
	if err := a.h.Request(generation, mode); err != nil {
		slog.Default().Error("the FreeRADIUS configuration could not be requested", "error", err.Error())
		return 0, raddb.Config{}, err
	}
	return generation, genCfg, nil
}

// finish waits for the supervisor's answer to generation.
func (a *raddbApplier) finish(ctx context.Context, generation uint64, genCfg raddb.Config, wait time.Duration) (apistore.RadiusApplyResult, error) {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	st, err := a.h.Await(wctx, generation, a.poll)
	if err != nil {
		return apistore.RadiusApplyResult{}, err
	}
	a.h.Prune(generation)

	res := apistore.RadiusApplyResult{Outcome: gen.RadiusApplyOutcomeFailed, Restarted: st.Restarted, Detail: st.Detail}
	if st.Applied() {
		res.Outcome = gen.RadiusApplyOutcomeApplied
		if st.Restarted {
			a.recordSnapshot(ctx, genCfg)
		}
		slog.Default().Info("FreeRADIUS took a configuration generation", "generation", generation, "restarted", st.Restarted)
	} else {
		slog.Default().Warn("FreeRADIUS refused a configuration generation", "generation", generation, "detail", st.Detail)
	}
	return res, nil
}

// recordSnapshot records what the restarted server loaded — the tree's
// configuration and the client table — so the next sync is classified against
// what is running.
func (a *raddbApplier) recordSnapshot(ctx context.Context, genCfg raddb.Config) {
	rows, err := a.st.NasProjection().AllRows(ctx)
	if err != nil {
		slog.Default().Warn("the start-up snapshot could not read the client table", "error", err.Error())
		return
	}
	clients := make([]raddb.ClientRow, len(rows))
	for i, r := range rows {
		clients[i] = raddb.ClientRow{NASName: r.NASName, Shortname: r.Shortname, Type: r.Type, Secret: r.Secret, Server: r.Server}
	}
	if err := raddb.WriteStartupSnapshot(raddbSnapshotPath(a.cfg), raddb.Snapshot{Config: genCfg, Clients: clients}); err != nil {
		slog.Default().Warn("the start-up snapshot could not be written, so the next sync cannot say whether it needs a restart", "error", err.Error())
	}
}

// raddbSnapshotPath is the startup snapshot in the handoff directory, or "" when
// this process writes no tree.
func raddbSnapshotPath(cfg config.Config) string {
	if cfg.RaddbTarget == "" {
		return ""
	}
	return raddb.SnapshotPathFor(cfg.RaddbTarget)
}

// generateRaddbInto is generateRaddb with the operator's settings, into dir: the
// worker count, and EAP-TLS with its material written into the tree.
//
// The EAP key is written 0640 into a directory only this process and the
// FreeRADIUS container can read (`deploy/secretdirs.sh`), which is the footing the
// tree's own database password has; the supervisor re-owns its copy to
// root:foyer-radius-tls so radiusd can read it after it drops to `freerad`. With
// EAP-TLS off the directory is absent, so a key nobody asked for is never in a tree.
func generateRaddbInto(cfg config.Config, s apistore.RadiusGeneratorSettings, dir string) (raddb.Config, error) {
	genCfg, err := raddbBaseConfig(cfg)
	if err != nil {
		return raddb.Config{}, err
	}
	if s.MaxServers > 0 {
		genCfg.MaxServers = s.MaxServers
		if genCfg.MaxSpareServers > s.MaxServers {
			genCfg.MaxSpareServers = s.MaxServers
		}
		if genCfg.StartServers > s.MaxServers {
			genCfg.StartServers = s.MaxServers
		}
		if genCfg.MinSpareServers > genCfg.MaxSpareServers {
			genCfg.MinSpareServers = genCfg.MaxSpareServers
		}
	}
	eap := filepath.Join(dir, eapDir)
	if s.EAP != nil {
		genCfg.EAP = raddb.EAPConfig{
			Enabled:        true,
			DefaultEAPType: "tls",
			CertDir:        eapDirInTree,
			ServerCertFile: eapDirInTree + "/server.pem",
			PrivateKeyFile: eapDirInTree + "/server.key",
			CAFile:         eapDirInTree + "/ca.pem",
		}
	}
	if err := raddb.NewGenerator(genCfg).Generate(dir); err != nil {
		return raddb.Config{}, err
	}
	if s.EAP == nil {
		return genCfg, os.RemoveAll(eap)
	}
	if err := os.MkdirAll(eap, 0o750); err != nil {
		return raddb.Config{}, fmt.Errorf("raddb: create %s: %w", eap, err)
	}
	for name, b := range map[string][]byte{"server.pem": s.EAP.CertificatePEM, "server.key": s.EAP.PrivateKeyPEM, "ca.pem": s.EAP.CAPEM} {
		if err := os.WriteFile(filepath.Join(eap, name), b, 0o640); err != nil {
			// The path, never the contents.
			return raddb.Config{}, fmt.Errorf("raddb: write %s: %w", filepath.Join(eap, name), err)
		}
	}
	return genCfg, nil
}
