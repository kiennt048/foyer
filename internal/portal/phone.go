package portal

import (
	"context"
	"errors"
	"net/url"
)

// Phone sign-in: a code sent to a number, typed back, and a grant.
//
// It is the fourth method and it is built to the shape of the other three rather
// than beside them. The code and its abuse controls belong to
// `internal/identity/otp`, behind the `PhoneCodes` port; **what this file decides
// is the order**, and the order is the security property:
//
//	send:   rate limit → VerifyPresence → the number, the limits, the ceiling → send
//	verify: rate limit → VerifyPresence → the code → grant → 303 → Authorize
//
// **Presence before the send is the part the field does not do.** PacketFence,
// UniFi and Omada all send the code first and check the device afterwards, at
// grant time. Here the controller is asked before a message is paid for, because
// the top risk on this endpoint is SMS toll fraud (`docs/rules/security.md`
// §9) and a device the venue's controller cannot place on the network has no
// business spending the venue's SMS budget. The cost is one controller call per
// send, and the send is already bounded far below the rate the controller is
// bounded at.
//
// The number never travels back to the browser, not even masked. It is bound to
// this browser's portal session server-side, and the redirect carries only
// `?sent=1` — a phone number in a `Location` header is a phone number in every
// proxy log between the handset and the portal.

// MethodPhone is a guest who verified a code sent to their number.
const MethodPhone Method = "phone"

// PagePhone is the phone sign-in document: the number form, or with `?sent=1` the
// code form.
const PagePhone Page = "phone/"

// The phone sign-in failures, each with copy in `web/portal/src/i18n`.
const (
	// ReasonPhoneInvalid — not a Vietnamese mobile number, the one destination
	// range codes are sent to.
	ReasonPhoneInvalid Reason = "phone-invalid"
	// ReasonCodeWrong — the code does not match. The code form stays open.
	ReasonCodeWrong Reason = "code-wrong"
	// ReasonCodeExpired — no live code for this browser: expired, used, replaced
	// or out of attempts. The guest's next step is the same for all of them.
	ReasonCodeExpired Reason = "code-expired"
	// ReasonCodeLimit — too many codes to this number, device or address.
	ReasonCodeLimit Reason = "code-limit"
	// ReasonCodeUnavailable — the venue cannot send right now: the daily ceiling
	// is reached, or the provider refused or did not answer. One code for both,
	// for the reason `ReasonGeneric` gives: which of the venue's systems failed is
	// not the business of somebody within radio range. The copy sends the guest
	// to another method rather than to try again.
	ReasonCodeUnavailable Reason = "code-unavailable"
)

// PhoneCodes is the port `internal/identity/otp` sits behind. Its errors are this
// package's, so the adapter maps them and this package imports nothing new.
type PhoneCodes interface {
	// Offered reports whether the site can send codes and how long a phone guest
	// is granted, in minutes.
	Offered(ctx context.Context, siteID string) (minutes int, ok bool)
	// Send issues and sends a code for this browser. The caller has run
	// `VerifyPresence`.
	Send(ctx context.Context, req PhoneCodeRequest) error
	// Check spends one attempt on this browser's live code, answering with the
	// number it was sent to.
	Check(ctx context.Context, sessionTokenHash, code string) (phone string, err error)
}

// PhoneCodeRequest is one guest asking for a code.
type PhoneCodeRequest struct {
	SiteID           string
	SessionTokenHash string
	ClientMAC        string
	ClientIP         string
	Phone            string
}

// The errors PhoneCodes returns.
var (
	ErrPhoneNotOffered = errors.New("portal: phone sign-in is not offered here")
	ErrPhoneInvalid    = errors.New("portal: not a number codes can be sent to")
	ErrPhoneLimited    = errors.New("portal: too many codes")
	ErrPhoneCannotSend = errors.New("portal: codes cannot be sent right now")
	ErrCodeWrong       = errors.New("portal: wrong code")
	ErrCodeExpired     = errors.New("portal: no live code")
)

// PhoneSend is what asking for a code decided.
type PhoneSend struct {
	// NotOffered answers `404`, like `acceptTerms` at a venue without it.
	NotOffered bool
	// Sent: the code is on its way; the guest goes to the code form.
	Sent bool
	// AlreadyOn: the driver says this device is already authorized. No code is
	// sent — spending the venue's money on a guest who is online is the phone
	// version of burning a voucher for them.
	AlreadyOn bool
	// Reason is why not, when none of the above.
	Reason Reason
}

// SendPhoneCode asks for a code to be sent to the number a guest typed.
func (s *Service) SendPhoneCode(ctx context.Context, sess Session, phone, clientIP string) PhoneSend {
	codes := s.ports.PhoneCodes
	if codes == nil {
		return PhoneSend{NotOffered: true}
	}
	if _, ok := codes.Offered(ctx, sess.SiteID); !ok {
		return PhoneSend{NotOffered: true}
	}
	if !s.allowSource("auth", sess) {
		return PhoneSend{Reason: ReasonCodeLimit}
	}
	switch v, why := s.presence(ctx, sess); {
	case v == VerdictAlreadyOn:
		return PhoneSend{AlreadyOn: true}
	case !v.MayGrant():
		return PhoneSend{Reason: why}
	}
	if !s.allowDevice("auth", sess) {
		return PhoneSend{Reason: ReasonCodeLimit}
	}
	err := codes.Send(ctx, PhoneCodeRequest{
		SiteID: sess.SiteID, SessionTokenHash: sess.TokenHash,
		ClientMAC: sess.ClientMAC, ClientIP: clientIP, Phone: phone,
	})
	switch {
	case err == nil:
		return PhoneSend{Sent: true}
	case errors.Is(err, ErrPhoneNotOffered):
		return PhoneSend{NotOffered: true}
	case errors.Is(err, ErrPhoneInvalid):
		return PhoneSend{Reason: ReasonPhoneInvalid}
	case errors.Is(err, ErrPhoneLimited):
		return PhoneSend{Reason: ReasonCodeLimit}
	default:
		return PhoneSend{Reason: ReasonCodeUnavailable}
	}
}

// PhoneLogin checks the code and grants. `NotOffered` answers `404`.
func (s *Service) PhoneLogin(ctx context.Context, sess Session, code string) Outcome {
	codes := s.ports.PhoneCodes
	if codes == nil {
		return notOffered()
	}
	minutes, ok := codes.Offered(ctx, sess.SiteID)
	if !ok {
		return notOffered()
	}
	if !s.allowSource("auth", sess) {
		return refuse(ReasonGeneric)
	}
	// Presence before the code is spent, for the reason a voucher is not claimed
	// for a device that is not on the network.
	switch v, why := s.presence(ctx, sess); {
	case v == VerdictAlreadyOn:
		return alreadyOn()
	case !v.MayGrant():
		return refuse(why)
	}
	// Only now, with the controller's word that this MAC is on the network, does
	// it get a budget of its own (see KeyDevice).
	if !s.allowDevice("auth", sess) {
		return refuse(ReasonGeneric)
	}

	phone, err := codes.Check(ctx, sess.TokenHash, code)
	if err != nil {
		switch {
		case errors.Is(err, ErrCodeWrong):
			return refuse(ReasonCodeWrong)
		case errors.Is(err, ErrCodeExpired):
			return refuse(ReasonCodeExpired)
		default:
			return refuse(ReasonGeneric)
		}
	}

	now := s.now()
	plan := Plan{DurationMinutes: &minutes}
	g := Grant{
		SiteID:    sess.SiteID,
		ClientMAC: sess.ClientMAC,
		APMAC:     sess.APMAC,
		NASID:     sess.NASID,
		Method:    MethodPhone,
		Phone:     phone,
		Plan:      plan,
		GrantedAt: now,
		ExpiresAt: expiry(now, plan, nil),
	}
	return s.issue(ctx, sess, g)
}

// PhoneSentPage is where a guest goes when a code is on its way.
func PhoneSentPage(lang string) string {
	return "/" + Language(lang) + "/" + string(PagePhone) + "?sent=1"
}

// PhoneFailure is where a phone guest who is not online is sent. A wrong code keeps
// the code form open; everything else returns to the number form, because the next
// step is a new code.
func PhoneFailure(lang string, r Reason) string {
	q := url.Values{}
	q.Set("e", string(r))
	if r == ReasonCodeWrong {
		q.Set("sent", "1")
	}
	return "/" + Language(lang) + "/" + string(PagePhone) + "?" + q.Encode()
}

// PhoneDetailsFailure is where a phone guest whose details were refused goes: back
// to the code form, because the code was not spent — the details are read first.
func PhoneDetailsFailure(lang string, r Reason) string {
	q := url.Values{}
	q.Set("e", string(r))
	q.Set("sent", "1")
	return "/" + Language(lang) + "/" + string(PagePhone) + "?" + q.Encode()
}

// identityOf is what a grant was presented with, for the operator's record.
func identityOf(g Grant) string {
	if g.Method == MethodPhone {
		return g.Phone
	}
	return g.Username
}
