package apistore_test

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// `disconnectSession` end to end: HTTP, CSRF, PostgreSQL, `radacct`, the per-NAS
// seam, a real `nas.CoADriver`, and a UDP socket on the other side that behaves like
// a NAS rather than like an echo.
//
// **What the fake NAS is and is not.** It is a hand-written RFC 5176 responder, so
// it proves the packet Foyer sends against the RFC and against this author's reading
// of it — nothing about a MikroTik. It is not free to invent the framing: it verifies
// the Request Authenticator and the Message-Authenticator with the shared secret the
// store holds before it answers anything, and a request that does not verify gets
// silence, the way a real NAS drops it. `docs/rules/testing.md`: *"ACKing a CoA is
// trivial; faithfully rejecting one is not."* Hardware remains UNVERIFIED.

// loopbackCoA builds the real CoA driver the product builds, aimed at the fake NAS.
//
// Only the destination differs from `VendorDrivers.ForNAS`: the row's address is a
// 10.x address in this harness's block, and the fake listens on loopback. Everything
// that decides what goes on the wire — the vendor, the secret unsealed from the
// store, the session source, the NAS-IP the driver compares the session against — is
// the row's own.
type loopbackCoA struct{ port int }

func (l loopbackCoA) ForNAS(c store.RadiusClient, secret string, sessions nas.SessionSource) (nas.NASDriver, error) {
	if c.Driver != "mikrotik" {
		return apistore.VendorDrivers{}.ForNAS(c, secret, sessions)
	}
	return nas.NewCoADriver(nas.CoAConfig{
		Vendor:       nas.MikroTikCoAVendor{},
		Host:         "127.0.0.1",
		Port:         l.port,
		Secret:       secret,
		Sessions:     sessions,
		NASIPAddress: c.Address.Addr(),
		Timeout:      400 * time.Millisecond,
	})
}

// fakeNAS answers Disconnect-Requests the way the case asks.
type fakeNAS struct {
	pc     net.PacketConn
	secret string

	mu       sync.Mutex
	answer   string // "ack", "nak503", "silent", "wrong-secret"
	requests [][]byte
	verified []bool
}

func newFakeNAS(t *testing.T, secret string) *fakeNAS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeNAS{pc: pc, secret: secret, answer: "ack"}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			req := append([]byte(nil), buf[:n]...)
			ok := verifyDisconnect(req, secret)
			f.mu.Lock()
			f.requests = append(f.requests, req)
			f.verified = append(f.verified, ok)
			answer := f.answer
			f.mu.Unlock()
			if !ok || answer == "silent" {
				continue // a NAS drops what it cannot authenticate
			}
			var reply []byte
			switch answer {
			case "ack":
				reply = radiusReply(req, secret, 41, 0)
			case "nak503":
				reply = radiusReply(req, secret, 42, 503)
			case "wrong-secret":
				reply = radiusReply(req, secret+"-not-this-one", 41, 0)
			}
			_, _ = pc.WriteTo(reply, addr)
		}
	}()
	return f
}

func (f *fakeNAS) port() int { return f.pc.LocalAddr().(*net.UDPAddr).Port }

func (f *fakeNAS) set(answer string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
	f.requests, f.verified = nil, nil
}

func (f *fakeNAS) seen() ([][]byte, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.requests...), append([]bool(nil), f.verified...)
}

// verifyDisconnect is what a NAS does before it answers: code 40, a Request
// Authenticator that is MD5(code‖id‖length‖16 zeros‖attrs‖secret), and a
// Message-Authenticator that is HMAC-MD5 over the packet with both zeroed.
func verifyDisconnect(req []byte, secret string) bool {
	if len(req) < 20 || req[0] != 40 || int(binary.BigEndian.Uint16(req[2:4])) != len(req) {
		return false
	}
	zeroed := append([]byte(nil), req...)
	copy(zeroed[4:20], make([]byte, 16))
	sum := md5.New()
	sum.Write(zeroed)
	sum.Write([]byte(secret))
	if !hmac.Equal(sum.Sum(nil), req[4:20]) {
		return false
	}
	off, ok := attrOffset(req[20:], 80)
	if !ok || req[20+off+1] != 18 {
		return false
	}
	want := append([]byte(nil), req[20+off+2:20+off+18]...)
	copy(zeroed[20+off+2:20+off+18], make([]byte, 16))
	mac := hmac.New(md5.New, []byte(secret))
	mac.Write(zeroed)
	return hmac.Equal(mac.Sum(nil), want)
}

func attrOffset(attrs []byte, want byte) (int, bool) {
	for off := 0; off+2 <= len(attrs); {
		l := int(attrs[off+1])
		if l < 2 || off+l > len(attrs) {
			return 0, false
		}
		if attrs[off] == want {
			return off, true
		}
		off += l
	}
	return 0, false
}

func attrValue(req []byte, want byte) []byte {
	off, ok := attrOffset(req[20:], want)
	if !ok {
		return nil
	}
	l := int(req[20+off+1])
	return req[20+off+2 : 20+off+l]
}

// radiusReply builds an ACK (41) or a NAK (42) carrying Error-Cause, signed with
// `secret`: Response Authenticator = MD5(code‖id‖length‖RequestAuth‖attrs‖secret).
func radiusReply(req []byte, secret string, code byte, errorCause uint32) []byte {
	var attrs []byte
	if errorCause != 0 {
		attrs = append(attrs, 101, 6)
		attrs = binary.BigEndian.AppendUint32(attrs, errorCause)
	}
	out := []byte{code, req[1], 0, 0}
	binary.BigEndian.PutUint16(out[2:4], uint16(20+len(attrs)))
	out = append(out, req[4:20]...)
	out = append(out, attrs...)
	sum := md5.New()
	sum.Write(out)
	sum.Write([]byte(secret))
	copy(out[4:20], sum.Sum(nil))
	return out
}

// disconnectWorld is a MikroTik hotspot router Foyer speaks to directly, one guest
// on it admitted with a voucher and holding a portal grant, and a Ruckus device with
// a guest Foyer must refuse to touch.
type disconnectWorld struct {
	*world
	nas                *fakeNAS
	secret             string
	router             store.RadiusClient
	session, acctID    string
	mac                string
	voucherID, grantID string
	ruckusSession      string
}

func newDisconnectWorld(t *testing.T) *disconnectWorld {
	t.Helper()
	secret := randomString()
	fake := newFakeNAS(t, secret)
	w := newWorldWith(t, func(cfg *apistore.Config) { cfg.NASDrivers = loopbackCoA{port: fake.port()} })
	w.signIn()
	ctx := context.Background()
	d := &disconnectWorld{world: w, nas: fake, secret: secret}

	ss := &store.SharedSecret{ID: uniq("sec"), SiteID: w.siteA, Label: "Hotspot router"}
	if err := w.st.SharedSecrets().Create(ctx, ss, []byte(secret)); err != nil {
		t.Fatalf("creating the shared secret: %v", err)
	}
	addr := w.nextAddr()
	d.router = store.RadiusClient{
		ID: uniq("rc"), SiteID: w.siteA, Name: "Hotspot router", Address: netip.MustParsePrefix(addr + "/32"),
		Source: "manual", Driver: "mikrotik", SharedSecretID: &ss.ID, RequireMsgAuth: true,
		Reachability: "ok", Enabled: true,
		Capabilities: store.Capabilities{WalledGarden: "ip-range", CanDeauthGuest: true},
	}
	if err := w.st.RadiusClients().Create(ctx, &d.router); err != nil {
		t.Fatalf("creating the router: %v", err)
	}

	// A voucher, redeemed for this session, the way the portal redeems one.
	b, err := w.adapter.CreateBatch(ctx, gen.VoucherBatchCreate{
		SiteId: w.siteA, Label: "Disconnect " + uniq("b"), Count: 1, ExpiryMode: gen.FromFirstUse,
		Plan: gen.PlanRequest{DurationMinutes: ptr(60)},
	}, w.adminUser)
	if err != nil {
		t.Fatalf("creating a batch: %v", err)
	}
	page, err := w.adapter.Vouchers(ctx, api.VoucherQuery{SiteID: w.siteA, BatchID: b.Id, Window: api.Window{Limit: 1}})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("reading the batch back: %v", err)
	}
	code := page.Items[0].Code
	d.voucherID = page.Items[0].Id
	d.session = uniq("acct")
	d.acctID = "mt-" + uniq("s")
	if _, err := w.st.Vouchers().Redeem(ctx, code, d.session, time.Now(), nil); err != nil {
		t.Fatalf("redeeming: %v", err)
	}

	d.mac = "94:2A:6F:10:20:30"
	now := time.Now()
	if _, err := w.owner.Exec(ctx, `
		INSERT INTO radacct (acctuniqueid, acctsessionid, username, nasipaddress, callingstationid,
		                     acctstarttime, acctupdatetime, acctinterval)
		VALUES ($1, $2, $3, $4::inet, $5, $6, $7, 300)`,
		d.session, d.acctID, code, addr, d.mac, now.Add(-10*time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("seeding radacct: %v", err)
	}

	d.grantID = uniq("grant")
	if err := w.st.Grants().CreateGrant(ctx, &store.PortalGrant{
		ID: d.grantID, SiteID: w.siteA, NASID: &d.router.ID, ClientMAC: d.mac,
		Method: "voucher", VoucherID: &d.voucherID, GrantedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("creating the grant: %v", err)
	}

	// A Ruckus device whose stored capability claims it can end a session — the
	// column that is only a claim — with a guest on it.
	rss := &store.SharedSecret{ID: uniq("sec"), SiteID: w.siteA, Label: "SmartZone"}
	if err := w.st.SharedSecrets().Create(ctx, rss, []byte(randomString())); err != nil {
		t.Fatalf("creating a shared secret: %v", err)
	}
	raddr := w.nextAddr()
	ruckus := &store.RadiusClient{
		ID: uniq("rc"), SiteID: w.siteA, Name: "SmartZone atrium", Address: netip.MustParsePrefix(raddr + "/32"),
		Source: "manual", Driver: "ruckus", SharedSecretID: &rss.ID, Reachability: "ok", Enabled: true,
		Capabilities: store.Capabilities{WalledGarden: "none", CanDeauthGuest: true},
	}
	if err := w.st.RadiusClients().Create(ctx, ruckus); err != nil {
		t.Fatalf("creating the ruckus device: %v", err)
	}
	d.ruckusSession = uniq("acct")
	if _, err := w.owner.Exec(ctx, `
		INSERT INTO radacct (acctuniqueid, acctsessionid, username, nasipaddress, callingstationid,
		                     acctstarttime, acctupdatetime, acctinterval)
		VALUES ($1, 'rk-1', $2, $3::inet, '94:2a:6f:99:88:77', $4, $5, 300)`,
		d.ruckusSession, code, raddr, now.Add(-5*time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("seeding radacct: %v", err)
	}
	return d
}

func (d *disconnectWorld) voucherState() string {
	d.t.Helper()
	v, err := d.st.Vouchers().Get(context.Background(), d.voucherID)
	if err != nil {
		d.t.Fatalf("reading the voucher: %v", err)
	}
	return v.State
}

func (d *disconnectWorld) grantRevoked() bool {
	d.t.Helper()
	g, err := d.st.Grants().Grant(context.Background(), d.grantID)
	if err != nil {
		d.t.Fatalf("reading the grant: %v", err)
	}
	return g.RevokedAt != nil
}

// TestDisconnectSendsAVerifiableDisconnectRequest is the acceptance test for
// `disconnectSession`'s wire half: one Disconnect-Request, identifying the session by the Acct-Session-Id
// `radacct` recorded and the NAS-IP the router is registered at, carrying a
// Message-Authenticator that verifies with the secret the store holds — and the ACK
// reported as a disconnect, with the withdrawals and the audit entry beside it.
func TestDisconnectSendsAVerifiableDisconnectRequest(t *testing.T) {
	d := newDisconnectWorld(t)

	raw := d.do(http.MethodPost, "/sessions/"+d.session+"/disconnect",
		map[string]any{"revokeVoucher": true, "revokeGrant": true}, http.StatusOK)
	var out gen.SessionDisconnectResult
	mustJSON(t, raw, &out)

	reqs, verified := d.nas.seen()
	if len(reqs) != 1 {
		t.Fatalf("the NAS saw %d requests, want exactly 1", len(reqs))
	}
	if !verified[0] {
		t.Fatal("the Disconnect-Request's Request Authenticator or Message-Authenticator does not verify " +
			"with the shared secret the store holds — a real NAS drops it silently")
	}
	req := reqs[0]
	if got := string(attrValue(req, 44)); got != d.acctID {
		t.Errorf("Acct-Session-Id = %q, want %q from radacct", got, d.acctID)
	}
	if got := attrValue(req, 4); len(got) != 4 || netip.AddrFrom4([4]byte(got)) != d.router.Address.Addr() {
		t.Errorf("NAS-IP-Address = %v, want %v", got, d.router.Address.Addr())
	}
	if off, _ := attrOffset(req[20:], 80); off != len(req)-20-18 {
		t.Error("Message-Authenticator is not the last attribute, so the HMAC does not cover what follows it")
	}

	if !out.Disconnected || out.Failure != nil {
		t.Fatalf("an ACK was answered as %s", truncate(raw))
	}
	if !out.VoucherRevoked || out.GrantsRevoked != 1 {
		t.Errorf("withdrawals not reported: %s", truncate(raw))
	}
	if d.voucherState() != "revoked" || !d.grantRevoked() {
		t.Errorf("the store does not show the withdrawals: voucher %q, grant revoked %v", d.voucherState(), d.grantRevoked())
	}

	// And radacct was not written: the device's Acct-Stop closes the row.
	var stop *time.Time
	if err := d.owner.QueryRow(context.Background(),
		`SELECT acctstoptime FROM radacct WHERE acctuniqueid = $1`, d.session).Scan(&stop); err != nil {
		t.Fatal(err)
	}
	if stop != nil {
		t.Error("Foyer wrote an Acct-Stop into radacct; FreeRADIUS owns that table")
	}

	var audit struct {
		Items []gen.AuditEntry `json:"items"`
	}
	d.getJSON("/audit?limit=50", &audit)
	var found *gen.AuditEntry
	for i, e := range audit.Items {
		if e.Kind == gen.AuditKindSessionDisconnected && e.Subject != nil && e.Subject.Id == d.session {
			found = &audit.Items[i]
		}
	}
	if found == nil {
		t.Fatal("no session-disconnected entry in the audit log")
	}
	if found.SiteId == nil || *found.SiteId != d.siteA || found.ActorUsername != d.adminUser {
		t.Errorf("the entry does not name the venue and the actor: %+v", found)
	}
	if p := *found.Params; p["disconnected"] != true || p["voucherRevoked"] != true || p["voucherId"] != d.voucherID {
		t.Errorf("the entry does not say what happened: %v", p)
	}
}

// TestDisconnectReportsWhatTheNASAnswered: a NAK carries its Error-Cause, silence is
// `no-answer`, and a reply signed with another secret is `bad-secret` — three
// different afternoons for an operator, and never a disconnect.
func TestDisconnectReportsWhatTheNASAnswered(t *testing.T) {
	d := newDisconnectWorld(t)
	for _, c := range []struct {
		answer, cause string
		errorCause    any
	}{
		{"nak503", "refused", float64(503)},
		{"silent", "no-answer", nil},
		{"wrong-secret", "bad-secret", nil},
	} {
		t.Run(c.answer, func(t *testing.T) {
			d.nas.set(c.answer)
			raw := d.do(http.MethodPost, "/sessions/"+d.session+"/disconnect", map[string]any{}, http.StatusOK)
			var out gen.SessionDisconnectResult
			mustJSON(t, raw, &out)
			if out.Disconnected {
				t.Fatalf("%s was reported as a disconnect", c.answer)
			}
			if out.Failure == nil || out.Failure.Type != gen.ProblemssessionEndFailed || out.Failure.Params == nil {
				t.Fatalf("no failure: %s", truncate(raw))
			}
			p := *out.Failure.Params
			if p["cause"] != c.cause || p["errorCause"] != c.errorCause {
				t.Errorf("failure params %v, want cause=%s errorCause=%v", p, c.cause, c.errorCause)
			}
			if p["nasName"] != "Hotspot router" {
				t.Errorf("the failure names device %v", p["nasName"])
			}
			if d.voucherState() == "revoked" {
				t.Error("a plain disconnect revoked the voucher")
			}
		})
	}
}

// TestDisconnectOnRuckusIsRefusedBeforeAnythingChanges: the stored capability says
// the device can, the driver seam has nothing to build for Ruckus, and the driver
// wins. Nothing is revoked and nothing is sent.
func TestDisconnectOnRuckusIsRefusedBeforeAnythingChanges(t *testing.T) {
	d := newDisconnectWorld(t)
	raw := d.do(http.MethodPost, "/sessions/"+d.ruckusSession+"/disconnect",
		map[string]any{"revokeVoucher": true, "revokeGrant": true}, http.StatusConflict)
	var p struct {
		Type   gen.ProblemType `json:"type"`
		Params map[string]any  `json:"params"`
	}
	mustJSON(t, raw, &p)
	if p.Type != gen.ProblemsdisconnectUnsupported || p.Params["driver"] != "ruckus" || p.Params["nasName"] != "SmartZone atrium" {
		t.Fatalf("answer %s, want disconnect-unsupported naming the ruckus device", truncate(raw))
	}
	if d.voucherState() == "revoked" {
		t.Error("the voucher was revoked on a device Foyer cannot disconnect")
	}
	if reqs, _ := d.nas.seen(); len(reqs) != 0 {
		t.Errorf("%d packets were sent for a refused disconnect", len(reqs))
	}
}
