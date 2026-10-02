package store

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Who logs in, and why RESET ROLE gains nothing.
//
// Until migration 00019 every process authenticated as the schema owner — a
// superuser in `deploy/` — and the application then issued `SET ROLE foyer_app`.
// `SET ROLE` is not a boundary: any statement in the session can `RESET ROLE` and
// be the superuser again, so one SQL injection was a shell on the database host.
// FreeRADIUS used the same login and dropped nothing at all.
//
// Now the two processes log in as the privilege roles themselves. The session user
// of the control plane IS `foyer_app`; `RESET ROLE` lands there. That is the whole
// property, and `RequireLeastPrivilege` is the check that refuses to run without it.
//
// PostgREST's `authenticator` is the well-known design that keeps `SET ROLE` and
// makes it safe by giving the login role no privilege of its own. Foyer has one
// application role, not one per end user, so the simpler shape — log in as it —
// buys the same property with nothing to get wrong.

const (
	// RadiusRole is the role FreeRADIUS logs in as: SELECT on `nas` and on the
	// credential view, SELECT/INSERT/UPDATE on `radacct`, SELECT/INSERT on
	// `radpostauth`, and nothing else in the schema. Migration 00002 grants it and
	// 00019 lets it log in.
	RadiusRole = "foyer_radius"

	// ReaperRole is the stale-session reaper's role (migration 00001): it closes
	// accounting rows FreeRADIUS never closed. Migration 00021 lets it log in,
	// because since 00019 the control plane's login may not SET ROLE into it.
	ReaperRole = "foyer_reaper"
)

// loginRoles are the roles SetLoginPassword will set a password on. A closed set,
// because the function runs as the schema owner and a role name reaches ALTER ROLE
// as an identifier, and a caller wanting to set the superuser's password is a
// caller to refuse. `foyer_reaper` and `foyer_retention` joined it with migration
// 00021, when cmd/foyer's maintenance jobs began to run as them: after 00019 the
// application's login is a member of no other role, so a SET ROLE from its pool is
// refused, and a login of their own is the only way those jobs run at all.
var loginRoles = map[string]bool{DefaultAppRole: true, RadiusRole: true, ReaperRole: true, RetentionRole: true}

// MinLoginPasswordLength is the shortest password SetLoginPassword accepts.
// deploy/first-boot.sh generates 48 hex characters (192 bits); sixteen is the
// floor below which the value is a human's choice rather than a generated one.
const MinLoginPasswordLength = 16

// scramIterations is PostgreSQL's own default (`scram_iterations`, 4096), so a
// verifier set here is indistinguishable from one `\password` would have set.
const scramIterations = 4096

// SetLoginPassword sets role's password, connecting to ownerURL as a role that may
// ALTER it — in practice the schema owner, from `foyerctl migrate`.
//
// **The password never reaches the server.** `ALTER ROLE … PASSWORD 'x'` sends x in
// the clear, and PostgreSQL's own documentation warns that it may then be logged by
// the server (`log_statement`, an error context, `pg_stat_statements`). psql's
// `\password` avoids that by computing the SCRAM-SHA-256 verifier on the client
// (libpq's PQencryptPasswordConn) and sending only that; this does the same. A
// verifier is not a credential: SCRAM stores StoredKey = H(ClientKey), and a client
// has to prove knowledge of ClientKey itself.
//
// Idempotent in effect: running it twice with the same password leaves the same
// password under a fresh salt. Established connections are unaffected.
func SetLoginPassword(ctx context.Context, ownerURL, role, password string) error {
	if !loginRoles[role] {
		return fmt.Errorf("store: %q is not a login role this package provisions (want %s, %s, %s or %s)", role, DefaultAppRole, RadiusRole, ReaperRole, RetentionRole)
	}
	verifier, err := scramVerifier(password)
	if err != nil {
		return fmt.Errorf("store: password for %s: %w", role, err)
	}

	conn, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		return fmt.Errorf("store: connecting to set the %s password: %w", role, err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Utility statements take no bind parameters, so the verifier is a literal.
	// It is quoted anyway, and scramVerifier's output is base64, digits, `$` and
	// `:` by construction — verifierShape re-checks that rather than trusting it.
	if !verifierShape.MatchString(verifier) {
		return errors.New("store: refusing a SCRAM verifier of unexpected shape")
	}
	//
	// PASSWORD only, never LOGIN: whether a role may log in is migration 00019's
	// decision, recorded in the schema's history, and a provisioning step that
	// quietly granted it would make that migration decorative. So a database that
	// has not been migrated is refused here, by name, instead of being fixed.
	var canLogin, super bool
	err = conn.QueryRow(ctx, `SELECT rolcanlogin, rolsuper FROM pg_roles WHERE rolname = $1`, role).Scan(&canLogin, &super)
	if err != nil {
		return fmt.Errorf("store: reading role %s: %w (has `foyerctl migrate` run?)", role, err)
	}
	if super {
		return fmt.Errorf("store: role %s is a SUPERUSER; refusing to give it a password", role)
	}
	if !canLogin {
		return fmt.Errorf("store: role %s is NOLOGIN; migrations 00019 and 00021 make it a login role, so run `foyerctl migrate` first", role)
	}
	stmt := "ALTER ROLE " + pgx.Identifier{role}.Sanitize() + " WITH PASSWORD '" + verifier + "'"
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("store: setting the %s password: %w", role, err)
	}
	return nil
}

var verifierShape = regexp.MustCompile(`^SCRAM-SHA-256\$[0-9]+:[A-Za-z0-9+/=]+\$[A-Za-z0-9+/=]+:[A-Za-z0-9+/=]+$`)

// scramVerifier computes the string PostgreSQL stores in pg_authid.rolpassword for
// a SCRAM-SHA-256 password, per RFC 5802 §3 and RFC 7677:
//
//	SaltedPassword = PBKDF2-HMAC-SHA-256(password, salt, i)
//	ClientKey = HMAC(SaltedPassword, "Client Key");  StoredKey = SHA-256(ClientKey)
//	ServerKey = HMAC(SaltedPassword, "Server Key")
//	→ SCRAM-SHA-256$<i>:<salt>$<StoredKey>:<ServerKey>
//
// RFC 5802 runs the password through SASLprep first. For printable ASCII that is
// the identity, so the password is confined to printable ASCII rather than a
// second implementation of stringprep being written here: a generated password is
// hex, and a human-chosen one outside that range is refused by name rather than
// silently hashed into a verifier the server would compute differently.
func scramVerifier(password string) (string, error) {
	if len(password) < MinLoginPasswordLength {
		return "", fmt.Errorf("it is %d characters; the floor is %d, and deploy/first-boot.sh generates 48", len(password), MinLoginPasswordLength)
	}
	for _, r := range password {
		if r < 0x21 || r > 0x7e {
			return "", errors.New("it contains a character outside printable ASCII; SCRAM would SASLprep it and the verifier computed here could disagree with the server's")
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return scramVerifierWith(password, salt, scramIterations)
}

func scramVerifierWith(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	clientKey := mac(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := mac(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(storedKey[:]), b64(serverKey)), nil
}

// LoginPrivilege is what a connection authenticated as, as PostgreSQL sees it.
type LoginPrivilege struct {
	SessionUser string
	Superuser   bool
	BypassRLS   bool
	CreateRole  bool
	CreateDB    bool
	Replication bool
	// MemberOf is every role the session user can SET ROLE to (directly or
	// through a chain), other than itself. `RESET ROLE` followed by `SET ROLE x`
	// is available for every x here.
	MemberOf []string
	// OwnedRelations counts tables, views and sequences the session user owns in
	// this database. An owner can ALTER, DROP and — without FORCE — read past
	// row security; the application owning anything is the migrator's job leaking.
	OwnedRelations int
}

// Problems lists every way this login holds more than role's privileges. Empty
// means `RESET ROLE` gains nothing.
func (p LoginPrivilege) Problems(role string) []string {
	var out []string
	if p.Superuser {
		out = append(out, "it is a SUPERUSER")
	}
	if p.BypassRLS {
		out = append(out, "it has BYPASSRLS")
	}
	if p.CreateRole {
		out = append(out, "it has CREATEROLE")
	}
	if p.CreateDB {
		out = append(out, "it has CREATEDB")
	}
	if p.Replication {
		out = append(out, "it has REPLICATION")
	}
	for _, m := range p.MemberOf {
		if m != role {
			out = append(out, "it can SET ROLE "+m)
		}
	}
	if p.OwnedRelations > 0 {
		out = append(out, fmt.Sprintf("it owns %d relation(s) in this database", p.OwnedRelations))
	}
	if p.SessionUser != role && !contains(p.MemberOf, role) {
		out = append(out, "it is not "+role+" and cannot become it")
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// querier is the one method LoginPrivileges needs; *pgxpool.Pool and *pgx.Conn
// both have it.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// LoginPrivileges reads what the connection behind q authenticated as. It asks
// about `session_user`, never `current_user`: after `SET ROLE` the two differ, and
// the session user is what `RESET ROLE` returns to.
func LoginPrivileges(ctx context.Context, q querier) (LoginPrivilege, error) {
	var p LoginPrivilege
	err := q.QueryRow(ctx, `
		SELECT r.rolname, r.rolsuper, r.rolbypassrls, r.rolcreaterole, r.rolcreatedb, r.rolreplication,
		       coalesce((SELECT array_agg(m.rolname ORDER BY m.rolname)
		                 FROM pg_roles m
		                 WHERE m.oid <> r.oid
		                   AND pg_has_role(r.oid, m.oid, 'MEMBER')), '{}'),
		       (SELECT count(*) FROM pg_class c WHERE c.relowner = r.oid)
		FROM pg_roles r
		WHERE r.rolname = session_user`).Scan(
		&p.SessionUser, &p.Superuser, &p.BypassRLS, &p.CreateRole, &p.CreateDB, &p.Replication,
		&p.MemberOf, &p.OwnedRelations)
	if err != nil {
		return p, fmt.Errorf("store: reading the session user's privileges: %w", err)
	}
	return p, nil
}

// ErrOverPrivilegedLogin is returned by NewStore when the connection string
// authenticates as something RESET ROLE could escalate from.
var ErrOverPrivilegedLogin = errors.New("store: the database login holds more than the application role's privileges")

// RequireLeastPrivilege refuses a store whose login is more than role.
func RequireLeastPrivilege(ctx context.Context, s *Store, role string) error {
	p, err := LoginPrivileges(ctx, s.pool)
	if err != nil {
		return err
	}
	if problems := p.Problems(role); len(problems) > 0 {
		return fmt.Errorf("%w: FOYER_DATABASE_URL logs in as %q, and %s. "+
			"The control plane must log in as %s itself, so that RESET ROLE gains nothing. "+
			"Run `foyerctl migrate` as the schema owner with FOYER_APP_DB_PASSWORD set, then use "+
			"postgres://%s:<that password>@… here (deploy/first-boot.sh does both)",
			ErrOverPrivilegedLogin, p.SessionUser, strings.Join(problems, "; "), role, role)
	}
	return nil
}
