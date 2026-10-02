package nas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The measurement instrument for a real UniFi controller: which Integration API
// answers, whether a second authorize changes a live session, and what the client
// object carries.
//
// It exists because those questions are questions about what a controller
// *actually sends*, and a summary answers only the questions
// somebody thought to ask. Every step here records the raw body, so a field
// nobody predicted is still in the output.

// ProbeStep is one request the probe made, recorded verbatim.
type ProbeStep struct {
	// Name is the measurement this step belongs to: "integration", "ap-identity",
	// "client-detail", "reauthorize" or "legacy-devices".
	Name        string        `json:"name"`
	What        string        `json:"what"`
	Method      string        `json:"method"`
	URL         string        `json:"url"`
	RequestBody string        `json:"requestBody,omitempty"`
	Status      int           `json:"status,omitempty"`
	Body        string        `json:"body,omitempty"`
	Err         string        `json:"error,omitempty"`
	Duration    time.Duration `json:"duration"`
	// Note is what this step means — why a 401 here is a positive result, what
	// a missing field would imply. Written next to the evidence, not in a doc.
	Note string `json:"note,omitempty"`
	// Findings are what the tool could read out of the raw body without
	// interpreting it: which keys are present, how many entries a table has.
	// They never replace Body — they point at the part of it worth reading.
	Findings []string `json:"findings,omitempty"`
}

// UniFiProbePlan is what the probe should attempt.
//
// Authorize and RunReauthorize default to false because both change the state of
// a live network. A measurement that surprises the operator is one nobody runs twice.
type UniFiProbePlan struct {
	SiteID    string
	ClientMAC string

	// Authorize performs AUTHORIZE_GUEST_ACCESS with First. Off by default.
	Authorize bool
	// RunReauthorize issues a second AUTHORIZE_GUEST_ACCESS with Second on the
	// already-authorized client, which is the whole of the live re-authorization
	// measurement.
	RunReauthorize bool

	First  AuthorizeRequest
	Second AuthorizeRequest
}

const probeBodyLimit = 1 << 20 // 1 MiB — a client list, not a firmware image.

// RunProbe executes the measurement sequence and returns every request it made.
//
// Two phases, and the second runs whatever the first did. The Integration API
// and the legacy private API need different credentials and answer different
// questions, and a controller with no Integrations tab makes the legacy phase
// the primary measurement rather than the supplement — so a failure in one must
// not cost the run the other.
func (u *UniFiDriver) RunProbe(ctx context.Context, plan UniFiProbePlan) []ProbeStep {
	steps := u.runIntegrationProbe(ctx, plan)
	return append(steps, u.runLegacyProbe(ctx, plan)...)
}

// runIntegrationProbe is the API-key half.
//
// It never stops at the first failure: a 401 on one prefix and a 200 on another
// is the finding, and a probe that aborts on the 401 never reports it.
func (u *UniFiDriver) runIntegrationProbe(ctx context.Context, plan UniFiProbePlan) []ProbeStep {
	var steps []ProbeStep
	record := func(s ProbeStep) { steps = append(steps, s) }

	site := u.siteFor(plan.SiteID)

	// --- flavour -----------------------------------------------------------
	record(u.probeRequest(ctx, "integration", "controller identity (unauthenticated)",
		http.MethodGet, u.baseURL+"/api/system", nil,
		"UniFi OS answers here with its own product and version. A standalone Network Application does not."))

	flavourStep := u.probeRequest(ctx, "integration", "flavour probe",
		http.MethodGet, u.baseURL+unifiOSStatusPath, nil,
		"200 or 401 means UniFi OS: login /api/auth/login, prefix /proxy/network. Anything else means a standalone controller on 8443. A 401 is a POSITIVE result here.")
	flavour := FlavourStandalone
	if flavourStep.Status == http.StatusOK || flavourStep.Status == http.StatusUnauthorized {
		flavour = FlavourUniFiOS
	}
	flavourStep.Note += fmt.Sprintf(" → flavour: %s", flavour)
	record(flavourStep)
	u.setFlavour(flavour)

	if u.apiKey == "" {
		record(ProbeStep{
			Name: "integration", What: "conclusion",
			Note: "no API key, so the Integration API steps were skipped. Set FOYER_UNIFI_API_KEY " +
				"or -key-file to run them. The legacy phase below needs a local admin username and " +
				"password instead, and is the primary measurement on a controller with no Integrations tab.",
		})
		return steps
	}

	// --- which Integration API prefix answers -------------------------------
	chosen := ""
	for _, prefix := range integrationCandidates(flavour) {
		s := u.probeRequest(ctx, "integration", "list sites at prefix "+prefix,
			http.MethodGet, u.baseURL+prefix+"/v1/sites", nil,
			"404 means this prefix is not the one. 401 means the route exists and the key was rejected. 200 is the answer.")
		record(s)
		if s.Status != 0 && s.Status != http.StatusNotFound && chosen == "" {
			chosen = prefix
		}
	}
	if chosen == "" {
		record(ProbeStep{
			Name: "integration", What: "conclusion",
			Note: "no Integration API prefix answered. Either the controller is below Network Application " +
				UniFiIntegrationAPIMinVersion + ", or it is unreachable. The legacy phase below is then the " +
				"only path, and it needs a local admin username and password.",
		})
		return steps
	}
	u.setIntegrationPrefix(chosen)
	record(ProbeStep{
		Name: "integration", What: "conclusion",
		Note: "Integration API base: " + u.baseURL + chosen + "/v1",
	})

	// --- version, for the >= 9.1.105 floor ----------------------------------
	record(u.probeRequest(ctx, "integration", "controller version",
		http.MethodGet, u.integrationBase(ctx)+"/info", nil,
		"The Integration API needs Network Application >= "+UniFiIntegrationAPIMinVersion+
			". Record the exact string and which key carries it — the field name is unmeasured."))

	// --- the device list a client's uplinkDeviceId resolves against ----------
	//
	// It runs before the client steps and whether or not a MAC was given,
	// because the half that matters most is the *shape* of this answer: whether
	// it carries `id` alongside `macAddress` at all, whether it is paginated,
	// and whether any BSSID appears anywhere in it. Those are facts about the
	// controller and need no guest standing in the lobby.
	devices := u.probeRequest(ctx, "ap-identity", "site device list (uplinkDeviceId resolves here)",
		http.MethodGet, u.deviceListURL(ctx, site, 0, unifiDevicePageSize), nil,
		"security.md §4 asks for the access point, the redirect names it by MAC, and the client object "+
			"names it by UUID. This is the only thing that joins them. Record `totalCount` against "+
			"`count`: the list is paginated and `limit` is clamped silently, so a truncated read looks "+
			"exactly like a complete one.")
	devices.Findings = deviceListFindings(devices.Status, devices.Body)
	record(devices)

	if plan.ClientMAC == "" {
		record(ProbeStep{
			Name: "integration", What: "conclusion",
			Note: "no client MAC given, so the lookup, authorize and detail steps were skipped. Pass -mac to run them.",
		})
		return steps
	}

	// --- the lookup that is also VerifyPresence -----------------------------
	mac := normalizeMAC(plan.ClientMAC)
	lookup := u.probeRequest(ctx, "integration", "client lookup (this call is VerifyPresence)",
		http.MethodGet, u.clientsURL(ctx, site, mac), nil,
		"access.authorized answers 'still unauthorized'; the row existing at all answers 'really associated'.")
	lookup.Findings = apIdentityFindings(lookup.Status, lookup.Body)
	lookup.Findings = append(lookup.Findings, uplinkResolutionFindings(lookup.Status, lookup.Body, devices.Status, devices.Body)...)
	record(lookup)

	clientID := firstClientID(lookup.Body, mac)
	if clientID == "" {
		record(ProbeStep{
			Name: "integration", What: "conclusion",
			Note: "no client id could be read from the lookup answer for " + mac +
				". Join the network with that device and run again; the action calls need the controller's client id, not the MAC.",
		})
		return steps
	}
	record(ProbeStep{Name: "integration", What: "conclusion", Note: "client id: " + clientID})

	// --- the whole client object --------------------------------------------
	record(u.clientDetailStep(ctx, site, clientID, "full client object (before any action)",
		"Does this path carry BYTE COUNTERS at all, or only the authorization window "+
			"(access.authorization.{authorizedAt,expiresAt,dataUsageLimitMBytes})? "+
			"And does ANY field identify an access point or an uplink? If one does, "+
			"security.md §4's access-point clause is satisfiable on the Integration API alone. "+
			"Every field matters — do not summarise."))

	if !plan.Authorize {
		record(ProbeStep{
			Name: "integration", What: "conclusion",
			Note: "read-only run: no authorization was issued. Pass -authorize to complete the integration run, and -reauthorize for the live re-authorization check. Both change a live network.",
		})
		return steps
	}

	// --- the authorize action -----------------------------------------------
	first := plan.First
	first.ClientMAC = mac
	record(u.probeJSON(ctx, "integration", "AUTHORIZE_GUEST_ACCESS (first limits)",
		http.MethodPost, u.clientActionURL(ctx, site, clientID),
		authorizePayload(actionAuthorize, first),
		"Does a real controller accept this with an API key, and what does it answer?"))

	record(u.clientDetailStep(ctx, site, clientID, "full client object (after first authorize)",
		"Compare against the pre-authorize object: which fields moved, and whether the limits appear anywhere."))

	if !plan.RunReauthorize {
		record(ProbeStep{
			Name: "reauthorize", What: "conclusion",
			Note: "the live re-authorization check was not run. Pass -reauthorize to issue a second authorize with different limits on the now-authorized client.",
		})
		return steps
	}

	// --- does a second authorize change a live session? ---------------------
	second := plan.Second
	second.ClientMAC = mac
	record(u.probeJSON(ctx, "reauthorize", "AUTHORIZE_GUEST_ACCESS again, different limits",
		http.MethodPost, u.clientActionURL(ctx, site, clientID),
		authorizePayload(actionAuthorize, second),
		"This is the whole of the live re-authorization check and it decides Capabilities.LiveReauthorize. "+
			"An accepted call is NOT the answer: the answer is whether the LIVE session's limits changed, "+
			"which needs the device still associated and the next step read carefully."))

	record(u.clientDetailStep(ctx, site, clientID, "full client object (after second authorize)",
		"If the authorization window and the limits reflect the SECOND call while the device stayed associated, "+
			"LiveReauthorize is true. If the device was disconnected and had to rejoin, it is false. Record which happened."))

	return steps
}

// runLegacyProbe is the local-admin half, and it answers whether the legacy
// device list maps a BSSID to its access point.
//
// `populateAccessPointMACIP()` in PacketFence walks this list and caches mac → ip
// for every AP *and every BSSID in its vap_table*, because the AP MAC in a UniFi
// redirect is otherwise unresolvable. That is DOCUMENTED against a nine-version-old
// article, so what this phase measures is whether the mapping is still there on
// the controller in front of you.
func (u *UniFiDriver) runLegacyProbe(ctx context.Context, plan UniFiProbePlan) []ProbeStep {
	var steps []ProbeStep
	record := func(s ProbeStep) { steps = append(steps, s) }

	if !u.hasLegacyCredentials() {
		record(ProbeStep{
			Name: "legacy-devices", What: "legacy device list — skipped",
			Note: "this step needs a LOCAL ADMIN username and password, not an API key. " +
				"Pass -username and set FOYER_UNIFI_PASSWORD (or -password-file). " +
				"NOT REQUIRED: the legacy list is not the sole route to the access point a redirect " +
				"names, because the Integration API client object carries uplinkDeviceId. The shipped " +
				"driver confirms the access point with the API key alone. What this step still answers is the vap_table BSSID mapping, which nothing " +
				"needs unless a redirect is one day seen carrying a BSSID.",
		})
		return steps
	}

	// --- login, recorded without its body -----------------------------------
	loginStep := ProbeStep{
		Name: "legacy-devices", What: "legacy login", Method: http.MethodPost, URL: u.loginPath(ctx),
		Note: "the request body carries a local admin password and is deliberately NOT recorded. " +
			"On UniFi OS the response's X-CSRF-Token must be echoed on every write, or every POST answers 403.",
	}
	start := time.Now()
	status, gotCSRF, err := u.loginRecorded(ctx)
	loginStep.Duration = time.Since(start)
	loginStep.Status = status
	if err != nil {
		loginStep.Err = err.Error()
	}
	loginStep.Findings = []string{
		fmt.Sprintf("X-CSRF-Token in the login response: %s", yesNo(gotCSRF)),
	}
	record(loginStep)
	if err != nil {
		record(ProbeStep{
			Name: "legacy-devices", What: "conclusion",
			Note: "the legacy login failed, so the device list could not be read. Check the username and " +
				"password, and note that a UniFi OS console wants a LOCAL account rather than a Ubiquiti SSO one.",
		})
		return steps
	}

	// --- the device list -----------------------------------------------------
	site := u.siteFor(plan.SiteID)
	base := fmt.Sprintf("%s/api/s/%s", u.networkBase(ctx), url.PathEscape(site))
	answered := false
	for _, suffix := range legacyDeviceCandidates() {
		s := u.probeRequest(ctx, "legacy-devices", "legacy device list at "+suffix,
			http.MethodGet, base+suffix, nil,
			"PacketFence's populateAccessPointMACIP() reads this. What matters is each device's "+
				"vap_table: it maps every BSSID to the AP that broadcasts it, and the redirect's `ap` "+
				"parameter is a BSSID as often as it is a device MAC.")
		if s.Status == http.StatusOK {
			s.Findings = legacyDeviceFindings(s.Body)
			answered = true
		}
		record(s)
		if answered {
			break
		}
	}
	if !answered {
		record(ProbeStep{
			Name: "legacy-devices", What: "conclusion",
			Note: "no legacy device path answered 200. If this controller also has no Integration API, " +
				"there is no known way to resolve the access point a redirect names, and the access-point " +
				"check is a product decision rather than an implementation gap.",
		})
	}
	return steps
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (u *UniFiDriver) clientDetailStep(ctx context.Context, site, clientID, what, note string) ProbeStep {
	s := u.probeRequest(ctx, "client-detail", what, http.MethodGet, u.clientDetailURL(ctx, site, clientID), nil, note)
	s.Findings = apIdentityFindings(s.Status, s.Body)
	return s
}

func (u *UniFiDriver) clientDetailURL(ctx context.Context, siteID, clientID string) string {
	return fmt.Sprintf("%s/sites/%s/clients/%s",
		u.integrationBase(ctx), url.PathEscape(siteID), url.PathEscape(clientID))
}

func (u *UniFiDriver) clientActionURL(ctx context.Context, siteID, clientID string) string {
	return u.clientDetailURL(ctx, siteID, clientID) + "/actions"
}

// deviceListURL is one page of the site device list, offset and limit written
// out rather than defaulted — the default page is 25 and output that does
// not say which page it read cannot be read back as evidence about a site with
// more devices than that.
func (u *UniFiDriver) deviceListURL(ctx context.Context, siteID string, offset, limit int) string {
	q := url.Values{}
	q.Set("offset", strconv.Itoa(offset))
	q.Set("limit", strconv.Itoa(limit))
	return fmt.Sprintf("%s/sites/%s/devices?%s",
		u.integrationBase(ctx), url.PathEscape(siteID), q.Encode())
}

// ---------------------------------------------------------------------------
// Reading the raw bodies — pointing, never interpreting
// ---------------------------------------------------------------------------

// apIdentityKeywords are the key-name fragments that could carry an access
// point's identity. Deliberately generous: a false positive costs one line of
// output, and a false negative is the whole question answered wrong.
var apIdentityKeywords = []string{
	"uplink", "apmac", "ap_mac", "accesspoint", "access_point",
	"bssid", "essid", "ssid", "radio", "wlan",
	"devicemac", "device_mac", "deviceid", "device_id", "gateway",
}

// apIdentityFindings answers the access-point question mechanically: does anything in this body name
// an access point or an uplink?
//
// It reports what is present and says plainly when nothing is, because "no field
// identifies the AP" is a finding that has to survive a reader skimming 40 lines
// of JSON — which is exactly how it would otherwise be missed.
//
// **It refuses to conclude anything from a body that did not answer the question.**
// Given the legacy site name `default`, the Integration API — which wants the
// UUID — answers `400 'default' is not a valid 'siteId' value`. A scan over that
// ERROR body finds no uplink field in it — correctly, there is none — and would
// print "NO field in this answer identifies an access point", which is the
// opposite of the truth: `uplinkDeviceId` is on the client all along.
//
// A negative finding drawn from a non-2xx body is not a measurement, it is the
// absence of one, and the two must not print the same sentence.
func apIdentityFindings(status int, body string) []string {
	if status < 200 || status > 299 {
		return []string{
			fmt.Sprintf("ap-identity: NOT ANSWERED — this request returned HTTP %d, so its body "+
				"describes an error rather than a client.", status),
			"→ absence of a field here is not evidence of absence in the API. Fix the request " +
				"and re-run. If the status is 400 on a site id, note that the Integration API " +
				"wants the site UUID from /v1/sites, not the legacy name `default`.",
		}
	}
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return nil
	}
	hits := map[string]string{}
	collectAPIdentity("", doc, hits)

	if len(hits) == 0 {
		return []string{
			"ap-identity: NO field in this answer identifies an access point or an uplink.",
			"→ security.md §4's AP clause cannot be satisfied from the Integration API alone; " +
				"the legacy device list is the only known source.",
		}
	}
	paths := make([]string, 0, len(hits))
	for p := range hits {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := []string{fmt.Sprintf("ap-identity: %d field(s) here could identify an access point or uplink:", len(paths))}
	for _, p := range paths {
		out = append(out, "    "+p+" = "+hits[p])
	}
	out = append(out, "→ if one of these resolves to the `ap` parameter a UniFi redirect carries, "+
		"§4's AP clause IS satisfiable on the Integration API alone. "+
		"Check what it actually holds — a device id is not a MAC.")
	return out
}

// deviceListFindings reads the device list for the three things that decide
// whether the access-point check works on this controller: whether it joins an
// id to a MAC, whether this page is all of it, and whether a BSSID appears
// anywhere.
//
// The BSSID line is the one worth having. A BSSID was measured to differ from its
// access point's MAC in octets an arithmetic rule cannot predict — so if a
// redirect ever names one, only a table resolves it, and only the *legacy* device
// list has that table. A controller whose Integration API grew BSSIDs would change
// whether a second credential is ever needed, and this line is what would notice.
func deviceListFindings(status int, body string) []string {
	if status < 200 || status > 299 {
		return []string{
			fmt.Sprintf("ap-identity: NOT ANSWERED — the device list returned HTTP %d, so nothing here "+
				"says what a client's uplinkDeviceId resolves to.", status),
			"→ a 400 on the site id means this ran against the legacy site name; the Integration " +
				"API wants the UUID from /v1/sites.",
		}
	}
	var page struct {
		Count      int              `json:"count"`
		TotalCount int              `json:"totalCount"`
		Limit      int              `json:"limit"`
		Data       []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		return []string{"ap-identity: the device list did not decode as the documented envelope: " + err.Error()}
	}

	joined := 0
	for _, d := range page.Data {
		if scalarString(d["id"]) != "" && scalarString(d["macAddress"]) != "" {
			joined++
		}
	}
	out := []string{fmt.Sprintf(
		"ap-identity: %d of %d device(s) on this page carry BOTH `id` and `macAddress`, which is the join "+
			"a uplinkDeviceId needs. totalCount = %d, limit = %d.",
		joined, len(page.Data), page.TotalCount, page.Limit)}
	if page.TotalCount > len(page.Data) {
		out = append(out, fmt.Sprintf(
			"→ TRUNCATED: this page holds %d of %d devices. A driver that stopped here would treat "+
				"every device past the cut as unresolvable.", len(page.Data), page.TotalCount))
	}
	if strings.Contains(strings.ToLower(body), "bssid") {
		out = append(out, "→ a BSSID appears in this answer. That is NEW: it was absent on Network "+
			"10.5.67, and if it is present a redirect naming a BSSID becomes resolvable here rather "+
			"than only on the legacy device list.")
	} else {
		out = append(out, "→ no BSSID anywhere in this answer, which matches 10.5.67. A redirect "+
			"naming a BSSID rather than a device MAC is therefore unresolvable on the Integration "+
			"API alone, and is reported as unconfirmed rather than refused.")
	}
	return out
}

// uplinkResolutionFindings performs the join the driver performs, on the two
// bodies this run actually received.
//
// It is the access-point question as a *behaviour* rather than as a field scan: the
// earlier finding proved a field exists, and a field existing is not the same
// claim as a field resolving. A controller could carry `uplinkDeviceId` and
// place clients under devices the site list does not return, and every field
// scan in the world would report success.
func uplinkResolutionFindings(clientStatus int, clientBody string, deviceStatus int, deviceBody string) []string {
	if clientStatus < 200 || clientStatus > 299 || deviceStatus < 200 || deviceStatus > 299 {
		return nil
	}
	var clients struct {
		Data []struct {
			MACAddress     string `json:"macAddress"`
			UplinkDeviceID string `json:"uplinkDeviceId"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(clientBody), &clients); err != nil || len(clients.Data) == 0 {
		return nil
	}
	uplink := clients.Data[0].UplinkDeviceID
	if uplink == "" {
		return []string{"ap-identity: this client carries no uplinkDeviceId, so its access point is " +
			"unresolvable — reported as unconfirmed, never as a mismatch."}
	}
	var devices struct {
		Data []struct {
			ID         string `json:"id"`
			MACAddress string `json:"macAddress"`
			Name       string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(deviceBody), &devices); err != nil {
		return nil
	}
	for _, d := range devices.Data {
		if d.ID == uplink {
			return []string{fmt.Sprintf(
				"ap-identity RESOLVED: uplinkDeviceId %s → %s (%s). This is the comparison "+
					"VerifyPresenceDetail makes against the redirect's `ap` parameter.",
				uplink, normalizeMAC(d.MACAddress), d.Name)}
		}
	}
	return []string{fmt.Sprintf(
		"ap-identity: uplinkDeviceId %s matches NO device on the page read above. Either the site has more "+
			"devices than one page, or the controller places this client under something it does not "+
			"list. Unresolvable is reported as unconfirmed, never as a mismatch.", uplink)}
}

func collectAPIdentity(path string, node any, out map[string]string) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			lower := strings.ToLower(key)
			for _, kw := range apIdentityKeywords {
				if strings.Contains(lower, kw) {
					out[childPath] = scalarString(child)
					break
				}
			}
			collectAPIdentity(childPath, child, out)
		}
	case []any:
		for i, child := range v {
			collectAPIdentity(fmt.Sprintf("%s[%d]", path, i), child, out)
		}
	}
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return `"` + t + `"`
	case bool:
		return fmt.Sprintf("%t", t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case []any:
		return fmt.Sprintf("(array of %d)", len(t))
	case map[string]any:
		return fmt.Sprintf("(object with %d keys)", len(t))
	default:
		return fmt.Sprintf("%v", t)
	}
}

// legacyDeviceFindings answers the legacy question: is the BSSID → access point mapping the
// redirect needs actually in this answer?
func legacyDeviceFindings(body string) []string {
	var doc struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return []string{"legacy-devices: the answer is not the {\"data\":[…]} shape this step expected — read it raw."}
	}

	out := []string{fmt.Sprintf("legacy-devices: %d device(s) in the list.", len(doc.Data))}
	withVAP, totalBSSID := 0, 0

	for _, dev := range doc.Data {
		mac := scalarOrDash(dev, "mac")
		ip := scalarOrDash(dev, "ip")
		model := scalarOrDash(dev, "model")
		name := scalarOrDash(dev, "name")

		bssids := bssidsOf(dev["vap_table"])
		if len(bssids) > 0 {
			withVAP++
			totalBSSID += len(bssids)
		}
		shown := bssids
		if len(shown) > 4 {
			shown = append(append([]string{}, shown[:4]...), fmt.Sprintf("… and %d more", len(bssids)-4))
		}
		line := fmt.Sprintf("    mac=%s ip=%s model=%s name=%s vap_table=%d", mac, ip, model, name, len(bssids))
		if len(shown) > 0 {
			line += " bssids=[" + strings.Join(shown, " ") + "]"
		}
		out = append(out, line)
	}

	switch {
	case len(doc.Data) == 0:
		out = append(out, "→ the list is empty, so nothing can be resolved from it.")
	case withVAP == 0:
		out = append(out, "→ NO device carries a vap_table. The BSSID → access point mapping "+
			"PacketFence relies on is NOT in this answer on this controller version, and that is "+
			"a finding worth more than the whole rest of this run.")
	default:
		out = append(out, fmt.Sprintf("→ %d of %d devices carry a vap_table, %d BSSIDs in total. "+
			"The mapping the redirect needs is present; check that the redirect's `ap` value appears "+
			"among the device MACs or the BSSIDs above.", withVAP, len(doc.Data), totalBSSID))
	}
	return out
}

func scalarOrDash(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return "—"
	}
	s := scalarString(v)
	return strings.Trim(s, `"`)
}

func bssidsOf(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, entry := range arr {
		vap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"bssid", "mac"} {
			if s, ok := vap[key].(string); ok && s != "" {
				out = append(out, strings.ToLower(s))
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

func (u *UniFiDriver) probeJSON(ctx context.Context, name, what, method, urlStr string, payload map[string]any, note string) ProbeStep {
	body, err := json.Marshal(payload)
	if err != nil {
		return ProbeStep{Name: name, What: what, Method: method, URL: urlStr, Err: err.Error(), Note: note}
	}
	return u.probeRequest(ctx, name, what, method, urlStr, body, note)
}

// probeRequest performs one request and records it. The API key is sent and
// never recorded: this output is meant to be shared.
func (u *UniFiDriver) probeRequest(ctx context.Context, name, what, method, urlStr string, body []byte, note string) ProbeStep {
	step := ProbeStep{Name: name, What: what, Method: method, URL: urlStr, Note: note}
	if body != nil {
		step.RequestBody = prettyJSON(body)
	}

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rdr)
	if err != nil {
		step.Err = err.Error()
		return step
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if u.apiKey != "" {
		req.Header.Set("X-API-KEY", u.apiKey)
	}
	u.mu.RLock()
	token := u.csrfToken
	u.mu.RUnlock()
	if token != "" {
		req.Header.Set("X-CSRF-Token", token)
	}

	start := time.Now()
	resp, err := u.httpClient.Do(req)
	step.Duration = time.Since(start)
	if err != nil {
		step.Err = err.Error()
		return step
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	step.Status = resp.StatusCode
	step.Body = prettyJSON(raw)
	if readErr != nil {
		step.Err = readErr.Error()
	}
	return step
}

// prettyJSON indents a JSON body and leaves anything else exactly as it came,
// because "it was not JSON" is itself a measurement.
func prettyJSON(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

// firstClientID reads the client id out of a recorded lookup body. It matches on
// the MAC rather than taking data[0], because a filter the controller ignored
// would otherwise hand the caller somebody else's client id.
func firstClientID(body, mac string) string {
	var list unifiClientList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		return ""
	}
	for _, c := range list.Data {
		if normalizeMAC(c.MACAddress) == mac {
			return c.ID
		}
	}
	return ""
}
