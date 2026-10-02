package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/redact"
)

// Personal data: what this schema stores about a guest, for how long, and how one
// guest is erased.
//
// ── what the field does, and what this copies ────────────────────────────────
//
// PacketFence runs a maintenance task per table -- `acct_cleanup`, `auth_log_cleanup`,
// `admin_api_audit_log_cleanup`, `node_cleanup` -- each with its own window, a batch
// size and a timer, and `node_cleanup` ships *disabled* (window 0D) because deleting
// a registered device is a decision an operator makes, not a default. Those are the
// three things copied here: a period per category rather than one global number,
// batched deletes on a schedule, and "keep until an operator removes it" as a legal
// value for the one category that holds identities an operator created.
//
// Where it diverges: PacketFence's windows live in a config file, and Foyer's live in
// the database (migration 00020), because the console is where an operator of a
// Vietnamese venue will look for the retention policy Decree 356/2025/ND-CP expects
// them to have, and because the sweep's own record of what it did belongs next to it.
//
// ── the two registries ───────────────────────────────────────────────────────
//
// **Retention categories** say which tables age out and after how long.
// **Erasers** say which tables hold rows naming one data subject and how to remove
// them. `PersonalColumns` is the list both are checked against: every column in the
// schema that holds a MAC, a username, a phone number or an e-mail address is on it,
// with the subject kind that erases it or the reason nothing does -- and
// `privacy_test.go` reads the live schema and fails for a column that is not. A table
// added later that stores a phone number therefore fails the suite until whoever
// added it says how it is erased. `docs/operator/personal-data.md` is held to the
// same list by the same test.

// SubjectKind is what identifies the data subject of an erasure.
type SubjectKind string

const (
	// SubjectMAC is a device, by its MAC address. On iOS 14+ and Android 10+ that is
	// usually a per-SSID private address: a pseudonym for one person on one network,
	// which is personal data all the same (GDPR Recital 30 names device identifiers).
	SubjectMAC SubjectKind = "mac"
	// SubjectAccount is a guest account an operator created, by its id.
	SubjectAccount SubjectKind = "account"
	// SubjectPhone is a person by their phone number — the identifier a phone
	// sign-in guest has, and the one a guest who typed it into a sign-in form can
	// give. Phone numbers are stored in five tables, and without this kind no
	// erasure could be asked for by one.
	SubjectPhone SubjectKind = "phone"
	// SubjectEmail is a person by an e-mail address they gave on a sign-in form or
	// with a marketing consent. Without it, the two tables that hold an address
	// could not be erased by one.
	SubjectEmail SubjectKind = "email"
)

// Subject is one data subject, resolved.
type Subject struct {
	Kind SubjectKind
	// MAC is the canonical colon form, and MACHex the twelve lower-case hex digits
	// the erasure statements compare normalised columns against.
	MAC, MACHex string
	// AccountID, Username and SiteID are set for SubjectAccount.
	AccountID, Username, SiteID string
	// PhoneDigits are the number's digit strings — international and, for a
	// Vietnamese number, national — that a column normalised to its digits is
	// compared with. PhoneSpellings are the exact strings a username may hold
	// (`+84…`, `84…`, `0…`). Both are set for SubjectPhone.
	PhoneDigits, PhoneSpellings []string
	// Email is the address, trimmed; matched case-insensitively. Set for
	// SubjectEmail.
	Email string

	// pseudonym is what the audit log names the subject by afterwards
	// (auditpseudonym.go). Set by Erase.
	pseudonym string
}

// ErrInvalidSubject is returned for a MAC that is not twelve hex digits, or a phone
// number that is not eight to fifteen digits.
var ErrInvalidSubject = errors.New("store: not a MAC address")

// PhoneSubject parses a phone number in any spelling a person writes one:
// eight to fifteen digits (E.164's ceiling), an optional leading `+`, and spaces,
// dots, dashes or brackets between them — the contract's `GuestPhoneInput` shape.
// A Vietnamese number is recognised in both forms, `0912345678` and `84912345678`,
// because guests write the first and Foyer stores the second.
func PhoneSubject(raw string) (Subject, error) {
	raw = strings.TrimSpace(raw)
	var d []byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= '0' && c <= '9':
			d = append(d, c)
		case c == '+' && i == 0:
		case c == ' ' || c == '.' || c == '-' || c == '(' || c == ')':
		default:
			return Subject{}, ErrInvalidSubject
		}
	}
	if len(d) < 8 || len(d) > 15 {
		return Subject{}, ErrInvalidSubject
	}
	intl, national := string(d), ""
	switch {
	case len(d) == 10 && d[0] == '0':
		intl, national = "84"+string(d[1:]), string(d)
	case len(d) == 11 && strings.HasPrefix(intl, "84"):
		national = "0" + intl[2:]
	}
	s := Subject{Kind: SubjectPhone, PhoneDigits: []string{intl}, PhoneSpellings: []string{"+" + intl, intl}}
	if national != "" {
		s.PhoneDigits = append(s.PhoneDigits, national)
		s.PhoneSpellings = append(s.PhoneSpellings, national)
	}
	return s, nil
}

// EmailSubject parses an e-mail address with the portal form's own shape check
// (one `@`, something each side, a dot in the domain, no whitespace, at most 254).
func EmailSubject(raw string) (Subject, error) {
	e := strings.TrimSpace(raw)
	at := strings.LastIndexByte(e, '@')
	if len(e) > 254 || strings.ContainsAny(e, " \t\r\n") || strings.Count(e, "@") != 1 ||
		at < 1 || at == len(e)-1 || !strings.Contains(e[at+1:], ".") {
		return Subject{}, ErrInvalidSubject
	}
	return Subject{Kind: SubjectEmail, Email: e}, nil
}

// phoneDigitsSQL is a column reduced to its digits, for comparison with
// `Subject.PhoneDigits`. A sequential scan, as every erasure's is: an erasure is
// rare and the operator is waiting for a complete answer, not a fast one.
func phoneDigitsSQL(column string) string {
	return `regexp_replace(coalesce(` + column + `, ''), '[^0-9]', '', 'g')`
}

// MACSubject parses any vendor spelling of a MAC.
func MACSubject(raw string) (Subject, error) {
	// Shape first: MACOnly keeps hex digits and drops everything else, so twelve hex
	// digits somewhere inside a longer string ("cafe-0011-2233-44") would otherwise
	// pass as a MAC and erase a device nobody named.
	if !looksLikeMAC(raw) {
		return Subject{}, ErrInvalidSubject
	}
	hex := MACOnly(raw)
	return Subject{Kind: SubjectMAC, MAC: NormaliseMAC(hex), MACHex: hex}, nil
}

// looksLikeMAC accepts the four spellings a NAS actually sends: colons, dashes,
// Cisco's dotted triplets, or none.
func looksLikeMAC(raw string) bool {
	s := toLowerASCII(raw)
	isHex := func(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }
	switch len(s) {
	case 12:
		for i := 0; i < 12; i++ {
			if !isHex(s[i]) {
				return false
			}
		}
		return true
	case 17:
		sep := s[2]
		if sep != ':' && sep != '-' {
			return false
		}
		for i := 0; i < 17; i++ {
			if i%3 == 2 {
				if s[i] != sep {
					return false
				}
			} else if !isHex(s[i]) {
				return false
			}
		}
		return true
	case 14:
		for i := 0; i < 14; i++ {
			if i == 4 || i == 9 {
				if s[i] != '.' {
					return false
				}
			} else if !isHex(s[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// Masked is how an erased subject is named afterwards -- in the audit entry, in a
// log line. It must not be the identifier itself: an audit log that keeps the MAC
// it records erasing has not erased it. The last two octets survive so an operator
// can tell two erasures apart and match one to the request that asked for it.
func (s Subject) Masked() string {
	switch s.Kind {
	case SubjectMAC:
		if len(s.MACHex) == 12 {
			return redact.MAC(s.MACHex)
		}
	case SubjectAccount:
		return "account " + s.AccountID
	case SubjectPhone:
		if len(s.PhoneDigits) > 0 {
			return redact.Phone(s.PhoneDigits[0])
		}
	case SubjectEmail:
		return redact.Email(s.Email)
	}
	return string(s.Kind)
}

// AccountSubject resolves a guest account to the identifiers its rows carry.
func (s *Store) AccountSubject(ctx context.Context, accountID string) (Subject, error) {
	a, err := s.Accounts().Get(ctx, accountID)
	if err != nil {
		return Subject{}, err
	}
	return Subject{Kind: SubjectAccount, AccountID: a.ID, Username: a.Username, SiteID: a.SiteID}, nil
}

// ── the column registry ──────────────────────────────────────────────────────

// PersonalColumn is one column that holds, or travels with, a guest identifier.
type PersonalColumn struct {
	Table, Column string
	// Holds is what the value is, in the operator document's words.
	Holds string
	// Identifies lists the subject kinds this column is the identifier *for*: an
	// erasure of that kind matches on this column, and the functional test proves
	// no row still matches afterwards.
	Identifies []SubjectKind
	// WithRow marks a column that is not itself an identifier but describes the
	// subject (the access point they were on, the page they wanted), and goes when
	// its row does.
	WithRow bool
	// CascadeFrom names the table whose erasure removes this row by foreign-key
	// cascade, for a table with no eraser of its own.
	CascadeFrom string
	// NotGuestData says why a column matched by the schema scan is not a guest's
	// personal data and is not erased by a data-subject erasure. Empty for every
	// column that is.
	NotGuestData string
	// Pseudonymised marks a column whose rows are kept and whose identifier an
	// erasure replaces with the subject's pseudonym — the audit log's (00052).
	Pseudonymised bool
}

var (
	registryMu      sync.Mutex
	personalColumns []PersonalColumn
	erasers         []Eraser
	categories      []RetentionCategory
)

// RegisterPersonalColumn adds a column to the registry. Call it from an init in the
// file that owns the table, beside the eraser that removes it.
func RegisterPersonalColumn(c PersonalColumn) {
	registryMu.Lock()
	defer registryMu.Unlock()
	personalColumns = append(personalColumns, c)
}

// PersonalColumns returns the registry, sorted by table and column.
func PersonalColumns() []PersonalColumn {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := slices.Clone(personalColumns)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Column < out[j].Column
	})
	return out
}

func init() {
	for _, c := range []PersonalColumn{
		{Table: "radacct", Column: "callingstationid", Holds: "client MAC (Calling-Station-Id)", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "radacct", Column: "username", Holds: "guest username, or the MAC under MAC authentication", Identifies: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone}},
		{Table: "radacct", Column: "calledstationid", Holds: "access point MAC and SSID", WithRow: true},
		{Table: "radacct", Column: "framedipaddress", Holds: "IP address the guest was given", WithRow: true},
		{Table: "radpostauth", Column: "callingstationid", Holds: "client MAC (Calling-Station-Id)", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "radpostauth", Column: "username", Holds: "username offered at sign-in, or the MAC under MAC authentication", Identifies: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone}},
		{Table: "radpostauth", Column: "calledstationid", Holds: "access point MAC and SSID", WithRow: true},
		{Table: "authorization_events", Column: "client_mac", Holds: "client MAC", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "authorization_events", Column: "username", Holds: "username offered at sign-in (a phone guest's number)", Identifies: []SubjectKind{SubjectAccount, SubjectPhone}},
		{Table: "authorization_events", Column: "ap_mac", Holds: "access point MAC", WithRow: true},
		{Table: "portal_sessions", Column: "client_mac", Holds: "client MAC", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "portal_sessions", Column: "ap_mac", Holds: "access point MAC", WithRow: true},
		{Table: "portal_sessions", Column: "ssid", Holds: "network name", WithRow: true},
		{Table: "portal_sessions", Column: "original_url", Holds: "page the guest was trying to reach", WithRow: true},
		{Table: "portal_grants", Column: "client_mac", Holds: "client MAC", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "portal_grants", Column: "account_id", Holds: "guest account that signed in", Identifies: []SubjectKind{SubjectAccount}},
		{Table: "portal_grants", Column: "ap_mac", Holds: "access point MAC", WithRow: true},
		{Table: "accounts", Column: "username", Holds: "guest account username", Identifies: []SubjectKind{SubjectAccount}},
		{Table: "accounts", Column: "display_name", Holds: "guest account display name", WithRow: true},
		{Table: "account_pap_verifiers", Column: "username", Holds: "802.1X username", Identifies: []SubjectKind{SubjectAccount}, CascadeFrom: "accounts"},
		// A view (00016) FreeRADIUS reads verifiers through: it has no rows of its own,
		// so it holds nothing once account_pap_verifiers does not.
		{Table: "radius_pap_verifiers", Column: "username", Holds: "802.1X username, as FreeRADIUS reads it", Identifies: []SubjectKind{SubjectAccount}, CascadeFrom: "accounts"},

		{Table: "operators", Column: "username", Holds: "operator sign-in name",
			NotGuestData: "an operator, not a guest: removed by deleteOperator"},
		{Table: "operators", Column: "display_name", Holds: "operator display name",
			NotGuestData: "an operator, not a guest: removed by deleteOperator"},
		{Table: "audit_log", Column: "actor_username", Holds: "operator who made a change",
			NotGuestData: "an operator, not a guest: kept for accountability until the audit retention period ends"},
		{Table: "controllers", Column: "username", Holds: "controller API account",
			NotGuestData: "a credential for the venue's own controller, not a person using the network"},
		{Table: "access_points", Column: "mac", Holds: "access point MAC",
			NotGuestData: "network equipment, not a guest device"},
	} {
		RegisterPersonalColumn(c)
	}
}

// ── erasers ──────────────────────────────────────────────────────────────────

// Eraser removes every row of one table that names one data subject.
type Eraser struct {
	Table string
	Kinds []SubjectKind
	// Order runs lower first within one connection's transaction. Children before
	// parents: portal sessions before the grants they point at, grants before the
	// account.
	Order int
	Erase func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error)
	// Pseudonymises marks an eraser that keeps its rows and rewrites the
	// identifier in them; its count is reported apart from the rows removed.
	Pseudonymises bool

	// onRetention puts the eraser on the foyer_retention connection. Only
	// retention.go sets it, because only that file may name the FreeRADIUS tables in
	// a DELETE.
	onRetention bool
}

// RegisterEraser adds an application-side eraser: it runs on the application's
// connection, as foyer_app, inside the same transaction as every other one.
func RegisterEraser(e Eraser) {
	e.onRetention = false
	registerEraser(e)
}

func registerEraser(e Eraser) {
	registryMu.Lock()
	defer registryMu.Unlock()
	erasers = append(erasers, e)
}

// Erasers returns the registered erasers for one subject kind, in run order.
func Erasers(kind SubjectKind) []Eraser {
	registryMu.Lock()
	defer registryMu.Unlock()
	var out []Eraser
	for _, e := range erasers {
		if slices.Contains(e.Kinds, kind) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

func init() {
	// portal_sessions first: for an account, the sessions are found through the
	// grants the next eraser removes.
	RegisterEraser(Eraser{Table: "portal_sessions", Kinds: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone}, Order: 10,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectMAC {
				return execCount(ctx, tx, `DELETE FROM portal_sessions WHERE client_mac = $1::macaddr`, s.MAC)
			}
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, `
					DELETE FROM portal_sessions WHERE grant_id IN (`+phoneGrants+`)`, s.PhoneDigits)
			}
			return execCount(ctx, tx, `
				DELETE FROM portal_sessions
				WHERE grant_id IN (SELECT id FROM portal_grants WHERE account_id = $1)`, s.AccountID)
		}})
	// Deleting a grant cascades to its authorization_events (00014, ON DELETE
	// CASCADE); the retention connection has already removed the ones that name the
	// subject directly.
	RegisterEraser(Eraser{Table: "portal_grants", Kinds: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone}, Order: 20,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectMAC {
				return execCount(ctx, tx, `DELETE FROM portal_grants WHERE client_mac = $1::macaddr`, s.MAC)
			}
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, `DELETE FROM portal_grants WHERE id IN (`+phoneGrants+`)`, s.PhoneDigits)
			}
			return execCount(ctx, tx, `DELETE FROM portal_grants WHERE account_id = $1`, s.AccountID)
		}})
	// The account itself. Its NT hash and 802.1X verifier go with it by cascade
	// (00005, 00016), which is why account_pap_verifiers has no eraser of its own.
	RegisterEraser(Eraser{Table: "accounts", Kinds: []SubjectKind{SubjectAccount}, Order: 90,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			return execCount(ctx, tx, `DELETE FROM accounts WHERE id = $1`, s.AccountID)
		}})
}

// phoneGrants selects the grants a phone subject was verified for; `$1` is
// `Subject.PhoneDigits`.
var phoneGrants = `SELECT id FROM portal_grants WHERE ` + phoneDigitsSQL("phone") + ` = ANY($1)`

// ErasureReport is what one erasure removed, per table. Every table an eraser covers
// for the subject's kind is listed, including the ones that held nothing: a report
// that omits a table cannot be told apart from an erasure that never looked there.
type ErasureReport struct {
	Subject Subject
	At      time.Time
	Removed []ErasedTable
	Total   int64
	// Pseudonymised lists the tables whose rows were kept with the identifier
	// replaced (the audit log), and how many rows each. Not counted in Total.
	Pseudonymised []ErasedTable
}

// ErasedTable is one table's count.
type ErasedTable struct {
	Table string
	Rows  int64
}

// Erase removes every row naming the subject, from every registered table.
//
// ── the order, and the one window it leaves ──────────────────────────────────
//
// The FreeRADIUS side runs first, on its own connection and in one transaction; the
// application side second, in another. They cannot be one transaction: they are two
// roles on two connections, and a design that held the application's transaction
// open while the retention connection deleted cascaded rows would deadlock against
// itself where PostgreSQL cannot see it. Erasing is idempotent, so the failure
// between the two commits -- FreeRADIUS rows gone, Foyer rows still there -- is
// repaired by running the erasure again, and the caller is told it failed. The
// caller writes the audit entry only after both succeed.
//
// ── the audit entry is part of the erasure ───────────────────────────────────
//
// `EraseRecorded` takes the entry as a function of the report and inserts it in
// the application's transaction, after every eraser: if it cannot be written the
// Foyer side rolls back and the caller is told the erasure failed. What that
// cannot cover is the FreeRADIUS side, which committed first for the reason above;
// it is idempotent, so the retry the failure asks for is also the one that records
// it. The window is logged at ERROR by the caller, never silent.
func Erase(ctx context.Context, app *Store, ret RetentionEraser, s Subject) (ErasureReport, error) {
	return EraseRecorded(ctx, app, ret, s, nil)
}

// EraseRecorded is Erase with the audit entry written atomically with the
// application side. A nil record writes none (tests, and Erase).
func EraseRecorded(ctx context.Context, app *Store, ret RetentionEraser, s Subject, record func(ErasureReport) (*AuditEntry, error)) (ErasureReport, error) {
	if app == nil || ret == nil {
		return ErasureReport{}, errors.New("store: erasure needs both the application and the retention connection")
	}
	var err error
	if s.pseudonym, err = s.AuditPseudonym(app.Keys()); err != nil {
		return ErasureReport{}, err
	}
	var onRet, onApp []Eraser
	for _, e := range Erasers(s.Kind) {
		if e.onRetention {
			onRet = append(onRet, e)
		} else {
			onApp = append(onApp, e)
		}
	}

	counts, err := ret.EraseRetained(ctx, s)
	if err != nil {
		return ErasureReport{}, err
	}
	var rep ErasureReport
	err = app.tx(ctx, func(tx pgx.Tx) error {
		for _, e := range onApp {
			n, err := e.Erase(ctx, tx, s)
			if err != nil {
				return fmt.Errorf("store: erasing from %s: %w", e.Table, err)
			}
			counts[e.Table] += n
		}
		rep = erasureReport(s, append(onRet, onApp...), counts)
		if record == nil {
			return nil
		}
		entry, err := record(rep)
		if err != nil {
			return err
		}
		if entry == nil {
			return errors.New("store: an erasure's audit entry is required and none was given")
		}
		return appendAudit(ctx, tx, entry)
	})
	if err != nil {
		return ErasureReport{}, err
	}
	return rep, nil
}

func erasureReport(s Subject, ran []Eraser, counts map[string]int64) ErasureReport {
	rep := ErasureReport{Subject: s, At: time.Now().UTC()}
	seen := map[string]bool{}
	for _, e := range ran {
		if seen[e.Table] {
			continue
		}
		seen[e.Table] = true
		t := ErasedTable{Table: e.Table, Rows: counts[e.Table]}
		if e.Pseudonymises {
			rep.Pseudonymised = append(rep.Pseudonymised, t)
			continue
		}
		rep.Removed = append(rep.Removed, t)
		rep.Total += t.Rows
	}
	sort.Slice(rep.Removed, func(i, j int) bool { return rep.Removed[i].Table < rep.Removed[j].Table })
	sort.Slice(rep.Pseudonymised, func(i, j int) bool { return rep.Pseudonymised[i].Table < rep.Pseudonymised[j].Table })
	return rep
}

// ── retention categories ─────────────────────────────────────────────────────

// SweepTargets is what a category's sweep may use: the application's connection
// for Foyer's own mutable tables, the retention connection for everything else.
type SweepTargets struct {
	App       *Store
	Retention *Retention
	// BatchSize is the rows one DELETE removes (`retentionBatchSize`). Zero is the
	// default.
	BatchSize int
}

// RetentionCategory is one kind of personal data with its own period.
type RetentionCategory struct {
	ID string
	// DefaultDays applies until an operator chooses otherwise. Zero means *keep
	// until erased or deleted*, and only a category with MinDays 0 may have it.
	DefaultDays, MinDays, MaxDays int
	// Tables are what the sweep removes rows from, for the console and the document.
	Tables []string
	// Sweep deletes what is older than cutoff and returns how many rows went.
	Sweep func(ctx context.Context, t SweepTargets, cutoff time.Time) (int64, error)
}

// RegisterRetentionCategory adds a category. A later feature that stores personal
// data in a table of its own registers one beside its eraser.
func RegisterRetentionCategory(c RetentionCategory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	categories = append(categories, c)
}

// RetentionCategories returns the registered categories in registration order.
func RetentionCategories() []RetentionCategory {
	registryMu.Lock()
	defer registryMu.Unlock()
	return slices.Clone(categories)
}

// RetentionCategoryByID finds one category.
func RetentionCategoryByID(id string) (RetentionCategory, bool) {
	for _, c := range RetentionCategories() {
		if c.ID == id {
			return c, true
		}
	}
	return RetentionCategory{}, false
}

// maxRetentionDays is ten years. Anything longer is not a retention period.
const maxRetentionDays = 3650

func init() {
	// The defaults, and why each is the number it is. All are starting points an
	// operator adjusts to their own purpose; none is a legal minimum, and a venue
	// under an obligation to keep records longer raises it (Law 91/2025/QH15 Art. 3
	// clause 3: "unless otherwise prescribed by law").
	//
	// accounting 90: a quarter, enough to answer a billing or abuse enquiry about
	//   last month's sessions. PacketFence keeps raw radacct for 1 day and
	//   aggregates; Foyer has no aggregate, so its session history *is* this table.
	// authAttempts 30: diagnostic -- "why can't this guest get online" is a question
	//   about this week. PacketFence's auth_log window is 1 week.
	// portalGrants 90: the portal's twin of accounting ("what the venue decided"),
	//   kept as long so the two can still be read together.
	// audit 730: operator accountability outlives guest data. PacketFence keeps its
	//   admin audit log 6 months; two years covers an annual review with margin.
	// guestAccounts 0: an account is an identity an operator created, and deleting
	//   one on a timer is PacketFence's node_cleanup, which ships disabled.
	for _, c := range []RetentionCategory{
		{ID: "accounting", DefaultDays: 90, MinDays: 1, MaxDays: maxRetentionDays,
			Tables: []string{"radacct"},
			Sweep: func(ctx context.Context, t SweepTargets, cutoff time.Time) (int64, error) {
				return t.Retention.SweepAccounting(ctx, cutoff)
			}},
		{ID: "authAttempts", DefaultDays: 30, MinDays: 1, MaxDays: maxRetentionDays,
			Tables: []string{"radpostauth", "authorization_events"},
			Sweep: func(ctx context.Context, t SweepTargets, cutoff time.Time) (int64, error) {
				a, err := t.Retention.SweepAuthLog(ctx, cutoff)
				if err != nil {
					return a, err
				}
				b, err := t.Retention.SweepAuthorizationEvents(ctx, cutoff)
				return a + b, err
			}},
		{ID: "portalGrants", DefaultDays: 90, MinDays: 1, MaxDays: maxRetentionDays,
			Tables: []string{"portal_grants"},
			Sweep: func(ctx context.Context, t SweepTargets, cutoff time.Time) (int64, error) {
				// Aged by when the grant ENDED (grantEnded), never simply by when it
				// was granted: an unlimited grant still in use is not removed from
				// under the guest who holds it.
				return sweepInBatches(ctx, t.App.pool, `
					DELETE FROM portal_grants WHERE id IN (
						SELECT g.id FROM portal_grants g
						WHERE `+grantEnded+` < $1
						ORDER BY g.granted_at LIMIT $2)`, cutoff)
			}},
		{ID: "audit", DefaultDays: 730, MinDays: 30, MaxDays: maxRetentionDays,
			Tables: []string{"audit_log"},
			Sweep: func(ctx context.Context, t SweepTargets, cutoff time.Time) (int64, error) {
				return t.Retention.SweepAudit(ctx, cutoff)
			}},
		{ID: "guestAccounts", DefaultDays: 0, MinDays: 0, MaxDays: maxRetentionDays,
			Tables: []string{"accounts"},
			Sweep: func(ctx context.Context, t SweepTargets, cutoff time.Time) (int64, error) {
				// Unused since the cutoff: never signed in and created before it, or
				// last signed in before it. The account's grants go first, because a
				// grant row's CHECK forbids an account-method grant with no account.
				var n int64
				err := t.App.tx(ctx, func(tx pgx.Tx) error {
					const stale = `SELECT id FROM accounts WHERE coalesce(last_login_at, created_at) < $1`
					if _, err := tx.Exec(ctx, `DELETE FROM portal_sessions WHERE grant_id IN (
						SELECT id FROM portal_grants WHERE account_id IN (`+stale+`))`, cutoff); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `DELETE FROM portal_grants WHERE account_id IN (`+stale+`)`, cutoff); err != nil {
						return err
					}
					tag, err := tx.Exec(ctx, `DELETE FROM accounts WHERE id IN (`+stale+`)`, cutoff)
					n = tag.RowsAffected()
					return err
				})
				return n, err
			}},
	} {
		RegisterRetentionCategory(c)
	}
}

// grantEnded is when grant `g` stopped meaning anything, which is what the
// portalGrants period is counted from. The first of these that exists:
//
//  1. **It expired or was revoked**, whichever came first.
//  2. **The same device was granted again at the same venue.** A grant is the
//     record of one decision; the next decision about that device supersedes it.
//  3. **The device was last heard from at this venue** — the newest accounting
//     activity (stop, else interim update, else start) for any spelling of its MAC
//     on a NAS of this site, and never earlier than the grant itself.
//  4. Otherwise when it was granted.
//
// (1) else (4) alone would delete an unlimited grant still in use 90 days after
// it was granted while its guest was online. Nothing authorizes from a grant row —
// sessionplan does not join them and a voucher's single use is `vouchers.state` —
// so that would be a record-keeping defect: the console losing the grant of a
// guest it still shows online. PacketFence ages a node by its `last_seen`, not by its registration
// date, for the same reason; (3) is that rule on the rows Foyer has.
//
// (3) reads `radacct` through the same longest-prefix client match every session
// read uses, so a device on another venue's NAS keeps nothing alive here. A
// deployment whose accounting retention is shorter than its grant retention loses
// the activity first, and the grant then ages from its last activity still on
// record — the shorter period wins, which is the direction retention should fail.
var grantEnded = `coalesce(
	least(g.expires_at, g.revoked_at),
	(SELECT min(n.granted_at) FROM portal_grants n
	  WHERE n.site_id = g.site_id AND n.client_mac = g.client_mac AND n.granted_at > g.granted_at),
	greatest(g.granted_at, (
		SELECT max(coalesce(ra.acctstoptime, ra.acctupdatetime, ra.acctstarttime))
		FROM radacct ra` + clientForNAS + `
		WHERE ra.callingstationid = ANY(` + macSpellingsSQL("g.client_mac") + `)
		  AND rc.site_id = g.site_id)))`

// macSpellingsSQL is `MACSpellings` in SQL, for a `macaddr` column: the colon,
// dash, dotted and bare forms, each in lower and upper case — the eight spellings a
// NAS puts in Calling-Station-Id, matched with `= ANY` so `radacct`'s index answers.
func macSpellingsSQL(col string) string {
	colon := col + "::text"
	bare := "replace(" + colon + ", ':', '')"
	dash := "replace(" + colon + ", ':', '-')"
	dotted := "substr(" + bare + ", 1, 4) || '.' || substr(" + bare + ", 5, 4) || '.' || substr(" + bare + ", 9, 4)"
	var forms []string
	for _, f := range []string{colon, dash, dotted, bare} {
		forms = append(forms, f, "upper("+f+")")
	}
	return "ARRAY[" + strings.Join(forms, ", ") + "]"
}

// ── policies: what an operator chose ─────────────────────────────────────────

// RetentionPolicy is one category's effective period and what the sweep last did.
type RetentionPolicy struct {
	Category  RetentionCategory
	Days      int
	IsDefault bool
	UpdatedBy *string
	LastSweep *RetentionSweep
}

// RetentionSweep is the sweep's own record of one category.
type RetentionSweep struct {
	At      time.Time
	Cutoff  *time.Time
	Removed int64
	Failed  bool
}

// PrivacyRepo reads and writes retention policies and sweep records. It runs on the
// application's connection; it deletes nothing.
type PrivacyRepo struct{ s *Store }

// Privacy returns the retention-policy repository.
func (s *Store) Privacy() *PrivacyRepo { return &PrivacyRepo{s: s} }

// RetentionPolicies returns every registered category with its effective period.
func (r *PrivacyRepo) RetentionPolicies(ctx context.Context) ([]RetentionPolicy, error) {
	overrides := map[string]struct {
		days int
		by   *string
	}{}
	rows, err := r.s.pool.Query(ctx, `SELECT category, days, updated_by FROM retention_policies`)
	if err != nil {
		return nil, mapErr(err)
	}
	for rows.Next() {
		var cat string
		var days int
		var by *string
		if err := rows.Scan(&cat, &days, &by); err != nil {
			rows.Close()
			return nil, err
		}
		overrides[cat] = struct {
			days int
			by   *string
		}{days, by}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sweeps := map[string]*RetentionSweep{}
	rows, err = r.s.pool.Query(ctx,
		`SELECT category, last_run_at, last_cutoff, last_removed, last_failed FROM retention_sweeps`)
	if err != nil {
		return nil, mapErr(err)
	}
	for rows.Next() {
		var cat string
		var sw RetentionSweep
		if err := rows.Scan(&cat, &sw.At, &sw.Cutoff, &sw.Removed, &sw.Failed); err != nil {
			rows.Close()
			return nil, err
		}
		sweeps[cat] = &sw
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []RetentionPolicy
	for _, c := range RetentionCategories() {
		p := RetentionPolicy{Category: c, Days: c.DefaultDays, IsDefault: true, LastSweep: sweeps[c.ID]}
		if o, ok := overrides[c.ID]; ok {
			p.Days, p.IsDefault, p.UpdatedBy = o.days, false, o.by
		}
		out = append(out, p)
	}
	return out, nil
}

// ErrRetentionOutOfRange is returned for a period outside a category's bounds, and
// ErrUnknownRetentionCategory for a category the code does not register.
var (
	ErrRetentionOutOfRange      = errors.New("store: retention period out of range")
	ErrUnknownRetentionCategory = errors.New("store: unknown retention category")
)

// RetentionChange sets one category's period. Nil Days restores the default.
type RetentionChange struct {
	Category string
	Days     *int
}

// SetRetention applies every change in one transaction, or none of them.
func (r *PrivacyRepo) SetRetention(ctx context.Context, changes []RetentionChange, actor string) error {
	return r.SetRetentionRecorded(ctx, changes, actor, nil)
}

// SetRetentionRecorded is SetRetention with its audit entry written in the same
// transaction: an entry that cannot be built or written rolls the
// change back. A nil record writes none.
func (r *PrivacyRepo) SetRetentionRecorded(ctx context.Context, changes []RetentionChange, actor string, record func() (*AuditEntry, error)) error {
	for _, ch := range changes {
		c, ok := RetentionCategoryByID(ch.Category)
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownRetentionCategory, ch.Category)
		}
		if ch.Days != nil && (*ch.Days < c.MinDays || *ch.Days > c.MaxDays) {
			return fmt.Errorf("%w: %s must be between %d and %d days", ErrRetentionOutOfRange, c.ID, c.MinDays, c.MaxDays)
		}
	}
	return r.s.tx(ctx, func(tx pgx.Tx) error {
		for _, ch := range changes {
			if ch.Days == nil {
				if _, err := tx.Exec(ctx, `DELETE FROM retention_policies WHERE category = $1`, ch.Category); err != nil {
					return mapErr(err)
				}
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO retention_policies (category, days, updated_by, updated_at)
				VALUES ($1, $2, $3, now())
				ON CONFLICT (tenant_id, category)
				DO UPDATE SET days = EXCLUDED.days, updated_by = EXCLUDED.updated_by, updated_at = now()`,
				ch.Category, *ch.Days, actor); err != nil {
				return mapErr(err)
			}
		}
		if record == nil {
			return nil
		}
		entry, err := record()
		if err != nil {
			return err
		}
		if entry == nil {
			return errors.New("store: a retention change's audit entry is required and none was given")
		}
		return appendAudit(ctx, tx, entry)
	})
}

func (r *PrivacyRepo) recordSweep(ctx context.Context, category string, at time.Time, cutoff *time.Time, removed int64, failed bool) error {
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO retention_sweeps (category, last_run_at, last_cutoff, last_removed, last_failed)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, category) DO UPDATE
		SET last_run_at = EXCLUDED.last_run_at, last_cutoff = EXCLUDED.last_cutoff,
		    last_removed = EXCLUDED.last_removed, last_failed = EXCLUDED.last_failed`,
		category, at, cutoff, removed, failed)
	return mapErr(err)
}

// SweepRetention runs every category once against the periods in force at `now`,
// records what each did, and logs it. It keeps going past a failing category: one
// table that cannot be swept is not a reason to keep every other one forever.
func SweepRetention(ctx context.Context, t SweepTargets, now time.Time, logger *slog.Logger) (int64, error) {
	if t.App == nil || t.Retention == nil {
		return 0, errors.New("store: the retention sweep needs both the application and the retention connection")
	}
	if logger == nil {
		logger = slog.Default()
	}
	policies, err := t.App.Privacy().RetentionPolicies(ctx)
	if err != nil {
		return 0, err
	}
	ctx = WithSweepBatch(ctx, t.BatchSize)
	var total int64
	var errs []error
	for _, p := range policies {
		if p.Days == 0 {
			// Kept until erased. Recorded all the same, so "when did the sweep last
			// look at this" has an answer even for a category it leaves alone.
			_ = t.App.Privacy().recordSweep(ctx, p.Category.ID, now, nil, 0, false)
			continue
		}
		days := p.Days
		if days < p.Category.MinDays {
			// The floor, re-applied here as well as by the database's CHECK
			// (00051): a period the API would have refused is never
			// what a sweep deletes by, whatever wrote it.
			logger.Error("retention period below its floor; sweeping at the floor",
				"category", p.Category.ID, "stored", p.Days, "floor", p.Category.MinDays)
			days = p.Category.MinDays
		}
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
		n, err := p.Category.Sweep(ctx, t, cutoff)
		total += n
		if rerr := t.App.Privacy().recordSweep(ctx, p.Category.ID, now, &cutoff, n, err != nil); rerr != nil {
			errs = append(errs, rerr)
		}
		if err != nil {
			// The category and the count, never the statement's error text on its own
			// line: an error can quote a value from the row it failed on.
			logger.Error("retention sweep failed", "category", p.Category.ID, "removedBeforeFailure", n, "error", err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", p.Category.ID, err))
			continue
		}
		logger.Info("retention sweep", "category", p.Category.ID, "days", days,
			"cutoff", cutoff.UTC().Format(time.RFC3339), "removed", n, "tables", p.Category.Tables)
	}
	return total, errors.Join(errs...)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// sweepBatchKey carries the operator's batch size (`retentionBatchSize`,
// MaintenanceLimits) from SweepRetention down to every category's DELETE loop.
//
// A context value rather than a parameter, and deliberately: every category's
// Sweep closure and the four exported `Retention.Sweep*` methods would otherwise
// change signature for one integer that none of them decides, and the default is
// what a caller that never heard of the setting -- a test, an erasure -- should get.
type sweepBatchKey struct{}

// WithSweepBatch returns a context whose retention DELETEs remove n rows each. A
// value below one is ignored.
func WithSweepBatch(ctx context.Context, n int) context.Context {
	if n < 1 {
		return ctx
	}
	return context.WithValue(ctx, sweepBatchKey{}, n)
}

// sweepBatchFrom is the batch in force: the operator's, else the default. PacketFence
// uses 100 per task per minute; this runs less often, so it takes more per statement
// and loops.
func sweepBatchFrom(ctx context.Context) int {
	if n, ok := ctx.Value(sweepBatchKey{}).(int); ok && n > 0 {
		return n
	}
	return MaintenanceLimits["retentionBatchSize"].Default
}

// sweepInBatches runs a `$1 = cutoff, $2 = batch` DELETE until a pass removes fewer
// rows than a batch, or the context ends.
func sweepInBatches(ctx context.Context, pool *pgxpool.Pool, stmt string, cutoff time.Time) (int64, error) {
	var total int64
	batch := sweepBatchFrom(ctx)
	for {
		tag, err := pool.Exec(ctx, stmt, cutoff, batch)
		if err != nil {
			return total, mapErr(err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(batch) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

func execCount(ctx context.Context, tx pgx.Tx, stmt string, args ...any) (int64, error) {
	tag, err := tx.Exec(ctx, stmt, args...)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}
