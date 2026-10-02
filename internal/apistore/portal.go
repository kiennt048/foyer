package apistore

import (
	"context"
	"errors"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// PortalSettingsStore — the writable twin of what a guest sees.

// PortalSettings reads the settings and the hero image's metadata, never its bytes.
//
// Two reads rather than one, and the split is the repository's: a few hundred
// kilobytes of JPEG on every read of the portal's configuration is a cost the
// connection pool should not pay, and the guest portal reads this on every
// document. A site with no photograph is not an error.
func (a *Adapter) PortalSettings(ctx context.Context, siteID string) (gen.PortalSettings, error) {
	p, err := a.st.Portal().Get(ctx, siteID)
	if err != nil {
		return gen.PortalSettings{}, out(err)
	}
	hero, err := a.st.Portal().HeroImageMeta(ctx, siteID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return gen.PortalSettings{}, out(err)
	}
	return portalOut(*p, hero)
}

// AmendPortalSettings applies the console's patch.
//
// Read, apply, upsert — because the repository's write is a whole-row upsert and a
// patch that wrote only its own fields would blank every other one. The read is
// what makes a `PATCH` a patch.
//
// **Terms are replaced wholesale, never merged per language.** They are legal text
// a venue authored; a merge would leave one language saying something the venue has
// since withdrawn.
func (a *Adapter) AmendPortalSettings(ctx context.Context, siteID string, in gen.PortalSettingsUpdate) (gen.PortalSettings, error) {
	p, err := a.st.Portal().Get(ctx, siteID)
	if err != nil {
		return gen.PortalSettings{}, out(err)
	}
	if in.VenueName != nil {
		p.VenueName = *in.VenueName
	}
	if in.NetworkName != nil {
		p.NetworkName = in.NetworkName
	}
	if in.LogoDataUri != nil {
		p.LogoDataURI = in.LogoDataUri
	}
	if in.ClickThroughMinutes != nil {
		p.ClickThroughMinutes = in.ClickThroughMinutes
	}
	if in.Languages != nil {
		langs := make([]string, 0, len(*in.Languages))
		for _, l := range *in.Languages {
			langs = append(langs, string(l))
		}
		p.Languages = langs
	}
	if in.Methods != nil {
		methods := make([]string, 0, len(*in.Methods))
		for _, m := range *in.Methods {
			methods = append(methods, string(m))
		}
		p.Methods = methods
	}
	if in.Terms != nil && in.Terms.Summary != nil {
		p.TermsSummary = *in.Terms.Summary
	}
	// **The design is replaced whole, like terms.** A block list merged per
	// element is how two console tabs lose each other's edits, and the console
	// always holds all of it. The API validated it before this was called.
	if in.Design != nil {
		raw, err := designIn(*in.Design)
		if err != nil {
			return gen.PortalSettings{}, err
		}
		p.Design = raw
	}

	if err := a.st.Portal().Upsert(ctx, p); err != nil {
		return gen.PortalSettings{}, out(err)
	}
	hero, err := a.st.Portal().HeroImageMeta(ctx, siteID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return gen.PortalSettings{}, out(err)
	}
	return portalOut(*p, hero)
}

// StoreHeroImage takes decoded bytes, not base64.
//
// The size ceiling was already applied by the caller to what will actually be
// served: base64 inflates by a third, so a limit applied to the submitted string
// would be a third more generous than the one that matters — and what matters is
// the bytes the guest's phone waits for.
//
// **The filename is not stored**, because `portal_hero_images` has no column for
// one: it is a single row per site, replaced on upload, and the served URL is
// derived from the site and the content type. `internal/api/memory` builds the URL
// from the submitted filename, so the two disagree on that member. Reported rather
// than answered by adding a column from this side of the seam.
func (a *Adapter) StoreHeroImage(ctx context.Context, siteID, _, contentType string, data []byte) (gen.HeroImage, error) {
	h := &store.HeroImage{SiteID: siteID, ContentType: contentType, Data: data}
	if err := a.st.Portal().PutHeroImage(ctx, h); err != nil {
		return gen.HeroImage{}, out(err)
	}
	return heroOut(*h), nil
}

func (a *Adapter) DeleteHeroImage(ctx context.Context, siteID string) error {
	return out(a.st.Portal().DeleteHeroImage(ctx, siteID))
}

// HeroImageBytes serves the public venue photograph: content type and bytes,
// nothing else. `HeroImage` (not `HeroImageMeta`) because this is the one route
// the bytes comment names — reading settings must not drag JPEG through the pool.
func (a *Adapter) HeroImageBytes(ctx context.Context, siteID string) (string, []byte, error) {
	h, err := a.st.Portal().HeroImage(ctx, siteID)
	if err != nil {
		return "", nil, out(err)
	}
	return h.ContentType, h.Data, nil
}
