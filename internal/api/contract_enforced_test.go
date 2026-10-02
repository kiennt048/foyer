package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The contract's own rules must be enforced at the boundary, not discovered by
// PostgreSQL.
//
// Without request validation, both cases below answer 500 from a CHECK
// constraint where the contract declares 422 (reproduced on a fresh install):
//
//	POST /voucher-batches   omitted `expiryMode`, which VoucherBatchCreate REQUIRES
//	POST /controllers       sent a `tlsMode` outside the TlsMode enum
//
// oapi-codegen emits required fields as plain values and enums as `type X string`,
// so a missing field is the zero value and a bad enum is just a string. Hand-written
// checks covered some fields of the same request and not others — `label`, `siteId`
// and `count` were checked while `expiryMode` was not — which is the worst
// arrangement available: the boundary looks like it validates, so the next field
// added is assumed covered and is not.
func TestTheContractRefusesWhatItDoesNotPermit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		body   map[string]any
		field  string
		reason string
	}{
		{
			name: "a required field the request omits",
			path: "/voucher-batches",
			body: map[string]any{
				"siteId": "site-1", "label": "Reception", "count": 5,
				"plan": map[string]any{"durationMinutes": 60},
			},
			field:  "expiryMode",
			reason: "VoucherBatchCreate lists expiryMode as required; omitting it reached voucher_batches_expiry_mode_check",
		},
		{
			name: "a value outside an enum",
			path: "/controllers",
			body: map[string]any{
				"name": "c", "vendor": "unifi", "baseUrl": "https://example",
				"authMode": "api-key", "tlsMode": "insecure-skip-verify",
			},
			field:  "tlsMode",
			reason: "TlsMode permits verify and trust-on-first-use; anything else reached controllers_tls_mode_check",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.signIn(adminUser)

			resp := r.do(http.MethodPost, tc.path, tc.body)
			body := readAll(t, resp)
			resp.Body.Close()

			if resp.StatusCode == http.StatusInternalServerError {
				t.Fatalf("answered 500 — the value reached the database, which is the defect this test exists for.\n  %s", tc.reason)
			}
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("got %d, want 422.\n  %s\n  %s", resp.StatusCode, tc.reason, body)
			}
			var problem struct {
				Params struct {
					Fields map[string]string `json:"fields"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(body), &problem); err != nil {
				t.Fatalf("the refusal is not a problem document: %v\n  %s", err, body)
			}
			if _, named := problem.Params.Fields[tc.field]; !named {
				t.Errorf("the refusal does not name %q, so the console cannot put it beside the input:\n  %s",
					tc.field, body)
			}
		})
	}
}
