package apistore

import (
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
)

// TestGardenKindOut_TheTwoEnumsAgreeOnOneWord.
//
// Converting the driver's garden value into the contract's with a bare
// `gen.GardenKind(caps.WalledGarden)` is wrong. The two enums are three-valued for
// the same reason and spell two of the three differently, so that conversion
// emits `fqdn` and `ip` — values `GardenKind.Valid()` rejects and the console's
// three-way switch does not know.
//
// Nothing else would catch it, because the producers can agree by accident: a
// driver that returns the one word both enums share, and a fake that returns the
// wrong spelling of a capability no deployment has, which nothing asserts on — a
// fake encoding its author's assumption, seen from the output side.
//
// The expectations are read off `GardenKind.Valid()` rather than typed, so a
// value that stops being a member of the contract's enum fails here.
func TestGardenKindOut_TheTwoEnumsAgreeOnOneWord(t *testing.T) {
	for _, tc := range []struct {
		in   nas.WalledGardenType
		want gen.GardenKind
	}{
		{nas.WalledGardenFQDN, gen.GardenKindFqdnSni},
		{nas.WalledGardenIP, gen.GardenKindIpRange},
		{nas.WalledGardenNone, gen.GardenKindNone},
	} {
		got := gardenKindOut(tc.in)
		if got != tc.want {
			t.Errorf("gardenKindOut(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if !got.Valid() {
			t.Errorf("gardenKindOut(%q) = %q, which is not a member of the contract's enum", tc.in, got)
		}
	}

	// The defect, re-derived from the two enums rather than remembered: the
	// conversion that stood in `probeNas` produced a value the contract rejects.
	// This goes red the day somebody aligns the vocabularies, which is the day
	// this function becomes dead code and should be deleted rather than kept.
	if bare := gen.GardenKind(nas.WalledGardenFQDN); bare.Valid() {
		t.Errorf("gen.GardenKind(nas.WalledGardenFQDN) = %q and is now valid; the two enums have "+
			"converged and this translation is dead code", bare)
	}

	// A driver reporting something neither enum knows degrades to `none`. The
	// weakest value, never the strongest: an unrecognised garden is not a
	// garden the console may promise.
	if got := gardenKindOut(nas.WalledGardenType("dns-only")); got != gen.GardenKindNone {
		t.Errorf("an unrecognised garden became %q, want %q", got, gen.GardenKindNone)
	}
}

// TestGardenKindOut_MatchesTheShippedDriver.
//
// The one value that reaches this function on a real deployment, derived from
// the driver rather than named here, so the pair cannot drift apart quietly.
func TestGardenKindOut_MatchesTheShippedDriver(t *testing.T) {
	shipped := nas.ShippedCapabilities(nas.VendorUniFi).WalledGarden
	if got := gardenKindOut(shipped); !got.Valid() {
		t.Errorf("the shipped UniFi driver reports %q, which converts to the invalid %q", shipped, got)
	}
}
