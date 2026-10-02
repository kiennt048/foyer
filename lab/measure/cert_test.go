package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"strings"
	"testing"
	"time"
)

// The generated certificate must be refusable by iOS for exactly one reason: it
// is self-signed. Every other reason iOS has to refuse a certificate would make
// the HTTPS question's answer meaningless, so each of them is a case here — and each case is
// written as a certificate that *should fail*, because a gate that has only ever
// been shown passing certificates is the sixth mechanism in this repository that
// looked like a check and was not.

func testCert(t *testing.T, mutate func(*x509.Certificate)) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber:          mustSerial(),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             now,
		NotAfter:              now.Add(leafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("192.0.2.23")},
	}
	mutate(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUsableOnIOSAcceptsAWellShapedLeaf(t *testing.T) {
	c := testCert(t, func(*x509.Certificate) {})
	if err := usableOnIOS(c, []net.IP{net.ParseIP("192.0.2.23")}, time.Now()); err != nil {
		t.Fatalf("a correctly shaped certificate was rejected: %v", err)
	}
}

func TestUsableOnIOSRejectsWhatIOSRejects(t *testing.T) {
	serving := []net.IP{net.ParseIP("192.0.2.23")}

	cases := []struct {
		name   string
		mutate func(*x509.Certificate)
		want   string
	}{
		{
			// The pre-2019 shape: a CommonName and nothing else. iOS ignores the
			// CommonName outright, so this certificate matches no host at all.
			name:   "no subjectAltName",
			mutate: func(c *x509.Certificate) { c.IPAddresses = nil },
			want:   "no subjectAltName",
		},
		{
			name:   "SAN for the wrong address",
			mutate: func(c *x509.Certificate) { c.IPAddresses = []net.IP{net.ParseIP("10.0.0.1")} },
			want:   "does not cover",
		},
		{
			// One day over Apple's ceiling. 399 rather than 800 on purpose: a gate
			// that only catches an absurd lifetime does not catch the realistic
			// mistake, which is a certificate minted for "about a year".
			name:   "399 days",
			mutate: func(c *x509.Certificate) { c.NotAfter = c.NotBefore.Add(399 * 24 * time.Hour) },
			want:   "validity period",
		},
		{
			name:   "no serverAuth",
			mutate: func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} },
			want:   "serverAuth",
		},
		{
			name: "already expired",
			mutate: func(c *x509.Certificate) {
				c.NotBefore = time.Now().Add(-48 * time.Hour)
				c.NotAfter = time.Now().Add(-24 * time.Hour)
			},
			want: "expired",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := usableOnIOS(testCert(t, tc.mutate), serving, time.Now())
			if err == nil {
				t.Fatalf("accepted a certificate iOS would refuse (%s); the measurement would then be of this file rather than of trust policy", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("rejected for the wrong reason: got %q, want something containing %q", err, tc.want)
			}
		})
	}
}

func TestEnsureTLSRegeneratesWhenTheAddressChanged(t *testing.T) {
	dir := t.TempDir()
	first := []net.IP{net.ParseIP("192.0.2.23")}

	a, err := ensureTLS(dir, first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !a.regenerated {
		t.Fatal("the first run must generate a certificate")
	}

	// A second run on the same network reuses it, or every phone that installed
	// the root has to install it again for no reason.
	b, err := ensureTLS(dir, first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.regenerated {
		t.Fatalf("regenerated for no reason: %s", b.reason)
	}

	// A second run on a different network must not serve a certificate whose SAN
	// names an address the phone is not talking to: that failure looks exactly
	// like the refusal the measurement is trying to observe.
	c, err := ensureTLS(dir, []net.IP{net.ParseIP("10.42.0.7")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.regenerated {
		t.Fatal("kept a certificate that does not cover the address now being served")
	}
	if !strings.Contains(c.reason, "does not cover") {
		t.Fatalf("regenerated without saying why: %q", c.reason)
	}
	if err := usableOnIOS(c.leaf, []net.IP{net.ParseIP("10.42.0.7")}, time.Now()); err != nil {
		t.Fatalf("the replacement is not usable either: %v", err)
	}
}
