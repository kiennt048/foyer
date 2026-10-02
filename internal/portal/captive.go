package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// The RFC 8908 Captive Portal API, answering for the device that asked.
//
// ── what it answered before ──────────────────────────────────────────────────
//
// `RFC8908Handler(true, …)`: `captive: true` to every caller, forever — including
// every device that had just redeemed a voucher. An operating system that honours
// the API and believes it keeps treating a guest who is online as a guest who is
// not. The field is optional to implement and wrong to
// implement as a constant.
//
// ── how the device is found ──────────────────────────────────────────────────
//
//  1. **The portal cookie, through `Lookup`** — the same resolution every other
//     reader in this package uses, so a browser that holds a session is answered
//     from its own grant and nothing else.
//  2. **The request's source address, through the `AddressBook`.** The operating
//     system's API client is not the portal's WebView and carries no cookie; RFC
//     8908 §4 expects the server to identify it by address. Only the transport
//     source is read, never a forwarding header. An address that has meant more
//     than one MAC is answered as unknown.
//
// Neither finds anything → `captive: true`, which is also the answer for a grant
// that is expired, revoked, or whose network was never opened
// (`StateNotOnNetwork`): in all three the device is, in fact, captive.
//
// ── what is and is not in the answer ─────────────────────────────────────────
//
// `seconds-remaining` when the grant has an end, because that number exists and
// is exact. **`bytes-remaining` is never sent.** The portal holds the plan's
// allowance and not what was used — usage lives in `radacct`, arrives every five
// minutes at best, and on the controller-API path may never arrive at all — so any
// figure here would be the allowance presented as a remainder, which is the lie
// `Plan.EnforcedBy` exists to prevent. `can-extend-session` is never sent:
// there is no extension flow, and Android does not parse it
// (`docs/protocol-reality.md`).
//
// ── RFC 8908 requires HTTPS, and this handler cannot supply it ───────────────
//
// §4: the API "MUST be accessed … using an https URI", and a client MUST NOT
// proceed on a certificate it cannot validate. The listener decides the scheme,
// not this handler. On a deployment with no portal certificate the endpoint still
// answers — the portal's own pages and a test can read it — but an operating
// system is expected to ignore an `http://` API URI, and Android is documented to
// fall back silently. `docs/portal-https.md` states the limitation to the
// operator.

// CapportData represents RFC 8908 Captive Portal JSON payload.
type CapportData struct {
	Captive          bool   `json:"captive"`
	UserPortalURL    string `json:"user-portal-url,omitempty"`
	VenueInfoURL     string `json:"venue-info-url,omitempty"`
	SecondsRemaining *int   `json:"seconds-remaining,omitempty"`
	BytesRemaining   *int   `json:"bytes-remaining,omitempty"`
	CanExtendSession bool   `json:"can-extend-session,omitempty"`
}

// CaptiveHandler is `GET /api/v1/captive-portal` on the portal listeners.
//
// portalURL is `user-portal-url` and venueURL is `venue-info-url`; an empty one
// is omitted rather than sent empty, because ChromeOS rejects the whole document
// over one malformed URL member.
func (s *Service) CaptiveHandler(portalURL, venueURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := CapportData{
			Captive:       true,
			UserPortalURL: portalURL,
			VenueInfoURL:  venueURL,
		}
		now := s.now()
		if g, ok := s.captiveGrant(r.Context(), r); ok && g.StateAt(now) == StateConnected {
			data.Captive = false
			if g.ExpiresAt != nil {
				secs := int(g.ExpiresAt.Sub(now) / time.Second)
				data.SecondsRemaining = &secs
			}
		}

		// `no-store`: RFC 8908 asks for `private` or stricter, and the answer is
		// about one device at one moment.
		w.Header().Set("Content-Type", MediaTypeCaptiveJSON)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(data)
	}
}

// captiveGrant finds the grant held by the device that sent r. See CaptiveHandler.
func (s *Service) captiveGrant(ctx context.Context, r *http.Request) (Grant, bool) {
	if sess, err := s.Lookup(ctx, r); err == nil {
		if sess.GrantID == "" {
			return Grant{}, false
		}
		g, err := s.ports.Grants.Grant(ctx, sess.GrantID)
		return g, err == nil
	}

	addr, ok := ClientAddress(r)
	if !ok {
		return Grant{}, false
	}
	now := s.now()
	entry, ok := s.ports.Addresses.Lookup(addr, now)
	if !ok || entry.Ambiguous {
		return Grant{}, false
	}
	grantID := entry.GrantID
	if grantID == "" {
		// Granted after the last observation — the redemption's own request
		// resolved the session before the grant existed. The session row still
		// knows, for as long as it lives.
		sess, err := s.ports.Sessions.Session(ctx, entry.TokenHash)
		if err != nil || sess.GrantID == "" {
			return Grant{}, false
		}
		grantID = sess.GrantID
		s.ports.Addresses.AttachGrant(entry.TokenHash, grantID, now)
	}
	g, err := s.ports.Grants.Grant(ctx, grantID)
	if err != nil {
		return Grant{}, false
	}
	// The grant must be for the MAC this address was bound to. It always is by
	// construction; checked because an address answering for a grant issued to
	// a different device is the one outcome this endpoint must never produce.
	if NormaliseMAC(g.ClientMAC) != NormaliseMAC(entry.ClientMAC) {
		return Grant{}, false
	}
	return g, true
}
