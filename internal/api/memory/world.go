// Package memory is an in-memory implementation of every port `internal/api`
// declares.
//
// **It is a fake and it is labelled one.** `docs/rules/testing.md` is
// explicit that a hand-written mock encodes the author's assumptions — the same
// assumptions that produced the bug — so nothing here is evidence about
// hardware, about PostgreSQL, or about what a UniFi controller does. What it is
// evidence about is the API layer above it: routing, the gates, pagination,
// filtering, the shape of every response, and the refusals.
//
// It exists for two callers:
//
//   - the tests in `internal/api`, which need a store they can put a known world
//     into and drive;
//   - `tools/codegen/differ`, the differential harness, which **seeds this world
//     from the running `web/mock`** and then drives the same scenario into both.
//     Seeding rather than transcribing is deliberate: every timestamp in
//     `web/fixtures` is `Date.now() ± a fixed offset` computed at module load, so
//     a Go copy of the fixture world would disagree with the mock on every date
//     the moment either process restarted.
//
// A deployment uses the adapters over `internal/store`'s repositories
// (`internal/apistore`) instead. The port interfaces are what both sides
// implement, so the swap is additive.
package memory

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
)

// World is the data, with no behaviour on it, so that a harness can load one
// wholesale from JSON. `Store` is what implements the ports over it.
type World struct {
	Sites       []gen.Site                     `json:"sites"`
	Controllers []gen.Controller               `json:"controllers"`
	Nas         []gen.Nas                      `json:"nas"`
	Batches     []gen.VoucherBatch             `json:"batches"`
	Vouchers    []gen.Voucher                  `json:"vouchers"`
	Sessions    []gen.Session                  `json:"sessions"`
	Accounts    []gen.Account                  `json:"accounts"`
	Operators   []gen.Operator                 `json:"operators"`
	Audit       []gen.AuditEntry               `json:"audit"`
	Attempts    []gen.AuthAttempt              `json:"attempts"`
	Garden      map[string]gen.WalledGarden    `json:"garden"`
	Portal      map[string]gen.PortalSettings  `json:"portal"`
	Alarms      map[string][]gen.Alarm         `json:"alarms"`
	Overviews   map[string]gen.Overview        `json:"overviews"`
	Coverage    map[string]gen.MacCoverage     `json:"coverage"`
	BlindSpots  map[string][]gen.AuthBlindSpot `json:"blindSpots"`
	Capability  map[string]gen.ControllerProbe `json:"controllerProbes"`
	NasProbes   map[string]gen.NasProbe        `json:"nasProbes"`
	Discovery   map[string]gen.DiscoveryResult `json:"discovery"`

	// Credentials maps an operator id to the password this fake will accept.
	//
	// It holds a plaintext password, which is exactly what a real store must
	// never do — and that is why it lives in a package named `memory` under this
	// comment. A real `OperatorDirectory` verifies an argon2id verifier it never
	// returns; this one exists only so a test can sign in.
	Credentials map[string]string `json:"credentials"`
}

// Store implements every port over a World.
type Store struct {
	mu    sync.RWMutex
	world *World

	admin  map[string]api.AdminSession
	nextID int
	clock  func() time.Time

	// mustChange models `operators.must_change_password`. See MustChangePassword
	// for why it is a stored flag rather than a comparison.
	mustChange map[string]bool

	// setupDigest models the open `console_setup` row: the SHA-256 of the code an
	// installer printed, nil when no setup is open. See setup.go.
	setupDigest []byte

	// heroBlobs holds venue photograph bytes by `HeroImage.url`. The world
	// carries metadata only — a JSON fixture cannot hold megabytes of JPEG —
	// so the bytes live beside it, keyed by the URL both sides already agree
	// on. A second map rather than a field on the world, because serialising
	// bytes into a fixture snapshot would ship them to every test that loads
	// one.
	heroBlobs map[string]heroBlob

	// retention holds the operator's retention overrides; see privacy.go.
	retention map[gen.RetentionCategoryId]int

	// guestData is the guest-data port's state; see guestdata.go.
	guestData memGuestData
	// settings is the maintenance and guest Wi-Fi ports' state; see settings.go.
	settings memSettings
	// deviceKeys is the trusted-device key ring, newest first; see devicekeys.go.
	deviceKeys []memDeviceKey
	// use by `PortalPorts`. Exposed so a test can read what was granted — the
	// named security test asserts that **nothing** was, and a store with no way
	// to observe emptiness would leave it asserting a status code that a portal
	// refusing for the wrong reason also returns.
	PortalStore *portal.MemoryStore

	// SecretPush decides what `Capabilities.RotateSecret` reports and whether a
	// push succeeds. Default: every device can be pushed to.
	SecretPush func(n gen.Nas) (bool, gen.ChangeClass, error)
	// EndSessionErr decides whether the driver can end a session. Default: yes.
	EndSessionErr func(sessionID string) error
	// CanEndSessionErr overrides whether the device's driver can end a session at
	// all. Default: the device's stored `canDeauthGuest`.
	CanEndSessionErr func(sessionID string) error

	// phone is phone sign-in's state, created on first use — see phone.go.
	phoneOnce sync.Once
	phone     *PhoneWorld
}

// heroBlob is one stored photograph: its content type and its bytes.
type heroBlob struct {
	contentType string
	data        []byte
}

// NewStore wraps a world. A nil world becomes an empty one.
func NewStore(w *World) *Store {
	if w == nil {
		w = &World{}
	}
	w.ensureMaps()
	return &Store{
		world:     w,
		admin:     map[string]api.AdminSession{},
		clock:     time.Now,
		heroBlobs: map[string]heroBlob{},
	}
}

func (w *World) ensureMaps() {
	if w.Garden == nil {
		w.Garden = map[string]gen.WalledGarden{}
	}
	if w.Portal == nil {
		w.Portal = map[string]gen.PortalSettings{}
	}
	if w.Alarms == nil {
		w.Alarms = map[string][]gen.Alarm{}
	}
	if w.Overviews == nil {
		w.Overviews = map[string]gen.Overview{}
	}
	if w.Coverage == nil {
		w.Coverage = map[string]gen.MacCoverage{}
	}
	if w.BlindSpots == nil {
		w.BlindSpots = map[string][]gen.AuthBlindSpot{}
	}
	if w.Capability == nil {
		w.Capability = map[string]gen.ControllerProbe{}
	}
	if w.NasProbes == nil {
		w.NasProbes = map[string]gen.NasProbe{}
	}
	if w.Discovery == nil {
		w.Discovery = map[string]gen.DiscoveryResult{}
	}
	if w.Credentials == nil {
		w.Credentials = map[string]string{}
	}
}

// World exposes the data for seeding and for assertions.
func (s *Store) World() *World { return s.world }

// SetClock makes time reproducible in a test.
func (s *Store) SetClock(f func() time.Time) { s.clock = f }

// Ports hands the whole store to `api.New`.
func (s *Store) Ports() api.Ports {
	return api.Ports{
		Sites:         s,
		Controllers:   s,
		AccessPoints:  s,
		Devices:       s,
		Vouchers:      s,
		Sessions:      s,
		Accounts:      s,
		Operators:     s,
		AdminSessions: s,
		Setup:         s,
		Audit:         s,
		AuthAttempts:  s,
		Portal:        s,
		Garden:        s,
		Dashboard:     s,
		Privacy:       s,
		Reports:       s,
		Guests:        s,
		PhoneSignIn:   s,
		GuestData:     s,
		TimeZones:     s,
		Maintenance:   s,
		GuestWifi:     s,
		DeviceKeys:    s,
	}
}

func (s *Store) id(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s%04d", prefix, s.nextID)
}

// ── paging ───────────────────────────────────────────────────────────────────

// slicePage is the cursor implementation, and the cursor is a decimal offset.
//
// Opaque to a client — the contract types it `string` and says nothing more —
// but an offset here rather than a keyset, because this world is a slice and a
// keyset cursor over one would be a fiction. `internal/store`'s adapter is free
// to encode something else: the format lives behind the port, which is why
// `api.Window` carries the cursor as a string rather than as an integer.
func slicePage[T any](items []T, w api.Window) api.Page[T] {
	start := 0
	if w.Cursor != "" {
		if n, err := strconv.Atoi(w.Cursor); err == nil && n > 0 {
			start = n
		}
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + w.Limit
	if end > len(items) {
		end = len(items)
	}
	page := api.Page[T]{Items: append([]T{}, items[start:end]...)}
	if end < len(items) {
		page.NextCursor = ptr(strconv.Itoa(end))
	}
	return page
}

func ptr[T any](v T) *T { return &v }

// matches is the free-text `q`: case-insensitive substring across the field list
// each operation names. There is no scope selector — the contract's parameter
// says "free-text match across every identifying field at once".
func matches(needle string, fields ...*string) bool {
	if needle == "" {
		return true
	}
	needle = strings.ToLower(needle)
	for _, f := range fields {
		if f == nil {
			continue
		}
		if strings.Contains(strings.ToLower(*f), needle) {
			return true
		}
	}
	return false
}
