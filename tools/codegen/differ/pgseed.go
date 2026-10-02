package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// Writing the oracle's world into PostgreSQL.
//
// ── the direction this runs in, and why ──────────────────────────────────────
//
// `web/mock` answers with the contract's **read models**. The schema underneath the
// adapter holds three nouns — controller, RADIUS client, access point — so a `Nas` the oracle
// returns is one `radius_clients` row and — where the device has a MAC — one
// `access_points` row, and a `Session` is a `radacct` row that resolves back to both
// by address and by `Called-Station-Id`. This file performs that inversion.
//
// It is written **against `internal/store`'s repositories and raw SQL, never against
// `internal/apistore`**. That is the whole point: if the seeder went through the
// adapter, a binding that reads the wrong column would write the wrong column too
// and the comparison would agree with itself. The two halves have to be independent
// or the harness is a mirror.
//
// ── this file is the one write-time-guard exemption in the repository ────────
//
// The guard blocks `INSERT INTO radacct` in any `.go` file, and it is right to:
// FreeRADIUS owns the accounting tables. **But the guard is a proxy — the
// PostgreSQL role is the guarantee.** `foyer_app` holds `SELECT` on `radacct` and
// `radpostauth` and nothing else, the reaper's `UPDATE` grant is four columns wide,
// and PostgreSQL refuses everything else regardless of what any Go file says.
//
// This file seeds as the **schema owner**, into a throwaway container it created
// itself, so the application still cannot write accounting and the invariant is
// untouched. `assertOwner` below establishes that in code rather than in a comment,
// because an exemption resting on a claim would be a mechanism that looks like a
// check and is not.
//
// ── what the inversion cannot carry ──────────────────────────────────────────
//
// Some of the oracle's fields have nowhere to go. `radpostauth` holds the stock
// FreeRADIUS column set verbatim — no NAS address, no tenant, no event source — so
// the portal and driver event streams the contract's `AuthAttempt.source` defines
// cannot be seeded at all. A device with no MAC has no `access_points` row, so
// nothing carries an AP name for it.
//
// **Those are findings, not harness bugs, and they are declared as such in
// scenario.go with the reason beside them.** The rule the memory mode already
// carries applies unchanged: the contract wins, a divergence is declared, and a
// declared divergence that stops happening fails the run.
//
// Two things this file does invent, and both are stated where they are used:
// `radius_clients.address`, because the contract's `Nas` has no address field; and
// `Called-Station-Id`, because it is a RADIUS attribute rather than a Foyer record
// and no read model exposes it.

// seeder holds what one seeding run needs to keep consistent between tables.
type seeder struct {
	ctx context.Context
	h   *pgHarness
	w   *memory.World

	// clientAddr is the address the `clients.conf` row for a device was given,
	// which is what every one of that device's `radacct` rows must arrive from.
	clientAddr map[string]netip.Addr
	// clientID is the `radius_clients` id behind a device id. They differ exactly
	// when the device has an access point.
	clientID map[string]string
	// calledStation is the AP MAC this device's sessions carry at the head of
	// their `Called-Station-Id`.
	calledStation map[string]string
	// synthetic counts the addresses this seeder had to invent because the read
	// model carries none. Printed, so the number is never a surprise.
	synthetic int
	// notSeeded records what the schema could not hold, by category. Printed
	// beside the coverage figures, because a gap nobody prints reads as no gap.
	notSeeded map[string]int

	nextHost int
}

func newSeeder(ctx context.Context, h *pgHarness, w *memory.World) *seeder {
	return &seeder{
		ctx:           ctx,
		h:             h,
		w:             w,
		clientAddr:    map[string]netip.Addr{},
		clientID:      map[string]string{},
		calledStation: map[string]string{},
		notSeeded:     map[string]int{},
	}
}

// seed writes the whole world, in dependency order.
func (s *seeder) seed() error {
	if err := s.assertOwner(); err != nil {
		return err
	}
	steps := []struct {
		what string
		fn   func() error
	}{
		{"sites", s.sites},
		{"operators", s.operators},
		{"controllers", s.controllers},
		{"devices", s.devices},
		{"voucher batches", s.vouchers},
		{"accounts", s.accounts},
		{"portal settings", s.portal},
		{"walled garden", s.garden},
		{"audit", s.audit},
		{"accounting", s.accounting},
		{"auth attempts", s.attempts},
	}
	for _, st := range steps {
		if err := st.fn(); err != nil {
			return fmt.Errorf("seeding %s: %w", st.what, err)
		}
	}
	return nil
}

// assertOwner refuses to seed through a de-privileged connection.
//
// This is the check the guard exemption rests on. Without it the exemption would be
// a promise that this connection is the owner, and a promise is not a check. If
// the pool ever became the application's, the writes below would be refused by
// PostgreSQL anyway — but they would be refused halfway through
// a seed, and the message would be about a permission rather than about the harness
// having quietly changed what it is.
func (s *seeder) assertOwner() error {
	var user string
	if err := s.h.owner.QueryRow(s.ctx, `SELECT current_user`).Scan(&user); err != nil {
		return fmt.Errorf("reading current_user: %w", err)
	}
	switch user {
	case store.DefaultAppRole, "foyer_reaper", "foyer_radius":
		return fmt.Errorf(
			"the seeding connection is %q, not the schema owner.\n"+
				"  This harness writes accounting only because it owns a throwaway container;\n"+
				"  the application role holds SELECT and nothing else, and that is the\n"+
				"  guarantee the write-time guard stands in for. Refusing here rather than\n"+
				"  discovering it as a permission error halfway through a seed", user)
	}
	return nil
}

func (s *seeder) exec(sql string, args ...any) error {
	_, err := s.h.owner.Exec(s.ctx, sql, args...)
	return err
}

// ── sites, operators ─────────────────────────────────────────────────────────

func (s *seeder) sites() error {
	for _, site := range s.w.Sites {
		if err := s.h.store.Sites().Create(s.ctx, &store.Site{
			ID: site.Id, Name: site.Name, Environment: string(site.Environment),
		}); err != nil {
			return err
		}
	}
	return nil
}

// operators are seeded with one argon2id verifier each.
//
// The password is the harness constant, and it is the same one the memory mode
// hands the fake — so "signing in succeeds" means the same thing in both modes, and
// the *wrong-password* step compares a real verification against the oracle's
// missing one rather than two fakes.
func (s *seeder) operators() error {
	hash, err := identity.HashPassword(harnessPassword)
	if err != nil {
		return err
	}
	for _, op := range s.w.Operators {
		o := &store.Operator{
			ID: op.Id, Username: op.Username, DisplayName: op.DisplayName,
			PasswordHash: hash, Role: string(op.Role), Enabled: op.Enabled,
		}
		if err := s.h.store.Operators().Create(s.ctx, o); err != nil {
			return err
		}
		// `created_at` defaults to now() and `last_login_at` is written by signing
		// in, so both are set directly. Every fixture timestamp is `Date.now() ± an
		// offset`, and a harness that let them default would be comparing clocks.
		if err := s.exec(
			`UPDATE operators SET created_at = $2, last_login_at = $3 WHERE id = $1`,
			op.Id, op.CreatedAt, op.LastLoginAt); err != nil {
			return err
		}
	}
	return nil
}

// ── controllers ──────────────────────────────────────────────────────────────

func (s *seeder) controllers() error {
	seen := map[string]bool{}
	for _, c := range s.w.Controllers {
		if seen[c.Id] {
			continue
		}
		seen[c.Id] = true

		rec := &store.Controller{
			ID: c.Id, Name: c.Name, Vendor: string(c.Vendor), BaseURL: c.BaseUrl,
			AuthMode: string(c.AuthMode), TLSMode: string(c.TlsMode),
			VendorControllerID:     c.ControllerId,
			CertificateFingerprint: c.CertificateFingerprint,
			Reachability:           string(c.Reachability),
			Version:                c.Version,
			VersionSupported:       c.VersionSupported,
		}
		// A credential exists or it does not, and `credentialSet` is the only thing
		// the console is ever told about one. The value is this process's own
		// random bytes, sealed by the store like any other.
		var credential []byte
		if c.CredentialSet {
			rec.Username = ptr("harness")
			credential = randomBytes(24)
		}
		if err := s.h.store.Controllers().Create(s.ctx, rec, credential); err != nil {
			return err
		}
		if err := s.exec(`
			UPDATE controllers
			   SET reachability = $2, version = $3, version_supported = $4,
			       certificate_fingerprint = $5, last_contact_at = $6,
			       created_at = coalesce($7, created_at)
			 WHERE id = $1`,
			c.Id, string(c.Reachability), c.Version, c.VersionSupported,
			c.CertificateFingerprint, c.LastContactAt, c.CreatedAt); err != nil {
			return err
		}
		for _, vs := range c.VendorSites {
			if err := s.vendorSite(c.Id, vs.Id, vs.Name, vs.BoundSiteId); err != nil {
				return err
			}
		}
	}
	return nil
}

// vendorSite writes one binding, and asks PostgreSQL what is allowed rather than
// restating it.
//
// **Carrying the constraint in Go would be the same defect the harness exists to
// find.** `controller_vendor_sites_bound_idx` is `(controller_id, bound_site_id)`
// since migration `00011` — the ambiguity that is real is two vendor sites *of one
// controller* on one site, since every resolution path goes through the device —
// and a Go copy written against an earlier index would refuse bindings the
// database accepts, silently, while printing a line naming an index that does not
// say that.
//
// So the rule is not restated. The binding is written as the oracle has it; if
// PostgreSQL refuses it as a duplicate, the row is written unbound instead and the
// refusal is counted **under the constraint the database itself named**. The seeder
// then tracks whatever the schema enforces, in either direction, without anybody
// remembering to come back here.
func (s *seeder) vendorSite(controllerID, vendorSiteID, name string, bound *string) error {
	const upsert = `
		INSERT INTO controller_vendor_sites (controller_id, vendor_site_id, name, bound_site_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (controller_id, vendor_site_id) DO UPDATE
		   SET name = excluded.name, bound_site_id = excluded.bound_site_id`

	err := s.exec(upsert, controllerID, vendorSiteID, name, bound)
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if bound == nil || !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
		return err
	}
	// A binding the schema will not hold. Written unbound so the run continues and
	// the controller reads diverge on exactly the field involved, and counted —
	// never dropped quietly, because this is the one category where the oracle's
	// world and the schema genuinely disagree.
	s.notSeeded[fmt.Sprintf(
		"vendor-site bindings written unbound: %q is already bound, and %s allows one",
		*bound, pgErr.ConstraintName)]++
	return s.exec(upsert, controllerID, vendorSiteID, name, nil)
}

// uniqueViolation is PostgreSQL's SQLSTATE for a duplicate key.
//
// Matched together with the constraint name rather than on the code alone:
// `database.md` records a permission test that passed while the table was open
// because two different objects answered the same SQLSTATE, and a negative case
// whose subject has more than one way to fail is a test of whichever guard fires
// first.
const uniqueViolation = "23505"

// coaVendorSeed narrows the generated enum pointer to the string the store holds.
//
// `radius_clients.coa_vendor` is a nullable text column and the read model turns it
// back into the enum, so the seed only has to carry the value through.
func coaVendorSeed(v *gen.CoaVendor) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

// ── devices: shared secrets, clients.conf rows, access points ────────────────

func (s *seeder) devices() error {
	if err := s.sharedSecrets(); err != nil {
		return err
	}
	for _, n := range s.w.Nas {
		if err := s.device(n); err != nil {
			return fmt.Errorf("%s: %w", n.Id, err)
		}
	}
	return nil
}

// sharedSecrets writes one row per secret the devices name.
//
// **Many devices share one**, because the secret lives in a controller-level RADIUS
// profile — per-device secrets were the previous object model's breakage. The read
// model names the id and nothing else about the secret, so the label is the id and
// `last_used_at` is the newest `secretLastUsedAt` across the devices presenting it,
// which is what that column means.
func (s *seeder) sharedSecrets() error {
	type secretInfo struct {
		siteID   string
		lastUsed *time.Time
	}
	secrets := map[string]*secretInfo{}
	var order []string
	for _, n := range s.w.Nas {
		if n.SharedSecretId == nil {
			continue
		}
		info, ok := secrets[*n.SharedSecretId]
		if !ok {
			info = &secretInfo{siteID: n.SiteId}
			secrets[*n.SharedSecretId] = info
			order = append(order, *n.SharedSecretId)
		}
		if n.SecretLastUsedAt != nil && (info.lastUsed == nil || n.SecretLastUsedAt.After(*info.lastUsed)) {
			info.lastUsed = n.SecretLastUsedAt
		}
	}
	for _, id := range order {
		info := secrets[id]
		ss := &store.SharedSecret{ID: id, SiteID: info.siteID, Label: id}
		// `randomSecret()` and not `randomBytes()`: a shared secret is projected
		// into `nas.secret` as text, so `SharedSecretRepo.Create` refuses a byte
		// that is not printable ASCII — and 24 random bytes contain one with
		// probability 1 for every practical purpose. Seeded with raw bytes, **the
		// adapter mode of this harness would not run at all**: it would die in the
		// seeder, before the first comparison, with `store: shared secret must be
		// printable ASCII`.
		if err := s.h.store.SharedSecrets().Create(s.ctx, ss, []byte(randomSecret())); err != nil {
			return err
		}
		if err := s.exec(`UPDATE shared_secrets SET last_used_at = $2 WHERE id = $1`,
			id, info.lastUsed); err != nil {
			return err
		}
	}
	return nil
}

// device writes one `Nas` back into the two rows it is a view of.
func (s *seeder) device(n gen.Nas) error {
	// The `clients.conf` row's id. It is the device's own only when the device has
	// no access point — which is the whole content of `coalesce(ap.id, rc.id)` and
	// the mismatch `ListNas` in `internal/store/radiusclients.go` resolves. Giving the client
	// a *different* id wherever there is an AP is what makes that resolution
	// observable here: a harness that used one id for both could not tell the two
	// apart, and would pass for an adapter that had never resolved anything.
	clientID := n.Id
	if n.Mac != nil {
		clientID = "rc-" + n.Id
	}
	s.clientID[n.Id] = clientID

	addr := s.addressFor(n)
	s.clientAddr[n.Id] = addr

	c := &store.RadiusClient{
		ID: clientID, SiteID: n.SiteId, Name: n.Name, Description: n.Description,
		Address: netip.PrefixFrom(addr, addr.BitLen()),
		Source:  string(n.Source), Driver: string(n.Driver),
		ControllerID: n.ControllerId, SharedSecretID: n.SharedSecretId,
		NASIdentifier: n.NasIdentifier, CoAPort: n.CoaPort,
		// **Carried, because a field the seeder drops is a difference this harness
		// invents.** Without this line the Cisco fixture would reach PostgreSQL
		// with no vendor (`coa_vendor`, migration `00015`) and the run would report
		// `coaVendor: only the mock has it ("cisco")` — a finding about the harness
		// wearing the shape of a finding about the server.
		CoAVendor: coaVendorSeed(n.CoaVendor),
		// Both switches are `security {}` globals in the generated config and the
		// columns are inert (`security.md` §3). They are written true so that no
		// row ever looks like a switch somebody turned off.
		RequireMsgAuth: true, LimitProxyState: true,
		MessageAuthState:     string(n.MessageAuthenticator),
		AccountingConfigured: n.AccountingConfigured,
		Reachability:         string(n.Reachability),
		Capabilities: store.Capabilities{
			LiveReauthorize: n.Capabilities.LiveReauthorize,
			WalledGarden:    string(n.Capabilities.WalledGarden),
			HardQuota:       n.Capabilities.HardQuota,
			BandwidthLimits: n.Capabilities.BandwidthLimits,
			DynamicVLAN:     n.Capabilities.DynamicVlan,
			RotateSecret:    n.Capabilities.RotateSecret,
			CanDeauthGuest:  n.Capabilities.CanDeauthGuest,
		},
		Enabled: n.Enabled,
	}
	if err := s.h.store.RadiusClients().Create(s.ctx, c); err != nil {
		return err
	}
	if n.CreatedAt != nil {
		if err := s.exec(`UPDATE radius_clients SET created_at = $2 WHERE id = $1`,
			clientID, *n.CreatedAt); err != nil {
			return err
		}
	}

	// A device with a MAC is an access point and gets its own row. One without is
	// its own NAS — a MikroTik router, a CoA-only switch — and there is nothing to
	// discover. That is not an omission: an access point invented here would put a
	// name and a MAC into the world that the oracle does not have, and the whole
	// value of seeding from the oracle is that the harness carries no world of its
	// own.
	if n.Mac == nil {
		s.calledStation[n.Id] = syntheticStationMAC(n.Id)
		return nil
	}

	via := "self"
	if n.RadiusVia != nil {
		via = string(*n.RadiusVia)
	}
	state := "online"
	switch n.Reachability {
	case gen.NasReachabilityUnreachable:
		state = "offline"
	case gen.NasReachabilityUnknown:
		state = "unknown"
	}
	if err := s.exec(`
		INSERT INTO access_points
			(id, site_id, controller_id, mac, name, model, firmware, ip_address,
			 radius_client_id, radius_via, state, last_seen_at, created_at)
		VALUES ($1,$2,$3,$4::macaddr,$5,$6,$7,$8::inet,$9,$10,$11,$12,coalesce($13, now()))`,
		n.Id, n.SiteId, n.ControllerId, *n.Mac, n.Name, n.Model, n.Firmware,
		n.IpAddress, clientID, via, state, n.LastSeenAt, n.CreatedAt); err != nil {
		return err
	}
	s.calledStation[n.Id] = store.NormaliseMAC(*n.Mac)
	return nil
}

// addressFor gives a device the address its Access-Requests arrive from.
//
// **The contract's `Nas` has no address**, so this is invented rather than carried,
// and the count of invented addresses is printed. `ipAddress` is used wherever the
// oracle has one, because that is the address the device actually holds and using it
// keeps `listNas?q=<address>` comparable; otherwise an address is taken from
// 198.51.100.0/24 (RFC 5737 TEST-NET-2), which nothing else in this world uses.
func (s *seeder) addressFor(n gen.Nas) netip.Addr {
	if n.IpAddress != nil {
		if a, err := netip.ParseAddr(*n.IpAddress); err == nil {
			return a
		}
	}
	s.synthetic++
	s.nextHost++
	return netip.AddrFrom4([4]byte{198, 51, 100, byte(s.nextHost)})
}

// syntheticStationMAC invents the MAC at the head of a `Called-Station-Id` for a
// device with no access point.
//
// A real NAS sends one regardless — a MikroTik puts its own radio's address there —
// so a session with none would be a fixture no deployment produces, and the SSID
// would be unparseable for a reason that is the harness's rather than the product's.
// The address is derived from the device id and carries the locally-administered
// bit, so it can never collide with a discovered access point's MAC.
func syntheticStationMAC(deviceID string) string {
	sum := sha256.Sum256([]byte(deviceID))
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", sum[0], sum[1], sum[2], sum[3], sum[4])
}

// ── vouchers ─────────────────────────────────────────────────────────────────

func (s *seeder) vouchers() error {
	byBatch := map[string][]gen.Voucher{}
	for _, v := range s.w.Vouchers {
		byBatch[v.BatchId] = append(byBatch[v.BatchId], v)
	}

	for _, b := range s.w.Batches {
		vs := byBatch[b.Id]
		news := make([]store.NewVoucher, 0, len(vs))
		for _, v := range vs {
			news = append(news, store.NewVoucher{ID: v.Id, Code: v.Code})
		}
		rec := &store.VoucherBatch{
			ID: b.Id, SiteID: b.SiteId, Label: b.Label, Count: b.Count,
			CreatedBy: b.CreatedBy, ExpiryMode: string(b.ExpiryMode),
			ExpiresAt: b.ExpiresAt,
			Plan: store.Plan{
				Name: b.Plan.Name, DurationMinutes: b.Plan.DurationMinutes,
				DataAllowanceMB: b.Plan.DataAllowanceMb,
				DownKbps:        b.Plan.DownKbps, UpKbps: b.Plan.UpKbps,
				EnforcedBy: string(b.Plan.EnforcedBy),
			},
		}
		if b.Plan.NameLang != nil {
			rec.Plan.NameLang = ptr(string(*b.Plan.NameLang))
		}
		if err := s.h.store.Vouchers().CreateBatch(s.ctx, rec, news); err != nil {
			return err
		}
		if err := s.exec(`UPDATE voucher_batches SET created_at = $2 WHERE id = $1`,
			b.Id, b.CreatedAt); err != nil {
			return err
		}

		// The state histogram the console renders is aggregated from the voucher
		// rows rather than stored on the batch, so the states have to be written: a
		// batch seeded all-`unused` would make `counts` agree with nothing, and the
		// expired / exhausted / revoked-mid-session codes are exactly the rows the
		// fixtures exist to carry.
		for _, v := range vs {
			if err := s.exec(`
				UPDATE vouchers
				   SET state = $2, first_used_at = $3, expires_at = $4,
				       revoked_at = $5, current_session_id = $6,
				       created_at = coalesce($7, created_at)
				 WHERE id = $1`,
				v.Id, string(v.State), v.FirstUsedAt, v.ExpiresAt,
				v.RevokedAt, v.CurrentSessionId, v.CreatedAt); err != nil {
				return err
			}
		}
	}

	known := map[string]bool{}
	for _, b := range s.w.Batches {
		known[b.Id] = true
	}
	for _, v := range s.w.Vouchers {
		if !known[v.BatchId] {
			s.notSeeded["vouchers whose batch the oracle did not list"]++
		}
	}
	return nil
}

// ── accounts, portal, garden, audit ──────────────────────────────────────────

func (s *seeder) accounts() error {
	hash, err := identity.HashPassword(harnessPassword)
	if err != nil {
		return err
	}
	for _, a := range s.w.Accounts {
		rec := &store.Account{
			ID: a.Id, SiteID: a.SiteId, Username: a.Username, DisplayName: a.DisplayName,
			PasswordHash: hash, Enabled: a.Enabled,
		}
		if a.Plan != nil {
			rec.Plan = store.Plan{
				Name: a.Plan.Name, DurationMinutes: a.Plan.DurationMinutes,
				DataAllowanceMB: a.Plan.DataAllowanceMb,
				DownKbps:        a.Plan.DownKbps, UpKbps: a.Plan.UpKbps,
				EnforcedBy: string(a.Plan.EnforcedBy),
			}
			if a.Plan.NameLang != nil {
				rec.Plan.NameLang = ptr(string(*a.Plan.NameLang))
			}
		}
		if err := s.h.store.Accounts().Create(s.ctx, rec); err != nil {
			return err
		}
		if err := s.exec(`UPDATE accounts SET created_at = $2, last_login_at = $3 WHERE id = $1`,
			a.Id, a.CreatedAt, a.LastLoginAt); err != nil {
			return err
		}
		if err := s.credentialVerifiers(a); err != nil {
			return err
		}
	}
	return nil
}

// credentialBase is the scheme every identity in this schema has, and the prefix
// `internal/store` builds every `credentialKind` from.
const credentialBase = "argon2id"

// seededCredentialComponents are the opt-in verifiers credentialVerifiers can
// write, named by the component `internal/store` appends to `credentialKind` when
// the row exists.
//
// **This is the seeder's knowledge, stated as data so a test can read it.** See
// TestEverySeededCredentialKindDecomposes, which asks the contract for the enum's
// members and requires every one of them to be built from these and nothing else.
var seededCredentialComponents = map[string]bool{"nt": true, "bcrypt": true}

// unknownCredentialComponents returns the parts of a `credentialKind` this seeder
// cannot produce a verifier for. Empty means the value is fully seedable.
func unknownCredentialComponents(kind string) []string {
	parts := strings.Split(kind, "+")
	var unknown []string
	if parts[0] != credentialBase {
		unknown = append(unknown, parts[0])
	}
	for _, p := range parts[1:] {
		if !seededCredentialComponents[p] {
			unknown = append(unknown, p)
		}
	}
	return unknown
}

func hasCredentialComponent(kind, component string) bool {
	for _, p := range strings.Split(kind, "+")[1:] {
		if p == component {
			return true
		}
	}
	return false
}

// credentialVerifiers writes the opt-in rows behind `credentialKind`, and then
// checks that it wrote the right ones.
//
// ── why this reads the value apart instead of matching it whole ──────────────
//
// `credentialKind` is **derived**, never stored as a flag: `internal/store` builds
// it as `argon2id`, plus `+nt` if a row exists in `account_nt_hashes`, plus
// `+bcrypt` if one exists in `account_pap_verifiers`. That is what makes it
// trustworthy — a flag can be wrong, a `LEFT JOIN` cannot — and it is also what
// makes an equality test against one enum member the wrong shape here.
//
// An equality like `a.CredentialKind == gen.Argon2idNt` goes quiet when the enum
// grows `argon2id+bcrypt` and `argon2id+nt+bcrypt`: the `+bcrypt` accounts get no
// verifier, the `+nt+bcrypt` account does not get even its NT hash, and the differ
// reports `credentialKind` against a product that is answering correctly about the
// database it was actually given — a loud finding, with a real difference, against
// a defect that does not exist.
//
// So the arms are read out of the kind the same way the database composes it, one
// verifier per component. **A member that is a new combination of components this
// seeder knows needs no edit here at all**, which is the case that would go quiet.
//
// ── the component it does not know is the one it cannot derive ───────────────
//
// Deriving cannot cover a genuinely new component — `argon2id+scrypt`, say. Two
// things catch that, at different moments and for different readers:
//
//   - TestEverySeededCredentialKindDecomposes reads the enum out of the contract and
//     fails at `go test` time, before anybody runs the differ.
//   - This function reads the account back and compares the kind the database
//     computed with the one the oracle declared, and **aborts the run** on a
//     mismatch. It is the backstop for a fact that arrives from the fixture world
//     rather than from the enum.
//
// Aborting rather than recording it in `notSeeded`: a summary line at the end of a
// report lets the comparison proceed on a fact the seeder failed to reproduce, which
// is how a false divergence gets printed with an exit code. A seeder that cannot
// build the world must say so before either server is asked about it.
func (s *seeder) credentialVerifiers(a gen.Account) error {
	kind := string(a.CredentialKind)

	// PEAP-MSCHAPv2. An NT hash is password-equivalent, which is why the repository
	// makes storing one its own named decision rather than a field on the account.
	if hasCredentialComponent(kind, "nt") {
		if err := s.h.store.Accounts().OptInToMSCHAPv2(s.ctx, a.Id, randomBytes(16), nil); err != nil {
			return err
		}
	}
	// 802.1X over EAP-TTLS/PAP. bcrypt, not argon2id, because `rlm_pap` has no
	// argon2 scheme — `docs/rules/security.md` §1. The verifier is hashed here
	// for the same reason the password is: `internal/store` never sees a cleartext
	// one.
	if hasCredentialComponent(kind, "bcrypt") {
		verifier, err := identity.HashPAPVerifier(harnessPassword)
		if err != nil {
			return err
		}
		if err := s.h.store.Accounts().OptInTo8021X(s.ctx, a.Id, verifier, nil); err != nil {
			return err
		}
	}

	got, err := s.h.store.Accounts().Get(s.ctx, a.Id)
	if err != nil {
		return err
	}
	if got.CredentialKind != kind {
		return fmt.Errorf(
			"seeding %s: the oracle says credentialKind %q and the database computes %q "+
				"(components this seeder cannot write: %v). It writes one opt-in row per "+
				"component and knows `nt` and `bcrypt`; a component it does not know "+
				"produces no row, and the comparison would then report `credentialKind` as "+
				"a defect in `internal/apistore` against a product answering correctly "+
				"about the database it was given. Teach "+
				"credentialVerifiers the new verifier, or record why it cannot be written",
			a.Id, kind, got.CredentialKind, unknownCredentialComponents(kind))
	}
	return nil
}

func (s *seeder) portal() error {
	for siteID, p := range s.w.Portal {
		rec := &store.PortalSettings{
			SiteID: siteID, PortalURL: p.PortalUrl, VenueName: p.VenueName,
			NetworkName: p.NetworkName, LogoDataURI: p.LogoDataUri,
			ClickThroughMinutes: p.ClickThroughMinutes,
			TermsSummary:        p.Terms.Summary,
		}
		for _, l := range p.Languages {
			rec.Languages = append(rec.Languages, string(l))
		}
		for _, m := range p.Methods {
			rec.Methods = append(rec.Methods, string(m))
		}
		// The design as the oracle answers it, whole — the column holds the
		// contract's `PortalDesign` verbatim, so this is the same encoding the
		// adapter writes rather than an inversion. `blocks` goes in as an array
		// even if the oracle's world decoded to a nil slice.
		design := p.Design
		if design.Blocks == nil {
			design.Blocks = []gen.PortalBlock{}
		}
		raw, err := json.Marshal(design)
		if err != nil {
			return fmt.Errorf("encoding the portal design of %s: %w", siteID, err)
		}
		rec.Design = raw
		if err := s.h.store.Portal().Upsert(s.ctx, rec); err != nil {
			return err
		}
		if p.HeroImage != nil {
			s.notSeeded["portal hero images — the oracle serves bytes this harness does not fetch"]++
		}
	}
	return nil
}

func (s *seeder) garden() error {
	for _, g := range s.w.Garden {
		for _, e := range g.Entries {
			if err := s.h.store.Garden().Create(s.ctx, &store.GardenEntry{
				ID: e.Id, SiteID: e.SiteId, Kind: string(e.Kind),
				Value: e.Value, Note: e.Note, Origin: string(e.Origin),
			}); err != nil {
				return err
			}
			if err := s.exec(`UPDATE walled_garden_entries SET created_at = $2 WHERE id = $1`,
				e.Id, e.CreatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *seeder) audit() error {
	for _, e := range s.w.Audit {
		rec := &store.AuditEntry{
			ID: e.Id, ActorID: e.ActorId, ActorUsername: e.ActorUsername,
			Kind: string(e.Kind), SiteID: e.SiteId,
		}
		if e.Params != nil {
			rec.Params = map[string]any(*e.Params)
		}
		if e.Subject != nil {
			rec.SubjectKind = string(e.Subject.Kind)
			rec.SubjectID = e.Subject.Id
			rec.SubjectName = e.Subject.Name
		}
		if err := s.h.store.Audit().Append(s.ctx, rec); err != nil {
			return err
		}
		// `audit_log` is INSERT-only for the application role — an audit trail a
		// process can rewrite is not one — so the timestamp is corrected through
		// the owner's connection, for the same reason the accounting rows are.
		if err := s.exec(`UPDATE audit_log SET at = $2 WHERE id = $1`, e.Id, e.At); err != nil {
			return err
		}
	}
	return nil
}

// ── the two tables Go may not write ──────────────────────────────────────────

// accounting writes `radacct` through the schema owner's connection.
//
// It cannot be written any other way and that is the point: `foyer_app` holds SELECT
// on `radacct` and nothing else, so an INSERT through the store's own pool is
// refused by PostgreSQL rather than by a code review. This connection stands in for
// FreeRADIUS, which is the only thing allowed to write it — and `assertOwner` has
// already established that it really is the owner rather than the application.
func (s *seeder) accounting() error {
	for _, sess := range s.w.Sessions {
		addr, ok := s.clientAddr[sess.NasId]
		if !ok {
			// A session on a device the oracle did not list. Counted, because a
			// row dropped silently here would make the adapter look short.
			s.notSeeded["sessions whose device the oracle did not list"]++
			continue
		}
		called := s.calledStation[sess.NasId]
		if sess.Ssid != nil {
			called += ":" + *sess.Ssid
		}
		acctSessionID := ""
		if sess.AcctSessionId != nil {
			acctSessionID = *sess.AcctSessionId
		}
		if err := s.exec(`
			INSERT INTO radacct
				(acctuniqueid, acctsessionid, username, nasipaddress, callingstationid,
				 calledstationid, acctstarttime, acctupdatetime, acctstoptime,
				 acctinterval, acctsessiontime, acctinputoctets, acctoutputoctets,
				 acctterminatecause, framedipaddress)
			VALUES ($1,$2,$3,$4::inet,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::inet)`,
			sess.Id, acctSessionID, sess.Username, addr.String(), sess.CallingStationId,
			called, sess.StartedAt, sess.LastUpdateAt, sess.EndedAt,
			int64ptr(sess.Freshness.IntervalSeconds), int64ptr(sess.SessionSeconds),
			// The column holds the 64-bit total the generated accounting query has
			// already reconstructed from Acct-*-Octets and Acct-*-Gigawords, so it
			// is `inputOctetsTotal` that goes in and never the wrapping counter.
			// Seeding the reported figure instead would quietly un-wrap the one
			// fixture that exists to carry a wrap.
			int64ptr(sess.InputOctetsTotal), int64ptr(sess.OutputOctetsTotal),
			sess.TerminateCause, sess.IpAddress); err != nil {
			return err
		}
	}
	return nil
}

// attempts writes `radpostauth`.
//
// **Only the RADIUS-sourced rows, because only those exist.** `radpostauth` is the
// stock FreeRADIUS column set kept verbatim — the project's exit hatch — and it has
// no column for an event source, a voucher, an account or a portal grant. The
// contract's `AuthAttempt.source` names three streams and PostgreSQL holds one of
// them. The rest are counted here, and `listAuthAttempts` is declared divergent in
// scenario.go with that reason rather than being quietly padded.
func (s *seeder) attempts() error {
	for _, at := range s.w.Attempts {
		if at.Source != gen.Radius {
			s.notSeeded[fmt.Sprintf("auth attempts with source=%q — radpostauth has no such column", at.Source)]++
			continue
		}
		username := ""
		if at.Username != nil {
			username = *at.Username
		}
		reply := "Access-Reject"
		if at.Outcome == gen.Accepted {
			reply = "Access-Accept"
		}
		called := at.CalledStationId
		if called == nil && at.NasId != nil {
			// The row's site and device are resolved from the called-station MAC
			// and from nothing else, so a row that names a device but no station is
			// given that device's. Without it the adapter would drop the row from
			// every site-scoped query — its documented behaviour, and one that
			// would look here like a missing row rather than a missing MAC.
			if cs, ok := s.calledStation[*at.NasId]; ok {
				called = ptr(cs)
			}
		}
		if err := s.exec(`
			INSERT INTO radpostauth (username, reply, callingstationid, calledstationid, authdate)
			VALUES ($1,$2,$3,$4,$5)`,
			username, reply, at.CallingStationId, called, at.At); err != nil {
			return err
		}
	}
	return nil
}

// ── what the seeding carried, printed ────────────────────────────────────────

func (s *seeder) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  seeded into PostgreSQL: %d sites · %d controllers · %d devices · %d sessions ·"+
		" %d vouchers in %d batches · %d operators\n",
		len(s.w.Sites), countControllers(s.w), len(s.w.Nas), len(s.w.Sessions),
		len(s.w.Vouchers), len(s.w.Batches), len(s.w.Operators))
	fmt.Fprintf(&b, "  %d of %d devices needed an invented clients.conf address: the contract's `Nas`\n"+
		"  carries no address, so one is allocated wherever `ipAddress` is null.\n",
		s.synthetic, len(s.w.Nas))

	// **The one line in this report that is about what cannot be compared.**
	// Every session, account, device and voucher batch in the oracle's world lives
	// in one of its three sites, so no comparison here can show a site scope being
	// honoured or dropped — the property where a naive adapter answers
	// `listAuthAttempts?siteId=` with every site's usernames and MACs. Printed on
	// every run rather than only under `-sabotage site-scope`, because a harness
	// that could not have caught something has to say so where the result is read.
	if occupied := occupiedSites(s.w); len(occupied) < 2 {
		fmt.Fprintf(&b, "  **site scoping is not compared by this run**: the oracle's world has rows in\n"+
			"  %d of its %d sites (%s), so dropping the scope entirely would change no answer.\n"+
			"  `-sabotage site-scope` refuses for the same reason. Known limitation of `web/fixtures`.\n",
			len(occupied), len(s.w.Sites), strings.Join(occupied, ", "))
	}
	if len(s.notSeeded) > 0 {
		var names []string
		for k := range s.notSeeded {
			names = append(names, k)
		}
		sort.Strings(names)
		b.WriteString("  what the schema could not hold, counted rather than dropped quietly:\n")
		for _, k := range names {
			fmt.Fprintf(&b, "    · %-4d %s\n", s.notSeeded[k], k)
		}
	}
	return b.String()
}

func countControllers(w *memory.World) int {
	seen := map[string]bool{}
	for _, c := range w.Controllers {
		seen[c.Id] = true
	}
	return len(seen)
}

func int64ptr(v *int) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func ptr[T any](v T) *T { return &v }
