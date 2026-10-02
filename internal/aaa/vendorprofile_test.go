package aaa

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
)

// The seam, from the Policy API's side.
//
// `docs/rules/nas-drivers.md` says rendering a decision into vendor
// attributes is `VendorProfile`'s job and this package's job is the decision.
// These tests hold the split: what the engine decides is one set of assertions,
// what reaches the wire is another, and the second one changes when the profile
// changes and not otherwise.

func voucherStore(v *VoucherInfo) PolicyStore {
	return &mockStore{vouchers: map[string]*VoucherInfo{v.Code: v}}
}

func activeVoucher() *VoucherInfo {
	return &VoucherInfo{
		Code:            "SEAM-1",
		State:           "unused",
		DurationMinutes: 60,
		RxRateKbps:      10000,
		TxRateKbps:      5000,
	}
}

// TestTheEngineRendersThroughTheProfileAndNotByHand.
//
// One decision, three vendors, three different replies — with the *decision*
// identical in all three. If the engine were still rendering, the reply would be
// the same three times and this test would be green against the collapse it
// exists to stop.
func TestTheEngineRendersThroughTheProfileAndNotByHand(t *testing.T) {
	v := activeVoucher()

	cases := []struct {
		vendor nas.Vendor
		want   map[string]string
		absent []string
	}{
		{
			// The floor: standard attributes only. No rate limit, because no RFC
			// attribute carries one — see decideVoucher.
			vendor: nas.VendorManual,
			want:   map[string]string{"Session-Timeout": "3600", "Acct-Interim-Interval": "300"},
			absent: []string{"Mikrotik-Rate-Limit", "WISPr-Bandwidth-Max-Down", "Filter-Id"},
		},
		{
			vendor: nas.VendorMikroTik,
			want: map[string]string{
				"Session-Timeout":     "3600",
				"Mikrotik-Rate-Limit": "5000k/10000k",
			},
			absent: []string{"WISPr-Bandwidth-Max-Down"},
		},
		{
			vendor: nas.VendorRuckus,
			want: map[string]string{
				"Session-Timeout":          "3600",
				"WISPr-Bandwidth-Max-Down": "10000000",
				"WISPr-Bandwidth-Max-Up":   "5000000",
			},
			absent: []string{"Mikrotik-Rate-Limit"},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.vendor), func(t *testing.T) {
			e := NewEngine(voucherStore(v), 300).WithVendorProfile(nas.VendorProfileFor(tc.vendor))

			dec, err := e.Authorize(context.Background(), Request{UserName: v.Code})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if !dec.Allow {
				t.Fatalf("an active voucher was refused: %s", dec.Reason)
			}
			for k, want := range tc.want {
				if dec.ReplyAttributes[k] != want {
					t.Errorf("%s = %q, want %q (all attributes: %v)", k, dec.ReplyAttributes[k], want, dec.ReplyAttributes)
				}
			}
			for _, k := range tc.absent {
				if _, present := dec.ReplyAttributes[k]; present {
					t.Errorf("%s reached a %s reply", k, tc.vendor)
				}
			}
		})
	}
}

// TestARateLimitNoProfileCanRenderIsReportedNotDropped.
//
// The engine does not write `Filter-Id = "<rx>/<tx>"`, which RFC 2865 §5.11
// defines as the *name* of a filter list already on the NAS. Not writing it is
// only an improvement if the loss is loud: an operator who set a rate limit on a
// vendor with no attribute for it must be able to find out, and the running
// system knows it in exactly one place.
func TestARateLimitNoProfileCanRenderIsReportedNotDropped(t *testing.T) {
	e := NewEngine(voucherStore(activeVoucher()), 300)

	dec, err := e.Authorize(context.Background(), Request{UserName: "SEAM-1"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	var reported bool
	for _, u := range dec.Unrenderable {
		if strings.Contains(u, "Kbps") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("a rate limit vanished without a word: Unrenderable = %v", dec.Unrenderable)
	}

	// And it reaches a log line, which is the only place an operator can see it.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	e.writeDecision(httptest.NewRecorder(), logger, dec)

	if !strings.Contains(buf.String(), "unrenderable") {
		t.Errorf("nothing was logged about the part of the decision that did not reach the NAS: %q", buf.String())
	}
}

// TestTheCiscoAVPairThatCannotBeServedIsNamed.
//
// The finding, asserted end to end: a Cisco captive reply is two Cisco-AVPair
// attributes and `Decision.ReplyAttributes` is a `map[string]string`. One is
// lost by construction. The wire format belongs to `internal/raddb` and
// `rlm_rest`, so the Policy API's obligation is to say which one went, loudly,
// rather than to invent an encoding.
func TestTheCiscoAVPairThatCannotBeServedIsNamed(t *testing.T) {
	e := NewEngine(&mockStore{}, 300).WithVendorProfile(nas.VendorProfileFor(nas.VendorCisco))

	dec := e.allow(nas.ReplyDecision{
		Captive:        true,
		PortalURL:      "https://portal.example.net/en/",
		PreAuthACL:     "ACL-WEBAUTH-REDIRECT",
		SessionTimeout: time.Hour,
	})

	if len(dec.DroppedAttributes) != 1 {
		t.Fatalf("dropped = %v; exactly one Cisco-AVPair cannot survive a single-valued reply", dec.DroppedAttributes)
	}
	if !strings.HasPrefix(dec.DroppedAttributes[0], "Cisco-AVPair=") {
		t.Errorf("dropped %q, want the AV-pair the map could not hold", dec.DroppedAttributes[0])
	}
	// The one that survived is the ACL: without it the client is not made
	// captive at all, which is a network quietly letting strangers on.
	if got := dec.ReplyAttributes["Cisco-AVPair"]; !strings.HasPrefix(got, "url-redirect-acl=") {
		t.Errorf("Cisco-AVPair = %q; the ACL is the half whose absence is dangerous", got)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	e.writeDecision(httptest.NewRecorder(), logger, dec)
	if !strings.Contains(buf.String(), "dropped_attributes") {
		t.Errorf("an attribute was dropped and nothing said so: %q", buf.String())
	}
}

// TestEveryProfileSurvivesTheCredentialGate is where the two gates meet.
//
// `internal/nas` cannot import this package to check its own output against
// ForbiddenReplyAttribute — the dependency runs the other way — so the real gate
// is applied to the real profiles here. A vendor attribute that trips it drops
// the **whole reply** as a 503 with a credential-leak alarm beside it, so a
// single bad attribute name would take out every reply for that vendor, and the
// alarm would point at the wrong thing.
func TestEveryProfileSurvivesTheCredentialGate(t *testing.T) {
	d := nas.ReplyDecision{
		Captive:         true,
		PortalURL:       "https://portal.example.net/en/",
		PreAuthACL:      "ACL-WEBAUTH-REDIRECT",
		PreAuthRole:     "foyer-preauth",
		Role:            "foyer-guest",
		SessionTimeout:  time.Hour,
		IdleTimeout:     10 * time.Minute,
		InterimInterval: 300 * time.Second,
		RxKbps:          10000,
		TxKbps:          5000,
		VLAN:            "40",
		Class:           "Foyer:guest",
		FilterID:        "guest-10m",
	}

	vendors := append([]nas.Vendor{nas.VendorManual, nas.VendorOmada}, nas.RegisteredVendorProfiles()...)
	for _, v := range vendors {
		p := nas.VendorProfileFor(v)
		attrs, _ := nas.CollapseToMap(p.RenderReply(d))
		if bad := ForbiddenReplyAttributes(attrs); len(bad) > 0 {
			t.Errorf("%s renders %v, which the reply gate refuses — every reply for that vendor would be a 503", v, bad)
		}
		if len(attrs) == 0 {
			t.Errorf("%s rendered nothing at all for a full decision", v)
		}
	}
}

// TestTheDefaultProfileIsTheFloorAndNotAGuess: an engine nobody configured must
// not be rendering a vendor's attributes at a NAS of some other vendor.
func TestTheDefaultProfileIsTheFloorAndNotAGuess(t *testing.T) {
	e := NewEngine(&mockStore{}, 300)
	if e.VendorProfile() == nil {
		t.Fatal("an engine with no configured profile has none, so allow() would fall back silently")
	}
	if got := e.VendorProfile().Vendor(); got != nas.VendorManual {
		t.Errorf("default profile is %q; an unconfigured engine must render standards only", got)
	}
}
