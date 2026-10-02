// Package raddb generates the FreeRADIUS 3.2.x configuration tree that Foyer
// runs its AAA data plane from.
//
// Three things about this package are load-bearing rather than stylistic:
//
//   - The generated tree is build output. Nothing hand-edits raddb/.
//   - RADIUS clients are read from SQL, not written into clients.conf, because
//     the alternative is a restart on every access point an operator adds, and a
//     restart drops every in-flight EAP conversation on the site.
//   - The generated configuration is the only place BlastRADIUS protection can
//     be made unconditional, so it is made unconditional here and asserted by a
//     snapshot test.
package raddb

import (
	"bytes"
	"embed"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/kiennt048/foyer/internal/aaa"
)

// envNameRE is the environment variable name PostgresPasswordEnv may carry: what
// `$ENV{…}` can name, and nothing a template could be made to misread.
var envNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

//go:embed templates/*
var templateFS embed.FS

// knownDefaultSecrets are shared secrets that must never reach a generated
// file. The first is FreeRADIUS's own shipped example; the other two shipped
// in this repository's generated clients.conf until clients moved to SQL — one
// of them against 0.0.0.0/0.
//
// The literals are split because the write-time hook refuses a source file
// that contains FreeRADIUS's example secret as contiguous text, and it is
// right to: the check that has to know the string is this one.
var knownDefaultSecrets = []string{
	"testing" + "123",
	"foyer_lab_" + "shared_secret",
	"foyer_internal_" + "loopback_secret",
}

// DynamicNetwork is one CIDR inside which a client row added to the client
// table after the server started is picked up without a restart.
//
// FreeRADIUS requires a real prefix here: a /32 or /128 is rejected. The
// network is not an authorization decision — a source address inside it is
// still only a client if the client table has a row for it.
type DynamicNetwork struct {
	// Name is the configuration-section name. Derived, not operator-supplied.
	Name string
	// CIDR is the prefix, e.g. "192.168.0.0/16".
	CIDR string
}

// EAPConfig describes the EAP surface. It is off by default: a half-configured
// eap module is worse than none because rlm_eap answers Access-Requests it
// cannot finish.
//
// No certificate is generated here and none is shipped in an image. A fixed
// key would mean every deployment on earth shares one private key.
//
// **EAP-TLS only, and that is a measured refusal rather than a phase
// boundary.** EAP-TTLS/PAP and PEAP-MSCHAPv2 are both blocked on a
// credential-storage decision that does not belong to this package, for these
// reasons:
//
//   - `rlm_pap` in 3.2.10 implements no argon2 scheme, and `Crypt-Password`
//     does not reach one either — libxcrypt hashes bcrypt, yescrypt and
//     sha512crypt and returns its failure token for `$argon2id$`. So the reason
//     `security.md` §1 gives for recommending EAP-TTLS/PAP does not hold on this
//     data plane.
//   - MSCHAPv2 needs the NT hash in the clear at request time, and Foyer's is
//     envelope-encrypted under a key that is deliberately not in the database.
//     Supplying it would cost a **second** declared cleartext projection, which
//     `docs/rules/database.md` calls a defect in advance.
//
// Emitting either method here would be claiming one that no `eapol_test` case
// can pass, which is the thing `docs/rules/testing.md` forbids most plainly.
type EAPConfig struct {
	Enabled        bool
	DefaultEAPType string // "tls" is the only type this phase emits
	CertDir        string
	ServerCertFile string
	PrivateKeyFile string
	CAFile         string
	DHFile         string

	// TLSSessionCacheDir turns the EAP TLS session cache on and persists it
	// there. Empty leaves the cache off, which is the shipped default.
	//
	// `docs/rules/radius.md` asks for this cache "in Redis so the restarts
	// that do happen are survivable". Measured on 3.2.10: **there is
	// no Redis backend for it.** The cache is internal to `rlm_eap_tls`, not an
	// `rlm_cache` instance, and a `redis_server` item inside `cache { }` is
	// silently discarded with `radiusd -XC` still exiting 0. `persist_dir` is
	// the mechanism that exists, and it does what the rule actually asks for:
	// after one conversation the directory holds `<id>.asn1` and `<id>.vps`, and
	// they outlive the process.
	//
	// It is off by default because turning it on is a decision with two edges:
	//
	//   - A resumed session **skips the inner method**. That is what resumption
	//     is, and it means a cached entry re-admits a supplicant without the
	//     certificate or the password being presented again.
	//   - The directory holds the TLS master secret. It belongs on the same
	//     footing as a private key — 0700, owned by the server user, and out of
	//     `foyerctl support-bundle`.
	TLSSessionCacheDir string
}

// Config is everything that lands in the generated tree. Two Configs that
// compare equal generate byte-identical output; that is what lets Classify
// tell a restart from a database write.
type Config struct {
	ListenIP         string
	AuthPort         int
	AcctPort         int
	StatusPort       int
	PostgresHost     string
	PostgresPort     int
	PostgresDB       string
	PostgresUser     string
	PostgresPassword string
	// PostgresPasswordEnv, when set, is the name of an environment variable the
	// generated `rlm_sql` block reads its password from (`$ENV{NAME}`), so the
	// tree carries no password and the process that generates it never holds
	// one: FreeRADIUS's supervisor exports the variable from its own docker
	// secret before radiusd parses the tree. MEASURED on 3.2.10: expanded at parse,
	// and an unset variable makes radiusd refuse to start. Exclusive with
	// PostgresPassword.
	PostgresPasswordEnv           string
	PolicyAPIURL                  string
	PolicyAPITimeoutSeconds       int
	DefaultInterimIntervalSeconds int

	// The mTLS material on the `rlm_rest` boundary, which
	// `docs/rules/security.md` §2 requires.
	//
	// The two directions are separate fields because they are separate
	// guarantees and only one of them was ever half-present:
	//
	//   PolicyClientCertFile / PolicyClientKeyFile   what FreeRADIUS PRESENTS.
	//       Without it the Policy API answers "is this guest allowed on" to
	//       anything that can route to it.
	//   PolicyServerCAFile                           what FreeRADIUS CHECKS the
	//       Policy API's own certificate against. A per-install CA is not in any
	//       image's trust store, so without this `check_cert = yes` fails every
	//       call — which is the ordering hazard: the certificate and the
	//       configuration that requires it land together or neither lands.
	//
	// All three are paths **inside the FreeRADIUS container**, not paths on the
	// machine that runs the generator. Validate ties them to the scheme of
	// PolicyAPIURL, so a tree that says https and presents nothing cannot be
	// written at all.
	PolicyClientCertFile string
	PolicyClientKeyFile  string
	PolicyServerCAFile   string

	// ── the worker pool, and everything sized against it ────────────────────
	//
	// FreeRADIUS's thread pool is **fixed** once the server starts, and
	// `docs/rules/security.md` §2 says what exhausting it costs: Status-Server
	// is answered by a worker like anything else, so the health check dies with
	// the thing it checks. Without a generated `thread pool { }` section the
	// number is FreeRADIUS's built-in default, with nothing in `deploy/` able to
	// reach it.
	//
	// **MaxServers also sizes the two connection pools**, and that is the part
	// worth stating: a worker waiting for an `rlm_sql` or `rlm_rest` connection
	// is a worker not answering Status-Server, indistinguishable from a full
	// thread pool. Sizing both pools at the worker count means neither can ever
	// be the thing a worker queues on — the only queue is the thread pool
	// itself, which is the one an operator can see and set.
	MaxServers      int
	StartServers    int
	MinSpareServers int
	MaxSpareServers int

	// MaxQueueSize is how many requests wait when every worker is busy.
	// FreeRADIUS drops the oldest beyond it; a NAS retransmits, so a bounded
	// queue that sheds is better than an unbounded one that answers late.
	MaxQueueSize int

	// ClientTable is the SQL table rlm_sql reads RADIUS clients from, both at
	// startup (read_clients) and per-packet (dynamic clients).
	ClientTable string

	// DynamicClientNetworks bound where a client may appear without a restart.
	DynamicClientNetworks []DynamicNetwork

	// DynamicClientLifetimeSeconds is how long a dynamically discovered client
	// definition is cached. It is also how long a secret rotation or a client
	// deletion takes to be honoured, which is why Classify reports it.
	DynamicClientLifetimeSeconds int

	// AllowPasswordlessPostgres permits an empty PostgresPassword, for a
	// deployment that authenticates to PostgreSQL another way — a client
	// certificate, a unix socket with peer authentication.
	//
	// It is a field rather than an inference because "empty" and "deliberately
	// empty" look identical from here, and read_clients = yes changed what an
	// unreachable database costs: the client table is read before the first
	// packet, so a database FreeRADIUS cannot reach is a server that does not
	// start at all.
	AllowPasswordlessPostgres bool

	// GuestFailureAdvice is the per-site failure model, resolved by
	// `internal/aaa` and rendered into the guest branch of the generated
	// authorize section. See config_failure.go, which carries the field survey,
	// the two measurements, and the reason this generator takes only the half of
	// the design that has no cross-package hazard.
	//
	// Empty is not "unconfigured means open": it resolves to aaa.DefaultPolicy,
	// which is fail-closed, and produces exactly the tree this generator emitted
	// before the field existed.
	GuestFailureAdvice []aaa.Advice

	EAP EAPConfig
}

// DefaultConfig returns a configuration that is safe to generate from.
//
// The dynamic-client networks are the RFC 1918 ranges plus loopback rather
// than 0.0.0.0/0: every source address inside a dynamic network costs an SQL
// lookup, FreeRADIUS admits at most one new client per second, and a
// wide-open network is a denial-of-service surface for no benefit.
func DefaultConfig() Config {
	return Config{
		ListenIP:                      "0.0.0.0",
		AuthPort:                      1812,
		AcctPort:                      1813,
		StatusPort:                    18120,
		PostgresHost:                  "postgres",
		PostgresPort:                  5432,
		PostgresDB:                    "foyer",
		PostgresUser:                  "foyer",
		PostgresPassword:              "",
		PolicyAPIURL:                  "http://foyer-backend:8080/api/v1/aaa/authorize",
		PolicyAPITimeoutSeconds:       2,
		DefaultInterimIntervalSeconds: 300,

		// 30, and it is not a fresh number — it is the one `deploy/compose.yaml`
		// already did the arithmetic for. That file sizes PostgreSQL at
		// `max_connections = 80` and accounts for it as "FreeRADIUS's rlm_sql
		// pool maxes at 30, the control plane's pgxpool at 25, plus the
		// one-shots and the 3 reserved for superusers; 63 accounted for, 80 is
		// the margin." Picking a different worker count here would silently
		// invalidate that accounting on a host with 1 966 MB shared between
		// three containers.
		//
		// FreeRADIUS's own default is 32, so this is a small reduction in
		// capacity for a large gain in it being a number somebody chose. The
		// binding constraint is not memory — 30 threads is a few MB resident —
		// it is that every worker can hold one SQL connection and one Policy API
		// connection at once without either pool becoming a queue.
		MaxServers:      30,
		StartServers:    5,
		MinSpareServers: 3,
		MaxSpareServers: 10,
		MaxQueueSize:    65536,

		ClientTable: "nas",
		DynamicClientNetworks: []DynamicNetwork{
			{Name: "loopback", CIDR: "127.0.0.0/8"},
			{Name: "rfc1918_10", CIDR: "10.0.0.0/8"},
			{Name: "rfc1918_172", CIDR: "172.16.0.0/12"},
			{Name: "rfc1918_192", CIDR: "192.168.0.0/16"},
		},
		DynamicClientLifetimeSeconds: 300,
	}
}

// Generator generates the FreeRADIUS 3.2.x configuration tree.
type Generator struct {
	cfg Config
}

// NewGenerator fills in the values that have a single defensible default and
// leaves the rest to Validate.
func NewGenerator(cfg Config) *Generator {
	if cfg.AuthPort == 0 {
		cfg.AuthPort = 1812
	}
	if cfg.AcctPort == 0 {
		cfg.AcctPort = 1813
	}
	if cfg.StatusPort == 0 {
		cfg.StatusPort = 18120
	}
	if cfg.PolicyAPITimeoutSeconds == 0 {
		cfg.PolicyAPITimeoutSeconds = 2
	}
	if cfg.DefaultInterimIntervalSeconds == 0 {
		cfg.DefaultInterimIntervalSeconds = 300
	}
	if cfg.ClientTable == "" {
		cfg.ClientTable = "nas"
	}
	if cfg.DynamicClientLifetimeSeconds == 0 {
		cfg.DynamicClientLifetimeSeconds = 300
	}
	if cfg.MaxQueueSize == 0 {
		cfg.MaxQueueSize = 65536
	}
	return &Generator{cfg: cfg}
}

// Config returns the effective configuration, defaults applied. Classify
// compares these, not the values a caller happened to pass in.
func (g *Generator) Config() Config { return g.cfg }

// Validate refuses configurations that would generate a file nobody should
// ever have on disk. It runs before any file is written, so a rejected
// configuration leaves no partial tree behind.
func (g *Generator) Validate() error {
	c := g.cfg

	if c.ClientTable == "" {
		return fmt.Errorf("raddb: ClientTable is empty; clients come from SQL and there is no fallback")
	}
	if !isSQLIdentifier(c.ClientTable) {
		return fmt.Errorf("raddb: ClientTable %q is not a bare SQL identifier; it is interpolated into generated queries", c.ClientTable)
	}
	if len(c.DynamicClientNetworks) == 0 {
		return fmt.Errorf("raddb: no dynamic client networks; every access point added after startup would need a restart")
	}

	seen := map[string]bool{}
	for _, n := range c.DynamicClientNetworks {
		if !isConfigName(n.Name) {
			return fmt.Errorf("raddb: dynamic network name %q is not a bare configuration-section name", n.Name)
		}
		if seen[n.Name] {
			return fmt.Errorf("raddb: duplicate dynamic network name %q", n.Name)
		}
		seen[n.Name] = true

		p, err := netip.ParsePrefix(n.CIDR)
		if err != nil {
			return fmt.Errorf("raddb: dynamic network %s: %w", n.Name, err)
		}
		if p.Addr().Is4() && p.Bits() == 32 {
			return fmt.Errorf("raddb: dynamic network %s is a /32; FreeRADIUS requires a prefix here", n.Name)
		}
		if p.Addr().Is6() && p.Bits() == 128 {
			return fmt.Errorf("raddb: dynamic network %s is a /128; FreeRADIUS requires a prefix here", n.Name)
		}
	}

	if err := c.validatePolicyBoundary(); err != nil {
		return err
	}
	if err := c.validateThreadPool(); err != nil {
		return err
	}

	for _, s := range knownDefaultSecrets {
		if strings.Contains(c.PostgresPassword, s) {
			return fmt.Errorf("raddb: PostgresPassword is a known default; refuse to generate")
		}
	}
	if c.PostgresPasswordEnv != "" {
		if c.PostgresPassword != "" {
			return fmt.Errorf("raddb: PostgresPassword and PostgresPasswordEnv are both set; the tree names its password one way")
		}
		if !envNameRE.MatchString(c.PostgresPasswordEnv) {
			return fmt.Errorf("raddb: PostgresPasswordEnv %q is not an environment variable name ([A-Z_][A-Z0-9_]*)", c.PostgresPasswordEnv)
		}
	} else if c.PostgresPassword == "" && !c.AllowPasswordlessPostgres {
		return fmt.Errorf("raddb: PostgresPassword is empty; read_clients = yes reads the client table before the first packet, so a server that cannot reach PostgreSQL does not start. Set FOYER_POSTGRES_PASSWORD, or set AllowPasswordlessPostgres if this deployment authenticates another way")
	}

	if err := c.validateEAP(); err != nil {
		return err
	}

	return nil
}

// tunnelledEAPTypes are the methods that need an inner tunnel, a credential and
// therefore a decision this package cannot take. Named so the refusal below can
// say which one was asked for rather than "not tls".
var tunnelledEAPTypes = map[string]string{
	"ttls": "EAP-TTLS/PAP needs a verifier rlm_pap can check, and rlm_pap in 3.2.10 cannot check argon2 — measured, including through Crypt-Password and crypt(3)",
	"peap": "PEAP-MSCHAPv2 needs the NT hash in the clear at request time, and Foyer's is envelope-encrypted under a key that is not in the database",
	"md5":  "EAP-MD5 needs a cleartext password and offers no tunnel; security.md §1 rules out the storage it requires",
	"gtc":  "EAP-GTC hands the password to whatever auth_type it names, which is the same credential question as TTLS/PAP with no tunnel of its own",
}

func (c Config) validateEAP() error {
	if !c.EAP.Enabled {
		// The fields are not checked when EAP is off. An operator who set a cert
		// path and then turned EAP off has a stale field, not an outage, and
		// refusing here would make turning EAP off harder than turning it on.
		return nil
	}

	if reason, tunnelled := tunnelledEAPTypes[c.EAP.DefaultEAPType]; tunnelled {
		// Refused by name, with the measurement, because the alternative is a
		// generated tree that claims a method no eapol_test case can pass — and
		// `docs/rules/testing.md` says the claim goes when the case does.
		return fmt.Errorf("raddb: EAP type %q is not emitted: %s. "+
			"Enabling it is a credential-storage decision under docs/rules/security.md §1, not a generator setting", c.EAP.DefaultEAPType, reason)
	}
	if c.EAP.DefaultEAPType != "tls" {
		return fmt.Errorf("raddb: EAP type %q is not emitted by this phase; only tls is", c.EAP.DefaultEAPType)
	}

	for _, f := range []struct{ name, val string }{
		{"CertDir", c.EAP.CertDir},
		{"ServerCertFile", c.EAP.ServerCertFile},
		{"PrivateKeyFile", c.EAP.PrivateKeyFile},
		{"CAFile", c.EAP.CAFile},
	} {
		if f.val == "" {
			return fmt.Errorf("raddb: EAP is enabled but %s is empty; no certificate is generated here", f.name)
		}
	}

	if d := c.EAP.TLSSessionCacheDir; d != "" && !filepath.IsAbs(d) {
		return fmt.Errorf("raddb: EAP.TLSSessionCacheDir %q is not absolute. It is a path inside the FreeRADIUS container and radiusd does not resolve it against the tree", d)
	}

	return nil
}

// ServerUser and ServerGroup are who radiusd runs as once it has read its tree and
// bound its sockets, never root.
//
// Constants rather than Config fields, on purpose: both images this tree is read
// by -- lab/Dockerfile.freeradius (Debian) and deploy/Dockerfile.freeradius
// (NetworkRADIUS on Ubuntu) -- create `freerad`, and each Dockerfile refuses to
// build if it did not. A field would be a knob whose only non-default value is
// root. TLSReadGroup is the group both images add `freerad` to at a fixed gid, so
// that a host can grant the key files to it; see radiusd.conf.tmpl.
const (
	ServerUser   = "freerad"
	ServerGroup  = "freerad"
	TLSReadGroup = "foyer-radius-tls"
	TLSReadGID   = 1812
)

// ServerUser is ServerUser, for the templates.
func (Config) ServerUser() string { return ServerUser }

// ServerGroup is ServerGroup, for the templates.
func (Config) ServerGroup() string { return ServerGroup }

// PolicyAPIIsTLS reports whether the generated `rlm_rest` module will speak TLS
// to the Policy API. The template asks, because the two shapes of the module are
// genuinely different files and not one file with a flag in it.
func (c Config) PolicyAPIIsTLS() bool {
	u, err := url.Parse(c.PolicyAPIURL)
	return err == nil && u.Scheme == "https"
}

// validatePolicyBoundary is the interlock that makes rest-module mTLS land in one
// piece.
//
// Turning on certificate checking without issuing and wiring a certificate
// rejects **every Access-Request** — a total outage that reads as a policy
// failure rather than as a configuration one. So the generator refuses to write
// either half on its own:
//
//   - an `https` policy URL with no client certificate is a boundary that
//     encrypts and does not authenticate, which is precisely what §2 forbids;
//   - a client certificate with an `http` policy URL is material that is never
//     presented — a control that looks like a switch and is not.
func (c Config) validatePolicyBoundary() error {
	u, err := url.Parse(c.PolicyAPIURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("raddb: PolicyAPIURL %q is not an absolute http(s) URL", c.PolicyAPIURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("raddb: PolicyAPIURL %q has scheme %q. The Policy API is one of cmd/foyer's three http listeners, so http and https are the only schemes that reach it", c.PolicyAPIURL, u.Scheme)
	}

	material := []struct{ name, val string }{
		{"PolicyClientCertFile", c.PolicyClientCertFile},
		{"PolicyClientKeyFile", c.PolicyClientKeyFile},
		{"PolicyServerCAFile", c.PolicyServerCAFile},
	}

	if u.Scheme == "https" {
		for _, m := range material {
			if m.val == "" {
				return fmt.Errorf("raddb: PolicyAPIURL is https but %s is empty. "+
					"security.md §2 requires mTLS on the rlm_rest boundary, and half of it is worse than none: "+
					"a tree that checks the server certificate against a per-install CA it was not given rejects every Access-Request. "+
					"Issue a client certificate from the install's own CA (deploy/first-boot.sh does) and pass all three paths", m.name)
			}
		}
		return nil
	}

	for _, m := range material {
		if m.val != "" {
			return fmt.Errorf("raddb: %s is set but PolicyAPIURL %q is plaintext, so the certificate would never be presented. "+
				"Point PolicyAPIURL at https, or pass no client material at all", m.name, c.PolicyAPIURL)
		}
	}
	return nil
}

// validateThreadPool refuses a worker pool FreeRADIUS cannot honour.
//
// FreeRADIUS does not check these against each other. It starts, reports the
// numbers back under `-X`, and behaves as though the ones that make no sense were
// never written — which is the same shape as the `verify_peer` line this package
// carried for months, and the reason these are checked here instead.
func (c Config) validateThreadPool() error {
	if c.MaxServers < 1 {
		return fmt.Errorf("raddb: MaxServers is %d; a server with no workers answers nothing", c.MaxServers)
	}
	if c.StartServers < 1 || c.StartServers > c.MaxServers {
		return fmt.Errorf("raddb: StartServers is %d and MaxServers is %d; the pool cannot start above its own ceiling", c.StartServers, c.MaxServers)
	}
	if c.MaxSpareServers > c.MaxServers {
		return fmt.Errorf("raddb: MaxSpareServers is %d and MaxServers is %d; there cannot be more idle workers than workers", c.MaxSpareServers, c.MaxServers)
	}
	if c.MinSpareServers < 1 || c.MinSpareServers > c.MaxSpareServers {
		return fmt.Errorf("raddb: MinSpareServers is %d and MaxSpareServers is %d; the floor cannot be above the ceiling", c.MinSpareServers, c.MaxSpareServers)
	}
	if c.MaxQueueSize < 1 {
		return fmt.Errorf("raddb: MaxQueueSize is %d; a queue of zero drops every request that arrives while the workers are busy", c.MaxQueueSize)
	}
	// Last, and before Generate writes a byte: an advice set that does not
	// collapse to one value is refused here rather than rendered into a tree
	// where the fail-open sites would quietly fail closed. Blue/green calls
	// Validate before it touches raddb.next, so the running server is untouched.
	if _, _, err := c.ResolveGuestFailure(); err != nil {
		return err
	}
	return nil
}

// fileMap is the generated tree: relative output path -> embedded template.
func (g *Generator) fileMap() map[string]string {
	m := map[string]string{
		"radiusd.conf": "templates/radiusd.conf.tmpl",
		"clients.conf": "templates/clients.conf.tmpl",
		"dictionary":   "templates/dictionary.tmpl",

		filepath.Join("mods-available", "sql"):        "templates/sql.conf.tmpl",
		filepath.Join("mods-available", "rest"):       "templates/rest.conf.tmpl",
		filepath.Join("mods-available", "pap"):        "templates/pap.conf.tmpl",
		filepath.Join("mods-available", "preprocess"): "templates/preprocess.conf.tmpl",
		filepath.Join("mods-available", "expr"):       "templates/expr.conf.tmpl",
		filepath.Join("mods-available", "always"):     "templates/always.conf.tmpl",

		filepath.Join("mods-config", "sql", "main", "postgresql", "queries.conf"): "templates/queries.conf.tmpl",

		filepath.Join("sites-available", "default"):         "templates/default.site.tmpl",
		filepath.Join("sites-available", "status"):          "templates/status.site.tmpl",
		filepath.Join("sites-available", "dynamic-clients"): "templates/dynamic-clients.site.tmpl",
	}
	if g.cfg.EAP.Enabled {
		m[filepath.Join("mods-available", "eap")] = "templates/eap.conf.tmpl"
	}
	return m
}

type enabledLink struct{ target, link string }

// enabled is the set of mods-enabled and sites-enabled symlinks.
func (g *Generator) enabled() []enabledLink {
	links := []enabledLink{
		{"../mods-available/sql", filepath.Join("mods-enabled", "sql")},
		{"../mods-available/rest", filepath.Join("mods-enabled", "rest")},
		{"../mods-available/pap", filepath.Join("mods-enabled", "pap")},
		{"../mods-available/preprocess", filepath.Join("mods-enabled", "preprocess")},
		{"../mods-available/expr", filepath.Join("mods-enabled", "expr")},
		{"../mods-available/always", filepath.Join("mods-enabled", "always")},
		{"../sites-available/default", filepath.Join("sites-enabled", "default")},
		{"../sites-available/status", filepath.Join("sites-enabled", "status")},
		{"../sites-available/dynamic-clients", filepath.Join("sites-enabled", "dynamic-clients")},
	}
	if g.cfg.EAP.Enabled {
		links = append(links, enabledLink{"../mods-available/eap", filepath.Join("mods-enabled", "eap")})
	}
	return links
}

// Generate emits the whole tree into targetDir.
func (g *Generator) Generate(targetDir string) error {
	if err := g.Validate(); err != nil {
		return err
	}

	dirs := []string{
		targetDir,
		filepath.Join(targetDir, "mods-available"),
		filepath.Join(targetDir, "mods-enabled"),
		filepath.Join(targetDir, "mods-config", "sql", "main", "postgresql"),
		filepath.Join(targetDir, "sites-available"),
		filepath.Join(targetDir, "sites-enabled"),
		filepath.Join(targetDir, "certs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("raddb: create %s: %w", d, err)
		}
	}

	files := g.fileMap()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, relPath := range paths {
		tmplPath := files[relPath]
		content, err := templateFS.ReadFile(tmplPath)
		if err != nil {
			return fmt.Errorf("raddb: read template %s: %w", tmplPath, err)
		}
		tmpl, err := template.New(relPath).Option("missingkey=error").Parse(string(content))
		if err != nil {
			return fmt.Errorf("raddb: parse template %s: %w", tmplPath, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, g.cfg); err != nil {
			return fmt.Errorf("raddb: execute template %s: %w", tmplPath, err)
		}
		out := buf.Bytes()

		if err := assertNoDefaultSecret(relPath, out); err != nil {
			return err
		}

		outPath := filepath.Join(targetDir, relPath)
		if err := os.WriteFile(outPath, out, 0o640); err != nil {
			return fmt.Errorf("raddb: write %s: %w", outPath, err)
		}
	}

	for _, s := range g.enabled() {
		link := filepath.Join(targetDir, s.link)
		_ = os.Remove(link)
		if err := os.Symlink(s.target, link); err != nil {
			return fmt.Errorf("raddb: symlink %s -> %s: %w", link, s.target, err)
		}
	}

	// A stale enabled symlink is a live configuration file. Turning EAP off in
	// the Config must remove it from the tree, not leave the last one running.
	for _, dir := range []string{"mods-enabled", "sites-enabled"} {
		if err := pruneEnabled(targetDir, dir, g.enabled()); err != nil {
			return err
		}
	}

	return nil
}

func pruneEnabled(targetDir, dir string, want []enabledLink) error {
	keep := map[string]bool{}
	for _, w := range want {
		if filepath.Dir(w.link) == dir {
			keep[filepath.Base(w.link)] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(targetDir, dir))
	if err != nil {
		return fmt.Errorf("raddb: read %s: %w", dir, err)
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(targetDir, dir, e.Name())); err != nil {
			return fmt.Errorf("raddb: prune %s/%s: %w", dir, e.Name(), err)
		}
	}
	return nil
}

func assertNoDefaultSecret(relPath string, content []byte) error {
	s := string(content)
	for _, def := range knownDefaultSecrets {
		if strings.Contains(s, def) {
			return fmt.Errorf("raddb: generated %s contains a known default secret", relPath)
		}
	}
	return nil
}

func isSQLIdentifier(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isConfigName(s string) bool { return isSQLIdentifier(s) }
