package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// PortalRepo owns `portal_settings` and `portal_hero_images`.
type PortalRepo struct{ s *Store }

const portalColumns = `
	site_id, portal_url, venue_name, network_name, logo_data_uri,
	languages, methods, click_through_minutes, terms, design, updated_at`

func scanPortalSettings(row interface{ Scan(...any) error }) (*PortalSettings, error) {
	var p PortalSettings
	var terms struct {
		Summary map[string]string `json:"summary"`
	}
	var termsRaw []byte
	err := row.Scan(&p.SiteID, &p.PortalURL, &p.VenueName, &p.NetworkName, &p.LogoDataURI,
		&p.Languages, &p.Methods, &p.ClickThroughMinutes, &termsRaw, &p.Design, &p.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if len(termsRaw) > 0 {
		if err := json.Unmarshal(termsRaw, &terms); err != nil {
			return nil, err
		}
	}
	p.TermsSummary = terms.Summary
	if p.TermsSummary == nil {
		p.TermsSummary = map[string]string{}
	}
	return &p, nil
}

// Get returns a site's portal settings.
func (r *PortalRepo) Get(ctx context.Context, siteID string) (*PortalSettings, error) {
	return scanPortalSettings(r.s.pool.QueryRow(ctx,
		`SELECT `+portalColumns+` FROM portal_settings WHERE site_id = $1`, siteID))
}

// Upsert writes a site's portal settings, creating the row if it is the first time.
func (r *PortalRepo) Upsert(ctx context.Context, p *PortalSettings) error {
	summary := p.TermsSummary
	if summary == nil {
		summary = map[string]string{}
	}
	terms, err := json.Marshal(map[string]any{"summary": summary})
	if err != nil {
		return err
	}
	if len(p.Languages) == 0 {
		p.Languages = []string{"en"}
	}
	if len(p.Methods) == 0 {
		p.Methods = []string{"voucher"}
	}

	// A nil design is "not supplied", never "empty": the INSERT takes the
	// column default and the UPDATE keeps the stored one. Site creation upserts
	// a row with only a venue name, and that path must not be the one that
	// wipes a venue's page. `RETURNING design` so the caller holds what the row
	// now says either way.
	var design any
	if len(p.Design) > 0 {
		design = string(p.Design)
	}

	return mapErr(r.s.pool.QueryRow(ctx, `
		INSERT INTO portal_settings
			(site_id, portal_url, venue_name, network_name, logo_data_uri,
			 languages, methods, click_through_minutes, terms, design, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
		        coalesce($10::jsonb, '{"accent":null,"headline":{},"blocks":[]}'::jsonb), now())
		ON CONFLICT (site_id) DO UPDATE SET
			portal_url            = EXCLUDED.portal_url,
			venue_name            = EXCLUDED.venue_name,
			network_name          = EXCLUDED.network_name,
			logo_data_uri         = EXCLUDED.logo_data_uri,
			languages             = EXCLUDED.languages,
			methods               = EXCLUDED.methods,
			click_through_minutes = EXCLUDED.click_through_minutes,
			terms                 = EXCLUDED.terms,
			design                = coalesce($10::jsonb, portal_settings.design),
			updated_at            = now()
		RETURNING design, updated_at`,
		p.SiteID, p.PortalURL, p.VenueName, p.NetworkName, p.LogoDataURI,
		p.Languages, p.Methods, p.ClickThroughMinutes, terms, design).Scan(&p.Design, &p.UpdatedAt))
}

// HeroImage returns the venue photograph, bytes included.
//
// The bytes are only ever fetched by the one route that serves them. Reading portal
// settings does not touch this table, because a few hundred kilobytes of JPEG on
// every guest document is a cost the portal's byte budget cannot absorb.
func (r *PortalRepo) HeroImage(ctx context.Context, siteID string) (*HeroImage, error) {
	var h HeroImage
	err := r.s.pool.QueryRow(ctx, `
		SELECT site_id, content_type, bytes, data, uploaded_at
		FROM portal_hero_images WHERE site_id = $1`, siteID).
		Scan(&h.SiteID, &h.ContentType, &h.Bytes, &h.Data, &h.UploadedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &h, nil
}

// HeroImageMeta returns everything but the bytes.
func (r *PortalRepo) HeroImageMeta(ctx context.Context, siteID string) (*HeroImage, error) {
	var h HeroImage
	err := r.s.pool.QueryRow(ctx, `
		SELECT site_id, content_type, bytes, uploaded_at
		FROM portal_hero_images WHERE site_id = $1`, siteID).
		Scan(&h.SiteID, &h.ContentType, &h.Bytes, &h.UploadedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &h, nil
}

// PutHeroImage replaces the venue photograph.
func (r *PortalRepo) PutHeroImage(ctx context.Context, h *HeroImage) error {
	h.Bytes = len(h.Data)
	return mapErr(r.s.pool.QueryRow(ctx, `
		INSERT INTO portal_hero_images (site_id, content_type, bytes, data)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (site_id) DO UPDATE SET
			content_type = EXCLUDED.content_type,
			bytes        = EXCLUDED.bytes,
			data         = EXCLUDED.data,
			uploaded_at  = now()
		RETURNING uploaded_at`,
		h.SiteID, h.ContentType, h.Bytes, h.Data).Scan(&h.UploadedAt))
}

// DeleteHeroImage removes the venue photograph.
func (r *PortalRepo) DeleteHeroImage(ctx context.Context, siteID string) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM portal_hero_images WHERE site_id = $1`, siteID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------

// AuditRepo owns `audit_log`. Append and read; there is no update and no delete,
// and the grant in migration 00003 is what makes that true rather than this file.
type AuditRepo struct{ s *Store }

// Append writes one immutable record.
func (r *AuditRepo) Append(ctx context.Context, e *AuditEntry) error {
	return appendAudit(ctx, r.s.pool, e)
}

// appendAudit is Append on any connection or transaction, so an irreversible
// change can write its record in the same transaction as the change (EraseRecorded).
func appendAudit(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, e *AuditEntry) error {
	params := e.Params
	if params == nil {
		params = map[string]any{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	err = q.QueryRow(ctx, `
		INSERT INTO audit_log (id, at, actor_id, actor_username, kind, params, site_id,
		                       subject_kind, subject_id, subject_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING at`,
		e.ID, at, e.ActorID, e.ActorUsername, e.Kind, raw, e.SiteID,
		e.SubjectKind, e.SubjectID, e.SubjectName).Scan(&e.At)
	return mapErr(err)
}

// AuditFilter narrows an audit listing.
type AuditFilter struct {
	ListOptions
	Kind string
}

// List returns audit records newest first.
func (r *AuditRepo) List(ctx context.Context, f AuditFilter) (Page[AuditEntry], error) {
	limit := clampLimit(f.Limit)
	after, err := decodeCursor(f.Cursor)
	if err != nil {
		return Page[AuditEntry]{}, err
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT id, at, actor_id, actor_username, kind, params, site_id,
		       subject_kind, subject_id, subject_name
		FROM audit_log
		WHERE ($1 = '' OR site_id = $1)
		  AND ($2 = '' OR kind = $2)
		  AND ($3 = '' OR id < $3)
		  AND ($4 = '' OR actor_username ILIKE '%' || $4 || '%' OR coalesce(subject_name, '') ILIKE '%' || $4 || '%')
		ORDER BY at DESC, id DESC
		LIMIT $5`, f.SiteID, f.Kind, after, f.Query, limit+1)
	if err != nil {
		return Page[AuditEntry]{}, mapErr(err)
	}
	defer rows.Close()

	var items []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var raw []byte
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorUsername, &e.Kind, &raw,
			&e.SiteID, &e.SubjectKind, &e.SubjectID, &e.SubjectName); err != nil {
			return Page[AuditEntry]{}, mapErr(err)
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &e.Params); err != nil {
				return Page[AuditEntry]{}, err
			}
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return Page[AuditEntry]{}, mapErr(err)
	}
	return paginate(items, limit, func(e AuditEntry) string { return e.ID }), nil
}
