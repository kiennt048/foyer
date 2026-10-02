package store_test

import (
	"context"
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// The controller / RADIUS client / access point model, driven rather than
// described.
//
// One controller, one shared secret, three client addresses, three access points.
// A schema that minted a secret per access point could not express that, and
// would have nowhere to put the MAC the NAS redirect carries.
func TestThreeNounsHaveDifferentCardinalities(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	ctl := &store.Controller{
		ID: uniq("ctl"), Name: "Lobby UniFi", Vendor: "unifi",
		BaseURL: "https://192.0.2.20:11443", AuthMode: "api-key", TLSMode: "trust-on-first-use",
	}
	fatalf(t, st.Controllers().Create(ctx, ctl, []byte(randomString())), "registering a controller")
	if !ctl.CredentialSet {
		t.Fatal("credentialSet is false immediately after storing a credential")
	}

	secret := mustSecret(t, st, site.ID)

	devices := []store.DiscoveredAP{
		{MAC: "94:2a:6f:d0:30:57", Name: "AP Lobby", IPAddress: addr(t, "10.20.0.11"), State: "online"},
		{MAC: "94-2a-6f-d0-30-58", Name: "AP Bar", IPAddress: addr(t, "10.20.0.12"), State: "online"},
		{MAC: "942a6fd03059", Name: "AP Terrace", IPAddress: addr(t, "10.20.0.13"), State: "offline"},
	}

	got, err := st.AccessPoints().Reconcile(ctx, ctl.ID, site.ID, secret.ID, "unifi", devices, time.Now())
	fatalf(t, err, "first discovery run")
	if got.Adopted != 3 || got.Updated != 0 || got.Missing != 0 {
		t.Fatalf("first run: adopted=%d updated=%d missing=%d, want 3/0/0", got.Adopted, got.Updated, got.Missing)
	}

	t.Run("three addresses share one secret", func(t *testing.T) {
		clients, err := st.RadiusClients().ListClients(ctx, site.ID)
		fatalf(t, err, "listing clients.conf rows")
		if len(clients) != 3 {
			t.Fatalf("got %d clients.conf rows, want 3 -- one address per access point", len(clients))
		}
		for _, c := range clients {
			if c.SharedSecretID == nil || *c.SharedSecretID != secret.ID {
				t.Fatalf("client %s does not present the site's one secret", c.ID)
			}
			if !c.RequireMsgAuth || !c.LimitProxyState {
				t.Fatalf("client %s is not BlastRADIUS-protected by default", c.ID)
			}
		}
		applies, err := st.SharedSecrets().AppliesTo(ctx, secret.ID)
		fatalf(t, err, "reading the secret's blast radius")
		if len(applies) != 3 {
			t.Fatalf("the secret applies to %d clients, want 3", len(applies))
		}
	})

	t.Run("the redirect's MAC resolves to a device", func(t *testing.T) {
		// The exact spelling UniFi sends in the redirect query string.
		ap, err := st.AccessPoints().GetByMAC(ctx, "94:2a:6f:d0:30:57")
		fatalf(t, err, "resolving the redirect MAC")
		if ap.Name != "AP Lobby" {
			t.Fatalf("resolved to %q", ap.Name)
		}
		// And the two other spellings of a second device's address are one value.
		for _, spelling := range []string{"94-2A-6F-D0-30-58", "942a6fd03058", "94:2a:6f:d0:30:58"} {
			if _, err := st.AccessPoints().GetByMAC(ctx, spelling); err != nil {
				t.Fatalf("MAC spelling %q did not resolve: %v", spelling, err)
			}
		}
	})

	t.Run("the device view carries both halves", func(t *testing.T) {
		page, err := st.RadiusClients().ListNas(ctx, store.ListOptions{SiteID: site.ID})
		fatalf(t, err, "listing the device view")
		if len(page.Items) != 3 {
			t.Fatalf("device view has %d rows, want 3", len(page.Items))
		}
		for _, n := range page.Items {
			if n.MAC == nil {
				t.Fatalf("device %s has no MAC", n.ID)
			}
			if n.IPAddress == nil {
				t.Fatalf("device %s has no address", n.ID)
			}
			if !n.SecretSet {
				t.Fatalf("device %s reports no secret", n.ID)
			}
			if n.RadiusVia != "self" {
				t.Fatalf("device %s reports radiusVia %q", n.ID, n.RadiusVia)
			}
			if n.ID == n.RadiusClient.ID {
				t.Fatalf("device id and clients.conf row id are the same value (%s); "+
					"they are two different objects", n.ID)
			}
		}
	})

	t.Run("a device that disappears is marked, not deleted", func(t *testing.T) {
		got, err := st.AccessPoints().Reconcile(ctx, ctl.ID, site.ID, secret.ID, "unifi", devices[:2], time.Now())
		fatalf(t, err, "second discovery run")
		if got.Adopted != 0 || got.Updated != 2 || got.Missing != 1 {
			t.Fatalf("second run: adopted=%d updated=%d missing=%d, want 0/2/1",
				got.Adopted, got.Updated, got.Missing)
		}

		gone, err := st.AccessPoints().GetByMAC(ctx, "94:2a:6f:d0:30:59")
		if err != nil {
			t.Fatalf("the missing access point's row was deleted: %v", err)
		}
		if gone.State != "missing" || gone.MissingSince == nil {
			t.Fatalf("state=%q missingSince=%v, want missing with a timestamp", gone.State, gone.MissingSince)
		}
	})

	t.Run("a returning device is un-marked", func(t *testing.T) {
		got, err := st.AccessPoints().Reconcile(ctx, ctl.ID, site.ID, secret.ID, "unifi", devices, time.Now())
		fatalf(t, err, "third discovery run")
		if got.Missing != 0 {
			t.Fatalf("missing=%d after every device reappeared", got.Missing)
		}
		back, err := st.AccessPoints().GetByMAC(ctx, "94:2a:6f:d0:30:59")
		fatalf(t, err, "reading the returned access point")
		if back.MissingSince != nil {
			t.Fatalf("missingSince is still %v", back.MissingSince)
		}
	})

	t.Run("a controller still referenced reports itself in use", func(t *testing.T) {
		n, err := st.Controllers().InUse(ctx, ctl.ID)
		fatalf(t, err, "counting references")
		if n != 3 {
			t.Fatalf("in use by %d clients, want 3", n)
		}
	})
}

// A CoA driver has no controller and no access point. It is one clients.conf row,
// and the device view has to render it with the AP-shaped fields empty rather than
// omitting it.
func TestManualClientWithNoAccessPointIsStillADevice(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)

	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Branch MikroTik",
		Address: netip.MustParsePrefix("10.30.0.1/32"),
		Source:  "manual", Driver: "mikrotik", SharedSecretID: &secret.ID,
		CoAPort: ptrInt(1700), RequireMsgAuth: true, LimitProxyState: true, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, client), "creating a manual client")

	nas, err := st.RadiusClients().GetNas(ctx, client.ID)
	fatalf(t, err, "reading the device view")
	if nas.MAC != nil {
		t.Fatalf("a device with no access point reported a MAC: %v", *nas.MAC)
	}
	if nas.RadiusVia != "self" {
		t.Fatalf("radiusVia is %q, want self", nas.RadiusVia)
	}
	if nas.CoAPort == nil || *nas.CoAPort != 1700 {
		t.Fatal("the per-NAS CoA port did not survive; 3799 is common but Cisco frequently uses 1700")
	}
	if nas.Name != "Branch MikroTik" {
		t.Fatalf("display name is %q", nas.Name)
	}
}

func TestSharedSecretRoundTripsThroughTheEnvelope(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	value := []byte(randomString())
	ss := &store.SharedSecret{ID: uniq("sec"), SiteID: site.ID, Label: "Guest profile"}
	fatalf(t, st.SharedSecrets().Create(ctx, ss, value), "storing a secret")

	back, err := st.SharedSecrets().Reveal(ctx, ss.ID)
	fatalf(t, err, "revealing")
	if string(back) != string(value) {
		t.Fatal("the revealed secret differs from what was stored")
	}

	// The column holds ciphertext, not the value.
	var stored []byte
	fatalf(t, st.Pool().QueryRow(ctx, `SELECT secret FROM shared_secrets WHERE id = $1`, ss.ID).Scan(&stored),
		"reading the raw column")
	if string(stored) == string(value) {
		t.Fatal("the shared secret is stored in cleartext")
	}

	rotated := []byte(randomString())
	fatalf(t, st.SharedSecrets().Rotate(ctx, ss.ID, rotated), "rotating")
	back, err = st.SharedSecrets().Reveal(ctx, ss.ID)
	fatalf(t, err, "revealing after rotation")
	if string(back) != string(rotated) {
		t.Fatal("rotation did not take")
	}
	meta, err := st.SharedSecrets().Get(ctx, ss.ID)
	fatalf(t, err, "reading metadata")
	if meta.LastRotatedAt == nil {
		t.Fatal("lastRotatedAt was not stamped")
	}
}

// An account is argon2id until somebody opts it in, and the console reads that from
// the separate table rather than from a flag.
func TestMSCHAPv2OptInIsVisibleAndSeparate(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	acct := &store.Account{
		ID: uniq("acct"), SiteID: site.ID, Username: "reception",
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$notarealhash",
		Enabled:      true,
	}
	fatalf(t, st.Accounts().Create(ctx, acct), "creating an account")

	got, err := st.Accounts().Get(ctx, acct.ID)
	fatalf(t, err, "reading the account")
	if got.CredentialKind != "argon2id" {
		t.Fatalf("credentialKind is %q before any opt-in", got.CredentialKind)
	}

	ntHash := []byte{0x8a, 0xb2, 0xc3, 0xd4, 0xe5, 0xf6, 0x07, 0x18, 0x29, 0x3a, 0x4b, 0x5c, 0x6d, 0x7e, 0x8f, 0x90}
	operator := "linh"
	fatalf(t, st.Accounts().OptInToMSCHAPv2(ctx, acct.ID, ntHash, &operator), "opting in")

	got, err = st.Accounts().Get(ctx, acct.ID)
	fatalf(t, err, "re-reading the account")
	if got.CredentialKind != "argon2id+nt" {
		t.Fatalf("credentialKind is %q after opting in", got.CredentialKind)
	}

	// The hash is in its own table, sealed, and no account query returns it.
	var raw []byte
	fatalf(t, st.Pool().QueryRow(ctx, `SELECT nt_hash FROM account_nt_hashes WHERE account_id = $1`, acct.ID).Scan(&raw),
		"reading the raw NT hash column")
	if string(raw) == string(ntHash) {
		t.Fatal("the NT hash is stored unencrypted; it is password-equivalent")
	}
	back, err := st.Accounts().NTHash(ctx, acct.ID)
	fatalf(t, err, "unsealing the NT hash")
	if string(back) != string(ntHash) {
		t.Fatal("the NT hash did not survive the round trip")
	}

	fatalf(t, st.Accounts().OptOutOfMSCHAPv2(ctx, acct.ID), "opting out")
	got, err = st.Accounts().Get(ctx, acct.ID)
	fatalf(t, err, "re-reading after opt-out")
	if got.CredentialKind != "argon2id" {
		t.Fatalf("credentialKind is %q after opting out", got.CredentialKind)
	}
}

func TestVoucherBatchAndSingleUseRedemption(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	batch := &store.VoucherBatch{
		ID: uniq("bat"), SiteID: site.ID, Label: "Front desk, August",
		Count: 3, ExpiryMode: "from-first-use",
		Plan: store.Plan{DurationMinutes: ptrInt(120), EnforcedBy: "nas"},
	}
	codes := []store.NewVoucher{
		{ID: uniq("v"), Code: "ABCDEFGH2345"},
		{ID: uniq("v"), Code: "BCDEFGHJ3456"},
		{ID: uniq("v"), Code: "CDEFGHJK4567"},
	}
	fatalf(t, st.Vouchers().CreateBatch(ctx, batch, codes), "creating a batch")

	got, err := st.Vouchers().GetBatch(ctx, batch.ID)
	fatalf(t, err, "reading the batch")
	if got.Counts.Unused != 3 {
		t.Fatalf("histogram says %d unused, want 3", got.Counts.Unused)
	}

	t.Run("from-first-use leaves expiry unset until redemption", func(t *testing.T) {
		v, err := st.Vouchers().GetByCode(ctx, codes[0].Code)
		fatalf(t, err, "reading a code")
		if v.ExpiresAt != nil {
			t.Fatalf("an unredeemed from-first-use voucher already expires at %v", v.ExpiresAt)
		}
	})

	t.Run("redemption is single-use", func(t *testing.T) {
		d := 2 * time.Hour
		first, err := st.Vouchers().Redeem(ctx, codes[0].Code, "sess-1", time.Now(), &d)
		fatalf(t, err, "first redemption")
		if first.State != "in-use" || first.ExpiresAt == nil {
			t.Fatalf("state=%q expiresAt=%v after redemption", first.State, first.ExpiresAt)
		}

		if _, err := st.Vouchers().Revoke(ctx, codes[0].ID, time.Now()); err != nil {
			t.Fatalf("revoking: %v", err)
		}
		if _, err := st.Vouchers().Redeem(ctx, codes[0].Code, "sess-2", time.Now(), &d); err != store.ErrNotFound {
			t.Fatalf("a revoked code was redeemed again: %v", err)
		}
	})

	t.Run("the histogram follows state", func(t *testing.T) {
		got, err := st.Vouchers().GetBatch(ctx, batch.ID)
		fatalf(t, err, "re-reading the batch")
		if got.Counts.Revoked != 1 || got.Counts.Unused != 2 {
			t.Fatalf("histogram is unused=%d revoked=%d, want 2/1", got.Counts.Unused, got.Counts.Revoked)
		}
	})
}

// A required garden entry is not the operator's to delete: without the portal's own
// name and its certificate chain's revocation endpoints, the portal cannot serve
// itself, and a guest sees a captive portal that never loads.
func TestRequiredGardenEntriesCannotBeDeleted(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	required := &store.GardenEntry{
		ID: uniq("g"), SiteID: site.ID, Kind: "fqdn", Value: "portal.example", Origin: "required",
	}
	fatalf(t, st.Garden().Create(ctx, required), "creating a required entry")

	operator := &store.GardenEntry{
		ID: uniq("g"), SiteID: site.ID, Kind: "fqdn", Value: "menu.example", Origin: "operator",
	}
	fatalf(t, st.Garden().Create(ctx, operator), "creating an operator entry")

	if err := st.Garden().Delete(ctx, site.ID, required.ID); err != store.ErrNotFound {
		t.Fatalf("a required entry was deleted: %v", err)
	}
	fatalf(t, st.Garden().Delete(ctx, site.ID, operator.ID), "deleting an operator entry")

	entries, err := st.Garden().ListForSite(ctx, site.ID)
	fatalf(t, err, "listing the garden")
	if len(entries) != 1 || entries[0].ID != required.ID {
		t.Fatalf("garden has %d entries after the deletions", len(entries))
	}
}

// The garden is only as strong as the device that enforces it least well.
func TestEffectiveGardenKindIsTheWeakestDriver(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)

	strong := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Capable", Address: netip.MustParsePrefix("10.40.0.1/32"),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
		Capabilities: store.Capabilities{WalledGarden: "fqdn-sni"},
	}
	fatalf(t, st.RadiusClients().Create(ctx, strong), "creating the capable client")

	kind, err := st.Garden().EffectiveKind(ctx, site.ID)
	fatalf(t, err, "reading the effective kind")
	if kind != "fqdn-sni" {
		t.Fatalf("effective kind is %q with one capable driver", kind)
	}

	weak := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Limited", Address: netip.MustParsePrefix("10.40.0.2/32"),
		Source: "manual", Driver: "coa", SharedSecretID: &secret.ID, Enabled: true,
		Capabilities: store.Capabilities{WalledGarden: "none"},
	}
	fatalf(t, st.RadiusClients().Create(ctx, weak), "creating the limited client")

	kind, err = st.Garden().EffectiveKind(ctx, site.ID)
	fatalf(t, err, "reading the effective kind again")
	if kind != "none" {
		t.Fatalf("effective kind is %q after adding a driver that cannot do a garden at all", kind)
	}
}

func TestPortalSettingsAndHeroImage(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	settings := &store.PortalSettings{
		SiteID: site.ID, VenueName: "Quán Cà Phê",
		Languages: []string{"vi", "en"}, Methods: []string{"voucher", "click-through"},
		ClickThroughMinutes: ptrInt(30),
		TermsSummary:        map[string]string{"vi": "Điều khoản", "en": "Terms"},
	}
	fatalf(t, st.Portal().Upsert(ctx, settings), "writing portal settings")

	got, err := st.Portal().Get(ctx, site.ID)
	fatalf(t, err, "reading portal settings")
	if got.VenueName != "Quán Cà Phê" || len(got.Languages) != 2 {
		t.Fatalf("settings did not round trip: %+v", got)
	}
	if got.TermsSummary["vi"] != "Điều khoản" {
		t.Fatalf("terms did not round trip: %v", got.TermsSummary)
	}
	// Written without a design, the row holds the empty one rather than NULL:
	// the contract requires `design` on every read.
	assertDesign(t, got.Design, `{"accent":null,"headline":{},"blocks":[]}`)

	// A design round-trips verbatim, and an upsert that does not carry one — the
	// site-creation path, which knows only a venue name — keeps it rather than
	// wiping the venue's page.
	design := `{"accent":"#12655c","headline":{"vi":"Chào mừng"},"blocks":[{"id":"b1","kind":"offer","placement":"welcome","visible":true,"code":"CAFE7","content":{"vi":{"title":"Giảm 10%"}}}]}`
	got.Design = json.RawMessage(design)
	fatalf(t, st.Portal().Upsert(ctx, got), "writing a design")
	again, err := st.Portal().Get(ctx, site.ID)
	fatalf(t, err, "reading the design back")
	assertDesign(t, again.Design, design)

	again.Design = nil
	again.VenueName = "Quán Cà Phê Số 7"
	fatalf(t, st.Portal().Upsert(ctx, again), "upserting without a design")
	kept, err := st.Portal().Get(ctx, site.ID)
	fatalf(t, err, "reading after an upsert without a design")
	assertDesign(t, kept.Design, design)
	if kept.VenueName != "Quán Cà Phê Số 7" {
		t.Fatalf("the upsert without a design did not write its other fields: %q", kept.VenueName)
	}

	image := &store.HeroImage{SiteID: site.ID, ContentType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}}
	fatalf(t, st.Portal().PutHeroImage(ctx, image), "uploading a hero image")
	if image.Bytes != 5 {
		t.Fatalf("byte count is %d", image.Bytes)
	}

	meta, err := st.Portal().HeroImageMeta(ctx, site.ID)
	fatalf(t, err, "reading hero image metadata")
	if len(meta.Data) != 0 {
		t.Fatal("the metadata read pulled the image bytes through the pool")
	}

	full, err := st.Portal().HeroImage(ctx, site.ID)
	fatalf(t, err, "reading the hero image")
	if len(full.Data) != 5 {
		t.Fatalf("image is %d bytes", len(full.Data))
	}

	fatalf(t, st.Portal().DeleteHeroImage(ctx, site.ID), "deleting the hero image")
	if _, err := st.Portal().HeroImageMeta(ctx, site.ID); err != store.ErrNotFound {
		t.Fatalf("the hero image survived deletion: %v", err)
	}
}

// The deployment must never be left with nobody who can administer it: there is no
// password-reset channel and no recovery short of the Go binary's first-boot path.
func TestLastAdministratorIsCountable(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()

	admin := &store.Operator{
		ID: uniq("op"), Username: uniq("admin"), PasswordHash: "$argon2id$notarealhash",
		Role: "administrator", Enabled: true,
	}
	fatalf(t, st.Operators().Create(ctx, admin), "creating an administrator")

	before, err := st.Operators().CountOtherEnabledAdministrators(ctx, admin.ID)
	fatalf(t, err, "counting other administrators")

	second := &store.Operator{
		ID: uniq("op"), Username: uniq("admin"), PasswordHash: "$argon2id$notarealhash",
		Role: "administrator", Enabled: true,
	}
	fatalf(t, st.Operators().Create(ctx, second), "creating a second administrator")

	after, err := st.Operators().CountOtherEnabledAdministrators(ctx, admin.ID)
	fatalf(t, err, "counting again")
	if after != before+1 {
		t.Fatalf("count went from %d to %d after adding an administrator", before, after)
	}

	// A disabled administrator does not count. Disabling the last one leaves the
	// deployment just as stranded as deleting it.
	enabled := false
	if _, err := st.Operators().Update(ctx, second.ID, store.OperatorPatch{Enabled: &enabled}); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	back, err := st.Operators().CountOtherEnabledAdministrators(ctx, admin.ID)
	fatalf(t, err, "counting after disabling")
	if back != before {
		t.Fatalf("a disabled administrator is still counted (%d, want %d)", back, before)
	}
}

func TestConsoleSessionCarriesACSRFTokenAndDiesWithTheOperator(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()

	op := &store.Operator{
		ID: uniq("op"), Username: uniq("staff"), PasswordHash: "$argon2id$notarealhash",
		Role: "staff", Enabled: true,
	}
	fatalf(t, st.Operators().Create(ctx, op), "creating an operator")

	sess := &store.OperatorSession{
		TokenHash: uniq("tok"), OperatorID: op.ID, CSRFToken: randomString(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	fatalf(t, st.Authn().Create(ctx, sess), "creating a session")

	gotSess, gotOp, err := st.Authn().Resolve(ctx, sess.TokenHash)
	fatalf(t, err, "resolving the session")
	if gotSess.CSRFToken != sess.CSRFToken || gotOp.ID != op.ID {
		t.Fatal("the session did not round trip")
	}

	// Disabling an operator has to stop their live sessions on the next request,
	// or an administrator disabling an account has bought nothing.
	enabled := false
	_, err = st.Operators().Update(ctx, op.ID, store.OperatorPatch{Enabled: &enabled})
	fatalf(t, err, "disabling the operator")
	if _, _, err := st.Authn().Resolve(ctx, sess.TokenHash); err != store.ErrNotFound {
		t.Fatalf("a disabled operator's session still resolves: %v", err)
	}
}

func addr(t *testing.T, s string) *netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return &a
}

func ptrInt(v int) *int { return &v }

// assertDesign compares as JSON values, because jsonb normalises key order and
// whitespace and a byte comparison would fail a faithful round trip.
func assertDesign(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("stored design is not JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("expected design is not JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("design did not round trip:\n got %s\nwant %s", got, want)
	}
}
