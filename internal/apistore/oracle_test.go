package apistore_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// The adapter, compared against the behavioural oracle — one link removed.
//
// ── why this exists, and what it is not ──────────────────────────────────────
//
// `web/mock` is the behavioural oracle for `internal/api`, and
// `make diff-api` drives one scenario into the mock and into the Go server and
// compares 31 of 50 operations. **That harness builds its Go server over
// `internal/api/memory`** — it seeds the fake world from the running mock and drives
// both. So what it proves is that the *handlers* answer what the mock answers, and
// it proves nothing at all about this package: `tools/codegen/differ` does not
// import `internal/apistore` and its result does not depend on this package at
// all.
//
// This closes the second link. The same world, the same requests, two port
// implementations: `internal/api/memory`, which `make diff-api` has already
// compared against the mock, and the adapter over real PostgreSQL. A difference
// here is a difference from the oracle by transitivity, and it is the only way to
// get that comparison without changing the harness in `tools/codegen`.
//
// **Still open**: `differ` wants a mode that builds its ports from
// `apistore` over a seeded database, so the comparison runs against the wiring an
// operator actually gets rather than against a fake standing where it will be.
//
// ── what is compared, and what is not ────────────────────────────────────────
//
// The list and read operations, over the same rows, including the site scopes and
// the filters neither the repository nor the fake can push down. Not compared, each
// with its reason:
//
//   - `getOverview` and `listAlarms` — the fake returns whatever world it was
//     handed, so seeding it from the adapter and comparing would be an equality
//     check on a value copied one line earlier. They are asserted against the rows
//     directly in e2e_test.go instead, which is the check that has content.
//   - the mutating operations — the fake mints its own ids and clocks, and the two
//     worlds would diverge on the first write. `internal/api`'s own suite drives
//     those against the fake, and e2e_test.go drives them against PostgreSQL.
//   - the cursor — opaque by contract and different by construction: the fake's is
//     a decimal offset over a slice and the adapter's carries a repository cursor.
//     Comparing them would be comparing an implementation detail the contract
//     explicitly refuses to define.
func TestTheAdapterAnswersWhatTheOracleAnswers(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	// The world the fake gets is read out of the adapter, so the two servers are
	// answering about the same rows rather than about two hand-written copies.
	// Transcribing a second fixture set is what makes a differential harness
	// disagree about every timestamp the moment either side restarts.
	fake := memory.NewStore(w.snapshot())
	oracleSrv, err := api.New(api.Config{Ports: fake.Ports()})
	if err != nil {
		t.Fatal(err)
	}
	oracle := httptest.NewServer(oracleSrv.Handler())
	defer oracle.Close()

	// Volatile keys are declared **per path, never globally**, so a field stays
	// compared everywhere it is not volatile. There is exactly one: signing in to
	// the second server stamps its own `lastLoginAt` on the operator, which is
	// the harness moving the world rather than the two servers disagreeing about
	// it.
	volatile := map[string][]string{"/operators": {"lastLoginAt"}}

	paths := []string{
		"/sites",
		"/nas?siteId=" + w.siteA,
		"/nas?siteId=" + w.siteB,
		"/nas?siteId=" + w.siteA + "&q=lobby",
		"/sessions?siteId=" + w.siteA,
		"/sessions?siteId=" + w.siteA + "&state=orphaned",
		"/sessions?siteId=" + w.siteA + "&state=online,orphaned",
		"/sessions?siteId=" + w.siteA + "&nasId=" + w.deviceRefusing,
		"/vouchers?siteId=" + w.siteA,
		"/vouchers?siteId=" + w.siteA + "&state=in-use",
		"/vouchers?siteId=" + w.siteA + "&batchId=" + w.batchA,
		"/voucher-batches?siteId=" + w.siteA,
		"/accounts?siteId=" + w.siteA,
		"/accounts?siteId=" + w.siteB,
		"/operators",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			mine := normalise(t, w.rawGet(path), volatile[path]...)
			theirs := normalise(t, w.getFrom(oracle, path), volatile[path]...)
			if !reflect.DeepEqual(mine, theirs) {
				t.Fatalf("the adapter and the oracle disagree on %s\n  adapter: %s\n   oracle: %s",
					path, mustMarshal(t, mine), mustMarshal(t, theirs))
			}
			// A comparison of two empty answers is not a comparison. Every path
			// above exists because it has rows.
			if isEmpty(mine) {
				t.Fatalf("%s returned nothing on either side, so this case compares nothing", path)
			}
		})
	}
}

// snapshot reads the adapter's own answers into the shape the fake takes.
func (w *world) snapshot() *memory.World {
	w.t.Helper()
	world := &memory.World{}

	w.getJSON("/sites", &world.Sites)
	var nas struct {
		Items []gen.Nas `json:"items"`
	}
	w.getJSON("/nas?siteId="+w.siteA+"&limit=200", &nas)
	world.Nas = nas.Items
	var nasB struct {
		Items []gen.Nas `json:"items"`
	}
	w.getJSON("/nas?siteId="+w.siteB+"&limit=200", &nasB)
	world.Nas = append(world.Nas, nasB.Items...)

	for _, site := range []string{w.siteA, w.siteB} {
		var sessions struct {
			Items []gen.Session `json:"items"`
		}
		w.getJSON("/sessions?siteId="+site+"&limit=200", &sessions)
		world.Sessions = append(world.Sessions, sessions.Items...)

		var batches struct {
			Items []gen.VoucherBatch `json:"items"`
		}
		w.getJSON("/voucher-batches?siteId="+site+"&limit=200", &batches)
		world.Batches = append(world.Batches, batches.Items...)

		var vouchers struct {
			Items []gen.Voucher `json:"items"`
		}
		w.getJSON("/vouchers?siteId="+site+"&limit=200", &vouchers)
		world.Vouchers = append(world.Vouchers, vouchers.Items...)

		var accounts struct {
			Items []gen.Account `json:"items"`
		}
		w.getJSON("/accounts?siteId="+site+"&limit=200", &accounts)
		world.Accounts = append(world.Accounts, accounts.Items...)
	}

	var operators struct {
		Items []gen.Operator `json:"items"`
	}
	w.getJSON("/operators?limit=200", &operators)
	world.Operators = operators.Items

	// The fake's `Vouchers` scopes by looking the batch up, so a voucher whose
	// batch it does not hold is invisible to it. Both lists come from the same
	// reads above, so the two agree by construction rather than by luck.
	world.Credentials = map[string]string{w.adminID: w.adminPassword}
	return world
}

// getFrom drives the same request into the second server, with its own session.
func (w *world) getFrom(srv *httptest.Server, path string) []byte {
	w.t.Helper()
	if w.oracleJar == nil {
		w.oracleJar = map[string]string{}
		w.signInTo(srv)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL+api.BasePath+path, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	for name, value := range w.oracleJar {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := readAll(w.t, resp)
	if resp.StatusCode != http.StatusOK {
		w.t.Fatalf("oracle GET %s: %d: %s", path, resp.StatusCode, truncate(raw))
	}
	return raw
}

func (w *world) signInTo(srv *httptest.Server) {
	w.t.Helper()
	body := mustMarshal(w.t, map[string]any{"username": w.adminUser, "password": w.adminPassword})
	resp, err := srv.Client().Post(srv.URL+api.BasePath+"/auth/session", "application/json", bytesReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.t.Fatalf("signing in to the oracle: %d: %s", resp.StatusCode, truncate(readAll(w.t, resp)))
	}
	for _, c := range resp.Cookies() {
		w.oracleJar[c.Name] = c.Value
	}
}

// normalise removes the two things that are allowed to differ.
//
// `nextCursor` is opaque by contract and the two encodings are different by
// construction. Everything else is compared exactly — including `null` against an
// omitted property, because both sides are `oapi-codegen` output and encode an
// absent optional the same way, so an inequality there would be a real difference
// rather than an encoding one.
func normalise(t *testing.T, raw []byte, volatile ...string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding: %v: %s", err, truncate(raw))
	}
	drop := map[string]bool{"nextCursor": true}
	for _, k := range volatile {
		drop[k] = true
	}
	return strip(v, drop)
}

func strip(v any, drop map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if drop[k] {
				continue
			}
			out[k] = strip(val, drop)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, strip(item, drop))
		}
		return out
	default:
		return v
	}
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		items, ok := t["items"].([]any)
		return ok && len(items) == 0
	case []any:
		return len(t) == 0
	default:
		return v == nil
	}
}
