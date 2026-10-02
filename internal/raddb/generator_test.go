package raddb

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// generate emits a tree into a temporary directory and returns its path.
func generate(t *testing.T, mutate func(*Config)) string {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.PostgresPassword = "pg-password-for-this-test-only"
	if mutate != nil {
		mutate(&cfg)
	}
	if err := NewGenerator(cfg).Generate(dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return dir
}

func readGenerated(t *testing.T, dir string, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{dir}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}

// blockRE finds a `client <name> {` ... `}` section at top level.
var clientBlockRE = regexp.MustCompile(`(?ms)^client\s+(\S+)\s*\{(.*?)^\}`)

// TestRequireMessageAuthenticatorSnapshot is one of the two named security
// snapshots in docs/rules/testing.md. It must not be deleted without a
// replacement that covers the same guarantee.
//
// The guarantee: every RADIUS client this server will ever talk to requires a
// Message-Authenticator, and limits Proxy-State. FreeRADIUS's own default is
// "auto", which decides from the first packet a client sends and therefore
// silently leaves the weakest gear unprotected (BlastRADIUS, CVE-2024-3596).
//
// Where the guarantee lives moved when clients moved to SQL, and the test moved
// with it. rlm_sql reads six columns for a client — id, nasname, shortname,
// type, secret, server — and there is no seventh, so a client loaded from the
// client table cannot carry the flag itself. It therefore has to hold in three
// places at once, and all three are asserted:
//
//  1. globally, in radiusd.conf's security section, which is what a client
//     loaded by read_clients inherits;
//  2. on every network section in clients.conf, which is what a dynamically
//     discovered client inherits;
//  3. explicitly on each discovered client, in the dynamic-clients server.
//
// Asserting only (1) would pass for a build where a client section overrides it
// back to "auto", which is exactly the shape of the defect this guards.
func TestRequireMessageAuthenticatorSnapshot(t *testing.T) {
	dir := generate(t, nil)

	// 1. The global default.
	radiusd := readGenerated(t, dir, "radiusd.conf")
	sec := sectionOf(t, radiusd, "security {")
	for _, want := range []string{
		"require_message_authenticator = yes",
		"limit_proxy_state = yes",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("radiusd.conf security section is missing %q\n--- section ---\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "require_message_authenticator = auto") {
		t.Errorf("radiusd.conf security section leaves require_message_authenticator on FreeRADIUS's auto default")
	}

	// 2. Every client section in clients.conf, whatever it is called.
	clients := readGenerated(t, dir, "clients.conf")
	blocks := clientBlockRE.FindAllStringSubmatch(clients, -1)
	if len(blocks) == 0 {
		t.Fatalf("clients.conf declares no client section at all; the dynamic networks are how a client is ever admitted\n%s", clients)
	}
	if len(blocks) != len(DefaultConfig().DynamicClientNetworks) {
		t.Errorf("clients.conf has %d client sections, the config declares %d networks",
			len(blocks), len(DefaultConfig().DynamicClientNetworks))
	}
	for _, b := range blocks {
		name, body := b[1], b[2]
		if !strings.Contains(body, "require_message_authenticator = yes") {
			t.Errorf("client %s does not require Message-Authenticator:\n%s", name, body)
		}
		if !strings.Contains(body, "limit_proxy_state = yes") {
			t.Errorf("client %s does not limit Proxy-State:\n%s", name, body)
		}
	}

	// 3. Each client discovered from the client table.
	dyn := readGenerated(t, dir, "sites-available", "dynamic-clients")
	if !strings.Contains(dyn, "&FreeRADIUS-Client-Require-MA = yes") {
		t.Errorf("dynamic-clients does not set Require-MA on the clients it admits:\n%s", dyn)
	}
	if strings.Contains(dyn, "&FreeRADIUS-Client-Require-MA = no") {
		t.Errorf("dynamic-clients admits clients that do not require Message-Authenticator")
	}
}

// TestPostAuthQueryNeverWritesPass is the second named security snapshot.
//
// Stock FreeRADIUS 3.2.x inserts '%{%{User-Password}:-%{Chap-Password}}' into
// radpostauth.pass on every authentication attempt, accepted or rejected. That
// is a cleartext credential store, populated by default, in a table the admin
// console reads — and it sits in the one place nothing else in this repository
// guards, because radpostauth is FreeRADIUS's table and the rule everywhere
// else is "do not write it".
//
// We generate the query, so this one is ours.
func TestPostAuthQueryNeverWritesPass(t *testing.T) {
	dir := generate(t, nil)
	queries := readGenerated(t, dir, "mods-config", "sql", "main", "postgresql", "queries.conf")

	idx := strings.Index(queries, "post-auth {")
	if idx == -1 {
		t.Fatalf("queries.conf has no post-auth section")
	}
	postAuth := queries[idx:]

	// The column list, parsed rather than substring-matched: "pass" appears
	// inside "password" and inside prose, and a test that cannot tell those
	// apart is one that goes green for the wrong reason in both directions.
	cols := insertColumns(t, postAuth)
	if len(cols) == 0 {
		t.Fatalf("could not read the post-auth INSERT column list from:\n%s", postAuth)
	}
	for _, c := range cols {
		if c == "pass" {
			t.Errorf("post-auth query writes radpostauth.pass, which is a cleartext credential store: columns %v", cols)
		}
	}

	for _, forbidden := range []string{"User-Password", "Chap-Password", "Password.Cleartext", "Password.NT"} {
		if strings.Contains(postAuth, forbidden) {
			t.Errorf("post-auth query references %s; nothing downstream may echo a submitted credential", forbidden)
		}
	}

	// The station-id columns are the other half. The stock query leaves them
	// empty even though the schema has them, so a stock deployment records no
	// MAC on any attempt — and the MAC is the only identifier an operator has
	// when a guest is standing in front of them.
	for _, want := range []string{"username", "reply", "authdate", "calledstationid", "callingstationid"} {
		if !contains(cols, want) {
			t.Errorf("post-auth query does not populate %s: columns %v", want, cols)
		}
	}
	for _, want := range []string{"%{Called-Station-Id}", "%{Calling-Station-Id}"} {
		if !strings.Contains(postAuth, want) {
			t.Errorf("post-auth query names %s in no value; the column would be written empty", want)
		}
	}
}

// TestGeneratedTreeHoldsNoSharedSecret is the invariant that made the two
// snapshots above testable at all: clients come from SQL, so there is no
// shared secret anywhere in the generated tree to get wrong.
func TestGeneratedTreeHoldsNoSharedSecret(t *testing.T) {
	dir := generate(t, nil)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.HasPrefix(trimmed, "secret") && strings.Contains(trimmed, "=") {
				t.Errorf("%s assigns a shared secret in generated output: %q", rel, trimmed)
			}
			for _, def := range knownDefaultSecrets {
				if strings.Contains(line, def) {
					t.Errorf("%s contains a known default secret", rel)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestClientsComeFromSQL pins the mechanism that makes registering an access
// point a database write.
func TestClientsComeFromSQL(t *testing.T) {
	dir := generate(t, func(c *Config) { c.ClientTable = "radius_clients" })

	sql := readGenerated(t, dir, "mods-available", "sql")
	if !strings.Contains(sql, "read_clients = yes") {
		t.Errorf("rlm_sql does not read clients:\n%s", sql)
	}
	if !strings.Contains(sql, `client_table = "radius_clients"`) {
		t.Errorf("rlm_sql does not use the configured client table:\n%s", sql)
	}

	queries := readGenerated(t, dir, "mods-config", "sql", "main", "postgresql", "queries.conf")
	if !strings.Contains(queries, "client_query = ") {
		t.Errorf("queries.conf has no client_query; read_clients has nothing to run")
	}
	if !strings.Contains(queries, "FROM radius_clients") {
		t.Errorf("client_query does not read the configured table:\n%s", queries)
	}

	// Six columns, in the order rlm_sql maps positionally. Getting the order
	// wrong hands FreeRADIUS the shortname as a secret and it starts anyway.
	if !strings.Contains(queries, "SELECT id, nasname, shortname, type, secret, server FROM radius_clients") {
		t.Errorf("client_query is not the six columns rlm_sql reads, in order:\n%s", queries)
	}

	dyn := readGenerated(t, dir, "sites-available", "dynamic-clients")
	if !strings.Contains(dyn, "FROM radius_clients") {
		t.Errorf("dynamic-clients does not look up the configured table:\n%s", dyn)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sites-enabled", "dynamic-clients")); err != nil {
		t.Errorf("dynamic-clients is not enabled: %v", err)
	}
}

// TestValidateRefusesUnsafeConfigs covers the refusals that stop a bad tree
// reaching disk at all.
func TestValidateRefusesUnsafeConfigs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no dynamic networks", func(c *Config) { c.DynamicClientNetworks = nil }, "restart"},
		{"host route as a network", func(c *Config) {
			c.DynamicClientNetworks = []DynamicNetwork{{Name: "one", CIDR: "192.0.2.1/32"}}
		}, "/32"},
		{"client table is not an identifier", func(c *Config) { c.ClientTable = "nas; DROP TABLE radacct" }, "identifier"},
		{"duplicate network name", func(c *Config) {
			c.DynamicClientNetworks = []DynamicNetwork{
				{Name: "one", CIDR: "10.0.0.0/8"},
				{Name: "one", CIDR: "192.168.0.0/16"},
			}
		}, "duplicate"},
		{"default postgres password", func(c *Config) { c.PostgresPassword = "testing" + "123" }, "known default"},
		{"no postgres password", func(c *Config) { c.PostgresPassword = "" }, "does not start"},
		{"EAP without a certificate", func(c *Config) {
			c.EAP = EAPConfig{Enabled: true, DefaultEAPType: "tls"}
		}, "CertDir"},
		// The two blocked methods are refused BY NAME with the reason, not with
		// "not tls". An operator who asks for PEAP is asking a
		// credential-storage question, and an error that says "this phase" tells
		// them to wait for a phase that cannot arrive on its own.
		{"PEAP names the credential it cannot get", func(c *Config) {
			c.EAP = EAPConfig{Enabled: true, DefaultEAPType: "peap", CertDir: "/c", ServerCertFile: "/c/s", PrivateKeyFile: "/c/k", CAFile: "/c/ca"}
		}, "NT hash in the clear"},
		{"TTLS names the measurement that blocks it", func(c *Config) {
			c.EAP = EAPConfig{Enabled: true, DefaultEAPType: "ttls", CertDir: "/c", ServerCertFile: "/c/s", PrivateKeyFile: "/c/k", CAFile: "/c/ca"}
		}, "cannot check argon2"},
		{"an EAP type nobody has thought about", func(c *Config) {
			c.EAP = EAPConfig{Enabled: true, DefaultEAPType: "fast", CertDir: "/c", ServerCertFile: "/c/s", PrivateKeyFile: "/c/k", CAFile: "/c/ca"}
		}, "not emitted by this phase"},
		{"a session cache path that radiusd cannot resolve", func(c *Config) {
			c.EAP = EAPConfig{Enabled: true, DefaultEAPType: "tls", CertDir: "/c", ServerCertFile: "/c/s", PrivateKeyFile: "/c/k", CAFile: "/c/ca",
				TLSSessionCacheDir: "tlscache"}
		}, "is not absolute"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PostgresPassword = "pg-password-for-this-test-only"
			tc.mutate(&cfg)
			dir := t.TempDir()
			err := NewGenerator(cfg).Generate(dir)
			if err == nil {
				t.Fatalf("Generate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Errorf("a rejected config left %d entries behind; validation must run before any file is written", len(entries))
			}
		})
	}
}

// TestPasswordlessPostgresIsAllowedWhenItIsSaidOutLoud: the refusal above is
// about an accident, not about the deployment that authenticates to PostgreSQL
// with a client certificate or over a socket. Those exist, and refusing them
// outright would be the wider reading of the rule rather than the true one.
func TestPasswordlessPostgresIsAllowedWhenItIsSaidOutLoud(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PostgresPassword = ""
	cfg.AllowPasswordlessPostgres = true
	if err := NewGenerator(cfg).Generate(t.TempDir()); err != nil {
		t.Fatalf("Generate refused a deliberately passwordless configuration: %v", err)
	}
}

// TestEAPIsOffByDefaultAndPrunedWhenTurnedOff covers the stale-symlink case: a
// mods-enabled entry is a live configuration file, so removing EAP from the
// Config has to remove it from the tree.
func TestEAPIsOffByDefaultAndPrunedWhenTurnedOff(t *testing.T) {
	dir := t.TempDir()

	on := DefaultConfig()
	on.PostgresPassword = "pg-password-for-this-test-only"
	on.EAP = EAPConfig{
		Enabled: true, DefaultEAPType: "tls", CertDir: "/certs",
		ServerCertFile: "/certs/server.pem", PrivateKeyFile: "/certs/server.key", CAFile: "/certs/ca.pem",
	}
	if err := NewGenerator(on).Generate(dir); err != nil {
		t.Fatalf("Generate with EAP: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "mods-enabled", "eap")); err != nil {
		t.Fatalf("eap not enabled when EAP.Enabled is true: %v", err)
	}

	off := DefaultConfig()
	off.PostgresPassword = "pg-password-for-this-test-only"
	if err := NewGenerator(off).Generate(dir); err != nil {
		t.Fatalf("Generate without EAP: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "mods-enabled", "eap")); err == nil {
		t.Errorf("eap is still enabled after EAP was turned off; a stale mods-enabled entry is a live module")
	}
	def := readGenerated(t, dir, "sites-available", "default")
	if strings.Contains(def, "\n\t\teap {") {
		t.Errorf("default site still calls eap with the module gone:\n%s", def)
	}
}

// TestEAPReturnsBeforeThePolicyEngineCanOverwriteAuthType pins a detail that
// is invisible and load-bearing.
//
// rlm_eap answers `updated` for the packet where it sets Auth-Type and `ok` for
// the continuation packets. The stock configuration returns on `ok` only,
// because it has a user lookup to fall through to. This one does not: the next
// module is rlm_rest, and it sets Auth-Type itself. Returning on `ok` alone
// would therefore overwrite Auth-Type on the first packet of every EAP
// conversation, and the symptom would be an Access-Reject for a supplicant that
// never got to present anything.
func TestEAPReturnsBeforeThePolicyEngineCanOverwriteAuthType(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.PostgresPassword = "pg-password-for-this-test-only"
	cfg.EAP = EAPConfig{
		Enabled: true, DefaultEAPType: "tls", CertDir: "/certs",
		ServerCertFile: "/certs/server.pem", PrivateKeyFile: "/certs/server.key", CAFile: "/certs/ca.pem",
	}
	if err := NewGenerator(cfg).Generate(dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	site := readGenerated(t, dir, "sites-available", "default")
	authorize := sectionOf(t, site, "authorize {")

	eapAt := strings.Index(authorize, "eap {")
	restAt := strings.Index(authorize, "\n\t\trest {")
	if eapAt == -1 || restAt == -1 {
		t.Fatalf("authorize does not call both eap and rest:\n%s", authorize)
	}
	if eapAt > restAt {
		t.Errorf("rest runs before eap; the policy engine would set Auth-Type first")
	}

	eapBlock := authorize[eapAt:restAt]
	for _, want := range []string{"ok = return", "updated = return"} {
		if !strings.Contains(eapBlock, want) {
			t.Errorf("eap block does not carry %q:\n%s", want, eapBlock)
		}
	}
}

// TestPolicyFailureAndPolicyDenialAreDifferentBranches.
//
// From outside they are the same Access-Reject, and telling them apart is the
// difference between a guest typing the wrong voucher and the control plane
// being down. The rule this serves is that fail behaviour is stated rather than
// left to unlang's default; the default here is closed.
func TestPolicyFailureAndPolicyDenialAreDifferentBranches(t *testing.T) {
	dir := generate(t, nil)
	authorize := sectionOf(t, readGenerated(t, dir, "sites-available", "default"), "authorize {")

	// `invalid` does NOT belong with `fail`; an assertion requiring that it did
	// would hold a defect in place.
	// `invalid` is the Policy API answering 400 because it could not read the
	// question, and the question is built from client-chosen values: one
	// control byte in a User-Name made it unreadable, and at a fail-open site
	// the failure branch is an accept. See
	// TestAnUnreadableQuestionIsRefusedNotFailedOpen.
	if !strings.Contains(authorize, "elsif (fail) {") {
		t.Errorf("authorize has no branch for the policy engine not answering:\n%s", authorize)
	}
	for _, want := range []string{"\"policy engine unavailable\"", "\"denied by policy\""} {
		if !strings.Contains(authorize, want) {
			t.Errorf("authorize does not distinguish the two rejects: missing %s", want)
		}
	}

	// Every branch that is not an accept rejects. Fail-open is a per-site
	// setting this generator does not yet receive, and a generator that quietly
	// accepted on failure would be the worst possible reading of "configurable".
	//
	// Five: the failure branch splits on EAP presence, because 802.1X fails
	// closed whatever the site is configured for (two); a denial (one); and,
	// the allow branch itself rejects twice -- an account allow with
	// no verifiable password, and an allow that does not say what it allowed.
	if got := strings.Count(authorize, "Auth-Type := Reject"); got != 5 {
		t.Errorf("expected five rejecting branches, got %d:\n%s", got, authorize)
	}
	if !strings.Contains(authorize, "if (&EAP-Message) {") {
		t.Errorf("the failure branch does not classify 802.1X on EAP presence:\n%s", authorize)
	}
	// Not a Service-Type heuristic. Service-Type is NAS-supplied, varies by
	// vendor and is absent more often than not; "the request carries EAP" is
	// the narrow reading and the true one.
	if strings.Contains(authorize, "Service-Type ==") {
		t.Errorf("802.1X is being detected from Service-Type:\n%s", authorize)
	}
}

// TestPolicyRequestCarriesNoCredentialAndNoEAPBlob.
//
// The rest module's body is the one place a credential could leave the data
// plane, and it would leave silently. rlm_rest carries authorization decisions
// in both directions: the question may not contain a password, and it may not
// contain the EAP conversation either, because an EAP conversation is not an
// authorization decision and Go is not in it.
func TestPolicyRequestCarriesNoCredentialAndNoEAPBlob(t *testing.T) {
	dir := generate(t, nil)
	rest := readGenerated(t, dir, "mods-available", "rest")

	for _, forbidden := range []string{
		"User-Password", "Chap-Password", "CHAP-Password",
		"MS-CHAP", "Password.Cleartext", "Password.NT",
		"%{EAP-Message}",
	} {
		if strings.Contains(rest, forbidden) {
			t.Errorf("the Policy API request carries %s; rlm_rest carries decisions, not credentials", forbidden)
		}
	}

	// What it must carry, and where each field comes from.
	for _, want := range []string{
		`"user_name_hex":"%{hex:&User-Name}"`,
		`"outer_user_name_hex":"%{hex:&User-Name}"`,
		`"eap_message":"%{jsonquote:%{Tmp-String-0}}"`,
		`"radius_client":"%{jsonquote:%{client:shortname}}"`,
	} {
		if !strings.Contains(rest, want) {
			t.Errorf("the Policy API request is missing %s", want)
		}
	}

	// The timeout budget. A NAS retransmits at roughly 3 s, and a second copy
	// of a request whose first copy is still blocking an rlm_rest worker is the
	// worst possible time to be asked again.
	if !strings.Contains(rest, "connect_timeout = 0.5") {
		t.Errorf("connect_timeout is not 0.5 s:\n%s", rest)
	}
	// Comments stripped: this file explains why start = 5 is wrong, and a
	// substring check that cannot tell an explanation from a setting would go
	// red on the explanation and green on the setting.
	if strings.Contains(uncommented(rest), "start = 5") {
		t.Errorf("the rest pool opens connections at startup, which makes the control plane a start-time dependency of the data plane")
	}
}

// sectionOf returns the text of the brace-delimited section starting at header.
func sectionOf(t *testing.T, s, header string) string {
	t.Helper()
	i := strings.Index(s, header)
	if i == -1 {
		t.Fatalf("no %q section", header)
	}
	depth := 0
	inComment := false
	for j := i; j < len(s); j++ {
		switch {
		case s[j] == '\n':
			inComment = false
		case inComment:
			// A brace in a comment is prose, not structure. Without this the
			// reader silently returns a truncated section and every assertion
			// on it passes for the wrong reason.
		case s[j] == '#':
			inComment = true
		case s[j] == '{':
			depth++
		case s[j] == '}':
			depth--
			if depth == 0 {
				return s[i : j+1]
			}
		}
	}
	t.Fatalf("%q section is not closed", header)
	return ""
}

var insertColsRE = regexp.MustCompile(`(?s)INSERT INTO\s+radpostauth\s*\((.*?)\)`)

// insertColumns reads the column list out of the post-auth INSERT, stripping
// the line continuations FreeRADIUS's configuration format uses.
func insertColumns(t *testing.T, postAuth string) []string {
	t.Helper()
	m := insertColsRE.FindStringSubmatch(postAuth)
	if m == nil {
		return nil
	}
	raw := strings.NewReplacer("\\", " ", "\n", " ", "\t", " ").Replace(m[1])
	var cols []string
	for _, part := range strings.Split(raw, ",") {
		if c := strings.TrimSpace(part); c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

// uncommented drops comment lines, so a check on a setting cannot be satisfied
// or broken by prose that mentions the setting.
func uncommented(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
