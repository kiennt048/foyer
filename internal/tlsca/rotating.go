package tlsca

import (
	"crypto/tls"
	"sync"
	"time"
)

// Rotating runs the renewal check on every handshake instead of once at startup.
//
// # Why this exists, and why it is not optional
//
// EnsureSelfSigned checks expiry at the moment it is called, so a process that
// starts with a fresh certificate and then runs for 825 days serves an expired one
// and never notices. It is tempting to call that a limitation bounded by "an
// assumption about restart cadence" — a box that restarts every couple of months
// renews in time.
//
// **That assumption is false for what `deploy/` installs.** There is no timer, no
// cron entry, no unattended reboot and no periodic pull anywhere in `deploy/`;
// `restart: unless-stopped` survives a reboot and never causes one. So on such a
// host nothing restarts the control plane, and the start-time check is a
// guarantee about starts rather than about service.
//
// No value of RenewBeforeDays fixes that, which is the useful thing to notice: a
// window is a bet on a restart cadence, and against an unbounded gap every bet
// loses. Widening it to a year would only rotate the operator's fingerprint sooner
// while leaving the hole exactly where it is. The remedy has to be a check that
// runs without a restart, and a TLS handshake is the only thing that reliably
// happens on this path.
//
// 825 days is about 27 months, so nobody will be watching when it arrives. That is
// the argument for having this now rather than after the first install has been up
// two years.
//
// # This is inert until the caller uses it
//
// A tls.Config carrying Certificates and not GetCertificate never calls this, and
// nothing here can tell. The caller adopts it by building the config with
// GetCertificate; see the example in the method's own doc.
type Rotating struct {
	certPath, keyPath string

	// required is the deployment's declared names, kept because a renewal is a
	// fresh certificate and a fresh certificate that dropped them would be a
	// console that works until the day it rotates.
	required []string

	// generated is what the construction-time EnsureSelfSigned reported, kept for
	// the caller's one startup line. It is not updated by later rotations: it
	// answers "what happened at startup", and a later rotation is a different
	// event that Describe would word differently.
	generated bool

	// now is a seam for tests only. Nothing else may set it: a clock this package
	// controls is how a test observes a rotation that would otherwise take two
	// years to arrive, and it must never become a way for a caller to move the
	// expiry of a certificate a browser will judge by the real clock.
	now func() time.Time

	mu sync.Mutex
	// cert is swapped, never mutated, so a handshake holding the old pointer is
	// unaffected by a rotation happening beside it.
	cert *tls.Certificate
	// nextAttempt throttles retries after a failed renewal. Without it, an expired
	// certificate on a read-only filesystem means a key generation and two failed
	// writes per TLS handshake, which turns a certificate problem into a load
	// problem on the one port an operator would use to fix it.
	nextAttempt time.Time
}

// retryInterval is how long a failed renewal waits before trying again. Long
// enough that a broken filesystem cannot be hammered, short enough that fixing the
// filesystem takes effect without a restart — which is the entire point of this
// type.
const retryInterval = time.Minute

// NewRotating loads or generates the pair exactly as EnsureSelfSigned does, and
// returns a holder that will keep it fresh.
//
// Construction-time errors are startup errors and are returned unchanged: a
// console that cannot make its first certificate must fail loudly, not serve
// handshake errors later.
func NewRotating(certPath, keyPath string, required []string) (*Rotating, error) {
	cert, generated, err := EnsureSelfSigned(certPath, keyPath, required)
	if err != nil {
		return nil, err
	}
	return &Rotating{
		certPath:  certPath,
		keyPath:   keyPath,
		required:  required,
		generated: generated,
		now:       time.Now,
		cert:      &cert,
	}, nil
}

// Initial returns what EnsureSelfSigned returned at construction: the certificate
// and whether it was generated. Pass both to Describe for the one startup line.
func (r *Rotating) Initial() (tls.Certificate, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.cert, r.generated
}

// GetCertificate answers a TLS handshake, renewing first if the certificate has
// reached its renewal window.
//
//	srv.TLSConfig = &tls.Config{
//		GetCertificate: rotating.GetCertificate,
//		MinVersion:     tls.VersionTLS12,
//	}
//
// **Never leave Certificates set alongside it as a fallback.** Go prefers
// GetCertificate, so the stale copy would be dormant rather than wrong — until
// somebody reorders the config and the console silently goes back to serving the
// certificate it had at startup, which is the exact defect this type removes and
// which nothing would report.
//
// It never fails a handshake over a renewal it could not perform. An expired
// certificate reaches the browser as a dated, diagnosable warning; a handshake
// error reaches it as a bare TLS alert on the one port an operator would use to
// investigate. The first is recoverable by a human and the second is not, so a
// failed renewal keeps serving what it has.
func (r *Rotating) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if !needsRenewal(r.cert.Leaf, r.required, now) || now.Before(r.nextAttempt) {
		return r.cert, nil
	}

	fresh, err := renew(r.certPath, r.keyPath, r.required, now)
	if err != nil {
		// Keep serving. The next attempt is a minute away rather than the next
		// handshake, so a read-only disk costs one failed renewal a minute
		// instead of one per connection.
		r.nextAttempt = now.Add(retryInterval)
		return r.cert, nil
	}
	r.cert = &fresh
	r.nextAttempt = time.Time{}
	return r.cert, nil
}
