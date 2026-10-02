package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
)

// What a skip is allowed to say, and what happens when it stops being true.
//
// ── the species this file exists against ─────────────────────────────────────
//
// A skip in `scenario.go` reading *"not implemented in Go — phase 4"* goes on
// reading it after the operation is implemented and
// `api.NotImplementedOperations()` is empty. **Nothing goes red when the reason
// stops being true**, because a skip removes the coverage that would have gone
// red and leaves a sentence in its place.
//
// That is a check that looks like one and is not, and of an unusual species: not an assertion that
// cannot fail, not an input that cannot be wrong, not an assertion aimed where the
// subject does not appear, not a lock that passes by waiting, not coverage that
// cannot see an unrun branch. It is **coverage that removed itself and left a note
// explaining why**, and the note outlived the fact.
//
// ── what this can enforce, honestly ──────────────────────────────────────────
//
// The rule the species implies is *a skip must name a condition a test can
// evaluate, not a phase*. Most of that is a property of English and belongs to
// whoever reads the reason. Two parts of it are mechanical, and only two:
//
//  1. **A skip may not claim an operation is not implemented in Go unless the
//     package says so.** `api.NotImplementedOperations()` is the package's own
//     statement and exists for exactly this caller.
//  2. **A skip may not date itself to a phase, a wave or a milestone.** A phase
//     number is the shape of reason that cannot expire loudly: nothing in a run
//     can evaluate "phase 4", so nothing can notice when phase 4 ends.
//
// **Be honest about how much rule 1 currently derives.** `internal/api`'s
// `notimplemented_test.go` pins that slice empty *in both directions*, so today
// the check reduces to "no skip may say 'not implemented in Go'" — a constant
// dressed as a derivation. It becomes a real cross-check the day the slice can be
// non-empty again, and it is written against the function rather than against the
// constant so that day needs no edit here. Stating that is the point:
// `docs/rules/database.md` has a section about restating a rule whose authority
// is one file away, and a comment claiming more derivation than it performs is the
// same defect in prose.
//
// ── and it must not flatten the legitimate skips ─────────────────────────────
//
// There are twenty-odd `Skip:` and `AdapterSkip:` sites in `scenario.go` and most
// of them are correct: `SignOut` would end the session the rest of the scenario
// depends on, `ChangeOwnPassword` would change the password it signs in with,
// `CreateController` mints ids that embed `Date.now()`, several are "not yet
// scripted". Every one of those names something a reader can check, and none of
// them is touched by either rule above. A mechanism that made them fail would be
// a mechanism that trades one silence for a wall of noise.

// phaseWords are the ways a reason dates itself instead of naming a condition.
//
// Deliberately short. The failure being prevented is a reason that expires when a
// schedule moves, and every word here is one that only a schedule gives meaning
// to. A longer list would start catching sentences that legitimately mention when
// something happened — the difference is whether the reason is *about* the date.
var phaseWords = []string{"phase ", "wave ", "milestone ", "sprint ", "later this "}

// notImplementedClaim is the sentence a skip may not make on its own authority.
const notImplementedClaim = "not implemented in go"

// validateSkips refuses a scenario whose skips have stopped being true.
//
// It runs before either server is driven, because the alternative is a report that
// says a run agreed on everything it compared while quietly not comparing an
// operation for a reason that expired. A skip is not a smaller assertion; it is
// the absence of one, and the absence has to be justified on every run.
func validateSkips(steps []step) error {
	if err := everyOperationAccountedFor(steps); err != nil {
		return err
	}
	implemented := func(op string) bool { return !api.NotImplemented(op) }

	var faults []string
	seen := map[string]bool{}
	for _, s := range steps {
		for _, reason := range []string{s.Skip, s.AdapterSkip} {
			if reason == "" {
				continue
			}
			key := s.Op + "|" + reason
			if seen[key] {
				continue
			}
			seen[key] = true
			lower := strings.ToLower(reason)

			if strings.Contains(lower, notImplementedClaim) && implemented(s.Op) {
				faults = append(faults, fmt.Sprintf(
					"%s is skipped as %q, but `internal/api` implements it — "+
						"`api.NotImplementedOperations()` does not name it. Either the operation "+
						"really is unbuilt, in which case add it to `notImplementedOperations` "+
						"and let `notimplemented_test.go` see it, or the reason has expired and "+
						"the step has to be scripted",
					s.Op, notImplementedClaim))
			}
			for _, w := range phaseWords {
				if strings.Contains(lower, w) {
					faults = append(faults, fmt.Sprintf(
						"%s is skipped for a reason that names %q. A phase is not a condition a "+
							"run can evaluate, so nothing here can notice when it ends — which is "+
							"how five portal operations stayed uncompared for weeks after they were "+
							"built. Say what has to be true for this step to be drivable",
						s.Op, strings.TrimSpace(w)))
					break
				}
			}
		}
	}
	if len(faults) == 0 {
		return nil
	}
	sort.Strings(faults)
	return fmt.Errorf("%d skip(s) name a reason this run cannot evaluate:\n    · %s\n\n"+
		"  A skip is the absence of a comparison, and the absence has to justify itself on\n"+
		"  every run. See `docs/rules/testing.md`, \"a skip whose reason expires\".",
		len(faults), strings.Join(faults, "\n    · "))
}

// everyOperationAccountedFor refuses a scenario that leaves an operation out of
// both lists.
//
// **This is the worse half of the species.** An expired skip is a sentence that
// has stopped being true; an operation in neither list has no sentence at all.
// `report` already prints `NO REASON RECORDED` for one, and a line at the bottom
// of `make diff-api`'s output is a line nobody reads — so an operation as central
// as `createSite` can sit there without anything failing, and the shape recurs
// every time an operation is added.
//
// The denominator is read off the generated interface, so this cannot fall behind
// the contract: an operation added to `openapi.yaml` appears in
// `StrictServerInterface` on the next `make generate` and is required here on the
// same run. That is deliberately inconvenient — the inconvenience is the point,
// and it lands on whoever adds the operation rather than on whoever reads the
// report six weeks later.
func everyOperationAccountedFor(steps []step) error {
	accounted := map[string]bool{}
	for _, s := range steps {
		if s.Op != "" {
			accounted[s.Op] = true
		}
	}
	var missing []string
	for _, op := range generatedOperations() {
		if !accounted[op] {
			missing = append(missing, op)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%d contract operation(s) appear in neither list:\n    · %s\n\n"+
		"  Every operation is either driven or carries a `Skip` naming the condition that\n"+
		"  stops it being driven. One in neither is printed as `NO REASON RECORDED` at the\n"+
		"  bottom of a report nobody reads to the bottom of, where an operation can stay\n"+
		"  unscripted without anything noticing.",
		len(missing), strings.Join(missing, "\n    · "))
}
