package policy_test

import (
	"context"
	"crypto/rand"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/aaa"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// Tier 2 for three RADIUS-path guarantees -- the half of each that only a real
// FreeRADIUS 3.2.10 can prove, because the claim is about what unlang does with an
// answer, and no Go test can run unlang.
//
//   - The credential is checked. An allow branch of `Auth-Type := Accept` for
//     every allow would accept on the username alone: no password would reach
//     any check, and a voucher over RADIUS would never be claimed -- so it would
//     be unlimited.
//   - The policy question is encoded safely. The body sent to the Policy API is
//     spliced from client-chosen strings. Measured: `"` and `\` are escaped by
//     the server's own printing, but a control byte arrives as `\001` and a
//     non-UTF-8 byte as `\377` -- not JSON -- and a 400 routed into the FAILURE
//     branch would, at a fail-open site, be an accept.
//   - radiusd does not run as root. Asserted for every stack, in startLabOn.
//
// Both tests are listed in the Makefile's LAB_REQUIRED_TESTS, so `make lab-test`
// refuses to be green if either skipped.

// TestRADIUSVerifiesTheCredential checks the credential against the real schema:
// the Policy API serves internal/store, and FreeRADIUS reads the verifier from the
// same database.
func TestRADIUSVerifiesTheCredential(t *testing.T) {
	requireDocker(t)
	l := startLabWithStore(t)
	w := seedCredentials(t, l.appURL, l.ownerURL)

	accept := func(t *testing.T, out string) {
		t.Helper()
		if !strings.Contains(out, "Received Access-Accept") {
			t.Fatalf("want Access-Accept.\nradclient said:\n%s\nradiusd said:\n%s", out, l.radiusdLogs(t, 150))
		}
	}
	reject := func(t *testing.T, out, why string) {
		t.Helper()
		if !strings.Contains(out, "Received Access-Reject") {
			t.Fatalf("%s -- want Access-Reject.\nradclient said:\n%s\nradiusd said:\n%s", why, out, l.radiusdLogs(t, 150))
		}
	}

	// The control first: every refusal below is only evidence if the same stack
	// accepts the right password for the same account.
	t.Run("the right password for an account is accepted", func(t *testing.T) {
		accept(t, l.radclient(t, papRequest(w.account, w.password)))
	})

	t.Run("a wrong password for a real account is refused", func(t *testing.T) {
		out := l.radclient(t, papRequest(w.account, w.password+"-not"))
		reject(t, out, "the account's username was enough to get on the network")
		// The reject tells a hotspot login page nothing about the account: the
		// allow's attributes were put in the reply before rlm_pap refused, and
		// they must not ride out in the reject (RFC 2865 §5.44).
		if strings.Contains(out, "Session-Timeout") || strings.Contains(out, "Acct-Interim-Interval") {
			t.Errorf("the Access-Reject for a wrong password carries the account's plan:\n%s", out)
		}
	})

	t.Run("the account authorized is the account whose password was checked", func(t *testing.T) {
		// Two venues, one username; only the NEWER account opted in. rlm_pap
		// checks the newer one's verifier -- the view's usernames are global --
		// so the allow must be the newer one's too. A Policy API that authorized
		// the OLDEST account with the name would let the newer venue's password
		// get a guest on under the older venue's plan.
		out := l.radclient(t, papRequest(w.shared, w.sharedPassword))
		accept(t, out)
		if !strings.Contains(out, "Session-Timeout = 1800") {
			t.Errorf("accepted with the wrong account's plan (want the opted-in account's 30 minutes, "+
				"Session-Timeout = 1800):\n%s", out)
		}
	})

	// The verifier is read with the sql xlat, whose escape rewrites every byte
	// outside rlm_sql's safe_characters as =XX. With the User-Name spliced in
	// bare, `o'brien` is looked up as `o=27brien` -- its right password refused
	// on both images -- and on the NetworkRADIUS build so is `nguyễn`
	// (measured). So the name travels as hex. Both halves are asserted: the right password is accepted, a wrong
	// one refused, and the quote does not break out of the SQL literal.
	for _, name := range []string{w.quoted, w.unicode} {
		t.Run("an account named "+strconv.Quote(name)+" is checked, and only with its own password", func(t *testing.T) {
			reject(t, l.radclient(t, papRequest(name, w.password+"-not")),
				"a wrong password was accepted for an account whose name needs escaping")
			accept(t, l.radclient(t, papRequest(name, w.password)))
		})
	}

	t.Run("an account with no RADIUS verifier is refused whatever the password", func(t *testing.T) {
		reject(t, l.radclient(t, papRequest(w.argonOnly, w.password)),
			"an argon2id-only account was accepted over RADIUS; rlm_pap cannot verify argon2id, so nothing checked the password")
	})

	t.Run("an account request with no User-Password is refused", func(t *testing.T) {
		reject(t, l.radclient(t, chapLikeRequest(w.account)),
			"an account was accepted with no password attribute at all")
	})

	for _, tc := range []struct{ code, state string }{
		{w.exhausted, "exhausted"},
		{w.inUse, "in-use"},
		{w.expired, "expired (state)"},
		{w.stale, "expired (clock; the sweep has not run)"},
		{w.revoked, "revoked"},
	} {
		t.Run("a voucher that is "+tc.state+" is refused", func(t *testing.T) {
			reject(t, l.radclient(t, papRequest(tc.code, "irrelevant")),
				"a "+tc.state+" voucher was accepted over RADIUS")
		})
	}

	t.Run("an unused voucher sold at another venue is refused and not spent", func(t *testing.T) {
		reject(t, l.radclient(t, papRequest(w.foreign, "irrelevant")),
			"a voucher sold at another venue was accepted by this venue's RADIUS client")
		if state, firstUsed := voucherRow(t, l.ownerURL, w.foreign); state != "unused" || firstUsed != nil {
			t.Errorf("the foreign voucher is now %q, first_used_at %v -- this venue spent another venue's code", state, firstUsed)
		}
	})

	t.Run("an unused voucher is accepted once, claimed, and then refused", func(t *testing.T) {
		accept(t, l.radclient(t, papRequest(w.unused, "irrelevant")))

		state, firstUsed := voucherRow(t, l.ownerURL, w.unused)
		if state != "in-use" || firstUsed == nil {
			t.Errorf("after an accept over RADIUS the voucher is %q, first_used_at %v -- it was not claimed", state, firstUsed)
		}
		reject(t, l.radclient(t, papRequest(w.unused, "irrelevant")),
			"a single-use voucher was accepted a second time over RADIUS")
	})

	// The code as a guest types it at a NAS login page. A guest may type the
	// dashes printed on the slip, or lowercase, and the portal accepts both, so
	// the RADIUS lookup must not be the User-Name exactly either. Each is
	// accepted AND claimed -- the stored row is the one that was spent, not a
	// lookalike.
	for _, tc := range []struct{ what, code, typed string }{
		{"as printed on the slip", w.printed, w.printed[:4] + "-" + w.printed[4:8] + "-" + w.printed[8:]},
		{"in lowercase", w.lower, strings.ToLower(w.lower)},
	} {
		t.Run("an unused voucher typed "+tc.what+" is accepted and claimed", func(t *testing.T) {
			accept(t, l.radclient(t, papRequest(tc.typed, "irrelevant")))
			if state, firstUsed := voucherRow(t, l.ownerURL, tc.code); state != "in-use" || firstUsed == nil {
				t.Errorf("after an accept for %q the voucher %s is %q, first_used_at %v -- it was not the one claimed",
					tc.typed, tc.code, state, firstUsed)
			}
		})
	}

	t.Run("two concurrent sessions get two accounting rows", func(t *testing.T) {
		// The acct_unique finding. Without an Acct-Unique-Session-Id the
		// generated INSERT writes acctuniqueid = '' and radacct's UNIQUE admits
		// exactly one session in the whole deployment.
		for _, sid := range []string{"lab-acct-1", "lab-acct-2"} {
			out := l.exec(t, acctStart(w.account, sid),
				"radclient", "-x", "-t", "10", "-r", "1", "127.0.0.1:1813", "acct", l.secret)
			if !strings.Contains(out, "Received Accounting-Response") {
				t.Fatalf("Accounting-Request for %s got no Accounting-Response:\n%s\nradiusd said:\n%s",
					sid, out, l.radiusdLogs(t, 80))
			}
		}
		ids := accountingIDs(t, l.ownerURL, w.account)
		if len(ids) != 2 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
			t.Errorf("radacct holds %q for two sessions; want two distinct, non-empty acctuniqueids", ids)
		}
	})
}

// TestPolicyQuestionEncoding checks the policy question's encoding on both kinds
// of site. A hostile User-Name must be refused at a fail-closed site AND at a
// fail-open one: the fail-open site is where a question that cannot be read would
// turn into an accept.
func TestPolicyQuestionEncoding(t *testing.T) {
	requireDocker(t)

	hostile := map[string]string{
		// The obvious JSON injection. Measured NOT to work on 3.2.10 even
		// without jsonquote -- the server escapes `"` -- and kept, because it
		// is the one that would matter most if it ever did: the spliced second
		// key names a voucher the fixture store accepts.
		"a spliced second user_name":   `x\",\"user_name\":\"` + labVoucher() + `\"}`,
		"a double quote":               labVoucher() + `\"`,
		"a backslash":                  labVoucher() + `\\`,
		"a C0 control byte":            `LAB\001VOUCHER`,
		"an escape byte":               labVoucher() + `\033`,
		"a trailing newline":           labVoucher() + `\n`,
		"a byte that is not UTF-8":     labVoucher() + `\377`,
		"a DEL":                        labVoucher() + `\177`,
		"NUL-free control soup":        `\001\002\003\004`,
		"a quote-and-control mixture":  `x\",\001\"user_name\":\"` + labVoucher(),
		"control byte then the prefix": `\001` + labVoucher(),

		// Go's TrimSpace trims Unicode White_Space, so a subject trimmed with it
		// would turn each of these into the real voucher. Octal escapes are UTF-8, as radclient puts them on the
		// wire: C2 85 is NEL, a C1 control; C2 A0 is NBSP; E2 80 A8 is U+2028.
		"a trailing NEL (C1)":       labVoucher() + `\302\205`,
		"a C1 control U+0080":       labVoucher() + `\302\200`,
		"a trailing NBSP":           labVoucher() + `\302\240`,
		"a leading NBSP":            `\302\240` + labVoucher(),
		"a trailing LINE SEPARATOR": labVoucher() + `\342\200\250`,
		"a trailing ASCII space":    labVoucher() + ` `,
	}

	for _, stack := range []struct {
		name   string
		advice []aaa.Advice
	}{
		{"fail-closed", nil},
		{"fail-open", []aaa.Advice{aaa.SiteFailurePolicy{
			SiteID: "lab-fail-open", GuestFailureMode: aaa.FailOpen, GuestGraceSeconds: 600,
		}.Advice(300)}},
	} {
		t.Run(stack.name, func(t *testing.T) {
			l := startLabWith(t, stack.advice)

			// The control: this stack accepts the real voucher, so a refusal
			// below is about the name and not about a stack that refuses all.
			if out := l.radclient(t, papRequest(labVoucher(), "irrelevant")); !strings.Contains(out, "Received Access-Accept") {
				t.Fatalf("the %s stack does not accept the valid voucher, so no refusal below proves anything.\n%s\nradiusd said:\n%s",
					stack.name, out, l.radiusdLogs(t, 120))
			}

			for name, userName := range hostile {
				t.Run(name, func(t *testing.T) {
					out := l.radclient(t, rawUserNameRequest(userName))
					if !strings.Contains(out, "Received Access-Reject") {
						t.Errorf("User-Name with %s was not refused at a %s site.\nradclient said:\n%s\nradiusd said:\n%s",
							name, stack.name, out, l.radiusdLogs(t, 80))
					}
					if carriesDegradedClass(out) {
						t.Errorf("User-Name with %s reached the FAILURE branch -- a degraded grant -- at a %s site", name, stack.name)
					}
				})
			}
		})
	}
}

// ── requests ────────────────────────────────────────────────────────────────

func papRequest(user, password string) string {
	return rawUserNameRequest(radclientQuote(user), "User-Password = "+strconv.Quote(password))
}

// chapLikeRequest carries no User-Password at all -- what a NAS configured for
// CHAP sends, and what an account over RADIUS can no longer be accepted with.
func chapLikeRequest(user string) string {
	return rawUserNameRequest(radclientQuote(user), "")
}

// rawUserNameRequest takes the User-Name as radclient's own quoted-string body,
// so a test can spell a control byte as `\001` and radclient puts the byte on the
// wire -- measured: jsonquote on the server saw \u0001.
func rawUserNameRequest(userNameBody string, extra ...string) string {
	attrs := []string{
		`User-Name = "` + userNameBody + `"`,
		`NAS-IP-Address = 127.0.0.1`,
		`NAS-Identifier = "lab-policy"`,
		`Calling-Station-Id = "02:11:22:33:44:55"`,
		`Called-Station-Id = "00:11:22:33:44:00:foyer-lab"`,
		`Message-Authenticator = 0x00`,
	}
	if len(extra) == 0 {
		attrs = append(attrs, `User-Password = "irrelevant"`)
	}
	for _, e := range extra {
		if e != "" {
			attrs = append(attrs, e)
		}
	}
	return strings.Join(attrs, "\n") + "\n"
}

// radclientQuote escapes a plain value for the inside of radclient's quotes.
func radclientQuote(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func acctStart(user, sessionID string) string {
	return strings.Join([]string{
		`Acct-Status-Type = Start`,
		`User-Name = "` + radclientQuote(user) + `"`,
		`Acct-Session-Id = "` + sessionID + `"`,
		`NAS-IP-Address = 127.0.0.1`,
		`NAS-Identifier = "lab-policy"`,
		`NAS-Port = 1`,
		`Calling-Station-Id = "02:11:22:33:44:55"`,
		`Called-Station-Id = "00:11:22:33:44:00:foyer-lab"`,
	}, "\n") + "\n"
}

// ── the world ───────────────────────────────────────────────────────────────

type credentialWorld struct {
	account, argonOnly, password string

	// shared is one username held by two accounts at two sites; only the newer
	// one has a RADIUS verifier, and its password is sharedPassword.
	shared, sharedPassword string

	// quoted and unicode are opted-in accounts whose names the sql xlat's escape
	// has to handle; their password is password.
	quoted, unicode string

	unused, inUse, exhausted, expired, stale, revoked string

	// printed and lower are unused vouchers the harness sends the way a guest
	// types them at a NAS login page: as printed on the slip, and in lowercase.
	printed, lower string

	// foreign is an unused voucher sold at a venue the harness's RADIUS client
	// does not belong to (site binding).
	foreign string
}

// seedCredentials writes the world through internal/store -- the same code the
// admin console writes through -- so the constraints the schema enforces (the
// argon2id and bcrypt CHECKs, the global verifier username) are the ones in force.
// It logs in as the console does, appURL; ownerURL is only for the raw UPDATEs that
// put vouchers into states no console operation produces.
func seedCredentials(t *testing.T, appURL, ownerURL string) credentialWorld {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := store.NewStore(ctx, appURL)
	if err != nil {
		t.Fatalf("opening the harness store: %v", err)
	}
	defer st.Close()

	w := credentialWorld{
		account:   "lab-guest-" + randomToken(t)[:8],
		argonOnly: "lab-argon-" + randomToken(t)[:8],
		password:  "correct horse " + randomToken(t)[:10],
	}

	site := &store.Site{ID: "site-" + randomToken(t)[:8], Name: "Lab lobby"}
	if err := st.Sites().Create(ctx, site); err != nil {
		t.Fatalf("site: %v", err)
	}

	hash, err := identity.HashPassword(w.password)
	if err != nil {
		t.Fatal(err)
	}
	w.quoted = "lab-o'brien-" + randomToken(t)[:6]
	w.unicode = "lab-nguyễn-" + randomToken(t)[:6]
	minutes := 60
	for _, name := range []string{w.account, w.argonOnly, w.quoted, w.unicode} {
		a := &store.Account{
			ID: "acct-" + randomToken(t)[:10], SiteID: site.ID, Username: name,
			PasswordHash: hash, Enabled: true, Plan: store.Plan{DurationMinutes: &minutes},
		}
		if err := st.Accounts().Create(ctx, a); err != nil {
			t.Fatalf("account %s: %v", name, err)
		}
		if name == w.argonOnly {
			continue
		}
		verifier, err := identity.HashPAPVerifier(w.password)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Accounts().OptInTo8021X(ctx, a.ID, verifier, nil); err != nil {
			t.Fatalf("verifier: %v", err)
		}
	}

	// The binding case: the OLDER account has no verifier and a 60-minute plan,
	// the NEWER one a verifier and a 30-minute plan.
	w.shared = "lab-shared-" + randomToken(t)[:8]
	w.sharedPassword = "battery staple " + randomToken(t)[:10]
	for i, minutes := range []int{60, 30} {
		s := &store.Site{ID: "site-" + randomToken(t)[:8], Name: "Lab venue " + strconv.Itoa(i)}
		if err := st.Sites().Create(ctx, s); err != nil {
			t.Fatalf("site: %v", err)
		}
		m := minutes
		a := &store.Account{
			ID: "acct-" + randomToken(t)[:10], SiteID: s.ID, Username: w.shared,
			PasswordHash: hash, Enabled: true, Plan: store.Plan{DurationMinutes: &m},
		}
		if err := st.Accounts().Create(ctx, a); err != nil {
			t.Fatalf("account %s at %s: %v", w.shared, s.ID, err)
		}
		if i == 1 {
			verifier, err := identity.HashPAPVerifier(w.sharedPassword)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Accounts().OptInTo8021X(ctx, a.ID, verifier, nil); err != nil {
				t.Fatalf("verifier: %v", err)
			}
		}
	}

	codes := make([]string, 8)
	vouchers := make([]store.NewVoucher, len(codes))
	for i := range codes {
		codes[i] = voucherCode(t)
		vouchers[i] = store.NewVoucher{ID: "v-" + randomToken(t)[:10], Code: codes[i]}
	}
	w.unused, w.inUse, w.exhausted, w.expired, w.stale, w.revoked, w.printed, w.lower =
		codes[0], codes[1], codes[2], codes[3], codes[4], codes[5], codes[6], codes[7]
	batch := &store.VoucherBatch{
		ID: "b-" + randomToken(t)[:10], SiteID: site.ID, Label: "lab", Count: len(codes),
		Plan: store.Plan{DurationMinutes: &minutes}, ExpiryMode: "from-first-use",
	}
	if err := st.Vouchers().CreateBatch(ctx, batch, vouchers); err != nil {
		t.Fatalf("voucher batch: %v", err)
	}

	// The harness's radclient runs inside the FreeRADIUS container, so every
	// request arrives from labNASAddress. Registering that client at the lobby is
	// what an operator does in the console, and it is what makes
	// the lobby's vouchers good here: a voucher over RADIUS is claimed at the site
	// of the client it arrived from, and nowhere else.
	rc := &store.RadiusClient{
		ID: "rc-" + randomToken(t)[:10], SiteID: site.ID, Name: "Lab NAS",
		Address: netip.MustParsePrefix(labNASAddress + "/32"),
		Source:  "manual", Driver: "mikrotik", Enabled: true,
	}
	if err := st.RadiusClients().Create(ctx, rc); err != nil {
		t.Fatalf("binding the harness RADIUS client to the lobby: %v", err)
	}
	elsewhere := &store.Site{ID: "site-" + randomToken(t)[:8], Name: "Lab rooftop"}
	if err := st.Sites().Create(ctx, elsewhere); err != nil {
		t.Fatalf("site: %v", err)
	}
	w.foreign = voucherCode(t)
	foreignBatch := &store.VoucherBatch{
		ID: "b-" + randomToken(t)[:10], SiteID: elsewhere.ID, Label: "rooftop", Count: 1,
		Plan: store.Plan{DurationMinutes: &minutes}, ExpiryMode: "from-first-use",
	}
	if err := st.Vouchers().CreateBatch(ctx, foreignBatch,
		[]store.NewVoucher{{ID: "v-" + randomToken(t)[:10], Code: w.foreign}}); err != nil {
		t.Fatalf("foreign voucher batch: %v", err)
	}

	pool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, u := range []struct{ sql, code string }{
		{`UPDATE vouchers SET state = 'in-use', first_used_at = now() WHERE code = $1`, w.inUse},
		{`UPDATE vouchers SET state = 'exhausted', first_used_at = now() WHERE code = $1`, w.exhausted},
		{`UPDATE vouchers SET state = 'expired', expires_at = now() - interval '1 hour' WHERE code = $1`, w.expired},
		{`UPDATE vouchers SET expires_at = now() - interval '1 minute' WHERE code = $1`, w.stale},
		{`UPDATE vouchers SET state = 'revoked', revoked_at = now() WHERE code = $1`, w.revoked},
	} {
		if _, err := pool.Exec(ctx, u.sql, u.code); err != nil {
			t.Fatalf("%s: %v", u.sql, err)
		}
	}
	return w
}

// voucherCode draws a code from the schema's own alphabet (migration 00005).
func voucherCode(t *testing.T) string {
	t.Helper()
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func voucherRow(t *testing.T, ownerURL, code string) (string, *time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var state string
	var first *time.Time
	if err := pool.QueryRow(ctx, `SELECT state, first_used_at FROM vouchers WHERE code = $1`, code).Scan(&state, &first); err != nil {
		t.Fatal(err)
	}
	return state, first
}

func accountingIDs(t *testing.T, ownerURL, user string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT acctuniqueid FROM radacct WHERE username = $1 ORDER BY acctsessionid`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// ── the process is not root ─────────────────────────────────────────────────

var uidLineRE = regexp.MustCompile(`(?m)^Uid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)

// assertRadiusdIsNotRoot reads the running server's own credentials, from the
// kernel rather than from the configuration that asked for them: /proc/1/status
// of the container, where radiusd is PID 1. All four uids -- real, effective,
// saved, filesystem -- must be non-zero; a saved uid of 0 is a process that can
// become root again.
func assertRadiusdIsNotRoot(t *testing.T, container string) {
	t.Helper()
	out := mustRun(t, 20*time.Second, "docker", "exec", container, "cat", "/proc/1/status")
	m := uidLineRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no Uid line in radiusd's /proc/1/status:\n%s", out)
	}
	for i, which := range []string{"real", "effective", "saved", "filesystem"} {
		if m[i+1] == "0" {
			t.Fatalf("radiusd's %s uid is 0: the AAA data plane is running as root.\n%s",
				which, strings.SplitN(out, "\nGid:", 2)[0])
		}
	}
	t.Logf("MEASURED: radiusd runs as uid %s (real/effective/saved/fs %s/%s/%s/%s)", m[2], m[1], m[2], m[3], m[4])
}
