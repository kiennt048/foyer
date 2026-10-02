package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// RFC 8908, answered per device.
//
// The endpoint does not answer `captive: true` to everyone. These drive the real flow — the front door mints a session from a NAS redirect
// at one source address, the guest redeems, and the operating system's API client
// asks **with no cookie** from the same address, which is what Android's and iOS's
// clients do — and assert on the JSON the operating system would parse.

type captiveRig struct {
	*rig
	clock *time.Time
}

func newCaptiveRig(t *testing.T) *captiveRig {
	t.Helper()
	r := newRig(t, present(), nil)
	clock := r.now
	// The rig's clock is fixed; these tests need to move it. Rebuilt with the
	// same ports and a clock the test owns.
	svc, err := NewService(ServiceConfig{
		Ports: r.svc.Ports(),
		Now:   func() time.Time { return clock },
		NewID: func() (string, error) { return "grant-1", nil },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	r.svc = svc
	r.store.Now = func() time.Time { return clock }
	return &captiveRig{rig: r, clock: &clock}
}

const (
	guestMAC  = "aa:bb:cc:dd:ee:01"
	guestAddr = "192.0.2.10"
)

// arrive is the NAS redirect reaching the front door from addr. It returns the
// portal cookie and the session it minted.
func (c *captiveRig) arrive(t *testing.T, addr, mac string) (*http.Cookie, Session) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/guest/s/default/?id="+mac+"&ap=11:22:33:44:55:66", nil)
	req.RemoteAddr = addr + ":51000"
	rec := httptest.NewRecorder()
	c.svc.EntryHandler([]string{"en"}, "en")(rec, req)
	var cookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == SessionCookieName {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatalf("the front door minted no session for %s at %s", mac, addr)
	}
	sess, err := c.store.Session(context.Background(), HashToken(cookie.Value))
	if err != nil {
		t.Fatalf("minted session not found: %v", err)
	}
	return cookie, sess
}

// ask is the operating system's RFC 8908 query from addr, with an optional cookie.
func (c *captiveRig) ask(t *testing.T, addr string, cookie *http.Cookie, header ...string) (CapportData, *http.Response) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/captive-portal", nil)
	req.RemoteAddr = addr + ":52000"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	c.svc.CaptiveHandler("https://portal.test/en/", "")(rec, req)
	res := rec.Result()
	var data CapportData
	body := rec.Body.String()
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatalf("the RFC 8908 answer is not JSON: %v (%q)", err, body)
	}
	// `captive` is mandatory and must be a boolean, or Android discards the whole
	// payload (`docs/protocol-reality.md`). Asserted on the wire, not the struct.
	if !strings.Contains(body, `"captive":true`) && !strings.Contains(body, `"captive":false`) {
		t.Fatalf("the answer carries no boolean captive member: %q", body)
	}
	return data, res
}

func (c *captiveRig) redeem(t *testing.T, sess Session) {
	t.Helper()
	out := c.svc.Redeem(context.Background(), sess, "ABCDEFGHJKMN")
	if !out.Connected || out.Grant == nil {
		t.Fatalf("redemption did not grant: %+v", out)
	}
}

func TestTheCaptiveAPIAnswersFalseForTheDeviceHoldingAGrant(t *testing.T) {
	c := newCaptiveRig(t)
	_, sess := c.arrive(t, guestAddr, guestMAC)

	before, res := c.ask(t, guestAddr, nil)
	if !before.Captive {
		t.Fatalf("captive = false before any grant exists")
	}
	if ct := res.Header.Get("Content-Type"); ct != MediaTypeCaptiveJSON {
		t.Errorf("Content-Type = %q, want %q", ct, MediaTypeCaptiveJSON)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (RFC 8908: private or stricter)", cc)
	}

	c.redeem(t, sess)

	// No cookie: the operating system's client is not the portal's WebView.
	after, _ := c.ask(t, guestAddr, nil)
	if after.Captive {
		t.Fatalf("captive = true for the device that holds a live grant, asking from the address it redeemed at")
	}
	if after.SecondsRemaining == nil || *after.SecondsRemaining != 3600 {
		t.Errorf("seconds-remaining = %v, want 3600 for a one-hour plan redeemed just now", after.SecondsRemaining)
	}
	if after.BytesRemaining != nil {
		t.Errorf("bytes-remaining = %d: the portal holds the allowance, not the usage, so any figure is invented", *after.BytesRemaining)
	}
	if after.UserPortalURL != "https://portal.test/en/" {
		t.Errorf("user-portal-url = %q", after.UserPortalURL)
	}
	if after.VenueInfoURL != "" {
		t.Errorf("venue-info-url = %q, want it omitted when none is configured", after.VenueInfoURL)
	}
}

func TestTheCaptiveAPIAnswersTrueForEveryOtherDevice(t *testing.T) {
	c := newCaptiveRig(t)
	_, sess := c.arrive(t, guestAddr, guestMAC)
	c.redeem(t, sess)

	stranger, _ := c.ask(t, "192.0.2.99", nil)
	if !stranger.Captive {
		t.Errorf("captive = false for an address the portal never saw")
	}
	if stranger.SecondsRemaining != nil {
		t.Errorf("a captive answer carried seconds-remaining %d", *stranger.SecondsRemaining)
	}

	// A forwarding header naming the granted address is a header the caller
	// typed. It must not make a stranger the guest.
	forged, _ := c.ask(t, "192.0.2.99", nil, "X-Forwarded-For", guestAddr, "Forwarded", "for="+guestAddr)
	if !forged.Captive {
		t.Errorf("captive = false for a stranger who sent X-Forwarded-For: %s", guestAddr)
	}

	// A second device that reached the front door and has not redeemed.
	_, _ = c.arrive(t, "192.0.2.11", "aa:bb:cc:dd:ee:02")
	unpaid, _ := c.ask(t, "192.0.2.11", nil)
	if !unpaid.Captive {
		t.Errorf("captive = false for a device with a session and no grant")
	}
}

func TestTheCaptiveAPIFindsTheGrantByCookieFromAnyAddress(t *testing.T) {
	c := newCaptiveRig(t)
	cookie, sess := c.arrive(t, guestAddr, guestMAC)
	c.redeem(t, sess)

	// The portal's own page asking, from an address the book never saw.
	got, _ := c.ask(t, "198.51.100.7", cookie)
	if got.Captive {
		t.Errorf("captive = true for a browser whose own cookie names a live grant")
	}
}

func TestTheCaptiveAPIAnswersTrueOnceTheGrantHasEnded(t *testing.T) {
	cases := []struct {
		name string
		end  func(c *captiveRig, g Grant)
	}{
		{"expired", func(c *captiveRig, _ Grant) { *c.clock = c.clock.Add(61 * time.Minute) }},
		{"revoked", func(c *captiveRig, g Grant) {
			at := *c.clock
			g.RevokedAt = &at
			_ = c.store.CreateGrant(context.Background(), g)
		}},
		{"the network was never opened", func(c *captiveRig, g Grant) {
			g.Delivery = DeliveryFailed
			_ = c.store.CreateGrant(context.Background(), g)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCaptiveRig(t)
			_, sess := c.arrive(t, guestAddr, guestMAC)
			c.redeem(t, sess)
			if live, _ := c.ask(t, guestAddr, nil); live.Captive {
				t.Fatalf("precondition: captive = true while the grant is live")
			}
			tc.end(c, c.store.Grants()[0])
			got, _ := c.ask(t, guestAddr, nil)
			if !got.Captive {
				t.Errorf("captive = false for a grant that is %s", tc.name)
			}
		})
	}
}

// The pre-authentication session lives thirty minutes; a grant lives as long as
// its plan. The operating system keeps asking for the whole of the second.
func TestTheCaptiveAPIAnswerOutlivesThePreAuthenticationSession(t *testing.T) {
	c := newCaptiveRig(t)
	_, sess := c.arrive(t, guestAddr, guestMAC)
	c.redeem(t, sess)

	*c.clock = c.clock.Add(45 * time.Minute)
	if _, err := c.store.Session(context.Background(), sess.TokenHash); err == nil {
		t.Fatalf("precondition: the session row should have expired by now")
	}
	got, _ := c.ask(t, guestAddr, nil)
	if got.Captive {
		t.Fatalf("captive = true 45 minutes into a 60-minute grant, because the session row that earned it expired")
	}
	if got.SecondsRemaining == nil || *got.SecondsRemaining != 15*60 {
		t.Errorf("seconds-remaining = %v, want 900", got.SecondsRemaining)
	}
}

// The CNA destroys its cookies when its window closes, so an online guest who meets
// the portal again is a new, grantless session for the same MAC at the same
// address. That must not make the operating system think the device is captive.
func TestAGuestWhoMeetsThePortalAgainIsStillAnsweredOnline(t *testing.T) {
	c := newCaptiveRig(t)
	_, sess := c.arrive(t, guestAddr, guestMAC)
	c.redeem(t, sess)
	*c.clock = c.clock.Add(10 * time.Minute)

	_, again := c.arrive(t, guestAddr, guestMAC)
	if again.GrantID != "" {
		t.Fatalf("precondition: the second arrival should be a fresh session with no grant")
	}
	got, _ := c.ask(t, guestAddr, nil)
	if got.Captive {
		t.Errorf("captive = true for a device whose grant is live, because it met the front door again with no cookie")
	}
	if got.SecondsRemaining == nil || *got.SecondsRemaining != 50*60 {
		t.Errorf("seconds-remaining = %v, want 3000", got.SecondsRemaining)
	}
}

// NAT between the guests and the portal: one address, many devices. The answer
// must fall back to the pre-existing `captive: true`, never to another guest's grant.
//
// **The order is the test.** Had the granted guest arrived first and the second
// one after, the test would stay green with the ambiguity check deleted, because
// the second arrival simply overwrites the entry with a session that holds no
// grant. The dangerous order is the granted guest being observed *last*: behind NAT
// the last browser to load any page decides the answer for every device on the
// address, and without the ambiguity mark the ungranted one is told it is online.
func TestAnAddressThatMeantTwoDevicesIsAnsweredAsUnknown(t *testing.T) {
	c := newCaptiveRig(t)
	cookieA, sessA := c.arrive(t, guestAddr, guestMAC)
	c.redeem(t, sessA)
	_, _ = c.arrive(t, guestAddr, "aa:bb:cc:dd:ee:02")

	if got, _ := c.ask(t, guestAddr, nil); !got.Captive {
		t.Errorf("captive = false for an address two MACs have arrived from — one of them has no grant")
	}

	// Device A's connected page loads again, through its cookie, from the shared
	// address. The address is still two devices.
	req := httptest.NewRequest(http.MethodGet, "/en/connected/", nil)
	req.RemoteAddr = guestAddr + ":53000"
	req.AddCookie(cookieA)
	if _, err := c.svc.Lookup(context.Background(), req); err != nil {
		t.Fatalf("device A's own cookie did not resolve: %v", err)
	}
	if got, _ := c.ask(t, guestAddr, nil); !got.Captive {
		t.Errorf("captive = false for a cookieless query from an address two MACs share, because the granted one was seen last")
	}
	// A itself is still answered from its own cookie.
	if got, _ := c.ask(t, guestAddr, cookieA); got.Captive {
		t.Errorf("captive = true for device A asking with its own cookie")
	}
}

// The API server's service and the front door's are two `Service` values in
// `cmd/foyer`. Sharing `Ports.Addresses` is what lets the endpoint read what the
// other one observed.
func TestTwoServicesSharingAnAddressBookSeeEachOthersObservations(t *testing.T) {
	c := newCaptiveRig(t)
	book := NewAddressBook()
	ports := c.svc.Ports()
	ports.Addresses = book
	front, err := NewService(ServiceConfig{Ports: ports, Now: func() time.Time { return *c.clock }})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := NewService(ServiceConfig{Ports: ports, Now: func() time.Time { return *c.clock }})
	if err != nil {
		t.Fatal(err)
	}
	c.svc = front
	_, sess := c.arrive(t, guestAddr, guestMAC)
	c.redeem(t, sess)

	c.svc = endpoint
	if got, _ := c.ask(t, guestAddr, nil); got.Captive {
		t.Errorf("captive = true: the endpoint's service did not see the front door's observation through the shared book")
	}
}

// lyingBook is an `AddressBook` whose entry names a grant issued to a different
// MAC than the one the entry was bound to. `MemoryAddressBook` never produces
// that by construction; a shared implementation behind the port could.
type lyingBook struct{ entry AddressEntry }

func (lyingBook) Observe(netip.Addr, string, string, string, time.Time) {}
func (lyingBook) AttachGrant(string, string, time.Time)                 {}
func (b lyingBook) Lookup(netip.Addr, time.Time) (AddressEntry, bool)   { return b.entry, true }

// The endpoint re-checks that the grant an address points at was issued to the
// MAC that address was bound to. Without that check it answers `captive: false`
// — and the other device's `seconds-remaining` — to whoever holds the address.
// Every other test stays green with the check deleted, because the in-process
// book never hands out a mismatched pair; this one drives the port.
func TestTheCaptiveAPINeverAnswersFromAGrantIssuedToAnotherMAC(t *testing.T) {
	c := newCaptiveRig(t)
	_, sess := c.arrive(t, guestAddr, guestMAC)
	c.redeem(t, sess)
	g, err := c.store.Grant(context.Background(), "grant-1")
	if err != nil || g.StateAt(*c.clock) != StateConnected {
		t.Fatalf("precondition: grant-1 is not live for %s: %v", guestMAC, err)
	}

	ports := c.svc.Ports()
	ports.Addresses = lyingBook{AddressEntry{TokenHash: "someone-else", ClientMAC: "aa:bb:cc:dd:ee:99", GrantID: "grant-1"}}
	svc, err := NewService(ServiceConfig{Ports: ports, Now: func() time.Time { return *c.clock }})
	if err != nil {
		t.Fatal(err)
	}
	c.svc = svc
	if got, _ := c.ask(t, "192.0.2.99", nil); !got.Captive || got.SecondsRemaining != nil {
		t.Errorf("an address bound to aa:bb:cc:dd:ee:99 was answered from %s's grant: %+v", guestMAC, got)
	}
}

// An address that a second MAC arrives from forgets the first MAC's grant — it
// does not merely hide it behind the ambiguity mark. The mark lasts one window
// from the conflict; an entry the second device keeps refreshing outlives it,
// and must not then carry the first device's grant.
func TestAnAddressThatChangedHandsDoesNotKeepTheFormerDevicesGrant(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	addr := netip.MustParseAddr(guestAddr)
	b := NewAddressBookWith(time.Hour, 16)
	b.Observe(addr, "tok-a", guestMAC, "grant-a", t0)
	b.Observe(addr, "tok-b", "aa:bb:cc:dd:ee:02", "", t0.Add(10*time.Minute))
	if e, ok := b.Lookup(addr, t0.Add(11*time.Minute)); !ok || !e.Ambiguous || e.GrantID != "" {
		t.Errorf("right after the conflict: %+v, %v; want ambiguous and no grant", e, ok)
	}
	// Only the second device, from here on.
	b.Observe(addr, "tok-b", "aa:bb:cc:dd:ee:02", "", t0.Add(50*time.Minute))
	e, ok := b.Lookup(addr, t0.Add(75*time.Minute))
	if !ok || e.Ambiguous {
		t.Fatalf("precondition: after the ambiguity window the entry is %+v, %v; want live and unambiguous", e, ok)
	}
	if e.GrantID != "" || e.ClientMAC != "aa:bb:cc:dd:ee:02" {
		t.Errorf("the address now means aa:bb:cc:dd:ee:02 and still carries grant %q (MAC %s)", e.GrantID, e.ClientMAC)
	}
}

func TestTheAddressBookIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	b := NewAddressBookWith(time.Hour, 3)
	for i := 1; i <= 5; i++ {
		addr := netip.AddrFrom4([4]byte{192, 0, 2, byte(i)})
		b.Observe(addr, "tok", "aa:bb:cc:dd:ee:0"+string(rune('0'+i)), "", now.Add(time.Duration(i)*time.Second))
	}
	if n := b.Len(); n != 3 {
		t.Errorf("the book holds %d entries, want its capacity of 3", n)
	}
	if _, ok := b.Lookup(netip.AddrFrom4([4]byte{192, 0, 2, 1}), now.Add(10*time.Second)); ok {
		t.Errorf("the address seen longest ago survived eviction")
	}
	if _, ok := b.Lookup(netip.AddrFrom4([4]byte{192, 0, 2, 5}), now.Add(2*time.Hour)); ok {
		t.Errorf("an entry outlived its window")
	}
}

// Eviction is by last observation, not by first: a guest whose page keeps
// loading is not the one pushed out by a stranger's new address.
func TestTheAddressBookEvictsTheAddressSeenLongestAgo(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	b := NewAddressBookWith(time.Hour, 3)
	addr := func(i byte) netip.Addr { return netip.AddrFrom4([4]byte{192, 0, 2, i}) }
	for i := byte(1); i <= 3; i++ {
		b.Observe(addr(i), "tok", "aa:bb:cc:dd:ee:0"+string(rune('0'+i)), "", now.Add(time.Duration(i)*time.Second))
	}
	b.Observe(addr(1), "tok", "aa:bb:cc:dd:ee:01", "", now.Add(4*time.Second))
	b.Observe(addr(4), "tok", "aa:bb:cc:dd:ee:04", "", now.Add(5*time.Second))
	at := now.Add(6 * time.Second)
	if _, ok := b.Lookup(addr(1), at); !ok {
		t.Errorf("the address observed most recently but one was evicted")
	}
	if _, ok := b.Lookup(addr(2), at); ok {
		t.Errorf("the address seen longest ago survived eviction")
	}
	if n := b.Len(); n != 3 {
		t.Errorf("the book holds %d entries, want its capacity of 3", n)
	}
}

func TestClientAddressIsTheTransportSourceOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[::ffff:192.0.2.10]:4000"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	got, ok := ClientAddress(req)
	if !ok || got != netip.MustParseAddr(guestAddr) {
		t.Errorf("ClientAddress = %v, %v; want the unmapped transport source %s", got, ok, guestAddr)
	}
}
