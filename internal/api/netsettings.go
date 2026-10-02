package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/ratelimit"
)

// The network settings in force, and how a write reaches the running process.
//
// ── the rule, and the shape it forces ────────────────────────────────────────
//
// Every operator setting is set in the console, stored in the database, and
// **takes effect without restarting Foyer**. So none of the values here is read
// once at start — not the throttles, not the ceilings, not the portal address —
// each is a value the running process reads per request, and a write is followed by `ReloadNetwork`, which reads
// the store back and swaps the whole set in one atomic store. A request sees either
// the old set or the new one, never half of each.
//
// The field survey that decided "reload on write" over "read per request with a
// cache": Traefik and Caddy both swap a whole configuration atomically on change
// rather than consulting a store per request, and PacketFence's `pfconfig` does the
// same across processes with a namespace invalidation. One process here, so the
// swap is a pointer.
//
// ── the environment is a bootstrap, and loses ────────────────────────────────
//
// `FOYER_PORTAL_URL`, `FOYER_VENUE_URL` and the certificate files are honoured, so
// a deployment that sets them keeps its portal. A value saved in the console
// wins over them, and every answer says which one is in force (`source`).

// Bounds and defaults. The defaults are the figures `hardening.go` declares as
// constants; the bounds are what keeps a setting from being a way round the thing
// it configures.
//
//   - A throttle's burst is at least 1 and its interval at least 100 ms, so no
//     value opens a sign-in form to unlimited guessing; the most permissive allowed
//     is 1 000 back to back and then ten a second, which a NATed venue of a few
//     thousand guests needs and which `internal/identity`'s bound on concurrent
//     argon2id still sits behind.
//   - The console's ceiling starts at one megabyte, because the largest
//     console body the contract accepts (`updatePortalSettings` with a logo) is
//     most of one: a lower figure would refuse a request the contract promises.
//   - The header ceiling is at most 128 KiB, which is what the listeners are built
//     with — `net/http` fixes MaxHeaderBytes when a server is made, so the setting
//     is enforced per request below that fixed outer bound (`LimitHeaders`).
const (
	throttleBurstMin, throttleBurstMax           = 1, 1000
	throttleIntervalMinMs, throttleIntervalMaxMs = 100, 600_000

	portalBytesMin, portalBytesMax       = 8 << 10, 128 << 10
	consoleBytesMin, consoleBytesMax     = 1 << 20, 16 << 20
	anonymousBytesMin, anonymousBytesMax = 8 << 10, 128 << 10
	headerBytesMin                       = 16 << 10

	// DefaultHeaderBytes is the header ceiling a deployment starts with.
	DefaultHeaderBytes = 64 << 10
	// MaxHeaderCeiling is the most the header setting may be, and what a listener
	// serving this package's handlers must set as `http.Server.MaxHeaderBytes`.
	MaxHeaderCeiling = 128 << 10

	// DefaultRadiusMaxServers is `raddb.DefaultConfig().MaxServers`, restated
	// because this package does not import the generator;
	// `TestTheRadiusDefaultsAreTheGenerators` in `internal/apistore` holds the two
	// equal.
	DefaultRadiusMaxServers = 30
	radiusMaxServersMin     = 4
	radiusMaxServersMax     = RadiusMaxServersCeiling

	coaTimeoutMinMs, coaTimeoutMaxMs = 200, 10_000
	coaRetriesMin, coaRetriesMax     = 0, 5
)

// The worker ceiling is a PostgreSQL connection budget, not a thread count.
//
// `internal/raddb` sizes the rlm_sql pool AT the worker count (`sql.conf.tmpl`,
// `pool { max = max_servers }`), so that no worker ever queues on a connection
// instead of on the thread pool — a worker blocked on a pool is a worker not
// answering Status-Server (`docs/rules/security.md` §2). So every worker the
// console allows is a PostgreSQL connection FreeRADIUS may open, and a console
// that allowed 256 against a server sized at `max_connections = 80` would let an
// operator raising the one number the Network screen offers exhaust PostgreSQL
// for the control plane, the reaper and `foyerctl` together, and the first
// symptom would be the console itself failing.
//
// PacketFence owns its database's connection limit as a product setting
// (`pf.conf` `database_advanced.max_connections`, default 1000) and its docs send an
// operator who runs out of connections to raise that one number; the pools that
// consume it are not left to be sized against a limit nobody states. Foyer's limit
// is fixed by `deploy/compose.yaml`, so the ceiling is derived from it, and
// `TestTheWorkerCeilingFitsThePostgresBudget` reads that file: lower
// `max_connections` there and the test is red; raise it and this may rise with it.
// The console keeps its one field and its bound, but the bound cannot promise a
// number the database cannot serve.
const (
	// PostgresMaxConnections is `max_connections` in `deploy/compose.yaml`.
	PostgresMaxConnections = 80
	// postgresSuperuserReserved is PostgreSQL's default
	// `superuser_reserved_connections`; no Foyer login is a superuser, so none of
	// Foyer's connections can use these.
	postgresSuperuserReserved = 3
	// controlPlanePoolConns is `internal/store`'s default pgxpool size (Config.MaxConns).
	controlPlanePoolConns = 25
	// maintenancePoolConns is the reaper's pool (cmd/foyer/maintenance.go, 2) and
	// the retention sweep's (store.OpenRetention, 2).
	maintenancePoolConns = 2 + 2
	// oneShotConns is two `foyerctl` commands at once (doctor, sync-nas and
	// apply-raddb each open a pool of 2), so an operator's diagnosis during an
	// incident is not the connection that tips the server over.
	oneShotConns = 2 * 2

	// RadiusMaxServersCeiling is the most FreeRADIUS workers — and so rlm_sql
	// connections — the console accepts: what PostgreSQL has left once every other
	// login has its pool. 80 − 3 − 25 − 4 − 4 = 44.
	RadiusMaxServersCeiling = PostgresMaxConnections - postgresSuperuserReserved -
		controlPlanePoolConns - maintenancePoolConns - oneShotConns
)

func policyOf(t gen.SignInThrottle) ratelimit.Policy {
	return ratelimit.Policy{Burst: t.Burst, Every: time.Duration(t.IntervalMs) * time.Millisecond}
}

func throttleOf(p ratelimit.Policy) gen.SignInThrottle {
	return gen.SignInThrottle{Burst: p.Burst, IntervalMs: int(p.Every / time.Millisecond)}
}

// DefaultThrottles are the four shipped buckets.
func DefaultThrottles() gen.SignInThrottles {
	return gen.SignInThrottles{
		ConsoleAddress: throttleOf(adminAddressPolicy),
		ConsoleAccount: throttleOf(adminAccountPolicy),
		PortalAddress:  throttleOf(portalAddressPolicy),
		PortalAccount:  throttleOf(portalAccountPolicy),
	}
}

// DefaultBodyCeilings are the shipped ceilings.
func DefaultBodyCeilings() gen.BodyCeilings {
	return gen.BodyCeilings{
		PortalBytes:    int(PortalBodyLimit),
		ConsoleBytes:   int(AdminBodyLimit),
		AnonymousBytes: int(AnonymousBodyLimit),
		HeaderBytes:    DefaultHeaderBytes,
	}
}

// networkBounds are the bounds the console is offered and a write is held to. The
// three body ceilings' maxima are the memory plan's (memplan.go): no
// value an operator can save exceeds what this container can decode.
func (s *Server) networkBounds() gen.NetworkSettingBounds {
	d, c := DefaultThrottles(), s.bodyPlan.clamp(DefaultBodyCeilings())
	p := s.bodyPlan
	return gen.NetworkSettingBounds{
		ThrottleBurst:      gen.NetworkBound{Minimum: throttleBurstMin, Maximum: throttleBurstMax, Default: d.PortalAddress.Burst},
		ThrottleIntervalMs: gen.NetworkBound{Minimum: throttleIntervalMinMs, Maximum: throttleIntervalMaxMs, Default: d.PortalAddress.IntervalMs},
		PortalBytes:        gen.NetworkBound{Minimum: portalBytesMin, Maximum: int(p.smallMax), Default: c.PortalBytes},
		ConsoleBytes:       gen.NetworkBound{Minimum: int(p.consoleMin), Maximum: int(p.consoleMax), Default: c.ConsoleBytes},
		AnonymousBytes:     gen.NetworkBound{Minimum: anonymousBytesMin, Maximum: int(p.smallMax), Default: c.AnonymousBytes},
		HeaderBytes:        gen.NetworkBound{Minimum: headerBytesMin, Maximum: MaxHeaderCeiling, Default: c.HeaderBytes},
	}
}

func radiusMaxServersBound() gen.NetworkBound {
	return gen.NetworkBound{Minimum: radiusMaxServersMin, Maximum: radiusMaxServersMax, Default: DefaultRadiusMaxServers}
}

// NetworkBootstrap is what the deployment supplied before anything was saved in
// the console — `FOYER_PORTAL_URL` or the address derived from the listener, and
// `FOYER_VENUE_URL`.
type NetworkBootstrap struct {
	PortalURL       string
	PortalURLSource gen.NetworkSettingSource
	VenueURL        string
}

// PortalPair is the portal certificate saved in the console, parsed and ready to
// serve. The key is inside `Certificate` and nowhere else.
type PortalPair struct {
	Certificate tls.Certificate
	Info        config.CertificateInfo
	UpdatedAt   time.Time
	UpdatedBy   string
}

// EffectiveNetwork is the whole set in force, swapped atomically.
type EffectiveNetwork struct {
	PortalURL       string
	PortalURLSource gen.NetworkSettingSource
	VenueURL        string
	VenueURLSource  gen.NetworkSettingSource
	Throttles       gen.SignInThrottles
	Ceilings        gen.BodyCeilings
	// PortalCertificate is the console's pair; nil when none is saved, and the
	// deployment's certificate files (if any) are then the runtime's business.
	PortalCertificate *PortalPair
}

// NetworkRuntime is the process that serves what these settings configure — the
// portal listeners in `cmd/foyer`. It is told every new set, and it answers what
// the portal presents, because only the thing serving a certificate knows whether
// it is being served.
type NetworkRuntime interface {
	ApplyNetwork(EffectiveNetwork)
	PortalCertificateStatus() gen.PortalCertificateStatus
}

type netState struct {
	cur       atomic.Pointer[EffectiveNetwork]
	bootstrap NetworkBootstrap
	runtime   NetworkRuntime
}

// AttachNetwork names the runtime after construction, for a composition root whose
// runtime needs this server to build its handlers. Call it before serving, and
// before the first ReloadNetwork.
func (s *Server) AttachNetwork(r NetworkRuntime) { s.net.runtime = r }

func (s *Server) network() *EffectiveNetwork {
	if e := s.net.cur.Load(); e != nil {
		return e
	}
	e := s.fromBootstrap()
	return &e
}

func (s *Server) fromBootstrap() EffectiveNetwork {
	b := s.net.bootstrap
	e := EffectiveNetwork{
		PortalURL:       b.PortalURL,
		PortalURLSource: b.PortalURLSource,
		VenueURL:        b.VenueURL,
		VenueURLSource:  gen.NetworkSettingSourceUnset,
		Throttles:       DefaultThrottles(),
		Ceilings:        s.bodyPlan.clamp(DefaultBodyCeilings()),
	}
	if e.PortalURLSource == "" {
		e.PortalURLSource = gen.NetworkSettingSourceEnvironment
	}
	if e.PortalURL == "" {
		e.PortalURLSource = gen.NetworkSettingSourceUnset
	}
	if b.VenueURL != "" {
		e.VenueURLSource = gen.NetworkSettingSourceEnvironment
	}
	return e
}

// PortalURL is the portal's public address in force, without a trailing slash.
func (s *Server) PortalURL() string { return strings.TrimRight(s.network().PortalURL, "/") }

// VenueURL is RFC 8908's venue-info-url in force; empty is none.
func (s *Server) VenueURL() string { return s.network().VenueURL }

// ReloadNetwork reads the saved settings and puts them in force. Called at start by
// `cmd/foyer` and after every write in this package.
//
// A stored certificate that no longer loads — a key sealed under a retired KEK —
// is left out rather than failing the reload: the rest of the settings must still
// take effect, and the runtime reports the portal as serving what it can.
func (s *Server) ReloadNetwork(ctx context.Context) error {
	eff := s.fromBootstrap()
	if s.ports.Network != nil {
		stored, err := s.ports.Network.StoredNetwork(ctx)
		if err != nil {
			return err
		}
		if stored.PortalURL != nil {
			eff.PortalURL = *stored.PortalURL
			eff.PortalURLSource = gen.NetworkSettingSourceConsole
		}
		if stored.VenueURL != nil {
			eff.VenueURL = *stored.VenueURL
			eff.VenueURLSource = gen.NetworkSettingSourceConsole
		}
		if stored.Throttles != nil {
			eff.Throttles = *stored.Throttles
		}
		if stored.BodyCeilings != nil {
			// Held to this container's plan: a value saved on a larger one must
			// not outrun the memory this one has.
			eff.Ceilings = s.bodyPlan.clamp(*stored.BodyCeilings)
		}
		pair, err := s.ports.Network.PortalCertificate(ctx)
		switch {
		case err == nil:
			if p, perr := servablePair(pair); perr == nil {
				eff.PortalCertificate = p
			}
		case errors.Is(err, ErrNotFound):
		default:
			return err
		}
	}
	s.putNetwork(eff)
	return nil
}

func (s *Server) putNetwork(eff EffectiveNetwork) {
	s.net.cur.Store(&eff)
	s.throttle.set(eff.Throttles, s.now())
	if s.net.runtime != nil {
		s.net.runtime.ApplyNetwork(eff)
	}
}

// servablePair parses a stored pair for serving, with the checks every pair gets
// that do not read the clock (`config.ParseCertificateChain`). The time is not a
// refusal here — the runtime reports an expired or not-yet-valid certificate as
// not served, which is a state an operator must be able to see rather than one
// that silently vanishes.
func servablePair(c StoredCertificate) (*PortalPair, error) {
	pair, chain, err := config.ParseCertificateChain([]byte(c.CertificatePEM), c.KeyPEM)
	if err != nil {
		return nil, err
	}
	return &PortalPair{
		Certificate: pair,
		Info:        config.DescribeCertificate(chain[0], len(chain)),
		UpdatedAt:   c.UpdatedAt,
		UpdatedBy:   c.UpdatedBy,
	}, nil
}

// portalCertificateStatus is what the portal presents: the runtime's answer when
// there is a runtime, and otherwise the stored pair described against the address
// in force — which is what a runtime would serve.
func (s *Server) portalCertificateStatus() gen.PortalCertificateStatus {
	if s.net.runtime != nil {
		return s.net.runtime.PortalCertificateStatus()
	}
	return DescribePortalPair(s.network().PortalCertificate, s.PortalURL(), s.now())
}

// DescribePortalPair is the status of a console pair at an address, for a runtime
// that serves it. Exported so `cmd/foyer` describes what it serves in the same
// words this package would.
func DescribePortalPair(p *PortalPair, portalURL string, now time.Time) gen.PortalCertificateStatus {
	if p == nil {
		return gen.PortalCertificateStatus{State: gen.PortalCertificateStateNone, Source: gen.PortalCertificateStatusSourceNone}
	}
	st := describeInfo(p.Info, gen.PortalCertificateStatusSourceConsole)
	st.UpdatedAt, st.UpdatedBy = ptr(p.UpdatedAt), ptr(p.UpdatedBy)
	if reason, ok := NotServedReason(p.Certificate.Leaf, portalURL, now); !ok {
		st.State = gen.PortalCertificateStateNotServed
		st.NotServedReason = &reason
	}
	return st
}

// DescribeCertificateInfo is a status for a certificate from any source, served.
func DescribeCertificateInfo(info config.CertificateInfo, source gen.PortalCertificateStatusSource) gen.PortalCertificateStatus {
	return describeInfo(info, source)
}

func describeInfo(info config.CertificateInfo, source gen.PortalCertificateStatusSource) gen.PortalCertificateStatus {
	return gen.PortalCertificateStatus{
		State:             gen.PortalCertificateStateServed,
		Source:            source,
		KeySet:            true,
		Subject:           ptr(info.Subject),
		Issuer:            ptr(info.Issuer),
		DnsNames:          ptr(info.DNSNames),
		IpAddresses:       ptr(info.IPAddresses),
		NotBefore:         ptr(info.NotBefore),
		NotAfter:          ptr(info.NotAfter),
		FingerprintSha256: ptr(info.FingerprintSHA256),
		ChainLength:       ptr(info.ChainLength),
	}
}

// NotServedReason answers why a leaf cannot be served at an address, if it cannot.
//
// The clock rule is `config.CheckValidAt`, the one an upload and a certificate
// file are held to: before its NotBefore a leaf is `not-yet-valid`, after its
// NotAfter `expired`. It is asked per handshake, so a pair that becomes valid is
// served from that moment and one that expires stops being served at once.
func NotServedReason(leaf *x509.Certificate, portalURL string, now time.Time) (gen.PortalCertificateStatusNotServedReason, bool) {
	if leaf == nil {
		return gen.PortalCertificateStatusNotServedReasonNameNotCovered, false
	}
	var ce *config.CertificateError
	if err := config.CheckValidAt([]*x509.Certificate{leaf}, now); errors.As(err, &ce) {
		if ce.Fault == config.CertNotYetValid {
			return gen.PortalCertificateStatusNotServedReasonNotYetValid, false
		}
		return gen.PortalCertificateStatusNotServedReasonExpired, false
	}
	if err := config.CheckPortalPair(leaf, portalURL); errors.As(err, &ce) {
		if ce.Fault == config.CertNameNotCovered {
			return gen.PortalCertificateStatusNotServedReasonNameNotCovered, false
		}
		return gen.PortalCertificateStatusNotServedReasonPortalUrlNotHttps, false
	}
	return "", true
}

// ── the throttles and ceilings, read per request ─────────────────────────────

func (t *signInThrottle) set(p gen.SignInThrottles, now time.Time) {
	t.adminAddress.SetPolicy(policyOf(p.ConsoleAddress), now)
	t.adminAccount.SetPolicy(policyOf(p.ConsoleAccount), now)
	// The trusted devices' buckets are the same setting: a device is trusted to be
	// the account's owner, not to guess faster.
	t.adminDevice.SetPolicy(policyOf(p.ConsoleAccount), now)
	t.portalAddress.SetPolicy(policyOf(p.PortalAddress), now)
	t.portalAccount.SetPolicy(policyOf(p.PortalAccount), now)
}

func (s *Server) ceilings() gen.BodyCeilings { return s.network().Ceilings }

// PortalListenerBodyLimit is the portal listener's body ceiling in force.
func (s *Server) PortalListenerBodyLimit() int64 { return int64(s.ceilings().PortalBytes) }

// AdminListenerBodyLimit is the largest body any console operation accepts now:
// the console's ceiling, or the hero image's when that is larger.
func (s *Server) AdminListenerBodyLimit() int64 {
	if c := int64(s.ceilings().ConsoleBytes); c > heroImageBodyLimit {
		return c
	}
	return heroImageBodyLimit
}

// LimitBody is `http.MaxBytesHandler` with a ceiling read per request, for a
// listener whose ceiling an operator can change while it serves.
func LimitBody(h http.Handler, limit func() int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := *r
		r2.Body = http.MaxBytesReader(w, r.Body, limit())
		h.ServeHTTP(w, &r2)
	})
}

// LimitHeaders refuses a request whose request line and headers exceed the header
// ceiling in force, with `431`. The listener's own `MaxHeaderBytes` is
// MaxHeaderCeiling, fixed when it was built; this is the setting below it.
func (s *Server) LimitHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if headerSize(r) > s.ceilings().HeaderBytes {
			w.Header().Set("Connection", "close")
			http.Error(w, "Request header fields too large.", http.StatusRequestHeaderFieldsTooLarge)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// headerSize is what `net/http` counts against MaxHeaderBytes, near enough: the
// request line and every field with its separators.
func headerSize(r *http.Request) int {
	n := len(r.Method) + len(r.RequestURI) + len(r.Proto) + 4
	for k, vs := range r.Header {
		for _, v := range vs {
			n += len(k) + len(v) + 4
		}
	}
	return n
}
