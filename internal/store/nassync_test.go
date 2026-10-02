package store_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/store"
)

// The sync is a whole-tenant operation: it removes every row in `nas` that this
// tenant's system of record no longer produces. That is the point of it, and it
// makes the shared test database a problem — the other tests in this package leave
// `radius_clients` rows behind whose shared secrets were sealed under a keyring
// that no longer exists, and a sync that could see them would fail to unseal one
// and abort.
//
// So each sync test gets a tenant of its own. That is not a workaround: it is the
// tenant column doing exactly the job migration 00008 added it for, and if these
// tests interfered with the rest of the package it would mean the scoping did not
// work.
type syncFixture struct {
	st     *store.Store
	tenant string
	site   *store.Site
	secret *store.SharedSecret
	owner  *pgxpool.Pool
	// keys is the store's own keyring, kept so a test can write a sealed value
	// the repository would now refuse -- the shape of a row that predates a
	// constraint. See reseal.
	keys *store.Keyring
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	requireDocker(t)

	owner := ownerPool(t)
	ctx := context.Background()

	var tenantID string
	slug := uniq("tenant")
	if err := owner.QueryRow(ctx,
		`INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, slug).
		Scan(&tenantID); err != nil {
		t.Fatalf("creating a tenant for this test: %v", err)
	}

	keys := testKeyring(t)
	st, err := store.Open(ctx, store.Config{
		URL:      ownerURL(t),
		TenantID: tenantID,
		Keys:     keys,
		MaxConns: 4,
	})
	if err != nil {
		t.Fatalf("opening store for tenant %s: %v", tenantID, err)
	}
	t.Cleanup(st.Close)

	f := &syncFixture{st: st, tenant: tenantID, owner: owner, keys: keys}
	f.site = &store.Site{ID: uniq("site"), Name: "Sync Venue", Environment: "lab"}
	fatalf(t, st.Sites().Create(ctx, f.site), "creating the site")
	f.secret = &store.SharedSecret{ID: uniq("sec"), SiteID: f.site.ID, Label: "UniFi RADIUS profile"}
	fatalf(t, st.SharedSecrets().Create(ctx, f.secret, []byte("shared-secret-"+randomString())), "creating the shared secret")
	return f
}

func (f *syncFixture) client(t *testing.T, cidr string, opts ...func(*store.RadiusClient)) *store.RadiusClient {
	t.Helper()
	c := &store.RadiusClient{
		ID: uniq("rc"), SiteID: f.site.ID, Name: "AP " + cidr,
		Address: netip.MustParsePrefix(cidr),
		Source:  "discovered", Driver: "unifi", SharedSecretID: &f.secret.ID,
		RequireMsgAuth: true, LimitProxyState: true, Enabled: true,
	}
	for _, o := range opts {
		o(c)
	}
	fatalf(t, f.st.RadiusClients().Create(context.Background(), c), "creating client %s", cidr)
	return c
}

// reseal replaces a stored secret's sealed value without going through the
// repository, which is the only way to produce a row the repository would now
// refuse to write. It stands in for a row written before the constraint existed.
func (f *syncFixture) reseal(t *testing.T, id string, secret []byte) {
	t.Helper()
	sealed, err := f.keys.Seal(secret, store.SecretContext("shared_secrets", "secret", id))
	fatalf(t, err, "sealing a secret the repository would refuse")
	_, err = f.owner.Exec(context.Background(),
		`UPDATE shared_secrets SET secret = $2 WHERE id = $1`, id, sealed)
	fatalf(t, err, "rewriting the sealed secret as the owner")
}

func (f *syncFixture) sync(t *testing.T) store.NasSync {
	t.Helper()
	res, err := f.st.NasProjection().Sync(context.Background())
	fatalf(t, err, "syncing the nas projection")
	return res
}

func rowsByName(rows []store.NasRow) map[string]store.NasRow {
	m := map[string]store.NasRow{}
	for _, r := range rows {
		m[r.NASName] = r
	}
	return m
}

// The NAS projection: `radius_clients` is the system of record and `nas` is
// what FreeRADIUS reads. Before this, nothing copied one into the other and a
// freshly migrated database discarded every packet as coming from an unknown
// client.
func TestSyncProjectsTheSystemOfRecordIntoTheClientTable(t *testing.T) {
	f := newSyncFixture(t)
	lobby := f.client(t, "10.60.0.1/32")

	res := f.sync(t)
	if res.Inserted != 1 {
		t.Fatalf("Inserted = %d, want 1 (skipped: %v)", res.Inserted, res.Skipped)
	}

	got := rowsByName(res.Rows)["10.60.0.1"]
	if got.NASName != "10.60.0.1" {
		t.Fatalf("the client was not projected: %+v", res.Rows)
	}
	if got.Shortname != lobby.Name {
		t.Errorf("shortname is %q, want the client's own name %q", got.Shortname, lobby.Name)
	}
	// FreeRADIUS's nas_type is checkrad's vocabulary — cisco, livingston, other —
	// not the vendor family. `driver` is a different axis and writing "unifi" here
	// would be a value the server does not recognise dressed up as configuration.
	if got.Type != "other" {
		t.Errorf("type is %q; the driver name is not a FreeRADIUS nas_type", got.Type)
	}

	// The secret has to arrive in the clear, because FreeRADIUS validates
	// Message-Authenticator against it and has no way to ask Foyer.
	sealed, err := f.st.SharedSecrets().Reveal(context.Background(), f.secret.ID)
	fatalf(t, err, "revealing the shared secret")
	if got.Secret != string(sealed) {
		t.Error("the projected secret is not the shared secret; FreeRADIUS would reject every packet from this client")
	}
}

// A single host is written bare and a range keeps its prefix, because `Classify`
// keys on that shape: a range can only ever enter FreeRADIUS's client list through
// the startup load, since discovery matches one Packet-Src-IP-Address at a time.
func TestSyncKeepsARangeARange(t *testing.T) {
	f := newSyncFixture(t)
	f.client(t, "10.61.0.1/32")
	f.client(t, "10.62.0.0/24")

	rows := rowsByName(f.sync(t).Rows)
	if _, ok := rows["10.61.0.1"]; !ok {
		t.Errorf("a /32 was not written as a bare address: %v", rows)
	}
	if _, ok := rows["10.62.0.0/24"]; !ok {
		t.Errorf("a range lost its prefix length: %v", rows)
	}
}

// Running it twice writes nothing the second time. Not an optimisation: a sync
// that rewrites every row on every run makes "what changed" unanswerable, and
// `Classify` is built entirely on answering it.
func TestSyncIsIdempotent(t *testing.T) {
	f := newSyncFixture(t)
	f.client(t, "10.63.0.1/32")
	f.client(t, "10.63.0.2/32")

	first := f.sync(t)
	if first.Inserted != 2 {
		t.Fatalf("Inserted = %d, want 2", first.Inserted)
	}

	second := f.sync(t)
	if second.Inserted != 0 || second.Updated != 0 || len(second.Removed) != 0 {
		t.Fatalf("a second sync wrote something: %+v", second)
	}
	if second.Unchanged != 2 {
		t.Fatalf("Unchanged = %d, want 2", second.Unchanged)
	}
}

// Rotating the shared secret changes every client that presents it, because the
// secret is a property of a controller-level RADIUS profile rather than of one
// device. That is the arithmetic `shared_secrets` exists for, and the projection
// has to carry it through.
func TestSyncCarriesASecretRotationToEveryClientOnThatSecret(t *testing.T) {
	f := newSyncFixture(t)
	f.client(t, "10.64.0.1/32")
	f.client(t, "10.64.0.2/32")
	f.sync(t)

	rotated := []byte("rotated-secret-" + randomString())
	fatalf(t, f.st.SharedSecrets().Rotate(context.Background(), f.secret.ID, rotated), "rotating")

	res := f.sync(t)
	if res.Updated != 2 {
		t.Fatalf("Updated = %d, want 2 — one rotation, two clients on the profile: %+v", res.Updated, res)
	}
	for _, r := range res.Rows {
		if r.Secret != string(rotated) {
			t.Errorf("client %s still carries the old secret", r.NASName)
		}
	}
}

// The half that is easy to leave out and impossible to notice.
//
// A sync that only inserts and updates passes every test that checks a client
// works. It also leaves a decommissioned access point holding a valid RADIUS
// client, which is a security defect rather than an untidiness. Deleting the
// client row is the operator's action; removing the projection row is this
// function's, and nothing else does it — `nas.radius_client_id` is deliberately
// not a foreign key, so no cascade can make this pass while the DELETE is broken.
func TestSyncRemovesAClientThatWasDecommissioned(t *testing.T) {
	f := newSyncFixture(t)
	keep := f.client(t, "10.65.0.1/32")
	gone := f.client(t, "10.65.0.2/32")
	_ = keep
	f.sync(t)

	fatalf(t, f.st.RadiusClients().Delete(context.Background(), gone.ID), "deleting the decommissioned client")

	res := f.sync(t)
	// Named, because a count cannot be handed to `raddb.TableBefore` and the
	// classifier answers `none` for a revocation it cannot see.
	if len(res.Removed) != 1 || res.Removed[0] != "10.65.0.2" {
		t.Fatalf("Removed = %v, want [10.65.0.2]: %+v", res.Removed, res)
	}
	if _, still := rowsByName(res.Rows)["10.65.0.2"]; still {
		t.Fatal("the decommissioned client is still in the table FreeRADIUS reads")
	}

	// And the row is actually gone from the table, not merely absent from the
	// result the function reports about itself.
	var n int
	fatalf(t, f.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM nas WHERE nasname = '10.65.0.2'`).Scan(&n), "counting")
	if n != 0 {
		t.Fatalf("%d rows for a deleted client survive in nas", n)
	}
}

// Disabling a client is the same removal by a gentler name. An operator who
// switches a NAS off in the console and finds it still authenticating has been
// told something false by the UI.
func TestSyncRemovesADisabledClient(t *testing.T) {
	f := newSyncFixture(t)
	c := f.client(t, "10.66.0.1/32")
	f.sync(t)

	off := false
	fatalf(t, f.st.RadiusClients().Update(context.Background(), c.ID, store.RadiusClientPatch{Enabled: &off}), "disabling")

	res := f.sync(t)
	if len(res.Removed) != 1 || res.Removed[0] != "10.66.0.1" {
		t.Fatalf("Removed = %v, want [10.66.0.1] after disabling: %+v", res.Removed, res)
	}
}

// The projection is not a place to keep things. A row nothing here wrote —
// `lab/blue-green/seed-client.sh` puts one in by hand — is removed, because a
// client table that accumulates is a client table nobody can reason about.
//
// This is also the case that cannot pass by accident. There is no foreign key
// behind it and no cascade: if Sync's DELETE were removed, this row would survive
// and this test would be the one that went red.
//
// Both shapes, because they are two rows. A hand-seeded row carrying this
// tenant's id is not what a hand seed looks like: `seed-client.sh` names four
// columns and `tenant_id` is not among them, so the row it writes has no tenant
// at all, and a DELETE scoped to `tenant_id = foyer_current_tenant()` steps
// straight past it. The second row below is the one the comment above describes.
func TestSyncRemovesARowNothingProduced(t *testing.T) {
	f := newSyncFixture(t)
	f.client(t, "10.67.0.1/32")
	f.sync(t)

	_, err := f.owner.Exec(context.Background(), `
		INSERT INTO nas (nasname, shortname, type, secret, tenant_id)
		VALUES ('10.67.9.9', 'hand-seeded', 'other', 'a-secret-nobody-here-wrote', $1::uuid)`, f.tenant)
	fatalf(t, err, "seeding a row by hand")

	// Verbatim the column list in lab/blue-green/seed-client.sh, over a connection
	// with no tenant GUC — which is what `psql` is. Both halves matter: the column
	// list leaves tenant_id to its DEFAULT, and the missing GUC makes that DEFAULT
	// evaluate to NULL. Through this package's own owner pool it would not, because
	// that pool sets the GUC, and the row would land in the default tenant instead
	// of nowhere. The check below is what caught that while this test was written.
	seedLikeTheScript(t, "10.67.9.10", "ap-10-67-9-10", "the-seed-script-shape")

	var untenanted bool
	fatalf(t, f.owner.QueryRow(context.Background(),
		`SELECT tenant_id IS NULL FROM nas WHERE nasname = '10.67.9.10'`).Scan(&untenanted),
		"reading the seeded row back")
	if !untenanted {
		t.Fatal("the seed-script shape carries a tenant after all, so this test is no longer the case it claims to be")
	}

	res := f.sync(t)
	if !sameStrings(res.Removed, []string{"10.67.9.10", "10.67.9.9"}) {
		t.Fatalf("Removed = %v, want both seeded rows; a hand-seeded row survived the sync: %+v", res.Removed, res)
	}

	var left int
	fatalf(t, f.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM nas WHERE nasname IN ('10.67.9.9', '10.67.9.10')`).Scan(&left), "counting")
	if left != 0 {
		t.Fatalf("%d hand-seeded rows survive in the table FreeRADIUS reads", left)
	}
}

// A row nobody owns, at an address this tenant's system of record does produce, is
// adopted rather than refused — and the adoption is a write even when the five
// projected columns already match.
//
// This is the path `lab/blue-green/seed-client.sh` leaves behind when an operator
// later registers the same access point properly. Reporting it Unchanged would
// leave the row untenanted forever: the next sync would delete it, because a row
// with no tenant is a row no sync produced, and the access point would drop off
// the network at a moment unrelated to anything the operator did.
func TestSyncClaimsAnUntenantedRowAtAnAddressItOwns(t *testing.T) {
	f := newSyncFixture(t)
	c := f.client(t, "10.72.0.1/32")
	f.sync(t)

	// Strip the ownership, leaving the row byte-identical otherwise. That is the
	// hard version: a sync that only compares the five columns reports Unchanged.
	_, err := f.owner.Exec(context.Background(),
		`UPDATE nas SET tenant_id = NULL, radius_client_id = NULL WHERE nasname = '10.72.0.1'`)
	fatalf(t, err, "un-owning the row")

	res := f.sync(t)
	if res.Updated != 1 || res.Unchanged != 0 {
		t.Fatalf("Updated = %d, Unchanged = %d; want the untenanted row claimed, not passed over: %+v",
			res.Updated, res.Unchanged, res)
	}

	var tenant, clientID string
	fatalf(t, f.owner.QueryRow(context.Background(),
		`SELECT coalesce(tenant_id::text, ''), coalesce(radius_client_id, '') FROM nas WHERE nasname = '10.72.0.1'`).
		Scan(&tenant, &clientID), "reading the claimed row")
	if tenant != f.tenant || clientID != c.ID {
		t.Fatalf("the row reads back as tenant %q client %q, want %q and %q", tenant, clientID, f.tenant, c.ID)
	}

	// And the second run is quiet. A claim that reports Updated every time is a
	// row permanently about to be adopted, which is the same defect wearing the
	// opposite sign.
	again := f.sync(t)
	if again.Unchanged != 1 || again.Updated != 0 {
		t.Fatalf("the second sync reports Updated = %d, Unchanged = %d; the claim did not stick: %+v",
			again.Updated, again.Unchanged, again)
	}
}

// A client with no shared secret cannot be projected: `nas.secret` is NOT NULL and
// there is nothing to put in it. Skipping it silently would be a NAS the console
// lists and FreeRADIUS discards every packet from, with nothing anywhere saying
// why — so it is reported by address and by reason.
func TestSyncReportsAClientWithNoSecretRatherThanDroppingIt(t *testing.T) {
	f := newSyncFixture(t)
	f.client(t, "10.68.0.1/32")
	orphan := f.client(t, "10.68.0.2/32", func(c *store.RadiusClient) { c.SharedSecretID = nil })

	res := f.sync(t)
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want one entry", res.Skipped)
	}
	if res.Skipped[0].ClientID != orphan.ID || res.Skipped[0].Address != "10.68.0.2" {
		t.Errorf("the skip does not name the client or its address: %+v", res.Skipped[0])
	}
	if !strings.Contains(res.Skipped[0].Reason, "shared secret") {
		t.Errorf("the reason does not say what is missing: %q", res.Skipped[0].Reason)
	}
	if _, written := rowsByName(res.Rows)["10.68.0.2"]; written {
		t.Error("a client with no secret was projected anyway")
	}
	if res.Inserted != 1 {
		t.Errorf("Inserted = %d; the other client should still have been written", res.Inserted)
	}
}

// No keyring is a refusal, never a projection with the secrets left out. A client
// table written without secrets authenticates nothing and looks fully populated.
func TestSyncWithNoKeyringRefuses(t *testing.T) {
	requireDocker(t)
	st, err := store.Open(context.Background(), store.Config{URL: ownerURL(t), MaxConns: 2})
	fatalf(t, err, "opening a store with no keyring")
	t.Cleanup(st.Close)

	if _, err := st.NasProjection().Sync(context.Background()); !errors.Is(err, store.ErrNoKeyring) {
		t.Fatalf("err = %v, want ErrNoKeyring", err)
	}
}

// `radius_clients` is UNIQUE (tenant_id, address); `nas.nasname` is
// globally unique, because FreeRADIUS matches a packet's source address against it
// and its client list has no tenant in it. On private ranges two venues sharing
// 192.168.1.1 is the common case, not an edge one.
//
// Last-writer-wins here would point one tenant's access points at another tenant's
// shared secret: a cross-tenant authentication bypass with an audit trail that
// looks entirely normal. So the sync refuses, and the refusal names both tenants
// and the address, because an error that says only "already taken" sends an
// operator to the wrong database.
func TestSyncRefusesToTakeOverAnotherTenantsAddress(t *testing.T) {
	a := newSyncFixture(t)
	b := newSyncFixture(t)

	a.client(t, "10.69.0.1/32")
	a.sync(t)

	conflicting := b.client(t, "10.69.0.1/32")
	_, err := b.st.NasProjection().Sync(context.Background())
	if !errors.Is(err, store.ErrClientAddressTaken) {
		t.Fatalf("err = %v, want ErrClientAddressTaken", err)
	}

	var conflict *store.NasAddressConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("the error carries no fields, so nothing can render it: %v", err)
	}
	if conflict.NASName != "10.69.0.1" {
		t.Errorf("NASName = %q", conflict.NASName)
	}
	if conflict.Tenant != b.tenant || conflict.OtherTenant != a.tenant {
		t.Errorf("the conflict names tenants %q and %q, want %q and %q",
			conflict.Tenant, conflict.OtherTenant, b.tenant, a.tenant)
	}
	if conflict.ClientID != conflicting.ID {
		t.Errorf("ClientID = %q, want %q", conflict.ClientID, conflicting.ID)
	}

	// And nothing was written: a refused sync leaves the projection as it was
	// rather than half-applied.
	var owner string
	fatalf(t, a.owner.QueryRow(context.Background(),
		`SELECT tenant_id::text FROM nas WHERE nasname = '10.69.0.1'`).Scan(&owner), "reading the held row")
	if owner != a.tenant {
		t.Fatalf("the row changed hands: tenant_id is %q, want %q", owner, a.tenant)
	}
}

// The refusal takes the whole sync with it, including the clients that had no
// conflict at all.
//
// Separate from the test above because it is a different claim and the two fail
// for different reasons. That one says the held row is not taken over; this one
// says the *rest* of the estate is not half-written. Sync's own documentation
// makes the promise -- "the whole of it is one transaction; a partial projection
// is a subset of the estate that works and a subset that does not, with nothing to
// say which" -- and a promise about a transaction is exactly the kind that a
// function can make and not keep, because the successful half looks correct.
//
// The clean client sorts before the conflicting one on purpose: wantedRows orders
// by nasname, so 10.74.0.1 is INSERTed and then 10.74.0.9 refuses. Reverse the
// order and the transaction never gets as far as writing anything, and this test
// passes without exercising a rollback.
func TestARefusedSyncWritesNothingAtAll(t *testing.T) {
	a := newSyncFixture(t)
	b := newSyncFixture(t)

	a.client(t, "10.74.0.9/32")
	a.sync(t)

	b.client(t, "10.74.0.1/32") // no conflict; written first
	b.client(t, "10.74.0.9/32") // the collision
	if _, err := b.st.NasProjection().Sync(context.Background()); !errors.Is(err, store.ErrClientAddressTaken) {
		t.Fatalf("err = %v, want ErrClientAddressTaken", err)
	}

	var n int
	fatalf(t, b.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM nas WHERE nasname = '10.74.0.1'`).Scan(&n), "counting")
	if n != 0 {
		t.Fatalf("the un-conflicted client was projected anyway: %d rows for 10.74.0.1. "+
			"A half-applied sync is the state nothing in the product can describe: some of this "+
			"tenant's access points authenticate and some do not, and the sync reported an error "+
			"about a third one.", n)
	}
}

// One tenant's sync must not touch another's rows. Without this the DELETE would
// be a whole-table operation and the first sync after adding a second tenant would
// take that tenant's entire estate off the air.
func TestSyncRemovesOnlyItsOwnTenantsRows(t *testing.T) {
	a := newSyncFixture(t)
	b := newSyncFixture(t)

	a.client(t, "10.70.0.1/32")
	a.sync(t)

	b.client(t, "10.70.0.2/32")
	res := b.sync(t)
	if len(res.Removed) != 0 {
		t.Fatalf("Removed = %v; tenant B's sync deleted tenant A's clients", res.Removed)
	}

	var n int
	fatalf(t, a.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM nas WHERE nasname = '10.70.0.1'`).Scan(&n), "counting")
	if n != 1 {
		t.Fatal("tenant A's client did not survive tenant B's sync")
	}
}

// Rows() is the tenant's own view of the projection. It has to agree with what
// Sync just wrote, or the console lists a client table that was never written.
func TestRowsAgreesWithWhatSyncWrote(t *testing.T) {
	f := newSyncFixture(t)
	f.client(t, "10.71.0.1/32")
	f.client(t, "10.71.0.0/24")
	written := f.sync(t)

	read, err := f.st.NasProjection().Rows(context.Background())
	fatalf(t, err, "reading the projection back")

	if len(read) != len(written.Rows) {
		t.Fatalf("Rows() returned %d rows, Sync wrote %d", len(read), len(written.Rows))
	}
	got, want := rowsByName(read), rowsByName(written.Rows)
	for name, w := range want {
		if got[name] != w {
			t.Errorf("client %s reads back as %+v, was written as %+v", name, got[name], w)
		}
	}
}

// The startup snapshot is a whole-table read, and Rows() is not one.
//
// FreeRADIUS reads `nas` with `read_clients = yes` over a connection that never
// sets the tenant GUC, so it loads every row there is: another tenant's, and the
// hand-seeded ones with no tenant at all. A startup snapshot built from Rows()
// would omit precisely the rows the server did load — which classify as new on the next
// comparison, and a new client inside a dynamic network classifies `sql-only`. The
// operator is then told a change needs no restart while the running server holds
// the old definition.
//
// Asserted as a difference between the two readers rather than as a count, because
// a count is satisfied by both of them being wrong in the same direction.
func TestAllRowsSeesWhatFreeRADIUSLoadsAndRowsDoesNot(t *testing.T) {
	a := newSyncFixture(t)
	b := newSyncFixture(t)

	a.client(t, "10.73.0.1/32")
	a.sync(t)
	b.client(t, "10.73.0.2/32")
	b.sync(t)

	// The third row is the shape lab/blue-green/seed-client.sh writes: no tenant,
	// no radius_clients row, and loaded by the server exactly like the other two.
	seedLikeTheScript(t, "10.73.0.3", "ap-10-73-0-3", "hand-seeded")

	scoped, err := a.st.NasProjection().Rows(context.Background())
	fatalf(t, err, "reading this tenant's rows")
	all, err := a.st.NasProjection().AllRows(context.Background())
	fatalf(t, err, "reading every row FreeRADIUS loads")

	inScoped, inAll := rowsByName(scoped), rowsByName(all)
	for _, name := range []string{"10.73.0.1", "10.73.0.2", "10.73.0.3"} {
		if _, ok := inAll[name]; !ok {
			t.Errorf("AllRows does not carry %s, which FreeRADIUS loads; the snapshot describes a server that does not exist", name)
		}
	}
	if _, ok := inScoped["10.73.0.2"]; ok {
		t.Error("Rows() leaked another tenant's client into this tenant's view")
	}
	if _, ok := inScoped["10.73.0.3"]; ok {
		t.Error("Rows() carried a row with no tenant; it is scoped to foyer_current_tenant()")
	}
	if len(inAll) <= len(inScoped) {
		t.Fatalf("AllRows returned %d rows and Rows() returned %d; the two are not distinguishable here, so this test proves nothing",
			len(inAll), len(inScoped))
	}
}

// seedLikeTheScript writes one `nas` row the way lab/blue-green/seed-client.sh
// does: the four columns that script names, over a connection that has never set
// the `foyer.tenant_id` GUC.
//
// A separate connection rather than the package's owner pool, and it is not
// fussiness. That pool sets the GUC, so `tenant_id`'s DEFAULT of
// foyer_current_tenant() resolves to the default tenant and the row arrives
// *owned* — which is the opposite of the state these tests are about. psql has no
// GUC; this is psql.
func seedLikeTheScript(t *testing.T, nasname, shortname, secret string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerURL(t))
	fatalf(t, err, "connecting without a tenant scope")
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `
		INSERT INTO nas (nasname, shortname, type, secret)
		VALUES ($1, $2, 'other', $3)`, nasname, shortname, secret)
	fatalf(t, err, "seeding %s the way lab/blue-green/seed-client.sh does", nasname)
}
