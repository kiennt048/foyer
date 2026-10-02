# internal/aaa — CONTRACT

The Policy API answers authorization questions from FreeRADIUS over `rlm_rest`, and
nothing else.

## Invariants

1. **No credential material crosses this boundary.** Never an NT or cleartext
   password attribute, never a list qualifier of any kind. `reply.go` gates the
   reply about to be written, not the intent behind it, because `Decision.
   ReplyAttributes` is a public map that a VendorProfile or a vendor-attribute
   column can write to after the decision is made. On a violation the **whole
   reply is dropped** and the answer is 503 — a reply that carried credential
   material is a reply whose provenance is unknown.

   The stakes are not one password. Returning a verifier here turns the Policy API
   into an endpoint that yields the credential for **any username supplied**: a full
   credential-store dump, over HTTP, without touching PostgreSQL. FreeRADIUS fetches
   verifiers itself via `rlm_sql` from a minimally-privileged view.

   **"Minimally-privileged" is true at both ends** (migration `00019`):
   FreeRADIUS logs in as `foyer_radius`, which may read that view and `nas` and
   nothing else of Foyer's, and the view `radius_pap_verifiers` is owned by
   `foyer_credential_view`, which holds SELECT on the five columns its query names —
   never by a superuser. `internal/store/loginroles_test.go` asserts both through
   FreeRADIUS's real login, and `deploy/verify.sh` asks the running database.

   **An allow is authorization, not authentication, and it says which it still
   needs**. Every allow carries
   `Foyer-Identity-Kind` (`IdentityKindAttribute`): `voucher` — the code was the
   bearer credential, nothing further to prove — or `account` — the generated
   virtual server must still have `rlm_pap` verify `User-Password` against the
   bcrypt verifier it reads from `radius_pap_verifiers`, and refuses when there is
   none. Without the kind, an allow would have to become `Auth-Type := Accept` and a
   guest account's username alone would get a device on the network. The attribute is defined
   at 3000 in the generated dictionary, which FreeRADIUS never encodes into a
   packet (measured on 3.2.10), and it is set in `Engine.allow` and nowhere else.
   An allow with no kind is refused by the virtual server.

2. **Authorization keys on the inner identity only.** `Request.AuthorizationSubject`
   is the single function that answers "who is this about", and it returns
   `UserName`. `OuterUserName` is client-supplied and arbitrary; a path that reads it
   lets an attacker choose their own authorization subject. An inner identity that is
   a tunnel placeholder (`anonymous`, `anonymous@realm`, `@realm`) is **refused**
   rather than looked up — as a rejection and never as an error, because an error
   reaches the failure branch and on a fail-open site that would authorize it.

   **The subject is the attribute's own string: never trimmed.** Trimming it with
   `strings.TrimSpace` would trim Unicode White_Space — a voucher followed by NBSP,
   NEL (U+0085) or U+2028 would be the voucher. An **account** is looked up exactly as sent, which is also how the tree's
   rlm_sql reads its PAP verifier. A **voucher** is the one identity with a typed
   form: `voucherKey` upper-cases ASCII and drops ASCII `-` and space — the
   portal's rule for what a guest types (`internal/portal.NormaliseCode`), so
   `abcd-efgh-jkmn` over RADIUS is the code printed `ABCD-EFGH-JKMN` — and
   is deliberately no wider: any other character, including NBSP and letters whose
   Unicode upper case is ASCII, makes the subject not a voucher, and it is then
   looked up as itself. The portal drops those characters because a browser paste
   is its input; a User-Name is also an account's name, and there an invisible
   character is a way to make the question mean something other than what the
   Policy API reads (row `invalid` below). `TestAUnicodeSpaceIsNotTrimmedIntoAnIdentity`,
   `TestAVoucherTypedAsPrintedIsTheVoucher`, `TestAnAccountIsItsNameExactly`, and
   tier 2 in `lab/policy` (`TestPolicyQuestionEncoding`,
   `TestRADIUSVerifiesTheCredential`).

3. **Three answers, never two.** Callers depend on the difference:

   | Outcome | HTTP | `rlm_rest` rcode | Meaning |
   |---|---|---|---|
   | allow | 200 + JSON reply pairs | `ok` / `updated` | authorized |
   | deny | 403 | not `fail`, not `invalid` | authoritatively refused; a decision |
   | undecided | 503 | `fail` | could not decide; the site's declared failure branch runs |
   | unreadable request | 400 | `invalid` | **refused** — the reject branch, never the failure branch |

   The last row is a refusal, not the failure branch, and routing it to the
   failure branch would be a hole. The question is spliced from values the CLIENT
   chose; measured on 3.2.10, an unencoded control byte in `User-Name` arrives as
   `\001` — not JSON — which would let the client pick the failure branch, and at a
   fail-open site that is an accept. So the generated module JSON-encodes every value (`%{jsonquote:}`, and the
   two identities as `user_name_hex`/`outer_user_name_hex` from `%{hex:}`
   because jsonquote mangles bytes >= 0x80 on amd64 — decodeRequest decodes
   them and refuses bad hex, non-UTF-8, and a name sent both ways), the
   handler refuses anything but one object of string values with no duplicate
   key, no unknown field, no trailing data and valid UTF-8 (`decodeRequest`) —
   and a key is known only by its EXACT name, because `encoding/json` matches
   `USER_NAME` to `user_name` case-insensitively and a byte-wise duplicate check
   does not see that pair — and
   the virtual server sends `invalid` to the reject branch. An identity carrying a
   C0 control, DEL, a C1 control (U+0080–U+009F: NEL is one) or
   U+FFFD is a **denial** (`identityFault`), not an error.
   rlm_rest's mapping, measured: 204→`ok`, 200+body→`updated`, 401→`reject`,
   403→`userlock`, 404→`notfound`, 400/422→`invalid`, 500/503→`fail`.

   **A store that cannot be reached is undecided, never a denial.** Answering "not
   found" for a database outage mints an authoritative Access-Reject while the
   control plane knows nothing, and bypasses the per-site failure model entirely — a
   venue configured fail-open is silently fail-closed for the duration, and nothing
   says so. `PolicyStore` implementations must therefore distinguish absence
   (`(nil, nil)` or `ErrIdentityNotFound`, or implement `IdentityNotFoundReporter`)
   from failure.

4. **Failure behaviour is declared per site, and this package cannot apply it.**
   By the time the question is asked, nothing here is reachable. `failure.go` owns
   the *model*; `internal/raddb` renders `Advice` into the generated virtual server,
   which is where it actually runs. Changing a site's failure mode is therefore a
   `restart`-class change.

   - **802.1X is fail-closed and is not configurable.** `SiteFailurePolicy.For`
     ignores the configured value for `MethodDot1X`. Failing open there puts
     unauthenticated devices on a corporate network.
   - Guest is the operator's choice. Anything that is not one of the two defined
     modes — empty, a typo, a value from a newer schema — resolves closed.
   - A fail-open grant is **bounded** (`Session-Timeout`, capped at an hour) and
     carries `Class = Foyer:degraded` so those sessions are identifiable in
     `radacct` afterwards. It carries **nothing that would have been looked up**.

5. **Bounded cost per request, because `rlm_rest` blocks a FreeRADIUS worker.**
   An enforced context deadline (default 1.5 s, below `rest`'s 2 s, below the ~3 s
   NAS retransmit interval), a concurrency ceiling that **refuses rather than
   queues** (default 64), and a capped body (16 KiB). Each answers 503 when it
   fires, so a Policy API that is merely slow is treated exactly like one that is
   down — otherwise a fail-open site quietly becomes fail-closed under load.

6. **This package never writes accounting tables.** It evaluates policy.

   **A voucher is claimed only at the site the request arrived at.**
   The site is `PolicyStore.RADIUSClientSite(Request.ClientIP)` — the most
   specific enabled RADIUS client covering the packet's source address, which is
   the client FreeRADIUS verified the packet against. It is resolved **before** the
   voucher's state is read into a reason, so a code from another venue is refused
   with the same sentence as a code that does not exist, and a request whose
   client resolves to no site is refused outright: there is no venue to claim at,
   and `ClaimVoucher` with an empty site would claim at every venue. Survey:
   PacketFence resolves the switch (and its location) from the RADIUS client's
   address; ClearPass keys Network Devices and their location groups on the NAD IP.

   It does write **one** row outside them: an `unused` voucher is **claimed**
   (`PolicyStore.ClaimVoucher`) before it is allowed — `in-use`, `first_used_at`,
   and a from-first-use expiry, in the portal's own single statement, so a code
   is single-use across the portal and RADIUS together. Only `unused` is
   claimable; `in-use`, `exhausted`, `expired`, `revoked` and any unknown state
   are denials, and a claim that finds nothing (somebody else won) is a denial.
   `Session-Timeout` is the plan's duration or the time left before the code
   expires, whichever is shorter. A path that did not claim would accept `in-use`
   and `exhausted` codes.

7. **It decides; it does not render.** `docs/rules/nas-drivers.md` keeps two
   abstractions apart because vendor families differ in **how a client is made
   captive in the first place, and that happens inside the RADIUS reply**. This
   package therefore writes no vendor attribute names itself.
   `Engine.allow` is the **only** place an accept `Decision` is built, and it
   renders a `nas.ReplyDecision` through a `nas.VendorProfile`. A second path
   that assembled attributes by hand would undo the seam without failing a
   compile, so there is a test that asserts one decision produces three different
   replies under three vendors.

   Two consequences:

   - **`Filter-Id` never carries a rate pair.** RFC 2865 §5.11 defines Filter-Id as
     the **name of a filter list already configured on the NAS** — the same shape as
     Cisco's `url-redirect-acl`, a name Foyer cannot create — so a value such as
     `"10000/5000"` is a filter-list name that would have to exist on the device, and
     nothing treats it as a rate. A rate travels on the decision, and a profile with
     an attribute for it renders one.
   - **What a profile cannot carry is reported, never dropped.**
     `Decision.Unrenderable` and `Decision.DroppedAttributes` are logged by
     `writeDecision` — names only, never values — because a rate limit an
     operator typed into the admin console that reaches no NAS is the failure
     `docs/rules/frontend.md` is written about, and this is the only place in
     the running system that knows.

   **The vendor is not resolved per request.** `NewEngine` installs the RFC-only
   profile and `WithVendorProfile` selects one for a lab or a single-vendor
   deployment. Per-request selection would resolve the vendor from the RADIUS client
   the packet arrived on.

## Required of the store

`sites` carries two columns for invariant 4 to be operator-configurable (migrations
`00007` and `00009`); a site with no row of its own resolves to `DefaultPolicy`, which
is fail-closed:

```sql
ALTER TABLE sites
    ADD COLUMN guest_policy_failure_mode text NOT NULL DEFAULT 'fail-closed'
        CHECK (guest_policy_failure_mode IN ('fail-closed', 'fail-open')),
    ADD COLUMN guest_policy_failure_grace_seconds integer NOT NULL DEFAULT 300
        CHECK (guest_policy_failure_grace_seconds BETWEEN 60 AND 3600);
```

**The `guest_` qualifier is load-bearing and is not a naming preference.**
`SiteFailurePolicy.For` ignores `GuestFailureMode` entirely for `MethodDot1X`, which
is why the Go side has never been ambiguous. SQL has no `For()`: an ad-hoc query, a
future admin API and `\d sites` at 2 a.m. all see the column and none of them sees
the method. There is no column for the 802.1X answer because it is not configurable
— a venue may wave guests through while the control plane is blind; it may not
choose that for a corporate supplicant.

The constraint names carry the qualifier too: `\d sites` prints
`sites_guest_policy_failure_mode_check` and
`sites_guest_policy_failure_grace_seconds_check`.

`NOT NULL DEFAULT` rather than nullable: a site that has never been configured must
not have a NULL failure mode, because every reader then has to decide what NULL
means and one of them will decide it means "unset, so allow".

## Required of the generated configuration

See `lab/policy/README.md` for what is measured. The failure branch is explicit —
`elsif (fail)`, stated in place rather than reached through an `else`, with every
`rlm_rest` rcode given an action so that a denial's `userlock` does not return before
the branches run — and 802.1X is classified on EAP presence inside it.

**`Advice` is pre-resolved at generate time, never at request time.** That is forced
rather than chosen: by the time the failure branch runs, the thing that would resolve
a rule is the thing that is down. The generator therefore wants the resolved *set*
plus the client-to-value mapping, not the rule. Per-site routing (one virtual server
per distinct `Advice`, routed by the `nas` projection's `server` column) is not built:
the generator requires every site to agree on one `Advice` and refuses by name when
they do not.

The request body carries `user_name_hex`, `outer_user_name_hex`, `eap_message`
(presence only), `radius_client` and `client_ip`. Neither client field is
NAS-supplied: `radius_client` is `%{client:shortname}`, a value the *server* looked
up, and `client_ip` is the packet's source address, the key FreeRADIUS matched the
client by on a packet verified with that client's secret. `nas_identifier` is
NAS-supplied and attacker-chosen. **A voucher's site is resolved from
`client_ip`** (invariant 6).

## Proven by

`internal/aaa/*_test.go` — the inner/outer identity lock and the credential gate.
`lab/policy/` — SIGSTOP against a real FreeRADIUS.
