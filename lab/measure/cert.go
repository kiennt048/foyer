package main

// The certificate is the measurement.
//
// The HTTPS question asks whether the iOS Captive Network Assistant will display a page served
// over HTTPS with a certificate it does not trust. That question only has an
// answer if the certificate is *otherwise* perfect: if iOS refuses a leaf with
// no subjectAltName, or one valid for 400 days, the operator learns nothing
// about self-signedness and the deployment shape stays undecided for another
// week. Apple's requirements for TLS server certificates, in force since
// September 2020, are the ones encoded here:
//
//   - subjectAltName is mandatory; the CommonName is ignored outright
//   - the validity period must be 398 days or fewer
//   - extendedKeyUsage must contain id-kp-serverAuth
//   - RSA keys must be 2048 bits or more; the hash must be SHA-256 or better
//
// usableOnIOS asserts all four at startup, and a certificate on disk that fails
// any of them is regenerated rather than served. That is the difference between
// "iOS refused a self-signed certificate" and "iOS refused this file", and the
// two answers point at completely different deployment shapes.
//
// What is generated is a root and a leaf, not a bare self-signed leaf. The root
// is offered at /ca.crt so the operator can measure the other branch in the same
// sitting: install the root on the phone, and the same page is served over a
// certificate the device trusts. That branch is the fallback if the HTTPS
// question comes back "refused outright", so it is worth measuring in the same
// session rather than a second one.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	// appleMaxLeafValidity is Apple's published ceiling. A certificate over it is
	// rejected on iOS 13+ and macOS 10.15+ regardless of who signed it.
	appleMaxLeafValidity = 398 * 24 * time.Hour
	// leafValidity leaves a day of headroom under that ceiling, so a clock skew
	// between this host and the phone cannot turn 398 into 399.
	leafValidity = 397 * 24 * time.Hour
	// caValidity matches the leaf deliberately. iOS does not apply the 398-day
	// rule to a root, but a lab instrument whose root outlives its leaf invites
	// someone to reuse a stale root next year and wonder why the phone objects.
	caValidity = 397 * 24 * time.Hour
)

type tlsMaterial struct {
	server tls.Certificate
	leaf   *x509.Certificate
	ca     *x509.Certificate
	caPEM  []byte
	// regenerated says whether this run wrote new files, and why. Printed at
	// startup: a root that changed silently means every phone that installed the
	// old one now fails, and that failure looks exactly like a negative answer to the HTTPS question.
	regenerated bool
	reason      string
}

// usableOnIOS reports why iOS would refuse this certificate for a reason that is
// not "you signed it yourself". A nil error means any refusal observed on the
// device is a finding about trust policy rather than about this file.
func usableOnIOS(c *x509.Certificate, ips []net.IP, now time.Time) error {
	if len(c.IPAddresses) == 0 {
		return errors.New("no subjectAltName of type iPAddress: iOS ignores the CommonName entirely, so this certificate matches no host at all")
	}
	for _, want := range ips {
		if !slices.ContainsFunc(c.IPAddresses, func(have net.IP) bool { return have.Equal(want) }) {
			return fmt.Errorf("subjectAltName does not cover %s: the phone will report a name mismatch, which is not the question being asked", want)
		}
	}
	if life := c.NotAfter.Sub(c.NotBefore); life > appleMaxLeafValidity {
		return fmt.Errorf("validity period is %.0f days: iOS rejects any TLS server certificate over %.0f days, whoever signed it",
			life.Hours()/24, appleMaxLeafValidity.Hours()/24)
	}
	if now.Before(c.NotBefore) {
		return fmt.Errorf("not valid until %s", c.NotBefore.Format(time.RFC3339))
	}
	if now.After(c.NotAfter) {
		return fmt.Errorf("expired at %s", c.NotAfter.Format(time.RFC3339))
	}
	if !slices.Contains(c.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return errors.New("extendedKeyUsage does not contain serverAuth")
	}
	switch c.SignatureAlgorithm {
	case x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA,
		x509.ECDSAWithSHA256, x509.ECDSAWithSHA384, x509.ECDSAWithSHA512,
		x509.SHA256WithRSAPSS, x509.SHA384WithRSAPSS, x509.SHA512WithRSAPSS,
		x509.PureEd25519:
	default:
		return fmt.Errorf("signature algorithm %s is below the SHA-256 floor", c.SignatureAlgorithm)
	}
	switch pub := c.PublicKey.(type) {
	case *rsa.PublicKey:
		if pub.N.BitLen() < 2048 {
			return fmt.Errorf("RSA key is %d bits, under the 2048-bit floor", pub.N.BitLen())
		}
	case *ecdsa.PublicKey:
		if pub.Curve.Params().BitSize < 256 {
			return fmt.Errorf("EC key is %d bits, under the 256-bit floor", pub.Curve.Params().BitSize)
		}
	}
	return nil
}

// ensureTLS loads the root and leaf from dir, or generates them. It regenerates
// whenever the leaf on disk would fail on the device — including the ordinary
// case of the laptop having moved to a different network since last time, which
// changes the LAN address the SAN has to carry.
func ensureTLS(dir string, ips []net.IP, dnsNames []string) (*tlsMaterial, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}

	m, reason := loadTLS(dir, ips)
	if m != nil {
		return m, nil
	}

	m, err := generateTLS(dir, ips, dnsNames)
	if err != nil {
		return nil, err
	}
	m.regenerated = true
	m.reason = reason
	return m, nil
}

func loadTLS(dir string, ips []net.IP) (*tlsMaterial, string) {
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, "no certificate on disk yet"
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
	if err != nil {
		return nil, "the stored leaf and key could not be loaded: " + err.Error()
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, "the stored leaf could not be parsed: " + err.Error()
	}
	if err := usableOnIOS(leaf, ips, time.Now()); err != nil {
		return nil, "the stored leaf is not usable on iOS: " + err.Error()
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil, "the stored root is not PEM"
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, "the stored root could not be parsed: " + err.Error()
	}
	pair.Leaf = leaf
	return &tlsMaterial{server: pair, leaf: leaf, ca: ca, caPEM: caPEM}, ""
}

func generateTLS(dir string, ips []net.IP, dnsNames []string) (*tlsMaterial, error) {
	now := time.Now().Add(-1 * time.Hour) // absorb a clock skew between host and phone

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          mustSerial(),
		Subject:               pkix.Name{CommonName: "Foyer measurement root", Organization: []string{"Foyer lab"}},
		NotBefore:             now,
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          mustSerial(),
		Subject:               pkix.Name{CommonName: "Foyer measurement host", Organization: []string{"Foyer lab"}},
		NotBefore:             now,
		NotAfter:              now.Add(leafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
		DNSNames:              dnsNames,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, err
	}
	if err := usableOnIOS(leaf, ips, time.Now()); err != nil {
		// A generator that emits a certificate its own gate rejects is a bug in
		// this file, and it must stop the program rather than serve the file.
		return nil, fmt.Errorf("generated a certificate that iOS would refuse for the wrong reason: %w", err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, err
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER})

	writes := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"ca.crt", caPEM, 0o644},
		{"ca.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER}), 0o600},
		{"server.crt", append(append([]byte{}, leafPEM...), caPEM...), 0o644},
		{"server.key", keyPEM, 0o600},
	}
	for _, w := range writes {
		if err := os.WriteFile(filepath.Join(dir, w.name), w.data, w.mode); err != nil {
			return nil, fmt.Errorf("write %s: %w", w.name, err)
		}
	}

	return &tlsMaterial{
		server: tls.Certificate{
			Certificate: [][]byte{leafDER, caDER},
			PrivateKey:  leafKey,
			Leaf:        leaf,
		},
		leaf:  leaf,
		ca:    ca,
		caPEM: caPEM,
	}, nil
}

func mustSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		panic("measure: crypto/rand is unavailable: " + err.Error())
	}
	return n
}
