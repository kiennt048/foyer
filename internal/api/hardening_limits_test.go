package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// hardening_test.go proves the two operations it sends a body to; these tests are
// about every other one, and about what a body costs after it is read.

// limitOf reads `params.fieldParams.body.limitBytes` out of a 413.
func limitOf(t *testing.T, body []byte) int64 {
	t.Helper()
	var p struct {
		Type   string `json:"type"`
		Params struct {
			FieldParams map[string]map[string]float64 `json:"fieldParams"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("not a problem document: %v\n%s", err, body)
	}
	if p.Type != string(gen.ProblemstooLarge) {
		t.Fatalf("type = %q, want %q", p.Type, gen.ProblemstooLarge)
	}
	return int64(p.Params.FieldParams["body"]["limitBytes"])
}

// TestEveryOperationWithABodyRefusesAnOversizedOne walks the contract rather than
// a list: every operation that declares a request body, on the listener it
// belongs to and on the other one, with a declared length over every ceiling and
// with a streamed body of the same size. Each must answer `413` and stop reading.
//
// A list of two operations is how the anonymous-megabyte defect below can stay
// invisible: the one console operation such a list tries is `signIn`, which
// nothing decodes into a large tree.
func TestEveryOperationWithABodyRefusesAnOversizedOne(t *testing.T) {
	sw, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	type op struct{ id, method, path, contentType string }
	var ops []op
	for pattern, item := range sw.Paths.Map() {
		for method, o := range item.Operations() {
			if o.RequestBody == nil || o.RequestBody.Value == nil {
				continue
			}
			var cts []string
			for ct := range o.RequestBody.Value.Content {
				cts = append(cts, ct)
			}
			sort.Strings(cts)
			ops = append(ops, op{o.OperationID, method, param.ReplaceAllString(pattern, "x1"), cts[0]})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].id < ops[j].id })
	if len(ops) < 15 {
		t.Fatalf("found %d operations with a request body; the walk is not reading the contract", len(ops))
	}

	srv := bareServer(t)
	listeners := []struct {
		name    string
		handler http.Handler
		limit   int64
	}{
		{"console", srv.Handler(), api.AnonymousBodyLimit},
		{"portal", srv.PortalHandler(), api.PortalBodyLimit},
	}
	const size = 8 << 20 // over every ceiling, the hero image's included
	for _, l := range listeners {
		for _, o := range ops {
			for _, streamed := range []bool{false, true} {
				name := l.name + "/" + o.id + "/declared"
				if streamed {
					name = l.name + "/" + o.id + "/streamed"
				}
				t.Run(name, func(t *testing.T) {
					body := &endless{limit: size}
					req := httptest.NewRequest(o.method, api.BasePath+o.path, body)
					req.ContentLength = size
					if streamed {
						req.ContentLength = -1
					}
					req.Header.Set("Content-Type", o.contentType)
					rec := httptest.NewRecorder()
					l.handler.ServeHTTP(rec, req)
					if rec.Code != http.StatusRequestEntityTooLarge {
						t.Fatalf("%s %s: %d, want 413\n%s", o.method, o.path, rec.Code, rec.Body.String())
					}
					if got := limitOf(t, rec.Body.Bytes()); got != l.limit {
						t.Fatalf("limitBytes = %d, want %d for a caller with no session", got, l.limit)
					}
					if taken := body.n.Load(); taken > l.limit+64<<10 {
						t.Fatalf("read %d bytes of a body it should have stopped at %d", taken, l.limit)
					}
				})
			}
		}
	}
}

// TestAnAnonymousCallerGetsTheAnonymousCeiling: the console's megabyte is for a
// signed-in operator. Anybody else — and anybody calling a public operation — is
// held to the portal's 16 KiB, because the validator decodes a body before the
// gate has looked at the caller and the decoded tree is tens of times the text.
func TestAnAnonymousCallerGetsTheAnonymousCeiling(t *testing.T) {
	padded := func(n int) map[string]any {
		return map[string]any{"username": "somebody", "password": "something-long", "padding": strings.Repeat("a", n)}
	}
	const n = 64 << 10 // over the anonymous ceiling, far under the signed-in one

	t.Run("anonymous, an operation that needs a session", func(t *testing.T) {
		r := newRig(t)
		resp := r.do(http.MethodPost, "/accounts", padded(n))
		b := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("an anonymous %d KiB body answered %d, want 413\n%s", n>>10, resp.StatusCode, b)
		}
		if got := limitOf(t, []byte(b)); got != api.AnonymousBodyLimit {
			t.Fatalf("limitBytes = %d, want the anonymous %d", got, api.AnonymousBodyLimit)
		}
	})

	t.Run("signed in, the same body", func(t *testing.T) {
		r := newRig(t)
		r.signIn(adminUser)
		resp := r.do(http.MethodPost, "/accounts", padded(n))
		b := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			t.Fatalf("a signed-in operator's %d KiB body was refused as too large; the console's ceiling is %d\n%s",
				n>>10, api.AdminBodyLimit, b)
		}
	})

	t.Run("signIn is public and gets the anonymous ceiling even with a session", func(t *testing.T) {
		r := newRig(t)
		r.signIn(adminUser)
		resp := r.do(http.MethodPost, "/auth/session", padded(n))
		b := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("a %d KiB sign-in answered %d, want 413\n%s", n>>10, resp.StatusCode, b)
		}
		if got := limitOf(t, []byte(b)); got != api.AnonymousBodyLimit {
			t.Fatalf("limitBytes = %d, want the anonymous %d", got, api.AnonymousBodyLimit)
		}
	})
}

// TestAnAnonymousFloodOfLargeBodiesStaysSmall is the property itself, measured:
// twenty anonymous callers at once, each streaming a megabyte of `[0,0,0,…]` —
// under the console's own ceiling, so without the anonymous one every one of them
// would be read and decoded — must not move the heap by more than a few megabytes.
//
// Without the anonymous ceiling this peaks at roughly 900 MiB in this test (40 MiB
// of tree per request); the control-plane container is 320M.
func TestAnAnonymousFloodOfLargeBodiesStaysSmall(t *testing.T) {
	h := bareServer(t).Handler()
	body := []byte("[" + strings.Repeat("0,", int(api.AdminBodyLimit)/2-8) + "0]")

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	const callers = 20
	var wg sync.WaitGroup
	codes := make([]int, callers)
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, api.BasePath+"/accounts", bytes.NewReader(body))
			req.ContentLength = -1 // streamed: the declared-length refusal is not what is being measured
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(done)
	<-sampled

	grew := int64(peak.Load()) - int64(base.HeapAlloc)
	t.Logf("%d anonymous 1 MiB bodies at once: heap grew %d MiB at peak, answers %v", callers, grew>>20, codes[:3])
	if slices.ContainsFunc(codes, func(c int) bool { return c != http.StatusRequestEntityTooLarge }) {
		t.Errorf("answers %v, want 413 for every one", codes)
	}
	// The body itself is shared and pre-allocated; what is measured is what
	// serving it cost. 64 MiB is a generous bound on twenty 16 KiB reads.
	if grew > 64<<20 {
		t.Fatalf("serving %d anonymous 1 MiB bodies grew the heap by %d MiB; the container is 320M", callers, grew>>20)
	}
}
