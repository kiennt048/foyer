package portal

import (
	"net/url"
	"strings"
)

// The open-redirect defence, and it is the one place in the portal where the
// threat is not theoretical.
//
// The NAS hands the portal the URL the client was originally going to, unsigned,
// and the portal shows it back. `docs/rules/security.md` §4:
//
//	the victim is on an untrusted network, *expects* to be redirected, and has
//	just typed credentials.
//
// Every part of that sentence is doing work. On any other site a redirect to a
// look-alike host is suspicious; here it is the advertised behaviour of the
// product, immediately after the guest entered something into a form.
//
// Hence two rules and not one:
//
//  1. **Allowlist the scheme, and reject the shapes that are not schemes.**
//     `javascript:` and `data:` are the obvious ones. `//evil.example` is not a
//     scheme at all — it is protocol-relative and inherits the portal's, so a
//     check that only looked at `u.Scheme` passes it. And
//     `http://portal.example@evil.example` *is* `http`, with a host of
//     `evil.example` and a visible prefix that reads as the destination to
//     exactly the guest this is aimed at.
//  2. **Render it as a link the guest chooses to follow, never an automatic
//     bounce.** That is `internal/api`'s side of the rule and it is why
//     `PortalStatus.originalUrl` is a field rather than a `Location`.
//
// The contract states the same allowlist a third time, as a `pattern` on
// `PortalStatus.originalUrl`, and the duplication is deliberate: a regex is a
// shape test, `url.Parse` is a parser, and the browser uses a parser. Neither
// replaces the other.

// SafeReturnURL allowlists a NAS-supplied destination.
//
// It returns the empty string for anything it will not vouch for, and the caller
// renders no link at all rather than a link somewhere else. Silence is the right
// failure here: a guest who does not get their original page back is mildly
// inconvenienced, and a guest who gets somebody else's has been phished on a
// network the venue vouched for.
func SafeReturnURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// A control character anywhere is a request to split a header or to break a
	// renderer, never a URL. Refused before parsing, because `url.Parse`
	// tolerates some of them.
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	if len(raw) > 2048 {
		// The contract caps it at 2048. A longer value is not a destination
		// anybody typed.
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// Scheme allowlist. `u.Scheme` is lower-cased by the parser.
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	// Protocol-relative. `//evil.example` parses with an empty scheme and is
	// caught above; this catches the case where a scheme was supplied and the
	// authority is still missing, which is not a destination.
	if u.Host == "" {
		return ""
	}
	// Userinfo. `http://portal.example@evil.example` — the half a guest reads is
	// the half that is not the host.
	if u.User != nil {
		return ""
	}
	// A host that still contains an `@` after parsing, or whitespace, is a shape
	// the contract's own pattern rejects and is not something a NAS produces.
	if strings.ContainsAny(u.Host, "@ \t") {
		return ""
	}
	return u.String()
}
