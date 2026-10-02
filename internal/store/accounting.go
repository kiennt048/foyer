package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// AccountingRepo reads `radacct` and `radpostauth`, and has no write method.
//
// That is not a convention this file keeps. The application connects as
// `foyer_app`, which holds SELECT on those two tables and nothing else, so an
// INSERT added here would be refused by PostgreSQL before any reviewer saw it.
// `readonly_test.go` makes that refusal happen on purpose.
//
// Neither table carries `tenant_id` -- the stock FreeRADIUS column set is kept
// verbatim, because it is the project's exit hatch. Scoping is therefore derived:
// every query below joins `radius_clients`, which does carry a tenant and does have
// a row-level policy, by **longest-prefix match on the NAS address**. That is what
// FreeRADIUS itself does when matching a client, so a /24 covering a fleet resolves
// the same way here as it does there.
//
// The join is inner rather than outer, so a row whose NAS matches no client
// disappears from these results. That is deliberate -- the contract requires
// `Session.nasId` and there is nothing honest to put there -- and it is why
// UnmatchedClientAddresses exists: the dropped rows have to be countable, or a
// silent drop becomes a console that shows nothing and means nothing.
type AccountingRepo struct{ s *Store }

// The longest-prefix client match, as a lateral subquery.
//
// `>>=` is "contains or equals", so a client at 10.0.0.0/24 matches an
// Access-Request from 10.0.0.7 and a client at 10.0.0.7/32 matches it more
// specifically. Ordering by masklen descending and taking one row picks the more
// specific, which is the rule FreeRADIUS applies.
const clientForNAS = `
	JOIN LATERAL (
		SELECT c.id, c.name, c.site_id
		FROM radius_clients c
		WHERE c.address >>= ra.nasipaddress
		ORDER BY masklen(c.address) DESC
		LIMIT 1
	) rc ON true`

// Called-Station-Id is `<AP MAC><separator><SSID>` and vendors disagree about the
// separator inside the MAC. This accepts both spellings and treats the first colon
// after a complete six-octet MAC as the boundary.
//
// Parametrised by the row alias because two tables carry the column -- `radacct`
// and `radpostauth` -- and **the spelling must be known in exactly one place.** It
// was known in two for a while: this join, and a regexp in `internal/apistore`
// that resolved the same MAC in Go because the repository could not.
func calledStationParts(row string) string {
	return `
	LEFT JOIN LATERAL (
		SELECT m[1] AS ap_mac, nullif(m[2], '') AS ssid
		FROM regexp_match(coalesce(` + row + `.calledstationid, ''),
		                  '^([0-9A-Fa-f]{2}(?:[-:][0-9A-Fa-f]{2}){5}):?(.*)$') AS m
	) cs ON true
	LEFT JOIN access_points ap ON ap.mac = replace(cs.ap_mac, '-', ':')::macaddr`
}

// The client's only access point, when it has exactly one.
//
// This is the second of the three ways a row resolves to a device, and it exists
// because plenty of NAS send no Called-Station-Id at all. One access point behind a
// clients.conf row is not a guess -- there is nothing else it could be. Two are, so
// `HAVING count(*) = 1` yields no row and the coalesce falls through to the client.
//
// Aggregated rather than selected because an aggregate is what lets `HAVING` filter
// the group; the group has one row, so both values come from it.
const soleAccessPoint = `
	LEFT JOIN LATERAL (
		SELECT (array_agg(a.id))[1] AS id, (array_agg(a.name))[1] AS name
		FROM access_points a
		WHERE a.radius_client_id = rc.id
		HAVING count(*) = 1
	) sole ON true`

// A session is orphaned when it is still open and nothing has been heard from it
// for more than twice the interval the NAS said it would report at.
//
// Twice, and not once, because a single missed Interim-Update is a lost UDP packet.
// The floor is 300 seconds: Acct-Interim-Interval has a widely-implemented
// five-minute floor, so a NAS claiming less is not believed.
const sessionState = `
	CASE
		WHEN ra.acctstoptime IS NOT NULL THEN 'ended'
		WHEN ra.acctupdatetime IS NULL AND ra.acctstarttime IS NULL THEN 'unknown'
		WHEN coalesce(ra.acctupdatetime, ra.acctstarttime)
		     < now() - (2 * greatest(coalesce(ra.acctinterval, 300), 300) || ' seconds')::interval
		THEN 'orphaned'
		ELSE 'online'
	END`

// `nasId` is the **device** id, which is not the clients.conf row's.
//
// A `Nas` in the contract is a device: `ListNas` gives it `coalesce(ap.id, rc.id)`,
// so twelve access points behind one clients.conf row are twelve devices. This
// column selected `rc.id` and was self-consistently wrong -- a device id read off
// `listNas` and sent back as `listSessions?nasId=` matched nothing, and the device
// a failed session-end names could not be looked up at all.
//
// Three resolutions, narrowest first, and the last is not a fallback but the correct
// answer for a device with no access point: a MikroTik that is its own NAS has a
// clients.conf row and nothing else.
const sessionColumns = `
	ra.acctuniqueid, ra.acctsessionid, rc.site_id,
	coalesce(ap.id, sole.id, rc.id), coalesce(ap.name, sole.name, rc.name),
	` + sessionState + `,
	coalesce(ra.callingstationid, ''), ra.calledstationid, ra.username,
	host(ra.framedipaddress), ap.name, cs.ssid,
	ra.acctstarttime, ra.acctstoptime, ra.acctupdatetime, ra.acctterminatecause,
	ra.acctinputoctets, ra.acctoutputoctets, ra.acctsessiontime, ra.acctinterval,
	` + recordedIdentityKind

// recordedIdentityKind is what Foyer's own records say the username is, at the
// session's venue: a voucher, an account, or NULL for neither.
//
// **It exists because the export masks on it.** A voucher code never leaves in full
// (`exportSessions`), and for a voucher session the NAS put the code in User-Name —
// so a session classed `account` by mistake is a code exported whole. The shape guess
// (`identityKind`: twelve upper-case alphanumerics) made that mistake for any
// spelling the NAS changed (lower case, separators). The vouchers and accounts
// tables are the record; the shape is only the fallback for a username neither
// knows (a voucher whose batch was deleted, MAC authentication).
//
// Three probes, in an order that never mislabels an account and never lets a code
// out in full: the code exactly as issued (what the RADIUS lookup matches); an
// account's exact username (unique per venue, 00001); and the code normalised as
// the portal normalises one — upper case, separators dropped — which catches a NAS
// that respelled it. A username that normalises onto a code and is also an account
// is the account, by the second probe; one that normalises onto a code and is
// nothing else is masked, which is the direction to be wrong in.
const recordedIdentityKind = `CASE
		WHEN EXISTS (SELECT 1 FROM vouchers v WHERE v.site_id = rc.site_id AND v.code = ra.username)
			THEN 'voucher'
		WHEN EXISTS (SELECT 1 FROM accounts a WHERE a.site_id = rc.site_id AND a.username = ra.username)
			THEN 'account'
		WHEN EXISTS (SELECT 1 FROM vouchers v WHERE v.site_id = rc.site_id
		             AND v.code = upper(regexp_replace(ra.username, '[^0-9A-Za-z]', '', 'g')))
			THEN 'voucher'
	END`

func (r *AccountingRepo) scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	var s Session
	var startedAt *time.Time
	var recorded *string
	err := row.Scan(&s.ID, &s.AcctSessionID, &s.SiteID, &s.NasID, &s.NasName, &s.State,
		&s.CallingStationID, &s.CalledStationID, &s.Username,
		&s.IPAddress, &s.APName, &s.SSID,
		&startedAt, &s.EndedAt, &s.LastUpdateAt, &s.TerminateCause,
		&s.InputOctets, &s.OutputOctets, &s.SessionSeconds, &s.AcctInterval, &recorded)
	if err != nil {
		return nil, mapErr(err)
	}
	if startedAt != nil {
		s.StartedAt = *startedAt
	}
	s.CountersWrapped = wrapped(s.InputOctets) || wrapped(s.OutputOctets)
	s.MACIsRandomised = MACIsLocallyAdministered(s.CallingStationID)
	s.IdentityKind = identityKind(s.Username)
	if recorded != nil {
		s.IdentityKind = *recorded
	}
	return &s, nil
}

// wrapped reports whether the reconstructed 64-bit total exceeded 2^32.
//
// The generated accounting query already combines Acct-Input-Octets with
// Acct-Input-Gigawords, so the column holds the true total rather than the wrapping
// 32-bit counter. A value above 2^32 therefore means the gigawords attribute
// carried a non-zero high word, which is the fact the console needs: it is why a
// raw counter read off the NAS and this figure disagree.
//
// Never subtract two of these without knowing that. A delta taken across a wrap on
// a server that ignores gigawords reads as a session whose total dropped by
// 4 294 967 296 -- "used" goes negative and "remaining" hands the guest 4 GiB.
func wrapped(v *int64) bool {
	const twoPow32 = int64(4294967296)
	return v != nil && *v >= twoPow32
}

// MACIsLocallyAdministered reports the locally-administered bit.
//
// iOS 14+ and Android 10+ present a private MAC per SSID by default, so this is the
// ordinary case rather than an anomaly. It matters because it is the difference
// between "this is a device we have seen before" and "this is a pseudonym that
// happens to be stable on this SSID" -- and because the console must never present
// a randomised address as an identity.
func MACIsLocallyAdministered(mac string) bool {
	hex := make([]rune, 0, 12)
	for _, c := range strings.ToLower(mac) {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			hex = append(hex, c)
		}
	}
	if len(hex) < 2 {
		return false
	}
	first, err := strconv.ParseUint(string(hex[:2]), 16, 8)
	if err != nil {
		return false
	}
	return first&0x02 != 0
}

// identityKind guesses from the username shape, for a username Foyer's records do
// not know (`recordedIdentityKind` answers first). It is deliberately
// conservative: `unknown` is a better answer than a wrong one.
func identityKind(username *string) string {
	if username == nil || *username == "" {
		return "unknown"
	}
	u := *username
	if len(u) == 12 && strings.IndexFunc(u, func(r rune) bool {
		return !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return "voucher"
	}
	if strings.ContainsAny(u, ":-") && len(MACOnly(u)) == 12 {
		return "mac-auth"
	}
	return "account"
}

// MACOnly returns just the hex digits of a MAC-like string.
func MACOnly(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// SessionFilter narrows a session listing.
type SessionFilter struct {
	ListOptions
	State string
	MAC   string
}

// ListSessions returns accounting rows, newest first.
func (r *AccountingRepo) ListSessions(ctx context.Context, f SessionFilter) (Page[Session], error) {
	limit := clampLimit(f.Limit)
	after, err := decodeCursor(f.Cursor)
	if err != nil {
		return Page[Session]{}, err
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT `+sessionColumns+`
		FROM radacct ra`+clientForNAS+calledStationParts("ra")+soleAccessPoint+`
		WHERE ($1 = '' OR rc.site_id = $1)
		  AND ($2 = '' OR `+sessionState+` = $2)
		  AND ($3 = '' OR ra.callingstationid = $3)
		  AND ($4 = '' OR ra.acctuniqueid < $4)
		  AND ($5 = '' OR coalesce(ra.username, '') ILIKE '%' || $5 || '%'
		               OR coalesce(ra.callingstationid, '') ILIKE '%' || $5 || '%')
		ORDER BY ra.acctstarttime DESC NULLS LAST, ra.acctuniqueid DESC
		LIMIT $6`, f.SiteID, f.State, f.MAC, after, f.Query, limit+1)
	if err != nil {
		return Page[Session]{}, mapErr(err)
	}
	defer rows.Close()

	var items []Session
	for rows.Next() {
		s, err := r.scanSession(rows)
		if err != nil {
			return Page[Session]{}, err
		}
		items = append(items, *s)
	}
	if err := rows.Err(); err != nil {
		return Page[Session]{}, mapErr(err)
	}
	return paginate(items, limit, func(s Session) string { return s.ID }), nil
}

// GetSession returns one accounting row by its acctuniqueid.
func (r *AccountingRepo) GetSession(ctx context.Context, id string) (*Session, error) {
	return r.scanSession(r.s.pool.QueryRow(ctx, `
		SELECT `+sessionColumns+`
		FROM radacct ra`+clientForNAS+calledStationParts("ra")+soleAccessPoint+`
		WHERE ra.acctuniqueid = $1`, id))
}

// RadiusSession is what `radacct` records about one live session, for the driver
// family that has no way to ask.
//
// ── why this type and not `Session` ──────────────────────────────────────────
//
// `Session` is the console's read model: a state, an SSID, byte counters, a device
// name. A CoA driver needs none of that and must not be handed it — what it needs is
// the handful of values a NAS will match a session on, and RFC 5176 has no query in
// it, so **every field here is something the driver may put on the wire.** A wide
// type at this seam invites a driver to identify a session by a value the NAS never
// sent.
//
// ── what `radacct` cannot tell a CoA driver, and why that is not faked here ──
//
// `nas.SessionSource` asks for "every identifier the platform recorded --
// Acct-Session-Id, NAS-IP-Address, NAS-Identifier, User-Name". The stock FreeRADIUS
// column set carries the first, second and fourth, and it carries **no
// NAS-Identifier and no Cisco `audit-session-id`**: neither is a column, the set is
// kept verbatim because it is the project's exit hatch, and adding one would end
// that.
//
// So they are absent, and they are deliberately not filled in from
// `radius_clients.nas_identifier`. That value is Foyer's own record of what it told
// the device to call itself, and the CoA driver compares the session's NAS-Identifier
// against **its own configuration, which comes from the same row** — so a synthesised
// value would make the driver's "is this session on this NAS?" check compare a copy
// of a value against itself. That is a check that cannot fail.
type RadiusSession struct {
	AcctSessionID string
	// AcctUniqueID is FreeRADIUS's own key for the row. Never sent to a NAS -- it
	// is Foyer's handle on the session, for logs and for a second read.
	AcctUniqueID     string
	CallingStationID string
	UserName         string
	// NASIPAddress is the address the accounting packet came from, in host form.
	// It is the **only** device identifier this table records, which makes it the
	// only basis a CoA driver has for deciding a session belongs to it.
	NASIPAddress string
	SiteID       string
	NASID        string
}

// ActiveSessionForMAC resolves a MAC to the live session Foyer's own RADIUS server
// has for it, if any.
//
// ── the three-way contract, which is the whole point ─────────────────────────
//
// `(nil, ErrNotFound)` means *no such session*, and any other error means *I could
// not look*. `nas.SessionSource` insists on the distinction for the same reason
// `aaa.PolicyStore` does: a caller that cannot tell them apart turns a database
// outage into "the guest's device is not on the network", and a CoA driver would
// then refuse a presence check it has no opinion about.
//
// ── why a spelling list rather than a normalising expression ─────────────────
//
// `Calling-Station-Id` has no canonical spelling and vendors disagree. Normalising
// the column would be exact and would also make `radacct_callingstationid_idx`
// unusable, on the one table in this schema that only grows -- so the candidates are
// generated from the normalised MAC and matched with `= ANY`, which the index
// answers.
//
// A spelling not in the list is a miss, and a miss **fails closed**: no session
// found means the driver reports the device absent, and `docs/rules/security.md` §4 makes
// presence the precondition for a grant. Being wrong in that direction costs a guest
// a retry; being wrong in the other authorizes a device nothing confirmed.
func (r *AccountingRepo) ActiveSessionForMAC(ctx context.Context, mac string) (*RadiusSession, error) {
	spellings := MACSpellings(mac)
	if len(spellings) == 0 {
		// Not a MAC at all. `ErrNotFound` rather than an error: a caller that
		// handed this a hostname is asking about a device that has no session,
		// which is a fact and not a failure to look.
		return nil, ErrNotFound
	}

	var s RadiusSession
	err := r.s.pool.QueryRow(ctx, `
		SELECT ra.acctsessionid, ra.acctuniqueid, coalesce(ra.callingstationid, ''),
		       coalesce(ra.username, ''), host(ra.nasipaddress), rc.site_id, rc.id
		FROM radacct ra`+clientForNAS+`
		WHERE ra.callingstationid = ANY($1)
		  AND ra.acctstoptime IS NULL
		ORDER BY ra.acctstarttime DESC NULLS LAST, ra.radacctid DESC
		LIMIT 1`, spellings).
		Scan(&s.AcctSessionID, &s.AcctUniqueID, &s.CallingStationID, &s.UserName,
			&s.NASIPAddress, &s.SiteID, &s.NASID)
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

// MACSpellings renders the ways a NAS might have written one address.
//
// Derived from the normalised value rather than from the caller's string, so the
// same six candidates come out whatever spelling arrives. Cisco's dotted form is
// included because Cisco is in the CoA family this exists for.
func MACSpellings(mac string) []string {
	hex := MACOnly(mac)
	if len(hex) != 12 {
		return nil
	}
	colon := hex[0:2] + ":" + hex[2:4] + ":" + hex[4:6] + ":" + hex[6:8] + ":" + hex[8:10] + ":" + hex[10:12]
	dash := strings.ReplaceAll(colon, ":", "-")
	dotted := hex[0:4] + "." + hex[4:8] + "." + hex[8:12]
	out := make([]string, 0, 8)
	for _, form := range []string{colon, dash, dotted, hex} {
		out = append(out, form, strings.ToUpper(form))
	}
	return out
}

// SessionCounts is the overview's view of accounting.
type SessionCounts struct {
	Online   int
	Orphaned int
	// OldestOnlineUpdate is the least recent update among the sessions counted by
	// `Online` -- and among those only. Nil when none is online, and the console
	// must render that as "no data" rather than as zero.
	//
	// ── why not `min` over open sessions ──
	//
	// A filter of `state IN ('online', 'orphaned')` would take this timestamp over
	// rows that `Online` and `Orphaned` — two separate figures over disjoint sets —
	// each count only part of. `internal/apistore` attaches it to `clientsOnline`,
	// so a site with twelve guests all heard from thirty seconds ago and one
	// forty-day orphan would report *the online count* as forty days stale. The
	// number would be seconds old; the sentence beside it forty days wrong; and the
	// console renders the sentence because `docs/rules/frontend.md` requires it to.
	//
	// **A freshness is derived over exactly the rows its figure counts.** That is
	// the whole of the rule, and `web/mock` derives it over the same row set.
	//
	// The counter-argument, because it is real and it is not what this field is
	// for: an orphan may still be associated, so `Online` may be an undercount.
	// True -- and that uncertainty already travels twice, as `Orphaned` and as the
	// `sessions-orphaned` alarm. Ageing an unrelated timestamp is a third telling
	// that says both more than it knows (about the online figure) and less than it
	// knows (it says "old", not "one open session cannot be vouched for").
	//
	OldestOnlineUpdate *time.Time

	// OldestOrphanUpdate is the least recent update among the sessions counted by
	// `Orphaned` -- and among those only, by the same rule. Nil when none is
	// orphaned.
	//
	// ── the orphan side of the same rule ──
	//
	// `Overview.orphanedSessions` is a `Metric`, not a bare integer, so it has a
	// freshness of its own to carry. Whether an orphan-side timestamp is needed is
	// a fact the contract owns, and a comment here arguing otherwise would be a
	// second copy of it -- the shape `docs/rules/database.md` has a section about.
	//
	// **The rule is the same rule; what it means is not the same meaning.** For
	// `Online`, an old sample is an artefact of the instrument: the count may be
	// wrong. For `Orphaned` the age is the subject, and it is the only useful
	// sentence about the figure -- one access point that rebooted ten minutes ago
	// and rows nothing has reaped since July are the same integer and different
	// faults. `min` over the counted rows separates them; the integer cannot.
	//
	// There is deliberately no interval alongside it. A row joins this set after
	// `2 * max(coalesce(acctinterval, 300), 300)` of silence -- see `sessionState`
	// -- so the set mixes rows judged at ten minutes with rows judged at two
	// hours, and no single cadence is true of all of them. `internal/apistore`
	// publishes `intervalSeconds: null` for that reason and not as an omission.
	OldestOrphanUpdate *time.Time
}

// CountSessions answers the overview's two session figures in one pass.
func (r *AccountingRepo) CountSessions(ctx context.Context, siteID string) (SessionCounts, error) {
	var c SessionCounts
	err := r.s.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE state = 'online'),
			count(*) FILTER (WHERE state = 'orphaned'),
			min(last_update) FILTER (WHERE state = 'online'),
			min(last_update) FILTER (WHERE state = 'orphaned')
		FROM (
			SELECT `+sessionState+` AS state,
			       coalesce(ra.acctupdatetime, ra.acctstarttime) AS last_update
			FROM radacct ra`+clientForNAS+`
			WHERE ($1 = '' OR rc.site_id = $1) AND ra.acctstoptime IS NULL
		) t`, siteID).Scan(&c.Online, &c.Orphaned, &c.OldestOnlineUpdate, &c.OldestOrphanUpdate)
	return c, mapErr(err)
}

// UnmatchedClientAddresses lists NAS addresses that appear in accounting but match
// no RADIUS client.
//
// This is the `unknown-radius-client` blind spot, and it is the reason the joins
// above may be inner joins. An Access-Request from an address matching no client is
// discarded by FreeRADIUS without a reply, so the guest sees a network that does
// not work and Foyer sees nothing at all -- unless it counts these.
func (r *AccountingRepo) UnmatchedClientAddresses(ctx context.Context) ([]string, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT DISTINCT host(ra.nasipaddress)
		FROM radacct ra
		WHERE NOT EXISTS (SELECT 1 FROM radius_clients c WHERE c.address >>= ra.nasipaddress)
		ORDER BY 1`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, addr)
	}
	return out, mapErr(rows.Err())
}

// ---------------------------------------------------------------------------
// radpostauth, and the driver stream it is unioned with
// ---------------------------------------------------------------------------

// The two streams `listAuthAttempts` answers, projected onto one column list.
//
// `AuthAttempt.source` declares three streams: `radius` for what FreeRADIUS saw,
// `portal` for Foyer's own decision on a voucher, and `driver` for **the
// controller refusing after every check Foyer owns has passed, which is invisible
// from both of the other two sources.** `radpostauth` writes only the first; with
// no other writer the operation would answer a query for the other two with an
// empty list -- and the failure the third describes is the one an operator is most
// likely to be looking for.
//
// `authorization_events` is the other writer (migration 00014), and this is where
// the two are joined. **The union is here rather than in a caller because a page has one
// cursor.** A caller that read a page from each repository and merged them in Go
// could not name a resume point inside either page, so a row in the middle of a
// merged chunk would be unreachable; a caller that returned one stream after the
// other would show an operator every RADIUS reject in the retention window before
// the first driver event.
//
// Ordered and paged on `(at, source, id)` descending. `at` alone is not unique --
// two post-auth rows share a microsecond routinely -- and a cursor on a non-unique
// key skips or repeats rows. `source` sits in the middle because the two streams
// have independent sequences, so a timestamp and an id together are still not a key.
const authAttemptColumns = `
	source, ord_id, at, username, outcome, reply, callingstationid, calledstationid,
	nas_id, site_id, grant_id, voucher_id, failure, status, attempts`

// The radius half: a `radpostauth` row, resolved to a device and a site through
// `Called-Station-Id` exactly as it always was.
const radiusAttemptSelect = `
	SELECT 'radius'::text AS source, rp.id AS ord_id, rp.authdate AS at,
	       rp.username,
	       CASE WHEN rp.reply = 'Access-Accept' THEN 'accepted' ELSE 'rejected' END AS outcome,
	       rp.reply,
	       rp.callingstationid, rp.calledstationid,
	       ap.id AS nas_id, ap.site_id,
	       NULL::text AS grant_id, NULL::text AS voucher_id,
	       NULL::text AS failure, NULL::integer AS status, NULL::integer AS attempts
	FROM radpostauth rp`

// The driver half.
//
// `nas_id` resolves to the **device** id the console knows -- the access point's
// where the event carries one, the clients.conf row's otherwise -- because that is
// what `coalesce(ap.id, sole.id, rc.id)` means on the session query, and a `nasId`
// read off one screen has to mean the same thing on the other.
//
// `calledstationid` carries the access point's MAC alone rather than the
// `<MAC><separator><SSID>` a NAS would send. It is what this row actually knows, and
// synthesising an SSID onto it would be the API asserting something it did not
// observe.
const driverAttemptSelect = `
	SELECT 'driver'::text AS source, ae.id AS ord_id, ae.at,
	       coalesce(ae.username, '') AS username,
	       CASE WHEN ae.delivery = 'open' THEN 'accepted' ELSE 'rejected' END AS outcome,
	       '' AS reply,
	       ae.client_mac::text AS callingstationid, ae.ap_mac::text AS calledstationid,
	       coalesce(aep.id, ae.nas_id) AS nas_id, ae.site_id,
	       ae.grant_id, ae.voucher_id, ae.failure, ae.status, ae.attempts
	FROM authorization_events ae
	LEFT JOIN access_points aep ON aep.mac = ae.ap_mac`

// AuthAttemptFilter narrows an attempt listing.
//
// `ListOptions.SiteID` is a **scope**, not a preference, and it is read here rather
// than by a caller -- see the comment on ListAuthAttempts. A field on this struct
// that nothing looked at would be worse than an absent one: a switch that looks
// like a switch and configures nothing.
type AuthAttemptFilter struct {
	ListOptions
	Outcome string
	// Source narrows to one stream: `radius`, `driver`, or empty for both. A
	// caller asking for `portal` gets nothing rather than being ignored --
	// nothing writes that stream, and answering it with rows from another layer
	// would be the API claiming an event came from a place that never wrote one.
	Source string
}

// formatAuthAttemptCursor renders the `(at, source, id)` tuple a page resumes after.
//
// One opaque string rather than three parameters, because that is what the
// contract's cursor is and what `ListOptions` carries. RFC3339Nano round-trips
// PostgreSQL's microsecond precision without loss.
func formatAuthAttemptCursor(a AuthAttempt) string {
	return a.At.UTC().Format(time.RFC3339Nano) + "|" + a.Source + "|" + strconv.FormatInt(a.ID, 10)
}

func parseAuthAttemptCursor(raw string) (time.Time, string, int64, error) {
	parts := strings.SplitN(raw, "|", 3)
	if len(parts) != 3 {
		return time.Time{}, "", 0, errMalformedCursor
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", 0, errMalformedCursor
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return time.Time{}, "", 0, errMalformedCursor
	}
	return at, parts[1], id, nil
}

var errMalformedCursor = errors.New("store: malformed cursor")

// ListAuthAttempts returns both streams, newest first.
//
// There is no field a submitted password could arrive in, which is the point: the
// stock FreeRADIUS post-auth query inserts the credential into `radpostauth.pass`
// on every attempt, accepted or rejected. Foyer generates that query and omits the
// column, and this type omits any field it could land in.
//
// ── the site scope, which is the part that matters ───────────────────────────
//
// `radpostauth` keeps the stock column set verbatim, so it carries **no NAS address
// and no tenant**. `ListSessions` derives a site by joining `radius_clients` on
// `radacct.nasipaddress`; there is no such column here, so a repository that
// returned rows unscoped would answer `listAuthAttempts?siteId=` with **every
// site's** usernames and MACs -- on the one screen an operator opens when a guest is
// standing in front of them. That is a scoping hole, not a missing feature, and it
// cannot be left to a caller to remember.
//
// The one identifier the row does carry is `Called-Station-Id`, and
// `access_points.mac` resolves it to a device and a site. It is the same join the
// session query performs, which is the reason this lives here and not in an adapter:
// two places knowing how a `Called-Station-Id` is spelled is one place too many.
//
// A row whose called-station resolves to nothing is **excluded whenever a site is
// asked for**, because including it would be the leak, and **included when none is**,
// because those rows matter most: an Access-Request from a device nothing recognises
// is the ordinary symptom of the most common real fault.
//
// The driver half needs none of that reasoning: `authorization_events` is Foyer's
// own table, carries its own `site_id` and its own tenant, and sits behind a
// row-level policy. Its scope is a column comparison. It is worth noticing that the
// difficult half of this query is difficult because of a schema decision taken for
// an unrelated reason -- keeping `radpostauth` stock so the data outlives Foyer.
//
// ── why each branch is bounded before the merge ──────────────────────────────
//
// Each half applies the cursor and every filter and takes its own `limit + 1` rows
// before the union sorts them. The global newest `n` rows can contain at most `n`
// from either stream, so each stream's own newest `n` is provably enough -- and
// without the inner bound this query sorts the whole retention window to answer one
// page.
func (r *AccountingRepo) ListAuthAttempts(ctx context.Context, f AuthAttemptFilter) (Page[AuthAttempt], error) {
	limit := clampLimit(f.Limit)
	raw, err := decodeCursor(f.Cursor)
	if err != nil {
		return Page[AuthAttempt]{}, err
	}
	var (
		curAt     *time.Time
		curSource string
		curID     int64
	)
	if raw != "" {
		at, source, id, err := parseAuthAttemptCursor(raw)
		if err != nil {
			return Page[AuthAttempt]{}, err
		}
		curAt, curSource, curID = &at, source, id
	}

	// The `portal` stream has no writer. Empty is the honest answer; ignoring the
	// filter would hand back RADIUS rows under somebody else's label.
	if f.Source == "portal" {
		return Page[AuthAttempt]{}, nil
	}

	// The predicate both branches share, written once so the two cannot drift.
	// $1 cursor timestamp, $2 cursor source, $3 cursor id, $4 site, $5 outcome,
	// $6 search, $7 source, $8 limit.
	const bounds = `
		WHERE ($1::timestamptz IS NULL OR (at, source, ord_id) < ($1, $2, $3))
		  AND ($4 = '' OR site_id = $4)
		  AND ($5 = '' OR outcome = $5)
		  AND ($6 = '' OR username ILIKE '%' || $6 || '%'
		               OR coalesce(callingstationid, '') ILIKE '%' || $6 || '%')
		  AND ($7 = '' OR source = $7)
		ORDER BY at DESC, source DESC, ord_id DESC
		LIMIT $8`

	rows, err := r.s.pool.Query(ctx, `
		WITH radius AS (
			SELECT * FROM (`+radiusAttemptSelect+calledStationParts("rp")+`) rs`+bounds+`
		), driver AS (
			SELECT * FROM (`+driverAttemptSelect+`) ds`+bounds+`
		)
		SELECT `+authAttemptColumns+`
		FROM (SELECT * FROM radius UNION ALL SELECT * FROM driver) t
		ORDER BY at DESC, source DESC, ord_id DESC
		LIMIT $8`,
		curAt, curSource, curID, f.SiteID, f.Outcome, f.Query, f.Source, limit+1)
	if err != nil {
		return Page[AuthAttempt]{}, mapErr(err)
	}
	defer rows.Close()

	var items []AuthAttempt
	for rows.Next() {
		var a AuthAttempt
		if err := rows.Scan(&a.Source, &a.ID, &a.At, &a.Username, &a.Outcome, &a.Reply,
			&a.CallingStationID, &a.CalledStationID, &a.NasID, &a.SiteID,
			&a.GrantID, &a.VoucherID, &a.Failure, &a.Status, &a.Attempts); err != nil {
			return Page[AuthAttempt]{}, mapErr(err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return Page[AuthAttempt]{}, mapErr(err)
	}
	return paginate(items, limit, formatAuthAttemptCursor), nil
}

// MacCoverage reports whether post-auth rows actually carry the calling station.
//
// The stock FreeRADIUS post-auth query leaves the station-id columns empty even
// though the schema has them, so a stock deployment records no MAC on any attempt --
// and the MAC is the only identifier an operator has when a guest is standing in
// front of them. Foyer's generated query populates them; this measures whether the
// rows on disk actually do.
//
// `unknown` when there are no rows at all, because "we have never recorded an
// attempt" and "we record attempts without MACs" are different problems.
//
// Scoped by the same called-station join as the listing, and for the same reason.
// Deployment-wide is honest for a single-site install and wrong for a two-venue one,
// where a site whose devices all record MACs would be reported `not-recorded`
// because another site's do not -- the operator would be told to go and fix a
// configuration that is already correct.
func (r *AccountingRepo) MacCoverage(ctx context.Context, siteID string) (string, error) {
	var total, withMAC int
	err := r.s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE coalesce(callingstationid, '') <> '')
		FROM (
			SELECT rp.callingstationid
			FROM radpostauth rp`+calledStationParts("rp")+`
			WHERE ($1 = '' OR ap.site_id = $1)
			ORDER BY rp.id DESC
			LIMIT 500
		) t`, siteID).
		Scan(&total, &withMAC)
	if err != nil {
		return "", mapErr(err)
	}
	switch {
	case total == 0:
		return "unknown", nil
	case withMAC == 0:
		return "not-recorded", nil
	default:
		return "recorded", nil
	}
}
