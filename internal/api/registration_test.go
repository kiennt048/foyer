package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// TestCreateNasRefusesAnAddressTheDatabaseWouldRefuse.
//
// `radius_clients.address` is `inet NOT NULL`, so an omitted address that reached
// the column would come back as a `500`, `/problems/internal` — for a field the
// operator simply had not filled in. So `NasCreate` requires `ipAddress`:
// FreeRADIUS matches a packet to a client **by its source address and nothing
// else**, so a `clients.conf` row without one is a client every Access-Request is
// discarded for.
//
// Two refusals rather than one, and they are not interchangeable. *Blank* is
// `required` and its copy is "Required."; *unparseable* is `not-an-ip-address` and
// its copy names what an address looks like. A client keying on `(type, field,
// fault)` renders a different sentence for each, which is the entire reason
// `FieldFault` exists.
func TestCreateNasRefusesAnAddressTheDatabaseWouldRefuse(t *testing.T) {
	cases := []struct {
		name  string
		body  map[string]any
		fault gen.FieldFault
	}{
		{
			name:  "absent",
			body:  map[string]any{"siteId": testSite, "name": "Back office", "driver": "coa"},
			fault: gen.FieldFaultRequired,
		},
		{
			name: "blank",
			body: map[string]any{
				"siteId": testSite, "name": "Back office", "driver": "coa", "ipAddress": "   ",
			},
			fault: gen.FieldFaultRequired,
		},
		{
			name: "not an address",
			body: map[string]any{
				"siteId": testSite, "name": "Back office", "driver": "coa", "ipAddress": "10.20.0.two",
			},
			fault: gen.FieldFaultNotAnIpAddress,
		},
		{
			name: "an address with too many octets",
			body: map[string]any{
				"siteId": testSite, "name": "Back office", "driver": "coa", "ipAddress": "10.20.0.1.9",
			},
			fault: gen.FieldFaultNotAnIpAddress,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.signIn(adminUser)
			resp := r.do(http.MethodPost, "/nas", tc.body)
			body := readAll(t, resp)
			resp.Body.Close()

			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("got %d, want 422 — a field the operator did not fill in is not "+
					"an internal error\n%s", resp.StatusCode, body)
			}
			var problem struct {
				Type   string            `json:"type"`
				Errors map[string]string `json:"errors"`
				Params struct {
					Fields map[string]string `json:"fields"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(body), &problem); err != nil {
				t.Fatalf("not a problem document: %v\n%s", err, body)
			}
			if problem.Type != string(gen.Problemsvalidation) {
				t.Errorf("type = %q, want %q", problem.Type, gen.Problemsvalidation)
			}
			if got := problem.Params.Fields["ipAddress"]; got != string(tc.fault) {
				t.Errorf("fault = %q, want %q — the console keys its copy on this\n%s",
					got, tc.fault, body)
			}
			// `errors` and `params.fields` are asserted to be the same key set in
			// `web/mock`; a field marked and not described is one the console can
			// highlight and not explain.
			if _, ok := problem.Errors["ipAddress"]; !ok {
				t.Errorf("marks `ipAddress` and does not describe it\n%s", body)
			}
		})
	}
}

// TestCreateNasAcceptsAHostOrARange — the other half, because a refusal test on
// its own is satisfied by a handler that refuses everything.
//
// Both shapes, because `radius_clients.address` is `inet` rather than `cidr`:
// Aruba's dynamic-radius-proxy toggle decides which of the two an operator needs
// and Foyer cannot see that toggle.
func TestCreateNasAcceptsAHostOrARange(t *testing.T) {
	for _, address := range []string{"10.20.0.4", "10.20.0.0/16", "2001:db8::1", "2001:db8::/48"} {
		t.Run(address, func(t *testing.T) {
			r := newRig(t)
			r.signIn(adminUser)
			resp := r.do(http.MethodPost, "/nas", map[string]any{
				// `coaVendor` because the driver is `coa`, which is one enum member
				// for Cisco and Aruba: they identify a session differently and the
				// wrong one is answered with an ACK and no effect, so the
				// registration refuses a device that names neither. See
				// `TestCreateNasRefusesCoAWithNoVendor` for that half.
				"siteId": testSite, "name": "Back office", "driver": "coa", "ipAddress": address,
				"coaVendor": "cisco",
			})
			body := readAll(t, resp)
			resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("got %d, want 201\n%s", resp.StatusCode, body)
			}
		})
	}
}

// TestCreateNasRefusesCoAWithNoVendor — at the gate that stops the row being made.
//
// `NasDriverKind` has one member, `coa`, for Cisco and Aruba. They identify a
// session differently — Cisco IOS-XE by a `Cisco-AVPair audit-session-id` VSA, Aruba
// by `Calling-Station-Id` in its own spelling — and RFC 5176 §2.3 makes the NAS match
// on every session-identification attribute in the request, so the wrong one matches
// nothing. A careful device answers `Error-Cause 503`; **a careless one ignores the
// request and ACKs it**, which `docs/rules/testing.md` says is precisely the
// failure a lab cannot produce.
//
// So it is refused where somebody can still type the answer, and refused again where
// the driver is built (`apistore.ErrCoAVendorUnknown`, for the rows this gate cannot
// reach). Both halves are asserted here — a refusal test on its own is satisfied by a
// handler that refuses every driver, which would be a different defect on four of the
// five.
func TestCreateNasRefusesCoAWithNoVendor(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodPost, "/nas", map[string]any{
		"siteId": testSite, "name": "Campus edge", "driver": "coa", "ipAddress": "10.21.0.4",
	})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 — a device Foyer cannot pick a dialect for must not be "+
			"registered as though it can be driven\n%s", resp.StatusCode, body)
	}
	var problem struct {
		Errors map[string]string `json:"errors"`
		Params struct {
			Fields map[string]string `json:"fields"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatalf("not a problem document: %v\n%s", err, body)
	}
	if got := problem.Params.Fields["coaVendor"]; got != string(gen.FieldFaultRequired) {
		t.Errorf("fault = %q, want %q\n%s", got, gen.FieldFaultRequired, body)
	}
	if _, ok := problem.Errors["coaVendor"]; !ok {
		t.Errorf("marks `coaVendor` and does not describe it\n%s", body)
	}

	// The four drivers that are not `coa` name a vendor by naming themselves, so
	// none of them is asked for one.
	for _, driver := range []string{"mikrotik"} {
		ok := r.do(http.MethodPost, "/nas", map[string]any{
			"siteId": testSite, "name": "Hotspot router", "driver": driver, "ipAddress": "10.21.0.5",
		})
		status := ok.StatusCode
		ok.Body.Close()
		if status != http.StatusCreated {
			t.Errorf("driver %q was refused %d; only `coa` names a family rather than a vendor",
				driver, status)
		}
	}
}

// TestControllerAmendmentIsRecordedWithoutTheCredential.
//
// This entry was **deliberately absent** until `AuditKind` gained a member for it:
// an amendment that replaces a stored credential is neither a registration nor a
// deletion, which is all `listAudit` claimed to record. Leaving it unrecorded rather
// than mislabelled was right, and it was also a hole in the one log that exists to
// answer *who changed the thing that broke* — a controller whose API key was swapped
// is a controller somebody else may now be driving.
//
// The entry names the fields and **never a value**: `apiKey` and `password` are
// write-only in this contract, and an audit log read months later by somebody who
// was not there is the last place either should reappear. That is asserted here
// rather than left to the constructor, because it is the property that would be
// quietest to lose.
//
// **`controller-discovery-run` is not driven here**, and the reason is worth stating
// rather than leaving as a silence: `internal/api/memory`'s `Discover` answers a
// scripted result and adopts nothing, so a drive through this rig would exercise the
// audit call and none of the counts that make the entry worth reading. It is driven
// end to end in `web/mock`, where devices actually get adopted, and the enum sweep
// there is what holds it.
func TestControllerAmendmentIsRecordedWithoutTheCredential(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	// Registered through the API rather than seeded into the shared world, so this
	// test cannot change what any other test counts.
	created := r.do(http.MethodPost, "/controllers", map[string]any{
		"name": "Cloud Key", "vendor": "unifi", "baseUrl": "https://10.20.9.9:8443",
		"authMode": "api-key", "tlsMode": "verify", "apiKey": "the-first-key",
	})
	createdBody := readAll(t, created)
	created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("register a controller: got %d, want 201\n%s", created.StatusCode, createdBody)
	}
	var controller struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createdBody), &controller); err != nil {
		t.Fatalf("controller: %v\n%s", err, createdBody)
	}

	const secret = "an-api-key-that-must-not-appear-in-a-log"
	resp := r.do(http.MethodPatch, "/controllers/"+controller.Id, map[string]any{"apiKey": secret})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("amend: got %d, want 200\n%s", resp.StatusCode, body)
	}

	entries := auditEntries(t, r)
	updated := findAudit(entries, gen.AuditKindControllerUpdated)
	if updated == nil {
		t.Fatalf("replacing a controller's API key recorded nothing")
	}
	if updated.Subject.Kind != gen.AuditSubjectKindController {
		t.Errorf("subject kind = %q, want controller", updated.Subject.Kind)
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the audit entry carries the credential that was set:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"apiKey"`) {
		t.Errorf("the entry does not name which field moved, so it records that "+
			"*something* changed — the shape of a log nobody trusts:\n%s", raw)
	}
}

func auditEntries(t *testing.T, r *rig) []gen.AuditEntry {
	t.Helper()
	resp := r.do(http.MethodGet, "/audit?limit=200", nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list audit: %d\n%s", resp.StatusCode, body)
	}
	var page struct {
		Items []gen.AuditEntry `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("audit page: %v\n%s", err, body)
	}
	return page.Items
}

func findAudit(entries []gen.AuditEntry, kind gen.AuditKind) *gen.AuditEntry {
	for i := range entries {
		if entries[i].Kind == kind {
			return &entries[i]
		}
	}
	return nil
}

// TestCreateSiteIsTheStepBeforeCreateNas — and the reason it is in this file.
//
// `createNas` requires a `siteId` and refuses `422` without one, correctly. A
// migrated database holds **zero** sites. With only `listSites` on `/sites`, a
// stranger who installed Foyer could sign in, be told the site switcher was empty,
// and have no way at all to fill it — a fresh install stopping at step one, on a
// path where every step after it works.
//
// **The assertion is the walk, not the status code.** A `201` from `createSite`
// alone is satisfied by a handler that mints an id nothing else accepts; what
// closes the gap is that the id it returns is one `createNas` will take. So this
// registers a device into the site it just made, and it uses **only** that site —
// never `testSite`, which is the seeded row that would hide this.
func TestCreateSiteIsTheStepBeforeCreateNas(t *testing.T) {
	// `freshRig`, not `newRig`: with one venue per install, a world that already
	// has `testSite` cannot reach this operation's `201` at all. That rule does not
	// weaken this coverage — it needs the zero-site world a real install starts in.
	r := freshRig(t)
	r.signIn(adminUser)

	// Nothing yet, asserted rather than assumed. Everything below is about a
	// deployment with no venue, and a rig that quietly regained one would leave this
	// test green while testing a `createSite` that should have refused.
	empty := r.do(http.MethodGet, "/sites", nil)
	before := readAll(t, empty)
	empty.Body.Close()
	if strings.TrimSpace(before) != "[]" {
		t.Fatalf("a fresh deployment already lists sites: %s", before)
	}

	resp := r.do(http.MethodPost, "/sites", map[string]any{"name": "  Quán Cà Phê Số 7  "})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("got %d, want 201 — a deployment with no site cannot register a device\n%s",
			resp.StatusCode, body)
	}
	var site gen.Site
	if err := json.Unmarshal([]byte(body), &site); err != nil {
		t.Fatalf("not a Site: %v\n%s", err, body)
	}
	if site.Id == "" {
		t.Fatalf("created a site with no id — nothing can be attached to it\n%s", body)
	}
	// Trimmed, because the venue name is typed and a leading space is not part of
	// the venue's name. Asserted rather than assumed: this is the value the site
	// switcher renders and the audit log will one day denormalise.
	if site.Name != "Quán Cà Phê Số 7" {
		t.Errorf("name = %q, want the trimmed venue name\n%s", site.Name, body)
	}
	// `live`, not `lab`. `SiteCreate` accepts no environment, and the loud value
	// is the right default: `lab` is the badge that says *you may point this at
	// fake gear*, and a real venue must not wear it by accident.
	if site.Environment != gen.SiteEnvironmentLive {
		t.Errorf("environment = %q, want live\n%s", site.Environment, body)
	}

	// It is in the directory, which is the screen an operator reads next.
	list := r.do(http.MethodGet, "/sites", nil)
	listed := readAll(t, list)
	list.Body.Close()
	if !strings.Contains(listed, site.Id) {
		t.Errorf("the site it just created is not in listSites\n%s", listed)
	}

	// The portal settings row is born beside it. A venue with no settings is a
	// Portal screen reading "Not found" and a guest with no context — the
	// defect this deployment met with a real site and zero `portal_settings`
	// rows. GET is the read; PATCH is the proof the row is writable, not a
	// 404 wearing a different status.
	got := r.do(http.MethodGet, "/sites/"+site.Id+"/portal", nil)
	gotBody := readAll(t, got)
	got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("getPortalSettings on the new site got %d, want 200\n%s", got.StatusCode, gotBody)
	}
	var settings gen.PortalSettings
	if err := json.Unmarshal([]byte(gotBody), &settings); err != nil {
		t.Fatalf("not PortalSettings: %v\n%s", err, gotBody)
	}
	if settings.VenueName != "Quán Cà Phê Số 7" {
		t.Errorf("venueName = %q, want the name the site was created with\n%s", settings.VenueName, gotBody)
	}
	// The portal's address comes from the process, not the store: the console
	// cannot construct an origin that is right outside development, and no
	// per-site row holds one. A rig with no PortalURL wired answers null; one
	// that wires it answers it. Both are asserted because a preview of somebody
	// else's portal — or of nothing, silently — is the failure either way.
	if settings.PortalUrl != nil {
		t.Errorf("portalUrl = %q on a rig with no PortalURL wired, want null rather than a guess\n%s", *settings.PortalUrl, gotBody)
	}
	patched := r.do(http.MethodPatch, "/sites/"+site.Id+"/portal", map[string]any{"venueName": "Quán Cà Phê Số 7 — tầng trệt"})
	patchedBody := readAll(t, patched)
	patched.Body.Close()
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("updatePortalSettings on the new site got %d, want 200\n%s", patched.StatusCode, patchedBody)
	}
	// And the whole point: a device attaches to it.
	made := r.do(http.MethodPost, "/nas", map[string]any{
		"siteId": site.Id, "name": "Front of house", "driver": "mikrotik",
		"ipAddress": "10.30.0.4",
	})
	madeBody := readAll(t, made)
	made.Body.Close()
	if made.StatusCode != http.StatusCreated {
		t.Fatalf("registering a device into the new site got %d, want 201 — the site "+
			"operation exists and does not close the gap\n%s", made.StatusCode, madeBody)
	}

	// ── and the second venue, which this deployment may not have ─────────────
	//
	// One venue per install, asserted from the same world that just went
	// from zero sites to one — so it is the product itself closing the door, not a
	// count that was never zero.
	second := r.do(http.MethodPost, "/sites", map[string]any{"name": "Chi nhánh Hai"})
	secondBody := readAll(t, second)
	second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("the second createSite got %d, want 409\n%s", second.StatusCode, secondBody)
	}
	var refusal gen.Problem
	if err := json.Unmarshal([]byte(secondBody), &refusal); err != nil {
		t.Fatalf("the refusal is not a Problem: %v\n%s", err, secondBody)
	}
	// NOT `/problems/duplicate`: nothing collided, so *choose another name* is
	// advice that cannot work, and the two types differ because what the operator
	// does next differs.
	if refusal.Type != gen.ProblemsoneSitePerInstall {
		t.Errorf("refused as %q, want %q\n%s", refusal.Type, gen.ProblemsoneSitePerInstall, secondBody)
	}
}

// TestCreateSiteIsRecorded — the half of the audit trail a new enum member does
// not buy.
//
// Adding `site-created` to `AuditKind` makes the entry *legal*. A kind nothing
// emits is the same defect one step along: a catalogue entry no client can ever
// be shown to render correctly. So this
// drives the operation and reads the log back through `listAudit`, which is the
// surface an operator actually has.
//
// **`freshRig`, and that is load-bearing rather than incidental.** A world that
// already holds a venue answers `createSite` with `409`, so the only world in
// which this entry can be written at all is the one a real installation starts
// in — the same reason `TestCreateSiteIsTheStepBeforeCreateNas` is here and not in
// a rig with `testSite` seeded.
func TestCreateSiteIsRecorded(t *testing.T) {
	r := freshRig(t)
	r.signIn(adminUser)

	// Nothing recorded yet, asserted rather than assumed: an entry that was
	// already in the log would satisfy every assertion below without this
	// operation having written anything.
	if existing := findAudit(auditEntries(t, r), gen.AuditKindSiteCreated); existing != nil {
		t.Fatalf("a fresh deployment already has a site-created entry (%s) — this test "+
			"cannot tell whether createSite wrote one", existing.Id)
	}

	resp := r.do(http.MethodPost, "/sites", map[string]any{"name": "Quán Cà Phê Số 7"})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("createSite got %d, want 201\n%s", resp.StatusCode, body)
	}
	var site gen.Site
	if err := json.Unmarshal([]byte(body), &site); err != nil {
		t.Fatalf("not a Site: %v\n%s", err, body)
	}

	entry := findAudit(auditEntries(t, r), gen.AuditKindSiteCreated)
	if entry == nil {
		t.Fatal("creating a venue recorded nothing — the log that holds every later " +
			"change to this site says nothing about where it came from")
	}
	// **Invariant 10, from the other side.** The kind exists so that this entry
	// need not be filed under a near-enough one; a subject of any other kind would
	// be `nas-registered`'s mistake wearing the right name.
	if entry.Subject == nil || entry.Subject.Kind != gen.AuditSubjectKindSite {
		t.Fatalf("subject = %+v, want kind `site` — the change made a venue", entry.Subject)
	}
	if entry.Subject.Id != site.Id {
		t.Errorf("subject id = %q, want the venue that was created (%q)", entry.Subject.Id, site.Id)
	}
	// The name is denormalised on the entry for the same reason `actorUsername` is:
	// a log read months later cannot look up a subject that has been renamed, and
	// the console's copy catalogue deliberately does not carry it.
	if entry.Subject.Name == nil || *entry.Subject.Name != site.Name {
		t.Errorf("subject name = %v, want %q", entry.Subject.Name, site.Name)
	}
	if entry.ActorUsername != adminUser {
		t.Errorf("actor = %q, want the operator who created it (%q)", entry.ActorUsername, adminUser)
	}
	// No composed prose, the same rule every kind carries: the console renders
	// `title` only for a kind it predates, and a server that always sends one is a
	// server whose clients never implement the kind.
	if entry.Title != nil || entry.Detail != nil {
		t.Errorf("the entry carries server prose: title=%v detail=%v", entry.Title, entry.Detail)
	}
}

// TestCreateSiteRefusesAVenueWithNoName.
//
// `sites.name` is `text NOT NULL` and `""` satisfies that, so the database is not
// the gate here: an unnamed venue would be stored and the site switcher would
// render a gap the operator cannot click. Whitespace counts as blank for the same
// reason the name is trimmed on the way in.
//
// Two halves, because a refusal test alone is satisfied by a handler that refuses
// everything — the acceptance half is `TestCreateSiteIsTheStepBeforeCreateNas`, and
// it is deliberately not repeated here.
func TestCreateSiteRefusesAVenueWithNoName(t *testing.T) {
	for _, name := range []string{"", "   "} {
		r := newRig(t)
		r.signIn(adminUser)
		resp := r.do(http.MethodPost, "/sites", map[string]any{"name": name})
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("name %q got %d, want 422\n%s", name, resp.StatusCode, body)
		}
		var problem struct {
			Errors map[string]string `json:"errors"`
			Params struct {
				Fields map[string]string `json:"fields"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(body), &problem); err != nil {
			t.Fatalf("not a problem document: %v\n%s", err, body)
		}
		// The two key sets are the same set — `errors` carries the prose a client
		// with no copy falls back to, `fields` carries the kind a client with copy
		// renders from, and either half alone passes for a server that has drifted.
		if got := problem.Params.Fields["name"]; got != string(gen.FieldFaultRequired) {
			t.Errorf("name %q: fault = %q, want %q\n%s", name, got, gen.FieldFaultRequired, body)
		}
		if _, ok := problem.Errors["name"]; !ok {
			t.Errorf("name %q: marks `name` and does not describe it\n%s", name, body)
		}
	}
}

// TestCreateSiteIsAdministratorOnly.
//
// `x-permission: nas:manage`, which staff do not hold. The site is the container
// every device, portal setting and walled-garden entry hangs off, so making one is
// network configuration rather than day-to-day work — the same gate `createNas`
// and `createController` carry, and for the same reason.
//
// The gate is computed from the contract rather than written in Go, so this test
// is checking the annotation as much as the code.
func TestCreateSiteIsAdministratorOnly(t *testing.T) {
	r := newRig(t)
	r.signIn(staffUser)
	resp := r.do(http.MethodPost, "/sites", map[string]any{"name": "Somebody else's venue"})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("staff got %d, want 403\n%s", resp.StatusCode, body)
	}
}
