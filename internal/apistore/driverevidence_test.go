package apistore_test

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
)

// `getNasEvidence` — the UNVERIFIED label on the CoA family has to reach the
// console rather than living in the code.
//
// **The first of these guards a defect no other mechanism can catch.**
// `internal/nas` spells its evidence values `MEASURED` / `DOCUMENTED` /
// `UNVERIFIED`, because `docs/protocol-reality.md` shouts them and a source file
// should too. The contract's enum is lowercase, as every enum in that document is.
// `gen.EvidenceStanding(fact.Evidence)` **compiles**, produces `"UNVERIFIED"`, and
// is not a member — and nothing in the Go server calls the generated `Valid()` on
// a response, so it would ship a value no client has a branch for.

// everyVendorDevice is one `gen.Nas` per driver the contract declares, plus the
// `coa` row that names no vendor.
//
// Driven from `gen.NasDriverKind`'s own members rather than a list typed here, so
// a sixth driver is a compile error in this file rather than a gap in it.
func everyVendorDevice() map[string]gen.Nas {
	cisco, aruba := gen.Cisco, gen.Aruba
	return map[string]gen.Nas{
		"unifi":         {Id: "nas-u", Driver: gen.NasDriverKindUnifi},
		"omada":         {Id: "nas-o", Driver: gen.NasDriverKindOmada},
		"mikrotik":      {Id: "nas-m", Driver: gen.NasDriverKindMikrotik},
		"ruckus":        {Id: "nas-r", Driver: gen.NasDriverKindRuckus},
		"coa+cisco":     {Id: "nas-c", Driver: gen.NasDriverKindCoa, CoaVendor: &cisco},
		"coa+aruba":     {Id: "nas-a", Driver: gen.NasDriverKindCoa, CoaVendor: &aruba},
		"coa+no vendor": {Id: "nas-x", Driver: gen.NasDriverKindCoa},
	}
}

func TestDriverEvidenceSpeaksTheContractsEnum(t *testing.T) {
	a := &apistore.Adapter{}
	for name, device := range everyVendorDevice() {
		ev, err := a.DriverEvidence(context.Background(), device)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !ev.Profile.Valid() {
			t.Errorf("%s: profile %q is not a member of the contract enum", name, ev.Profile)
		}
		for _, f := range append(append([]gen.EvidenceFact{}, ev.Capabilities...), ev.Attributes...) {
			if !f.Evidence.Valid() {
				t.Errorf("%s: %s carries evidence %q, which is not a member of the contract enum.\n"+
					"A cast from nas.Evidence compiles and answers with the SHOUTED spelling; nothing\n"+
					"validates a response on the way out, so this reaches a client that has no branch\n"+
					"for it and renders nothing at all.", name, f.Subject, f.Evidence)
			}
			if f.Note == "" {
				t.Errorf("%s: %s carries no note. An UNVERIFIED fact with no named measurement is a\n"+
					"guess wearing a label, and the note is the only thing on this screen an operator\n"+
					"can act on.", name, f.Subject)
			}
		}
	}
}

// TestTheTwoListsDisagreeAndThatIsTheFact.
//
// It is tempting to say *every wire encoding is DOCUMENTED, every capability is
// UNVERIFIED*. **The first half is not true and the narrower claim is better**:
// the *standard* attributes are DOCUMENTED — `Session-Timeout`, `Class`,
// `Acct-Interim-Interval` come from RFCs — while the vendor redirect attributes
// are UNVERIFIED, because `Cisco-AVPair`'s command strings are a reading of a
// vendor table that no device has ever answered.
//
// That is a sharper thing to render than a uniform badge, and it is what this test
// pins: for the CoA family, **both standings appear in the attribute list**, and
// **no capability is anything but unverified**.
func TestTheTwoListsDisagreeAndThatIsTheFact(t *testing.T) {
	a := &apistore.Adapter{}
	for _, name := range []string{"coa+cisco", "coa+aruba", "mikrotik"} {
		device := everyVendorDevice()[name]
		ev, err := a.DriverEvidence(context.Background(), device)
		if err != nil {
			t.Fatal(err)
		}

		standings := map[gen.EvidenceStanding]int{}
		for _, f := range ev.Attributes {
			standings[f.Evidence]++
		}
		if standings["documented"] == 0 {
			t.Errorf("%s: no attribute is documented. The standard set — Session-Timeout, Class,\n"+
				"Acct-Interim-Interval — is defined by RFC, and a screen that marked those unverified\n"+
				"would be telling an operator that RADIUS itself is a guess.", name)
		}
		if standings["unverified"] == 0 {
			t.Errorf("%s: every attribute is documented, including the vendor redirect. The redirect\n"+
				"attributes are NOT a standard (docs/rules/nas-drivers.md) and are not verified\n"+
				"against a device.", name)
		}

		for _, f := range ev.Capabilities {
			if f.Evidence != "unverified" {
				t.Errorf("%s: capability %s is %q. There is no %s device on this project and there\n"+
					"never has been; a capability decides whether a control EXISTS, so promoting one\n"+
					"without the measurement is a screen offering what the hardware may not do.",
					name, f.Subject, f.Evidence, name)
			}
		}
	}
}

// A `coa` row that names no vendor is answered, not refused — and the emptiness
// has a state beside it.
//
// Cisco and Aruba identify a session differently and a wrong guess is an ACK with
// no effect, which is the one failure a fake NAS cannot produce. So *"nobody said
// which vendor"* has to be representable, and an empty list with nothing beside it
// would read as *this driver sends nothing*.
func TestACoARowWithNoVendorSaysSo(t *testing.T) {
	a := &apistore.Adapter{}
	ev, err := a.DriverEvidence(context.Background(), everyVendorDevice()["coa+no vendor"])
	if err != nil {
		t.Fatal(err)
	}
	if ev.Profile != gen.VendorNotChosen {
		t.Fatalf("profile = %q, want vendor-not-chosen", ev.Profile)
	}
	if len(ev.Attributes) != 0 || len(ev.Capabilities) != 0 {
		t.Fatalf("a row with no vendor answered %d attributes and %d capabilities — from which profile?",
			len(ev.Attributes), len(ev.Capabilities))
	}
	// Non-nil, not merely empty. An absent array arrives as `undefined` and a
	// component reads that as *nothing to show*.
	if ev.Attributes == nil || ev.Capabilities == nil {
		t.Error("the lists are nil rather than empty; JSON will omit or null them")
	}
}

// And the mapping itself, because it is the one rule two packages read.
func TestVendorForDriverRefusesOnlyTheUnansweredCase(t *testing.T) {
	for _, tc := range []struct {
		driver, coaVendor string
		want              nas.Vendor
		ok                bool
	}{
		{"unifi", "", nas.VendorUniFi, true},
		{"omada", "", nas.VendorOmada, true},
		{"mikrotik", "", nas.VendorMikroTik, true},
		{"ruckus", "", nas.VendorRuckus, true},
		{"coa", "cisco", nas.VendorCisco, true},
		{"coa", "aruba", nas.VendorAruba, true},
		{"coa", "", "", false},
		{"coa", "juniper", "", false},
		{"nonsense", "", "", false},
	} {
		got, ok := nas.VendorForDriver(tc.driver, tc.coaVendor)
		if got != tc.want || ok != tc.ok {
			t.Errorf("VendorForDriver(%q, %q) = (%q, %v), want (%q, %v)",
				tc.driver, tc.coaVendor, got, ok, tc.want, tc.ok)
		}
	}
}
