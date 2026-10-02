package api

import (
	"context"
	"errors"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Every operator setting is set in the console, stored in the database and in
// force without a restart. These are the ports for two of them: the maintenance
// schedule and the guest network's Wi-Fi password.

// MaintenanceStore is the control plane's own maintenance configuration
// (`getMaintenanceSettings`, `updateMaintenanceSettings`).
//
// The limits travel on the read, as a retention category's do, so the handler
// refuses on the figures the store enforces rather than on a copy of them.
type MaintenanceStore interface {
	MaintenanceSettings(ctx context.Context) (gen.MaintenanceSettings, error)
	// SetMaintenanceSettings replaces the values and puts them in force on the
	// running scheduler. `actor` is denormalised onto the row.
	SetMaintenanceSettings(ctx context.Context, v gen.MaintenanceSettingsValues, actor string) (gen.MaintenanceSettings, error)
}

// GuestWifiKeyStore is the WPA passphrase of a site's guest network.
//
// **Write-only everywhere but one method.** `GuestWifiKey` and
// `SetGuestWifiKey` answer a status and never the value; `VoucherSlipWifi` is the
// voucher slip's read, the one place it is disclosed, because a slip exists to
// print it. Every method answers ErrNotFound for a site that does not exist.
type GuestWifiKeyStore interface {
	GuestWifiKey(ctx context.Context, siteID string) (gen.GuestWifiKeyStatus, error)
	// SetGuestWifiKey stores the passphrase, or clears it for nil. The handler has
	// already refused anything that is not a WPA passphrase.
	SetGuestWifiKey(ctx context.Context, siteID string, passphrase *string, actor string) (gen.GuestWifiKeyStatus, error)
	VoucherSlipWifi(ctx context.Context, siteID string) (gen.VoucherSlipWifi, error)
}

// The refusals an account's credential change can make, distinct from a failure.
var (
	// ErrPasswordRequiredFor8021X: an opt-in to 802.1X with no password to make
	// the bcrypt verifier from, on an account that is not already opted in.
	ErrPasswordRequiredFor8021X = errors.New("api: an 802.1X opt-in needs the password in the same request")
	// ErrPasswordTooLongFor8021X: more than bcrypt's 72 input bytes, with 802.1X
	// in force.
	ErrPasswordTooLongFor8021X = errors.New("api: the password is longer than 72 bytes, which 802.1X cannot verify")
	// ErrPAPUsernameTaken: another venue's opted-in identity holds this username,
	// and an 802.1X supplicant sends no site.
	ErrPAPUsernameTaken = errors.New("api: another venue's 802.1X identity already uses this username")
)

// OperatorUsername is the signed-in operator's username, or "" outside a
// console request. For a port that records who made a decision in its own row —
// `account_pap_verifiers.opted_in_by` — without the handler threading an actor
// through a signature the contract's body defines.
func OperatorUsername(ctx context.Context) string {
	if c := callerFrom(ctx); c != nil {
		return c.Operator.Username
	}
	return ""
}

// papPasswordMaxBytes is bcrypt's input limit, in bytes, stated for the refusal's
// figure. `identity.HashPAPVerifier` is what enforces it.
const papPasswordMaxBytes = 72

// ValidWifiPassphrase is IEEE 802.11-2020 Annex J.4.1's passphrase: 8 to 63
// printable ASCII characters. The store refuses the same (`store.ValidWifiPassphrase`)
// as a backstop; this is where the API's refusal comes from, so both servers the
// console can be pointed at refuse identically.
func ValidWifiPassphrase(p string) bool {
	if len(p) < 8 || len(p) > 63 {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 32 || p[i] > 126 {
			return false
		}
	}
	return true
}
