package api

import (
	"context"
	"net/http"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
)

// The guest portal's five operations.
//
// The reason this file reads the way it does is the reason they cannot be
// faked:
//
//	Each needs a real `VerifyPresence`, a MAC-bound grant store, and the
//	portal's own listener — **a fake answering "present" would make the
//	security test pass.**
//
// All three now exist: `internal/nas` verifies presence for UniFi including the
// still-unauthorized check, `internal/portal` holds the grant store behind an
// interface, and `PortalHandler` is the second surface. What is left here is the
// mapping between the contract's shapes and the portal's decisions, plus the two
// rules that are properties of the *response* rather than of the decision:
//
//  1. **`303 See Other` on every outcome of an authenticating operation.** The
//     iOS Captive Network Assistant re-checks captivity only after a full
//     document navigation. Without one its button stays "Cancel", and Cancel
//     disassociates the device — so a `fetch`-shaped login authenticates the
//     guest and then throws them off the network, and Playwright reports a pass.
//     There is a test on the status code for exactly that reason: it is
//     invisible in a browser and fatal on hardware.
//  2. **The document is written before the NAS is asked to authorize.** Android
//     destroys the login WebView the instant the network validates, so anything
//     written after that may never be seen. `portalRedirect` writes the redirect,
//     flushes it, and only then runs the driver call the service handed it.

// ── the guest session ────────────────────────────────────────────────────────

type guestKey struct{}

func guestFrom(ctx context.Context) (portal.Session, bool) {
	s, ok := ctx.Value(guestKey{}).(portal.Session)
	return s, ok
}

// guestSession resolves the browser-to-MAC binding, on the portal surface only.
//
// It is a middleware rather than a call in each handler because the contract's
// portal operations **declare no parameters at all** — `getPortalContext` and
// `getPortalStatus` take none, and the three form operations take only `code`,
// `username`, `password` and `lang`. There is therefore no operation through
// which a MAC can arrive, and the only carrier left is the cookie the front door
// set. One middleware resolving it is one place to get the binding wrong.
//
// A request that resolves to nothing is not refused here. The handlers answer
// their own refusals, because the right refusal differs: `getPortalContext`
// answers `403` with a problem document and `redeemVoucher` answers `303` to a
// page, and a middleware that refused uniformly would answer one of them wrongly.
func (s *Server) guestSession(f gen.StrictHandlerFunc, _ string) gen.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		if s.portal != nil {
			if sess, err := s.portal.Resolve(ctx, w, r); err == nil {
				ctx = context.WithValue(ctx, guestKey{}, sess)
			}
		}
		// The peer address, for phone sign-in's per-address limit. See clientIP.
		ctx = context.WithValue(ctx, clientIPKey{}, clientIP(r))
		return f(ctx, w, r, request)
	}
}

// ── the 303, and the ordering it carries ─────────────────────────────────────

// portalRedirect is the answer to every outcome of every authenticating
// operation.
//
// It satisfies all three `…ResponseObject` interfaces because the three
// operations answer identically — the contract gives them one shared
// `PortalRedirect` response and states the rule once. Writing three copies would
// be three places for the status code to drift, and the status code is the thing
// that decides whether an iPhone stays on the network.
//
// **`location` is root-relative and the contract says so.** `portal.Connected`
// and `portal.Failure` build a path; a header typed as a bare `string` would let
// `web/mock`'s absolute URL at `PORTAL_ORIGIN` conform just as well, and neither
// server could be called wrong. `PortalRedirect.headers.Location` carries a
// `pattern`, and
// `TestPortalRedirectLocationMatchesTheContractsPattern` reads it out of the
// embedded document rather than restating it. A guest is on plain HTTP with no
// route off this SSID: an absolute value would not carry a scheme and a host, it
// would pick them, on the screen where they have just typed a credential.
type portalRedirect struct {
	location string

	// after is the NAS authorization, and it runs **after** the response has
	// been written and flushed. See the file comment. Nil where there is nothing
	// to authorize: a refusal, or a guest the driver already reported as on.
	after func(ctx context.Context) error

	// ctx is the request's, captured so `after` can run inside the same request
	// without being cancelled by the client hanging up. See `write`.
	ctx context.Context
}

func (p portalRedirect) write(w http.ResponseWriter) error {
	// `Cache-Control: no-store` is already set by `securityHeaders` and the
	// contract requires it on this response by name. Set again here so that the
	// guarantee does not depend on a middleware somebody reorders.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", p.location)
	w.WriteHeader(http.StatusSeeOther)

	if p.after == nil {
		return nil
	}
	// Flush so the redirect is on the wire before the controller is called.
	// Without this the response sits in a buffer for the length of an HTTP round
	// trip to somebody's UniFi, which is precisely the window in which Android
	// tears the WebView down.
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}

	// The client has what it needs and may hang up; the network still has to be
	// opened. `WithoutCancel` keeps the driver call alive past that, and the
	// timeout keeps it from outliving the request forever.
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authorizeTimeout)
	defer cancel()

	// **The error is dropped *here*, and it is not lost.**
	//
	// Nothing can be reported to this client: the status line is on the wire, and
	// returning the error would be worse than useless, because the generated
	// handler passes a non-nil error from `Visit…` to `ResponseErrorHandlerFunc`,
	// which would try to write a problem document over a response that has
	// already been written — a superfluous `WriteHeader` and a corrupt body.
	//
	// Dropped with nowhere else to go, the failure would reach nobody at all: a
	// guest whose grant existed and whose controller call failed would see the
	// connected page and no internet, and neither the console, the log nor their
	// own screen would say so. The channel is inside the closure rather than out here —
	// `portal.Service.deliver` retries what is worth retrying and records the
	// outcome through the `Authorizations` port, which writes both the operator's
	// `authorize-failed` event and the grant's `Delivery`, so the guest's next
	// `/portal/status` reads `not-on-network` instead of `connected`.
	//
	// It is inside the closure and not here on purpose. This function has the
	// response writer and nothing else; the grant, the placement and the session
	// are the service's, and reassembling them at this seam would be a second copy
	// of what the service already holds.
	_ = p.after(ctx)
	return nil
}

// authorizeTimeout bounds the out-of-band call. Longer than the policy API's
// budget because this one is not blocking a FreeRADIUS worker: it is an HTTPS
// request to a controller that may be on the far side of a site-to-site link.
const authorizeTimeout = 10 * time.Second

func (p portalRedirect) VisitRedeemVoucherResponse(w http.ResponseWriter) error {
	return p.write(w)
}
func (p portalRedirect) VisitAcceptTermsResponse(w http.ResponseWriter) error { return p.write(w) }
func (p portalRedirect) VisitLoginWithAccountResponse(w http.ResponseWriter) error {
	return p.write(w)
}

// redirectFor turns a service outcome into the response.
//
// `page` is where a *failure* returns the guest — the form they submitted, so the
// message lands beside the field. Success always goes to the connected page.
func redirectFor(ctx context.Context, out portal.Outcome, page portal.Page, lang, submittedCode string) portalRedirect {
	if !out.Connected {
		return portalRedirect{location: portal.Failure(page, lang, out.Reason, submittedCode)}
	}
	return portalRedirect{location: portal.Connected(lang), after: out.Authorize, ctx: ctx}
}

// ── getPortalContext ─────────────────────────────────────────────────────────

// GetPortalContext is what the portal may show this client.
//
// ── why an unreachable controller answers 200 ────────────────────────────────
//
// The contract declares two responses and enumerates what the `403` means: "The
// client is not associated, is on a different AP or site, or is already
// authorized." **An outage is not in that list**, and answering `403` for one is
// the collapse `internal/portal/presence.go` exists to undo — the venue's
// controller being down, rendered to a guest as a statement about their phone,
// on the one screen where they can neither see the difference nor escalate.
//
// So a controller that cannot answer gets the venue's page. Nothing is granted:
// the three authenticating operations run the same check and fail closed on the
// same verdict, and they tell the guest `generic` rather than `not-present`. What
// this operation returns is a venue name, a language list and the terms — there
// is nothing here to protect, and `403` would be protecting it from the guest
// rather than from an attacker.
//
// The NAS parameters are never echoed back. Returning them would make this
// endpoint an oracle for what the portal believes about an attacker-supplied MAC,
// and the contract says so.
func (s *Server) GetPortalContext(ctx context.Context, _ gen.GetPortalContextRequestObject) (gen.GetPortalContextResponseObject, error) {
	if s.portal == nil || s.ports.Portal == nil {
		return nil, errNotImplemented
	}
	sess, ok := guestFrom(ctx)
	if !ok {
		// No session and no NAS parameters: nothing here names a device, so
		// there is nothing the driver could confirm.
		return gen.GetPortalContext403ApplicationProblemPlusJSONResponse(deviceNotVerified().Problem()), nil
	}

	if v := s.portal.Look(ctx, sess); v != portal.VerdictProceed && v != portal.VerdictUnavailable {
		return gen.GetPortalContext403ApplicationProblemPlusJSONResponse(deviceNotVerified().Problem()), nil
	}

	set, err := s.ports.Portal.PortalSettings(ctx, sess.SiteID)
	if err != nil {
		// A site with no portal settings is a deployment that has not been
		// configured, not a device that failed a check. It must not be reported
		// as one — `404` is not a response this operation declares and `403`
		// would be a lie about the guest.
		return nil, internalProblem()
	}
	pc := portalContextFrom(set, s.siteToday(ctx, sess.SiteID))
	dc := portalDataCollection(s.guestSetting(ctx, sess.SiteID))
	pc.DataCollection = &dc
	pc.PhoneCodeMinutes = s.phoneCodeMinutes(ctx, sess.SiteID, set.Methods)
	return gen.GetPortalContext200JSONResponse(pc), nil
}

// portalContextFrom is the read model the guest gets.
//
// It is deliberately a narrowing of `PortalSettings` rather than the same object:
// the settings carry `siteId`, `portalUrl` and the byte budgets, which are the
// operator's business and not the guest's.
//
// The design is narrowed to `today` — the deployment's calendar date — so a block
// an owner hid, or a promotion outside its dates, is not in the response at all.
// This is a pre-authentication read anybody in radio range can make; "not
// rendered" would still be "published".
func portalContextFrom(set PortalSetting, today string) gen.PortalContext {
	out := gen.PortalContext{
		VenueName:   set.VenueName,
		NetworkName: set.NetworkName,
		LogoDataUri: set.LogoDataUri,
		Languages:   set.Languages,
	}
	if len(out.Languages) == 0 {
		// The contract requires at least one, and a venue that configured none
		// still has to render a page. English is the catalogue's default.
		out.Languages = []gen.Language{gen.En}
	}
	out.Methods = make([]gen.PortalContextMethods, 0, len(set.Methods))
	for _, m := range set.Methods {
		out.Methods = append(out.Methods, gen.PortalContextMethods(m))
	}
	if set.HeroImage != nil {
		url := set.HeroImage.Url
		out.HeroImageUrl = &url
	}
	// The whole map, per language, verbatim. Terms are legal text: the portal
	// resolves which one to render and marks the language it is written in, and
	// nothing anywhere paraphrases, machine-translates or silently substitutes.
	summary := map[string]string{}
	for k, v := range set.Terms.Summary {
		summary[k] = v
	}
	out.Terms.Summary = &summary
	design := narrowDesign(set.Design, today)
	out.Design = &design
	return out
}

// ── redeemVoucher ────────────────────────────────────────────────────────────

// RedeemVoucher answers `303 See Other`, always.
//
// Success and failure are both navigations, because the CNA only re-checks
// captivity after one. The failure carries the submitted code back, normalised
// and capped, so that a guest who mistyped one of twelve characters one-handed on
// a phone does not retype all twelve — there is no client state to hold it and
// the flow must work with JavaScript off, so the server carries it.
func (s *Server) RedeemVoucher(ctx context.Context, request gen.RedeemVoucherRequestObject) (gen.RedeemVoucherResponseObject, error) {
	if s.portal == nil {
		return nil, errNotImplemented
	}
	var code, lang string
	var form guestForm
	if b := request.Body; b != nil {
		code = b.Code
		lang = languageOf(b.Lang)
		form = guestForm{b.Name, b.Phone, b.Email, (*string)(b.MarketingConsent), b.ConsentVersion}
	}

	sess, ok := guestFrom(ctx)
	if !ok {
		return portalRedirect{location: portal.Failure(portal.PageVoucher, lang, portal.ReasonNotPresent, code)}, nil
	}
	// Before the claim: a voucher is not spent on a form about to be refused.
	in, why := readGuestForm(s.guestSetting(ctx, sess.SiteID), form)
	if why != "" {
		return portalRedirect{location: portal.Failure(portal.PageVoucher, lang, why, code)}, nil
	}
	out := s.portal.Redeem(ctx, sess, code)
	if out.Connected {
		s.keepGuestInput(ctx, sess, out, in)
	}
	return redirectFor(ctx, out, portal.PageVoucher, lang, code), nil
}

// ── acceptTerms ──────────────────────────────────────────────────────────────

// AcceptTerms is click-through: the guest reads the terms and presses one button.
//
// `404` where the venue has not enabled it, rather than granting: an endpoint
// that works while its button is hidden is an endpoint somebody finds, and this
// is the one with no credential in front of it.
//
// **A presence failure answers `303` to the problem page, not `403`**, here and
// in `web/mock` alike, and the contract declares the same. Two reasons, and the
// second is the decisive one: a form navigation answered
// `403 application/problem+json` renders
// raw JSON to a guest inside a mini-browser with no client left to turn it into a
// sentence, and it is not a navigation the Captive Network Assistant treats as a
// captivity re-check, so the button stays *Cancel* and Cancel disassociates the
// device.
//
// The `404` below stays, and the difference is the narrow one: no guest
// navigation reaches it. The button is hidden where the method is not offered, so
// that response answers a hand-made request rather than a flow.
func (s *Server) AcceptTerms(ctx context.Context, request gen.AcceptTermsRequestObject) (gen.AcceptTermsResponseObject, error) {
	if s.portal == nil || s.ports.Portal == nil {
		return nil, errNotImplemented
	}
	var lang string
	var form guestForm
	if b := request.Body; b != nil {
		lang = languageOf(b.Lang)
		form = guestForm{b.Name, b.Phone, b.Email, (*string)(b.MarketingConsent), b.ConsentVersion}
	}

	sess, ok := guestFrom(ctx)
	if !ok {
		return portalRedirect{location: portal.Failure(portal.PageProblem, lang, portal.ReasonNotPresent, "")}, nil
	}

	offered, minutes := s.clickThrough(ctx, sess.SiteID)
	// Accepting the terms is not consenting to marketing, and nothing here reads it
	// as one: the checkbox is read by `readGuestForm` exactly as on the other two.
	in, why := readGuestForm(s.guestSetting(ctx, sess.SiteID), form)
	if why != "" && offered {
		// Back to the welcome page, where the form and its fields are.
		return portalRedirect{location: portal.Failure(portal.PageWelcome, lang, why, "")}, nil
	}
	out := s.portal.Agree(ctx, sess, offered, minutes)
	if out.NotOffered {
		return gen.AcceptTerms404ApplicationProblemPlusJSONResponse(methodNotOffered().Problem()), nil
	}
	if out.Connected {
		s.keepGuestInput(ctx, sess, out, in)
	}
	return redirectFor(ctx, out, portal.PageProblem, lang, ""), nil
}

// clickThrough reads the venue's setting.
//
// The duration is never defaulted. `PortalSettings.clickThroughMinutes` is
// "required in practice whenever click-through is offered" and the server refuses
// the combination without it: a mode with no credential and no duration is
// unmetered public internet handed out on one tap, which is a decision a venue
// makes deliberately or does not make at all.
func (s *Server) clickThrough(ctx context.Context, siteID string) (offered bool, minutes int) {
	set, err := s.ports.Portal.PortalSettings(ctx, siteID)
	if err != nil {
		return false, 0
	}
	for _, m := range set.Methods {
		if m == gen.PortalMethodClickThrough {
			offered = true
		}
	}
	if set.ClickThroughMinutes != nil {
		minutes = *set.ClickThroughMinutes
	}
	return offered, minutes
}

// ── loginWithAccount ─────────────────────────────────────────────────────────

// LoginWithAccount answers `303 See Other`, always, for the same reason as
// `redeemVoucher`.
//
// **Nothing submitted here is ever echoed.** The voucher path carries the code
// back because a guest cannot reasonably retype twelve random characters; a
// username is not in that position and a password never is. A username in a
// redirect is a username in every proxy log between the phone and the portal, and
// `AuthAttempt` carries the same rule for the same reason.
func (s *Server) LoginWithAccount(ctx context.Context, request gen.LoginWithAccountRequestObject) (gen.LoginWithAccountResponseObject, error) {
	if s.portal == nil {
		return nil, errNotImplemented
	}
	var username, password, lang string
	var form guestForm
	if b := request.Body; b != nil {
		username = b.Username
		password = b.Password
		lang = languageOf(b.Lang)
		form = guestForm{b.Name, b.Phone, b.Email, (*string)(b.MarketingConsent), b.ConsentVersion}
	}

	sess, ok := guestFrom(ctx)
	if !ok {
		return portalRedirect{location: portal.Failure(portal.PageAccount, lang, portal.ReasonNotPresent, "")}, nil
	}
	in, why := readGuestForm(s.guestSetting(ctx, sess.SiteID), form)
	if why != "" {
		return portalRedirect{location: portal.Failure(portal.PageAccount, lang, why, "")}, nil
	}
	out := s.portal.Login(ctx, sess, username, password)
	if out.Connected {
		s.keepGuestInput(ctx, sess, out, in)
	}
	return redirectFor(ctx, out, portal.PageAccount, lang, ""), nil
}

// ── getPortalStatus ──────────────────────────────────────────────────────────

// GetPortalStatus is what the connected page may show about this grant.
//
// Deliberately thin: it carries what the venue granted, **not what the client has
// used**. That number comes from `radacct`, advances only on Interim-Update or
// Stop, has a 300-second floor, and on the controller-API path may never arrive
// at all — so there is no figure here that could be rendered without a freshness
// beside it, and the honest answer is not to carry one.
//
// `404` means no grant answers to this browser, and nothing else. A grant that
// has **ended** is answered with `200` and its real state. If `expired` and
// `revoked` were unreachable on the wire, a page reading the plan rather than the
// state would show a green tick to a guest whose voucher had just been revoked.
func (s *Server) GetPortalStatus(ctx context.Context, _ gen.GetPortalStatusRequestObject) (gen.GetPortalStatusResponseObject, error) {
	if s.portal == nil {
		return nil, errNotImplemented
	}
	sess, ok := guestFrom(ctx)
	if !ok {
		return gen.GetPortalStatus404ApplicationProblemPlusJSONResponse(noGrant().Problem()), nil
	}
	g, state, err := s.portal.Status(ctx, sess)
	if err != nil {
		return gen.GetPortalStatus404ApplicationProblemPlusJSONResponse(noGrant().Problem()), nil
	}

	out := gen.PortalStatus{
		State:     gen.PortalStatusState(state),
		Plan:      planFrom(g.Plan),
		GrantedAt: &g.GrantedAt,
		ExpiresAt: g.ExpiresAt,
	}
	// The same venue that answered `/portal/context`, or the success page
	// welcomes the guest to a different business than the one they consented to.
	if s.ports.Portal != nil {
		if set, err := s.ports.Portal.PortalSettings(ctx, g.SiteID); err == nil {
			name := set.VenueName
			out.VenueName = &name
		}
	}
	// Allowlisted when it was stored and allowlisted again here. Rendered as a
	// link the guest chooses to follow, never an automatic bounce — the victim of
	// an open redirect here is on an untrusted network, expects to be redirected,
	// and has just typed credentials.
	if safe := portal.SafeReturnURL(sess.OriginalURL); safe != "" {
		out.OriginalUrl = &safe
	}
	return gen.GetPortalStatus200JSONResponse(out), nil
}

func planFrom(p portal.Plan) gen.Plan {
	out := gen.Plan{
		DurationMinutes: p.DurationMinutes,
		DataAllowanceMb: p.DataAllowanceMB,
		UpKbps:          p.UpKbps,
		DownKbps:        p.DownKbps,
		EnforcedBy:      gen.PlanEnforcedBy(p.EnforcedBy),
	}
	if p.Name != "" {
		name := p.Name
		out.Name = &name
	}
	if p.NameLang != "" {
		// Venue-authored text reaching a guest inside a document whose language
		// may be something else. The portal renders it verbatim and never
		// translates, so the only honest thing it can do is mark it.
		lang := gen.Language(p.NameLang)
		out.NameLang = &lang
	}
	if out.EnforcedBy == "" {
		out.EnforcedBy = gen.PlanEnforcedByUnenforced
	}
	return out
}

// languageOf reads the form's `lang` through the portal's allowlist.
//
// The value ends up in a `Location` header, so it is allowlisted rather than
// escaped: two known strings cannot carry a path traversal or a newline, and an
// escape can always be got wrong.
func languageOf(l *gen.Language) string {
	if l == nil {
		return portal.Language("")
	}
	return portal.Language(string(*l))
}
