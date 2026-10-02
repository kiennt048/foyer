package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

// fakeProjection stands in for store.NasProjectionRepo.
//
// The rendering is what matters here — the conflict carries a field per fact
// precisely so that something can print them — and rendering is exactly the part a
// PostgreSQL container proves nothing about.
type fakeProjection struct {
	res     store.NasSync
	syncErr error
	rows    []store.NasRow
	rowsErr error
	syncs   int
}

func (f *fakeProjection) Sync(context.Context) (store.NasSync, error) {
	f.syncs++
	if f.syncErr != nil {
		return store.NasSync{}, f.syncErr
	}
	return f.res, nil
}

func (f *fakeProjection) AllRows(context.Context) ([]store.NasRow, error) {
	return f.rows, f.rowsErr
}

func runSync(t *testing.T, repo nasProjection, snapshotPath string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := syncNas(context.Background(), &buf, repo, snapshotPath)
	return buf.String(), err
}

func TestSyncNasSaysWhatItChanged(t *testing.T) {
	repo := &fakeProjection{res: store.NasSync{
		Inserted: 2, Updated: 1, Unchanged: 4, Removed: []string{"10.0.0.42"},
		At: time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
		Rows: []store.NasRow{
			{NASName: "172.19.0.4", Shortname: "lab-ap", Type: "other", Secret: "must-not-be-printed"},
		},
		Skipped: []store.NasSkip{{
			ClientID: "rc-9",
			Address:  "10.0.0.9",
			Reason:   "no shared secret assigned; FreeRADIUS discards every packet from this address until one is",
		}},
	}}

	out, err := runSync(t, repo, filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	for _, want := range []string{"inserted   2", "updated    1", "unchanged  4", "removed    1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output does not report %q:\n%s", want, out)
		}
	}
	// A removal is named, not counted. The row is gone from the table by the
	// time an operator looks, so the run that deleted it is the only thing that can
	// say which address it was -- and the classification below is about exactly
	// that address.
	if !strings.Contains(out, "removed from the projection") || !strings.Contains(out, "10.0.0.42") {
		t.Fatalf("the removed client is not named:\n%s", out)
	}
	// A skipped client is not a warning to swallow: FreeRADIUS discards every
	// packet from that address and nothing else in the product says so.
	if !strings.Contains(out, "NOT projected") || !strings.Contains(out, "10.0.0.9") || !strings.Contains(out, "rc-9") {
		t.Fatalf("the skipped client is not surfaced:\n%s", out)
	}
	if strings.Contains(out, "must-not-be-printed") {
		t.Fatalf("a shared secret was printed:\n%s", out)
	}
}

// An operator who runs a sync and is told nothing has been given a reason to run it
// again.
func TestSyncNasSaysWhenNothingChanged(t *testing.T) {
	repo := &fakeProjection{res: store.NasSync{Unchanged: 3, At: time.Now().UTC()}}
	out, err := runSync(t, repo, filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !strings.Contains(out, "nothing changed") {
		t.Fatalf("a run that changed nothing does not say so:\n%s", out)
	}
}

// The refusal, which is the half that matters.
//
// Only one of two tenants can ever be projected onto one address, because
// FreeRADIUS matches a packet's source address against nasname and its client list
// has no tenant in it. A silent last-writer-wins points one tenant's access points
// at another tenant's shared secret — a cross-tenant authentication bypass with an
// audit trail that looks entirely normal.
func TestACrossTenantConflictIsRefusedAndNamesBothSides(t *testing.T) {
	repo := &fakeProjection{syncErr: &store.NasAddressConflict{
		NASName:       "192.168.1.1",
		Tenant:        "tenant-a",
		OtherTenant:   "tenant-b",
		ClientID:      "rc-mine",
		OtherClientID: "rc-theirs",
	}}

	out, err := runSync(t, repo, filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("a cross-tenant conflict exited zero; a script would report success for a site that is off the air")
	}
	if !strings.Contains(err.Error(), "192.168.1.1") {
		t.Fatalf("the error does not name the address: %v", err)
	}
	for _, want := range []string{"192.168.1.1", "tenant-a", "tenant-b", "rc-mine", "rc-theirs", "REFUSED"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the refusal does not render %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Nothing was written") {
		t.Fatalf("the refusal does not say the whole sync was refused:\n%s", out)
	}
}

// The holding row may have been seeded by hand, in which case there is no client id
// to print. The typed error says so in words rather than leaving an empty field, and
// so must the rendering.
func TestAConflictWithAHandSeededHolderSaysSoInWords(t *testing.T) {
	repo := &fakeProjection{syncErr: &store.NasAddressConflict{
		NASName:     "192.168.1.1",
		Tenant:      "tenant-a",
		OtherTenant: "tenant-b",
		ClientID:    "rc-mine",
	}}

	out, err := runSync(t, repo, filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("a cross-tenant conflict exited zero")
	}
	if !strings.Contains(out, "seeded by hand") {
		t.Fatalf("a hand-seeded holder is not named:\n%s", out)
	}
	if strings.Contains(out, "client \n") || strings.Contains(out, "client ,") {
		t.Fatalf("an empty client id was rendered:\n%s", out)
	}
}

// A conflict must reach the shell as a non-zero status, not only as text. dispatch
// is the one place that mapping lives.
func TestAFailingCommandExitsNonZero(t *testing.T) {
	const name = "test-only-failing-command"
	Register(&Command{
		Name:    name,
		Summary: "fails, so that the error-to-exit-status mapping is asserted rather than assumed",
		Run:     func([]string) error { return errors.New("refused") },
	})
	t.Cleanup(func() { delete(commands, name) })

	if code := dispatch([]string{"foyerctl", name}); code == 0 {
		t.Fatal("a command that returned an error exited zero")
	}
}

// The sync and the snapshot are two halves of one answer: what changed, and whether
// FreeRADIUS has to be restarted for it to take effect.
func TestSyncNasClassifiesAgainstTheStartupSnapshot(t *testing.T) {
	cfg := raddb.DefaultConfig()
	cfg.PostgresPassword = testPGPassword
	loaded := []raddb.ClientRow{{NASName: "172.19.0.4", Shortname: "lab-ap", Type: "other", Secret: "first-secret-value"}}

	t.Run("a rotation on a loaded client is a restart", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), raddb.SnapshotName)
		if err := raddb.WriteStartupSnapshot(path, raddb.Snapshot{Config: cfg, Clients: loaded}); err != nil {
			t.Fatal(err)
		}
		repo := &fakeProjection{
			res:  store.NasSync{Updated: 1, At: time.Now().UTC()},
			rows: []store.NasRow{{NASName: "172.19.0.4", Shortname: "lab-ap", Type: "other", Secret: "second-secret-value"}},
		}
		out, err := runSync(t, repo, path)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if !strings.Contains(out, string(raddb.ClassRestart)) || !strings.Contains(out, "does not re-read it") {
			t.Fatalf("a rotation on a startup-loaded client was not reported as a restart:\n%s", out)
		}
	})

	t.Run("a new client inside a dynamic network is sql-only", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), raddb.SnapshotName)
		if err := raddb.WriteStartupSnapshot(path, raddb.Snapshot{Config: cfg, Clients: loaded}); err != nil {
			t.Fatal(err)
		}
		repo := &fakeProjection{
			res: store.NasSync{Inserted: 1, At: time.Now().UTC()},
			rows: []store.NasRow{
				{NASName: "172.19.0.4", Shortname: "lab-ap", Type: "other", Secret: "first-secret-value"},
				{NASName: "10.0.0.7", Shortname: "new-ap", Type: "other", Secret: "third-secret-value"},
			},
		}
		out, err := runSync(t, repo, path)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if !strings.Contains(out, string(raddb.ClassSQLOnly)) || !strings.Contains(out, "No restart") {
			t.Fatalf("a discoverable client was not reported as sql-only:\n%s", out)
		}
		if !strings.Contains(out, "effective within 5m0s") {
			t.Fatalf("the delay a cached definition costs is not stated:\n%s", out)
		}
	})

	// ── a client written and removed between restarts ───────────────────────
	//
	// The classifier was under-informed rather than wrong, and this is the exact
	// shape of what it could not see. A client is written after the last restart
	// and deleted before the next one, so it is in **neither** set `Classify`
	// compares — not the startup snapshot, not the table as it now stands — and the
	// answer is `none`: *the deletion took effect*. Measured on 3.2.10 it has not.
	// The running server discovered that client from the table and goes on
	// answering it from the definition it cached.
	//
	// A `store.NasSync.Removed` that was a bare `int` would be precisely the shape
	// that cannot be handed on. Both halves are asserted here, because a test that
	// only checked the restart half would pass for a `Classify` that answered
	// `restart` for everything.
	t.Run("a row removed between restarts is a restart, and only when it is named", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), raddb.SnapshotName)
		if err := raddb.WriteStartupSnapshot(path, raddb.Snapshot{Config: cfg, Clients: loaded}); err != nil {
			t.Fatal(err)
		}
		// The table after the sync holds only the client the server loaded. The
		// removed one is invisible to both sets by construction.
		table := []store.NasRow{{NASName: "172.19.0.4", Shortname: "lab-ap", Type: "other", Secret: "first-secret-value"}}

		named, err := runSync(t, &fakeProjection{
			res:  store.NasSync{Removed: []string{"10.0.0.7"}, At: time.Now().UTC()},
			rows: table,
		}, path)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if !strings.Contains(named, string(raddb.ClassRestart)) {
			t.Fatalf("a revocation the running server discovered was not reported as a restart:\n%s", named)
		}
		if !strings.Contains(named, "10.0.0.7") {
			t.Fatalf("the classification does not name the removed client:\n%s", named)
		}

		// And the other half: the same sync reporting no removals must still
		// answer `none`. Without it "restart" could be coming from anywhere.
		silent, err := runSync(t, &fakeProjection{
			res:  store.NasSync{Unchanged: 1, At: time.Now().UTC()},
			rows: table,
		}, path)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if strings.Contains(silent, string(raddb.ClassRestart)) {
			t.Fatalf("a sync that removed nothing was reported as needing a restart:\n%s", silent)
		}
	})

	t.Run("a missing snapshot is not knowing, not nothing loaded", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), raddb.SnapshotName)
		repo := &fakeProjection{
			res:  store.NasSync{Updated: 1, At: time.Now().UTC()},
			rows: []store.NasRow{{NASName: "172.19.0.4", Shortname: "lab-ap", Type: "other", Secret: "second-secret-value"}},
		}
		out, err := runSync(t, repo, path)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if !strings.Contains(out, "no startup snapshot") || !strings.Contains(out, "is not known") {
			t.Fatalf("a missing snapshot did not read as not knowing:\n%s", out)
		}
		// The wrong answer this avoids: treating the absence as an empty client
		// list classifies the rotation as sql-only and tells the operator no
		// restart is needed while the server refuses every packet.
		if strings.Contains(out, string(raddb.ClassSQLOnly)) {
			t.Fatalf("a missing snapshot produced a classification anyway:\n%s", out)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the classification wrote a snapshot: %v", err)
		}
	})
}
