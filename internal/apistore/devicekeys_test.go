package apistore_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/store"
)

// TestATrustedDeviceSurvivesARestartOverPostgres is the trusted-device lockout end
// to end: the operator's browser signs in, the process is replaced by a new one — a
// new Store pool, a new adapter, a new api.Server, the same database and FOYER_KEK —
// and a stranger empties the account's shared bucket. The operator's device cookie
// must still open its own bucket. With a key drawn per process this answers 429: a
// lockout after every restart.
func TestATrustedDeviceSurvivesARestartOverPostgres(t *testing.T) {
	w := newWorld(t)

	attempt := func(srv *httptest.Server, password string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+api.BasePath+"/auth/session",
			strings.NewReader(`{"username":"`+w.adminUser+`","password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	first := attempt(w.server, w.adminPassword, nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("the operator's sign-in answered %d", first.StatusCode)
	}
	var device *http.Cookie
	for _, c := range first.Cookies() {
		if c.Name == "__Host-foyer_device" {
			device = c
		}
	}
	if device == nil {
		t.Fatal("a successful sign-in set no trusted-device cookie")
	}

	// The restart: nothing of the first process survives but the database.
	st, err := store.Open(context.Background(), store.Config{URL: ownerURL, Keys: w.st.Keys(), MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	adapter, err := apistore.New(apistore.Config{Store: st, Drivers: fakeDrivers{}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Config{Ports: adapter.Ports()})
	if err != nil {
		t.Fatal(err)
	}
	restarted := httptest.NewServer(srv.Handler())
	t.Cleanup(restarted.Close)

	for i := 0; i < 5; i++ {
		attempt(restarted, "a-guess", nil)
	}
	if got := attempt(restarted, w.adminPassword, nil).StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("with the shared bucket emptied an untrusted attempt answered %d, want 429 — the test is not measuring the lockout", got)
	}
	if got := attempt(restarted, w.adminPassword, device).StatusCode; got != http.StatusOK {
		t.Fatalf("after a restart the operator's trusted device answered %d, want 200: the device key did not survive the restart", got)
	}
}
