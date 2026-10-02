package raddb

import (
	"errors"
	"fmt"
	"os"
)

// ErrNoStartupSnapshot is a snapshot path with nothing at it: nothing recorded what
// the running server loaded, so whether a client-table change needs a restart is
// not known. An answer, not a failure — and never to be rendered as `none`.
var ErrNoStartupSnapshot = errors.New("raddb: no startup snapshot: nothing recorded what the running server loaded")

// ClassifySync answers the question a sync of the client table leaves open: does
// FreeRADIUS have to be restarted for it to take effect?
//
// **It lives here rather than in `foyerctl sync-nas`**, because the console
// runs the sync itself after every RADIUS-client write and has to answer the same
// question the same way; two copies of this is how a console comes to say "no
// restart" about a revocation the command line would have called `restart`.
//
// `all` is the whole client table as it now stands — every tenant, every row,
// because that is what the server loads — and `removed` is the names the sync
// deleted. Both halves are required and both are why this is not a one-liner:
//
//   - Without `removed`, a row written after the last restart and deleted before
//     the next is in neither set and classifies `none` — *the revocation took
//     effect* — while on 3.2.10 the running server keeps answering it from the
//     definition it cached.
//   - The comparison is redacted on both sides. A live snapshot against the plain
//     Classify answers `restart` for every client every time, because a secret
//     never equals its digest.
func ClassifySync(snapshotPath string, all []ClientRow, removed []string) (Change, StartupSnapshot, error) {
	f, err := ReadStartupSnapshot(snapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		return Change{}, StartupSnapshot{}, ErrNoStartupSnapshot
	}
	if err != nil {
		return Change{}, StartupSnapshot{}, fmt.Errorf("raddb: the startup snapshot could not be read: %w", err)
	}
	redacted := RedactSnapshot(Snapshot{Clients: all}, f.Salt)
	change := ClassifyRedacted(f.Redacted,
		Snapshot{Config: f.Redacted.Config, Clients: redacted.Clients},
		TableBefore(removedRows(removed)))
	return change, f, nil
}

// removedRows renders the names a sync deleted as the rows TableBefore reads. A
// name and nothing else, which is all Classify consults from it; nil in is nil out,
// and TableBefore(nil) is still *supplied*: "looked and found none".
func removedRows(names []string) []ClientRow {
	if len(names) == 0 {
		return nil
	}
	rows := make([]ClientRow, 0, len(names))
	for _, n := range names {
		rows = append(rows, ClientRow{NASName: n})
	}
	return rows
}
