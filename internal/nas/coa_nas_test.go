package nas

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
)

// A fake NAS that refuses eleven ways and accepts one.
//
// `docs/rules/testing.md` is the specification for this file:
//
//	ACKing a CoA is trivial; faithfully *rejecting* one is not. Real gear NAKs
//	on session-identification mismatches, differs per vendor, is picky about
//	source IP, sometimes listens on 1700, and returns Error-Cause codes you must
//	handle.
//
// So every accept path here is the weak half of a test and is treated as such.
// The eleven refusals, in the order the responder applies them:
//
//	 1. a packet shorter than a RADIUS header            → silence
//	 2. no Message-Authenticator                          → silence (FreeRADIUS 3.2.x requires one)
//	 3. Message-Authenticator does not verify             → silence
//	 4. Request Authenticator does not verify             → silence
//	 5. a malformed attribute or VSA                      → silence
//	 6. no session-identification attribute at all        → NAK 402 Missing-Attribute
//	 7. identification matching no session                → NAK 503 Session-Context-Not-Found
//	 8. identification matching more than one session     → NAK 508 Multiple-Session-Selection-Unsupported
//	 9. CoA-Request on a device that only does Disconnect → NAK 405 Unsupported-Service
//	10. a vendor attribute this device does not know      → NAK 401 Unsupported-Attribute
//	11. a session it can see and will not remove          → NAK 504 Session-Context-Not-Removable
//
// The one accept: every authenticator verifies, the identification matches
// exactly one session, and every change attribute is one this device knows.
//
// **What it still cannot do, stated rather than implied.** It is not picky about
// source IP — the client dials a connected UDP socket, so there is one source
// and refusing it would refuse everything. It never answers slowly, never
// fragments, never reorders, and never disagrees with itself between two
// requests. Real gear does all of that, and none of it is here.

const (
	fakeNASSecret = "fixture-coa-4b19ad"
)

// rawAttr is one decoded RADIUS attribute.
type rawAttr struct {
	Type  uint8
	Value []byte
}

// coaPacket is a decoded request.
type coaPacket struct {
	Code  uint8
	ID    uint8
	Attrs []rawAttr
	VSAs  []VendorAttribute
}

// decodeCoAPacket walks RFC 2865's attribute layout.
//
// Written from the RFC rather than from `coa.go`, because a decoder that mirrors
// an encoder agrees with its mistakes. The vendor-specific branch follows §5.26:
// four bytes of vendor id, then the recommended `vendor-type, vendor-length,
// value` sub-attribute — and it insists the two lengths agree, which is the
// check that catches an off-by-one an encoder and a decoder could otherwise
// share.
func decodeCoAPacket(b []byte) (coaPacket, error) {
	if len(b) < 20 {
		return coaPacket{}, fmt.Errorf("packet is %d bytes, minimum 20", len(b))
	}
	declared := int(binary.BigEndian.Uint16(b[2:4]))
	if declared != len(b) {
		return coaPacket{}, fmt.Errorf("declared length %d, actual %d", declared, len(b))
	}
	p := coaPacket{Code: b[0], ID: b[1]}
	rest := b[20:]
	for len(rest) > 0 {
		if len(rest) < 2 {
			return coaPacket{}, fmt.Errorf("trailing byte with no attribute header")
		}
		t, l := rest[0], int(rest[1])
		if l < 2 || l > len(rest) {
			return coaPacket{}, fmt.Errorf("attribute %d declares length %d with %d bytes left", t, l, len(rest))
		}
		val := rest[2:l]
		if t == attrVendorSpecific {
			if len(val) < 6 {
				return coaPacket{}, fmt.Errorf("vendor attribute carries %d bytes, minimum 6", len(val))
			}
			vendorID := binary.BigEndian.Uint32(val[:4])
			sub := val[4:]
			vt, vl := sub[0], int(sub[1])
			if vl < 2 || vl != len(sub) {
				return coaPacket{}, fmt.Errorf(
					"vendor sub-attribute declares length %d but %d bytes follow the vendor id; "+
						"exactly one sub-attribute per RADIUS attribute is what this decoder accepts", vl, len(sub))
			}
			p.VSAs = append(p.VSAs, VendorAttribute{
				VendorID: vendorID, VendorType: vt, Value: append([]byte(nil), sub[2:]...),
			})
		} else {
			p.Attrs = append(p.Attrs, rawAttr{Type: t, Value: append([]byte(nil), val...)})
		}
		rest = rest[l:]
	}
	return p, nil
}

func (p coaPacket) attr(t uint8) (string, bool) {
	for _, a := range p.Attrs {
		if a.Type == t {
			return string(a.Value), true
		}
	}
	return "", false
}

// fakeSession is one session this NAS believes it has, spelled the way this
// device spells things.
type fakeSession struct {
	AcctSessionID string
	// CallingStationID as the *device* writes it. The driver has to match this
	// exactly, which is what turns "every vendor spells a MAC differently" from
	// a comment into a test.
	CallingStationID string
	UserName         string
	AuditSessionID   string
	// Removable false is a session the device can see and refuses to end.
	Removable bool
}

// fakeNAS is the responder.
type fakeNAS struct {
	pc     net.PacketConn
	secret string

	mu       sync.Mutex
	sessions []fakeSession
	// supportsCoA false is a device that implements Disconnect-Request and not
	// CoA-Request, which RFC 5176 §3.1 explicitly permits.
	supportsCoA bool
	// knownVendorAttrs is what change attributes this device understands, keyed
	// "vendorID/vendorType". A CoA carrying anything else is NAK 401.
	knownVendorAttrs map[string]bool
	// ackEverything is the device the whole package is written against: it
	// answers ACK whatever it is sent. Used to prove Probe notices.
	ackEverything bool
	// replyWithWrongFamily answers a CoA-Request with a Disconnect-ACK.
	replyWithWrongFamily bool
	// silent never answers. A device behind a firewall, on the wrong port, or
	// one that ignores a request it cannot match — three different problems that
	// look identical from here, which is the point.
	silent bool
	// replySecret, when set, signs the answer with a secret the client does not
	// hold. That is a forged reply, and also a device whose secret was changed
	// on one side only.
	replySecret string

	// observed records every decoded request, for assertions about what went on
	// the wire rather than about what came back.
	observed  []coaPacket
	refusals  []uint32
	acks      int
	dropped   []string
	teardowns []string
}

func newFakeNAS(t *testing.T, mutate func(*fakeNAS)) *fakeNAS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	n := &fakeNAS{
		pc:               pc,
		secret:           fakeNASSecret,
		supportsCoA:      true,
		knownVendorAttrs: map[string]bool{},
	}
	if mutate != nil {
		mutate(n)
	}
	go n.serve()
	t.Cleanup(func() { _ = pc.Close() })
	return n
}

func (n *fakeNAS) addr() string { return n.pc.LocalAddr().String() }

// allow teaches this device one vendor attribute, **under the lock**.
//
// `go test -race` is what catches the alternative: `newFakeNAS` starts the
// serving goroutine before it returns, so every call here writes a map the
// responder is already reading. Without the lock it is green without the
// detector, on every run, because the first request happens milliseconds later.
//
// Configure a device through `newFakeNAS`'s mutate where the ordering allows;
// this exists for the cases that read the listener's address first.
func (n *fakeNAS) allow(vendorID uint32, vendorType uint8) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.knownVendorAttrs[fmt.Sprintf("%d/%d", vendorID, vendorType)] = true
}

func (n *fakeNAS) serve() {
	buf := make([]byte, 4096)
	for {
		count, from, err := n.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		req := append([]byte(nil), buf[:count]...)
		if out := n.answer(req); out != nil {
			_, _ = n.pc.WriteTo(out, from)
		}
	}
}

// answer applies the eleven refusals. A nil return is silence, which is a real
// answer on UDP and the one a client has to survive.
func (n *fakeNAS) answer(req []byte) []byte {
	n.mu.Lock()
	defer n.mu.Unlock()

	drop := func(why string) []byte {
		n.dropped = append(n.dropped, why)
		return nil
	}

	if n.silent {
		return drop("this device never answers")
	}
	// 1. too short.
	if len(req) < 20 {
		return drop("shorter than a RADIUS header")
	}
	// 5a. structurally invalid.
	p, err := decodeCoAPacket(req)
	if err != nil {
		return drop("undecodable: " + err.Error())
	}
	n.observed = append(n.observed, p)

	// 2 and 3. Message-Authenticator, which FreeRADIUS 3.2.x requires on both
	// request codes. A device that skipped this check would accept a forged
	// request from anyone who can send a UDP datagram.
	off := findAttrOffset(req[20:], attrMessageAuthentica)
	if off < 0 {
		return drop("no Message-Authenticator")
	}
	probe := append([]byte(nil), req...)
	got := append([]byte(nil), probe[20+off+2:20+off+18]...)
	for i := 0; i < 16; i++ {
		probe[20+off+2+i] = 0
	}
	zeroAuth := append([]byte(nil), probe...)
	for i := 4; i < 20; i++ {
		zeroAuth[i] = 0
	}
	mac := hmac.New(md5.New, []byte(n.secret))
	mac.Write(zeroAuth)
	if !hmac.Equal(mac.Sum(nil), got) {
		return drop("Message-Authenticator does not verify")
	}

	// 4. Request Authenticator: MD5(code‖id‖length‖16 zeros‖attrs‖secret).
	check := append([]byte(nil), req...)
	for i := 4; i < 20; i++ {
		check[i] = 0
	}
	sum := md5.New()
	sum.Write(check)
	sum.Write([]byte(n.secret))
	if !hmac.Equal(sum.Sum(nil), req[4:20]) {
		return drop("Request Authenticator does not verify")
	}

	if n.replyWithWrongFamily {
		n.acks++
		return n.reply(req, codeDisconnectACK, 0, false)
	}
	if n.ackEverything {
		n.acks++
		if p.Code == codeCoARequest {
			return n.reply(req, codeCoAACK, 0, false)
		}
		return n.reply(req, codeDisconnectACK, 0, false)
	}

	isCoA := p.Code == codeCoARequest
	nak := func(cause uint32) []byte {
		n.refusals = append(n.refusals, cause)
		code := uint8(codeDisconnectNAK)
		if isCoA {
			code = codeCoANAK
		}
		return n.reply(req, code, cause, true)
	}

	// 9. a device that does not implement CoA-Request.
	if isCoA && !n.supportsCoA {
		return nak(ErrorCauseUnsupportedService)
	}

	idents, changes := n.split(p)

	// 6. nothing to identify a session with.
	if len(idents) == 0 {
		return nak(ErrorCauseMissingAttribute)
	}

	matched := n.matching(idents)
	// 7 and 8.
	switch {
	case len(matched) == 0:
		return nak(ErrorCauseSessionContextNotFound)
	case len(matched) > 1:
		return nak(ErrorCauseMultipleSessionSelectionUnsupported)
	}

	if isCoA {
		// A CoA-Request that changes nothing asks the device to do nothing.
		if len(changes) == 0 {
			return nak(ErrorCauseMissingAttribute)
		}
		// 10. an attribute this device has never heard of.
		for _, c := range changes {
			if !n.knownVendorAttrs[fmt.Sprintf("%d/%d", c.VendorID, c.VendorType)] {
				return nak(ErrorCauseUnsupportedAttribute)
			}
		}
		n.acks++
		return n.reply(req, codeCoAACK, 0, false)
	}

	// 11. a session it can see and will not end.
	if !matched[0].Removable {
		return nak(ErrorCauseSessionContextNotRemovable)
	}
	n.teardowns = append(n.teardowns, matched[0].AcctSessionID)
	n.acks++
	return n.reply(req, codeDisconnectACK, 0, false)
}

// split separates session identification from what a request wants changed.
//
// A vendor attribute is identification when it is an `audit-session-id` AV-pair
// and a change otherwise, which is Cisco's own split and the only one modelled
// here.
func (n *fakeNAS) split(p coaPacket) (map[string]string, []VendorAttribute) {
	idents := map[string]string{}
	if v, ok := p.attr(attrUserName); ok {
		idents["user-name"] = v
	}
	if v, ok := p.attr(attrCallingStationID); ok {
		idents["calling-station-id"] = v
	}
	if v, ok := p.attr(attrAcctSessionID); ok {
		idents["acct-session-id"] = v
	}
	var changes []VendorAttribute
	for _, va := range p.VSAs {
		s := string(va.Value)
		if va.VendorID == ciscoVendorID && va.VendorType == ciscoAVPairAttrType &&
			strings.HasPrefix(s, ciscoAVAuditSessionIDKey+"=") {
			idents["audit-session-id"] = strings.TrimPrefix(s, ciscoAVAuditSessionIDKey+"=")
			continue
		}
		changes = append(changes, va)
	}
	return idents, changes
}

// matching keeps the sessions that agree with **every** identifier in the
// request, which is what RFC 5176 §2.3 requires. String equality throughout: a
// MAC spelled differently is a MAC that does not match, and that is the point.
func (n *fakeNAS) matching(idents map[string]string) []fakeSession {
	var out []fakeSession
	for _, s := range n.sessions {
		ok := true
		for k, v := range idents {
			var have string
			switch k {
			case "user-name":
				have = s.UserName
			case "calling-station-id":
				have = s.CallingStationID
			case "acct-session-id":
				have = s.AcctSessionID
			case "audit-session-id":
				have = s.AuditSessionID
			}
			if have != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

func (n *fakeNAS) reply(req []byte, code uint8, cause uint32, hasCause bool) []byte {
	secret := n.secret
	if n.replySecret != "" {
		secret = n.replySecret
	}
	return buildReply(req, secret, code, cause, hasCause)
}

func (n *fakeNAS) snapshot() (refusals []uint32, acks int, dropped []string, observed []coaPacket) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]uint32(nil), n.refusals...), n.acks,
		append([]string(nil), n.dropped...), append([]coaPacket(nil), n.observed...)
}

// fakeSessions is a SessionSource for the driver tests.
//
// It answers the three ways `SessionSource` documents — found, not found, and
// "I could not look" — because a source that can only do the first cannot drive
// the branch where a database outage must not read as a guest's device being
// absent.
type fakeSessions struct {
	byMAC map[string]Session
	fail  error
}

func (f fakeSessions) ActiveSession(_ context.Context, mac string) (Session, bool, error) {
	if f.fail != nil {
		return Session{}, false, f.fail
	}
	s, ok := f.byMAC[normalizeMAC(mac)]
	return s, ok, nil
}
