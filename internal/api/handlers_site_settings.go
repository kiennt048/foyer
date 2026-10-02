package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Portal settings and the walled garden — the two site-scoped surfaces.

// portalBudgets is the three byte figures the portal is bounded by, **read out of
// the contract rather than copied from it**.
//
// ── why these are not constants ──────────────────────────────────────────────
//
// The venue photograph's limit rests on one measurement — a real photograph
// downscaled to 900 px, the macOS CNA's full window width, is 89,697 B at JPEG
// q70 — and two hand-written copies of it drift: a server constant of 200 kB
// beside a contract figure of 100 kB means *"a console that accepts a 200 kB
// photograph and a CI gate that rejects it produces an operator who has done
// nothing wrong and a build that is red."* `web/tools/check-budget.mjs` is the second
// gate and only sees the built tree, so a server more generous than the gate
// refuses nothing and defers the refusal to CI — after the operator has already
// succeeded.
//
// The figure is the contract's 100 kB, and it is not written here: that would be
// a second hand-maintained copy (`docs/rules/database.md` — derive the stand-in
// from what it stands in for). `web/contract/openapi.yaml` declares `x-max-decoded-bytes` beside
// `HeroImageUpload.dataBase64`, `oapi-codegen` embeds the document verbatim, and
// this reads it back.
//
// ── and the logo has the disagreement's worse cousin ─────────────────────────
//
// A limit held only in TypeScript (`LOGO_MAX_BYTES`) binds a console and binds no
// server: a `PATCH /sites/{id}/portal` with a **40 960 byte** `logoDataUri` would
// be answered `200 OK` and stored, where `web/mock` refuses it, and the
// operator's evidence that the logo was acceptable would be the server accepting
// it. A logo is *inside* the document, so an unbounded one blows the byte budget
// that `web/tools/check-budget.mjs` hard-fails on, and it is paid on every portal
// launch because Android clears the cache each time.
//
// The figure sits on `PortalSettingsUpdate.logoDataUri`, the field it refuses,
// and is read here the same way.
//
// ── the third one bounds no field ────────────────────────────────────────────
//
// `codeBudgetBytes` is not a ceiling on any request — nothing a client sends can
// violate it — so it has no field to sit beside and is declared on
// `PortalSettings.budgets.codeBudgetBytes` as `x-budget-bytes`, which is where it
// is reported. It is read rather than written here for the same reason as the
// other two.
//
// **This panics rather than falling back.** The specification is compiled into
// this binary, so the extensions are present or the build is wrong — a condition
// every test in this package trips over on the first request, which is what a
// silent default would hide. A default here would be the copy again, wearing a
// different hat.
var portalBudgets = mustPortalBudgets()

// portalByteBudgets is the read-back set. Unexported and unstored: none of these
// is a fact about a site, so no repository has a row for one and no adapter emits
// one — `internal/api` fills `PortalSettings.budgets` on the way out, which is why
// both the memory fake and `internal/apistore` report the same three numbers
// without either of them knowing what they are.
type portalByteBudgets struct {
	logo  int
	image int
	code  int
}

// specSchema is as much of an OpenAPI schema as reading three integers needs.
//
// Recursive on `properties` because one of the three lives one level down, inside
// `PortalSettings.budgets`.
//
// `encoding/json` over `gen.GetSpecJSON()` rather than `kin-openapi`: the raw
// bytes are enough for three integers, and `go.mod` is frozen — `getkin/kin-openapi`
// is an indirect dependency today and importing it here would promote it.
type specSchema struct {
	MaxDecodedBytes *int                  `json:"x-max-decoded-bytes"`
	BudgetBytes     *int                  `json:"x-budget-bytes"`
	Properties      map[string]specSchema `json:"properties"`
}

func mustPortalBudgets() portalByteBudgets {
	raw, err := gen.GetSpecJSON()
	if err != nil {
		panic("api: the embedded OpenAPI document could not be read: " + err.Error())
	}
	var doc struct {
		Components struct {
			Schemas map[string]specSchema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic("api: the embedded OpenAPI document is not JSON: " + err.Error())
	}
	pick := func(s specSchema) *int { return s.MaxDecodedBytes }
	return portalByteBudgets{
		image: mustDeclaredBytes(doc.Components.Schemas, pick, "x-max-decoded-bytes",
			"HeroImageUpload", "dataBase64"),
		logo: mustDeclaredBytes(doc.Components.Schemas, pick, "x-max-decoded-bytes",
			"PortalSettingsUpdate", "logoDataUri"),
		code: mustDeclaredBytes(doc.Components.Schemas,
			func(s specSchema) *int { return s.BudgetBytes }, "x-budget-bytes",
			"PortalSettings", "budgets", "codeBudgetBytes"),
	}
}

// mustDeclaredBytes walks `schemas[path[0]].properties[path[1]]…` and reads one
// figure off the leaf.
//
// The path is walked rather than reached into so that a schema renamed out from
// under this says *which* hop was missing, instead of a zero value that reads
// identically to a document that declares nothing.
func mustDeclaredBytes(schemas map[string]specSchema, of func(specSchema) *int, extension string, path ...string) int {
	node, ok := schemas[path[0]]
	if !ok {
		panic("api: the contract has no schema " + path[0] +
			"; a byte ceiling cannot be read from a schema that is not there")
	}
	where := path[0]
	for _, hop := range path[1:] {
		next, ok := node.Properties[hop]
		if !ok {
			panic("api: the contract's " + where + " has no property " + hop +
				"; nothing here may substitute a figure for one the document does not declare")
		}
		node, where = next, where+"."+hop
	}
	n := of(node)
	if n == nil || *n <= 0 {
		panic("api: " + where + " declares no " + extension + "; the contract is the " +
			"authority for that figure and nothing here may substitute one")
	}
	return *n
}

func (s *Server) GetPortalSettings(ctx context.Context, request gen.GetPortalSettingsRequestObject) (gen.GetPortalSettingsResponseObject, error) {
	if s.ports.Portal == nil {
		return nil, errNotImplemented
	}
	set, err := s.ports.Portal.PortalSettings(ctx, string(request.SiteId))
	if err != nil {
		return nil, err
	}
	// Where the guest portal is actually served. Answered from the process, not
	// the store: no per-site row holds it, and a console-constructed origin is
	// right in development and wrong everywhere else. A deployment that never
	// set one still serves the portal, so an empty value answers null rather
	// than a guess.
	if u := s.PortalURL(); u != "" {
		set.PortalUrl = &u
	}
	set.Design = normaliseDesign(set.Design)
	return gen.GetPortalSettings200JSONResponse(withBudgets(set)), nil
}

// withBudgets attaches the three figures the console sizes an upload against.
//
// **Here rather than in a store.** `budgets` is `required`-membered, and were it
// left to each adapter to emit, a console missing it would fall back to numbers it
// had typed in — which is how a limit and a ceiling come to disagree in the first
// place. None of the three is a fact about a
// site: they are the shape of the product, read from the document this binary was
// generated from. A per-site row for one would be a value that can drift from the
// gate that enforces it, which is the defect rather than the fix.
//
// The struct literal restates the generated anonymous type because Go cannot name
// one — and that restatement is a *compile-time* check rather than a copy: change
// the contract's `budgets` properties and this stops building, which is exactly
// the failure a silent second copy would not have.
func withBudgets(set gen.PortalSettings) gen.PortalSettings {
	set.Budgets = &struct {
		CodeBudgetBytes int `json:"codeBudgetBytes"`
		ImageMaxBytes   int `json:"imageMaxBytes"`
		LogoMaxBytes    int `json:"logoMaxBytes"`
	}{
		CodeBudgetBytes: portalBudgets.code,
		ImageMaxBytes:   portalBudgets.image,
		LogoMaxBytes:    portalBudgets.logo,
	}
	return set
}

func (s *Server) UpdatePortalSettings(ctx context.Context, request gen.UpdatePortalSettingsRequestObject) (gen.UpdatePortalSettingsResponseObject, error) {
	if s.ports.Portal == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body

	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}

	if in.VenueName != nil && strings.TrimSpace(*in.VenueName) == "" {
		errs["venueName"] = "Required."
		faults["venueName"] = gen.FieldFaultRequired
	}
	// **An empty `methods` is a portal no guest can use, and it fails silently:**
	// the welcome page renders, the guest reads it, and there is nothing to
	// press. Same for `languages`.
	if in.Methods != nil && len(*in.Methods) == 0 {
		errs["methods"] = "At least one way to sign in."
		faults["methods"] = gen.FieldFaultRequired
	}
	if in.Languages != nil && len(*in.Languages) == 0 {
		errs["languages"] = "At least one language."
		faults["languages"] = gen.FieldFaultRequired
	}
	// Click-through is consent-only access, so the window is the whole of what
	// the venue granted. Without it the grant has no end.
	if in.Methods != nil && hasClickThrough(*in.Methods) && in.ClickThroughMinutes != nil && *in.ClickThroughMinutes < 1 {
		errs["clickThroughMinutes"] = "Required with click-through."
		faults["clickThroughMinutes"] = gen.FieldFaultRequiredWithClickThrough
	}
	// The logo is a data URI or a same-origin path, never another origin. A data
	// URI is inlined and spent from the code budget; a path is served by the
	// portal itself, whose address is already required to be in the walled
	// garden. An external origin would have to be added on every driver and
	// would be refused here rather than debugged there.
	//
	// The shape is the portal renderer's own `safeHeroUrl`, not a second rule:
	// root-relative, no protocol-relative `//`, no whitespace or quote that
	// breaks out of the attribute it lands in.
	if in.LogoDataUri != nil && *in.LogoDataUri != "" &&
		!strings.HasPrefix(*in.LogoDataUri, "data:image/") && !isSameOriginPath(*in.LogoDataUri) {
		errs["logoDataUri"] = "Not an inline image or a portal path."
		faults["logoDataUri"] = gen.FieldFaultNotADataUri
	}
	// The page design: the rules beyond the schema, first failure wins, one key
	// for the whole design. The schema's own violations (lengths, enums, the
	// accent pattern) were answered by the request validator before this ran,
	// against paths like `design.blocks.0.content.vi.title`.
	if in.Design != nil {
		if fault, block, bad := designFault(*in.Design); bad {
			errs["design"] = designFaultProse(fault, block)
			faults["design"] = fault
		}
	}
	// Phone sign-in is offered only where a provider can send. A button that
	// cannot send a code is a control that lies, to the guest standing in front
	// of it.
	if in.Methods != nil && hasPhone(*in.Methods) && !s.phoneOffered(ctx, string(request.SiteId)) {
		errs["methods"] = "Configure phone sign-in before offering it."
		faults["methods"] = gen.FieldFaultNeedsPhoneProvider
	}
	if len(faults) > 0 {
		return nil, validation(errs, faults)
	}

	// **And it has to fit.** Shape first, size second, and size on its own return
	// because `/problems/too-large` is a different refusal from `/problems/valid-
	// ation` and carries `bytes` and `limitBytes` — the console renders the two
	// figures, never a sentence containing them.
	//
	// Decoded, not encoded, for the reason `putPortalHeroImage` gives: base64
	// inflates by a third, so a ceiling on the submitted string would be a third
	// more generous than the one that matters, and what matters is the bytes the
	// guest's phone waits for. It waits for these bytes *before the first word*,
	// because a data URI is inside the document.
	if in.LogoDataUri != nil && strings.HasPrefix(*in.LogoDataUri, "data:") {
		n := dataURIBytes(*in.LogoDataUri)
		if n < 0 {
			return nil, validation(
				map[string]string{"logoDataUri": "Not an inline image."},
				map[string]gen.FieldFault{"logoDataUri": gen.FieldFaultNotADataUri},
			)
		}
		if n > portalBudgets.logo {
			return nil, tooLarge("logoDataUri", gen.FieldFaultTooLarge, map[string]any{
				"bytes": n, "limitBytes": portalBudgets.logo,
			})
		}
	}

	// The settings in force **now**, copied before the write: the entry is a
	// difference between these two states, and a store that amended its own
	// value in place would otherwise have rewritten the `from` side.
	before, err := s.ports.Portal.PortalSettings(ctx, string(request.SiteId))
	if err != nil {
		return nil, err
	}
	was := snapshotPortalAudit(before)

	set, err := s.ports.Portal.AmendPortalSettings(ctx, string(request.SiteId), in)
	if err != nil {
		return nil, err
	}
	now := snapshotPortalAudit(set)

	who := callerFrom(ctx)
	subject := gen.AuditSubject{Kind: gen.AuditSubjectKindSite, Id: set.SiteId, Name: ptr(set.VenueName)}
	// Terms are recorded separately from the rest of the settings, because they
	// are legal text a venue authored: "what did the terms say on the day this
	// guest agreed" is a different question from "who renamed the venue".
	//
	// **Both rows are written from the difference, not from the fields the
	// request mentioned** (`audit_portal.go`). A PATCH names every field the
	// console holds, so deciding from the request records a change to each of
	// them on every save — including the save an operator makes after reading a
	// form and pressing the button. An empty difference writes no entry.
	if changed := termsChanges(was, now); len(changed) > 0 {
		s.recordIn(ctx, who, set.SiteId, gen.AuditKindPortalTermsChanged, subject, map[string]any{"changed": changed})
	}
	if changed := settingsChanges(was, now); len(changed) > 0 {
		s.recordIn(ctx, who, set.SiteId, gen.AuditKindPortalSettingsChanged, subject, map[string]any{"changed": changed})
	}
	// The console re-renders from the write response, so the origin it previews
	// from must be on it too — a `portalUrl` present on GET and absent on PATCH
	// is a preview that vanishes the moment the operator saves.
	if u := s.PortalURL(); u != "" {
		set.PortalUrl = &u
	}
	set.Design = normaliseDesign(set.Design)
	return gen.UpdatePortalSettings200JSONResponse(withBudgets(set)), nil
}

func (s *Server) PutPortalHeroImage(ctx context.Context, request gen.PutPortalHeroImageRequestObject) (gen.PutPortalHeroImageResponseObject, error) {
	if s.ports.Portal == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body

	if in.DataBase64 == "" {
		return nil, validation(
			map[string]string{"dataBase64": "Required."},
			map[string]gen.FieldFault{"dataBase64": gen.FieldFaultRequired},
		)
	}
	switch in.ContentType {
	case gen.Imagejpeg, gen.Imagepng, gen.Imagewebp:
	default:
		return nil, validation(
			map[string]string{"contentType": "Not a format the portal can serve."},
			map[string]gen.FieldFault{"contentType": gen.FieldFaultUnsupportedImageFormat},
		)
	}

	// Decoded, not encoded. Base64 inflates by a third, so a ceiling applied to
	// the submitted string would be a third more generous than the one that
	// matters — and what matters is the bytes the guest's phone waits for.
	data, err := base64.StdEncoding.DecodeString(in.DataBase64)
	if err != nil {
		return nil, validation(
			map[string]string{"dataBase64": "Not readable."},
			map[string]gen.FieldFault{"dataBase64": gen.FieldFaultNotADataUri},
		)
	}
	if len(data) > portalBudgets.image {
		return nil, tooLarge("dataBase64", gen.FieldFaultTooLarge, map[string]any{
			"bytes": len(data), "limitBytes": portalBudgets.image,
		})
	}

	img, err := s.ports.Portal.StoreHeroImage(ctx, string(request.SiteId), in.Filename, string(in.ContentType), data)
	if err != nil {
		return nil, err
	}
	return gen.PutPortalHeroImage200JSONResponse(img), nil
}

func (s *Server) DeletePortalHeroImage(ctx context.Context, request gen.DeletePortalHeroImageRequestObject) (gen.DeletePortalHeroImageResponseObject, error) {
	if s.ports.Portal == nil {
		return nil, errNotImplemented
	}
	if err := s.ports.Portal.DeleteHeroImage(ctx, string(request.SiteId)); err != nil {
		return nil, err
	}
	return gen.DeletePortalHeroImage204Response{}, nil
}

// VenueAssetHandler serves a site's venue photograph as raw bytes.
//
// Public by necessity: the `<img>` must load pre-authentication on a client
// with no default route, and the portal's own address is already a required
// walled-garden entry — same-origin egress is reachable by construction. The
// site id is in the path, never in a session, so there is nothing to scope by
// beyond it; an unknown site is 404 rather than 403, because the metadata for
// it is already public through `PortalContext`.
//
// Out of the contract on purpose: raw bytes are not JSON, and the generated
// strict handlers only speak the contract's media types. Mounted by `cmd/foyer`
// on the portal listener, beside — never inside — the `/api/v0` tree.
func (s *Server) VenueAssetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.ports.Portal == nil {
			http.NotFound(w, r)
			return
		}
		siteID, ok := venueAssetSite(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		contentType, data, err := s.ports.Portal.HeroImageBytes(r.Context(), siteID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		// No-store, like every portal document: the URL is content-type-stable
		// but content-unstable — re-upload replaces the row at the same URL —
		// so a cached copy is a stale photograph, not a fast one.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	})
}

// venueAssetSite resolves `/venue/{siteID}/...` to the site id. The trailing
// segment is the filename the uploader gave (or the content-type-derived name
// the adapter uses) — transport, never trusted: the row is resolved by site,
// so a renamed file still serves and a forged one serves nothing new.
func venueAssetSite(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/venue/")
	if !ok || rest == "" {
		return "", false
	}
	siteID, _, ok := strings.Cut(rest, "/")
	if !ok || siteID == "" || strings.Contains(siteID, ".") {
		return "", false
	}
	return siteID, true
}

// ── walled garden ────────────────────────────────────────────────────────────

func (s *Server) GetWalledGarden(ctx context.Context, request gen.GetWalledGardenRequestObject) (gen.GetWalledGardenResponseObject, error) {
	if s.ports.Garden == nil {
		return nil, errNotImplemented
	}
	g, err := s.ports.Garden.Garden(ctx, string(request.SiteId))
	if err != nil {
		return nil, err
	}
	if g.Entries == nil {
		g.Entries = []GardenEntry{}
	}
	if g.Drivers == nil {
		g.Drivers = []gen.GardenDriverCoverage{}
	}
	return gen.GetWalledGarden200JSONResponse(g), nil
}

// CreateGardenEntry allows one destination before authentication.
//
// **Every entry is unauthenticated egress**, so the narrowest form that works is
// the only acceptable one. §5 forbids allowing a destination by CDN IP range:
// modern CDNs host arbitrary customers, so "allow the payment provider" becomes
// "allow a general-purpose HTTPS tunnel". An `ip-range` entry is therefore
// accepted only where no device in the site can do FQDN/SNI.
func (s *Server) CreateGardenEntry(ctx context.Context, request gen.CreateGardenEntryRequestObject) (gen.CreateGardenEntryResponseObject, error) {
	if s.ports.Garden == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body
	site := string(request.SiteId)

	value := strings.TrimSpace(in.Value)
	if value == "" {
		return nil, validation(
			map[string]string{"value": "Required."},
			map[string]gen.FieldFault{"value": gen.FieldFaultRequired},
		)
	}
	switch in.Kind {
	case gen.GardenEntryKindFqdn:
		if !looksLikeHostname(value) {
			return nil, validation(
				map[string]string{"value": "Not a hostname."},
				map[string]gen.FieldFault{"value": gen.FieldFaultNotAHostname},
			)
		}
	case gen.GardenEntryKindIpRange:
		if _, _, err := net.ParseCIDR(value); err != nil {
			return nil, validation(
				map[string]string{"value": "Not a CIDR range."},
				map[string]gen.FieldFault{"value": gen.FieldFaultNotACidr},
			)
		}
	}

	entry, err := s.ports.Garden.AddGardenEntry(ctx, site, gen.GardenEntryCreate{
		Kind: in.Kind, Value: value, Note: in.Note,
	})
	if err != nil {
		if isConflict(err) {
			return nil, duplicate(value)
		}
		return nil, err
	}
	s.recordIn(ctx, callerFrom(ctx), site, gen.AuditKindGardenEntryAdded, gen.AuditSubject{
		Kind: gen.AuditSubjectKindGardenEntry, Id: entry.Id, Name: ptr(entry.Value),
	}, nil)
	return gen.CreateGardenEntry201JSONResponse(entry), nil
}

// DeleteGardenEntry is refused for an entry whose origin is `required`.
//
// Those are the portal's own FQDN, its certificate chain's OCSP/CRL endpoints
// and the RFC 8908 endpoint: remove one and the portal cannot serve itself,
// which presents to every guest as a network that is simply broken. The console
// does not render a delete control for them at all — this is the second gate,
// for a client that tries anyway.
func (s *Server) DeleteGardenEntry(ctx context.Context, request gen.DeleteGardenEntryRequestObject) (gen.DeleteGardenEntryResponseObject, error) {
	if s.ports.Garden == nil {
		return nil, errNotImplemented
	}
	site := string(request.SiteId)

	g, err := s.ports.Garden.Garden(ctx, site)
	if err != nil {
		return nil, err
	}
	var found *GardenEntry
	for i := range g.Entries {
		if g.Entries[i].Id == request.EntryId {
			found = &g.Entries[i]
			break
		}
	}
	if found == nil {
		return nil, notFound()
	}
	if found.Origin == gen.GardenEntryOriginRequired {
		e := gardenEntryRequired()
		e.Params = map[string]any{"value": found.Value}
		return nil, e
	}

	if err := s.ports.Garden.RemoveGardenEntry(ctx, site, found.Id); err != nil {
		return nil, err
	}
	s.recordIn(ctx, callerFrom(ctx), site, gen.AuditKindGardenEntryRemoved, gen.AuditSubject{
		Kind: gen.AuditSubjectKindGardenEntry, Id: found.Id, Name: ptr(found.Value),
	}, nil)
	return gen.DeleteGardenEntry204Response{}, nil
}

// ── small predicates ─────────────────────────────────────────────────────────

// dataURIBytes is the decoded length of a `data:` URI's payload, or `-1` if the
// string is not one.
//
// **Deliberately the same arithmetic as `dataUriBytes` in
// `web/contract/src/index.ts`**, including the two cases that are easy to get
// wrong: a URI with no `;base64` carries its payload literally, and a base64
// payload's decoded length is `len*3/4` less the padding. The two cannot share an
// implementation across the language seam, so they are held together by refusing
// and accepting at the same figure — which is what
// `TestLogoCeilingIsTheContractsAndIsNotCopiedHere` measures through the boundary
// rather than against this function.
//
// It does not decode. The point is the size of a thing that may be several tens of
// kilobytes and is about to be refused; allocating it first to find out it is too
// big is the shape of the problem, not the measurement of it.
func dataURIBytes(uri string) int {
	comma := strings.Index(uri, ",")
	if !strings.HasPrefix(uri, "data:") || comma < 0 {
		return -1
	}
	meta, payload := uri[len("data:"):comma], uri[comma+1:]
	if !strings.Contains(meta, ";base64") {
		return len(payload)
	}
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '+', r == '/', r == '=':
			return r
		}
		return -1
	}, payload)
	pad := 0
	switch {
	case strings.HasSuffix(clean, "=="):
		pad = 2
	case strings.HasSuffix(clean, "="):
		pad = 1
	}
	if n := len(clean)*3/4 - pad; n > 0 {
		return n
	}
	return 0
}

var hostnamePattern = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func looksLikeHostname(v string) bool { return hostnamePattern.MatchString(v) }

func hasClickThrough(methods []gen.PortalMethod) bool {
	for _, m := range methods {
		if m == gen.PortalMethodClickThrough {
			return true
		}
	}
	return false
}

// isSameOriginPath is the portal renderer's `safeHeroUrl` in Go: root-relative,
// never protocol-relative, nothing that breaks out of the attribute. One shape
// for every linked venue asset rather than one rule per field.
func isSameOriginPath(v string) bool {
	if len(v) < 2 || v[0] != '/' || v[1] == '/' {
		return false
	}
	return !strings.ContainsAny(v, " \t\r\n\"'<>")
}

func isConflict(err error) bool { return errors.Is(err, ErrConflict) }
