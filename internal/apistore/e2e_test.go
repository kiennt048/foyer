package apistore_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
)

// A meaningful slice of the contract, driven over HTTP against real PostgreSQL.
//
// **The failure this suite exists against is an adapter that type-checks and does
// nothing.** A server with no ports answers every operation `501`; a partially
// wired one answers `501` from exactly the operations nobody drove. So each case below asserts a *row*, not a status: the id that came back is
// the id that was seeded, the count is the count in the table, the figure is the one
// the accounting rows imply.

func TestTheAdapterAnswersTheContractFromRealRows(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	t.Run("sites", func(t *testing.T) {
		var sites []gen.Site
		w.getJSON("/sites", &sites)
		if !containsFunc(sites, func(s gen.Site) bool { return s.Id == w.siteA }) ||
			!containsFunc(sites, func(s gen.Site) bool { return s.Id == w.siteB }) {
			t.Fatalf("the two seeded sites did not come back: %+v", sites)
		}
	})

	t.Run("nas is the device read model, not the clients.conf row", func(t *testing.T) {
		var page struct {
			Items      []gen.Nas `json:"items"`
			NextCursor *string   `json:"nextCursor"`
		}
		w.getJSON("/nas?siteId="+w.siteA, &page)
		if len(page.Items) != 2 {
			t.Fatalf("site A has two devices, got %d: %+v", len(page.Items), page.Items)
		}
		for _, n := range page.Items {
			if n.SiteId != w.siteA {
				t.Fatalf("a device from another site reached a site-scoped list: %s", n.Id)
			}
			// The whole point of `radius_clients LEFT JOIN access_points`: the
			// device carries the MAC discovery resolved, and the row still
			// reaches a `clients.conf` entry.
			if n.Mac == nil {
				t.Errorf("%s came back with no MAC, so the LEFT JOIN produced nothing", n.Name)
			}
			if !n.SecretSet {
				t.Errorf("%s reports no shared secret, but one was sealed for it", n.Name)
			}
			if n.IpAddress == nil {
				t.Errorf("%s came back with no address, so nothing could be written to clients.conf", n.Name)
			}
		}
		// Capabilities are the stored ones rather than a default: a device with
		// `walledGarden: none` must not render as though it had a garden.
		refusing := findFunc(page.Items, func(n gen.Nas) bool { return n.MessageAuthenticator == gen.NasMessageAuthenticatorRefused })
		if refusing == nil {
			t.Fatal("the device that refuses Message-Authenticator did not survive the round trip")
		}
		if refusing.Capabilities.WalledGarden != gen.GardenKindNone {
			t.Errorf("walledGarden = %q, want none", refusing.Capabilities.WalledGarden)
		}
		// `accountingConfigured` is three-valued and the middle value is the one
		// that gets lost: nil means not measured, and rendering it as `missing`
		// is the defect the column exists against.
		if refusing.AccountingConfigured == nil || *refusing.AccountingConfigured {
			t.Errorf("accountingConfigured = %v, want an explicit false", refusing.AccountingConfigured)
		}
	})

	t.Run("sessions carry the counters and their freshness", func(t *testing.T) {
		var page struct {
			Items []gen.Session `json:"items"`
		}
		w.getJSON("/sessions?siteId="+w.siteA, &page)
		if len(page.Items) != 3 {
			t.Fatalf("site A has three accounting rows, got %d", len(page.Items))
		}

		orphaned := findFunc(page.Items, func(s gen.Session) bool { return s.Id == w.sessionOrphaned })
		if orphaned == nil {
			t.Fatal("the session with no Acct-Stop did not come back")
		}
		// Forty days open with no stop record. `orphaned`, never `online`: the
		// derivation is the store's `sessionState`, and this asserts it survived
		// the adapter.
		if orphaned.State != gen.SessionStateOrphaned {
			t.Errorf("a session open for forty days with no Acct-Stop is %q, want orphaned", orphaned.State)
		}
		if orphaned.Freshness.State != gen.FreshnessStateStale {
			t.Errorf("freshness = %q, want stale — an interim update forty days old is not a sample", orphaned.Freshness.State)
		}

		wrapped := findFunc(page.Items, func(s gen.Session) bool { return s.Id == w.sessionWrapped })
		if wrapped == nil {
			t.Fatal("the wrapping-counter session did not come back")
		}
		// **The two halves must disagree.** `inputOctets` is what the NAS
		// reported — a 32-bit counter — and `inputOctetsTotal` is the
		// reconstruction. Equal values here would mean the adapter handed the
		// reconstruction back twice and a client subtracting two of them would
		// go 4 GiB negative.
		if !wrapped.CountersWrapped {
			t.Error("countersWrapped = false for a total past 2^32")
		}
		if wrapped.InputOctets == nil || wrapped.InputOctetsTotal == nil {
			t.Fatal("a wrapped session came back with no counters")
		}
		if *wrapped.InputOctets >= *wrapped.InputOctetsTotal {
			t.Errorf("inputOctets %d is not below inputOctetsTotal %d, so the wrap was not carried",
				*wrapped.InputOctets, *wrapped.InputOctetsTotal)
		}
		if *wrapped.InputOctets != 12345 {
			t.Errorf("the reported counter is %d, want the low word 12345", *wrapped.InputOctets)
		}

		// The access point behind the called-station id resolved to a name.
		online := findFunc(page.Items, func(s gen.Session) bool { return s.Id == w.sessionOnline })
		if online == nil || online.ApName == nil {
			t.Fatalf("the online session did not resolve its access point: %+v", online)
		}
		if online.Ssid == nil || *online.Ssid != "Guest" {
			t.Errorf("ssid = %v, want the tail of the Called-Station-Id", online.Ssid)
		}
	})

	t.Run("a session filter the repository cannot take is still applied", func(t *testing.T) {
		var page struct {
			Items []gen.Session `json:"items"`
		}
		// Two states at once: `SessionFilter.State` is single-valued, so this is
		// the path that filters after the read and refills the page.
		w.getJSON("/sessions?siteId="+w.siteA+"&state=online,orphaned", &page)
		for _, s := range page.Items {
			if s.State != gen.SessionStateOnline && s.State != gen.SessionStateOrphaned {
				t.Fatalf("state=%q survived a two-state filter", s.State)
			}
		}
		if len(page.Items) < 2 {
			t.Fatalf("want at least the online and the orphaned row, got %d", len(page.Items))
		}

		// And by device, which the repository has no filter for at all.
		var byNas struct {
			Items []gen.Session `json:"items"`
		}
		w.getJSON("/sessions?siteId="+w.siteA+"&nasId="+w.deviceRefusing, &byNas)
		for _, s := range byNas.Items {
			if s.NasId != w.deviceRefusing {
				t.Fatalf("nasId=%q survived a filter for %q", s.NasId, w.deviceRefusing)
			}
		}
		if len(byNas.Items) != 1 {
			t.Fatalf("one session is on that device, got %d", len(byNas.Items))
		}
	})

	t.Run("vouchers and their histogram", func(t *testing.T) {
		var batch gen.VoucherBatch
		w.getJSON("/voucher-batches/"+w.batchA, &batch)
		if batch.Count != 4 {
			t.Fatalf("count = %d, want 4", batch.Count)
		}
		if batch.Counts == nil || batch.Counts.Unused == nil || batch.Counts.InUse == nil {
			t.Fatal("the histogram came back empty, so it was composed rather than aggregated")
		}
		// Aggregated from the voucher rows, so redeeming one moves it. A stored
		// counter would still say four unused.
		if *batch.Counts.Unused != 3 || *batch.Counts.InUse != 1 {
			t.Errorf("unused/inUse = %d/%d, want 3/1 after one redemption", *batch.Counts.Unused, *batch.Counts.InUse)
		}
		// The plan's enforcement was computed from the site's own devices. Site A
		// has one device with `hardQuota: false`, and a data allowance the
		// weakest device cannot cap is `foyer-sampled`, never `nas`.
		if batch.Plan.EnforcedBy != gen.PlanEnforcedByFoyerSampled {
			t.Errorf("enforcedBy = %q, want foyer-sampled: the weakest enabled device in the site cannot cap a quota", batch.Plan.EnforcedBy)
		}
		if batch.CreatedBy == nil || *batch.CreatedBy != w.adminUser {
			t.Errorf("createdBy = %v, want the signed-in operator's username", batch.CreatedBy)
		}
	})

	t.Run("the walled garden reports what each device can enforce", func(t *testing.T) {
		var g gen.WalledGarden
		w.getJSON("/sites/"+w.siteA+"/walled-garden", &g)
		// The weakest, not the strongest: site A has an fqdn-sni device and a
		// `none` device, so the garden is `none`.
		if g.EffectiveKind != gen.GardenKindNone {
			t.Errorf("effectiveKind = %q, want none — a garden is only as strong as the device that enforces it least well", g.EffectiveKind)
		}
		if len(g.Drivers) != 2 {
			t.Errorf("driver coverage lists %d devices, want 2", len(g.Drivers))
		}
		if len(g.Entries) != 1 || g.Entries[0].Origin != gen.GardenEntryOriginRequired {
			t.Fatalf("the required entry did not survive: %+v", g.Entries)
		}
		// Refused, and by the database rather than by a check somebody can
		// forget: removing the portal's own name presents to every guest as a
		// network that is simply broken.
		w.do(http.MethodDelete, "/sites/"+w.siteA+"/walled-garden/"+g.Entries[0].Id, nil, http.StatusConflict)
	})

	t.Run("the overview is derived, and every figure carries its freshness", func(t *testing.T) {
		var o gen.Overview
		w.getJSON("/overview?siteId="+w.siteA, &o)
		if o.NasTotal != 2 {
			t.Errorf("nasTotal = %d, want 2", o.NasTotal)
		}
		// The device that refuses Message-Authenticator is unhealthy even though
		// it is reachable.
		if o.NasUnhealthy != 1 {
			t.Errorf("nasUnhealthy = %d, want 1", o.NasUnhealthy)
		}
		if o.ClientsOnline.Value == nil {
			t.Fatal("clientsOnline has no value, so nothing was counted")
		}
		if o.OrphanedSessions == nil || o.OrphanedSessions.Value == nil || *o.OrphanedSessions.Value != 1 {
			t.Fatalf("orphanedSessions = %v, want 1", o.OrphanedSessions)
		}
		// Derived from `radacct`, whose counters advance on Interim-Update at
		// best every five minutes. A `live` figure here would be the console
		// asserting something the network never told it.
		if o.ClientsOnline.Freshness.State == gen.FreshnessStateLive {
			t.Error("clientsOnline claims to be live, but it comes from accounting")
		}
		// **The assertion is about which rows the timestamp was taken over.**
		// This world holds two online sessions updated a minute or two
		// ago and one orphan whose last interim update was forty days ago. The
		// orphan is not in `clientsOnline` — it is the whole of
		// `orphanedSessions` — so a freshness derived over both reports the
		// online count as forty days stale while it is two minutes old.
		if o.ClientsOnline.Freshness.State != gen.FreshnessStateSampled {
			t.Errorf("clientsOnline freshness = %q, want sampled: its two sessions were updated "+
				"minutes ago, and the forty-day row is counted by orphanedSessions instead",
				o.ClientsOnline.Freshness.State)
		}
		if o.ClientsOnline.Freshness.SampledAt == nil {
			t.Error("clientsOnline is sampled from rows and carries no sample time")
		} else if age := time.Since(*o.ClientsOnline.Freshness.SampledAt); age > time.Hour {
			t.Errorf("clientsOnline was sampled %s ago; the rows it counts are minutes old, so the "+
				"timestamp came from a row the figure does not count", age)
		}
		// **The orphan count dates itself, and not with the online count's
		// timestamp.**
		//
		// This world holds two online sessions updated a minute or two ago and
		// one orphan whose last interim update was forty days ago. So the two
		// figures on this screen that come from `radacct` must carry two
		// different timestamps, and the failure this catches is that they carry
		// one — either because `orphanFreshness` was copied from
		// `onlineFreshness`, or because the store took one `min` over the union,
		// which is exactly the defect the assertion above guards against.
		orphan := o.OrphanedSessions.Freshness
		if orphan.SampledAt == nil {
			t.Fatal("the orphan count carries no sample time, so the one useful sentence about " +
				"it — when the oldest of these was last heard from — has nowhere to go")
		}
		if age := time.Since(*orphan.SampledAt); age < 24*time.Hour {
			t.Errorf("the orphan count was sampled %s ago; the one row it counts was last heard "+
				"from forty days ago, so this timestamp came from rows the figure does not count", age)
		}
		// The non-vacuity half: two figures over disjoint row sets must not agree
		// about the same instant. If they ever do, this world stopped being able
		// to tell a per-figure derivation from a shared one and neither assertion
		// above is evidence.
		if o.ClientsOnline.Freshness.SampledAt != nil &&
			orphan.SampledAt.Equal(*o.ClientsOnline.Freshness.SampledAt) {
			t.Error("the online and orphan figures carry one timestamp between them, so neither " +
				"is derived over the rows it counts")
		}
		// `stale` is true of every counted row — no update within twice its own
		// interval is what put it in the set — and it is redundant with the
		// value, which the contract says out loud. It is asserted anyway because
		// the alternative that looks reasonable, `live`, is the one that would
		// make the console drop the timestamp entirely.
		if orphan.State != gen.FreshnessStateStale {
			t.Errorf("orphanedSessions freshness = %q, want stale", orphan.State)
		}
		// **No interval, and it is a fact rather than an omission.** A row joins
		// this set after `2 × max(its own interval, 300s)`, so the set mixes rows
		// judged at ten minutes with rows judged at two hours. Publishing the
		// 300-second floor the online figure declares would have the console
		// print "every 5 minutes" beside rows defined by nothing arriving.
		if orphan.IntervalSeconds != nil {
			t.Errorf("orphanedSessions declares a %ds interval; there is no cadence behind a set "+
				"of rows whose membership is decided against each row's own interval",
				*orphan.IntervalSeconds)
		}

		// A plain table count, and the only figure on the screen that may say so.
		if o.VouchersRedeemedToday.Freshness.State != gen.FreshnessStateLive {
			t.Errorf("vouchersRedeemedToday freshness = %q, want live", o.VouchersRedeemedToday.Freshness.State)
		}
		if o.AlarmCount == 0 {
			t.Error("no alarms, but the world has a device refusing Message-Authenticator and an orphaned session")
		}
	})

	t.Run("alarms are kinds and params, never sentences", func(t *testing.T) {
		var alarms []gen.Alarm
		w.getJSON("/alarms?siteId="+w.siteA, &alarms)
		kinds := map[gen.AlarmKind]bool{}
		for _, a := range alarms {
			kinds[a.Kind] = true
			if a.Title != nil && *a.Title != "" {
				t.Errorf("alarm %s carries server-composed text: a server cannot know the reader's language", a.Kind)
			}
		}
		for _, want := range []gen.AlarmKind{
			gen.AlarmKindNasMessageAuthenticatorRefused,
			gen.AlarmKindNasNoAccounting,
			gen.AlarmKindNasNoWalledGarden,
			gen.AlarmKindSessionsOrphaned,
		} {
			if !kinds[want] {
				t.Errorf("no %s alarm, but the world has the condition for it", want)
			}
		}
	})

	t.Run("registering a device returns its secret exactly once", func(t *testing.T) {
		raw := w.do(http.MethodPost, "/nas", map[string]any{
			"siteId": w.siteA, "name": "New MikroTik", "driver": "mikrotik",
			"ipAddress": "10.10.0.31",
		}, http.StatusCreated)
		var created struct {
			Nas          gen.Nas `json:"nas"`
			SharedSecret string  `json:"sharedSecret"`
		}
		mustJSON(t, raw, &created)
		if created.SharedSecret == "" {
			t.Fatal("no shared secret came back, so the device cannot be configured")
		}
		if len(created.SharedSecret) < 24 {
			t.Errorf("the shared secret is %d characters; there is no default and it is crypto/rand", len(created.SharedSecret))
		}

		// And never again. Reading the device back must not carry it.
		var back gen.Nas
		w.getJSON("/nas/"+created.Nas.Id, &back)
		if !back.SecretSet {
			t.Error("secretSet = false immediately after one was stored")
		}
		if strContains(w.rawGet("/nas/"+created.Nas.Id), created.SharedSecret) {
			t.Fatal("the shared secret came back from getNas: it is returned exactly once and never again")
		}

		// Rotation mints a new one and says what applying it costs.
		//
		// Driven against the device that has been measured reachable rather than
		// the one just created, because a freshly registered device is
		// `reachability: unknown` and `rotateNasSecret` refuses that: the refusal
		// reads `n.Reachability != ok`, so it treats *not yet measured* as *cannot be
		// reached*, which is the wide reading of a claim that was narrow.
		rotated := w.do(http.MethodPost, "/nas/"+w.deviceOK+"/secret", nil, http.StatusOK)
		var rot struct {
			SharedSecret string          `json:"sharedSecret"`
			ChangeClass  gen.ChangeClass `json:"changeClass"`
		}
		mustJSON(t, rotated, &rot)
		if rot.SharedSecret == "" || rot.SharedSecret == created.SharedSecret {
			t.Error("rotation returned nothing, or the same secret")
		}
		// `restart` until somebody measures otherwise: FreeRADIUS 3.2.x reads SQL
		// clients at startup and SIGHUP does not reliably reload them.
		if rot.ChangeClass != gen.ChangeClassRestart {
			t.Errorf("changeClass = %q, want restart from the conservative classifier", rot.ChangeClass)
		}
	})

	t.Run("audit entries are written by the operations that require a permission", func(t *testing.T) {
		var page struct {
			Items []gen.AuditEntry `json:"items"`
		}
		// **Scoped by `actorId`, and it has to be.** The container and the tenant
		// are shared across every world in this package, so the audit log holds
		// every world's entries — an unscoped read would see them all, and
		// asserting they named *this* world's operator would hold only for as long
		// as no other test registered a device through the API. It is the same
		// shape as the fixture-world lesson in `docs/rules/testing.md`: an assertion that is
		// true because of what the world happens not to contain.
		//
		// Filtering by the **id** and asserting on the **username** is also the
		// stronger reading. `actorUsername` is denormalised on purpose — it has to
		// outlive the operator it names — so the two agreeing is a property worth
		// checking rather than a tautology.
		w.getJSON("/audit?actorId="+w.adminID, &page)
		if len(page.Items) == 0 {
			t.Fatal("no audit entries at all for the operator who registered a device and rotated a secret")
		}
		kinds := map[gen.AuditKind]bool{}
		for _, e := range page.Items {
			kinds[e.Kind] = true
			if e.ActorUsername != w.adminUser {
				t.Errorf("actorUsername = %q, want the operator who acted", e.ActorUsername)
			}
		}
		for _, want := range []gen.AuditKind{gen.AuditKindNasRegistered, gen.AuditKindNasSecretRotated} {
			if !kinds[want] {
				t.Errorf("no %s entry, but that operation ran in this test", want)
			}
		}
	})

	t.Run("nothing echoes a submitted credential", func(t *testing.T) {
		// The same rule the portal carries for the username echo, applied to the
		// console: a password reaching a response, an audit entry or an error is
		// the failure, whichever of the three it is.
		body := w.rawGet("/audit")
		if strContains(body, w.adminPassword) {
			t.Fatal("the audit log contains a submitted password")
		}
	})
}

// TestRevokingAnInUseVoucherFindsItsSession is the operation whose honesty is the
// point: revoking a voucher succeeds, and ending the session it is being used for
// is a separate driver call that can fail on its own.
//
// `radacct` has no voucher column, so the link has to be resolved — and a `revoke`
// that could not find the session would answer `sessionEnded: false` for a guest who
// is demonstrably still online, which is exactly the dishonesty the operation's
// `failure` member exists to prevent.
func TestRevokingAnInUseVoucherFindsItsSession(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	raw := w.do(http.MethodPost, "/vouchers/"+w.voucherInUse+"/revoke", nil, http.StatusOK)
	var result struct {
		Voucher      gen.Voucher  `json:"voucher"`
		SessionEnded bool         `json:"sessionEnded"`
		Failure      *gen.Failure `json:"failure"`
	}
	mustJSON(t, raw, &result)

	if result.Voucher.State != gen.VoucherStateRevoked {
		t.Fatalf("state = %q, want revoked", result.Voucher.State)
	}
	// The session is on a device with no controller, so the driver call cannot be
	// made and the response says so rather than claiming the guest is off the
	// network. Either outcome is honest; a `sessionEnded: true` with nothing done
	// is not, and `failure == nil && sessionEnded == false` would mean the session
	// was never found at all.
	if !result.SessionEnded && result.Failure == nil {
		t.Fatal("the voucher's live session was not found: `radacct` has no voucher column and the link was not resolved")
	}
	if result.Failure != nil && result.Failure.Type != gen.ProblemssessionEndFailed {
		t.Errorf("failure type = %q, want /problems/session-end-failed", result.Failure.Type)
	}
}

// TestRotationIsRefusedOnlyWhereThereIsNoRouteBack pins the narrow reading.
//
// `RotateSecret: false` makes rotation **manual**, not impossible. Both conditions
// have to hold — the driver cannot push *and* the device is unreachable — before
// the operation is refused, because refusing on either alone would leave a device
// unable to rotate a secret at all.
func TestRotationIsRefusedOnlyWhereThereIsNoRouteBack(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	// Reachable, and the driver cannot push. Rotation is still available.
	w.do(http.MethodPost, "/nas/"+w.deviceOK+"/secret", nil, http.StatusOK)

	// Unreachable, and the driver cannot push. No route back, so it is refused.
	raw := w.do(http.MethodPost, "/nas/"+w.deviceUnreachable+"/secret", nil, http.StatusConflict)
	if !strContains(raw, "rotation-refused") {
		t.Fatalf("want /problems/rotation-refused, got %s", truncate(raw))
	}
}

// TestProbeReportsTheDriversCapabilitiesAndOnlyWordsTheContractKnows locks the
// product side of the seam against a fake that encodes its author's assumptions.
//
// Two things can go wrong here and each hides the other.
//
// **A fake that is the most capable driver that could exist.** A mock returning
// `WalledGardenFQDN` — the strongest of three — where the only shipped driver
// returns `WalledGardenNone`, the weakest, and `RotateSecret: true` where every
// vendor in the design returns false, sends any test reaching a gate through it
// down the branch no deployment takes. A capability decides whether an admin
// control exists at all.
//
// **And nothing reaching it anyway.** If every device this harness seeded had no
// controller, `probeNas` would take the no-controller path and `SecretRotation`
// would short-circuit before it asked a driver, so the capability values would have
// no route into the product code. One device has a controller so that this path is
// walked rather than described.
//
// The garden assertion is derived in two steps rather than typed: the shipped
// driver's own value first, then the single contract word that spells it. The two
// enums agree on exactly one of three words, and a bare conversion in `probeNas`
// would emit `fqdn`, which `GardenKind.Valid()` rejects and the console's three-way
// switch does not know.
func TestProbeReportsTheDriversCapabilitiesAndOnlyWordsTheContractKnows(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	raw := w.do(http.MethodPost, "/nas/"+w.deviceOK+"/probe", nil, http.StatusOK)
	var probe gen.NasProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("reading the probe: %v: %s", err, truncate(raw))
	}

	shipped := nas.ShippedCapabilities(nas.VendorUniFi)
	for _, c := range []struct {
		name      string
		got, want bool
	}{
		{"hardQuota", probe.Capabilities.HardQuota, shipped.HardQuota},
		{"bandwidthLimits", probe.Capabilities.BandwidthLimits, shipped.BandwidthLimits},
		{"liveReauthorize", probe.Capabilities.LiveReauthorize, shipped.LiveReauthorize},
		{"rotateSecret", probe.Capabilities.RotateSecret, shipped.RotateSecret},
	} {
		if c.got != c.want {
			t.Errorf("probe reports %s = %v, the shipped driver reports %v", c.name, c.got, c.want)
		}
	}

	if !probe.Capabilities.WalledGarden.Valid() {
		t.Errorf("probe reports walledGarden = %q, which is not a member of the contract's enum",
			probe.Capabilities.WalledGarden)
	}
	if shipped.WalledGarden != nas.WalledGardenNone {
		t.Fatalf("the shipped UniFi driver now reports %q; this assertion needs the contract's word "+
			"for that value rather than the one below", shipped.WalledGarden)
	}
	if probe.Capabilities.WalledGarden != gen.GardenKindNone {
		t.Errorf("probe reports walledGarden = %q, want %q — the shipped driver cannot maintain a "+
			"garden, and a probe that says otherwise puts a control on a screen that does nothing",
			probe.Capabilities.WalledGarden, gen.GardenKindNone)
	}
}

// TestProbeCarriesTheDriversWarningsAndTheUnconditionalOneIsAlwaysThere locks two
// things, and both halves of it are the point.
//
// **The warnings have to be made before they can be carried.** A
// `ProbeAccessPoint` that answered out of `Capabilities()` and
// `DiscoverAccessPoints()` without calling `Probe()` would leave `warnings` an empty
// array on a path where no driver had been asked for one. The console renders the
// array, so an operator would read a device with no caveats over a driver that
// reports one on every probe.
//
// **It is a kind, and the assertion is on the kind.** `ProbeWarning` is a kind plus
// params, so a driver emitting English sentences would mean filling one from the
// other by matching on prose — a hand-maintained copy with a regular expression on
// top. A test that matched on the sentence would put that copy back
// one layer down, so every assertion here is on `gen.ProbeWarningKind`.
//
// **And the §4 notice is asserted on a controller that is fine.** That is the whole
// reason it must travel: on a driver with no mechanism for `docs/rules/security.md` §4's
// access-point clause, three of the four checks happen and the fourth does not — on
// a healthy controller exactly as much as on a broken one. A warning that only
// appeared when something else was wrong is one an operator learns to expect the
// absence of.
//
// **This is not a claim about UniFi.** Measured on a real controller, the UniFi
// Integration API's client object carries `uplinkDeviceId` and it resolves to the
// access point, so `UniFiDriver.unconditionalWarnings()` is empty and
// `canConfirmAccessPoint()` is true. This test's subject is the fixture driver,
// which cannot confirm.
//
// The lock is still worth having and its subject is still real, but note what that
// does to the surrounding evidence: an **absent** notice means *this driver checks*
// on one vendor and *somebody forgot* on another, and the warning list cannot
// separate them. `nas.ShippedConfirmsAccessPoint` is the question to ask instead of
// reading an absence as an answer.
//
// **Made to fail once.** Deleting the `d.Probe(ctx)` call turns this red on an empty `warnings` while every
// other assertion in this suite stays green, which is the answer to *is an empty
// warnings array something a test can catch.* Making the driver's §4 notice
// conditional on `TLSInsecureSkipVerify` turns three of `TestUniFiProbe`'s four
// rows red and leaves the fourth green, which is the shape of the defect this half
// guards.
func TestProbeCarriesTheDriversWarningsAndTheUnconditionalOneIsAlwaysThere(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	// The device whose controller answers. `deviceUnreachable` would prove less:
	// a warning beside a failure is what an operator expects anyway.
	raw := w.do(http.MethodPost, "/nas/"+w.deviceOK+"/probe", nil, http.StatusOK)
	var probe gen.NasProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("reading the probe: %v: %s", err, truncate(raw))
	}

	if len(probe.Warnings) == 0 {
		t.Fatalf("probe of a healthy device carries no warnings at all — the contract has the field, "+
			"the console renders it, and the driver produces one on every probe: %s", truncate(raw))
	}
	kinds := map[gen.ProbeWarningKind]gen.ProbeWarning{}
	for _, warn := range probe.Warnings {
		if !warn.Kind.Valid() {
			t.Errorf("warning kind %q is not a member of the contract's enum; the console has copy "+
				"for the closed list and nothing for this", warn.Kind)
		}
		kinds[warn.Kind] = warn
	}

	if _, ok := kinds[gen.AccessPointUnconfirmed]; !ok {
		t.Errorf("a healthy controller's probe carries %v and not %q — the access point a redirect "+
			"claims is not checked on this driver whatever the controller answers, and a green tick "+
			"over it tells an operator a check happened",
			keysOf(kinds), gen.AccessPointUnconfirmed)
	}

	// The derived one, and the reason it is derived: `unverified` names which
	// capabilities are believed rather than measured, and this names how many.
	// Two hand-written lists of one fact is how they disagree, so the count is
	// read off the same answer rather than typed here.
	if len(probe.Unverified) == 0 {
		t.Fatal("the probe names no unverified capability; the shipped driver has three and this " +
			"assertion has lost its subject")
	}
	unverified, ok := kinds[gen.CapabilitiesUnverified]
	if !ok {
		t.Fatalf("the probe names %v as unverified and carries no %q warning — the capability table "+
			"is not the only place an operator reads this", probe.Unverified, gen.CapabilitiesUnverified)
	}
	if unverified.Params == nil {
		t.Fatalf("%q carries no params; the console interpolates the count and cannot compute it",
			gen.CapabilitiesUnverified)
	}
	// JSON numbers arrive as float64.
	if got, want := (*unverified.Params)["count"], float64(len(probe.Unverified)); got != want {
		t.Errorf("%q says count = %v while unverified names %d capabilities — one answer, two figures",
			gen.CapabilitiesUnverified, got, len(probe.Unverified))
	}

	// And nothing the contract does not name got through. `internal/nas` reports
	// `certificate-verification-disabled`, which this API's `TlsMode` cannot
	// produce and whose kind the contract deliberately omits; it must be dropped
	// at the boundary rather than coerced into a kind the console has copy for.
	if _, ok := kinds[gen.ProbeWarningKind("certificate-verification-disabled")]; ok {
		t.Error("a kind the contract does not define reached the wire")
	}
}

func keysOf(m map[gen.ProbeWarningKind]gen.ProbeWarning) []gen.ProbeWarningKind {
	out := make([]gen.ProbeWarningKind, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (w *world) rawGet(path string) []byte {
	w.t.Helper()
	return w.do(http.MethodGet, path, nil, http.StatusOK)
}

func mustJSON(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decoding: %v: %s", err, truncate(raw))
	}
}

func strContains(haystack []byte, needle string) bool {
	return needle != "" && bytes.Contains(haystack, []byte(needle))
}

func containsFunc[T any](items []T, pred func(T) bool) bool {
	return findFunc(items, pred) != nil
}

func findFunc[T any](items []T, pred func(T) bool) *T {
	for i := range items {
		if pred(items[i]) {
			return &items[i]
		}
	}
	return nil
}

// TestASiteCreatedThroughTheContractCanCarryADevice — a created site is usable, against
// real PostgreSQL.
//
// **The `internal/api` half of this walk is weaker than it looks and that is why
// this exists.** The in-memory fake's `RegisterAccessPoint` does not check that
// the site is real, so a `createSite` that minted an id nothing else accepts would
// still let that test's device registration answer `201`. Here
// `radius_clients.site_id` is `REFERENCES sites(id)`, so a bogus id is a foreign
// key violation and the walk is a claim the database has to agree with.
//
// It uses **a world with no sites at all**, in a tenant of its own. `w.siteA` and
// `w.siteB` are what every other case in this package leans on, and a world that
// always has a site is precisely the shape that hides a `createSite` nothing else
// can use.
//
// **Its own tenant**, and that is not tidiness. A deployment is one venue per
// install, so `createSite` refuses when the deployment already has a site, and this
// package shares one PostgreSQL schema with every sibling test that seeds two.
// Without row-level security scoping the question to THIS deployment, the walk
// below could never reach a `201`.
func TestASiteCreatedThroughTheContractCanCarryADevice(t *testing.T) {
	w := newFreshInstall(t)
	w.signIn()

	// Nothing yet, and asserted rather than assumed: every line below is about what
	// happens in a deployment with no venue, and if the tenant scoping stopped
	// working this test would go on passing for the wrong reason — against a world
	// that has sites, testing a `createSite` that should have refused.
	var existing []gen.Site
	w.getJSON("/sites", &existing)
	if len(existing) != 0 {
		t.Fatalf("a fresh install already has %d site(s); the tenant scoping is not holding, "+
			"and nothing below is testing what it claims", len(existing))
	}

	name := "Venue " + uniq("v")
	var site gen.Site
	raw := w.do(http.MethodPost, "/sites", map[string]any{"name": name}, http.StatusCreated)
	if err := json.Unmarshal(raw, &site); err != nil {
		t.Fatalf("createSite: %v: %s", err, truncate(raw))
	}
	if site.Id == "" || site.Name != name {
		t.Fatalf("createSite returned %+v, want a minted id and the venue's name", site)
	}
	if site.Environment != gen.SiteEnvironmentLive {
		t.Errorf("environment = %q, want live — a created site is a venue", site.Environment)
	}

	// The row, not the response. An adapter that answered from its argument and
	// wrote nothing would satisfy every assertion above.
	var stored, environment string
	if err := w.owner.QueryRow(t.Context(),
		`SELECT name, environment FROM sites WHERE id = $1`, site.Id,
	).Scan(&stored, &environment); err != nil {
		t.Fatalf("the created site is not in `sites`: %v", err)
	}
	if stored != name || environment != "live" {
		t.Errorf("stored (%q, %q), want (%q, live)", stored, environment, name)
	}

	// And the step a site exists for: a device attaches to it. `mikrotik`
	// because it needs no controller, which is what a first install has.
	addr := w.nextAddr()
	var made struct {
		Nas gen.Nas `json:"nas"`
	}
	raw = w.do(http.MethodPost, "/nas", map[string]any{
		"siteId": site.Id, "name": "Front of house", "driver": "mikrotik", "ipAddress": addr,
	}, http.StatusCreated)
	if err := json.Unmarshal(raw, &made); err != nil {
		t.Fatalf("createNas: %v: %s", err, truncate(raw))
	}

	// The `clients.conf` row exists and is scoped to the site that was just made.
	// This is the assertion the foreign key backs: a site id `createNas` cannot
	// use is a `createSite` that closes nothing.
	var clients int
	if err := w.owner.QueryRow(t.Context(),
		`SELECT count(*) FROM radius_clients WHERE site_id = $1`, site.Id,
	).Scan(&clients); err != nil {
		t.Fatal(err)
	}
	if clients != 1 {
		t.Fatalf("radius_clients rows in the new site = %d, want 1", clients)
	}

	// ── and the second venue, which this deployment may not have ─────────────
	//
	// One venue per install, asserted from the same world that just went
	// from zero sites to one. A refusal asserted against a world that STARTED with
	// sites proves only that the count is non-zero; this one proves the product
	// itself closed the door behind it.
	raw = w.do(http.MethodPost, "/sites", map[string]any{"name": "Chi nhánh Hai"}, http.StatusConflict)
	var refusal gen.Problem
	if err := json.Unmarshal(raw, &refusal); err != nil {
		t.Fatalf("the second createSite: %v: %s", err, truncate(raw))
	}
	// NOT `/problems/duplicate`. Nothing collided — the name is new — and a client
	// rendering *"choose another name"* would send the operator back to the form to
	// do something that cannot work.
	if refusal.Type != gen.ProblemsoneSitePerInstall {
		t.Errorf("the second createSite was refused as %q, want %q", refusal.Type, gen.ProblemsoneSitePerInstall)
	}

	// Nothing half-written. `CreateFirst` is one statement precisely so that a
	// refused call cannot leave a row behind.
	var after []gen.Site
	w.getJSON("/sites", &after)
	if len(after) != 1 {
		t.Fatalf("the deployment has %d sites after a refused second createSite, want 1", len(after))
	}
}
