// Package aaa is the Policy API: the authorization decisions FreeRADIUS asks for
// over `rlm_rest`, and nothing else.
//
// Three invariants shape every file in it, and each one is a rule rather than a
// preference. See CONTRACT.md for the full list.
//
//   - **Authorization only.** No credential material crosses this boundary, in
//     either direction. FreeRADIUS fetches password verifiers itself through
//     `rlm_sql` from a minimally-privileged view; see reply.go for the gate.
//     That sentence is true of the generated configuration and not only of this
//     comment: an account this package authorizes is answered with
//     IdentityKindAccount, and the virtual server then refuses it unless rlm_pap
//     verifies User-Password against `radius_pap_verifiers`. Without that, an
//     allow for an account would become Auth-Type := Accept and no password
//     would reach any check at all.
//   - **Inner identity only.** The outer identity in PEAP and TTLS is chosen
//     freely by the client. See identity.go.
//   - **Failure is declared, never inherited.** What happens while this package is
//     unreachable is a per-site decision, pre-positioned in the generated
//     FreeRADIUS configuration because by then nothing here can be asked. See
//     failure.go.
package aaa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
)

// ErrIdentityNotFound is how a PolicyStore says "there is no such voucher or
// account", as distinct from "I could not find out".
//
// The distinction is the whole point and it is easy to lose. A store that returns
// one error for both makes a PostgreSQL outage look like a wrong voucher code,
// which produces an Access-Reject -- an authoritative denial, minted while the
// control plane knew nothing. That answer bypasses the site's failure model
// entirely: a venue configured fail-open is silently fail-closed for as long as the
// database is down, and nothing in the logs says so.
//
// So: a missing identity is `(nil, ErrIdentityNotFound)` or `(nil, nil)`. Every
// other error means undecided, and undecided reaches the declared failure branch.
var ErrIdentityNotFound = errors.New("aaa: identity not found")

// Request is the authorization payload received from FreeRADIUS's `rlm_rest`.
type Request struct {
	// UserName is the **inner** identity, and the only field authorization may
	// key on. Read it through AuthorizationSubject.
	UserName string `json:"user_name"`

	// OuterUserName is the identity presented outside a TLS tunnel. It is
	// client-supplied and arbitrary -- `anonymous@example.com` is the norm -- and
	// exists here for logging and for the anonymity check in identity.go. Nothing
	// may authorize on it. See `docs/rules/security.md` §8.
	OuterUserName string `json:"outer_user_name,omitempty"`

	// UserNameHex and OuterUserNameHex are the same two identities as the
	// attribute's own bytes in hex, which is how the generated tree sends them.
	// decodeRequest decodes each into its plain field and empties it, so
	// nothing past the handler ever reads these. See decodeRequest for why.
	UserNameHex      string `json:"user_name_hex,omitempty"`
	OuterUserNameHex string `json:"outer_user_name_hex,omitempty"`

	// EAPMessage carries the presence of an EAP-Message attribute, never its
	// contents: it is the one signal that separates an 802.1X supplicant from a
	// guest, and 802.1X is fail-closed regardless of what the site configured.
	// The generated `rest` module sends a presence marker rather than the blob.
	EAPMessage string `json:"eap_message,omitempty"`

	CallingStationID string `json:"calling_station_id"`
	CalledStationID  string `json:"called_station_id"`
	NASIP            string `json:"nas_ip"`
	NASIdentifier    string `json:"nas_identifier"`
	ServiceType      string `json:"service_type,omitempty"`

	// RADIUSClient is the shortname of the client the packet arrived on, from
	// FreeRADIUS's own `%{client:shortname}` -- which is a lookup in the `nas`
	// projection, not a field in the packet.
	//
	// That distinction is the whole reason this is the field and SiteID is not.
	// FreeRADIUS resolves clients from six columns and none of them is a site, so
	// the generator has no honest source for one. What it does have is an
	// identifier the *server* looked up rather than one the sender chose --
	// unlike NASIdentifier, which is NAS-supplied and therefore attacker-chosen.
	// The two are not interchangeable and the trusted one is this.
	//
	// Site is resolved from it on this side. Until it arrives this is empty, the
	// site is unknown, and an unknown site is DefaultPolicy, which is fail-closed.
	RADIUSClient string `json:"radius_client,omitempty"`

	// ClientIP is the packet's source address, `%{Packet-Src-IP-Address}` (or its
	// IPv6 twin), and it is what a voucher's site is resolved from.
	//
	// **Why the source address and not the shortname above.** It is the key
	// FreeRADIUS itself found the client by, and the packet was authenticated
	// with that client's shared secret (Message-Authenticator is required), so a
	// NAS cannot claim another client's address without holding that client's
	// secret. The shortname is the client's *name*, which two venues may share.
	// PacketFence resolves the switch — and with it the location — from the
	// RADIUS client's address the same way, and ClearPass keys its Network
	// Device and device-group (location) on the NAD's IP.
	ClientIP string `json:"client_ip,omitempty"`
}

// Decision represents the policy engine authorization outcome.
type Decision struct {
	Allow           bool              `json:"allow"`
	Reason          string            `json:"reason,omitempty"`
	ReplyAttributes map[string]string `json:"reply_attributes,omitempty"`

	// Unrenderable names the parts of the decision the vendor profile had no
	// attribute for. It is never served -- writeDecision writes ReplyAttributes
	// and nothing else -- and it exists so the loss is not silent.
	//
	// `docs/rules/nas-drivers.md`: never a field that silently does nothing.
	// A rate limit an operator typed into the admin console, dropped because no
	// attribute on this vendor carries it, is exactly the failure
	// `docs/rules/frontend.md` is about -- a control that reaches nothing,
	// with no error anywhere.
	Unrenderable []string `json:"unrenderable,omitempty"`

	// DroppedAttributes names attributes that were rendered and then lost to the
	// single-valued reply format. See nas.CollapseToMap: Cisco needs two
	// Cisco-AVPair attributes in one reply and a map of pairs holds one.
	DroppedAttributes []string `json:"dropped_attributes,omitempty"`
}

// PolicyStore defines the persistence interface required by the policy engine.
//
// **Every method must distinguish "no such row" from "I could not look".** Return
// `(nil, nil)` or `(nil, ErrIdentityNotFound)` for the first; return any other
// error for the second, and the engine will answer undecided rather than deny. A
// store whose sentinel is its own may implement IdentityNotFoundReporter instead of
// changing its signatures.
type PolicyStore interface {
	LookupVoucher(ctx context.Context, code string) (*VoucherInfo, error)

	// ClaimVoucher moves an `unused` voucher to `in-use`, stamps first use and --
	// for a from-first-use batch -- the expiry, and returns it as it now is. It
	// must be ONE statement that tests the state and changes it, the same one the
	// portal claims with (`store.GrantRepo.ClaimVoucher`), so that a voucher is
	// single-use across both paths and two requests racing for it cannot both
	// win. A voucher that is no longer claimable is `(nil, ErrIdentityNotFound)`
	// or `(nil, nil)`; any other error is undecided.
	//
	// siteID is the site the request arrived at, never empty: a store must refuse
	// an empty one rather than claim across every venue, because an unscoped
	// claim is exactly how a code sold at site A opened site B's network.
	ClaimVoucher(ctx context.Context, siteID, code string, at time.Time) (*VoucherInfo, error)

	// RADIUSClientSite resolves the site of the RADIUS client whose address
	// contains clientIP — the most specific enabled one, which is the one
	// FreeRADIUS matched. An address no enabled client covers, or one that does
	// not parse, is `("", ErrIdentityNotFound)`; any other error is undecided.
	RADIUSClientSite(ctx context.Context, clientIP string) (string, error)

	LookupGuestAccount(ctx context.Context, username string) (*GuestAccountInfo, error)
	IsMACAllowed(ctx context.Context, mac string) (bool, error)
}

// The identity kind an allow names, and why FreeRADIUS has to be told it.
//
// **An allow is an authorization decision, not an authentication.** For a voucher
// the two coincide: the code is the bearer secret, the Policy API is the only
// thing that knows which codes exist, and knowing one is the whole of the proof.
// For an account they do not: a username is not a secret, and an allow for one
// that became `Auth-Type := Accept` with no password checked anywhere would put
// any device that knew a guest's username on the network.
//
// So an allow carries which of the two it is, in an attribute FreeRADIUS reads
// and never sends: `Foyer-Identity-Kind` is defined in the generated dictionary
// at a number in the 3000-4000 range, which FreeRADIUS 3.2 treats as
// server-internal and does not encode into a packet (measured on 3.2.10:
// rlm_rest placed it in the reply list and radclient never saw it).
// The virtual server accepts a voucher on its own, sends an account through
// rlm_pap against the verifier it reads itself from `radius_pap_verifiers`, and
// rejects any allow whose kind it does not recognise -- including an allow with
// no kind at all, which is what a Policy API older than this attribute returns.
//
// The value is a decision about what still has to be proven, not credential
// material, and it names no identity. Only allowAs sets it.
const (
	IdentityKindAttribute = "Foyer-Identity-Kind"
	IdentityKindVoucher   = "voucher"
	IdentityKindAccount   = "account"
)

// IdentityNotFoundReporter lets a PolicyStore keep its own not-found sentinel
// without this package importing the package that defines it.
//
// `internal/store` imports `internal/aaa`, so the dependency cannot run the other
// way; one method here is the whole adaptation.
type IdentityNotFoundReporter interface {
	IsIdentityNotFound(err error) bool
}

// VoucherInfo represents the necessary voucher properties for RADIUS authorization.
type VoucherInfo struct {
	Code string
	// State is the schema's: unused, in-use, exhausted, expired, revoked
	// (migration 00005). Only `unused` is claimable; see decideVoucher.
	State           string
	DurationMinutes int
	RxRateKbps      int
	TxRateKbps      int
	ExpiresAt       *time.Time
	// SiteID is the venue the voucher's batch was issued for. Empty means the
	// store does not say, and the claim's own site condition is then the only
	// guard — which the real store's always is anyway.
	SiteID string
}

// GuestAccountInfo represents the necessary account properties for RADIUS
// authorization.
type GuestAccountInfo struct {
	Username        string
	Enabled         bool
	DurationMinutes int
	RxRateKbps      int
	TxRateKbps      int
}

// Engine evaluates RADIUS authentication and authorization requests.
type Engine struct {
	store                  PolicyStore
	defaultInterimInterval int
	notFound               func(error) bool

	// now is the clock a claim is stamped with. time.Now outside tests.
	now func() time.Time

	// profile renders a decision into attributes. It is never nil; see NewEngine
	// and WithVendorProfile.
	profile nas.VendorProfile
}

// NewEngine creates a new policy engine instance.
func NewEngine(store PolicyStore, defaultInterimInterval int) *Engine {
	if defaultInterimInterval <= 0 {
		defaultInterimInterval = DefaultGraceSeconds
	}
	e := &Engine{
		store:                  store,
		defaultInterimInterval: defaultInterimInterval,
		notFound:               func(err error) bool { return errors.Is(err, ErrIdentityNotFound) },
		now:                    time.Now,
	}
	if r, ok := store.(IdentityNotFoundReporter); ok && r != nil {
		e.notFound = r.IsIdentityNotFound
	}
	// The RFC-only profile: the standard attributes, and nothing invented for a
	// vendor nobody has named yet. A vendor would be resolved from
	// Request.RADIUSClient -- the value FreeRADIUS looked up rather than one the
	// NAS chose -- and that resolution does not exist here yet, so the floor is
	// what ships. See WithVendorProfile.
	e.profile = nas.VendorProfileFor(nas.VendorManual)
	return e
}

// WithVendorProfile sets which vendor's attributes this engine renders into, and
// returns it.
//
// Separate from NewEngine because the vendor is a per-request fact, not a
// per-engine one, and pretending otherwise in the constructor would freeze a
// deployment to one vendor. **The per-request path does not exist yet**: it needs
// the site and vendor resolved from Request.RADIUSClient, which is the same
// missing resolution CONTRACT.md records for the failure model. Until it lands,
// this is how a lab or a single-vendor deployment selects one, and it is
// deliberately a written sentence at a call site rather than a default nobody
// reads.
func (e *Engine) WithVendorProfile(p nas.VendorProfile) *Engine {
	if p != nil {
		e.profile = p
	}
	return e
}

// VendorProfile reports the profile this engine renders through.
func (e *Engine) VendorProfile() nas.VendorProfile { return e.profile }

// allow renders an abstract decision into the reply this Policy API serves.
//
// It is the only place an allow Decision is built, and that is what makes the
// seam real rather than advisory: there is no second path that assembles
// attributes by hand, and adding one would be the collapse
// `docs/rules/nas-drivers.md` forbids.
//
// An allow built here alone carries no Foyer-Identity-Kind, and the generated
// virtual server REFUSES an allow without one: every authorization path goes
// through allowAs, which says what the allow still needs proven.
func (e *Engine) allow(d nas.ReplyDecision) *Decision {
	p := e.profile
	if p == nil {
		p = nas.VendorProfileFor(nas.VendorManual)
	}
	attrs, dropped := nas.CollapseToMap(p.RenderReply(d))
	return &Decision{
		Allow:             true,
		ReplyAttributes:   attrs,
		Unrenderable:      p.Unrenderable(d),
		DroppedAttributes: dropped,
	}
}

// allowAs is allow plus the identity kind -- IdentityKindVoucher or
// IdentityKindAccount -- written last, over anything a profile rendered under the
// same name: it is the one attribute in the reply the virtual server makes an
// AUTHENTICATION decision on, so nothing but this function may set it.
func (e *Engine) allowAs(kind string, d nas.ReplyDecision) *Decision {
	dec := e.allow(d)
	if dec.ReplyAttributes == nil {
		dec.ReplyAttributes = map[string]string{}
	}
	dec.ReplyAttributes[IdentityKindAttribute] = kind
	return dec
}

// deny builds an authoritative rejection.
func deny(reason string) *Decision { return &Decision{Allow: false, Reason: reason} }

// Authorize evaluates a request from FreeRADIUS `rlm_rest`.
//
// It returns exactly one of three things, and the caller depends on the difference:
//
//   - `(allow, nil)`   -- authorized, with the reply attributes to send.
//   - `(deny, nil)`    -- authoritatively refused. This is a decision.
//   - `(nil, err)`     -- undecided. The handler answers 503, `rlm_rest` returns
//     `fail`, and the generated config applies the site's declared failure mode.
//     A store that cannot be reached lands here, never in the second case.
func (e *Engine) Authorize(ctx context.Context, req Request) (*Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if fault := identityFault(req); fault != "" {
		return deny(fault), nil
	}
	subject := req.AuthorizationSubject()

	if e.store == nil {
		// No store is not "no identities". It is a Policy API that cannot answer,
		// and an engine constructed without one must say so rather than reject
		// every guest in the venue with an authoritative denial.
		return nil, errors.New("aaa: no policy store configured")
	}

	// 1. Is the identity a voucher? Looked up in its stored form when it has one
	// -- `abcd-efgh-jkmn` is the code printed as ABCD-EFGH-JKMN (voucherKey) --
	// and as itself otherwise.
	key := subject
	if k, ok := voucherKey(subject); ok {
		key = k
	}
	voucher, err := e.store.LookupVoucher(ctx, key)
	switch {
	case err != nil && !e.notFound(err):
		return nil, fmt.Errorf("voucher lookup: %w", err)
	case err == nil && voucher != nil:
		return e.decideVoucher(ctx, req, voucher)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 2. Is it a guest account?
	account, err := e.store.LookupGuestAccount(ctx, subject)
	switch {
	case err != nil && !e.notFound(err):
		return nil, fmt.Errorf("account lookup: %w", err)
	case err == nil && account != nil:
		return e.decideAccount(account), nil
	}

	// Neither. An authoritative denial, and the reason names no identity: the
	// string is logged by FreeRADIUS, and echoing a submitted value back into a
	// log is the same mistake as echoing it into a page.
	return deny("identity not found or not authorized"), nil
}

// decideVoucher and decideAccount decide, and describe. They do not render.
//
// The rate limit is not written straight into Filter-Id as "<rx>/<tx>".
// RFC 2865 §5.11 defines Filter-Id as the **name of a filter list already
// configured on the NAS** -- the same shape as Cisco's url-redirect-acl, a name
// Foyer cannot create -- so "10000/5000" would be a filter-list name that has to
// exist on the device, and nothing has ever been observed treating it as a rate.
// It travels as a rate on the decision: a profile with an attribute for it
// renders one, and a profile without reports it in Unrenderable rather than
// emitting something that looks like it works.
//
// ── a voucher over RADIUS is claimed, exactly as the portal claims it ─────────
//
// Checking only `revoked` and `expired` and allowing everything else -- `in-use`
// and `exhausted` included -- without claiming would mean no single use, no
// first-use timestamp, no from-first-use expiry: a code printed on a slip would be
// unlimited over RADIUS while the portal honoured it once. So the rule is the
// portal's rule, stated in one place: only `unused` is claimable, the claim is the store's single statement,
// and a claim that finds nothing -- somebody else won, or the state moved between
// the read and the claim -- is a refusal.
//
// The read before the claim only chooses the reason a refusal gives. It decides
// nothing: the claim's own WHERE clause is the guarantee.
//
// ── and it is claimed at the site the request arrived at ────────────────────
//
// A claim that carried no site would let a code printed for venue A open venue
// B's network. The site is
// resolved from the RADIUS client the packet came from (Request.ClientIP), and
// it is checked **before** the voucher's state: a code belonging to another venue
// gets the same sentence as a code that does not exist, so a NAS at B cannot learn
// which of A's codes are revoked, expired or in use. A request whose client
// resolves to no site is refused — there is no venue to claim at, and claiming at
// every venue is the defect.
func (e *Engine) decideVoucher(ctx context.Context, req Request, v *VoucherInfo) (*Decision, error) {
	site, err := e.store.RADIUSClientSite(ctx, req.ClientIP)
	switch {
	case err != nil && !e.notFound(err):
		return nil, fmt.Errorf("radius client site: %w", err)
	case err != nil || site == "":
		return deny("the RADIUS client is not bound to a site"), nil
	}
	if v.SiteID != "" && v.SiteID != site {
		return deny("identity not found or not authorized"), nil
	}

	now := e.now()
	switch v.State {
	case "revoked":
		return deny("voucher revoked"), nil
	case "expired":
		return deny("voucher expired"), nil
	case "exhausted":
		return deny("voucher exhausted"), nil
	case "in-use":
		return deny("voucher already in use"), nil
	case "unused":
	default:
		// A state this package does not know is not a state it may authorize.
		return deny("voucher state not claimable"), nil
	}
	if v.ExpiresAt != nil && !v.ExpiresAt.After(now) {
		return deny("voucher expired"), nil
	}

	claimed, err := e.store.ClaimVoucher(ctx, site, v.Code, now)
	switch {
	case err != nil && !e.notFound(err):
		return nil, fmt.Errorf("voucher claim: %w", err)
	case err != nil || claimed == nil:
		return deny("voucher already in use"), nil
	}

	timeout := time.Duration(claimed.DurationMinutes) * time.Minute
	if claimed.ExpiresAt != nil {
		left := claimed.ExpiresAt.Sub(now).Truncate(time.Second)
		if left <= 0 {
			return deny("voucher expired"), nil
		}
		// A from-creation voucher claimed an hour before its batch expires does
		// not get the plan's eight hours: the session ends when the code does.
		if timeout <= 0 || left < timeout {
			timeout = left
		}
	}
	return e.allowAs(IdentityKindVoucher, nas.ReplyDecision{
		SessionTimeout:  timeout,
		InterimInterval: time.Duration(e.defaultInterimInterval) * time.Second,
		RxKbps:          claimed.RxRateKbps,
		TxKbps:          claimed.TxRateKbps,
	}), nil
}

// decideAccount authorizes an account and does NOT authenticate it. The allow says
// IdentityKindAccount, and the generated virtual server turns that into
// `Auth-Type := PAP` against the verifier rlm_sql reads -- or into a rejection when
// there is no verifier or no User-Password. See the IdentityKind constants.
func (e *Engine) decideAccount(a *GuestAccountInfo) *Decision {
	if !a.Enabled {
		return deny("account disabled")
	}
	return e.allowAs(IdentityKindAccount, nas.ReplyDecision{
		SessionTimeout:  time.Duration(a.DurationMinutes) * time.Minute,
		InterimInterval: time.Duration(e.defaultInterimInterval) * time.Second,
		RxKbps:          a.RxRateKbps,
		TxKbps:          a.TxRateKbps,
	})
}
