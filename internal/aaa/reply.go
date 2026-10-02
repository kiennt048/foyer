package aaa

import (
	"regexp"
	"sort"
	"strings"
)

// The boundary credentials never cross.
//
// `docs/rules/security.md` §2 states the consequence rather than the rule,
// because the consequence is the part that is easy to under-read: returning an NT
// or cleartext password attribute from here does not leak one password, it turns
// the Policy API into an endpoint that yields the verifier for **any username
// supplied**. A full credential-store dump, over HTTP, without touching PostgreSQL.
//
// There is a write-time hook that blocks the three literal spellings in this
// package's source, and it is the reason this comment paraphrases them rather than
// naming them. That guard is worth having and is not sufficient: it matches source
// text, so it cannot see an attribute name assembled at runtime, read from a
// voucher batch's vendor-attribute column, or supplied by a VendorProfile that does
// not exist yet. What follows is a gate on the value actually being written, which
// is the thing the guard is a proxy for.

// forbiddenNamePattern matches an attribute name that is credential material or a
// container for it.
//
// Deliberately broad on the credential side and deliberately not broad anywhere
// else: every attribute the Policy API legitimately emits -- Session-Timeout,
// Acct-Interim-Interval, Filter-Id, Class, Reply-Message, Tunnel-Type,
// Tunnel-Medium-Type, Tunnel-Private-Group-Id, WISPr-Bandwidth-Max-Down,
// Aruba-User-Role, Ruckus-External-Url -- passes it, and there is a test that
// spells all of them out so a future widening of this expression fails loudly
// rather than quietly costing a feature.
//
// `private` is deliberately not a term: it would refuse
// **Tunnel-Private-Group-Id** -- the VLAN attribute, and the one thing
// `Capabilities.DynamicVLAN` exists to deliver. The gate would drop every
// dynamic-VLAN reply in the product, as a 503 with a credential-leak alarm beside
// it, and nothing else would say why. `TLS-Private-Key` and its family are
// caught by `\bkey\b` regardless, so the term would buy nothing.
var forbiddenNamePattern = regexp.MustCompile(
	`(?i)pass|secret|credenti|cleartext|nt-?hash|lm-?hash|ntlm|\bmd4\b|chap|\bkey\b|\bcert`)

// ForbiddenReplyAttribute reports whether an attribute may never leave the Policy
// API.
//
// Two independent rules, and the first is the structural one:
//
//  1. **No list qualifier.** `rlm_rest` reads the JSON key to decide which
//     attribute list the value lands in, so a key qualified with FreeRADIUS's
//     control list is a write into that list -- which is where a password verifier
//     would have to land to be usable. The Policy API answers authorization and
//     therefore has business with exactly one list, reply, so a key carrying any
//     qualifier at all is refused without needing to know which qualifiers are
//     dangerous. Refusing the shape rather than the name is what survives the next
//     one.
//
//  2. **No credential-shaped name**, for the case where a qualifier is absent
//     because the caller assembled a bare attribute name.
func ForbiddenReplyAttribute(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return true
	}
	if strings.ContainsAny(n, ":&") {
		return true
	}
	return forbiddenNamePattern.MatchString(n)
}

// ForbiddenReplyAttributes returns every offending key in a reply, sorted.
//
// All of them rather than the first, because the log line this feeds is read once,
// during an incident, by somebody deciding how bad it is.
func ForbiddenReplyAttributes(attrs map[string]string) []string {
	var bad []string
	for k := range attrs {
		if ForbiddenReplyAttribute(k) {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	return bad
}
