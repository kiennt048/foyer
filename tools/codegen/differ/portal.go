package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/portal"
)

// The guest surface, and the fake that stands behind it.
//
// ── why this file exists ─────────────────────────────────────────────────────
//
// Until it did, `main.go` built `api.New(api.Config{Ports: …})` with `Config.Portal`
// left zero. `internal/api/server.go` says `s.portal` is nil unless a deployment
// wired it and every portal handler answers `501` on that nil, so **the five guest
// operations could not have been compared even if nothing had skipped them**: the
// harness would have diffed a structurally-refusing Go server against a granting
// mock, and every line of the report would have been a fact about the harness.
//
// ── the trap, named before it is walked into ─────────────────────────────────
//
// `Config.Portal` is documented "all of it or none of it", so wiring it means
// supplying a driver seam — and **a fake `VerifyPresence` that returns true for
// everything is exactly what `docs/rules/testing.md`'s Principles call "an input
// the harness cannot be wrong about"**, the kind that produces a loud finding
// against a product that does not exist. The named security test here is *no
// grant is created for a MAC the driver did not confirm present*; a permissive
// fake makes it pass while the product is broken.
//
// So the fake is **derived rather than asserted**, in three separate ways, and each
// one is a thing that can be wrong:
//
//   - **Capabilities come off the shipped driver.** `nas.MockDriver`'s `Caps` are
//     `nas.ShippedCapabilities(vendor)` — read from `internal/nas/unifi.go`, not
//     restated — so a capability gate reached through this fake is the branch a
//     deployment reaches.
//   - **Presence refuses by default.** `nas.NewStrictMockDriver` answers *not
//     present* for every MAC it has not been told about, which is the direction a
//     security fake must fail in. `NewMockDriver` is the permissive one and is not
//     used here.
//   - **The one MAC it does know is placed on a real access point.** `WithClient`
//     records the AP MAC read out of the oracle's own device row, and
//     `MockClient.presence` answers `PresenceAPMismatch` when the claimed AP differs.
//     So `harnessGuestAP` is load-bearing: point a step at another device's MAC and
//     the run goes red.
//
// What it deliberately does **not** claim is the site. `MockClient.SiteID` is left
// empty, which `internal/nas` documents as *the controller does not place this
// client and the claimed site is not checked* — because the two modes resolve
// different vendor site identifiers for the same device (the fake world has no
// controller row and names the device itself; the adapter reads the controller's
// bound vendor site), and a value here would have to be right in one mode and wrong
// in the other. An unchecked claim stated out loud is worth more than a claim
// invented to make a check look present. It is also, per
// `docs/protocol-reality.md`, close to what UniFi actually gives: nothing in the
// Integration API's client object identifies an access point either.

// harnessGuestMAC is the client address the guest steps claim.
//
// It is deliberately outside every fixture range and **locally administered** —
// the second-least-significant bit of the first octet is set — because that is
// what a phone presents on a captive-portal SSID: iOS 14+ and Android 10+ both
// default to a per-SSID private address (`docs/rules/security.md` §6). A
// harness driving a burned-in OUI would be driving the case that has become rare.
const harnessGuestMAC = "02:1a:2b:3c:4d:5e"

// harnessGuestSSID and harnessGuestURL are the other two NAS redirect parameters.
//
// They are unsigned query values anyone within radio range can forge, which is the
// whole reason `VerifyPresence` exists — so the harness supplies them the way an
// access point would and lets both servers decide what to believe.
const (
	harnessGuestSSID = "Foyer Guest"
	harnessGuestURL  = "http://neverssl.example/start"
)

// guestPlacement is where the harness's guest is standing.
//
// Both servers resolve a site from the **access point MAC** and refuse an AP they
// have never heard of, so this has to name a device that really exists in the
// oracle's world rather than a literal.
type guestPlacement struct {
	NasID string
	APMAC string
	Site  string
}

// harnessGuestAP picks the access point the guest steps claim to be standing at.
//
// Chosen by property and never by id, for the reason the rest of this scenario
// gives: a hardcoded `nas-hcm-01` breaks the day somebody renames a fixture and —
// worse — would pass silently if the row were removed.
//
// The properties are the ones the two servers actually require of it:
//
//   - **a MAC**, because that is the only thing a NAS redirect carries that can
//     resolve to a device at all;
//   - **a controller**, because `internal/apistore`'s placement carries a
//     `ControllerID` and its driver seam refuses rather than picking one when the
//     access point is bound to nothing;
//   - **enabled, and a reachable controller**, because a device behind a controller
//     the harness's own driver reports unreachable answers `PresenceUnavailable`,
//     which is a real product branch and not the one these steps are about.
//
// It returns the empty placement when the world holds no such device, and the
// caller turns that into a skip whose reason names the condition rather than a
// phase — see `portalSkipReason`.
func harnessGuestAP(w *memory.World) guestPlacement {
	reachable := map[string]bool{}
	for _, c := range w.Controllers {
		reachable[c.Id] = c.Reachability == gen.NasReachabilityOk
	}
	usable := func(n gen.Nas) bool {
		if n.Mac == nil || *n.Mac == "" || !n.Enabled {
			return false
		}
		return n.ControllerId != nil && reachable[*n.ControllerId]
	}
	// The site the rest of the scenario reads is preferred, so that a divergence
	// on the guest surface is not also a divergence about which venue answered.
	// It is a preference and not a requirement: a world whose first site has no
	// usable access point still gets compared, from wherever one is.
	preferred := ""
	if len(w.Sites) > 0 {
		preferred = w.Sites[0].Id
	}
	var fallback guestPlacement
	for _, n := range w.Nas {
		if !usable(n) {
			continue
		}
		place := guestPlacement{NasID: n.Id, APMAC: portal.NormaliseMAC(*n.Mac), Site: n.SiteId}
		if n.SiteId == preferred {
			return place
		}
		if (fallback == guestPlacement{}) {
			fallback = place
		}
	}
	return fallback
}

// portalSkipReason is the condition under which the guest surface cannot be
// compared, or the empty string when it can.
//
// **It names a condition this harness evaluates on every run**, rather than a
// phase like "not implemented in Go — phase 4", which goes on being printed long
// after the implementation lands. A phase number cannot go stale loudly. This can:
// populate the fixtures with a controller-bound access point and the skip
// disappears by itself.
func portalSkipReason(w *memory.World) string {
	if (harnessGuestAP(w) == guestPlacement{}) {
		return "the oracle's world holds no enabled access point with a MAC behind a reachable " +
			"controller, so neither server can resolve a NAS redirect to a site — the placement " +
			"is what both look up first. Evaluated per run by `harnessGuestAP`, not asserted"
	}
	return ""
}

// harnessGuestDriver builds the driver seam the guest surface is compared over.
//
// See the file comment for why every value in it is read off something rather than
// chosen: capabilities off the shipped UniFi driver, presence refused by default,
// and one known client placed on the access point the oracle's own row names.
func harnessGuestDriver(place guestPlacement) *nas.MockDriver {
	return nas.NewStrictMockDriver(nas.VendorUniFi).
		WithClient(harnessGuestMAC, nas.MockClient{
			// The controller's own identifier for the client. UniFi's action
			// endpoint takes this and never the MAC, so a driver seam that lost
			// it would be one that could not authorize what it had just verified.
			ID: "harness-client-" + strings.ReplaceAll(harnessGuestMAC, ":", ""),
			// The access point the controller says this client is on. A step that
			// claims a different one is answered `PresenceAPMismatch`, which is
			// what makes this an input that *can* be wrong.
			APMAC: place.APMAC,
		})
}

// portalEntry is the NAS redirect, spelled the way UniFi spells it.
//
// `id` is the client MAC and `ap` the access point's; `ssid` and `url` are the
// other two unsigned values a controller appends. Both servers read them, and
// they are sent to both sides identically so that any difference in how they are
// read is left to the report rather than arranged away here.
//
// The query lands on `getPortalContext`, which is where a guest arrives. The
// operations after it carry no parameters at all — the contract gives them none —
// and are resolved from the cookie the entry set, which is the binding
// `docs/rules/security.md` §4 requires and the reason a MAC cannot arrive twice.
func portalEntry(path string, place guestPlacement) string {
	q := url.Values{}
	q.Set("id", harnessGuestMAC)
	q.Set("ap", place.APMAC)
	q.Set("ssid", harnessGuestSSID)
	q.Set("url", harnessGuestURL)
	return path + "?" + q.Encode()
}

// ── the guest flow, driven ───────────────────────────────────────────────────

// guestSteps is the scenario's guest half: one browser, in order, from the NAS
// redirect to a grant.
//
// **It is a flow rather than a set of independent requests**, because the guest
// surface is one: the contract gives four of the five operations no parameters at
// all, so everything after the entry is resolved from the cookie the entry set
// (`docs/rules/security.md` §4 — the MAC arrives once and is bound server-side).
// A step here that ran on its own would be a step with no session, which is a real
// branch and is driven deliberately below rather than by accident.
//
// The order is load-bearing twice over. `getPortalStatus` is asked **before** any
// grant exists and again after, because `404` and `200` are two different claims
// and a harness that only ever saw one of them would be comparing the easy half.
// And the operation that mutates the oracle's world most — a successful
// redemption — is driven late, for the same reason the denial steps are: a step
// that spends a voucher changes what every later step reads.
// portalRedirectHeaders is what is left of the difference every portal `303`
// carried, declared once and attached to each of them.
//
// **It covers `content-type` only.** A declaration that goes on describing a
// difference which has stopped happening is the same species as a skip whose
// reason has expired: it is printed on every run and it is no longer true, and the
// harness cannot tell the difference because it only checks that *something* under
// these keys still differs.
//
// `location` is not declared. A bare `type: string` on the contract's
// `PortalRedirect.Location` would let an absolute `${PORTAL_ORIGIN}/en/connected/`
// and a root-relative `/en/connected/` both conform, and nothing could say which
// was right. The contract gives it a pattern that refuses the absolute shape, and
// the mock's helper takes a “ `/${string}` “, so an absolute URL there is a
// compile error. Both servers answer root-relative.
//
// `content-type` is **neither server's choice**. It is not Hono's `c.redirect`: that sets no content
// type. `@hono/node-server`'s `buildOutgoingHttpHeaders` does
// `res["content-type"] ??= "text/plain; charset=UTF-8"` on every bodyless
// response, so it spans every `204` and `303` the oracle serves. The Go server
// sends none, the contract declares no body for this response, and a client must
// not read the header. An adapter artefact, declared so it cannot be mistaken for
// a decision.
var portalRedirectHeaders = divergence{
	Keys: []string{"content-type"},
	Reason: "the oracle's HTTP adapter — `@hono/node-server`, not Hono and not the handler — " +
		"defaults `Content-Type: text/plain; charset=UTF-8` on **every** bodyless response, " +
		"so it appears on each portal `303` and on every `204` beside them. The Go server " +
		"sends none. The contract declares no body for this response and no client may read " +
		"the header, so neither is wrong and neither is checkable. **`location` is not part " +
		"of this declaration**: `PortalRedirect.Location` carries a pattern that refuses an " +
		"absolute URL, and both servers answer root-relative. That matters more than a type: " +
		"a captive portal is reached over plain HTTP before the guest has a route anywhere, " +
		"so an absolute `Location` would pick a scheme and a host instead of inheriting them, " +
		"on the one screen where the guest has just typed a credential.",
}

func guestSteps(w *memory.World) []step {
	place := harnessGuestAP(w)
	if reason := portalSkipReason(w); reason != "" {
		return []step{
			{Op: "GetPortalContext", Skip: reason},
			{Op: "GetPortalStatus", Skip: reason},
			{Op: "RedeemVoucher", Skip: reason},
			{Op: "AcceptTerms", Skip: reason},
			{Op: "LoginWithAccount", Skip: reason},
		}
	}

	// The mock's own portal cookie is minted on the first request whatever the
	// query says, so the entry has to be first on both sides or the two are
	// comparing different browsers.
	steps := []step{
		{
			// **An access point neither server knows.**
			//
			// A well-formed entry cannot show this: the mock's default venue and the
			// site this guest's access point resolves to are the same one in this
			// fixture world, so the two answer identically. An access point neither
			// server has ever heard of does. Both resolve the AP MAC to a placement
			// and refuse when there is none, because a portal that fell back to a
			// default site for an unknown AP is a portal authorizing devices on
			// networks it does not manage.
			//
			// An oracle that minted a client from nothing and ignored `id`, `ap`,
			// `ssid` and `url` would answer this step `200` with a venue's name, its
			// languages and its terms — **it could not represent `VerifyPresence`
			// failing**, and no suite run against it could see the refusal. This
			// step is the one thing here that compares it.
			//
			// The address is in the IANA documentation range (RFC 7042 §2.1.2), so
			// it cannot collide with a fixture and cannot be a real radio.
			// `web/fixtures` exports it as `UNREGISTERED_AP_MAC` and `web/mock`'s
			// own tests assert no device row carries it.
			Op: "GetPortalContext", Method: http.MethodGet,
			Path:  portalEntry("/portal/context", guestPlacement{APMAC: "00:00:5e:00:53:ff"}),
			Guest: true,
		},
		{
			// The real entry, at an access point the oracle's own world holds. From
			// here the guest has a session on both sides and every later operation
			// is resolved from the cookie — the contract gives them no parameters.
			Op: "GetPortalContext", Method: http.MethodGet,
			Path: portalEntry("/portal/context", place), Guest: true,
		},
		{
			Op: "GetPortalStatus", Method: http.MethodGet, Path: "/portal/status", Guest: true,
			// No grant yet. Both must answer `404 /problems/no-grant`, and the
			// step exists because the operation's other answer is the one a
			// harness naturally drives — a status read after a success proves
			// nothing about the read before one.
		},
		{
			Op: "RedeemVoucher", Method: http.MethodPost, Path: "/portal/voucher",
			Form: true, Guest: true,
			Body:     map[string]string{"code": "ZZZZZZZZZZZZ", "lang": "en"},
			Declared: []divergence{portalRedirectHeaders},
			// A code no batch could have generated. Both answer `303` back to the
			// voucher page with `e=not-found` and the submitted code echoed — the
			// echo is the whole reason the failure is a navigation and not a
			// problem document, and it is compared here rather than assumed. The
			// echo is compared: `code=ZZZZZZZZZZZZ` has to survive on both sides,
			// normalised and capped, or a guest retypes twelve characters.
		},
	}

	if code, ok := unusedVoucherCodeElsewhere(w, place.Site); ok {
		steps = append(steps, step{
			Op: "RedeemVoucher", Method: http.MethodPost, Path: "/portal/voucher",
			Form: true, Guest: true,
			Body:     map[string]string{"code": code, "lang": "en"},
			Declared: []divergence{portalRedirectHeaders},
			// **A real code, sold by the other venue.** A `redeemVoucher` that
			// searched every voucher unscoped would let the café's portal grant the
			// hotel's room key, and a declaration that describes such a defect is a
			// poor substitute for a step that fails on it.
			//
			// It is drivable because both sides agree: a voucher whose batch
			// belongs to another venue is reported **unknown**, never as *wrong
			// site*, so this compares `e=not-found` and the echo exactly as the
			// `ZZZZZZZZZZZZ` step above does. The difference between the two is the
			// whole point — that code exists nowhere, this one exists and is
			// claimable, one venue over. A server that resolves a voucher without
			// its batch answers `/connected/` here and `e=not-found` there.
			//
			// It spends nothing while both sides refuse, so it is safe in front of
			// the redemption below. If either side ever stops refusing it, that run
			// is red anyway.
		})
	} else {
		steps = append(steps, step{
			Op: "RedeemVoucher",
			Skip: "every voucher in state `unused` in this world belongs to a batch in the " +
				"site this guest is standing in, so there is no other venue's code to submit " +
				"and a cross-venue refusal cannot be driven. Evaluated per run by " +
				"`unusedVoucherCodeElsewhere` — and it is the fixture world's fault, not the " +
				"product's: `docs/rules/frontend.md` requires a second venue that is " +
				"populated rather than merely present",
		})
	}

	if code, ok := unusedVoucherCode(w, place.Site); ok {
		steps = append(steps, step{
			Op: "RedeemVoucher", Method: http.MethodPost, Path: "/portal/voucher",
			Form: true, Guest: true,
			Body:     map[string]string{"code": code, "lang": "en"},
			Declared: []divergence{portalRedirectHeaders},
		})
	} else {
		steps = append(steps, step{
			Op: "RedeemVoucher",
			Skip: "no voucher in state `unused` belongs to a batch in the site this guest is " +
				"standing in, so a redemption could only be driven against a code both servers " +
				"already refuse — which the `not-found` step above already compares. Evaluated " +
				"per run by `unusedVoucherCode`",
		})
	}

	steps = append(steps,
		step{
			Op: "GetPortalStatus", Method: http.MethodGet, Path: "/portal/status", Guest: true,
			// The same read, after the redemption. `grantedAt` and `expiresAt` are
			// wall-clock values two processes mint independently.
			Volatile: []string{"grantedAt", "expiresAt"},
			// `originalUrl` is compared, not declared. Both servers answer the `url`
			// the NAS redirect actually carried — allowlisted on the way in and again
			// on the way out. An oracle that answered a constant of its own invention
			// here would make the one value on this screen that comes from the
			// guest's own browsing a fixture constant, **whose allowlist could never
			// refuse anything.** Both servers carry `harnessGuestURL` through, so this
			// step compares the value.
		},
		step{
			Op: "AcceptTerms", Method: http.MethodPost, Path: "/portal/agree",
			Form: true, Guest: true, Body: map[string]string{"lang": "en"},
			Declared: []divergence{portalRedirectHeaders},
			// **A property this step cannot show, recorded where it will be read.**
			// Whether click-through is offered, and for how many minutes, must be
			// answered from the guest's own placement — a handler that read a literal
			// site would answer from a site the guest may not be in.
			//
			// It is a comment and **not a declaration**, because the guest here
			// stands in `site-hcm`: the two agree either way, and a declaration that
			// never fires is one this harness fails on and should. It becomes
			// drivable here the day `harnessGuestAP` can be made to pick a device in
			// the other populated site. Until then the two venues are compared in
			// `web/mock/src/portal-nas-claim.test.ts`, where a hotel guest granted
			// the café's 30 minutes instead of the hotel's 45 is red.
		},
	)

	if username, ok := enabledAccount(w, place.Site); ok {
		steps = append(steps, step{
			Op: "LoginWithAccount", Method: http.MethodPost, Path: "/portal/account",
			Form: true, Guest: true,
			Body: map[string]string{
				"username": username,
				// Deliberately not a password anything could accept. No fixture
				// account carries a credential — §7 forbids one in this
				// repository — so **there is no string that authenticates here**,
				// and the interesting question is what each server does with that.
				//
				// A `loginWithAccount` that accepted any non-empty password, or
				// searched accounts unscoped, would let a café's portal sign in a
				// hotel's guest — reachable by anyone within radio range. Both
				// servers answer `303` to the account page with
				// `e=bad-credentials`, so what is left on this step is the redirect
				// spelling every portal `303` carries.
				//
				// The success half is not drivable here and deliberately is not
				// invented: a password that authenticated on both sides would have
				// to be a static credential in this repository, which §7 forbids.
				// `web/mock` verifies against one it generates per process, and
				// `web/mock/src/portal-nas-claim.test.ts` drives both outcomes and
				// both venues against it.
				"password": "this-is-not-the-password",
				"lang":     "en",
			},
			Declared: []divergence{portalRedirectHeaders},
		})
	} else {
		steps = append(steps, step{
			Op: "LoginWithAccount",
			Skip: "no enabled account belongs to the site this guest is standing in, so the " +
				"credential path could only be driven against an unknown username — which is " +
				"the same answer as a wrong password by design and would compare nothing extra. " +
				"Evaluated per run by `enabledAccount`",
		})
	}
	return steps
}

// unusedVoucherCode finds a redeemable code in one site, and
// unusedVoucherCodeElsewhere finds one in any other.
//
// By property, never by id, and **resolved through the batch**, because that is
// where a voucher's venue lives: there is no `site_id` on a code, and both servers
// answer this question by joining. `internal/api/memory`'s `ClaimVoucher` refuses a
// voucher whose batch belongs to another venue, and `store.GrantRepo.ClaimVoucher`
// carries `AND b.site_id = $1` in the one UPDATE that claims it.
//
// The two are one pair and are driven as one, in the order they are declared: the
// stranger's code first, refused, then the guest's own, granted. Splitting them was
// this file's declared blind spot — it said in as many words that a cross-site code
// would produce a divergence rather than a redemption, and so never sent one, which
// is how `web/mock`'s `redeemVoucher` went on searching `world.vouchers` unscoped
// while `make diff-api` reported agreement on this operation.
func unusedVoucherCode(w *memory.World, siteID string) (string, bool) {
	return unusedVoucherCodeWhere(w, func(in string) bool { return in == siteID })
}

func unusedVoucherCodeElsewhere(w *memory.World, siteID string) (string, bool) {
	return unusedVoucherCodeWhere(w, func(in string) bool { return in != "" && in != siteID })
}

func unusedVoucherCodeWhere(w *memory.World, want func(siteID string) bool) (string, bool) {
	site := map[string]string{}
	for _, b := range w.Batches {
		site[b.Id] = b.SiteId
	}
	for _, v := range w.Vouchers {
		if v.State == gen.VoucherStateUnused && v.Code != "" && want(site[v.BatchId]) {
			return v.Code, true
		}
	}
	return "", false
}

// enabledAccount finds a local account in one site, for the credential path.
func enabledAccount(w *memory.World, siteID string) (string, bool) {
	for _, a := range w.Accounts {
		if a.Enabled && a.SiteId == siteID && a.Username != "" {
			return a.Username, true
		}
	}
	return "", false
}
