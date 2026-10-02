package apistore

import (
	"context"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// WalledGardenStore — unauthenticated egress.
//
// Every row here is a destination any device that associates can reach without
// identifying itself, which is why the list is short, audited, and why `required`
// entries are not the operator's to delete.

// Garden returns the entries together with what each device can actually enforce.
//
// **The coverage travels with the entries because a site is not homogeneous.** An
// entry that works on a UniFi access point does nothing at all on a driver
// reporting `walledGarden: none`, and the operator has to be told that beside the
// entry rather than after a support call.
//
// `effectiveKind` is the **weakest** enabled device's, not the strongest: a garden
// is only as strong as the device that enforces it least well, and a site with one
// FQDN/SNI driver and one IP-range driver has an IP-range garden.
func (a *Adapter) Garden(ctx context.Context, siteID string) (gen.WalledGarden, error) {
	entries, err := a.st.Garden().ListForSite(ctx, siteID)
	if err != nil {
		return gen.WalledGarden{}, out(err)
	}
	kind, err := a.st.Garden().EffectiveKind(ctx, siteID)
	if err != nil {
		return gen.WalledGarden{}, out(err)
	}
	devices, err := a.st.RadiusClients().ListNas(ctx, listOptions(siteID, "", "", 200))
	if err != nil {
		return gen.WalledGarden{}, out(err)
	}

	g := gen.WalledGarden{
		SiteId:        siteID,
		EffectiveKind: gen.GardenKind(kind),
		Entries:       make([]gen.GardenEntry, 0, len(entries)),
		Drivers:       make([]gen.GardenDriverCoverage, 0, len(devices.Items)),
	}
	for _, e := range entries {
		g.Entries = append(g.Entries, gardenEntryOut(e))
	}
	for _, n := range devices.Items {
		if !n.Enabled {
			continue
		}
		g.Drivers = append(g.Drivers, gen.GardenDriverCoverage{
			NasId:        n.ID,
			Name:         n.Name,
			WalledGarden: capabilitiesOut(n.Capabilities).WalledGarden,
		})
	}
	return g, nil
}

// AddGardenEntry allows one destination before authentication.
//
// The uniqueness is the database's — `UNIQUE (site_id, kind, value)` — rather than a
// read-then-write here, so two operators adding the same hostname at once produce
// one row and one refusal instead of two rows.
func (a *Adapter) AddGardenEntry(ctx context.Context, siteID string, in gen.GardenEntryCreate) (gen.GardenEntry, error) {
	e := &store.GardenEntry{
		ID:     a.newID("garden-"),
		SiteID: siteID,
		Kind:   string(in.Kind),
		Value:  in.Value,
		Note:   in.Note,
		Origin: string(gen.GardenEntryOriginOperator),
	}
	if err := a.st.Garden().Create(ctx, e); err != nil {
		return gen.GardenEntry{}, out(err)
	}
	return gardenEntryOut(*e), nil
}

// RemoveGardenEntry deletes an operator entry.
//
// A `required` entry is excluded by the repository's own `WHERE` clause and arrives
// here as `ErrNotFound`. The caller has already refused it with the right problem
// document; this is the second gate, and it is the database's rather than a check
// somebody can forget — removing the portal's own FQDN, its OCSP/CRL endpoints or
// the RFC 8908 endpoint presents to every guest as a network that is simply broken.
func (a *Adapter) RemoveGardenEntry(ctx context.Context, siteID, entryID string) error {
	return out(a.st.Garden().Delete(ctx, siteID, entryID))
}
