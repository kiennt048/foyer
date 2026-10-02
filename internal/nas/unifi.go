package nas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UniFiFlavour is which of the two UniFi deployment shapes a controller is.
//
// It is *probed, never asked*. The two shapes differ in the login path, in the
// URL prefix every subsequent call needs, and in whether a CSRF token must be
// echoed — three things an operator cannot be expected to know and which a
// single unauthenticated request settles.
type UniFiFlavour string

const (
	// FlavourUnknown means the flavour has not been probed yet.
	FlavourUnknown UniFiFlavour = ""
	// FlavourUniFiOS is a UniFi OS console (UDM, UDM-Pro, UNVR, Cloud Key
	// gen2+, UniFi OS Server). Ports 80/443, login /api/auth/login, every
	// Network call behind /proxy/network, and cmd/stamgr needs X-CSRF-Token.
	FlavourUniFiOS UniFiFlavour = "unifi-os"
	// FlavourStandalone is a self-hosted Network Application. Port 8443,
	// login /api/login, no /proxy/network prefix.
	FlavourStandalone UniFiFlavour = "standalone"
)

const (
	// unifiOSStatusPath answering 200 or 401 is the UniFi OS tell.
	// DOCUMENTED — PacketFence pf::Switch::Ubiquiti::Unifi::_connect().
	unifiOSStatusPath = "/proxy/network/status"

	unifiOSNetworkPrefix = "/proxy/network"
	unifiOSLoginPath     = "/api/auth/login"
	standaloneLoginPath  = "/api/login"

	// unifiOSIntegrationPrefix is where the Integration API lives on UniFi OS.
	// A client that built {baseURL}/v1/sites/… could not reach any UniFi OS
	// controller at all: on UOSSERVER 1.0.1,
	// /proxy/network/integration/v1/sites answers 401 and /v1/sites does not exist.
	unifiOSIntegrationPrefix = "/proxy/network/integration"

	// standaloneIntegrationPrefix is the same API without the UniFi OS reverse
	// proxy in front of it. UNVERIFIED: derived from the fact that /proxy/network
	// is added by UniFi OS rather than served by the Network application, not
	// measured on a standalone controller. `foyerctl probe-unifi` tries every
	// candidate and prints which one answered, which is what settles it.
	standaloneIntegrationPrefix = "/integration"

	// UniFiIntegrationAPIMinVersion is the Network Application version that
	// first shipped the Integration API. Below it the legacy private API and a
	// local admin username and password are the only path.
	// DOCUMENTED — Ubiquiti Help Center article 31228198640023.
	UniFiIntegrationAPIMinVersion = "9.1.105"
)

// integrationCandidates lists the Integration API prefixes to try for a
// flavour, most likely first.
func integrationCandidates(f UniFiFlavour) []string {
	switch f {
	case FlavourUniFiOS:
		return []string{unifiOSIntegrationPrefix}
	case FlavourStandalone:
		return []string{standaloneIntegrationPrefix, unifiOSIntegrationPrefix}
	default:
		return []string{unifiOSIntegrationPrefix, standaloneIntegrationPrefix}
	}
}

// TLSMode is how the driver treats the controller's certificate.
//
// It is a field because controllers ship self-signed certificates and every
// shipped integration disables verification — PacketFence sets
// verify_hostname => 0, TP-Link's own PHP sample sets CURLOPT_SSL_VERIFYPEER
// = FALSE. A registration form offering no choice fails on first contact for
// most deployments; one that silently disables verification hands an on-path
// attacker the controller API key. So: default to the safe thing, and make the
// unsafe choice a value the operator had to type.
type TLSMode string

const (
	// TLSVerify is the default: the platform trust store, hostname verified.
	TLSVerify TLSMode = "verify"
	// TLSPinnedSHA256 verifies the certificate against a SHA-256 fingerprint the
	// operator recorded at registration. This is the right answer for an on-prem
	// controller with a self-signed certificate: it survives a self-signed chain
	// without surrendering to an on-path attacker.
	TLSPinnedSHA256 TLSMode = "pinned-sha256"
	// TLSInsecureSkipVerify disables certificate verification entirely. Explicit,
	// surfaced as a Probe warning, and never a default.
	TLSInsecureSkipVerify TLSMode = "insecure-skip-verify"
	// TLSTrustOnFirstUse pins whatever the controller presents on first contact
	// and refuses a change afterwards. It is `pinned-sha256` with the pin learned
	// rather than typed, and once learned the two are the same code path.
	//
	// It exists because it is the only mode a real controller can be registered
	// with: `web/contract/openapi.yaml` and migration `00004` both permit exactly
	// `verify` and `trust-on-first-use`, controllers ship self-signed certificates
	// so `verify` cannot succeed, and this driver implemented neither of the
	// remaining two under those names. Measured against a UniFi OS
	// Server: registering the documented way and probing answered
	// `500 unifi: unknown TLS mode "trust-on-first-use"`.
	//
	// FIRST CONTACT IS THE TRUSTED MOMENT and nothing here hides that. An attacker
	// present at registration is pinned instead of the controller. What TOFU buys
	// is that every contact after the first is verified against what the operator
	// actually registered, which `insecure-skip-verify` never is.
	TLSTrustOnFirstUse TLSMode = "trust-on-first-use"
)

// UniFi registers its own capability-read factory, so `driver.go` has no table
// naming it. See RegisterShippedDriver: three `switch vendor` arms in the core
// would make *"a new driver is one file with no core changes"* false by three
// lines every time.
func init() {
	RegisterShippedDriver(VendorUniFi, func() NASDriver {
		return NewUniFiDriver(UniFiConfig{BaseURL: capabilityReadURL})
	})
}

// UniFiDriver implements NASDriver for Ubiquiti UniFi Controllers.
type UniFiDriver struct {
	baseURL    string
	apiKey     string
	username   string
	password   string
	site       string
	tlsMode    TLSMode
	httpClient *http.Client

	// seen carries the certificate fingerprint the first handshake observed. A
	// trust-on-first-use registration reads it afterwards and stores it, and every
	// contact after that is verified against it. See ObservedFingerprint.
	seen *observedCert

	// requireAPMatch turns an *unconfirmable* access-point claim into a refusal.
	// A contradicted one is refused whatever this says. See UniFiConfig.
	requireAPMatch bool

	// coaFallback, when set, is used by DeauthorizeGuest if the controller API
	// is unreachable. UNVERIFIED against hardware — see coa.go.
	coaFallback *CoAClient

	mu                sync.RWMutex
	flavour           UniFiFlavour
	integrationPrefix string
	csrfToken         string
	loggedIn          bool

	// deviceMaps caches one device-id → MAC map per site, so the access-point
	// check `security.md` §4 requires costs one controller call per site per
	// unifiDeviceMapTTL rather than one per grant. See unifi_devicemap.go.
	//
	// Its own mutex, not mu: building a map calls integrationBase, which takes
	// mu, and a driver that deadlocked on its first grant would be a security
	// check that turns into an outage.
	deviceMu   sync.Mutex
	deviceMaps map[string]*unifiDeviceMap

	// now is the driver's clock, nil meaning time.Now. Present so the cache's
	// bound can be tested as a bound instead of with a sleep.
	now func() time.Time
}

// UniFiConfig holds connection settings for a UniFi Controller.
type UniFiConfig struct {
	BaseURL  string
	APIKey   string
	Username string
	Password string
	Site     string

	// Flavour, when set, skips detection. Leave empty and the driver probes.
	Flavour UniFiFlavour
	// IntegrationPrefix overrides the per-flavour default. Set it from what
	// `foyerctl probe-unifi` reported for this controller.
	IntegrationPrefix string

	TLSMode TLSMode
	// PinnedSHA256 is the certificate fingerprint, hex, colons optional.
	// Required when TLSMode is TLSPinnedSHA256.
	PinnedSHA256 string

	// Insecure is the pre-existing spelling of TLSInsecureSkipVerify and is
	// kept so callers do not break. Prefer TLSMode: the named constant is
	// legible in a config file, a bool is not.
	Insecure bool

	Timeout time.Duration

	// RequireAPMatch refuses a grant whose claimed access point could not be
	// **resolved**. It does not govern a mismatch: a redirect naming an access
	// point the controller says this client is not under is refused with
	// PresenceAPMismatch whatever this is set to, because a switch that could
	// turn that off would be the AP-mismatch bypass with a flag on it.
	//
	// What is left for it to govern is the residue — a client the controller
	// places under no device, or an uplink id the site has no device for. Those
	// pass by default with `Presence.APConfirmed` false, which is honest and is
	// what the driver does wherever it cannot compare. Turning this on says
	// *this venue scopes policy by access point, so an unverifiable claim is not
	// good enough*, and costs guests on any controller that does not carry
	// `uplinkDeviceId`.
	RequireAPMatch bool

	// CoAFallback, when set, lets DeauthorizeGuest fall back to RFC 5176
	// Disconnect if the controller API is unreachable. UniFi's captive-portal
	// *authorization* has no RADIUS equivalent; revocation does.
	CoAFallback *CoAClient
}

// NewUniFiDriver creates a new UniFi Controller driver instance.
func NewUniFiDriver(cfg UniFiConfig) *UniFiDriver {
	d, err := NewUniFiDriverWithError(cfg)
	if err != nil {
		// Preserved for callers that cannot handle an error here. The driver is
		// returned unusable rather than silently downgraded to no verification:
		// every call fails with the configuration error attached.
		return &UniFiDriver{
			baseURL: strings.TrimRight(cfg.BaseURL, "/"),
			site:    defaultString(cfg.Site, "default"),
			tlsMode: cfg.TLSMode,
			// An inert driver never handshakes, so it observes nothing. The
			// carrier is present rather than nil so ObservedFingerprint answers ""
			// instead of panicking on a driver a caller kept hold of.
			seen:       &observedCert{},
			httpClient: &http.Client{Transport: errTransport{err: err}, Timeout: time.Second},
		}
	}
	return d
}

// NewUniFiDriverWithError creates a driver and reports a configuration that
// cannot be honoured — a pinned mode with no fingerprint, an unparsable URL.
func NewUniFiDriverWithError(cfg UniFiConfig) (*UniFiDriver, error) {
	if cfg.Site == "" {
		cfg.Site = "default"
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		return nil, errors.New("unifi: BaseURL is required")
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("unifi: BaseURL is not a URL: %w", err)
	}

	mode := cfg.TLSMode
	if mode == "" {
		mode = TLSVerify
		if cfg.Insecure {
			mode = TLSInsecureSkipVerify
		}
	}

	seen := &observedCert{}
	tlsCfg, err := buildTLSConfig(mode, cfg.PinnedSHA256, seen)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// A cookie jar is not optional on the legacy path: both flavours answer the
	// login call with a session cookie and expect it back on cmd/stamgr.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("unifi: cookie jar: %w", err)
	}

	return &UniFiDriver{
		baseURL:           baseURL,
		apiKey:            cfg.APIKey,
		username:          cfg.Username,
		password:          cfg.Password,
		site:              cfg.Site,
		tlsMode:           mode,
		seen:              seen,
		flavour:           cfg.Flavour,
		integrationPrefix: cfg.IntegrationPrefix,
		requireAPMatch:    cfg.RequireAPMatch,
		coaFallback:       cfg.CoAFallback,
		httpClient: &http.Client{
			Jar:       jar,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
			Timeout:   timeout,
		},
	}, nil
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// errTransport fails every request with one configuration error, so a
// misconfigured driver is inert rather than quietly insecure.
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// observedCert records the leaf fingerprint a handshake presented, so a
// trust-on-first-use registration can persist what it pinned.
//
// It is written from the TLS callback and read after the request completes, so it
// carries its own mutex: `http.Transport` may dial on another goroutine.
type observedCert struct {
	mu  sync.Mutex
	hex string
}

func (o *observedCert) set(sum [sha256.Size]byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.hex == "" {
		o.hex = hex.EncodeToString(sum[:])
	}
}

// Fingerprint is the leaf certificate seen on the first handshake this client
// made, lower-case hex, or "" if no TLS handshake has happened.
func (o *observedCert) Fingerprint() string {
	if o == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hex
}

func buildTLSConfig(mode TLSMode, pinned string, seen *observedCert) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch mode {
	case TLSVerify:
		return cfg, nil
	case TLSInsecureSkipVerify:
		cfg.InsecureSkipVerify = true
		return cfg, nil
	case TLSTrustOnFirstUse:
		if strings.TrimSpace(pinned) == "" {
			// Nothing pinned yet: this is the first contact. Accept what is
			// presented and record it, so the caller can store it and every later
			// contact is verified against it.
			//
			// The acceptance is bounded to this one moment by the CALLER, which
			// must persist Fingerprint() before using the driver again. A caller
			// that forgets has built insecure-skip-verify with extra steps, and
			// that is the failure mode to watch for rather than this branch.
			cfg.InsecureSkipVerify = true
			cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("unifi: the controller presented no certificate")
				}
				seen.set(sha256.Sum256(rawCerts[0]))
				return nil
			}
			return cfg, nil
		}
		// Something is pinned: from here TOFU and pinned-sha256 are the same
		// check, deliberately sharing one implementation so the strict half of
		// TOFU cannot drift from the mode it is supposed to equal.
		fallthrough
	case TLSPinnedSHA256:
		want, err := parseFingerprint(pinned)
		if err != nil {
			return nil, err
		}
		// The platform verifier is turned off because the chain is self-signed
		// by construction; the fingerprint below is the verification, and it is
		// stricter than the chain check it replaces.
		cfg.InsecureSkipVerify = true
		// **The leaf, and only the leaf.** `rawCerts[0]` is the one certificate
		// whose private key the peer proved it holds, in the handshake's
		// CertificateVerify; everything after it is a list the peer typed. With
		// the platform verifier off nothing checks that the rest of the chain
		// even signs the leaf, so accepting a match anywhere in it accepts
		// `[attacker-leaf, real-controller-cert]` — the controller's certificate
		// is public, anyone who has ever connected to it holds a copy — and hands
		// the API key to whoever is on path.
		//
		// The field agrees: a pin is on the end-entity certificate or on its
		// SPKI, never "somewhere in what was sent". OkHttp's CertificatePinner
		// pins against the *cleaned*, verified chain for that reason, and with no
		// verified chain here the leaf is the only certificate that proves
		// anything. `TestUniFiPinIsCheckedAgainstTheLeafOnly` drives the forged
		// chain through a real handshake.
		cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("unifi: the controller presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			seen.set(sum)
			if subtle.ConstantTimeCompare(sum[:], want) == 1 {
				return nil
			}
			return errors.New("unifi: server certificate does not match the pinned SHA-256 fingerprint")
		}
		return cfg, nil
	default:
		return nil, fmt.Errorf("unifi: unknown TLS mode %q", mode)
	}
}

func parseFingerprint(s string) ([]byte, error) {
	clean := strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s))
	if clean == "" {
		return nil, errors.New("unifi: TLS mode pinned-sha256 needs PinnedSHA256")
	}
	raw, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("unifi: PinnedSHA256 is not hex: %w", err)
	}
	if len(raw) != sha256.Size {
		return nil, fmt.Errorf("unifi: PinnedSHA256 is %d bytes, want %d", len(raw), sha256.Size)
	}
	return raw, nil
}

func (u *UniFiDriver) Vendor() Vendor {
	return VendorUniFi
}

// Capabilities reports what this driver can do on the operator's behalf.
//
// Two of these values are not measured. They are returned anyway — the type has
// no third state — and CapabilityEvidence is where that is said out loud. Do not
// promote one here without taking the measurement its Note names.
func (u *UniFiDriver) Capabilities() Capabilities {
	return Capabilities{
		CanAuthorizeGuest: true,
		CanDeauthGuest:    true,
		HardQuota:         true,
		BandwidthLimits:   true,
		// UniFi's guest pre-authorization allowance is configured on the
		// controller, and no documented Integration API endpoint programs it.
		// So the *driver* cannot maintain a garden — which is what this field
		// claims — even though the controller has the feature.
		WalledGarden: WalledGardenNone,
		// MEASURED true on the Integration API: a second AUTHORIZE_GUEST_ACCESS
		// replaced the live authorization's limits while `connectedAt` stayed put.
		// **Only that path was measured.** The legacy cmd/stamgr path a local
		// admin account drives is a different endpoint nobody has re-issued on a
		// live client, so it stays false there — the narrow reading of the
		// measurement, not a
		// guess in either direction. See CapabilityEvidence and Reauthorize.
		LiveReauthorize: !u.legacyMode(),
		// The narrow claim: this gates only *pushing* a secret to the device.
		// Generating a new secret is Foyer's own work and must stay available,
		// or a UniFi site could never rotate one at all.
		RotateSecret: false,
	}
}

// CapabilityEvidence implements EvidencedDriver.
func (u *UniFiDriver) CapabilityEvidence() CapabilityEvidence {
	const article = "Ubiquiti Help Center article 31228198640023 (External Hotspot API / Integration API)"
	return CapabilityEvidence{
		CapCanAuthorizeGuest: {
			Evidence: EvidenceDocumented,
			Note:     "POST /v1/sites/{siteId}/clients/{clientId}/actions with AUTHORIZE_GUEST_ACCESS — " + article,
		},
		CapCanDeauthGuest: {
			Evidence: EvidenceDocumented,
			Note:     "the same action endpoint with UNAUTHORIZE_GUEST_ACCESS — " + article,
		},
		CapHardQuota: {
			Evidence: EvidenceDocumented,
			Note:     "the authorize action takes timeLimitMinutes and dataUsageLimitMBytes — " + article,
		},
		CapBandwidthLimits: {
			Evidence: EvidenceDocumented,
			Note:     "the authorize action takes rxRateLimitKbps and txRateLimitKbps — " + article,
		},
		CapWalledGarden: {
			Evidence: EvidenceUnverified,
			Note: "no documented Integration API endpoint programs UniFi's guest pre-authorization " +
				"allowance. Settled by: enumerate the Integration API's site-settings operations on a " +
				"real controller and look for one that writes the allowance list.",
		},
		CapLiveReauthorize: u.liveReauthorizeEvidence(),
		CapRotateSecret: {
			Evidence: EvidenceUnverified,
			Note: "no documented Integration API operation writes the RADIUS profile's shared secret. " +
				"Settled by: enumerate the site-settings operations on a real controller. This gates the *push* " +
				"only — generating a secret stays Foyer's own work either way.",
		},
	}
}

// liveReauthorizeEvidence is the live re-authorization measurement, read narrowly.
//
// It was measured on the Integration API: `connectedAt` unchanged across two
// AUTHORIZE_GUEST_ACCESS calls with different limits, and the response carrying
// `revokedAuthorization` beside `grantedAuthorization` — the authorization is
// replaced, the association is not touched. That is a fact about that endpoint.
// A driver holding only a local admin account never calls it; it issues
// `cmd/stamgr authorize-guest`, and nobody has re-issued that on a live client.
func (u *UniFiDriver) liveReauthorizeEvidence() CapabilityFact {
	if u.legacyMode() {
		return CapabilityFact{
			Evidence: EvidenceUnverified,
			Note: "Live re-authorization was measured on the Integration API only; this controller is reached with a local admin " +
				"account, whose cmd/stamgr authorize-guest has never been re-issued on a live client. " +
				"Settled by: authorize a joined client twice through cmd/stamgr with different limits and " +
				"record whether stat/sta's assoc_time survives the second call.",
		}
	}
	return CapabilityFact{
		Evidence: EvidenceMeasured,
		Note: "Measured against a UniFi Network Application on the Integration API: " +
			"a second AUTHORIZE_GUEST_ACCESS with different limits replaced the authorization while " +
			"the client's connectedAt stayed unchanged. Re-authorizing also resets the authorization's usage counters.",
	}
}

// legacyMode reports whether this driver reaches the controller through the
// legacy private API only — a local admin account and no API key. Every call
// that has an Integration API form takes the legacy one in this mode, which is
// the sole path below Network Application 9.1.105.
func (u *UniFiDriver) legacyMode() bool { return u.apiKey == "" && u.username != "" }

// Reauthorize implements Reauthorizer: a second AUTHORIZE_GUEST_ACCESS, which
// was measured to replace a live guest's limits without disconnecting it.
//
// Refused in legacy mode rather than attempted, for the reason
// liveReauthorizeEvidence gives: `Capabilities.LiveReauthorize` is false there,
// and an action the capability model says is unavailable must not quietly
// happen anyway.
func (u *UniFiDriver) Reauthorize(ctx context.Context, req AuthorizeRequest) error {
	if u.legacyMode() {
		return &UnsupportedActionError{Vendor: VendorUniFi, Action: "reauthorize a live guest",
			Reason: "only the Integration API's re-authorization was measured; this controller is reached with a local admin account"}
	}
	return u.AuthorizeGuest(ctx, req)
}

var _ Reauthorizer = (*UniFiDriver)(nil)

// ---------------------------------------------------------------------------
// URL construction
// ---------------------------------------------------------------------------

// detectFlavour probes the controller shape. Cached after the first call.
func (u *UniFiDriver) detectFlavour(ctx context.Context) UniFiFlavour {
	u.mu.RLock()
	f := u.flavour
	u.mu.RUnlock()
	if f != FlavourUnknown {
		return f
	}

	f = FlavourStandalone
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.baseURL+unifiOSStatusPath, nil)
	if err == nil {
		resp, err := u.httpClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			// 200 or 401 both mean the UniFi OS reverse proxy answered. A 401 is
			// the *positive* signal here, which is why this cannot be written as
			// "did the request succeed".
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				f = FlavourUniFiOS
			}
		}
	}

	u.mu.Lock()
	if u.flavour == FlavourUnknown {
		u.flavour = f
	}
	f = u.flavour
	u.mu.Unlock()
	return f
}

// integrationBase returns the Integration API base, probing the flavour if it
// is not yet known.
func (u *UniFiDriver) integrationBase(ctx context.Context) string {
	u.mu.RLock()
	prefix := u.integrationPrefix
	u.mu.RUnlock()
	if prefix == "" {
		prefix = integrationCandidates(u.detectFlavour(ctx))[0]
	}
	return u.baseURL + prefix + "/v1"
}

// networkBase returns the base for the legacy private API (cmd/stamgr).
func (u *UniFiDriver) networkBase(ctx context.Context) string {
	if u.detectFlavour(ctx) == FlavourUniFiOS {
		return u.baseURL + unifiOSNetworkPrefix
	}
	return u.baseURL
}

func (u *UniFiDriver) loginPath(ctx context.Context) string {
	if u.detectFlavour(ctx) == FlavourUniFiOS {
		return u.baseURL + unifiOSLoginPath
	}
	return u.baseURL + standaloneLoginPath
}

// setIntegrationPrefix records a prefix that was observed to answer.
func (u *UniFiDriver) setIntegrationPrefix(prefix string) {
	u.mu.Lock()
	u.integrationPrefix = prefix
	u.mu.Unlock()
}

func (u *UniFiDriver) setFlavour(f UniFiFlavour) {
	u.mu.Lock()
	u.flavour = f
	u.mu.Unlock()
}

// Site returns the configured site identifier.
func (u *UniFiDriver) Site() string { return u.site }

// BaseURL returns the configured controller base URL.
func (u *UniFiDriver) BaseURL() string { return u.baseURL }

// authMode reports which credential the driver will use.
func (u *UniFiDriver) authMode() string {
	if u.apiKey != "" {
		return "api-key"
	}
	if u.username != "" {
		return "legacy-login"
	}
	return "none"
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// APIError is a non-2xx answer from the controller, carrying enough to
// diagnose it and nothing that could echo a credential.
type APIError struct {
	Method string
	URL    string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("unifi: %s %s returned %d: %s", e.Method, e.URL, e.Status, e.Body)
}

// bodyExcerpt bounds what a controller error can push into a log line.
func bodyExcerpt(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

// ---------------------------------------------------------------------------
// Integration API
// ---------------------------------------------------------------------------

// unifiClient is the subset of the Integration API client object this driver
// reads. Every other field is deliberately not modelled: the probe prints the
// whole object, and modelling fields nobody has seen is how a UI ends up promising
// figures the controller does not send.
type unifiClient struct {
	ID         string `json:"id"`
	MACAddress string `json:"macAddress"`
	Name       string `json:"name"`
	IPAddress  string `json:"ipAddress"`
	Type       string `json:"type"`
	// UplinkDeviceID is the controller's own id for the device this client is
	// connected through — the access point, on a wireless client. MEASURED:
	// it is a UUID and it is **not** a MAC, so it resolves only through
	// the site's device list. See unifi_devicemap.go.
	UplinkDeviceID string `json:"uplinkDeviceId"`
	Access         struct {
		Type          string `json:"type"`
		Authorized    bool   `json:"authorized"`
		Authorization struct {
			AuthorizedAt         string `json:"authorizedAt"`
			ExpiresAt            string `json:"expiresAt"`
			DataUsageLimitMBytes int64  `json:"dataUsageLimitMBytes"`
		} `json:"authorization"`
	} `json:"access"`
}

type unifiClientList struct {
	Data []unifiClient `json:"data"`
}

// normalizeMAC lowercases and colon-separates the several shapes a MAC arrives
// in — the NAS redirect, an operator typing, and the controller's own answer do
// not agree on separators or case.
func normalizeMAC(mac string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(mac)) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			b.WriteRune(r)
		}
	}
	hexOnly := b.String()
	if len(hexOnly) != 12 {
		// Not a MAC we recognise. Return the lowercased original so the caller's
		// comparison fails rather than matching something it should not.
		return strings.ToLower(strings.TrimSpace(mac))
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = hexOnly[i*2 : i*2+2]
	}
	return strings.Join(parts, ":")
}

func (u *UniFiDriver) newIntegrationRequest(ctx context.Context, method, urlStr string, body []byte) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if u.apiKey != "" {
		req.Header.Set("X-API-KEY", u.apiKey)
	}
	return req, nil
}

// clientsURL builds the documented client-lookup call.
//
//	GET {integration}/v1/sites/{siteId}/clients?filter=macAddress.eq('aa:bb:…')
//
// The filter value is query-escaped: a MAC arrives from an unsigned NAS redirect
// query parameter and is attacker-controlled.
func (u *UniFiDriver) clientsURL(ctx context.Context, siteID, mac string) string {
	base := fmt.Sprintf("%s/sites/%s/clients", u.integrationBase(ctx), url.PathEscape(siteID))
	if mac == "" {
		return base
	}
	q := url.Values{}
	q.Set("filter", fmt.Sprintf("macAddress.eq('%s')", mac))
	return base + "?" + q.Encode()
}

func (u *UniFiDriver) siteFor(siteID string) string {
	if siteID != "" {
		return siteID
	}
	return u.site
}

// lookupClient runs the documented client-lookup call and returns the matching
// client, or nil when the controller knows of no such client.
func (u *UniFiDriver) lookupClient(ctx context.Context, siteID, mac string) (*unifiClient, error) {
	urlStr := u.clientsURL(ctx, siteID, mac)
	req, err := u.newIntegrationRequest(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unifi: client lookup: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Method: http.MethodGet, URL: urlStr, Status: resp.StatusCode, Body: bodyExcerpt(resp.Body)}
	}

	var list unifiClientList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("unifi: decode client list: %w", err)
	}
	for i := range list.Data {
		if normalizeMAC(list.Data[i].MACAddress) == mac {
			return &list.Data[i], nil
		}
	}
	return nil, nil
}

// VerifyPresence confirms the claimed MAC is really associated in the claimed
// site and is still unauthorized, before any grant is created.
//
// The bool is the gate. VerifyPresenceDetail carries why.
func (u *UniFiDriver) VerifyPresence(ctx context.Context, claimedMAC, apMAC, siteID string) (bool, error) {
	p, err := u.VerifyPresenceDetail(ctx, claimedMAC, apMAC, siteID)
	return p.OK, err
}

// VerifyPresenceDetail implements PresenceDetailer.
//
// The client lookup answers three of security.md §4's four clauses: it proves
// the device is associated in that site, and `access.authorized` proves it has
// not already been let in. A device the controller does not know of is a device
// that never associated — which is exactly the request the unsigned UniFi
// redirect parameters let anybody make.
//
// **The fourth clause costs a second call, and this driver pays it.**
// The client object carries `uplinkDeviceId`; the site's device list
// resolves that id to the access point's MAC; the redirect's `ap` parameter is a
// MAC. A resolved contradiction is a refusal — unconditionally, because a
// checked-and-wrong claim is not the same thing as an unchecked one, and letting
// a configuration switch turn it off would put an unchecked access-point claim
// back with a flag on it.
// `RequireAPMatch` governs only the residue: a claim nothing could resolve.
func (u *UniFiDriver) VerifyPresenceDetail(ctx context.Context, claimedMAC, apMAC, siteID string) (Presence, error) {
	if claimedMAC == "" {
		return Presence{Outcome: PresenceNotFound}, errors.New("unifi: claimed MAC is empty")
	}
	site := u.siteFor(siteID)
	mac := normalizeMAC(claimedMAC)

	if u.legacyMode() {
		return u.legacyPresence(ctx, site, mac, apMAC)
	}

	client, err := u.lookupClient(ctx, site, mac)
	if err != nil {
		// Unreachable or refusing is never "yes". It is also never a silent
		// "no": the caller must be able to tell a refusal from an outage.
		return Presence{Outcome: PresenceUnavailable, SiteID: site}, err
	}
	if client == nil {
		return Presence{Outcome: PresenceNotFound, SiteID: site}, nil
	}
	if client.Access.Authorized {
		return Presence{
			Outcome:  PresenceAlreadyAuthorized,
			ClientID: client.ID,
			SiteID:   site,
		}, nil
	}
	resolution, err := u.resolveClaimedAccessPoint(ctx, site, client.UplinkDeviceID, apMAC)
	switch {
	case err != nil:
		// **Fail closed, and it is free.** The device list is a call to the same
		// controller over the same credential as the grant itself, so a
		// controller that cannot answer it cannot honour AUTHORIZE_GUEST_ACCESS
		// either — fail-open would buy a grant the guest could not use. The
		// guest fail-open security.md §2 allows is about the *Policy API*, where
		// RADIUS can still let somebody on without Go; nothing here can.
		//
		// It is reported as an outage rather than as a refusal so that
		// portal.Classify sends the guest to the counter instead of telling them
		// their device is not on the network.
		return Presence{Outcome: PresenceUnavailable, ClientID: client.ID, SiteID: site}, err
	case resolution == apContradicted:
		// Checked, and wrong. The redirect names an access point this client is
		// demonstrably not under, which is the assertion an unsigned query
		// parameter lets anybody make.
		return Presence{Outcome: PresenceAPMismatch, ClientID: client.ID, SiteID: site}, nil
	case resolution == apUnresolved && apMAC != "" && u.requireAPMatch:
		// Nothing was compared, and the operator asked for the claim to be
		// enforced rather than merely reported.
		return Presence{Outcome: PresenceAPMismatch, ClientID: client.ID, SiteID: site}, nil
	}

	return Presence{
		OK:       true,
		Outcome:  PresenceVerified,
		ClientID: client.ID,
		SiteID:   site,
		// The truth, not a constant in either direction: true only when the
		// claimed access point was resolved and held.
		APConfirmed: resolution == apMatched,
	}, nil
}

// authorizePayload builds the documented action body.
func authorizePayload(action string, req AuthorizeRequest) map[string]any {
	payload := map[string]any{"action": action}
	if action != actionAuthorize {
		return payload
	}
	if req.TimeLimitMinutes > 0 {
		payload["timeLimitMinutes"] = req.TimeLimitMinutes
	}
	if req.DataUsageLimitMB > 0 {
		payload["dataUsageLimitMBytes"] = req.DataUsageLimitMB
	}
	if req.RxRateLimitKbps > 0 {
		payload["rxRateLimitKbps"] = req.RxRateLimitKbps
	}
	if req.TxRateLimitKbps > 0 {
		payload["txRateLimitKbps"] = req.TxRateLimitKbps
	}
	return payload
}

const (
	actionAuthorize   = "AUTHORIZE_GUEST_ACCESS"
	actionUnauthorize = "UNAUTHORIZE_GUEST_ACCESS"
)

// clientAction posts one action against a client id.
//
// The id is the controller's own client id, not the MAC. The previous
// implementation put the MAC in this path; the documented shape is
// POST /v1/sites/{siteId}/clients/{clientId}/actions, and the id comes from the
// lookup call that VerifyPresence already has to make.
func (u *UniFiDriver) clientAction(ctx context.Context, siteID, clientID string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	urlStr := u.clientActionURL(ctx, siteID, clientID)

	req, err := u.newIntegrationRequest(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return err
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("unifi: client action: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
		return nil
	default:
		return &APIError{Method: http.MethodPost, URL: urlStr, Status: resp.StatusCode, Body: bodyExcerpt(resp.Body)}
	}
}

// AuthorizeGuest authorizes a guest client on UniFi.
//
// With an API key it uses the Integration API. With only a local admin username
// and password it uses the legacy private API, which is the sole path on any
// controller below Network Application 9.1.105.
func (u *UniFiDriver) AuthorizeGuest(ctx context.Context, req AuthorizeRequest) error {
	if req.ClientMAC == "" {
		return errors.New("unifi: client MAC is required")
	}
	if u.apiKey == "" {
		return u.legacyStamgr(ctx, actionAuthorize, req)
	}
	site := u.siteFor(req.SiteID)
	mac := normalizeMAC(req.ClientMAC)

	client, err := u.lookupClient(ctx, site, mac)
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("unifi: no client %s in site %s", mac, site)
	}
	return u.clientAction(ctx, site, client.ID, authorizePayload(actionAuthorize, req))
}

// DeauthorizeGuest revokes guest access.
//
// If the controller API is unreachable and a CoA fallback is configured, it
// falls back to RFC 5176 Disconnect. UniFi's captive-portal *authorization* has
// no RADIUS equivalent; revocation does.
func (u *UniFiDriver) DeauthorizeGuest(ctx context.Context, req DeauthorizeRequest) error {
	err := u.deauthorizeViaController(ctx, req)
	if err == nil {
		return nil
	}
	if u.coaFallback == nil {
		return err
	}
	// Only a transport failure justifies the fallback. A 4xx means the
	// controller answered and said no, and disconnecting anyway would be
	// acting on our own opinion of its answer.
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return err
	}
	coaErr := u.coaFallback.Disconnect(ctx, Session{
		CallingStationID: normalizeMAC(req.ClientMAC),
		SiteID:           u.siteFor(req.SiteID),
	})
	if coaErr != nil {
		return fmt.Errorf("unifi: controller unreachable (%v) and CoA fallback failed: %w", err, coaErr)
	}
	return nil
}

func (u *UniFiDriver) deauthorizeViaController(ctx context.Context, req DeauthorizeRequest) error {
	if req.ClientMAC == "" {
		return errors.New("unifi: client MAC is required")
	}
	if u.apiKey == "" {
		return u.legacyStamgr(ctx, actionUnauthorize, AuthorizeRequest{
			ClientMAC: req.ClientMAC,
			SiteID:    req.SiteID,
		})
	}
	site := u.siteFor(req.SiteID)
	mac := normalizeMAC(req.ClientMAC)

	client, err := u.lookupClient(ctx, site, mac)
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("unifi: no client %s in site %s", mac, site)
	}
	return u.clientAction(ctx, site, client.ID, authorizePayload(actionUnauthorize, AuthorizeRequest{}))
}

// DiscoverAccessPoints discovers APs connected to this controller.
//
// Access points are discovered, never configured: the AP MAC in a UniFi redirect
// is otherwise unresolvable, which is why PacketFence caches mac → ip for every
// AP and every BSSID in its vap_table.
//
// It reads the same paginated list the access-point map does, and for the same
// reason: one request keeps whatever came back, which on a real controller is
// **the first 25 devices**, silently. A venue's twenty-sixth access point would
// be invisible to discovery, so it could never be registered, so no session
// could ever name it.
func (u *UniFiDriver) DiscoverAccessPoints(ctx context.Context) ([]DiscoveredAP, error) {
	if u.apiKey == "" {
		return u.legacyDevices(ctx)
	}
	devices, err := u.listDevices(ctx, u.site)
	if err != nil {
		return nil, err
	}
	aps := make([]DiscoveredAP, 0, len(devices))
	for _, d := range devices {
		aps = append(aps, DiscoveredAP{
			MAC:       normalizeMAC(d.MACAddress),
			Name:      d.Name,
			IPAddress: d.IPAddress,
			Model:     d.Model,
		})
	}
	return aps, nil
}

// ---------------------------------------------------------------------------
// Legacy private API — controllers below 9.1.105
// ---------------------------------------------------------------------------

// login authenticates against the legacy private API and captures the CSRF
// token UniFi OS then requires on every write.
//
// UniFi OS silently answers 403 to every cmd/stamgr POST if the X-CSRF-Token
// from the login response is not echoed. "Silently" is the operative word: the
// call looks like a permissions problem and is not one.
// DOCUMENTED — PacketFence issue #9107.
func (u *UniFiDriver) login(ctx context.Context) error {
	_, _, err := u.loginRecorded(ctx)
	return err
}

// loginRecorded is login, reporting the HTTP status and whether a CSRF token
// came back. The probe needs both and must never touch what was sent: the
// request body carries a local admin password, and output meant for sharing
// cannot contain one.
func (u *UniFiDriver) loginRecorded(ctx context.Context) (int, bool, error) {
	if u.username == "" {
		return 0, false, errors.New("unifi: legacy API needs a local admin username and password")
	}
	u.mu.RLock()
	done, existing := u.loggedIn, u.csrfToken
	u.mu.RUnlock()
	if done {
		return 0, existing != "", nil
	}

	body, err := json.Marshal(map[string]string{
		"username": u.username,
		"password": u.password,
	})
	if err != nil {
		return 0, false, err
	}
	urlStr := u.loginPath(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("unifi: login: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return resp.StatusCode, false, &APIError{
			Method: http.MethodPost, URL: urlStr, Status: resp.StatusCode, Body: bodyExcerpt(resp.Body),
		}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	token := resp.Header.Get("X-CSRF-Token")
	if token == "" {
		token = resp.Header.Get("x-csrf-token")
	}

	u.mu.Lock()
	u.loggedIn = true
	u.csrfToken = token
	u.mu.Unlock()
	return resp.StatusCode, token != "", nil
}

// hasLegacyCredentials reports whether the legacy private API is reachable at
// all with what this driver was given.
func (u *UniFiDriver) hasLegacyCredentials() bool { return u.username != "" }

// legacyDeviceCandidates are the paths the device list has been documented at.
// PacketFence's populateAccessPointMACIP() walks the trailing-slash form; most
// other clients use the bare one. Which of them answers on Network 10.5.67 is
// itself unmeasured, so the probe records both rather than picking.
func legacyDeviceCandidates() []string {
	return []string{"/stat/device", "/stat/device/"}
}

// legacyStamgr issues the legacy cmd/stamgr guest command.
func (u *UniFiDriver) legacyStamgr(ctx context.Context, action string, req AuthorizeRequest) error {
	if err := u.login(ctx); err != nil {
		return err
	}
	site := u.siteFor(req.SiteID)
	mac := normalizeMAC(req.ClientMAC)

	cmd := map[string]any{"cmd": "authorize-guest", "mac": mac}
	if action == actionUnauthorize {
		cmd = map[string]any{"cmd": "unauthorize-guest", "mac": mac}
	} else {
		if req.TimeLimitMinutes > 0 {
			cmd["minutes"] = req.TimeLimitMinutes
		}
		if req.DataUsageLimitMB > 0 {
			cmd["bytes"] = req.DataUsageLimitMB
		}
		if req.RxRateLimitKbps > 0 {
			cmd["down"] = req.RxRateLimitKbps
		}
		if req.TxRateLimitKbps > 0 {
			cmd["up"] = req.TxRateLimitKbps
		}
		if req.APMAC != "" {
			cmd["ap_mac"] = normalizeMAC(req.APMAC)
		}
	}

	body, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	urlStr := fmt.Sprintf("%s/api/s/%s/cmd/stamgr", u.networkBase(ctx), url.PathEscape(site))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	u.mu.RLock()
	token := u.csrfToken
	u.mu.RUnlock()
	if token != "" {
		httpReq.Header.Set("X-CSRF-Token", token)
	}

	resp, err := u.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("unifi: cmd/stamgr: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden && token == "" {
		return &APIError{
			Method: http.MethodPost, URL: urlStr, Status: resp.StatusCode,
			Body: "403 with no X-CSRF-Token to echo — the login response carried none. " +
				"On UniFi OS this is the documented failure, not a permissions problem.",
		}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return &APIError{Method: http.MethodPost, URL: urlStr, Status: resp.StatusCode, Body: bodyExcerpt(resp.Body)}
	}
	return nil
}

// legacyPresence is VerifyPresenceDetail for a controller reached with a local
// admin account.
//
// ── why this exists: local-admin mode could never let a guest on ─────────────
//
// `AuthorizeGuest` has had a legacy branch since the driver was written, and the
// portal never reached it: `VerifyPresence` runs first (security.md §4), and it
// only ever called the Integration API's client lookup — which, with no API key,
// the controller answers 401. Every guest on a controller below Network 9.1.105
// or configured with a local account met "the network could not confirm your
// device", while the configuration screen accepted the account without complaint.
//
// ── the call, and how it is known ────────────────────────────────────────────
//
//	GET {network}/api/s/{site}/stat/sta/{mac}
//
// The legacy private API's list of **currently associated** stations, filtered to
// one MAC. It is the call Art-of-WiFi's UniFi-API-client makes for
// `list_clients($mac)` and the one the community API reference (ubntwiki,
// "UniFi Controller API") lists as the active-station list — the same private API
// whose `cmd/stamgr` and `stat/device` this driver and PacketFence's
// `populateAccessPointMACIP()` already use. Its station object carries all four
// of §4's clauses in one answer: the MAC, `authorized` (still unauthorized?), the
// site in the path, and `ap_mac` — the access point the station is associated to,
// as a MAC, so the claim in the redirect is compared directly and no device map is
// needed. Community-documented; UNVERIFIED on real hardware, where only the
// Integration API has been measured.
//
// A station the controller does not know answers either an empty `data` or
// `api.err.UnknownStation` (with a 400 on some versions); both are "not
// associated", which is the refusal §4 exists to make. Anything else that is not
// a 200 is an outage, never a yes.
func (u *UniFiDriver) legacyPresence(ctx context.Context, site, mac, apMAC string) (Presence, error) {
	if err := u.login(ctx); err != nil {
		return Presence{Outcome: PresenceUnavailable, SiteID: site}, err
	}
	urlStr := fmt.Sprintf("%s/api/s/%s/stat/sta/%s", u.networkBase(ctx), url.PathEscape(site), url.PathEscape(mac))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return Presence{Outcome: PresenceUnavailable, SiteID: site}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return Presence{Outcome: PresenceUnavailable, SiteID: site}, fmt.Errorf("unifi: stat/sta: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Meta struct {
			RC  string `json:"rc"`
			Msg string `json:"msg"`
		} `json:"meta"`
		Data []struct {
			ID         string `json:"_id"`
			MAC        string `json:"mac"`
			APMAC      string `json:"ap_mac"`
			Authorized bool   `json:"authorized"`
		} `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	decodeErr := json.Unmarshal(raw, &body)
	unknown := decodeErr == nil && strings.Contains(body.Meta.Msg, "UnknownStation")
	switch {
	case unknown:
		return Presence{Outcome: PresenceNotFound, SiteID: site}, nil
	case resp.StatusCode != http.StatusOK:
		return Presence{Outcome: PresenceUnavailable, SiteID: site},
			&APIError{Method: http.MethodGet, URL: urlStr, Status: resp.StatusCode, Body: bodyExcerpt(bytes.NewReader(raw))}
	case decodeErr != nil:
		return Presence{Outcome: PresenceUnavailable, SiteID: site}, fmt.Errorf("unifi: decode stat/sta: %w", decodeErr)
	}

	for _, sta := range body.Data {
		if normalizeMAC(sta.MAC) != mac {
			continue
		}
		if sta.Authorized {
			return Presence{Outcome: PresenceAlreadyAuthorized, ClientID: sta.ID, SiteID: site}, nil
		}
		claimed, actual := normalizeMAC(apMAC), normalizeMAC(sta.APMAC)
		switch {
		case claimed != "" && actual != "" && claimed != actual:
			// Checked, and wrong — unconditionally a refusal, as on the
			// Integration API path.
			return Presence{Outcome: PresenceAPMismatch, ClientID: sta.ID, SiteID: site}, nil
		case claimed != "" && actual == "" && u.requireAPMatch:
			return Presence{Outcome: PresenceAPMismatch, ClientID: sta.ID, SiteID: site}, nil
		}
		return Presence{
			OK: true, Outcome: PresenceVerified, ClientID: sta.ID, SiteID: site,
			APConfirmed: claimed != "" && claimed == actual,
		}, nil
	}
	return Presence{Outcome: PresenceNotFound, SiteID: site}, nil
}

// legacyDevices walks the legacy device list, which is where PacketFence reads
// AP MAC → IP from.
func (u *UniFiDriver) legacyDevices(ctx context.Context) ([]DiscoveredAP, error) {
	if err := u.login(ctx); err != nil {
		return nil, err
	}
	urlStr := fmt.Sprintf("%s/api/s/%s/stat/device", u.networkBase(ctx), url.PathEscape(u.site))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Method: http.MethodGet, URL: urlStr, Status: resp.StatusCode, Body: bodyExcerpt(resp.Body)}
	}

	var result struct {
		Data []struct {
			MAC   string `json:"mac"`
			Name  string `json:"name"`
			IP    string `json:"ip"`
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	aps := make([]DiscoveredAP, 0, len(result.Data))
	for _, d := range result.Data {
		aps = append(aps, DiscoveredAP{MAC: normalizeMAC(d.MAC), Name: d.Name, IPAddress: d.IP, Model: d.Model})
	}
	return aps, nil
}

// ---------------------------------------------------------------------------
// Probe
// ---------------------------------------------------------------------------

// unconditionalWarnings is empty for this driver, and the emptiness is the
// finding rather than an omission.
//
// The security.md §4 access-point gap is not attached to every probe, because
// `VerifyPresenceDetail` **can** check the access point a redirect claims — the
// client object carries `uplinkDeviceId` and the site's device list resolves it
// — and the driver spends the call. So the sentence that warning carries is not
// true of this driver, and a warning that says the opposite of what the code does
// is worse than no warning: it teaches an operator that the notice means nothing.
//
// The condition has not disappeared, it is **conditional**, and `Probe`
// emits it when the device list cannot be read or resolves no device. That is a
// warning about the controller in front of the operator rather than about the
// driver, which is the only kind worth reading.
//
// The method stays so that `ShippedProbeWarnings` reaches it and answers nil
// rather than nothing-was-asked, and so this paragraph sits where somebody
// looking for the deleted warning will find it.
func (u *UniFiDriver) unconditionalWarnings() []ProbeWarning { return nil }

// canConfirmAccessPoint reports whether this driver has a mechanism for
// security.md §4's access-point clause at all.
//
// It is a statement about the mechanism, not about a configuration or a
// controller: given the Integration API, `uplinkDeviceId` plus the device list
// resolve the claim (measured, the device list on Network 10.5.67). Whether a
// particular controller answers is what `Probe` reports.
//
// It exists so the registry test can cross-check a driver's unconditional
// warnings against what the driver can do, in both directions. Without it, "no
// §4 notice" and "the notice was forgotten" are the same absence.
func (u *UniFiDriver) canConfirmAccessPoint() bool { return true }

// Probe checks connectivity, discovers the flavour and the URL prefix that
// actually answers, and reports every condition an operator must see before
// trusting this controller — including the capabilities nobody has measured.
func (u *UniFiDriver) Probe(ctx context.Context) (*ProbeResult, error) {
	res := &ProbeResult{
		AuthMode:               u.authMode(),
		PresenceCheckSupported: true,
		Unverified:             UnverifiedCapabilities(u),
	}

	flavour := u.detectFlavour(ctx)
	res.Flavour = string(flavour)

	if u.tlsMode == TLSInsecureSkipVerify {
		res.Warnings = append(res.Warnings, ProbeWarning{
			Kind:   WarnCertificateVerificationDisabled,
			Params: map[string]any{"tlsMode": string(u.tlsMode)},
			Detail: "certificate verification is disabled for this controller; an on-path attacker can read " +
				"the API key. Prefer TLS mode pinned-sha256 with the controller's fingerprint.",
		})
	}
	res.Warnings = append(res.Warnings, u.unconditionalWarnings()...)

	if u.legacyMode() {
		// A local admin account never touches the Integration API, so probing it
		// would report "rejected the credential" about a credential that was
		// never sent, for a controller every guest call reaches fine.
		u.probeLegacy(ctx, res)
		return res, nil
	}

	// Find the prefix that answers, rather than assuming the one for the
	// flavour. A 401 counts as an answer: it proves the route exists and the
	// key is the only thing missing, which is precisely what a real UniFi OS
	// controller returns when probed unauthenticated.
	var lastErr error
	for _, prefix := range integrationCandidates(flavour) {
		urlStr := u.baseURL + prefix + "/v1/sites"
		req, err := u.newIntegrationRequest(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := u.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		status := resp.StatusCode
		// Read it rather than discard it. This is the call that lists the
		// controller's own sites, and discovery cannot adopt without one — the
		// body was being thrown away by the only request that fetches it.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if status == http.StatusOK {
			var page struct {
				Data []struct {
					ID                string `json:"id"`
					Name              string `json:"name"`
					InternalReference string `json:"internalReference"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &page); err == nil {
				for _, s := range page.Data {
					name := s.Name
					if name == "" {
						name = s.InternalReference
					}
					res.VendorSites = append(res.VendorSites, VendorSite{ID: s.ID, Name: name})
				}
			}
		}

		if status == http.StatusNotFound {
			continue
		}
		u.setIntegrationPrefix(prefix)
		res.APIBase = u.baseURL + prefix + "/v1"
		res.Reachable = status == http.StatusOK
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			res.Errors = append(res.Errors,
				fmt.Sprintf("the Integration API is present at %s but rejected the credential (HTTP %d)", res.APIBase, status))
		} else if status != http.StatusOK {
			res.Errors = append(res.Errors, fmt.Sprintf("HTTP status %d from %s", status, res.APIBase))
		}
		break
	}
	if res.APIBase == "" {
		res.Errors = append(res.Errors, "no Integration API prefix answered; this controller is probably below "+
			UniFiIntegrationAPIMinVersion+", which needs a local admin username and password")
		if lastErr != nil {
			res.Errors = append(res.Errors, lastErr.Error())
		}
	}

	res.Warnings = append(res.Warnings, u.accessPointWarnings(ctx, res)...)

	if v, err := u.applicationVersion(ctx); err == nil && v != "" {
		res.Version = v
		if cmp, ok := compareVersions(v, UniFiIntegrationAPIMinVersion); ok && cmp < 0 {
			w := VersionBelowFloorWarning(v, UniFiIntegrationAPIMinVersion)
			w.Detail = fmt.Sprintf(
				"Network Application %s is below %s, the documented floor for the Integration API; "+
					"the legacy private API and a local admin account are the only path",
				v, UniFiIntegrationAPIMinVersion)
			res.Warnings = append(res.Warnings, w)
		}
	}

	return res, nil
}

// probeLegacy is Probe for a controller reached with a local admin account: the
// login every legacy call starts with, then the controller's own site list
// (`GET {network}/api/self/sites`, the legacy API's answer to the Integration
// API's `/v1/sites` — community-documented, UNVERIFIED on real hardware).
//
// The site list is what discovery adopts from, and a legacy site id is its short
// `name` (`default`), which is what `cmd/stamgr` and `stat/sta` take in the path.
func (u *UniFiDriver) probeLegacy(ctx context.Context, res *ProbeResult) {
	res.APIBase = u.networkBase(ctx) + "/api"
	if _, _, err := u.loginRecorded(ctx); err != nil {
		res.Errors = append(res.Errors, "the local admin login was refused or did not answer: "+err.Error())
		return
	}
	urlStr := u.networkBase(ctx) + "/api/self/sites"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return
	}
	req.Header.Set("Accept", "application/json")
	resp, err := u.httpClient.Do(req)
	if err != nil {
		res.Errors = append(res.Errors, "the site list did not answer: "+err.Error())
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		res.Errors = append(res.Errors, fmt.Sprintf("HTTP status %d from %s", resp.StatusCode, urlStr))
		return
	}
	var page struct {
		Data []struct {
			Name string `json:"name"`
			Desc string `json:"desc"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		res.Errors = append(res.Errors, "the site list is not the legacy API's shape: "+err.Error())
		return
	}
	for _, s := range page.Data {
		name := s.Desc
		if name == "" {
			name = s.Name
		}
		res.VendorSites = append(res.VendorSites, VendorSite{ID: s.Name, Name: name})
	}
	res.Reachable = true
}

// applicationVersion asks the controller what it is running.
//
// Two shapes are tried because the field name on /v1/info is not measured; the
// probe command prints the whole body so whoever runs it can see which key a
// real controller uses.
func (u *UniFiDriver) applicationVersion(ctx context.Context) (string, error) {
	urlStr := u.integrationBase(ctx) + "/info"
	req, err := u.newIntegrationRequest(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{Method: http.MethodGet, URL: urlStr, Status: resp.StatusCode, Body: bodyExcerpt(resp.Body)}
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	for _, key := range []string{"applicationVersion", "version", "server_version"} {
		if v, ok := body[key].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", nil
}

// compareVersions compares dotted numeric versions. The bool is false when
// either side is not dotted-numeric, because guessing an ordering for a version
// string we cannot parse is how a version floor turns into a false alarm.
func compareVersions(a, b string) (int, bool) {
	pa, ok := splitVersion(a)
	if !ok {
		return 0, false
	}
	pb, ok := splitVersion(b)
	if !ok {
		return 0, false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func splitVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	fields := strings.Split(v, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// ObservedFingerprint is the SHA-256 of the leaf certificate this client saw on
// its first handshake, lower-case hex, or "" if it has made none.
//
// A caller registering a controller with TLSTrustOnFirstUse MUST read this after
// the first successful request and persist it — migration `00004` carries
// `controllers.certificate_fingerprint` for exactly this. A caller that does not
// has built insecure-skip-verify with extra steps: every contact stays a first
// contact, and nothing is ever verified.
func (u *UniFiDriver) ObservedFingerprint() string { return u.seen.Fingerprint() }
