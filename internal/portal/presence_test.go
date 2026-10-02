package portal

import (
	"context"
	"errors"
	"testing"

	"github.com/kiennt048/foyer/internal/nas"
)

// What a refusal *says*, as distinct from what it *does*.
//
// `Classify` is covered by the service suite, which asserts that nothing is
// granted. These assert the other half: that the guest is told something true.
// They are a separate file because they are a separate claim — a refusal that
// correctly withholds a grant and describes the wrong situation is a defect that
// no test of the grant can see, and that is exactly what shipped.

// TestAGuestUnderAnUnconfirmedAccessPointIsNotToldTheyAreMissing.
//
// **The lock, and it is red against the mapping this replaces.** `Verdict` had a
// `Reason()` method, so every refusal answered `not-present` — *"Your device
// could not be confirmed on this network. Turn Wi-Fi off and on again to start
// over."* — including the guest whose device the controller had just returned a
// client row for. They are standing in the venue, connected to the SSID, reading
// a page served over it, being told they are not on it.
//
// Driven through `Redeem` rather than through `Explain` alone: the reason has to
// survive the service, because the service is what puts it in the `303`.
func TestAGuestUnderAnUnconfirmedAccessPointIsNotToldTheyAreMissing(t *testing.T) {
	r := newRig(t, nas.Presence{Outcome: nas.PresenceAPMismatch, ClientID: "c-1"}, nil)

	out := r.svc.Redeem(context.Background(), r.session, "abcd-efgh-jkmn")

	if out.Connected {
		t.Fatal("a device the driver would not vouch for was let on — §4 is unconditional")
	}
	if out.Reason != ReasonAPUnconfirmed {
		t.Errorf("reason is %q, want %q — the guest is associated here and %q tells them they are not",
			out.Reason, ReasonAPUnconfirmed, ReasonNotPresent)
	}
	// And the refusal is still a refusal: the single-use code must not be spent
	// on a grant that did not happen.
	if len(r.store.Grants()) != 0 {
		t.Error("a grant exists off an unconfirmed access point")
	}
}

// TestEveryRefusingOutcomeGetsTheSentenceItEarns.
//
// The table is over `nas`'s own outcomes rather than over the reasons, because
// the input side is where the collapse was: six outcomes reached four verdicts
// reached two sentences.
//
// **`PresenceSiteMismatch` staying `not-present` is asserted, not left out.** It
// is the case the fix could most easily have swept up, and the line is real:
// access points inside a site are one network, so *"not on this network"* is
// false for a device under the wrong one; sites are separate networks, so for a
// client the controller places in another site the sentence is true as written.
func TestEveryRefusingOutcomeGetsTheSentenceItEarns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome nas.PresenceOutcome
		want    Reason
	}{
		{"a device that never associated", nas.PresenceNotFound, ReasonNotPresent},
		{"a device in another site — another network", nas.PresenceSiteMismatch, ReasonNotPresent},
		{"a device here, under an access point nothing vouched for", nas.PresenceAPMismatch, ReasonAPUnconfirmed},
		{"the venue's controller", nas.PresenceUnavailable, ReasonGeneric},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Explain(nas.Presence{Outcome: tc.outcome}, nil); got != tc.want {
				t.Errorf("Explain(%s) = %q, want %q", tc.outcome, got, tc.want)
			}
		})
	}
}

// TestAnOutageBesideAnAPMismatchIsStillTheVenuesFault.
//
// The ordering inside `Explain`, and it is the same ordering `Classify` states
// for the same reason. A driver that answered `PresenceAPMismatch` beside an
// error would be a driver bug; reading the outcome first would turn that bug into
// an instruction to the guest to reconnect, forever, over a controller that is
// down. `Explain` asks `Classify` rather than re-testing the error, so the two
// cannot drift into disagreeing about the same answer.
func TestAnOutageBesideAnAPMismatchIsStillTheVenuesFault(t *testing.T) {
	p := nas.Presence{Outcome: nas.PresenceAPMismatch}
	if got := Explain(p, errors.New("i/o timeout")); got != ReasonGeneric {
		t.Errorf("Explain = %q, want %q — an outage was reported as the guest's placement", got, ReasonGeneric)
	}
	if Classify(p, errors.New("i/o timeout")) != VerdictUnavailable {
		t.Error("Classify and Explain disagree about the same answer")
	}
}

// TestNothingExplainSaysIsAGrant.
//
// `Explain` exists to describe refusals and has no business being consulted
// about a success, but nothing in the type system says so — it takes the same
// `nas.Presence` `Classify` does. This pins that a verified answer does not
// acquire a failure sentence that some later caller might render beside a
// connected page.
func TestNothingExplainSaysIsAGrant(t *testing.T) {
	p := nas.Presence{OK: true, Outcome: nas.PresenceVerified, APConfirmed: true}
	if Explain(p, nil) == ReasonAPUnconfirmed {
		t.Error("a confirmed access point produced the unconfirmed sentence")
	}
	if !Classify(p, nil).MayGrant() {
		t.Fatal("the fixture is not a grant, so this test asserts nothing about one")
	}
}
