package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
)

// Guest documents rendered from settings, driven against the handler with a
// venue behind it: the welcome page names the venue, offers its methods, and
// carries its terms; the query survives onto the language links; failures echo
// through `?e=`; the connected page reads the grant projection.
func TestGuestDocumentsRenderTheVenue(t *testing.T) {
	store := world(t)
	srv, err := api.New(api.Config{
		Ports:  store.Ports(),
		Portal: store.PortalPorts(newGuestDriver()),
		Now:    func() time.Time { return at(0) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	// Auth through the console surface; settings and documents on their own
	auth := httptest.NewServer(srv.Handler())
	t.Cleanup(auth.Close)
	r := &rig{t: t, server: auth, store: store}
	r.signIn(adminUser)

	docs := httptest.NewServer(srv.GuestDocumentsHandler())
	t.Cleanup(docs.Close)

	// The fixture venue offers one method; rename it so the assertion names the
	// row rather than the fallback.
	patch := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{
		"venueName": "Quán Cà Phê Số 7",
		"methods":   []string{"voucher"},
		"languages": []string{"vi"},
		"terms":     map[string]any{"summary": map[string]string{"vi": "Nội quy nhà."}},
	})
	patch.Body.Close()
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d", patch.StatusCode)
	}

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := docs.Client().Get(docs.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, body := get("/vi/?id=aa&ap=bb")
	if code != http.StatusOK {
		t.Fatalf("GET /vi/: %d", code)
	}
	for _, want := range []string{
		"Quán Cà Phê Số 7", "Nội quy nhà.", `href="/vi/voucher/?id=aa`,
		"<title>Kết nối Wi-Fi · Quán Cà Phê Số 7</title>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("welcome lacks %q", want)
		}
	}
	if strings.Contains(body, "I have an account") {
		t.Error("welcome offers an account the venue did not configure")
	}
	if strings.Contains(body, "Internet access is provided as is") {
		t.Error("welcome carries catalogue terms beside the venue's own")
	}

	// Unknown error reasons read generic, never blank and never echoed: the
	// catalogue renders what it can, and a hand-written link is not a refusal
	// the server made.
	if _, problem := get("/vi/problem/?e=no-such-reason"); !strings.Contains(problem, "Chưa kết nối được") {
		t.Error("problem page without a known reason renders no title")
	} else if strings.Contains(problem, `value="no-such-reason"`) || strings.Contains(problem, ">no-such-reason<") {
		t.Error("unknown reason leaks into copy or a field — the query surviving on links is transport, not content")
	} else if !strings.Contains(problem, "Có lỗi xảy ra") {
		t.Error("unknown reason renders no generic sentence")
	}
	if _, known := get("/vi/problem/?e=not-found"); !strings.Contains(known, "Không tìm thấy mã này") {
		t.Error("known reason renders no sentence")
	}

	// A rejected code comes back normalised into the field.
	if _, voucher := get("/vi/voucher/?e=not-found&code=abcd-efgh-jkmn"); !strings.Contains(voucher, `value="ABCD-EFGH-JKMN"`) {
		t.Error("rejected code does not echo normalised into the field")
	}

	// No grant behind this browser: the connected shape, not a green tick.
	if _, connected := get("/vi/connected/"); !strings.Contains(connected, "Chưa kết nối được") {
		t.Error("connected without a grant renders no not-connected shape")
	} else if strings.Contains(connected, "Bạn đã có mạng") {
		t.Error("connected without a grant shows the online tick")
	}
}

func TestGuestDocumentsNeedNoSessionOrClaim(t *testing.T) {
	store := world(t)
	srv, err := api.New(api.Config{
		Ports:  store.Ports(),
		Portal: store.PortalPorts(newGuestDriver()),
		Now:    func() time.Time { return at(0) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.GuestDocumentsHandler())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/en/")
	if err != nil {
		t.Fatalf("GET /en/: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /en/: %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(b), "The Venue") {
		t.Error("single-site welcome names no venue — the fallback must answer the deployment's own site")
	}
	if !strings.Contains(string(b), "<!doctype html>") {
		t.Error("not a document")
	}
}
