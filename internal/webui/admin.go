package webui

import (
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

// assetPrefix is where Vite emits the console's content-hashed files.
const assetPrefix = "/assets/"

// adminTree is the console: one document and a directory of hashed assets.
type adminTree struct {
	routes  routes
	index   *asset
	missing []string // assets index.html references that are not in the tree
}

// assetRef finds what `index.html` loads: Vite emits the entry script and the
// stylesheet as `src="/assets/index-<hash>.js"` and `href="/assets/index-<hash>.css"`.
var assetRef = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

func newAdminTree(fsys fs.FS) *adminTree {
	r := load(fsys, adminCacheControl)
	t := &adminTree{routes: r, index: r["/"]}
	if t.index == nil {
		return t
	}
	for _, m := range assetRef.FindAllSubmatch(t.index.body, -1) {
		if ref := string(m[1]); r[ref] == nil {
			t.missing = append(t.missing, ref)
		}
	}
	if len(assetRef.FindAllSubmatch(t.index.body, -1)) == 0 {
		// A console document that loads nothing is not a console document. Vite
		// always emits the entry script, so zero references means this `index.html`
		// came from somewhere else — the portal's root stub, say, which is also an
		// `index.html` at the root of a tree.
		t.missing = append(t.missing, "(index.html references no assets at all)")
	}
	return t
}

// built is the presence of the console document **and of everything it loads**.
//
// ── the second half matters ───────────────────────────────────────────────────
//
// Asking only whether `index.html` is there would be the shape
// `docs/rules/testing.md` warns about: a check on the cheapest observable rather
// than on the property. `index.html` is 440 bytes and carries no application at all
// — the console is `assets/index-<hash>.js`, 127 kB gzip of it — so a tree holding
// the document and no assets reports *built*, serves `200 text/html`, and renders a
// white screen with a 404 in the browser console.
//
// That is not hypothetical here: the build step syncs two directories, and a copy
// that lands one and not the other, or that leaves a stale hash behind when the
// entry chunk is rebuilt, produces exactly this. So the document's **own** `src` and
// `href` attributes are the authority — derived from the artefact rather than a list
// restated in Go — and every one of them has to resolve.
func (t *adminTree) built() bool { return t.index != nil && len(t.missing) == 0 }

// adminCacheControl splits the console's tree the way its build already splits it.
//
// Vite emits `index.html` referencing `assets/index-<hash>.js`, so the two halves
// have opposite caching properties, and one policy for both gets one of them wrong.
// A hashed file is immutable by construction — its name changes when its bytes do —
// and `index.html` is the mutable pointer at the current set of names.
//
// The console is an operator's tool on a machine with internet, so unlike the portal
// there is a real cache to use and a real reason to: 127 kB gzip of entry chunk on
// every navigation is a bill `web/tools/check-admin-budget.mjs` exists to keep down.
func adminCacheControl(urlPath string) string {
	if strings.HasPrefix(urlPath, assetPrefix) {
		return "public, max-age=31536000, immutable"
	}
	// `no-cache` and not `no-store`: it means *revalidate*, and revalidation here is
	// real — every asset carries an ETag computed from its own bytes, so the usual
	// answer is a `304` of a few hundred bytes rather than the document.
	return "no-cache"
}

func (t *adminTree) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := cleanPath(r.URL.Path)

	if hiddenSegment(p) {
		http.NotFound(w, r)
		return
	}
	if a, ok := t.routes[p]; ok {
		a.serve(w, r)
		return
	}

	// ── the two things the fallback must not swallow ─────────────────────────
	//
	// **`/api/…`.** The console and the admin API are the same origin by design, so
	// a mistyped or removed API path lands here. Answered with the fallback it would
	// be `200 text/html`, and the console would report a JSON parse error — a
	// support call about the wrong thing entirely, in the one place where the right
	// answer, `404`, is already unambiguous.
	//
	// **A miss under `/assets/`.** Those names carry a content hash, so a miss is
	// never a typo — it is a stale or half-copied tree, exactly what a build step
	// that copies over the top without deleting produces. The fallback would answer
	// a `<script src>` with an HTML document, and the console would fail with a
	// syntax error naming a line in a page. `404` says which file is gone.
	if p == "/api" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, assetPrefix) {
		http.NotFound(w, r)
		return
	}

	// Everything else is a client-side route: `/sessions`, `/nas/abc`, a deep link
	// somebody bookmarked. The SPA fallback is what makes a reload survive.
	t.index.serve(w, r)
}
