package apistore_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// A control the console shows must not answer 500.
//
// `PushSecret`'s premise is that **every driver in `internal/nas` reports
// `RotateSecret: false`**, so `rotateNasSecret` asks `SecretRotation` first and
// never reaches it. That premise holds for a **driver-computed** capability. It
// does not hold for `radius_clients.capabilities`, which is a **written column** —
// an operator's registration, a discovery run, or a fixture can put `true` in it,
// and the console reads that column to decide whether the control exists at all.
//
// So a device whose row claims it can push could produce this: the control is
// shown, the operator presses it, the secret is generated and stored, and the
// response is `/problems/internal`. The operator has no idea whether the device now has a
// secret nobody can read.
//
// `docs/rules/nas-drivers.md` says never to show a field that silently does
// nothing. This is its inverse and it is worse: a field that loudly does nothing,
// after a write.
func TestRotationSurvivesAStoredCapabilityNoDriverBacks(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	// A device whose stored capabilities claim the driver can push a secret.
	// Nothing in `internal/nas` can, and the column does not know that.
	id := w.seedNas(w.siteA, "Claims it can push", w.nextAddr(), store.Capabilities{
		WalledGarden: "fqdn-sni", RotateSecret: true,
	}, "ok", "enforced", ptr(true))

	raw := w.do(http.MethodPost, "/nas/"+id+"/secret", nil, http.StatusOK)
	var out gen.RotateNasSecret200JSONResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("reading the rotation: %v: %s", err, truncate(raw))
	}
	if len(out.SharedSecret) < 20 {
		t.Errorf("the rotated secret is %d characters — there is no default and it is crypto/rand",
			len(out.SharedSecret))
	}
	// `restart` is the conservative classification and is what the operator is
	// warned about before they confirm: FreeRADIUS 3.2.x reads SQL clients at
	// startup and SIGHUP does not reliably reload them.
	if out.ChangeClass != gen.ChangeClassRestart {
		t.Errorf("changeClass is %q, want restart", out.ChangeClass)
	}

	// And the stored secret really changed, so the operator has something to type
	// into the device. A rotation that answered 200 and stored nothing would be
	// the same defect wearing a status code.
	var secretSet bool
	if err := w.owner.QueryRow(w.t.Context(),
		`SELECT ss.secret IS NOT NULL
		 FROM radius_clients rc JOIN shared_secrets ss ON ss.id = rc.shared_secret_id
		 WHERE rc.id = $1`, id).Scan(&secretSet); err != nil {
		t.Fatalf("reading the stored secret: %v", err)
	}
	if !secretSet {
		t.Error("the rotation answered 200 and stored no secret")
	}
}
