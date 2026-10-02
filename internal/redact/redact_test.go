package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestMACKeepsTheLastTwoOctetsOfEverySpelling(t *testing.T) {
	for _, in := range []string{"AA-BB-CC-00-11-22", "aa:bb:cc:00:11:22", "aabb.cc00.1122", "AABBCC001122"} {
		if got := MAC(in); got != "••:••:••:••:11:22" {
			t.Errorf("MAC(%q) = %q", in, got)
		}
	}
	for _, in := range []string{"guest@example.com", "0912345678", "aa:bb:cc:00:11"} {
		if got := MAC(in); got != "(not a MAC)" {
			t.Errorf("MAC(%q) = %q, want it not echoed", in, got)
		}
	}
	if MAC("") != "" {
		t.Error("an empty MAC is not empty")
	}
}

func TestPhoneKeepsTheLastThreeDigits(t *testing.T) {
	if got := Phone("+84 912 345 678"); got != "••••••••678" {
		t.Errorf("Phone = %q", got)
	}
	if got := Phone("12"); got != "••" {
		t.Errorf("a short value is echoed: %q", got)
	}
}

func TestEmailKeepsOneLetterAndTheTopLevelDomain(t *testing.T) {
	for in, want := range map[string]string{
		"Minh.Nguyen@cafe.example": "m•••@•••.example",
		"a@b":                      "a•••@•••",
		"no-at-sign":               "(not an e-mail address)",
		"@example.com":             "(not an e-mail address)",
	} {
		if got := Email(in); got != want {
			t.Errorf("Email(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIPKeepsTheNetworkOnly(t *testing.T) {
	for in, want := range map[string]string{
		"10.20.0.77":             "10.20.0.0/24",
		"[2001:db8:1:2::77]":     "2001:db8:1::/48",
		"::ffff:192.0.2.9":       "192.0.2.0/24",
		"not an address at all!": "(not an address)",
	} {
		if got := IP(in); got != want {
			t.Errorf("IP(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTheServerErrorLogCarriesNoGuestAddress drives the line `net/http` writes for
// every failed handshake on the guest portal.
func TestTheServerErrorLogCarriesNoGuestAddress(t *testing.T) {
	var buf bytes.Buffer
	w := lineWriter{&buf}
	for _, line := range []string{
		"http: TLS handshake error from 10.20.0.77:51514: remote error: tls: unknown certificate\n",
		"http: TLS handshake error from [2001:db8:1:2::77]:51514: EOF\n",
		"client aa-bb-cc-00-11-22 did something\n",
	} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	out := buf.String()
	for _, leaked := range []string{"10.20.0.77", "2001:db8:1:2::77", "aa-bb-cc"} {
		if strings.Contains(out, leaked) {
			t.Errorf("the log still carries %q:\n%s", leaked, out)
		}
	}
	for _, kept := range []string{"10.20.0.0/24", "2001:db8:1::/48", "••:••:••:••:11:22", "unknown certificate"} {
		if !strings.Contains(out, kept) {
			t.Errorf("the log lost %q:\n%s", kept, out)
		}
	}
}
