package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// Certificates minted once per run and sent to BOTH servers, byte for byte.
//
// The skips these replace said a run-time certificate's "fingerprint and expiry
// cannot match between two processes". They can: the fingerprint is a hash of the
// bytes and the expiry is a field in them, and the harness hands the same bytes to
// each server. What differed was only that nobody had minted them in the harness.

// harnessPortalHost is the name the portal certificate covers, and the https portal
// address the harness saves first so that both servers accept it.
const harnessPortalHost = "portal.harness.test"

type mintedPEM struct {
	certificate, privateKey, ca string
}

// mintPortalPair is a self-signed ECDSA leaf for harnessPortalHost.
func mintPortalPair() (mintedPEM, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return mintedPEM{}, err
	}
	now := time.Now().Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: harnessPortalHost},
		DNSNames:     []string{harnessPortalHost},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return mintedPEM{}, err
	}
	return encodePair(der, key)
}

// mintEapPair is a server certificate signed by a CA of its own, as EAP-TLS needs.
func mintEapPair() (mintedPEM, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return mintedPEM{}, err
	}
	now := time.Now().Truncate(time.Second)
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Harness RADIUS CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return mintedPEM{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return mintedPEM{}, err
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "radius.harness.test"},
		DNSNames:     []string{"radius.harness.test"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		return mintedPEM{}, err
	}
	p, err := encodePair(der, key)
	if err != nil {
		return mintedPEM{}, err
	}
	p.ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return p, nil
}

func encodePair(der []byte, key *ecdsa.PrivateKey) (mintedPEM, error) {
	k, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return mintedPEM{}, fmt.Errorf("encoding the key: %w", err)
	}
	return mintedPEM{
		certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		privateKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})),
	}, nil
}
