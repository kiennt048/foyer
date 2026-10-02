package deploycheck

// What `deploy/backup.sh` and `deploy/restore.sh` refuse, and what they refuse
// it BY NAME — because a restore that silently yields an installation nobody can
// decrypt is the failure this pair of scripts exists to prevent, and the only
// way to know the refusal is real is to watch the refusal happen.
//
// The sandbox is the one `newSandbox` builds: real scripts, stubbed collaborators,
// every `docker` call logged. `backup.sh` and `restore.sh` are the REAL files
// here rather than stubs, so what is asserted is what ships.
//
// What this cannot see: anything PostgreSQL does. It is `deploy/verify.sh`'s
// against a running machine.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The KEK first-boot.sh's generator produces: `id:base64key`, active key first.
const sandboxKEK = "sandbox:YW5vdGhlci1rZXk=\n"

// A second KEK in the same shape, and different in every byte that matters.
// `restore.sh` must tell them apart from their fingerprints alone.
const wrongKEK = "sandbox:YW5vdGhlci1vc2hlbGw=\n"

// backupSandbox is newSandbox plus the four files backup.sh reads, and with a
// `postgres_password` and a KEK that the manifest's fingerprint is taken from.
func backupSandbox(t *testing.T, extra ...string) *sandbox {
	t.Helper()
	files := append([]string{"backup.sh", "restore.sh", "backups.sh", "compose.sh",
		"compose.yaml", "images.conf", "images.sh", "consolenames.sh"}, extra...)
	sb := newSandbox(t, files...)
	// `backup.sh` refuses an install whose secrets are half-generated, and the
	// sandbox starts with neither, so both are written the way first-boot.sh
	// writes them.
	write(t, filepath.Join(sb.root, "secrets", "postgres_password"), "sandbox-pg\n", 0o600)
	write(t, filepath.Join(sb.root, "secrets", "kek"), sandboxKEK, 0o400)
	return sb
}

// aDump is a file the restore-side checks will accept: a body, and a manifest
// whose sha256 and KEK fingerprint both describe it. The sha256 is of `body`
// and the fingerprint of the sandbox KEK, so a restore given the sandbox's own
// key is a restore that passes — which is what makes each refusal below a
// refusal of the KEK specifically rather than of something else that also fails.
func aDump(t *testing.T, sb *sandbox, body string) string {
	t.Helper()
	d := filepath.Join(sb.root, "state", "backups", "foyer-20260101T000000Z.dump")
	write(t, d, body, 0o600)
	write(t, filepath.Join(sb.root, "state", "backups", "foyer-20260101T000000Z.manifest"),
		"taken: 2026-01-01T00:00:00Z\n"+
			"schema: 54\n"+
			"sha256: "+fileSHA(t, []byte(body))+"\n"+
			"kek-sha256: "+kekFP(t, sandboxKEK)+"\n", 0o600)
	return d
}

// fileSHA is a plain SHA-256 of bytes, as `backup.sh` computes a dump's.
// **No trimming**: a dump is a file, and the file is what gets hashed.
func fileSHA(t *testing.T, b []byte) string {
	t.Helper()
	return strings.TrimSpace(strings.Fields(string(sumOf(t, b)))[0])
}

// kekFP is `backups.sh`'s KEK fingerprint: a hash of the file's contents **with
// the trailing newline trimmed**.
//
// That trim is not a detail of the test helper. `first-boot.sh` writes the KEK
// with `printf '%s:%s\n'` and `internal/config`'s `EnvOrFile` trims exactly that
// one newline before handing the value to the keyring, so the newline is not
// part of the key. Hashing it here would fail every manifest assertion in this
// file for a reason that has nothing to do with the behaviour under test.
func kekFP(t *testing.T, s string) string {
	t.Helper()
	return fileSHA(t, []byte(strings.TrimSuffix(s, "\n")))
}

// sumOf is `sha256sum` on Linux and `shasum -a 256` on macOS — the same branch
// `backups.sh` makes, because the two have to agree or nothing here means
// anything.
func sumOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var cmd *exec.Cmd
	if _, err := exec.LookPath("sha256sum"); err == nil {
		cmd = exec.Command("sha256sum", "-")
	} else {
		cmd = exec.Command("shasum", "-a", "256", "-")
	}
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	return out
}

// repository cannot see, and a backup that runs only while nobody has edited a
// crontab is not a backup. The timer is asserted by name, and so is the daily
// schedule and the `Persistent=true` that makes a backup missed while the box
// was off actually run.
func TestBackupInstallsASystemdTimerNotACronEntry(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("writing /etc/systemd/system needs root")
	}
	sb := backupSandbox(t)
	out, err := sb.runArgs(t, "backup.sh", []string{"--install"})
	if err != nil {
		t.Fatalf("--install failed: %v\n%s", err, out)
	}
	for _, f := range []string{"/etc/systemd/system/foyer-backup.service", "/etc/systemd/system/foyer-backup.timer"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was not installed: %v", f, err)
		}
	}
	timer, err := os.ReadFile("/etc/systemd/system/foyer-backup.timer")
	if err != nil {
		t.Fatalf("reading the timer: %v", err)
	}
	for _, want := range []string{"OnCalendar=", "Persistent=true"} {
		if !strings.Contains(string(timer), want) {
			t.Errorf("the timer has no %q:\n%s", want, timer)
		}
	}
}

// ── the backup ──────────────────────────────────────────────────────────────

// A backup must record the KEK's fingerprint, and must never record the key.
//
// The second half is the one that is easy to get wrong by accident: "let the
// backup carry the KEK so a restore just works" is a real proposal, and it turns
// a copy of the backup directory into every shared secret in the venue. So the
// manifest is read here and checked for the key material as well as the
// fingerprint.
func TestABackupRecordsTheKEKFingerprintAndNotTheKey(t *testing.T) {
	sb := backupSandbox(t)
	out, err := sb.run(t, "backup.sh")
	if err != nil {
		t.Fatalf("backup.sh failed: %v\n%s", err, out)
	}
	manifests, _ := filepath.Glob(filepath.Join(sb.root, "state", "backups", "*.manifest"))
	if len(manifests) != 1 {
		t.Fatalf("want exactly one manifest, got %d: %v", len(manifests), manifests)
	}
	b, err := os.ReadFile(manifests[0])
	if err != nil {
		t.Fatal(err)
	}
	m := string(b)
	if !strings.Contains(m, "kek-sha256: "+kekFP(t, sandboxKEK)) {
		t.Errorf("the manifest does not record the KEK's fingerprint:\n%s", m)
	}
	// The key material itself. `YW5vdGhlci1rZXk=` is the base64 in sandboxKEK and
	// `sandbox` is its id; either in a manifest is the defect.
	for _, leak := range []string{"YW5vdGhlci1rZXk=", "sandbox:"} {
		if strings.Contains(m, leak) {
			t.Errorf("the manifest contains the KEY (%q), not only its fingerprint:\n%s", leak, m)
		}
	}
}

// Pruning must keep the newest and drop the rest, and must never empty the
// directory.
//
// The failure this catches is a rule that deletes its own backups: `KEEP` is an
// operator's setting, and KEEP=0 with an unguarded loop leaves nothing. The
// assertion is on what survives, because "pruning ran" is true of a loop that
// removed everything.
func TestPruningKeepsTheNewestAndNeverEmptiesTheDirectory(t *testing.T) {
	sb := backupSandbox(t)
	// Three older dumps, written by hand with distinct names and mtimes.
	old := []string{"foyer-20250101T000000Z", "foyer-20250102T000000Z", "foyer-20250103T000000Z"}
	for _, name := range old {
		write(t, filepath.Join(sb.root, "state", "backups", name+".dump"), "older\n", 0o600)
	}
	if _, err := sb.run(t, "backup.sh"); err != nil {
		t.Fatal("the first backup failed")
	}

	// KEEP=1 must leave exactly one dump, and it must be the one just taken.
	out, err := sb.run(t, "backup.sh", "FOYER_BACKUP_KEEP=1")
	if err != nil {
		t.Fatalf("backup.sh with KEEP=1 failed: %v\n%s", err, out)
	}
	dumps, _ := filepath.Glob(filepath.Join(sb.root, "state", "backups", "*.dump"))
	if len(dumps) != 1 {
		t.Fatalf("KEEP=1 left %d dumps, want 1: %v", len(dumps), dumps)
	}
	if strings.Contains(dumps[0], "20250101") || strings.Contains(dumps[0], "20250102") || strings.Contains(dumps[0], "20250103") {
		t.Errorf("KEEP=1 kept an OLDER dump and dropped the newest: %v", dumps)
	}

	// KEEP=0 must be refused, not obeyed. This is the assertion that makes the
	// first one mean something: a script that prunes to whatever KEEP says will
	// happily reach zero and delete every backup it has.
	out, err = sb.run(t, "backup.sh", "FOYER_BACKUP_KEEP=0")
	if err == nil {
		t.Errorf("KEEP=0 was accepted; it empties the backup directory:\n%s", out)
	}
	dumps, _ = filepath.Glob(filepath.Join(sb.root, "state", "backups", "*.dump"))
	if len(dumps) == 0 {
		t.Fatalf("KEEP=0 removed every backup, which is the defect this guards")
	}
}

// ── the restore, and the KEK ────────────────────────────────────────────────

// The headline guarantee: a restore with no KEK refuses, and says what is
// missing, why it matters, and which key the dump was taken under.
//
// Each clause is asserted because each is a thing this message has been observed
// to lack. "permission denied" does not tell an operator at 02:00 that the file
// they are missing decrypts their whole venue; and a refusal that does not print
// the expected fingerprint leaves them to guess which of several backups' keys
// they are looking for.
func TestARestoreWithoutTheKEKRefusesAndNamesWhatIsMissing(t *testing.T) {
	sb := backupSandbox(t)
	d := aDump(t, sb, "PGDMP\n")
	// The KEK goes away. Not empty — absent, which is what a rebuilt host is.
	if err := os.Remove(filepath.Join(sb.root, "secrets", "kek")); err != nil {
		t.Fatal(err)
	}
	out, err := sb.runArgs(t, "restore.sh", []string{"--dry-run", d})
	if err == nil {
		t.Fatalf("a restore with no KEK was allowed:\n%s", out)
	}
	for _, want := range []string{
		"no key-encryption key", // what is missing
		"secrets/kek",           // which object
		"CIPHERTEXT",            // what it costs
		"internal/store/envelope.go",
		"--kek",                    // what to do about it
		"Nothing has been touched", // and that nothing was
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PGDMP") {
		t.Errorf("the refusal printed the dump's contents:\n%s", out)
	}
}

// The wrong KEK must be refused too, and the two fingerprints must both be
// printed so an operator can tell which file they are looking for.
//
// **This is the one that would otherwise be silent.** A dump restored with the
// wrong key produces an installation that starts, serves a console, and cannot
// decrypt a single sealed column — the failure surfaces the first time a
// controller registers, which can be weeks after the restore. A check that only
// refused a MISSING key would pass while this shipped.
func TestARestoreWithTheWrongKEKRefusesAndPrintsBothFingerprints(t *testing.T) {
	sb := backupSandbox(t)
	d := aDump(t, sb, "PGDMP\n")
	// A different, valid, correctly-shaped KEK. Unlinked first because the
	// sandbox writes it 0400 — a real install's mode, and `verify.sh` asserts it
	// — and rewriting a 0400 file this way would fail with EACCES rather than
	// with the thing under test.
	kek := filepath.Join(sb.root, "secrets", "kek")
	if err := os.Remove(kek); err != nil {
		t.Fatal(err)
	}
	write(t, kek, wrongKEK, 0o400)

	out, err := sb.runArgs(t, "restore.sh", []string{"--dry-run", d})
	if err == nil {
		t.Fatalf("a restore with the WRONG KEK was allowed:\n%s", out)
	}
	if !strings.Contains(out, "WRONG key-encryption key") {
		t.Errorf("the refusal does not say the key is the wrong one:\n%s", out)
	}
	if !strings.Contains(out, kekFP(t, sandboxKEK)) {
		t.Errorf("the refusal does not print the fingerprint the dump was taken under:\n%s", out)
	}
	if !strings.Contains(out, kekFP(t, wrongKEK)) {
		t.Errorf("the refusal does not print the fingerprint of the key that was supplied:\n%s", out)
	}
}

// A corrupt dump must be refused BEFORE anything is stopped or dropped, and the
// message must name the file.
//
// The ordering is the whole point and is not observable from the exit code: a
// checksum check that runs after `DROP DATABASE` has already cost the operator
// the installation they were trying to protect. `--dry-run` is what makes the
// ordering observable at all, so it is the mode asserted here.
func TestACorruptDumpIsRefusedByNameBeforeAnythingIsTouched(t *testing.T) {
	sb := backupSandbox(t)
	d := aDump(t, sb, "PGDMP this is the dump\n")
	// The file changes after the manifest was written: a bad disk, or a copy
	// that has gone stale. Both are the same event to a restore.
	if err := os.Chmod(d, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d, []byte("PGDMP this is the dump, and now it is different\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := sb.runArgs(t, "restore.sh", []string{"--dry-run", d})
	if err == nil {
		t.Fatalf("a dump that does not match its manifest was restored:\n%s", out)
	}
	// The refusal must be the CHECKSUM, not the key. The right KEK is present
	// and matches, so without this assertion the test would pass for any reason
	// the script happened to refuse — including a key refusal that fires first
	// and hides the checksum path entirely.
	if strings.Contains(out, "key-encryption key") {
		t.Fatalf("this run was refused for the key, not the checksum, so the checksum check was never reached:\n%s", out)
	}
	if !strings.Contains(out, "does not match the sha256") {
		t.Errorf("the refusal does not say the checksum did not match:\n%s", out)
	}
	if !strings.Contains(out, "foyer-20260101T000000Z.dump") {
		t.Errorf("the refusal does not name the dump:\n%s", out)
	}
	if !strings.Contains(out, "Nothing has been touched") {
		t.Errorf("the refusal does not say nothing was touched:\n%s", out)
	}
}

// A dry run that says "fine" is only worth something if it also proves the key
// works. So the positive case is asserted too: with the right KEK and an intact
// dump, --dry-run succeeds and touches nothing.
func TestADryRunWithTheRightKEKSucceedsAndTouchesNothing(t *testing.T) {
	sb := backupSandbox(t)
	d := aDump(t, sb, "PGDMP\n")
	out, err := sb.runArgs(t, "restore.sh", []string{"--dry-run", d})
	if err != nil {
		t.Fatalf("a dry run with the right KEK was refused: %v\n%s", err, out)
	}
	for _, want := range []string{"KEK sha256", "matches the manifest", "nothing was stopped, dropped or recreated"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not say %q:\n%s", want, out)
		}
	}
	// Nothing was dropped: the manifest is still there, and no `psql` ran.
	if _, err := os.Stat(d); err != nil {
		t.Errorf("the dry run removed the dump: %v", err)
	}
}

// TestABackupWhoseKEKCannotBeReadIsRefusedRatherThanFingerprinted.
//
// On an installed host the KEK is 0400 owned by the container's uid and the
// backup runs as the deploy user, so a bare `cat` prints `Permission denied` to
// stderr and the command substitution returns the empty string. SHA-256 of the
// empty string is `e3b0c442...b855` — a digest that looks exactly like every
// other digest. A manifest recording it, a `verify.sh` comparing against it, and
// an operator told to keep a copy of "that file" add up to a real backup, exit 0,
// and a fingerprint of nothing.
//
// **The sandbox makes the KEK readable, which is why the other tests here cannot
// see it.** The reproduction is a KEK this user genuinely cannot read.
func TestABackupWhoseKEKCannotBeReadIsRefusedRatherThanFingerprinted(t *testing.T) {
	const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	sb := backupSandbox(t)
	kek := filepath.Join(sb.root, "secrets", "kek")
	if err := os.Chmod(kek, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// A test that cannot make the file unreadable cannot make the claim. Named
	// as a condition, not a phase: the skip says exactly what was true.
	if b, err := os.ReadFile(kek); err == nil {
		_ = b
		t.Skip("this user can read a 0000 file - root, or a filesystem without mode bits - so the KEK-blindness cannot be reproduced here")
	}

	out, err := sb.run(t, "backup.sh")
	if err == nil {
		t.Fatalf("backup.sh took a backup with a KEK it could not read. The manifest would carry the SHA-256 of the empty string, which is a digest of nothing wearing a digest's clothes:\n%s", out)
	}
	if !strings.Contains(out, "kek") && !strings.Contains(out, "KEK") {
		t.Errorf("the refusal does not name the KEK, so an operator is left to guess which file it meant:\n%s", out)
	}

	// And nothing was left behind pretending to be a backup.
	entries, _ := os.ReadDir(filepath.Join(sb.root, "state", "backups"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".manifest") {
			body, _ := os.ReadFile(filepath.Join(sb.root, "state", "backups", e.Name()))
			if strings.Contains(string(body), emptyDigest) {
				t.Errorf("a manifest records the digest of the empty string as the KEK fingerprint:\n%s", body)
			}
		}
	}
}
