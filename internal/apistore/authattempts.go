package apistore

import (
	"context"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// AuthAttemptLog — "why can't this guest get online?".
//
// ── the site scope, and where it now lives ───────────────────────────────────
//
// `radpostauth` carries the stock FreeRADIUS column set verbatim, and that set has
// **no NAS address and no tenant**. `ListSessions` derives a site by joining
// `radius_clients` on `radacct.nasipaddress`; there is no such column here, so a
// repository that returned rows unscoped answers `listAuthAttempts?siteId=…` with
// **every site's** authentication attempts — usernames and MACs, across venues, on
// the screen an operator opens when a guest is standing in front of them.
//
// It is resolved in `internal/store`, beside the identical join the session query
// performs, rather than here from `Called-Station-Id` and a MAC index built in Go.
// The adapter passes the scope down and reads the device and the site off the row.
// **Two places knowing how a `Called-Station-Id` is spelled is the defect**, not
// the duplication of effort: a repository that cannot be
// asked a scoped question puts the security property in whichever caller remembers
// it.

// AuthAttempts lists both authentication streams, newest first.
//
// ── `source` is pushed down ──────────────────────────────────────────────────
//
// Answering it here, by returning an empty page for anything that is not `radius`,
// would be a hole: `radpostauth` is the RADIUS layer's table and every row in it is
// `radius` by construction, while `AuthEventKind: authorize-failed` is in the
// contract, describing *"the controller refusing after every check Foyer owns has
// passed, which is invisible from both of the other two sources"* — and an operator
// querying for it would get an empty list rather than the row they were looking for.
//
// `authorization_events` is the writer (migration 00014) and the repository unions
// the two streams onto one cursor, so the filter is a predicate rather than a
// short-circuit. `portal` is still empty, and still deliberately: nothing writes it,
// and answering it with rows from another layer would be the API claiming an event
// came from a place that never wrote one.
func (a *Adapter) AuthAttempts(ctx context.Context, q api.AuthAttemptQuery) (api.Page[gen.AuthAttempt], error) {
	outcome := ""
	if q.Outcome != nil {
		outcome = string(*q.Outcome)
	}
	source := ""
	if q.Source != nil {
		source = string(*q.Source)
	}

	return fill(q.Window,
		func(cursor string, limit int) (store.Page[store.AuthAttempt], error) {
			return a.st.Accounting().ListAuthAttempts(ctx, store.AuthAttemptFilter{
				ListOptions: listOptions(q.SiteID, q.Search, cursor, limit),
				Outcome:     outcome,
				Source:      source,
			})
		},
		func(at store.AuthAttempt) (gen.AuthAttempt, bool) {
			return authAttemptOut(at), true
		},
	)
}

// Coverage reports whether post-auth rows actually carry the calling station.
//
// The stock FreeRADIUS post-auth query leaves the station-id columns empty even
// though the schema has them, so a stock deployment records no MAC on any attempt —
// and the MAC is the only identifier an operator has when a guest is standing in
// front of them. Foyer's generated query populates them; this measures whether the
// rows on disk actually do.
//
// Scoped by the same called-station join as the listing. A deployment-wide answer
// is honest for a single-site install and wrong for a two-venue one: a site whose devices all record MACs would
// be reported `not-recorded` because another site's do not, and the operator would
// be sent to fix a configuration that is already correct.
func (a *Adapter) Coverage(ctx context.Context, siteID string) (gen.MacCoverage, error) {
	c, err := a.st.Accounting().MacCoverage(ctx, siteID)
	if err != nil {
		return gen.MacCoverageUnknown, out(err)
	}
	return gen.MacCoverage(c), nil
}

// BlindSpots names the ways of failing to get online that leave no row at all.
//
// Two are derivable and both are derived from measurements rather than composed:
//
//   - **`unknown-radius-client`** — an Access-Request from an address in no
//     `clients.conf` entry is discarded by FreeRADIUS without a reply, so the guest
//     sees a network that does not work and Foyer sees nothing. The repository can
//     count those addresses, and the count is what makes the inner joins in
//     `accounting.go` honest rather than a silent drop.
//
//   - **`no-radius-configured`** — a site where **no registered device has a shared
//     secret**. Nothing there can complete a RADIUS exchange, so the stream is empty
//     and a console that renders the empty list without saying so is telling the
//     operator nothing went wrong.
//
//     **The count travels, because one kind is carrying two situations.** *No device
//     is registered here* is the first-run state and the answer is to register one.
//     *Devices are registered and none has a secret* is either a deliberate
//     controller-API deployment or the single most common real fault in this product
//     — a wrong or missing secret, which FreeRADIUS answers by discarding the packet,
//     leaving nothing in `radpostauth` to report. `AuthBlindSpotKind` is a closed
//     enum with no member for the first, and `params` is where a fact like this
//     belongs: `before-retention` already carries `retentionDays` there, and the
//     property is `additionalProperties: true`, so nothing in the contract changes.
//
//     The predicate is not `no enabled RADIUS client at all`, which is a different
//     question and answers a site full of secretless devices with silence — the
//     case where nobody can get online being the one it cannot see.
//
// The other three are not derived, and inventing them would be worse than omitting
// them: `shared-secret-mismatch` is a packet FreeRADIUS discarded before any row was
// written and nothing in PostgreSQL records it; `never-associated` happened
// upstream of the radio; `before-retention` needs a retention policy that does not
// exist yet.
func (a *Adapter) BlindSpots(ctx context.Context, siteID string) ([]gen.AuthBlindSpot, error) {
	spots := []gen.AuthBlindSpot{}

	devices, withSecret, err := a.st.RadiusClients().RadiusReadiness(ctx, siteID)
	if err != nil {
		return nil, out(err)
	}
	if withSecret == 0 {
		// A count that is absent is not zero, and the console is written that way:
		// it falls back to the neutral sentence unless `deviceCount` is a number.
		// So the count is always sent here, including when it is zero — that is the
		// case it exists to name.
		params := map[string]any{"deviceCount": devices}
		spots = append(spots, gen.AuthBlindSpot{Kind: gen.NoRadiusConfigured, Params: &params})
	}

	unmatched, err := a.st.Accounting().UnmatchedClientAddresses(ctx)
	if err != nil {
		return nil, out(err)
	}
	if len(unmatched) > 0 {
		params := map[string]any{"addresses": unmatched, "count": len(unmatched)}
		spots = append(spots, gen.AuthBlindSpot{Kind: gen.UnknownRadiusClient, Params: &params})
	}
	return spots, nil
}
