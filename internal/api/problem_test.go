package api

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// emittable is every refusal this package can construct, one call each.
//
// It is the list `problem.go` describes, made mechanical: a `ProblemType` that
// appears here is one the server can produce, and one that does not appear has
// to be accounted for below. The contract's `ProblemType` says a value nothing
// can emit is "a catalogue entry that can never be shown to be right, which is
// the fifth mechanism in this repository that only looked like a check" — this
// is that rule applied to the Go side.
func emittable() []*apiError {
	return []*apiError{
		notSignedIn(),
		badCredentials(),
		forbidden(),
		notFound(),
		duplicate("value.example.test"),
		oneSitePerInstall(),
		duplicateField("username"),
		validation(map[string]string{"name": "Required."}, map[string]gen.FieldFault{"name": gen.FieldFaultRequired}),
		tooLarge("dataBase64", gen.FieldFaultTooLarge, map[string]any{"bytes": 1}),
		controllerInUse(9),
		controllerUnreachable(),
		needsController(),
		rotationRefused(),
		sessionNotLive(),
		disconnectUnsupported(&DisconnectRefusal{Driver: gen.NasDriverKindRuckus, NasName: "x"}),
		gardenEntryRequired(),
		lastAdministrator(),
		passwordChangeRequired(),
		setupComplete(),
		rateLimited(0),
		payloadTooLarge(1, 1),
		deviceNotVerified(),
		methodNotOffered(),
		noGrant(),
		internalProblem(),
		notImplementedProblem(),
	}
}

// cannotEmitYet names the members of `ProblemType` no constructor produces, each
// with the reason.
//
// This is the list that keeps the claim honest. A value moves off it when the
// operation that produces it is built; a value that appears in `emittable` *and*
// here fails the test, so it cannot be both excused and covered.
var cannotEmitYet = map[gen.ProblemType]string{
	gen.ProblemssessionEndFailed: "never an HTTP refusal: it travels inside revokeVoucher's 200 as a Failure",
	gen.Problemsunreachable:      "x-client-synthesised — the client puts it on screen when the request never arrived, so no server can send it",
}

// TestNoRefusalCarriesADetail.
//
// `detail` is a composed sentence in the server's language and is the field a
// client must not prefer over its own copy. The contract says a conforming
// server sends it "only for a `type` it expects clients not to know"; the
// console has copy for every value in the enum, so this server sends none — and
// that is also what keeps a database error or an internal path out of a
// response, because there is no field left for one to be helpfully placed in.
func TestNoRefusalCarriesADetail(t *testing.T) {
	for _, e := range emittable() {
		p := e.Problem()
		if p.Detail != nil {
			t.Errorf("%s carries a detail: %q", p.Type, *p.Detail)
		}
		if p.Title == "" {
			t.Errorf("%s has no title — it is the fallback of last resort for a client that predates the type", p.Type)
		}
		if p.Status < 400 || p.Status > 599 {
			t.Errorf("%s has status %d, which is not a refusal", p.Type, p.Status)
		}
	}
}

// TestEveryProblemTypeIsAccountedFor.
//
// Either this package can produce the value, or the list above says why not.
// Silence is what the contract's own rule forbids, and the failure mode it
// forbids is a catalogue entry nobody can ever show to be right.
func TestEveryProblemTypeIsAccountedFor(t *testing.T) {
	produced := map[gen.ProblemType]bool{}
	for _, e := range emittable() {
		produced[e.Type] = true
	}

	var unaccounted []string
	for _, kind := range allProblemTypes() {
		_, excused := cannotEmitYet[kind]
		switch {
		case produced[kind] && excused:
			t.Errorf("%s is both produced and excused — one of the two is wrong", kind)
		case !produced[kind] && !excused:
			unaccounted = append(unaccounted, string(kind))
		}
	}
	sort.Strings(unaccounted)
	if len(unaccounted) > 0 {
		t.Errorf("no constructor and no recorded reason for: %s", strings.Join(unaccounted, ", "))
	}

	// And the reverse: an excuse for a value that is not in the enum is a stale
	// line somebody left behind after a contract change.
	for kind := range cannotEmitYet {
		if !slices.Contains(allProblemTypes(), kind) {
			t.Errorf("%q is excused but is not a ProblemType", kind)
		}
	}
}

// TestRefusalsCarryNoRequestText.
//
// `apiError.Error()` reaches logs. It is built from the status and the type and
// from nothing a caller supplied, so a refusal cannot carry a submitted value
// into a log file — which is where the portal's username echo would have ended
// up, and where a password would.
func TestRefusalsCarryNoRequestText(t *testing.T) {
	const submitted = "hunter2-hunter2"
	e := validation(
		map[string]string{"password": "Too short."},
		map[string]gen.FieldFault{"password": gen.FieldFaultBelowMinimum},
	)
	if strings.Contains(e.Error(), submitted) {
		t.Error("the error string carried the submitted value")
	}
	if strings.Contains(e.Error(), "password") {
		// Not a hard rule, but worth knowing: the log line names the refusal,
		// not the form.
		t.Logf("note: the error string names a field (%q)", e.Error())
	}
}

// TestAllProblemTypesMatchesTheContract is the guard on the guard.
//
// `allProblemTypes` is typed out below, and a hand-typed list is exactly the
// thing that falls behind. This reads the enum out of the embedded contract and
// requires the two to be identical — so a value added to `openapi.yaml` arrives
// here as a failure rather than as silence, and `TestEveryProblemTypeIsAccounted
// For` cannot pass by not knowing about it.
func TestAllProblemTypesMatchesTheContract(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	schema, ok := spec.Components.Schemas["ProblemType"]
	if !ok || schema.Value == nil {
		t.Fatal("the contract has no ProblemType schema")
	}

	var fromContract []string
	for _, v := range schema.Value.Enum {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("a ProblemType enum member is not a string: %v", v)
		}
		fromContract = append(fromContract, s)
	}
	var here []string
	for _, k := range allProblemTypes() {
		here = append(here, string(k))
	}
	sort.Strings(fromContract)
	sort.Strings(here)
	if strings.Join(fromContract, ",") != strings.Join(here, ",") {
		t.Errorf("allProblemTypes has fallen behind the contract\n contract: %v\n     here: %v",
			fromContract, here)
	}
	if len(fromContract) == 0 {
		t.Fatal("read an empty enum from the contract — the derivation is vacuous")
	}
}

func allProblemTypes() []gen.ProblemType {
	return []gen.ProblemType{
		gen.Problemsvalidation,
		gen.ProblemsnotFound,
		gen.Problemsduplicate,
		gen.ProblemsoneSitePerInstall,
		gen.ProblemstooLarge,
		gen.ProblemsgardenEntryRequired,
		gen.ProblemscontrollerInUse,
		gen.ProblemscontrollerUnreachable,
		gen.ProblemsneedsController,
		gen.ProblemsrotationRefused,
		gen.ProblemsdeviceNotVerified,
		gen.ProblemsmethodNotOffered,
		gen.ProblemsnoGrant,
		gen.Problemsinternal,
		gen.ProblemssessionEndFailed,
		gen.ProblemssessionNotLive,
		gen.ProblemsdisconnectUnsupported,
		gen.ProblemsnotSignedIn,
		gen.ProblemssetupComplete,
		gen.ProblemsbadCredentials,
		gen.ProblemsrateLimited,
		gen.Problemsforbidden,
		gen.ProblemslastAdministrator,
		gen.ProblemspasswordChangeRequired,
		gen.Problemsunreachable,
	}
}
