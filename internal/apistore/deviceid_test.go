package apistore_test

import (
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The device id the console reads and the device id it sends back are one id.
//
// Asserted from the outside, over HTTP, because that is the only place the two
// halves meet: `listNas` and `listSessions` are answered by different repositories
// and each can be self-consistent on its own. The console reads an id off the device
// list and sends it back as `listSessions?nasId=`, and for every device that has an
// access point a mismatch matches nothing at all.
//
// The id is not re-derived in Go from `Called-Station-Id` here; the agreement is
// made in SQL, and the assertion stays because a repair in SQL can regress exactly
// the same way.
func TestADeviceIdFromTheListIsTheOneSessionsCarry(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	var devices struct {
		Items []gen.Nas `json:"items"`
	}
	w.getJSON("/nas?siteId="+w.siteA, &devices)
	if len(devices.Items) == 0 {
		t.Fatal("no devices in site A, so this compares nothing")
	}

	var sessions struct {
		Items []gen.Session `json:"items"`
	}
	w.getJSON("/sessions?siteId="+w.siteA, &sessions)
	if len(sessions.Items) == 0 {
		t.Fatal("no sessions in site A, so this compares nothing")
	}

	known := map[string]bool{}
	for _, d := range devices.Items {
		known[d.Id] = true
	}

	for _, s := range sessions.Items {
		if !known[s.NasId] {
			t.Errorf("session %s names device %q, which is in no row of listNas", s.Id, s.NasId)
		}
		// And it resolves as a device on its own, which is the lookup
		// `revokeVoucher` makes to name the device a failed session end failed on.
		w.do(http.MethodGet, "/nas/"+s.NasId, nil, http.StatusOK)
	}

	// The other direction, and the one that catches a fix that made the ids agree
	// by making every session name the same device: filtering by each device in
	// turn has to partition the site's sessions exactly.
	filtered := 0
	for _, d := range devices.Items {
		var page struct {
			Items []gen.Session `json:"items"`
		}
		w.getJSON("/sessions?siteId="+w.siteA+"&nasId="+d.Id, &page)
		for _, s := range page.Items {
			if s.NasId != d.Id {
				t.Errorf("a filter for device %q returned session %s on %q", d.Id, s.Id, s.NasId)
			}
		}
		filtered += len(page.Items)
	}
	if filtered != len(sessions.Items) {
		t.Errorf("filtering device by device reached %d of the site's %d sessions", filtered, len(sessions.Items))
	}
}
