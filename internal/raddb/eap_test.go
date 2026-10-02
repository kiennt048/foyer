package raddb

import (
	"strings"
	"testing"
)

// eapConfig is the smallest EAP-enabled configuration the generator accepts.
// Paths only; nothing here creates a certificate and no image ships one.
func eapConfig(c *Config) {
	c.EAP = EAPConfig{
		Enabled:        true,
		DefaultEAPType: "tls",
		CertDir:        "/etc/raddb/certs",
		ServerCertFile: "/etc/raddb/certs/server.pem",
		PrivateKeyFile: "/etc/raddb/certs/server.key",
		CAFile:         "/etc/raddb/certs/ca.pem",
	}
}

// TestTheEAPPathIsAuthorizedAtAll is the lock on a hole that shipped.
//
// `eap { ok = return; updated = return }` in authorize returns before `rest`, by
// design — nothing below may overwrite the Auth-Type rlm_eap sets. The
// consequence nobody had written down is that the policy engine was **never
// asked about an EAP request**: every certificate the configured CA had signed
// was accepted, permanently, and a revoked identity or a disabled account had no
// effect on 802.1X at all.
//
// Made to fail once: deleting the `rest { ... }` block from the generated
// post-auth turns this red and leaves every other test in the package green,
// which is the shape of the defect it is written against.
func TestTheEAPPathIsAuthorizedAtAll(t *testing.T) {
	site := readGenerated(t, generate(t, eapConfig), "sites-available", "default")
	post := sectionOf(t, site, "post-auth {")

	if !strings.Contains(post, "rest {") {
		t.Fatalf("the generated post-auth never calls the policy engine, so an EAP request is authorized by nothing\n--- post-auth ---\n%s", post)
	}

	// Every rcode given an action, for the same reason authorize gives them:
	// rlm_rest maps HTTP 403 to `userlock`, whose default action is to return
	// from the section — so a denial would skip the branch that refuses it.
	restBlock := sectionOf(t, post, "rest {")
	for _, code := range []string{"userlock", "fail", "invalid", "reject", "notfound", "noop", "ok", "updated"} {
		if !strings.Contains(restBlock, code+" = 1") {
			t.Errorf("post-auth's rest block gives no action for %q; unlang's default for userlock is to return, so the denial branch below it would never run", code)
		}
	}

	if !strings.Contains(post, "reject") {
		t.Error("post-auth consults the policy engine and never refuses on the answer")
	}
}

// TestEAPAuthorizationKeysOnTheProvenIdentity is security.md §8, applied to the
// method Foyer actually ships.
//
// §8 is usually described for PEAP and TTLS, where the outer identity is
// famously `anonymous@realm`. It is equally true of EAP-TLS and that is not
// obvious: measured on 3.2.10, a supplicant presenting the client
// certificate for one account while sending the `User-Name` of **another real
// account** authenticates successfully, and the outer server logs the name it
// chose. The only trustworthy identity in that exchange is the certificate's
// Common Name.
//
// So the body the Policy API is sent must carry the certificate CN in
// `user_name` — the field `internal/aaa.AuthorizationSubject` reads — and the
// supplicant's claim in `outer_user_name`, which that function is deliberately
// unable to reach.
func TestEAPAuthorizationKeysOnTheProvenIdentity(t *testing.T) {
	dir := generate(t, eapConfig)

	site := readGenerated(t, dir, "sites-available", "default")
	post := sectionOf(t, site, "post-auth {")
	if !strings.Contains(post, "Tmp-String-2 := \"%{TLS-Client-Cert-Common-Name}\"") {
		t.Errorf("post-auth does not resolve the certificate Common Name into Tmp-String-2, so there is no proven identity to key on\n--- post-auth ---\n%s", post)
	}
	// Tmp-String-0 already carries the EAP presence marker set in authorize.
	// Reusing it here would be a collision that only appears under EAP, which is
	// the one place nothing else in this suite runs.
	if strings.Contains(post, "Tmp-String-0 := \"%{TLS-Client-Cert-Common-Name}\"") {
		t.Error("post-auth writes the certificate CN over Tmp-String-0, which authorize uses for the EAP presence marker")
	}

	rest := readGenerated(t, dir, "mods-available", "rest")
	body := sectionOf(t, rest, "post-auth {")
	if body == "" {
		t.Fatal("the rest module has no post-auth section, so `rest` in post-auth would send the authorize section's body — keyed on the outer identity")
	}
	if !strings.Contains(body, `"user_name_hex":"%{hex:&Tmp-String-2}"`) {
		t.Errorf("the EAP-path policy request does not key on the proven identity:\n%s", body)
	}
	if strings.Contains(body, `"user_name_hex":"%{hex:&User-Name}"`) {
		t.Errorf("the EAP-path policy request keys on the supplicant's own claim. That is exactly what security.md §8 forbids: send `anonymous@`, get whatever `anonymous@` is allowed:\n%s", body)
	}
	if !strings.Contains(body, `"outer_user_name_hex":"%{hex:&User-Name}"`) {
		t.Errorf("the claim is not carried at all, so the Policy API cannot log it or apply its anonymity check:\n%s", body)
	}
}

// TestADeniedEAPRequestCarriesNoKeysAndNoSuccess.
//
// Measured: turning an Access-Accept into an Access-Reject in
// post-auth does not tear down the reply that was built on the way there. The
// refusal went out carrying `EAP-Message` holding an **EAP-Success** and both
// `MS-MPPE-Recv-Key` and `MS-MPPE-Send-Key` — so eapol_test reported
// `CTRL-EVENT-EAP-SUCCESS` for a session the NAS had just been told to refuse,
// and session keying material travelled in a packet that must carry none.
//
// Two different failures behind one green assertion, which is why this checks
// both: a supplicant that believes it authenticated, and keys on the wire.
func TestADeniedEAPRequestCarriesNoKeysAndNoSuccess(t *testing.T) {
	site := readGenerated(t, generate(t, eapConfig), "sites-available", "default")
	post := sectionOf(t, site, "post-auth {")

	for _, attr := range []string{"EAP-Message", "MS-MPPE-Recv-Key", "MS-MPPE-Send-Key"} {
		if !strings.Contains(post, attr+" !* ANY") {
			t.Errorf("the denial branch does not remove %s from the reply. Measured: the Access-Reject then carries it, "+
				"and for EAP-Message that means an EAP-Success inside a refusal\n--- post-auth ---\n%s", attr, post)
		}
	}
}

// TestTheAuthenticationLedgerRecordsTheProvenIdentity.
//
// `radpostauth` is the one table an operator turns to when somebody asks who got
// on the network, and the admin console reads it. The stock query writes
// `%{User-Name}`, which on any EAP method is a value the supplicant chose and
// nothing checked — measured naming another real account. Writing it here would
// make every 802.1X row an attacker-controlled audit record.
func TestTheAuthenticationLedgerRecordsTheProvenIdentity(t *testing.T) {
	q := readGenerated(t, generate(t, eapConfig), "mods-config", "sql", "main", "postgresql", "queries.conf")
	post := sectionOf(t, q, "post-auth {")

	if !strings.Contains(post, "%{%{Tmp-String-2}:-%{User-Name}}") {
		t.Errorf("the post-auth query writes an identity the supplicant chose:\n%s", post)
	}
	// The `:-` fallback has to be there too: on every non-EAP request
	// Tmp-String-2 is unset, and a bare %{Tmp-String-2} would record an empty
	// username for every voucher and every MAC-auth in the deployment.
	if strings.Contains(post, "'%{Tmp-String-2}'") {
		t.Error("the post-auth query has no fallback, so every non-EAP authentication would be recorded with an empty username")
	}
}

// TestNothingEmitsPasswordWithHeader is a guard against the most dangerous thing
// measured, and it is cheap only because the hole is not open yet.
//
// `rlm_pap` parses a `{scheme}` prefix in braces. An argon2id hash has none, so
// rlm_pap logs `No {...} in Password-With-Header, re-writing to
// Cleartext-Password` and **the encoded hash becomes the known-good password**.
// Proven with an eapol_test run that authenticated by sending the stored
// verifier as the password.
//
// That converts a one-way argon2id verifier into password-equivalent material —
// the property security.md §1 reserves for NT hashes — and it fails safe only
// for as long as nobody types the hash. A test asserting "the wrong password is
// refused" is green throughout.
//
// ── the first credential lookup, and its shape argued here ────────────────
//
// An account allow is verified by rlm_pap, so exactly ONE lookup exists, and
// everything else stays forbidden.
//
//   - It lives in sites-available/default and nowhere else. No
//     authorize_check_query, so rlm_sql never turns a table row into an
//     arbitrary attribute of the row's choosing.
//   - It reads `radius_pap_verifiers` -- the two-column view, enabled accounts
//     only, readable without a tenant GUC -- and nothing else.
//   - It lands in Crypt-Password and nothing else, and only after a regex has
//     checked it is bcrypt-shaped. Never Password-With-Header, Cleartext-Password
//     or NT-Password.
func TestNothingEmitsPasswordWithHeader(t *testing.T) {
	for _, name := range []string{"plain", "eap"} {
		t.Run(name, func(t *testing.T) {
			var mutate func(*Config)
			if name == "eap" {
				mutate = eapConfig
			}
			dir := generate(t, mutate)

			for _, f := range [][]string{
				{"mods-config", "sql", "main", "postgresql", "queries.conf"},
				{"mods-available", "sql"},
				{"mods-available", "rest"},
				{"mods-available", "pap"},
				{"sites-available", "default"},
			} {
				body := readGenerated(t, dir, f...)
				if strings.Contains(body, "Password-With-Header") {
					t.Errorf("%v emits Password-With-Header. rlm_pap rewrites an unrecognised header to "+
						"Cleartext-Password, so an argon2 hash placed there becomes the password — measured", f)
				}
				for _, credential := range []string{
					"authorize_check_query", "authorize_reply_query",
					"Cleartext-Password", "NT-Password",
				} {
					if strings.Contains(body, credential) {
						t.Errorf("%v mentions %s. The one verifier lookup in this tree is Crypt-Password "+
							"from radius_pap_verifiers, and any other shape is a decision under security.md §1", f, credential)
					}
				}
				if f[0] != "sites-available" && strings.Contains(body, "Crypt-Password") {
					t.Errorf("%v mentions Crypt-Password; the only verifier lookup lives in sites-available/default", f)
				}
			}

			site := uncommented(readGenerated(t, dir, "sites-available", "default"))
			const lookup = `&Crypt-Password := "%{sql:SELECT verifier FROM radius_pap_verifiers WHERE username = convert_from(decode('%{hex:&User-Name}', 'hex'), 'UTF8')}"`
			if n := strings.Count(site, "Crypt-Password :="); n != 1 || !strings.Contains(site, lookup) {
				t.Errorf("want exactly one verifier assignment, and it must be:\n  %s\ngot %d assignment(s)", lookup, n)
			}
			if !strings.Contains(site, `&control:Crypt-Password =~ /^\$2[ab]\$[0-9]{2}\$[.\/A-Za-z0-9]{53}$/`) {
				t.Error("the verifier reaches rlm_pap without its bcrypt shape being checked")
			}
		})
	}
}

// TestTheTLSSessionCacheSaysWhatItIs.
//
// `docs/rules/radius.md` asks for this cache in Redis. Measured: there is no
// Redis backend for it in 3.2.x — it is internal to rlm_eap_tls rather than an
// rlm_cache instance, and a `redis_server` item in the section is discarded
// while `radiusd -XC` still exits 0. `persist_dir` is what exists.
//
// Off by default, because a resumed session skips the method that would
// otherwise have to be completed. On, when a directory is named.
func TestTheTLSSessionCacheSaysWhatItIs(t *testing.T) {
	t.Run("off by default, and says what turning it on costs", func(t *testing.T) {
		eap := readGenerated(t, generate(t, eapConfig), "mods-available", "eap")
		if !strings.Contains(eap, "enable = no") {
			t.Error("the session cache is on in a tree that asked for no cache directory")
		}
		if strings.Contains(eap, "cost is zero") {
			t.Error("the template still says the cost of the missing cache is zero. It is zero only while nothing " +
				"uses EAP, and this file is the EAP module")
		}
	})

	t.Run("persisted when a directory is named", func(t *testing.T) {
		eap := readGenerated(t, generate(t, func(c *Config) {
			eapConfig(c)
			c.EAP.TLSSessionCacheDir = "/var/lib/foyer/tlscache"
		}), "mods-available", "eap")

		if !strings.Contains(eap, `persist_dir = "/var/lib/foyer/tlscache"`) {
			t.Errorf("the cache directory was named and the tree does not persist to it:\n%s", eap)
		}
		if !strings.Contains(eap, "enable = yes") {
			t.Error("persist_dir is set on a cache that is still disabled, which is a directory that never fills")
		}
		// The item, not the word: the comment beside it explains why Redis is
		// not available, and a test that cannot tell an explanation from a
		// configuration item would forbid writing the explanation down.
		if strings.Contains(eap, "redis_server") {
			t.Error("the tree configures redis_server for a cache that has no Redis backend in 3.2.x; the item " +
				"would be discarded silently and radiusd -XC would still exit 0")
		}
	})
}

// TestTurningEAPOffRemovesItEntirely: a stale mods-enabled symlink is a live
// configuration file, and the EAP surface is larger than one module.
func TestTurningEAPOffRemovesItEntirely(t *testing.T) {
	off := generate(t, nil)

	rest := readGenerated(t, off, "mods-available", "rest")
	if strings.Contains(rest, "post-auth {") {
		t.Error("a tree with EAP off carries the EAP-path policy section, which would be dead configuration " +
			"that reads like a live one")
	}
	site := readGenerated(t, off, "sites-available", "default")
	if strings.Contains(site, "TLS-Client-Cert-Common-Name") {
		t.Error("a tree with EAP off refers to the certificate CN")
	}
	// And the fallback in the ledger query still has to work, because the
	// non-EAP tree is every deployment that has not turned EAP on.
	q := readGenerated(t, off, "mods-config", "sql", "main", "postgresql", "queries.conf")
	if !strings.Contains(q, "%{%{Tmp-String-2}:-%{User-Name}}") {
		t.Error("the ledger query differs between the EAP and non-EAP trees, so one of them is untested by the other")
	}
}
