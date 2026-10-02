package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A guest's form is a document navigation inside the Captive Network Assistant,
// and whatever happens to it the guest gets a page.
//
// The two ways it can fail: a portal form posted **without `lang`** — which the
// contract declares optional — answering `422 application/problem+json` with
// `body: required`, because a request validator fills `Language`'s `default: en`
// into the form and then cannot re-encode a form; and a form the contract does
// refuse (`lang=xx`, no `code`) answering the same JSON document to a
// mini-browser with no back button.

// TestAPortalFormWithoutLangIsServed: `lang` is optional, so its absence is not a
// fault — the form is handled and the guest lands in the default language.
func TestAPortalFormWithoutLangIsServed(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		form       url.Values
	}{
		{"redeemVoucher", "/portal/voucher", url.Values{"code": {goodCode}}},
		// A form with fields but no `lang`. An empty body is the refusal below.
		{"acceptTerms", "/portal/agree", url.Values{"name": {""}}},
		{"loginWithAccount", "/portal/account", url.Values{"username": {guestUser}, "password": {guestPass}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := presentRig(t)
			g.arrive(guestMAC).Body.Close()
			resp := g.send(http.MethodPost, tc.path, tc.form)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status %d (%s), want 303 to the connected page — `lang` is optional",
					resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/en/connected/") {
				t.Errorf("Location is %q, want the connected page in the default language", loc)
			}
		})
	}
}

// TestAPortalFormTheContractRefusesIsAPage: a form the contract refuses goes back
// to the form it came from as a `303`, never as a problem document.
func TestAPortalFormTheContractRefusesIsAPage(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		form       url.Values
		want       string
	}{
		{"a language the portal does not speak", "/portal/voucher",
			url.Values{"code": {goodCode}, "lang": {"xx"}}, "/en/voucher/?e=generic"},
		{"no code at all", "/portal/voucher", url.Values{"lang": {"vi"}}, "/vi/voucher/?e=generic"},
		{"no password", "/portal/account", url.Values{"username": {guestUser}, "lang": {"vi"}}, "/vi/account/?e=generic"},
		{"an empty body", "/portal/agree", url.Values{}, "/en/?e=generic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := presentRig(t)
			g.arrive(guestMAC).Body.Close()
			resp := g.send(http.MethodPost, tc.path, tc.form)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status %d (%s), want 303 — a problem document renders raw JSON in the CNA",
					resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			if loc := resp.Header.Get("Location"); loc != tc.want {
				t.Errorf("Location is %q, want %q", loc, tc.want)
			}
		})
	}
}
