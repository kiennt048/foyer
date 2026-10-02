package apistore

import (
	"context"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// ControllerRegistry — the management planes an operator actually possesses.

// ControllersInSite returns the controllers an operator working in this site may see.
//
// A controller is bound through `vendorSites[].boundSiteId` rather than through a
// `siteId` of its own, because one controller manages many of the vendor's sites
// and an operator may adopt only some of them.
//
// The predicate is `controllerServesSite` and not an expression written here,
// because `dashboard.go` scopes the controller alarms and had a second copy of the
// same rule. An alarm the operator cannot find a row for is an alarm they cannot
// act on, and two copies is how the list and the dashboard come to disagree about
// one controller.
func (a *Adapter) ControllersInSite(ctx context.Context, siteID string) ([]gen.Controller, error) {
	rows, err := a.st.Controllers().List(ctx)
	if err != nil {
		return nil, out(err)
	}
	list := make([]gen.Controller, 0, len(rows))
	for _, c := range rows {
		if controllerServesSite(c, siteID) {
			list = append(list, controllerOut(c))
		}
	}
	return list, nil
}

// controllerServesSite says whether a controller belongs on the screen of an
// operator working in siteID.
//
// Two ways in, and they are different facts about the same object: it is **bound to
// this site**, or it is **bound to nothing at all** — registered, possibly probed,
// and not yet attached to a venue. The second is a state with a next step in it,
// not an absence, and hiding it removes the only screen that carries the step.
//
// **This does not test the probe.** `len(c.VendorSites) == 0` reads like *"a
// controller with no binding at all"*, and the two propositions are the same
// sentence only for as long as nothing records a controller's own sites.
// `UniFiDriver.Probe` reports the sites, so that predicate would make probing a
// controller remove it from every site view — including the one carrying
// `discoverFromController`, the call that would bind it. A registered controller
// could be probed once and then never adopted, and nothing in the product would
// say why.
//
// **Bound to nothing means bound to nothing, and not "has a vendor site nobody has
// adopted".** Read the wider way, per vendor site, a controller adopted into the
// café whose second vendor site is merely unadopted would be listed on the hotel's
// controllers screen, beside Delete and Discover. The price of the narrow reading is that a second vendor site
// is adoptable only from the Foyer site that already holds the controller — which
// costs nothing while `createSite` refuses the second venue (one venue per install)
// and is the safer half of the trade on the day it does not.
func controllerServesSite(c store.Controller, siteID string) bool {
	return siteID == "" || boundTo(c, siteID) || !boundAnywhere(c)
}

func boundTo(c store.Controller, siteID string) bool {
	for _, vs := range c.VendorSites {
		if vs.BoundSiteID != nil && *vs.BoundSiteID == siteID {
			return true
		}
	}
	return false
}

// boundAnywhere is the proposition `len(c.VendorSites) == 0` seems to say and does not.
func boundAnywhere(c store.Controller) bool {
	for _, vs := range c.VendorSites {
		if vs.BoundSiteID != nil {
			return true
		}
	}
	return false
}

func (a *Adapter) Controller(ctx context.Context, id string) (gen.Controller, error) {
	c, err := a.st.Controllers().Get(ctx, id)
	if err != nil {
		return gen.Controller{}, out(err)
	}
	return controllerOut(*c), nil
}

// RegisterController stores the controller and seals its credential.
//
// The credential is handed to the repository as bytes and sealed there rather than
// here, so that no code path in this package can store one unsealed. Which of the
// two fields carries it is decided by `authMode`, and the API has already refused
// the combination that has neither.
func (a *Adapter) RegisterController(ctx context.Context, in gen.ControllerCreate, _ string) (gen.Controller, error) {
	c := &store.Controller{
		ID:                 a.newID("ctl-"),
		Name:               in.Name,
		Vendor:             string(in.Vendor),
		BaseURL:            in.BaseUrl,
		AuthMode:           string(in.AuthMode),
		TLSMode:            string(in.TlsMode),
		VendorControllerID: in.ControllerId,
		Username:           in.Username,
	}
	var secret []byte
	switch {
	case in.ApiKey != nil && *in.ApiKey != "":
		secret = []byte(*in.ApiKey)
	case in.Password != nil && *in.Password != "":
		secret = []byte(*in.Password)
	}
	if err := a.st.Controllers().Create(ctx, c, secret); err != nil {
		return gen.Controller{}, out(err)
	}
	// Registered and never contacted: `unknown`, not `ok`. A registration form
	// cannot know whether the address resolves, the credential works, or the
	// software is new enough — that is what `probeController` is for, and claiming
	// `ok` here would be the console asserting three things nobody has checked.
	return controllerOut(*c), nil
}

// AmendController applies the console's patch.
//
// **The second write is not redundant.** A replaced credential invalidates what the
// last probe learned: leaving `reachability: ok` would report a controller as
// working on the strength of a credential that is no longer the one stored, and
// `discoverFromController` gates on exactly that field. `ControllerPatch` has no
// way to say it, so the reset goes through `RecordProbe` with every other observed
// value passed back unchanged — including `lastContactAt`, so that "last contact"
// does not become a timestamp for a contact that never happened.
//
// **Reported**: `store.ControllerPatch` wants a `Reachability` member, or `Update`
// should perform this reset itself. Doing it from this side is correct but is a
// second round trip and a second place to forget.
func (a *Adapter) AmendController(ctx context.Context, id string, in gen.ControllerUpdate) (gen.Controller, error) {
	before, err := a.st.Controllers().Get(ctx, id)
	if err != nil {
		return gen.Controller{}, out(err)
	}

	patch := store.ControllerPatch{
		Name:    in.Name,
		BaseURL: in.BaseUrl,
		TLSMode: (*string)(in.TlsMode),
	}
	if in.ControllerId != nil {
		patch.VendorControllerID = &in.ControllerId
	}
	if in.Username != nil {
		patch.Username = in.Username
	}
	replaced := false
	switch {
	case in.ApiKey != nil && *in.ApiKey != "":
		patch.Secret = ptr([]byte(*in.ApiKey))
		replaced = true
	case in.Password != nil && *in.Password != "":
		patch.Secret = ptr([]byte(*in.Password))
		replaced = true
	}

	c, err := a.st.Controllers().Update(ctx, id, patch)
	if err != nil {
		return gen.Controller{}, out(err)
	}
	// Guarded on `lastContactAt` as well as on reachability, because the
	// repository writes that column unconditionally and there is no value to pass
	// back when nothing has ever contacted this controller. In practice the two
	// travel together — a reachability other than `unknown` is a probe that ran —
	// so the guard costs nothing and refuses to invent a contact that never
	// happened.
	if replaced && before.Reachability != string(gen.NasReachabilityUnknown) && before.LastContactAt != nil {
		if err := a.st.Controllers().RecordProbe(ctx, id, store.ControllerProbe{
			Reachability:     string(gen.NasReachabilityUnknown),
			Version:          before.Version,
			VersionSupported: before.VersionSupported,
			Flavour:          before.Flavour,
			// Nil leaves the pinned fingerprint alone: the certificate did not
			// change because the password did.
			CertificateFingerprint: nil,
			// Nil leaves the vendor site list alone, bindings included.
			VendorSites: nil,
		}, *before.LastContactAt); err != nil {
			return gen.Controller{}, out(err)
		}
		c, err = a.st.Controllers().Get(ctx, id)
		if err != nil {
			return gen.Controller{}, out(err)
		}
	}
	return controllerOut(*c), nil
}

func (a *Adapter) ForgetController(ctx context.Context, id string) error {
	return out(a.st.Controllers().Delete(ctx, id))
}

// AccessPointsFrom counts what deleting this controller would strand.
//
// Two populations, and counting only one of them under-reports. Access points
// discovered from a controller are **cascade-deleted** with it; RADIUS clients that
// name it have their `controller_id` set to NULL and survive as `clients.conf` rows
// with no origin. Both are things an operator has to be warned about, and which one
// applies depends on the deployment: a UniFi site where every access point speaks
// RADIUS from its own address has one client row per device, and a controller
// proxying RADIUS for a fleet has access points and no client rows at all.
//
// So: every client naming the controller, plus every access point from it that has
// no client row of its own, which is exactly the set the first count misses.
func (a *Adapter) AccessPointsFrom(ctx context.Context, controllerID string) (int, error) {
	clients, err := a.st.Controllers().InUse(ctx, controllerID)
	if err != nil {
		return 0, out(err)
	}
	aps, err := a.st.AccessPoints().ListForSite(ctx, "")
	if err != nil {
		return 0, out(err)
	}
	n := clients
	for _, ap := range aps {
		if ap.ControllerID != nil && *ap.ControllerID == controllerID && ap.RadiusClientID == nil {
			n++
		}
	}
	return n, nil
}

// vendorSiteFor resolves the controller site bound to a Foyer site.
//
// Empty when nothing is bound, and the caller must treat that as "the operator has
// not chosen yet" rather than substituting a name. A hardcoded `default` site is a
// filed bug in a shipped UniFi integration, which is the reason the bindings are
// rows in the first place.
func vendorSiteFor(c store.Controller, siteID string) string {
	for _, vs := range c.VendorSites {
		if vs.BoundSiteID != nil && *vs.BoundSiteID == siteID {
			return vs.ID
		}
	}
	return ""
}

var _ api.ControllerRegistry = (*Adapter)(nil)
