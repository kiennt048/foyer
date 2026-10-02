package api

import (
	"sort"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// TestBodyOptionPermissionsMatchTheContract ties the contract's property-level
// `x-permission` annotations to the Go handlers that enforce them.
//
// The contract declares, on a request-body property, a permission that setting
// the property needs beyond the operation's own. `web/mock` enforces every such
// annotation generically from the document; the Go server's gate reads only the
// operation's `x-permission`, so each option is checked by its handler — and
// `enforcedOptions` below is the list of handlers that do, each with the test that
// proves it. An annotation added to the contract with no Go check fails here, and
// so does a Go check whose annotation was removed.
func TestBodyOptionPermissionsMatchTheContract(t *testing.T) {
	enforcedOptions := map[string]string{
		// DisconnectSession, TestDisconnectRevokeVoucherNeedsVouchersManage.
		"SessionDisconnectRequest.revokeVoucher": string(gen.VouchersManage),
		// DisconnectPortalGrant, TestDisconnectPortalGrantRevokeVoucherNeedsVouchersManage.
		"PortalGrantDisconnectRequest.revokeVoucher": string(gen.VouchersManage),
	}

	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	declared := map[string]string{}
	for name, ref := range spec.Components.Schemas {
		if ref.Value == nil {
			continue
		}
		for prop, p := range ref.Value.Properties {
			if p.Value == nil {
				continue
			}
			if v, ok := p.Value.Extensions["x-permission"].(string); ok {
				declared[name+"."+prop] = v
			}
		}
	}

	keys := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	for _, k := range keys(declared) {
		if want, ok := enforcedOptions[k]; !ok {
			t.Errorf("the contract gives %s its own permission (%s) and no Go handler checks it", k, declared[k])
		} else if want != declared[k] {
			t.Errorf("%s: the contract says %s, the Go handler checks %s", k, declared[k], want)
		}
	}
	for _, k := range keys(enforcedOptions) {
		if _, ok := declared[k]; !ok {
			t.Errorf("the Go handler checks %s, and the contract no longer declares a permission on it", k)
		}
	}
}
