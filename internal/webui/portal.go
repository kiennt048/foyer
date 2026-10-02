package webui

import (
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

// portalTree is the guest portal's eleven documents, minus the one that must not
// be served.
type portalTree struct{ routes routes }

func newPortalTree(fsys fs.FS) *portalTree {
	r := load(fsys, portalCacheControl)
	// ── the root stub is dropped, deliberately ───────────────────────────────
	//
	// `web/portal/dist/index.html` is the build's static best-effort: a
	// `<meta http-equiv="refresh">` to `/en/` with a script in front of it.
	// `build.ts` says in place that it **cannot do its job** — a static file
	// cannot see a request, so the meta refresh necessarily drops the query
	// string, and with it the `id` and `ap` that `VerifyPresence` is the only
	// evidence for. It then names the Go binary as what must replace it.
	//
	// This is that replacement. Deleting the route here means `/` belongs
	// unambiguously to `EntryHandler`, and there is no arrangement of a caller's
	// mux in which the stub can win — including the arrangement where somebody
	// mounts this handler first.
	delete(r, "/")
	return &portalTree{routes: r}
}

// built is true once at least one **language landing** exists — `/en/`, not merely
// some document somewhere.
//
// ── the reason ───────────────────────────────────────────────────────────────
//
// The distinction is not the root stub: `len(routes) > 0` is NOT satisfied by a
// tree holding only `index.html`, because [newPortalTree] deletes `/` before this
// ever runs, so the stub cannot make an empty tree look populated. That is true of
// the tree on disk and false of the tree this method reads.
//
// The world it actually separates is **a tree with sub-pages and no landing** —
// `en/voucher/` but no `en/`, which is what a partial sync or a renamed page
// produces. `len > 0` calls that built, `EntryHandler` then redirects to `/en/`,
// and `/en/` is a `404` — a guest with no document, one step further along.
func (t *portalTree) built() bool {
	for urlPath := range t.routes {
		if strings.Count(urlPath, "/") == 2 && strings.HasSuffix(urlPath, "/") {
			return true
		}
	}
	return false
}

// languagesOf is the set of language landings the tree carries: the top-level
// directories, which is what a language is on disk.
//
// Derived from the tree rather than restated. The list lives in TypeScript
// (`web/portal/src/i18n/index.ts`), Go cannot import it, and a second copy of it
// here would drift the day a language is added — silently, since a missing document
// is only visible to a guest whose phone asked for that language.
func languagesOf(t *portalTree) []string {
	var out []string
	for p := range t.routes {
		if strings.Count(p, "/") == 2 && strings.HasSuffix(p, "/") {
			out = append(out, strings.Trim(p, "/"))
		}
	}
	sort.Strings(out)
	return out
}

// portalCacheControl is `no-store`, for every document, always.
//
// `docs/rules/frontend.md` decides this, and the reasoning is not about
// freshness policy — it is about two clients that have no cache to reason about.
// Android calls `clearCache(true)` on **every** portal launch, and the iOS Captive
// Network Assistant is a `WKWebView` on an ephemeral `WKWebsiteDataStore` whose
// cookies, `localStorage`, IndexedDB and HTTP cache are memory-only and destroyed
// when the window closes. A validator buys nothing on the two clients that matter:
// there is nothing to revalidate against.
//
// What a cache *can* still do here is hold a stale document in front of a repaired
// portal — an intermediary, a desktop browser, a phone that is neither of those
// two. That is strictly a way to lose, which is why this is `no-store` rather than
// `no-cache`. The documents are 11–13 kB with their CSS and JS already inlined, and
// the request that fetches one is the only request the document makes.
func portalCacheControl(string) string { return "no-store" }

// resolve answers what should happen to a portal path: a document, a canonical
// redirect to the same path with its trailing slash, or nothing.
func (t *portalTree) resolve(urlPath string) (a *asset, redirect string, ok bool) {
	p := cleanPath(urlPath)
	if hiddenSegment(p) {
		return nil, "", false
	}
	if a, found := t.routes[p]; found {
		return a, "", true
	}
	// `/en/voucher` for `/en/voucher/`. Not a route the portal generates — its own
	// links carry the slash — so this is a guest who typed it, or a walled-garden
	// entry that dropped it.
	if !strings.HasSuffix(p, "/") {
		if _, found := t.routes[p+"/"]; found {
			return nil, p + "/", true
		}
	}
	return nil, "", false
}

func (t *portalTree) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, redirect, ok := t.resolve(r.URL.Path)
	switch {
	case !ok:
		http.NotFound(w, r)
	case redirect != "":
		// ── the query string comes with it ───────────────────────────────────
		//
		// The same rule `EntryHandler` carries, for the same reason and with the
		// same consequence for getting it wrong. A guest never arrives at a bare
		// path: the NAS appends `ap`, `id`, `t`, `ssid` and `url`, and those are
		// the **only** evidence `VerifyPresence` has to work from before any grant
		// exists (`docs/rules/security.md` §4), plus the only source for the
		// success page's "continue to where you were going".
		//
		// Relayed verbatim and not parsed. Every one of these parameters is
		// unsigned and attacker-controlled; forwarding them is transport, and they
		// are still untrusted at the far end.
		target := redirect
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		w.Header().Set("Cache-Control", "no-store")
		// `302` rather than `301`, to match `EntryHandler`. A permanent redirect is
		// cached by the user agent, and a captive portal is the one place where a
		// client holding onto last week's routing decision is a device that cannot
		// get online and cannot be told why.
		http.Redirect(w, r, target, http.StatusFound)
	default:
		a.serve(w, r)
	}
}
