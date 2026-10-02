package portal

import (
	"net/url"
	"slices"
)

// The machine-readable failure a `303` carries, and the pages it carries it to.
//
// ── why the server sends a code and not a sentence ───────────────────────────
//
// The portal's success path is a real document navigation — form POST, `303`,
// new document — because the Captive Network Assistant re-checks captivity only
// after one. That is not negotiable, and it makes the *failure* path a new
// document too, with no client state to hold anything. The CNA's data store is
// ephemeral and the flow must work with JavaScript disabled, so there is nowhere
// for a message to live except the URL.
//
// It is a code rather than the text because the server does not know the
// reader's language at the moment it refuses, and composing a sentence in the
// server's language is how a console ended up showing Vietnamese to an English
// speaker. `Problem.type` settles the same argument the same way for the API.

// Reason is one of the failures the portal's own message catalogue has copy for.
//
// **The set is not free.** Every value here must have an `error.<value>` key in
// `web/portal/src/i18n/en.ts`, and `reason_test.go` reads that file and requires
// it — a reason with no copy renders as `error.generic`, which tells a guest
// standing in a café "Something went wrong. Try again." about a voucher that was
// cancelled an hour ago.
type Reason string

const (
	// The four voucher outcomes. Separate because the instruction differs:
	// "check each character" against "ask staff for another one".
	ReasonNotFound    Reason = "not-found"
	ReasonAlreadyUsed Reason = "already-used"
	ReasonExpired     Reason = "expired"
	ReasonRevoked     Reason = "revoked"

	// ReasonShape is refused before anything is looked up: a submission that is
	// not twelve characters of the voucher alphabet cannot be a voucher, and
	// answering it without a store lookup is both faster and one fewer chance
	// for a lookup to leak timing.
	ReasonShape Reason = "shape"

	// ReasonNotPresent is the guest's own situation: the driver says that MAC is
	// not associated here.
	//
	// **It is the narrow claim and must stay narrow.** It says the device is not
	// on this network, and it sends the guest to reconnect — which is right when
	// that is true and a loop when it is not. `ReasonAPUnconfirmed` exists
	// because a device under the wrong access point *is* on the network.
	ReasonNotPresent Reason = "not-present"

	// ReasonAPUnconfirmed is the guest who is on this network at an access point
	// the venue could not confirm.
	//
	// **It is not "the device is not here" and it is not "the venue is broken",**
	// which is why it is neither of the two reasons that already existed. The
	// device is associated — the controller returned a client for that MAC — but
	// the access point the redirect named is not one the controller will vouch
	// for, so `security.md` §4's fourth clause is unmet and no grant may follow.
	//
	// **Two situations reach it and the copy has to be honest about both**, which
	// is what "unconfirmed" buys over "mismatch". `internal/nas` collapses them
	// into one `PresenceAPMismatch`:
	//
	//   - **checked and contradicted** — the controller resolved the client's
	//     uplink to a different access point. In practice a stale redirect, a
	//     device that roamed while the page was open, or a forged `ap` parameter.
	//     Reconnecting fixes the first two and nothing helps the third.
	//   - **not checked, and the venue requires it** — nothing resolved the
	//     claim and `UniFiConfig.RequireAPMatch` is set. Reconnecting never fixes
	//     this: it is the venue's policy meeting a controller that does not carry
	//     `uplinkDeviceId`, and on such a deployment *every* guest lands here.
	//
	// So the copy names the cheap action first and escalation second, rather
	// than sending a whole venue to toggle Wi-Fi — the same judgement
	// `VerdictUnavailable` already makes by answering `generic`.
	ReasonAPUnconfirmed Reason = "ap-unconfirmed"

	ReasonBadCredentials Reason = "bad-credentials"
	ReasonDisabled       Reason = "disabled"

	// ReasonGeneric is what the venue's own failures answer with — the
	// controller unreachable, the rate limit reached, a store that errored.
	//
	// It is deliberately the same code the guest would see for any other
	// internal fault, because the alternative is telling somebody within radio
	// range which of the venue's systems is down.
	ReasonGeneric Reason = "generic"

	// ReasonDetailsRequired: the venue marked a detail required to get online and
	// the guest left it blank. ReasonDetailsInvalid: a detail is not the shape it
	// claims — a phone number with letters in it, an e-mail address with no `@`.
	// Both are answered before anything is claimed, and neither carries the value
	// back: a phone number in a redirect is a phone number in a proxy log.
	ReasonDetailsRequired Reason = "details-required"
	ReasonDetailsInvalid  Reason = "details-invalid"
)

// Reasons is every value, for a test that must not be able to fall behind the
// constants.
func Reasons() []Reason {
	return []Reason{
		ReasonNotFound, ReasonAlreadyUsed, ReasonExpired, ReasonRevoked,
		ReasonShape, ReasonNotPresent, ReasonAPUnconfirmed,
		ReasonBadCredentials, ReasonDisabled, ReasonGeneric,
		ReasonPhoneInvalid, ReasonCodeWrong, ReasonCodeExpired, ReasonCodeLimit, ReasonCodeUnavailable,
		ReasonDetailsRequired, ReasonDetailsInvalid,
	}
}

// NoticeKind is a success a `303` carries, as `?n=`: the one outcome on this surface
// that is neither a sign-in nor a failure. Like Reason, every value has copy —
// `notice.<value>` in `web/portal/src/i18n/en.ts`.
type NoticeKind string

// NoticeConsentWithdrawn: the guest withdrew their marketing consent from the
// portal. Sent whether or not there was one to withdraw — see
// `withdrawOwnMarketingConsent`.
const NoticeConsentWithdrawn NoticeKind = "consent-withdrawn"

// Notices is every value, for the same test Reasons serves.
func Notices() []NoticeKind { return []NoticeKind{NoticeConsentWithdrawn} }

// Notice is where a guest is sent with a success to read.
func Notice(page Page, lang string, n NoticeKind) string {
	q := url.Values{}
	q.Set("n", string(n))
	return "/" + Language(lang) + "/" + string(page) + "?" + q.Encode()
}

// Languages are the two the portal ships. `web/contract`'s `Language` enum is
// the source and English is its default.
var languages = []string{"en", "vi"}

// Language allowlists a submitted language code.
//
// **This is a security boundary and not a nicety.** The value lands in a
// `Location` header, so an unvalidated one is a header the guest's browser
// follows: `lang=..%2f..%2fevil.example` produces a path that leaves the portal,
// and a value containing a newline produces two headers. An allowlist of two
// strings cannot do either, which is why this is an allowlist and not an escape.
func Language(submitted string) string {
	if slices.Contains(languages, submitted) {
		return submitted
	}
	return "en"
}

// Page names a document the portal serves, under the language prefix. The set
// matches `web/portal/src/pages` and a value outside it is a link to nothing.
type Page string

const (
	PageWelcome   Page = ""
	PageVoucher   Page = "voucher/"
	PageAccount   Page = "account/"
	PageConnected Page = "connected/"
	PageProblem   Page = "problem/"
)

// Connected is where a guest who is now online is sent.
func Connected(lang string) string {
	return "/" + Language(lang) + "/" + string(PageConnected)
}

// Failure is where a guest who is not online is sent, with the reason and — for
// a voucher — the code they submitted.
//
// **The code comes back, and it comes back normalised.** A guest who mistyped one
// of twelve characters one-handed, on a phone, in a mini-browser, would otherwise
// retype all twelve; there is no client state to hold it and the flow must
// survive JavaScript being off, so the server carries it. Normalisation is what
// makes that safe rather than merely convenient: `NormaliseCode` keeps only the
// voucher alphabet, so the value the portal renders back into an input is
// `[A-Z0-9]` by construction and a hand-written link cannot use this parameter to
// put arbitrary text on a page a stranger is reading.
//
// A password is never echoed and neither is a username. The first is obvious;
// the second is not, and it is the rule `AuthAttempt` carries for the same
// reason — a username in a URL is a username in a proxy log.
// **The echo is gated on the page, not on the caller.** `PageVoucher` is the only
// document with a field this may be rendered into, so a caller that passed a
// username here would get it dropped rather than published — the rule is enforced
// where it can be checked once, instead of at three call sites where it would
// have to be got right three times.
func Failure(page Page, lang string, r Reason, submittedCode string) string {
	q := url.Values{}
	q.Set("e", string(r))
	if page == PageVoucher {
		if echo := echoCode(submittedCode); echo != "" {
			q.Set("code", echo)
		}
	}
	return "/" + Language(lang) + "/" + string(page) + "?" + q.Encode()
}

// echoCode bounds what may travel back. The cap is generous enough that an
// over-long entry still reads as over-long and short enough that the URL cannot
// be used as a payload carrier.
func echoCode(submitted string) string {
	clean := NormaliseCode(submitted)
	if len(clean) > 24 {
		clean = clean[:24]
	}
	return clean
}
