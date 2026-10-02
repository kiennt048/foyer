package portal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// The portal's front door, and the only place a MAC is ever learned.
//
// ── the shape of the problem ─────────────────────────────────────────────────
//
// The contract's five portal operations carry **no parameters**. `getPortalContext`
// and `getPortalStatus` declare none at all; the three authenticating operations
// declare a form body of `code`, `username`, `password` and `lang` and nothing
// else. There is therefore no operation in the document through which a client
// MAC can arrive, and that is correct rather than an omission: the MAC arrives
// once, from the NAS, on the redirect that brought the guest here, and is bound
// to the browser server-side from then on (`docs/rules/security.md` §4).
//
// The route that receives that redirect is not in the contract either, because it
// is not an API — it is the document the access point sends a phone to. It is
// `GET /` on the portal listener — plus `GET /guest/...`, because UniFi appends
// `/guest/s/<site>/` to the bare IP its External Portal field takes — and this
// file is what it does.
//
// ── what the NAS actually sends ──────────────────────────────────────────────
//
// UniFi appends `ap`, `id`, `t`, `url` and `ssid`. Every one of them is an
// unsigned query parameter and anybody within radio range can call this URL with
// values of their choosing, so nothing here treats them as facts: they are a
// *claim* that is recorded and then put to the driver before any grant exists.
// Other vendors spell them differently, which is a driver concern and is why the
// names are in one place here rather than spread through handlers.

const (
	// SessionCookieName is the portal's own, and it is deliberately not the
	// console's.
	//
	// The console's cookie carries the `__Host-` prefix, which the user agent
	// enforces: `Secure`, `Path=/`, no `Domain`. That is what stops this
	// listener — reachable pre-authentication by anyone within radio range —
	// from setting a cookie the console will read. The portal's own cannot carry
	// the prefix, because the prefix mandates `Secure` and a captive portal is
	// frequently reached over plain HTTP before the guest has a route to
	// anywhere. `Secure` is therefore set from the connection rather than
	// unconditionally; see `setSessionCookie`.
	SessionCookieName = "foyer_portal_session"

	// paramClientMAC is UniFi's `id`. `mac` is accepted too because more than
	// one vendor spells it that way and a portal that refused would be refusing
	// a guest for a vendor's naming.
	paramClientMAC    = "id"
	paramClientMACAlt = "mac"
	paramAPMAC        = "ap"
	paramSSID         = "ssid"
	paramOriginalURL  = "url"
)

// ErrNoClaim means this request carries no session and no NAS parameters.
//
// It is not an internal error: it is a browser that reached the portal without
// being sent there, which happens every time somebody types the address by hand.
// The caller answers `403` — there is no MAC, so there is nothing the driver
// could confirm.
var ErrNoClaim = errors.New("portal: no session and no NAS redirect parameters")

// Resolve returns the session this request belongs to.
//
// One implementation for both callers — the front door and the API middleware —
// because a second one is a second place for the MAC binding to be got wrong.
//
// ── the mid-flow MAC change, stated narrowly ─────────────────────────────────
//
// §4 says to reject one. What is rejected is precisely this: **an existing
// binding is never re-pointed at a different MAC.** A request that carries a
// session for `aa:…` and NAS parameters naming `bb:…` does not update the row; the
// old session is forgotten and a new one is minted for `bb:…`, which then has to
// pass `VerifyPresence` on its own like any other.
//
// The narrow reading is the true one here, and the wide one would be a claim this
// cannot support: nothing stops an attacker deleting their own cookie and
// starting again, so this is not a barrier to *trying* a second MAC. What it
// prevents is a grant issued after a presence check on one address being carried
// over to another — the only thing in this area that would actually be a
// vulnerability.
func (s *Service) Resolve(ctx context.Context, w http.ResponseWriter, r *http.Request) (Session, error) {
	claimed := claimFrom(r)

	if token := cookieToken(r); token != "" {
		sess, err := s.ports.Sessions.Session(ctx, HashToken(token))
		switch {
		case err == nil && (claimed.ClientMAC == "" || claimed.ClientMAC == sess.ClientMAC):
			sess.Source = r.RemoteAddr
			s.observe(r, sess)
			return sess, nil
		case err == nil:
			// A different MAC. Forget rather than re-point.
			_ = s.ports.Sessions.ForgetSession(ctx, sess.TokenHash)
		}
	}

	if claimed.ClientMAC == "" {
		return Session{}, ErrNoClaim
	}
	sess, err := s.mint(ctx, w, r, claimed)
	if err == nil {
		sess.Source = r.RemoteAddr
		s.observe(r, sess)
	}
	return sess, err
}

// Lookup returns the session this request's cookie names, without minting one.
// Resolve mints where a claim answers; document renders must not — a row for
// every prefetch, preview, forged link and crawler would fill the session
// table with browsers nobody sent. Cookie match only: a claim without a cookie
// is a stranger the front door has not met, and a cookie naming another MAC's
// row resolves to nothing rather than re-pointing — the same refusal Resolve
// applies, minus the mint that would follow it.
func (s *Service) Lookup(ctx context.Context, r *http.Request) (Session, error) {
	token := cookieToken(r)
	if token == "" {
		return Session{}, ErrNoClaim
	}
	sess, err := s.ports.Sessions.Session(ctx, HashToken(token))
	if err != nil {
		return Session{}, err
	}
	if claimed := claimFrom(r); claimed.ClientMAC != "" && claimed.ClientMAC != sess.ClientMAC {
		return Session{}, ErrNoClaim
	}
	sess.Source = r.RemoteAddr
	s.observe(r, sess)
	return sess, nil
}

// observe records that the browser at this request's source address holds this
// session — the one fact the RFC 8908 endpoint needs and cannot get from a
// cookie. Only ever called with a session that resolved from the cookie or was
// just minted; see address.go for why that is the whole of the trust it carries.
func (s *Service) observe(r *http.Request, sess Session) {
	addr, ok := ClientAddress(r)
	if !ok {
		return
	}
	s.ports.Addresses.Observe(addr, sess.TokenHash, sess.ClientMAC, sess.GrantID, s.now())
}

// claim is what the NAS said, before anybody has checked any of it.
type claim struct {
	ClientMAC   string
	APMAC       string
	SSID        string
	OriginalURL string
}

func claimFrom(r *http.Request) claim {
	q := r.URL.Query()
	mac := NormaliseMAC(q.Get(paramClientMAC))
	if mac == "" {
		mac = NormaliseMAC(q.Get(paramClientMACAlt))
	}
	return claim{
		ClientMAC: mac,
		APMAC:     NormaliseMAC(q.Get(paramAPMAC)),
		// The SSID is venue text that reaches a rendered page, so it is length
		// capped here and escaped there. Neither alone is enough.
		SSID: truncate(q.Get(paramSSID), 64),
		// Allowlisted on the way *in*, not only on the way out. A value that
		// never enters the store in a dangerous shape cannot be rendered in one
		// by a future reader who forgets to check.
		OriginalURL: SafeReturnURL(q.Get(paramOriginalURL)),
	}
}

func (s *Service) mint(ctx context.Context, w http.ResponseWriter, r *http.Request, c claim) (Session, error) {
	place, err := s.ports.Sites.Placement(ctx, c.APMAC)
	if err != nil {
		// An access point nobody registered. Refused rather than defaulted: a
		// portal that fell back to some site for an AP it has never heard of is
		// a portal authorizing devices on networks it does not manage.
		return Session{}, err
	}

	token, hash, err := newSessionToken()
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	sess := Session{
		TokenHash: hash,
		SiteID:    place.SiteID,
		NASID:     place.NASID,
		// The driver takes the controller's own site identifier, never Foyer's.
		// Conflating the two is how a lookup lands in the wrong place on a
		// controller that manages more than one of the vendor's sites.
		VendorSiteID: place.VendorSiteID,
		// And which controller to ask. Resolved here, with the rest of the
		// placement and from the same row, so that Verify and Authorize cannot
		// end up talking to two different controllers about one device.
		ControllerID: place.ControllerID,
		Accounting:   place.Accounting,
		ClientMAC:    c.ClientMAC,
		APMAC:        c.APMAC,
		SSID:         c.SSID,
		OriginalURL:  c.OriginalURL,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.sessionLifetime),
	}

	if err := s.ports.Sessions.CreateSession(ctx, sess); err != nil {
		return Session{}, err
	}
	setSessionCookie(w, r, token)
	return sess, nil
}

// EntryHandler is `GET /` on the portal listener: the URL the access point sends
// a guest's browser to.
//
// It does three things and deliberately not a fourth. It negotiates a language,
// it binds the browser to the MAC the NAS named, and it redirects to a document.
// **It does not call the driver**, because the Captive Network Assistant probes
// this address repeatedly while a guest reads a page and a controller round trip
// per probe is a denial of service the venue pays for. The driver is called by
// `getPortalContext`, once, when the portal actually needs an answer.
//
// The query string is relayed onto the redirect exactly as it arrived. That is
// transport rather than trust — the same relay the language links do — and it is
// what keeps a portal document able to render before the session cookie has been
// round-tripped.
func (s *Service) EntryHandler(available []string, defaultLang string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A session may not be establishable: no NAS parameters, an unknown
		// access point, a store that is down. None of those is a reason to
		// refuse a document — the welcome page renders and `getPortalContext`
		// is where the guest is told. Refusing here would answer a bare `403`
		// to somebody who typed the venue's address into a browser.
		_, _ = s.Resolve(r.Context(), w, r)

		lang := NegotiateLanguage(r.Header.Get("Accept-Language"), available, defaultLang)
		target := "/" + lang + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// ── cookies and tokens ───────────────────────────────────────────────────────

func cookieToken(r *http.Request) string {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// setSessionCookie writes the portal's session cookie.
//
// **No `Max-Age` and no `Expires`.** The Captive Network Assistant's data store
// is ephemeral and destroys its cookies when the window closes regardless of what
// is asked for, so a persistence attribute here would be a promise to a client
// that cannot keep it — and binding the portal session to the browser session is
// the behaviour that is wanted anyway. The server-side row has its own bound,
// which is the one that is enforced.
//
// `Secure` follows the connection. A captive portal is regularly reached over
// plain HTTP — the guest has no route to a certificate authority and, on iOS,
// whether the CNA will accept a self-signed certificate at all is an open
// question (`docs/portal-https.md`). Setting `Secure` unconditionally would produce a portal that
// works in a browser and silently drops every cookie on a phone, which is the
// failure mode this project is least able to see.
func setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// newSessionToken returns the bearer value and the hash that gets stored.
//
// `crypto/rand`, and the write-time hook refuses the predictable alternative
// anywhere under this package: this token names a MAC binding, so a guessable one
// is somebody else's portal session.
func newSessionToken() (value, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	value = base64.RawURLEncoding.EncodeToString(buf)
	return value, HashToken(value), nil
}

// HashToken is what the store holds, so that a database dump contains no live
// portal sessions.
func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newGrantID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "grant_" + hex.EncodeToString(buf), nil
}

// ── normalisation ────────────────────────────────────────────────────────────

// NormaliseMAC returns the lower-case, colon-separated form, or "".
//
// Everything that is not a hex digit is dropped first, so `AA-BB-CC-DD-EE-FF`,
// `aabb.ccdd.eeff` and `AABBCCDDEEFF` are one address — vendors spell it all
// three ways in the same product. An input that does not yield exactly twelve hex
// digits is not an address and becomes the empty string rather than a partial
// one: a truncated MAC that still looks like a MAC is a lookup against the wrong
// device.
func NormaliseMAC(raw string) string {
	var hexes strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			hexes.WriteRune(r)
		}
	}
	h := hexes.String()
	if len(h) != 12 {
		return ""
	}
	var out strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			out.WriteByte(':')
		}
		out.WriteString(h[i : i+2])
	}
	return out.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// FormLanguage reads the language a form submitted. Present so that every reader
// of a `lang` field goes through the allowlist rather than remembering to.
func FormLanguage(form url.Values) string { return Language(form.Get("lang")) }
