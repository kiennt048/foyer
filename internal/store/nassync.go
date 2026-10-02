package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// NasProjectionRepo owns `nas`, the table FreeRADIUS reads with
// `read_clients = yes`.
//
// It is a repository of its own rather than three methods on RadiusClientRepo
// because the two tables are not the same thing and the difference is the whole
// point. `radius_clients` is the system of record: it is what an operator edits,
// what the console lists, and what carries the tenant, the driver and the
// capabilities. `nas` is a projection of it into the six columns rlm_sql reads,
// plus the one cleartext copy of a shared secret that exists anywhere in the
// deployment.
//
// Nothing wrote that projection until this file existed, which meant a freshly
// migrated database had an empty client table and **FreeRADIUS discarded every
// packet as coming from an unknown client**.
type NasProjectionRepo struct{ s *Store }

// NasRow is one row of the projection, in the columns rlm_sql actually reads.
//
// rlm_sql maps six columns positionally -- `id, nasname, shortname, type, secret,
// server` -- and reordering them hands FreeRADIUS the shortname as a shared secret
// and it starts anyway. The row id is not here because nothing outside the table
// uses it.
//
// This is deliberately a different type from `raddb.ClientRow`, which has the same
// five fields. Making them one type would mean `internal/store` importing the
// generator or the generator importing the store, and neither should know the
// other exists: the composition root maps between them, which is five lines and
// keeps the dependency out of both packages.
type NasRow struct {
	NASName   string
	Shortname string
	Type      string
	Secret    string
	Server    string
}

// NasSkip is a RADIUS client that could not be projected, and why.
//
// A skipped client is not a warning to swallow: FreeRADIUS will discard every
// packet from that address, the guest sees a network that does not work, and
// nothing else in the product says so. The caller is expected to surface these.
type NasSkip struct {
	ClientID string
	Address  string
	Reason   string
}

// NasSync is what one sync did.
//
// Removed is kept separately from the rest because it is the half that is easy to
// leave out and impossible to notice: a sync that only ever inserts and updates
// looks identical in every test that checks a client works, and leaves a
// decommissioned access point holding a valid RADIUS client. That is a security
// defect rather than an untidiness.
type NasSync struct {
	Inserted  int
	Updated   int
	Unchanged int

	// Removed names the `nas` rows this sync deleted, by `nasname`.
	//
	// **Names, not a count.** It was `int`, and a count cannot be handed to
	// `raddb.TableBefore` — so `foyerctl sync-nas` classified a revocation with no
	// idea which rows had gone, and `Classify` compared two sets that both lacked
	// a row written after the last restart and deleted before the next one. It
	// answered `none`: *the deletion took effect*. Measured on 3.2.10 it has not —
	// the running server keeps answering that client from the definition it
	// cached. See `raddb.TableBefore`, which is variadic for the same reason this
	// is a slice: *nobody looked* and *nothing was removed* are opposite answers,
	// and a number cannot tell them apart either.
	//
	// It carries `nasname` alone. `TableBefore` reads names only and nothing else
	// wants a shared secret for a row that no longer exists.
	Removed []string

	// Rows is the projection as it now stands for this tenant, ordered by
	// nasname.
	//
	// **Not the startup snapshot**, for the reason spelled out on Rows and
	// AllRows: FreeRADIUS loads the whole table and this is one tenant's slice of
	// it. Use AllRows for a snapshot, at the moment the server is restarted rather
	// than at the moment the sync finished.
	Rows []NasRow

	// Skipped names the clients that were not projected.
	Skipped []NasSkip

	// At is when the read of `radius_clients` happened.
	At time.Time
}

// ErrClientAddressTaken is returned when a NAS address is already projected by
// another tenant.
//
// `radius_clients` is UNIQUE (tenant_id, address); `nas.nasname` is UNIQUE full
// stop, because FreeRADIUS's client list is one flat global namespace with no
// tenant in it -- and `nasname` is the value the server *matches a packet's source
// address against*, so it cannot be prefixed, scoped or synthesised out of the
// collision. Two tenants may legitimately hold the same address, and on private
// ranges two venues sharing 192.168.1.1 is the common case rather than an edge
// one. Only one of them can ever be projected.
//
// The sync refuses rather than taking the row over, and the reason is not tidiness.
// Last-writer-wins here points one tenant's access points at another tenant's
// shared secret: a cross-tenant authentication bypass, arriving with an audit trail
// that looks entirely normal.
var ErrClientAddressTaken = errors.New("store: NAS address is already projected by another tenant")

// NasAddressConflict is ErrClientAddressTaken with the facts an operator needs
// attached, rather than only in the sentence.
//
// It is a type and not just a formatted string because the message has to reach
// somewhere an operator looks. An error whose only reader is a log file is one that
// gets discovered during an outage, and the fields below are what a console row or
// an API problem document would need to render: which address, which two tenants,
// and which client on each side.
type NasAddressConflict struct {
	NASName string
	// Tenant is the tenant whose sync was refused; OtherTenant already holds the
	// row. Both are named because "already taken" without saying by whom sends an
	// operator to the wrong database.
	Tenant      string
	OtherTenant string
	// ClientID is the radius_clients row that could not be projected.
	ClientID string
	// OtherClientID is the radius_clients row that holds the projection, or empty
	// where the existing row was seeded by hand and has no system of record.
	OtherClientID string
}

func (e *NasAddressConflict) Error() string {
	held := "a row seeded by hand with no radius_clients row behind it"
	if e.OtherClientID != "" {
		held = "client " + e.OtherClientID
	}
	// An empty OtherTenant would print as `held by tenant ` and read like a bug in
	// this formatter rather than a fact about the row. Sync cannot reach here with
	// one -- an untenanted row is claimed, not refused -- so this is the case that
	// only a concurrent writer produces, and it is named rather than left blank.
	by := "tenant " + e.OtherTenant
	if e.OtherTenant == "" {
		by = "no tenant"
	}
	return fmt.Sprintf(
		"%s: %s is held by %s (%s); tenant %s cannot also project client %s onto it, "+
			"because FreeRADIUS matches a packet's source address against nasname and its client list has no tenant in it",
		ErrClientAddressTaken.Error(), e.NASName, by, held, e.Tenant, e.ClientID)
}

func (e *NasAddressConflict) Unwrap() error { return ErrClientAddressTaken }

// NasProjection returns the repository that owns the client table FreeRADIUS
// reads.
func (s *Store) NasProjection() *NasProjectionRepo { return s.nasProjection }

// Rows returns the projection as it currently stands for this tenant.
//
// **It is not the startup snapshot.** A startup snapshot has to be what the running server loaded, and FreeRADIUS reads
// this table with `read_clients = yes` over a connection that never sets the
// tenant GUC -- so it loads every row, including another tenant's and including
// the hand-seeded ones with no tenant at all. A snapshot missing a row the server
// loaded classifies that client as new, and a new client inside a dynamic network
// classifies `sql-only`: the operator is told a secret rotation needs no restart
// while the running server goes on refusing every packet on the new secret.
//
// Use AllRows for a snapshot. This is the tenant's own view -- what an operator
// sees, what a console lists -- and it is scoped for the same reason Sync's DELETE
// is.
func (r *NasProjectionRepo) Rows(ctx context.Context) ([]NasRow, error) {
	return r.rows(ctx, `WHERE tenant_id = foyer_current_tenant()`)
}

// AllRows returns every row FreeRADIUS will load, with no tenant scope at all.
//
// The absence of a scope is the point, so it is stated rather than left to be
// noticed: `nas` carries no row-level policy -- deliberately, because a policy here
// yields a server that loads no clients and discards every packet -- and
// FreeRADIUS's client list is one flat namespace. A snapshot of what the server
// loaded is therefore a whole-table read by construction, and any narrower one is
// a snapshot of a server that does not exist.
//
// It reads `nas.secret`, which is the one declared cleartext copy of a shared
// secret in the deployment. Callers taking a snapshot must put it through
// raddb.WriteStartupSnapshot, which digests every secret before anything reaches
// disk; nothing else should be calling this.
func (r *NasProjectionRepo) AllRows(ctx context.Context) ([]NasRow, error) {
	return r.rows(ctx, "")
}

func (r *NasProjectionRepo) rows(ctx context.Context, where string) ([]NasRow, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT nasname, shortname, type, secret, coalesce(server, '')
		FROM `+nasTable+` `+where+`
		ORDER BY nasname`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	out := []NasRow{}
	for rows.Next() {
		var n NasRow
		if err := rows.Scan(&n.NASName, &n.Shortname, &n.Type, &n.Secret, &n.Server); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, n)
	}
	return out, mapErr(rows.Err())
}

// nasTable is the projection's name. It is a constant rather than a parameter
// because `raddb.Config.ClientTable` defaults to it and the generator interpolates
// that value into the queries FreeRADIUS runs; two places naming the table is
// already one too many, and a third that could disagree at runtime would be a
// server reading an empty table and saying nothing.
const nasTable = "nas"

// nasType is what goes in the `type` column, always.
//
// FreeRADIUS's nas_type is a hint for `checkrad`'s simultaneous-use check and its
// vocabulary is cisco / livingston / portslave / computone / other. It is not the
// vendor family. `radius_clients.driver` is a different axis entirely -- it names
// which NASDriver performs out-of-band actions -- and writing "unifi" into a field
// whose values mean something else would be a value FreeRADIUS does not recognise
// dressed up as configuration.
const nasType = "other"

// Sync writes `nas` from `radius_clients` joined to `shared_secrets`, and removes
// every row this tenant's system of record no longer produces.
//
// The exact write surface, because a synchronisation between two tables is the
// classic place for a function that runs, returns nil and copies nothing:
//
//   - It reads `radius_clients` (enabled rows only) under row-level security, so
//     it sees one tenant, and unseals each client's shared secret through
//     `SharedSecrets().Reveal` -- one call per client, not a join, because the
//     ciphertext is bound to `table/column/row-id` and only that repository knows
//     how to open it. No keyring is ErrNoKeyring, not a silent skip: a projection
//     written without secrets is a client table that authenticates nothing.
//   - That read happens **before** the transaction below, so a broken keyring
//     fails before anything is written. The window between the read and the write
//     is a real one: a client registered inside it is projected by the next sync
//     rather than this one, which is the same statement as "the sync is a
//     snapshot", and is why nothing here reports what it did as current.
//   - It INSERTs rows that are absent, UPDATEs rows whose five columns differ or
//     whose tenant is NULL, and touches nothing else. An unchanged client this
//     tenant already owns is not rewritten.
//   - It DELETEs every row whose nasname is not in the wanted set and whose
//     tenant_id is this tenant's **or NULL** -- the second covers rows nothing
//     here wrote, such as `lab/blue-green/seed-client.sh`'s. That is intended: the
//     projection is not a place to keep things.
//   - It never writes, claims or deletes a row held by another tenant. That case
//     is ErrClientAddressTaken and it aborts the whole sync; see
//     NasAddressConflict for why refusing beats taking over.
//   - The whole of it is one transaction. A partial projection is a subset of the
//     estate that works and a subset that does not, with nothing to say which.
//
// What it does not do: it does not restart FreeRADIUS, and it does not decide
// whether one is needed. `raddb.Classify` answers that from the startup snapshot,
// and the answer is not derivable from here -- a row this function writes may be
// picked up without a restart or may not, depending on whether the running server
// loaded that client at startup, which is a fact about the server and not about
// the table.
func (r *NasProjectionRepo) Sync(ctx context.Context) (NasSync, error) {
	var res NasSync
	res.At = time.Now().UTC()

	if r.s.cfg.Keys == nil {
		return res, ErrNoKeyring
	}

	wanted, skipped, err := r.wantedRows(ctx, true)
	if err != nil {
		return res, err
	}
	res.Skipped = skipped

	err = r.s.tx(ctx, func(tx pgx.Tx) error {
		current, err := currentRows(ctx, tx)
		if err != nil {
			return err
		}

		plan := classifyProjection(wanted, current, true)
		names := plan.names
		res.Unchanged = plan.unchanged

		for _, w := range plan.insert {
			if err := insertRow(ctx, tx, w); err != nil {
				return err
			}
			res.Inserted++
		}
		for _, u := range plan.update {
			// Either the five projected columns differ, or the row has no tenant
			// and this sync is claiming it. Both are an UPDATE, and the second is
			// why ownership is part of the classification rather than a column
			// comparison.
			if err := updateRow(ctx, tx, u.row); err != nil {
				return err
			}
			res.Updated++
		}

		// Rows with no tenant go too, and that is intended rather than an
		// oversight: the projection is not a place to keep things, and a row
		// nothing produces is a RADIUS client nothing revokes. It is also the
		// declared cost of seeding one by hand, in the words `\d+ nas` prints.
		//
		// `RETURNING nasname` rather than `RowsAffected`: the caller has to
		// be able to *name* what went, because `raddb.Classify` cannot see a row
		// that appeared and was deleted between two restarts and answers `none`
		// for it — on a revocation. A count is exactly the shape that cannot be
		// passed on. It is ordered so two runs that remove the same rows print
		// and classify identically.
		//
		// Rebuilt each time rather than appended to: this closure runs again on a
		// serialization retry, and a slice left over from the attempt that rolled
		// back would name rows twice.
		res.Removed = nil
		rows, err := tx.Query(ctx, `
			DELETE FROM `+nasTable+`
			WHERE (tenant_id = foyer_current_tenant() OR tenant_id IS NULL)
			  AND nasname <> ALL($1::text[])
			RETURNING nasname`, names)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return mapErr(err)
			}
			res.Removed = append(res.Removed, name)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		sort.Strings(res.Removed)
		return nil
	})
	if err != nil {
		return NasSync{At: res.At, Skipped: skipped}, err
	}

	res.Rows = make([]NasRow, 0, len(wanted))
	for _, w := range wanted {
		res.Rows = append(res.Rows, w.row)
	}
	return res, nil
}

// NasPlanRow names one row a sync would write.
//
// **It never carries the secret**, and that is not squeamishness: this reaches a
// terminal, a `foyerctl doctor` transcript and a support bundle, and one of the five
// projected columns is the shared secret. `Differs` names the columns that do not
// match and nothing about their values.
type NasPlanRow struct {
	ClientID string
	NASName  string
	Differs  []string
}

// NasPlan is what a Sync would do, computed without writing anything.
//
// It exists because `foyerctl doctor` had to answer "is the projection current?"
// from outside this package, and `wantedRows` and `nasName` are unexported. What it
// did instead was parse both sides back into a `netip.Prefix` and compare those --
// a comparison that **cannot disagree about anything except the address**, so a row
// whose shortname or shared secret had drifted read as current. That is the symptom
// operators call in: one access point refused with "Shared secret is incorrect",
// with every other check green.
//
// Re-implementing the naming rule outside this package was the alternative, and
// `docs/rules/database.md` warns why: bare for a host, prefixed for a range, and the difference
// lands in a UNIQUE column.
type NasPlan struct {
	// Insert names clients with no row in `nas`. Each is a device whose every
	// packet FreeRADIUS discards as coming from an unknown NAS.
	Insert []NasPlanRow

	// Update names rows whose projected columns have drifted, or that no sync
	// produced and this tenant would claim.
	Update []NasPlanRow

	// Unchanged counts the rows already correct and already owned.
	Unchanged int

	// Remove names the `nas` rows a sync would delete: a projection nothing
	// produces any more is a RADIUS client nothing revokes.
	Remove []string

	// Skipped names the clients that cannot be projected at all, with the reason.
	Skipped []NasSkip

	// SecretsCompared is false when the process holds no keyring, in which case
	// `Update` reflects the four other columns and ownership only.
	//
	// **A caller must say so.** A check that could not run reads exactly like one
	// that passed, and shared-secret drift is the single most likely thing this
	// plan is asked about.
	SecretsCompared bool

	// At is when `radius_clients` was read.
	At time.Time
}

// Plan reports what Sync would do and writes nothing.
//
// Without a keyring it still answers: a client with no shared secret assigned is a
// column comparison rather than an unseal, and so are the inserts and the removals.
// What it cannot do is compare secrets, and it says which by leaving
// SecretsCompared false rather than by reporting agreement it did not check.
func (r *NasProjectionRepo) Plan(ctx context.Context) (NasPlan, error) {
	withSecrets := r.s.cfg.Keys != nil
	plan := NasPlan{At: time.Now().UTC(), SecretsCompared: withSecrets}

	wanted, skipped, err := r.wantedRows(ctx, withSecrets)
	if err != nil {
		return NasPlan{At: plan.At}, err
	}
	plan.Skipped = skipped

	current, err := currentRows(ctx, r.s.pool)
	if err != nil {
		return NasPlan{At: plan.At}, err
	}

	c := classifyProjection(wanted, current, withSecrets)
	plan.Unchanged = c.unchanged
	plan.Remove = c.remove
	for _, w := range c.insert {
		plan.Insert = append(plan.Insert, NasPlanRow{ClientID: w.clientID, NASName: w.row.NASName})
	}
	for _, u := range c.update {
		plan.Update = append(plan.Update, NasPlanRow{
			ClientID: u.row.clientID, NASName: u.row.row.NASName, Differs: u.differs,
		})
	}
	return plan, nil
}

// projectionPlan is the classification Sync and Plan share.
//
// Shared rather than reimplemented, because two answers to "what would this write"
// that can disagree are worse than one that is wrong: a dry run nobody trusts is a
// dry run nobody reads.
type projectionPlan struct {
	insert    []wantedRow
	update    []plannedUpdate
	unchanged int
	remove    []string
	names     []string
}

type plannedUpdate struct {
	row     wantedRow
	differs []string
}

func classifyProjection(wanted []wantedRow, current map[string]heldRow, compareSecrets bool) projectionPlan {
	p := projectionPlan{names: make([]string, 0, len(wanted))}
	kept := make(map[string]bool, len(wanted))

	for _, w := range wanted {
		p.names = append(p.names, w.row.NASName)
		kept[w.row.NASName] = true

		have, present := current[w.row.NASName]
		if !present {
			p.insert = append(p.insert, w)
			continue
		}
		differs := columnsThatDiffer(have, w.row, compareSecrets)
		if len(differs) == 0 {
			p.unchanged++
			continue
		}
		p.update = append(p.update, plannedUpdate{row: w, differs: differs})
	}

	for name := range current {
		if !kept[name] {
			p.remove = append(p.remove, name)
		}
	}
	sort.Strings(p.remove)
	return p
}

// columnsThatDiffer names what an UPDATE would change, ownership included.
//
// Ownership is in the list because a row with a NULL tenant belongs to nobody and
// claiming it is a write even when all five columns already match -- without that,
// such a row reports unchanged forever and stays permanently about to be adopted.
func columnsThatDiffer(have heldRow, want NasRow, compareSecrets bool) []string {
	var out []string
	if !have.owned {
		out = append(out, "tenant")
	}
	if have.row.Shortname != want.Shortname {
		out = append(out, "shortname")
	}
	if have.row.Type != want.Type {
		out = append(out, "type")
	}
	if compareSecrets && have.row.Secret != want.Secret {
		out = append(out, "shared secret")
	}
	if have.row.Server != want.Server {
		out = append(out, "server")
	}
	return out
}

// wantedRow is one projected row plus the client id it came from.
type wantedRow struct {
	clientID string
	row      NasRow
}

// wantedRows reads the system of record and renders it into projection rows.
//
// The unsealing happens here rather than inside the transaction so that a broken
// keyring fails before anything is written. A failed unseal is an error and not a
// skip: it means the KEK is wrong or a ciphertext is damaged, and quietly dropping
// those clients would take a site off the air one access point at a time.
// withSecrets false is the dry-run path on a process that holds no keyring: the
// rows come back with an empty Secret and nothing is unsealed. It is never the sync
// path, which checks for a keyring before it gets here -- a projection written
// without secrets is a client table that authenticates nothing.
func (r *NasProjectionRepo) wantedRows(ctx context.Context, withSecrets bool) ([]wantedRow, []NasSkip, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT rc.id, rc.name, rc.address, rc.shared_secret_id
		FROM radius_clients rc
		WHERE rc.enabled
		ORDER BY rc.address`)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()

	type candidate struct {
		id       string
		name     string
		address  netip.Prefix
		secretID *string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.name, &c.address, &c.secretID); err != nil {
			return nil, nil, mapErr(err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, mapErr(err)
	}

	var (
		wanted  []wantedRow
		skipped []NasSkip
		byName  = map[string]string{}
	)
	for _, c := range candidates {
		nasname := nasName(c.address)

		if c.secretID == nil {
			skipped = append(skipped, NasSkip{
				ClientID: c.id,
				Address:  nasname,
				Reason:   "no shared secret assigned; FreeRADIUS discards every packet from this address until one is",
			})
			continue
		}

		var secret []byte
		if withSecrets {
			var err error
			secret, err = r.s.SharedSecrets().Reveal(ctx, *c.secretID)
			if err != nil {
				return nil, nil, fmt.Errorf("store: projecting client %s (%s): unsealing shared secret %s: %w",
					c.id, nasname, *c.secretID, err)
			}
			if len(secret) == 0 {
				skipped = append(skipped, NasSkip{
					ClientID: c.id,
					Address:  nasname,
					Reason:   "shared secret " + *c.secretID + " holds no value",
				})
				continue
			}
		}

		// The second layer of the secret-shape check. `SharedSecrets().Create`
		// refuses a secret `nas.secret` cannot hold, so a row reaching here with one was written
		// before that constraint existed or by something outside this package.
		// Skipped rather than returned as an error: this is one corrupt row, and
		// failing the whole sync would take every other client off the air with it.
		//
		// What it replaces is the original failure -- PostgreSQL answering
		// `invalid byte sequence for encoding "UTF8": 0xff`, from inside a
		// transaction, naming neither the client nor the secret.
		if withSecrets {
			if err := checkSecretBytes(secret); err != nil {
				skipped = append(skipped, NasSkip{
					ClientID: c.id,
					Address:  nasname,
					Reason:   "shared secret " + *c.secretID + " cannot be projected: " + err.Error(),
				})
				continue
			}
		}

		// Two enabled clients on one address is two clients.conf rows FreeRADIUS
		// resolves by accident of ordering. The schema forbids it per tenant with
		// UNIQUE (tenant_id, address), so reaching this means an address written
		// two different ways -- 10.0.0.1 and 10.0.0.1/32 are one inet value, but a
		// future column or a hand-written row need not be.
		if other, dup := byName[nasname]; dup {
			return nil, nil, fmt.Errorf("store: clients %s and %s both project to nasname %s; FreeRADIUS would resolve them by ordering",
				other, c.id, nasname)
		}
		byName[nasname] = c.id

		shortname := c.name
		if shortname == "" {
			shortname = nasname
		}

		wanted = append(wanted, wantedRow{
			clientID: c.id,
			row: NasRow{
				NASName:   nasname,
				Shortname: shortname,
				Type:      nasType,
				Secret:    string(secret),
				Server:    "",
			},
		})
	}

	sort.Slice(wanted, func(i, j int) bool { return wanted[i].row.NASName < wanted[j].row.NASName })
	return wanted, skipped, nil
}

// nasName renders a client address the way FreeRADIUS names a client.
//
// A single host is written bare and a range keeps its prefix length. The
// distinction is not cosmetic: `radius_clients.address` is an `inet` because
// Aruba's dynamic-radius-proxy shape needs a range covering a fleet, and a range
// can only ever enter FreeRADIUS's client list through the startup load --
// discovery matches one `Packet-Src-IP-Address` at a time and never matches a
// range. `raddb.Classify` keys that decision on the shape of this string.
//
// It is computed in Go rather than with PostgreSQL's host()/text() because those
// two disagree about when to print the prefix length and the difference lands in a
// UNIQUE column.
func nasName(p netip.Prefix) string {
	if p.Bits() == p.Addr().BitLen() {
		return p.Addr().String()
	}
	return p.String()
}

// heldRow is one row this sync is allowed to write, and whether it already
// belongs to this tenant.
//
// The second field is not bookkeeping. A row with a NULL tenant was seeded by
// something outside `internal/store` and belongs to nobody; this sync may claim it,
// but claiming it is a write even when all five projected columns already match.
// Without the flag such a row reports Unchanged forever and keeps its NULL tenant,
// so it stays permanently about to be adopted.
type heldRow struct {
	row   NasRow
	owned bool
}

// rowQuerier is what currentRows needs and no more: the transaction Sync runs in,
// or the pool Plan reads through without opening one.
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// currentRows reads the rows this sync may write: its own tenant's, plus any that
// no sync produced.
//
// A row held by *another* tenant is deliberately absent, which is what turns it
// into an insert, a unique violation on nasname, and ErrClientAddressTaken naming
// both sides. See insertRow.
func currentRows(ctx context.Context, q rowQuerier) (map[string]heldRow, error) {
	rows, err := q.Query(ctx, `
		SELECT nasname, shortname, type, secret, coalesce(server, ''),
		       tenant_id IS NOT NULL AS owned
		FROM `+nasTable+`
		WHERE tenant_id = foyer_current_tenant() OR tenant_id IS NULL`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	out := map[string]heldRow{}
	for rows.Next() {
		var h heldRow
		n := &h.row
		if err := rows.Scan(&n.NASName, &n.Shortname, &n.Type, &n.Secret, &n.Server, &h.owned); err != nil {
			return nil, mapErr(err)
		}
		out[n.NASName] = h
	}
	return out, mapErr(rows.Err())
}

// insertRow adds one projection row, and refuses to take over another tenant's.
//
// The ON CONFLICT clause exists for exactly one case: the nasname is present but
// belongs to a different tenant, so it was not in `currentRows` and this looked
// like an insert. DO NOTHING plus a zero row count turns that into
// ErrClientAddressTaken naming the address, rather than a bare unique-violation
// that reads like a bug in this function.
func insertRow(ctx context.Context, tx pgx.Tx, w wantedRow) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO `+nasTable+` (nasname, shortname, type, secret, server, tenant_id, radius_client_id)
		VALUES ($1, $2, $3, $4, nullif($5, ''), foyer_current_tenant(), $6)
		ON CONFLICT (nasname) DO NOTHING`,
		w.row.NASName, w.row.Shortname, w.row.Type, w.row.Secret, w.row.Server, w.clientID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return conflictFor(ctx, tx, w)
	}
	return nil
}

// conflictFor names both sides of a refused projection.
//
// `nas` carries no row-level policy -- deliberately, because FreeRADIUS never sets
// the tenant GUC -- so this read can see the other tenant's row, which is the only
// reason the error can say whose it is. That is the one place the missing policy
// helps rather than costs.
func conflictFor(ctx context.Context, tx pgx.Tx, w wantedRow) error {
	c := &NasAddressConflict{NASName: w.row.NASName, ClientID: w.clientID}
	err := tx.QueryRow(ctx, `
		SELECT coalesce(foyer_current_tenant()::text, ''),
		       coalesce(tenant_id::text, ''),
		       coalesce(radius_client_id, '')
		FROM `+nasTable+`
		WHERE nasname = $1`, w.row.NASName).Scan(&c.Tenant, &c.OtherTenant, &c.OtherClientID)
	if err != nil {
		// The row that caused the conflict is no longer readable. Say what is
		// known rather than replacing a real refusal with a lookup failure.
		return fmt.Errorf("%w: %s (client %s); the holding row could not be read: %v",
			ErrClientAddressTaken, w.row.NASName, w.clientID, err)
	}
	return c
}

// updateRow rewrites one projection row, and claims it if no tenant held it.
//
// The row count is checked, and that is not defensiveness about a statement that
// obviously matches. `currentRows` read this row earlier in the same transaction,
// so a zero here means a concurrent writer took it between the read and this
// write -- another tenant's sync, or an operator editing `nas` by hand. Without
// the check the result reports Updated for a row it did not write, which is the
// exact shape this file's own documentation warns about: a synchronisation that
// runs, returns nil and copies nothing.
func updateRow(ctx context.Context, tx pgx.Tx, w wantedRow) error {
	tag, err := tx.Exec(ctx, `
		UPDATE `+nasTable+` SET
			shortname        = $2,
			type             = $3,
			secret           = $4,
			server           = nullif($5, ''),
			radius_client_id = $6,
			tenant_id        = foyer_current_tenant()
		WHERE nasname = $1
		  AND (tenant_id = foyer_current_tenant() OR tenant_id IS NULL)`,
		w.row.NASName, w.row.Shortname, w.row.Type, w.row.Secret, w.row.Server, w.clientID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return conflictFor(ctx, tx, w)
	}
	return nil
}
