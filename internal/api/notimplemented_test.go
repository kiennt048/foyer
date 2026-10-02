package api

import (
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// generatedOperationIDs reads the operation names off the generated interface.
//
// Off the *interface*, not off a list here: that is what makes it impossible for
// this file to fall behind the contract. An operation added to `openapi.yaml`
// appears in `StrictServerInterface` on the next `make generate`, so it appears
// here on the same run.
func generatedOperationIDs(t *testing.T) []string {
	t.Helper()
	iface := reflect.TypeOf((*gen.StrictServerInterface)(nil)).Elem()
	out := make([]string, 0, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		out = append(out, iface.Method(i).Name)
	}
	sort.Strings(out)
	return out
}

// TestTheContractAndTheInterfaceAgree derives the operation count from two
// independent sources.
//
// A hand-written router can register 27 routes against a contract of 50
// operations with nothing anywhere reporting the gap. A count derived from two
// independent sources cannot drift silently the same way.
func TestTheContractAndTheInterfaceAgree(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	var fromContract []string
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			fromContract = append(fromContract, strings.ToUpper(op.OperationID[:1])+op.OperationID[1:])
		}
	}
	sort.Strings(fromContract)

	fromInterface := generatedOperationIDs(t)
	if strings.Join(fromContract, ",") != strings.Join(fromInterface, ",") {
		t.Fatalf("the contract and the generated interface disagree\n contract: %d\ninterface: %d",
			len(fromContract), len(fromInterface))
	}
	// Why some of the 97 exist, where the reason is not obvious from the name:
	//
	// `createSite`: `createNas` requires a `siteId`, a migrated database holds zero
	// sites, and without it nothing in the product could put the first row in.
	//
	// `getNasEvidence`: the UNVERIFIED label on the CoA family has to reach the
	// admin console rather than living in the code, and it is its own operation
	// rather than a field on `NasProbe` because nothing in it is established by a
	// probe.
	//
	// `getRetentionPolicy`, `updateRetentionPolicy` and `eraseDataSubject`: a
	// retention period for personal data and a per-person erasure, which Law
	// 91/2025/QH15 Article 14 and GDPR Article 17 both require an operator to be
	// able to honour.
	//
	// `exportSessions`, `exportVouchers`, `exportGuests` and `getTrends`: the
	// `reports` tag — exports, and history for the dashboard.
	//
	// `disconnectSession`: a way to disconnect a guest, reaching the act every
	// driver already implements.
	//
	// Phone sign-in: two guest operations, `requestPhoneCode` and
	// `verifyPhoneCode`, because a code has to be sent before it can be typed and
	// both steps are form navigations; and `getPhoneSignIn` / `updatePhoneSignIn`,
	// because the provider's keys are write-only secrets that do not belong on
	// `PortalSettings`.
	//
	// `getGuestDataSettings`, `updateGuestDataSettings`, `listMarketingConsents`,
	// `withdrawMarketingConsent`, `withdrawOwnMarketingConsent` and
	// `exportMarketingConsents`: guest data and marketing consent.
	//
	// The `network` tag, eleven operations, because every operator setting is set
	// in the console. The portal's address, throttles and ceilings
	// (`getNetworkSettings`, `updateNetworkSettings`), its certificate
	// (`putPortalCertificate`, `deletePortalCertificate`), FreeRADIUS's generator
	// settings, EAP-TLS certificate, client-table sync and apply
	// (`getRadiusServerSettings`, `updateRadiusServerSettings`, `putEapCertificate`,
	// `syncRadiusClients`, `applyRadiusConfiguration`), and per-device CoA timing
	// (`listCoaTiming`, `updateNasCoaTiming`).
	//
	// Under the same rule, seven messaging operations: `getSiteTimeZone` /
	// `updateSiteTimeZone` (the calendar every daily figure counts in, rather than
	// an unset `TZ`), `getPhoneAbuseControls` / `updatePhoneAbuseControls` (the OTP
	// limits, tunable within bounds), `getPhoneSpendCap` / `updatePhoneSpendCap`
	// (the deployment-wide cap) and `sendPhoneTestMessage`.
	//
	// And five data operations: `getMaintenanceSettings` and
	// `updateMaintenanceSettings` (the interval and ceilings, rather than an
	// environment variable and constants), `getGuestWifiKey` and
	// `updateGuestWifiKey` (the guest network's WPA passphrase, write-only), and
	// `getVoucherSlipWifi` (the voucher slip's one read of it).
	//
	// `getAdminSetup` and `completeAdminSetup`: the first-access setup, so that no
	// administrator password ships with the install.
	//
	// `listPortalGrants` and `disconnectPortalGrant`: on a UniFi SSID with no
	// RADIUS accounting profile a guest has no `radacct` row, so there is no
	// session to disconnect; the portal's own open authorizations are listed and
	// ended on their controller, which is how UniFi's Hotspot Manager and Omada's
	// portal list do it.
	if len(fromInterface) != 97 {
		t.Errorf("expected 97 operations, found %d — if the contract genuinely changed, "+
			"update this number in the same commit as the reason", len(fromInterface))
	}
}

// TestNotImplementedIsAnExactList keeps the gap honest in both directions.
//
// The list held five names — the guest portal's — with the reason that each
// needed a real `VerifyPresence`, a MAC-bound grant store and the portal's own
// listener. All three exist and all five are implemented, so the list is empty.
//
// **An empty list still needs this test, and needs it more than a populated one
// did.** A populated list has a visible cost: somebody reading the file sees five
// operations refused. An empty one looks like nothing, so the only thing standing
// between it and a name quietly rejoining it is an assertion on the count. Every
// name must also be a real operation, so a rename in the contract cannot leave a
// stale entry excusing an operation that no longer exists.
func TestNotImplementedIsAnExactList(t *testing.T) {
	known := generatedOperationIDs(t)
	for _, id := range notImplementedOperations {
		if !slices.Contains(known, id) {
			t.Errorf("%q is listed as not implemented but is not an operation", id)
		}
	}
	if len(notImplementedOperations) != 0 {
		t.Errorf("the unimplemented set has %d members, expected 0 — "+
			"adding one back is fine, adding it silently is not: %v",
			len(notImplementedOperations), notImplementedOperations)
	}
}

// TestEveryOperationIsAMethodWithABody is the positive half of the empty
// unimplemented set above.
//
// An empty list of refusals is not a list of implementations, and the absence of
// a refusal is not evidence of an implementation:
// `Server` satisfies `StrictServerInterface` by construction, so a handler that
// returned `nil, nil` would compile, register, and answer an empty body. This
// requires every generated operation to be a method **declared on `*Server` in
// this package**, which is the property the compiler does not check.
func TestEveryOperationIsAMethodWithABody(t *testing.T) {
	srv := reflect.TypeOf(&Server{})
	var missing []string
	for _, id := range generatedOperationIDs(t) {
		m, ok := srv.MethodByName(id)
		if !ok {
			missing = append(missing, id)
			continue
		}
		// Two in and two out — `(ctx, request) (response, error)` — is the strict
		// shape. Anything else is a name collision with an unrelated method
		// rather than a handler.
		if m.Type.NumIn() != 3 || m.Type.NumOut() != 2 {
			missing = append(missing, id+" (wrong signature)")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("not implemented as methods on *Server: %s", strings.Join(missing, ", "))
	}
}

// TestUnimplementedOperationsAnswer501 is the point of the whole arrangement.
//
// Not `200` with plausible JSON — that is the defect this package was rewritten
// to remove — and not a silent `404`, which reads as a routing mistake. `501`
// with a problem document says the operation exists in the contract and this
// server has not built it.
func TestUnimplementedOperationsAnswer501(t *testing.T) {
	e := asAPIError(errNotImplemented)
	if e.Status != 501 {
		t.Errorf("status is %d, want 501", e.Status)
	}
	if e.Type != gen.Problemsinternal {
		t.Errorf("type is %q, want a ProblemType the contract declares", e.Type)
	}
	p := e.Problem()
	if p.Detail != nil {
		t.Error("carries a detail — a refusal must not carry server text a client would render")
	}
}

// TestTheGuestSurfaceIsExactlyThePortalTag.
//
// The console and the guest portal are on separate listeners, and the split is
// derived from the contract's `portal` tag rather than from a list. This is the
// derivation, checked: the guest surface is the portal operations and nothing
// else, and — the direction that matters — no operation requiring a
// permission is on it. An operation that needed `nas:manage` and appeared on the
// listener bound to `0.0.0.0` would be the console reachable pre-authentication
// by everyone within radio range.
func TestTheGuestSurfaceIsExactlyThePortalTag(t *testing.T) {
	g, err := readGates()
	if err != nil {
		t.Fatalf("readGates: %v", err)
	}
	var guest []string
	for id, gate := range g {
		if surfaceFor(gate) != surfacePortal {
			continue
		}
		guest = append(guest, id)
		if gate.Requirement != reqPublic {
			t.Errorf("%s is on the guest surface and requires %q — the portal listener is "+
				"bound wide and reachable before authentication", id, gate.Requirement)
		}
	}
	sort.Strings(guest)
	// PascalCase, because `oapi-codegen` rewrites the operationIds in the spec it
	// embeds and `readGates` keys on what it finds there. That is also what makes
	// `s.gates[operationID]` resolve at all, since the strict handler names
	// operations the same way.
	//
	// Phone sign-in is two of them: a code is requested, then typed. And a guest
	// withdraws their marketing consent from the portal they gave it on (Decree
	// 356/2025/ND-CP asks for an easy way out).
	want := []string{"AcceptTerms", "GetPortalContext", "GetPortalStatus", "LoginWithAccount", "RedeemVoucher",
		"RequestPhoneCode", "VerifyPhoneCode", "WithdrawOwnMarketingConsent"}
	if strings.Join(guest, ",") != strings.Join(want, ",") {
		t.Errorf("guest surface is %v, want %v", guest, want)
	}
}

func timeFixture() time.Time {
	return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
}
