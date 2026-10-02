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

// TestLargeSignedInBodiesTakeTurns: bodies over the anonymous ceiling share one
// in-flight budget (bodybudget.go). Without it, nothing would bound how many
// signed-in megabytes are decoded at once, and each is ~40× its size in heap.
func TestLargeSignedInBodiesTakeTurns(t *testing.T) {
	store := world(t)
	srv, err := api.New(api.Config{Ports: store.Ports(), Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetBodyBudget(300 << 10) // decoded bytes: B (17 KiB of string) weighs ~221 KiB
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	r := &rig{t: t, server: ts, store: store}
	r.signIn(adminUser)

	send := func(body io.Reader, length int64) <-chan int {
		done := make(chan int, 1)
		req, err := http.NewRequest(http.MethodPost, ts.URL+api.BasePath+"/sites", body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = length
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Foyer-CSRF", r.csrf)
		req.AddCookie(r.cookie)
		go func() {
			resp, err := ts.Client().Do(req)
			if err != nil {
				done <- -1
				return
			}
			resp.Body.Close()
			done <- resp.StatusCode
		}()
		return done
	}

	// A: streamed, length unknown, so it is weighed at its ceiling — all of the
	// budget — and it holds it until its body ends.
	pr, pw := io.Pipe()
	// Registered after the server's Close, so it runs first: a failure below must
	// not leave the handler reading forever and Close waiting on it.
	t.Cleanup(func() { pw.CloseWithError(io.ErrUnexpectedEOF) })
	a := send(pr, -1)
	if _, err := pw.Write([]byte(`{"name":`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	// C: small, under the anonymous ceiling. Never waits.
	select {
	case <-send(strings.NewReader(`{"name":""}`), 11):
	case <-time.After(5 * time.Second):
		t.Fatal("a small body waited behind a large one; only bodies over the anonymous ceiling share the budget")
	}

	// B: 17 KiB declared, over the anonymous 16 KiB. Must wait for A.
	big := `{"name":"` + strings.Repeat("x", 17<<10) + `"}`
	b := send(strings.NewReader(big), int64(len(big)))
	select {
	case s := <-b:
		t.Fatalf("a second large body was served (%d) while the first held the whole budget", s)
	case <-time.After(300 * time.Millisecond):
	}

	if _, err := pw.Write([]byte(`"x"}`)); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	for name, ch := range map[string]<-chan int{"A": a, "B": b} {
		select {
		case s := <-ch:
			if s <= 0 || s == http.StatusRequestEntityTooLarge {
				t.Fatalf("%s failed (%d)", name, s)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never answered after the budget was released", name)
		}
	}
}
