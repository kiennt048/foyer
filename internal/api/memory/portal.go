package memory

import (
	"context"
	"errors"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/portal"
)

// The guest portal's ports, over the same World.
//
// **A fake, and this file is the one where saying so matters most.** The named
// security test is *"no grant is created for a MAC the driver did not confirm
// present"*, and the thing that would make it pass while the product is broken is
// a driver here that answers "present" to everything. `internal/nas` ships two
// mock drivers precisely because of that — `NewMockDriver` is permissive and
// `NewStrictMockDriver` is not — and `PortalPorts` takes whichever the caller
// hands it rather than choosing. Swapping one for the other is how that test is
// made to fail on purpose.

var (
	_ portal.Credentials  = (*Store)(nil)
	_ portal.SiteResolver = (*Store)(nil)
)

// PortalPorts wires the guest surface over this world.
//
// The driver is a parameter with no default. A fake with a permissive driver
// baked in would be a fake that quietly satisfies §4, and a caller that forgot to
// pass one would get the permissive behaviour by omission — which is the one
// direction this must never fail in.
func (s *Store) PortalPorts(driver nas.NASDriver) portal.Ports {
	if s.PortalStore == nil {
		s.PortalStore = portal.NewMemoryStore()
		s.PortalStore.Now = s.clock
	}
	return portal.Ports{
		Sessions:    s.PortalStore,
		Grants:      s.PortalStore,
		Credentials: s,
		Sites:       s,
		Drivers:     &driverPort{driver: driver},
		// Wired, not defaulted. `portal.NewService` supplies a log-only recorder
		// when this is nil, and a suite running on that would prove the failure
		// reaches a log line and nothing else — while the two readers that
		// matter, `listAuthAttempts` and the guest's connected page, could stay
		// silent without the suite noticing.
		Authorizations: s.PortalStore,
		PhoneCodes:     s.phoneCodes(),
	}
}

// ── the driver seam ──────────────────────────────────────────────────────────

type driverPort struct{ driver nas.NASDriver }

// Verify reaches for `PresenceDetailer` where the driver implements it.
//
// The fallback is honest about what it loses: a driver that can only answer a
// bool can produce `verified` or `not-found` and nothing else, so an outage behind
// such a driver still reads to the guest as a refusal. That is a property of the
// driver, and the fix is to implement the optional interface — which UniFi and
// both mocks do.
func (d *driverPort) Verify(ctx context.Context, s portal.Session) (nas.Presence, error) {
	site := s.VendorSiteID
	if site == "" {
		site = s.SiteID
	}
	if det, ok := d.driver.(nas.PresenceDetailer); ok {
		return det.VerifyPresenceDetail(ctx, s.ClientMAC, s.APMAC, site)
	}
	ok, err := d.driver.VerifyPresence(ctx, s.ClientMAC, s.APMAC, site)
	if err != nil {
		return nas.Presence{Outcome: nas.PresenceUnavailable}, err
	}
	if !ok {
		return nas.Presence{Outcome: nas.PresenceNotFound}, nil
	}
	return nas.Presence{OK: true, Outcome: nas.PresenceVerified}, nil
}

// Authorize takes the placement because a grant carries Foyer's site id and the
// driver takes the controller's. This fake had the same conflation the adapter did.
func (d *driverPort) Authorize(ctx context.Context, p portal.Placement, g portal.Grant) error {
	site := p.VendorSiteID
	if site == "" {
		site = p.SiteID
	}
	req := nas.AuthorizeRequest{
		ClientMAC: g.ClientMAC,
		APMAC:     g.APMAC,
		SiteID:    site,
	}
	// The plan is handed to the NAS where the NAS is the thing enforcing it.
	// Where it is not, sending the numbers anyway would be asking a device to
	// promise something its driver has already said it cannot do.
	if g.Plan.EnforcedBy == portal.EnforcedByNAS {
		if g.Plan.DurationMinutes != nil {
			req.TimeLimitMinutes = *g.Plan.DurationMinutes
		}
		if g.Plan.DataAllowanceMB != nil {
			req.DataUsageLimitMB = *g.Plan.DataAllowanceMB
		}
		if g.Plan.UpKbps != nil {
			req.TxRateLimitKbps = *g.Plan.UpKbps
		}
		if g.Plan.DownKbps != nil {
			req.RxRateLimitKbps = *g.Plan.DownKbps
		}
	}
	return d.driver.AuthorizeGuest(ctx, req)
}

func (d *driverPort) HardQuota(_ context.Context, _ portal.Session) bool {
	return d.driver.Capabilities().HardQuota
}

// ── where a guest is standing ────────────────────────────────────────────────

// Placement resolves the access point the NAS named to a site.
//
// An unknown access point is `ErrNotFound` and no session is minted. There is no
// fallback to "the only site", because a portal that defaults for an AP it has
// never heard of is a portal authorizing devices on a network it does not manage
// — and in a world with one site that default would be invisible until the second
// site existed.
func (s *Store) Placement(_ context.Context, apMAC string) (portal.Placement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.world.Nas {
		if n.Mac != nil && portal.NormaliseMAC(*n.Mac) == apMAC && apMAC != "" {
			// The fake world's controller is the device's own registration:
			// there is no separate controller row in `memory.World`, so the
			// placement names the device and the adapter over PostgreSQL is
			// where a real controller id comes from.
			//
			// `accountingConfigured` is three-valued in the fixtures for the same
			// reason it is nullable in the schema, and it decides whether a
			// limited plan behind a soft-quota driver is `foyer-sampled` or
			// `unenforced`.
			place := portal.Placement{
				SiteID: n.SiteId, NASID: n.Id, VendorSiteID: n.SiteId, ControllerID: n.Id,
			}
			switch {
			case n.AccountingConfigured == nil:
				place.Accounting = portal.AccountingUnknown
			case *n.AccountingConfigured:
				place.Accounting = portal.AccountingArrives
			default:
				place.Accounting = portal.AccountingAbsent
			}
			return place, nil
		}
	}
	return portal.Placement{}, portal.ErrNotFound
}

// ── credentials ──────────────────────────────────────────────────────────────

// ClaimVoucher is the single-use guarantee, as far as an in-memory world can
// carry it.
//
// It is one critical section, which is the in-memory analogue of one statement:
// the state is tested and changed under the same lock, so two guests submitting
// the same code cannot both read `unused`. **That is not evidence about
// PostgreSQL** — the real guarantee is a conditional `UPDATE … WHERE state =
// 'unused'` plus a partial unique index on `portal_grants(voucher_id)`, written
// out in `internal/portal/CONTRACT.md`. A mutex here proves the rule is expressed
// in the interface, not that the database keeps it.
func (s *Store) ClaimVoucher(_ context.Context, siteID, code, clientMAC string, at time.Time) (portal.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.world.Vouchers {
		v := &s.world.Vouchers[i]
		// Constant-time, because a voucher is a bearer credential submitted by
		// anybody within radio range. See `portal.SameCode` for why the SQL path
		// cannot do this and what stands in for it there.
		if !portal.SameCode(v.Code, code) {
			continue
		}
		batch, ok := s.batchOf(v.BatchId)
		if !ok || (siteID != "" && batch.SiteId != siteID) {
			// A voucher from another venue is not a voucher here. Reported as
			// unknown rather than as "wrong site": an answer that distinguished
			// them would tell a stranger which codes exist somewhere else.
			return portal.Claim{}, portal.ErrVoucherUnknown
		}

		switch v.State {
		case gen.VoucherStateRevoked:
			return portal.Claim{}, portal.ErrVoucherRevoked
		case gen.VoucherStateExpired:
			return portal.Claim{}, portal.ErrVoucherExpired
		case gen.VoucherStateInUse, gen.VoucherStateExhausted:
			return portal.Claim{}, portal.ErrVoucherUsed
		}
		if v.ExpiresAt != nil && !v.ExpiresAt.After(at) {
			return portal.Claim{}, portal.ErrVoucherExpired
		}

		v.State = gen.VoucherStateInUse
		if v.FirstUsedAt == nil {
			t := at
			v.FirstUsedAt = &t
		}
		// `from-first-use` sets the voucher's own expiry *now*, which is the
		// whole of what the mode means. `from-creation` leaves whatever the
		// batch set.
		if batch.ExpiryMode == gen.FromFirstUse && batch.Plan.DurationMinutes != nil {
			t := at.Add(time.Duration(*batch.Plan.DurationMinutes) * time.Minute)
			v.ExpiresAt = &t
		}
		_ = clientMAC // the real store records the claiming MAC; see CONTRACT.md

		return portal.Claim{
			VoucherID: v.Id,
			BatchID:   batch.Id,
			Plan:      planIn(batch.Plan),
			ExpiresAt: v.ExpiresAt,
		}, nil
	}
	return portal.Claim{}, portal.ErrVoucherUnknown
}

func (s *Store) batchOf(id string) (gen.VoucherBatch, bool) {
	for _, b := range s.world.Batches {
		if b.Id == id {
			return b, true
		}
	}
	return gen.VoucherBatch{}, false
}

// AuthenticateGuest verifies a credential it never returns.
//
// The world holds plaintext in `Credentials`, which is exactly what a real store
// must not do and is why that field carries the comment it does. A real
// `AccountDirectory` verifies an argon2id verifier; this exists so a test can log
// in.
func (s *Store) AuthenticateGuest(_ context.Context, siteID, username, password string) (portal.Guest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Accounts {
		a := &s.world.Accounts[i]
		if a.Username != username || (siteID != "" && a.SiteId != siteID) {
			continue
		}
		if password == "" || s.world.Credentials[a.Id] != password {
			return portal.Guest{}, portal.ErrBadCredentials
		}
		if !a.Enabled {
			return portal.Guest{}, portal.ErrAccountDisabled
		}
		now := s.clock()
		a.LastLoginAt = &now
		g := portal.Guest{AccountID: a.Id, Username: a.Username}
		if a.Plan != nil {
			g.Plan = planIn(*a.Plan)
		}
		return g, nil
	}
	// An unknown username and a wrong password are one answer. Distinguishing
	// them publishes which accounts exist, on an endpoint reachable by anyone
	// within radio range.
	return portal.Guest{}, portal.ErrBadCredentials
}

// planIn maps the contract's plan onto the portal's.
//
// `enforcedBy` is deliberately dropped. It is re-derived at grant time from the
// driver that will actually be asked to honour the plan, because the value stored
// on a batch was decided when the batch was created and a batch is redeemable
// anywhere in the site — including on a device registered afterwards with
// different capabilities.
func planIn(p gen.Plan) portal.Plan {
	out := portal.Plan{
		DurationMinutes: p.DurationMinutes,
		DataAllowanceMB: p.DataAllowanceMb,
		UpKbps:          p.UpKbps,
		DownKbps:        p.DownKbps,
	}
	if p.Name != nil {
		out.Name = *p.Name
	}
	if p.NameLang != nil {
		out.NameLang = string(*p.NameLang)
	}
	return out
}

// ErrNoPortal is what a caller gets for asking this fake for a portal store it
// never built. Present so the failure names itself rather than arriving as a nil
// dereference three frames away.
var ErrNoPortal = errors.New("memory: no portal store — call PortalPorts first")
