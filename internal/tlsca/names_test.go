package tlsca

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The names in this file are the ones a *deployment* declares, as opposed to the
// ones this package derives from the machine it is running on. The distinction is
// the whole subject: derived names are what a container can see, and a container
// cannot see the Docker host's LAN address — which, since the console is published
// there, is the only address an operator ever dials.
//
// So these tests are about one property with two halves that pull against each
// other: a declared name the stored certificate does not cover must cause a
// replacement, and a declared name it does cover must cause nothing at all. Only
// the first half is the feature; without the second, a console on DHCP would hand
// its operator a new fingerprint on a schedule nobody can predict, which is the
// same trust-on-first-use signal spent until it means nothing.

func declaredPaths(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "admin.crt"), filepath.Join(dir, "admin.key")
}

// TestADeclaredAddressBecomesAnIPSAN catches the whole feature being cosmetically
// right and functionally absent.
//
// A verifier asked about an address consults iPAddress SANs and nothing else, so
// "192.0.2.7" recorded as a dNSName produces a certificate that *lists the address
// an operator configured* and satisfies no dial of it. The assertion is a real
// handshake under that server name rather than a look at the field, because the
// field being populated is what a wrong implementation also achieves.
func TestADeclaredAddressBecomesAnIPSAN(t *testing.T) {
	cert, _, err := EnsureSelfSigned("", "", []string{"192.0.2.7"})
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	if err := handshake(t, cert, "192.0.2.7", poolOf(cert)); err != nil {
		t.Fatalf("a client dialling 192.0.2.7 could not verify the certificate that declared it: %v", err)
	}

	var asDNS bool
	for _, n := range cert.Leaf.DNSNames {
		if n == "192.0.2.7" {
			asDNS = true
		}
	}
	if asDNS {
		t.Error("the address is in DNSNames as well: it belongs only in IPAddresses, and a dNSName that looks like an address is the shape that passes a field check and fails every browser")
	}
}

func TestADeclaredNameBecomesADNSSAN(t *testing.T) {
	cert, _, err := EnsureSelfSigned("", "", []string{"Console.Example.Test."})
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	// Lower-cased and the trailing dot dropped, because that is the form a
	// verifier compares against.
	if err := handshake(t, cert, "console.example.test", poolOf(cert)); err != nil {
		t.Fatalf("a client dialling console.example.test could not verify it: %v", err)
	}
}

// TestANameThatIsNeitherIsRefusedBeforeAnythingIsWritten keeps a typo loud. A
// silently dropped name is a console that starts, reports success, and then fails
// name validation for the one address anybody uses.
func TestANameThatIsNeitherIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	certPath, keyPath := declaredPaths(t)

	_, _, err := EnsureSelfSigned(certPath, keyPath, []string{"localhost", "not a host name"})
	if err == nil {
		t.Fatal("a name that is neither an address nor a DNS name was accepted")
	}
	if !strings.Contains(err.Error(), "not a host name") {
		t.Errorf("the refusal does not name the value that caused it, so an operator cannot find it: %v", err)
	}
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was written despite the refusal", p)
		}
	}
}

// TestAStoredCertificateMissingADeclaredNameIsReplaced is the migration path, and
// it is the reason this is not merely a generation-time feature.
//
// An existing install can hold a certificate covering only the names its
// container could see. Publishing the console on the host's address makes that
// certificate unusable for the only dial that matters, and never-overwrite is
// scoped to a certificate that still works — which this one, on validity alone,
// still does. Without this rule a declared name reaches new installs and no
// existing one, which is exactly the shape that would leave a ten-year
// certificate that cannot be lowered.
func TestAStoredCertificateMissingADeclaredNameIsReplaced(t *testing.T) {
	certPath, keyPath := declaredPaths(t)

	before, _, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	if err := handshake(t, before, "192.0.2.9", poolOf(before)); err == nil {
		t.Fatal("the certificate already covers 192.0.2.9, so this test cannot observe a replacement")
	}

	after, generated, err := EnsureSelfSigned(certPath, keyPath, []string{"192.0.2.9"})
	if err != nil {
		t.Fatalf("second EnsureSelfSigned: %v", err)
	}
	if !generated {
		t.Error("generated is false: the stored certificate cannot serve the declared address and was kept anyway")
	}
	if Fingerprint(after) == Fingerprint(before) {
		t.Fatal("the same certificate came back, so nothing was replaced")
	}
	if err := handshake(t, after, "192.0.2.9", poolOf(after)); err != nil {
		t.Errorf("the replacement still does not cover the declared address: %v", err)
	}

	// On disk, not merely in the return value: the next start loads the file.
	reloaded, generatedAgain, err := EnsureSelfSigned(certPath, keyPath, []string{"192.0.2.9"})
	if err != nil {
		t.Fatalf("third EnsureSelfSigned: %v", err)
	}
	if generatedAgain {
		t.Error("it regenerated a second time: the replacement was not written, or the rule does not settle")
	}
	if Fingerprint(reloaded) != Fingerprint(after) {
		t.Error("the file holds a different certificate from the one that was returned")
	}
}

// TestADeclaredNameAlreadyCoveredChangesNothing is the other half, and it is the
// half that makes the first half safe to ship.
//
// A rule that regenerates whenever the names are not identical would rotate the
// operator's fingerprint every time an interface appeared or a lease moved. What is
// asserted is coverage, not equality: extra names in the stored certificate are
// fine, and only a *missing declared* name is a reason to replace anything.
func TestADeclaredNameAlreadyCoveredChangesNothing(t *testing.T) {
	certPath, keyPath := declaredPaths(t)

	first, _, err := EnsureSelfSigned(certPath, keyPath, []string{"192.0.2.9", "console.example.test"})
	if err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	for i := range 3 {
		again, generated, err := EnsureSelfSigned(certPath, keyPath, []string{"192.0.2.9", "console.example.test"})
		if err != nil {
			t.Fatalf("call %d: %v", i+2, err)
		}
		if generated {
			t.Fatalf("call %d regenerated a certificate that already covers every declared name: an operator would be asked to trust a new fingerprint on every restart", i+2)
		}
		if Fingerprint(again) != Fingerprint(first) {
			t.Fatalf("call %d returned a different certificate", i+2)
		}
	}
}

// TestDerivedNamesDoNotDriveReplacement holds the boundary the package doc draws.
//
// The names this package derives from the machine change whenever a container is
// recreated. If those participated in the coverage rule, every `docker compose up`
// would be a new fingerprint. Only what the deployment declared is load-bearing —
// so a certificate carrying names nobody asked for this time is still the right
// certificate.
func TestDerivedNamesDoNotDriveReplacement(t *testing.T) {
	certPath, keyPath := declaredPaths(t)

	first, _, err := EnsureSelfSigned(certPath, keyPath, []string{"console.example.test"})
	if err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	again, generated, err := EnsureSelfSigned(certPath, keyPath, nil)
	if err != nil {
		t.Fatalf("second EnsureSelfSigned: %v", err)
	}
	if generated {
		t.Error("declaring fewer names replaced the certificate: coverage is the rule, not equality")
	}
	if Fingerprint(again) != Fingerprint(first) {
		t.Error("a different certificate came back")
	}
}

// TestARenewalKeepsTheDeclaredNames closes the gap between the two mechanisms.
//
// Renewal and declaration are separate reasons to make a certificate, and a
// renewal that forgot the declared names would produce a console that works for
// two years and then stops on a morning nobody connects to a certificate.
func TestARenewalKeepsTheDeclaredNames(t *testing.T) {
	certPath, keyPath := declaredPaths(t)

	rot, err := NewRotating(certPath, keyPath, []string{"192.0.2.9"})
	if err != nil {
		t.Fatalf("NewRotating: %v", err)
	}
	initial, _ := rot.Initial()

	// Far enough forward that the renewal window has opened.
	rot.now = func() time.Time { return time.Now().Add((MaxValidityDays - RenewBeforeDays + 1) * 24 * time.Hour) }
	got, err := rot.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if Fingerprint(*got) == Fingerprint(initial) {
		t.Fatal("nothing was renewed, so this test observed nothing")
	}
	// VerifyHostname rather than a handshake: the renewal was issued under the
	// moved clock, so a live handshake would fail on notBefore and say nothing
	// about the names. This asserts the names and only the names.
	if err := got.Leaf.VerifyHostname("192.0.2.9"); err != nil {
		t.Errorf("the renewed certificate dropped the declared address: %v", err)
	}
}
