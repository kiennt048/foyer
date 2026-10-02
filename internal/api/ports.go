package api

import (
	"context"
	"errors"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
)

// The seam between this package and everything that persists or talks to hardware.
//
// ── why the interfaces live here and not in internal/store ───────────────────
//
// A port is declared by the side that needs it. If the API imported
// `internal/store`'s repository types, the two would have to change together and
// neither could be tested without the other. Declaring them here lets the API be
// driven end to end against in-memory fakes (`internal/api/memory`) and wired to
// real repositories (`internal/apistore`) by adapters that satisfy these
// interfaces. Nothing in this package imports `internal/store`.
//
// ── why the types are the contract's own ─────────────────────────────────────
//
// Every method here speaks in `gen` types — the structs generated from
// `web/contract/openapi.yaml`. That is deliberate and is the opposite of the
// usual advice about not letting a wire format reach a repository.
//
// The reason is that the contract *is* the domain model here. It was designed
// first, and carries decisions the database does not: that a
// counter travels with the freshness of its sample, that `Nas.secretSet` is a
// boolean because the secret is write-only, that `Operator` and `Account` are two
// objects and not one table with a flag. A second Go model mirroring 71 schemas
// would be 71 opportunities for those decisions to be restated slightly
// differently, and the mapping code between them is where they would drift. The
// generated types are regenerated from the contract, so they cannot drift from
// it at all.
//
// The aliases below exist so that a reader of this file sees domain nouns rather
// than a package qualifier on every line.

type (
	Site          = gen.Site
	Nas           = gen.Nas
	NasCreate     = gen.NasCreate
	NasProbe      = gen.NasProbe
	Controller    = gen.Controller
	Voucher       = gen.Voucher
	VoucherBatch  = gen.VoucherBatch
	Session       = gen.Session
	Account       = gen.Account
	Operator      = gen.Operator
	AuditEntry    = gen.AuditEntry
	AuthAttempt   = gen.AuthAttempt
	Alarm         = gen.Alarm
	GardenEntry   = gen.GardenEntry
	PortalSetting = gen.PortalSettings
	HeroImage     = gen.HeroImage
)

// Errors a port may return. Anything else becomes `/problems/internal` with no
// text carried across — see `asAPIError`.
var (
	// ErrNotFound: the row does not exist, or does not belong to this site.
	ErrNotFound = errors.New("api: not found")
	// ErrConflict: a uniqueness constraint refused the write.
	ErrConflict = errors.New("api: conflict")
	// ErrSiteExists: this deployment already has its one site.
	//
	// Separate from ErrConflict because nothing collided: no name was taken and no
	// id was reused. It is a policy refusal — one venue per install — and a client that rendered it as *"that name is already in
	// use"* would send the operator back to the form to try another name, which
	// cannot work.
	ErrSiteExists = errors.New("api: this deployment already has a site")
	// ErrNeedsSite: an operation that adopts into a site was given none, and there
	// is no default to fall back to.
	//
	// Separate from ErrNotFound and from ErrConflict because nothing is missing and
	// nothing collided: the operator simply has to say which venue. Folded into a
	// generic error, `discoverFromController` would answer "Something went wrong"
	// for a condition whose entire remedy is naming a site — the contract calls `siteId` optional and says it defaults to the controller's,
	// which is true only once a controller is bound to one.
	ErrNeedsSite = errors.New("api: this operation needs a site to adopt into")
)

// ── paging ───────────────────────────────────────────────────────────────────

// Page is what every list port returns.
//
// `NextCursor` is nil when there is no further page, and there is deliberately
// no total: the contract says so on `listSessions` and the reason generalises —
// `COUNT(*)` over an accounting table is not affordable at the sizes this
// reaches, and a total that is affordable on five tables and not on the sixth is
// a shape a client cannot rely on.
type Page[T any] struct {
	Items      []T
	NextCursor *string
}

// Window is the cursor and limit every list operation carries.
type Window struct {
	Cursor string
	Limit  int
}

// ── the ports ────────────────────────────────────────────────────────────────

// SiteDirectory answers which sites an operator may administer, and makes them.
// v1 roles are deployment-wide, so this does not yet take a per-operator scope —
// see `listOperators` in the contract for the conditions that would reopen that.
//
// `CreateSite` is on the *directory* rather than in a registry of its own because
// a site has no lifecycle here beyond existing: nothing renames one, nothing
// deletes one, and a `SiteRegistry` interface with a single method would be a
// second name for this one.
//
// It exists at all because a freshly migrated database holds **zero** sites and
// `createNas` requires one. A test world that always starts with a site cannot
// see that the first one is unreachable — the *world cannot express the property*
// shape, at deployment scale.
type SiteDirectory interface {
	Sites(ctx context.Context) ([]Site, error)
	// CreateSite mints the id. The contract does not accept one, because a site
	// id reaches every scoped table and an operator-chosen one is a permanent
	// identifier typed once into a first-run form.
	CreateSite(ctx context.Context, in gen.SiteCreate) (Site, error)
}

// ControllerRegistry holds the management planes an operator actually possesses.
type ControllerRegistry interface {
	ControllersInSite(ctx context.Context, siteID string) ([]Controller, error)
	Controller(ctx context.Context, id string) (Controller, error)
	RegisterController(ctx context.Context, in gen.ControllerCreate, siteID string) (Controller, error)
	AmendController(ctx context.Context, id string, in gen.ControllerUpdate) (Controller, error)
	// ForgetController is refused while access points discovered from it are
	// still registered; the count is what the refusal carries.
	ForgetController(ctx context.Context, id string) error
	// AccessPointsFrom counts what a delete would orphan.
	AccessPointsFrom(ctx context.Context, controllerID string) (int, error)
}

// AccessPointRegistry holds the devices Foyer speaks RADIUS to.
//
// `RegisterAccessPoint` returns the shared secret because the contract returns
// it exactly once and never again; keeping that in the signature is what stops a
// later reader adding a "fetch the secret" method to make a screen easier.
type AccessPointRegistry interface {
	AccessPoints(ctx context.Context, siteID, search string, w Window) (Page[Nas], error)
	AccessPoint(ctx context.Context, id string) (Nas, error)
	RegisterAccessPoint(ctx context.Context, in NasCreate) (nas Nas, sharedSecret string, err error)
	AmendAccessPoint(ctx context.Context, id string, name *string, enabled *bool) (Nas, error)
	ForgetAccessPoint(ctx context.Context, id string) error
	// RecordProbe writes back what a probe measured, and only that. Where the
	// probe measured nothing the stored record is left exactly as it was — an
	// unreachable device does not lose what was last known about it.
	RecordProbe(ctx context.Context, id string, p NasProbe) error
	// StoreRotatedSecret persists a secret the caller generated. Generating one
	// is Foyer's job unconditionally; pushing it is the driver's and may be
	// impossible — see `DeviceOperations`.
	StoreRotatedSecret(ctx context.Context, id, secret string) error
}

// DeviceOperations is the driver seam: everything that leaves the process and
// talks to somebody's network.
//
// It is a port rather than a direct dependency on `internal/nas` for two
// reasons. The API must be drivable with no controller in the room, and
// `docs/rules/nas-drivers.md` is explicit that a hand-written NAS mock
// encodes the author's assumptions — so the fake behind this interface is
// labelled a fake rather than mistaken for coverage.
type DeviceOperations interface {
	ProbeController(ctx context.Context, c Controller) (gen.ControllerProbe, error)
	// Discover reconciles rather than adds: a device the controller no longer
	// reports is marked missing, never deleted.
	Discover(ctx context.Context, c Controller, siteID, vendorSiteID string) (gen.DiscoveryResult, error)
	ProbeAccessPoint(ctx context.Context, n Nas) (NasProbe, error)
	// DriverEvidence answers how this device's capabilities and wire attributes
	// are known, so the UNVERIFIED label reaches the console rather than living
	// in the code.
	//
	// **It is deliberately not part of `ProbeAccessPoint`.** Nothing here is
	// established by a probe: the facts are properties of the driver and the
	// vendor profile and are identical before and after any packet is sent.
	// Returning them from a probe would turn a citation into a measurement,
	// which is the exact thing `NasProbe`'s shape exists to prevent.
	DriverEvidence(ctx context.Context, n Nas) (gen.DriverEvidence, error)
	// SecretRotation answers both questions rotation needs, together.
	//
	// `canPush` is `Capabilities.RotateSecret`. False means rotation is
	// *manual*, not impossible — the narrower reading
	// (`docs/rules/nas-drivers.md`).
	//
	// `class` is what the operator is warned about before they confirm, and it
	// is here rather than computed in the API on purpose: whether a secret
	// change is `none`, `sql-only` or `restart` is a fact about how FreeRADIUS
	// loads clients and how this vendor's driver applies one, and the API is
	// not the package that knows either. Guessing `sql-only` in a handler would
	// be the console telling an operator that nothing restarts, on the strength
	// of nobody having looked.
	SecretRotation(ctx context.Context, n Nas) (canPush bool, class gen.ChangeClass, err error)
	PushSecret(ctx context.Context, n Nas, secret string) error
	// EndSession is what revoking an in-use voucher needs. Whether it can be
	// done at all depends on the NAS, so the answer is reported rather than
	// assumed — see `revokeVoucher`.
	//
	// A failure the device reported is a `*SessionEndError`, so `disconnectSession`
	// can say *refused*, *no answer* or *wrong secret* rather than one sentence
	// for all three. A device whose driver cannot end a session at all answers
	// `ErrDeviceCannotDisconnect`.
	EndSession(ctx context.Context, sessionID string) error
	// CanEndSession answers, **without sending anything**, whether the driver
	// behind this session's device can end a session at all. Nil means it can;
	// `ErrDeviceCannotDisconnect` (wrapped in a `*DisconnectRefusal` carrying
	// the device's name and driver) means it cannot.
	//
	// Separate from `EndSession` because `disconnectSession` revokes a voucher
	// and a grant *before* it asks the device, and a refusal has to come before
	// either of those: an operator told "this device cannot do that" must find
	// nothing else changed.
	CanEndSession(ctx context.Context, sessionID string) error
}

// ErrDeviceCannotDisconnect: the driver behind this device has no way to end a
// session — Ruckus by decision (`docs/rules/nas-drivers.md`), a vendor with no
// driver yet, or a device Foyer cannot build a driver for.
var ErrDeviceCannotDisconnect = errors.New("api: this device's driver cannot end a session")

// DisconnectRefusal is `ErrDeviceCannotDisconnect` with the two facts the
// console interpolates. Never a sentence — `params`, not `detail`.
type DisconnectRefusal struct {
	Driver  gen.NasDriverKind
	NasName string
}

func (e *DisconnectRefusal) Error() string { return ErrDeviceCannotDisconnect.Error() }
func (e *DisconnectRefusal) Unwrap() error { return ErrDeviceCannotDisconnect }

// SessionEndCause is why a device did not end a session, in the four words the
// contract's `SessionDisconnectResult.failure.params.cause` allows.
type SessionEndCause string

const (
	// SessionEndRefused: the device answered and said no — an RFC 5176 NAK, or a
	// controller answering the unauthorize with a 4xx.
	SessionEndRefused SessionEndCause = "refused"
	// SessionEndNoAnswer: nothing answered within the retransmit budget. UDP
	// silence is ambiguous and is reported as exactly that.
	SessionEndNoAnswer SessionEndCause = "no-answer"
	// SessionEndBadSecret: something answered and its Response Authenticator did
	// not verify — the shared secret on the device is not the one Foyer holds.
	SessionEndBadSecret SessionEndCause = "bad-secret"
	// SessionEndOther: anything else. The error's text never reaches a client.
	SessionEndOther SessionEndCause = "error"
)

// SessionEndError is a device's failure to end a session, typed so the handler
// can branch on it rather than on a string.
type SessionEndError struct {
	Cause SessionEndCause
	// ErrorCause is the RFC 5176 §3.5 Error-Cause a NAK carried, when it carried
	// one. It is the most useful number a refusal can hold: 503 means the NAS has
	// no such session, 501 that it was told not to.
	ErrorCause    uint32
	HasErrorCause bool
	Err           error
}

func (e *SessionEndError) Error() string {
	if e.Err == nil {
		return "api: the device did not end the session (" + string(e.Cause) + ")"
	}
	return "api: the device did not end the session (" + string(e.Cause) + "): " + e.Err.Error()
}
func (e *SessionEndError) Unwrap() error { return e.Err }

// GuestAccess withdraws what let a session's device on, so a disconnect is not
// undone by the device's next association.
//
// It is not `SessionLog`, which is read-only and always will be: nothing here
// touches `radacct`. It names what the portal wrote (`portal_grants`) and which
// voucher a session was admitted with.
type GuestAccess interface {
	// RevokeGrantsForSession revokes every open portal grant for this session's
	// client MAC at the session's venue, and answers how many it revoked. Zero is
	// not an error: a RADIUS-only session has no grant.
	RevokeGrantsForSession(ctx context.Context, sessionID string) (int, error)
	// VoucherForSession names the voucher this session was admitted with, or ""
	// when none can be identified — which is an answer, not a failure.
	VoucherForSession(ctx context.Context, sessionID string) (string, error)
}

// VoucherStore holds batches and the individual codes in them.
type VoucherStore interface {
	Batches(ctx context.Context, siteID string, w Window) (Page[VoucherBatch], error)
	Batch(ctx context.Context, id string) (VoucherBatch, error)
	// CreateBatch generates the codes. They are crypto/rand and single-use;
	// generating them in the store rather than here keeps the one transaction
	// that must be atomic in one place.
	CreateBatch(ctx context.Context, in gen.VoucherBatchCreate, actorUsername string) (VoucherBatch, error)
	Vouchers(ctx context.Context, q VoucherQuery) (Page[Voucher], error)
	Voucher(ctx context.Context, id string) (Voucher, error)
	Revoke(ctx context.Context, id string) (Voucher, error)
}

// VoucherQuery is what `listVouchers` can narrow on.
type VoucherQuery struct {
	SiteID  string
	BatchID string
	States  []gen.VoucherState
	Search  string
	Window  Window
}

// SessionLog is read-only and always will be. `radacct` belongs to FreeRADIUS,
// and an "edit session" affordance can never be built — so this interface has no
// write method and gaining one would be a contract change, not a refactor.
type SessionLog interface {
	Sessions(ctx context.Context, q SessionQuery) (Page[Session], error)
	SessionByID(ctx context.Context, id string) (Session, error)
}

// SessionQuery is what `listSessions` can narrow on, plus one field the
// contract does not expose as a query parameter.
//
// `VoucherID` is there because `revokeVoucher` has to find the session a voucher
// is being used for, and `Voucher.currentSessionId` is not that answer: it is
// nullable, it can be stale, and a revoke that keyed on it would silently do
// nothing for a session the accounting knows about and the voucher row does not.
type SessionQuery struct {
	SiteID    string
	States    []gen.SessionState
	NasID     string
	MAC       string
	VoucherID string
	Search    string
	Window    Window
}

// AccountDirectory holds guest identities. argon2id, always — an account opted
// in to PEAP-MSCHAPv2 additionally has an NT hash in a separate,
// envelope-encrypted table that this interface deliberately cannot reach.
type AccountDirectory interface {
	Accounts(ctx context.Context, siteID, search string, w Window) (Page[Account], error)
	CreateAccount(ctx context.Context, in gen.AccountCreate) (Account, error)
	AmendAccount(ctx context.Context, id string, in AccountAmendment) (Account, error)
	DeleteAccount(ctx context.Context, id string) error
}

// AccountAmendment is `updateAccount`'s body.
//
// `Password` and `EapTtlsPap` travel together: the 802.1X opt-in stores a bcrypt
// verifier, a
// verifier is made from the cleartext, and Foyer keeps no cleartext — so the opt-in
// can only be set in the request that carries the password. The implementation
// answers ErrPasswordRequiredFor8021X, ErrPasswordTooLongFor8021X and
// ErrPAPUsernameTaken, and applies all of it or none.
type AccountAmendment struct {
	DisplayName *string
	Enabled     *bool
	Password    *string
	EapTtlsPap  *bool
}

// OperatorDirectory holds the people who may sign in to the console.
//
// `Authenticate` is here rather than in the API because the API must never see
// a password verifier. It takes the submitted password and returns an operator
// or nothing; there is deliberately no method that returns a hash.
type OperatorDirectory interface {
	Operators(ctx context.Context, search string, w Window) (Page[Operator], error)
	OperatorByID(ctx context.Context, id string) (Operator, error)
	CreateOperator(ctx context.Context, in gen.OperatorRequest) (Operator, error)
	AmendOperator(ctx context.Context, id string, in gen.OperatorUpdate) (Operator, error)
	DeleteOperator(ctx context.Context, id string) error
	// AdministratorCount is what refuses the last one. There is no
	// password-reset channel, so a deployment with nobody who can administer it
	// has no recovery short of the binary's first-boot path.
	AdministratorCount(ctx context.Context) (int, error)

	// Authenticate returns the operator when the credential is correct and this
	// operator is enabled. A disabled operator is indistinguishable from an
	// unknown one from the caller's side, because distinguishing them publishes
	// the account's existence.
	Authenticate(ctx context.Context, username, password string) (Operator, error)
	// ChangePassword verifies `current` before setting `next`. The session
	// proves the browser was signed in once; it does not prove who is at the
	// keyboard, and an unattended console is the ordinary case.
	ChangePassword(ctx context.Context, operatorID, current, next string) error
	RecordSignIn(ctx context.Context, operatorID string, at time.Time) error

	// MustChangePassword reports whether this operator is signed in on a
	// temporary password somebody else set -- `operators.must_change_password`,
	// whose only writer is `foyerctl create-operator --must-change-password`.
	// While it is true the gate refuses every operation but `getAuthSession`,
	// `signOut` and `changeOwnPassword`. No install ships a known password, so
	// this is never a shipped credential.
	//
	// **A separate read on every authenticated request, and not a value carried on
	// the session.** Putting it on `AdminSession` at sign-in costs nothing and goes
	// stale in the one direction that matters: an operator changes their password,
	// the flag on their existing session still says true, and the console refuses
	// every screen after they did exactly what it asked -- with no way out, because
	// the change operation would then want a `currentPassword` that is no longer
	// current. A primary-key lookup on a row the pool just read is the cheaper
	// mistake.
	MustChangePassword(ctx context.Context, operatorID string) (bool, error)
}

// ConsoleSetup is the first-access setup: how a fresh install gets its first
// administrator without shipping a credential (`completeAdminSetup`).
//
// The installer opens it with a random code (`foyerctl open-setup`); it closes
// the first time `CompleteSetup` succeeds and never reopens through the API.
type ConsoleSetup interface {
	// SetupOpen reports whether an installer opened the setup and nobody has
	// completed it.
	SetupOpen(ctx context.Context) (bool, error)
	// HasAdministrator reports whether an enabled administrator exists. It is
	// what separates "this install is finished" from "this install never
	// finished", which `SetupOpen` alone cannot tell: both answer `false` when no
	// code is open, and only the second one has nobody to sign in as.
	HasAdministrator(ctx context.Context) (bool, error)
	// CompleteSetup verifies the code and, **in one transaction**, creates an
	// enabled administrator with this username and password and closes the setup,
	// so two browsers racing for it get one administrator and one ErrSetupClosed.
	// ErrSetupCodeWrong for a code that does not match, ErrConflict for a username
	// an operator already has.
	CompleteSetup(ctx context.Context, code, username, password string) (Operator, error)
}

// ErrSetupClosed: there is no setup to complete -- it was completed, or no
// installer opened one.
var ErrSetupClosed = errors.New("api: the first-access setup is not open")

// ErrSetupCodeWrong: the setup is open and this is not its code.
var ErrSetupCodeWrong = errors.New("api: that is not the setup code")

// AdminSessionStore is the console's own session, and nothing else's.
//
// The token is hashed before it reaches this interface: `Create` takes a hash,
// and `Lookup` takes one. A store that held the bearer value could hand out a
// live session from a database dump.
type AdminSessionStore interface {
	CreateAdminSession(ctx context.Context, s AdminSession) error
	LookupAdminSession(ctx context.Context, tokenHash string) (AdminSession, error)
	DeleteAdminSession(ctx context.Context, tokenHash string) error
}

// AdminSession is one signed-in console.
//
// `TokenHash` is a hash and `CSRFToken` is not, and the asymmetry is the point.
// The session token is a bearer credential: whoever holds it is signed in, so a
// database dump must not contain one. The CSRF token is not — it grants nothing
// on its own, and it only has a job when it is presented *alongside* the cookie.
// It is stored in the clear because `getAuthSession` has to be able to hand it
// back: the cookie is `httpOnly`, so a console that could not re-read the token
// after a reload could never make another state-changing request.
type AdminSession struct {
	TokenHash  string
	CSRFToken  string
	OperatorID string
	ExpiresAt  time.Time
}

// AuditLog records what cannot be undone or what is security-relevant.
//
// Read-only from the console's side — there is no update and no delete, and
// there must never be one: a log an administrator can edit is not a log. Append
// is not a write operation the API exposes; it is what the API does *to* the log
// as a side effect of the operations that already require a permission.
type AuditLog interface {
	Audit(ctx context.Context, q AuditQuery) (Page[AuditEntry], error)
	Append(ctx context.Context, e AuditEntry) error
}

// AuditQuery is what `listAudit` can narrow on.
type AuditQuery struct {
	ActorID string
	From    *time.Time
	To      *time.Time
	Window  Window
}

// AuthAttemptLog answers "why can't this guest get online?".
//
// `Coverage` and `BlindSpots` are separate methods because the contract makes
// both required members of the response: an empty result set means *we have no
// record*, and a console that renders that as *nothing went wrong* is the exact
// failure the operation exists to prevent. A port that could return the items
// without the bounds would let a caller forget them.
type AuthAttemptLog interface {
	AuthAttempts(ctx context.Context, q AuthAttemptQuery) (Page[AuthAttempt], error)
	Coverage(ctx context.Context, siteID string) (gen.MacCoverage, error)
	BlindSpots(ctx context.Context, siteID string) ([]gen.AuthBlindSpot, error)
}

// AuthAttemptQuery is what `listAuthAttempts` can narrow on.
type AuthAttemptQuery struct {
	SiteID  string
	Search  string
	Outcome *gen.AuthOutcome
	Source  *gen.AuthSource
	Window  Window
}

// PortalSettingsStore is the writable twin of `PortalContext`.
type PortalSettingsStore interface {
	PortalSettings(ctx context.Context, siteID string) (PortalSetting, error)
	AmendPortalSettings(ctx context.Context, siteID string, in gen.PortalSettingsUpdate) (PortalSetting, error)
	// StoreHeroImage takes decoded bytes, not base64: the size ceiling is
	// enforced on what will actually be served, and a caller that passed the
	// encoded length would be enforcing a limit a third too generous.
	StoreHeroImage(ctx context.Context, siteID, filename, contentType string, data []byte) (HeroImage, error)
	DeleteHeroImage(ctx context.Context, siteID string) error
	// HeroImageBytes returns the stored bytes for one site's photograph, for
	// the public `GET /venue/...` route on the portal listener. A guest has
	// no session to scope by — the site id is in the path — so this is a
	// narrower read than `PortalSettings`, and the only member the route needs.
	HeroImageBytes(ctx context.Context, siteID string) (contentType string, data []byte, err error)
}

// WalledGardenStore holds unauthenticated egress.
//
// `Garden` returns the driver coverage alongside the entries because a site is
// not homogeneous: an entry that works on a UniFi access point does nothing at
// all on a driver reporting `GardenKind: none`, and the operator has to be told
// that beside the entry rather than after a support call.
type WalledGardenStore interface {
	Garden(ctx context.Context, siteID string) (gen.WalledGarden, error)
	AddGardenEntry(ctx context.Context, siteID string, in gen.GardenEntryCreate) (GardenEntry, error)
	// RemoveGardenEntry is refused for an entry whose origin is `required` —
	// the portal's own FQDN, its OCSP/CRL endpoints, the RFC 8908 endpoint.
	// Removing one presents to every guest as a network that is simply broken.
	RemoveGardenEntry(ctx context.Context, siteID, entryID string) error
}

// PrivacyStore is retention periods and data-subject erasure.
//
// The retention policy speaks the contract's types because the categories *are*
// the contract's closed enum. The erasure takes the request's subject and returns
// the receipt, because resolving a subject to the identifiers the tables carry and
// running every registered eraser is one job, and the API half-doing it would be
// the second copy of the registry.
type PrivacyStore interface {
	RetentionPolicy(ctx context.Context) (gen.RetentionPolicy, error)
	// AmendRetentionPolicy applies every change or none. `actor` is denormalised
	// onto the stored override, for the audit log's reason.
	//
	// **`record` is the change's audit entry, and it is not optional**: the port
	// writes it in the same
	// transaction as the change, and a change whose entry cannot be written — or
	// whose `record` fails — is not made. A retention period set short is data
	// the next sweep deletes; it is never changed without a record of who did it.
	AmendRetentionPolicy(ctx context.Context, changes []gen.RetentionPolicyChange, actor string,
		record func() (AuditEntry, error)) (gen.RetentionPolicy, error)
	// EraseDataSubject answers ErrNotAMACAddress for a MAC that is not one,
	// ErrNotAPhoneNumber for a number that is not one, ErrNotAnEmailAddress for an
	// address that is not one and ErrNotFound for an unknown account. The receipt's
	// `id` is the caller's to mint, so it can be the audit entry's subject id.
	//
	// `record` builds the erasure's audit entry from the receipt; the port writes it
	// in the transaction that erases Foyer's own tables, and fails the erasure —
	// leaving those tables as they were — when it cannot.
	EraseDataSubject(ctx context.Context, subject ErasureSubject,
		record func(gen.DataSubjectErasure) (AuditEntry, error)) (gen.DataSubjectErasure, error)
}

// ErasureSubject is one erasure request's subject: the kind, and the one
// identifier that kind is named by.
type ErasureSubject struct {
	Kind                         gen.DataSubjectKind
	MAC, AccountID, Phone, Email string
}

// ErrNotAMACAddress: an erasure subject that is not a MAC in a spelling a NAS sends.
var ErrNotAMACAddress = errors.New("api: not a MAC address")

// ErrNotAPhoneNumber: an erasure subject that is not eight to fifteen digits.
var ErrNotAPhoneNumber = errors.New("api: not a phone number")

// ErrNotAnEmailAddress: an erasure subject that fails the sign-in form's shape
// check for an e-mail address.
var ErrNotAnEmailAddress = errors.New("api: not an e-mail address")

// DashboardReader is the overview and the alarm list.
//
// Every figure it returns carries its own freshness, because the figures come
// from accounting that arrives every five minutes at best and on some
// deployments never arrives at all. A bare number here is the portal promising
// a cap that nothing on the network applies.
type DashboardReader interface {
	Overview(ctx context.Context, siteID string) (gen.Overview, error)
	Alarms(ctx context.Context, siteID string) ([]Alarm, error)
}

// ReportReader answers the `reports` operations: three streamed exports and the
// trends behind the dashboard chart.
//
// **Every export method takes a visitor and returns nothing to hold.** An export
// is a range of a table that only grows — `radacct` above all — and a method that
// returned a slice would put the whole of it in this process's memory, in a
// container with a 320 MiB ceiling, at the request of whoever can reach the
// console. The visitor is called once per row in the contract's order, and its
// error stops the scan.
//
// The rows are the contract's own types where the contract has one — a `Session`
// is exported as the same `Session` `listSessions` answers, so the two cannot
// disagree about a state, a freshness or a wrapped counter. A grant has no contract
// type (`PortalStatus` is a deliberate projection of one), so it travels as the
// portal's own `portal.Grant`.
//
// Deliberately **not** on `SessionLog`, `VoucherStore` or the portal ports. Those
// are the aggregates' own seams; a bulk read behind its own permission is a
// different capability, and one interface is what lets a deployment wire it or not
// — and what a later export (marketing consent) extends with one method.
type ReportReader interface {
	// SessionsStarted visits the site's sessions with `startedAt` in the range,
	// oldest first.
	SessionsStarted(ctx context.Context, siteID string, r Range, visit func(Session) error) error
	// VoucherActivity visits the site's vouchers created or first used in the
	// range, ordered by creation, oldest first.
	VoucherActivity(ctx context.Context, siteID string, r Range, visit func(Voucher) error) error
	// GuestGrants visits the site's portal grants made in the range, oldest first.
	GuestGrants(ctx context.Context, siteID string, r Range, visit func(portal.Grant) error) error
	// Trends answers one bucket per range given, in the same order. `Start` and
	// `End` are the caller's; the counts and the freshness are the store's.
	Trends(ctx context.Context, siteID string, buckets []Range) ([]gen.TrendBucket, error)
}

// Range is `[From, To)`.
type Range struct {
	From time.Time
	To   time.Time
}

// Contains is the half-open rule, stated once for every implementation.
func (r Range) Contains(t time.Time) bool { return !t.Before(r.From) && t.Before(r.To) }

// Ports is the whole seam, in one value.
//
// A nil member is not a panic waiting to happen: `Server` refuses to start
// without the ones its enabled operations need, and reports which are missing.
// That is what lets an adapter wire the repositories one aggregate at a time.
type Ports struct {
	Sites         SiteDirectory
	Controllers   ControllerRegistry
	AccessPoints  AccessPointRegistry
	Devices       DeviceOperations
	Vouchers      VoucherStore
	Sessions      SessionLog
	Accounts      AccountDirectory
	Operators     OperatorDirectory
	AdminSessions AdminSessionStore
	// Setup is the first-access setup. Nil answers `required: false` and refuses
	// `completeAdminSetup` with `409`, which is the truth for an install nobody
	// opened one on.
	Setup        ConsoleSetup
	Audit        AuditLog
	AuthAttempts AuthAttemptLog
	Portal       PortalSettingsStore
	Garden       WalledGardenStore
	Dashboard    DashboardReader
	Privacy      PrivacyStore
	Reports      ReportReader
	Guests       GuestAccess
	// PhoneSignIn is how a site sends phone sign-in codes — see handlers_phone.go.
	PhoneSignIn PhoneSignInStore
	GuestData   GuestDataStore
	// Network is the `network` tag: transport and RADIUS-plane settings. See
	// ports_network.go.
	Network NetworkSettingsStore
	// TimeZones is each site's calendar — see handlers_sitetime.go.
	TimeZones SiteTimeZoneStore
	// Maintenance and GuestWifi are ports_settings.go's.
	Maintenance MaintenanceStore
	GuestWifi   GuestWifiKeyStore
	// PortalGrants is the open portal authorizations and their controller-side
	// disconnect — see ports_portalgrants.go.
	PortalGrants PortalGrantStore
	// DeviceKeys is the trusted-device cookie's key ring, persisted and sealed so
	// a restart does not untrust every device at once — see
	// devicecookie.go. Nil keeps a per-process key.
	DeviceKeys DeviceCookieKeyStore
}

// GuestDataStore is what a venue asks its guests for, what they gave, and the
// marketing consents they ticked.
//
// **Two records, and the port keeps them apart.** `RecordGuestDetails` is what a
// guest typed, bound to the grant it was typed for; `RecordMarketingConsent` is
// called only for a guest who ticked the box, and is the only way a consent row
// comes to exist. Nothing here infers one from the other or from the terms.
type GuestDataStore interface {
	// GuestDataSettings answers a venue that never saved any with no fields and
	// consent off, never with ErrNotFound.
	GuestDataSettings(ctx context.Context, siteID string) (gen.GuestDataSettings, error)
	// SetGuestDataSettings replaces the setting and publishes every consent text
	// under its version, keeping every version ever published.
	SetGuestDataSettings(ctx context.Context, siteID string, in gen.GuestDataSettingsUpdate) (gen.GuestDataSettings, error)
	RecordGuestDetails(ctx context.Context, d GuestDetails) error
	// RecordMarketingConsent answers ErrUnknownConsentVersion for a version this
	// venue never published: there is no sentence to say the guest agreed to.
	RecordMarketingConsent(ctx context.Context, c ConsentGiven) error
	MarketingConsents(ctx context.Context, siteID string, state *gen.MarketingConsentState, w Window) (Page[gen.MarketingConsent], error)
	// WithdrawMarketingConsent is idempotent, and ErrNotFound for a consent that is
	// not this venue's.
	WithdrawMarketingConsent(ctx context.Context, siteID, consentID string) (gen.MarketingConsent, error)
	// WithdrawDeviceConsents withdraws every active consent one device gave at one
	// venue, on the guest's own request.
	WithdrawDeviceConsents(ctx context.Context, siteID, mac string) (int, error)
	// ConsentsBetween visits the consents given or withdrawn in the range, by when
	// they were given, oldest first. The export's rows.
	ConsentsBetween(ctx context.Context, siteID string, r Range, visit func(gen.MarketingConsent) error) error
}

// GuestDetails is what a guest typed into a sign-in form. Nil is blank.
type GuestDetails struct {
	GrantID, SiteID    string
	Name, Phone, Email *string
}

// ConsentGiven is a ticked box, with the version of the text beside it.
type ConsentGiven struct {
	SiteID, ClientMAC  string
	GrantID            *string
	Name, Phone, Email *string
	Version            string
}

// ErrUnknownConsentVersion: a consent naming a text the venue never published.
var ErrUnknownConsentVersion = errors.New("api: not a consent text this venue published")
