package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/portal"
)

// Phone sign-in: the two guest operations and the operator's configuration.
//
// The guest half is `redeemVoucher`'s shape exactly — `303` on every outcome, the
// document before the authorization — and the decisions are `portal.Service`'s.
// What is here is the mapping, plus the one fact only an HTTP handler has: the
// client's address, for the per-address limit.

// PhoneSignInStore is the operator's view of how a site sends codes.
//
// **It never returns a key.** `gen.PhoneSignIn` has no field one could arrive in —
// `credentialsSet` is a bool — which is the shape `AuthAttempt` uses to make a
// password unrepresentable rather than merely unwritten.
type PhoneSignInStore interface {
	// PhoneSignIn answers an unconfigured site with `provider: null`, and an
	// unknown site with ErrNotFound.
	PhoneSignIn(ctx context.Context, siteID string) (gen.PhoneSignIn, error)
	// SetPhoneSignIn replaces the configuration. Nil keys keep the stored ones;
	// the first configuration without both is ErrKeysRequired.
	SetPhoneSignIn(ctx context.Context, siteID string, in gen.PhoneSignInUpdate) (gen.PhoneSignIn, error)

	// PhoneAbuseControls answers a venue that never tuned its limits with the
	// defaults and `customised: false`; an unknown site is ErrNotFound.
	PhoneAbuseControls(ctx context.Context, siteID string) (gen.PhoneAbuseControls, error)
	// SetPhoneAbuseControls replaces the venue's limits, which the caller has
	// validated. The next code requested is held to them.
	SetPhoneAbuseControls(ctx context.Context, siteID string, in gen.PhoneAbuseControlsUpdate) (gen.PhoneAbuseControls, error)
	// PhoneSpendCap is the deployment-wide daily cap across every site.
	PhoneSpendCap(ctx context.Context) (gen.PhoneSpendCap, error)
	SetPhoneSpendCap(ctx context.Context, dailyLimit *int) (gen.PhoneSpendCap, error)
	// SendPhoneTestMessage sends one counted message through the saved provider.
	// Every outcome is a result, not an error; an unknown site is ErrNotFound.
	SendPhoneTestMessage(ctx context.Context, siteID, phone string) (gen.PhoneTestMessageResult, error)
}

// ErrKeysRequired is a first configuration that did not bring its keys.
var ErrKeysRequired = errKeysRequired{}

type errKeysRequired struct{}

func (errKeysRequired) Error() string { return "api: the provider's keys are required the first time" }

// ── the client's address ─────────────────────────────────────────────────────

type clientIPKey struct{}

// clientIP is the address the guest's request came from, for the per-address
// limit. **`RemoteAddr` and nothing else**: an `X-Forwarded-For` is a header the
// guest writes, and a limit keyed on it is a limit the guest chooses. The portal is
// published directly (`deploy/compose.yaml`, `80:8081`), so the peer is the guest.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

// ── requestPhoneCode ─────────────────────────────────────────────────────────

func (p portalRedirect) VisitRequestPhoneCodeResponse(w http.ResponseWriter) error {
	return p.write(w)
}

func (p portalRedirect) VisitVerifyPhoneCodeResponse(w http.ResponseWriter) error {
	return p.write(w)
}

// RequestPhoneCode sends a code and answers `303`, always — to the code form when
// one is on its way, back to the number form otherwise. The number is never in the
// redirect.
func (s *Server) RequestPhoneCode(ctx context.Context, request gen.RequestPhoneCodeRequestObject) (gen.RequestPhoneCodeResponseObject, error) {
	if s.portal == nil {
		return nil, errNotImplemented
	}
	var phone, lang string
	if request.Body != nil {
		phone = request.Body.Phone
		lang = languageOf(request.Body.Lang)
	}
	sess, ok := guestFrom(ctx)
	if !ok {
		if s.portal.Ports().PhoneCodes == nil {
			return gen.RequestPhoneCode404ApplicationProblemPlusJSONResponse(methodNotOffered().Problem()), nil
		}
		return portalRedirect{location: portal.PhoneFailure(lang, portal.ReasonNotPresent)}, nil
	}
	if !s.phoneMethodOffered(ctx, sess.SiteID) {
		return gen.RequestPhoneCode404ApplicationProblemPlusJSONResponse(methodNotOffered().Problem()), nil
	}
	out := s.portal.SendPhoneCode(ctx, sess, phone, clientIPFrom(ctx))
	switch {
	case out.NotOffered:
		return gen.RequestPhoneCode404ApplicationProblemPlusJSONResponse(methodNotOffered().Problem()), nil
	case out.AlreadyOn:
		return portalRedirect{location: portal.Connected(lang)}, nil
	case out.Sent:
		return portalRedirect{location: portal.PhoneSentPage(lang)}, nil
	default:
		return portalRedirect{location: portal.PhoneFailure(lang, out.Reason)}, nil
	}
}

// VerifyPhoneCode checks the code and grants, answering `303` on every outcome and
// asking the NAS to authorize only after the redirect is on the wire.
func (s *Server) VerifyPhoneCode(ctx context.Context, request gen.VerifyPhoneCodeRequestObject) (gen.VerifyPhoneCodeResponseObject, error) {
	if s.portal == nil {
		return nil, errNotImplemented
	}
	var code, lang string
	var form guestForm
	if b := request.Body; b != nil {
		code = b.Code
		lang = languageOf(b.Lang)
		form = guestForm{name: b.Name, email: b.Email, consent: (*string)(b.MarketingConsent), version: b.ConsentVersion}
	}
	sess, ok := guestFrom(ctx)
	if !ok {
		if s.portal.Ports().PhoneCodes == nil {
			return gen.VerifyPhoneCode404ApplicationProblemPlusJSONResponse(methodNotOffered().Problem()), nil
		}
		return portalRedirect{location: portal.PhoneFailure(lang, portal.ReasonNotPresent)}, nil
	}
	if !s.phoneMethodOffered(ctx, sess.SiteID) {
		return gen.VerifyPhoneCode404ApplicationProblemPlusJSONResponse(methodNotOffered().Problem()), nil
	}
	// The venue's guest-data setting, read before the code is spent — the same
	// order as a voucher, which is not claimed for a form about to be refused. A
	// refused form keeps the code form open (`sent=1`): the code is still live.
	set := s.guestSetting(ctx, sess.SiteID)
	in, why := readGuestForm(withoutPhoneField(set), form)
	if why != "" {
		return portalRedirect{location: portal.PhoneDetailsFailure(lang, why)}, nil
	}
	out := s.portal.PhoneLogin(ctx, sess, code)
	if out.NotOffered {
		return gen.VerifyPhoneCode404ApplicationProblemPlusJSONResponse(methodNotOffered().Problem()), nil
	}
	if !out.Connected {
		return portalRedirect{location: portal.PhoneFailure(lang, out.Reason)}, nil
	}
	if out.Grant != nil {
		s.keepGuestInput(ctx, sess, out, withVerifiedPhone(set, form, in, out.Grant.Phone))
	}
	return portalRedirect{location: portal.Connected(lang), after: out.Authorize, ctx: ctx}, nil
}

// withoutPhoneField is the setting a phone guest's code form is read against: the
// venue's, less `phone`. The number was just proved by the code; asking the guest
// to type it again would be asking for a second, unproved copy of it.
func withoutPhoneField(set gen.GuestDataSettings) gen.GuestDataSettings {
	out := set
	out.Fields = make([]gen.GuestDataFieldSetting, 0, len(set.Fields))
	for _, f := range set.Fields {
		if f.Field != gen.GuestDataFieldPhone {
			out.Fields = append(out.Fields, f)
		}
	}
	return out
}

// withVerifiedPhone completes what the form gave with the number the code proved:
// the venue's `phone` field, where it collects one, and the consent's contact.
// `readGuestForm`'s rule for consent is unchanged — a ticked `yes`, a version,
// and a contact the venue collects — and the verified number is that contact only
// at a venue that asked for a phone number.
func withVerifiedPhone(set gen.GuestDataSettings, f guestForm, in guestInput, phone string) guestInput {
	collects := false
	for _, field := range set.Fields {
		if field.Field == gen.GuestDataFieldPhone {
			collects = true
		}
	}
	if !collects || phone == "" {
		return in
	}
	p := phone
	in.details.Phone = &p
	if in.consent != nil {
		in.consent.Phone = &p
		return in
	}
	if set.MarketingConsent.Enabled && text(f.consent) == "yes" && text(f.version) != "" {
		in.consent = &ConsentGiven{Name: in.details.Name, Phone: &p, Email: in.details.Email, Version: text(f.version)}
	}
	return in
}

// ── the operator's configuration ─────────────────────────────────────────────

// PhoneLimits is otp.Limits as the contract reports it (`PhoneSignInLimits`),
// read from the package that enforces them rather than restated.
func PhoneLimits(l otp.Limits) gen.PhoneSignInLimits {
	return gen.PhoneSignInLimits{
		PerNumberPerHour:  l.PerNumberHour,
		PerNumberPerDay:   l.PerNumberDay,
		PerDevicePerHour:  l.PerDeviceHour,
		PerAddressPerHour: l.PerAddressHour,
		CodeMinutes:       int(l.CodeLifetime.Minutes()),
		MaxAttempts:       l.MaxAttempts,
	}
}

// withPhoneLimits reports the limits **in force at this venue** — its tuned ones
// where it has them — so the sign-in panel and the abuse-controls panel cannot
// state two different sets.
func (s *Server) withPhoneLimits(ctx context.Context, p gen.PhoneSignIn) gen.PhoneSignIn {
	p.Limits = PhoneLimits(otp.DefaultLimits)
	if c, err := s.ports.PhoneSignIn.PhoneAbuseControls(ctx, p.SiteId); err == nil {
		p.Limits = c.Limits
	}
	p.Country = gen.PhoneSignInCountryVN
	return p
}

func (s *Server) GetPhoneSignIn(ctx context.Context, request gen.GetPhoneSignInRequestObject) (gen.GetPhoneSignInResponseObject, error) {
	if s.ports.PhoneSignIn == nil {
		return nil, errNotImplemented
	}
	p, err := s.ports.PhoneSignIn.PhoneSignIn(ctx, string(request.SiteId))
	if err != nil {
		return nil, err
	}
	return gen.GetPhoneSignIn200JSONResponse(s.withPhoneLimits(ctx, p)), nil
}

// UpdatePhoneSignIn validates what the schema cannot say and writes one audit
// entry whose params say whether the keys changed — never what they are.
func (s *Server) UpdatePhoneSignIn(ctx context.Context, request gen.UpdatePhoneSignInRequestObject) (gen.UpdatePhoneSignInResponseObject, error) {
	if s.ports.PhoneSignIn == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body
	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}
	if !otp.ValidTemplate(in.MessageTemplate) {
		errs["messageTemplate"] = "Put {code} in the message exactly once."
		faults["messageTemplate"] = gen.FieldFaultMissingCodePlaceholder
	}
	switch in.Provider {
	case gen.EsmsSms:
		if blank(in.Brandname) {
			errs["brandname"] = "Required for SMS."
			faults["brandname"] = gen.FieldFaultRequired
		}
	case gen.EsmsZalo:
		if blank(in.ZaloOaId) {
			errs["zaloOaId"] = "Required for Zalo."
			faults["zaloOaId"] = gen.FieldFaultRequired
		}
		if blank(in.ZaloTemplateId) {
			errs["zaloTemplateId"] = "Required for Zalo."
			faults["zaloTemplateId"] = gen.FieldFaultRequired
		}
	}
	for field, v := range map[string]*string{"apiKey": in.ApiKey, "secretKey": in.SecretKey} {
		// A pasted key that brought a space or a newline with it is a key that
		// differs from the provider's by a character nobody can see.
		if v != nil && !printableNoSpace(*v) {
			errs[field] = "Paste the key without spaces or line breaks."
			faults[field] = gen.FieldFaultRequired
		}
	}
	if len(faults) > 0 {
		return nil, validation(errs, faults)
	}

	p, err := s.ports.PhoneSignIn.SetPhoneSignIn(ctx, string(request.SiteId), in)
	if err != nil {
		if err == ErrKeysRequired {
			return nil, validation(
				map[string]string{"apiKey": "Required the first time.", "secretKey": "Required the first time."},
				map[string]gen.FieldFault{"apiKey": gen.FieldFaultRequired, "secretKey": gen.FieldFaultRequired},
			)
		}
		return nil, err
	}
	who := callerFrom(ctx)
	subject := gen.AuditSubject{Kind: gen.AuditSubjectKindSite, Id: p.SiteId}
	s.recordIn(ctx, who, p.SiteId, gen.AuditKindPortalSettingsChanged, subject, map[string]any{
		"section":            "phone-sign-in",
		"credentialsChanged": in.ApiKey != nil || in.SecretKey != nil,
	})
	return gen.UpdatePhoneSignIn200JSONResponse(s.withPhoneLimits(ctx, p)), nil
}

// ── abuse controls, the spend cap and the test message ───────────────────────

func (s *Server) GetPhoneAbuseControls(ctx context.Context, request gen.GetPhoneAbuseControlsRequestObject) (gen.GetPhoneAbuseControlsResponseObject, error) {
	if s.ports.PhoneSignIn == nil {
		return nil, errNotImplemented
	}
	c, err := s.ports.PhoneSignIn.PhoneAbuseControls(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	return gen.GetPhoneAbuseControls200JSONResponse(withAbuseBounds(c)), nil
}

// UpdatePhoneAbuseControls applies the rules the schema cannot state — the day
// limit not below the hour limit, each range a real range — then stores them.
// The bounds themselves are the schema's, enforced by request validation, and the
// database's CHECKs behind that.
func (s *Server) UpdatePhoneAbuseControls(ctx context.Context, request gen.UpdatePhoneAbuseControlsRequestObject) (gen.UpdatePhoneAbuseControlsResponseObject, error) {
	if s.ports.PhoneSignIn == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	site, err := s.siteNamed(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	in := *request.Body
	if in.Limits.PerNumberPerDay < in.Limits.PerNumberPerHour {
		return nil, withFieldParams(validation(
			map[string]string{"limits.perNumberPerDay": fmt.Sprintf("At least %d.", in.Limits.PerNumberPerHour)},
			map[string]gen.FieldFault{"limits.perNumberPerDay": gen.FieldFaultBelowMinimum}),
			map[string]map[string]any{"limits.perNumberPerDay": {"minimumCount": in.Limits.PerNumberPerHour}})
	}
	seen := map[string]bool{}
	for _, p := range in.MobilePrefixes {
		if !otp.ValidPrefix(p) || seen[p] {
			return nil, validation(map[string]string{"mobilePrefixes": "Choose from 03, 05, 07, 08 and 09, each once."},
				map[string]gen.FieldFault{"mobilePrefixes": gen.FieldFaultDuplicate})
		}
		seen[p] = true
	}
	if len(in.MobilePrefixes) == 0 {
		return nil, validation(map[string]string{"mobilePrefixes": "Required."},
			map[string]gen.FieldFault{"mobilePrefixes": gen.FieldFaultRequired})
	}
	after, err := s.ports.PhoneSignIn.SetPhoneAbuseControls(ctx, site.Id, in)
	if err != nil {
		return nil, err
	}
	s.recordIn(ctx, callerFrom(ctx), site.Id, gen.AuditKindPortalSettingsChanged,
		gen.AuditSubject{Kind: gen.AuditSubjectKindSite, Id: site.Id, Name: ptr(site.Name)},
		map[string]any{"section": "phone-abuse-controls", "fields": []string{"limits", "mobilePrefixes"},
			"limits": after.Limits, "mobilePrefixes": after.MobilePrefixes})
	return gen.UpdatePhoneAbuseControls200JSONResponse(withAbuseBounds(after)), nil
}

func (s *Server) GetPhoneSpendCap(ctx context.Context, _ gen.GetPhoneSpendCapRequestObject) (gen.GetPhoneSpendCapResponseObject, error) {
	if s.ports.PhoneSignIn == nil {
		return nil, errNotImplemented
	}
	c, err := s.ports.PhoneSignIn.PhoneSpendCap(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetPhoneSpendCap200JSONResponse(withSpendCapBounds(c)), nil
}

// UpdatePhoneSpendCap sets or clears the deployment-wide cap. The deployment's
// entry, not a venue's (`siteId` null): the cap bounds every site at once.
func (s *Server) UpdatePhoneSpendCap(ctx context.Context, request gen.UpdatePhoneSpendCapRequestObject) (gen.UpdatePhoneSpendCapResponseObject, error) {
	if s.ports.PhoneSignIn == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	before, err := s.ports.PhoneSignIn.PhoneSpendCap(ctx)
	if err != nil {
		return nil, err
	}
	after, err := s.ports.PhoneSignIn.SetPhoneSpendCap(ctx, request.Body.DailyLimit)
	if err != nil {
		return nil, err
	}
	s.record(ctx, callerFrom(ctx), gen.AuditKindPhoneSpendCapChanged,
		gen.AuditSubject{Kind: gen.AuditSubjectKindPhoneSpendCap, Id: "phone-spend-cap"},
		map[string]any{"from": before.DailyLimit, "to": after.DailyLimit})
	return gen.UpdatePhoneSpendCap200JSONResponse(withSpendCapBounds(after)), nil
}

// SendPhoneTestMessage sends one counted message to the operator's own number. The
// entry names the outcome and never the number: the log outlives the reason the
// number was typed.
func (s *Server) SendPhoneTestMessage(ctx context.Context, request gen.SendPhoneTestMessageRequestObject) (gen.SendPhoneTestMessageResponseObject, error) {
	if s.ports.PhoneSignIn == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	site, err := s.siteNamed(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	res, err := s.ports.PhoneSignIn.SendPhoneTestMessage(ctx, site.Id, request.Body.Phone)
	if err != nil {
		return nil, err
	}
	s.recordIn(ctx, callerFrom(ctx), site.Id, gen.AuditKindPortalSettingsChanged,
		gen.AuditSubject{Kind: gen.AuditSubjectKindSite, Id: site.Id, Name: ptr(site.Name)},
		map[string]any{"section": "phone-test-message", "fields": []string{"testMessage"}, "outcome": string(res.Outcome)})
	return gen.SendPhoneTestMessage200JSONResponse(res), nil
}

func blank(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }

func printableNoSpace(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// phoneOffered is `updatePortalSettings`' check that `phone` is only offered where
// a provider is configured. A button for a method that cannot send is a control
// that lies, and the guest who presses it is the one who pays.
func (s *Server) phoneOffered(ctx context.Context, siteID string) bool {
	if s.ports.PhoneSignIn == nil {
		return false
	}
	p, err := s.ports.PhoneSignIn.PhoneSignIn(ctx, siteID)
	return err == nil && p.Provider != nil && p.CredentialsSet
}

// phoneMethodOffered is whether the venue shows the phone button at all. A
// configured provider is not enough: an endpoint that works while its button is
// hidden is an endpoint somebody finds — `acceptTerms` answers `404` for the same
// reason, and this is the method whose every request costs the venue money.
func (s *Server) phoneMethodOffered(ctx context.Context, siteID string) bool {
	if s.ports.Portal == nil {
		return false
	}
	set, err := s.ports.Portal.PortalSettings(ctx, siteID)
	return err == nil && hasPhone(set.Methods)
}

// phoneCodeMinutes is how long a code sent at this venue works, for the sentence
// on the code form — the venue's tuned `codeMinutes` where it has one, the
// product default where it has not, and nil where the venue does not offer phone
// sign-in. Read from the same abuse controls `otp.Service.Send` sets the expiry
// from, so the page cannot promise a lifetime the code does not have — a fixed
// "five minutes" in the copy would be wrong at every venue that tuned it.
func (s *Server) phoneCodeMinutes(ctx context.Context, siteID string, methods []gen.PortalMethod) *int {
	if !hasPhone(methods) || s.ports.PhoneSignIn == nil {
		return nil
	}
	l := PhoneLimits(otp.DefaultLimits)
	if c, err := s.ports.PhoneSignIn.PhoneAbuseControls(ctx, siteID); err == nil {
		l = c.Limits
	}
	m := l.CodeMinutes
	return &m
}

func hasPhone(methods []gen.PortalMethod) bool {
	for _, m := range methods {
		if m == gen.PortalMethodPhone {
			return true
		}
	}
	return false
}
