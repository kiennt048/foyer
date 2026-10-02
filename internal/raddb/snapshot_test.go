package raddb

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testSecretA = "d0f1a2b3c4d5e6f70819" + "2a3b4c5d6e7f8091"
	testSecretB = "9f8e7d6c5b4a39281706" + "5f4e3d2c1b0a9988"
	testPGPass  = "postgres-password-" + "not-a-default"
)

func loadedSnapshot() Snapshot {
	cfg := DefaultConfig()
	cfg.PostgresPassword = testPGPass
	return Snapshot{
		Config: cfg,
		Clients: []ClientRow{
			{NASName: "10.0.0.5", Shortname: "ap-lobby", Type: "other", Secret: testSecretA},
			{NASName: "10.0.0.0/24", Shortname: "aruba-fleet", Type: "other", Secret: testSecretA},
		},
	}
}

// The file is beside the configuration tree and is read by whoever classifies a
// change. `docs/rules/database.md` declares `nas.secret` as the one cleartext
// copy of a shared secret in the deployment; a second one here would make that
// sentence false, and the sentence is the only reason the first copy is
// acceptable.
func TestStartupSnapshotHoldsNoSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), SnapshotName)
	if err := WriteStartupSnapshot(path, loadedSnapshot()); err != nil {
		t.Fatalf("writing snapshot: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testSecretA, testSecretB, testPGPass} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the startup snapshot contains %q in the clear", secret)
		}
	}
	if !strings.Contains(string(raw), digestPrefix) {
		t.Errorf("the snapshot contains no digest at all, so it is not redacting anything:\n%s", raw)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("snapshot mode is %04o, want 0600", perm)
	}
}

// A server that has not changed is a server nobody has to restart.
func TestSnapshotClassifiesAnUnchangedServerAsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), SnapshotName)
	running := loadedSnapshot()
	if err := WriteStartupSnapshot(path, running); err != nil {
		t.Fatal(err)
	}

	f, err := ReadStartupSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Classify(running); got.Class != ClassNone {
		t.Fatalf("Class = %s, want none; reasons: %v", got.Class, got.Reasons)
	}
}

// The measured case, and the one the whole snapshot exists for. Rotating the
// secret of a client the running server loaded at startup leaves that server on
// the old secret: FreeRADIUS answers *"invalid Message-Authenticator! (Shared
// secret is incorrect.)"* until it restarts. Classifying it `sql-only` would tell
// an operator a rotation had taken effect while every access point on that secret
// was locked out.
func TestSnapshotClassifiesASecretRotationOnALoadedClientAsRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), SnapshotName)
	if err := WriteStartupSnapshot(path, loadedSnapshot()); err != nil {
		t.Fatal(err)
	}
	f, err := ReadStartupSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}

	wanted := loadedSnapshot()
	wanted.Clients[0].Secret = testSecretB

	got := f.Classify(wanted)
	if got.Class != ClassRestart {
		t.Fatalf("Class = %s, want restart; reasons: %v", got.Class, got.Reasons)
	}
	if !strings.Contains(strings.Join(got.Reasons, "\n"), "10.0.0.5") {
		t.Errorf("the reason does not name the client whose secret changed: %v", got.Reasons)
	}
}

// The other half, and it must stay the other half: an address the running server
// has never seen, inside a dynamic network, is a database write and nothing more.
// A snapshot that made everything `restart` would be safe and useless.
func TestSnapshotClassifiesANewDiscoverableClientAsSQLOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), SnapshotName)
	if err := WriteStartupSnapshot(path, loadedSnapshot()); err != nil {
		t.Fatal(err)
	}
	f, err := ReadStartupSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}

	wanted := loadedSnapshot()
	wanted.Clients = append(wanted.Clients, ClientRow{
		NASName: "10.0.0.6", Shortname: "ap-bar", Type: "other", Secret: testSecretA,
	})

	got := f.Classify(wanted)
	if got.Class != ClassSQLOnly {
		t.Fatalf("Class = %s, want sql-only; reasons: %v", got.Class, got.Reasons)
	}
	if got.EffectiveWithin == 0 {
		t.Error("a sql-only change carries how long it takes to be honoured, and this one reports zero")
	}
}

// A removed client is a restart, and this is the security-relevant direction: the
// running server holds a decommissioned access point in its client list until it
// restarts, whatever the table says.
func TestSnapshotClassifiesARemovedLoadedClientAsRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), SnapshotName)
	if err := WriteStartupSnapshot(path, loadedSnapshot()); err != nil {
		t.Fatal(err)
	}
	f, err := ReadStartupSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}

	wanted := loadedSnapshot()
	wanted.Clients = wanted.Clients[1:]

	got := f.Classify(wanted)
	if got.Class != ClassRestart {
		t.Fatalf("Class = %s, want restart; reasons: %v", got.Class, got.Reasons)
	}
}

// The trap this file's redaction creates, pinned so that the next person to reach
// for the package-level Classify sees why they must not.
//
// A digest never equals the secret under it, so comparing the file's redacted
// snapshot against a live one answers `restart` for every client, with plausible
// reasons and nothing that looks wrong. `StartupSnapshot.Classify` exists to make
// that unreachable; this test is what says the danger is real rather than
// theoretical.
func TestComparingARedactedSnapshotAgainstALiveOneIsWrongInASilentWay(t *testing.T) {
	path := filepath.Join(t.TempDir(), SnapshotName)
	running := loadedSnapshot()
	if err := WriteStartupSnapshot(path, running); err != nil {
		t.Fatal(err)
	}
	f, err := ReadStartupSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}

	// The wrong call: the file's redacted side against unredacted live values.
	wrong := Classify(f.Redacted, running)
	if wrong.Class != ClassRestart {
		t.Fatalf("Class = %s; this test exists because the wrong comparison answers restart for everything", wrong.Class)
	}

	// The right one, on identical inputs, answers none.
	if right := f.Classify(running); right.Class != ClassNone {
		t.Fatalf("Class = %s, want none", right.Class)
	}
}

// A file this build cannot read is refused rather than misread. A snapshot that
// parses into the wrong shape produces a confident wrong classification.
func TestSnapshotOfAnotherFormatVersionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), SnapshotName)
	if err := os.WriteFile(path, []byte(`{"version":99,"salt":"AAAA","redacted":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStartupSnapshot(path); err == nil {
		t.Fatal("a snapshot from format version 99 was accepted")
	}
}

// "No file" and "the server loaded nothing" are opposite answers, and the second
// classifies every existing client as new. The caller has to be able to tell them
// apart, so absence stays os.ErrNotExist rather than becoming an empty snapshot.
func TestMissingSnapshotIsNotAnEmptyOne(t *testing.T) {
	_, err := ReadStartupSnapshot(filepath.Join(t.TempDir(), SnapshotName))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

// Two installs holding the same shared secret must not produce the same digest,
// or a snapshot lifted from one says something true about the other's secrets.
func TestSaltDiffersPerFileSoDigestsDoNotTravel(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	if err := WriteStartupSnapshot(a, loadedSnapshot()); err != nil {
		t.Fatal(err)
	}
	if err := WriteStartupSnapshot(b, loadedSnapshot()); err != nil {
		t.Fatal(err)
	}
	fa, err := ReadStartupSnapshot(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := ReadStartupSnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	if fa.Redacted.Clients[0].Secret == fb.Redacted.Clients[0].Secret {
		t.Fatal("the same secret digested to the same value in two files; the salt is not doing anything")
	}
}
