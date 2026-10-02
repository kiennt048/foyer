package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Guest details and marketing consent (migration 00023).
//
// ── what the field does, and what this copies ────────────────────────────────
//
// Purple — the product this market's venues compare against — ships a registration
// form with separate, unticked checkboxes per purpose and says in its own guidance
// that marketing may not be the price of free Wi-Fi. Meraki's sign-on splash takes a
// name and an e-mail and leaves consent to the administrator; UniFi's own portal
// collects nothing, and the add-ons that do (Spotipo, Art of WiFi) add a
// configurable form. Vietnam's Decree 356/2025/ND-CP forbids pre-ticked boxes and
// silence-as-consent, and asks for consent verifiable as to time and content, per
// purpose, with an easy way out. The three things taken from that: a closed set of
// fields each optional unless the venue says otherwise; a consent that exists only
// as a row written when the box was ticked, carrying the SHA-256 of the exact text;
// and withdrawal from the portal itself as well as from the console.
//
// ── the two records ──────────────────────────────────────────────────────────
//
// `guest_details` lives and dies with its grant and is never a mailing list.
// `marketing_consents` holds a row only for a guest who ticked the box. See the
// migration for why they are apart.

// GuestDataField is one field a venue may ask for. The contract's closed enum.
type GuestDataField string

const (
	FieldName  GuestDataField = "name"
	FieldPhone GuestDataField = "phone"
	FieldEmail GuestDataField = "email"
)

// GuestDataFieldSetting is one field and whether a guest must fill it in.
type GuestDataFieldSetting struct {
	Field    GuestDataField `json:"field"`
	Required bool           `json:"required"`
}

// GuestDataSettings is what one venue asks for.
type GuestDataSettings struct {
	SiteID         string
	Fields         []GuestDataFieldSetting
	ConsentEnabled bool
	// ConsentText is the operator's text per language, trimmed; a language with no
	// text is absent.
	ConsentText map[string]string
	// Versions is ConsentVersion of each text in ConsentText.
	Versions  map[string]string
	UpdatedAt time.Time
}

// ConsentVersion is the SHA-256, lower-case hex, of a consent text as shown:
// UTF-8, surrounding whitespace trimmed. Both servers compute it the same way.
func ConsentVersion(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

// GuestDataRepo owns the four tables of migration 00023.
type GuestDataRepo struct{ s *Store }

// GuestData returns the guest-data repository.
func (s *Store) GuestData() *GuestDataRepo { return &GuestDataRepo{s: s} }

// Settings returns a venue's settings. A venue that never saved any collects
// nothing: no fields and consent off — the answer, not an error.
func (r *GuestDataRepo) Settings(ctx context.Context, siteID string) (*GuestDataSettings, error) {
	out := &GuestDataSettings{SiteID: siteID, ConsentText: map[string]string{}, Versions: map[string]string{}}
	var fields, text []byte
	err := r.s.pool.QueryRow(ctx, `
		SELECT fields, consent_enabled, consent_text, updated_at
		FROM guest_data_settings WHERE site_id = $1`, siteID).
		Scan(&fields, &out.ConsentEnabled, &text, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		out.Fields = []GuestDataFieldSetting{}
		return out, nil
	}
	if err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal(fields, &out.Fields); err != nil {
		return nil, err
	}
	if out.Fields == nil {
		out.Fields = []GuestDataFieldSetting{}
	}
	if err := json.Unmarshal(text, &out.ConsentText); err != nil {
		return nil, err
	}
	for lang, body := range out.ConsentText {
		out.Versions[lang] = ConsentVersion(body)
	}
	return out, nil
}

// PutSettings replaces a venue's settings and publishes each consent text under its
// version. A text published once is kept: a consent given under it names it.
func (r *GuestDataRepo) PutSettings(ctx context.Context, in *GuestDataSettings) error {
	text := map[string]string{}
	for lang, body := range in.ConsentText {
		if t := strings.TrimSpace(body); t != "" {
			text[lang] = t
		}
	}
	fields := in.Fields
	if fields == nil {
		fields = []GuestDataFieldSetting{}
	}
	rawFields, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	rawText, err := json.Marshal(text)
	if err != nil {
		return err
	}
	return r.s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO guest_data_settings (site_id, fields, consent_enabled, consent_text, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (tenant_id, site_id) DO UPDATE SET
				fields = EXCLUDED.fields, consent_enabled = EXCLUDED.consent_enabled,
				consent_text = EXCLUDED.consent_text, updated_at = now()`,
			in.SiteID, rawFields, in.ConsentEnabled, rawText); err != nil {
			return mapErr(err)
		}
		for lang, body := range text {
			if _, err := tx.Exec(ctx, `
				INSERT INTO marketing_consent_texts (site_id, version, lang, body)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (tenant_id, site_id, version) DO NOTHING`,
				in.SiteID, ConsentVersion(body), lang, body); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

// GuestDetails is what a guest typed into a sign-in form, bound to its grant.
type GuestDetails struct {
	GrantID, SiteID    string
	Name, Phone, Email *string
}

// RecordDetails stores a guest's details beside the grant they were given for. A
// submission with every field blank records nothing.
func (r *GuestDataRepo) RecordDetails(ctx context.Context, d GuestDetails) error {
	if d.Name == nil && d.Phone == nil && d.Email == nil {
		return nil
	}
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO guest_details (grant_id, site_id, name, phone, email)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (grant_id) DO NOTHING`, d.GrantID, d.SiteID, d.Name, d.Phone, d.Email)
	return mapErr(err)
}

// Details returns what was recorded for one grant.
func (r *GuestDataRepo) Details(ctx context.Context, grantID string) (*GuestDetails, error) {
	var d GuestDetails
	err := r.s.pool.QueryRow(ctx, `
		SELECT grant_id, site_id, name, phone, email FROM guest_details WHERE grant_id = $1`, grantID).
		Scan(&d.GrantID, &d.SiteID, &d.Name, &d.Phone, &d.Email)
	if err != nil {
		return nil, mapErr(err)
	}
	return &d, nil
}

// MarketingConsent is one consent a guest ticked.
type MarketingConsent struct {
	ID, SiteID, ClientMAC string
	GrantID               *string
	Name, Phone, Email    *string
	Lang                  string
	Version               string
	Text                  string
	ConsentedAt           time.Time
	WithdrawnAt           *time.Time
	WithdrawnBy           *string
}

// ConsentGiven is what the portal hands over when a guest ticked the box.
type ConsentGiven struct {
	SiteID, ClientMAC  string
	GrantID            *string
	Name, Phone, Email *string
	// Version is what the page submitted: the SHA-256 of the text it rendered.
	Version string
}

// ErrUnknownConsentVersion: the submitted version is not a text this venue ever
// published, so there is no sentence the guest can be said to have agreed to.
var ErrUnknownConsentVersion = errors.New("store: not a consent text this venue published")

// RecordConsent writes one consent. The language is the text's own, read from the
// published row rather than from the form: the guest agreed to a sentence, and the
// sentence knows which language it is in.
func (r *GuestDataRepo) RecordConsent(ctx context.Context, c ConsentGiven) (*MarketingConsent, error) {
	id, err := consentID()
	if err != nil {
		return nil, err
	}
	var out MarketingConsent
	err = r.s.pool.QueryRow(ctx, `
		INSERT INTO marketing_consents (id, site_id, client_mac, grant_id, account_id, name, phone, email, lang, consent_version)
		SELECT $1, t.site_id, $3::macaddr, $4,
		       -- The account the grant was an account sign-in for, kept on the consent
		       -- itself: the grant ages out (SET NULL) and an erasure by account must
		       -- still find this row (00051).
		       (SELECT g.account_id FROM portal_grants g WHERE g.id = $4),
		       $5, $6, $7, t.lang, t.version
		FROM marketing_consent_texts t
		WHERE t.site_id = $2 AND t.version = $8
		RETURNING `+consentColumnsNoText,
		id, c.SiteID, c.ClientMAC, c.GrantID, c.Name, c.Phone, c.Email, c.Version).
		Scan(consentScanTargets(&out)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownConsentVersion
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return &out, nil
}

const consentColumnsNoText = `id, site_id, client_mac::text, grant_id, name, phone, email, lang,
	consent_version, consented_at, withdrawn_at, withdrawn_by`

func consentScanTargets(c *MarketingConsent) []any {
	return []any{&c.ID, &c.SiteID, &c.ClientMAC, &c.GrantID, &c.Name, &c.Phone, &c.Email, &c.Lang,
		&c.Version, &c.ConsentedAt, &c.WithdrawnAt, &c.WithdrawnBy}
}

const consentSelect = `
	SELECT c.id, c.site_id, c.client_mac::text, c.grant_id, c.name, c.phone, c.email, c.lang,
	       c.consent_version, c.consented_at, c.withdrawn_at, c.withdrawn_by, t.body
	FROM marketing_consents c
	JOIN marketing_consent_texts t ON t.site_id = c.site_id AND t.version = c.consent_version`

func scanConsent(row interface{ Scan(...any) error }) (*MarketingConsent, error) {
	var c MarketingConsent
	if err := row.Scan(append(consentScanTargets(&c), &c.Text)...); err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

// consentID is sortable by creation: newest-first is `ORDER BY id DESC`, which is
// what the cursor pages on.
func consentID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("mc-%016x%s", time.Now().UnixNano(), hex.EncodeToString(b[:])), nil
}

// Consent returns one consent at one venue.
func (r *GuestDataRepo) Consent(ctx context.Context, siteID, id string) (*MarketingConsent, error) {
	return scanConsent(r.s.pool.QueryRow(ctx, consentSelect+` WHERE c.site_id = $1 AND c.id = $2`, siteID, id))
}

// withdraw is the one statement both withdrawals are. The contact goes in the same
// UPDATE that marks the row withdrawn, so no reader ever sees a withdrawn consent
// that still carries an e-mail address — and the CHECK in 00023 forbids one.
const withdraw = `
	UPDATE marketing_consents
	SET withdrawn_at = now(), withdrawn_by = $3, name = NULL, phone = NULL, email = NULL
	WHERE site_id = $1 AND withdrawn_at IS NULL AND `

// Withdraw withdraws one consent on a guest's behalf. Idempotent: a consent
// already withdrawn is returned as it stands.
func (r *GuestDataRepo) Withdraw(ctx context.Context, siteID, id, by string) (*MarketingConsent, error) {
	if _, err := r.s.pool.Exec(ctx, withdraw+`id = $2`, siteID, id, by); err != nil {
		return nil, mapErr(err)
	}
	return r.Consent(ctx, siteID, id)
}

// WithdrawDevice withdraws every active consent one device gave at one venue, and
// says how many.
func (r *GuestDataRepo) WithdrawDevice(ctx context.Context, siteID, mac, by string) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, withdraw+`client_mac = $2::macaddr`, siteID, mac, by)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

// ConsentFilter narrows the list.
type ConsentFilter struct {
	ListOptions
	// State is "", "active" or "withdrawn".
	State string
}

// Consents lists one venue's consents, newest first.
func (r *GuestDataRepo) Consents(ctx context.Context, f ConsentFilter) (Page[MarketingConsent], error) {
	limit := clampLimit(f.Limit)
	after, err := decodeCursor(f.Cursor)
	if err != nil {
		return Page[MarketingConsent]{}, err
	}
	rows, err := r.s.pool.Query(ctx, consentSelect+`
		WHERE c.site_id = $1
		  AND ($2 = '' OR ($2 = 'active') = (c.withdrawn_at IS NULL))
		  AND ($3 = '' OR c.id < $3)
		ORDER BY c.id DESC
		LIMIT $4`, f.SiteID, f.State, after, limit+1)
	if err != nil {
		return Page[MarketingConsent]{}, mapErr(err)
	}
	defer rows.Close()
	var items []MarketingConsent
	for rows.Next() {
		c, err := scanConsent(rows)
		if err != nil {
			return Page[MarketingConsent]{}, err
		}
		items = append(items, *c)
	}
	if err := rows.Err(); err != nil {
		return Page[MarketingConsent]{}, mapErr(err)
	}
	return paginate(items, limit, func(c MarketingConsent) string { return c.ID }), nil
}

// ConsentsBetween visits the venue's consents given or withdrawn in `[from, to)`,
// by when they were given, oldest first — the export's rows.
func (r *GuestDataRepo) ConsentsBetween(ctx context.Context, siteID string, from, to time.Time, visit func(MarketingConsent) error) error {
	rows, err := r.s.pool.Query(ctx, consentSelect+`
		WHERE c.site_id = $1
		  AND ((c.consented_at >= $2 AND c.consented_at < $3)
		    OR (c.withdrawn_at >= $2 AND c.withdrawn_at < $3))
		ORDER BY c.consented_at, c.id`, siteID, from, to)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanConsent(rows)
		if err != nil {
			return err
		}
		if err := visit(*c); err != nil {
			return stopped(err)
		}
	}
	return mapErr(rows.Err())
}

// ── the registries (privacy.go) ──────────────────────────────────────────────

func init() {
	for _, c := range []PersonalColumn{
		{Table: "guest_details", Column: "name", Holds: "name a guest gave at sign-in", WithRow: true},
		{Table: "guest_details", Column: "phone", Holds: "phone number a guest gave at sign-in", Identifies: []SubjectKind{SubjectPhone}},
		{Table: "guest_details", Column: "email", Holds: "e-mail address a guest gave at sign-in", Identifies: []SubjectKind{SubjectEmail}},
		{Table: "marketing_consents", Column: "client_mac", Holds: "device the marketing consent was given from", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "marketing_consents", Column: "name", Holds: "name given with a marketing consent", WithRow: true},
		{Table: "marketing_consents", Column: "phone", Holds: "phone number given with a marketing consent", Identifies: []SubjectKind{SubjectPhone}},
		{Table: "marketing_consents", Column: "email", Holds: "e-mail address given with a marketing consent", Identifies: []SubjectKind{SubjectEmail}},
		{Table: "marketing_consents", Column: "account_id", Holds: "guest account the consent was given under", Identifies: []SubjectKind{SubjectAccount}},
	} {
		RegisterPersonalColumn(c)
	}

	// Before the grants (Order 20): for an account, the consents are found through
	// the grants that eraser removes, and the grant_id link is SET NULL once they go.
	// `guest_details` needs no eraser of its own: it cascades from its grant.
	RegisterEraser(Eraser{Table: "marketing_consents", Kinds: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone, SubjectEmail}, Order: 15,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectMAC {
				return execCount(ctx, tx, `DELETE FROM marketing_consents WHERE client_mac = $1::macaddr`, s.MAC)
			}
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, `
					DELETE FROM marketing_consents
					WHERE `+phoneDigitsSQL("phone")+` = ANY($1) OR grant_id IN (`+phoneGrants+`)`, s.PhoneDigits)
			}
			if s.Kind == SubjectEmail {
				return execCount(ctx, tx, `DELETE FROM marketing_consents WHERE lower(email) = lower($1)`, s.Email)
			}
			// By the account the consent names, and through a grant that still
			// exists for a row recorded before 00051 wrote account_id: the grant is
			// SET NULL once it ages out, which is how an account's consent was
			// missed before that migration.
			return execCount(ctx, tx, `
				DELETE FROM marketing_consents
				WHERE account_id = $1
				   OR grant_id IN (SELECT id FROM portal_grants WHERE account_id = $1)`, s.AccountID)
		}})

	// A guest's details that give the number, whichever way they signed in. For a
	// MAC or an account they go with their grant by cascade; a number can be on a
	// voucher guest's details whose grant is nobody's phone grant.
	RegisterEraser(Eraser{Table: "guest_details", Kinds: []SubjectKind{SubjectPhone, SubjectEmail}, Order: 12,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectEmail {
				// The details that give the address. The grant they came with is the
				// device's sign-in and stays, as a phone guest's voucher grant does.
				return execCount(ctx, tx, `DELETE FROM guest_details WHERE lower(email) = lower($1)`, s.Email)
			}
			return execCount(ctx, tx, `
				DELETE FROM guest_details
				WHERE `+phoneDigitsSQL("phone")+` = ANY($1) OR grant_id IN (`+phoneGrants+`)`, s.PhoneDigits)
		}})

	// marketingConsents 730: a consent is not a licence forever. Two years is the
	// refresh interval the UK ICO's consent guidance names, and a withdrawn row — the
	// venue's evidence that it stopped — is kept as long again from its withdrawal.
	RegisterRetentionCategory(RetentionCategory{ID: "marketingConsents", DefaultDays: 730, MinDays: 30, MaxDays: maxRetentionDays,
		Tables: []string{"marketing_consents"},
		Sweep: func(ctx context.Context, t SweepTargets, cutoff time.Time) (int64, error) {
			return sweepInBatches(ctx, t.App.pool, `
				DELETE FROM marketing_consents WHERE id IN (
					SELECT id FROM marketing_consents
					WHERE coalesce(withdrawn_at, consented_at) < $1
					ORDER BY consented_at LIMIT $2)`, cutoff)
		}})
}
