package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PhoneRepo owns `phone_sign_in`, `phone_codes` and `phone_code_sends` (migration
// 00022): how a site sends phone sign-in codes, the codes, and the daily ceiling.
//
// The guarantees are `internal/identity/otp.Store`'s and each method below says
// which one it carries. The code itself is never here — only its keyed MAC.
type PhoneRepo struct{ s *Store }

// Phone is constructed per call rather than held on Store, so this aggregate adds
// a file rather than a field to the shared Store struct.
func (s *Store) Phone() *PhoneRepo { return &PhoneRepo{s: s} }

// The two refusals Issue can make, distinct from a failure to decide.
var (
	ErrPhoneLimited = errors.New("store: too many codes to this number")
	ErrPhoneCeiling = errors.New("store: the site's daily message ceiling is reached")
)

// PhoneSignIn is one site's configuration, without its keys.
type PhoneSignIn struct {
	SiteID          string
	Provider        string
	Brandname       *string
	ZaloOAID        *string
	ZaloTemplateID  *string
	Sandbox         bool
	DailyLimit      int
	SessionMinutes  int
	MessageTemplate string
	UpdatedAt       time.Time
}

// Get reads the configuration. The keys are not selected: a read the console makes
// has no business holding them even in memory.
func (r *PhoneRepo) Get(ctx context.Context, siteID string) (*PhoneSignIn, error) {
	var p PhoneSignIn
	err := r.s.pool.QueryRow(ctx, `
		SELECT site_id, provider, brandname, zalo_oa_id, zalo_template_id, sandbox,
		       daily_limit, session_minutes, message_template, updated_at
		  FROM phone_sign_in WHERE site_id = $1`, siteID).
		Scan(&p.SiteID, &p.Provider, &p.Brandname, &p.ZaloOAID, &p.ZaloTemplateID, &p.Sandbox,
			&p.DailyLimit, &p.SessionMinutes, &p.MessageTemplate, &p.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

// RevealKeys unseals the provider's keys. One caller: the send path, for the
// length of one send. Never a list, never logged.
func (r *PhoneRepo) RevealKeys(ctx context.Context, siteID string) (apiKey, secretKey []byte, err error) {
	var a, s []byte
	if err := r.s.pool.QueryRow(ctx,
		`SELECT api_key, secret_key FROM phone_sign_in WHERE site_id = $1`, siteID).Scan(&a, &s); err != nil {
		return nil, nil, mapErr(err)
	}
	if apiKey, err = r.s.cfg.Keys.Open(a, SecretContext("phone_sign_in", "api_key", siteID)); err != nil {
		return nil, nil, err
	}
	if secretKey, err = r.s.cfg.Keys.Open(s, SecretContext("phone_sign_in", "secret_key", siteID)); err != nil {
		return nil, nil, err
	}
	return apiKey, secretKey, nil
}

// Upsert writes the configuration. A nil key keeps the stored one; the first write
// needs both, and refuses with ErrSecretUnset without them.
func (r *PhoneRepo) Upsert(ctx context.Context, p *PhoneSignIn, apiKey, secretKey []byte) error {
	var sealedA, sealedS []byte
	var err error
	if apiKey != nil {
		if err := checkSecretBytes(apiKey); err != nil {
			return err
		}
		if sealedA, err = r.s.cfg.Keys.Seal(apiKey, SecretContext("phone_sign_in", "api_key", p.SiteID)); err != nil {
			return err
		}
	}
	if secretKey != nil {
		if err := checkSecretBytes(secretKey); err != nil {
			return err
		}
		if sealedS, err = r.s.cfg.Keys.Seal(secretKey, SecretContext("phone_sign_in", "secret_key", p.SiteID)); err != nil {
			return err
		}
	}
	return r.s.tx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM phone_sign_in WHERE site_id = $1)`, p.SiteID).Scan(&exists); err != nil {
			return mapErr(err)
		}
		if !exists && (sealedA == nil || sealedS == nil) {
			return ErrSecretUnset
		}
		if !exists {
			return mapErr(tx.QueryRow(ctx, `
				INSERT INTO phone_sign_in
					(site_id, provider, brandname, zalo_oa_id, zalo_template_id, api_key, secret_key,
					 sandbox, daily_limit, session_minutes, message_template)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				RETURNING updated_at`,
				p.SiteID, p.Provider, p.Brandname, p.ZaloOAID, p.ZaloTemplateID, sealedA, sealedS,
				p.Sandbox, p.DailyLimit, p.SessionMinutes, p.MessageTemplate).Scan(&p.UpdatedAt))
		}
		return mapErr(tx.QueryRow(ctx, `
			UPDATE phone_sign_in SET
				provider = $2, brandname = $3, zalo_oa_id = $4, zalo_template_id = $5,
				api_key    = coalesce($6, api_key),
				secret_key = coalesce($7, secret_key),
				sandbox = $8, daily_limit = $9, session_minutes = $10, message_template = $11,
				updated_at = now()
			 WHERE site_id = $1
			RETURNING updated_at`,
			p.SiteID, p.Provider, p.Brandname, p.ZaloOAID, p.ZaloTemplateID, sealedA, sealedS,
			p.Sandbox, p.DailyLimit, p.SessionMinutes, p.MessageTemplate).Scan(&p.UpdatedAt))
	})
}

// SentOn is today's count against the ceiling, for the console.
func (r *PhoneRepo) SentOn(ctx context.Context, siteID, day string) (int, error) {
	var n int
	err := r.s.pool.QueryRow(ctx,
		`SELECT coalesce((SELECT sent FROM phone_code_sends WHERE site_id = $1 AND day = $2::date), 0)`,
		siteID, day).Scan(&n)
	return n, mapErr(err)
}

// PhoneCodeIssue is one code about to be sent and the limits it has to fit.
type PhoneCodeIssue struct {
	ID               string
	SiteID           string
	SessionTokenHash string
	ClientMAC        string
	Phone            string
	CodeMAC          []byte
	KeyID            string
	At               time.Time
	ExpiresAt        time.Time
	MaxAttempts      int
	Day              string
	DailyLimit       int
	// GlobalLimit is the deployment-wide cap (`phone_spend_cap`); nil is none.
	GlobalLimit   *int
	PerNumberHour int
	PerNumberDay  int
}

// Issue is the whole send decision in one transaction.
//
// **An advisory lock per site and number** serialises two requests for the same
// number, so the count and the insert cannot interleave: without it two requests
// in the same millisecond both count two, both fit under three, and four codes
// go out. The ceiling is a conditional upsert — `WHERE sent < limit` — which is
// atomic on its own row. Earlier live codes for the same browser are retired in the
// same transaction, so asking again does not multiply the attempts available.
func (r *PhoneRepo) Issue(ctx context.Context, in PhoneCodeIssue) error {
	return r.s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended('foyer.phone:' || $1 || ':' || $2, 0))`,
			in.SiteID, in.Phone); err != nil {
			return mapErr(err)
		}
		var hour, day int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE created_at > $3::timestamptz - interval '1 hour'),
			       count(*)
			  FROM phone_codes
			 WHERE site_id = $1 AND phone = $2 AND created_at > $3::timestamptz - interval '1 day'`,
			in.SiteID, in.Phone, in.At).Scan(&hour, &day); err != nil {
			return mapErr(err)
		}
		if hour >= in.PerNumberHour || day >= in.PerNumberDay {
			return ErrPhoneLimited
		}
		if err := countSend(ctx, tx, in.SiteID, in.Day, in.DailyLimit, in.GlobalLimit); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE phone_codes SET consumed_at = $2
			 WHERE session_token_hash = $1 AND consumed_at IS NULL`, in.SessionTokenHash, in.At); err != nil {
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO phone_codes
				(id, site_id, session_token_hash, client_mac, phone, code_mac, key_id,
				 created_at, expires_at, max_attempts)
			VALUES ($1, $2, $3, $4::macaddr, $5, $6, $7, $8, $9, $10)`,
			in.ID, in.SiteID, in.SessionTokenHash, NormaliseMAC(in.ClientMAC), in.Phone, in.CodeMAC, in.KeyID,
			in.At, in.ExpiresAt, in.MaxAttempts)
		return mapErr(err)
	})
}

// countSend charges one message against the site's ceiling and the deployment's,
// inside the caller's transaction, or refuses with ErrPhoneCeiling.
//
// Both are conditional upserts, atomic on their own row, so two sends in the same
// millisecond cannot both fit under the last unit of either budget. The global row
// is counted whether or not a cap is set (`$3 IS NULL`), so a cap set at noon
// already knows the morning's count. A refusal of the second rolls back the first,
// because both are in the caller's transaction.
func countSend(ctx context.Context, tx pgx.Tx, siteID, day string, dailyLimit int, globalLimit *int) error {
	var sent int
	err := tx.QueryRow(ctx, `
		INSERT INTO phone_code_sends (site_id, day, sent) VALUES ($1, $2::date, 1)
		ON CONFLICT (site_id, day) DO UPDATE SET sent = phone_code_sends.sent + 1
		 WHERE phone_code_sends.sent < $3
		RETURNING sent`, siteID, day, dailyLimit).Scan(&sent)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPhoneCeiling
	}
	if err != nil {
		return mapErr(err)
	}
	if sent > dailyLimit {
		// The first row of a day is inserted as 1 whatever the limit; a limit
		// below one is refused by the schema, so this is a guard, not a path.
		return ErrPhoneCeiling
	}
	var total int
	err = tx.QueryRow(ctx, `
		INSERT INTO phone_code_sends_total (day, sent) VALUES ($1::date, 1)
		ON CONFLICT (tenant_id, day) DO UPDATE SET sent = phone_code_sends_total.sent + 1
		 WHERE $2::integer IS NULL OR phone_code_sends_total.sent < $2::integer
		RETURNING sent`, day, globalLimit).Scan(&total)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPhoneCeiling
	}
	if err != nil {
		return mapErr(err)
	}
	if globalLimit != nil && total > *globalLimit {
		return ErrPhoneCeiling
	}
	return nil
}

// CountSend charges one message that is not a code — the console's test message —
// against both ceilings, in its own transaction. It is the same charge Issue makes
// and fails closed the same way.
func (r *PhoneRepo) CountSend(ctx context.Context, siteID, day string, dailyLimit int, globalLimit *int) error {
	return r.s.tx(ctx, func(tx pgx.Tx) error {
		return countSend(ctx, tx, siteID, day, dailyLimit, globalLimit)
	})
}

// ── the operator's limits and the deployment's cap (migration 00033) ─────────

// PhoneAbuseControls is one venue's limits and destination ranges.
type PhoneAbuseControls struct {
	SiteID         string
	PerNumberHour  int
	PerNumberDay   int
	PerDeviceHour  int
	PerAddressHour int
	CodeMinutes    int
	MaxAttempts    int
	MobilePrefixes []string
	UpdatedAt      time.Time
}

// AbuseControls reads a venue's limits. ErrNotFound means none were ever set, and
// the product defaults apply.
func (r *PhoneRepo) AbuseControls(ctx context.Context, siteID string) (*PhoneAbuseControls, error) {
	var c PhoneAbuseControls
	err := r.s.pool.QueryRow(ctx, `
		SELECT site_id, per_number_hour, per_number_day, per_device_hour, per_address_hour,
		       code_minutes, max_attempts, mobile_prefixes, updated_at
		  FROM phone_abuse_controls WHERE site_id = $1`, siteID).
		Scan(&c.SiteID, &c.PerNumberHour, &c.PerNumberDay, &c.PerDeviceHour, &c.PerAddressHour,
			&c.CodeMinutes, &c.MaxAttempts, &c.MobilePrefixes, &c.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

// SetAbuseControls replaces a venue's limits. The bounds are the table's CHECKs; a
// value outside them is a constraint violation, which the API's schema validation
// has already refused as a 422.
func (r *PhoneRepo) SetAbuseControls(ctx context.Context, c *PhoneAbuseControls) error {
	return mapErr(r.s.pool.QueryRow(ctx, `
		INSERT INTO phone_abuse_controls
			(site_id, per_number_hour, per_number_day, per_device_hour, per_address_hour,
			 code_minutes, max_attempts, mobile_prefixes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (site_id) DO UPDATE SET
			per_number_hour = EXCLUDED.per_number_hour, per_number_day = EXCLUDED.per_number_day,
			per_device_hour = EXCLUDED.per_device_hour, per_address_hour = EXCLUDED.per_address_hour,
			code_minutes = EXCLUDED.code_minutes, max_attempts = EXCLUDED.max_attempts,
			mobile_prefixes = EXCLUDED.mobile_prefixes, updated_at = now()
		RETURNING updated_at`,
		c.SiteID, c.PerNumberHour, c.PerNumberDay, c.PerDeviceHour, c.PerAddressHour,
		c.CodeMinutes, c.MaxAttempts, c.MobilePrefixes).Scan(&c.UpdatedAt))
}

// SpendCap reads the deployment-wide daily cap. A nil limit is no cap; a zero
// updatedAt is a cap never set.
func (r *PhoneRepo) SpendCap(ctx context.Context) (limit *int, updatedAt time.Time, err error) {
	err = r.s.pool.QueryRow(ctx, `SELECT daily_limit, updated_at FROM phone_spend_cap`).Scan(&limit, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	return limit, updatedAt, mapErr(err)
}

// SetSpendCap sets or clears the deployment-wide daily cap.
func (r *PhoneRepo) SetSpendCap(ctx context.Context, limit *int) (time.Time, error) {
	var at time.Time
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO phone_spend_cap (daily_limit) VALUES ($1)
		ON CONFLICT (tenant_id) DO UPDATE SET daily_limit = EXCLUDED.daily_limit, updated_at = now()
		RETURNING updated_at`, limit).Scan(&at)
	return at, mapErr(err)
}

// PhoneCodeAttempt is one spent attempt: enough to check the code.
type PhoneCodeAttempt struct {
	ID          string
	Phone       string
	CodeMAC     []byte
	KeyID       string
	Attempts    int
	MaxAttempts int
}

// Attempt spends one attempt on this browser's newest live code and returns its MAC,
// in one statement.
//
// The MAC is returned only by the UPDATE that counted the attempt, so there is no
// read of it that is not also a spend: a hundred parallel guesses get
// `max_attempts` comparisons. The outer WHERE repeats the liveness conditions
// because PostgreSQL re-evaluates them on the locked row when two updates race —
// the subquery alone would let the loser count a sixth attempt.
func (r *PhoneRepo) Attempt(ctx context.Context, sessionTokenHash string, at time.Time) (*PhoneCodeAttempt, error) {
	var a PhoneCodeAttempt
	err := r.s.pool.QueryRow(ctx, `
		UPDATE phone_codes SET attempts = attempts + 1
		 WHERE id = (SELECT id FROM phone_codes
		              WHERE session_token_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		              ORDER BY created_at DESC LIMIT 1)
		   AND consumed_at IS NULL AND expires_at > $2 AND attempts < max_attempts
		RETURNING id, phone, code_mac, key_id, attempts, max_attempts`, sessionTokenHash, at).
		Scan(&a.ID, &a.Phone, &a.CodeMAC, &a.KeyID, &a.Attempts, &a.MaxAttempts)
	if err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

// Consume marks a code used. Of two requests carrying the right code, one wins.
func (r *PhoneRepo) Consume(ctx context.Context, id string, at time.Time) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE phone_codes SET consumed_at = $2 WHERE id = $1 AND consumed_at IS NULL`, id, at)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Phone sign-in's personal data, in the registry `privacy.go` holds every table
// to. A code row names the device
// that asked and the number it was sent to; a phone grant carries the number the
// guest proved. `phone_code_sends` is a per-site daily counter, names nobody, and
// is deliberately not reaped with the codes, or the ceiling would reset.
func init() {
	for _, c := range []PersonalColumn{
		{Table: "phone_codes", Column: "client_mac", Holds: "client MAC that asked for a sign-in code", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "phone_codes", Column: "phone", Holds: "phone number a sign-in code was sent to", Identifies: []SubjectKind{SubjectPhone}},
		{Table: "portal_grants", Column: "phone", Holds: "phone number a phone guest verified", Identifies: []SubjectKind{SubjectPhone}},
		// Caught by the scan's `_mac` and not a device address at all.
		{Table: "phone_codes", Column: "code_mac", Holds: "keyed hash (HMAC) of a sign-in code",
			NotGuestData: "a message authentication code of the one-time code, not a MAC address; it goes with its code row"},
	} {
		RegisterPersonalColumn(c)
	}
	// Before the grants (Order 20): a code is not a child of anything, so the order
	// only has to be stable.
	RegisterEraser(Eraser{Table: "phone_codes", Kinds: []SubjectKind{SubjectMAC, SubjectPhone}, Order: 5,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, `DELETE FROM phone_codes WHERE `+phoneDigitsSQL("phone")+` = ANY($1)`, s.PhoneDigits)
			}
			return execCount(ctx, tx, `DELETE FROM phone_codes WHERE client_mac = $1::macaddr`, s.MAC)
		}})
}

// PhoneCodeRetention is how long a code row is kept: the per-number limit's longest
// window (5 a day), after which nothing reads it. `internal/jobs` reaps on it.
const PhoneCodeRetention = 24 * time.Hour

// ReapCodes deletes codes older than the per-number limit's longest window.
// `internal/jobs` runs it every cycle with now - PhoneCodeRetention.
func (r *PhoneRepo) ReapCodes(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM phone_codes WHERE created_at < $1`, before)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}
