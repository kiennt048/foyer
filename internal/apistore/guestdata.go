package apistore

import (
	"context"
	"errors"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

var _ api.GuestDataStore = (*Adapter)(nil)

// GuestDataSettings is the venue's setting, or the empty one.
func (a *Adapter) GuestDataSettings(ctx context.Context, siteID string) (gen.GuestDataSettings, error) {
	set, err := a.st.GuestData().Settings(ctx, siteID)
	if err != nil {
		return gen.GuestDataSettings{}, out(err)
	}
	return guestDataOut(set), nil
}

func (a *Adapter) SetGuestDataSettings(ctx context.Context, siteID string, in gen.GuestDataSettingsUpdate) (gen.GuestDataSettings, error) {
	set := &store.GuestDataSettings{
		SiteID: siteID, ConsentEnabled: in.MarketingConsent.Enabled,
		ConsentText: map[string]string{},
		Fields:      make([]store.GuestDataFieldSetting, 0, len(in.Fields)),
	}
	for _, f := range in.Fields {
		set.Fields = append(set.Fields, store.GuestDataFieldSetting{Field: store.GuestDataField(f.Field), Required: f.Required})
	}
	if t := in.MarketingConsent.Text.En; t != nil {
		set.ConsentText["en"] = *t
	}
	if t := in.MarketingConsent.Text.Vi; t != nil {
		set.ConsentText["vi"] = *t
	}
	if err := a.st.GuestData().PutSettings(ctx, set); err != nil {
		return gen.GuestDataSettings{}, out(err)
	}
	return a.GuestDataSettings(ctx, siteID)
}

func guestDataOut(set *store.GuestDataSettings) gen.GuestDataSettings {
	o := gen.GuestDataSettings{Fields: make([]gen.GuestDataFieldSetting, 0, len(set.Fields))}
	for _, f := range set.Fields {
		o.Fields = append(o.Fields, gen.GuestDataFieldSetting{Field: gen.GuestDataField(f.Field), Required: f.Required})
	}
	o.MarketingConsent.Enabled = set.ConsentEnabled
	if t, ok := set.ConsentText["en"]; ok {
		o.MarketingConsent.Text.En = ptr(t)
		o.MarketingConsent.Versions.En = ptr(set.Versions["en"])
	}
	if t, ok := set.ConsentText["vi"]; ok {
		o.MarketingConsent.Text.Vi = ptr(t)
		o.MarketingConsent.Versions.Vi = ptr(set.Versions["vi"])
	}
	return o
}

func (a *Adapter) RecordGuestDetails(ctx context.Context, d api.GuestDetails) error {
	return out(a.st.GuestData().RecordDetails(ctx, store.GuestDetails{
		GrantID: d.GrantID, SiteID: d.SiteID, Name: d.Name, Phone: d.Phone, Email: d.Email,
	}))
}

func (a *Adapter) RecordMarketingConsent(ctx context.Context, c api.ConsentGiven) error {
	_, err := a.st.GuestData().RecordConsent(ctx, store.ConsentGiven{
		SiteID: c.SiteID, ClientMAC: c.ClientMAC, GrantID: c.GrantID,
		Name: c.Name, Phone: c.Phone, Email: c.Email, Version: c.Version,
	})
	if errors.Is(err, store.ErrUnknownConsentVersion) {
		return api.ErrUnknownConsentVersion
	}
	return out(err)
}

func (a *Adapter) MarketingConsents(ctx context.Context, siteID string, state *gen.MarketingConsentState, w api.Window) (api.Page[gen.MarketingConsent], error) {
	f := store.ConsentFilter{ListOptions: listOptions(siteID, "", w.Cursor, w.Limit)}
	if state != nil {
		f.State = string(*state)
	}
	page, err := a.st.GuestData().Consents(ctx, f)
	if err != nil {
		return api.Page[gen.MarketingConsent]{}, out(err)
	}
	items := make([]gen.MarketingConsent, 0, len(page.Items))
	for _, c := range page.Items {
		items = append(items, consentOut(c))
	}
	return api.Page[gen.MarketingConsent]{Items: items, NextCursor: page.NextCursor}, nil
}

func (a *Adapter) WithdrawMarketingConsent(ctx context.Context, siteID, consentID string) (gen.MarketingConsent, error) {
	c, err := a.st.GuestData().Withdraw(ctx, siteID, consentID, "operator")
	if err != nil {
		return gen.MarketingConsent{}, out(err)
	}
	return consentOut(*c), nil
}

func (a *Adapter) WithdrawDeviceConsents(ctx context.Context, siteID, mac string) (int, error) {
	n, err := a.st.GuestData().WithdrawDevice(ctx, siteID, mac, "guest")
	return int(n), out(err)
}

func (a *Adapter) ConsentsBetween(ctx context.Context, siteID string, r api.Range, visit func(gen.MarketingConsent) error) error {
	return out(a.st.GuestData().ConsentsBetween(ctx, siteID, r.From, r.To, func(c store.MarketingConsent) error {
		return visit(consentOut(c))
	}))
}

func consentOut(c store.MarketingConsent) gen.MarketingConsent {
	o := gen.MarketingConsent{
		Id: c.ID, SiteId: c.SiteID, ClientMac: c.ClientMAC,
		ConsentedAt: c.ConsentedAt.UTC(), Lang: gen.Language(c.Lang),
		ConsentVersion: c.Version, ConsentText: c.Text,
		Name: c.Name, Phone: c.Phone, Email: c.Email,
		State: gen.Active,
	}
	if c.WithdrawnAt != nil {
		t := c.WithdrawnAt.UTC()
		o.WithdrawnAt, o.State = &t, gen.Withdrawn
	}
	if c.WithdrawnBy != nil {
		by := gen.ConsentWithdrawnBy(*c.WithdrawnBy)
		o.WithdrawnBy = &by
	}
	return o
}
