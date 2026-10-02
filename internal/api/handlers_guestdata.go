package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/export"
	"github.com/kiennt048/foyer/internal/portal"
)

// Guest details and marketing consent.
//
// ── the rule this file exists to keep ────────────────────────────────────────
//
// **Consent is a ticked box and nothing else.** Not accepting the terms, not
// submitting a form, not filling in an e-mail address, not a value the server did
// not expect: `marketingConsent` must be exactly `yes`, the venue must be asking for
// consent at that moment, and the version beside it must be a text the venue
// published. Anything short of all three records nothing, and the guest is let on
// all the same — consent is never the price of the network (Purple's rule, and
// Decree 356/2025/ND-CP's: no pre-ticked box, and silence is not consent).
//
// ── what a guest's details cost them ─────────────────────────────────────────
//
// Only the fields the venue asks for are read; anything else in the form is
// dropped unread. A required field left blank, or a field that is not the shape it
// claims, sends the guest back to the form **before** anything is claimed — a
// voucher is not spent on a form that is about to be refused — with a reason and
// never with the value: a phone number in a redirect is a phone number in every
// proxy log between the phone and the portal.

// ── the admin operations ─────────────────────────────────────────────────────

func (s *Server) GetGuestDataSettings(ctx context.Context, request gen.GetGuestDataSettingsRequestObject) (gen.GetGuestDataSettingsResponseObject, error) {
	if s.ports.GuestData == nil {
		return nil, errNotImplemented
	}
	if _, err := s.siteNamed(ctx, request.SiteId); err != nil {
		return nil, err
	}
	set, err := s.ports.GuestData.GuestDataSettings(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	return gen.GetGuestDataSettings200JSONResponse(set), nil
}

func (s *Server) UpdateGuestDataSettings(ctx context.Context, request gen.UpdateGuestDataSettingsRequestObject) (gen.UpdateGuestDataSettingsResponseObject, error) {
	if s.ports.GuestData == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	site, err := s.siteNamed(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	if fault := guestDataFault(*request.Body); fault != nil {
		return nil, fault
	}
	after, err := s.ports.GuestData.SetGuestDataSettings(ctx, site.Id, *request.Body)
	if err != nil {
		return nil, err
	}
	fields := make([]string, 0, len(after.Fields))
	for _, f := range after.Fields {
		name := string(f.Field)
		if f.Required {
			name += " (required)"
		}
		fields = append(fields, name)
	}
	s.recordIn(ctx, callerFrom(ctx), site.Id, gen.AuditKindGuestDataSettingsChanged,
		gen.AuditSubject{Kind: gen.AuditSubjectKindSite, Id: site.Id, Name: ptr(site.Name)},
		map[string]any{"fields": fields, "marketingConsent": after.MarketingConsent.Enabled})
	return gen.UpdateGuestDataSettings200JSONResponse(after), nil
}

// guestDataFault applies the three rules beyond the schema, first failure wins.
func guestDataFault(in gen.GuestDataSettingsUpdate) *apiError {
	seen := map[gen.GuestDataField]bool{}
	contact := false
	for _, f := range in.Fields {
		if seen[f.Field] {
			return validation(map[string]string{"fields": "Each field once."},
				map[string]gen.FieldFault{"fields": gen.FieldFaultDuplicate})
		}
		seen[f.Field] = true
		if f.Field == gen.GuestDataFieldPhone || f.Field == gen.GuestDataFieldEmail {
			contact = true
		}
	}
	if !in.MarketingConsent.Enabled {
		return nil
	}
	if strings.TrimSpace(text(in.MarketingConsent.Text.En)) == "" && strings.TrimSpace(text(in.MarketingConsent.Text.Vi)) == "" {
		return validation(map[string]string{"marketingConsent.text": "Required."},
			map[string]gen.FieldFault{"marketingConsent.text": gen.FieldFaultRequired})
	}
	if !contact {
		return validation(map[string]string{"fields": "Collect a phone number or an e-mail address."},
			map[string]gen.FieldFault{"fields": gen.FieldFaultNeedsContactField})
	}
	return nil
}

func (s *Server) ListMarketingConsents(ctx context.Context, request gen.ListMarketingConsentsRequestObject) (gen.ListMarketingConsentsResponseObject, error) {
	if s.ports.GuestData == nil {
		return nil, errNotImplemented
	}
	if _, err := s.siteNamed(ctx, request.SiteId); err != nil {
		return nil, err
	}
	p := request.Params
	page, err := s.ports.GuestData.MarketingConsents(ctx, request.SiteId, p.State, windowFrom(p.Cursor, p.Limit))
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []gen.MarketingConsent{}
	}
	return gen.ListMarketingConsents200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *Server) WithdrawMarketingConsent(ctx context.Context, request gen.WithdrawMarketingConsentRequestObject) (gen.WithdrawMarketingConsentResponseObject, error) {
	if s.ports.GuestData == nil {
		return nil, errNotImplemented
	}
	if _, err := s.siteNamed(ctx, request.SiteId); err != nil {
		return nil, err
	}
	c, err := s.ports.GuestData.WithdrawMarketingConsent(ctx, request.SiteId, request.ConsentId)
	if err != nil {
		return nil, err
	}
	// By id, never by name or address: the log keeps who withdrew what, not the
	// contact the withdrawal just cleared.
	s.recordIn(ctx, callerFrom(ctx), request.SiteId, gen.AuditKindMarketingConsentWithdrawn,
		gen.AuditSubject{Kind: gen.AuditSubjectKindMarketingConsent, Id: c.Id}, nil)
	return gen.WithdrawMarketingConsent200JSONResponse(c), nil
}

// ── the export ───────────────────────────────────────────────────────────────

// consentCells is how a consent becomes a row, checked against the contract's
// `x-csv-columns` in both directions by `readReports`.
var consentCells = map[string]func(gen.MarketingConsent) string{
	"consentId":   func(c gen.MarketingConsent) string { return c.Id },
	"consentedAt": func(c gen.MarketingConsent) string { return export.At(c.ConsentedAt) },
	"state":       func(c gen.MarketingConsent) string { return string(c.State) },
	"withdrawnAt": func(c gen.MarketingConsent) string { return export.Time(c.WithdrawnAt) },
	"withdrawnBy": func(c gen.MarketingConsent) string {
		if c.WithdrawnBy == nil {
			return ""
		}
		return string(*c.WithdrawnBy)
	},
	"name":           func(c gen.MarketingConsent) string { return export.Text(c.Name) },
	"phone":          func(c gen.MarketingConsent) string { return export.Text(c.Phone) },
	"email":          func(c gen.MarketingConsent) string { return export.Text(c.Email) },
	"lang":           func(c gen.MarketingConsent) string { return string(c.Lang) },
	"consentVersion": func(c gen.MarketingConsent) string { return c.ConsentVersion },
	"clientMac":      func(c gen.MarketingConsent) string { return c.ClientMac },
}

func (s *Server) ExportMarketingConsents(ctx context.Context, request gen.ExportMarketingConsentsRequestObject) (gen.ExportMarketingConsentsResponseObject, error) {
	if s.ports.GuestData == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	r, err := s.exportRange("ExportMarketingConsents", p.From, p.To)
	if err != nil {
		return nil, err
	}
	stream, err := begin(ctx, s, string(p.SiteId), r, s.reports.consents,
		func(ctx context.Context, visit func(gen.MarketingConsent) error) error {
			return s.ports.GuestData.ConsentsBetween(ctx, string(p.SiteId), r, visit)
		})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (c *csvStream) VisitExportMarketingConsentsResponse(w http.ResponseWriter) error {
	return c.stream(w)
}

// ── the portal ───────────────────────────────────────────────────────────────

// guestForm is the guest-detail half of an authenticating form, as submitted.
type guestForm struct {
	name, phone, email, consent, version *string
}

// guestInput is what the form amounts to once the venue's setting has read it.
type guestInput struct {
	details GuestDetails
	// consent is set only for a ticked box beside a text the venue is asking with.
	consent *ConsentGiven
}

// readGuestForm applies the venue's setting to a submission. A non-empty reason
// sends the guest back to the form, and nothing has been claimed yet.
func readGuestForm(set gen.GuestDataSettings, f guestForm) (guestInput, portal.Reason) {
	var in guestInput
	for _, field := range set.Fields {
		var raw *string
		var dst **string
		var ok func(string) bool
		switch field.Field {
		case gen.GuestDataFieldName:
			raw, dst, ok = f.name, &in.details.Name, validName
		case gen.GuestDataFieldPhone:
			raw, dst, ok = f.phone, &in.details.Phone, validPhone
		case gen.GuestDataFieldEmail:
			raw, dst, ok = f.email, &in.details.Email, validEmail
		default:
			continue
		}
		v := strings.TrimSpace(text(raw))
		if v == "" {
			if field.Required {
				return guestInput{}, portal.ReasonDetailsRequired
			}
			continue
		}
		if !ok(v) {
			return guestInput{}, portal.ReasonDetailsInvalid
		}
		*dst = &v
	}

	// All three, or nothing: the venue is asking, the box says exactly `yes`, and a
	// version came with it. A consent with no way to contact anybody is a MAC on a
	// list and nothing else, so it is not kept either.
	if set.MarketingConsent.Enabled && text(f.consent) == "yes" && text(f.version) != "" &&
		(in.details.Phone != nil || in.details.Email != nil) {
		in.consent = &ConsentGiven{
			Name: in.details.Name, Phone: in.details.Phone, Email: in.details.Email,
			Version: text(f.version),
		}
	}
	return in, ""
}

func validName(v string) bool {
	if utf8.RuneCountInString(v) > 120 {
		return false
	}
	return !strings.ContainsFunc(v, unicode.IsControl)
}

// validPhone: digits with an optional leading `+`, and spaces, dots, dashes or
// brackets between them; eight to fifteen digits, E.164's ceiling.
func validPhone(v string) bool {
	if len(v) > 32 {
		return false
	}
	digits := 0
	for i, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' && i == 0:
		case r == ' ', r == '.', r == '-', r == '(', r == ')':
		default:
			return false
		}
	}
	return digits >= 8 && digits <= 15
}

// validEmail is a shape check, not a delivery check: one `@`, something on each
// side, a dot inside the domain, no whitespace, nothing that closes a tag.
func validEmail(v string) bool {
	if len(v) > 254 || strings.ContainsFunc(v, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '<' || r == '>' || r == '"'
	}) {
		return false
	}
	at := strings.IndexByte(v, '@')
	if at < 1 || at != strings.LastIndexByte(v, '@') {
		return false
	}
	domain := v[at+1:]
	dot := strings.IndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1 && !strings.HasSuffix(domain, ".")
}

// guestSetting is the venue's setting as the portal reads it, or none: a venue
// whose setting cannot be read asks for nothing, rather than refusing guests.
func (s *Server) guestSetting(ctx context.Context, siteID string) gen.GuestDataSettings {
	none := gen.GuestDataSettings{Fields: []gen.GuestDataFieldSetting{}}
	if s.ports.GuestData == nil || siteID == "" {
		return none
	}
	set, err := s.ports.GuestData.GuestDataSettings(ctx, siteID)
	if err != nil {
		return none
	}
	return set
}

// keepGuestInput records what a guest who is now online gave.
//
// Called only on `Connected`, so a refused sign-in leaves nothing. Failures are
// logged by kind and never with the value, and do not undo the sign-in: the
// network is what the guest came for, and a consent that failed to save is the
// safe direction of failure.
func (s *Server) keepGuestInput(ctx context.Context, sess portal.Session, out portal.Outcome, in guestInput) {
	if s.ports.GuestData == nil {
		return
	}
	var grantID *string
	if out.Grant != nil {
		id := out.Grant.ID
		grantID = &id
		d := in.details
		d.GrantID, d.SiteID = id, sess.SiteID
		if err := s.ports.GuestData.RecordGuestDetails(ctx, d); err != nil {
			slog.Default().Warn("guest details not recorded", "site", sess.SiteID, "error", err.Error())
		}
	}
	if in.consent == nil {
		return
	}
	c := *in.consent
	c.SiteID, c.ClientMAC, c.GrantID = sess.SiteID, sess.ClientMAC, grantID
	if err := s.ports.GuestData.RecordMarketingConsent(ctx, c); err != nil && !errors.Is(err, ErrUnknownConsentVersion) {
		slog.Default().Warn("marketing consent not recorded", "site", sess.SiteID, "error", err.Error())
	}
}

// WithdrawOwnMarketingConsent is the guest's own way out, from the portal.
//
// It answers the same `303` whether or not anything was withdrawn: whether a device
// ever consented is not something a stranger holding its MAC should learn by
// pressing a button.
//
// **Only a session that holds a grant withdraws anything** — the browser that
// signed in, carrying the cookie it was given. A session with no grant is refused
// even when `VerifyPresence` confirms its MAC now: presence proves a MAC is on the
// network, not that the caller is that device, and MACs are visible over the air.
// Otherwise anybody on the portal could claim an online guest's MAC in a fresh
// session and withdraw that guest's consent. A withdrawal nobody asked for is not the safe direction either: it
// erases the venue's record of what a guest agreed to on a stranger's say-so.
//
// The field agrees on where the line sits. Keycloak's account console revokes an
// application's consent only from the user's own authenticated session, and so
// does authentik's user interface; neither accepts an identifier the caller names.
// Here the authenticated session is the grant's: it was issued after a presence
// check to exactly this cookie, and the withdrawal button is only rendered on the
// connected page, which only a session holding a grant can reach — so the guest
// who can see the button is the guest this accepts. A guest who lost that browser
// asks the venue, whose console withdraws on their behalf
// (`withdrawMarketingConsent`).
func (s *Server) WithdrawOwnMarketingConsent(ctx context.Context, request gen.WithdrawOwnMarketingConsentRequestObject) (gen.WithdrawOwnMarketingConsentResponseObject, error) {
	if s.portal == nil {
		return nil, errNotImplemented
	}
	var lang string
	if request.Body != nil {
		lang = languageOf(request.Body.Lang)
	}
	done := portalRedirect{location: portal.Notice(portal.PageConnected, lang, portal.NoticeConsentWithdrawn)}
	sess, ok := guestFrom(ctx)
	if !ok || s.ports.GuestData == nil || sess.GrantID == "" {
		return done, nil
	}
	if _, err := s.ports.GuestData.WithdrawDeviceConsents(ctx, sess.SiteID, sess.ClientMAC); err != nil {
		slog.Default().Warn("marketing consent not withdrawn", "site", sess.SiteID, "error", err.Error())
		return portalRedirect{location: portal.Failure(portal.PageConnected, lang, portal.ReasonGeneric, "")}, nil
	}
	return done, nil
}

func (p portalRedirect) VisitWithdrawOwnMarketingConsentResponse(w http.ResponseWriter) error {
	return p.write(w)
}

// portalDataCollection is the guest's narrowing of the setting: the fields, and the
// consent texts only when the venue is asking.
func portalDataCollection(set gen.GuestDataSettings) gen.PortalDataCollection {
	out := gen.PortalDataCollection{Fields: set.Fields}
	if out.Fields == nil {
		out.Fields = []gen.GuestDataFieldSetting{}
	}
	if set.MarketingConsent.Enabled {
		out.MarketingConsent = &gen.PortalMarketingConsent{
			Text: set.MarketingConsent.Text, Versions: set.MarketingConsent.Versions,
		}
	}
	return out
}
