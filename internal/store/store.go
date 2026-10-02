// Package store is Foyer's PostgreSQL layer: the migrations that define the
// schema, and one repository per aggregate.
//
// Two properties of this package are enforced by the database rather than by the
// code, because a rule the code enforces is a rule the next caller can forget:
//
//   - The application logs in as `foyer_app`, which holds SELECT and nothing else
//     on `radacct` and `radpostauth`. An INSERT from Go is refused by PostgreSQL,
//     not by a code review. `readonly_test.go` makes that failure happen.
//   - Every tenant-scoped table carries a row-level policy keyed on a session GUC
//     this package sets on connect. An unset GUC matches no row, so a forgotten
//     scope is an empty result rather than a cross-tenant read.
//
// See CONTRACT.md for the invariants.
package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/kiennt048/foyer/internal/config"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a lookup resolves no row. Callers distinguish it
// from a transport failure with errors.Is; the API layer maps it to
// /problems/not-found and nothing else does.
var ErrNotFound = errors.New("store: record not found")

// ErrConflict is returned when a write violates a uniqueness constraint. It is the
// contract's /problems/duplicate, kept distinct from a generic write failure so a
// handler never renders "duplicate" for a dead connection.
var ErrConflict = errors.New("store: conflicting record")

const (
	// DefaultAppRole is the PostgreSQL role the application runs as, and since
	// migration 00019 the role it LOGS IN as.
	//
	// Authenticating as the schema owner and then dropping into this role is not
	// a privilege reduction: `SET ROLE` is undone by `RESET ROLE`, which any
	// statement in the session may issue, so a control plane logged in as the
	// owner would be one SQL injection from being a superuser. NewStore refuses
	// that login; see loginroles.go.
	//
	// `Open` still issues `SET ROLE` — the test harness and the differential
	// harness authenticate as the owner and need it — but the product does not
	// rely on it for anything. When the login is foyer_app, the SET is a no-op.
	DefaultAppRole = "foyer_app"

	// DefaultTenantID is the single tenant v1 ships with. Migration 00001 inserts
	// exactly this row.
	DefaultTenantID = "00000000-0000-0000-0000-000000000001"

	// NoRole is the value Config.Role takes to mean "issue no SET ROLE at all",
	// leaving the connection with whatever privileges it authenticated with.
	//
	// It is a sentinel and not the empty string, and that distinction is the whole point.
	// The empty string cannot disable the SET: `withDefaults` substitutes
	// `foyer_app` first, so `Open` never sees an empty role, and a branch that
	// honoured one **would never execute**. A store opened as the schema owner
	// would still run as the application role and answer `permission denied for
	// table tenants`, which reads as a broken grant rather than as an option that
	// does not exist.
	//
	// Empty means *unset*, and unset takes the default, because that is what every
	// caller wants and making "" mean NoRole would silently stop de-privileging all
	// of them.
	//
	// `-` cannot collide with a real role name: `roleName` forbids it, and Open
	// checks every other value against that pattern before it reaches SET ROLE.
	NoRole = "-"
)

// roleName is deliberately strict. The role reaches SET ROLE, which takes an
// identifier rather than a parameter, so it is quoted rather than bound and the
// only safe input is one that cannot need quoting in the first place.
var roleName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Config describes how to reach PostgreSQL and who to be once there.
type Config struct {
	// URL is the libpq connection string.
	URL string

	// Role is SET on every connection in the pool. Empty means unset and takes
	// DefaultAppRole; NoRole disables the SET entirely, which is correct only when
	// the connection already authenticates as a role with the right privileges --
	// a deployment that issues foyer_app its own password, or a harness that has to
	// seed as the schema owner. It is never correct as a way to get past a
	// permission error.
	Role string

	// TenantID is written to the `foyer.tenant_id` GUC on every connection and is
	// what every row-level policy compares against.
	TenantID string

	// Keys unseals the envelope-encrypted columns: RADIUS shared secrets,
	// controller credentials and NT hashes. Nil is allowed -- a deployment that
	// has not configured a KEK can still read everything else -- and every
	// repository that touches a sealed column returns ErrNoKeyring rather than
	// silently handing back ciphertext.
	Keys *Keyring

	MaxConns int32
	MinConns int32
}

func (c Config) withDefaults() Config {
	if c.Role == "" {
		c.Role = DefaultAppRole
	}
	if c.TenantID == "" {
		c.TenantID = DefaultTenantID
	}
	if c.MaxConns <= 0 {
		c.MaxConns = 25
	}
	if c.MinConns < 0 {
		c.MinConns = 0
	}
	return c
}

// Store owns the connection pool and hands out one repository per aggregate.
//
// It is not itself a repository. Loose package functions reachable from every
// caller name no boundary at all; the accessors below are the boundary, and `Accounting()` being the only one with no
// write method is the shape of the read-only rule.
type Store struct {
	pool *pgxpool.Pool
	cfg  Config

	sites         *SiteRepo
	operators     *OperatorRepo
	authn         *OperatorSessionRepo
	controllers   *ControllerRepo
	secrets       *SharedSecretRepo
	clients       *RadiusClientRepo
	nasProjection *NasProjectionRepo
	aps           *AccessPointRepo
	vouchers      *VoucherRepo
	accounts      *AccountRepo
	accounting    *AccountingRepo
	garden        *GardenRepo
	portal        *PortalRepo
	grants        *GrantRepo
	audit         *AuditRepo
}

// Open connects, verifies the connection, and wires the repositories.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	cfg = cfg.withDefaults()

	if cfg.Role != NoRole && !roleName.MatchString(cfg.Role) {
		return nil, fmt.Errorf("store: refusing role name %q: must match %s", cfg.Role, roleName)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("store: parsing connection string: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 30 * time.Minute

	// Every connection in the pool arrives already scoped and already
	// de-privileged. Doing it here rather than per query is what makes it
	// impossible to forget: there is no code path that acquires a connection and
	// skips this.
	role, tenant := cfg.Role, cfg.TenantID
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if _, err := conn.Exec(ctx, `SELECT set_config('foyer.tenant_id', $1, false)`, tenant); err != nil {
			return fmt.Errorf("store: setting tenant scope: %w", err)
		}
		if role == NoRole {
			return nil
		}
		// SET ROLE last: everything after it, for the life of this connection,
		// runs with the application role's privileges.
		if _, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			return fmt.Errorf("store: assuming role %q: %w", role, err)
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("store: creating connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: connecting: %w", err)
	}

	s := &Store{pool: pool, cfg: cfg}
	s.sites = &SiteRepo{s: s}
	s.operators = &OperatorRepo{s: s}
	s.authn = &OperatorSessionRepo{s: s}
	s.controllers = &ControllerRepo{s: s}
	s.secrets = &SharedSecretRepo{s: s}
	s.clients = &RadiusClientRepo{s: s}
	s.nasProjection = &NasProjectionRepo{s: s}
	s.aps = &AccessPointRepo{s: s}
	s.vouchers = &VoucherRepo{s: s}
	s.accounts = &AccountRepo{s: s}
	s.accounting = &AccountingRepo{s: s}
	s.garden = &GardenRepo{s: s}
	s.portal = &PortalRepo{s: s}
	s.grants = &GrantRepo{s: s}
	s.audit = &AuditRepo{s: s}
	return s, nil
}

// NewStore opens a store with the default role, tenant and keyring-from-environment.
//
// It is the entry point cmd/foyer and cmd/foyerctl use. FOYER_KEK being unset is
// not an error here: the process may only need to read sites and sessions, and
// refusing to start would take the whole control plane down over a column it never
// touches. The repositories that do touch a sealed column refuse individually.
//
// **It refuses a login that holds more than foyer_app** — a superuser, the schema
// owner, anything that can SET ROLE elsewhere — with ErrOverPrivilegedLogin. That
// refusal is the product's half: migration 00019 makes a least-privileged
// login possible, and this makes it the only kind the control plane accepts.
// There is deliberately no lab lift. The lab logs in as foyer_app too, because a
// lab that ran as the superuser would be the one place nobody noticed a grant the
// application now needs and does not have.
func NewStore(ctx context.Context, connString string) (*Store, error) {
	keys, err := KeyringFromEnv()
	if err != nil {
		return nil, err
	}
	return NewStoreWithKeyring(ctx, connString, keys)
}

// NewStoreWithKeyring is NewStore with the keyring supplied rather than read from
// the environment — `cmd/foyer`'s entry point, which reads FOYER_KEK once in
// config.Load. Every refusal NewStore makes, this makes; NewStore is this.
func NewStoreWithKeyring(ctx context.Context, connString string, keys *Keyring) (*Store, error) {
	s, err := Open(ctx, Config{URL: connString, Keys: keys})
	if err != nil {
		return nil, err
	}
	if err := RequireLeastPrivilege(ctx, s, DefaultAppRole); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close releases every connection.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Pool exposes the underlying pool. Tests and the differential harness use it;
// product code goes through a repository.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Keys returns the configured keyring, which may be nil.
func (s *Store) Keys() *Keyring { return s.cfg.Keys }

func (s *Store) Sites() *SiteRepo                 { return s.sites }
func (s *Store) Operators() *OperatorRepo         { return s.operators }
func (s *Store) Authn() *OperatorSessionRepo      { return s.authn }
func (s *Store) Controllers() *ControllerRepo     { return s.controllers }
func (s *Store) SharedSecrets() *SharedSecretRepo { return s.secrets }
func (s *Store) RadiusClients() *RadiusClientRepo { return s.clients }
func (s *Store) AccessPoints() *AccessPointRepo   { return s.aps }
func (s *Store) Vouchers() *VoucherRepo           { return s.vouchers }
func (s *Store) Accounts() *AccountRepo           { return s.accounts }

// Accounting is the only repository allowed to read radacct and radpostauth, and
// it has no write method. The database agrees: see migration 00002.
func (s *Store) Accounting() *AccountingRepo { return s.accounting }

func (s *Store) Garden() *GardenRepo { return s.garden }
func (s *Store) Portal() *PortalRepo { return s.portal }

// Grants owns the guest portal's browser-to-MAC bindings and the grants they earn.
// It is separate from Portal() because `portal_settings` is what an operator
// configures and these two tables are what a guest produces.
func (s *Store) Grants() *GrantRepo { return s.grants }

func (s *Store) Audit() *AuditRepo { return s.audit }

// tx runs fn inside a transaction, rolling back on error.
func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// KeyringFromEnv reads FOYER_KEK, or FOYER_KEK_FILE. An unset variable yields
// (nil, nil): see NewStore for why that is not an error at this level.
//
// The _FILE form is not optional decoration — deploy/compose.yaml supplies the key
// that way and no other, because a key in an environment variable is readable
// through `docker inspect` and /proc/<pid>/environ, and FOYER_KEK unseals every
// enveloped column in the database.
//
// It goes through internal/config rather than os.Getenv, and the failure that
// avoids is worth naming because of its shape rather than its size. Unset yields
// (nil, nil) BY DESIGN, so a reader that ignored FOYER_KEK_FILE would complain
// about nothing: the control plane would start, report healthy, pass its own
// doctor, and then answer 500 to the first controller registration. A convention
// half a codebase implements is worse than one nobody does, because the half that
// does makes the other half look deliberate.
func KeyringFromEnv() (*Keyring, error) {
	spec, err := config.EnvOrFile("FOYER_KEK")
	if err != nil {
		return nil, err
	}
	return KeyringFromSpec(spec)
}

// KeyringFromSpec is KeyringFromEnv for a caller that has already read FOYER_KEK:
// `cmd/foyer`, whose config.Load reads it once for every use in the process. An
// empty spec yields (nil, nil), exactly as an unset variable does.
func KeyringFromSpec(spec string) (*Keyring, error) {
	if spec == "" {
		return nil, nil
	}
	return NewKeyring(spec)
}
