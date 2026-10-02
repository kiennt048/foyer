package store

import (
	"os"
	"path/filepath"
	"testing"
)

// KeyringFromEnv must honour FOYER_KEK_FILE, because deploy/compose.yaml supplies
// the key that way and no other.
//
// Without it, registering a controller against a deployed stack answers 500 with
// ErrNoKeyring, on a host where /run/secrets/kek exists, is readable, and is named
// by FOYER_KEK_FILE.
//
// The hazard recurs: internal/config implements the _FILE convention, and a
// caller beside it reads os.Getenv directly. In a foyerctl command that fails
// loudly at startup. HERE IT FAILS LATE AND QUIETLY: an unset variable yields
// (nil, nil) by design, so the process starts, reports healthy, passes its own
// doctor, and then refuses the first operation that needs to seal anything.
//
// A convention that half the codebase implements is worse than one nobody does,
// because the half that does makes the other half look intentional.
func TestKeyringFromEnvReadsTheFileForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kek")
	// The shape first-boot.sh generates: `id:base64key`.
	const spec = "2026-09:YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE="
	if err := os.WriteFile(path, []byte(spec+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("FOYER_KEK")
	t.Setenv("FOYER_KEK_FILE", path)

	kr, err := KeyringFromEnv()
	if err != nil {
		t.Fatalf("KeyringFromEnv with only FOYER_KEK_FILE set: %v", err)
	}
	if kr == nil {
		t.Fatal("KeyringFromEnv returned no keyring though FOYER_KEK_FILE names a readable key — " +
			"this is the original defect: the process starts, reports healthy, and refuses " +
			"the first operation that needs to seal anything")
	}
	if kr.ActiveKeyID() != "2026-09" {
		t.Errorf("active key id = %q, want the id from the file", kr.ActiveKeyID())
	}
}

// And the documented (nil, nil) for genuinely unset must survive, or the fix is
// satisfied by making every process refuse to start without a key.
func TestKeyringFromEnvStillYieldsNothingWhenNothingIsSet(t *testing.T) {
	os.Unsetenv("FOYER_KEK")
	os.Unsetenv("FOYER_KEK_FILE")
	kr, err := KeyringFromEnv()
	if err != nil || kr != nil {
		t.Fatalf("unset should be (nil, nil), got (%v, %v)", kr, err)
	}
}
