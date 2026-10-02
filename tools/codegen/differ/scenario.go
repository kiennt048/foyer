package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
	"github.com/kiennt048/foyer/internal/store"
)

// The scenario script.
//
// **It drives the ugly rows on purpose.** `docs/rules/frontend.md` says the
// fixtures are ugly so that a design review validates a world that exists, and
// the same argument applies twice over to a differential harness: two servers
// agree trivially on a tidy path. So the script asks for the 40-day orphan by
// id, filters for the wrapping counter, reads the controller whose credential
// has expired, and lists the vouchers in every state including the ones revoked
// mid-session.
//
// **A step may declare an expected divergence, and must give a reason.** The
// contract wins on every disagreement — a divergence is a known defect of
// `web/mock`, never an assertion loosened. Declaring it here keeps the run green
// while making the list of known mock defects impossible to lose: an undeclared
// difference fails the run, and a declared one that stops happening also fails
// it, so the list cannot rot into an excuse.

// divergence is one declared difference, and what it is allowed to cover.
//
// **Keys are the point.** A declaration naming `plan` and `voucherId` accounts for
// exactly those two fields; a wrong byte counter on the same row is still an
// undeclared difference and still fails the run. `Keys` empty means the whole
// answer, which is right for a step where the two servers disagree about the status
// itself — and wrong everywhere else, because a declaration that swallows an entire
// response stops the step detecting anything at all.
//
// The three pseudo-keys `status`, `content-type` and `location` name the parts of an
// answer that are not body fields, and are written the same way.
type divergence struct {
	Keys   []string
	Reason string
}

// What the scenario leaves behind, named once.
//
// The mutating steps at the end of the script create these four, and
// `mockAlreadyDriven` looks for them. They are constants rather than literals in
// two places because the detector is only as good as its agreement with the
// scenario: a mutation added without a marker here is a run this harness cannot
// tell it has already made.
const (
	harnessGardenValue  = "harness.example.test"
	harnessAccountName  = "harness.guest"
	harnessOperatorName = "harness.operator"
	harnessBatchLabel   = "Harness batch"
	harnessNasName      = "Harness MikroTik"
	// The address the registered device claims. `nasname` is one flat namespace
	// — FreeRADIUS matches a packet to a client by source address and nothing
	// else — so this has to miss every fixture, and the fixtures live in
	// `10.20.0.0/24`.
	harnessNasAddress = "10.20.7.7"
)

// step is one request driven into both servers.
type step struct {
	// Op is the operationId this step exercises, for the coverage report.
	Op string

	Method string
	Path   string
	Body   any

	// Volatile names response keys that cannot match between two processes.
	Volatile []string

	// Collect follows `nextCursor` to the end and compares the whole collection
	// rather than page one. See `client.collect`: a page is not comparable
	// between two servers that order differently, and the contract orders
	// nothing.
	Collect bool

	// Divergence, when set, says this step is expected to differ and why. The
	// text is the finding, and it is printed in the report whether or not the
	// run is otherwise green. It covers the whole answer.
	Divergence string

	// Declared holds key-scoped declarations that apply in **both** modes.
	// `Divergence` above covers a whole answer, which is right where the two
	// servers disagree about the status itself and wrong everywhere else.
	Declared []divergence

	// Adapter holds declarations that apply **only when the Go server is
	// `internal/apistore` over PostgreSQL**. They are separate from `Divergence`
	// because they are about a different pair: the memory mode's Go server is
	// handed the oracle's own rows and cannot disagree about them, so a difference
	// that only the adapter mode can produce is a fact about the schema or the
	// adapter and not about `web/mock`.
	Adapter []divergence

	// AdapterVolatile names keys that cannot match in the adapter mode alone —
	// an id PostgreSQL mints, for instance, where the fake reuses the oracle's.
	AdapterVolatile []string

	// AdapterSkip records a step the adapter mode does not drive, with the
	// reason. It contributes to that mode's coverage report as *not covered*.
	AdapterSkip string

	// Form encodes the body as `application/x-www-form-urlencoded` rather than
	// as JSON.
	//
	// **Three contract operations accept nothing else.** `redeemVoucher`,
	// `acceptTerms` and `loginWithAccount` declare only
	// `application/x-www-form-urlencoded`, because the portal submits them as
	// document navigations from a `<form>` with JavaScript disabled — the CNA
	// re-checks captivity only after one. A harness sending JSON to them would
	// be comparing two servers' opinions of a media type it had chosen wrongly.
	Form bool

	// Guest drives the step on the **portal** surface: the portal's own cookie,
	// no console session and no CSRF token.
	//
	// The five portal operations are `x-permission: public` with `security: []`.
	// Sending an administrator's cookie to them would compare what an operator
	// gets, and what a phone gets is the question.
	Guest bool

	// SignedIn drives the step with the admin session and the CSRF token.
	// Steps that test the absence of either set it false.
	Anonymous bool
	// NoCSRF omits the token while keeping the session.
	NoCSRF bool

	// Skip records an operation the harness deliberately does not compare, with
	// the reason. It contributes to the coverage report as *not covered*.
	Skip string
}

// declarations is what one mode has to account for on this step.
func (s step) declarations(mode string) []divergence {
	out := append([]divergence{}, s.Declared...)
	if s.Divergence != "" {
		out = append(out, divergence{Reason: s.Divergence})
	}
	if mode == modeAdapter {
		out = append(out, s.Adapter...)
	}
	return out
}

func (s step) volatileKeys(mode string) map[string]bool {
	out := map[string]bool{}
	for _, k := range s.Volatile {
		out[k] = true
	}
	if mode == modeAdapter {
		for _, k := range s.AdapterVolatile {
			out[k] = true
		}
	}
	return out
}

func (s step) skipped(mode string) string {
	if s.Skip != "" {
		return s.Skip
	}
	if mode == modeAdapter {
		return s.AdapterSkip
	}
	return ""
}

// buildScenario needs the seeded world, because a script with hardcoded ids
// would only work against one fixture snapshot — and the point of seeding from
// the mock is that the fixture world is free to change.
func buildScenario(w *memory.World) ([]step, error) {
	portalPair, err := mintPortalPair()
	if err != nil {
		return nil, fmt.Errorf("minting the portal certificate: %w", err)
	}
	eapPair, err := mintEapPair()
	if err != nil {
		return nil, fmt.Errorf("minting the EAP certificate: %w", err)
	}
	// The fixture world has no device that sends RFC 5176 itself — the one this
	// scenario registers does, and each server mints its id — so the write that
	// can be compared is the refusal for a device that does not.
	quietNas := findNonCoANas(w)
	// A site the scenario never configures phone sign-in for.
	unconfigured := ""
	if len(w.Sites) > 1 {
		unconfigured = w.Sites[1].Id
	}
	site := w.Sites[0].Id

	orphan := findSession(w, gen.SessionStateOrphaned)
	wrapping := findWrappingCounter(w)
	stale := findStalest(w)
	expiredCred := findController(w, gen.NasReachabilityCredentialsExpired)
	unreachable := findNas(w, gen.NasReachabilityUnreachable)
	refusing := findRefusingNas(w)
	revoked := findVoucher(w, gen.VoucherStateRevoked)
	exhausted := findVoucher(w, gen.VoucherStateExhausted)
	expired := findVoucher(w, gen.VoucherStateExpired)
	inUse := findVoucher(w, gen.VoucherStateInUse)

	steps := []step{
		// ── the two ways in ──────────────────────────────────────────────
		{
			Op: "GetAuthSession", Method: http.MethodGet, Path: "/auth/session",
			Volatile: []string{"csrfToken"},
		},
		{
			Op: "SignIn", Method: http.MethodPost, Path: "/auth/session",
			Body:     map[string]string{"username": adminUsername(w), "password": harnessPassword},
			Volatile: []string{"csrfToken"}, Anonymous: true,
		},
		// **No declaration sits here, and its absence is the assertion.** A mock
		// whose `beginAdminSession` matched on username and `enabled` and accepted
		// any non-empty password would answer this step 200 on one side and 401 on
		// the other. Both verify, both sides answer 401, and a declaration left
		// behind would fail the run as one that stopped happening.
		{
			Op: "SignIn", Method: http.MethodPost, Path: "/auth/session",
			Body:      map[string]string{"username": adminUsername(w), "password": "this-is-not-the-password"},
			Anonymous: true,
		},
		{
			Op: "SignIn", Method: http.MethodPost, Path: "/auth/session",
			Body: map[string]string{"username": "", "password": ""}, Anonymous: true,
		},
		// The first-access setup. Every world here is an install whose setup was
		// completed long ago — nobody opened one — so both servers answer
		// `required: false`, and `409 setup-complete` whatever the body says. The
		// open-setup path is `first-run.test.ts` on the mock and `setup_test.go` on
		// Go, against worlds that have one.
		{Op: "GetAdminSetup", Method: http.MethodGet, Path: "/auth/setup", Anonymous: true},
		{
			Op: "CompleteAdminSetup", Method: http.MethodPost, Path: "/auth/setup",
			Body: map[string]string{
				"setupCode": "0000-0000-0000-0000", "username": "someone", "password": "long-enough-password",
			},
			Anonymous: true,
		},

		// ── reads, over the ugly rows ────────────────────────────────────
		{Op: "ListSites", Method: http.MethodGet, Path: "/sites"},
		{
			Op: "GetOverview", Method: http.MethodGet, Path: "/overview?siteId=" + site,
			Adapter: []divergence{{
				Keys: []string{"alarmCount", "value", "nasUnhealthy", "orphanedSessions"},
				Reason: "**these are the first derivations this harness has ever compared.** The memory " +
					"mode replays the figures it read out of the oracle, so `getOverview` was a " +
					"transport check there and says so in the README; here both servers compute them " +
					"from rows. Every difference below is downstream of a rule the two apply " +
					"differently — the `orphaned` threshold (see listSessions), the vendor-site " +
					"binding the schema refuses (see listControllers), and the window " +
					"`vouchersRedeemedToday` counts over. The figures are printed rather than " +
					"summarised, because a declaration whose numbers nobody sees is a check that " +
					"looks like one and is not.",
			}},
		},
		{
			Op: "ListAlarms", Method: http.MethodGet, Path: "/alarms?siteId=" + site,
			Adapter: []divergence{{
				Keys: []string{"body"},
				Reason: "the alarm set is derived, and the two servers raise different ones: the " +
					"oracle raises one per fixture condition, the adapter raises what its own rows " +
					"support. Same cause as `getOverview` above. `body` names the whole document " +
					"because this operation answers a bare array rather than a `Page`.",
			}},
		},
		// Three controllers bind a vendor site to one Foyer site in the oracle's
		// world, and migration `00011` makes that storable: every resolution path
		// goes through the device, so the ambiguity that is real is two vendor sites
		// *of one controller*, and the index is `(controller_id, bound_site_id)`
		// rather than `UNIQUE (bound_site_id)`. The world stores as written and the
		// two servers agree.
		{Op: "ListControllers", Method: http.MethodGet, Path: "/controllers?siteId=" + site},
		{
			Op: "ListNas", Method: http.MethodGet, Path: "/nas?siteId=" + site + "&limit=200", Collect: true,
			Adapter: nasShapeDivergences(w),
		},
		{
			Op: "ListNas", Method: http.MethodGet, Path: "/nas?siteId=" + site + "&limit=2",
			AdapterSkip: "a page is a different two devices on each side, because the two servers " +
				"order the list differently and the contract orders nothing. The whole collection " +
				"is compared by the `limit=200` step above; comparing page one would be comparing " +
				"the orders, which is reported once instead",
		},
		{
			Op: "ListNas", Method: http.MethodGet, Path: "/nas?siteId=" + site + "&limit=2&cursor=2",
			AdapterSkip: "the cursor is opaque by contract and different by construction — a decimal " +
				"offset over a slice against a repository keyset — so `cursor=2` is the oracle's " +
				"own encoding and means nothing to the adapter",
		},
		{Op: "ListNas", Method: http.MethodGet, Path: "/nas?siteId=" + site + "&q=unifi"},
		{Op: "ListVoucherBatches", Method: http.MethodGet, Path: "/voucher-batches?siteId=" + site + "&limit=200", Collect: true},
		{Op: "ListVouchers", Method: http.MethodGet, Path: "/vouchers?siteId=" + site + "&limit=200", Collect: true},
		{
			Op: "ListSessions", Method: http.MethodGet, Path: "/sessions?siteId=" + site + "&limit=200", Collect: true,
			Adapter: append(sessionIdentityDivergences(w), sessionShapeDivergences(w)...),
		},
		{
			// **The evidence panel, and it is a read with no state behind it.**
			// `getNasEvidence` answers what the driver knows about itself —
			// capability evidence and wire-attribute evidence — so it is the same
			// answer on every call and the only thing that can differ is what each
			// server thinks the device's driver is. Left out of the scenario, it
			// would print `NO REASON RECORDED` in the coverage report: a gap with no
			// sentence at all rather than one that expired.
			Op: "GetNasEvidence", Method: http.MethodGet,
			Path: "/nas/" + firstNasID(w) + "/evidence",
		},
		{Op: "ListAccounts", Method: http.MethodGet, Path: "/accounts?siteId=" + site + "&limit=200", Collect: true},
		{Op: "ListOperators", Method: http.MethodGet, Path: "/operators?limit=200", Collect: true},
		{Op: "ListAudit", Method: http.MethodGet, Path: "/audit?limit=200", Collect: true},
		{
			Op: "ListAuthAttempts", Method: http.MethodGet,
			Path: "/auth-attempts?siteId=" + site + "&limit=200", Collect: true,
			Adapter: nonRadiusAttemptDivergence(w),
		},
		// **`budgets` carries no declaration on this step, because the two servers
		// agree on it.**
		//
		// `PortalSettings.budgets` and the logo's ceiling live in `internal/api`, not
		// in an adapter: the contract declares all three figures
		// (`x-max-decoded-bytes` beside the two fields they bound, `x-budget-bytes`
		// beside `codeBudgetBytes`) and the server reads them out of the document it
		// was generated from, so the memory fake and `internal/apistore` answer
		// identically without either storing a byte count.
		//
		// A declaration left standing would fail this harness with the same exit
		// code as a real drift — the false positive `testing.md` records, where a
		// report written against a defect calls its repair a regression.
		{Op: "GetPortalSettings", Method: http.MethodGet, Path: "/sites/" + site + "/portal"},
		{Op: "GetWalledGarden", Method: http.MethodGet, Path: "/sites/" + site + "/walled-garden"},
	}
	// Here, among the reads and before the first mutation — see reportReadSteps.
	steps = append(steps, reportReadSteps(w, site)...)

	// A session with no `Acct-Stop` that has been online for forty days. It is
	// the case the whole freshness apparatus exists for: `orphaned` is not
	// `online`, and a server that reported it as either `online` or `ended`
	// would be guessing.
	if orphan != "" {
		steps = append(steps,
			step{
				Op: "GetSession", Method: http.MethodGet, Path: "/sessions/" + orphan,
				Adapter: sessionIdentityDivergences(w),
			},
			step{
				Op: "ListSessions", Method: http.MethodGet, Path: "/sessions?siteId=" + site + "&state=orphaned",
				Adapter: []divergence{{
					Keys:   []string{"items"},
					Reason: orphanRuleReason,
				}},
			},
		)
	}
	// The wrapping byte counter. Nothing here subtracts two counters, and the
	// row travels so that the first figure anyone adds has something to be
	// visibly wrong about.
	if wrapping != "" {
		steps = append(steps, step{
			Op: "GetSession", Method: http.MethodGet, Path: "/sessions/" + wrapping,
			// The identity declarations only: the counters themselves are compared
			// exactly, which is the point of driving this row at all.
			Adapter: []divergence{sessionIdentityDivergences(w)[0]},
		})
	}
	// Twenty-minute-stale interim updates: the figure and its freshness travel
	// together or the console is asserting a number nothing on the network said.
	if stale != "" {
		steps = append(steps, step{
			Op: "GetSession", Method: http.MethodGet, Path: "/sessions/" + stale,
			Adapter: sessionIdentityDivergences(w),
		})
	}
	if expiredCred != "" {
		steps = append(steps,
			// Its binding was one of the two the old index refused; migration `00011`
			// holds it, and `boundSiteId` now matches. See `listControllers` above.
			step{Op: "GetController", Method: http.MethodGet, Path: "/controllers/" + expiredCred},
			step{
				Op: "ProbeController", Method: http.MethodPost, Path: "/controllers/" + expiredCred + "/probe",
				AdapterSkip: "a probe result is what a driver observed, and this harness has no " +
					"controller. The oracle replays a fixture; the adapter runs a fake driver that " +
					"is labelled one. Comparing them would be comparing two inventions, and " +
					"replaying the oracle's answers through Go would be inventing evidence about " +
					"hardware — which `docs/rules/nas-drivers.md` names as the exact failure of " +
					"a hand-written NAS mock",
			},
			// Discovery from a controller nothing can read is refused rather
			// than answered with an empty result — an empty read is what marks
			// every device missing.
			// **This one agrees, and it is the step worth saying so about.**
			// Discovery from a controller nothing can read is refused rather than
			// answered with an empty result — an empty read is what marks every
			// device missing — and both servers refuse it with the same problem
			// type, the same status and the same reachability. Ordering matters: a
			// `probeController` run ahead of it would write `unreachable` back onto
			// the controller first and make `params.reachability` differ.
			step{Op: "DiscoverFromController", Method: http.MethodPost,
				Path: "/controllers/" + expiredCred + "/discover", Body: map[string]any{}},
		)
	} else {
		// **A conditional step is a step that can leave an operation in neither
		// list**, the unaccounted-operation gap arriving through the back door: the
		// coverage report would print `NO REASON RECORDED` for an operation that
		// is scripted, purely because the world of the day did not carry the row
		// it needs. So each conditional branch states its condition, and
		// `validateSkips` requires one.
		const noExpired = "no controller in the oracle's world reports " +
			"`credentials-expired`, and both of these are about the answer a controller " +
			"nothing can read gives. Evaluated per run by `findController`"
		steps = append(steps,
			step{Op: "GetController", Skip: noExpired},
			step{Op: "ProbeController", Skip: noExpired},
			step{Op: "DiscoverFromController", Skip: noExpired},
		)
	}
	if unreachable != "" {
		steps = append(steps,
			step{Op: "GetNas", Method: http.MethodGet, Path: "/nas/" + unreachable},
			step{
				Op: "ProbeNas", Method: http.MethodPost, Path: "/nas/" + unreachable + "/probe",
				AdapterSkip: "the same reason as `probeController`: the checks a probe returns are " +
					"what a driver measured, and neither side measured anything here",
			},
		)
	} else {
		const noUnreachable = "no device in the oracle's world reports `unreachable`, and both " +
			"of these are about what a device nothing can reach answers. Evaluated per run by " +
			"`findNas`"
		steps = append(steps,
			step{Op: "GetNas", Skip: noUnreachable},
			step{Op: "ProbeNas", Skip: noUnreachable},
		)
	}
	// The device that refuses `require_message_authenticator` — BlastRADIUS,
	// CVE-2024-3596. A server that reported this as `unknown` would turn a
	// per-NAS warning into silence.
	if refusing != "" {
		steps = append(steps, step{
			Op: "GetNas", Method: http.MethodGet, Path: "/nas/" + refusing,
			Adapter: apLessDivergence(w),
		})
	}
	for label, id := range map[string]string{
		"revoked": revoked, "exhausted": exhausted, "expired": expired, "in-use": inUse,
	} {
		if id == "" {
			continue
		}
		steps = append(steps, step{
			Op: "ListVouchers", Method: http.MethodGet,
			Path: "/vouchers?siteId=" + site + "&state=" + label + "&limit=200",
		})
	}

	// ── refusals ─────────────────────────────────────────────────────────
	steps = append(steps,
		step{Op: "GetNas", Method: http.MethodGet, Path: "/nas/nas-does-not-exist"},
		step{Op: "GetController", Method: http.MethodGet, Path: "/controllers/ctl-does-not-exist"},
		step{Op: "GetSession", Method: http.MethodGet, Path: "/sessions/sess-does-not-exist"},
		step{Op: "GetVoucherBatch", Method: http.MethodGet, Path: "/voucher-batches/batch-does-not-exist"},
		step{
			Op: "CreateGardenEntry", Method: http.MethodPost,
			Path: "/sites/" + site + "/walled-garden",
			Body: map[string]any{"kind": "fqdn", "value": "not a hostname at all"},
		},
		step{
			Op: "CreateNas", Method: http.MethodPost, Path: "/nas",
			Body: map[string]any{"name": "Typed by hand", "siteId": site, "driver": "unifi"},
		},
	)

	// A required garden entry cannot be deleted: it is the portal's own FQDN or
	// its OCSP/CRL endpoint, and removing one presents to every guest as a
	// network that is simply broken.
	if required := findRequiredGardenEntry(w, site); required != "" {
		steps = append(steps, step{
			Op: "DeleteGardenEntry", Method: http.MethodDelete,
			Path: "/sites/" + site + "/walled-garden/" + required,
		})
	} else {
		steps = append(steps, step{
			Op: "DeleteGardenEntry",
			Skip: "this site's walled garden holds no `required` entry, and the refusal is the " +
				"only branch of this operation worth comparing — deleting an optional entry " +
				"would remove a row every later garden comparison reads. Evaluated per run by " +
				"`findRequiredGardenEntry`",
		})
	}

	// ── mutations, last, because they move both worlds ───────────────────
	steps = append(steps,
		step{
			Op: "CreateGardenEntry", Method: http.MethodPost,
			Path:     "/sites/" + site + "/walled-garden",
			Body:     map[string]any{"kind": "fqdn", "value": harnessGardenValue},
			Volatile: []string{"id"},
		},
		step{
			Op: "CreateAccount", Method: http.MethodPost, Path: "/accounts",
			Body:     map[string]any{"username": harnessAccountName, "password": harnessPassword, "siteId": site},
			Volatile: []string{"id"},
			Declared: []divergence{{
				// Four keys because the two modes disagree in different shapes: the
				// fake omits `plan` altogether and the adapter answers an empty one,
				// so the difference lands on the property in one mode and inside it
				// in the other. Naming both is what keeps the declaration true of
				// the run in front of it rather than of the last one.
				Keys: []string{"plan", "name", "nameLang", "dataAllowanceMb"},
				Reason: "the request carries no plan and the servers fill one differently: the " +
					"oracle hands the new account the site's own default — \"Nhân viên\", 10 GB — " +
					"and the Go side stores the empty plan it was given. **The contract does not " +
					"say a site has a default plan**, and `AccountCreate.plan` is optional with " +
					"nothing said about what happens when it is absent. Known contract gap: " +
					"a guest account created with no plan is either unenforced or inherits " +
					"something, and an operator cannot tell which from the screen. It is visible " +
					"only against a freshly started mock — a second run answers 409 for the " +
					"username the first one created, which is why `make diff-api` starts its own.",
			}},
		},
		step{
			Op: "CreateVoucherBatch", Method: http.MethodPost, Path: "/voucher-batches",
			Body: map[string]any{
				"label": harnessBatchLabel, "siteId": site, "count": 3,
				"expiryMode": "from-first-use",
				"plan":       map[string]any{"durationMinutes": 60},
			},
			Volatile: []string{"id"},
			Divergence: "`createdBy` — the mock writes the literal \"quản trị\", a Vietnamese role noun " +
				"hardcoded in server.ts; the Go server writes the signed-in operator's username. The " +
				"contract types the field `{ type: string }` and says nothing more, so this is a judgement " +
				"rather than a rule — but it is the same judgement the contract makes everywhere it does " +
				"speak: `AuditKind`, `AlarmKind` and `ProblemType` all exist because a server composing " +
				"text in its own language greeted an English-only console in Vietnamese. A username is an " +
				"identifier and needs no translation; a role noun is a sentence fragment and does. Known " +
				"contract gap — the field wants a description saying which it is.",
		},
		// Before the registration below: the device it creates speaks CoA, and its
		// id is minted by each server, so the list would line two different ids up.
		step{Op: "ListCoaTiming", Method: http.MethodGet, Path: "/network/coa-timing",
			Declared: []divergence{{
				Keys: []string{"nextCursor"},
				Reason: "not a page: `listCoaTiming` answers `{items, defaults}` — every " +
					"CoA-capable device, bounded by the device table — and its schema has no " +
					"cursor. `requiredNullables` reads any answer with `items` as a `Page`.",
			}}},
		// **A registration that succeeds.**
		//
		// `CreateNas` would be in the coverage report as covered on the strength of
		// the 422 above alone — a `unifi` refused for needing a controller. Then the
		// only branch of the operation that *writes* a device would never run on
		// either side, and every field a registration decides would be outside this
		// harness while the report said the operation was compared. Operation-level
		// coverage cannot see that; it is the same shape as a green suite over a
		// branch that has never executed.
		//
		// **`coaPort` is the field this exists for.** No port is sent, exactly as the
		// registration wizard sends none, so both servers are asked what an
		// unspecified CoA port becomes. `internal/apistore` stores the operator's
		// value or `nil`, and so does `web/mock`; `dropNulls` makes that agreement
		// mutual rather than a matched guess — a mock that filled in 3799 from a
		// default table of its own would keep a key Go does not have, which is
		// reported as a difference naming the field and the value.
		//
		// `mikrotik` and not `coa`: the driver that needs no `coaVendor`, so the step
		// stays about the port. The refusal for a missing vendor has its own drive.
		step{
			Op: "CreateNas", Method: http.MethodPost, Path: "/nas",
			Body: map[string]any{
				"siteId": site, "name": harnessNasName,
				"driver": "mikrotik", "ipAddress": harnessNasAddress,
			},
			// `id` and `sharedSecretId` because the two servers mint their own;
			// `sharedSecret` because it is freshly generated and is the one field that
			// must never be equal.
			//
			// No divergence is declared. `accountingConfigured` is absent on a device
			// nothing has probed — absence carries the third state on both servers,
			// never `false`. `sharedSecretId` is in `Volatile` rather than ignored:
			// the two servers agree that the property is *there* beside a freshly
			// generated secret, and disagree about the id because each mints its own.
			// Comparing presence and ignoring the value is what says so.
			//
			// A declaration whose divergence stopped happening is not left standing.
			// The harness fails a declared divergence that no longer fires, with the
			// same exit code as a real drift.
			Volatile: []string{"id", "sharedSecret", "sharedSecretId"},
		},
		step{
			Op: "CreateOperator", Method: http.MethodPost, Path: "/operators",
			Body: map[string]any{
				"username": harnessOperatorName, "password": harnessPassword, "role": "staff",
			},
			Volatile: []string{"id"},
		},
	)
	if unreachable != "" {
		// Rotation refused: the driver cannot push and nothing can reach the
		// device, which is the one combination with no route back.
		steps = append(steps, step{
			Op: "RotateNasSecret", Method: http.MethodPost, Path: "/nas/" + unreachable + "/secret",
			Volatile: []string{"sharedSecret"},
			Adapter: []divergence{{
				Reason: "the Go server refuses with `409` and the oracle rotates with `200`. **The " +
					"refusal is correct and the oracle does not model it**: the driver cannot push " +
					"a secret to this device and nothing can reach it, which is the one " +
					"combination with no route back — rotating would take it off the air until " +
					"somebody stands in front of it. Neither condition refuses on its own, which " +
					"is the narrow reading `docs/rules/nas-drivers.md` records. Known limitation of " +
					"`web/mock`. The question is routed through the driver rather than the stored " +
					"`capabilities` column — a device whose recorded capability claimed a push " +
					"would otherwise reach a function that always fails and answer `500` — and " +
					"this harness's own fake does not claim `RotateSecret: true`, which no " +
					"shipped driver reports.",
			}},
		})
	}
	if inUse != "" {
		// Revoking a voucher that is in use does not by itself end the session;
		// the response reports what actually happened.
		steps = append(steps, step{
			Op: "RevokeVoucher", Method: http.MethodPost, Path: "/vouchers/" + inUse + "/revoke",
			Adapter: unrecordedVoucherSessionDivergence(w, inUse),
		})
	} else {
		steps = append(steps, step{
			Op: "RevokeVoucher",
			Skip: "no voucher in the oracle's world is `in-use`, and revoking one that is not is " +
				"the uninteresting half: the case worth comparing is whether revocation ends " +
				"the session the voucher is currently carrying. Evaluated per run by " +
				"`findVoucher`",
		})
	}

	// ── disconnectSession: the one action on a session ───────────────────
	//
	// Three answers, each drawn from the world rather than typed: a session that
	// has ended (409 session-not-live), one on a device whose capability says it
	// cannot (409 disconnect-unsupported), and one that can. No withdrawal is asked
	// for on the success step, so it changes the session and nothing a later step
	// reads about vouchers or grants.
	steps = append(steps, disconnectSteps(w, inUse)...)

	// ── denial, last, because the mock does not deny ─────────────────────
	//
	// These two run at the end on purpose. `web/mock` has no session gate and no
	// CSRF check, so it *performs* the request the Go server refuses — the
	// unauthenticated read is harmless, but the refused rotation succeeds on the
	// mock and clears that device's `secretLastUsedAt`. Driving them earlier
	// made every later comparison of that row disagree, and the disagreement
	// looked like a Go defect rather than a side effect of the oracle's own gap.
	// Ordering is part of the scenario, not an accident of it.
	steps = append(steps,
		step{
			Op: "ListNas", Method: http.MethodGet, Path: "/nas?siteId=" + site,
			Anonymous: true,
			Divergence: "web/mock sets `REQUIRE_SESSION = false`, so an unauthenticated request reaches " +
				"every handler. The Go server answers 401 with /problems/not-signed-in, which is what the " +
				"`sessionCookie` scheme describes. This is the defect the rewrite exists to remove.",
		},
		step{
			Op: "RotateNasSecret", Method: http.MethodPost,
			Path: "/nas/" + firstNasID(w) + "/secret", NoCSRF: true,
			Volatile: []string{"sharedSecret"},
			Divergence: "the mock issues a CSRF token and never checks it — its own comment says the check " +
				"is the Go binary's job. The contract's `csrfToken` scheme requires the header on every " +
				"state-changing request outside the `portal` tag, so the Go server answers 403.",
		},
	)

	steps = append(steps, guestSteps(w)...)

	// After the guest steps on purpose: they are what make grants exist on both
	// sides, and a guest export compared over two empty lists agrees for the wrong
	// reason.
	steps = append(steps, reportGuestSteps(site)...)
	// ── phone sign-in ────────────────────────────────────────────────────
	//
	// Unconfigured first, so the two guest operations are compared on their
	// refusal: `404 /problems/method-not-offered`, the answer a venue without a
	// provider gives. The send itself is not driven: it would reach the site's
	// provider, and neither server can be handed a provider fake from here —
	// `internal/api/phone_test.go` and `web/mock/src/phone.test.ts` drive it
	// against one each. Then the operator configures it and the read-back is
	// compared, which is the half that carries the write-only keys.
	steps = append(steps,
		step{Op: "GetPhoneSignIn", Method: http.MethodGet, Path: "/sites/" + site + "/portal/phone"},
		step{
			Op: "RequestPhoneCode", Method: http.MethodPost, Path: "/portal/phone",
			Form: true, Guest: true, Body: map[string]string{"phone": "0912345678", "lang": "en"},
		},
		step{
			Op: "VerifyPhoneCode", Method: http.MethodPost, Path: "/portal/phone/code",
			Form: true, Guest: true, Body: map[string]string{"code": "123456", "lang": "en"},
		},
		step{
			Op: "UpdatePhoneSignIn", Method: http.MethodPut, Path: "/sites/" + site + "/portal/phone",
			Body: map[string]any{
				"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 200, "sessionMinutes": 60,
				"messageTemplate": "{code} la ma dang nhap Wi-Fi",
				// Harness values, not credentials: nothing anywhere accepts them.
				"apiKey": "differ-api-key", "secretKey": "differ-secret-key",
			},
		},
		step{Op: "GetPhoneSignIn", Method: http.MethodGet, Path: "/sites/" + site + "/portal/phone"},
	)

	// ── createSite, which a fresh install stands on ──────────────────────
	//
	// **This operation must be in one of the two lists.** Unscripted and with no
	// `Skip`, the coverage report would print `CreateSite — NO REASON RECORDED` —
	// the same species as an expired skip, one step earlier: a gap with no
	// sentence at all rather than a sentence that had stopped being true.
	//
	// Both servers mint their own ids, and **it does not matter**, because neither
	// server reaches the mint: an install holds one venue, every world this
	// harness runs against already holds a site, and both sides refuse the second
	// call. The comparison
	// is therefore of the *refusal*, which is the branch a real deployment hits on
	// every call after the first and the one with a problem type on it.
	//
	// `201` stays uncompared and says why, in a reason a run can evaluate: it
	// needs a world with no sites, and `web/mock` has one behind `FOYER_MOCK_FRESH`
	// — but a differ started against it could seed nothing and compare nothing else.
	steps = append(steps,
		step{
			Op: "CreateSite", Method: http.MethodPost, Path: "/sites",
			Body: map[string]any{"name": "Harness venue"},
		},
		step{
			Op: "CreateSite", Method: http.MethodPost, Path: "/sites",
			Body: map[string]any{"name": "   "},
			// A blank name after trimming. `sites.name` is `text NOT NULL` and ""
			// satisfies it, so the refusal is the handler's rather than the
			// schema's — and a venue with no name renders a gap in the site
			// switcher on the first screen an operator sees.
		},
	)

	// ── what this harness does not compare ───────────────────────────────
	//
	// Listed as steps so the coverage report counts them as gaps rather than
	// leaving them out of the denominator. A silent gap reads as "covered
	// everything", which is the failure mode this list exists against.
	//
	// **Every reason below names a condition, not a phase.** `validateSkips`
	// enforces the half of that a program can check; the rest is on the reader.
	// A skip that says "not implemented in Go — phase 4" goes on saying it after
	// the implementation lands, which is what `docs/rules/testing.md` ("A skip
	// whose reason expires") describes: coverage that removed itself and left a
	// note, and the note outlived the fact.
	steps = append(steps,
		step{Op: "SignOut", Skip: "would end the session the rest of the scenario depends on; " +
			"covered by internal/api's own suite instead"},
		step{Op: "ChangeOwnPassword", Skip: "would change the password the harness signs in with; " +
			"covered by internal/api's own suite instead"},
		step{Op: "CreateController", Skip: "the mock's controller ids embed Date.now(), and a created " +
			"controller is then read back by id in later steps — comparable only by blanking the id, " +
			"which would stop comparing the thing that matters"},
		step{Op: "UpdateController", Skip: "depends on CreateController"},
		step{Op: "DeleteController", Skip: "depends on CreateController"},
		step{Op: "UpdateNas", Skip: "no fixture NAS is safe to rename mid-scenario without moving " +
			"every later list comparison"},
		step{Op: "DeleteNas", Skip: "removing a fixture device would change every session and alarm " +
			"that references it"},
		step{Op: "UpdateAccount", Skip: "not yet scripted"},
		step{Op: "DeleteAccount", Skip: "not yet scripted"},
		step{Op: "UpdateOperator", Skip: "not yet scripted"},
		step{Op: "DeleteOperator", Skip: "not yet scripted"},
		step{Op: "UpdatePortalSettings", Skip: "not yet scripted"},
		step{Op: "PutPortalHeroImage", Skip: "the mock stores the image under a URL containing " +
			"Date.now(); comparable only by blanking the URL"},
		step{Op: "DeletePortalHeroImage", Skip: "depends on PutPortalHeroImage"},

		// Privacy: retention and erasure. The read compares defaults and bounds
		// across all three implementations; `lastSweep` is volatile because only a
		// running scheduler writes it and neither harness runs one.
		step{Op: "GetRetentionPolicy", Method: http.MethodGet, Path: "/privacy/retention",
			Volatile: []string{"lastSweep"}},
		step{Op: "UpdateRetentionPolicy", Method: http.MethodPut, Path: "/privacy/retention",
			Body: map[string]any{"changes": []map[string]any{
				{"category": "accounting", "days": 60},
				{"category": "audit", "days": nil},
			}},
			Volatile: []string{"lastSweep"}},
		// A MAC that no fixture holds, so the erasure removes nothing in any world
		// and the receipt -- every table searched, all zero, the subject masked --
		// is comparable. The erasure itself is proven against PostgreSQL in
		// internal/store/privacy_test.go.
		step{Op: "EraseDataSubject", Method: http.MethodPost, Path: "/privacy/erasures",
			Body:     map[string]any{"subject": map[string]any{"kind": "mac", "mac": "02-00-5E-10-00-99"}},
			Volatile: []string{"id", "at"}},

		// Guest data and marketing consent. The setting is
		// compared before and after a change — including each text's SHA-256, which
		// both servers compute — and the consent list and export are compared empty:
		// no scripted sign-in ticks the box, and the consent rule itself is proven in
		// internal/api (handlers) and internal/store (PostgreSQL).
		step{Op: "GetGuestDataSettings", Method: http.MethodGet, Path: "/sites/" + site + "/guest-data"},
		step{Op: "UpdateGuestDataSettings", Method: http.MethodPut, Path: "/sites/" + site + "/guest-data",
			Body: map[string]any{
				"fields": []map[string]any{
					{"field": "name", "required": false},
					{"field": "email", "required": true},
				},
				"marketingConsent": map[string]any{
					"enabled": true,
					"text": map[string]any{
						"en": "Send me offers from this venue by e-mail.",
						"vi": "Gửi cho tôi ưu đãi của quán qua email.",
					},
				},
			}},
		step{Op: "ListMarketingConsents", Method: http.MethodGet, Path: "/sites/" + site + "/marketing-consents"},
		step{Op: "ExportMarketingConsents", Method: http.MethodGet,
			Path: "/exports/marketing-consents?" + reportWindow(site, time.Now().Truncate(time.Second), 30)},
		step{Op: "WithdrawMarketingConsent", Skip: "no consent exists in either world to withdraw: " +
			"no scripted portal sign-in ticks the marketing box"},
		step{Op: "WithdrawOwnMarketingConsent", Skip: "needs a portal session with a verified MAC and " +
			"a consent behind it, which no scripted portal flow creates"},

		// Every setting an operator makes in the console — network, RADIUS server and
		// phone sign-in — read and WRITTEN. Every write is compared; a write that
		// changes something the rest of the run depends on is put back by the step
		// after it, and the harness mints the certificates it uploads once and sends
		// both servers the same bytes (certs.go).
		step{Op: "GetNetworkSettings", Method: http.MethodGet, Path: "/network/settings",
			Declared: []divergence{portalURLDerived}},
		// An https portal address first, so the certificate below covers the address
		// both servers hold; then the shipped throttles and ceilings, so the servers
		// the rest of the run is driven through are exactly as they were.
		step{Op: "UpdateNetworkSettings", Method: http.MethodPut, Path: "/network/settings",
			Body: map[string]any{"portalUrl": "https://" + harnessPortalHost + "/", "venueUrl": nil,
				"throttles": api.DefaultThrottles(), "bodyCeilings": api.DefaultBodyCeilings()}},
		step{Op: "PutPortalCertificate", Method: http.MethodPut, Path: "/network/portal-certificate",
			Body:     map[string]any{"certificatePem": portalPair.certificate, "privateKeyPem": portalPair.privateKey},
			Volatile: []string{"updatedAt"}},
		step{Op: "DeletePortalCertificate", Method: http.MethodDelete, Path: "/network/portal-certificate"},
		step{Op: "UpdateNetworkSettings", Method: http.MethodPut, Path: "/network/settings",
			Body: map[string]any{"portalUrl": nil, "venueUrl": nil,
				"throttles": api.DefaultThrottles(), "bodyCeilings": api.DefaultBodyCeilings()},
			Declared: []divergence{portalURLDerived}},
		// `lastSync` is each server's own history: the adapter's seed creates the
		// fixture devices through the store, which syncs, and the mock's world is
		// loaded rather than created and syncs on the scenario's secret rotation.
		step{Op: "GetRadiusServerSettings", Method: http.MethodGet, Path: "/network/radius",
			Volatile: []string{"lastSync"},
			Declared: []divergence{radiusApplyAvailable}},
		step{Op: "UpdateRadiusServerSettings", Method: http.MethodPut, Path: "/network/radius",
			Body:     map[string]any{"maxServers": api.DefaultRadiusMaxServers + 2, "eapTlsEnabled": false},
			Volatile: []string{"lastSync"}, Declared: []divergence{radiusApplyAvailable}},
		step{Op: "PutEapCertificate", Method: http.MethodPut, Path: "/network/radius/eap-certificate",
			Body: map[string]any{"certificatePem": eapPair.certificate, "privateKeyPem": eapPair.privateKey,
				"caPem": eapPair.ca},
			Volatile: []string{"lastSync", "updatedAt"}, Declared: []divergence{radiusApplyAvailable}},
		step{Op: "UpdateRadiusServerSettings", Method: http.MethodPut, Path: "/network/radius",
			Body:     map[string]any{"maxServers": api.DefaultRadiusMaxServers, "eapTlsEnabled": false},
			Volatile: []string{"lastSync", "updatedAt"}, Declared: []divergence{radiusApplyAvailable}},
		// A manual sync: its time is the run's, and `removed` names client-table ids
		// each world mints for itself. The fixture device with no secret is skipped
		// by both, under an address only the adapter's seed has.
		step{Op: "SyncRadiusClients", Method: http.MethodPost, Path: "/network/radius/sync",
			Volatile: []string{"at", "removed"},
			Declared: []divergence{{
				Keys: []string{"nasName"},
				Reason: "the skipped device has no address in the contract's world (`Nas` carries " +
					"none and the fixture's `ipAddress` is null); the adapter's seed invents one " +
					"from TEST-NET-2 (`pgseed.addressFor`) and the mock names the device instead.",
			}}},
		step{Op: "ApplyRadiusConfiguration", Skip: "needs FreeRADIUS and the supervisor in its image " +
			"to answer; the Go world runs neither (measured in the lab instead)"},
		step{Op: "UpdateNasCoaTiming", Method: http.MethodPut, Path: "/nas/" + quietNas + "/coa-timing",
			Body: map[string]any{"timeoutMs": 3000, "retries": 2}},
		step{Op: "UpdateNasCoaTiming", Method: http.MethodPut, Path: "/nas/nas-nobody/coa-timing",
			Body: map[string]any{"timeoutMs": 3000, "retries": 2}},
		step{Op: "ListPortalGrants", Skip: "which grants are open is derived from each world's own " +
			"clock and from grants each world's portal steps made with its own ids and times; the " +
			"adapter's list is asserted in internal/apistore/portalgrants_open_test.go"},
		step{Op: "DisconnectPortalGrant", Method: http.MethodPost,
			Path: "/portal-grants/grant-does-not-exist/disconnect", Body: map[string]any{}},
		step{Op: "GetSiteTimeZone", Method: http.MethodGet, Path: "/sites/" + site + "/time-zone"},
		// Storing the zone the site already has is not a change: `updatedAt` stays
		// null on both. Then a real change and back; its instant is the run's.
		step{Op: "UpdateSiteTimeZone", Method: http.MethodPut, Path: "/sites/" + site + "/time-zone",
			Body: map[string]any{"timeZone": "Asia/Ho_Chi_Minh"}},
		step{Op: "UpdateSiteTimeZone", Method: http.MethodPut, Path: "/sites/" + site + "/time-zone",
			Body: map[string]any{"timeZone": "Asia/Bangkok"}, Volatile: []string{"updatedAt"}},
		step{Op: "UpdateSiteTimeZone", Method: http.MethodPut, Path: "/sites/" + site + "/time-zone",
			Body: map[string]any{"timeZone": "Asia/Ho_Chi_Minh"}, Volatile: []string{"updatedAt"}},
		step{Op: "GetPhoneAbuseControls", Method: http.MethodGet,
			Path: "/sites/" + site + "/portal/phone/abuse-controls"},
		step{Op: "UpdatePhoneAbuseControls", Method: http.MethodPut,
			Path: "/sites/" + site + "/portal/phone/abuse-controls",
			Body: map[string]any{
				"limits": map[string]any{"perNumberPerHour": 2, "perNumberPerDay": 4, "perDevicePerHour": 4,
					"perAddressPerHour": 200, "codeMinutes": 5, "maxAttempts": 5},
				"mobilePrefixes": []string{"03", "09"},
			},
			Volatile: []string{"updatedAt"}},
		step{Op: "GetPhoneSpendCap", Method: http.MethodGet, Path: "/phone-sign-in/spend-cap"},
		step{Op: "UpdatePhoneSpendCap", Method: http.MethodPut, Path: "/phone-sign-in/spend-cap",
			Body: map[string]any{"dailyLimit": 500}, Volatile: []string{"updatedAt"}},
		step{Op: "UpdatePhoneSpendCap", Method: http.MethodPut, Path: "/phone-sign-in/spend-cap",
			Body: map[string]any{"dailyLimit": nil}, Volatile: []string{"updatedAt"}},
		// At a site with NO phone sign-in configured, so both servers answer
		// `not-configured` and nothing is sent. Never at `site`: the scenario gave it
		// an eSMS configuration above, and the Go server's provider is the real one —
		// a test message there is an HTTPS request to esms.vn with the harness's
		// made-up keys (measured: it answers `101`, wrong keys).
		step{Op: "SendPhoneTestMessage", Method: http.MethodPost,
			Path: "/sites/" + unconfigured + "/portal/phone/test-message",
			Body: map[string]any{"phone": "0912345678"}, Skip: noSecondSite(unconfigured)},
		step{Op: "GetMaintenanceSettings", Method: http.MethodGet, Path: "/privacy/maintenance"},
		step{Op: "UpdateMaintenanceSettings", Method: http.MethodPut, Path: "/privacy/maintenance",
			Body: map[string]any{"intervalSeconds": 600, "staleAfterMissedUpdates": 4,
				"staleCeilingPerPass": 200, "retentionBatchSize": 500},
			Volatile: []string{"updatedAt"}},
		step{Op: "UpdateMaintenanceSettings", Method: http.MethodPut, Path: "/privacy/maintenance",
			Body: map[string]any{"intervalSeconds": 300, "staleAfterMissedUpdates": 4,
				"staleCeilingPerPass": 200, "retentionBatchSize": 500},
			Volatile: []string{"updatedAt"}},
		step{Op: "GetGuestWifiKey", Method: http.MethodGet, Path: "/sites/" + site + "/guest-wifi-key"},
		step{Op: "GetVoucherSlipWifi", Method: http.MethodGet, Path: "/sites/" + site + "/voucher-slip-wifi"},
		step{Op: "UpdateGuestWifiKey", Method: http.MethodPut, Path: "/sites/" + site + "/guest-wifi-key",
			Body: map[string]any{"passphrase": "harness wifi passphrase"}, Volatile: []string{"updatedAt"}},
		step{Op: "GetVoucherSlipWifi", Method: http.MethodGet, Path: "/sites/" + site + "/voucher-slip-wifi"},
		step{Op: "UpdateGuestWifiKey", Method: http.MethodPut, Path: "/sites/" + site + "/guest-wifi-key",
			Body: map[string]any{"passphrase": nil}, Volatile: []string{"updatedAt"}},
	)

	return steps, nil
}

// ── finding the pathological rows ────────────────────────────────────────────
//
// By property, never by id. A hardcoded `sess-orphaned-no-stop` would break the
// day somebody renamed a fixture, and — worse — would pass silently if the row
// were removed, which is exactly the "moving fixture that heals itself" the
// frontend rules warn about.

// radiusApplyAvailable is the one difference in the RADIUS server settings, on
// every answer that carries them.
var radiusApplyAvailable = divergence{
	Keys: []string{"applyAvailable"},
	Reason: "the mock stands for an installation — deploy/ and lab/ — where the control plane " +
		"writes FreeRADIUS's tree and the supervisor in the FreeRADIUS image has announced " +
		"itself (internal/raddb/handoff.go); the differ's Go world is a bare process with no " +
		"FOYER_RADDB_TARGET, which truthfully answers false.",
}

// portalURLDerived is the one difference in the network settings, on every answer
// that carries them.
var portalURLDerived = divergence{
	Keys: []string{"source", "value"},
	Reason: "`portalUrl` — with no saved address and no deployment one, the mock " +
		"derives the portal's address from its own listener (`source: derived`); the " +
		"Go server under the harness is given no FOYER_PORTAL_URL and answers `unset`. " +
		"Both are the deployment's configuration, not the contract's.",
}

// noSecondSite is the skip for a step that needs a site other than the scenario's
// own, in a world that has only one.
func noSecondSite(id string) string {
	if id != "" {
		return ""
	}
	return "the world has one site, and this step needs one the scenario did not configure"
}

// findNonCoANas is the first fixture device whose driver does not send RFC 5176
// itself — one `updateNasCoaTiming` must refuse.
func findNonCoANas(w *memory.World) string {
	for _, n := range w.Nas {
		if n.Driver != gen.NasDriverKindMikrotik && n.Driver != gen.NasDriverKindCoa {
			return n.Id
		}
	}
	return ""
}

func findSession(w *memory.World, state gen.SessionState) string {
	for _, s := range w.Sessions {
		if s.State == state {
			return s.Id
		}
	}
	return ""
}

// findWrappingCounter looks for a counter close enough to 2^32 that the next
// interim update wraps it.
func findWrappingCounter(w *memory.World) string {
	const nearWrap = 4_000_000_000
	for _, s := range w.Sessions {
		if s.OutputOctets != nil && *s.OutputOctets > nearWrap {
			return s.Id
		}
		if s.InputOctets != nil && *s.InputOctets > nearWrap {
			return s.Id
		}
		if s.CountersWrapped {
			return s.Id
		}
	}
	return ""
}

func findStalest(w *memory.World) string {
	for _, s := range w.Sessions {
		if s.Freshness.State == gen.FreshnessStateStale {
			return s.Id
		}
	}
	return ""
}

func findController(w *memory.World, r gen.NasReachability) string {
	for _, c := range w.Controllers {
		if c.Reachability == r {
			return c.Id
		}
	}
	return ""
}

func findNas(w *memory.World, r gen.NasReachability) string {
	for _, n := range w.Nas {
		if n.Reachability == r {
			return n.Id
		}
	}
	return ""
}

func findRefusingNas(w *memory.World) string {
	for _, n := range w.Nas {
		if n.MessageAuthenticator == gen.NasMessageAuthenticatorRefused {
			return n.Id
		}
	}
	return ""
}

// disconnectSteps drives `disconnectSession` three ways, each against a session
// the world actually has, and skips — naming the missing row — when it does not.
func disconnectSteps(w *memory.World, inUseVoucher string) []step {
	nasByID := map[string]gen.Nas{}
	for _, n := range w.Nas {
		nasByID[n.Id] = n
	}
	var ended, cannot, can string
	for _, s := range w.Sessions {
		n, ok := nasByID[s.NasId]
		switch {
		case s.State == gen.SessionStateEnded:
			if ended == "" {
				ended = s.Id
			}
		case !ok:
		case !n.Capabilities.CanDeauthGuest:
			if cannot == "" {
				cannot = s.Id
			}
		case s.State == gen.SessionStateOnline && n.Reachability == gen.NasReachabilityOk &&
			(s.VoucherId == nil || *s.VoucherId != inUseVoucher):
			if can == "" {
				can = s.Id
			}
		}
	}
	one := func(id, missing string, extra step) step {
		if id == "" {
			return step{Op: "DisconnectSession", Skip: "no session in the oracle's world is " + missing +
				"; evaluated per run by `disconnectSteps`"}
		}
		extra.Op, extra.Method, extra.Path = "DisconnectSession", http.MethodPost, "/sessions/"+id+"/disconnect"
		if extra.Body == nil {
			extra.Body = map[string]any{}
		}
		return extra
	}
	return []step{
		{Op: "DisconnectSession", Method: http.MethodPost, Path: "/sessions/sess-does-not-exist/disconnect",
			Body: map[string]any{}},
		one(ended, "`ended`", step{Body: map[string]any{"revokeVoucher": true, "revokeGrant": true}}),
		one(cannot, "open on a device whose `canDeauthGuest` is false",
			step{Body: map[string]any{"revokeVoucher": true, "revokeGrant": true}}),
		one(can, "online on a reachable device that can end it", step{
			Adapter: []divergence{{
				Keys: []string{"disconnected", "failure"},
				Reason: "the oracle has no driver and ends the session; the adapter asks the " +
					"harness's strict UniFi fake, which knows only the one guest client the " +
					"portal steps place and answers *no client* for this MAC — which the " +
					"product reports as `session-end-failed` with `cause: error` rather than " +
					"as a disconnect. That is the property `disconnectSession` exists for: a " +
					"device that did not confirm is never reported as one that did. The wire " +
					"exchange with a device that does confirm is asserted in " +
					"`internal/apistore/disconnect_test.go` against a UDP fake NAS.",
			}},
		}),
	}
}

func findVoucher(w *memory.World, state gen.VoucherState) string {
	for _, v := range w.Vouchers {
		if v.State == state {
			return v.Id
		}
	}
	return ""
}

func findRequiredGardenEntry(w *memory.World, site string) string {
	g, ok := w.Garden[site]
	if !ok {
		return ""
	}
	for _, e := range g.Entries {
		if e.Origin == gen.GardenEntryOriginRequired {
			return e.Id
		}
	}
	return ""
}

func firstNasID(w *memory.World) string {
	if len(w.Nas) == 0 {
		return "nas-none"
	}
	return w.Nas[0].Id
}

func adminUsername(w *memory.World) string {
	for _, op := range w.Operators {
		if op.Role == gen.Administrator && op.Enabled {
			return op.Username
		}
	}
	return ""
}

func requireAdmin(w *memory.World) error {
	if adminUsername(w) == "" {
		return fmt.Errorf("the mock has no enabled administrator to sign in as")
	}
	return nil
}

// ── the adapter-mode declarations, and why each is one ───────────────────────
//
// Every one of these is a difference that **only the adapter mode can produce**,
// because the memory mode's Go server is handed the oracle's own rows and cannot
// disagree about them. Each therefore says something about the schema, about the
// adapter, or about a fixture the database will not accept — never about `web/mock`
// alone.
//
// They are declared narrowly, several small ones rather than one that covers a step.
// A declaration is satisfied when **any** of its keys differs, so a wide one stops
// noticing when the others stop, and a divergence that stops happening is the thing
// the run is supposed to fail on.

// orphanRuleReason is the disagreement about when a session stops being `online`.
const orphanRuleReason = "**the two servers disagree about when a session stops being `online`, and the " +
	"contract does not say.** `internal/store` calls a session `orphaned` once nothing has been heard " +
	"from it for more than twice the interval the NAS said it would report at, floored at 300 seconds " +
	"— the rule `docs/rules/radius.md` states, and the reason the 40-day fixture is not `ended`. " +
	"The oracle keeps the 20-minute-stale session `online` and carries the staleness in `freshness` " +
	"instead, which is a defensible second answer: it separates *is this session open* from *how old " +
	"is the figure beside it*. Both are reasonable and they are not the same, so a console reading " +
	"one and a report reading the other disagree about how many guests are on the network. " +
	"**Known contract gap**: `SessionState.orphaned` needs a definition, and until it has one " +
	"the state is per-implementation."

// sessionIdentityDivergences: what `radacct` alone cannot say about who a session is.
//
// The second declaration is **derived from the world rather than typed**, for the
// same reason the scenario finds its pathological rows by property and never by id.
// It named a fixture defect — the oracle marking addresses randomised whose
// locally-administered bit is clear — and a hardcoded declaration for a defect is
// one that turns the run red the day somebody fixes it, reporting a repair as a
// regression. Measured instead: declared while the oracle's world still holds such
// a row, absent when it does not, with the count in the reason so a reader sees
// what it covered rather than what it once covered.
func sessionIdentityDivergences(w *memory.World) []divergence {
	out := []divergence{
		{
			Keys: []string{"plan", "voucherId", "identityKind"},
			Reason: "**`radacct` cannot name the identity behind a session, so each server " +
				"reaches a plan a different way.** `internal/apistore` resolves the plan by key join on " +
				"`radacct.username`, so it is the plan the identity was actually sold; the " +
				"oracle's fixture sessions carry one picked from three at random, unrelated to " +
				"the identity. **The servers disagree about the value, not about its presence**. " +
				"Known limitation of " +
				"`web/fixtures`. `voucherId` is filled only when the query was *for* a " +
				"voucher, which `sessions.go` states in place. `identityKind` is guessed from the " +
				"shape of `radacct.username`, and the oracle's voucher sessions carry a null " +
				"username, so every one of them reads `unknown` where the oracle says `voucher`. " +
				"The first is a gap in `internal/apistore`; the third is the honest answer to a " +
				"world where the code was never recorded, and it becomes real when grants exist.",
		},
	}

	if bad := macClaimMismatches(w); len(bad) > 0 {
		out = append(out, divergence{
			Keys: []string{"macIsRandomised"},
			Reason: fmt.Sprintf("**the oracle marks addresses randomised that are not, on %d of its %d "+
				"sessions** — %s among them. `internal/store` reads the locally-administered bit, "+
				"which is what `docs/rules/security.md` §6 requires and the only thing that can "+
				"be read off a MAC. Known limitation of `web/fixtures`: the Go side is right, and a "+
				"console that calls a real OUI a pseudonym tells an operator the opposite of the "+
				"truth about the device in front of them. **This declaration is derived from the "+
				"world on every run**, so the day the fixtures are corrected it disappears rather "+
				"than turning the repair red.", len(bad), len(w.Sessions), bad[0]),
		})
	}
	return out
}

// macClaimMismatches names the sessions whose `macIsRandomised` disagrees with the
// bit the address actually carries.
//
// The harness computes the same fact both servers compute, from the oracle's own
// rows, which is what lets a declaration say *which side is right* rather than only
// that the two differ. `store.MACIsLocallyAdministered` is the function
// `internal/store` answers with, so this is the adapter's own rule applied to the
// oracle's data rather than a second implementation of it.
func macClaimMismatches(w *memory.World) []string {
	var out []string
	for _, s := range w.Sessions {
		if s.MacIsRandomised == nil {
			continue
		}
		if *s.MacIsRandomised != store.MACIsLocallyAdministered(s.CallingStationId) {
			out = append(out, s.CallingStationId)
		}
	}
	sort.Strings(out)
	return out
}

// sessionShapeDivergences: what the three-noun schema and the state rule change.
func sessionShapeDivergences(w *memory.World) []divergence {
	var out []divergence
	if len(apLessDevices(w)) > 0 {
		out = append(out, divergence{
			Keys: []string{"apName"},
			Reason: "a device with no MAC has no `access_points` row, so nothing carries an access " +
				"point's name for it — a MikroTik that is its own NAS, or a CoA-only switch. The " +
				"oracle answers the device's own name instead. That is the three-noun model " +
				"working rather than failing: `Session.apName` is the radio a guest was standing " +
				"under, and for those devices there is no such record. **Known contract " +
				"gap**: `apName` wants a sentence saying it is absent for a device that is " +
				"its own NAS, or the console will read the absence as missing data.",
		})
	}
	return append(out, divergence{
		Keys:   []string{"state", "acctSessionId", "intervalSeconds"},
		Reason: orphanRuleReason + " Two smaller ones travel with it, both on the sessions the oracle calls `unknown`: it writes `acctSessionId: \"\"` where the adapter omits the property, and it writes `freshness.intervalSeconds: null` where the adapter answers the interval that *would* apply. `unknown` itself has no representation in `radacct` — the adapter reaches it only when a row has neither a start nor an update, which no real Acct-Start produces.",
	})
}

// apLessDivergence: the fields an access point carries and a bare NAS does not.
//
// Declared only while the world holds a device with no MAC. A world of nothing but
// access points would make this vanish, and a typed declaration would then report
// that as a failure.
func apLessDivergence(w *memory.World) []divergence {
	if len(apLessDevices(w)) == 0 {
		return nil
	}
	return []divergence{{
		Keys: []string{"model", "firmware", "lastSeenAt"},
		Reason: "same cause as `Session.apName`: `model`, `firmware` and `lastSeenAt` are the access " +
			"point's columns, and a device with no MAC has no access point. The oracle carries them " +
			"on the device itself. This is the one place the three-noun model is visible from " +
			"outside, and it is worth a line in the contract rather than a surprise in a table.",
	}}
}

// inventedAddressDivergence is the harness's own, and is declared as such.
//
// Declared only while the world holds a device the oracle gives no `ipAddress`,
// because that is the only device the seeder has to invent an address for.
func inventedAddressDivergence(w *memory.World) []divergence {
	addressless := 0
	for _, n := range w.Nas {
		if n.IpAddress == nil {
			addressless++
		}
	}
	if addressless == 0 {
		return nil
	}
	return []divergence{{
		Keys: []string{"ipAddress"},
		Reason: "**this one is the harness's, and it is declared rather than hidden.** " +
			"`radius_clients.address` is NOT NULL and the contract's `Nas` carries no address at " +
			"all, so a device the oracle gives no `ipAddress` still needs one to be stored — the " +
			"seeder allocates it out of 198.51.100.0/24 and prints how many it invented. " +
			"`nasOut` then falls back to the client address when the access point has none, which " +
			"is deliberate, so the invented value surfaces as `ipAddress`. The price is that a " +
			"genuine `ipAddress` difference on the device list would land inside this " +
			"declaration; it is scoped to that one key for that reason.",
	}}
}

func nasShapeDivergences(w *memory.World) []divergence {
	return append(apLessDivergence(w), inventedAddressDivergence(w)...)
}

// ── the predicates the declarations above are derived from ───────────────────
//
// **Every declaration that names a property of the world is computed here rather
// than typed above.** A `macIsRandomised` written against a fixture defect would
// turn the fixture's repair red, and a `pgseed.go` that restated
// `controller_vendor_sites`' unique index in Go would go on refusing bindings for
// a rule a migration had narrowed. A
// declaration is a claim about the run in front of it, and a claim that cannot
// change with the world is a claim about a world that no longer exists.

// apLessDevices names the devices with no MAC — a MikroTik that is its own NAS, a
// CoA-only switch. They have no `access_points` row, which is what the model-,
// firmware-, lastSeenAt- and apName-shaped divergences are all about.
func apLessDevices(w *memory.World) []string {
	var out []string
	for _, n := range w.Nas {
		if n.Mac == nil {
			out = append(out, n.Id)
		}
	}
	sort.Strings(out)
	return out
}

// nonRadiusAttemptDivergence fires while the world holds an event `radpostauth`
// cannot carry.
func nonRadiusAttemptDivergence(w *memory.World) []divergence {
	kinds := map[gen.AuthSource]int{}
	for _, at := range w.Attempts {
		if at.Source != gen.Radius {
			kinds[at.Source]++
		}
	}
	if len(kinds) == 0 {
		return nil
	}
	total := 0
	var named []string
	for k, n := range kinds {
		total += n
		named = append(named, fmt.Sprintf("%d %s", n, k))
	}
	sort.Strings(named)

	return []divergence{{
		Keys: []string{"items", "macCoverage", "blindSpots"},
		Reason: fmt.Sprintf("the contract's `AuthAttempt.source` names three event streams — `radius`, "+
			"`portal`, `driver` — and PostgreSQL holds one. `radpostauth` carries the stock "+
			"FreeRADIUS column set verbatim, which is the project's exit hatch, and it has no "+
			"column for an event source, a voucher, an account or a portal grant. The oracle's "+
			"world has %d such rows (%s); the harness cannot seed them and the adapter could not "+
			"answer them if it did. `macCoverage` and `blindSpots` are derived from the rows that "+
			"remain. **This is the gap that decides whether the console's \"why can't this guest "+
			"get online\" screen can be built at all**, and it is a schema question rather than an "+
			"adapter defect: the two streams need a table.", total, strings.Join(named, ", ")),
	}}
}

// ── has this mock been driven before ─────────────────────────────────────────

// mockAlreadyDriven names the artefacts a previous run of this scenario left in
// the oracle.
//
// **Why this refuses rather than warns.** The scenario ends in mutations, and a
// second run against the same mock instance compares against a world the first run
// moved. Measured, that looks like this: three invocations against one long-lived
// mock produced six differences on the third — `secretLastUsedAt` present on the Go
// side for three devices, a voucher batch reporting `inUse 2 / revoked 2` against
// `0 / 4`, and two `freshness.state` flips. Every one was an earlier run's own
// rotation or revocation, and **every one of them names a real field and reads
// exactly like a real disagreement**.
//
// The `409` on `harness.guest` is loud and self-explaining. Rotation and revocation
// drift is quiet, and the report cannot tell it from a finding — so the report must
// not be produced. A red run that means nothing is the same disease as a green one
// that means nothing.
//
// The four artefacts are markers standing in for the quiet drift rather than the
// problem themselves: they are created in the same pass that rotates a secret and
// revokes a voucher, so finding any one of them establishes that the pass ran.
func mockAlreadyDriven(w *memory.World) []string {
	var found []string
	for _, a := range w.Accounts {
		if a.Username == harnessAccountName {
			found = append(found, "an account named "+harnessAccountName)
			break
		}
	}
	for _, op := range w.Operators {
		if op.Username == harnessOperatorName {
			found = append(found, "an operator named "+harnessOperatorName)
			break
		}
	}
	for _, b := range w.Batches {
		if b.Label == harnessBatchLabel {
			found = append(found, "a voucher batch labelled "+strconv.Quote(harnessBatchLabel))
			break
		}
	}
	for _, n := range w.Nas {
		if n.Name == harnessNasName {
			found = append(found, "a device named "+strconv.Quote(harnessNasName))
			break
		}
	}
	if gardenHasHarnessEntry(w) {
		found = append(found, "a walled-garden entry for "+harnessGardenValue)
	}
	return found
}

func gardenHasHarnessEntry(w *memory.World) bool {
	for _, g := range w.Garden {
		for _, e := range g.Entries {
			if e.Value == harnessGardenValue {
				return true
			}
		}
	}
	return false
}

// unrecordedVoucherSessionDivergence fires while the oracle's world gives the
// adapter no way to find the session a voucher is being used for.
//
// `revokeVoucher` looks in exactly two places, and `sessions.go` argues for both
// because they fail in different directions: `vouchers.current_session_id`, which
// can be stale, and `radacct.username` matching the code, which is what a NAS puts
// in User-Name for a redeemed voucher. When the world records neither, the adapter
// finds nothing to end and answers `sessionEnded: false` rather than claiming the
// guest is off the network.
//
// Derived rather than typed, so that a fixture set which starts recording the code
// on its sessions retires this declaration instead of turning red.
func unrecordedVoucherSessionDivergence(w *memory.World, voucherID string) []divergence {
	var code string
	for _, v := range w.Vouchers {
		if v.Id == voucherID {
			code = v.Code
			if v.CurrentSessionId != nil && *v.CurrentSessionId != "" {
				return nil // the voucher names its session; the adapter can end it
			}
			break
		}
	}
	if code == "" {
		return nil
	}
	for _, sess := range w.Sessions {
		if sess.Username != nil && strings.EqualFold(*sess.Username, code) {
			return nil // the code is in User-Name; the adapter can find it
		}
	}

	return []divergence{{
		Keys: []string{"sessionEnded"},
		Reason: "the revoke succeeds on both sides and the two disagree about whether a session " +
			"went with it. `revokeVoucher` finds the session two ways — " +
			"`vouchers.current_session_id`, null for this code, and `radacct.username` matching " +
			"the code, which no session in the oracle's world carries. So the adapter finds " +
			"nothing to end and says so rather than claiming the guest is off the network, which " +
			"is what `sessions.go` argues for. Same root as `identityKind`: the oracle's sessions " +
			"do not record the code they were authorised with.",
	}}
}
