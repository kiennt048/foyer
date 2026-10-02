package aaa

import (
	"strings"
	"unicode/utf8"

	"github.com/kiennt048/foyer/internal/identity"
)

// Inner identity only.
//
// `docs/rules/security.md` §8: in PEAP and TTLS the outer identity is
// client-supplied and arbitrary. A supplicant sends `anonymous@example.com` on the
// outside of the tunnel and its real name on the inside, and the outer value never
// gets checked against anything -- that is the point of it. Authorization that
// reads the outer identity therefore lets an attacker choose their own
// authorization subject: send `anonymous@`, get whatever `anonymous@` is allowed.
//
// This file exists so that there is exactly one function that answers "who is this
// request about", and so the answer cannot be reached through the outer field by
// accident.

// AuthorizationSubject is the only identity authorization may key on.
//
// It returns the inner identity and nothing else. OuterUserName is carried on the
// Request for logging and for the anonymity check below, and is unreachable from
// here on purpose: a call site that wanted the outer identity would have to say so
// in its own code rather than get it by writing the shorter expression.
//
// **It is the attribute's own string, untrimmed.** `strings.TrimSpace` would be
// wrong here: Go's TrimSpace trims Unicode White_Space, not ASCII blanks, so a
// trailing NBSP (U+00A0), NEL (U+0085) or U+2028 would be removed and the
// remainder would match a real voucher. An account is compared as the NAS sent
// it -- which is also how the generated tree's own rlm_sql lookup of the PAP
// verifier compares it, so the account the Policy API authorizes and the one
// whose password rlm_pap checks cannot differ by whitespace. A voucher is the one identity with a typed
// form, and it is normalised on its own terms by voucherKey below, not by
// trimming the subject.
func (r Request) AuthorizationSubject() string {
	return r.UserName
}

// voucherLength is the stored voucher: twelve characters from
// identity.UnambiguousCharacterSet, which is also the vouchers table's CHECK
// (`^[A-HJ-KM-NP-Z2-9]{12}$`, migration 00005).
const voucherLength = 12

// voucherKey is the stored form of a voucher typed at a NAS login page, and
// whether subject is one at all.
//
// A MikroTik hotspot page, or any captive page that sends the code as User-Name,
// passes on what the guest typed: `abcd-efgh-jkmn` or `ABCD EFGH JKMN` for a code
// printed as `ABCD-EFGH-JKMN`. A lookup of `WHERE code = $1` on that string would
// refuse every printed spelling and every lowercase one over RADIUS while the
// portal accepted it. The
// portal's rule (`internal/portal.NormaliseCode`) is: any casing, any spacing,
// dashes or none. This is that rule, and deliberately no wider:
//
//   - ASCII letters are upper-cased; ASCII `-` and ASCII space are separators
//     and are dropped wherever they fall, as the portal drops them.
//   - **Anything else refuses the voucher reading entirely** -- NBSP, NEL, a
//     full-width letter, a letter whose upper case happens to be ASCII (`ı` is
//     `I` and `ſ` is `S` under Go's ToUpper). The portal drops those too,
//     because a browser paste is its input and a guest cannot see them; a RADIUS
//     User-Name is also the name of an account, and there an invisible character
//     is a way to make the question mean something it does not, not a typing
//     habit. Refusing the reading does not
//     refuse the request: the subject is then looked up as itself, exactly.
//   - The result must be twelve characters of the voucher alphabet, or it is not
//     a voucher and the subject is looked up unchanged -- which is also what
//     keeps a non-code-shaped voucher in a test fixture reachable.
func voucherKey(subject string) (string, bool) {
	var b strings.Builder
	b.Grow(voucherLength)
	for i := 0; i < len(subject); i++ {
		c := subject[i]
		switch {
		case c == '-' || c == ' ':
			continue
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			return "", false
		}
		if strings.IndexByte(identity.UnambiguousCharacterSet, c) < 0 {
			return "", false
		}
		b.WriteByte(c)
	}
	if b.Len() != voucherLength {
		return "", false
	}
	return b.String(), true
}

// anonymousLocalParts are the placeholder local-parts a supplicant is expected to
// present outside the tunnel. They are not a blocklist of bad names: they are the
// values RFC 7542 §2.1 and every supplicant vendor use to mean "I am not telling
// you yet".
var anonymousLocalParts = map[string]bool{
	"":          true,
	"anonymous": true,
	"anon":      true,
}

// IsAnonymousIdentity reports whether an identity is a tunnel placeholder rather
// than a name anything can be authorized against.
//
// `anonymous`, `anonymous@realm`, `@realm` and the empty string. Case-insensitive,
// because the supplicant chooses the case and nothing normalises it on the way in.
func IsAnonymousIdentity(id string) bool {
	id = strings.TrimSpace(id)
	local := id
	if at := strings.LastIndex(id, "@"); at >= 0 {
		local = id[:at]
	}
	return anonymousLocalParts[strings.ToLower(local)]
}

// Realm returns the part after the last `@`, or "" when there is none.
//
// Last rather than first: RFC 7542 §2.2 allows `@` in the utf8-username, and a
// realm split on the first one hands an attacker control of the realm by putting
// one in their name.
func Realm(id string) string {
	if at := strings.LastIndex(strings.TrimSpace(id), "@"); at >= 0 {
		return strings.TrimSpace(id)[at+1:]
	}
	return ""
}

// identityFault describes why a request has no identity worth looking up.
//
// It is returned as a rejection reason rather than an error: "this request cannot
// be authorized" is a decision, not a failure of the Policy API, and the two must
// answer with different HTTP statuses -- a failure reaches the generated config's
// failure branch, and a rejection must not.
func identityFault(r Request) string {
	subject := r.AuthorizationSubject()
	if subject == "" {
		return "no inner identity in request"
	}
	if IsAnonymousIdentity(subject) {
		// The inner identity is the tunnel placeholder. Either the tunnel never
		// reached an inner identity, or the generated `rest` module is sending
		// `%{User-Name}` from the outer session -- and both are the same failure
		// from here: a lookup on a value the client chose freely.
		//
		// Rejecting is the narrow answer. Looking it up is not, and would make a
		// voucher or account that happened to be called `anonymous` reachable by
		// anybody who has not authenticated at all.
		return "anonymous identity reached the authorization path"
	}
	if hasUnprintable(r.UserName) || hasUnprintable(r.OuterUserName) {
		// A C0 control, DEL or a replacement character is not part of any
		// identity this system issues -- voucher codes are twelve letters and
		// digits, and an account name is typed into a form. It arrives only from a
		// client building a question on purpose, and it is refused as the
		// decision it is: a refusal, never an error, because an error would reach
		// the failure branch and a fail-open site would let it on.
		return "identity carries characters no identity holds"
	}
	return ""
}

// hasUnprintable reports a C0 control, DEL, a C1 control, or U+FFFD -- the last
// being what invalid UTF-8 becomes if it ever gets past the handler's own refusal.
//
// C1 (U+0080..U+009F) matters as much as C0, and NEL (U+0085) is one: Go's
// TrimSpace counts it as white space, so a voucher followed by NEL could be
// trimmed back to the voucher and authorized. It is a
// control character in every sense this function means, and no identity Foyer
// issues contains one.
func hasUnprintable(s string) bool {
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == utf8.RuneError {
			return true
		}
	}
	return false
}
