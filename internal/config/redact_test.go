package config

import (
	"strings"
	"testing"
)

// A startup refusal is printed to a log anybody with the container can read, and
// the console's writes answer with the same rule. Neither may repeat a URL's
// userinfo, which is the one part of a URL that can be a credential. Before this
// change httpOrigin quoted the value as given, and Postgres() wrapped a
// *url.Error — which repeats the whole database URL, password included.
func TestARefusedURLNeverRepeatsItsUserinfo(t *testing.T) {
	const secret = "s3cret-pw"
	for _, raw := range []string{
		"https://guest:" + secret + "@wifi.example/en/", // parses, has userinfo
		"guest:" + secret + "@wifi.example",             // parses as scheme "guest", no host
		"http://guest:" + secret + "@wifi example/",     // does not parse
	} {
		c := valid()
		c.PortalURL = raw
		err := c.Validate()
		if err == nil {
			t.Errorf("%q was accepted as a portal address", raw)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal of a portal URL repeats its password: %v", err)
		}
		if err := CheckWebAddress(raw); err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("CheckWebAddress(%q) = %v: accepted, or repeats the password", raw, err)
		}
	}

	// With TLS on, the https requirement quotes the URL too.
	c := valid()
	c.PortalTLSCert, c.PortalTLSKey, c.PortalTLSAddr = "/c.pem", "/k.pem", "0.0.0.0:8444"
	c.PortalURL = "http://guest:" + secret + "@wifi.example/en/"
	if err := c.Validate(); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("the https refusal: %v", err)
	}

	// Where nothing is secret the value is still shown, because a refusal that
	// does not say what it refused is one an operator cannot act on.
	c = valid()
	c.PortalURL = "ftp://wifi.example/"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "ftp://wifi.example/") {
		t.Errorf("a URL with no userinfo is no longer named in its refusal: %v", err)
	}

	d := Config{DatabaseURL: "postgres://foyer_app:" + secret + "@db:5x/foyer", RaddbPostgresPassword: "x"}
	if _, err := d.Postgres(); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("Postgres() on an unparseable URL: %v", err)
	}
}

// TestLoadCarriesWhatTheControlPlaneUsedToReadItself — the variables cmd/foyer
// needs, read through Load rather than on its own. A maintenance role URL that cannot be read is
// carried as that pool's error and is not a startup refusal: this process also
// answers every Wi-Fi sign-in.
func TestLoadCarriesWhatTheControlPlaneUsedToReadItself(t *testing.T) {
	t.Setenv("FOYER_DATABASE_URL", "postgres://foyer_app:a-generated-value@db:5432/foyer")
	t.Setenv("FOYER_POLICY_HOST", "foyer:8080")
	t.Setenv("FOYER_REAPER_DATABASE_URL", "postgres://foyer_reaper:r@db/foyer")
	t.Setenv("FOYER_RETENTION_DATABASE_URL", "postgres://foyer_retention:r@db/foyer")
	t.Setenv("FOYER_RETENTION_DATABASE_URL_FILE", "/nonexistent")
	t.Setenv("GOMEMLIMIT", "256MiB")

	c := Load()
	if c.PolicyHost != "foyer:8080" {
		t.Errorf("PolicyHost = %q", c.PolicyHost)
	}
	if c.ReaperDatabase.Value != "postgres://foyer_reaper:r@db/foyer" || c.ReaperDatabase.Err != nil {
		t.Errorf("ReaperDatabase = %+v", c.ReaperDatabase)
	}
	if c.RetentionDatabase.Err == nil {
		t.Error("a retention URL given twice was read without complaint")
	}
	if !c.GoMemLimitSet {
		t.Error("GOMEMLIMIT was set and GoMemLimitSet is false")
	}
	c.PortalURL = "https://portal.example/en/"
	if err := c.Validate(); err != nil {
		t.Errorf("a maintenance pool's configuration refused the start: %v", err)
	}
}
