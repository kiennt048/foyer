package config

import (
	"strings"
	"testing"
)

// The startup assertions, each one made to fail.
//
// `docs/rules/security.md` §7 asks for "a startup assertion, not a doc note", and
// the difference between the two is entirely whether anybody has watched it refuse.
// Every case below constructs the configuration that must be rejected and asserts
// the rejection **names the variable to change** — a refusal an operator cannot act
// on is a refusal they work around.

// valid is a configuration that starts, so that each case below changes exactly one
// thing and the failure is attributable.
func valid() Config {
	return Config{
		DatabaseURL: "postgres://foyer:a-generated-value@db:5432/foyer?sslmode=require",
		PortalAddr:  "0.0.0.0:8081",
		AdminAddr:   "127.0.0.1:8443",
		PolicyAddr:  "127.0.0.1:8080",
		PortalURL:   "https://portal.example/en/",
	}
}

func TestAValidConfigurationStarts(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("the baseline configuration was refused, so every case below is measuring the wrong thing: %v", err)
	}
}

func TestItRefusesToStart(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		// wants is what the message has to name, because a refusal that does not
		// say which variable to change is one an operator disables rather than
		// fixes.
		wants []string
	}{
		{
			// There is no default connection string. A fallback to a known lab
			// URL is a deployment quietly running against whatever answers on
			// 5432.
			name:   "with no database",
			mutate: func(c *Config) { c.DatabaseURL = "" },
			wants:  []string{"FOYER_DATABASE_URL"},
		},
		{
			// §7. Default credentials in a compose file are the most common way
			// appliance software gets mass-owned, and the check runs on every
			// start rather than behind an environment name — a security check
			// gated on a variable being set correctly is a security check that
			// does not run.
			name:   "on the lab's known password",
			mutate: func(c *Config) { c.DatabaseURL = "postgres://foyer:foyer_lab_secret@db:5432/foyer" },
			wants:  []string{"foyer_lab_secret", LabInsecureVar},
		},
		{
			name:   "on a shipped postgres default",
			mutate: func(c *Config) { c.DatabaseURL = "postgres://postgres:postgres@db:5432/foyer" },
			wants:  []string{LabInsecureVar},
		},
		{
			// §2, and the one that is easiest to leave as a doc note. The Policy
			// API carries authorization decisions between FreeRADIUS and this
			// process; on a shared bridge network that is a wire anybody on the
			// network can read and answer.
			name:   "with a plaintext Policy API on a wildcard bind",
			mutate: func(c *Config) { c.PolicyAddr = "0.0.0.0:8080" },
			wants:  []string{"FOYER_POLICY_TLS_CERT", LabInsecureVar},
		},
		{
			name:   "with a plaintext Policy API on a routable address",
			mutate: func(c *Config) { c.PolicyAddr = "10.0.0.5:8080" },
			wants:  []string{"FOYER_POLICY_TLS_CERT"},
		},
		{
			name:   "with half a TLS configuration",
			mutate: func(c *Config) { c.PolicyTLSCert = "/etc/foyer/policy.crt" },
			wants:  []string{"FOYER_POLICY_TLS_KEY"},
		},
		{
			// §7 again: the admin API must not be mounted on a guest-facing
			// listener's mux. A guest-reachable
			// origin that also answers `/api/v0` shares a cookie jar with the
			// console.
			name:   "with the console and the portal on one port",
			mutate: func(c *Config) { c.AdminAddr = "0.0.0.0:8081" },
			wants:  []string{"FOYER_ADMIN_ADDR", "FOYER_PORTAL_ADDR"},
		},
		{
			// A wildcard and a loopback bind on the same port are the same
			// socket in every way that matters, so the collision has to be
			// caught by more than a string comparison.
			name:   "with a wildcard colliding with a loopback bind",
			mutate: func(c *Config) { c.PolicyAddr = "0.0.0.0:8443"; c.PolicyTLSCert = "c"; c.PolicyTLSKey = "k" },
			wants:  []string{"FOYER_ADMIN_ADDR"},
		},
		{
			name:   "on an address that is not host:port",
			mutate: func(c *Config) { c.AdminAddr = "8082" },
			wants:  []string{"FOYER_ADMIN_ADDR"},
		},
		{
			// §4's scheme allowlist, applied to the value RFC 8908 hands the
			// guest's operating system. A `file://` there is a URL a phone is
			// told to open.
			name:   "on a portal URL that is not http",
			mutate: func(c *Config) { c.PortalURL = "file:///etc/passwd" },
			wants:  []string{"FOYER_PORTAL_URL"},
		},
		{
			name:   "on a portal URL carrying userinfo",
			mutate: func(c *Config) { c.PortalURL = "https://user:pw@portal.example/en/" },
			wants:  []string{"FOYER_PORTAL_URL"},
		},
		{
			// §2 asks for **mTLS**, and this is the half that looks finished:
			// a certificate, no client CA, on a bridge network — an encrypted
			// Policy API that answers an authorization question for anything
			// that can route to the port.
			name: "with an encrypted Policy API that does not check who is calling",
			mutate: func(c *Config) {
				c.PolicyAddr = "0.0.0.0:8080"
				c.PolicyTLSCert = "/run/foyer/tls/policy.crt"
				c.PolicyTLSKey = "/run/foyer/tls/policy.key"
			},
			wants: []string{"FOYER_POLICY_CLIENT_CA", "§2"},
		},
		{
			// And the lab acknowledgement does not lift it. The lab issues its
			// own pair rather than stepping around the boundary, because the lab
			// is the only place a real FreeRADIUS ever reads the generated
			// rlm_rest module.
			name: "with the lab acknowledgement set, which does not lift mTLS",
			mutate: func(c *Config) {
				c.PolicyAddr = "0.0.0.0:8080"
				c.PolicyTLSCert = "/run/foyer/tls/policy.crt"
				c.PolicyTLSKey = "/run/foyer/tls/policy.key"
				c.LabInsecure = true
			},
			wants: []string{"FOYER_POLICY_CLIENT_CA"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("started, and this configuration must be refused")
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q, so an operator cannot act on it: %v", want, err)
				}
			}
		})
	}
}

// TestTheLabAcknowledgementIsExactAndNarrow.
//
// It is spelled as a sentence rather than as a boolean so that it cannot be set by
// habit, and it lifts exactly two refusals — the lab's known password and a
// plaintext Policy API — and nothing else. An escape hatch that widened to cover a
// third would be the reason the other assertions stopped being assertions.
func TestTheLabAcknowledgementIsExactAndNarrow(t *testing.T) {
	lab := valid()
	lab.DatabaseURL = "postgres://foyer:foyer_lab_secret@postgres:5432/foyer?sslmode=disable"
	lab.PolicyAddr = "0.0.0.0:8080"
	lab.LabInsecure = true
	if err := lab.Validate(); err != nil {
		t.Fatalf("the lab's own configuration is refused even with the acknowledgement set: %v", err)
	}

	// Anything other than the exact sentence is not the acknowledgement, which is
	// what LoadConfig compares. A truthy-looking value must not work.
	for _, near := range []string{"true", "1", "yes", "i-am-a-lab", strings.ToUpper(LabAcknowledgement)} {
		t.Setenv(LabInsecureVar, near)
		if Load().LabInsecure {
			t.Errorf("%q was accepted as the lab acknowledgement", near)
		}
	}
	t.Setenv(LabInsecureVar, LabAcknowledgement)
	if !Load().LabInsecure {
		t.Fatal("the exact acknowledgement was not accepted")
	}

	// It does not lift the address collision, which is not a lab-versus-deployment
	// question: two listeners on one port is broken everywhere.
	collide := lab
	collide.AdminAddr = "0.0.0.0:8081"
	if err := collide.Validate(); err == nil {
		t.Error("the lab acknowledgement lifted the three-listener requirement, which it must never do")
	}
}

// TestWarningsAreSaidRatherThanRefused.
//
// Three conditions are worth a line at every start and are not refusals, and the
// difference is whether the deployment is wrong or merely worth looking at. An
// absent `FOYER_KEK` in particular is deliberately not fatal: `internal/store`
// returns `ErrNoKeyring` by name from the three methods that need one, so the
// refusal happens where the key is needed rather than in front of everything that
// does not.
func TestWarningsAreSaidRatherThanRefused(t *testing.T) {
	c := valid()
	c.KEK = ""
	c.AdminAddr = "0.0.0.0:8443"
	c.LabInsecure = true

	if err := c.Validate(); err != nil {
		t.Fatalf("a condition worth a warning was made a refusal: %v", err)
	}
	joined := strings.Join(c.Warnings(), "\n")
	for _, want := range []string{"FOYER_KEK", "FOYER_ADMIN_ADDR", LabInsecureVar} {
		if !strings.Contains(joined, want) {
			t.Errorf("nothing warns about %s:\n%s", want, joined)
		}
	}

	// And a configuration with nothing to say says nothing, so the warnings stay
	// worth reading.
	if w := valid().Warnings(); len(w) != 1 || !strings.Contains(w[0], "FOYER_KEK") {
		t.Errorf("the baseline warns about more than the absent key: %v", w)
	}
}

// TestLoopbackOnly is the predicate the §2 assertion turns on, so it is pinned
// separately: a wildcard bind includes loopback and is not loopback, and reading it
// as loopback would make the whole assertion vacuous.
func TestLoopbackOnly(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		"0.0.0.0:8080":   false,
		"[::]:8080":      false,
		":8080":          false,
		"10.0.0.5:8080":  false,
		"nonsense":       false,
	}
	for addr, want := range cases {
		if got := LoopbackOnly(addr); got != want {
			t.Errorf("LoopbackOnly(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestTheConsoleCertificatePairIsAllOrNothing covers the console pair's refusal,
// and it covers the policy pair's identical refusal beside it —
// which had no test at all until this one, in a package whose entire job is to
// turn a misconfiguration into a message.
//
// The console pair's stakes are higher than the policy pair's, and that is why
// this exists rather than being left as obvious. `internal/tlsca` **writes**
// these when they are absent, so half a pair would have the process generate a
// certificate beside a key it was told to use: two halves of two different
// identities, and a console that presents a certificate whose key it does not
// hold. The policy pair can only fail to load.
//
// Empty-and-empty is not an error and must not become one: it is how a
// developer's `go run ./cmd/foyer` gets HTTPS on a certificate generated in
// memory, which is the whole reason no default path is baked in.
func TestTheConsoleCertificatePairIsAllOrNothing(t *testing.T) {
	base := func() Config {
		return Config{
			DatabaseURL: "postgres://u:p@localhost:5432/foyer",
			PortalAddr:  "0.0.0.0:8081",
			AdminAddr:   "127.0.0.1:8443",
			// Loopback so the *policy* refusal does not fire and mask this one:
			// a fixture that trips a neighbouring rule tests the neighbour.
			PolicyAddr: "127.0.0.1:8080",
			PortalURL:  "http://portal.example",
		}
	}
	for _, tc := range []struct {
		name       string
		cert, key  string
		wantRefuse bool
	}{
		{"neither is the developer's path and must be allowed", "", "", false},
		{"both is what compose sets", "/x/admin.crt", "/x/admin.key", false},
		{"certificate without a key", "/x/admin.crt", "", true},
		{"key without a certificate", "", "/x/admin.key", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			c.AdminTLSCert, c.AdminTLSKey = tc.cert, tc.key
			err := c.Validate()
			if tc.wantRefuse && err == nil {
				t.Fatalf("cert=%q key=%q was accepted; half a pair makes the process "+
					"generate a certificate beside a key it was told to use", tc.cert, tc.key)
			}
			if !tc.wantRefuse && err != nil {
				t.Fatalf("cert=%q key=%q was refused: %v", tc.cert, tc.key, err)
			}
			if tc.wantRefuse && !strings.Contains(err.Error(), "FOYER_ADMIN_TLS_CERT") {
				t.Errorf("the refusal is %q and does not name the variable an operator "+
					"would have to set: a message that does not name its subject is a "+
					"message somebody has to go and find", err)
			}
		})
	}
}

// ── the console's default reachability, which is a decision and not a value ──
//
// A management console has to be there by default — without it an operator has
// no way to use the product, and an SSH tunnel is not an operator interface — so
// the default binds to every interface, on 8443.
//
// Changing that default would be a product decision, and without a pin the port
// and the binding could both move with a green suite. These pin it in the
// direction the decision points.

func TestTheConsoleIsReachableByDefault(t *testing.T) {
	t.Setenv("FOYER_ADMIN_ADDR", "")
	got := Load().AdminAddr
	if LoopbackOnly(got) {
		t.Errorf("FOYER_ADMIN_ADDR defaults to %q, which only this machine can reach: the console is the product's only interface and a default nobody can open is not one", got)
	}
	if !strings.HasSuffix(got, ":8443") {
		t.Errorf("FOYER_ADMIN_ADDR defaults to %q; the console is HTTPS and 8443 is the port the deployment publishes, so a second number here is a console on a port nothing forwards", got)
	}
}

func TestDeclaredNamesAcceptBothFormsTheyArriveIn(t *testing.T) {
	// "hostname -I" emits space-separated with a trailing space; a human writing
	// compose.yaml writes commas. Both reach this variable.
	for _, in := range []string{
		"192.0.2.10 server ",
		"192.0.2.10,server",
		" 192.0.2.10 , server ",
	} {
		t.Setenv("FOYER_ADMIN_TLS_NAMES", in)
		got := Load().AdminTLSNames
		if len(got) != 2 || got[0] != "192.0.2.10" || got[1] != "server" {
			t.Errorf("FOYER_ADMIN_TLS_NAMES=%q parsed to %q; a mangled name becomes a subjectAltName nothing matches", in, got)
		}
	}
	t.Setenv("FOYER_ADMIN_TLS_NAMES", "   ")
	if got := Load().AdminTLSNames; got != nil {
		t.Errorf("whitespace parsed to %q rather than nothing", got)
	}
}

func TestABoundConsoleWithNoDeclaredNamesSaysSo(t *testing.T) {
	c := valid()
	c.AdminAddr = "0.0.0.0:8443"

	found := false
	for _, w := range c.Warnings() {
		if strings.Contains(w, "FOYER_ADMIN_TLS_NAMES") {
			found = true
		}
	}
	if !found {
		t.Error("a console bound to a network with no declared names produced no warning: inside a container its certificate omits the address it is published on, and the only symptom is a browser refusing the name")
	}

	// And it must go quiet once they are declared, or it is a line operators learn
	// to scroll past.
	c.AdminTLSNames = []string{"192.0.2.10"}
	for _, w := range c.Warnings() {
		if strings.Contains(w, "FOYER_ADMIN_TLS_NAMES") {
			t.Errorf("the warning still fires with names declared: %q", w)
		}
	}
}
