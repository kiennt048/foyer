package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// These tests are in `package api` rather than `package api_test` because what
// they pin is internal: the gate table, the role table and the cookie. The
// behavioural suite is next door in `server_test.go` and drives the HTTP surface
// instead.

// TestEveryOperationHasAGate is the deny-by-default guarantee at its root.
//
// The gate is looked up by operationId, and an operationId with no entry is
// refused — so a missing entry cannot open an endpoint. This asserts the table
// covers the generated interface anyway, because "refused" and "unreachable" are
// different problems and the second one is a 403 somebody debugs for an hour.
func TestEveryOperationHasAGate(t *testing.T) {
	g, err := readGates()
	if err != nil {
		t.Fatalf("readGates: %v", err)
	}
	for _, id := range generatedOperationIDs(t) {
		if _, ok := g[id]; !ok {
			t.Errorf("%s has no gate — the contract declares no x-permission for it", id)
		}
	}
	if len(g) != len(generatedOperationIDs(t)) {
		t.Errorf("gate table has %d entries, the generated interface has %d",
			len(g), len(generatedOperationIDs(t)))
	}
}

// TestGateValuesAreRealPermissions asserts every requirement is either one of
// the two non-permission values or a member of `OperatorPermission`.
//
// Without this, a typo in the contract — `nas:mange` — would produce a
// requirement no role can ever hold, and the operation would be permanently
// 403 for everybody with no error anywhere.
func TestGateValuesAreRealPermissions(t *testing.T) {
	g, err := readGates()
	if err != nil {
		t.Fatalf("readGates: %v", err)
	}
	known := map[gen.OperatorPermission]bool{}
	for _, p := range allPermissions() {
		known[p] = true
	}
	for id, gate := range g {
		switch gate.Requirement {
		case reqPublic, reqAuthenticated:
			continue
		}
		if !known[gen.OperatorPermission(gate.Requirement)] {
			t.Errorf("%s requires %q, which is not an OperatorPermission", id, gate.Requirement)
		}
	}
}

// TestEveryPermissionIsRequiredBySomething is the other direction, and it is the
// one that catches a gate that has never been closed.
//
// The contract says a permission no operation requires is "a value nothing ever
// withholds", which is a distinction the product does not have. If this fails,
// either an operation lost its annotation or the enum gained a member nothing
// uses.
func TestEveryPermissionIsRequiredBySomething(t *testing.T) {
	g, err := readGates()
	if err != nil {
		t.Fatalf("readGates: %v", err)
	}
	required := map[gen.OperatorPermission]bool{}
	for _, gate := range g {
		required[gen.OperatorPermission(gate.Requirement)] = true
	}
	for _, p := range allPermissions() {
		if !required[p] {
			t.Errorf("no operation requires %q", p)
		}
	}
}

// TestRoleGrantsCoverTheEnum pins the duplicated `ROLE_PERMISSIONS` table.
//
// It is duplicated from `web/contract/src/index.ts` because the contract keeps
// it out of `openapi.yaml` on purpose, so nothing mechanical connects the two.
// This is the substitute: every permission the enum declares is held by at least
// one role, and no role holds a permission the enum does not declare.
func TestRoleGrantsCoverTheEnum(t *testing.T) {
	held := map[gen.OperatorPermission]bool{}
	known := map[gen.OperatorPermission]bool{}
	for _, p := range allPermissions() {
		known[p] = true
	}
	for role, grants := range roleGrants {
		for _, p := range grants {
			if !known[p] {
				t.Errorf("role %q holds %q, which is not an OperatorPermission", role, p)
			}
			held[p] = true
		}
	}
	for _, p := range allPermissions() {
		if !held[p] {
			t.Errorf("no role holds %q — a permission nothing grants is a control nobody can use", p)
		}
	}
}

// TestStaffCannotManageTheNetwork states the split the contract argues for, so
// that widening it is a visible edit here rather than a quiet one in a map.
func TestStaffCannotManageTheNetwork(t *testing.T) {
	staff := permissionsFor(gen.Staff)
	mustNot := []gen.OperatorPermission{
		gen.NasManage, gen.GardenManage, gen.PortalManage, gen.OperatorsManage, gen.AuditRead,
	}
	for _, p := range mustNot {
		if slices.Contains(staff, p) {
			t.Errorf("staff holds %q", p)
		}
	}
	// And the narrow reading: staff *may* read the network configuration. A
	// console where a staff member cannot see that shared secrets exist is a
	// console where they cannot tell an administrator that one needs rotating.
	for _, p := range []gen.OperatorPermission{gen.NasRead, gen.GardenRead, gen.PortalRead} {
		if !slices.Contains(staff, p) {
			t.Errorf("staff does not hold %q, which the contract says it must", p)
		}
	}
	if len(permissionsFor("nonexistent-role")) != 0 {
		t.Error("an unrecognised role was granted something")
	}
}

// TestCSRFCoversEveryStateChangingAdminOperation makes true, on the Go side, the
// claim the contract makes about `web/mock`.
//
// The mock issues a CSRF token and never checks it — its own comment says so —
// so the rule "every state-changing request outside the portal tag" has never
// been exercised anywhere. This computes the expected set from the contract's
// own facts (method, tag, `security: []`) and compares it against what the gate
// table derived, so the rule and its enforcement cannot drift apart.
func TestCSRFCoversEveryStateChangingAdminOperation(t *testing.T) {
	g, err := readGates()
	if err != nil {
		t.Fatalf("readGates: %v", err)
	}
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}

	var want, got []string
	for pattern, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			_ = pattern
			// A download is a GET whose 200 is `text/csv` (the exports): it writes an
			// audit entry and hands personal data out, so it is guarded like a write.
			download := false
			if ok := op.Responses.Status(http.StatusOK); method == http.MethodGet && ok != nil && ok.Value != nil {
				download = ok.Value.Content.Get("text/csv") != nil
			}
			stateChanging := method == http.MethodPost || method == http.MethodPut ||
				method == http.MethodPatch || method == http.MethodDelete || download
			guestFacing := slices.Contains(op.Tags, "portal")
			sessionless := op.Security != nil && len(*op.Security) == 0
			if stateChanging && !guestFacing && !sessionless {
				want = append(want, op.OperationID)
			}
			if g[op.OperationID].NeedsCSRF {
				got = append(got, op.OperationID)
			}
		}
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, ",") != strings.Join(got, ",") {
		t.Errorf("CSRF coverage differs from the contract's rule\n want: %v\n  got: %v", want, got)
	}
	if len(want) == 0 {
		t.Fatal("computed an empty expected set — the derivation is vacuous")
	}
}

// TestSessionCookieAttributes pins the session cookie's attributes.
//
// `__Host-` is refused by the browser unless the cookie is Secure, has Path=/
// and carries no Domain, which is what stops the guest portal — a sibling host
// reachable pre-authentication by anyone within radio range — from setting a
// cookie the console will read.
func TestSessionCookieAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	setSessionCookie(rec, "value", timeFixture())

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != "__Host-foyer_admin_session" {
		t.Errorf("name is %q, want the __Host- prefixed name", c.Name)
	}
	if !c.Secure {
		t.Error("not Secure — __Host- requires it and a browser will refuse the cookie")
	}
	if !c.HttpOnly {
		t.Error("not HttpOnly — a readable session cookie is one a script can exfiltrate")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite is %v, want Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path is %q, want /", c.Path)
	}
	if c.Domain != "" {
		t.Errorf("Domain is %q — __Host- forbids one", c.Domain)
	}
}

// TestSameTokenRefusesTheEmptyCase is small and specific.
//
// `subtle.ConstantTimeCompare` returns 1 for two empty strings, so a session
// with no CSRF token would accept a request with no header — the check would be
// present, run on every request, and protect nothing.
func TestSameTokenRefusesTheEmptyCase(t *testing.T) {
	if sameToken("", "") {
		t.Error("empty submitted and empty stored compared equal")
	}
	if sameToken("abc", "") || sameToken("", "abc") {
		t.Error("one empty side compared equal")
	}
	if !sameToken("abc", "abc") {
		t.Error("equal tokens did not compare equal")
	}
}

// TestSessionCookieNameIsTheContractsName is deliberately not a second copy of
// the string.
//
// `TestSessionCookieAttributes` above spells `__Host-foyer_admin_session` out.
// That pins the *server* against an accidental edit and says nothing at all
// about whether the document a client is generated from agrees. It did not: the
// contract named the cookie `foyer_admin_session` while this server set the
// `__Host-` prefixed name, so a client written from
// `securitySchemes.sessionCookie` was reading a cookie nothing writes — and
// every test in this package passed, because every one of them asked the server
// what it had just done.
//
// The name is read out of the embedded contract rather than restated here, so
// the two cannot drift again in either direction: an edit to `openapi.yaml`
// fails this, and so does an edit to `sessionCookieName`.
func TestSessionCookieNameIsTheContractsName(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	scheme := spec.Components.SecuritySchemes["sessionCookie"]
	if scheme == nil || scheme.Value == nil {
		t.Fatal("the contract declares no `sessionCookie` security scheme — this test is vacuous")
	}
	if scheme.Value.Type != "apiKey" || scheme.Value.In != "cookie" {
		t.Fatalf("`sessionCookie` is %q in %q, not an apiKey in a cookie — its `name` means something else",
			scheme.Value.Type, scheme.Value.In)
	}
	if scheme.Value.Name != sessionCookieName {
		t.Errorf("the contract names the session cookie %q; this server sets %q",
			scheme.Value.Name, sessionCookieName)
	}
	// And the prefix itself, because resolving the drift by moving the *server*
	// to the contract's unprefixed name would satisfy the equality above while
	// dropping the prefix. `__Host-` is enforced by the user agent rather than by us: a
	// cookie so named is refused unless it is Secure, `Path=/` and carries no
	// `Domain`, which is what stops the guest portal — a sibling host anyone
	// within radio range can reach before authenticating — from setting a cookie
	// the console will read.
	if !strings.HasPrefix(scheme.Value.Name, "__Host-") {
		t.Errorf("the contract names the session cookie %q, which carries no `__Host-` prefix",
			scheme.Value.Name)
	}
}

func allPermissions() []gen.OperatorPermission {
	return []gen.OperatorPermission{
		gen.SessionsRead, gen.SessionsManage, gen.VouchersManage, gen.AccountsManage,
		gen.NasRead, gen.NasManage, gen.GardenRead, gen.GardenManage,
		gen.PortalRead, gen.PortalManage, gen.OperatorsManage, gen.AuditRead,
		gen.PrivacyManage,
		gen.ReportsExport,
	}
}

// TestEveryGuestFormHasAPageToReturnTo derives the guest form operations from the
// contract — every one whose request body is a form — and requires a page for each
// in `guestFormPage`, so a seventh cannot be added that answers a refusal as JSON.
func TestEveryGuestFormHasAPageToReturnTo(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	var forms []string
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			if op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			if op.RequestBody.Value.Content.Get("application/x-www-form-urlencoded") == nil {
				continue
			}
			id := strings.ToUpper(op.OperationID[:1]) + op.OperationID[1:]
			forms = append(forms, id)
			if _, ok := guestFormPage[id]; !ok {
				t.Errorf("%s takes a form and has no page in guestFormPage: a refusal would reach the CNA as JSON", id)
			}
		}
	}
	if len(forms) != len(guestFormPage) {
		t.Errorf("the contract has %d form operations and guestFormPage names %d", len(forms), len(guestFormPage))
	}
}
