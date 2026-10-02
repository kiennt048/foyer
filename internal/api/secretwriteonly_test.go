package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The write-only guarantee, asserted as **absence** rather than as the presence of
// the absence of a feature.
//
// Every other way of holding this fails in the same direction. "There is no port
// method that reads a secret back" is a claim about an interface, and an interface
// gains a method. "`secretSet` is a boolean" is a claim about today's struct. The
// test that holds it is the one that says: here are two values the server
// generated, and here is every answer it can give, and the values are in none of
// them.
//
// **The failure this guards against is concrete.** A discovered access point that
// is adopted becomes an enabled RADIUS client, and it is permanently unable to
// authenticate if the console never shows the shared secret Foyer generated: the
// API is write-only, so a value the server does not return at creation is gone.
// The console can show it *only* because the rotation response carries the
// value. So the property is: a secret is disclosed exactly once,
// by the write that created it, and is not retrievable by any other call.
//
// A test asserting a secret is absent from a response is the only thing that holds
// that, because every alternative asserts the absence of a mechanism and this
// asserts the absence of the data.

// walkEveryAdminRead drives every GET in the contract that the rig can answer, and
// calls `check` with each response body.
//
// The operation list is derived from the embedded contract rather than typed, for
// the reason `TestUnauthenticatedRequestToAProtectedOperationIs401` derives its own:
// a typed list is a list somebody forgets to extend, and the forgotten entry is
// precisely the endpoint that leaks. A path parameter is filled with the device
// under test, which is the row a secret would travel with if it travelled at all.
// rawQuery renders the required parameters the contract asks for. Sorted, so the
// path a failure names is the same path every run.
func rawQuery(q map[string]string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+url.QueryEscape(q[k]))
	}
	return strings.Join(parts, "&")
}

func walkEveryAdminRead(t *testing.T, r *rig, nasID string, check func(operation, body string)) {
	t.Helper()

	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	ops := make([]string, 0, 64)
	for path, item := range spec.Paths.Map() {
		if item.Get == nil {
			continue
		}
		ops = append(ops, path)
	}
	if len(ops) < 40 {
		t.Fatalf("only %d GET operations found in the contract — the walk is not covering the surface", len(ops))
	}

	/*
	 * Required query parameters, taken from the contract rather than guessed.
	 *
	 * A walk that fills path parameters and nothing else gets fifteen of the
	 * forty-two reads back as `422 query: Missing or unreadable` — a refusal produced
	 * before the handler ran, which is exactly the body that cannot contain a secret.
	 * It reports green over a surface where only nineteen of forty-two responses are
	 * real, and a sabotage that leaks the rotated secret through `ListNas` sails
	 * straight through it.
	 *
	 * So the query parameters are read out of the spec, and the count of reads that
	 * actually reached a handler is asserted by the caller rather than assumed. A walk
	 * that quietly degrades into refusals is worse than no walk, because it is a
	 * comment about the walk.
	 */
	requiredQuery := map[string]map[string]string{}
	for path, item := range spec.Paths.Map() {
		if item.Get == nil {
			continue
		}
		need := map[string]string{}
		for _, p := range item.Get.Parameters {
			if p == nil || p.Value == nil || !p.Value.Required {
				continue
			}
			switch p.Value.Name {
			case "siteId":
				need["siteId"] = testSite
			case "accountId":
				need["accountId"] = "acct-1"
			case "voucherBatchId":
				need["voucherBatchId"] = "vb-1"
			case "operatorId":
				need["operatorId"] = "op-staff"
			case "nasId":
				need["nasId"] = nasID
			case "termId":
				need["termId"] = "terms-en"
			}
		}
		if len(need) > 0 {
			requiredQuery[path] = need
		}
	}

	for _, path := range ops {
		filled := strings.ReplaceAll(path, "{nasId}", nasID)
		filled = strings.ReplaceAll(filled, "{siteId}", testSite)
		filled = strings.ReplaceAll(filled, "{id}", nasID)
		filled = strings.ReplaceAll(filled, "{accountId}", "acct-1")
		filled = strings.ReplaceAll(filled, "{sessionId}", "sess-1")
		filled = strings.ReplaceAll(filled, "{voucherBatchId}", "vb-1")
		filled = strings.ReplaceAll(filled, "{voucherId}", "vch-1")
		filled = strings.ReplaceAll(filled, "{operatorId}", "op-staff")
		filled = strings.ReplaceAll(filled, "{entryId}", "ge-portal")
		filled = strings.ReplaceAll(filled, "{termId}", "terms-en")
		filled = strings.ReplaceAll(filled, "{reportId}", "rpt-sessions")
		if q := requiredQuery[path]; len(q) > 0 {
			filled += "?" + rawQuery(q)
		}
		// A path this rig cannot satisfy answers 404 or 422; the body is still
		// checked, because a refusal is a response too and the interesting leak
		// would be in a body that got built before the refusal.
		resp := r.do(http.MethodGet, filled, nil)
		body := readAll(t, resp)
		resp.Body.Close()
		check("GET "+filled+" ["+strconv.Itoa(resp.StatusCode)+"]", body)
	}
}

func TestAStoredSharedSecretIsNotRetrievableByAnyOtherOperation(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	// Two values the server generated, by the two operations that generate one.
	// Both are captured from real responses rather than invented, so the walk below
	// is looking for strings this server actually issued.
	created := r.do(http.MethodPost, "/nas", map[string]any{
		"name": "Write-only probe", "ipAddress": "10.90.0.41", "siteId": testSite,
		"driver": "mikrotik",
	})
	createdBody := readAll(t, created)
	created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("register: got %d, want 201\n%s", created.StatusCode, createdBody)
	}
	var reg struct {
		Nas          gen.Nas  `json:"nas"`
		SharedSecret string   `json:"sharedSecret"`
		_            struct{} `json:"-"`
	}
	if err := json.Unmarshal([]byte(createdBody), &reg); err != nil {
		t.Fatalf("decode createNas: %v\n%s", err, createdBody)
	}
	if len(reg.SharedSecret) < 32 {
		t.Fatalf("registration returned no usable secret (%d chars); the walk below would prove nothing",
			len(reg.SharedSecret))
	}

	// A device nothing has probed is `unknown`, and a driver that cannot push plus
	// an unreachable device is the one combination `rotateNasSecret` refuses — so it
	// would refuse here and the test would pass on a `200` it never got. Reachable,
	// which is the state the real controller's access points are in.
	for i := range r.store.World().Nas {
		if r.store.World().Nas[i].Id == reg.Nas.Id {
			r.store.World().Nas[i].Reachability = gen.NasReachabilityOk
		}
	}

	rotated := r.do(http.MethodPost, "/nas/"+reg.Nas.Id+"/secret", nil)
	rotatedBody := readAll(t, rotated)
	rotated.Body.Close()
	if rotated.StatusCode != http.StatusOK {
		t.Fatalf("rotate: got %d, want 200\n%s", rotated.StatusCode, rotatedBody)
	}
	var rot struct {
		SharedSecret string `json:"sharedSecret"`
	}
	_ = json.Unmarshal([]byte(rotatedBody), &rot)
	if rot.SharedSecret == "" {
		t.Fatal("rotation returned no secret — this is one of the two responses that carries one")
	}
	if rot.SharedSecret == reg.SharedSecret {
		t.Error("rotation returned the value it was already holding, so the walk below proves nothing")
	}

	secrets := map[string]string{
		"the registered secret": reg.SharedSecret,
		"the rotated secret":    rot.SharedSecret,
	}

	/*
	 * `reached` counts the reads that actually ran a handler. Without it the walk
	 * looks identical whether it covered the API or refused most of it — which is
	 * precisely how a sabotage gets through a walk that does not count.
	 */
	checked, reached := 0, 0
	walkEveryAdminRead(t, r, reg.Nas.Id, func(operation, body string) {
		checked++
		if strings.Contains(operation, "[200]") {
			reached++
		}
		for what, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Errorf("%s returned %s.\nThe API is write-only and this is the one property that "+
					"holds it: a response that yields a value the server already holds is a "+
					"credential-store read, and nothing about the one-time disclosure at "+
					"registration or rotation justifies one.\n%s", operation, what, body)
			}
		}
	})
	if checked < 40 {
		t.Errorf("walked only %d reads; the guarantee is not established over the surface", checked)
	}
	/*
	 * Two thirds, and the number is measured rather than chosen for comfort: this
	 * world wires no store behind four operations and holds no row for five ids the
	 * contract names. The floor is a floor — a walk that answered 200 twenty times
	 * out of forty-five is no longer walking the API, it is walking the refusals, and
	 * every guarantee above it becomes a statement about bodies produced before any
	 * handler ran.
	 */
	if reached < 25 {
		t.Errorf("only %d of %d reads reached a handler.\nA read that answers 422 or 404 is refused "+
			"before the handler builds anything, so it cannot carry a secret and cannot disprove a "+
			"leak. The walk has stopped covering the surface and is now reporting on refusals.",
			reached, checked)
	}
}

// The same guarantee, held **structurally** rather than behaviourally.
//
// The behavioural walk above proves the current handlers do not leak. This proves
// the *contract* has no shape in which they could, which is the property that
// survives somebody adding a convenient "get the secret for this NAS" screen next
// year — the walk would only catch that if the new handler were also driven here.
//
// A response schema carrying `sharedSecret` is the whole of the surface, because a
// handler cannot return a field the schema does not declare: `oapi-codegen` types
// every response from it, and a hand-built body would fail validation.
func TestTheContractDeclaresTheSharedSecretOnExactlyTwoResponses(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}

	type found struct{ operation string }
	var carriers []found
	schemas := 0

	// Resolved rather than read as raw text: a `$ref` is a name here, and a test
	// that matched the string `sharedSecret` would count the property in
	// `RotateNasSecret200` once per place it is referenced.
	/*
	 * Every composition keyword, and this is not completeness for its own sake.
	 *
	 * A walk over `properties`, `items` and `additionalProperties` only is **wrong,
	 * and a sabotage shows it**: the very shape a careless edit takes
	 * —
	 *
	 *     schema:
	 *       allOf:
	 *         - $ref: "#/components/schemas/Nas"
	 *         - type: object
	 *           properties:
	 *             sharedSecret: { type: string }
	 *
	 * — is a sibling of the property, not a child of it, and such a walk sails
	 * straight through it and reports green. A test that cannot see the most
	 * obvious way to add the thing it forbids is not a test; it is a comment about
	 * the test. `allOf` is walked because that is the shape above, and
	 * `anyOf`/`oneOf` beside it because they have the same property.
	 */
	var visit func(*openapi3.SchemaRef, string)
	visit = func(schema *openapi3.SchemaRef, operation string) {
		if schema == nil || schema.Value == nil {
			return
		}
		if _, ok := schema.Value.Properties["sharedSecret"]; ok {
			carriers = append(carriers, found{operation})
		}
		for _, ref := range schema.Value.Properties {
			visit(ref, operation)
		}
		visit(schema.Value.Items, operation)
		if ap := schema.Value.AdditionalProperties; ap.Has != nil {
			visit(ap.Schema, operation)
		}
		for _, ref := range schema.Value.AllOf {
			visit(ref, operation)
		}
		for _, ref := range schema.Value.AnyOf {
			visit(ref, operation)
		}
		for _, ref := range schema.Value.OneOf {
			visit(ref, operation)
		}
	}

	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Responses == nil {
				continue
			}
			for status, resp := range op.Responses.Map() {
				if resp.Value == nil || resp.Value.Content == nil {
					continue
				}
				for media, content := range resp.Value.Content {
					_ = media
					if content.Schema == nil {
						continue
					}
					schemas++
					visit(content.Schema, strings.ToUpper(method)+" "+path+" {"+status+"}")
				}
			}
		}
	}
	if schemas < 50 {
		t.Fatalf("only %d response schemas examined — the contract is not what this test thinks", schemas)
	}

	got := make([]string, 0, len(carriers))
	for _, c := range carriers {
		// Named by the *value* so a rename of the property is a rename here too.
		got = append(got, c.operation)
	}

	want := map[string]bool{
		"POST /nas {201}":                true,
		"POST /nas/{nasId}/secret {200}": true,
	}
	for _, op := range got {
		if !want[op] {
			t.Errorf("%s declares a `sharedSecret` and may not.\n"+
				"Exactly two responses in this contract may carry one, and both are the write that "+
				"created it. A third is the beginning of a read API for stored secrets, and there is "+
				"no port method that would supply it — so it would be a new leak, not a new feature.",
				op)
		}
		delete(want, op)
	}
	for op := range want {
		t.Errorf("%s no longer carries a `sharedSecret`. That is the disclosure the console "+
			"renders once, and removing it leaves an operator holding a device that cannot "+
			"authenticate and no way to learn the value.", op)
	}
}

// A rotation must leave no trace of its value in the audit log.
//
// Separate from the walk above, and separate for a reason: the log is the one
// response a *different* operator reads months later, with no session of theirs in
// it and no screen that says a value is there. `docs/rules/security.md` §3
// requires secrets redacted from logs and from the support bundle, and the
// rotation is the operation most likely to be written down.
func TestRotatingASecretWritesNoValueIntoTheAuditLog(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodPost, "/nas/nas-1/secret", nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate nas-1: got %d, want 200\n%s", resp.StatusCode, body)
	}
	var out struct {
		SharedSecret string `json:"sharedSecret"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if out.SharedSecret == "" {
		t.Fatal("no secret returned, so there is nothing for the log to be wrongly free of")
	}

	log := r.do(http.MethodGet, "/audit?limit=200", nil)
	logBody := readAll(t, log)
	log.Body.Close()
	if strings.Contains(logBody, out.SharedSecret) {
		t.Errorf("the rotated secret is in the audit log.\nA log read months later by somebody "+
			"who was not there is the last place a shared secret should reappear, and the entry "+
			"already names the change class and whether the push succeeded — which is the whole "+
			"of what an operator reading it months later needs.\n%s", logBody)
	}
	// The entry itself is the reason this test is not vacuous: the rotation is
	// recorded, and what is recorded is everything except the value.
	if !strings.Contains(logBody, "nas-secret-rotated") {
		t.Errorf("the rotation was not audited at all, so the assertion above proves nothing:\n%s", logBody)
	}
}

// The disclosure is `no-store`, and so is every response on this surface.
//
// Named in the acceptance criteria, and it is the reason the reveal lives in the
// response the write already returned rather than in a second endpoint a cache
// could hold: `securityHeaders` puts `no-store` on the whole API, so the one
// response that carries a value cannot be written to disk by an intermediary, kept
// by a shared proxy, or restored from the browser's own cache on a Back.
func TestTheOneResponseCarryingASecretIsNotCacheable(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	// Read and write, because a cache is only consulted on the read half of that
	// pair and the value in question was fetched with `fetch` by the console.
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPost, "/nas", map[string]any{
			"name": "No-store probe", "ipAddress": "10.90.0.42", "siteId": testSite,
			"driver": "mikrotik",
		}, http.StatusCreated},
		{http.MethodGet, "/nas/nas-1", nil, http.StatusOK},
	} {
		resp := r.do(tc.method, tc.path, tc.body)
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s: got %d, want %d\n%s", tc.method, tc.path, resp.StatusCode, tc.want, body)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s %s: Cache-Control = %q, want no-store.\nThis is the response that carries a "+
				"shared secret, and a cacheable one is a copy of it on disk that Foyer does not own "+
				"and cannot revoke.", tc.method, tc.path, cc)
		}
	}
}
