package aaa

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The named security test from `docs/rules/security.md` §2: no credential
// material crosses the `rlm_rest` boundary.
//
// It can be made to fail. The sabotage is the ForbiddenReplyAttributes block in
// writeDecision, deleted; under it the subtest "a reply carrying credential
// material is not served at all" of
// TestReplyGateRefusesCredentialMaterialAndServesEverythingElse turns red.
//
// The subject is writeDecision rather than a helper written for the test, because
// writeDecision is the function every reply actually passes through -- Handler
// calls it, HTTPHandler calls it, and there is no second path out.

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestReplyGateRefusesCredentialMaterialAndServesEverythingElse is deliberately two
// halves.
//
// A gate that refuses everything satisfies the first half perfectly and is useless:
// it is the Policy API answering 503 to every request, which on a fail-open site is
// a network that lets everybody on and on a fail-closed one is an outage. The
// second half is what says the gate is a gate rather than a wall.
func TestReplyGateRefusesCredentialMaterialAndServesEverythingElse(t *testing.T) {
	e := NewEngine(&mockStore{}, 300)

	t.Run("a reply carrying credential material is not served at all", func(t *testing.T) {
		// A whole, otherwise-valid decision with one poisoned key -- which is the
		// realistic shape. Nothing sets out to serve a password; a vendor
		// attribute column, a VendorProfile or a merge writes one in after the
		// decision was made.
		decision := &Decision{Allow: true, ReplyAttributes: map[string]string{
			"Session-Timeout":       "3600",
			"Acct-Interim-Interval": "300",
			"NT-Password":           "8846f7eaee8fb117ad06bdd830b7586c",
		}}

		w := httptest.NewRecorder()
		e.writeDecision(w, discardLogger(), decision)

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d, want %d: a reply of unknown provenance is undecided, not an accept",
				w.Code, http.StatusServiceUnavailable)
		}
		body := w.Body.String()
		if strings.Contains(body, "8846f7eaee8fb117ad06bdd830b7586c") {
			t.Fatalf("the verifier reached the wire: %s", body)
		}
		// The whole reply is dropped, not the offending key: a decision that
		// carried credential material is a decision nobody can account for, and
		// serving the rest of it serves an unaccountable authorization.
		if strings.Contains(body, "Session-Timeout") {
			t.Errorf("the surviving attributes were served alongside the refusal: %s", body)
		}
	})

	t.Run("an ordinary reply is served in full", func(t *testing.T) {
		attrs := map[string]string{
			"Session-Timeout":       "3600",
			"Acct-Interim-Interval": "300",
			"Filter-Id":             "5000/2000",
			"Class":                 DegradedClass,
		}
		w := httptest.NewRecorder()
		e.writeDecision(w, discardLogger(), &Decision{Allow: true, ReplyAttributes: attrs})

		if w.Code != http.StatusOK {
			t.Fatalf("status %d for an ordinary reply, want 200", w.Code)
		}
		var got map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("reply is not a JSON dictionary of pairs, which is what rlm_rest consumes: %v", err)
		}
		if len(got) != len(attrs) {
			t.Fatalf("served %d attributes, want %d: %v", len(got), len(attrs), got)
		}
		for k, v := range attrs {
			if got[k] != v {
				t.Errorf("%s served as %q, want %q", k, got[k], v)
			}
		}
	})
}

// TestForbiddenReplyAttributeNames spells out both lists.
//
// The permitted list is not decoration: forbiddenNamePattern is broad, and a
// widening of it that quietly swallowed Filter-Id or Tunnel-Private-Group-Id would
// cost a feature with no error anywhere -- the admin console would offer a rate
// limit that never reached the NAS, which is the exact failure
// `docs/rules/frontend.md` is about.
func TestForbiddenReplyAttributeNames(t *testing.T) {
	permitted := []string{
		"Session-Timeout",
		"Idle-Timeout",
		"Acct-Interim-Interval",
		"Filter-Id",
		"Class",
		"Reply-Message",
		"Termination-Action",
		"Tunnel-Type",
		"Tunnel-Medium-Type",
		"Tunnel-Private-Group-Id",
		"WISPr-Bandwidth-Max-Down",
		"WISPr-Bandwidth-Max-Up",
		"Aruba-User-Role",
		"Ruckus-External-Url",
		"Mikrotik-Rate-Limit",
		"Cisco-AVPair",
	}
	for _, name := range permitted {
		if ForbiddenReplyAttribute(name) {
			t.Errorf("%s is an attribute the Policy API legitimately emits and the gate refused it", name)
		}
	}

	forbidden := []string{
		"User-Password",
		"Cleartext-Password",
		"NT-Password",
		"LM-Password",
		"Password",
		"password",
		"Chap-Password",
		"MS-CHAP-Response",
		"Tunnel-Password",
		"NT-Hash",
		"nthash",
		"EAP-Key-Name",
		"MS-MPPE-Send-Key",
		"MS-MPPE-Recv-Key",
		"Client-Secret",
		"credential",
		"TLS-Client-Cert-Filename",
		"", // an empty key lands wherever rlm_rest decides, which is not a decision this API gets to skip
	}
	for _, name := range forbidden {
		if !ForbiddenReplyAttribute(name) {
			t.Errorf("%q was allowed over the rlm_rest boundary", name)
		}
	}
}

// TestListQualifiersAreRefusedByShape is the structural half of the gate, and it is
// the one that survives.
//
// `rlm_rest` reads the JSON key to decide which FreeRADIUS attribute list a value
// lands in. A password verifier is only useful to an attacker if it reaches the
// control list, so the qualifier is the mechanism -- and refusing every qualifier
// costs nothing, because the Policy API answers authorization and has business with
// exactly one list. The names change; the shape does not.
func TestListQualifiersAreRefusedByShape(t *testing.T) {
	qualified := []string{
		"control:Session-Timeout",
		"reply:Session-Timeout",
		"request:User-Name",
		"session-state:Foo",
		"&control:Foo",
		"outer.request:User-Name",
	}
	for _, name := range qualified {
		if !ForbiddenReplyAttribute(name) {
			t.Errorf("%q carries a list qualifier and was allowed; the Policy API writes reply attributes and nothing else", name)
		}
	}
}

// TestForbiddenReplyAttributesReportsAllOfThem: the log line this feeds is read
// once, during an incident, by somebody deciding how bad it is. "One offending
// attribute" and "four" are different incidents.
func TestForbiddenReplyAttributesReportsAllOfThem(t *testing.T) {
	bad := ForbiddenReplyAttributes(map[string]string{
		"Session-Timeout": "60",
		"NT-Password":     "x",
		"User-Password":   "y",
		"control:Foo":     "z",
	})
	if len(bad) != 3 {
		t.Fatalf("reported %v, want all three offenders", bad)
	}
	// Sorted, so the log line is comparable between two incidents.
	if bad[0] != "NT-Password" || bad[1] != "User-Password" || bad[2] != "control:Foo" {
		t.Errorf("offenders are not in a stable order: %v", bad)
	}
}
