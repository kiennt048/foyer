package nas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The access-point map: the Integration API's device list, read once per site
// and reused until it goes stale.
//
// ── why this file exists ─────────────────────────────────────────────────────
//
// `security.md` §4 requires four things to be true before a grant: the MAC is
// really associated, **on the claimed access point**, in the claimed site, and
// still unauthorized. Three of them fall out of one client lookup. The fourth
// does not, and it is tempting to call it unsatisfiable.
//
// It is not. **It was measured that the client object carries `uplinkDeviceId`**,
// and the site's device list resolves that id to a MAC. So the clause costs one
// extra call, and this driver pays it.
//
// The two identifiers do not match, and that is the whole reason this is not a
// one-line comparison: the redirect names the access point by **MAC**, the
// client object names it by **UUID**, and code that compares them directly finds
// nothing and reports every guest as unconfirmed. The device list is the only
// thing that joins them.
//
// ── what a controller answers ────────────────────────────────────────────────
//
//	GET /proxy/network/integration/v1/sites/{siteId}/devices
//	→ {"offset":0,"limit":25,"count":1,"totalCount":1,
//	   "data":[{"id":"00000000-0000-4000-8000-0000000000a1","macAddress":"00:00:5e:00:53:01",
//	            "name":"Lobby AP","features":["accessPoint"], …}]}
//
// and that id is exactly the `uplinkDeviceId` a client object carries for a
// device associated to it. The join is real, not inferred.
//
// Two properties of that answer are load-bearing and neither is obvious:
//
//   - **It is paginated, and `limit` is silently clamped.** `?limit=201` answers
//     200 with `"limit":200` in the envelope — no error, no warning. A caller
//     that asks for one big page and trusts what comes back reads a truncated
//     list as a complete one, and every device past the cut becomes unresolvable.
//     That is a false *unconfirmed* today and a false *refusal* under
//     `RequireAPMatch`. So this walks `offset` until `totalCount` is covered.
//   - **The list form and the detail form disagree about `features`.** The list
//     sends `["accessPoint"]`, the detail endpoint sends `{"accessPoint":{}}`.
//     Only the list is modelled here; a struct that assumed one shape would
//     decode the other as an error or as nothing.
//
// And one thing the Integration API does **not** carry, measured the same run:
// **no BSSID, anywhere.** The device detail endpoint's `interfaces.radios[]`
// carries `wlanStandard`, `frequencyGHz`, `channelWidthMHz` and `channel`, and
// no BSSID at all. The BSSID → access point mapping exists only on the legacy
// device list's `vap_table`. That is why a second credential is not required, and
// why an unresolvable claim is *unconfirmed* here rather than refused — see
// `resolveClaimedAccessPoint`.
const (
	// unifiDeviceMapTTL bounds how long a fetched map is reused.
	//
	// The number is chosen against what the map describes rather than against a
	// request rate: **a device-id → MAC map changes when somebody mounts an
	// access point on a wall.** Five minutes means a newly adopted AP is
	// confirmable within a coffee, and that a venue authorizing a guest every
	// few seconds still makes one device call per site per five minutes rather
	// than one per grant — which is the steady state confirming the access point
	// needs.
	//
	// It is a named constant because a bounded cache with the bound written into
	// the comparison is a magic number that nothing can read back; the TTL test
	// reads *this* rather than restating it.
	unifiDeviceMapTTL = 5 * time.Minute

	// unifiDevicePageSize is the largest page the Integration API serves.
	// MEASURED: asking for more is clamped without saying so.
	unifiDevicePageSize = 200

	// unifiDeviceMaxPages bounds the walk so a controller that answers a
	// nonsense `totalCount`, or one that never advances, cannot hold a guest's
	// grant open forever. 50 pages is 10 000 devices, which is an order of
	// magnitude past the largest single UniFi site anybody documents.
	unifiDeviceMaxPages = 50
)

// unifiDevice is the subset of the device list this driver reads.
//
// `features` is modelled because it is what says a device is an access point
// rather than a switch, and the probe needs to distinguish "this site has no
// access point to resolve against" from "this site has devices".
type unifiDevice struct {
	ID         string   `json:"id"`
	MACAddress string   `json:"macAddress"`
	Name       string   `json:"name"`
	IPAddress  string   `json:"ipAddress"`
	Model      string   `json:"model"`
	Features   []string `json:"features"`
}

// featureAccessPoint is the value the list form reports for a device that is an
// access point. MEASURED on 10.5.67.
const featureAccessPoint = "accessPoint"

func (d unifiDevice) isAccessPoint() bool {
	for _, f := range d.Features {
		if f == featureAccessPoint {
			return true
		}
	}
	return false
}

// unifiDeviceMap is one site's device list, resolved and stamped.
type unifiDeviceMap struct {
	// byID resolves a client's uplinkDeviceId to that device's MAC, normalised.
	byID map[string]string
	// macs holds every device MAC in the site, normalised. It answers a
	// different question from byID and the difference decides an outcome: "is
	// the MAC this redirect claims a device on this controller at all?"
	macs map[string]struct{}
	// accessPoints counts the devices that report themselves as access points.
	accessPoints int
	fetched      time.Time
}

// accessPointMap returns the site's device map, fetching it when there is none
// or the one in hand has aged past unifiDeviceMapTTL.
//
// The HTTP call happens outside the lock. Two grants arriving together may
// therefore both fetch, which costs one extra idempotent GET and is the right
// trade: holding a mutex across a controller round trip would make every
// concurrent guest wait on the slowest one.
func (u *UniFiDriver) accessPointMap(ctx context.Context, siteID string) (*unifiDeviceMap, error) {
	if m := u.cachedDeviceMap(siteID); m != nil {
		return m, nil
	}
	return u.refreshDeviceMap(ctx, siteID)
}

func (u *UniFiDriver) cachedDeviceMap(siteID string) *unifiDeviceMap {
	u.deviceMu.Lock()
	defer u.deviceMu.Unlock()
	m, ok := u.deviceMaps[siteID]
	if !ok {
		return nil
	}
	if u.clock().Sub(m.fetched) >= unifiDeviceMapTTL {
		return nil
	}
	return m
}

func (u *UniFiDriver) refreshDeviceMap(ctx context.Context, siteID string) (*unifiDeviceMap, error) {
	devices, err := u.listDevices(ctx, siteID)
	if err != nil {
		return nil, err
	}
	m := &unifiDeviceMap{
		byID:    make(map[string]string, len(devices)),
		macs:    make(map[string]struct{}, len(devices)),
		fetched: u.clock(),
	}
	for _, d := range devices {
		mac := normalizeMAC(d.MACAddress)
		if d.ID != "" && mac != "" {
			m.byID[d.ID] = mac
		}
		if mac != "" {
			m.macs[mac] = struct{}{}
		}
		if d.isAccessPoint() {
			m.accessPoints++
		}
	}

	u.deviceMu.Lock()
	if u.deviceMaps == nil {
		u.deviceMaps = make(map[string]*unifiDeviceMap, 1)
	}
	u.deviceMaps[siteID] = m
	u.deviceMu.Unlock()
	return m, nil
}

// listDevices walks every page of the Integration API device list.
//
// It stops on a page that returns nothing, so a controller whose `totalCount`
// disagrees with what it actually serves terminates rather than spinning.
func (u *UniFiDriver) listDevices(ctx context.Context, siteID string) ([]unifiDevice, error) {
	base := fmt.Sprintf("%s/sites/%s/devices", u.integrationBase(ctx), url.PathEscape(siteID))
	var out []unifiDevice

	for page, offset := 0, 0; page < unifiDeviceMaxPages; page++ {
		q := url.Values{}
		q.Set("offset", fmt.Sprint(offset))
		q.Set("limit", fmt.Sprint(unifiDevicePageSize))
		urlStr := base + "?" + q.Encode()

		req, err := u.newIntegrationRequest(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, err
		}
		resp, err := u.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("unifi: device list: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			body := bodyExcerpt(resp.Body)
			resp.Body.Close()
			return nil, &APIError{Method: http.MethodGet, URL: urlStr, Status: resp.StatusCode, Body: body}
		}
		var envelope struct {
			Count      int           `json:"count"`
			TotalCount int           `json:"totalCount"`
			Data       []unifiDevice `json:"data"`
		}
		err = json.NewDecoder(resp.Body).Decode(&envelope)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("unifi: decode device list: %w", err)
		}

		out = append(out, envelope.Data...)
		offset += len(envelope.Data)
		if len(envelope.Data) == 0 || offset >= envelope.TotalCount {
			return out, nil
		}
	}
	// Every page answered and the count never ran out. Returning what was read
	// would be a truncated map presented as a complete one, which is the exact
	// failure the pagination is here to prevent.
	return nil, fmt.Errorf("unifi: device list did not terminate within %d pages of %d",
		unifiDeviceMaxPages, unifiDevicePageSize)
}

// apResolution is what the driver was able to establish about the access point
// the redirect claimed.
type apResolution int

const (
	// apUnresolved — nothing was compared. The client carries no uplink, or the
	// site has no device answering to the one it carries. This is **not** a
	// mismatch: an access point adopted between the two calls, a wired client,
	// or a redirect naming a BSSID rather than a device MAC all land here, and
	// refusing them would refuse guests over a claim nobody checked.
	apUnresolved apResolution = iota
	// apMatched — the client's uplink device is the access point the redirect
	// named. §4's fourth clause, satisfied.
	apMatched
	// apContradicted — both sides resolved and they name different devices. The
	// redirect is wrong about where this guest is standing, which is the thing
	// an unsigned query parameter lets anybody assert.
	apContradicted
)

// resolveClaimedAccessPoint compares what the redirect claimed against what the
// controller says.
//
// The three-way answer is the point. A bool would have to fold "not checked"
// into one of the other two, and both folds are wrong: fold it into *matched*
// and `APConfirmed` becomes a constant again; fold it into *contradicted* and a
// venue whose controller does not place a client stops serving guests.
func (u *UniFiDriver) resolveClaimedAccessPoint(ctx context.Context, siteID, uplinkDeviceID, claimedAPMAC string) (apResolution, error) {
	claimed := normalizeMAC(claimedAPMAC)
	if claimed == "" {
		return apUnresolved, nil
	}

	m, err := u.accessPointMap(ctx, siteID)
	if err != nil {
		return apUnresolved, err
	}
	if uplinkDeviceID == "" {
		return apUnresolved, nil
	}

	actual, ok := m.byID[uplinkDeviceID]
	if !ok {
		// The map may simply be older than the access point. Re-read once — but
		// only if what we hold was not fetched by this very call, so a client
		// pointing at a device that genuinely does not exist costs one extra
		// call and not one per attempt.
		if u.clock().Sub(m.fetched) > 0 {
			if fresh, ferr := u.refreshDeviceMap(ctx, siteID); ferr == nil {
				m = fresh
				actual, ok = m.byID[uplinkDeviceID]
			}
		}
	}
	if !ok {
		return apUnresolved, nil
	}
	if actual == claimed {
		return apMatched, nil
	}
	return apContradicted, nil
}

// clock is the driver's time source. A field rather than time.Now so the TTL is
// testable as a bound rather than as a sleep.
func (u *UniFiDriver) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// accessPointWarnings is the security.md §4 access-point notice, emitted only
// when this controller actually cannot answer the question.
//
// Three conditions, and each one is a different sentence to an operator:
//
//   - **no API key.** Presence verification runs entirely on the Integration
//     API, so a driver holding only a local admin account confirms nothing —
//     the access point least of all.
//   - **the device list did not answer.** The route is absent on this
//     controller, or the credential is refused on it. Whatever the cause, the
//     id in `uplinkDeviceId` resolves to nothing.
//   - **the site holds no device at all.** The call succeeded and there is
//     nothing in it, so every uplink id is unresolvable for as long as that
//     stays true.
//
// A probe that fires none of these is a controller where §4's fourth clause is
// checked, which is what the extra call buys and what the operator should be able to see
// by the notice's absence.
func (u *UniFiDriver) accessPointWarnings(ctx context.Context, res *ProbeResult) []ProbeWarning {
	if u.apiKey == "" {
		return []ProbeWarning{AccessPointUnconfirmedWarning(
			"this controller is registered with no API key, so the Integration API client object — " +
				"the only thing that names the access point a client is under — cannot be read")}
	}

	site := probeSiteID(u.site, res.VendorSites)
	m, err := u.refreshDeviceMap(ctx, site)
	if err != nil {
		return []ProbeWarning{AccessPointUnconfirmedWarning(
			"the site's device list did not answer, so a client's uplinkDeviceId resolves to no " +
				"access point: " + err.Error())}
	}
	if len(m.byID) == 0 {
		return []ProbeWarning{AccessPointUnconfirmedWarning(
			"this site's device list is empty, so there is no access point for a client's " +
				"uplinkDeviceId to resolve to")}
	}
	return nil
}

// probeSiteID picks the site identifier the device list should be asked about.
//
// The Integration API and the legacy API use **different identifier spaces for
// one site**: the id is a UUID such as `00000000-0000-4000-8000-000000000001`,
// the legacy name is `default`, and `/v1/sites/default/devices` answers
// `400 'default' is not a valid 'siteId' value`. An access-point field scan run
// over that error body finds no `uplinkDeviceId` in it and prints a confident
// negative about the API's schema — the instrument wrong before the API is.
//
// A probe that asked about `u.site` verbatim would repeat it one layer down: it
// would warn that access points cannot be confirmed on a controller that
// confirms them perfectly well. So the configured value is resolved against the
// sites the probe has just listed, by id, then by name, and a controller with
// exactly one site answers for it.
func probeSiteID(configured string, sites []VendorSite) string {
	for _, s := range sites {
		if s.ID == configured {
			return s.ID
		}
	}
	for _, s := range sites {
		if strings.EqualFold(s.Name, configured) {
			return s.ID
		}
	}
	if len(sites) == 1 {
		return sites[0].ID
	}
	return configured
}
