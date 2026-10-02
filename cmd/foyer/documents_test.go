package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/portal"
)

// Guest documents rendered per request, driven through the real listener
// composition: the same mux a guest's phone reaches, with a venue behind it.
//
// These tests exist because the embedded documents answer every venue with the
// catalogue defaults — "Foyer", both methods, catalogue terms — and no test
// that fetches a static route can see it. A document carrying the wrong
// venue's name is a 200 with valid HTML, which is exactly what green means
// while a guest reads about a business they are not standing in.

func TestGuestWelcomeRendersWithoutASession(t *testing.T) {
	ports, _ := testPortalPorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}
	listener := portalListener(testConfig(), srv, svc, testObservability(t))
	ts := httptest.NewServer(listener.srv.Handler)
	t.Cleanup(ts.Close)

	// No session, no claim: the catalogue defaults, never a refusal. A guest
	// who typed the venue's address renders Foyer rather than a 403.
	resp, err := ts.Client().Get(ts.URL + "/en/")
	if err != nil {
		t.Fatalf("GET /en/: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /en/: %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if !strings.Contains(string(body), "Foyer") {
		t.Error("default welcome names no venue")
	}
	if !strings.Contains(string(body), "<!doctype html>") {
		t.Error("not a document")
	}
}

// TestGuestUnknownPathsAreNotDocuments — a mistyped URL is a 404, never a
// venue page. Serving a fallback for one would turn every scan, prefetch and
// typo into a grant-capable-looking page.
func TestGuestUnknownPathsAreNotDocuments(t *testing.T) {
	ports, _ := testPortalPorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}
	listener := portalListener(testConfig(), srv, svc, testObservability(t))
	ts := httptest.NewServer(listener.srv.Handler)
	t.Cleanup(ts.Close)

	for _, path := range []string{"/en/nope/", "/de/", "/en/../../etc/passwd"} {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

// TestGuestDocumentsDoNotMintSessions — a GET that created a session row for
// every prefetch, preview and crawler would fill the session table with
// browsers nobody sent. Fetch two documents with no claim and require the
// store to stay empty.
func TestGuestDocumentsDoNotMintSessions(t *testing.T) {
	ports, mem := testPortalPorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}
	listener := portalListener(testConfig(), srv, svc, testObservability(t))
	ts := httptest.NewServer(listener.srv.Handler)
	t.Cleanup(ts.Close)

	// Anonymous, plus a forged claim for a MAC the front door never met: the
	// case that minted through `Resolve` even with the cookie discarded. A row
	// for it would be a session for a browser nobody sent, fillable by anyone
	// within radio range one GET at a time.
	for _, path := range []string{"/en/", "/vi/voucher/", "/en/?id=99:88:77:66:55:44&ap=11:22:33:44:55:66"} {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if n := len(mem.Sessions()); n != 0 {
		t.Errorf("%d sessions exist after anonymous and forged document fetches — a GET must not mint rows", n)
	}
}
