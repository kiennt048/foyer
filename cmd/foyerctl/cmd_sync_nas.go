package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

func init() {
	Register(&Command{
		Name:    "sync-nas",
		Usage:   "[--db <url>] [--root <dir>] [--tenant <uuid>]",
		Summary: "Write the nas projection FreeRADIUS reads from radius_clients",
		Run:     runSyncNas,
	})
}

// `foyerctl sync-nas` is a caller for the RADIUS-client projection.
//
// **It is not how an operator syncs.** An operator never runs a CLI, and a RADIUS
// client added in the console must not reach FreeRADIUS only when somebody runs
// this. The control plane runs the same sync itself after every RADIUS-client
// write, audits it
// and shows its state in the console (`internal/apistore/network.go`,
// `syncRadiusClients`). This command stays as the recovery tool for a support
// session, and both answer the restart question through `raddb.ClassifySync`.
//
// `radius_clients` is the system of record — what an operator edits, what carries
// the tenant, the driver and the capabilities. `nas` is its projection into the six
// columns rlm_sql reads, and FreeRADIUS loads that table with `read_clients = yes`.
// Nothing outside `internal/store`'s own tests wrote it, so a freshly migrated
// database had an empty client table and the server discarded every packet as
// coming from an unknown client. Measured on 3.2.10: `Ignoring request to auth
// address * port 1812 bound to server default from unknown client 172.19.0.4`.
//
// What it deliberately does not do is decide whether FreeRADIUS needs restarting.
// That is not derivable from the table: it depends on whether the running server
// loaded a given client at startup, which is a fact about the server. The startup
// snapshot records it and this command reads it — see classifyAgainstSnapshot.
func runSyncNas(args []string) error {
	fs := flag.NewFlagSet("sync-nas", flag.ExitOnError)
	dbDefault, err := envOrFileDefault("FOYER_DATABASE_URL")
	if err != nil {
		return err
	}
	dbURL := fs.String("db", dbDefault, "PostgreSQL connection URL")
	tenant := fs.String("tenant", "", "tenant to sync; defaults to the single tenant v1 ships with")
	root := fs.String("root", "/var/lib/foyer", "directory holding the raddb symlink and the startup snapshot")
	snapshot := fs.String("snapshot", "", "startup snapshot to classify against; defaults to "+raddb.SnapshotName+" inside --root")
	timeout := fs.Duration("timeout", 30*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbURL == "" {
		return errors.New("--db or FOYER_DATABASE_URL is required")
	}
	if *snapshot == "" {
		*snapshot = raddb.SnapshotPathFor(*root)
	}

	// The keyring is required rather than optional here. Every projected row
	// carries a shared secret that has to be unsealed to be written, and a
	// projection written without them is a client table that authenticates
	// nothing. `internal/store` returns ErrNoKeyring by name; this only makes the
	// refusal arrive with the variable's name attached.
	keys, err := store.KeyringFromEnv()
	if err != nil {
		return fmt.Errorf("reading FOYER_KEK: %w", err)
	}
	if keys == nil {
		return errors.New("FOYER_KEK is not set: every projected client carries a shared secret that has to be unsealed, and a projection written without them is a client table that authenticates nothing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	st, err := store.Open(ctx, store.Config{URL: *dbURL, TenantID: *tenant, Keys: keys, MaxConns: 2})
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer st.Close()

	return syncNas(ctx, os.Stdout, st.NasProjection(), *snapshot)
}

// nasProjection is the part of store.NasProjectionRepo this command uses, named so
// that the rendering below — which is the whole point of the typed conflict — can
// be exercised without a PostgreSQL container.
type nasProjection interface {
	Sync(ctx context.Context) (store.NasSync, error)
	AllRows(ctx context.Context) ([]store.NasRow, error)
}

// syncNas runs one sync and says what it did.
//
// An operator who runs a sync and is told nothing has been given a reason to run it
// again, so every count is printed including the zeroes, and a run that changed
// nothing says so in those words.
func syncNas(ctx context.Context, w io.Writer, repo nasProjection, snapshotPath string) error {
	res, err := repo.Sync(ctx)
	if err != nil {
		var conflict *store.NasAddressConflict
		if errors.As(err, &conflict) {
			renderConflict(w, conflict)
			// Non-zero, because the estate is not in the state the operator asked
			// for: this tenant's access points are not projected and FreeRADIUS
			// discards their packets. A zero exit here would be a script reporting
			// success for a site that is off the air.
			return fmt.Errorf("cross-tenant address conflict on %s: nothing was written", conflict.NASName)
		}
		return err
	}

	renderSync(w, res)
	// **The removed rows travel with the classification.** Without them
	// `Classify` compares the startup snapshot against the table as it now stands,
	// and a row that was written after the last restart and deleted before the next
	// one is in neither set — so it produces no reason and the answer is `none`:
	// the operator is told a revocation took effect. On 3.2.10 it has not.
	//
	// Supplied even when the slice is empty, and that is the whole point of the
	// option being variadic: *supplied and empty* says **this sync removed
	// nothing**, which is a fact this caller has and `foyerctl doctor` does not.
	classifyAgainstSnapshot(ctx, w, repo, snapshotPath, res.Removed)
	return nil
}

// renderConflict prints every fact the typed error carries.
//
// The type has a field per fact precisely so this can render rather than log, and
// the reason it must render is not tidiness. `nas.nasname` is UNIQUE full stop —
// FreeRADIUS matches a packet's source address against it and its client list has
// no tenant in it — so of two tenants holding 192.168.1.1, which on private ranges
// is the common case rather than an edge one, only one can ever be projected. A
// silent last-writer-wins points one tenant's access points at another tenant's
// shared secret: a cross-tenant authentication bypass arriving with an audit trail
// that looks entirely normal.
//
// "Already taken" without saying by whom sends an operator to the wrong database,
// so both sides are named; and where the holding row was seeded by hand there is no
// client id to print, which is said in words rather than left as an empty field.
func renderConflict(w io.Writer, c *store.NasAddressConflict) {
	held := "a row seeded by hand, with no radius_clients row behind it"
	if c.OtherClientID != "" {
		held = "client " + c.OtherClientID
	}
	holder := c.OtherTenant
	if holder == "" {
		holder = "no tenant (a concurrent writer took the row)"
	}

	fmt.Fprintf(w, "REFUSED  %s is already projected by another tenant. Nothing was written.\n\n", c.NASName)
	fmt.Fprintf(w, "  address         %s\n", c.NASName)
	fmt.Fprintf(w, "  refused         tenant %s, client %s\n", c.Tenant, c.ClientID)
	fmt.Fprintf(w, "  held by         tenant %s, %s\n\n", holder, held)
	fmt.Fprint(w, "FreeRADIUS matches a packet's source address against nasname and its client\n"+
		"list has one flat namespace with no tenant in it, so only one of the two can\n"+
		"ever be projected. Taking the row over would point this tenant's access\n"+
		"points at the other tenant's shared secret.\n\n"+
		"Change one of the two addresses, or disable the client that no longer sits\n"+
		"there. Nothing in this sync was applied: the whole of it is one transaction.\n")
}

func renderSync(w io.Writer, res store.NasSync) {
	fmt.Fprintf(w, "synced the nas projection at %s\n\n", res.At.Format(time.RFC3339))
	fmt.Fprintf(w, "  inserted   %d\n", res.Inserted)
	fmt.Fprintf(w, "  updated    %d\n", res.Updated)
	fmt.Fprintf(w, "  unchanged  %d\n", res.Unchanged)
	fmt.Fprintf(w, "  removed    %d\n", len(res.Removed))

	if res.Inserted == 0 && res.Updated == 0 && len(res.Removed) == 0 {
		fmt.Fprintf(w, "\nnothing changed: the projection was already in step with radius_clients.\n")
	}

	// Named, not counted. A removal is the change an operator most needs to be able
	// to check, because the row is gone from the table by the time they look — and
	// the classification below is about these addresses specifically.
	if len(res.Removed) > 0 {
		fmt.Fprintf(w, "\n%d client(s) removed from the projection:\n", len(res.Removed))
		for _, name := range res.Removed {
			fmt.Fprintf(w, "  %s\n", name)
		}
	}

	if len(res.Rows) > 0 {
		fmt.Fprintf(w, "\n%d client(s) projected for this tenant:\n", len(res.Rows))
		for _, r := range res.Rows {
			// Never the secret. `nas.secret` is the one declared cleartext copy
			// of a shared secret in the deployment and a terminal is not a second
			// place for it to exist.
			fmt.Fprintf(w, "  %-20s %s\n", r.NASName, r.Shortname)
		}
	}

	if len(res.Skipped) > 0 {
		// Loud, because a skipped client is not a warning to swallow: FreeRADIUS
		// discards every packet from that address, the guest sees a network that
		// does not work, and nothing else in the product says so.
		fmt.Fprintf(w, "\n%d client(s) NOT projected:\n", len(res.Skipped))
		for _, s := range res.Skipped {
			fmt.Fprintf(w, "  %-20s client %s\n      %s\n", s.Address, s.ClientID, s.Reason)
		}
	}
}

// classifyAgainstSnapshot answers the question a sync leaves open: does FreeRADIUS
// have to be restarted for this to take effect?
//
// **The answer is `raddb.ClassifySync`'s**, which is also what the console's
// automatic, in-process sync uses.
// This function only reads the whole table and renders; the comparison — redacted
// on both sides, with the removed rows supplied — lives in one place.
//
// It compares the client table against the startup snapshot and nothing else — the
// generated tree is not compared, because this command does not generate one, and
// pretending otherwise would report `restart` for every configuration value that
// happens to differ from the flags of whoever last ran apply-raddb.
//
// A failure to classify is reported and is not an error: the sync happened, and
// telling an operator their write failed because a file could not be read would be
// a worse lie than not answering.
func classifyAgainstSnapshot(ctx context.Context, w io.Writer, repo nasProjection, path string, removed []string) {
	rows, err := repo.AllRows(ctx)
	if err != nil {
		fmt.Fprintf(w, "\nthe client table could not be re-read, so this run cannot say whether a\nrestart is needed: %v\n", err)
		return
	}
	// The whole table and no tenant scope, because that is what the server loads.
	change, f, err := raddb.ClassifySync(path, clientRows(rows), removed)
	if errors.Is(err, raddb.ErrNoStartupSnapshot) {
		fmt.Fprintf(w, "\nno startup snapshot at %s: nothing recorded what the running server\n"+
			"loaded, so whether these changes need a restart is not known. A snapshot is\n"+
			"written by `foyerctl apply-raddb --restart-command …` at cutover.\n", path)
		return
	}
	if err != nil {
		fmt.Fprintf(w, "\nthe startup snapshot could not be read, so this run cannot say whether a\nrestart is needed: %v\n", err)
		return
	}

	fmt.Fprintf(w, "\nagainst the startup snapshot taken %s: %s\n", f.WrittenAt.Format(time.RFC3339), change)
	for _, r := range change.Reasons {
		fmt.Fprintf(w, "  %s\n", r)
	}
	switch change.Class {
	case raddb.ClassRestart:
		fmt.Fprint(w, "\nRestarting drops every in-flight EAP conversation and the in-memory TLS\n"+
			"session cache with it, so every 802.1X supplicant on site re-authenticates.\n"+
			"SIGHUP is not an escape: it does not reliably reload clients, listeners or\n"+
			"virtual servers in 3.2.x.\n")
	case raddb.ClassSQLOnly:
		fmt.Fprint(w, "\nNo restart. A client the running server has not seen is discovered from the\n"+
			"client table; the delay above is how long a definition it has already cached\n"+
			"keeps the value it cached.\n")
	}
}
