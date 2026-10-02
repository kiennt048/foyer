package nas

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// These tests drive a hand-written UDP responder, and docs/rules/testing.md
// is explicit about what that is worth: "ACKing a CoA is trivial; faithfully
// rejecting one is not." So the responder here verifies the Request
// Authenticator and the Message-Authenticator before answering anything, and
// refuses when they do not check out — which is the half a mock usually skips
// and the half that would have hidden an encoder bug.
//
// Nothing in coa.go has been run against real gear. It stays UNVERIFIED until
// a lab session says otherwise.

const coaTestSecret = "fixture-shared-9f2c41"

type coaResponder struct {
	pc      net.PacketConn
	secret  string
	reply   func(req []byte) []byte // nil return = stay silent
	packets atomic.Int32
}

func newCoAResponder(t *testing.T, secret string, reply func(req []byte) []byte) *coaResponder {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	r := &coaResponder{pc: pc, secret: secret, reply: reply}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			r.packets.Add(1)
			req := make([]byte, n)
			copy(req, buf[:n])
			if out := r.reply(req); out != nil {
				_, _ = pc.WriteTo(out, addr)
			}
		}
	}()
	t.Cleanup(func() { _ = pc.Close() })
	return r
}

func (r *coaResponder) addr() string { return r.pc.LocalAddr().String() }

// verifyRequest is what a real NAS does before it answers, and what a mock that
// only ACKs never does.
func verifyRequest(t *testing.T, req []byte, secret string) {
	t.Helper()
	if len(req) < 20 {
		t.Fatalf("request is %d bytes", len(req))
	}
	if req[0] != codeDisconnectRequest {
		t.Errorf("code = %d, want %d (Disconnect-Request)", req[0], codeDisconnectRequest)
	}
	if got := int(binary.BigEndian.Uint16(req[2:4])); got != len(req) {
		t.Errorf("declared length %d, actual %d", got, len(req))
	}

	// Request Authenticator: MD5(code‖id‖length‖16 zeros‖attrs‖secret).
	zeroed := make([]byte, len(req))
	copy(zeroed, req)
	for i := 4; i < 20; i++ {
		zeroed[i] = 0
	}
	sum := md5.New()
	sum.Write(zeroed)
	sum.Write([]byte(secret))
	if !hmac.Equal(sum.Sum(nil), req[4:20]) {
		t.Errorf("Request Authenticator does not verify")
	}

	// Message-Authenticator: HMAC-MD5 over the packet with the Authenticator
	// field zeroed and its own value zeroed.
	off := findAttrOffset(req[20:], attrMessageAuthentica)
	if off < 0 {
		t.Fatalf("no Message-Authenticator; FreeRADIUS requires one on Disconnect-Request")
	}
	probe := make([]byte, len(req))
	copy(probe, req)
	for i := 4; i < 20; i++ {
		probe[i] = 0
	}
	want := make([]byte, 16)
	copy(want, probe[20+off+2:20+off+18])
	for i := 0; i < 16; i++ {
		probe[20+off+2+i] = 0
	}
	mac := hmac.New(md5.New, []byte(secret))
	mac.Write(probe)
	if !hmac.Equal(mac.Sum(nil), want) {
		t.Errorf("Message-Authenticator does not verify")
	}
}

func findAttrOffset(attrs []byte, want uint8) int {
	off := 0
	for off+2 <= len(attrs) {
		l := int(attrs[off+1])
		if l < 2 || off+l > len(attrs) {
			return -1
		}
		if attrs[off] == want {
			return off
		}
		off += l
	}
	return -1
}

// buildReply assembles a well-formed answer the client must accept.
func buildReply(req []byte, secret string, code uint8, errorCause uint32, hasCause bool) []byte {
	var attrs []byte
	if hasCause {
		attrs = append(attrs, attrErrorCause, 6)
		attrs = binary.BigEndian.AppendUint32(attrs, errorCause)
	}
	length := 20 + len(attrs)
	reply := make([]byte, 0, length)
	reply = append(reply, code, req[1])
	reply = binary.BigEndian.AppendUint16(reply, uint16(length))
	reply = append(reply, req[4:20]...) // the request authenticator, for now
	reply = append(reply, attrs...)

	sum := md5.New()
	sum.Write(reply)
	sum.Write([]byte(secret))
	copy(reply[4:20], sum.Sum(nil))
	return reply
}

func testSession() Session {
	return Session{
		CallingStationID: "1c:71:25:63:e4:24",
		AcctSessionID:    "8f2a41c0",
		NASIdentifier:    "ap-lobby",
		NASIPAddress:     netip.MustParseAddr("192.0.2.4"),
		UserName:         "guest",
	}
}

func TestCoADisconnect(t *testing.T) {
	cases := []struct {
		name       string
		replyCode  uint8
		cause      uint32
		hasCause   bool
		wantErr    bool
		wantCause  uint32
		wantTyped  bool
		wantNoCode bool
	}{
		{name: "ACK", replyCode: codeDisconnectACK},
		{
			name: "NAK 503 Session-Context-Not-Found", replyCode: codeDisconnectNAK,
			cause: ErrorCauseSessionContextNotFound, hasCause: true,
			wantErr: true, wantTyped: true, wantCause: ErrorCauseSessionContextNotFound,
		},
		{
			name: "NAK 508 Multiple-Session-Selection-Unsupported", replyCode: codeDisconnectNAK,
			cause: ErrorCauseMultipleSessionSelectionUnsupported, hasCause: true,
			wantErr: true, wantTyped: true, wantCause: ErrorCauseMultipleSessionSelectionUnsupported,
		},
		{
			name: "NAK with no Error-Cause is still a refusal", replyCode: codeDisconnectNAK,
			wantErr: true, wantTyped: true, wantNoCode: true,
		},
		{
			name: "an unexpected code is not success", replyCode: 99, wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCoAResponder(t, coaTestSecret, func(req []byte) []byte {
				verifyRequest(t, req, coaTestSecret)
				return buildReply(req, coaTestSecret, tc.replyCode, tc.cause, tc.hasCause)
			})
			c := &CoAClient{Addr: r.addr(), Secret: coaTestSecret, Timeout: time.Second, Retries: 1}

			err := c.Disconnect(context.Background(), testSession())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Disconnect err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantTyped {
				return
			}
			var coaErr *CoAError
			if !errors.As(err, &coaErr) {
				t.Fatalf("error %v is not a *CoAError; a NAK flattened into a string reports success on a no-op", err)
			}
			if tc.wantNoCode {
				if coaErr.HasErrorCause {
					t.Errorf("HasErrorCause = true for a NAK that carried none")
				}
				return
			}
			if coaErr.ErrorCause != tc.wantCause {
				t.Errorf("ErrorCause = %d, want %d", coaErr.ErrorCause, tc.wantCause)
			}
			if !coaErr.HasErrorCause {
				t.Errorf("HasErrorCause = false though the NAK carried %d", tc.wantCause)
			}
		})
	}
}

// TestCoADisconnect_RetransmitsThenTimesOut: CoA is UDP, so silence is a normal
// outcome. It must be retransmitted a bounded number of times and then named.
func TestCoADisconnect_RetransmitsThenTimesOut(t *testing.T) {
	r := newCoAResponder(t, coaTestSecret, func(req []byte) []byte { return nil })
	c := &CoAClient{Addr: r.addr(), Secret: coaTestSecret, Timeout: 80 * time.Millisecond, Retries: 2}

	start := time.Now()
	err := c.Disconnect(context.Background(), testSession())
	elapsed := time.Since(start)

	if !errors.Is(err, ErrCoATimeout) {
		t.Fatalf("err = %v, want ErrCoATimeout", err)
	}
	if got := r.packets.Load(); got != 3 {
		t.Errorf("the NAS saw %d packets, want 3 (one send plus two retransmits)", got)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("gave up after %s, which is less than the stated budget", elapsed)
	}
}

// TestCoADisconnect_ForgedReplyRejected: the Response Authenticator is the only
// thing separating an ACK from anything else that can send a UDP datagram.
func TestCoADisconnect_ForgedReplyRejected(t *testing.T) {
	r := newCoAResponder(t, coaTestSecret, func(req []byte) []byte {
		return buildReply(req, "a-different-shared-value", codeDisconnectACK, 0, false)
	})
	c := &CoAClient{Addr: r.addr(), Secret: coaTestSecret, Timeout: 300 * time.Millisecond}

	err := c.Disconnect(context.Background(), testSession())
	if err == nil {
		t.Fatalf("a reply signed with the wrong secret was accepted as an ACK")
	}
}

// TestCoADisconnect_MismatchedIdentifierRejected guards the other half of reply
// matching: a stale answer to a previous request is not an answer to this one.
func TestCoADisconnect_MismatchedIdentifierRejected(t *testing.T) {
	r := newCoAResponder(t, coaTestSecret, func(req []byte) []byte {
		spoiled := make([]byte, len(req))
		copy(spoiled, req)
		spoiled[1] = req[1] + 1
		return buildReply(spoiled, coaTestSecret, codeDisconnectACK, 0, false)
	})
	c := &CoAClient{Addr: r.addr(), Secret: coaTestSecret, Timeout: 300 * time.Millisecond}

	if err := c.Disconnect(context.Background(), testSession()); err == nil {
		t.Fatalf("a reply for a different identifier was accepted")
	}
}

func TestCoAClient_RefusesIncompleteConfiguration(t *testing.T) {
	cases := []struct {
		name string
		c    CoAClient
	}{
		{"no address", CoAClient{Secret: coaTestSecret}},
		{"no shared secret", CoAClient{Addr: "127.0.0.1:3799"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.c.Disconnect(context.Background(), testSession()); err == nil {
				t.Fatalf("an incomplete CoA client tried to send")
			}
		})
	}
}

func TestCoAAddr(t *testing.T) {
	if got := CoAAddr("192.168.1.1", 0); got != "192.168.1.1:3799" {
		t.Errorf("CoAAddr default = %q", got)
	}
	// Cisco frequently uses 1700, which is why the port is a field.
	if got := CoAAddr("10.0.0.1", 1700); got != "10.0.0.1:1700" {
		t.Errorf("CoAAddr(1700) = %q", got)
	}
}
