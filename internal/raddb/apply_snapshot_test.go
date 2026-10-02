package raddb

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Gate 4 records what the restarted server will load, before it loads it.
//
// The ordering is the assertion, not a detail: a snapshot written after the
// restart records rows the server may have discovered rather than loaded, and that
// distinction is the entire difference between `sql-only` and `restart`. The fake
// Restart below reads the file, so a snapshot written afterwards fails here rather
// than in production six weeks later.
func TestGateFourWritesTheStartupSnapshotBeforeRestarting(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)
	a.SnapshotPath = SnapshotPathFor(a.Root)
	a.StartupClients = func(context.Context) ([]ClientRow, error) {
		return []ClientRow{
			{NASName: "10.0.0.5", Shortname: "ap-lobby", Type: "other", Secret: testSecretA},
		}, nil
	}

	var seenAtRestart int
	var restartErr error
	a.Restart = func(context.Context) error {
		f, err := ReadStartupSnapshot(a.SnapshotPath)
		if err != nil {
			restartErr = err
			return nil
		}
		seenAtRestart = len(f.Redacted.Clients)
		return nil
	}

	rep, err := a.Apply(context.Background(), testConfig())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if restartErr != nil {
		t.Fatalf("the snapshot was not readable when the server was restarted: %v", restartErr)
	}
	if seenAtRestart != 1 {
		t.Fatalf("the restart saw %d clients in the snapshot, want 1", seenAtRestart)
	}

	gate4 := rep.Results[len(rep.Results)-1]
	if !strings.Contains(gate4.Detail, "1 clients recorded") {
		t.Errorf("gate 4 does not report the snapshot it wrote: %q", gate4.Detail)
	}
}

// No snapshot path is allowed, and it has to *say* it is a guess — the same
// precedent the readiness probe already sets when Ready is nil. A silent absence
// here is an operator being shown a classification nobody told them was invented.
func TestGateFourSaysSoWhenNoSnapshotIsConfigured(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)

	rep, err := a.Apply(context.Background(), testConfig())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	gate4 := rep.Results[len(rep.Results)-1]
	if !strings.Contains(gate4.Detail, "guess") {
		t.Errorf("gate 4 passed with no snapshot and did not say the next classification is a guess: %q", gate4.Detail)
	}
	if _, err := os.Stat(SnapshotPathFor(a.Root)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a snapshot was written when none was configured: %v", err)
	}
}

// A configured path with no source of rows is an error, not an empty file.
//
// The two claims are opposites. An empty snapshot says the server loaded no
// clients, so every client in the table classifies as new and discoverable — which
// is `sql-only` for changes that need a restart. "We did not look" has to be the
// absence of the file.
func TestGateFourRefusesToWriteASnapshotItCannotFill(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)
	a.SnapshotPath = SnapshotPathFor(a.Root)
	a.StartupClients = nil

	_, err := a.Apply(context.Background(), testConfig())
	if err == nil {
		t.Fatal("Apply succeeded with a snapshot path and no way to read the client table")
	}
	if _, statErr := os.Stat(a.SnapshotPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("an empty snapshot was written anyway: %v", statErr)
	}
}

// A cutover that rolls back leaves no snapshot, because there is no server it
// would describe.
//
// This is the one direction the snapshot mechanism exists to avoid. The file is
// written before the restart -- deliberately, and
// WriteStartupSnapshot argues for that ordering -- so a restart that then fails
// leaves a record claiming the running server loaded this client list at startup,
// while the running server is still on the previous configuration and never loaded
// any of it. A client registered just before that failed apply reads back as
// loaded-and-unchanged, Classify answers `none`, and the operator is told there is
// nothing to apply about a NAS whose every packet the server discards.
//
// Both halves are asserted. The absence of the file is the fix; the sentence in the
// gate's detail is what tells an operator reading the report why the next
// classification is going to say it does not know.
func TestARolledBackCutoverLeavesNoSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(a *Applier)
	}{
		{
			name: "the restart fails",
			arrange: func(a *Applier) {
				a.Restart = func(context.Context) error { return errors.New("radiusd would not start") }
			},
		},
		{
			name: "the live server does not answer after the cutover",
			arrange: func(a *Applier) {
				a.Restart = func(context.Context) error { return nil }
				a.VerifyLive = func(context.Context) error { return errors.New("no reply on 1812") }
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{version: goodVersion}
			a := testApplier(t, r)
			a.SnapshotPath = SnapshotPathFor(a.Root)
			a.StartupClients = func(context.Context) ([]ClientRow, error) {
				return []ClientRow{
					{NASName: "10.0.0.5", Shortname: "ap-lobby", Type: "other", Secret: testSecretA},
				}, nil
			}
			tc.arrange(a)

			rep, err := a.Apply(context.Background(), testConfig())
			if err == nil {
				t.Fatal("Apply succeeded; this test is about the rollback path and did not reach it")
			}
			if !rep.RolledBack {
				t.Fatal("the report does not say it rolled back, so the snapshot's absence below would prove nothing")
			}

			if _, statErr := os.Stat(a.SnapshotPath); !errors.Is(statErr, os.ErrNotExist) {
				f, readErr := ReadStartupSnapshot(a.SnapshotPath)
				t.Fatalf("a snapshot survived a rolled-back cutover, so it describes a server that never ran: "+
					"stat %v, read %v, %d clients recorded. The next Classify compares against it and answers "+
					"`none` for a client the running server has never seen.",
					statErr, readErr, len(f.Redacted.Clients))
			}

			gate4 := rep.Results[len(rep.Results)-1]
			if !strings.Contains(gate4.Detail, "startup snapshot discarded") {
				t.Errorf("gate 4 does not say the snapshot was discarded, so nothing tells the operator why the "+
					"next classification will say it does not know: %q", gate4.Detail)
			}
		})
	}
}

// And the sentence is not emitted when there was nothing to discard. A report that
// says it discarded a snapshot it never wrote is a report an operator stops
// reading.
func TestNoSnapshotConfiguredMeansNothingIsDiscardedOnRollback(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)
	a.Restart = func(context.Context) error { return errors.New("radiusd would not start") }

	rep, err := a.Apply(context.Background(), testConfig())
	if err == nil {
		t.Fatal("Apply succeeded; this test is about the rollback path")
	}
	gate4 := rep.Results[len(rep.Results)-1]
	if strings.Contains(gate4.Detail, "snapshot discarded") {
		t.Errorf("gate 4 claims to have discarded a snapshot that was never configured: %q", gate4.Detail)
	}
}
