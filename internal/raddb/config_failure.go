package raddb

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kiennt048/foyer/internal/aaa"
)

// The per-site failure model, as far as the generator can carry it.
//
// `internal/aaa/failure.go` builds the model and says why it cannot be applied at
// request time: by the time the question is asked, the Policy API is the thing that
// is down, so anything it would have said is unavailable. The behaviour has to be
// pre-positioned in FreeRADIUS's own configuration -- which makes it generated, and
// makes changing a site's failure mode a `restart`-class change.
//
// ── what the field survey found, and where this diverges ──────────────────────
//
// **Nobody in this industry ships per-site fail-open in the RADIUS policy.**
//
//   - PacketFence does not call `rest` in `authorize` at all; the decision is made
//     in `post-auth` and branches on an *attribute*, not on the module rcode. Its
//     `rest` module sets no action overrides, so `fail` and `invalid` keep
//     MOD_ACTION_RETURN, the section returns before the branch is reached, and
//     `rad_postauth()` turns RLM_MODULE_FAIL into an Access-Reject. The `:-Allow`
//     default in its condition looks like fail-open and is not: it covers an API
//     that answered without the attribute, never an API that did not answer.
//     PacketFence buys availability with a Galera cluster, not with a knob.
//   - authentik's RADIUS outpost rejects on every error path, with no flag.
//   - ClearPass's degraded mode is a read-only local replica that goes on
//     answering; there is no "permit while blind" setting.
//   - daloRADIUS is not in the request path and has no opinion.
//   - Cisco ISE is the only per-scope knob, and it is per policy *rule* and about
//     an unreachable identity store rather than an unreachable control plane.
//
// **The industry's actual answer lives on the NAS**, not on the RADIUS server:
// Cisco's Inaccessible Authentication Bypass (`authentication event server dead
// action authorize vlan N`) and Aruba's `critical-role` / `cached-critical-role`.
// The reason is sound and worth stating in place: the case that matters most is the
// RADIUS server being unreachable, and a server that is unreachable cannot express
// a policy at all.
//
// So Foyer diverges, and the divergence is deliberate and narrow. What this covers
// is the failure mode Foyer's own architecture creates -- the Go control plane down
// while FreeRADIUS is up and answering -- and nothing wider. A NAS that cannot
// reach Foyer at all is answered by critical-VLAN on the NAS or not at all, and
// this setting must never be described as if it covered that.
//
// The divergence is worth taking because Foyer's target venues are hospitality
// and retail, where a lobby full of guests who cannot connect is the loss; PacketFence's
// centre of gravity is enterprise NAC, where closed is right. That is a difference
// in deployment, not a disagreement about the protocol -- which is why the setting
// is per site rather than per install, and why 802.1X is excluded from it whatever
// the site says.
//
// ── the supply problem, and the column that solves it ──────────────────────────
//
// The survey names the two idiomatic FreeRADIUS supplies for a value that has to
// survive the outage: `%{client:<attr>}` from `clients.conf`, and `rlm_cache`.
// **Neither is available here.** `read_clients = yes` means clients come from SQL,
// `rlm_sql` reads exactly six columns, and a custom per-client attribute has
// nowhere to live -- the same six-column wall that made
// `require_message_authenticator` a global rather than a per-client setting
// (`docs/rules/security.md` §3). `rlm_cache` serves a last good answer, which is
// a different feature.
//
// **There is a third supply the survey did not name, and it is measured, not
// reasoned.** The sixth column *is* `server`, and FreeRADIUS maps it to the
// client's virtual server. Measured on 3.2.10, aarch64:
//
//	rlm_sql (sql): Adding client 127.0.0.1 (labclient) to SERVERCOL clients list
//	No 'listen' section in virtual server SERVERCOL. Adding client to global client list
//	...
//	Reply-Message = "SERVER-FROM-SQL-COLUMN"
//
// So a per-client route to a per-advice virtual server is expressible, it survives
// the outage because it is baked into the client list at startup, and it is
// compatible with SQL clients. That is the mechanism `lab/policy/README.md` records
// for the generator, and it holds.
//
// ── why this file does not build that, yet ─────────────────────────────────────
//
// Measured in the same run, and it is the reason routing is not wired here:
//
//	$ radiusd -XC        # nas.server names a virtual server the tree does not have
//	rlm_sql (sql): Adding client 127.0.0.1 (labclient) to DOES_NOT_EXIST clients list
//	Cannot add client - virtual server DOES_NOT_EXIST does not exist
//	Configuration appears to be OK          <-- gate 1 passes
//	$ radclient ...
//	(0) No reply from server for ID 82
//
// **A `nas.server` value with no matching generated server silently drops the
// client.** Not a refusal to start, not a non-zero `-XC`: the server comes up, says
// "Configuration appears to be OK", and every packet from that NAS goes unanswered
// forever. Blue/green gate 1 cannot see it, and gates 2 and 3 cannot either --
// they probe with the harness's own client, not the operator's.
//
// The two writers involved are `internal/raddb` (generates the tree, restart-class)
// and `internal/store`'s `nassync` (writes `nas.server`, sql-only, no restart), in
// different processes at different times with no ordering guarantee between them.
// Writing the projection before the tree that carries the server it names is a
// total, silent outage for that NAS, in the window between them.
//
// That invariant belongs to both packages, so this file builds the half that has
// no cross-package hazard: **the advice set must
// collapse to one value, and it is rendered into `default`, where every client
// already lands with no routing at all.** A set that does not collapse is refused
// by name at Validate -- before the blue/green apply writes anything -- rather than
// generated into a tree where the fail-open sites would quietly fail closed.
//
// `nassync` already carries `server` through its diff, insert and update paths and
// pins it to `""`. Populating it is the other half, and it needs the ordering
// invariant above with it.

// resolvedGuestFailure is the single advice the generated tree expresses.
type resolvedGuestFailure struct {
	adv        aaa.Advice
	configured bool
}

// guestFailureKey is what makes two sites' advice the same advice.
//
// SiteID is excluded by construction: it differs for every site and is not a
// property of the *behaviour*. GraceAttributes is excluded because it is derived
// from the other three by `aaa` and cannot disagree with them.
func guestFailureKey(a aaa.Advice) string {
	return fmt.Sprintf("dot1x=%s guest=%s grace=%d", a.Dot1X, a.Guest, a.GraceSeconds)
}

// ResolveGuestFailure collapses GuestFailureAdvice to the one value the generated
// tree can carry, or names why it cannot.
//
// An empty set is not an error and is not "unconfigured means open": it resolves to
// aaa.DefaultPolicy, which is fail-closed, and is exactly the tree this generator
// emitted before the field existed.
func (c Config) ResolveGuestFailure() (aaa.Advice, bool, error) {
	if len(c.GuestFailureAdvice) == 0 {
		return aaa.DefaultPolicy("").Advice(c.DefaultInterimIntervalSeconds), false, nil
	}

	byKey := map[string][]string{}
	for _, a := range c.GuestFailureAdvice {
		if !a.Guest.Valid() || !a.Dot1X.Valid() {
			return aaa.Advice{}, true, fmt.Errorf(
				"raddb: site %q carries failure modes dot1x=%q guest=%q; neither is a mode and a mode this generator cannot read is not silently closed here, because a caller that produced one has a defect the tree would hide",
				a.SiteID, a.Dot1X, a.Guest)
		}
		if a.Dot1X != aaa.FailClosed {
			return aaa.Advice{}, true, fmt.Errorf(
				"raddb: site %q asks for dot1x=%q; 802.1X is fail-closed and is not the operator's to change (internal/aaa/failure.go)",
				a.SiteID, a.Dot1X)
		}
		byKey[guestFailureKey(a)] = append(byKey[guestFailureKey(a)], a.SiteID)
	}

	if len(byKey) > 1 {
		return aaa.Advice{}, true, fmt.Errorf(
			"raddb: %s. Routing a client to a per-advice virtual server is the mechanism for this and it is not built: "+
				"it needs `nas.server` populated by internal/store, and a measured ordering invariant with it -- a nas.server "+
				"value naming a server the generated tree does not have makes radiusd start, pass `radiusd -XC`, and answer "+
				"nothing at all from that NAS. Until that lands, every site must agree; this refusal is at Validate, so the "+
				"running configuration is untouched",
			describeGuestFailureSplit(byKey))
	}

	return c.GuestFailureAdvice[0], true, nil
}

// describeGuestFailureSplit names both sides, because "the sites disagree" is not
// something an operator can act on and "these three want fail-open" is.
func describeGuestFailureSplit(byKey map[string][]string) string {
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		sites := append([]string(nil), byKey[k]...)
		sort.Strings(sites)
		parts = append(parts, fmt.Sprintf("[%s] for site(s) %s", k, strings.Join(sites, ", ")))
	}
	return "the sites do not agree on a failure model: " + strings.Join(parts, "; ")
}

// guestFailure is the template's accessor. Validate has already refused a set that
// does not collapse, so this cannot fail by the time a template runs.
func (c Config) guestFailure() resolvedGuestFailure {
	adv, configured, err := c.ResolveGuestFailure()
	if err != nil {
		// Unreachable through Generate, which calls Validate first. Fail closed
		// rather than panicking: a wrong answer here is an Access-Accept.
		return resolvedGuestFailure{adv: aaa.DefaultPolicy("").Advice(c.DefaultInterimIntervalSeconds)}
	}
	return resolvedGuestFailure{adv: adv, configured: configured}
}

// GuestFailsOpen reports whether the generated guest failure branch accepts.
func (c Config) GuestFailsOpen() bool { return c.guestFailure().adv.Guest == aaa.FailOpen }

// GuestFailureConfigured distinguishes "every site is fail-closed" from "nothing is
// configured, so every site is fail-closed". They render identically and are not
// the same fact to an operator reading a generated config.
func (c Config) GuestFailureConfigured() bool { return c.guestFailure().configured }

// GuestGraceSeconds is the bounded lifetime of a fail-open grant.
func (c Config) GuestGraceSeconds() int { return c.guestFailure().adv.GraceSeconds }

// GuestGraceAttributes are the reply attributes a fail-open Access-Accept carries,
// as unlang assignment lines, already sorted.
//
// Derived by calling `aaa` with this generator's own interim interval rather than
// reading the map the Advice arrived with. The map is computed from an interval the
// caller chose, and the generated tree re-asserts Acct-Interim-Interval a few lines
// later from DefaultInterimIntervalSeconds -- so trusting the carried map is how a
// config comes to state two different intervals for one reply. Asking the authority
// with the value that will actually be emitted is the only form of this that cannot
// disagree with itself.
func (c Config) GuestGraceAttributes() []string {
	adv := c.guestFailure().adv
	if adv.Guest != aaa.FailOpen {
		return nil
	}
	p := aaa.SiteFailurePolicy{GuestFailureMode: adv.Guest, GuestGraceSeconds: adv.GraceSeconds}
	attrs := p.GraceAttributes(c.DefaultInterimIntervalSeconds)

	names := make([]string, 0, len(attrs))
	for k := range attrs {
		names = append(names, k)
	}
	sort.Strings(names)

	out := make([]string, 0, len(names))
	for _, n := range names {
		// Every value is quoted, including the numeric ones. FreeRADIUS parses a
		// quoted string into an integer attribute, and quoting uniformly keeps
		// this loop from having to know which attributes are integers -- which
		// would be a second, drifting copy of the dictionary.
		out = append(out, fmt.Sprintf("%s := %q", n, attrs[n]))
	}
	return out
}

// GuestFailureSummary is the line the generated config carries so an operator can
// read which branch they are getting without inferring it from unlang.
func (c Config) GuestFailureSummary() string {
	r := c.guestFailure()
	source := "no site configuration was supplied, so this is aaa.DefaultPolicy"
	if r.configured {
		source = fmt.Sprintf("resolved from %d configured site(s), which agree", len(c.GuestFailureAdvice))
	}
	if r.adv.Guest == aaa.FailOpen {
		return fmt.Sprintf("guest: FAIL-OPEN for %ds; 802.1X: fail-closed (not configurable) -- %s", r.adv.GraceSeconds, source)
	}
	return fmt.Sprintf("guest: fail-closed; 802.1X: fail-closed (not configurable) -- %s", source)
}
