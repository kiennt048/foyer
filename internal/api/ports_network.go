package api

import (
	"context"
	"errors"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The `network` tag's seam. Every operator setting is set in the console, stored
// in the database, and takes effect without a restart.
//
// **Persistence only.** The bounds, the effective value and where it came from,
// the certificate checks and the audit entries are this package's, in
// netsettings.go and handlers_network.go; the store keeps what it is given, and the
// two RADIUS actions — the client-table sync and the configuration apply — are the
// adapter's because they are the ones that reach past the database.

// NetworkSettingsStore persists the network settings and runs the RADIUS actions.
type NetworkSettingsStore interface {
	// StoredNetwork answers what was saved here. A new install has saved nothing,
	// which is a zero value and never ErrNotFound.
	StoredNetwork(ctx context.Context) (StoredNetwork, error)
	SaveTransport(ctx context.Context, in StoredTransport, actor string) error

	// PortalCertificate answers the stored pair, key included, for the one caller
	// that serves it; ErrNotFound when none is stored. The key never reaches an
	// answer this package sends.
	PortalCertificate(ctx context.Context) (StoredCertificate, error)
	// PutPortalCertificate seals the key and stores the pair, and portalURL with
	// it when not nil, in one write. ErrKeyringAbsent when there is nothing to
	// seal it under.
	PutPortalCertificate(ctx context.Context, certPEM string, keyPEM []byte, portalURL *string, actor string) error
	DeletePortalCertificate(ctx context.Context) error

	SaveRadius(ctx context.Context, maxServers int, eapTLS bool, actor string) error
	// EapCertificate is ErrNotFound when none is stored; the key is not on it.
	EapCertificate(ctx context.Context) (StoredCertificate, error)
	PutEapCertificate(ctx context.Context, certPEM string, keyPEM []byte, caPEM string, actor string) error

	// SyncRadiusClients writes the client table FreeRADIUS reads and records what
	// it did. A refusal or a failure is an answer, not an error: the error return
	// is for the record not being writable at all.
	SyncRadiusClients(ctx context.Context, trigger gen.RadiusSyncTrigger, actor string) (gen.RadiusClientSync, error)
	// ApplyRadius generates FreeRADIUS's tree from the stored settings and
	// restarts it, and records the outcome.
	ApplyRadius(ctx context.Context, actor string) (gen.RadiusApply, error)
	// ApplyAvailable reports whether this deployment writes the tree from this
	// process and can restart the server.
	ApplyAvailable() bool
	LastRadiusOperations(ctx context.Context) (*gen.RadiusClientSync, *gen.RadiusApply, error)

	// CoaTimings answers every device whose driver speaks RFC 5176 itself, with
	// its stored timing when there is one.
	CoaTimings(ctx context.Context) ([]CoaTimingRow, error)
	// SetCoaTiming stores one device's timing on its RADIUS client; nil clears it.
	// ErrNotFound for an unknown device, ErrNoCoA for one whose driver does not
	// speak CoA.
	SetCoaTiming(ctx context.Context, nasID string, t *gen.CoaTimingValues) (CoaTimingRow, error)
}

// ErrKeyringAbsent: there is no key-encryption key to seal a private key under.
var ErrKeyringAbsent = errors.New("api: no key-encryption key is configured")

// ErrNoCoA: this device's driver does not send CoA or Disconnect itself.
var ErrNoCoA = errors.New("api: this device's driver does not speak RFC 5176")

// StoredNetwork is what was saved in the console. Nil is "not saved here".
type StoredNetwork struct {
	PortalURL    *string
	VenueURL     *string
	Throttles    *gen.SignInThrottles
	BodyCeilings *gen.BodyCeilings

	RadiusMaxServers *int
	EapTLSEnabled    bool
	// RadiusChangedAt is when a generator setting last changed.
	RadiusChangedAt *time.Time

	UpdatedAt *time.Time
	UpdatedBy *string
}

// StoredTransport is one `updateNetworkSettings`, already checked.
type StoredTransport struct {
	PortalURL    *string
	VenueURL     *string
	Throttles    gen.SignInThrottles
	BodyCeilings gen.BodyCeilings
}

// StoredCertificate is a stored pair. KeyPEM is empty except from
// PortalCertificate, and is never logged.
type StoredCertificate struct {
	CertificatePEM string
	KeyPEM         []byte
	CAPEM          string
	UpdatedAt      time.Time
	UpdatedBy      string
}

// CoaTimingRow is one CoA-speaking device and its stored timing, nil for defaults.
type CoaTimingRow struct {
	NasID  string
	SiteID string
	Name   string
	Driver gen.NasDriverKind
	Stored *gen.CoaTimingValues
}
