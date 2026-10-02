package apistore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

// The `network` tag's persistence, and the two RADIUS actions that reach past the
// database: the client-table sync and the configuration apply — an operator never
// runs `foyerctl`.
//
// ── the sync, in process ─────────────────────────────────────────────────────
//
// A RADIUS client added in the console has to reach FreeRADIUS without anybody
// running `foyerctl sync-nas`. The API calls SyncRadiusClients after every
// RADIUS-client write, and this is that command's
// logic run here — `store.NasProjectionRepo.Sync`, one transaction, and the restart
// question answered by `raddb.ClassifySync` against the startup snapshot, the same
// function the command uses. What it did is kept (`radius_operations`) so the
// console can show the state, and audited by the API.
//
// ── the apply, through the deployment's own mechanism ────────────────────────
//
// `foyerctl apply-raddb --restart-command` ran an arbitrary shell command. **That
// command does not become a console setting**, and it is the one value on the
// tag's list that is deliberately not an input: a string an administrator types
// and this process executes would make `nas:manage` equal to a shell on the
// appliance, which no product surveyed offers — PacketFence restarts its services
// through its own `pfcmd service` and ClearPass through a fixed service list, never
// through a command the web UI supplies. The *action* is the console's
// (`applyRadiusConfiguration`); the *mechanism* ships with Foyer — no command at
// all, not even a deployment's: the tree goes through
// `raddb.Handoff` to a supervisor in the FreeRADIUS image that validates,
// restarts, confirms and rolls back (internal/raddb/handoff.go). RadiusApplier is
// that mechanism's seam.

// RadiusApplier writes FreeRADIUS's tree from the stored settings and has it
// taken up. `cmd/foyer` implements it over `raddb.Handoff` and the supervisor in
// the FreeRADIUS image; nil means this deployment writes no tree from the control
// plane at all.
type RadiusApplier interface {
	// Available reports whether a tree is written from this process and
	// FreeRADIUS's supervisor has announced itself.
	Available() bool
	// Apply writes the tree and waits for the supervisor's answer. A nil error
	// with Outcome `restart-unavailable` is a tree written that nothing will
	// load; an error is a tree that could not be written at all.
	Apply(ctx context.Context, s RadiusGeneratorSettings) (RadiusApplyResult, error)
}

// RadiusApplyResult is what one apply did.
type RadiusApplyResult struct {
	Outcome   gen.RadiusApplyOutcome
	Restarted bool
	// Detail is the supervisor's one sanitised line; see gen.RadiusApply.Detail.
	Detail string
}

// RadiusGeneratorSettings is what the console sets about the generated tree.
type RadiusGeneratorSettings struct {
	MaxServers int
	// EAP is the EAP-TLS material, nil when EAP-TLS is off.
	EAP *EAPMaterial
}

// EAPMaterial is the EAP-TLS server pair and client CA, in PEM. The key is
// unsealed for the one write that puts it in the tree.
type EAPMaterial struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
	CAPEM          []byte
}

// NetworkConfig is what NewNetwork needs.
type NetworkConfig struct {
	Store *store.Store
	// Applier is nil when this process writes no FreeRADIUS tree.
	Applier RadiusApplier
	// SnapshotPath is the startup snapshot a sync is classified against; empty
	// means no classification, which is said rather than guessed.
	SnapshotPath string
	Now          func() time.Time
}

// Network implements api.NetworkSettingsStore.
type Network struct {
	st       *store.Store
	applier  RadiusApplier
	snapshot string
	now      func() time.Time
}

var _ api.NetworkSettingsStore = (*Network)(nil)

// NewNetwork builds the port. It opens nothing.
func NewNetwork(cfg NetworkConfig) *Network {
	n := &Network{st: cfg.Store, applier: cfg.Applier, snapshot: cfg.SnapshotPath, now: cfg.Now}
	if n.now == nil {
		n.now = time.Now
	}
	return n
}

func (n *Network) repo() *store.NetworkSettingsRepo { return n.st.NetworkSettings() }

func networkErr(err error) error {
	if errors.Is(err, store.ErrNoKeyring) {
		return api.ErrKeyringAbsent
	}
	return out(err)
}

// ── settings ─────────────────────────────────────────────────────────────────

func (n *Network) StoredNetwork(ctx context.Context) (api.StoredNetwork, error) {
	row, err := n.repo().Get(ctx)
	if err != nil {
		return api.StoredNetwork{}, out(err)
	}
	s := api.StoredNetwork{
		PortalURL: row.PortalURL, VenueURL: row.VenueURL,
		RadiusMaxServers: row.RadiusMaxServers, EapTLSEnabled: row.EAPTLSEnabled,
		RadiusChangedAt: row.RadiusChangedAt, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy,
	}
	if len(row.Throttles) > 0 {
		var t gen.SignInThrottles
		if err := json.Unmarshal(row.Throttles, &t); err != nil {
			return api.StoredNetwork{}, fmt.Errorf("apistore: stored throttles: %w", err)
		}
		s.Throttles = &t
	}
	if len(row.BodyCeilings) > 0 {
		var c gen.BodyCeilings
		if err := json.Unmarshal(row.BodyCeilings, &c); err != nil {
			return api.StoredNetwork{}, fmt.Errorf("apistore: stored body ceilings: %w", err)
		}
		s.BodyCeilings = &c
	}
	return s, nil
}

func (n *Network) SaveTransport(ctx context.Context, in api.StoredTransport, actor string) error {
	t, err := json.Marshal(in.Throttles)
	if err != nil {
		return err
	}
	c, err := json.Marshal(in.BodyCeilings)
	if err != nil {
		return err
	}
	return out(n.repo().SaveTransport(ctx, in.PortalURL, in.VenueURL, t, c, actor))
}

func (n *Network) SaveRadius(ctx context.Context, maxServers int, eapTLS bool, actor string) error {
	return out(n.repo().SaveRadius(ctx, maxServers, eapTLS, actor))
}

// ── certificates ─────────────────────────────────────────────────────────────

func (n *Network) PortalCertificate(ctx context.Context) (api.StoredCertificate, error) {
	c, err := n.repo().Certificate(ctx, store.CertificatePortalTLS)
	if err != nil {
		return api.StoredCertificate{}, out(err)
	}
	key, err := n.repo().CertificateKey(ctx, store.CertificatePortalTLS)
	if errors.Is(err, store.ErrNoKeyring) {
		// Stored under a key this process no longer holds: nothing is servable,
		// which is what "not found" means to the one caller.
		return api.StoredCertificate{}, api.ErrNotFound
	}
	if err != nil {
		return api.StoredCertificate{}, out(err)
	}
	return api.StoredCertificate{CertificatePEM: c.CertificatePEM, KeyPEM: key, UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy}, nil
}

func (n *Network) PutPortalCertificate(ctx context.Context, certPEM string, keyPEM []byte, portalURL *string, actor string) error {
	return networkErr(n.repo().PutPortalCertificate(ctx, certPEM, keyPEM, portalURL, actor))
}

func (n *Network) DeletePortalCertificate(ctx context.Context) error {
	return out(n.repo().DeleteCertificate(ctx, store.CertificatePortalTLS))
}

func (n *Network) EapCertificate(ctx context.Context) (api.StoredCertificate, error) {
	c, err := n.repo().Certificate(ctx, store.CertificateEAPTLS)
	if err != nil {
		return api.StoredCertificate{}, out(err)
	}
	return api.StoredCertificate{CertificatePEM: c.CertificatePEM, CAPEM: deref(c.CAPEM), UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy}, nil
}

func (n *Network) PutEapCertificate(ctx context.Context, certPEM string, keyPEM []byte, caPEM string, actor string) error {
	return networkErr(n.repo().PutEAPCertificate(ctx, certPEM, keyPEM, caPEM, actor))
}

// ── the client-table sync ────────────────────────────────────────────────────

// SyncRadiusClients runs `foyerctl sync-nas`'s logic in process and records it.
func (n *Network) SyncRadiusClients(ctx context.Context, trigger gen.RadiusSyncTrigger, actor string) (gen.RadiusClientSync, error) {
	rec := gen.RadiusClientSync{
		At: n.now().UTC(), Trigger: trigger, Outcome: gen.RadiusClientSyncOutcomeSynced,
		Removed: []string{}, Skipped: []gen.RadiusClientSkip{}, Reasons: []string{},
	}
	if actor != "" {
		rec.ActorUsername = &actor
	}
	res, err := n.st.NasProjection().Sync(ctx)
	var conflict *store.NasAddressConflict
	switch {
	case errors.As(err, &conflict):
		// Named, both sides: "already taken" without saying by whom sends an
		// operator to the wrong database (`foyerctl sync-nas`'s renderConflict).
		rec.Outcome = gen.RadiusClientSyncOutcomeRefused
		rec.Reasons = append(rec.Reasons, fmt.Sprintf(
			"%s is already projected by another tenant, so nothing was written. Change one of the two addresses, or disable the client that no longer sits there.",
			conflict.NASName))
	case errors.Is(err, store.ErrNoKeyring):
		rec.Outcome = gen.RadiusClientSyncOutcomeRefused
		rec.Reasons = append(rec.Reasons,
			"No key-encryption key is configured, so no shared secret could be unsealed and nothing was written.")
	case err != nil:
		// The error's text is the database's and does not reach a client; it
		// reaches the log, which is where the answer below sends an operator. It
		// names client and secret ids, never a secret.
		log.Printf("WARNING: the RADIUS client-table sync (%s) failed and wrote nothing: %v", trigger, err)
		rec.Outcome = gen.RadiusClientSyncOutcomeFailed
		rec.Reasons = append(rec.Reasons, "The client table could not be written; nothing was changed. The reason is in the control plane's log.")
	default:
		rec.Inserted, rec.Updated, rec.Unchanged = res.Inserted, res.Updated, res.Unchanged
		rec.Removed = append(rec.Removed, res.Removed...)
		for _, s := range res.Skipped {
			rec.Skipped = append(rec.Skipped, gen.RadiusClientSkip{NasName: s.Address, Reason: s.Reason})
		}
		n.classify(ctx, &rec, res.Removed)
	}
	if werr := n.record(ctx, store.RadiusOperationSync, rec.At, rec); werr != nil {
		return rec, werr
	}
	return rec, nil
}

// classify answers whether the running server needs a restart for this sync. No
// snapshot is an absent class and a reason, never `none`.
func (n *Network) classify(ctx context.Context, rec *gen.RadiusClientSync, removed []string) {
	if n.snapshot == "" {
		rec.Reasons = append(rec.Reasons, "This deployment records no startup snapshot, so whether FreeRADIUS needs a restart to see this is not known.")
		return
	}
	rows, err := n.st.NasProjection().AllRows(ctx)
	if err != nil {
		rec.Reasons = append(rec.Reasons, "The client table could not be re-read, so whether a restart is needed is not known.")
		return
	}
	cr := make([]raddb.ClientRow, len(rows))
	for i, r := range rows {
		cr[i] = raddb.ClientRow{NASName: r.NASName, Shortname: r.Shortname, Type: r.Type, Secret: r.Secret, Server: r.Server}
	}
	change, _, err := raddb.ClassifySync(n.snapshot, cr, removed)
	if errors.Is(err, raddb.ErrNoStartupSnapshot) {
		rec.Reasons = append(rec.Reasons, "No startup snapshot exists yet, so whether FreeRADIUS needs a restart to see this is not known. The first apply from the console records one.")
		return
	}
	if err != nil {
		rec.Reasons = append(rec.Reasons, "The startup snapshot could not be read, so whether a restart is needed is not known.")
		return
	}
	class := gen.ChangeClass(change.Class)
	rec.ChangeClass = &class
	rec.Reasons = append(rec.Reasons, change.Reasons...)
}

func (n *Network) record(ctx context.Context, kind string, at time.Time, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return out(n.repo().RecordRadiusOperation(ctx, kind, at, b))
}

// ── the apply ───────────────────────────────────────────────────────────────

func (n *Network) ApplyAvailable() bool { return n.applier != nil && n.applier.Available() }

// ApplyRadius generates the tree from the stored settings and restarts the server
// through the deployment's mechanism, and records what happened.
func (n *Network) ApplyRadius(ctx context.Context, actor string) (gen.RadiusApply, error) {
	rec := gen.RadiusApply{At: n.now().UTC(), Outcome: gen.RadiusApplyOutcomeNotConfigured}
	if actor != "" {
		rec.ActorUsername = &actor
	}
	if n.applier != nil {
		settings, err := n.generatorSettings(ctx)
		if err != nil {
			return gen.RadiusApply{}, err
		}
		res, err := n.applier.Apply(ctx, settings)
		if err != nil {
			res = RadiusApplyResult{Outcome: gen.RadiusApplyOutcomeFailed,
				Detail: "The configuration could not be written; the reason is in the control plane's log."}
		}
		rec.Outcome = res.Outcome
		restarted := res.Restarted
		rec.Restarted = &restarted
		if res.Detail != "" {
			rec.Detail = &res.Detail
		}
	}
	if err := n.record(ctx, store.RadiusOperationApply, rec.At, rec); err != nil {
		return rec, err
	}
	return rec, nil
}

// RecordStartupApply records what FreeRADIUS's supervisor answered to the tree
// this process generated at start, so the console shows a start-up that failed —
// a stored EAP certificate FreeRADIUS refuses, say — instead of a stale "applied".
func (n *Network) RecordStartupApply(ctx context.Context, res RadiusApplyResult) error {
	rec := gen.RadiusApply{At: n.now().UTC(), Outcome: res.Outcome}
	restarted := res.Restarted
	rec.Restarted = &restarted
	if res.Detail != "" {
		rec.Detail = &res.Detail
	}
	return n.record(ctx, store.RadiusOperationApply, rec.At, rec)
}

// generatorSettings reads the stored generator settings, with the EAP key
// unsealed only when EAP-TLS is on.
func (n *Network) generatorSettings(ctx context.Context) (RadiusGeneratorSettings, error) {
	row, err := n.repo().Get(ctx)
	if err != nil {
		return RadiusGeneratorSettings{}, out(err)
	}
	s := RadiusGeneratorSettings{MaxServers: api.DefaultRadiusMaxServers}
	if row.RadiusMaxServers != nil {
		s.MaxServers = *row.RadiusMaxServers
	}
	// A row above the ceiling — one saved while the console allowed 256 — is held
	// to the PostgreSQL budget here, where the tree is generated: every worker is an
	// rlm_sql connection, and a stored number is not a reason to exhaust the
	// database (api.RadiusMaxServersCeiling). Migration 00054's CHECK holds the same
	// ceiling in SQL; this is the Go half, in the one function every generation
	// reads through.
	if s.MaxServers > api.RadiusMaxServersCeiling {
		s.MaxServers = api.RadiusMaxServersCeiling
	}
	if !row.EAPTLSEnabled {
		return s, nil
	}
	c, err := n.repo().Certificate(ctx, store.CertificateEAPTLS)
	if err != nil {
		return RadiusGeneratorSettings{}, out(err)
	}
	key, err := n.repo().CertificateKey(ctx, store.CertificateEAPTLS)
	if err != nil {
		return RadiusGeneratorSettings{}, networkErr(err)
	}
	s.EAP = &EAPMaterial{CertificatePEM: []byte(c.CertificatePEM), PrivateKeyPEM: key, CAPEM: []byte(deref(c.CAPEM))}
	return s, nil
}

// RadiusSettings is the stored generator settings for `cmd/foyer`'s start-up
// generation, which must write the same tree the console's apply would.
func (n *Network) RadiusSettings(ctx context.Context) (RadiusGeneratorSettings, error) {
	return n.generatorSettings(ctx)
}

func (n *Network) LastRadiusOperations(ctx context.Context) (*gen.RadiusClientSync, *gen.RadiusApply, error) {
	var sync *gen.RadiusClientSync
	var apply *gen.RadiusApply
	if b, _, err := n.repo().RadiusOperation(ctx, store.RadiusOperationSync); err != nil {
		return nil, nil, out(err)
	} else if b != nil {
		sync = &gen.RadiusClientSync{}
		if err := json.Unmarshal(b, sync); err != nil {
			return nil, nil, err
		}
	}
	if b, _, err := n.repo().RadiusOperation(ctx, store.RadiusOperationApply); err != nil {
		return nil, nil, out(err)
	} else if b != nil {
		apply = &gen.RadiusApply{}
		if err := json.Unmarshal(b, apply); err != nil {
			return nil, nil, err
		}
	}
	return sync, apply, nil
}

// ── CoA timing ──────────────────────────────────────────────────────────────

// speaksCoA is the drivers that send RFC 5176 themselves — `ForNAS`'s two.
func speaksCoA(driver string) bool {
	return driver == string(gen.NasDriverKindMikrotik) || driver == string(gen.NasDriverKindCoa)
}

func (n *Network) CoaTimings(ctx context.Context) ([]api.CoaTimingRow, error) {
	stored, err := n.repo().CoATimings(ctx)
	if err != nil {
		return nil, out(err)
	}
	var rows []api.CoaTimingRow
	cursor := ""
	for {
		page, err := n.st.RadiusClients().ListNas(ctx, store.ListOptions{Cursor: cursor, Limit: 200})
		if err != nil {
			return nil, out(err)
		}
		for _, d := range page.Items {
			if !speaksCoA(d.Driver) {
				continue
			}
			rows = append(rows, coaRow(d, stored))
		}
		if page.NextCursor == nil {
			return rows, nil
		}
		cursor = *page.NextCursor
	}
}

func coaRow(d store.Nas, stored map[string]store.CoATiming) api.CoaTimingRow {
	r := api.CoaTimingRow{NasID: d.ID, SiteID: d.SiteID, Name: d.Name, Driver: gen.NasDriverKind(d.Driver)}
	if t, ok := stored[d.RadiusClient.ID]; ok {
		r.Stored = &gen.CoaTimingValues{TimeoutMs: t.TimeoutMs, Retries: t.Retries}
	}
	return r
}

func (n *Network) SetCoaTiming(ctx context.Context, nasID string, t *gen.CoaTimingValues) (api.CoaTimingRow, error) {
	d, err := n.st.RadiusClients().GetNas(ctx, nasID)
	if err != nil {
		return api.CoaTimingRow{}, out(err)
	}
	if !speaksCoA(d.Driver) {
		return api.CoaTimingRow{}, api.ErrNoCoA
	}
	var v *store.CoATiming
	if t != nil {
		v = &store.CoATiming{TimeoutMs: t.TimeoutMs, Retries: t.Retries}
	}
	if err := n.repo().SetCoATiming(ctx, d.RadiusClient.ID, v); err != nil {
		return api.CoaTimingRow{}, out(err)
	}
	stored := map[string]store.CoATiming{}
	if v != nil {
		stored[d.RadiusClient.ID] = *v
	}
	return coaRow(*d, stored), nil
}
