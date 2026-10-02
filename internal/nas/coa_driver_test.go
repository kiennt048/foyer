package nas

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNASIP = netip.MustParseAddr("127.0.0.1")

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

// driverAgainst builds a CoADriver pointed at a fake NAS.
//
// The retransmit budget is deliberately short. Every refusal path here answers
// immediately, so a slow one is a test that has gone wrong rather than a test
// being patient.
func driverAgainst(t *testing.T, n *fakeNAS, v CoAVendor, sessions SessionSource) *CoADriver {
	t.Helper()
	host, port := splitHostPort(t, n.addr())
	d, err := NewCoADriver(CoAConfig{
		Vendor:   v,
		Host:     host,
		Port:     port,
		Secret:   fakeNASSecret,
		Timeout:  200 * time.Millisecond,
		Retries:  1,
		Sessions: sessions,
	})
	if err != nil {
		t.Fatalf("NewCoADriver: %v", err)
	}
	return d
}

// sourceWith is a SessionSource holding one session for testMAC.
func sourceWith(s Session) fakeSessions {
	s.NASIPAddress = testNASIP
	return fakeSessions{byMAC: map[string]Session{normalizeMAC(testMAC): s}}
}

// ---------------------------------------------------------------------------
// The wire format — the half that can be proved without a device
// ---------------------------------------------------------------------------

// TestVendorAttributeWireFormat asserts the exact bytes of a type-26 attribute.
//
// # Why bytes rather than a round trip
//
// A round trip through this package's own decoder proves the encoder agrees
// with the decoder, and an encoder and a decoder written by the same author on
// the same afternoon agree about their shared mistakes. So the expectation here
// is built from RFC 2865 §5.26 field by field — 26, total length, four bytes of
// vendor id, vendor type, vendor length, value — and the two lengths are
// asserted against each other, which is where an off-by-one would live.
//
// This is the strongest statement available without hardware, and it is worth
// being precise about what it does **not** say: that the *framing* is right, not
// that vendor id 9 is Cisco or that attribute 1 is Cisco-AVPair. Those are
// UNVERIFIED and named as such beside the constants.
func TestVendorAttributeWireFormat(t *testing.T) {
	const value = "subscriber:command=reauthenticate"
	got, err := encodeVendorAttribute(VendorString(ciscoVendorID, ciscoAVPairAttrType, value))
	if err != nil {
		t.Fatalf("encodeVendorAttribute: %v", err)
	}

	// Hand-derived: 2 header + 4 vendor id + 2 sub-header + len(value).
	wantLen := 8 + len(value)
	if len(got) != wantLen {
		t.Fatalf("encoded %d bytes, want %d", len(got), wantLen)
	}
	if got[0] != 26 {
		t.Errorf("type = %d, want 26 (Vendor-Specific, RFC 2865 §5.26)", got[0])
	}
	if int(got[1]) != wantLen {
		t.Errorf("length field = %d, want %d — RADIUS length counts its own two header bytes", got[1], wantLen)
	}
	if v := uint32(got[2])<<24 | uint32(got[3])<<16 | uint32(got[4])<<8 | uint32(got[5]); v != ciscoVendorID {
		t.Errorf("vendor id = %d, want %d, big endian", v, ciscoVendorID)
	}
	if got[6] != ciscoAVPairAttrType {
		t.Errorf("vendor type = %d, want %d", got[6], ciscoAVPairAttrType)
	}
	if int(got[7]) != len(value)+2 {
		t.Errorf("vendor length = %d, want %d — it counts the sub-attribute header and not the vendor id",
			got[7], len(value)+2)
	}
	if string(got[8:]) != value {
		t.Errorf("value = %q, want %q", got[8:], value)
	}
	// The two lengths must agree, which is the invariant a shared off-by-one
	// breaks and a round trip hides.
	if int(got[1]) != int(got[7])+6 {
		t.Errorf("length %d and vendor length %d disagree; the difference is exactly the six bytes "+
			"of attribute header plus vendor id", got[1], got[7])
	}
}

// TestVendorAttributeRefusesWhatCannotBeEncoded. A value that does not fit is a
// packet the NAS discards, so it is refused here where the caller can see it.
func TestVendorAttributeRefusesWhatCannotBeEncoded(t *testing.T) {
	if _, err := encodeVendorAttribute(VendorString(ciscoVendorID, 1, strings.Repeat("x", maxVendorValue+1))); err == nil {
		t.Error("a value one byte over the limit encoded; it would have overflowed the length field")
	}
	if _, err := encodeVendorAttribute(VendorString(ciscoVendorID, 1, "")); err == nil {
		t.Error("an empty vendor attribute encoded; there is nothing for a NAS to read")
	}
	// And the largest value that does fit must still encode, or the boundary is
	// off by one in the safe-looking direction.
	if _, err := encodeVendorAttribute(VendorString(ciscoVendorID, 1, strings.Repeat("x", maxVendorValue))); err != nil {
		t.Errorf("the largest legal value was refused: %v", err)
	}
}

// TestCoARequestWithNoSessionIdentityIsRefusedBeforeItIsSent.
//
// A request naming no session asks the NAS to act on something. The fake answers
// Error-Cause 402; a careless device would answer anything at all. Refusing in
// the client means the answer does not depend on which kind is on the far end.
func TestCoARequestWithNoSessionIdentityIsRefusedBeforeItIsSent(t *testing.T) {
	n := newFakeNAS(t, nil)
	host, port := splitHostPort(t, n.addr())
	c := &CoAClient{Addr: CoAAddr(host, port), Secret: fakeNASSecret, Timeout: 200 * time.Millisecond}

	if err := c.Disconnect(context.Background(), Session{NASIdentifier: "ap-lobby"}); !errors.Is(err, ErrCoANoSessionIdentity) {
		t.Fatalf("err = %v, want ErrCoANoSessionIdentity — NAS-Identifier names the device, not the session", err)
	}
	if _, _, _, observed := n.snapshot(); len(observed) != 0 {
		t.Errorf("%d packets reached the NAS; the refusal is supposed to happen first", len(observed))
	}
}

// TestCoAReplyOfTheWrongFamilyIsNotAnAnswer.
//
// A Disconnect-ACK answering a CoA-Request would mean the NAS ended the session
// instead of changing it. Reading it as success records that a guest's limits
// were raised at the moment they were thrown off the network.
func TestCoAReplyOfTheWrongFamilyIsNotAnAnswer(t *testing.T) {
	n := newFakeNAS(t, func(n *fakeNAS) { n.replyWithWrongFamily = true })
	host, port := splitHostPort(t, n.addr())
	c := &CoAClient{Addr: CoAAddr(host, port), Secret: fakeNASSecret, Timeout: 200 * time.Millisecond}

	err := c.CoA(context.Background(), Session{AcctSessionID: "s-1"},
		VendorString(ciscoVendorID, ciscoAVPairAttrType, ciscoCmdReauthenticate))
	if err == nil {
		t.Fatal("a Disconnect-ACK was accepted as an answer to a CoA-Request")
	}
	var coaErr *CoAError
	if errors.As(err, &coaErr) {
		t.Fatalf("the wrong-family reply was read as a NAK (%v); it is neither an ACK nor a refusal", err)
	}
}

// ---------------------------------------------------------------------------
// The fake itself — because an accept path is the weak half of a test
// ---------------------------------------------------------------------------

// TestFakeNASRefusesMoreThanItAccepts drives every refusal the responder has.
//
// It is a test of the harness rather than of the product, and it exists because,
// as `docs/rules/testing.md`'s Principles put it, *an input the harness cannot be
// wrong about* produces confident output in either direction. A fake
// whose refusal branches have never executed is a fake that only ACKs, wearing
// a comment that says otherwise.
func TestFakeNASRefusesMoreThanItAccepts(t *testing.T) {
	live := fakeSession{AcctSessionID: "sess-live", CallingStationID: "1c7125.63e4.24", Removable: true}
	stuck := fakeSession{AcctSessionID: "sess-stuck", Removable: false}
	twinA := fakeSession{AcctSessionID: "sess-twin-a", UserName: "guest", Removable: true}
	twinB := fakeSession{AcctSessionID: "sess-twin-b", UserName: "guest", Removable: true}

	cases := []struct {
		name string
		// mutate configures the device.
		mutate func(*fakeNAS)
		// send performs one request and returns its error.
		send func(*CoAClient) error
		// wantCause is the Error-Cause the device must answer with.
		wantCause uint32
	}{
		{
			name:   "no session-identification attribute at all",
			mutate: func(n *fakeNAS) { n.sessions = []fakeSession{live} },
			send: func(c *CoAClient) error {
				// Straight to the encoder, because the client refuses this
				// shape itself — which is the belt this braces.
				return c.send(context.Background(), codeDisconnectRequest,
					Session{NASIdentifier: "ap-lobby"}, nil)
			},
			wantCause: ErrorCauseMissingAttribute,
		},
		{
			name:   "identification matching no session",
			mutate: func(n *fakeNAS) { n.sessions = []fakeSession{live} },
			send: func(c *CoAClient) error {
				return c.Disconnect(context.Background(), Session{AcctSessionID: "sess-that-never-was"})
			},
			wantCause: ErrorCauseSessionContextNotFound,
		},
		{
			name:   "identification matching two sessions",
			mutate: func(n *fakeNAS) { n.sessions = []fakeSession{twinA, twinB} },
			send: func(c *CoAClient) error {
				return c.Disconnect(context.Background(), Session{UserName: "guest"})
			},
			wantCause: ErrorCauseMultipleSessionSelectionUnsupported,
		},
		{
			name: "a device that does not implement CoA-Request",
			mutate: func(n *fakeNAS) {
				n.sessions = []fakeSession{live}
				n.supportsCoA = false
			},
			send: func(c *CoAClient) error {
				return c.CoA(context.Background(), Session{AcctSessionID: live.AcctSessionID},
					VendorString(ciscoVendorID, ciscoAVPairAttrType, ciscoCmdReauthenticate))
			},
			wantCause: ErrorCauseUnsupportedService,
		},
		{
			name:   "a vendor attribute this device has never heard of",
			mutate: func(n *fakeNAS) { n.sessions = []fakeSession{live} },
			send: func(c *CoAClient) error {
				return c.CoA(context.Background(), Session{AcctSessionID: live.AcctSessionID},
					VendorString(99999, 3, "who-are-you"))
			},
			wantCause: ErrorCauseUnsupportedAttribute,
		},
		{
			name:   "a CoA-Request that changes nothing",
			mutate: func(n *fakeNAS) { n.sessions = []fakeSession{live} },
			send: func(c *CoAClient) error {
				return c.send(context.Background(), codeCoARequest,
					Session{AcctSessionID: live.AcctSessionID}, nil)
			},
			wantCause: ErrorCauseMissingAttribute,
		},
		{
			name:   "a session it can see and will not end",
			mutate: func(n *fakeNAS) { n.sessions = []fakeSession{stuck} },
			send: func(c *CoAClient) error {
				return c.Disconnect(context.Background(), Session{AcctSessionID: stuck.AcctSessionID})
			},
			wantCause: ErrorCauseSessionContextNotRemovable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := newFakeNAS(t, tc.mutate)
			n.allow(ciscoVendorID, ciscoAVPairAttrType)
			host, port := splitHostPort(t, n.addr())
			c := &CoAClient{Addr: CoAAddr(host, port), Secret: fakeNASSecret, Timeout: 300 * time.Millisecond}

			err := tc.send(c)
			var coaErr *CoAError
			if !errors.As(err, &coaErr) {
				t.Fatalf("err = %v, want a *CoAError carrying Error-Cause %d", err, tc.wantCause)
			}
			if !coaErr.HasErrorCause || coaErr.ErrorCause != tc.wantCause {
				t.Fatalf("Error-Cause = %d (present %v), want %d", coaErr.ErrorCause, coaErr.HasErrorCause, tc.wantCause)
			}
			if _, acks, _, _ := n.snapshot(); acks != 0 {
				t.Errorf("the device ACKed %d times on a refusal path", acks)
			}
		})
	}

	// The silent refusals. Silence is what a real server does with a packet
	// whose authenticators do not check out, and a client that cannot survive it
	// hangs rather than reporting.
	t.Run("a wrong shared secret is dropped, not refused", func(t *testing.T) {
		n := newFakeNAS(t, func(n *fakeNAS) { n.sessions = []fakeSession{live} })
		host, port := splitHostPort(t, n.addr())
		c := &CoAClient{Addr: CoAAddr(host, port), Secret: "not-the-secret", Timeout: 80 * time.Millisecond, Retries: 1}

		err := c.Disconnect(context.Background(), Session{AcctSessionID: live.AcctSessionID})
		if !errors.Is(err, ErrCoATimeout) {
			t.Fatalf("err = %v, want ErrCoATimeout — a dropped packet is silence, not a NAK", err)
		}
		_, acks, dropped, _ := n.snapshot()
		if acks != 0 {
			t.Errorf("the device ACKed %d times for a request signed with the wrong secret", acks)
		}
		if len(dropped) == 0 {
			t.Fatal("the device recorded no drop; the Message-Authenticator check never ran")
		}
		for _, why := range dropped {
			if !strings.Contains(why, "Message-Authenticator") {
				t.Errorf("dropped for %q, want the Message-Authenticator check", why)
			}
		}
	})

	// And the accept, kept last and kept small, because it is the half that
	// proves the least.
	t.Run("the one accept", func(t *testing.T) {
		n := newFakeNAS(t, func(n *fakeNAS) { n.sessions = []fakeSession{live} })
		host, port := splitHostPort(t, n.addr())
		c := &CoAClient{Addr: CoAAddr(host, port), Secret: fakeNASSecret, Timeout: 300 * time.Millisecond}

		if err := c.Disconnect(context.Background(), Session{AcctSessionID: live.AcctSessionID}); err != nil {
			t.Fatalf("a well-formed Disconnect for a live removable session was refused: %v", err)
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if len(n.teardowns) != 1 || n.teardowns[0] != live.AcctSessionID {
			t.Errorf("teardowns = %v, want exactly [%s]", n.teardowns, live.AcctSessionID)
		}
	})
}

// ---------------------------------------------------------------------------
// The drivers
// ---------------------------------------------------------------------------

// TestCiscoReauthorizeReachesTheDeviceAsAVendorAttribute is the end of the chain
// the VSA gap blocked: a driver call, through the encoder, decoded on the far
// side as `Cisco-AVPair = subscriber:command=reauthenticate`.
//
// The device is configured to know that attribute and nothing else, so a driver
// sending it under the wrong vendor id or attribute number gets Error-Cause 401
// rather than a pass.
func TestCiscoReauthorizeReachesTheDeviceAsAVendorAttribute(t *testing.T) {
	const audit = "0A0102030000001B4F2C1E55"
	n := newFakeNAS(t, func(n *fakeNAS) {
		n.sessions = []fakeSession{{AcctSessionID: "cisco-1", AuditSessionID: audit, Removable: true}}
	})
	n.allow(ciscoVendorID, ciscoAVPairAttrType)

	d := driverAgainst(t, n, CiscoCoAVendor{}, sourceWith(Session{
		AcctSessionID:    "cisco-1",
		AuditSessionID:   audit,
		CallingStationID: testMAC,
	}))

	if err := d.AuthorizeGuest(context.Background(), AuthorizeRequest{ClientMAC: testMAC}); err != nil {
		t.Fatalf("AuthorizeGuest: %v", err)
	}

	_, acks, _, observed := n.snapshot()
	if acks != 1 {
		t.Fatalf("the device ACKed %d times, want 1", acks)
	}
	if len(observed) != 1 {
		t.Fatalf("the device saw %d packets, want 1", len(observed))
	}
	p := observed[0]
	if p.Code != codeCoARequest {
		t.Errorf("code = %d, want %d (CoA-Request) — a reauthorize that ends the session is not a reauthorize",
			p.Code, codeCoARequest)
	}
	var pairs []string
	for _, va := range p.VSAs {
		if va.VendorID != ciscoVendorID || va.VendorType != ciscoAVPairAttrType {
			t.Errorf("a vendor attribute arrived as %d/%d, want %d/%d",
				va.VendorID, va.VendorType, ciscoVendorID, ciscoAVPairAttrType)
		}
		pairs = append(pairs, string(va.Value))
	}
	sort.Strings(pairs)
	want := []string{avPair(ciscoAVAuditSessionIDKey, audit), ciscoCmdReauthenticate, ciscoReauthTypeRerun}
	sort.Strings(want)
	if strings.Join(pairs, "|") != strings.Join(want, "|") {
		t.Errorf("AV-pairs = %v, want %v", pairs, want)
	}
	// Three vendor attributes with the same name in one packet, which is the
	// thing `CollapseToMap` cannot do on the reply side. On this side it works,
	// and that asymmetry is worth pinning: the limitation is the JSON dictionary
	// `rlm_rest` is answered with, not RADIUS.
	if len(p.VSAs) != 3 {
		t.Errorf("%d vendor attributes, want 3 repeated Cisco-AVPairs in one packet", len(p.VSAs))
	}
}

// TestCoADriverReportsEveryNAKRatherThanSucceeding is the rule stated in
// `docs/rules/nas-drivers.md` — *"A driver that ignores these reports success
// on a no-op"* — driven through the whole driver rather than through the client.
func TestCoADriverReportsEveryNAKRatherThanSucceeding(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*fakeNAS)
		act       func(*CoADriver) error
		wantCause uint32
	}{
		{
			name:   "the session ended between the lookup and the request",
			mutate: func(n *fakeNAS) { n.sessions = nil },
			act: func(d *CoADriver) error {
				return d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC})
			},
			wantCause: ErrorCauseSessionContextNotFound,
		},
		{
			name: "the identifier matches two sessions",
			mutate: func(n *fakeNAS) {
				n.sessions = []fakeSession{
					{AcctSessionID: "dup", Removable: true},
					{AcctSessionID: "dup", Removable: true},
				}
			},
			act: func(d *CoADriver) error {
				return d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC})
			},
			wantCause: ErrorCauseMultipleSessionSelectionUnsupported,
		},
		{
			name: "the device does not implement CoA-Request",
			mutate: func(n *fakeNAS) {
				n.sessions = []fakeSession{{AcctSessionID: "dup", Removable: true}}
				n.supportsCoA = false
			},
			act: func(d *CoADriver) error {
				return d.AuthorizeGuest(context.Background(), AuthorizeRequest{ClientMAC: testMAC})
			},
			wantCause: ErrorCauseUnsupportedService,
		},
		{
			name: "the device will not end this session",
			mutate: func(n *fakeNAS) {
				n.sessions = []fakeSession{{AcctSessionID: "dup", Removable: false}}
			},
			act: func(d *CoADriver) error {
				return d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC})
			},
			wantCause: ErrorCauseSessionContextNotRemovable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := newFakeNAS(t, tc.mutate)
			n.allow(ciscoVendorID, ciscoAVPairAttrType)
			d := driverAgainst(t, n, CiscoCoAVendor{}, sourceWith(Session{AcctSessionID: "dup"}))

			err := tc.act(d)
			var coaErr *CoAError
			if !errors.As(err, &coaErr) {
				t.Fatalf("err = %v (%T), want a *CoAError; a NAK flattened into a string reports success on a no-op",
					err, err)
			}
			if coaErr.ErrorCause != tc.wantCause {
				t.Errorf("Error-Cause = %d, want %d", coaErr.ErrorCause, tc.wantCause)
			}
			if !strings.Contains(coaErr.Error(), errorCauseName(tc.wantCause)) {
				t.Errorf("error text %q does not name the cause; an operator reading a log gets a number",
					coaErr.Error())
			}
		})
	}
}

// TestMACSpellingDecidesWhetherASessionIsFound is the guess this family is most
// likely to be wrong about, made mechanical.
//
// The device stores one spelling and matches on string equality, which is what a
// NAS does. A vendor rendering the other spelling gets Session-Context-Not-Found
// — the *visible* half of being wrong. The invisible half is a device that
// ignores a request it cannot match, and this harness cannot produce it, which
// is stated in `coa_nas_test.go` rather than implied.
func TestMACSpellingDecidesWhetherASessionIsFound(t *testing.T) {
	// A device that spells Calling-Station-Id the way MikroTik is believed to.
	deviceSpelling := macWith(testMAC, mikrotikMACSeparator, mikrotikMACGroup, mikrotikMACUppercase)
	if deviceSpelling == "" {
		t.Fatal("the fixture MAC does not parse")
	}

	for _, tc := range []struct {
		name    string
		vendor  CoAVendor
		wantErr bool
	}{
		{"the vendor that spells it the way this device does", MikroTikCoAVendor{}, false},
		{"a vendor that spells it another way", ArubaCoAVendor{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newFakeNAS(t, func(n *fakeNAS) {
				n.sessions = []fakeSession{{CallingStationID: deviceSpelling, Removable: true}}
			})
			// Only the MAC is available, so Identify has to fall through to it.
			d := driverAgainst(t, n, tc.vendor, sourceWith(Session{CallingStationID: testMAC}))

			err := d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC})
			if tc.wantErr {
				var coaErr *CoAError
				if !errors.As(err, &coaErr) || coaErr.ErrorCause != ErrorCauseSessionContextNotFound {
					t.Fatalf("err = %v, want Session-Context-Not-Found — a MAC in the wrong spelling names no session", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the matching spelling was refused: %v", err)
			}
		})
	}
}

// TestCoADriverVerifyPresenceNeverAnswersTrueWithoutEvidence.
//
// `security.md` §4 makes VerifyPresence mandatory before a grant, and RFC 5176
// has no query in it. The failure this guards is a driver that answers true
// because it has no way to say no — which turns the portal into a public API for
// authorizing arbitrary MACs.
func TestCoADriverVerifyPresenceNeverAnswersTrueWithoutEvidence(t *testing.T) {
	lookupFailed := errors.New("the accounting store is down")

	cases := []struct {
		name        string
		build       func(*testing.T) *CoADriver
		wantOutcome PresenceOutcome
		wantErr     bool
	}{
		{
			name: "no session source at all",
			build: func(t *testing.T) *CoADriver {
				n := newFakeNAS(t, nil)
				return driverAgainst(t, n, CiscoCoAVendor{}, nil)
			},
			wantOutcome: PresenceUnavailable,
			wantErr:     true,
		},
		{
			name: "the source could not look",
			build: func(t *testing.T) *CoADriver {
				n := newFakeNAS(t, nil)
				return driverAgainst(t, n, CiscoCoAVendor{}, fakeSessions{fail: lookupFailed})
			},
			wantOutcome: PresenceUnavailable,
			wantErr:     true,
		},
		{
			name: "no session for this MAC",
			build: func(t *testing.T) *CoADriver {
				n := newFakeNAS(t, nil)
				return driverAgainst(t, n, CiscoCoAVendor{}, fakeSessions{byMAC: map[string]Session{}})
			},
			wantOutcome: PresenceNotFound,
		},
		{
			name: "a session for this MAC on another NAS",
			build: func(t *testing.T) *CoADriver {
				n := newFakeNAS(t, nil)
				return driverAgainst(t, n, CiscoCoAVendor{}, fakeSessions{byMAC: map[string]Session{
					normalizeMAC(testMAC): {
						AcctSessionID: "elsewhere",
						NASIPAddress:  netip.MustParseAddr("10.9.9.9"),
					},
				}})
			},
			wantOutcome: PresenceSiteMismatch,
		},
		{
			// **Without this row a sabotage passes.** Making the unmade
			// comparison count as a pass would leave the whole suite green,
			// because every other session in it carries a NAS-IP-Address. An
			// accounting row that names no NAS is an unchecked claim, not a
			// match, and it is also not a mismatch — an operator told "the
			// device is somewhere else" would go looking for hardware that is
			// standing in front of them.
			name: "a session that names no NAS at all",
			build: func(t *testing.T) *CoADriver {
				n := newFakeNAS(t, nil)
				return driverAgainst(t, n, CiscoCoAVendor{}, fakeSessions{byMAC: map[string]Session{
					normalizeMAC(testMAC): {AcctSessionID: "unattributed"},
				}})
			},
			wantOutcome: PresenceUnavailable,
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.build(t)
			p, err := d.VerifyPresenceDetail(context.Background(), testMAC, "aa:bb:cc:dd:ee:ff", "site-1")
			if p.OK {
				t.Fatal("VerifyPresence answered true; a grant would be issued on no evidence at all")
			}
			if p.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", p.Outcome, tc.wantOutcome)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v — an outage must not read as a refusal", err, tc.wantErr)
			}
			ok, err2 := d.VerifyPresence(context.Background(), testMAC, "", "site-1")
			if ok {
				t.Error("the two-value form disagreed with the detailed one")
			}
			if (err2 != nil) != tc.wantErr {
				t.Errorf("the two-value form's error = %v, wantErr = %v", err2, tc.wantErr)
			}
		})
	}

	// The accept, and even it is qualified: the access point is never confirmed
	// on this family, so `APConfirmed` must stay false rather than quietly
	// passing an unchecked claim.
	t.Run("a session on this NAS is present, and still not AP-confirmed", func(t *testing.T) {
		n := newFakeNAS(t, nil)
		d := driverAgainst(t, n, CiscoCoAVendor{}, sourceWith(Session{AcctSessionID: "here"}))
		p, err := d.VerifyPresenceDetail(context.Background(), testMAC, "aa:bb:cc:dd:ee:ff", "site-1")
		if err != nil || !p.OK {
			t.Fatalf("presence = %+v, err = %v", p, err)
		}
		if p.APConfirmed {
			t.Error("APConfirmed is true; nothing on this family names an access point, so this " +
				"would be an unchecked claim reported as a check")
		}
	})
}

// TestCoADriverProbeReadsTheRefusal.
//
// Every branch of the probe, including the one that matters most: a device that
// ACKs a session id it cannot have issued is reachable *and* untrustworthy, and
// that has to be a warning rather than a clean bill of health.
func TestCoADriverProbeReadsTheRefusal(t *testing.T) {
	cases := []struct {
		name          string
		mutate        func(*fakeNAS)
		secret        string
		wantReachable bool
		wantErr       bool
		wantWarnKind  WarningKind
		// wantErrorNames is a substring the operator-facing diagnostic must
		// carry. A probe that fails without saying what to check next is a
		// support call.
		wantErrorNames string
	}{
		{
			name:          "a refusal is the healthy answer",
			mutate:        func(n *fakeNAS) { n.sessions = []fakeSession{{AcctSessionID: "real", Removable: true}} },
			secret:        fakeNASSecret,
			wantReachable: true,
		},
		{
			name:          "a device that ACKs whatever it is sent",
			mutate:        func(n *fakeNAS) { n.ackEverything = true },
			secret:        fakeNASSecret,
			wantReachable: true,
			wantWarnKind:  WarnCoAAcknowledgedUnknownSession,
		},
		{
			// The device drops a request it cannot authenticate, so from here
			// this is silence. Reported as unreachable *and* ambiguous, which
			// is the truth: the secret being wrong is one of four explanations
			// and nothing on the wire distinguishes them.
			name:           "the shared secret does not match, so the device says nothing",
			mutate:         func(n *fakeNAS) { n.sessions = []fakeSession{{AcctSessionID: "real", Removable: true}} },
			secret:         "not-the-secret",
			wantReachable:  false,
			wantErr:        true,
			wantErrorNames: "1700",
		},
		{
			// The other half of a mismatched secret, and the half an operator
			// can act on: something answered, and its answer does not verify.
			name: "a reply that does not verify is reachable and refused",
			mutate: func(n *fakeNAS) {
				n.sessions = []fakeSession{{AcctSessionID: "real", Removable: true}}
				n.replySecret = "a-different-shared-value"
			},
			secret:         fakeNASSecret,
			wantReachable:  true,
			wantErr:        true,
			wantErrorNames: "shared secret",
		},
		{
			// Nothing listening, a firewall, or the wrong port. Deterministic
			// here because the device is configured never to answer: a closed
			// UDP port answers ICMP unreachable on some platforms and nothing on
			// others, and a test that depends on which is a test about the
			// kernel.
			name:           "silence",
			mutate:         func(n *fakeNAS) { n.silent = true },
			secret:         fakeNASSecret,
			wantReachable:  false,
			wantErr:        true,
			wantErrorNames: "1700",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := newFakeNAS(t, tc.mutate)
			host, port := splitHostPort(t, n.addr())
			d, err := NewCoADriver(CoAConfig{
				Vendor: CiscoCoAVendor{}, Host: host, Port: port,
				Secret: tc.secret, Timeout: 80 * time.Millisecond, Retries: 1,
			})
			if err != nil {
				t.Fatalf("NewCoADriver: %v", err)
			}
			res, err := d.Probe(context.Background())
			if res == nil {
				t.Fatal("Probe returned no result; a registration screen has nothing to render")
			}
			if res.Reachable != tc.wantReachable {
				t.Errorf("Reachable = %v, want %v (err = %v)", res.Reachable, tc.wantReachable, err)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantWarnKind != "" && !func() bool { _, ok := findWarning(res.Warnings, tc.wantWarnKind); return ok }() {
				t.Errorf("warnings = %v, want one of kind %q", warningKinds(res.Warnings), tc.wantWarnKind)
			}
			// Unconditional on every row, healthy ones included: a warning that
			// only appears when something else is broken is one an operator
			// learns to expect the absence of.
			if !func() bool { _, ok := findWarning(res.Warnings, WarnAccessPointUnconfirmed); return ok }() {
				t.Errorf("warnings = %v, want the security.md §4 access-point notice on every probe",
					warningKinds(res.Warnings))
			}
			if len(res.Unverified) == 0 {
				t.Error("Unverified is empty; every capability on this family is a guess")
			}
			if tc.wantErrorNames != "" {
				// The ambiguity has to survive into the operator's copy, because
				// it is the fact: silence on UDP is four different problems and
				// the wrong port — Cisco's 1700 — is the common one.
				if len(res.Errors) == 0 || !strings.Contains(strings.Join(res.Errors, " "), tc.wantErrorNames) {
					t.Errorf("errors = %v, want one naming %q", res.Errors, tc.wantErrorNames)
				}
			}
		})
	}
}

// TestCoADriverDiscoveryRefusesRatherThanReturningNothing.
//
// An empty list means "this NAS has no access points", which is false: it has
// one and it is the NAS. The contract says the same thing from the other end —
// `ControllerVendor` excludes this family because the device is its own.
func TestCoADriverDiscoveryRefusesRatherThanReturningNothing(t *testing.T) {
	n := newFakeNAS(t, nil)
	d := driverAgainst(t, n, CiscoCoAVendor{}, sourceWith(Session{AcctSessionID: "x"}))
	aps, err := d.DiscoverAccessPoints(context.Background())
	if !errors.Is(err, ErrNoController) {
		t.Fatalf("err = %v, want ErrNoController", err)
	}
	if aps != nil {
		t.Errorf("aps = %v, want nil", aps)
	}
}

// TestCoAVendorsRefuseWhatTheyCannotDo pins the two refusals that are findings
// about Foyer rather than about a vendor.
func TestCoAVendorsRefuseWhatTheyCannotDo(t *testing.T) {
	cases := []struct {
		vendor  CoAVendor
		wantSub string
	}{
		{ArubaCoAVendor{}, "AuthorizeRequest has no role field"},
		{MikroTikCoAVendor{}, "cannot create one"},
	}
	for _, tc := range cases {
		t.Run(string(tc.vendor.Vendor()), func(t *testing.T) {
			n := newFakeNAS(t, func(n *fakeNAS) {
				n.sessions = []fakeSession{{AcctSessionID: "s", Removable: true}}
			})
			d := driverAgainst(t, n, tc.vendor, sourceWith(Session{AcctSessionID: "s"}))

			err := d.AuthorizeGuest(context.Background(), AuthorizeRequest{ClientMAC: testMAC})
			var unsupported *UnsupportedActionError
			if !errors.As(err, &unsupported) {
				t.Fatalf("err = %v (%T), want an *UnsupportedActionError an admin console can read", err, err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not say why", err)
			}
			if _, _, _, observed := n.snapshot(); len(observed) != 0 {
				t.Errorf("%d packets were sent for an action this vendor cannot perform", len(observed))
			}
		})
	}
}

// TestMikroTikChangesALiveSessionWithoutClaimingToCreateOne is the pair
// `CanAuthorizeGuest: false` beside `LiveReauthorize: true` made concrete.
func TestMikroTikChangesALiveSessionWithoutClaimingToCreateOne(t *testing.T) {
	n := newFakeNAS(t, func(n *fakeNAS) {
		n.sessions = []fakeSession{{AcctSessionID: "hotspot-1", Removable: true}}
	})
	n.allow(mikrotikVendorID, mikrotikRateLimitAttrType)

	d := driverAgainst(t, n, MikroTikCoAVendor{}, sourceWith(Session{AcctSessionID: "hotspot-1"}))

	if err := d.Reauthorize(context.Background(), AuthorizeRequest{
		ClientMAC: testMAC, RxRateLimitKbps: 8000, TxRateLimitKbps: 2000,
	}); err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	_, _, _, observed := n.snapshot()
	if len(observed) != 1 || len(observed[0].VSAs) != 1 {
		t.Fatalf("observed = %+v, want one CoA-Request carrying one vendor attribute", observed)
	}
	// The same rendering the profile uses, read off the profile so the assertion
	// cannot be a third copy of the format.
	want := mikrotikRateLimit(8000, 2000)
	if got := string(observed[0].VSAs[0].Value); got != want {
		t.Errorf("Mikrotik-Rate-Limit = %q, want %q", got, want)
	}

	// A request with no rate limit has nothing to carry, and sending it would
	// ask the NAS to change nothing.
	err := d.Reauthorize(context.Background(), AuthorizeRequest{ClientMAC: testMAC})
	if err == nil {
		t.Fatal("a CoA-Request with nothing to change was sent")
	}
	if _, _, _, observed := n.snapshot(); len(observed) != 1 {
		t.Errorf("%d packets total; the empty change should not have reached the device", len(observed))
	}
}

// TestMikroTikRateLimitIsRenderedOnceForBothHalves.
//
// The profile puts this attribute in an Access-Accept and the driver puts it in
// a CoA-Request. Two renderings would drift silently and asymmetrically: a guest
// would get one rate at login and another the moment an operator pressed apply.
func TestMikroTikRateLimitIsRenderedOnceForBothHalves(t *testing.T) {
	const rx, tx = 12000, 3000
	attrs := MikroTikProfile{}.RenderReply(ReplyDecision{RxKbps: rx, TxKbps: tx})
	var fromProfile string
	for _, a := range attrs {
		if a.Name == "Mikrotik-Rate-Limit" {
			fromProfile = a.Value
		}
	}
	if fromProfile == "" {
		t.Fatal("the profile rendered no rate limit")
	}
	vsas, err := MikroTikCoAVendor{}.Reauthorize(Session{}, AuthorizeRequest{
		RxRateLimitKbps: rx, TxRateLimitKbps: tx,
	})
	if err != nil || len(vsas) != 1 {
		t.Fatalf("Reauthorize = %v, %v", vsas, err)
	}
	if got := string(vsas[0].Value); got != fromProfile {
		t.Errorf("the driver renders %q and the profile renders %q for the same two numbers", got, fromProfile)
	}
}

// TestCoACapabilitiesMatchBehaviour holds a capability and the code it describes
// together, in both directions.
//
// A capability decides whether an admin control exists at all. One that says
// *yes* over a method that refuses is a control that silently does nothing; one
// that says *no* over a method that works hides a feature for no reason. Neither
// is visible from either side alone.
func TestCoACapabilitiesMatchBehaviour(t *testing.T) {
	vendors := []CoAVendor{CiscoCoAVendor{}, ArubaCoAVendor{}, MikroTikCoAVendor{}}
	// A request carrying everything AuthorizeRequest can carry, so a vendor that
	// refuses is refusing for want of a *field* rather than for want of a value.
	full := AuthorizeRequest{
		ClientMAC: testMAC, TimeLimitMinutes: 60, DataUsageLimitMB: 500,
		RxRateLimitKbps: 8000, TxRateLimitKbps: 2000,
	}

	var canReauth, cannot int
	for _, v := range vendors {
		t.Run(string(v.Vendor()), func(t *testing.T) {
			caps := v.Capabilities()
			_, err := v.Reauthorize(Session{AcctSessionID: "s"}, full)
			renders := err == nil
			if renders {
				canReauth++
			} else {
				cannot++
			}
			if caps.LiveReauthorize != renders {
				t.Errorf("LiveReauthorize = %v but Reauthorize %s (%v)",
					caps.LiveReauthorize, map[bool]string{true: "renders", false: "refuses"}[renders], err)
			}
			// Authorizing out of band is a strictly stronger claim than changing
			// a live session: you cannot do the first without the second.
			if caps.CanAuthorizeGuest && !caps.LiveReauthorize {
				t.Error("CanAuthorizeGuest without LiveReauthorize: there is no way to authorize a " +
					"session out of band without changing it")
			}
			// BandwidthLimits on this family means the CoA can carry a rate,
			// because there is no controller API to carry it instead.
			carriesRate := false
			if vsas, err := v.Reauthorize(Session{AcctSessionID: "s"}, full); err == nil {
				for _, va := range vsas {
					if strings.Contains(string(va.Value), "k/") {
						carriesRate = true
					}
				}
			}
			if caps.BandwidthLimits != carriesRate {
				t.Errorf("BandwidthLimits = %v but the CoA %s a rate — on this family there is no "+
					"controller API to carry it instead",
					caps.BandwidthLimits, map[bool]string{true: "carries", false: "does not carry"}[carriesRate])
			}
		})
	}
	// Both halves populated, or one of the two assertions above never ran.
	if canReauth == 0 || cannot == 0 {
		t.Fatalf("%d vendors reauthorize and %d do not; a half with no rows proves nothing",
			canReauth, cannot)
	}
}

// TestNoCoAVendorClaimsToBeMeasured is the sibling of
// TestNoVendorAttributeClaimsToBeMeasured, for the other abstraction.
//
// There is no Cisco, Aruba or MikroTik device on this project. A capability here
// that claimed MEASURED or DOCUMENTED would be a guess in a costume, and one
// labelled UNVERIFIED with no named measurement is the same thing with better
// manners.
func TestNoCoAVendorClaimsToBeMeasured(t *testing.T) {
	for _, v := range []CoAVendor{CiscoCoAVendor{}, ArubaCoAVendor{}, MikroTikCoAVendor{}} {
		ev := v.CapabilityEvidence()
		if len(ev) == 0 {
			t.Errorf("%s: no evidence at all", v.Vendor())
			continue
		}
		for name, fact := range ev {
			if fact.Evidence != EvidenceUnverified {
				t.Errorf("%s/%s claims %q; no device of this vendor has been near this project",
					v.Vendor(), name, fact.Evidence)
			}
			if !strings.Contains(strings.ToLower(fact.Note), "settled by") {
				t.Errorf("%s/%s is UNVERIFIED and names no measurement that would settle it: %q",
					v.Vendor(), name, fact.Note)
			}
		}
		// Every capability the type declares, not merely some of them: a map
		// with three entries and seven fields is silent about four values that
		// still ship.
		for _, want := range []string{
			CapCanAuthorizeGuest, CapCanDeauthGuest, CapHardQuota,
			CapBandwidthLimits, CapWalledGarden, CapLiveReauthorize, CapRotateSecret,
		} {
			if _, ok := ev[want]; !ok {
				t.Errorf("%s: no evidence for %s, which still ships as a value", v.Vendor(), want)
			}
		}
	}
}

// TestCoAWireNumbersAreDocumented is the sibling of
// TestNoCoAVendorClaimsToBeMeasured, and the two must disagree about the same
// vendors or one of them is wrong.
//
// # What the dictionary settled, and what it could not
//
// The vendor ids and attribute numbers were read out of
// `/usr/share/freeradius/dictionary.*` inside the image `lab/` builds — the file
// FreeRADIUS itself parses. All four were correct, and they are DOCUMENTED now.
//
// **That settles how to spell an attribute and nothing about what belongs in
// it.** So this asserts the encoding map is DOCUMENTED *and* cites the
// dictionary, while `TestNoCoAVendorClaimsToBeMeasured` continues to require
// every capability to be UNVERIFIED. A change that promoted a capability on the
// strength of this measurement turns that test red, which is the intended
// coupling: a dictionary cannot tell you whether a NAS honours a command.
//
// The second half is the one that would rot quietly. Every note here must still
// name what remains unmeasured, because the tempting edit after a successful
// measurement is to delete the caveats along with the label.
func TestCoAWireNumbersAreDocumented(t *testing.T) {
	vendors := []CoAVendor{CiscoCoAVendor{}, ArubaCoAVendor{}, MikroTikCoAVendor{}}

	for _, v := range vendors {
		t.Run(string(v.Vendor()), func(t *testing.T) {
			ev := v.AttributeEvidence()
			if len(ev) == 0 {
				t.Fatal("no attribute evidence; the vendor numbers are measured and unrecorded")
			}
			for name, fact := range ev {
				if fact.Evidence != EvidenceDocumented {
					t.Errorf("%s claims %q; the vendor id and attribute number were read from "+
						"FreeRADIUS's own dictionary and are DOCUMENTED", name, fact.Evidence)
				}
				if !strings.Contains(fact.Note, "dictionary") {
					t.Errorf("%s is DOCUMENTED and cites no source: %q", name, fact.Note)
				}
				// The caveat must survive the promotion. A DOCUMENTED encoding
				// beside no statement of what is still unknown reads as a
				// measured attribute, which is what it is not.
				if !strings.Contains(fact.Note, "UNVERIFIED") {
					t.Errorf("%s records a DOCUMENTED encoding and names nothing still unmeasured: %q",
						name, fact.Note)
				}
				if !strings.Contains(strings.ToLower(fact.Note), "settled by") {
					t.Errorf("%s names no measurement for what it still does not know: %q", name, fact.Note)
				}
			}
			// And the capability map must not have moved with it.
			for capName, fact := range v.CapabilityEvidence() {
				if fact.Evidence != EvidenceUnverified {
					t.Errorf("capability %s was promoted to %q on the strength of a dictionary read; "+
						"a dictionary says how to spell an attribute, never whether a device honours it",
						capName, fact.Evidence)
				}
			}
		})
	}

	// Every attribute this family actually emits must appear. Derived from what
	// the vendors render rather than listed, so an attribute added tomorrow
	// without evidence fails here instead of shipping unrecorded.
	t.Run("every emitted attribute is accounted for", func(t *testing.T) {
		full := AuthorizeRequest{ClientMAC: testMAC, TimeLimitMinutes: 60, RxRateLimitKbps: 8000, TxRateLimitKbps: 2000}
		session := Session{AcctSessionID: "s", AuditSessionID: "a", CallingStationID: testMAC, UserName: "guest"}
		emitted := 0
		for _, v := range vendors {
			var sent []VendorAttribute
			if _, ids, err := v.Identify(session); err == nil {
				sent = append(sent, ids...)
			}
			if cmd, err := v.Reauthorize(session, full); err == nil {
				sent = append(sent, cmd...)
			}
			if len(sent) == 0 {
				continue
			}
			emitted++
			if len(v.AttributeEvidence()) == 0 {
				t.Errorf("%s emits %d vendor attributes and records evidence for none", v.Vendor(), len(sent))
			}
		}
		if emitted == 0 {
			t.Fatal("no vendor emitted a vendor attribute; this subtest measured nothing")
		}
	})
}

// ---------------------------------------------------------------------------
// The claim that a new driver is one file
// ---------------------------------------------------------------------------

// gigabitPointCoA is a fictional vendor, defined entirely inside this test file.
//
// It exists to answer *"the design claims a new driver is one file with no core
// changes"* by executing it rather than asserting it in prose — the same shape
// as TestAddingAVendorProfileNeedsNoCoreChange, which did it for the other
// abstraction.
type gigabitPointCoA struct{}

func (gigabitPointCoA) Vendor() Vendor { return Vendor("gigabitpoint") }

func (gigabitPointCoA) Identify(s Session) (Session, []VendorAttribute, error) {
	return Session{
		NASIPAddress:     s.NASIPAddress,
		CallingStationID: macWith(s.CallingStationID, "-", 2, true),
	}, nil, nil
}

func (gigabitPointCoA) Reauthorize(_ Session, req AuthorizeRequest) ([]VendorAttribute, error) {
	return []VendorAttribute{VendorString(64999, 7, fmt.Sprintf("minutes=%d", req.TimeLimitMinutes))}, nil
}

func (gigabitPointCoA) Capabilities() Capabilities {
	return Capabilities{CanAuthorizeGuest: true, CanDeauthGuest: true, LiveReauthorize: true, WalledGarden: WalledGardenNone}
}

func (gigabitPointCoA) CapabilityEvidence() CapabilityEvidence {
	return CapabilityEvidence{CapCanDeauthGuest: {Evidence: EvidenceUnverified, Note: "fictional. Settled by: nothing, it does not exist."}}
}

// AttributeEvidence — added when `CoAVendor` gained the method, and the cost is
// worth naming precisely: **widening a seam costs every vendor file one method,
// and this file is a vendor file.** That is the ordinary price of a seam taking on
// a responsibility, not the failure `docs/rules/nas-drivers.md` warns about,
// which is *adding a vendor* forcing a change to the core. Nothing in `driver.go`
// or `coa_driver.go` knows this vendor exists, before or after.
func (gigabitPointCoA) AttributeEvidence() AttributeEvidence {
	return AttributeEvidence{
		"GigabitPoint-Minutes": {Evidence: EvidenceUnverified, Note: "fictional. Settled by: nothing, it does not exist."},
	}
}

// TestAddingACoAVendorNeedsNoCoreChange.
//
// # The answer
//
// **Yes, through the shipped-driver registry** — everything below is written in a
// test file, registers nothing in `driver.go`, and drives a real NAS conversation
// through the shared encoder.
//
// Without the registry, `ShippedCapabilities`, `ShippedCapabilityEvidence` and
// `ShippedProbeWarnings` would each carry a `switch vendor`, so adding a driver
// family would mean three edits to the core — and `MockDriver` reads those
// functions, so it would stand in for any driver missing from them, claiming the
// floor. See RegisterShippedDriver.
//
// One residue remains and is stated rather than hidden: `unconditionalWarnings`
// is unexported, so a driver defined **outside** `internal/nas` cannot supply
// probe warnings to `ShippedProbeWarnings`. Every driver in the design lives in
// this package, and a test file in it — like this one — is not outside it.
func TestAddingACoAVendorNeedsNoCoreChange(t *testing.T) {
	deviceSpelling := macWith(testMAC, "-", 2, true)
	n := newFakeNAS(t, func(n *fakeNAS) {
		n.sessions = []fakeSession{{CallingStationID: deviceSpelling, Removable: true}}
	})
	n.allow(64999, 7)

	d := driverAgainst(t, n, gigabitPointCoA{}, sourceWith(Session{CallingStationID: testMAC}))

	if d.Vendor() != Vendor("gigabitpoint") {
		t.Fatalf("Vendor() = %q", d.Vendor())
	}
	if err := d.AuthorizeGuest(context.Background(), AuthorizeRequest{ClientMAC: testMAC, TimeLimitMinutes: 45}); err != nil {
		t.Fatalf("the new vendor's reauthorize did not reach the device: %v", err)
	}
	if err := d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC}); err != nil {
		t.Fatalf("the new vendor's disconnect did not reach the device: %v", err)
	}

	_, acks, _, observed := n.snapshot()
	if acks != 2 || len(observed) != 2 {
		t.Fatalf("acks = %d over %d packets, want 2 and 2", acks, len(observed))
	}
	if got := string(observed[0].VSAs[0].Value); got != "minutes=45" {
		t.Errorf("the new vendor's own attribute arrived as %q", got)
	}
	if observed[0].VSAs[0].VendorID != 64999 {
		t.Errorf("vendor id = %d, want 64999 — the encoder took the vendor's word for it",
			observed[0].VSAs[0].VendorID)
	}
	// It inherits the whole RFC 5176 client — retransmit, Message-Authenticator,
	// reply verification, typed NAKs — without restating any of it, which is the
	// other half of "one file".
	if csid, ok := observed[1].attr(attrCallingStationID); !ok || csid != deviceSpelling {
		t.Errorf("Calling-Station-Id = %q (present %v), want %q", csid, ok, deviceSpelling)
	}
}

// TestRuckusIsNotInTheCoAFamily.
//
// `docs/rules/nas-drivers.md` is explicit: *"Ruckus is not a CoA vendor for
// captive portal."* SmartZone's external-portal model is the Northbound JSON
// API, and PacketFence documents that in non-proxy MAC-auth mode the
// deauthentication method must be set to HTTP/HTTPS so that API is used instead
// of RADIUS Disconnect — otherwise *"the code will try to use RADIUS by default
// and fail to disconnect the user."*
//
// **No RuckusDriver was built.** The Northbound API is a JSON surface nothing in
// this repository documents — no endpoint, no authentication shape, no error
// model — and writing one would be invention rather than a driver. What exists
// instead is this: the vendor has a profile, has no driver, and must never be
// handed a CoA one. A test rather than a comment, because the tempting shortcut
// is precisely *"it is RFC 5176, just point CoADriver at it"*, and that produces
// a driver that reports success and disconnects nobody.
func TestRuckusIsNotInTheCoAFamily(t *testing.T) {
	for _, v := range ShippedVendors() {
		if v == VendorRuckus {
			t.Fatalf("a driver is registered for %s. If it is the Northbound JSON API driver, this "+
				"test is the thing to update, with the PacketFence citation beside it. If it is a "+
				"CoADriver, it will report success and disconnect nobody.", v)
		}
	}
	// The profile is there and stays there: the reply half and the enforcement
	// half are independent, and a profile is not evidence that a driver is close.
	found := false
	for _, v := range RegisteredVendorProfiles() {
		if v == VendorRuckus {
			found = true
		}
	}
	if !found {
		t.Error("Ruckus lost its VendorProfile; the redirect half is separate from the enforcement half")
	}
}

// TestShippedDriversRegisterThemselves is the registry's own lock: the three
// derived facts must all come from the same place, for every vendor.
func TestShippedDriversRegisterThemselves(t *testing.T) {
	shipped := ShippedVendors()
	if len(shipped) < 2 {
		t.Fatalf("shipped vendors = %v; this test is about a table with more than one row", shipped)
	}
	for _, v := range shipped {
		if ShippedCapabilities(v) == (Capabilities{WalledGarden: WalledGardenNone}) {
			t.Errorf("%s ships a driver whose capabilities are the floor; either the driver claims "+
				"nothing at all or the registry is not reaching it", v)
		}
		if len(ShippedCapabilityEvidence(v)) == 0 {
			t.Errorf("%s ships a driver with no capability evidence", v)
		}
		// The §4 access-point notice, cross-checked against the driver's own
		// answer rather than against a list of vendors written here.
		//
		// Not *every driver has at least the §4 gap*: that is the shape of
		// assertion that quietly stops meaning anything the day it stops being
		// universal. It is two derived facts weighed against each other: a
		// driver that cannot confirm an access point must say so on every probe,
		// and one that can must not — because an absent notice means *this
		// driver checks* on UniFi and *somebody forgot* on a driver that does
		// not, and the warning list alone cannot tell those apart.
		_, notices := findWarning(ShippedProbeWarnings(v), WarnAccessPointUnconfirmed)
		if confirms := ShippedConfirmsAccessPoint(v); confirms == notices {
			if confirms {
				t.Errorf("%s ships a driver that confirms the access point a redirect claims and "+
					"warns unconditionally that it does not; one of the two is stale", v)
			} else {
				t.Errorf("%s ships a driver with no mechanism for security.md §4's access-point "+
					"clause and no unconditional notice saying so", v)
			}
		}
	}
	// And a vendor with no driver gets nothing rather than something plausible.
	if ShippedCapabilityEvidence(VendorOmada) != nil || ShippedProbeWarnings(VendorOmada) != nil {
		t.Error("Omada has no driver and answered something; inventing a caveat is the same defect " +
			"as inventing a capability")
	}
}
