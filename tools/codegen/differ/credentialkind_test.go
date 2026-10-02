package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Every member of `AccountCredentialKind` must decompose into verifiers the seeder
// can write, and the enum is read from the contract rather than typed here.
//
// ── the failure this exists to make loud ─────────────────────────────────────
//
// A seeder that writes the NT hash on `a.CredentialKind == gen.Argon2idNt` stops
// covering the world the moment the enum grows, without going red anywhere: the
// `+bcrypt` accounts get no verifier, the `+nt+bcrypt` account does not get even its
// NT hash, and `make diff-api` reports `credentialKind` as a defect in
// `internal/apistore` — against a product that is answering correctly about the
// database this file gave it. That reads as a security-visibility gap that does not
// exist.
//
// Widening the condition for each new member fixes that member and nothing else.
// **The next member widens nothing and goes quiet the same way**, which is why the
// seeder reads a kind apart into components and why this test asks the contract what the
// members are.
//
// ── why the contract and not `gen`'s constants ───────────────────────────────
//
// `oapi-codegen` emits the members as four separate `const`s and no slice, so a list
// here would be a hand-maintained second copy of the enum — the drift
// `docs/rules/database.md` names, and the one this whole test is about. The
// document is embedded in the binary and is the authority for what the enum is.
//
// ── how this fails ───────────────────────────────────────────────────────────
//
// The second half is the proof and it runs on every pass: `argon2id+scrypt` is fed
// to the same detector and must be reported as unseedable. Without it, a detector
// that returned nil for everything would pass the first half against any enum at
// all — an assertion that cannot fail, which `docs/rules/testing.md`'s
// Principles say a test must be seen failing to rule out.
//
// Driven for real as well, from the side that does not touch a shared file:
// narrowing `seededCredentialComponents` to `{"nt"}` — the same proposition as a
// member arriving with a component nothing writes — turns the first half red on
// **both** `argon2id+bcrypt` and `argon2id+nt+bcrypt`, naming `bcrypt`.
func TestEverySeededCredentialKindDecomposes(t *testing.T) {
	members := credentialKindEnum(t)
	if len(members) < 2 {
		t.Fatalf("the contract declares %d members of AccountCredentialKind; with fewer "+
			"than two this test cannot distinguish a seeder that handles the enum from "+
			"one that handles nothing", len(members))
	}
	if !slices.Contains(members, string(gen.Argon2idNtBcrypt)) {
		t.Fatalf("the contract's AccountCredentialKind no longer declares %q. That member "+
			"is the one both of pgseed's arms have to fire for, and it is what made the "+
			"single equality test's silence visible", gen.Argon2idNtBcrypt)
	}

	for _, m := range members {
		if unknown := unknownCredentialComponents(m); len(unknown) > 0 {
			t.Errorf("the contract declares credentialKind %q and pgseed.go cannot write "+
				"a verifier for %v. Every account the oracle reports with that kind will be "+
				"seeded without it, and `make diff-api` will report the difference as a "+
				"defect in `internal/apistore`. Teach credentialVerifiers the verifier and "+
				"add its component to seededCredentialComponents.", m, unknown)
		}
	}

	// The detector detects. A component no scheme in this schema uses, run through
	// the same function, must come back named — otherwise the loop above is green
	// for any enum whatsoever.
	if unknown := unknownCredentialComponents("argon2id+scrypt"); !slices.Contains(unknown, "scrypt") {
		t.Fatalf("unknownCredentialComponents(%q) returned %v; a component no verifier "+
			"exists for has to be named, or the assertion above cannot fail",
			"argon2id+scrypt", unknown)
	}
	// And the base scheme is part of the claim: a kind that is not built on argon2id
	// is not something this seeder can produce either.
	if unknown := unknownCredentialComponents("pbkdf2+nt"); !slices.Contains(unknown, "pbkdf2") {
		t.Fatalf("unknownCredentialComponents(%q) returned %v; the base scheme is not "+
			"checked, so a kind built on something other than %q would pass",
			"pbkdf2+nt", unknown, credentialBase)
	}
}

// credentialKindEnum reads the members out of the embedded specification.
//
// `encoding/json` over `gen.GetSpecJSON()` rather than `kin-openapi`, for the reason
// `internal/api`'s own ceiling reader gives: the raw bytes are enough for one enum,
// and `go.mod` is frozen.
func credentialKindEnum(t *testing.T) []string {
	t.Helper()
	raw, err := gen.GetSpecJSON()
	if err != nil {
		t.Fatalf("reading the embedded OpenAPI document: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the embedded OpenAPI document is not JSON: %v", err)
	}
	members := doc.Components.Schemas["Account"].Properties["credentialKind"].Enum
	if len(members) == 0 {
		t.Fatal("the contract declares no enum for Account.credentialKind; this test " +
			"reads its subject from there and has nothing to assert over")
	}
	return members
}
