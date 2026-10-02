package tlsca

// Every guarantee this package makes is tested against the implementation that
// would break it.
//
// A certificate test is the easiest place in the world to write a check that is
// not one: generate a certificate, assert err == nil, be green forever regardless
// of what the certificate says. So each test below is written against a specific
// wrong implementation, and goes red when tlsca.go is sabotaged that way.
//
// Where a sabotage can live in the test itself it does — TestCreateExclusiveRefuses
// re-injects the overwrite from the page, so to speak, by writing the file first.
// Where it cannot, it is a change to tlsca.go that the test's comment names.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// ── generate when absent ─────────────────────────────────────────────────────

func TestGeneratesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")

	cert, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	if !generated {
		t.Error("generated is false, but there was no certificate on disk to load")
	}
	if cert.Leaf == nil {
		t.Fatal("Leaf is nil: a caller cannot read the expiry or the names without re-parsing")
	}
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was not written: %v", p, err)
		}
	}

	// The whole point is that a browser can complete a handshake against it, so
	// complete one. "No error from the generator" is not that claim.
	if err := handshake(t, cert, "localhost", poolOf(cert)); err != nil {
		t.Errorf("a client that trusts this certificate could not complete a handshake with it: %v", err)
	}
}

func TestGeneratedKeyIsECDSAP256(t *testing.T) {
	cert := mustEnsureInMemory(t)
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("private key is %T, want *ecdsa.PrivateKey — see the package doc on why Ed25519 and RSA were not chosen", cert.PrivateKey)
	}
	if key.Curve != elliptic.P256() {
		t.Errorf("curve is %s, want P-256", key.Curve.Params().Name)
	}
	if got := cert.Leaf.SignatureAlgorithm; got != x509.ECDSAWithSHA256 {
		t.Errorf("signature algorithm is %s, want ECDSA-SHA256", got)
	}
}

func TestGeneratedCertificateIsALeafNotACA(t *testing.T) {
	leaf := mustEnsureInMemory(t).Leaf
	if leaf.IsCA {
		t.Error("IsCA is true: an operator who trusts this has trusted something that can mint a certificate for any name at all")
	}
	if !leaf.BasicConstraintsValid {
		t.Error("BasicConstraintsValid is false, so CA:FALSE is not actually asserted")
	}
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Error("extendedKeyUsage does not contain serverAuth; macOS will not use this certificate for TLS")
	}
	if leaf.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
		t.Error("keyUsage claims keyEncipherment, which an ECDSA key never performs")
	}
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("keyUsage does not claim digitalSignature, which ECDHE_ECDSA requires")
	}
	if leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("keyUsage claims keyCertSign on a leaf")
	}
}

// ── validity ─────────────────────────────────────────────────────────────────

// TestValidityIsExactlyWhatAppleAccepts locks both directions, and it has to.
//
// Too long is a console the operator's own Mac refuses with no click-through; too
// short is a re-trust the operator did not need to do. The number between them was
// measured rather than read; the package doc records how.
func TestValidityIsExactlyWhatAppleAccepts(t *testing.T) {
	leaf := mustEnsureInMemory(t).Leaf
	life := leaf.NotAfter.Sub(leaf.NotBefore)

	// The ceiling written as a number rather than through MaxValidityDays, because
	// the constant is exactly what a well-meaning edit would change. 826 days is
	// CSSMERR_TP_CERT_SUSPENDED on Apple's own evaluator with the certificate
	// supplied as its OWN ANCHOR — so trusting it is not a way around the limit and
	// no operator action recovers a console issued over it.
	const appleRefusesAbove = 825 * 24 * time.Hour
	if life > appleRefusesAbove {
		t.Errorf("validity is %.0f days. Apple's trust evaluator refuses a self-signed leaf over 825 days even as its "+
			"own anchor, with no click-through. That is a console "+
			"the operator's own Mac cannot open", life.Hours()/24)
	}
	if life < appleRefusesAbove {
		t.Errorf("validity is %.0f days, under the 825 the strictest evaluator accepts. Renewal is what makes a bounded "+
			"lifetime survivable here; issuing under the ceiling only rotates the operator's fingerprint sooner and "+
			"buys nothing", life.Hours()/24)
	}

	// At exactly a ceiling, an hour of drift is the difference between accepted and
	// refused. AddDate walks the calendar in the local zone, so a span crossing a
	// DST transition is 825 days plus or minus an hour; a Duration is exact. This
	// asserts the property rather than the implementation that provides it.
	if life%(24*time.Hour) != 0 {
		t.Errorf("validity is %v, not a whole number of days: calendar arithmetic has drifted the span, and at exactly "+
			"the ceiling that drift is what an 'or fewer' check rounds the wrong way", life)
	}

	// Backdated, so a browser a few minutes behind this host does not refuse it
	// with a date error hiding behind the authority warning.
	if !leaf.NotBefore.Before(time.Now()) {
		t.Errorf("NotBefore is %s, which is not in the past: a client with a slow clock gets a date error", leaf.NotBefore)
	}
}

// TestUntrustedFailsOnAuthorityNotOnValidity is the half of the package doc's
// central claim that Go can observe.
//
// It is two assertions on purpose, and they are the two branches: with the
// certificate in the client's roots the handshake succeeds *despite* a ten-year
// lifetime, and with no roots it fails on UnknownAuthority — not on validity. A
// verifier that applied a lifetime ceiling would fail the first; one that ignored
// trust entirely would pass the second.
func TestUntrustedFailsOnAuthorityNotOnValidity(t *testing.T) {
	cert := mustEnsureInMemory(t)

	if err := handshake(t, cert, "localhost", poolOf(cert)); err != nil {
		t.Errorf("trusting client: %v — Go applied something to a ten-year lifetime that the doc says it does not", err)
	}

	err := handshake(t, cert, "localhost", x509.NewCertPool())
	if err == nil {
		t.Fatal("a client with an empty root pool accepted a self-signed certificate")
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Errorf("untrusting client failed with %v, want x509.UnknownAuthorityError — "+
			"the doc claims the warning an operator sees is about authority, and any other reason means it is not", err)
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) && invalid.Reason == x509.Expired {
		t.Error("the failure is a validity failure, which is exactly what the ten-year decision claims cannot happen")
	}
}

// ── subject alternative names ────────────────────────────────────────────────

// TestSubjectAltNames derives what it expects from the same authority the
// implementation reads — net.InterfaceAddrs and os.Hostname — rather than naming
// this machine's addresses. `docs/rules/testing.md`: derive the stand-in from
// what it stands in for, or it is a second copy of a rule that drifts.
func TestSubjectAltNames(t *testing.T) {
	leaf := mustEnsureInMemory(t).Leaf

	for _, name := range []string{"localhost", "127.0.0.1", "::1"} {
		if err := leaf.VerifyHostname(name); err != nil {
			t.Errorf("VerifyHostname(%q): %v — an operator reaching the console there gets a name mismatch on top of the authority warning", name, err)
		}
	}

	if host, err := os.Hostname(); err == nil {
		for _, form := range hostnameForms(host) {
			if err := leaf.VerifyHostname(form); err != nil {
				t.Errorf("VerifyHostname(%q): %v", form, err)
			}
		}
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("net.InterfaceAddrs: %v", err)
	}
	counted := 0
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || !n.IP.IsGlobalUnicast() {
			continue
		}
		counted++
		if err := leaf.VerifyHostname(n.IP.String()); err != nil {
			t.Errorf("VerifyHostname(%q): %v — this machine has that address and the certificate does not cover it", n.IP, err)
		}
	}
	t.Logf("covered %d non-loopback address(es) from net.InterfaceAddrs", counted)

	// The other branch. Without it, a certificate with a wildcard SAN — or one
	// whose verification was stubbed — would satisfy everything above.
	if err := leaf.VerifyHostname("console.example.com"); err == nil {
		t.Error("VerifyHostname accepted a name this host has nothing to do with")
	}

	// A browser ignores the CommonName outright, so a certificate whose only
	// identity is there matches no host at all.
	if len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 0 {
		t.Error("no subjectAltName of any kind: every modern browser ignores the CommonName")
	}
}

func TestHostnameFormsRejectsWhatIsNotAName(t *testing.T) {
	for _, in := range []string{"", "192.0.2.10", "2001:db8::1", "-leading", "trailing-", "has_underscore", strings.Repeat("a", 64)} {
		if got := hostnameForms(in); got != nil {
			t.Errorf("hostnameForms(%q) = %v, want nil — that is not a name a verifier would ever match", in, got)
		}
	}
	got := hostnameForms("Foyer.Example.Lan.")
	want := []string{"foyer.example.lan", "foyer", "foyer.local"}
	if !slices.Equal(got, want) {
		t.Errorf("hostnameForms = %v, want %v", got, want)
	}
	if got := hostnameForms("box.local"); !slices.Equal(got, []string{"box.local", "box"}) {
		t.Errorf("hostnameForms(box.local) = %v, want [box.local box] with no duplicate .local", got)
	}
}

// ── load when present, never overwrite ───────────────────────────────────────

func TestLoadsWhenPresent(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")

	first, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil || !generated {
		t.Fatalf("first call: generated=%v err=%v", generated, err)
	}
	second, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if generated {
		t.Error("the second call reports it generated, with a complete pair already on disk")
	}
	if Fingerprint(first) != Fingerprint(second) {
		t.Errorf("the second call returned a different certificate:\n  first  %s\n  second %s\n"+
			"An operator who trusted the first one has just been silently un-trusted by a restart",
			Fingerprint(first), Fingerprint(second))
	}
	if second.Leaf == nil {
		t.Error("the loaded certificate has no Leaf, so a caller cannot read its expiry without re-parsing")
	}
}

// TestNeverOverwritesAValidPairItDidNotMake is the guarantee stated the way it
// matters: not "the same bytes come back" but "the bytes an operator put there are
// still there". The pre-placed certificate carries a CommonName this package would
// never generate, so a regenerating implementation cannot accidentally satisfy it.
//
// **VALID is load-bearing in the name.** The rule protects a certificate somebody
// may have accepted, and an expired one protects
// nobody — it is a console nobody can reach. TestAnExpiringPairIsRenewed is the
// other half, and the two must disagree about a pair inside the renewal window or
// neither is testing the rule.
func TestNeverOverwritesAValidPairItDidNotMake(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")

	certPEM, keyPEM := foreignPair(t)
	writeFile(t, certPath, certPEM, 0o644)
	writeFile(t, keyPath, keyPEM, 0o600)

	got, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	if generated {
		t.Fatal("generated is true against a complete pair already on disk")
	}
	if cn := got.Leaf.Subject.CommonName; cn != foreignCN {
		t.Errorf("returned a certificate with CommonName %q, want %q — the pair on disk was replaced", cn, foreignCN)
	}
	if on := readFile(t, certPath); string(on) != string(certPEM) {
		t.Error("the certificate on disk changed")
	}
	if on := readFile(t, keyPath); string(on) != string(keyPEM) {
		t.Error("the key on disk changed")
	}
}

// TestCreateExclusiveRefuses re-injects the defect from the test rather than
// recording that somebody once watched it fail. O_EXCL is what makes never-overwrite
// a property of the kernel instead of a property of the stat that preceded it, and
// the stat cannot be the thing under test here.
func TestCreateExclusiveRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "already-there")
	writeFile(t, path, []byte("an operator's file"), 0o644)

	err := createExclusive(path, []byte("replacement"), 0o644)
	if err == nil {
		t.Fatal("createExclusive overwrote an existing file")
	}
	if got := string(readFile(t, path)); got != "an operator's file" {
		t.Errorf("the file's contents are now %q", got)
	}
	if !strings.Contains(err.Error(), "Nothing was overwritten") {
		t.Errorf("the error does not say the file survived: %v", err)
	}
}

func TestPartialStateIsRefused(t *testing.T) {
	certPEM, keyPEM := foreignPair(t)

	for _, tc := range []struct {
		name     string
		writeKey bool
	}{
		{"certificate present, key missing", false},
		{"key present, certificate missing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			certPath := filepath.Join(dir, "admin.crt")
			keyPath := filepath.Join(dir, "admin.key")
			present, absent := certPath, keyPath
			if tc.writeKey {
				writeFile(t, keyPath, keyPEM, 0o600)
				present, absent = keyPath, certPath
			} else {
				writeFile(t, certPath, certPEM, 0o644)
			}

			_, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
			if err == nil {
				t.Fatal("half a certificate was accepted; a silent regeneration here un-trusts whatever the operator installed")
			}
			if generated {
				t.Error("generated is true on a refusal")
			}
			for _, p := range []string{certPath, keyPath} {
				if !strings.Contains(err.Error(), p) {
					t.Errorf("the error names neither file it is about; it must name %s\n  got: %v", p, err)
				}
			}
			// A refusal that created the missing half, or removed the surviving
			// one, would satisfy a test that only checked for an error.
			if _, err := os.Stat(absent); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s exists after the refusal: something was written", absent)
			}
			if _, err := os.Stat(present); err != nil {
				t.Errorf("%s no longer exists after the refusal: the survivor was removed", present)
			}
		})
	}
}

// ── renewal ──────────────────────────────────────────────────────────────────

// TestAnExpiringPairIsRenewed drives the boundary from both sides in one table, so
// the two halves cannot drift apart: the same fixture, the same call, and the only
// difference is where NotAfter sits relative to the window.
//
// A renewal that fired on everything would fail the "far from expiry" row; one that
// never fired would fail the other three. Neither half alone can say that.
func TestAnExpiringPairIsRenewed(t *testing.T) {
	window := time.Duration(RenewBeforeDays) * 24 * time.Hour

	for _, tc := range []struct {
		name     string
		notAfter time.Duration // relative to now
		renew    bool
	}{
		{"already expired", -24 * time.Hour, true},
		{"expires today", time.Hour, true},
		{"just inside the renewal window", window - 24*time.Hour, true},
		{"just outside the renewal window", window + 24*time.Hour, false},
		{"far from expiry", farFromExpiry, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			certPath := filepath.Join(dir, "admin.crt")
			keyPath := filepath.Join(dir, "admin.key")
			certPEM, keyPEM := pairWithValidity(t, time.Now().Add(-time.Hour), time.Now().Add(tc.notAfter))
			writeFile(t, certPath, certPEM, 0o644)
			writeFile(t, keyPath, keyPEM, 0o400)

			got, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
			if err != nil {
				t.Fatalf("EnsureSelfSigned: %v", err)
			}
			if generated != tc.renew {
				t.Fatalf("generated = %v, want %v", generated, tc.renew)
			}

			onDisk := readFile(t, certPath)
			replaced := string(onDisk) != string(certPEM)
			if replaced != tc.renew {
				t.Errorf("the file on disk was replaced = %v, want %v", replaced, tc.renew)
			}

			if !tc.renew {
				if cn := got.Leaf.Subject.CommonName; cn != foreignCN {
					t.Errorf("a certificate that is not near expiry was replaced (CommonName is now %q)", cn)
				}
				if string(readFile(t, keyPath)) != string(keyPEM) {
					t.Error("the key was replaced alongside a certificate that did not need renewing")
				}
				return
			}

			// Renewed: the replacement must actually be usable, not merely different.
			if got.Leaf.Subject.CommonName == foreignCN {
				t.Error("reported a renewal but returned the old certificate")
			}
			if time.Now().After(got.Leaf.NotAfter) {
				t.Error("the renewed certificate is already expired")
			}
			if needsRenewal(got.Leaf, nil, time.Now()) {
				t.Error("the renewed certificate is itself inside the renewal window, so every start renews forever")
			}
			if err := handshake(t, got, "localhost", poolOf(got)); err != nil {
				t.Errorf("the renewed certificate cannot complete a handshake: %v", err)
			}
			assertMode(t, keyPath, keyMode)
			assertKeyIsPrivate(t, keyPath)
			assertMode(t, certPath, certMode)

			// No temporary file survived the rename. A leftover .renewing-* would
			// accumulate one per restart, in the directory holding the key.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Errorf("the directory holds %d files after a renewal, want 2: %v", len(entries), entries)
			}
		})
	}
}

// TestRenewalKeepsTheFileAndTheServedCertificateIdentical is deploy/verify.sh's
// assertion, made here where it can be cheap: that check compares the certificate
// served on 8443 against the file on disk, and a renewal is the one moment those
// two could diverge — generate in memory, hand it to the server, and write
// something else (or nothing) to disk.
func TestRenewalKeepsTheFileAndTheServedCertificateIdentical(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")
	certPEM, keyPEM := pairWithValidity(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	writeFile(t, certPath, certPEM, 0o644)
	writeFile(t, keyPath, keyPEM, 0o400)

	served, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil || !generated {
		t.Fatalf("generated=%v err=%v", generated, err)
	}

	fromDisk, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("the renewed pair on disk does not load: %v", err)
	}
	if Fingerprint(served) != Fingerprint(fromDisk) {
		t.Errorf("the certificate handed to the caller is not the one written to disk:\n  served %s\n  file   %s\n"+
			"deploy/verify.sh compares exactly these two", Fingerprint(served), Fingerprint(fromDisk))
	}

	// And it survives a restart: the next call loads rather than renewing again.
	again, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if generated {
		t.Error("the start after a renewal renewed again")
	}
	if Fingerprint(again) != Fingerprint(served) {
		t.Error("the certificate changed on the start after a renewal")
	}
}

// TestAFailedRenewalLeavesTheOldPairIntact — a renewal that cannot write must not
// destroy the certificate it was replacing. The old one may be expired, but an
// expired certificate plus a loud browser warning is a recoverable state, and an
// empty directory on a box nobody can reach is not.
//
// **The two files live in different directories, and that is the whole design of
// this test.** With both in one read-only directory, it would stay green against
// the most plausible wrong implementation there is — delete the old pair, then
// write the new one — because a read-only directory also blocks the delete. The
// test could not reach the defect it is named for.
//
// Splitting them makes the certificate's directory writable, so a delete-first
// implementation succeeds in destroying the certificate while the key's staging
// still fails on the read-only side. The failure is then real and observable.
// A negative test whose subject has more than one way to be protected is a test of
// whichever guard fires first — `docs/rules/database.md` says this about
// SQLSTATEs, and it is the same shape in a filesystem.
func TestAFailedRenewalLeavesTheOldPairIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write into a read-only directory")
	}
	root := t.TempDir()
	certDir := filepath.Join(root, "cert") // writable: a delete-first bug can bite here
	keyDir := filepath.Join(root, "key")   // read-only: this is what makes the renewal fail
	for _, d := range []string{certDir, keyDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	certPath := filepath.Join(certDir, "admin.crt")
	keyPath := filepath.Join(keyDir, "admin.key")
	certPEM, keyPEM := pairWithValidity(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	writeFile(t, certPath, certPEM, 0o644)
	writeFile(t, keyPath, keyPEM, 0o400)

	if err := os.Chmod(keyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(keyDir, 0o700) })

	_, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err == nil {
		t.Fatal("a renewal that cannot write its key reported success")
	}
	if generated {
		t.Error("generated is true on a failed renewal")
	}
	if string(readFile(t, certPath)) != string(certPEM) {
		t.Error("the old certificate was destroyed by a renewal that could not complete")
	}
	if string(readFile(t, keyPath)) != string(keyPEM) {
		t.Error("the old key was destroyed by a renewal that could not complete")
	}
	// And nothing half-written was left where the next start would find it.
	entries, err := os.ReadDir(certDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the certificate directory holds %d files after a failed renewal, want 1: %v", len(entries), entries)
	}
}

func TestACorruptPairIsReportedNotReplaced(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")
	writeFile(t, certPath, []byte("this is not a certificate\n"), 0o644)
	writeFile(t, keyPath, []byte("nor is this a key\n"), 0o400)

	_, _, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err == nil {
		t.Fatal("a pair that cannot be loaded was accepted")
	}
	if got := string(readFile(t, certPath)); got != "this is not a certificate\n" {
		t.Errorf("the unreadable certificate was replaced: %q", got)
	}
	if !strings.Contains(err.Error(), "remove both") {
		t.Errorf("the error does not tell the operator how to recover: %v", err)
	}
}

// ── file modes ───────────────────────────────────────────────────────────────

func TestGeneratedFileModes(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")
	if _, _, err := EnsureSelfSigned(certPath, keyPath, nil); err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	assertMode(t, keyPath, keyMode)
	assertKeyIsPrivate(t, keyPath)
	assertMode(t, certPath, certMode)

	// The directory's mode is deliberately NOT asserted here: t.TempDir made it,
	// and an existing directory is left exactly as its owner made it — the same
	// asymmetry as the certificate. dirMode applies only to a directory this
	// package creates, which is TestCreatesTheDirectory's subject. A deployment
	// mounting the directory at 0755 keeps 0755, and the key inside is still 0400.
}

// TestKeyModeIsEnforcedOnEveryCall is deploy/first-boot.sh's keep() split: generated
// conditionally, mode enforced unconditionally. Without it a tightening never
// reaches an install that already exists — which is exactly what keep() guards
// against.
//
// The certificate half is the asymmetry, and it is asserted too: narrowing a key
// fixes an exposure, widening a certificate somebody deliberately restricted fixes
// nothing, so the certificate must be left alone.
func TestKeyModeIsEnforcedOnEveryCall(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")
	certPEM, keyPEM := foreignPair(t)
	writeFile(t, certPath, certPEM, 0o600)
	writeFile(t, keyPath, keyPEM, 0o644)

	if _, _, err := EnsureSelfSigned(certPath, keyPath, nil); err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	assertMode(t, keyPath, keyMode)
	assertKeyIsPrivate(t, keyPath)
	if got := modeOf(t, certPath); got != 0o600 {
		t.Errorf("the certificate is now %04o, was 0600: this call widened a file the operator had restricted", got)
	}
}

// ── directories ──────────────────────────────────────────────────────────────

func TestCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run", "foyer", "admin-tls")
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")
	if _, generated, err := EnsureSelfSigned(certPath, keyPath, nil); err != nil || !generated {
		t.Fatalf("generated=%v err=%v — a first boot has no directory yet", generated, err)
	}
	assertMode(t, dir, dirMode)
}

// TestAPartialWriteLeavesNothingBehind covers the branch where the certificate is
// written and the key cannot be. Left alone it would poison the install: every
// later boot would hit the partial-state refusal above, forever, over a file this
// process made itself.
func TestAPartialWriteLeavesNothingBehind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write into a 0500 directory, so the failure this test needs cannot be produced")
	}
	root := t.TempDir()
	readOnly := filepath.Join(root, "ro")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	certPath := filepath.Join(root, "admin.crt")
	keyPath := filepath.Join(readOnly, "admin.key")

	_, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err == nil {
		t.Fatal("writing into a directory this process cannot write was reported as success")
	}
	if generated {
		t.Error("generated is true on a failure")
	}
	if !strings.Contains(err.Error(), keyPath) {
		t.Errorf("the error does not name the file that could not be written: %v", err)
	}
	if _, err := os.Stat(certPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s survives a failed generation, so every later boot refuses on a partial state", certPath)
	}
}

func TestADirectoryWhereACertificateShouldBeIsNamed(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	if err := os.Mkdir(certPath, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := EnsureSelfSigned(certPath, filepath.Join(dir, "admin.key"), nil)
	if err == nil {
		t.Fatal("a directory was accepted as a certificate path")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("the error does not say what is wrong with the path: %v", err)
	}
}

// ── empty paths ──────────────────────────────────────────────────────────────

func TestEmptyPathsGenerateInMemoryAndWriteNothing(t *testing.T) {
	dir := t.TempDir()
	cert, generated, err := EnsureSelfSigned("", "", nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned(\"\", \"\", nil): %v", err)
	}
	if !generated {
		t.Error("generated is false, but nothing was loaded from anywhere")
	}
	if cert.Leaf == nil || cert.PrivateKey == nil {
		t.Fatal("the in-memory certificate is not usable")
	}
	if err := handshake(t, cert, "localhost", poolOf(cert)); err != nil {
		t.Errorf("the in-memory certificate cannot complete a handshake: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("something was written to disk: %v", entries)
	}

	// A second call must produce a different certificate. That is the honest
	// signal that nothing was persisted, and a caching implementation would hide
	// it behind a fingerprint that never changes.
	other, _, err := EnsureSelfSigned("", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(cert) == Fingerprint(other) {
		t.Error("two in-memory calls produced the same certificate, which would imply something persisted it")
	}
}

func TestOneEmptyPathIsRefused(t *testing.T) {
	for _, tc := range []struct{ certPath, keyPath string }{
		{"", "/tmp/admin.key"},
		{"/tmp/admin.crt", ""},
	} {
		_, generated, err := EnsureSelfSigned(tc.certPath, tc.keyPath, nil)
		if err == nil {
			t.Errorf("EnsureSelfSigned(%q, %q, nil) was accepted; half a configuration is a refusal, not a default", tc.certPath, tc.keyPath)
		}
		if generated {
			t.Error("generated is true on a refusal")
		}
	}
}

// ── fingerprint and description ──────────────────────────────────────────────

func TestFingerprintIsTheOneABrowserShows(t *testing.T) {
	cert := mustEnsureInMemory(t)
	got := Fingerprint(cert)

	// Computed by a different route on purpose — stdlib hex over the whole digest
	// rather than a byte-at-a-time format — so this is a check on Fingerprint and
	// not a second copy of it.
	sum := sha256.Sum256(cert.Certificate[0])
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	if strings.ReplaceAll(got, ":", "") != want {
		t.Errorf("Fingerprint = %s, want the SHA-256 of the DER, %s", got, want)
	}
	if parts := strings.Split(got, ":"); len(parts) != 32 {
		t.Errorf("Fingerprint has %d colon-separated groups, want 32 — that is the form a certificate viewer shows", len(parts))
	}
	if got != strings.ToUpper(got) {
		t.Errorf("Fingerprint is not uppercase: %s", got)
	}
	// It must be over the certificate, not over something constant. A stub
	// returning a fixed string would pass a single-certificate test.
	if other := Fingerprint(mustEnsureInMemory(t)); other == got {
		t.Error("two different certificates have the same fingerprint")
	}
	if Fingerprint(tls.Certificate{}) != "" {
		t.Error("Fingerprint of an empty certificate should be empty so a caller may print it unconditionally")
	}
}

// TestDescribeSaysWhichOfThreeThingsHappened locks the distinction that
// "generated and written to <path>" and "generated in memory, persisted
// nothing" are different sentences and only one of them belongs in a production log.
// A Describe that collapsed them would be satisfied by asserting only that each
// output is non-empty.
func TestDescribeSaysWhichOfThreeThingsHappened(t *testing.T) {
	cert := mustEnsureInMemory(t)
	const path = "/run/foyer/admin-tls/admin.crt"

	written := Describe(cert, path, true)
	memory := Describe(cert, "", true)
	loaded := Describe(cert, path, false)

	if written == memory || written == loaded || memory == loaded {
		t.Fatal("two of the three outcomes produce the same line")
	}
	if !strings.Contains(written, path) {
		t.Errorf("the written line does not name the path: %s", written)
	}
	if strings.Contains(memory, "wrote") || !strings.Contains(memory, "IN MEMORY") {
		t.Errorf("the in-memory line does not say it persisted nothing: %s", memory)
	}
	if strings.Contains(memory, path) {
		t.Errorf("the in-memory line names a path: %s", memory)
	}
	if strings.Contains(loaded, "generated") {
		t.Errorf("the loaded line claims a generation that did not happen: %s", loaded)
	}
	for _, line := range []string{written, memory, loaded} {
		if !strings.Contains(line, Fingerprint(cert)) {
			t.Errorf("a line omits the fingerprint, which is the only thing that lets an operator tell this warning from an attacker's: %s", line)
		}
	}
	if !strings.Contains(written, "localhost") {
		t.Errorf("the written line does not list the names, so a name mismatch has nothing to be diagnosed against: %s", written)
	}
}

func TestDescribeShoutsAboutAnExpiredCertificate(t *testing.T) {
	// Not generated by this package: it has no way to make an expired one, which
	// is the point of the ten-year default. This is what a pair from an older
	// install, or an operator's own, looks like on the day it lapses.
	expired := expiredPair(t)
	line := Describe(expired, "/run/foyer/admin-tls/admin.crt", false)
	if !strings.Contains(line, "WHICH HAS PASSED") {
		t.Errorf("an expired certificate is described as if it were fine: %s", line)
	}
	if fresh := Describe(mustEnsureInMemory(t), "/x", false); strings.Contains(fresh, "WHICH HAS PASSED") {
		t.Errorf("a valid certificate is described as expired: %s", fresh)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

const foreignCN = "a certificate this package would never generate"

func mustEnsureInMemory(t *testing.T) tls.Certificate {
	t.Helper()
	cert, _, err := EnsureSelfSigned("", "", nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	return cert
}

func poolOf(cert tls.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	return pool
}

// handshake serves one connection with cert and dials it verifying against roots.
// A real TLS exchange, because "the generator returned no error" is not evidence
// that a browser can reach the console.
func handshake(t *testing.T, cert tls.Certificate, serverName string, roots *x509.CertPool) error {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
		conn.Close()
	}()

	client, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		RootCAs:    roots,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return err
	}
	return client.Close()
}

// farFromExpiry is comfortably outside the renewal window, derived from the window
// rather than picked, so widening RenewBeforeDays cannot silently turn the
// never-overwrite tests into renewal tests wearing the wrong name.
const farFromExpiry = (RenewBeforeDays + 30) * 24 * time.Hour

// foreignPair is a valid, long-lived certificate and key that this package did not
// make, for the tests that must tell "left alone" from "regenerated into something
// valid".
//
// **Long-lived on purpose.** A pair that expires in 24 hours is inside the
// renewal window — so it is one this package is *supposed* to replace, and every
// test built on it would assert the opposite of the rule while still passing under
// its name. Never-overwrite protects only a pair outside the renewal window.
func foreignPair(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	return pairWithValidity(t, time.Now().Add(-time.Hour), time.Now().Add(farFromExpiry))
}

func expiredPair(t *testing.T) tls.Certificate {
	t.Helper()
	certPEM, keyPEM := pairWithValidity(t, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func pairWithValidity(t *testing.T, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	cert, err := generate(nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key := cert.PrivateKey.(*ecdsa.PrivateKey)
	tmpl := *cert.Leaf
	tmpl.Subject.CommonName = foreignCN
	// x509.CreateCertificate prefers RawSubject/RawIssuer when they are populated,
	// and they are on a *parsed* certificate — so without clearing them the new
	// CommonName is silently ignored and this helper hands back something
	// indistinguishable from what the package generates.
	tmpl.RawSubject, tmpl.RawIssuer = nil, nil
	tmpl.NotBefore, tmpl.NotAfter = notBefore, notAfter
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func writeFile(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // defeat the process umask
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	if got := modeOf(t, path); got != want {
		t.Errorf("%s is mode %04o, want %04o — deploy/verify.sh asserts exactly this on that host", path, got, want)
	}
}

// assertKeyIsPrivate states the guarantee independently of the constant that
// implements it.
//
// assertMode(keyPath, keyMode) compares the file against the package's own value,
// so changing keyMode to 0644 would leave it green — an input the harness cannot
// be wrong about (`docs/rules/testing.md`, Principles). This is
// the property deploy/verify.sh actually asserts on the host ("no group and no
// other bits"), written where a wrong constant cannot satisfy it.
func assertKeyIsPrivate(t *testing.T, path string) {
	t.Helper()
	if got := modeOf(t, path); got&0o077 != 0 {
		t.Errorf("the private key %s is mode %04o: it is readable or writable by group or other, which is what "+
			"docs/rules/security.md §7 is about and what deploy/verify.sh names as drift", path, got)
	}
}

// TestACertificateThatOutlivesTheCeilingIsReplaced is the migration half of
// never-overwrite.
//
// A deployment can be serving a certificate valid for ten years, issued before
// Apple's refusal of anything over 825 days (even for a leaf supplied as its own
// trust anchor) was taken into account. Lowering `MaxValidityDays` changes
// nothing on that box: never-overwrite is scoped to a *valid* certificate, and a
// ten-year certificate is valid for another decade. **The repair would not reach
// the machines that have the defect**, and `verify.sh` would report the
// certificate as fine because "not expired" is all it can honestly assert.
//
// The rule is the same one, read properly: a certificate outliving the ceiling is
// not one with plenty of time left, it is one an evaluator rejects outright — the
// same practical state as an expired one. Asserting against `MaxValidityDays`
// rather than a date is what makes a future lowering of the ceiling migrate
// existing installs instead of needing a hand on each box.
func TestACertificateThatOutlivesTheCeilingIsReplaced(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "admin.crt")
	keyPath := filepath.Join(dir, "admin.key")

	// A ten-year pair, exactly the shape the deployment held. Written through the
	// package's own generator so this is the real artefact and not a stand-in that
	// could differ from it in some way that matters.
	certPEM, keyPEM := pairWithValidity(t, time.Now().Add(-time.Hour), time.Now().Add(3650*24*time.Hour))
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o400); err != nil {
		t.Fatal(err)
	}
	old, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	got, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	if !generated {
		t.Fatal("a certificate valid until 2036 was kept. Never-overwrite protects a " +
			"certificate an operator may have trusted; it must not protect one no " +
			"browser will accept, or lowering the ceiling can never reach an install")
	}
	if Fingerprint(got) == Fingerprint(old) {
		t.Fatal("EnsureSelfSigned reported a replacement and returned the same certificate")
	}
	if got.Leaf.NotAfter.After(time.Now().Add(MaxValidityDays * 24 * time.Hour)) {
		t.Errorf("the replacement is valid until %s, past the ceiling it was issued to "+
			"respect — which would make every start replace it again",
			got.Leaf.NotAfter.Format("2006-01-02"))
	}

	// And the half that must not move: a certificate inside the ceiling and not
	// near expiry is still left alone. Without this, "replace it" passes by
	// replacing everything, every start, rotating the operator's fingerprint daily.
	again, generatedAgain, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned (second call): %v", err)
	}
	if generatedAgain {
		t.Fatal("the replacement was itself replaced on the next call: the ceiling check " +
			"is firing on a certificate that respects the ceiling")
	}
	if Fingerprint(again) != Fingerprint(got) {
		t.Error("a second call returned a different certificate without reporting a replacement")
	}
}
