//go:build unix

package tlsca

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestModesSurviveAHostileUmask holds the explicit os.Chmod that follows every
// file creation.
//
// Without this test, removing that chmod — leaving the mode entirely to what
// open(2) is passed, minus the process umask — leaves the rest of the suite
// **green**, for the dullest possible reason: the umask of a `go test` run is
// whatever the developer's shell has, usually 022, and 022 subtracts nothing from
// either 0644 or 0400. The chmod does real work that nothing else can see.
//
// That is the defect `docs/rules/testing.md` warns about, arriving through the
// environment rather than through a fixture: an input the harness cannot be wrong
// about. deploy/first-boot.sh already knows this — every generator in it runs under
// `( umask 077 )` and then chmods unconditionally — so the hostile umask is not a
// hypothetical, it is the shape of the script that will call this code's neighbours.
//
// Under umask 077 a certificate created with mode 0644 lands as 0600, and an
// operator cannot read the file whose fingerprint they were told to compare.
func TestModesSurviveAHostileUmask(t *testing.T) {
	// Process-global, and safe only because nothing in this package calls
	// t.Parallel. Restored before any other test runs.
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")

	if _, generated, err := EnsureSelfSigned(certPath, keyPath, nil); err != nil || !generated {
		t.Fatalf("generated=%v err=%v", generated, err)
	}
	assertMode(t, certPath, certMode)
	assertMode(t, keyPath, keyMode)
	assertKeyIsPrivate(t, keyPath)
}

// TestRenewedModesSurviveAHostileUmask is the same guarantee on the path that did
// not exist when the one above was written.
//
// Renewal writes through os.CreateTemp and os.Rename rather than through
// createExclusive, so it has its own mode handling and could bypass the explicit
// chmod the test above protects — silently, and only on a host with a tighter
// umask than a developer's shell. os.CreateTemp creates 0600 whatever is asked, so
// without the chmod the renewed *certificate* is unreadable to the operator who was
// told to compare its fingerprint, and the renewal is exactly when that fingerprint
// changed and most needs comparing.
func TestRenewedModesSurviveAHostileUmask(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")
	certPEM, keyPEM := pairWithValidity(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	writeFile(t, certPath, certPEM, 0o644)
	writeFile(t, keyPath, keyPEM, 0o400)

	if _, generated, err := EnsureSelfSigned(certPath, keyPath, nil); err != nil || !generated {
		t.Fatalf("generated=%v err=%v — an expired pair must be renewed", generated, err)
	}
	assertMode(t, certPath, certMode)
	assertMode(t, keyPath, keyMode)
	assertKeyIsPrivate(t, keyPath)
}
