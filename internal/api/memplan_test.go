package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/metrics"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// If the console's body ceiling could be raised to 16 MiB while the in-flight
// budget weighed any larger request as its own 4 MiB, one staff request would reach
// 437 MiB live in a 320M container. These tests hold the two
// halves of the fix: no value the console can save exceeds what the memory plan can
// decode, and a body is weighed at what its shape costs rather than clamped.

const mib = 1 << 20

// TestTheShippedPlanHoldsTheContractsLargestBody is the arithmetic, for the shipped
// container and for smaller and larger ones: the budgets fit inside the soft limit
// beside the argon2id slots and the process's tenth, the highest console ceiling
// decodes (as a string) inside the large budget, and the contract's largest hero
// photograph fits the shipped plan.
func TestTheShippedPlanHoldsTheContractsLargestBody(t *testing.T) {
	for _, tc := range []struct {
		name       string
		plan       api.MemoryPlan
		soft, hash int64
	}{
		{"shipped (zero plan)", api.MemoryPlan{}, 256 * mib, 128 * mib},
		{"deploy: GOMEMLIMIT 256MiB, two slots", api.MemoryPlan{SoftLimit: 256 * mib, HashBytes: 128 * mib}, 256 * mib, 128 * mib},
		{"a 160M cgroup: 128 MiB soft, one slot", api.MemoryPlan{SoftLimit: 128 * mib, HashBytes: 64 * mib}, 128 * mib, 64 * mib},
		{"a large box: 4 GiB soft, four slots", api.MemoryPlan{SoftLimit: 4096 * mib, HashBytes: 256 * mib}, 4096 * mib, 256 * mib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			large, small, consoleMax, smallMax := api.PlanBodies(tc.plan)
			if total := large + small + tc.hash + tc.soft/10; total > tc.soft {
				t.Fatalf("bodies %d + %d MiB beside %d MiB of argon2id and the process's tenth is %d MiB, over the %d MiB soft limit",
					large>>20, small>>20, tc.hash>>20, total>>20, tc.soft>>20)
			}
			if consoleMax*api.DecodePerByte > large {
				t.Fatalf("the console ceiling may be set to %d KiB, which decodes to %d MiB, over the %d MiB budget",
					consoleMax>>10, consoleMax*api.DecodePerByte>>20, large>>20)
			}
			if smallMax*api.DecodePerByte > small {
				t.Fatalf("the portal/anonymous ceiling may be set to %d KiB, over its %d KiB budget", smallMax>>10, small>>10)
			}
			if consoleMax > 16*mib {
				t.Fatalf("console ceiling bound %d MiB is above 16 MiB", consoleMax>>20)
			}
		})
	}
	large, _, consoleMax, _ := api.PlanBodies(api.MemoryPlan{})
	if w := api.HeroImageBodyLimit() * api.DecodePerByte; w > large {
		t.Fatalf("the contract's largest hero photograph weighs %d MiB, over the shipped %d MiB budget: uploads the contract accepts would be refused", w>>20, large>>20)
	}
	if consoleMax != 7*mib {
		t.Fatalf("the shipped console ceiling bound is %d KiB; web/mock/src/network.ts states 7 MiB and must be changed with it", consoleMax>>10)
	}
}

func newPlanRig(t *testing.T, plan api.MemoryPlan) *netRig {
	t.Helper()
	store := world(t)
	ports := store.Ports()
	fake := newFakeNetwork()
	ports.Network = fake
	srv, err := api.New(api.Config{Ports: ports, Memory: plan, Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.ReloadNetwork(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &netRig{rig: &rig{t: t, server: ts, store: store}, srv: srv, net: fake}
}

// TestTheConsoleCannotSetACeilingTheContainerCannotHold: the bound the console is
// offered is the plan's, a write above it is refused, and a value stored on a
// larger container is clamped when this one reads it.
func TestTheConsoleCannotSetACeilingTheContainerCannotHold(t *testing.T) {
	r := newPlanRig(t, api.MemoryPlan{SoftLimit: 256 * mib, HashBytes: 128 * mib})
	r.signIn(adminUser)
	_, _, consoleMax, _ := api.PlanBodies(api.MemoryPlan{SoftLimit: 256 * mib, HashBytes: 128 * mib})

	if got := int64(r.settings().Bounds.ConsoleBytes.Maximum); got != consoleMax {
		t.Fatalf("the console is offered a ceiling of up to %d KiB, want the plan's %d KiB", got>>10, consoleMax>>10)
	}
	code, body := r.put(func(in *gen.NetworkSettingsUpdate) { in.BodyCeilings.ConsoleBytes = 16 * mib })
	if f, _ := faultOf(t, body, "bodyCeilings.consoleBytes"); code != http.StatusUnprocessableEntity || f != gen.FieldFaultAboveMaximum {
		t.Fatalf("a 16 MiB console ceiling answered %d %s; want 422 above-maximum", code, body)
	}
	if code, body := r.put(func(in *gen.NetworkSettingsUpdate) { in.BodyCeilings.ConsoleBytes = int(consoleMax) }); code != http.StatusOK {
		t.Fatalf("the plan's own maximum was refused: %d %s", code, body)
	}

	// Saved at 16 MiB by a larger container, read by this one.
	stored := api.DefaultBodyCeilings()
	stored.ConsoleBytes = 16 * mib
	r.net.stored.BodyCeilings = &stored
	if err := r.srv.ReloadNetwork(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := int64(r.settings().BodyCeilings.ConsoleBytes); got != consoleMax {
		t.Fatalf("a stored 16 MiB ceiling is in force as %d KiB, want it clamped to %d KiB", got>>10, consoleMax>>10)
	}
}

// TestABodyIsWeighedAtItsRealSize: a signed-in body at the highest ceiling the
// console allows, of the densest shape. It must be refused before anything decodes
// it, and the process must not hold anything like its decoded size meanwhile; the
// same length as a string is served.
func TestABodyIsWeighedAtItsRealSize(t *testing.T) {
	plan := api.MemoryPlan{SoftLimit: 256 * mib, HashBytes: 128 * mib}
	r := newPlanRig(t, plan)
	r.signIn(adminUser)
	large, _, consoleMax, _ := api.PlanBodies(plan)
	if code, body := r.put(func(in *gen.NetworkSettingsUpdate) { in.BodyCeilings.ConsoleBytes = int(consoleMax) }); code != http.StatusOK {
		t.Fatalf("raising the ceiling: %d %s", code, body)
	}
	n := int(consoleMax) - 1024
	dense := []byte(`{"name":[` + strings.Repeat("0,", n/2-8) + `0]}`)
	plain := []byte(`{"name":"` + strings.Repeat("x", n-16) + `"}`)
	h := r.srv.Handler()

	send := func(body []byte) (int, uint64) {
		req := httptest.NewRequest(http.MethodPost, api.BasePath+"/sites", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Foyer-CSRF", r.csrf)
		req.AddCookie(r.cookie)
		rec := httptest.NewRecorder()
		runtime.GC()
		sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		metrics.Read(sample)
		base := sample[0].Value.Uint64()
		var peak atomic.Uint64
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
			for {
				select {
				case <-stop:
					return
				default:
				}
				metrics.Read(s)
				if v := s[0].Value.Uint64(); v > peak.Load() {
					peak.Store(v)
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
		h.ServeHTTP(rec, req)
		close(stop)
		<-done
		grew := uint64(0)
		if p := peak.Load(); p > base {
			grew = p - base
		}
		return rec.Code, grew
	}

	code, grew := send(dense)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a %d KiB body of the densest shape (~%d MiB decoded, budget %d MiB) answered %d, want 413",
			len(dense)>>10, int64(len(dense))*45>>20, large>>20, code)
	}
	// Reading and weighing it costs its bytes a few times over; decoding it would
	// cost ~36× (about 250 MiB here, past the whole budget). The bound is half the
	// budget, far under the latter. A quarter is too tight: under `go test -race`
	// this measured 25 MiB against 23, because the race instrumentation keeps more
	// of the read alive at the sampled peak: still an
	// order of magnitude from a decode, which is what this assertion tells apart.
	if limit := uint64(large / 2); grew > limit {
		t.Fatalf("refusing the dense body grew the heap by %d MiB (limit %d MiB): it was decoded before it was weighed", grew>>20, limit>>20)
	}
	if code, _ := send(plain); code == http.StatusRequestEntityTooLarge {
		t.Fatalf("a %d KiB string body at the ceiling was refused 413; its decoded weight fits the budget", len(plain)>>10)
	}
}
