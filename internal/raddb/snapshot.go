package raddb

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The startup snapshot: a record of what the running server loaded.
//
// `Classify` answers `none` / `sql-only` / `restart`, and the whole of that answer
// turns on one distinction it cannot see for itself: **is this client already in
// the running server's client list?** A row the server has never seen can be
// discovered from the client table without a restart. A row it loaded at startup
// is in its list already, and changing the table does not change that copy --
// rotating such a client's secret leaves the running server on the old one, which
// is measured and reads on the wire as *"invalid Message-Authenticator! (Shared
// secret is incorrect.)"*.
//
// Nothing recorded that fact, so every classification after a restart was a guess.
// This file is the record.
//
// Two things in a Snapshot must never reach disk: the shared secrets in
// `ClientRow.Secret`, and `Config.PostgresPassword`. `docs/rules/database.md`
// declares `nas.secret` as the *one* cleartext copy of a shared secret in the
// deployment, and a JSON file beside the configuration tree would be a second.
// So the file stores salted digests instead, and both sides of the comparison are
// digested with the same salt before `Classify` sees them -- which is why
// `Classify` itself needs no change and does not know any of this happened.

// snapshotVersion is the on-disk format version. A file written by a different
// version is refused rather than misread: a snapshot that parses into the wrong
// shape produces a confident wrong classification, which is worse than none.
const snapshotVersion = 1

// SnapshotName is the conventional file name inside the raddb root.
const SnapshotName = "startup-snapshot.json"

// digestPrefix marks a value as a digest so that nobody reading the file has to
// wonder whether they are looking at a secret.
const digestPrefix = "sha256:"

// StartupSnapshot is the on-disk record: a redacted Snapshot and the salt its
// digests were taken with.
type StartupSnapshot struct {
	Version int `json:"version"`

	// WrittenAt is when the record was taken, not when the server started. The
	// two differ by however long radiusd takes to read the client table, and a
	// row inserted inside that window is recorded as not-loaded when the server
	// may in fact have loaded it. See the note on Write.
	WrittenAt time.Time `json:"writtenAt"`

	// Salt is random per file. It is not a secret and does not need to be: its
	// job is to keep a digest from being comparable across two deployments, so
	// that a snapshot lifted from one install says nothing about another's
	// secrets.
	Salt []byte `json:"salt"`

	// Redacted is the Snapshot with every secret-bearing field replaced by a
	// digest. It is never a Snapshot you can start a server from.
	Redacted Snapshot `json:"redacted"`
}

// SnapshotPathFor returns the conventional snapshot path inside a raddb root.
func SnapshotPathFor(root string) string { return filepath.Join(root, SnapshotName) }

// WriteStartupSnapshot records what a server is about to load.
//
// **Call it before radiusd starts, not after.** The two orderings fail in
// different directions and only one of them is safe to be wrong about. Written
// before, a row that races the server's own read of the client table is recorded
// as not-loaded while the server may have loaded it -- and the next
// classification then says `sql-only` for a change that in fact needs a restart,
// which is the direction that hands an operator a secret rotation the running
// server ignores. Written after, the same race records a row as loaded that the
// server discovered dynamically, and the classification over-reports `restart`,
// which costs a restart nobody needed.
//
// Neither is free, and the window is milliseconds wide, but the caller who writes
// it before at least knows which way the error goes. That is the whole reason this
// paragraph exists.
func WriteStartupSnapshot(path string, s Snapshot) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("raddb: generating snapshot salt: %w", err)
	}

	f := StartupSnapshot{
		Version:   snapshotVersion,
		WrittenAt: time.Now().UTC(),
		Salt:      salt,
		Redacted:  RedactSnapshot(s, salt),
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("raddb: encoding startup snapshot: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("raddb: create %s: %w", filepath.Dir(path), err)
	}

	// Written through a temporary file and renamed, so that a reader never sees a
	// half-written snapshot. A truncated one would parse as fewer clients than the
	// server loaded, and every missing client would classify as `sql-only`.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("raddb: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("raddb: install %s: %w", path, err)
	}
	return nil
}

// ReadStartupSnapshot reads the record back.
//
// A missing file is reported as such -- use errors.Is(err, os.ErrNotExist) -- and
// the caller must treat it as "we do not know what the server loaded" rather than
// as "the server loaded nothing". Those are opposite answers: the second one
// classifies every existing client as new and says `sql-only` for all of them.
func ReadStartupSnapshot(path string) (StartupSnapshot, error) {
	var f StartupSnapshot
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("raddb: parsing %s: %w", path, err)
	}
	if f.Version != snapshotVersion {
		return f, fmt.Errorf("raddb: %s is format version %d, this build writes %d; refusing to classify against a shape it may not be",
			path, f.Version, snapshotVersion)
	}
	if len(f.Salt) == 0 {
		return f, fmt.Errorf("raddb: %s has no salt, so its digests cannot be reproduced", path)
	}
	return f, nil
}

// Classify compares what the recorded server loaded against what is wanted now.
//
// It redacts the wanted side with this file's own salt first, so the comparison is
// digest against digest. Doing it here rather than in the caller is what keeps a
// caller from passing a live Snapshot straight to the package-level Classify and
// getting `restart` for every client, every time, because a secret never equals
// its digest.
//
// Options are passed through untouched. TableBefore is the one that exists today
// and it consults names only, so it is not a hole in the redaction -- see its own
// doc comment.
func (f StartupSnapshot) Classify(wanted Snapshot, opts ...ClassifyOption) Change {
	return ClassifyRedacted(f.Redacted, RedactSnapshot(wanted, f.Salt), opts...)
}

// ClassifyRedacted is Classify, named so that a caller holding two already-redacted
// snapshots does not have to wonder whether it is the right function.
//
// It exists because the failure it guards is silent: comparing a redacted snapshot
// against a live one produces a well-formed `restart` answer with plausible
// reasons, and nothing about it looks wrong.
func ClassifyRedacted(running, wanted Snapshot, opts ...ClassifyOption) Change {
	return Classify(running, wanted, opts...)
}

// RedactSnapshot replaces every secret-bearing field with a salted digest.
//
// It is exported because redaction has to be applied to both sides of any
// comparison and a caller may need to do the second side itself. Applying it twice
// is not idempotent and does not need to be -- both sides are redacted exactly
// once, from live values.
func RedactSnapshot(s Snapshot, salt []byte) Snapshot {
	out := Snapshot{Config: s.Config}
	out.Config.PostgresPassword = digest(salt, s.Config.PostgresPassword)

	if s.Clients != nil {
		out.Clients = make([]ClientRow, len(s.Clients))
		copy(out.Clients, s.Clients)
		for i := range out.Clients {
			out.Clients[i].Secret = digest(salt, out.Clients[i].Secret)
		}
	}
	return out
}

// digest is salted SHA-256, base64, prefixed. An empty value stays empty so that
// "no password configured" is still readable in the file as no password.
//
// A digest is a change detector, not a credential, and it is only as unguessable
// as the value under it. Foyer's shared secrets are crypto/rand and its Postgres
// password is whatever the installer chose -- so this file is not a place to be
// relaxed about, and it is written 0600 for that reason.
func digest(salt []byte, v string) string {
	if v == "" {
		return ""
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(v))
	return digestPrefix + base64.RawStdEncoding.EncodeToString(h.Sum(nil))
}
