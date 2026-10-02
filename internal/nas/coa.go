package nas

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// RFC 5176 Disconnect / CoA. One encoder, used by the CoA driver family and by
// UniFi's revocation fallback.
//
// **UNVERIFIED against hardware.** Nothing in this file has been exercised
// against a real NAS. docs/rules/testing.md is explicit about why that
// matters here: "ACKing a CoA is trivial; faithfully rejecting one is not."
// The tests below drive a hand-written UDP responder, which proves the encoder
// and the NAK handling against this author's assumptions and nothing more. The
// deliverable that would change that is a lab/ test against real gear plus a
// line in docs/protocol-reality.md.
//
// The one thing the responder is *not* free to invent is the framing: it
// decodes what this file encodes by walking RFC 2865's own attribute layout,
// and the vendor-specific tests assert the exact bytes rather than only the
// round trip. A decoder that mirrors an encoder's mistake agrees with it; a
// hand-computed byte string does not.

// RADIUS packet codes used by RFC 5176.
const (
	codeDisconnectRequest = 40
	codeDisconnectACK     = 41
	codeDisconnectNAK     = 42
	codeCoARequest        = 43
	codeCoAACK            = 44
	codeCoANAK            = 45
)

// RADIUS attribute types used here.
//
// `attrVendorSpecific` is the prerequisite for the whole CoA driver family: a
// Cisco reauthorize is a CoA-Request
// carrying `Cisco-AVPair = subscriber:command=reauthenticate`, and that cannot
// be written with a `type, length, value` encoder. See encodeVendorAttribute.
const (
	attrUserName          = 1
	attrNASIPAddress      = 4
	attrVendorSpecific    = 26
	attrCallingStationID  = 31
	attrNASIdentifier     = 32
	attrAcctSessionID     = 44
	attrMessageAuthentica = 80
	attrErrorCause        = 101
)

// DefaultCoAPort is the common CoA/Disconnect port. It is a default and never a
// constant in the wire sense: the port is per-NAS, and Cisco frequently uses
// 1700. CoAClient.Addr carries the real one.
const DefaultCoAPort = 3799

// Error-Cause values a driver must handle rather than treat as success.
// RFC 5176 §3.1.
//
// The set is wider than the two `docs/rules/nas-drivers.md` names because
// the rule's point generalises: **every one of these is a refusal, and a driver
// that flattens any of them into success reports success on a no-op.** 402 and
// 405 are the two a *reauthorize* meets first — a request the NAS could not
// identify a session from, and a NAS that does not implement CoA-Request at all
// while implementing Disconnect-Request perfectly well.
const (
	ErrorCauseUnsupportedAttribute                uint32 = 401
	ErrorCauseMissingAttribute                    uint32 = 402
	ErrorCauseUnsupportedService                  uint32 = 405
	ErrorCauseAdministrativelyProhibited          uint32 = 501
	ErrorCauseSessionContextNotFound              uint32 = 503
	ErrorCauseSessionContextNotRemovable          uint32 = 504
	ErrorCauseMultipleSessionSelectionUnsupported uint32 = 508
)

// ErrCoATimeout is returned when no answer arrived within the deterministic
// budget. CoA is UDP: silence is a normal outcome and must be one the caller
// can name, not a generic error.
var ErrCoATimeout = errors.New("nas: CoA timed out with no answer")

// ErrCoABadAuthenticator is a reply whose Response Authenticator did not verify.
//
// Named rather than left as a sentence because it is the single most useful
// diagnostic this protocol produces: on UDP with no TLS, "the shared secret on
// the device does not match the one in Foyer" and "somebody forged a reply" are
// the same observation, and both are worth telling an operator in those words.
// CoADriver.Probe reports it as the configuration error it almost always is.
var ErrCoABadAuthenticator = errors.New("nas: CoA reply failed authenticator verification — the shared secret does not match, or the reply was forged")

// ErrCoANoSessionIdentity is a request that names no session.
//
// RFC 5176 §2.1 identifies the session from the attributes in the request. A
// request carrying none asks the NAS to act on *something*, and the answers to
// that are Error-Cause 402 Missing-Attribute on a careful NAS and anything at
// all on a careless one. Refusing before the packet leaves is cheaper than
// finding out which kind is on the other end.
var ErrCoANoSessionIdentity = errors.New("nas: CoA request identifies no session — none of Acct-Session-Id, Calling-Station-Id, audit-session-id or User-Name is set")

// VendorAttribute is one Vendor-Specific attribute, RFC 2865 §5.26.
//
// # Why this type exists at all
//
// Everything a CoA driver has to say beyond "here is the session" is vendor
// specific, and none of it fits a `type, length, value` encoder. Cisco's
// reauthorize is `Cisco-AVPair = subscriber:command=reauthenticate`; Aruba
// changes a live session by carrying `Aruba-User-Role`; MikroTik carries
// `Mikrotik-Rate-Limit`. Each is type 26 with a vendor id in front of a
// sub-attribute of its own.
//
// # What is known and what is guessed, because they are not the same
//
// The **framing** is DOCUMENTED — RFC 2865 §5.26 defines type 26 as a 4-byte
// vendor id followed by vendor data, and recommends the `vendor-type,
// vendor-length, value` sub-attribute layout that every vendor here uses. The
// tests assert those bytes against a hand-written expectation.
//
// The **numbers** are whatever the caller passes, and this type checks none of
// them. For the three vendors Foyer ships they are DOCUMENTED, read out of the dictionaries FreeRADIUS itself parses — see
// `FreeRADIUSDictionaryCitation` and each driver's `AttributeEvidence`. For
// anything else they are the caller's claim.
//
// **Why that distinction is worth a paragraph on a struct with three fields.** A
// wrong vendor id produces a syntactically valid attribute the NAS ignores — an
// ACK and no effect, the failure this whole file is written against, arriving
// through the one channel a typed `Error-Cause` cannot see. Nothing here or on
// the wire can catch it, so it is caught by reading the dictionary the server
// parses. That took one command and settled four numbers.
//
// What a dictionary cannot settle is what belongs *inside* the value: the
// AV-pair command strings and the rate-pair direction are still UNVERIFIED, and
// each vendor's `AttributeEvidence` says so beside the encoding it now knows.
type VendorAttribute struct {
	// VendorID is the IANA Private Enterprise Number.
	VendorID uint32
	// VendorType is the attribute number within that vendor's dictionary.
	VendorType uint8
	// Value is the attribute value, already in wire form. A vendor whose
	// attribute is a string puts its bytes here; nothing here interprets it.
	Value []byte
}

// VendorString builds a string-valued VSA, which is what all three of Cisco's,
// Aruba's and MikroTik's captive-portal attributes are.
func VendorString(vendorID uint32, vendorType uint8, value string) VendorAttribute {
	return VendorAttribute{VendorID: vendorID, VendorType: vendorType, Value: []byte(value)}
}

// maxVendorValue is what fits: an attribute's length field is one byte, and the
// wrapper costs 2 (type, length) + 4 (vendor id) + 2 (vendor type, vendor
// length) = 8.
const maxVendorValue = 255 - 8

// encodeVendorAttribute writes one type-26 attribute in RFC 2865 §5.26 form:
//
//	26 | length | vendor-id (4, big endian) | vendor-type | vendor-length | value
//
// where vendor-length counts the two sub-attribute header bytes and length
// counts everything including its own two.
//
// One VSA per RADIUS attribute, never several packed into one. RFC 2865 §5.26
// permits multiple sub-attributes inside a single type-26 attribute and warns in
// the same paragraph that it is not recommended, and FreeRADIUS's own encoder
// emits one per attribute. Packing would also make CollapseToMap's problem
// worse rather than better: two Cisco AV-pairs would become one attribute whose
// halves cannot be told apart by anything downstream.
func encodeVendorAttribute(a VendorAttribute) ([]byte, error) {
	if len(a.Value) > maxVendorValue {
		return nil, fmt.Errorf("nas: vendor attribute %d/%d is %d bytes, max %d",
			a.VendorID, a.VendorType, len(a.Value), maxVendorValue)
	}
	if len(a.Value) == 0 {
		return nil, fmt.Errorf("nas: vendor attribute %d/%d has an empty value", a.VendorID, a.VendorType)
	}
	out := make([]byte, 0, 8+len(a.Value))
	out = append(out, attrVendorSpecific, uint8(8+len(a.Value)))
	out = binary.BigEndian.AppendUint32(out, a.VendorID)
	out = append(out, a.VendorType, uint8(2+len(a.Value)))
	out = append(out, a.Value...)
	return out, nil
}

// CoAError is a NAK, carrying the Error-Cause the NAS gave.
//
// Typed rather than a bare error on purpose: a driver that flattens
// "Session-Context-Not-Found" into an error string reports success on a no-op
// the next time somebody adds a retry.
type CoAError struct {
	Code          uint8
	ErrorCause    uint32
	HasErrorCause bool
}

func (e *CoAError) Error() string {
	if !e.HasErrorCause {
		return fmt.Sprintf("nas: CoA rejected (code %d) with no Error-Cause", e.Code)
	}
	return fmt.Sprintf("nas: CoA rejected (code %d), Error-Cause %d (%s)",
		e.Code, e.ErrorCause, errorCauseName(e.ErrorCause))
}

func errorCauseName(c uint32) string {
	switch c {
	case ErrorCauseUnsupportedAttribute:
		return "Unsupported-Attribute"
	case ErrorCauseMissingAttribute:
		return "Missing-Attribute"
	case ErrorCauseUnsupportedService:
		return "Unsupported-Service"
	case ErrorCauseAdministrativelyProhibited:
		return "Administratively-Prohibited"
	case ErrorCauseSessionContextNotFound:
		return "Session-Context-Not-Found"
	case ErrorCauseSessionContextNotRemovable:
		return "Session-Context-Not-Removable"
	case ErrorCauseMultipleSessionSelectionUnsupported:
		return "Multiple-Session-Selection-Unsupported"
	default:
		return "unknown"
	}
}

// Session carries every identifier a platform might key a session on.
//
// Never assume one field identifies a session everywhere: Cisco falls back to
// Acct-Session-Id, Aruba and UniFi key on the MAC, Cisco IOS-XE has its own
// audit-session-id.
//
// **Two of these fields are not standard attributes and this encoder does not
// send them by itself.** `AuditSessionID` is a Cisco AV-pair, and `SiteID` is
// UniFi's and has no RADIUS representation at all. Which identifiers go into a
// packet is a per-vendor decision and therefore the driver's: see
// CoAVendorRules.Identify. Sending a Cisco VSA to an Aruba controller because
// the field happened to be populated is how a generic encoder starts encoding
// one vendor's assumptions.
type Session struct {
	AcctSessionID    string
	CallingStationID string
	AuditSessionID   string
	NASIPAddress     netip.Addr
	NASIdentifier    string
	SiteID           string
	UserName         string
}

// identifiesSomething reports whether this session names anything a NAS could
// match a session on. NAS-IP-Address and NAS-Identifier are excluded on purpose:
// they identify the *device* being asked, not the session being acted on.
func (s Session) identifiesSomething() bool {
	return s.AcctSessionID != "" || s.CallingStationID != "" ||
		s.AuditSessionID != "" || s.UserName != ""
}

// DefaultCoATimeout and DefaultCoARetries are what a client with no stored timing
// uses. An operator sets both per RADIUS client in the console
// (`updateNasCoaTiming`), because the right figure is a property of the path to one
// device rather than of the protocol; these are the answer for a device nobody has
// set, and the console shows them as the defaults.
const (
	DefaultCoATimeout = 2 * time.Second
	DefaultCoARetries = 0
)

// CoAClient sends RFC 5176 Disconnect-Requests.
type CoAClient struct {
	// Addr is host:port. The port is per-NAS.
	Addr string
	// Secret is the RADIUS shared secret for this NAS.
	Secret string
	// Timeout bounds one attempt. Total budget is Timeout × (Retries + 1).
	Timeout time.Duration
	// Retries is how many times the request is retransmitted after the first
	// send. UDP has no delivery guarantee, so this is not optional.
	Retries int

	// dial is a test seam.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// CoAAddr joins a host and a per-NAS port.
func CoAAddr(host string, port int) string {
	if port <= 0 {
		port = DefaultCoAPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Disconnect sends a Disconnect-Request and waits for an ACK.
//
// The variadic vendor attributes are the driver's: on Cisco IOS-XE the session
// is identified by `Cisco-AVPair = audit-session-id=…`, which is a VSA and not
// something this file can decide to send on its own.
func (c *CoAClient) Disconnect(ctx context.Context, s Session, extra ...VendorAttribute) error {
	if err := requireSessionIdentity(s, extra); err != nil {
		return err
	}
	return c.send(ctx, codeDisconnectRequest, s, extra)
}

// requireSessionIdentity refuses a request that names no session.
//
// On the public methods rather than inside send, so that a test can drive the
// far end's own refusal — a NAS answering Error-Cause 402 — without this check
// standing in front of it. The two guards are for two different readers: this
// one tells a caller, and the NAS's tells us what a device does with the shape
// when the caller is not us.
//
// The vendor attributes are not inspected: a driver may identify a session
// entirely through one, which is what Cisco's audit-session-id is.
func requireSessionIdentity(s Session, extra []VendorAttribute) error {
	if !s.identifiesSomething() && len(extra) == 0 {
		return ErrCoANoSessionIdentity
	}
	return nil
}

// CoA sends a CoA-Request — the half of RFC 5176 that changes a live session
// rather than ending it, and the half that needed vendor-specific encoding
// before it could exist at all.
//
// A NAS may implement Disconnect-Request and not CoA-Request; RFC 5176 §3.1
// gives it Error-Cause 405 Unsupported-Service to say so, and this returns that
// as a *CoAError rather than as success.
func (c *CoAClient) CoA(ctx context.Context, s Session, extra ...VendorAttribute) error {
	if err := requireSessionIdentity(s, extra); err != nil {
		return err
	}
	return c.send(ctx, codeCoARequest, s, extra)
}

func (c *CoAClient) send(ctx context.Context, code uint8, s Session, extra []VendorAttribute) error {
	if c.Addr == "" {
		return errors.New("nas: CoA client has no address")
	}
	if c.Secret == "" {
		return errors.New("nas: CoA client has no shared secret")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultCoATimeout
	}
	retries := c.Retries
	if retries < 0 {
		retries = 0
	}

	dial := c.dial
	if dial == nil {
		dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, address)
		}
	}

	var idByte [1]byte
	if _, err := rand.Read(idByte[:]); err != nil {
		return fmt.Errorf("nas: CoA identifier: %w", err)
	}
	identifier := idByte[0]

	packet, reqAuth, err := encodeRequest(code, identifier, c.Secret, s, extra)
	if err != nil {
		return err
	}

	conn, err := dial(ctx, "udp", c.Addr)
	if err != nil {
		return fmt.Errorf("nas: CoA dial %s: %w", c.Addr, err)
	}
	defer conn.Close()

	buf := make([]byte, 4096)
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := conn.Write(packet); err != nil {
			return fmt.Errorf("nas: CoA write: %w", err)
		}
		deadline := time.Now().Add(timeout)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return fmt.Errorf("nas: CoA deadline: %w", err)
		}
		n, err := conn.Read(buf)
		if err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				continue // retransmit
			}
			return fmt.Errorf("nas: CoA read: %w", err)
		}
		return interpretReply(buf[:n], code, identifier, reqAuth, c.Secret)
	}
	return ErrCoATimeout
}

// encodeRequest builds the packet and returns it with its Request Authenticator.
//
// Order matters and is not obvious. The Request Authenticator of a
// Disconnect-Request is MD5(code‖id‖length‖16 zero bytes‖attributes‖secret),
// and Message-Authenticator is HMAC-MD5 over the packet with its own value
// zeroed — which is circular unless the HMAC is computed while the
// Authenticator field is still zero. That is what happens here, and it is what
// FreeRADIUS does.
func encodeRequest(code, identifier uint8, secret string, s Session, extra []VendorAttribute) ([]byte, []byte, error) {
	var attrs []byte
	add := func(t uint8, v []byte) error {
		if len(v) == 0 {
			return nil
		}
		if len(v) > 253 {
			return fmt.Errorf("nas: CoA attribute %d is %d bytes, max 253", t, len(v))
		}
		attrs = append(attrs, t, uint8(len(v)+2))
		attrs = append(attrs, v...)
		return nil
	}

	if err := add(attrUserName, []byte(s.UserName)); err != nil {
		return nil, nil, err
	}
	if s.NASIPAddress.Is4() {
		ip := s.NASIPAddress.As4()
		if err := add(attrNASIPAddress, ip[:]); err != nil {
			return nil, nil, err
		}
	}
	if err := add(attrCallingStationID, []byte(s.CallingStationID)); err != nil {
		return nil, nil, err
	}
	if err := add(attrNASIdentifier, []byte(s.NASIdentifier)); err != nil {
		return nil, nil, err
	}
	if err := add(attrAcctSessionID, []byte(s.AcctSessionID)); err != nil {
		return nil, nil, err
	}
	// Vendor attributes go after the standard set and before
	// Message-Authenticator, which stays last so the HMAC covers everything.
	for _, va := range extra {
		enc, err := encodeVendorAttribute(va)
		if err != nil {
			return nil, nil, err
		}
		attrs = append(attrs, enc...)
	}
	// Message-Authenticator, zeroed for now.
	msgAuthOffset := len(attrs) + 2
	attrs = append(attrs, attrMessageAuthentica, 18)
	attrs = append(attrs, make([]byte, 16)...)

	length := 20 + len(attrs)
	packet := make([]byte, 0, length)
	packet = append(packet, code, identifier)
	packet = binary.BigEndian.AppendUint16(packet, uint16(length))
	packet = append(packet, make([]byte, 16)...) // Authenticator, zero for now
	packet = append(packet, attrs...)

	mac := hmac.New(md5.New, []byte(secret))
	mac.Write(packet)
	copy(packet[20+msgAuthOffset:20+msgAuthOffset+16], mac.Sum(nil))

	sum := md5.New()
	sum.Write(packet)
	sum.Write([]byte(secret))
	reqAuth := sum.Sum(nil)
	copy(packet[4:20], reqAuth)

	return packet, reqAuth, nil
}

// interpretReply validates the answer and turns a NAK into a typed error.
//
// `requestCode` is checked as well as the identifier, and that is not
// pedantry now that there are two request codes: a Disconnect-ACK is not an
// answer to a CoA-Request. Accepting one would mean a NAS that ends the session
// instead of changing it reports as a successful change — and the guest is
// thrown off the network while Foyer records that their limits were raised.
func interpretReply(reply []byte, requestCode, identifier uint8, reqAuth []byte, secret string) error {
	if len(reply) < 20 {
		return fmt.Errorf("nas: CoA reply is %d bytes, minimum 20", len(reply))
	}
	declared := int(binary.BigEndian.Uint16(reply[2:4]))
	if declared < 20 || declared > len(reply) {
		return fmt.Errorf("nas: CoA reply declares length %d, got %d bytes", declared, len(reply))
	}
	reply = reply[:declared]
	if reply[1] != identifier {
		return fmt.Errorf("nas: CoA reply identifier %d does not match request %d", reply[1], identifier)
	}

	// Response Authenticator = MD5(code‖id‖length‖RequestAuthenticator‖attrs‖secret).
	check := make([]byte, len(reply))
	copy(check, reply)
	copy(check[4:20], reqAuth)
	sum := md5.New()
	sum.Write(check)
	sum.Write([]byte(secret))
	if !hmac.Equal(sum.Sum(nil), reply[4:20]) {
		return ErrCoABadAuthenticator
	}

	code := reply[0]
	ack, nak := uint8(codeDisconnectACK), uint8(codeDisconnectNAK)
	if requestCode == codeCoARequest {
		ack, nak = codeCoAACK, codeCoANAK
	}
	switch code {
	case ack:
		return nil
	case nak:
		cause, ok := findErrorCause(reply[20:])
		return &CoAError{Code: code, ErrorCause: cause, HasErrorCause: ok}
	default:
		return fmt.Errorf("nas: reply code %d is not an answer to request code %d", code, requestCode)
	}
}

func findErrorCause(attrs []byte) (uint32, bool) {
	for len(attrs) >= 2 {
		t, l := attrs[0], int(attrs[1])
		if l < 2 || l > len(attrs) {
			return 0, false
		}
		if t == attrErrorCause && l == 6 {
			return binary.BigEndian.Uint32(attrs[2:6]), true
		}
		attrs = attrs[l:]
	}
	return 0, false
}
