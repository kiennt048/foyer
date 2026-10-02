package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// TestAnExportIsNotANavigation: an export is a GET authenticated by a
// `SameSite=Lax` cookie, which a browser sends on a cross-site top-level
// navigation — so without more, a link on any page would make a signed-in
// administrator download a CSV of personal data and write a `data-exported` audit
// entry in their name. It therefore needs the CSRF token the console sends on
// every fetch, and a request the browser marks cross-site or same-site is refused
// whatever it carries.
func TestAnExportIsNotANavigation(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)
	audited := func() int { return len(worldAuditOf(r.store, gen.AuditKindDataExported)) }

	send := func(token, fetchSite string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet,
			r.server.URL+api.BasePath+"/exports/sessions?"+rangeQuery(at(-7), at(1)), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(r.cookie)
		if token != "" {
			req.Header.Set("X-Foyer-CSRF", token)
		}
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		resp, err := r.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	before := audited()
	for _, c := range []struct {
		name, token, site string
	}{
		{"a navigation: the cookie and nothing else", "", ""},
		{"a cross-site navigation", "", "cross-site"},
		{"a cross-site request that somehow carries the token", r.csrf, "cross-site"},
		{"a page on the guest portal — the same site, another port", r.csrf, "same-site"},
		{"a wrong token", "not-the-token", "same-origin"},
	} {
		resp := send(c.token, c.site)
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, `"required":"csrf"`) {
			t.Errorf("%s: %d %s, want 403 naming csrf", c.name, resp.StatusCode, body)
		}
	}
	if n := audited(); n != before {
		t.Errorf("refused exports wrote %d data-exported entries", n-before)
	}

	// The console's own fetch: same origin, token set.
	resp := send(r.csrf, "same-origin")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the console's own export answered %d", resp.StatusCode)
	}
	if audited() != before+1 {
		t.Error("the console's export was not audited")
	}
}
