package aaa

import (
	"context"
	"reflect"
	"testing"
)

// The named security test from `docs/rules/testing.md`:
//
//	**Inner/outer identity** -- a mismatched outer identity does not influence the
//	authorization decision.
//
// It can be made to fail. The sabotage is one line in identity.go --
// AuthorizationSubject returning OuterUserName when it is set -- and under it
// TestOuterIdentityCannotChooseTheAuthorizationSubject and
// TestOuterIdentityDoesNotInfluenceAnAllowedDecision turn red, for different
// reasons, which is the point of having more than one.

func innerOuterStore() *mockStore {
	return &mockStore{
		vouchers: map[string]*VoucherInfo{
			"INNER-OK": {Code: "INNER-OK", State: "unused", DurationMinutes: 60},
			// The identity an attacker would like to be authorized as. It is a
			// real, valid voucher -- that is what makes it worth stealing -- and
			// the only thing standing between a stranger and it is that nothing
			// keys on the field they control.
			"OUTER-OK": {Code: "OUTER-OK", State: "unused", DurationMinutes: 480},
		},
		accounts: map[string]*GuestAccountInfo{},
	}
}

// TestOuterIdentityCannotChooseTheAuthorizationSubject is the attack, spelled out.
//
// A supplicant sends whatever it likes outside the tunnel; nothing checks it,
// because not checking it is the entire purpose of an anonymous outer identity. So
// a Policy API that reads it lets the client name their own authorization subject:
// present `OUTER-OK` on the outside, a name that is not a voucher at all on the
// inside, and be authorized as the eight-hour voucher.
func TestOuterIdentityCannotChooseTheAuthorizationSubject(t *testing.T) {
	engine := NewEngine(innerOuterStore(), 300)

	dec, err := engine.Authorize(context.Background(), Request{
		UserName:      "NOT-A-VOUCHER",
		OuterUserName: "OUTER-OK",
		EAPMessage:    "present",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allow {
		t.Fatal("an identity that is not a voucher was authorized because the outer identity was one: the client chose their own authorization subject")
	}
}

// TestOuterIdentityDoesNotInfluenceAnAllowedDecision is the other direction, and
// it is the subtler one: a valid inner identity must produce the *same* decision
// whatever is outside it, attributes included.
//
// Asserting only Allow would pass for a build where the outer identity picks the
// rate limit.
func TestOuterIdentityDoesNotInfluenceAnAllowedDecision(t *testing.T) {
	engine := NewEngine(innerOuterStore(), 300)

	outers := []string{
		"",
		"anonymous",
		"anonymous@example.com",
		"@example.com",
		"OUTER-OK",         // a different, longer voucher
		"NOT-A-VOUCHER",    // nothing at all
		"INNER-OK@example", // the inner name with a realm bolted on
	}

	var baseline *Decision
	for _, outer := range outers {
		dec, err := engine.Authorize(context.Background(), Request{
			UserName:      "INNER-OK",
			OuterUserName: outer,
			EAPMessage:    "present",
		})
		if err != nil {
			t.Fatalf("outer %q: unexpected error: %v", outer, err)
		}
		if !dec.Allow {
			t.Fatalf("outer %q: the inner identity INNER-OK was refused: %s", outer, dec.Reason)
		}
		if baseline == nil {
			baseline = dec
			continue
		}
		if !reflect.DeepEqual(baseline.ReplyAttributes, dec.ReplyAttributes) {
			t.Errorf("outer %q changed the reply attributes from %v to %v",
				outer, baseline.ReplyAttributes, dec.ReplyAttributes)
		}
	}
}

// TestAnonymousInnerIdentityIsRefusedRatherThanLookedUp.
//
// If the tunnel never reached an inner identity -- or the generated `rest` module
// is sending `%{User-Name}` from the outer session, which is the same thing from
// here -- the subject is a value the client chose freely. Refusing is the narrow
// answer. Looking it up would make a voucher or account named `anonymous` reachable
// by somebody who has not authenticated at all, which is a plausible name for a
// venue's shared account.
//
// And it must be a *rejection*, not an error: an error reaches the site's failure
// branch, and on a fail-open site that would authorize the anonymous outer identity
// rather than refuse it.
func TestAnonymousInnerIdentityIsRefusedRatherThanLookedUp(t *testing.T) {
	store := innerOuterStore()
	store.vouchers["anonymous"] = &VoucherInfo{Code: "anonymous", State: "unused", DurationMinutes: 999}
	engine := NewEngine(store, 300)

	for _, subject := range []string{"anonymous", "anonymous@example.com", "ANONYMOUS@corp", "@example.com", "anon", "  "} {
		dec, err := engine.Authorize(context.Background(), Request{UserName: subject, EAPMessage: "present"})
		if err != nil {
			t.Fatalf("subject %q produced an error rather than a decision: %v -- an error reaches the failure branch, which on a fail-open site authorizes it", subject, err)
		}
		if dec.Allow {
			t.Errorf("subject %q was authorized; it is a tunnel placeholder, not a name", subject)
		}
	}
}

// TestIsAnonymousIdentityAndRealm pins the two string rules underneath.
//
// Realm splits on the *last* `@` because RFC 7542 §2.2 permits `@` inside the
// username: splitting on the first hands an attacker control of the realm by
// putting one in their own name.
func TestIsAnonymousIdentityAndRealm(t *testing.T) {
	anon := []string{"", "  ", "anonymous", "Anonymous", "ANONYMOUS@corp.example", "anon@x", "@realm"}
	for _, s := range anon {
		if !IsAnonymousIdentity(s) {
			t.Errorf("%q is a tunnel placeholder and was not recognised as one", s)
		}
	}

	named := []string{"linh", "linh@corp.example", "anonymous.linh", "not-anonymous", "VALID-123", "a@b@corp.example"}
	for _, s := range named {
		if IsAnonymousIdentity(s) {
			t.Errorf("%q is a name and was treated as a placeholder", s)
		}
	}

	if got := Realm("a@b@corp.example"); got != "corp.example" {
		t.Errorf("Realm split on the first @ and got %q; RFC 7542 §2.2 permits @ in the username, so it must split on the last", got)
	}
	if got := Realm("linh"); got != "" {
		t.Errorf("Realm of a bare name is %q, want empty", got)
	}
}
