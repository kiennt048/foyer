package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Whether a kind of change belongs to a venue is a **decision**, and this file is
// what stops the decision going missing.
//
// `AuditEntry.siteId` is nullable and the contract says what null means — *"null for
// a change that is not a site's"*. Without a rule every entry this package writes is
// null, so the member means *nobody recorded the venue* for all eighteen kinds while
// claiming to mean the other thing. What closes that is not a column:
// `audit_log.site_id` and its `(site_id, at DESC)` index exist from migration
// `00003`, and `AuditRepo.List` filters on them. It is a **rule**, written down
// once in `auditScopes` and consulted rather than repeated at seventeen call sites.
//
// A rule that only some kinds obey is worse than no rule, because a reader cannot
// tell which half they are holding. So the completeness of the table is checked
// against the contract's own enum rather than against a number typed here — a
// nineteenth kind added to `openapi.yaml` fails this test on the run that adds it,
// with nothing to change but a decision.

// auditKindsInContract reads the enum out of the embedded document.
//
// **From the contract, never a list beside it** — the same derivation
// `TestTheContractAndTheInterfaceAgree` uses, and invariant 11's rule. A second copy
// of these eighteen names here would agree with the table forever and with the
// contract only until somebody edited one of them.
func auditKindsInContract(t *testing.T) []gen.AuditKind {
	t.Helper()
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	schema, ok := spec.Components.Schemas["AuditKind"]
	if !ok || schema.Value == nil {
		t.Fatal("the contract has no AuditKind schema, so nothing here can be derived from it")
	}
	if len(schema.Value.Enum) == 0 {
		t.Fatal("AuditKind declares no enum members — every assertion below would be vacuous")
	}
	out := make([]gen.AuditKind, 0, len(schema.Value.Enum))
	for _, v := range schema.Value.Enum {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("AuditKind member %v is not a string", v)
		}
		out = append(out, gen.AuditKind(s))
	}
	return out
}

// TestEveryAuditKindDeclaresItsScope — both directions, because each catches a
// different way for the rule to rot.
//
// A **member with no row** is a kind whose entries silently go back to carrying no
// venue: the zero value of `auditScope` is `scopeUndecided`, `recordIn` writes no
// site for it, and the log quietly regains the hole this table closes. A **row
// with no member** is a decision about a kind that no longer exists, which reads as
// coverage and is not.
//
// Deleting `gen.AuditKindVoucherRevoked` from the table fails it with
// `voucher-revoked is in the contract and has no entry in auditScopes`.
func TestEveryAuditKindDeclaresItsScope(t *testing.T) {
	inContract := map[gen.AuditKind]bool{}
	for _, kind := range auditKindsInContract(t) {
		inContract[kind] = true
		switch auditScopes[kind] {
		case scopeSite, scopeDeployment:
		default:
			t.Errorf("%s is in the contract and has no entry in auditScopes — its entries will "+
				"carry no venue, and null on them will mean two things again", kind)
		}
	}
	for kind := range auditScopes {
		if !inContract[kind] {
			t.Errorf("auditScopes decides the scope of %s, which is not a member of AuditKind — "+
				"a decision about a kind nothing can write is not coverage", kind)
		}
	}
}

// TestBothAuditScopesAreUsed is the vacuity guard on the test above.
//
// A table that answered `scopeDeployment` for all eighteen kinds would satisfy
// *"every kind declares its scope"* completely and would be the original defect
// under a new name: every entry site-less, every null meaning nothing. The same is
// true the other way — `scopeSite` everywhere would file the creation of an operator
// under whichever venue the administrator happened to be looking at.
//
// `docs/rules/frontend.md`: a world that cannot express a property makes every
// check of that property vacuous. This one asserts the property is expressible in
// both directions before the test above asserts it is decided.
func TestBothAuditScopesAreUsed(t *testing.T) {
	counts := map[auditScope]int{}
	for _, scope := range auditScopes {
		counts[scope]++
	}
	if counts[scopeSite] == 0 {
		t.Error("no kind is site-scoped, so no audit entry can ever answer a venue's filter")
	}
	if counts[scopeDeployment] == 0 {
		t.Error("no kind is deployment-wide, so a change belonging to no venue has nowhere honest to go")
	}
}

// TestADeploymentWideKindCannotBeGivenAVenue.
//
// The table is the authority and `recordIn` consults it, so a handler that passes a
// site for a kind that has none does not get to write one. It matters because the
// careless repair for this whole defect is *stamp the caller's current site onto
// every entry* — that satisfies a positive assertion and files the creation of an
// operator under a café. This is the gate that makes that impossible from inside the
// package rather than by review.
//
// Driven through `record`/`recordIn` with a stub port, so what is asserted is the
// entry that reaches the store rather than what a handler intended.
func TestADeploymentWideKindCannotBeGivenAVenue(t *testing.T) {
	ctx := context.Background()
	written := &capturingAudit{}
	s := &Server{
		ports: Ports{Audit: written},
		now:   func() time.Time { return time.Unix(0, 0).UTC() },
		newID: func(prefix string) string { return prefix + fmt.Sprint(len(written.entries)) },
	}
	who := &caller{Operator: Operator{Id: "op-1", Username: "linh"}}

	s.recordIn(ctx, who, "site-hcm", gen.AuditKindOperatorCreated,
		gen.AuditSubject{Kind: gen.AuditSubjectKindOperator, Id: "op-2"}, nil)
	s.recordIn(ctx, who, "site-hcm", gen.AuditKindGardenEntryAdded,
		gen.AuditSubject{Kind: gen.AuditSubjectKindGardenEntry, Id: "g-1"}, nil)
	s.record(ctx, who, gen.AuditKindControllerRegistered,
		gen.AuditSubject{Kind: gen.AuditSubjectKindController, Id: "ctl-1"}, nil)

	if len(written.entries) != 3 {
		t.Fatalf("wrote %d entries, want 3 — the rest of this test would be asserting on absences",
			len(written.entries))
	}
	if written.entries[0].SiteId != nil {
		t.Errorf("operator-created was written with siteId %q even though the caller's venue was "+
			"passed in; the kind is deployment-wide and the table decides, not the handler",
			*written.entries[0].SiteId)
	}
	if written.entries[1].SiteId == nil || *written.entries[1].SiteId != "site-hcm" {
		t.Errorf("garden-entry-added reached the store with siteId %v, want the venue it was made in",
			written.entries[1].SiteId)
	}
	if written.entries[2].SiteId != nil {
		t.Errorf("controller-registered carries a venue: %q", *written.entries[2].SiteId)
	}
}

// capturingAudit is a stand-in for the store and nothing else — it answers no
// query, because these tests read what was written rather than what is listed.
type capturingAudit struct{ entries []AuditEntry }

func (c *capturingAudit) Audit(_ context.Context, _ AuditQuery) (Page[AuditEntry], error) {
	return Page[AuditEntry]{Items: c.entries}, nil
}

func (c *capturingAudit) Append(_ context.Context, e AuditEntry) error {
	c.entries = append(c.entries, e)
	return nil
}
