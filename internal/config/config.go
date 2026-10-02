// Package config is what the control plane reads from its environment, and the
// assertions it refuses to start without.
//
// ── why this is a package and not a file in cmd/foyer ────────────────────────
//
// In `package main` nothing could import it — and something needs to.
// `foyerctl doctor` dials the three listeners and reports on the Policy API's
// exposure, so it reads the same three variables and their defaults.
//
// A second copy drifts, and not in the harmless direction. `cmd/foyer` admits to
// being a lab only for the exact sentence `LabInsecureVar` names; a copy that
// accepted **any** non-empty value would let `FOYER_LAB_INSECURE=yes` produce a
// doctor that reports a plaintext Policy API on a routable address as a permitted
// warning, while the control plane refuses to start on it at all — a diagnostic
// disagreeing with the thing it diagnoses, about a §2 boundary.
//
// The refusals are the part worth testing: `docs/rules/security.md` §7 asks for
// a startup assertion, not a doc note, and an assertion that has never been made to
// fail only looks like a check.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is the whole of what the control plane reads from its environment.
//
// `cmd/foyer` reads no variable of its own (`TestTheControlPlaneReadsTheEnvironmentOnlyThroughConfig`
// holds it to that), and it opens the store with the keyring built from KEK
// rather than letting `internal/store` read FOYER_KEK a second time. The one
// reader outside this package is the Go runtime, which reads GOMEMLIMIT itself.
type Config struct {
	DatabaseURL string

	// Three listeners, because §7 requires the admin UI and the Policy API to be
	// on separate listeners from the guest portal — and because they have three
	// genuinely different audiences.
	//
	// **The portal is reachable by anyone within radio range, before they have
	// authenticated to anything.** If it shared an origin with the console it
	// would share a cookie jar with the thing that configures RADIUS, and a
	// cross-site scripting bug in a venue's terms of service would be an
	// administrator session. Separate ports are what make `__Host-` prefixed,
	// `Path=/` cookies actually mean "this origin".
	PortalAddr string
	AdminAddr  string
	PolicyAddr string

	// PortalURL is where a guest's browser reaches the portal, which is not the
	// same fact as where it is bound: the deployment may be behind a proxy, and
	// this value is what RFC 8908 hands the operating system as `user-portal-url`.
	PortalURL string
	// PortalURLDerived says PortalURL was worked out from the listener rather than
	// supplied, which the console reports as `source: derived`.
	//
	// **These three and the certificate pair below are bootstrap values.** An
	// operator sets them in the admin console (`updateNetworkSettings`,
	// `putPortalCertificate`), stored in the database; a value saved there wins,
	// and these apply only until one is. A deployment that set them in the
	// environment keeps working unchanged.
	PortalURLDerived bool
	// VenueURL is RFC 8908's `venue-info-url`: a page about the venue. **Only
	// from FOYER_VENUE_URL, never derived.** A default of
	// `http://<portal bind>/venue` would, on a wildcard bind, be
	// `http://127.0.0.1:8081/venue` — an address no guest can reach, naming a
	// path the portal does not serve — and ChromeOS discards the whole API
	// answer over one bad URL member. Absent is omitted, which RFC 8908 allows.
	VenueURL string

	// PortalTLSCert and PortalTLSKey turn on an HTTPS portal listener at
	// PortalTLSAddr. **Optional, and off by default** — a deliberate decision
	// (`docs/portal-https.md`): the portal serves plain HTTP unless an operator
	// chooses otherwise, and the guest is never asked to accept anything they did
	// not choose to be shown.
	//
	// Supplied by the operator, never generated. A portal certificate is either
	// publicly trusted for a name the guests resolve — the only kind an operating
	// system's RFC 8908 client will accept — or a self-signed one the operator
	// decided their guests will click through once. Both are the operator's
	// decision, and a certificate generated here would be a third kind nobody
	// chose. Set together or not at all.
	//
	// With a pair set, the plain-HTTP listener keeps answering — it is where a
	// NAS redirect lands — but only to send the browser to the HTTPS origin of
	// PortalURL; see `cmd/foyer`'s portal listeners.
	PortalTLSAddr string
	PortalTLSCert string
	PortalTLSKey  string

	// PolicyTLSCert and PolicyTLSKey turn on TLS for the Policy API.
	// PolicyClientCA additionally requires a client certificate, which is the
	// mTLS §2 asks for on that boundary.
	PolicyTLSCert string
	PolicyTLSKey  string

	// AdminTLSCert and AdminTLSKey are where the console's certificate lives —
	// and, unlike the policy pair, where the process **writes** one if it is not
	// there. The console is HTTPS and the application generates every parameter,
	// so an operator sets neither of these: `deploy/compose.yaml` does.
	//
	// Empty means *serve HTTPS on a certificate generated in memory and persisted
	// nowhere*, which is what a developer's `go run ./cmd/foyer` gets. A default
	// path is deliberately not baked in: nothing else in this struct invents a
	// filesystem location, and a container's layout has no business inside a
	// binary that also runs in `lab/` and on a laptop.
	//
	// Exactly one set is a refusal, the same shape as the policy pair.
	AdminTLSCert string
	AdminTLSKey  string

	// AdminTLSNames are the names this console is dialled by, which is not a fact
	// this process can work out for itself.
	//
	// `internal/tlsca` derives every name it can see — localhost, this machine's
	// hostname, this machine's addresses — and inside a container that set does
	// **not** contain the Docker host's LAN address, which is the only address
	// anybody uses. A certificate that omits it is valid, correct by every other
	// check, and refused by every browser on the name.
	//
	// So the deployment declares them, and `deploy/` computes them from the host
	// with `hostname -I` and `hostname -f`: the constraint is that an operator
	// types no parameter, not that no parameter exists.
	//
	// **Not validated here on purpose.** `tlsca.EnsureSelfSigned` parses these
	// before it reads or writes anything and refuses a value that is neither an
	// address nor a DNS name, naming it. A second check in this package would be a
	// second copy of a rule that already has an owner — the shape
	// `docs/rules/database.md` has a section about — and it would drift the day
	// the SAN rules change.
	AdminTLSNames []string

	PolicyClientCA string

	// LabInsecure is the explicit acknowledgement that this process is a lab and
	// not a deployment. It is spelled as a sentence rather than as a boolean so
	// that it cannot be set by habit, and it is logged loudly at every start.
	LabInsecure bool

	// KEK is the key-encryption key for RADIUS shared secrets, controller
	// credentials and NT hashes. Absent is allowed and is not silently no
	// encryption: `internal/store` returns `ErrNoKeyring` by name from the three
	// methods that need one, so the refusal happens where the key is needed
	// rather than in front of everything that does not.
	KEK string

	// RaddbTarget is the handoff directory the control plane shares with the
	// FreeRADIUS container: each generated tree is written into it as
	// `gen-<N>/`, and the supervisor in the FreeRADIUS image takes it up and
	// answers (internal/raddb/handoff.go). A deployment's, set by compose — never
	// an operator's. The three fields under it are the mTLS material that tree
	// needs.
	//
	// They are paths **inside the FreeRADIUS container**, which is why they
	// cannot be derived from PolicyTLSCert and friends: those are paths inside
	// *this* container, and on a deployment the two sets are deliberately
	// different directories — the FreeRADIUS one holds no CA key.
	RaddbTarget           string
	RaddbPolicyClientCert string
	RaddbPolicyClientKey  string
	RaddbPolicyServerCA   string

	// RaddbPostgresPassword is the password of FreeRADIUS's OWN login,
	// RadiusDatabaseUser, from FOYER_POSTGRES_PASSWORD (or _FILE) — the same
	// variable `foyerctl generate-raddb` reads. Read only when RaddbTarget is set:
	// a control plane that writes no tree has no business holding the data
	// plane's credential.
	//
	// It is not derived from DatabaseURL: the control plane logs in as foyer_app
	// (migration 00019), which cannot write radacct, so deriving it would produce
	// a FreeRADIUS that starts and then fails every Accounting-Request.
	RaddbPostgresPassword string

	// RaddbPostgresPasswordEnv is FOYER_RADDB_POSTGRES_PASSWORD_ENV: the name of the
	// environment variable FreeRADIUS's generated `rlm_sql` block reads its password
	// from (`$ENV{NAME}`), instead of a password written into the tree. With it set this process reads no FOYER_POSTGRES_PASSWORD and
	// holds no FreeRADIUS credential at all; FreeRADIUS's supervisor exports the
	// variable from its own secret. Setting both is refused.
	RaddbPostgresPasswordEnv string

	// PolicyHost is FOYER_POLICY_HOST: the host:port a generated tree tells
	// FreeRADIUS to call the Policy API at, when that is not the address this
	// process bound — in the lab and on a deployment FreeRADIUS is another
	// container and reaches this one by service name. Empty derives it from
	// PolicyAddr (`cmd/foyer`'s policyReachable).
	PolicyHost string

	// ReaperDatabase and RetentionDatabase are the maintenance roles' own logins,
	// from FOYER_REAPER_DATABASE_URL and FOYER_RETENTION_DATABASE_URL (or _FILE).
	// Empty URL means *SET ROLE from DatabaseURL's pool*. Their read errors are
	// **not** startup refusals, unlike secretErr: this process also answers every
	// Wi-Fi sign-in, so a maintenance pool that cannot be configured is a job that
	// fails every cycle, which is what `cmd/foyer`'s openMaintenance does with it.
	ReaperDatabase    SecretValue
	RetentionDatabase SecretValue

	// The maintenance container (internal/jobs remote.go).
	// MaintenanceURL is FOYER_MAINTENANCE_URL: set on the control plane, it says the
	// maintenance jobs run elsewhere — `foyer maintenance`, in a container that alone
	// holds the reaper's and the retention role's logins — and that erasure's
	// retention half is asked of it there. MaintenanceToken (FOYER_MAINTENANCE_TOKEN,
	// or _FILE) authenticates the two to each other; MaintenanceAddr
	// (FOYER_MAINTENANCE_ADDR) is where `foyer maintenance` listens. Install-time
	// wiring, never an operator setting.
	MaintenanceURL   string
	MaintenanceToken SecretValue
	MaintenanceAddr  string

	// GoMemLimitSet says GOMEMLIMIT is in the environment. The Go runtime reads
	// the value itself; `cmd/foyer`'s memory plan only needs to know whether the
	// deployment stated a budget, so it does not replace it with its own.
	GoMemLimitSet bool

	// secretErr is what Load could not read. Load has no error return and should
	// not grow one — every caller would then have two ways to fail before it has
	// a Config — so the failure travels here and Validate reports it first.
	secretErr error
}

// SecretValue is a value EnvOrFile read, and what reading it cost.
type SecretValue struct {
	Value string
	Err   error
}

// LabAcknowledgement is the exact value that admits to being a lab.
//
// Exported so that `foyerctl doctor` compares against the same string. It kept its
// own rule -- any non-empty value -- and reported a configuration the control plane
// refuses to start on as a permitted warning.
const LabAcknowledgement = "i-am-a-lab-and-not-a-deployment"

// LabInsecureVar is the variable that carries it.
const LabInsecureVar = "FOYER_LAB_INSECURE"

// knownDefaults are values that must never reach a deployment.
//
// **Default credentials in a compose file are the most common way appliance
// software gets mass-owned** (§7), so this list is checked on every start and not
// only when some `FOYER_ENV` says production — an environment variable that has to
// be set correctly for a security check to run is a security check that does not
// run. The lab's own password is on the list precisely so that running the lab
// requires saying so.
var knownDefaults = []string{
	"foyer_lab_secret",
	"postgres:postgres",
	"postgres:password",
	"password@",
	":changeme@",
	":admin@",
}

// EnvOrFile reads NAME, or NAME_FILE if that is set.
//
// The shape is PostgreSQL's `POSTGRES_PASSWORD_FILE`, copied rather than
// invented, because the hole it closes is the same one: an environment variable
// is readable through `docker inspect` and `/proc/<pid>/environ` by anything that
// can reach the daemon or the namespace, and `FOYER_KEK` unseals every RADIUS
// shared secret, controller credential and NT hash in the database.
//
// Two rules, both deliberate:
//
//   - **Only the trailing newline is trimmed.** That is what `printf '%s\n' >
//     file` leaves behind. Trimming more would silently alter a secret that
//     legitimately ends in a space, and "the key does not work" is the hardest
//     kind of wrong to find.
//   - **Both forms set is a refusal**, not a precedence rule. Picking a winner
//     quietly is how a rotated key goes unnoticed for a week.
//
// Exported because `foyerctl` reads FOYER_POSTGRES_PASSWORD the same way, and a
// second implementation of "where does a secret come from" is how this package's
// own doc says the last drift started.
func EnvOrFile(name string) (string, error) {
	inline := os.Getenv(name)
	path := os.Getenv(name + "_FILE")
	if path == "" {
		return inline, nil
	}
	if inline != "" {
		return "", fmt.Errorf("%s and %s_FILE are both set; one of them would be ignored and there is no safe way to guess which", name, name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// The path, never the contents. This message reaches logs.
		return "", fmt.Errorf("%s_FILE = %q could not be read: %w", name, path, err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func readSecret(name string) SecretValue {
	v, err := EnvOrFile(name)
	return SecretValue{Value: v, Err: err}
}

// Load reads the environment. It does not validate; see Validate.
func Load() Config {
	dbURL, dbErr := EnvOrFile("FOYER_DATABASE_URL")
	kek, kekErr := EnvOrFile("FOYER_KEK")

	c := Config{
		secretErr:   errors.Join(dbErr, kekErr),
		DatabaseURL: dbURL,
		// The guest portal is bound wide because that is its job: every device
		// that associates has to reach it before it has reached anything else.
		PortalAddr: envOr("FOYER_PORTAL_ADDR", "0.0.0.0:8081"),
		// The console is bound to every interface by default: a console nobody
		// can open is not a product, so it is reachable on 8443 over HTTPS on a certificate this process makes, and
		// Warnings tells the operator to compare its fingerprint.
		// `TestTheConsoleIsReachableByDefault` pins both the binding and the port.
		AdminAddr: envOr("FOYER_ADMIN_ADDR", "0.0.0.0:8443"),
		// Only FreeRADIUS calls the Policy API. Loopback by default, and
		// non-loopback demands TLS — see Validate.
		PolicyAddr:            envOr("FOYER_POLICY_ADDR", "127.0.0.1:8080"),
		PortalURL:             os.Getenv("FOYER_PORTAL_URL"),
		VenueURL:              os.Getenv("FOYER_VENUE_URL"),
		PortalTLSAddr:         envOr("FOYER_PORTAL_TLS_ADDR", "0.0.0.0:8444"),
		PortalTLSCert:         os.Getenv("FOYER_PORTAL_TLS_CERT"),
		PortalTLSKey:          os.Getenv("FOYER_PORTAL_TLS_KEY"),
		PolicyTLSCert:         os.Getenv("FOYER_POLICY_TLS_CERT"),
		PolicyTLSKey:          os.Getenv("FOYER_POLICY_TLS_KEY"),
		AdminTLSCert:          os.Getenv("FOYER_ADMIN_TLS_CERT"),
		AdminTLSKey:           os.Getenv("FOYER_ADMIN_TLS_KEY"),
		AdminTLSNames:         splitList(os.Getenv("FOYER_ADMIN_TLS_NAMES")),
		PolicyClientCA:        os.Getenv("FOYER_POLICY_CLIENT_CA"),
		LabInsecure:           os.Getenv(LabInsecureVar) == LabAcknowledgement,
		KEK:                   kek,
		RaddbTarget:           os.Getenv("FOYER_RADDB_TARGET"),
		RaddbPolicyClientCert: os.Getenv("FOYER_RADDB_POLICY_CLIENT_CERT"),
		RaddbPolicyClientKey:  os.Getenv("FOYER_RADDB_POLICY_CLIENT_KEY"),
		RaddbPolicyServerCA:   os.Getenv("FOYER_RADDB_POLICY_SERVER_CA"),
		PolicyHost:            os.Getenv("FOYER_POLICY_HOST"),
		ReaperDatabase:        readSecret("FOYER_REAPER_DATABASE_URL"),
		RetentionDatabase:     readSecret("FOYER_RETENTION_DATABASE_URL"),
		MaintenanceURL:        os.Getenv("FOYER_MAINTENANCE_URL"),
		MaintenanceToken:      readSecret("FOYER_MAINTENANCE_TOKEN"),
		MaintenanceAddr:       envOr("FOYER_MAINTENANCE_ADDR", "0.0.0.0:8090"),
	}
	_, c.GoMemLimitSet = os.LookupEnv("GOMEMLIMIT")
	if c.RaddbTarget != "" {
		c.RaddbPostgresPasswordEnv = os.Getenv("FOYER_RADDB_POSTGRES_PASSWORD_ENV")
		pw, err := EnvOrFile("FOYER_POSTGRES_PASSWORD")
		c.RaddbPostgresPassword = pw
		c.secretErr = errors.Join(c.secretErr, err)
	}
	// Derived only for the plain-HTTP portal. With a certificate configured the
	// URL must name what the certificate covers, which nothing here can know, so
	// it is left empty and Validate refuses it by name.
	if c.PortalURL == "" && !c.PortalTLSConfigured() {
		c.PortalURL = "http://" + reachableHost(c.PortalAddr) + "/en/"
		c.PortalURLDerived = true
	}
	return c
}

// Validate is the startup assertion. Every refusal names what to change.
func (c Config) Validate() error {
	// First, because everything below reads values Load may not have been able to
	// read at all — and an unreadable FOYER_KEK_FILE would otherwise look exactly
	// like the deliberate absence of a key, which is permitted.
	if c.secretErr != nil {
		return c.secretErr
	}
	if c.DatabaseURL == "" {
		// No default connection string, and that is the change: a fallback to a
		// known lab URL is a deployment that quietly runs against whatever
		// happens to answer on 5432.
		return fmt.Errorf("FOYER_DATABASE_URL is not set: there is no default, because a default database URL is a deployment pointed somewhere nobody chose")
	}

	// §7, and unconditionally rather than behind an environment name.
	if def, found := knownDefault(c.DatabaseURL); found && !c.LabInsecure {
		return fmt.Errorf("FOYER_DATABASE_URL contains the known default %q. "+
			"Generate a password at first boot; a shipped credential is how appliance software gets mass-owned. "+
			"If this really is the lab, set %s=%s", def, LabInsecureVar, LabAcknowledgement)
	}

	if err := c.MaintenanceSplitError(); err != nil {
		return err
	}

	for _, addr := range []struct{ name, value string }{
		{"FOYER_PORTAL_ADDR", c.PortalAddr},
		{"FOYER_ADMIN_ADDR", c.AdminAddr},
		{"FOYER_POLICY_ADDR", c.PolicyAddr},
	} {
		if _, _, err := net.SplitHostPort(addr.value); err != nil {
			return fmt.Errorf("%s = %q is not host:port", addr.name, addr.value)
		}
	}

	// Shape before collisions, so a malformed address is named as one.
	if err := c.PortalTLSError(); err != nil {
		return err
	}

	// **The three listeners must be three listeners.** Sharing a port is what the
	// code this replaced did — the console was mounted on the policy listener's
	// mux — and it is not a tidiness question: a guest-reachable origin that also
	// answers `/api/v0` shares a cookie jar with the console.
	if err := distinctPorts(c); err != nil {
		return err
	}

	// §2 on the FreeRADIUS-to-Go boundary. One function, called from here and
	// from `foyerctl doctor`, for the reason this package exists at all.
	if err := c.PolicyBoundaryError(); err != nil {
		return err
	}

	if c.PortalURL != "" {
		if err := httpOrigin(c.PortalURL); err != nil {
			return fmt.Errorf("FOYER_PORTAL_URL: %w", err)
		}
	}
	if c.VenueURL != "" {
		if err := httpOrigin(c.VenueURL); err != nil {
			return fmt.Errorf("FOYER_VENUE_URL: %w", err)
		}
	}
	return nil
}

// PortalTLSConfigured reports whether the portal has a certificate and a key.
func (c Config) PortalTLSConfigured() bool {
	return c.PortalTLSCert != "" && c.PortalTLSKey != ""
}

// PortalTLSError is the startup assertion on the optional HTTPS portal.
//
// It does not read the files. `cmd/foyer` loads the pair and checks that the
// certificate covers PortalURL's host, because that is where the files are opened
// and a second reader here would be a second place for the rule to drift.
func (c Config) PortalTLSError() error {
	if (c.PortalTLSCert == "") != (c.PortalTLSKey == "") {
		return fmt.Errorf("FOYER_PORTAL_TLS_CERT and FOYER_PORTAL_TLS_KEY must be set together, or neither: with neither, the portal serves plain HTTP only")
	}
	if !c.PortalTLSConfigured() {
		return nil
	}
	if _, _, err := net.SplitHostPort(c.PortalTLSAddr); err != nil {
		return fmt.Errorf("FOYER_PORTAL_TLS_ADDR = %q is not host:port", c.PortalTLSAddr)
	}
	// **The HTTPS origin is where the plain-HTTP listener sends every guest**, and
	// it is RFC 8908's `user-portal-url`, which §4 requires to be https. It has to
	// be a name the certificate covers and the guests resolve. This process can
	// derive neither — the same lesson FOYER_ADMIN_TLS_NAMES records — so it is
	// required rather than guessed.
	u, err := url.Parse(c.PortalURL)
	if c.PortalURL == "" || err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("FOYER_PORTAL_TLS_CERT is set, so FOYER_PORTAL_URL must be the https:// address guests reach the portal at "+
			"(a name or address the certificate covers, e.g. https://wifi.example/en/); got %s. "+
			"The plain-HTTP listener redirects every guest there, and RFC 8908 requires user-portal-url to be https", shownURL(c.PortalURL))
	}
	if c.VenueURL != "" && !strings.HasPrefix(c.VenueURL, "https://") {
		return fmt.Errorf("FOYER_VENUE_URL = %s is not https while the portal serves HTTPS; RFC 8908 clients are handed it beside an https user-portal-url", shownURL(c.VenueURL))
	}
	return nil
}

// PortalHTTPSOrigin is `scheme://host[:port]` of PortalURL when the HTTPS portal
// is configured, and "" otherwise. It is where the plain-HTTP listener redirects.
func (c Config) PortalHTTPSOrigin() string {
	if !c.PortalTLSConfigured() {
		return ""
	}
	u, err := url.Parse(c.PortalURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return "https://" + u.Host
}

// PolicyTLSConfigured reports whether the Policy API has a certificate and a key.
// Exported for the same reason as LoopbackOnly.
func (c Config) PolicyTLSConfigured() bool {
	return c.PolicyTLSCert != "" && c.PolicyTLSKey != ""
}

// PostgresParams is what a generated `rlm_sql` block needs, taken from the
// connection string this process is itself using.
type PostgresParams struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	// PasswordEnv names the variable the generated tree reads the password from
	// instead; Password is then empty (RaddbPostgresPasswordEnv).
	PasswordEnv string
}

// RadiusDatabaseUser is the role FreeRADIUS logs in as. It is `store.RadiusRole`,
// spelled again here only because internal/store imports this package;
// cmd/foyer's `TestTheRadiusUserIsTheStoresRadiusRole` holds the two equal.
const RadiusDatabaseUser = "foyer_radius"

// Postgres is what a generated `rlm_sql` block needs: WHERE from
// FOYER_DATABASE_URL, WHO from FreeRADIUS's own login.
//
// The location is derived and the credential is not, and the split is the point.
// The location must be the control plane's own database, for the reason below.
// The credential must be FreeRADIUS's own: `RadiusDatabaseUser` with
// FOYER_POSTGRES_PASSWORD. The application's login is foyer_app, which may not
// write radacct, and the data plane may not read operators. Two roles, two
// passwords.
//
// ── the location ──
//
// **Derived rather than configured separately, and that is the whole point.** A
// tree generated from `raddb.DefaultConfig()` with one field overridden carries no
// database password and `postgres:5432/foyer` regardless of where this process is
// actually connected. A second variable would fix the empty password and keep the
// ability to disagree: a generated `rlm_sql` block
// pointing at a different database than the control plane uses is a failure with
// no symptom until an operator wonders why a new access point never appears.
//
// An empty password is an error here rather than a default, because
// `read_clients = yes` reads the client table before the first packet — a
// FreeRADIUS that cannot reach PostgreSQL is not degraded, it does not start.
func (c Config) Postgres() (PostgresParams, error) {
	var p PostgresParams
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		// *url.Error repeats the whole URL, and this one carries a password.
		// Only the reason survives, not the input.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return p, fmt.Errorf("FOYER_DATABASE_URL is not a URL: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return p, fmt.Errorf("FOYER_DATABASE_URL has scheme %q, want postgres", u.Scheme)
	}
	p.Host = u.Hostname()
	p.Port = 5432
	if s := u.Port(); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return p, fmt.Errorf("FOYER_DATABASE_URL port %q is not a number", s)
		}
		p.Port = n
	}
	p.Database = strings.TrimPrefix(u.Path, "/")
	if p.Database == "" {
		return p, fmt.Errorf("FOYER_DATABASE_URL names no database")
	}
	if c.RaddbPostgresPasswordEnv != "" {
		if c.RaddbPostgresPassword != "" {
			return p, fmt.Errorf("FOYER_RADDB_POSTGRES_PASSWORD_ENV and FOYER_POSTGRES_PASSWORD are both set: the tree names FreeRADIUS's password one way, and this process should hold none")
		}
		p.User = RadiusDatabaseUser
		p.PasswordEnv = c.RaddbPostgresPasswordEnv
		return p, nil
	}
	if c.RaddbPostgresPassword == "" {
		return p, fmt.Errorf("FOYER_POSTGRES_PASSWORD is not set, so a generated FreeRADIUS tree could not log in as %s. read_clients = yes reads the client table before the first packet, so that server would not start at all. It is FreeRADIUS's own password, not the one in FOYER_DATABASE_URL", RadiusDatabaseUser)
	}
	p.User = RadiusDatabaseUser
	p.Password = c.RaddbPostgresPassword
	return p, nil
}

// PolicyBoundaryError is the whole of §2's startup assertion on the
// FreeRADIUS-to-Go boundary, in one place because it has two callers.
//
// `foyerctl doctor` reports on this boundary too, and a doctor answering from its
// own copy of the rule drifts: accepting any non-empty FOYER_LAB_INSECURE calls a
// configuration the control plane refuses to start on "a permitted warning", and,
// in the more dangerous direction, reporting **statusOK** for a non-loopback Policy
// API with a certificate and no client CA calls a boundary fine that is encrypted,
// unauthenticated, and on a bridge network any other container can route across.
// A diagnostic cannot be a second opinion about a security boundary; it has to be
// the same opinion.
func (c Config) PolicyBoundaryError() error {
	if _, _, err := net.SplitHostPort(c.PolicyAddr); err != nil {
		return fmt.Errorf("FOYER_POLICY_ADDR = %q is not host:port", c.PolicyAddr)
	}
	if (c.PolicyTLSCert == "") != (c.PolicyTLSKey == "") {
		return fmt.Errorf("FOYER_POLICY_TLS_CERT and FOYER_POLICY_TLS_KEY must be set together")
	}
	// The same refusal for the console pair, and for a sharper reason: the process
	// *writes* these when they are absent, so a half-set pair would have it generate
	// a certificate beside a key it was told to use — two halves of two different
	// identities. `internal/tlsca` refuses the same state at the file level; this
	// refuses it before a listener is built.
	if (c.AdminTLSCert == "") != (c.AdminTLSKey == "") {
		return fmt.Errorf("FOYER_ADMIN_TLS_CERT and FOYER_ADMIN_TLS_KEY must be set together, or neither: with neither, the console generates a certificate in memory")
	}
	if LoopbackOnly(c.PolicyAddr) {
		// Only this machine can reach it. Still not mTLS, and Warnings says so.
		return nil
	}

	if !c.PolicyTLSConfigured() {
		if c.LabInsecure {
			return nil
		}
		return fmt.Errorf("FOYER_POLICY_ADDR = %q is not loopback and no TLS is configured. "+
			"The Policy API carries authorization decisions between FreeRADIUS and this process, and "+
			"docs/rules/security.md §2 requires mTLS on that boundary. "+
			"Set FOYER_POLICY_TLS_CERT and FOYER_POLICY_TLS_KEY (and FOYER_POLICY_CLIENT_CA for mTLS), "+
			"bind it to loopback, or — for lab/docker-compose.yml only — set %s=%s",
			c.PolicyAddr, LabInsecureVar, LabAcknowledgement)
	}

	// §2 asks for **mTLS**, and encryption alone is the half of it that looks
	// finished. A Policy API with a certificate and no client CA answers "is this
	// guest allowed on" to anything that can route to the port — which on a
	// deployment is every other container on the bridge, and the whole argument
	// for putting policy in a Go process instead of in unlang assumes otherwise.
	//
	// **The lab acknowledgement does not lift this one**, and that is deliberate:
	// it lifts a known-default database credential and a plaintext Policy API,
	// both of which the lab genuinely is. The lab issues its own pair
	// (`lab/make-policy-certs.sh`) and exercises this boundary rather than
	// stepping around it, because it is the only place the generated `rlm_rest`
	// module is ever read by a real FreeRADIUS.
	if c.PolicyClientCA == "" {
		return fmt.Errorf("FOYER_POLICY_ADDR = %q has TLS but no FOYER_POLICY_CLIENT_CA. "+
			"That is an encrypted Policy API that will answer an authorization question for anyone who can reach it, "+
			"and docs/rules/security.md §2 requires mTLS on that boundary rather than TLS. "+
			"Point FOYER_POLICY_CLIENT_CA at the CA that issued FreeRADIUS's client certificate "+
			"(deploy/first-boot.sh generates both), or bind the Policy API to loopback",
			c.PolicyAddr)
	}
	return nil
}

// Warnings are the things worth saying at every start that are not refusals.
func (c Config) Warnings() []string {
	var out []string
	if c.LabInsecure {
		out = append(out, LabInsecureVar+" is set: known-default credentials and a plaintext Policy API are permitted. This must never be set on a deployment.")
	}
	if c.KEK == "" {
		out = append(out, "FOYER_KEK is not set: registering a controller, storing an NT hash and rotating a shared secret will be refused, because there is nothing to seal them under.")
	}
	if !LoopbackOnly(c.AdminAddr) {
		// Not "put a TLS terminator in front of it": the console terminates TLS
		// itself.
		//
		// It is also not describing an unusual choice: a reachable console is the
		// default, on the ground that a console nobody can open is not a product.
		// A line that fires on every start of
		// every deployment has to earn its place by being the thing an operator
		// needs rather than an objection to the shape they were given — so it is
		// the fingerprint instruction, which is the only defence a self-signed
		// console has, and nothing else.
		out = append(out, "FOYER_ADMIN_ADDR is not loopback: the admin console is reachable from the network it is bound to, on a certificate no browser will vouch for. Compare the fingerprint this start printed before accepting it.")

		// **And this is the one that catches a real misconfiguration.**
		//
		// Bound to a network with no declared names, the certificate covers only
		// what this process could derive. On a laptop that is right. In a
		// container it is the host's address that is missing, and the symptom is
		// not a warning anywhere — it is a browser refusing the name, which reads
		// to an operator as "the certificate is broken" rather than as "nobody
		// told the console what it is called".
		if len(c.AdminTLSNames) == 0 {
			out = append(out, "FOYER_ADMIN_TLS_NAMES is empty while the console is bound to a network: its certificate will cover only the names this process can see for itself. Inside a container that excludes the address the console is published on, and a browser will refuse the name. deploy/ sets this from the host.")
		}
	}
	// The loopback case, which Validate does not refuse: only this machine can
	// reach it, so the exposure is a local process rather than the network. It is
	// still not mTLS and is still worth a line at every start.
	if LoopbackOnly(c.PolicyAddr) && c.PolicyTLSConfigured() && c.PolicyClientCA == "" {
		out = append(out, "FOYER_POLICY_CLIENT_CA is not set: the Policy API has TLS but does not check who is calling. On loopback that is a local process rather than the network, and §2 still asks for mTLS.")
	}
	return out
}

func distinctPorts(c Config) error {
	seen := map[string]string{}
	listeners := []struct{ name, value string }{
		{"FOYER_PORTAL_ADDR", c.PortalAddr},
		{"FOYER_ADMIN_ADDR", c.AdminAddr},
		{"FOYER_POLICY_ADDR", c.PolicyAddr},
	}
	// Only when it will be bound: an unused default must not refuse a start.
	if c.PortalTLSConfigured() {
		listeners = append(listeners, struct{ name, value string }{"FOYER_PORTAL_TLS_ADDR", c.PortalTLSAddr})
	}
	for _, l := range listeners {
		host, port, err := net.SplitHostPort(l.value)
		if err != nil {
			return err
		}
		// Two listeners collide when they share a port and either shares a host
		// or one of them is a wildcard. `127.0.0.1:8080` and `0.0.0.0:8080` are
		// the same socket in every way that matters.
		for name, other := range seen {
			oHost, oPort, _ := net.SplitHostPort(other)
			if port != oPort {
				continue
			}
			if host == oHost || isWildcard(host) || isWildcard(oHost) {
				return fmt.Errorf("%s and %s both answer on port %s. "+
					"The guest portal, the admin console and the Policy API are three listeners with three audiences "+
					"(docs/rules/security.md §7); sharing one means the network a guest is on can reach the console",
					name, l.name, port)
			}
		}
		seen[l.name] = l.value
	}
	return nil
}

func isWildcard(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::" || host == "[::]"
}

// LoopbackOnly reports whether this address can only be reached from the machine
// itself. A wildcard bind is not loopback even though it includes it.
//
// Exported because `foyerctl doctor` asks the same question about the same address
// and must answer it the same way. Two implementations of "is this reachable from
// the network" is how a diagnostic comes to disagree with a startup assertion.
func LoopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if isWildcard(host) {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// reachableHost turns a bind address into something a browser can be sent to.
//
// A wildcard bind is not an address anybody can navigate to, so it becomes
// loopback — which is right for a developer and visibly wrong for a deployment,
// where `FOYER_PORTAL_URL` is the value that has to be set. Guessing the machine's
// outward address would produce a `user-portal-url` that is right in development
// and wrong everywhere else, which is the worst place for it to be wrong.
func reachableHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if isWildcard(host) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// httpOrigin is the scheme allowlist §4 states for anything a guest's browser is
// pointed at. No `file://`, no protocol-relative, no userinfo.
//
// Its refusals reach the log and the console, so they quote the value through
// shownURL, which never repeats userinfo.
func httpOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s is not an absolute http(s) URL", shownURL(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s does not have an http or https scheme", shownURL(raw))
	}
	if u.User != nil {
		return fmt.Errorf("%s carries userinfo, which never belongs in a URL a guest is sent to", shownURL(raw))
	}
	return nil
}

// shownURL is a URL as a refusal may quote it: without its userinfo.
//
// `user:password@` is the one part of a URL that can be a credential, and a
// startup refusal is printed to a log anybody with the container can read. A
// value that parses has the userinfo removed and says so; one that does not
// parse — or parses as something with no authority, as `user:pw@host` does —
// cannot be cut reliably, so if it has an `@` anywhere it is not repeated at all.
func shownURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		c := *u
		c.User = nil
		return fmt.Sprintf("%q (userinfo removed)", c.String())
	}
	if strings.Contains(raw, "@") {
		return "the value given (not repeated: it contains '@', which may be userinfo)"
	}
	return fmt.Sprintf("%q", raw)
}

// KnownDefault reports whether value contains one of the known defaults, and
// which. Exported for `foyerctl migrate`, which sets the database login passwords
// and must refuse the lab's published ones by the same list the control plane
// refuses them by — a second list would agree with itself the day this one grew.
func KnownDefault(value string) (string, bool) { return knownDefault(value) }

func knownDefault(value string) (string, bool) {
	lower := strings.ToLower(value)
	for _, d := range knownDefaults {
		if strings.Contains(lower, d) {
			return d, true
		}
	}
	return "", false
}

// splitList reads a list from one environment variable.
//
// Commas *and* whitespace, because the two producers of this value write it
// differently and neither is wrong: a human writes "a, b", and `hostname -I`
// emits space-separated addresses with a trailing space. Accepting one form
// would make the shell pipeline that feeds it do string surgery, which is where
// a name gets mangled into something that is silently not a name.
func splitList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// MaintenanceSplitError is the control plane's half of the maintenance split: with
// FOYER_MAINTENANCE_URL set the maintenance jobs and the retention half of an
// erasure run in another container, so **this process must not also be given the
// logins that container exists to hold**. Setting both is refused rather than
// tolerated, because a deployment that gives the control plane the reaper's or the
// retention role's URL "as well" has undone the split without anything saying so.
func (c Config) MaintenanceSplitError() error {
	if c.MaintenanceURL == "" {
		return nil
	}
	if c.MaintenanceToken.Err != nil {
		return c.MaintenanceToken.Err
	}
	if c.MaintenanceToken.Value == "" {
		return fmt.Errorf("FOYER_MAINTENANCE_URL is set and FOYER_MAINTENANCE_TOKEN is not: the maintenance container refuses a caller with no token")
	}
	for _, s := range []struct {
		name string
		v    SecretValue
	}{{"FOYER_REAPER_DATABASE_URL", c.ReaperDatabase}, {"FOYER_RETENTION_DATABASE_URL", c.RetentionDatabase}} {
		if s.v.Value != "" || s.v.Err != nil {
			return fmt.Errorf("FOYER_MAINTENANCE_URL is set, so the maintenance jobs run in their own container, and %s is set here too: the control plane must not hold the login that container exists to keep from it", s.name)
		}
	}
	return nil
}

// MaintenanceProcessError is what `foyer maintenance` refuses to start without:
// the application's login for the jobs that run as foyer_app, a
// token to authenticate the control plane by, and an address — and **no KEK**. The
// maintenance jobs unseal nothing, so a container that exists to hold two logins
// the control plane must not have is not also given the key that unseals every
// secret in the database.
func (c Config) MaintenanceProcessError() error {
	if c.secretErr != nil {
		return c.secretErr
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("FOYER_DATABASE_URL is not set: the maintenance jobs that run as the application need its login")
	}
	if def, found := knownDefault(c.DatabaseURL); found && !c.LabInsecure {
		return fmt.Errorf("FOYER_DATABASE_URL contains the known default %q", def)
	}
	if c.KEK != "" {
		return fmt.Errorf("FOYER_KEK is set for the maintenance container: it unseals nothing and must not hold the key that unseals everything")
	}
	if c.MaintenanceToken.Err != nil {
		return c.MaintenanceToken.Err
	}
	if len(c.MaintenanceToken.Value) < 32 {
		return fmt.Errorf("FOYER_MAINTENANCE_TOKEN is unset or shorter than 32 characters: the maintenance container answers only a caller that presents it")
	}
	if _, _, err := net.SplitHostPort(c.MaintenanceAddr); err != nil {
		return fmt.Errorf("FOYER_MAINTENANCE_ADDR = %q is not host:port", c.MaintenanceAddr)
	}
	return nil
}
