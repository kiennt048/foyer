package raddb

import (
	"os"
	"strings"
	"testing"
)

// What `rlm_rest` actually parses, measured rather than remembered.
//
// ── what a plausible-looking block does ──────────────────────────────────────
//
// A `tls { verify_peer = no }` in `templates/rest.conf.tmpl` reads as "TLS
// without authentication". Measured against FreeRADIUS 3.2.10, it is not that.
// It is nothing at all:
//
//   - `verify_peer` is not a configuration item `rlm_rest` knows. The names it
//     knows are `ca_file`, `ca_info_file`, `ca_path`, `certificate_file`,
//     `private_key_file`, `private_key_password`, `random_file`, `check_cert`
//     and `check_cert_cn` — read out of `/usr/lib/freeradius/rlm_rest.so`. Two of
//     those look interchangeable and are not: `ca_info_file` is CURLOPT_CAINFO,
//     the trust store, and `ca_file` is CURLOPT_ISSUERCERT. A block with only
//     `ca_file` fails every request with CURLE 60 while looking correct in
//     `radiusd -XC`.
//   - `tls { }` is a **per-section** subsection, inside `authorize { }` and
//     `authenticate { }`. A module-level one is silently dropped: `radiusd -XC`
//     prints the whole `rest { }` section back as four items — `connect_uri`,
//     `connect_uri_socket_abstract`, `connect_timeout`, `http_negotiation` —
//     with no `tls` among them and no warning that a block has been ignored.
//   - The per-section defaults then in force are `check_cert = yes` and
//     `check_cert_cn = yes`, which is the **opposite** of what the ignored line
//     says. A reader auditing this boundary would read `verify_peer = no` and
//     conclude the wrong thing in both directions.
//   - `timeout` is per-section too, and a module-level one is dropped the same
//     way: `-XC` prints `timeout = 4.000000` in both sections while the
//     module-level line says 2.
//
// So this is the defect `docs/rules/testing.md` warns about, in a config
// template: a line that looks like a switch and is not.
func TestTheRestTLSBlockIsWhereFreeRADIUSParsesIt(t *testing.T) {
	for _, shape := range []struct {
		name    string
		mutate  func(*Config)
		wantTLS bool
	}{
		// Both shapes, because the placement rule holds for both and only one of
		// them can show that the material lands where FreeRADIUS reads it.
		{name: "plaintext, as lab/ and a loopback Policy API run it"},
		{name: "mTLS, as a deployment runs it", mutate: withPolicyMTLS, wantTLS: true},
	} {
		t.Run(shape.name, func(t *testing.T) {
			rest := configOnly(readGenerated(t, generate(t, shape.mutate), "mods-available", "rest"))

			// `verify_peer` is not an item rlm_rest has. Emitting it configures
			// nothing and misleads whoever reads the file next.
			if strings.Contains(rest, "verify_peer") {
				t.Error("the rest module emits verify_peer, which rlm_rest 3.2.10 does not know: " +
					"the item names are ca_file, certificate_file, private_key_file, check_cert, check_cert_cn")
			}

			// A `tls {` before the first section is a module-level one, and it is
			// dropped without a warning.
			firstSection := strings.Index(rest, "authenticate {")
			if firstSection < 0 {
				t.Fatal("the rest module has no authenticate section")
			}
			if i := strings.Index(rest[:firstSection], "tls {"); i >= 0 {
				t.Errorf("the rest module carries a module-level tls { } block at byte %d. "+
					"rlm_rest parses tls per section; radiusd -XC prints the module back with no tls in it", i)
			}

			for _, section := range []string{"authenticate", "authorize"} {
				body := sectionBody(t, rest, section)
				if got := strings.Contains(body, "tls {"); got != shape.wantTLS {
					t.Errorf("the rest module's %s section has tls { } = %v, want %v", section, got, shape.wantTLS)
				}
				if !shape.wantTLS {
					continue
				}
				// `ca_info_file` and not `ca_file`: they are CURLOPT_CAINFO and
				// CURLOPT_ISSUERCERT, and only the first is the trust store. A
				// tls block carrying just `ca_file` parses, prints back under
				// `radiusd -XC` identically, and fails every request with
				// CURLE 60 — measured against 3.2.10 and a real Policy API.
				for _, item := range []string{"ca_info_file", "certificate_file", "private_key_file", "check_cert = yes", "check_cert_cn = yes"} {
					if !strings.Contains(body, item) {
						t.Errorf("the rest module's %s section does not set %s, so the boundary is not mTLS", section, item)
					}
				}
			}
		})
	}
}

// TestThePerRequestTimeoutIsWhereFreeRADIUSReadsIt.
//
// Separate from the block above because it is a separate measurement with a
// separate consequence: a module-level `timeout` is dropped, and the per-section
// default that takes its place is 4 s — above the ~3 s NAS retransmit interval
// §2 puts the ceiling under. The number the caller asked for has to be *in the
// section*, not merely somewhere in the file.
func TestThePerRequestTimeoutIsWhereFreeRADIUSReadsIt(t *testing.T) {
	rest := configOnly(readGenerated(t, generate(t, func(c *Config) {
		c.PolicyAPITimeoutSeconds = 1
	}), "mods-available", "rest"))

	for _, section := range []string{"authenticate", "authorize"} {
		body := sectionBody(t, rest, section)
		if !strings.Contains(body, "timeout = 1") {
			t.Errorf("the rest module's %s section does not carry the configured timeout, "+
				"so rlm_rest uses its own default of 4 s:\n%s", section, body)
		}
	}
}

// TestBothRestSectionsCarryTheSameBoundary.
//
// `authenticate` and `authorize` are two copies of one decision, and rlm_rest
// gives them independent `tls` and `timeout` items. A repair that fixed one is
// a boundary that is authenticated for half the packets — and which half depends
// on the virtual server, which is a different file.
func TestBothRestSectionsCarryTheSameBoundary(t *testing.T) {
	rest := configOnly(readGenerated(t, generate(t, withPolicyMTLS), "mods-available", "rest"))

	strip := func(name string) string {
		body := sectionBody(t, rest, name)
		return strings.TrimSpace(strings.TrimPrefix(body, name+" {"))
	}
	if a, b := strip("authenticate"), strip("authorize"); a != b {
		t.Errorf("the two rest sections differ:\n--- authenticate ---\n%s\n--- authorize ---\n%s", a, b)
	}
}

// withPolicyMTLS is the deployment shape: an https Policy API and the three
// paths that make talking to it mutual.
func withPolicyMTLS(c *Config) {
	c.PolicyAPIURL = "https://foyer:8080/api/v1/aaa/authorize"
	c.PolicyClientCertFile = "/run/foyer/tls/radius.crt"
	c.PolicyClientKeyFile = "/run/foyer/tls/radius.key"
	c.PolicyServerCAFile = "/run/foyer/tls/ca.crt"
}

// TestTheGeneratorRefusesHalfOfTheMTLSBoundary.
//
// The ordering hazard, made unrepresentable. Turning on certificate checking
// without issuing the certificate rejects every Access-Request, and a tree that
// carries a certificate it will never present is a control that does nothing.
// Neither half can be written on its own.
func TestTheGeneratorRefusesHalfOfTheMTLSBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "https with no client certificate",
			mutate: func(c *Config) {
				withPolicyMTLS(c)
				c.PolicyClientCertFile = ""
			},
			want: "PolicyClientCertFile is empty",
		},
		{
			name: "https with no key",
			mutate: func(c *Config) {
				withPolicyMTLS(c)
				c.PolicyClientKeyFile = ""
			},
			want: "PolicyClientKeyFile is empty",
		},
		{
			name: "https with no CA to check the Policy API against",
			mutate: func(c *Config) {
				withPolicyMTLS(c)
				c.PolicyServerCAFile = ""
			},
			want: "PolicyServerCAFile is empty",
		},
		{
			name: "a client certificate that would never be presented",
			mutate: func(c *Config) {
				c.PolicyClientCertFile = "/run/foyer/tls/radius.crt"
				c.PolicyClientKeyFile = "/run/foyer/tls/radius.key"
				c.PolicyServerCAFile = "/run/foyer/tls/ca.crt"
			},
			want: "is plaintext, so the certificate would never be presented",
		},
		{
			name:   "a policy URL that is not a URL",
			mutate: func(c *Config) { c.PolicyAPIURL = "foyer-backend:8080" },
			want:   "is not an absolute http(s) URL",
		},
		{
			name:   "a policy URL with a scheme rlm_rest does not speak",
			mutate: func(c *Config) { c.PolicyAPIURL = "ftp://foyer-backend:8080/api/v1/aaa/authorize" },
			want:   "http and https are the only schemes that reach it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PostgresPassword = "pg-password-for-this-test-only"
			tc.mutate(&cfg)

			dir := t.TempDir()
			err := NewGenerator(cfg).Generate(dir)
			if err == nil {
				t.Fatal("the generator wrote a tree it should have refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal was %q, which does not name %q", err, tc.want)
			}
			// Validate runs before any file is written, so a refusal leaves
			// nothing behind for a later `-XC` to find and trust.
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("the refused generation left %d entries behind", len(entries))
			}
		})
	}
}

// configOnly strips FreeRADIUS comment lines.
//
// Not tidiness. The generated file's comments *quote* the wrong item names and
// the wrong placement, because saying what was measured is what the comments are
// for. An assertion that scanned the prose would be red for the file that
// explains the fix and green for the same file with the explanation deleted —
// a test that rewards removing the reasoning.
func configOnly(file string) string {
	var out []string
	for _, line := range strings.Split(file, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// sectionBody returns the text of one brace-delimited section of a generated
// module, so an assertion can say *where* a line is rather than only that the
// file contains it. Placement is the whole finding here.
func sectionBody(t *testing.T, file, name string) string {
	t.Helper()
	start := strings.Index(file, name+" {")
	if start < 0 {
		t.Fatalf("no %s section in the generated module", name)
	}
	depth := 0
	for i := start; i < len(file); i++ {
		switch file[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return file[start : i+1]
			}
		}
	}
	t.Fatalf("the %s section is not closed", name)
	return ""
}
