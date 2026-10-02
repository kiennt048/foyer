package tlsca

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// atClock points a Rotating at a fixed instant. Tests only — see the field's own
// comment. A rotation that would otherwise arrive in 27 months has to be
// observable in a test, and 27 months is exactly why nobody would be watching.
func atClock(r *Rotating, t time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = func() time.Time { return t }
}

func newRotatingWithValidPair(t *testing.T) (*Rotating, string, string) {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")
	r, err := NewRotating(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("NewRotating: %v", err)
	}
	return r, certPath, keyPath
}

func mustGet(t *testing.T, r *Rotating) tls.Certificate {
	t.Helper()
	cert, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("GetCertificate returned a nil certificate and no error, which fails the handshake with no explanation")
	}
	return *cert
}

func TestRotatingInitialReportsWhatStartupDid(t *testing.T) {
	r, certPath, keyPath := newRotatingWithValidPair(t)
	cert, generated := r.Initial()
	if !generated {
		t.Error("Initial reports it loaded, but the directory was empty")
	}
	if cert.Leaf == nil {
		t.Error("Initial returns a certificate with no Leaf, so Describe cannot read its expiry")
	}

	again, err := NewRotating(certPath, keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, generated := again.Initial(); generated {
		t.Error("the second construction reports it generated, with a valid pair already on disk")
	}
}

// TestRotatingServesTheSameCertificateWhileItIsValid is the half that keeps this
// type honest: a rotating holder that rotated eagerly would satisfy every renewal
// test below and quietly re-trust the operator on every handshake.
func TestRotatingServesTheSameCertificateWhileItIsValid(t *testing.T) {
	r, certPath, _ := newRotatingWithValidPair(t)
	before := readFile(t, certPath)
	first := Fingerprint(mustGet(t, r))

	for i := 0; i < 5; i++ {
		if got := Fingerprint(mustGet(t, r)); got != first {
			t.Fatalf("handshake %d got a different certificate: %s", i, got)
		}
	}
	if string(readFile(t, certPath)) != string(before) {
		t.Error("the file on disk was rewritten while the certificate was still valid")
	}
}

// TestRotatingRenewsWithoutARestart is the guarantee EnsureSelfSigned alone cannot
// make, and the reason this type exists.
//
// The deployment has no timer, no cron entry, no unattended reboot and no periodic
// pull, so nothing restarts the control plane. A start-time check on that host is a
// promise about starts, not about service — and 825 days is long enough that the
// day it matters is a day nobody is watching.
func TestRotatingRenewsWithoutARestart(t *testing.T) {
	r, certPath, keyPath := newRotatingWithValidPair(t)
	original := Fingerprint(mustGet(t, r))
	originalPEM := readFile(t, certPath)

	// Far enough forward that the certificate issued at construction is inside its
	// renewal window. Derived from the two constants, so changing either cannot
	// leave this test asserting against a window that no longer exists.
	late := time.Now().Add(time.Duration(MaxValidityDays-RenewBeforeDays+1) * 24 * time.Hour)
	atClock(r, late)

	renewed := mustGet(t, r)
	if Fingerprint(renewed) == original {
		t.Fatal("the certificate was not renewed: a process that never restarts serves this one past its expiry")
	}
	if needsRenewal(renewed.Leaf, nil, late) {
		t.Error("the renewed certificate is itself inside the renewal window, so every handshake renews forever")
	}
	if late.After(renewed.Leaf.NotAfter) {
		t.Error("the renewed certificate is already expired at the clock that triggered the renewal")
	}

	// It reached disk, so a restart agrees with the running process and
	// deploy/verify.sh's served-equals-file check still holds after a rotation.
	if string(readFile(t, certPath)) == string(originalPEM) {
		t.Error("the renewal was not written to disk: a restart would go back to the old certificate")
	}
	fromDisk, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("the renewed pair on disk does not load: %v", err)
	}
	if Fingerprint(fromDisk) != Fingerprint(renewed) {
		t.Error("the certificate being served is not the one on disk")
	}
	assertMode(t, keyPath, keyMode)
	assertKeyIsPrivate(t, keyPath)
	assertMode(t, certPath, certMode)

	// And it settles: the next handshake serves the renewal rather than renewing
	// again.
	if Fingerprint(mustGet(t, r)) != Fingerprint(renewed) {
		t.Error("a second handshake at the same instant renewed again")
	}
}

// TestRotatingKeepsServingWhenRenewalFails — an expired certificate reaches the
// browser as a dated, diagnosable warning. A handshake error reaches it as a bare
// TLS alert on the one port an operator would use to investigate. The first is
// recoverable by a human; the second is not.
func TestRotatingKeepsServingWhenRenewalFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write into a read-only directory")
	}
	r, certPath, _ := newRotatingWithValidPair(t)
	original := Fingerprint(mustGet(t, r))
	dir := filepath.Dir(certPath)

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	late := time.Now().Add(time.Duration(MaxValidityDays-RenewBeforeDays+1) * 24 * time.Hour)
	atClock(r, late)

	got, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("a renewal that could not be written failed the handshake: %v", err)
	}
	if got == nil {
		t.Fatal("GetCertificate returned nil rather than the certificate it still had")
	}
	if Fingerprint(*got) != original {
		t.Error("a failed renewal changed the served certificate")
	}
}

// TestRotatingThrottlesFailedRenewals is observed behaviourally rather than by
// counting syscalls: repair the filesystem and assert that nothing changes until
// the retry interval has passed, then that it does.
//
// Without the throttle an expired certificate on a read-only disk costs a key
// generation and two failed writes on every single connection, which turns a
// certificate problem into a load problem on the port an operator needs.
func TestRotatingThrottlesFailedRenewals(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write into a read-only directory")
	}
	r, certPath, _ := newRotatingWithValidPair(t)
	original := Fingerprint(mustGet(t, r))
	dir := filepath.Dir(certPath)

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	late := time.Now().Add(time.Duration(MaxValidityDays-RenewBeforeDays+1) * 24 * time.Hour)
	atClock(r, late)
	mustGet(t, r) // fails to renew, arms the throttle

	// Filesystem repaired, clock unmoved: the throttle is the only thing that can
	// stop a renewal now.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if Fingerprint(mustGet(t, r)) != original {
		t.Error("a renewal was retried immediately after a failure, so a broken filesystem is retried on every handshake")
	}

	// Past the interval it must try again — a throttle that never expires is an
	// outage that outlives its cause.
	atClock(r, late.Add(retryInterval+time.Second))
	if Fingerprint(mustGet(t, r)) == original {
		t.Error("the renewal never retried after the throttle interval, so fixing the filesystem needs a restart")
	}
}

// TestRotatingIsSafeUnderConcurrentHandshakes — GetCertificate is called from every
// accepting goroutine at once, and the renewal it may perform swaps the stored
// pointer. `go test -race` is the assertion; the count check is here so a
// deadlocked implementation fails rather than hangs silently.
func TestRotatingIsSafeUnderConcurrentHandshakes(t *testing.T) {
	r, _, _ := newRotatingWithValidPair(t)
	late := time.Now().Add(time.Duration(MaxValidityDays-RenewBeforeDays+1) * 24 * time.Hour)
	atClock(r, late)

	const n = 32
	var wg sync.WaitGroup
	seen := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cert, err := r.GetCertificate(nil)
			if err == nil && cert != nil {
				seen[i] = Fingerprint(*cert)
			}
		}()
	}
	wg.Wait()

	// Exactly one renewal happened, so every caller must have seen one of at most
	// two certificates — the original for anyone who arrived first, the renewal
	// for the rest. More than two means the lock is not covering the swap.
	distinct := map[string]bool{}
	for _, f := range seen {
		if f != "" {
			distinct[f] = true
		}
	}
	if len(distinct) > 2 {
		t.Errorf("%d distinct certificates were served under concurrency; the renewal is not serialised", len(distinct))
	}
	if len(distinct) == 0 {
		t.Error("no handshake got a certificate")
	}
}
