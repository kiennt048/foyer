package store

import (
	"encoding/json"
	"net/netip"
	"time"
)

// The shapes the repositories read and write.
//
// Field sets follow web/contract/openapi.yaml, which is the authority: this phase's
// deliverable is the contract, and a store type that carries a field the contract
// does not define is a field some screen will eventually render. Where a name
// differs it is because the store is closer to the database -- `RadiusClient` and
// `AccessPoint` are two records here and one `Nas` in the contract, which is the
// correction this schema exists to make.

// Site is Foyer's unit of vouchers, accounts and policy.
type Site struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Environment string    `json:"environment"` // live | lab
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Operator is a person who signs in to the admin console.
type Operator struct {
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	DisplayName  *string `json:"displayName"`
	PasswordHash string  `json:"-"` // argon2id, and never serialised
	Role         string  `json:"role"`
	Enabled      bool    `json:"enabled"`

	// MustChangePassword: this operator holds the credential the install shipped
	// with. Migration 00017 has the whole argument; the part that matters to a
	// caller is that it is a *gate*, not a hint -- internal/api refuses every
	// operation but getAuthSession, signOut and changeOwnPassword while it is set.
	//
	// Never serialised on Operator. The contract puts it on AuthSession, which is
	// the answer to "who am I", not on the operator record other administrators can
	// list -- an administrator does not need to know which of their colleagues has
	// not yet rotated a password, and a list endpoint is a worse place to learn it.
	MustChangePassword bool `json:"-"`

	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// OperatorSession is a signed-in console session.
//
// It carries the CSRF token rather than deriving it, because the double-submit
// check compares what the client sent against what the session says -- and a token
// derived from the session id is not a second secret.
type OperatorSession struct {
	TokenHash  string
	OperatorID string
	CSRFToken  string
	UserAgent  *string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// Capabilities is what a driver can do on the operator's behalf.
//
// It is load-bearing, not documentation: the console hides controls a false value
// makes impossible. Read the capability's own definition before acting on one --
// a false value may mean the action cannot happen at all (hide the control) or
// merely that it is manual (keep it, and state the price).
type Capabilities struct {
	LiveReauthorize bool   `json:"liveReauthorize"`
	WalledGarden    string `json:"walledGarden"` // none | ip-range | fqdn-sni
	HardQuota       bool   `json:"hardQuota"`
	BandwidthLimits bool   `json:"bandwidthLimits"`
	DynamicVLAN     bool   `json:"dynamicVlan"`
	RotateSecret    bool   `json:"rotateSecret"`
	// CanDeauthGuest is what the last probe read off the driver. Absent in a row
	// written before this field existed, which reads as false until the device is probed
	// again -- the pessimistic direction, and the console says why.
	CanDeauthGuest bool `json:"canDeauthGuest"`
}

// Controller is the management plane Foyer holds credentials for -- the object an
// operator actually possesses and configures.
type Controller struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Vendor   string `json:"vendor"`
	BaseURL  string `json:"baseUrl"`
	AuthMode string `json:"authMode"`
	TLSMode  string `json:"tlsMode"`

	// VendorControllerID is Omada's 32-character identifier, taken from the
	// controller's own address bar. Nil for every other vendor.
	VendorControllerID *string `json:"controllerId"`

	// Username is the non-secret half of a local-admin or hotspot-operator
	// credential. The secret half is sealed; see ControllerRepo.Credential.
	Username *string `json:"-"`

	// CredentialSet reports whether a secret exists, which is all the console is
	// ever told about it.
	CredentialSet bool `json:"credentialSet"`

	CertificateFingerprint *string `json:"certificateFingerprint"`
	Reachability           string  `json:"reachability"`
	Version                *string `json:"version"`
	VersionSupported       *bool   `json:"versionSupported"`
	Flavour                *string `json:"-"`

	VendorSites   []VendorSite `json:"vendorSites"`
	LastContactAt *time.Time   `json:"lastContactAt"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"-"`
}

// VendorSite is one of the controller's own sites, and which Foyer site it is bound
// to. Enumerated rather than assumed: a hardcoded `default` is a filed bug in the
// shipped UniFi integration this model is drawn from.
type VendorSite struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	BoundSiteID *string `json:"boundSiteId"`
}

// ControllerProbe is what a probe observed. It is applied to a Controller by
// ControllerRepo.RecordProbe rather than being stored on its own.
type ControllerProbe struct {
	Reachability           string
	Version                *string
	VersionSupported       *bool
	Flavour                *string
	CertificateFingerprint *string
	VendorSites            []VendorSite
}

// SharedSecret is the thing that is one per controller RADIUS profile and many per
// client. Rotating one affects a set, which is the truth and the reason it is its
// own record.
type SharedSecret struct {
	ID            string     `json:"id"`
	SiteID        string     `json:"siteId"`
	Label         string     `json:"label"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastRotatedAt *time.Time `json:"lastRotatedAt"`
	LastUsedAt    *time.Time `json:"lastUsedAt"`
}

// RadiusClient is a clients.conf row: an address, a secret reference, and the two
// BlastRADIUS switches.
type RadiusClient struct {
	ID          string  `json:"id"`
	SiteID      string  `json:"siteId"`
	Name        string  `json:"name"`
	Description *string `json:"description"`

	// Address is one host or a range. Aruba's dynamic-radius-proxy toggle decides
	// which an operator needs and Foyer cannot see that toggle, so both are
	// representable.
	Address netip.Prefix `json:"-"`

	Source string `json:"source"` // discovered | manual
	Driver string `json:"driver"`

	// CoAVendor says which member of the CoA family a `coa` client is: `cisco` or
	// `aruba`. Nil is *nobody has said*, and it is a real answer rather than a
	// missing one -- the two identify a session differently, and an identifier in
	// the wrong spelling matches no session, which a careless NAS answers by
	// ignoring the request and ACKing it. A nil value on a `coa` row is refused a
	// driver by name rather than given a guessed one. Migration 00015.
	//
	// Read only where Driver is `coa`, exactly as CoAPort is read only where CoA
	// is spoken at all.
	CoAVendor *string `json:"coaVendor"`

	ControllerID     *string `json:"controllerId"`
	SharedSecretID   *string `json:"sharedSecretId"`
	NASIdentifier    *string `json:"nasIdentifier"`
	CoAPort          *int    `json:"coaPort"`
	RequireMsgAuth   bool    `json:"-"`
	LimitProxyState  bool    `json:"-"`
	MessageAuthState string  `json:"messageAuthenticator"` // enforced | refused | unknown

	// AccountingConfigured is three-valued. Nil is "not measured" and the console
	// may not render it as "missing": false means every *byte* figure is absent
	// forever, and only that -- who is authorised and until when still comes from
	// the controller.
	AccountingConfigured *bool `json:"accountingConfigured"`

	Reachability  string       `json:"reachability"`
	Capabilities  Capabilities `json:"capabilities"`
	Enabled       bool         `json:"enabled"`
	LastRequestAt *time.Time   `json:"lastRequestAt"`
	ProbedAt      *time.Time   `json:"-"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"-"`
}

// AccessPoint is a radio a guest is standing under. Discovered from a controller,
// never typed, and identified by the MAC the NAS redirect carries.
type AccessPoint struct {
	ID           string  `json:"id"`
	SiteID       string  `json:"siteId"`
	ControllerID *string `json:"controllerId"`

	// MAC is stored as PostgreSQL `macaddr`, so the three spellings a vendor might
	// send are one value. Held here in the canonical colon form.
	MAC string `json:"mac"`

	Name     string  `json:"name"`
	Model    *string `json:"model"`
	Firmware *string `json:"firmware"`

	IPAddress      *string `json:"ipAddress"`
	RadiusClientID *string `json:"radiusClientId"`
	RadiusVia      string  `json:"radiusVia"` // self | controller

	State string `json:"state"` // online | offline | missing | unknown

	// MissingSince is set when discovery stops seeing the AP and cleared when it
	// returns. An AP that disappeared is not an AP that was deleted, and the row
	// survives either way -- deleting it would drop the MAC that resolves last
	// week's session rows.
	MissingSince *time.Time `json:"missingSince"`

	FirstSeenAt time.Time  `json:"-"`
	LastSeenAt  *time.Time `json:"lastSeenAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"-"`
}

// Nas is the contract's flattened view of a RADIUS client together with the access
// point speaking from that address, if there is one.
//
// The flattening lives here rather than in the schema so that the three nouns stay
// three rows. A CoA driver has no access point at all -- DriverShape calls that
// `implicit-single` -- and its Nas is a client with the AP-shaped fields nil.
type Nas struct {
	RadiusClient

	// ID and Name shadow the embedded client's on purpose. These are the device's,
	// and `n.RadiusClient.ID` still reaches the clients.conf row -- which is what
	// rotating a secret and generating config act on. Two ids that mean different
	// things is exactly the confusion the fused model was made of, so both stay
	// reachable and neither is spelled the same way.
	ID   string `json:"id"`
	Name string `json:"name"`

	MAC        *string    `json:"mac"`
	RadiusVia  string     `json:"radiusVia"`
	Model      *string    `json:"model"`
	Firmware   *string    `json:"firmware"`
	IPAddress  *string    `json:"ipAddress"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	SecretSet  bool       `json:"secretSet"`
	SecretUsed *time.Time `json:"secretLastUsedAt"`
}

// Discovery is what a reconciliation run against a controller changed.
type Discovery struct {
	Adopted        int      `json:"adopted"`
	Updated        int      `json:"updated"`
	Missing        int      `json:"missing"`
	SharedSecretID string   `json:"sharedSecretId"`
	AccessPointIDs []string `json:"accessPointIds"`
}

// DiscoveredAP is one device as the controller reported it.
type DiscoveredAP struct {
	MAC       string
	Name      string
	Model     *string
	Firmware  *string
	IPAddress *netip.Addr
	State     string
	RadiusVia string
}

// Plan is what an identity was sold.
//
// EnforcedBy is not decoration. `foyer-sampled` says the figure beside it came from
// accounting that advances on Interim-Update at best every five minutes, and the
// console must render the interval next to it rather than a hard cap.
type Plan struct {
	Name            *string `json:"name"`
	NameLang        *string `json:"nameLang"`
	DurationMinutes *int    `json:"durationMinutes"`
	DataAllowanceMB *int    `json:"dataAllowanceMb"`
	DownKbps        *int    `json:"downKbps"`
	UpKbps          *int    `json:"upKbps"`
	EnforcedBy      string  `json:"enforcedBy"` // nas | foyer-sampled | unenforced
}

// BatchCounts is the state histogram the console renders per batch.
type BatchCounts struct {
	Unused    int `json:"unused"`
	InUse     int `json:"inUse"`
	Exhausted int `json:"exhausted"`
	Expired   int `json:"expired"`
	Revoked   int `json:"revoked"`
}

// VoucherBatch is one printing run.
type VoucherBatch struct {
	ID            string      `json:"id"`
	SiteID        string      `json:"siteId"`
	Label         string      `json:"label"`
	Count         int         `json:"count"`
	CreatedBy     *string     `json:"createdBy"`
	Plan          Plan        `json:"plan"`
	ExpiryMode    string      `json:"expiryMode"`
	ValidForHours *int        `json:"-"`
	ExpiresAt     *time.Time  `json:"expiresAt"`
	CreatedAt     time.Time   `json:"createdAt"`
	Counts        BatchCounts `json:"counts"`
}

// Voucher is one code.
type Voucher struct {
	ID               string     `json:"id"`
	BatchID          string     `json:"batchId"`
	SiteID           string     `json:"siteId"`
	Code             string     `json:"code"`
	State            string     `json:"state"`
	FirstUsedAt      *time.Time `json:"firstUsedAt"`
	ExpiresAt        *time.Time `json:"expiresAt"`
	RevokedAt        *time.Time `json:"revokedAt"`
	CurrentSessionID *string    `json:"currentSessionId"`
	CreatedAt        time.Time  `json:"createdAt"`
}

// Account is a persistent guest login.
type Account struct {
	ID           string     `json:"id"`
	SiteID       string     `json:"siteId"`
	Username     string     `json:"username"`
	DisplayName  *string    `json:"displayName"`
	PasswordHash string     `json:"-"` // argon2id
	Enabled      bool       `json:"enabled"`
	Plan         Plan       `json:"plan"`
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"-"`

	// CredentialKind names every verifier this identity has, and it is COMPUTED
	// from whether a row exists in each of the two opt-in tables rather than
	// stored as a flag somebody can forget to set. A flag can be wrong; a LEFT
	// JOIN cannot.
	//
	//	argon2id             the portal and console verifier, and nothing else
	//	argon2id+nt          plus an NT hash: reachable by PEAP-MSCHAPv2
	//	argon2id+bcrypt      plus a bcrypt verifier: reachable by EAP-TTLS/PAP
	//	argon2id+nt+bcrypt   both
	//
	// The `+bcrypt` half is the price of offering EAP-TTLS/PAP through a second,
	// opted-in verifier in a scheme rlm_pap accepts, and the reason this string names schemes rather than saying "802.1X enabled":
	// an identity opted in to 802.1X has a **bcrypt** verifier, not argon2id, and
	// the console must not describe it as argon2.
	CredentialKind string `json:"credentialKind"`
}

// Session is one accounting row, read-only, forever. radacct belongs to
// FreeRADIUS.
type Session struct {
	ID               string     `json:"id"` // acctuniqueid
	AcctSessionID    string     `json:"acctSessionId"`
	SiteID           string     `json:"siteId"`
	NasID            string     `json:"nasId"`
	NasName          string     `json:"nasName"`
	State            string     `json:"state"` // online | ended | orphaned | unknown
	CallingStationID string     `json:"callingStationId"`
	CalledStationID  *string    `json:"-"`
	Username         *string    `json:"username"`
	IPAddress        *string    `json:"ipAddress"`
	APName           *string    `json:"apName"`
	SSID             *string    `json:"ssid"`
	StartedAt        time.Time  `json:"startedAt"`
	EndedAt          *time.Time `json:"endedAt"`
	LastUpdateAt     *time.Time `json:"lastUpdateAt"`
	TerminateCause   *string    `json:"terminateCause"`

	// InputOctets and OutputOctets are the 64-bit values the generated accounting
	// query already reconstructed from Acct-*-Octets and Acct-*-Gigawords. Never
	// subtract two of these without knowing that: a delta taken across a wrap
	// reads as a session whose total dropped by 4 294 967 296.
	InputOctets  *int64 `json:"inputOctets"`
	OutputOctets *int64 `json:"outputOctets"`

	// CountersWrapped is true when the reconstructed total exceeded 2^32, which is
	// to say the gigawords attributes carried a non-zero high word. It travels
	// with the figure so a screen can say why a raw counter and a total disagree.
	CountersWrapped bool `json:"countersWrapped"`

	SessionSeconds  *int64 `json:"sessionSeconds"`
	AcctInterval    *int64 `json:"-"`
	IdentityKind    string `json:"identityKind"`
	VoucherID       *string
	MACIsRandomised bool `json:"macIsRandomised"`
}

// AuthAttempt is one row of the authentication stream, from **either** layer that
// writes one.
//
// There is no field a password could arrive in, and that is deliberate: the stock
// FreeRADIUS post-auth query inserts the submitted credential into `pass` on every
// attempt, accepted or rejected. Foyer generates that query and omits the column;
// this type is the second gate on the same rule. The driver stream has the same
// property for a different reason -- `nas.APIError`'s URL and body are absent from
// `authorization_events` by construction, so there is nothing here for them to
// arrive through either.
//
// **`Source` says what this row is capable of knowing**, which is why it is the
// first field rather than a rendering detail. A `radius` row is a `radpostauth`
// row: it says an attempt happened and whether it was accepted, and it can never
// say why it was rejected. A `driver` row is Foyer's own record of what the
// controller answered *after* every check Foyer owns had passed -- the failure that
// is invisible from the other stream, because no `radacct` row will ever exist for
// a device that never got on.
type AuthAttempt struct {
	// Source is `radius` or `driver`. The `portal` stream the contract also
	// declares has no writer yet, and this type does not invent one.
	Source string `json:"source"`
	// ID is the row's key **within its own stream**. The two streams have
	// independent sequences, so this is not unique on its own -- see
	// AuthAttempt.Key and the adapter's rendering of `AuthAttempt.id`.
	ID               int64     `json:"id"`
	At               time.Time `json:"at"`
	Username         string    `json:"username"`
	Outcome          string    `json:"outcome"` // accepted | rejected
	Reply            string    `json:"-"`       // Access-Accept | Access-Reject
	CallingStationID *string   `json:"callingStationId"`
	CalledStationID  *string   `json:"calledStationId"`
	NasID            *string   `json:"nasId"`
	SiteID           *string   `json:"siteId"`

	// The driver stream's own four, nil on a `radius` row. They are what make an
	// `authorize-failed` row actionable: `Failure` says whether anybody answered,
	// `Status` says what they answered, and `Attempts` is why an operator can tell
	// a healthy controller from one that missed the first ask.
	GrantID   *string `json:"grantId"`
	VoucherID *string `json:"voucherId"`
	Failure   *string `json:"failure"` // unreachable | refused
	Status    *int    `json:"status"`
	Attempts  *int    `json:"attempts"`
}

// GardenEntry is one pre-auth allowance.
type GardenEntry struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"siteId"`
	Kind      string    `json:"kind"` // fqdn | ip-range
	Value     string    `json:"value"`
	Note      *string   `json:"note"`
	Origin    string    `json:"origin"` // required | operator
	CreatedAt time.Time `json:"createdAt"`
}

// HeroImage is the venue photograph, stored rather than linked.
type HeroImage struct {
	SiteID      string    `json:"siteId"`
	ContentType string    `json:"contentType"`
	Bytes       int       `json:"bytes"`
	Data        []byte    `json:"-"`
	UploadedAt  time.Time `json:"uploadedAt"`
}

// PortalSettings is everything the guest portal renders that a venue chose.
type PortalSettings struct {
	SiteID              string            `json:"siteId"`
	PortalURL           *string           `json:"portalUrl"`
	VenueName           string            `json:"venueName"`
	NetworkName         *string           `json:"networkName"`
	LogoDataURI         *string           `json:"logoDataUri"`
	Languages           []string          `json:"languages"`
	Methods             []string          `json:"methods"`
	ClickThroughMinutes *int              `json:"clickThroughMinutes"`
	TermsSummary        map[string]string `json:"-"`
	// Design is the contract's `PortalDesign`, carried as the JSON it is stored
	// as. Opaque here on purpose: this package does not import the generated
	// types, and the shape is the API's to validate before it is written (see
	// migration 00018). Nil on a write keeps whatever the row already holds —
	// the column default on a new row — so a caller that predates designs
	// cannot wipe one by upserting the fields it does know.
	Design    json.RawMessage `json:"-"`
	UpdatedAt time.Time       `json:"-"`
}

// AuditEntry is one immutable record of a sensitive action.
type AuditEntry struct {
	ID            string         `json:"id"`
	At            time.Time      `json:"at"`
	ActorID       string         `json:"actorId"`
	ActorUsername string         `json:"actorUsername"`
	Kind          string         `json:"kind"`
	Params        map[string]any `json:"params"`
	SiteID        *string        `json:"siteId"`
	SubjectKind   string         `json:"-"`
	SubjectID     string         `json:"-"`
	SubjectName   *string        `json:"-"`
}

// Page is one page of a cursor-paginated list.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}
