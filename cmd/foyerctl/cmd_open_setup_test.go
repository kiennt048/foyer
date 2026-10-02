package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// The installer captures standard output as the code, so it must be the code and
// nothing else, and it must be the code whose digest was stored.
func TestOpenSetupPrintsExactlyTheCodeItStored(t *testing.T) {
	var out, errOut bytes.Buffer
	var stored []byte
	if err := openSetup(&out, &errOut, func(d []byte) error { stored = d; return nil }); err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(out.String())
	if strings.Count(out.String(), "\n") != 1 || code == "" {
		t.Fatalf("standard output is %q, want one line: the code", out.String())
	}
	if !identity.SameSetupCode(code, stored) {
		t.Fatal("the printed code is not the one whose digest was stored")
	}
}

// Re-running the installer on an install that has an administrator is ordinary:
// nothing on standard output, and no error.
func TestOpenSetupOnASetUpInstallPrintsNoCode(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := openSetup(&out, &errOut, func([]byte) error { return store.ErrSetupNotNeeded }); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("printed %q on standard output with an administrator present", out.String())
	}
}

// TestTheInstallerShipsNoPassword reads the no-shipped-credential rule off the
// scripts that actually build an install: no operator is created by them with
// any password, fixed or generated, and both the fresh-install and the upgrade path
// open the setup instead — never piping a fixed value into
// `create-operator --password-stdin`.
func TestTheInstallerShipsNoPassword(t *testing.T) {
	for _, path := range []string{"../../deploy/first-boot.sh", "../../deploy/upgrade.sh"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var code []string
		for _, line := range strings.Split(string(raw), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				code = append(code, line)
			}
		}
		body := strings.Join(code, "\n")
		for _, banned := range []string{"create-operator", "--password", "admin_password"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s runs %q: the installer creates an operator with a password it chose", path, banned)
			}
		}
		if !strings.Contains(body, "open_console_setup") {
			t.Errorf("%s does not open the console setup, so an install it builds has nobody who can sign in", path)
		}
	}
}
