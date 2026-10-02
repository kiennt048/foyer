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

// TestAnonymousBodiesShareABudgetToo: the connection bound alone let every
// connection a listener holds decode a maximal anonymous body at once. Anonymous
// bodies draw on a budget of their own, weighed at what they declare.
func TestAnonymousBodiesShareABudgetToo(t *testing.T) {
	store := world(t)
	srv, err := api.New(api.Config{Ports: store.Ports(), Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetSmallBodyBudget(200 << 10) // decoded bytes: B (8 KiB) weighs ~106 KiB
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	post := func(body io.Reader, length int64) <-chan int {
		done := make(chan int, 1)
		req, err := http.NewRequest(http.MethodPost, ts.URL+api.BasePath+"/auth/session", body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = length
		req.Header.Set("Content-Type", "application/json")
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

	// A streamed anonymous body is weighed at the anonymous ceiling, 16 KiB.
	pr, pw := io.Pipe()
	// Registered after the server's Close, so it runs first: a failure below must
	// not leave the handler reading forever and Close waiting on it.
	t.Cleanup(func() { pw.CloseWithError(io.ErrUnexpectedEOF) })
	a := post(pr, -1)
	if _, err := pw.Write([]byte(`{"username":`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	body := `{"username":"` + strings.Repeat("u", 8<<10) + `","password":"p"}`
	b := post(strings.NewReader(body), int64(len(body)))
	select {
	case s := <-b:
		t.Fatalf("an 8 KiB anonymous body was decoded (%d) beside a 16 KiB one in a 20 KiB budget", s)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := pw.Write([]byte(`"x","password":"y"}`)); err != nil {
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
