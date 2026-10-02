package raddb

import (
	"strings"
	"testing"
)

// The Policy API resolves a voucher's venue from the packet's source address
// (internal/aaa, Request.ClientIP), so every authorize body must carry it — the
// plain call, the EAP-marked one and the inner-tunnel one. A body without it is a
// request with no venue, and the Policy API refuses every voucher in it.
func TestTheRestModuleSendsThePacketSource(t *testing.T) {
	dir := generate(t, nil)
	rest := readGenerated(t, dir, "mods-available", "rest")
	const want = `"client_ip":"%{jsonquote:%{%{Packet-Src-IP-Address}:-%{Packet-Src-IPv6-Address}}}"`
	var bodies int
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data = '{") {
			continue
		}
		bodies++
		if !strings.Contains(line, want) {
			t.Errorf("an authorize body does not carry the packet's source address:\n%s", line)
		}
	}
	if bodies == 0 {
		t.Fatal("no authorize body in mods-available/rest; this test asserted nothing")
	}
}
