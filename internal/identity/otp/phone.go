package otp

import (
	"strings"
)

// MobilePrefixes are the ranges of the Vietnamese mobile plan, in the national
// form a venue reads them in. The allowlist a venue sets is a subset of these.
var MobilePrefixes = []string{"03", "05", "07", "08", "09"}

// ValidPrefix reports whether p is one of MobilePrefixes.
func ValidPrefix(p string) bool {
	for _, m := range MobilePrefixes {
		if p == m {
			return true
		}
	}
	return false
}

// InRanges reports whether an E.164 number from NormaliseVN falls in one of the
// venue's open ranges. Empty ranges are all of MobilePrefixes: a venue that never
// narrowed the list has the whole plan, which is the default §9 asks for.
func InRanges(e164 string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	national, ok := strings.CutPrefix(e164, "+84")
	if !ok || national == "" {
		return false
	}
	for _, p := range prefixes {
		if "0"+national[:1] == p {
			return true
		}
	}
	return false
}

// NormaliseVN turns what a guest typed into an E.164 Vietnamese **mobile** number,
// or refuses it.
//
// ── the destination allowlist, and why it is this narrow ─────────────────────
//
// `docs/rules/security.md` §9: the top risk on an OTP endpoint is not brute
// force, it is SMS toll fraud — attackers submit premium-rate numbers in volume and
// take a cut of the termination fee — and the rule asks for "a destination-country
// allowlist defaulting to the install's own country". Every industry write-up of
// SMS pumping (Twilio, Authgear, Prelude) names geographic
// permission as the single cheapest control, because the fraud lives on
// international and premium ranges.
//
// So this accepts **Vietnamese mobile ranges only**: `+84` followed by nine digits
// whose first is 3, 5, 7, 8 or 9 — the post-2018 ten-digit mobile plan (03x, 05x,
// 07x, 08x, 09x). Landlines (02x) cannot receive an SMS and are refused here rather
// than paid for; everything international is refused. There is no second country
// yet because there is no second market yet, and adding one is a list edit here
// plus the contract's `PhoneSignIn.country` enum.
//
// Accepted spellings, because a guest types what is printed on their SIM card:
// `0912345678`, `0912 345 678`, `091.234.5678`, `+84912345678`, `84912345678`,
// `0084912345678`, and `+84 (0) 912 345 678`.
func NormaliseVN(raw string) (string, error) {
	var b strings.Builder
	plus := false
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			plus = true
		case r == ' ' || r == '.' || r == '-' || r == '(' || r == ')' || r == ' ':
		default:
			return "", ErrPhoneInvalid
		}
		if b.Len() > 16 {
			return "", ErrPhoneInvalid
		}
	}
	d := b.String()
	var national string
	switch {
	case plus && strings.HasPrefix(d, "84"):
		national = strings.TrimPrefix(d[2:], "0") // `+84 (0) 912…`
	case plus:
		return "", ErrPhoneInvalid // another country
	case strings.HasPrefix(d, "0084"):
		national = d[4:]
	case strings.HasPrefix(d, "84") && len(d) == 11:
		national = d[2:]
	case strings.HasPrefix(d, "0") && len(d) == 10:
		national = d[1:]
	default:
		return "", ErrPhoneInvalid
	}
	if len(national) != 9 {
		return "", ErrPhoneInvalid
	}
	switch national[0] {
	case '3', '5', '7', '8', '9':
	default:
		return "", ErrPhoneInvalid
	}
	return "+84" + national, nil
}
