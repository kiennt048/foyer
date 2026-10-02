package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The support bundle's whole reason to exist is that `radiusd -X` prints shared
// secrets in cleartext and somebody is going to mail that output to a stranger.
// So the test that matters is not "the command produced a file". It is:
//
//  1. **every credential-shaped value is gone**, including ones this redactor has
//     never seen and no list in this repository contains, and
//  2. **everything else is still there** — because a redactor that empties the
//     file satisfies (1) perfectly and is useless.
//
// Those two halves can contradict each other, which is the property
// `docs/rules/testing.md` says is worth more than either half alone.
//
// It can be made to fail. Narrowing the credential-assignment rule to
// `^(secret=)` — a rule that enumerates one spelling instead of generalising a
// shape — makes the run say:
//
//	--- FAIL: TestBundleRemovesCredentialsTheRedactorHasNeverSeen (0.00s)
//	    the shared secret from clients.conf is in the bundle in cleartext: "Zq7Xk…"
//	    the controller API key is in the bundle in cleartext: "Wc5Jt…"
//	    the guest's User-Password from radiusd -X is in the bundle in cleartext: "Rk4Dy…"
//	--- PASS: TestBundleKeepsEverythingThatIsNotACredential (0.00s)
//	--- FAIL: TestBundleNamesEveryRuleThatFired (0.00s)
//	    rule "credential-assignment" fired 0 times; the fixture contains a case for
//	    it, so either the rule is dead or the count is
//
// Three things that run is evidence for. The context half stays green while the
// credential half goes red, which is the disagreement the two halves exist to
// produce. The other three rules stay green, so the failure names *which* rule
// died rather than "the redactor". And the bundle still reports "3 redaction(s)"
// on its own stdout while leaking three credentials — a count is not a guarantee,
// which is why the manifest reports counts *per rule* and this test reads them.

// These are the values that must not survive. None of them is a known default,
// a placeholder, or a value any list in this repository contains: that is the
// point. A redactor that catches them is matching shape.
const (
	radiusSecret  = "Zq7Xk2Md8PvR4tB1sN9w"
	dbPassword    = "Hs3Ku8Lm2Pq7Vd1Nx6Yr"
	controllerKey = "Wc5Jt9Bn3Fh6Za2Qe8Um"
	guestPassword = "Rk4Dy7Gv1Sx8Lc3Ow6Ib"
	bearerToken   = "Nm2Pf6Tj9Yq4Hb7Ea1Zd"
	unlabelled    = "Vx8Cq3Rn7Ke1Mz5Ay9Tp"
)

// A fixture in the shapes this actually meets: FreeRADIUS debug output, a
// generated clients.conf fragment, a compose environment line, an HTTP exchange
// with the controller, and a key block.
const fixture = `radiusd: FreeRADIUS Version 3.2.10
client ap-lobby {
	ipaddr = 192.0.2.7
	secret = "` + radiusSecret + `"
	require_message_authenticator = yes
}
rlm_sql (sql): Connecting to postgres://foyer:` + dbPassword + `@postgres:5432/foyer
(3) Received Access-Request Id 42 from 192.0.2.7:57001
(3)   User-Name = "guest@lobby"
(3)   User-Password = "` + guestPassword + `"
(3)   NAS-IP-Address = 192.0.2.7
(3)   Calling-Station-Id = "aa:bb:cc:dd:ee:ff"
POST /api/s/default/cmd/stamgr HTTP/1.1
Authorization: Bearer ` + bearerToken + `
{"controller_api_key":"` + controllerKey + `","site":"default"}
FOYER_KEK=env:` + controllerKey + `
ap_shared_thing = ` + unlabelled + `
-----BEGIN PRIVATE KEY-----
MIIBVQIBADANBgkqhkiG9w0BAQEFAASCAT8wggE7AgEAAkEAxfakepemkeyfakepem
-----END PRIVATE KEY-----
`

// The strings that must survive, because a bundle nobody can read is a bundle
// nobody can act on.
var mustSurvive = []string{
	"FreeRADIUS Version 3.2.10",
	"client ap-lobby {",
	"ipaddr = 192.0.2.7",
	"require_message_authenticator = yes",
	`User-Name = "guest@lobby"`,
	`Calling-Station-Id = "aa:bb:cc:dd:ee:ff"`,
	"POST /api/s/default/cmd/stamgr",
	`"site":"default"`,
}

func writeBundle(t *testing.T, extraArgs ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "radiusd.log")
	if err := os.WriteFile(src, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bundle.tar.gz")

	args := append([]string{"--no-env", "--out", out, "--add", "radius=" + src}, extraArgs...)
	if err := runSupportBundle(args); err != nil {
		t.Fatalf("support-bundle: %v", err)
	}
	return readBundle(t, out)
}

func readBundle(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		blob, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = string(blob)
	}
	if len(entries) == 0 {
		t.Fatal("the archive is empty, so nothing below is measuring anything")
	}
	return entries
}

func joined(entries map[string]string) string {
	var b strings.Builder
	for name, body := range entries {
		b.WriteString(name)
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

func TestBundleRemovesCredentialsTheRedactorHasNeverSeen(t *testing.T) {
	all := joined(writeBundle(t))

	for _, c := range []struct{ what, value string }{
		{"the shared secret from clients.conf", radiusSecret},
		{"the database password inside the connection URL", dbPassword},
		{"the controller API key", controllerKey},
		{"the guest's User-Password from radiusd -X", guestPassword},
		{"the bearer token from the controller request", bearerToken},
	} {
		if strings.Contains(all, c.value) {
			t.Errorf("%s is in the bundle in cleartext: %q", c.what, c.value)
		}
	}

	// The PEM body, which no key-name rule can reach.
	if strings.Contains(all, "MIIBVQIBADANBgkqhkiG9w0BAQEFAASCAT8") {
		t.Error("the PEM key block survived into the bundle")
	}
}

func TestBundleKeepsEverythingThatIsNotACredential(t *testing.T) {
	// The half that can disagree with the half above. A redactor that deletes
	// the file passes every assertion in the previous test.
	all := joined(writeBundle(t))
	for _, keep := range mustSurvive {
		if !strings.Contains(all, keep) {
			t.Errorf("the bundle lost context it needs: %q is missing", keep)
		}
	}
}

func TestBundleNamesEveryRuleThatFired(t *testing.T) {
	entries := writeBundle(t)
	raw, ok := entries["manifest.json"]
	if !ok {
		t.Fatal("no manifest.json in the bundle")
	}
	var man bundleManifest
	if err := json.Unmarshal([]byte(raw), &man); err != nil {
		t.Fatalf("manifest is not readable JSON: %v", err)
	}
	for _, rule := range []string{"credential-assignment", "url-userinfo", "authorization-header", "pem-private-key"} {
		if man.Redactions[rule] == 0 {
			t.Errorf("rule %q fired 0 times; the fixture contains a case for it, so either the rule is dead or the count is", rule)
		}
	}
	if len(man.Sources) == 0 || man.Sources[0].Redactions == 0 {
		t.Error("the manifest claims the source needed no redaction, which contradicts the fixture")
	}
}

func TestTheBoundaryIsStatedRatherThanHidden(t *testing.T) {
	// A credential under a key no word class would recognise is NOT removed by
	// shape, and pretending otherwise is how a redactor gets trusted past what
	// it does. This asserts the real behaviour, and then asserts the escape
	// hatch that covers it.
	all := joined(writeBundle(t))
	if !strings.Contains(all, unlabelled) {
		t.Skip("a shape rule now covers ap_shared_thing; widen this test rather than deleting it")
	}

	withValue := joined(writeBundle(t, "--redact-value", unlabelled))
	if strings.Contains(withValue, unlabelled) {
		t.Errorf("--redact-value did not remove %q, so the one layer that covers unrecognisable keys does not work", unlabelled)
	}
}

func TestBinaryContentIsRefusedRatherThanShipped(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "core.dump")
	blob := append([]byte("secret = "+radiusSecret+"\x00"), make([]byte, 64)...)
	if err := os.WriteFile(bin, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bundle.tar.gz")
	if err := runSupportBundle([]string{"--no-env", "--out", out, "--add", "dump=" + bin}); err != nil {
		t.Fatal(err)
	}
	entries := readBundle(t, out)
	if strings.Contains(joined(entries), radiusSecret) {
		t.Error("a binary file was shipped with a credential in it; the redactor cannot scan bytes it cannot tokenise")
	}
	if !strings.Contains(entries["manifest.json"], "not text") {
		t.Error("the bundle dropped a file without saying so in the manifest")
	}
}

func TestEnvironmentSecretsAreHarvestedByNameAndFromURLs(t *testing.T) {
	got := environmentSecrets([]string{
		"FOYER_KEK=" + controllerKey,
		"FOYER_DATABASE_URL=postgres://foyer:" + dbPassword + "@postgres:5432/foyer",
		"PATH=/usr/bin:/bin",
		"FOYER_PORTAL_ADDR=0.0.0.0:8081",
	})
	want := map[string]bool{controllerKey: false, dbPassword: false}
	for _, g := range got {
		if _, ok := want[g]; ok {
			want[g] = true
		}
	}
	for value, found := range want {
		if !found {
			t.Errorf("environmentSecrets missed %q", value)
		}
	}
	for _, g := range got {
		if strings.Contains(g, "/usr/bin") || g == "0.0.0.0:8081" {
			t.Errorf("environmentSecrets harvested a value that is not a credential: %q", g)
		}
	}
}

func TestBundleRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.tar.gz")
	if err := os.WriteFile(out, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runSupportBundle([]string{"--no-env", "--out", out}); err == nil {
		t.Error("an existing bundle was overwritten without --force")
	}
}
