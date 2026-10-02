package api

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/webui"
)

// Guest documents, rendered per request from the venue's own settings.
//
// The embedded documents are pre-rendered with `context: null`, so every
// venue-named slot holds the catalogue default: "Foyer", both methods,
// catalogue terms. Serving them answers the wrong venue on every deployment
// that configured one — the defect this handler exists to close. It renders
// the same five pages from the same settings the JSON API already serves, per
// request, from the row the operator saved.
//
// ── what this is not ─────────────────────────────────────────────────────────
//
//   - Not a sixth portal operation. Documents are not in the contract and must
//     not be: they serve HTML, and the generated strict handlers only speak
//     the contract's media types. This is a plain `http.Handler`, mounted by
//     `cmd/foyer` on the portal listener beside — never inside — `/api/v0`.
//   - Not a presence check and not a grant path. Rendering answers what the
//     venue configured; `Look` answers whether this browser may act on it.
//     The CNA probes document URLs repeatedly while a guest reads, and a
//     controller round trip per probe is a denial of service the venue pays
//     for. No driver call, no grant creation, no credential check, no session
//     minting: a document requested with no claim renders the venue's
//     defaults, exactly as the embedded artefact does.
//   - Not a second renderer. Every slot below delegates to `portal.RenderPage`,
//     which is the twin of the TypeScript renderer — same pages, same
//     fallbacks, same allowlists. This handler resolves *which* document and
//     *which* inputs; the shape of the bytes is the portal package's.
//
// ── scoping ──────────────────────────────────────────────────────────────────
//
// The site comes from the browser's session where one answers, and from
// nothing where none does. A session bound to a site renders that site; a
// request with no session and no claim renders the catalogue defaults rather
// than refusing — refusing here would answer a bare `403` to somebody who
// typed the venue's address into a browser, and `EntryHandler` already refuses
// nothing for the same reason. A claim naming an access point nothing
// registered resolves to no site, not to a default one: a portal that fell
// back to some site for an AP it has never heard of is a portal authorizing
// devices on networks it does not manage.
//
// ── the connected page ───────────────────────────────────────────────────────
//
// It reads the grant projection, never the grant: state, plan, venue echo and
// return link. A browser with no grant behind it reads the not-connected
// shape, not a green tick — a document that can be reloaded, bookmarked and
// reopened by the CNA long after the grant ended must not prove what it
// cannot check.

// GuestDocumentsHandler serves the five guest pages rendered from settings.
func (s *Server) GuestDocumentsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.portal == nil {
			http.NotFound(w, r)
			return
		}
		lang, rest, ok := guestDocPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		in := portal.RenderInput{
			Lang:   portal.RenderLang(portal.Language(lang)),
			Path:   rest,
			Query:  verbatimQuery(r.URL),
			Error:  docReason(q.Get("e")),
			Code:   q.Get("code"),
			Notice: q.Get("n"),
			// Which of the phone page's two forms to show, and nothing more: the
			// code is checked against this browser's session, not this flag.
			PhoneSent: q.Get("sent") == "1",
		}
		var sess portal.Session
		var haveSess bool
		// Lookup, never Resolve: a document fetch must not mint a session row.
		// Prefetches, previews, crawlers and forged links all fetch documents;
		// a GET that created a row for each would fill the table with browsers
		// nobody sent. The front door owns minting; documents only read.
		if got, err := s.portal.Lookup(r.Context(), r); err == nil {
			sess, haveSess = got, true
		}
		// No session and no claim: the deployment's own site, where one answers.
		// A typed address renders the venue rather than the catalogue — the
		// welcome page is a document, not a gate. Unknown APs still resolve to
		// nothing: `Resolve` refuses them rather than defaulting, and so does
		// this. Single-site deployments are the whole fleet today; a second
		// site without a session is what makes this fallback ambiguous, and
		// that deployment does not exist yet.
		siteID := ""
		if haveSess {
			siteID = sess.SiteID
		} else if s.ports.Portal != nil {
			if sites, err := s.ports.Sites.Sites(r.Context()); err == nil && len(sites) == 1 {
				siteID = string(sites[0].Id)
			}
		}
		if siteID != "" && s.ports.Portal != nil {
			if set, err := s.ports.Portal.PortalSettings(r.Context(), siteID); err == nil {
				in.Settings = renderSettings(set, s.siteToday(r.Context(), siteID))
				in.Settings.Data = renderData(s.guestSetting(r.Context(), siteID))
				if m := s.phoneCodeMinutes(r.Context(), siteID, set.Methods); m != nil {
					in.Settings.PhoneCodeMinutes = *m
				}
			}
		}
		if haveSess && (rest == "connected/" || rest == "connected") {
			if g, state, err := s.portal.Status(r.Context(), sess); err == nil {
				st := renderStatus(g, state, sess)
				in.Status = &st
			}
		}
		page := portal.RenderPage(in)
		if page == nil {
			http.NotFound(w, r)
			return
		}
		doc := guestDocument(lang, page.Title, page.Body)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(doc))
		}
	})
}

// guestDocPath splits `/en/...` into the language and the path under it. The
// language is allowlisted — two known strings cannot carry a traversal — and
// the rest is resolved by the renderer, which refuses what is not a document.
func guestDocPath(path string) (lang, rest string, ok bool) {
	if !strings.HasPrefix(path, "/") {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(path, "/")
	slash := strings.Index(trimmed, "/")
	if slash < 0 {
		return "", "", false
	}
	lang, rest = trimmed[:slash], trimmed[slash+1:]
	if portal.Language(lang) != lang {
		return "", "", false
	}
	return lang, rest, true
}

// verbatimQuery carries the request's query onto the language links, so
// switching language keeps the NAS parameters, the return URL and a rejected
// voucher's `?e=`/`?code=`. Relayed, never re-serialised: re-encoding would
// decode `+` as a space and corrupt a `url` a NAS did not percent-encode.
func verbatimQuery(u *url.URL) string {
	if u == nil || u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

// docReason allowlists the failure echo: only reasons the catalogue can render
// reach a document. Anything else renders as no error rather than as a blank
// sentence — an unknown `?e=` is a hand-written link, not a refusal the server
// made.
func docReason(s string) string {
	// `portal.Reasons()` rather than a copy of it: the copy is how a new reason
	// renders as no error at all on the page it was sent to.
	if slices.Contains(portal.Reasons(), portal.Reason(s)) {
		return s
	}
	return ""
}

// renderSettings is the guest-visible narrowing the renderer reads. The design is
// narrowed here, by the same function `getPortalContext` uses, so the server-
// rendered page and the context a client renders from cannot show a guest two
// different sets of blocks on the same day.
func renderSettings(set PortalSetting, today string) *portal.RenderSettings {
	out := &portal.RenderSettings{
		VenueName:   set.VenueName,
		Methods:     genMethods(set.Methods),
		Languages:   genLanguages(set.Languages),
		Terms:       map[string]string{},
		NetworkName: derefStr(set.NetworkName),
		LogoDataURI: derefStr(set.LogoDataUri),
		Design:      renderDesign(narrowDesign(set.Design, today)),
	}
	if set.HeroImage != nil {
		out.HeroImageURL = set.HeroImage.Url
	}
	for k, v := range set.Terms.Summary {
		out.Terms[k] = v
	}
	return out
}

// renderData is `portalDataCollection` for the renderer: the same narrowing the
// JSON context gets, so the served document and the context cannot disagree.
func renderData(set gen.GuestDataSettings) *portal.RenderData {
	dc := portalDataCollection(set)
	out := &portal.RenderData{ConsentText: map[string]string{}, ConsentVersions: map[string]string{}}
	for _, f := range dc.Fields {
		out.Fields = append(out.Fields, portal.RenderField{Field: string(f.Field), Required: f.Required})
	}
	if mc := dc.MarketingConsent; mc != nil {
		for lang, pair := range map[string][2]*string{
			"en": {mc.Text.En, mc.Versions.En},
			"vi": {mc.Text.Vi, mc.Versions.Vi},
		} {
			if pair[0] != nil && pair[1] != nil {
				out.ConsentText[lang], out.ConsentVersions[lang] = *pair[0], *pair[1]
			}
		}
	}
	return out
}

func genMethods(in []gen.PortalMethod) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		out = append(out, string(m))
	}
	return out
}

func genLanguages(in []gen.Language) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		out = append(out, string(l))
	}
	return out
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func renderStatus(g portal.Grant, state portal.State, sess portal.Session) portal.RenderStatus {
	st := portal.RenderStatus{State: string(state), OriginalURL: sess.OriginalURL}
	plan := &portal.RenderPlan{
		Name:            g.Plan.Name,
		NameLang:        g.Plan.NameLang,
		DurationMinutes: g.Plan.DurationMinutes,
		DataAllowanceMB: g.Plan.DataAllowanceMB,
		EnforcedBy:      string(g.Plan.EnforcedBy),
	}
	st.Plan = plan
	return st
}

// guestDocument assembles the envelope around a rendered body: doctype,
// language, head, inlined stylesheet, body. The stylesheet is extracted from
// the embedded artefact's own document — one copy of the bytes, not two — so
// a rendered page wears the same CSS the static build shipped. Where no
// artefact answers, the body still renders unstyled rather than refusing: a
// guest with no stylesheet is better served than a guest with no page.
func guestDocument(lang, title, body string) string {
	return `<!doctype html>
<html lang="` + lang + `">
<head>
<meta charset="utf-8">
<title>` + portalEscape(title) + `</title>
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="color-scheme" content="dark light">
<meta name="robots" content="noindex,nofollow">
<meta name="referrer" content="no-referrer">
<style>` + webui.PortalCSS() + `</style>
</head>
<body>
` + body + `
</body>
</html>
`
}

func portalEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
