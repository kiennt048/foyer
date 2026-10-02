package raddb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveColour reads what the symlink an operator's radiusd follows actually points
// at. It is the only thing that says whether a rollback happened, because a report
// can claim anything.
func liveColour(t *testing.T, a *Applier) string {
	t.Helper()
	dst, err := os.Readlink(filepath.Join(a.Root, "raddb"))
	if err != nil {
		t.Fatalf("reading the live symlink: %v", err)
	}
	return filepath.Base(dst)
}

// Everything after the swap rolls the swap back, including the two steps most
// easily left out.
//
// Gate 4 swaps the symlink and *then* reads the client table for the startup
// snapshot. A failure there that returned from `Apply` without rolling back would
// leave the symlink pointing at a tree no server has been restarted onto — so the
// running server is still serving the previous configuration while the link says
// otherwise, and the next restart, whenever it happens and for whatever reason,
// silently cuts over to a configuration nobody chose to apply.
//
// **An unreachable rollback path is worse than none, because it looks like one**
// — the report would say `rolledBack: false` where every other failure in this
// gate says `true`, so the difference is invisible unless somebody reads the link.
//
// The client-table read is taken before the swap as well, which is the other
// half: the most likely failure here is a database that has gone away, and there
// is no reason for that to move anything at all.
func TestEveryFailureAfterTheSwapPutsTheSymlinkBack(t *testing.T) {
	clients := func(context.Context) ([]ClientRow, error) {
		return []ClientRow{{NASName: "10.0.0.5", Shortname: "ap-lobby", Type: "other", Secret: testSecretA}}, nil
	}

	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, a *Applier)
		// swapped is whether the symlink is expected to have moved at all. A
		// failure before the swap leaves the previous colour without any
		// rollback having been needed, and reporting that as a rollback would be
		// a report claiming work it did not do.
		rolledBack bool
	}{
		{
			name: "the client table cannot be read",
			arrange: func(_ *testing.T, a *Applier) {
				a.StartupClients = func(context.Context) ([]ClientRow, error) {
					return nil, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
				}
			},
			rolledBack: false, // it never swaps: the read is taken first.
		},
		{
			name: "the snapshot cannot be written",
			arrange: func(t *testing.T, a *Applier) {
				a.StartupClients = clients
				// A path that cannot be created: the parent is a file.
				blocker := filepath.Join(a.Root, "blocker")
				if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				a.SnapshotPath = filepath.Join(blocker, "startup.json")
			},
			rolledBack: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{version: goodVersion}
			a := testApplier(t, r)
			a.SnapshotPath = SnapshotPathFor(a.Root)
			a.StartupClients = clients

			restarts := 0
			a.Restart = func(context.Context) error { restarts++; return nil }

			// One apply that works, so there is a live configuration to roll back
			// to. A rollback on a first run has nothing to put back and would make
			// every assertion below true for the wrong reason.
			if _, err := a.Apply(context.Background(), testConfig()); err != nil {
				t.Fatalf("the first apply failed, so this test never reaches its subject: %v", err)
			}
			before := liveColour(t, a)
			restarts = 0

			tc.arrange(t, a)

			rep, err := a.Apply(context.Background(), testConfig())
			if err == nil {
				t.Fatal("Apply succeeded; this test is about a failure inside gate 4")
			}

			if got := liveColour(t, a); got != before {
				t.Fatalf("the symlink points at %q and the server was never restarted onto it; it was %q before. "+
					"The next restart, for any reason, cuts over to a configuration nobody applied.", got, before)
			}
			if restarts != 0 {
				t.Errorf("the server was restarted %d times on a cutover that failed before the restart", restarts)
			}
			if rep.RolledBack != tc.rolledBack {
				t.Errorf("rolledBack = %v, want %v", rep.RolledBack, tc.rolledBack)
			}

			gate4 := rep.Results[len(rep.Results)-1]
			if gate4.Gate.Number != 4 {
				t.Fatalf("the last gate reported is %d", gate4.Gate.Number)
			}
			if tc.rolledBack && !strings.Contains(gate4.Detail, before) {
				t.Errorf("gate 4 does not say where the symlink was put back to: %q", gate4.Detail)
			}
		})
	}
}
