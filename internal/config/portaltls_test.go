package config

import (
	"strings"
	"testing"
)

// The optional HTTPS portal's startup assertions, each made to refuse.

func withPortalTLS() Config {
	c := valid()
	c.PortalTLSAddr = "0.0.0.0:8444"
	c.PortalTLSCert = "/run/foyer/portal-tls/portal.crt"
	c.PortalTLSKey = "/run/foyer/portal-tls/portal.key"
	return c
}

func TestAPortalWithACertificateStarts(t *testing.T) {
	c := withPortalTLS()
	if err := c.Validate(); err != nil {
		t.Fatalf("a valid HTTPS portal configuration was refused: %v", err)
	}
	if got := c.PortalHTTPSOrigin(); got != "https://portal.example" {
		t.Errorf("PortalHTTPSOrigin = %q, want https://portal.example", got)
	}
	if got := valid().PortalHTTPSOrigin(); got != "" {
		t.Errorf("PortalHTTPSOrigin = %q with no certificate; the HTTP listener would redirect to nothing", got)
	}
}

func TestThePortalTLSConfigurationIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		wants  []string
	}{
		{"with a certificate and no key", func(c *Config) { c.PortalTLSKey = "" }, []string{"FOYER_PORTAL_TLS_CERT", "FOYER_PORTAL_TLS_KEY"}},
		{"with a key and no certificate", func(c *Config) { c.PortalTLSCert = "" }, []string{"FOYER_PORTAL_TLS_CERT", "FOYER_PORTAL_TLS_KEY"}},
		// The HTTP listener redirects every guest to this origin and RFC 8908
		// hands it to the operating system; docs/architecture.md §7 requires
		// https, and the process
		// cannot know which name the certificate covers.
		{"with an http portal URL", func(c *Config) { c.PortalURL = "http://portal.example/en/" }, []string{"FOYER_PORTAL_URL", "https"}},
		{"with no portal URL", func(c *Config) { c.PortalURL = "" }, []string{"FOYER_PORTAL_URL"}},
		{"with a malformed TLS address", func(c *Config) { c.PortalTLSAddr = "8444" }, []string{"FOYER_PORTAL_TLS_ADDR"}},
		// Two listeners on one socket is the §7 separation failing again.
		{"on the console's port", func(c *Config) { c.PortalTLSAddr = "0.0.0.0:8443" }, []string{"FOYER_PORTAL_TLS_ADDR", "FOYER_ADMIN_ADDR"}},
		{"with an http venue URL beside an https portal", func(c *Config) { c.VenueURL = "http://portal.example/about" }, []string{"FOYER_VENUE_URL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := withPortalTLS()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate accepted it")
			}
			for _, w := range tc.wants {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not name %q: %v", w, err)
				}
			}
		})
	}
}

// An unused default TLS address must not collide with anything: nothing binds it.
func TestAnUnconfiguredPortalTLSAddressIsNotBound(t *testing.T) {
	c := valid()
	c.PortalTLSAddr = c.AdminAddr
	if err := c.Validate(); err != nil {
		t.Fatalf("the TLS address was checked although no certificate is configured: %v", err)
	}
}

func TestLoadNoLongerInventsAVenueURLOrAnHTTPSPortalURL(t *testing.T) {
	t.Setenv("FOYER_PORTAL_URL", "")
	t.Setenv("FOYER_VENUE_URL", "")
	t.Setenv("FOYER_PORTAL_TLS_CERT", "")
	t.Setenv("FOYER_PORTAL_TLS_KEY", "")
	c := Load()
	// A default such as `http://127.0.0.1:8081/venue` would be unreachable by any
	// guest, naming a path the portal does not serve, and a malformed-looking member
	// is enough for ChromeOS to discard the whole RFC 8908 answer.
	if c.VenueURL != "" {
		t.Errorf("VenueURL = %q, want it absent unless FOYER_VENUE_URL says otherwise", c.VenueURL)
	}
	if !strings.HasPrefix(c.PortalURL, "http://") {
		t.Errorf("the plain-HTTP portal's derived URL is %q", c.PortalURL)
	}

	t.Setenv("FOYER_PORTAL_TLS_CERT", "/c")
	t.Setenv("FOYER_PORTAL_TLS_KEY", "/k")
	if got := Load().PortalURL; got != "" {
		t.Errorf("with a certificate, PortalURL was derived as %q; only the operator knows the name it covers", got)
	}
}
