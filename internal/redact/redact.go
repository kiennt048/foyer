// Package redact is how a guest's identifiers appear in a process log line.
//
// ── why a log line gets a masked form and not the identifier ─────────────────
//
// A container log is personal data the product cannot erase: `eraseDataSubject`
// reaches every table `docs/operator/personal-data.md` lists and no log line, and
// the log is shipped into every support bundle made from it. GDPR Article 5(1)(c)
// (data minimisation) and Article 25 (data protection by default) put the fix at
// the point of writing, and OWASP's Logging Cheat Sheet lists personal data among
// what a log should exclude or de-identify. So a line that needs to name a device
// names it the way the audit log already names an erased subject
// (`store.Subject.Masked`): enough to tell two lines apart and to match one to a
// guest standing at the desk, not enough to be the identifier.
//
// ── the forms, and where each came from ──────────────────────────────────────
//
//   - **MAC: the last two octets** (`••:••:••:••:11:22`), the audit log's own
//     form. The first three octets are the vendor or, on a phone, the random
//     half of a private address; neither identifies a guest to an operator, and the
//     tail is what an operator reads back off a phone's Wi-Fi details.
//   - **Phone number: the last three digits** (`•••••••678`), what a bank or an SMS
//     provider's console shows.
//   - **IP address: the network, not the host.** IPv4 keeps the /24 and IPv6 the
//     /48 — Google Analytics' `anonymizeIp` truncation, the one regulators have
//     been shown most often.
//
// PacketFence logs full MACs; it predates the Vietnamese law (91/2025/QH15) and
// GDPR-era defaults, and its own admin guide sends operators to logrotate for the
// consequence. Foyer rotates too (deploy/compose.yaml) and writes less first.
package redact

import (
	"io"
	"log"
	"net/netip"
	"os"
	"regexp"
	"strings"
)

// dot is the mask character, the one the audit log already uses.
const dot = "•"

// MAC is a MAC address in any vendor spelling, masked to its last two octets.
// Empty is empty; a value that is not twelve hex digits is not echoed at all.
func MAC(raw string) string {
	if raw == "" {
		return ""
	}
	var hex []byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			hex = append(hex, c)
		case c >= 'A' && c <= 'F':
			hex = append(hex, c+'a'-'A')
		case c == ':' || c == '-' || c == '.':
		default:
			return "(not a MAC)"
		}
	}
	if len(hex) != 12 {
		return "(not a MAC)"
	}
	return strings.Repeat(dot+dot+":", 4) + string(hex[8:10]) + ":" + string(hex[10:12])
}

// Phone is a phone number masked to its last three digits.
func Phone(raw string) string {
	var digits []byte
	for i := 0; i < len(raw); i++ {
		if raw[i] >= '0' && raw[i] <= '9' {
			digits = append(digits, raw[i])
		}
	}
	if len(digits) < 4 {
		return strings.Repeat(dot, len(digits))
	}
	return strings.Repeat(dot, len(digits)-3) + string(digits[len(digits)-3:])
}

// Email is an e-mail address masked to the first character of its local part and
// its top-level domain (`m•••@•••.vn`): the domain of a small business is as good as
// a name, so it goes too. It is the form an erasure by e-mail carries. A value with
// no `@` is not echoed.
func Email(raw string) string {
	at := strings.LastIndexByte(raw, '@')
	if at < 1 || at == len(raw)-1 {
		return "(not an e-mail address)"
	}
	local, domain := raw[:at], strings.ToLower(raw[at+1:])
	first := []rune(local)[0]
	tld := ""
	if dotAt := strings.LastIndexByte(domain, '.'); dotAt >= 0 {
		tld = domain[dotAt:]
	}
	return strings.ToLower(string(first)) + strings.Repeat(dot, 3) + "@" + strings.Repeat(dot, 3) + tld
}

// IP is an address truncated to its network: the /24 of an IPv4 address, the /48 of
// an IPv6 one. A value that is not an address is not echoed.
func IP(raw string) string {
	a, err := netip.ParseAddr(strings.Trim(raw, "[]"))
	if err != nil {
		return "(not an address)"
	}
	a = a.Unmap().WithZone("")
	bits := 48
	if a.Is4() {
		bits = 24
	}
	p, _ := a.Prefix(bits)
	return p.String()
}

var (
	macPattern  = regexp.MustCompile(`\b[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}\b`)
	ipv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Pattern = regexp.MustCompile(`\[[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*(?:%[^\]]*)?\]`)
)

// Line masks every MAC and IP address in free text: a line somebody else wrote,
// such as `net/http`'s "TLS handshake error from 10.0.0.7:51514".
func Line(s string) string {
	s = macPattern.ReplaceAllStringFunc(s, MAC)
	s = ipv6Pattern.ReplaceAllStringFunc(s, func(m string) string { return "[" + IP(m) + "]" })
	return ipv4Pattern.ReplaceAllStringFunc(s, IP)
}

// ErrorLog is an `http.Server.ErrorLog` that writes `Line` of every message to
// stderr. `net/http` logs a failed TLS handshake with the client's address and
// port, and on the guest portal every phone that refuses a certificate is one.
func ErrorLog() *log.Logger { return log.New(lineWriter{os.Stderr}, "", log.LstdFlags) }

type lineWriter struct{ w io.Writer }

func (l lineWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(l.w, Line(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}
