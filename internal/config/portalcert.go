package config

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Certificate checks for a pair an operator uploads in the console (the portal
// certificate is an operator setting, so it lives in the database rather than in
// /opt/foyer/secrets). One implementation, called by `internal/api` before
// anything is stored and by `cmd/foyer` before anything is served, because the two
// disagreeing about whether a pair is usable is a console that says "saved" over a
// portal that keeps serving the old one.
//
// What the field checks, and what this copies: Caddy, Traefik and the UniFi
// controller's certificate import all refuse a pair whose key is not the leaf's and
// a chain that is out of order, and they differ on the name check only because they
// know what name they are serving. This one does know — the host of the portal's
// public address — so it checks it, which is the check the console's own
// FOYER_ADMIN_TLS_NAMES history says is the one nothing else makes.
//
// **No trust-store check**, deliberately. A self-signed portal certificate is the operator's choice to make, at one tap per
// device; refusing one that no public CA issued would take that choice away.

// CertificateFault is why a pair was refused, in the words the contract's
// `certificate-refused` fault carries as its `reason`.
type CertificateFault string

const (
	CertNotPEM            CertificateFault = "not-pem"
	CertKeyMismatch       CertificateFault = "key-mismatch"
	CertExpired           CertificateFault = "expired"
	CertNotYetValid       CertificateFault = "not-yet-valid"
	CertChainBroken       CertificateFault = "chain-broken"
	CertNameNotCovered    CertificateFault = "name-not-covered"
	CertPortalURLNotHTTPS CertificateFault = "portal-url-not-https"
)

// Which input a CertificateError is about.
const (
	CertFieldCertificate = "certificate"
	CertFieldKey         = "key"
	CertFieldURL         = "url"
	CertFieldCA          = "ca"
)

// CertificateError is a refusal: which input, and why. The message names neither
// the key nor any of its bytes — it reaches logs and API answers.
type CertificateError struct {
	Field string
	Fault CertificateFault
	Err   error
}

func (e *CertificateError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("certificate refused (%s): %s", e.Field, e.Fault)
	}
	return fmt.Sprintf("certificate refused (%s): %s: %v", e.Field, e.Fault, e.Err)
}

func (e *CertificateError) Unwrap() error { return e.Err }

func certRefused(field string, fault CertificateFault, err error) error {
	return &CertificateError{Field: field, Fault: fault, Err: err}
}

// CertificateInfo describes a leaf, for the console. Public facts only.
type CertificateInfo struct {
	Subject           string
	Issuer            string
	DNSNames          []string
	IPAddresses       []string
	NotBefore         time.Time
	NotAfter          time.Time
	FingerprintSHA256 string
	ChainLength       int
}

// DescribeCertificate is the console's description of a leaf and its chain length.
func DescribeCertificate(leaf *x509.Certificate, chainLength int) CertificateInfo {
	sum := sha256.Sum256(leaf.Raw)
	info := CertificateInfo{
		Subject:           leaf.Subject.String(),
		Issuer:            leaf.Issuer.String(),
		DNSNames:          append([]string{}, leaf.DNSNames...),
		NotBefore:         leaf.NotBefore.UTC(),
		NotAfter:          leaf.NotAfter.UTC(),
		FingerprintSHA256: hex.EncodeToString(sum[:]),
		ChainLength:       chainLength,
	}
	info.IPAddresses = []string{}
	for _, ip := range leaf.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	return info
}

// ParseCertificates reads every CERTIFICATE block, in order, and refuses anything
// that is not one.
func ParseCertificates(field string, pemText []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := pemText
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, certRefused(field, CertNotPEM, fmt.Errorf("a %q block where a certificate belongs", block.Type))
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, certRefused(field, CertNotPEM, err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, certRefused(field, CertNotPEM, errors.New("no PEM certificate found"))
	}
	return out, nil
}

// ParseCertificatePair checks an uploaded pair and answers it ready to serve.
//
// In order, first failure wins: both inputs are PEM of the right kind, the key is
// the leaf's, each certificate is signed by the next (ParseCertificateChain), and
// every one is valid at `now` (CheckValidAt). The name is not checked here — see
// CoversHost — because the EAP-TLS certificate has no host to cover.
func ParseCertificatePair(certPEM, keyPEM []byte, now time.Time) (tls.Certificate, CertificateInfo, error) {
	pair, chain, err := ParseCertificateChain(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, CertificateInfo{}, err
	}
	if err := CheckValidAt(chain, now); err != nil {
		return tls.Certificate{}, CertificateInfo{}, err
	}
	return pair, DescribeCertificate(chain[0], len(chain)), nil
}

// ParseCertificateChain is every check on a pair that does not read the clock:
// both inputs PEM of the right kind, the key the leaf's, each certificate signed
// by the next. The answer's Leaf is set.
//
// Split from ParseCertificatePair for the deployment's certificate files, which
// `cmd/foyer` loads at start: a pair whose only fault is the time is loaded and
// reported `not-served` with the reason, rather than refusing to start a process
// that also answers every RADIUS request. Everything else is refused the same way
// for a file as for an upload.
func ParseCertificateChain(certPEM, keyPEM []byte) (tls.Certificate, []*x509.Certificate, error) {
	chain, err := ParseCertificates(CertFieldCertificate, certPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	if !hasPrivateKeyBlock(keyPEM) {
		return tls.Certificate{}, nil, certRefused(CertFieldKey, CertNotPEM, errors.New("no PEM private key found"))
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		if strings.Contains(err.Error(), "does not match") {
			return tls.Certificate{}, nil, certRefused(CertFieldKey, CertKeyMismatch, nil)
		}
		// Parsed as PEM and not as a key: an encrypted key, or an algorithm Go
		// does not serve. Said as `not-pem` on the key rather than invented.
		return tls.Certificate{}, nil, certRefused(CertFieldKey, CertNotPEM, errors.New("the private key does not parse"))
	}
	for i := 0; i+1 < len(chain); i++ {
		if err := chain[i].CheckSignatureFrom(chain[i+1]); err != nil {
			return tls.Certificate{}, nil, certRefused(CertFieldCertificate, CertChainBroken,
				fmt.Errorf("certificate %d is not signed by certificate %d: %w", i+1, i+2, err))
		}
	}
	pair.Leaf = chain[0]
	return pair, chain, nil
}

// CheckValidAt refuses a chain any certificate of which is not valid at now:
// `not-yet-valid` before its NotBefore, `expired` after its NotAfter, checked in
// that order, leaf first. The one clock rule, for an upload, for a certificate
// file, and for what the portal serves at this moment (`api.NotServedReason`).
func CheckValidAt(chain []*x509.Certificate, now time.Time) error {
	for _, c := range chain {
		if now.Before(c.NotBefore) {
			return certRefused(CertFieldCertificate, CertNotYetValid, nil)
		}
		if now.After(c.NotAfter) {
			return certRefused(CertFieldCertificate, CertExpired, nil)
		}
	}
	return nil
}

func hasPrivateKeyBlock(keyPEM []byte) bool {
	rest := keyPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		if strings.HasSuffix(block.Type, "PRIVATE KEY") {
			return true
		}
	}
}

// CoversHost refuses a leaf that does not name host. An IP literal is checked
// against the IP SANs, which is what a browser does.
func CoversHost(leaf *x509.Certificate, host string) error {
	if err := leaf.VerifyHostname(host); err != nil {
		return certRefused(CertFieldURL, CertNameNotCovered, err)
	}
	return nil
}

// PortalURLHost is the host of a portal address and whether it is https. The error
// is the `not-a-web-address` refusal.
func PortalURLHost(raw string) (host string, https bool, err error) {
	if err := httpOrigin(raw); err != nil {
		return "", false, err
	}
	u, _ := url.Parse(raw)
	host = u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return host, u.Scheme == "https", nil
}

// CheckWebAddress is the scheme allowlist §4 states for anything a guest's browser
// is pointed at: absolute http(s), no userinfo. Exported for the console's writes,
// which must refuse what Validate refuses at start.
func CheckWebAddress(raw string) error { return httpOrigin(raw) }

// CheckPortalPair is the whole rule for serving a pair at a portal address: the
// address is https, and the certificate covers its host.
func CheckPortalPair(leaf *x509.Certificate, portalURL string) error {
	host, https, err := PortalURLHost(portalURL)
	if err != nil {
		return certRefused(CertFieldURL, CertPortalURLNotHTTPS, err)
	}
	if !https {
		return certRefused(CertFieldURL, CertPortalURLNotHTTPS, nil)
	}
	return CoversHost(leaf, host)
}
