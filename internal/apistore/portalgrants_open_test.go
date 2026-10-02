package apistore_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// On a UniFi SSID with no RADIUS accounting profile there is no `radacct` row, so
// a guest the portal let on has no session for the console to disconnect — the
// grant exists and the session does not. The grant is listed and ends on its
// controller, UniFi's and Omada's own practice; a grant whose device is not a
// controller's is refused before anything changes.
func TestAPortalGuestWithNoSessionIsDisconnectedOnItsController(t *testing.T) {
	controller := &portalDrivers{d: nas.NewMockDriver(nas.VendorUniFi)}
	w := newWorldWith(t, func(c *apistore.Config) { c.Drivers = controller })
	w.signIn()
	ctx := context.Background()

	lobbyAP := w.macFor(1) // under nasOK, whose RADIUS client has the world's controller
	viaController := &store.PortalGrant{
		ID: uniq("grant"), SiteID: w.siteA, ClientMAC: "02:de:ad:be:ef:01", APMAC: &lobbyAP,
		Method: "click-through", Plan: store.Plan{EnforcedBy: "nas"}, GrantedAt: time.Now(),
	}
	noDevice := &store.PortalGrant{
		ID: uniq("grant"), SiteID: w.siteA, ClientMAC: "02:de:ad:be:ef:02",
		Method: "click-through", Plan: store.Plan{EnforcedBy: "unenforced"}, GrantedAt: time.Now(),
	}
	for _, g := range []*store.PortalGrant{viaController, noDevice} {
		if err := w.st.Grants().CreateGrant(ctx, g); err != nil {
			t.Fatalf("seeding a grant: %v", err)
		}
	}

	list := func() map[string]gen.PortalAuthorization {
		t.Helper()
		var page struct {
			Items []gen.PortalAuthorization `json:"items"`
		}
		if err := json.Unmarshal(w.do(http.MethodGet, "/portal-grants?siteId="+w.siteA, nil, http.StatusOK), &page); err != nil {
			t.Fatal(err)
		}
		out := map[string]gen.PortalAuthorization{}
		for _, it := range page.Items {
			out[it.Id] = it
		}
		return out
	}
	open := list()
	if g, ok := open[viaController.ID]; !ok || !g.CanDisconnect {
		t.Fatalf("the controller-authorized guest is %+v (listed %v); want listed and disconnectable", g, ok)
	}
	if g, ok := open[noDevice.ID]; !ok || g.CanDisconnect {
		t.Fatalf("the guest with no known device is %+v (listed %v); want listed and not disconnectable", g, ok)
	}

	var res gen.SessionDisconnectResult
	if err := json.Unmarshal(w.do(http.MethodPost, "/portal-grants/"+viaController.ID+"/disconnect",
		map[string]any{}, http.StatusOK), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Disconnected || res.GrantsRevoked != 1 || res.Failure != nil {
		t.Fatalf("disconnect = %+v; want the controller's confirmation and the grant withdrawn", res)
	}
	if _, asked := controller.d.Deauthorized[viaController.ClientMAC]; !asked {
		t.Fatalf("the controller was never asked to unauthorize %s; asked for %v",
			viaController.ClientMAC, controller.d.Deauthorized)
	}
	if g, err := w.st.Grants().Grant(ctx, viaController.ID); err != nil || g.RevokedAt == nil {
		t.Fatalf("the grant is not revoked after its disconnect: %+v, %v", g, err)
	}
	if _, still := list()[viaController.ID]; still {
		t.Error("a disconnected guest is still listed as open")
	}

	// Already over: nothing to end.
	w.do(http.MethodPost, "/portal-grants/"+viaController.ID+"/disconnect", map[string]any{}, http.StatusConflict)

	// No controller: refused, and nothing changed.
	w.do(http.MethodPost, "/portal-grants/"+noDevice.ID+"/disconnect", map[string]any{}, http.StatusConflict)
	if g, err := w.st.Grants().Grant(ctx, noDevice.ID); err != nil || g.RevokedAt != nil {
		t.Fatalf("a refused disconnect revoked the grant anyway: %+v, %v", g, err)
	}

	// Another venue's list does not carry this venue's guests.
	var other struct {
		Items []gen.PortalAuthorization `json:"items"`
	}
	if err := json.Unmarshal(w.do(http.MethodGet, "/portal-grants?siteId="+w.siteB, nil, http.StatusOK), &other); err != nil {
		t.Fatal(err)
	}
	for _, it := range other.Items {
		if it.Id == noDevice.ID {
			t.Error("site B lists site A's guest")
		}
	}
}
