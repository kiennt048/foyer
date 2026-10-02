// Package sms delivers a phone sign-in code to a handset.
//
// It is the provider seam behind `internal/identity/otp`, and it knows nothing
// about codes, limits or sessions: it is handed a number and a text and it either
// gets them to a provider or says why not.
//
// ── which provider, and why one ──────────────────────────────────────────────
//
// Surveyed before choosing: eSMS.vn, SpeedSMS, VietGuys and the
// carriers' own brandname gateways (Viettel, VNPT). All four sell the same
// regulated product — an SMS from a **registered brandname**, whose text must
// match a template the carrier pre-approved — and all four are plain HTTPS+JSON
// with a key pair. eSMS is the one adapter here because it is the only one whose
// published API also sends **Zalo ZNS** with the same credentials
// (`SendZaloMessage_V5_post`), which is the natural second channel in Vietnam,
// and because its `RequestId` field de-duplicates a retried request for 24 hours,
// which is the property a send that may be retried after a timeout needs.
// SpeedSMS (`/index.php/sms/send`, token as the basic-auth user) would be a second
// adapter of about eighty lines behind the same interface.
//
// **Everything here is UNVERIFIED against the live service.** It is written from
// eSMS's published documentation and driven in tests against an `httptest` fake of
// it; no message has been sent through a real account from this repository. A
// send through a real account to a real phone is what would make it MEASURED.
//
// ── what is never in an error or a log line ──────────────────────────────────
//
// The number, the text (which contains the code) and the keys. An error names the
// provider and its result code and nothing else, because errors end up in the
// process log and `docs/rules/security.md` §3 and §9 keep credentials and codes
// out of it. A phone number is personal data under Luật 91/2025/QH15 and has no
// business in an operator's log either.
package sms

import (
	"context"
	"errors"
	"strings"
)

// Message is one code on its way to one handset.
type Message struct {
	// To is E.164 — `+84912345678`. The adapter converts it to whatever the
	// provider wants on the wire.
	To string
	// Code is the six digits, for a channel that takes it as a template parameter
	// (Zalo ZNS). Never logged.
	Code string
	// Text is the rendered message, for a channel that sends free text inside a
	// registered template (SMS brandname). Never logged.
	Text string
	// RequestID is unique per code. A provider that de-duplicates on it makes a
	// retried send idempotent rather than a second charge.
	RequestID string
}

// Provider sends a message or says why not.
type Provider interface {
	Send(ctx context.Context, m Message) error
}

// The two ways a send fails, and the split is the one `portal.FailureKind` draws:
// a provider that **answered and refused** has an opinion (wrong keys, template not
// registered, balance exhausted) and asking again repeats it; one that **could not
// be reached** has none yet.
var (
	ErrRefused     = errors.New("sms: the provider refused the message")
	ErrUnreachable = errors.New("sms: the provider could not be reached")
)

// Refusal is a provider that answered and said no, with its result code — at most
// eight alphanumerics, never the provider's free text. It is ErrRefused under
// `errors.Is`. The code is what the console's test message shows an operator
// (`sendPhoneTestMessage.providerCode`), because *eSMS said 99* is what the
// provider's own support desk asks for.
type Refusal struct {
	Provider string
	Code     string
}

func (r *Refusal) Error() string {
	return ErrRefused.Error() + ": " + r.Provider + " CodeResult \"" + r.Code + "\""
}

func (r *Refusal) Unwrap() error { return ErrRefused }

// RefusalCode is the provider's result code when err is a Refusal, or "".
func RefusalCode(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

// Kind names the adapters, matching the contract's `PhoneSignInProvider`.
const (
	KindESMSSMS  = "esms-sms"
	KindESMSZalo = "esms-zalo"
)

// Config is what an adapter needs, already unsealed. It lives only as long as one
// send and is never logged — `String` exists so an accidental `%v` does not print
// the keys.
type Config struct {
	Kind           string
	APIKey         string
	SecretKey      string
	Brandname      string
	ZaloOAID       string
	ZaloTemplateID string
	Sandbox        bool
	// BaseURL overrides the provider's address. Tests point it at a fake; nothing
	// in production sets it.
	BaseURL string
}

// String redacts. A `Config` in a format string must not become a credential in a
// log line.
func (c Config) String() string {
	return "sms.Config{Kind:" + c.Kind + " keys:[redacted]}"
}

// ErrUnknownKind is a configuration nobody can send through.
var ErrUnknownKind = errors.New("sms: unknown provider")

// New builds the adapter a configuration names.
func New(c Config) (Provider, error) {
	switch c.Kind {
	case KindESMSSMS:
		if c.Brandname == "" || c.APIKey == "" || c.SecretKey == "" {
			return nil, errors.New("sms: esms-sms needs a brandname and both keys")
		}
		return &ESMS{cfg: c}, nil
	case KindESMSZalo:
		if c.ZaloOAID == "" || c.ZaloTemplateID == "" || c.APIKey == "" || c.SecretKey == "" {
			return nil, errors.New("sms: esms-zalo needs an OA id, a template id and both keys")
		}
		return &ESMSZalo{cfg: c}, nil
	default:
		return nil, ErrUnknownKind
	}
}

// nationalVN turns `+84912345678` into `0912345678`, the form eSMS's own examples
// use. A number that is not Vietnamese is passed through without its `+`; the otp
// package refuses those long before they get here.
func nationalVN(e164 string) string {
	if rest, ok := strings.CutPrefix(e164, "+84"); ok {
		return "0" + rest
	}
	return strings.TrimPrefix(e164, "+")
}
