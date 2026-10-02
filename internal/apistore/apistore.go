// Package apistore joins the two halves of one seam.
//
// `internal/api` declares fifteen narrow interfaces in `ports.go` and speaks the
// contract's generated types. `internal/store` owns PostgreSQL and speaks the
// three-noun object model — controller, RADIUS client, access point. Neither
// imports the other, so without this package `api.NewServer` returns a server with
// no ports wired and every operation answers `501`.
//
// ── why this is its own package ──────────────────────────────────────────────
//
// An adapter is where two models meet, so it belongs to neither. Putting it inside
// `internal/api` would make the API import the store — which is the coupling
// `ports.go` exists to avoid, and would put the store's rewrite back on the API's
// build path. Putting it inside `internal/store` would make the store import the
// contract's generated types, so a contract change would break the schema layer. A
// third package costs one import in `cmd/foyer` and keeps both invariants.
//
// ── what it does not do ──────────────────────────────────────────────────────
//
// It adds no behaviour that either side could have carried. Where a repository can
// answer a port directly it is called directly; where it cannot, the gap is
// **stated in place and reported**, never papered over. `internal/store/compat.go`
// is scheduled for deletion rather than extension, so nothing here builds on it.
package apistore

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/identity/sms"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/store"
)

// Config is what an Adapter needs.
type Config struct {
	// Store is required. There is no nil-store mode: a control plane without its
	// database is not a degraded control plane, and `cmd/foyer` refuses to start
	// without one for the same reason.
	Store *store.Store

	// Now, NewID and NewSecret exist so a test can make a response reproducible
	// without the adapter knowing it is under test. They default to the real
	// thing — `crypto/rand`, never `math/rand`.
	Now       func() time.Time
	NewID     func(prefix string) string
	NewSecret func() (string, error)

	// Changes classifies what applying a change does to the running FreeRADIUS.
	// Nil means the conservative classifier — see ChangeClassifier below.
	Changes ChangeClassifier

	// Drivers builds a NASDriver for a registered controller. Nil means the real
	// vendor drivers in `internal/nas`.
	Drivers Drivers

	// NASDrivers builds a NASDriver for a device Foyer speaks to directly — the
	// CoA family, which has no controller to build one from. See nasdrivers.go for
	// why it is a second interface rather than a method on `Drivers`.
	//
	// Nil takes `Drivers` when that value also implements it, so a harness supplies
	// one fake rather than two, and the real vendor drivers otherwise.
	NASDrivers NASDrivers

	// PortalLimiter bounds the pre-authentication state. Nil means an in-process
	// token bucket on `internal/portal`'s defaults.
	//
	// It is on the adapter rather than left to `api.New` because `cmd/foyer`
	// builds **two** `portal.Service` values over one seam — the API server's, and
	// the one behind the front door's `EntryHandler` — and two limiters would be
	// two budgets for one guest.
	PortalLimiter portal.Limiter

	// Retention is the foyer_retention connection data-subject erasure deletes
	// through. Nil is allowed -- cmd/foyer keeps serving when that pool will not
	// open -- and eraseDataSubject then answers /problems/internal rather than
	// pretending to have erased anything.
	Retention store.RetentionEraser
	// SMSProvider builds the adapter phone sign-in sends through. Nil means
	// `sms.New` — the real eSMS adapters. A test supplies a fake here; no saved
	// configuration can select one.
	SMSProvider func(sms.Config) (sms.Provider, error)
	// MaintenanceChanged is told the maintenance settings an operator just saved,
	// so the scheduler in this process runs to them at once rather than on its next
	// read. cmd/foyer wires it to the scheduler; nil is a process with none.
	MaintenanceChanged func(store.MaintenanceValues)
}

// Adapter implements every port `internal/api` declares, over `internal/store`.
type Adapter struct {
	st         *store.Store
	now        func() time.Time
	newID      func(prefix string) string
	newSecret  func() (string, error)
	changes    ChangeClassifier
	drivers    Drivers
	nasDrivers NASDrivers
	limiter    portal.Limiter
	retention  store.RetentionEraser
	// phone is phone sign-in's service, one per adapter — see phone.go.
	phone       *otp.Service
	smsProvider func(sms.Config) (sms.Provider, error)
	// zones caches each site's calendar — see sitetime.go.
	zones zoneCache

	maintenanceChanged func(store.MaintenanceValues)

	// addresses is the RFC 8908 address book — see portaladdresses.go. One per
	// adapter, so both listeners share its write throttle.
	addresses *addressBook
}

// Compile-time proof that every port is satisfied. Fifteen assertions rather
// than one, so a missing method names the interface it is missing from.
var (
	_ api.SiteDirectory       = (*Adapter)(nil)
	_ api.ControllerRegistry  = (*Adapter)(nil)
	_ api.AccessPointRegistry = (*Adapter)(nil)
	_ api.DeviceOperations    = (*Adapter)(nil)
	_ api.VoucherStore        = (*Adapter)(nil)
	_ api.SessionLog          = (*Adapter)(nil)
	_ api.AccountDirectory    = (*Adapter)(nil)
	_ api.OperatorDirectory   = (*Adapter)(nil)
	_ api.AdminSessionStore   = (*Adapter)(nil)
	_ api.AuditLog            = (*Adapter)(nil)
	_ api.AuthAttemptLog      = (*Adapter)(nil)
	_ api.PortalSettingsStore = (*Adapter)(nil)
	_ api.WalledGardenStore   = (*Adapter)(nil)
	_ api.DashboardReader     = (*Adapter)(nil)
	_ api.ReportReader        = (*Adapter)(nil)
)

// New builds an Adapter.
func New(cfg Config) (*Adapter, error) {
	if cfg.Store == nil {
		return nil, errors.New("apistore: a store is required")
	}
	a := &Adapter{
		st:         cfg.Store,
		now:        cfg.Now,
		newID:      cfg.NewID,
		newSecret:  cfg.NewSecret,
		changes:    cfg.Changes,
		drivers:    cfg.Drivers,
		nasDrivers: cfg.NASDrivers,
		limiter:    cfg.PortalLimiter,
		retention:  cfg.Retention,

		smsProvider: cfg.SMSProvider,

		maintenanceChanged: cfg.MaintenanceChanged,
	}
	a.addresses = newAddressBook(a)
	if a.now == nil {
		a.now = time.Now
	}
	if a.newID == nil {
		a.newID = NewID
	}
	if a.newSecret == nil {
		a.newSecret = NewSecret
	}
	if a.changes == nil {
		a.changes = ConservativeChangeClass{}
	}
	if a.drivers == nil {
		a.drivers = VendorDrivers{}
	}
	if a.nasDrivers == nil {
		// A harness that supplies one fake gets it on both seams. One that has no
		// opinion about the per-NAS half gets the real drivers, which refuse
		// everything they cannot build rather than answering plausibly.
		if nd, ok := a.drivers.(NASDrivers); ok {
			a.nasDrivers = nd
		} else {
			a.nasDrivers = VendorDrivers{}
		}
	}
	if a.limiter == nil {
		// There is no safe default of "none". Every authenticating operation on
		// the guest surface is an HTTP call to the venue's controller, on a
		// listener reachable by anyone within radio range.
		a.limiter = portal.NewTokenBucket(portal.DefaultBurst, portal.DefaultRefill)
	}
	a.phone = newPhoneService(a)
	return a, nil
}

// Ports hands the whole seam to `api.New`.
//
// Every member is the same value, because the interfaces are narrow views of one
// database rather than fifteen stores. Naming them separately is still what lets
// a handler say which capability it needs, and what lets a test replace one.
func (a *Adapter) Ports() api.Ports {
	return api.Ports{
		Sites:         a,
		Controllers:   a,
		AccessPoints:  a,
		Devices:       a,
		Vouchers:      a,
		Sessions:      a,
		Accounts:      a,
		Operators:     a,
		AdminSessions: a,
		Setup:         a,
		Audit:         a,
		AuthAttempts:  a,
		Portal:        a,
		Garden:        a,
		Dashboard:     a,
		Privacy:       a,
		Reports:       a,
		Guests:        a,
		PhoneSignIn:   a,
		GuestData:     a,
		// The network tag, with no FreeRADIUS tree writer: `cmd/foyer` replaces
		// it with one that has the deployment's applier and snapshot.
		Network:      NewNetwork(NetworkConfig{Store: a.st, Now: a.now}),
		TimeZones:    a,
		Maintenance:  a,
		GuestWifi:    a,
		PortalGrants: a,
		DeviceKeys:   a,
	}
}

// Store exposes the underlying store, for a caller that also needs the
// repositories directly — `cmd/foyer` wires the policy engine to the same one.
func (a *Adapter) Store() *store.Store { return a.st }

// ── errors ───────────────────────────────────────────────────────────────────

// out translates the store's sentinels into the API's.
//
// Only the two with a meaning are translated, and everything else is passed
// through to become `/problems/internal` carrying no text. Those two are the only
// errors a port can return with a meaning; a store error that reached a client
// would carry a table name and a constraint.
func out(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return api.ErrNotFound
	case errors.Is(err, store.ErrConflict):
		return api.ErrConflict
	default:
		return err
	}
}

// ── identifiers and secrets ──────────────────────────────────────────────────

// NewID mints an identifier: `crypto/rand`, base32 without padding, so it is safe
// in a path segment and in a `clients.conf` name.
//
// The same construction `internal/api` uses for audit ids. A sequential id would
// leak how many of a thing a deployment has, and would collide across two
// processes writing to one database.
func NewID(prefix string) string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("apistore: crypto/rand: %v", err))
	}
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
}

// NewSecret mints a RADIUS shared secret.
//
// There is no default and no fallback: FreeRADIUS ships an example secret in its
// sample configuration, and a shipped credential is how appliance software gets
// mass-owned (`docs/rules/security.md` §7). It returns an error rather than a
// placeholder when randomness is unavailable.
func NewSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// ── change classification ────────────────────────────────────────────────────

// ChangeClassifier answers what applying a change actually does to the running
// FreeRADIUS: nothing, a plain SQL write, or a restart.
//
// **The interface is declared here and the answer is not.** Whether a changed
// shared secret takes effect without a restart is a fact about how FreeRADIUS
// loads SQL clients and how a vendor's driver applies one, and `internal/raddb` is
// the package that knows, and `docs/rules/radius.md` requires every change to be classified `none` /
// `sql-only` / `restart` as a tested contract.
//
// `internal/raddb` does not export that classification today, so this package
// depends on the narrowest thing it can: an interface it can satisfy conservatively
// now and hand over later. **Left open rather than guessed.**
type ChangeClassifier interface {
	// SecretChangeClass answers for a shared-secret rotation on this driver.
	SecretChangeClass(driver gen.NasDriverKind) gen.ChangeClass
}

// ConservativeChangeClass answers `restart` for every driver.
//
// `restart` is the loud answer and is the only safe direction for a value whose
// entire job is to warn: FreeRADIUS 3.2.x reads SQL clients at startup, and SIGHUP
// does not reliably reload clients, listeners or virtual servers, so a changed
// secret taking effect is a restart until somebody measures otherwise. Answering
// `sql-only` here would be the console telling an operator that nothing restarts —
// and therefore that no corporate laptop on site will re-authenticate — on the
// strength of nobody having looked.
type ConservativeChangeClass struct{}

// SecretChangeClass implements ChangeClassifier.
func (ConservativeChangeClass) SecretChangeClass(gen.NasDriverKind) gen.ChangeClass {
	return gen.ChangeClassRestart
}

// ── small helpers ────────────────────────────────────────────────────────────

func ptr[T any](v T) *T { return &v }

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
