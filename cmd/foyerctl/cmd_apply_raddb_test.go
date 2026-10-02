package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/raddb"
)

// The probe client's shared secret for these tests. It is a value, not a default:
// nothing in the repository ships one, and `apply-raddb` refuses to run when the
// environment variable naming it is empty.
const testProbeSecret = "8Kj2wQ7fLp1vN4xR"

// testPGPassword stands in for the value an installer generates at first boot.
// The generator refuses an empty one: `read_clients = yes` means FreeRADIUS opens
// the database itself, and a blank password there is a server that loads no
// clients and says nothing about why.
const testPGPassword = "pgpw-not-a-default"

// fakeRunner answers the four gates without a FreeRADIUS install, and watches the
// one ordering this file exists to prove.
type fakeRunner struct {
	ran []string

	// restartCommand is the command whose execution stands for radiusd starting.
	restartCommand string
	// snapshotPath is looked at the moment that command runs.
	snapshotPath string

	// sawSnapshotAtRestart records whether the startup snapshot was on disk when
	// the server was restarted. That is the whole defect: a snapshot written
	// after the server starts describes rows it may have discovered rather than
	// loaded, and the two are the difference between `sql-only` and `restart`.
	sawSnapshotAtRestart bool
	restarts             int
	clientsAtRestart     int

	failOn string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.ran = append(f.ran, line)

	if f.restartCommand != "" && strings.Contains(line, f.restartCommand) {
		f.restarts++
		if snap, err := raddb.ReadStartupSnapshot(f.snapshotPath); err == nil {
			f.sawSnapshotAtRestart = true
			f.clientsAtRestart = len(snap.Redacted.Clients)
		}
	}
	if f.failOn != "" && strings.Contains(line, f.failOn) {
		return []byte("boom"), errors.New("command failed")
	}
	if len(args) > 0 && args[0] == "-v" {
		return []byte("radiusd: FreeRADIUS Version 3.2.10, for host aarch64-unknown-linux-gnu"), nil
	}
	return []byte("ok"), nil
}

func (f *fakeRunner) Start(_ context.Context, name string, args ...string) (raddb.Process, error) {
	f.ran = append(f.ran, "start: "+strings.Join(append([]string{name}, args...), " "))
	return fakeProcess{}, nil
}

type fakeProcess struct{}

func (fakeProcess) Stop(context.Context) error { return nil }

func (f *fakeRunner) sawArg(substr string) bool {
	for _, line := range f.ran {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// probeDir writes the one radclient case the gates need. Status-Server runs no
// policy at all, so a reply means exactly two things: this source address is a
// known client, and this shared secret is the right one.
func probeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := "#packet-type: status\nMessage-Authenticator = 0x00\nFreeRADIUS-Statistics-Type = 1\n"
	if err := os.WriteFile(filepath.Join(dir, statusServerProbe+".request"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func loadedClients(secret string) []raddb.ClientRow {
	return []raddb.ClientRow{{
		NASName:   "172.19.0.4",
		Shortname: "lab-ap",
		Type:      "other",
		Secret:    secret,
	}}
}

func planFor(t *testing.T, args ...string) *applyPlan {
	t.Helper()
	t.Setenv("FOYER_PROBE_SECRET", testProbeSecret)
	t.Setenv("FOYER_POSTGRES_PASSWORD", testPGPassword)
	t.Setenv("FOYER_DATABASE_URL", "")
	plan, generateOnly, err := parseApplyRaddb(args)
	if err != nil {
		t.Fatalf("parseApplyRaddb: %v", err)
	}
	if generateOnly != "" {
		t.Fatalf("unexpected generate-only run: %q", generateOnly)
	}
	return plan
}

// The snapshot is written before the server restarts, and the CLI is what wires it.
//
// Before this, `cmd_apply_raddb.go` built its Applier with no SnapshotPath and no
// StartupClients, so gate 4 reported "no snapshot path configured … so it is a
// guess" on every run and no snapshot was ever written outside a unit test in
// `internal/raddb`.
func TestTheStartupSnapshotIsWrittenBeforeTheServerRestarts(t *testing.T) {
	root := t.TempDir()
	restart := "systemctl restart freeradius"
	plan := planFor(t, "--root", root, "--probe-dir", probeDir(t), "--restart-command", restart)

	if plan.snapshotPath != raddb.SnapshotPathFor(root) {
		t.Fatalf("snapshot path = %q, want %q", plan.snapshotPath, raddb.SnapshotPathFor(root))
	}

	runner := &fakeRunner{restartCommand: restart, snapshotPath: plan.snapshotPath}
	a := plan.applier(runner, func(context.Context) ([]raddb.ClientRow, error) {
		return loadedClients("first-secret-value"), nil
	})

	rep, err := a.Apply(context.Background(), plan.cfg)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if failed := rep.Failed(); failed != nil {
		t.Fatalf("gate %d failed: %v", failed.Gate.Number, failed.Err)
	}

	if runner.restarts != 1 {
		t.Fatalf("restarts = %d, want 1; without one the symlink moves and the running server keeps its old tree", runner.restarts)
	}
	if !runner.sawSnapshotAtRestart {
		t.Fatal("the startup snapshot did not exist when the server was restarted; a snapshot taken after the start records rows the server may have discovered rather than loaded")
	}
	if runner.clientsAtRestart != 1 {
		t.Fatalf("snapshot held %d clients at restart, want 1", runner.clientsAtRestart)
	}

	cutover := rep.Results[len(rep.Results)-1]
	if !strings.Contains(cutover.Detail, "startup snapshot: 1 clients recorded") {
		t.Fatalf("gate 4 detail = %q, want it to report the snapshot", cutover.Detail)
	}

	// The live server is asked whether it answers, on the live port, after the
	// restart. Without that, gate 4 reports a successful cutover for a server
	// that died on startup and the rollback path is unreachable.
	if !runner.sawArg("127.0.0.1:1812") {
		t.Fatalf("no probe was sent at the live server after the cutover: %v", runner.ran)
	}

	// The secret never reaches the file. `nas.secret` is the one declared
	// cleartext copy of a shared secret in the deployment, and a JSON file beside
	// the configuration tree would be a second.
	body, err := os.ReadFile(plan.snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "first-secret-value") {
		t.Fatal("the startup snapshot contains a shared secret in cleartext")
	}
}

// The missing-snapshot defect, both halves in one test.
//
// A secret rotated on a client the running server loaded at startup needs a
// restart: measured on 3.2.10, the running server refuses the new secret with
// "invalid Message-Authenticator! (Shared secret is incorrect.) (from client
// lab-ap)" while still answering `Access-Accept` on the old one, in the same
// minute. With no snapshot there is nothing to know that from, the client reads as
// new, and a new client inside a dynamic network classifies `sql-only` — the
// operator is told a rotation needs no restart while the server refuses every
// packet.
func TestWithoutASnapshotARotationLooksLikeSQLOnlyAndWithOneItIsRestart(t *testing.T) {
	rotated := loadedClients("second-secret-value")

	// Half one: the state this command was in. No snapshot, so the baseline is
	// empty, and an empty baseline is not "we do not know" — it is "the server
	// loaded nothing".
	root := t.TempDir()
	plan := planFor(t, "--root", root, "--probe-dir", probeDir(t), "--no-snapshot")
	if plan.snapshotPath != "" {
		t.Fatal("--no-snapshot still configured a snapshot path")
	}
	runner := &fakeRunner{}
	a := plan.applier(runner, func(context.Context) ([]raddb.ClientRow, error) {
		return loadedClients("first-secret-value"), nil
	})
	if _, err := a.Apply(context.Background(), plan.cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(raddb.SnapshotPathFor(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a snapshot was written with --no-snapshot: %v", err)
	}
	guess := raddb.Classify(
		raddb.Snapshot{Config: plan.cfg},
		raddb.Snapshot{Config: plan.cfg, Clients: rotated},
	)
	if guess.Class != raddb.ClassSQLOnly {
		t.Fatalf("with no baseline the rotation classified %s, want %s; this half carries the defect and must keep reproducing it",
			guess.Class, raddb.ClassSQLOnly)
	}

	// Half two: the snapshot this command now writes, read back the way
	// `sync-nas` reads it.
	root2 := t.TempDir()
	restart := "systemctl restart freeradius"
	plan2 := planFor(t, "--root", root2, "--probe-dir", probeDir(t), "--restart-command", restart)
	runner2 := &fakeRunner{restartCommand: restart, snapshotPath: plan2.snapshotPath}
	a2 := plan2.applier(runner2, func(context.Context) ([]raddb.ClientRow, error) {
		return loadedClients("first-secret-value"), nil
	})
	if _, err := a2.Apply(context.Background(), plan2.cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}

	f, err := raddb.ReadStartupSnapshot(plan2.snapshotPath)
	if err != nil {
		t.Fatalf("reading the snapshot the CLI wrote: %v", err)
	}
	answer := f.Classify(raddb.Snapshot{Config: plan2.cfg, Clients: rotated})
	if answer.Class != raddb.ClassRestart {
		t.Fatalf("with the snapshot the rotation classified %s, want %s: %v", answer.Class, raddb.ClassRestart, answer.Reasons)
	}
	if len(answer.Reasons) == 0 || !strings.Contains(answer.Reasons[0], "does not re-read it") {
		t.Fatalf("reasons = %v, want the one naming the startup load", answer.Reasons)
	}
}

// No restart, no snapshot — and the operator is told why rather than left to
// notice a missing file.
func TestNoRestartCommandWritesNoSnapshotAndSaysSo(t *testing.T) {
	root := t.TempDir()
	plan := planFor(t, "--root", root, "--probe-dir", probeDir(t))

	if plan.snapshotPath != "" {
		t.Fatalf("snapshot path = %q with nothing to restart the server; a snapshot written before a restart that never happens claims a client list the running server did not load", plan.snapshotPath)
	}
	notes := strings.Join(plan.notes(), "\n")
	if !strings.Contains(notes, "no --restart-command") || !strings.Contains(notes, "sql-only") {
		t.Fatalf("notes do not explain the missing snapshot: %q", notes)
	}
}

// A dry run may write a snapshot, because gate 4's rollback then removes it. What
// must not survive is a file describing a server that never ran.
func TestDryRunLeavesNoSnapshotBehind(t *testing.T) {
	root := t.TempDir()
	plan := planFor(t, "--root", root, "--probe-dir", probeDir(t), "--dry-run")
	if plan.snapshotPath == "" {
		t.Fatal("a dry run configured no snapshot, so it exercises nothing about the path it is meant to rehearse")
	}

	read := 0
	a := plan.applier(&fakeRunner{}, func(context.Context) ([]raddb.ClientRow, error) {
		read++
		return loadedClients("first-secret-value"), nil
	})
	rep, err := a.Apply(context.Background(), plan.cfg)
	if err == nil {
		t.Fatal("a dry run reached the cutover")
	}
	if read != 1 {
		t.Fatalf("the client table was read %d times, want 1", read)
	}
	if failed := rep.Failed(); failed == nil || failed.Gate.Number != 4 {
		t.Fatalf("want gate 4 to be the one that stopped, got %+v", rep.Failed())
	}
	if _, err := os.Stat(plan.snapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the dry run left a snapshot behind, which claims a client list the running server never loaded: %v", err)
	}
}

// The snapshot's client table comes from the same coordinates the generated tree
// uses, so a deployment does not configure two databases that can disagree.
func TestTheClientTableDefaultsToTheGeneratedTreesDatabase(t *testing.T) {
	plan := planFor(t, "--root", t.TempDir(), "--probe-dir", probeDir(t),
		"--restart-command", "true", "--pg-host", "db.example", "--pg-port", "6543", "--pg-db", "foyer2", "--pg-user", "foyer3")

	want := "postgres://foyer3:" + testPGPassword + "@db.example:6543/foyer2"
	if plan.dbURL != want {
		t.Fatalf("dbURL = %q, want %q", plan.dbURL, want)
	}
}
