package apistore

import (
	"context"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// DashboardReader — the overview and the alarm list.
//
// **Every figure carries its own freshness, and that is not decoration.**
// `clientsOnline` is derived from `radacct`, whose counters advance on
// Interim-Update or Stop with a widely-implemented 300-second floor — and on the
// controller-API path there may be no accounting at all. A bare number on this
// screen is the console asserting something the network never told it, which is why
// `Metric.value` is nullable: a figure that could not be sampled is absent, never
// zero.

// Overview answers the dashboard's six figures.
func (a *Adapter) Overview(ctx context.Context, siteID string) (gen.Overview, error) {
	sessions, err := a.st.Accounting().CountSessions(ctx, siteID)
	if err != nil {
		return gen.Overview{}, out(err)
	}
	devices, err := a.st.RadiusClients().CountNas(ctx, siteID)
	if err != nil {
		return gen.Overview{}, out(err)
	}
	redeemed, err := a.st.Vouchers().CountRedeemedSince(ctx, siteID, startOfDay(a.now().In(a.location(ctx, siteID))))
	if err != nil {
		return gen.Overview{}, out(err)
	}
	alarms, err := a.Alarms(ctx, siteID)
	if err != nil {
		return gen.Overview{}, err
	}

	return gen.Overview{
		ClientsOnline: gen.Metric{
			Value:     ptr(float32(sessions.Online)),
			Freshness: onlineFreshness(sessions, a.now()),
		},
		// A count of rows in a table nobody has to sample. `live`, and it is the
		// only figure on this screen that may honestly say so.
		VouchersRedeemedToday: gen.Metric{
			Value:     ptr(float32(redeemed)),
			Freshness: gen.Freshness{State: gen.FreshnessStateLive, SampledAt: ptr(a.now())},
		},
		NasTotal:     devices.Total,
		NasUnhealthy: devices.Unhealthy,
		OrphanedSessions: &gen.Metric{
			Value:     ptr(float32(sessions.Orphaned)),
			Freshness: orphanFreshness(sessions, a.now()),
		},
		AlarmCount: len(alarms),
	}, nil
}

// startOfDay is midnight in t's zone — the site's (`sites.time_zone`), because
// "today" is the operator's day.
//
// A UTC day boundary would move the figure by hours in Vietnam, and an
// operator counting this morning's vouchers at 06:00 Indochina Time would be
// reading yesterday's.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// onlineFreshness says how old the count of online clients is.
//
// **Three clauses, and `web/mock`'s `/overview` implements the same three.** The
// contract states them under `Overview.clientsOnline`, because two servers each
// restating a rule in their own language is how two servers come to disagree, and
// there is no runtime both can share.
//
//  1. Nothing counted → `live`, sampled now. There is no sample behind a figure
//     with no rows in it, and zero is what the database knows at this instant.
//  2. Otherwise the sample time is the **oldest** last-update among the counted
//     rows, never the newest: the count is only as current as its least current
//     row, and a fleet where one access point stopped reporting an hour ago is a
//     count that is quietly an over-estimate by however many guests have left.
//  3. `stale` when that sample is older than twice the declared interval.
//
// The declared interval is the 300-second floor rather than any one NAS's request,
// so the verdict is reproducible from the two numbers the console renders. A row
// that asked for less and is late against its own promise is a *per-session* fault
// and travels on that session's own `Freshness`, where there is no floor.
//
// The rows are `c.OldestOnlineUpdate`'s, which is to say the ones `Online` counts,
// not every open session with orphans included — see the field.
func onlineFreshness(c store.SessionCounts, now time.Time) gen.Freshness {
	if c.OldestOnlineUpdate == nil {
		return gen.Freshness{State: gen.FreshnessStateLive, SampledAt: ptr(now)}
	}
	state := gen.FreshnessStateSampled
	if now.Sub(*c.OldestOnlineUpdate) > 2*interimFloor*time.Second {
		state = gen.FreshnessStateStale
	}
	return gen.Freshness{
		State:           state,
		SampledAt:       c.OldestOnlineUpdate,
		IntervalSeconds: ptr(interimFloor),
	}
}

// orphanFreshness says when the oldest unaccounted-for session was last heard from.
//
// `orphanedSessions` is a `Metric`, not a bare integer: as an integer, the one
// figure whose entire meaning is *these have not been heard from* could not say
// when any of them last was. This is where the difference between the two figures
// on this screen is decided.
//
// `Metric`'s rule is one rule and it is applied here unchanged — the sample is the
// oldest of exactly the rows the value counts. What is not the same is what an old
// sample *means*. On `clientsOnline` it is an artefact of the instrument and a
// warning that the number may be wrong. Here the age is the subject: nothing about
// forty days makes the count of them less true. Same derivation, different claim,
// and a client that renders the two identically says something false about one.
//
// Three clauses, and the third is the one that differs from `onlineFreshness`:
//
//  1. **No orphans → `live`, dated now.** The same reason as `onlineFreshness`:
//     there is no sample behind a figure with no rows in it.
//  2. **The sample is the oldest of the counted orphans.** It is the whole point of
//     making this figure a `Metric` — it separates one access point that rebooted
//     half an hour ago from rows nothing has reaped since July. Those are the same
//     integer and different faults.
//  3. **No interval, and `stale` whenever the count is non-zero.**
//
// **`IntervalSeconds` is nil, and that is a fact rather than an omission.** A row
// joins this set after `2 × max(its own interval, 300s)` of silence — see
// `store.sessionState` — so the set mixes rows judged at ten minutes with rows
// judged at two hours and no one cadence is true of all of them. `onlineFreshness`
// publishes the 300-second floor because that floor is what turns its age into a
// verdict; here membership already decided the verdict before the figure existed.
// Publishing 300 anyway would have the console print *every 5 minutes* beside a set
// of rows defined by nothing arriving.
//
// **`stale` is true and it is also redundant, and the contract says so out loud.**
// True: every counted row has had no update within twice its interval, which is
// what put it in the set, and it names the fault `sessions-orphaned` raises. Redundant:
// it is `stale` exactly when the count is non-zero, so it discriminates nothing the
// value does not already say. That is stated rather than dressed up — **on this
// figure the state is not the information, the timestamp is** — and it is why the
// console renders this card's sample time and not this card's verdict.
func orphanFreshness(c store.SessionCounts, now time.Time) gen.Freshness {
	if c.OldestOrphanUpdate == nil {
		return gen.Freshness{State: gen.FreshnessStateLive, SampledAt: ptr(now)}
	}
	return gen.Freshness{State: gen.FreshnessStateStale, SampledAt: c.OldestOrphanUpdate}
}

// Alarms derives the alarm list from what is actually recorded.
//
// **Every alarm here is a kind and its params, never a sentence.** The console
// renders the copy from its own catalogue, and the reason is on the first screen of
// the product: the mock composed its alarms in Vietnamese and the console is
// English-only, so the dashboard greeted an English-speaking operator in a language
// they had not chosen. A server cannot know the reader's language at the time it
// raises an alarm.
//
// Six of the eight kinds are derived. The two that are not are named at the bottom
// of this function, because an alarm nothing raises is a catalogue entry that can
// never be shown to render correctly.
func (a *Adapter) Alarms(ctx context.Context, siteID string) ([]gen.Alarm, error) {
	devices, err := a.st.RadiusClients().ListNas(ctx, listOptions(siteID, "", "", 200))
	if err != nil {
		return nil, out(err)
	}
	sessions, err := a.st.Accounting().CountSessions(ctx, siteID)
	if err != nil {
		return nil, out(err)
	}
	controllers, err := a.st.Controllers().List(ctx)
	if err != nil {
		return nil, out(err)
	}

	alarms := []gen.Alarm{}
	add := func(kind gen.AlarmKind, level gen.AlarmLevel, subjectKind gen.AlarmSubjectKind, id, name string, params map[string]any) {
		al := gen.Alarm{
			Id:    string(kind) + ":" + id,
			Kind:  kind,
			Level: level,
			Subject: &struct {
				Id   *string               `json:"id,omitempty"`
				Kind *gen.AlarmSubjectKind `json:"kind,omitempty"`
				Name *string               `json:"name,omitempty"`
			}{Id: ptr(id), Kind: ptr(subjectKind), Name: ptr(name)},
		}
		if len(params) > 0 {
			p := params
			al.Params = &p
		}
		alarms = append(alarms, al)
	}

	for _, n := range devices.Items {
		if !n.Enabled {
			continue
		}
		name := n.Name
		switch n.Reachability {
		case string(gen.NasReachabilityUnreachable):
			add(gen.AlarmKindNasUnreachable, gen.Urgent, gen.AlarmSubjectKindNas, n.ID, name,
				map[string]any{"nasName": name})
		case string(gen.NasReachabilityCredentialsExpired):
			add(gen.AlarmKindNasCredentialsExpired, gen.Urgent, gen.AlarmSubjectKindNas, n.ID, name,
				map[string]any{"nasName": name})
		}
		// A device that refuses Message-Authenticator is a device left open to
		// BlastRADIUS (CVE-2024-3596), and the rule is that this is loud and
		// per-NAS rather than a silent downgrade.
		if n.MessageAuthState == string(gen.NasMessageAuthenticatorRefused) {
			add(gen.AlarmKindNasMessageAuthenticatorRefused, gen.Urgent, gen.AlarmSubjectKindNas, n.ID, name,
				map[string]any{"nasName": name})
		}
		// `false`, never `nil`. Nil is "not measured" and the console may not
		// render it as missing — the distinction is the whole reason the column
		// is three-valued.
		if n.AccountingConfigured != nil && !*n.AccountingConfigured {
			add(gen.AlarmKindNasNoAccounting, gen.Warning, gen.AlarmSubjectKindNas, n.ID, name,
				map[string]any{"nasName": name})
		}
		if capabilitiesOut(n.Capabilities).WalledGarden == gen.GardenKindNone {
			add(gen.AlarmKindNasNoWalledGarden, gen.CautionMinor, gen.AlarmSubjectKindNas, n.ID, name,
				map[string]any{"nasName": name})
		}
	}

	if sessions.Orphaned > 0 {
		add(gen.AlarmKindSessionsOrphaned, gen.CautionMajor, gen.AlarmSubjectKindSite, siteID, siteID,
			map[string]any{"count": sessions.Orphaned})
	}
	// Stale is a different fault from orphaned: the sessions are inside their
	// window but the newest sample is older than two intervals, so the figures on
	// the dashboard are older than the dashboard implies.
	//
	// **Derived from the same timestamp `clientsOnline` carries, and not guarded
	// on `Orphaned == 0`.** Such a guard would only compensate for the wrong row
	// set: a `min` over open sessions makes any orphan produce a stale verdict, so
	// without it the alarm would fire on every deployment that had one. With the
	// timestamp taken over the rows the figure counts, an orphan cannot reach this
	// branch at all — and the guard would suppress a true signal, so a dashboard
	// whose figure is labelled `stale` would sit beside an alarm list that says
	// nothing about it. Two derivations of one fact that disagree is the same
	// defect as two servers restating one rule, one screen along.
	if sessions.OldestOnlineUpdate != nil && a.now().Sub(*sessions.OldestOnlineUpdate) > 2*interimFloor*time.Second {
		add(gen.AlarmKindSessionsStale, gen.CautionMinor, gen.AlarmSubjectKindSite, siteID, siteID,
			map[string]any{"sampledAt": sessions.OldestOnlineUpdate.UTC().Format(time.RFC3339)})
	}

	for _, c := range controllers {
		// **The same predicate `ControllersInSite` filters on, read rather than
		// restated.** Restating it here — `len(c.VendorSites) > 0 &&
		// !boundTo(...)` — would be `docs/rules/database.md`'s second-source-of-truth
		// defect in Go, and a change to the rule would need two call sites repaired
		// rather than one.
		// An alarm raised about a controller the operator cannot find a row for is
		// an alarm they cannot act on, so the two rules are one rule.
		if !controllerServesSite(c, siteID) {
			continue
		}
		// **Not a reachability alarm and must not be worded as one.** The
		// controller answers and the credential is accepted; what is missing is
		// confidence that this driver can drive *that version*. On the
		// controller-API vendors that is the entire authorization path, so RADIUS
		// keeps answering, every access point stays reachable, and no guest can be
		// let on.
		if c.VersionSupported != nil && !*c.VersionSupported {
			params := map[string]any{"controllerName": c.Name, "vendor": c.Vendor}
			if c.Version != nil {
				params["version"] = *c.Version
			}
			add(gen.AlarmKindControllerVersionUnsupported, gen.Urgent, gen.AlarmSubjectKindController, c.ID, c.Name, params)
		}
	}

	// `minimumVersion` is deliberately absent from the params above: it is the
	// driver's claim about itself, not a recorded fact about this controller, and
	// `ControllerProbe` is where it is reported. An alarm that named a version
	// nobody had checked against would be the console inventing an instruction.
	return alarms, nil
}
