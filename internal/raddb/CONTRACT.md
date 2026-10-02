# internal/raddb — CONTRACT

## Invariants

1. **`raddb/` is generated output.** This package owns it. Nothing hand-edits it.
   If you are editing a file under `raddb/` or `lab/raddb/`, change the
   generator instead.

2. **BlastRADIUS protection is unconditional (CVE-2024-3596).** Every client
   this server will ever talk to requires a Message-Authenticator and limits
   Proxy-State. FreeRADIUS's own default is `auto`, which decides from the first
   packet a client sends and therefore silently leaves the weakest gear
   unprotected.

   **Because clients come from SQL, it has to hold in three places at once.** `rlm_sql` reads exactly six columns for a client —
   `id, nasname, shortname, type, secret, server` — and there is no seventh, so
   a client loaded from the client table **cannot carry the flag itself**. So:

   - `security { require_message_authenticator = yes; limit_proxy_state = yes }`
     in `radiusd.conf`, which is what a client loaded by `read_clients` inherits;
   - the same two on every network section in `clients.conf`, which is what a
     dynamically discovered client inherits;
   - `&FreeRADIUS-Client-Require-MA = yes` per discovered client in
     `sites-available/dynamic-clients`.

   `TestRequireMessageAuthenticatorSnapshot` asserts all three. Asserting only
   the global one would pass for a build where a client section overrides it
   back to `auto`, which is the shape of the defect this guards.

3. **The generated `post-auth` query never writes `pass`.** Stock FreeRADIUS
   3.2.x inserts `'%{%{User-Password}:-%{Chap-Password}}'` into
   `radpostauth.pass` on **every** attempt, accepted or rejected — a cleartext
   credential store, populated by default, in a table the admin console reads.
   We generate the query, so this one is ours. It emits `username`, `reply`,
   `authdate`, `calledstationid`, `callingstationid` and `class`, and the
   station-id columns are populated rather than left empty: the MAC is the only
   identifier an operator has when a guest is standing in front of them.
   `TestPostAuthQueryNeverWritesPass`.

4. **There is no shared secret anywhere in the generated tree.** Clients come
   from SQL, so `clients.conf` declares networks and no secrets at all.
   `Generate` refuses any configuration or output containing a known default,
   and `TestGeneratedTreeHoldsNoSharedSecret` walks every generated file.

5. **Clients come from SQL, and registering an access point is a database
   write.** `read_clients = yes` loads the client table at startup;
   `sites-available/dynamic-clients` discovers a row added afterwards. The
   alternative — regenerate and restart — drops every in-flight EAP conversation
   and the in-memory TLS session cache with it. SIGHUP is not an escape: it does
   not reliably reload clients, listeners or virtual servers in 3.2.x.

6. **Dialect, not pin: `>= 3.2.9, < 3.3`.** `CheckDialect` is a range on
   purpose. 3.2.9 carried security fixes and is the floor; 3.3 and above are not
   known to be the same configuration language, and FreeRADIUS 4 is definitely
   not — it needs a second emitter, never an edit to this one. An equality check
   would turn every upstream security fix into a Foyer outage. Pin an exact
   version on the container image tag, where rolling back is a deployment
   decision.

7. **Change classification is a tested contract.** `Classify` answers `none` /
   `sql-only` / `restart` with the reasons that produced it, and `sql-only`
   carries how long it takes to be honoured. The operator is shown which of the
   three they are about to trigger, because the three cost the same to type and
   do not cost the same to the site.

   **A deletion is `restart` whichever way the client was admitted, and it needs
   a third input to be seen at all.** The two snapshots are what the server
   loaded at startup and what is wanted now; a row written *after* startup and
   deleted before the next restart is in neither, so without a third input it
   would produce no reason and the answer would be `none` — an operator told a
   deletion took effect while the running server went on authenticating that
   client. `TableBefore` supplies
   the table's previous state and only its now-absent rows are consulted, so a
   caller may pass the whole table or only what it removed.

   The class is `restart` and not `sql-only` because `sql-only` promises the
   change is honoured without one, within `EffectiveWithin`. For a discovered
   client the only figure available is `DynamicClientLifetimeSeconds` — what
   Foyer *writes into the tree*, not something anyone watched elapse;
   `docs/protocol-reality.md` marks the expiry UNVERIFIED. Quoting it would turn
   a configured intention into a promise about a running server, on a
   revocation. *"After a restart it stops"* is the measured bound, so it is the
   one the classification stands behind. A row the running server could neither
   load nor discover held nothing and still classifies `none`.

8. **Applying goes through four gates and gate 1 is not one of them on its
   own.** `radiusd -XC` is necessary and not sufficient — FreeRADIUS's own man
   page says the server can return zero from `-C` and still exit with an error
   when run normally, and some modules are skipped entirely. Gates 2 and 3
   carry the real load. `Applier.Apply` stops at the first failure and leaves
   the live tree untouched.

9. **The `rlm_rest` boundary is mutual, or the tree is not written.** `security.md`
   §2 requires mTLS between FreeRADIUS and the Policy API. `Validate` refuses an
   `https` `PolicyAPIURL` with no client certificate, and refuses client material
   with a plaintext one — so "encrypted but unauthenticated" and "carries a
   certificate it will never present" are both unrepresentable rather than
   documented.

   **The ordering hazard is worse than it reads.** `rlm_rest` opens `ca_info_file`
   at *instantiation*, not on the first request: with the file absent FreeRADIUS
   says `Failed parsing configuration item` and **does not start**. A tree that
   requires a certificate, generated before one exists, is a dead data plane
   reported as a syntax error.

   Three things about that `tls { }` block were measured, and each is easy to get
   wrong:

   - it is **per-section**, inside `authorize` and `authenticate`. A module-level
     one is discarded with no warning, and `verify_peer` is not an item `rlm_rest`
     has at all;
   - `timeout` is per-section too. A module-level one is discarded the same way,
     leaving the 4 s default against a NAS that retransmits at ~3 s;
   - it is **`ca_info_file`** (CURLOPT_CAINFO, the trust store), not `ca_file`
     (CURLOPT_ISSUERCERT). Both parse, `radiusd -XC` prints either back
     identically, and `ca_file` alone fails every request with CURLE 60.

10. **One worker count, and the two connection pools are sized from it.**
    `MaxServers` reaches `radiusd.conf`'s `thread pool`, `mods-available/sql`'s
    pool and `mods-available/rest`'s pool from one field. A connection pool
    smaller than the thread pool makes the pool the queue, and a worker waiting on
    a pool is a worker not answering Status-Server — which is the §2 failure with
    a different name. 30, because `deploy/compose.yaml` already sized PostgreSQL
    at `max_connections = 80` on that basis.

    **Gate 1 cannot see this section.** `radiusd -XC` does not parse `thread pool`
    at all — the pool is initialised after module instantiation — and `-X` implies
    `-s`, single-threaded. So `Validate` checks the relationships itself rather
    than deferring to a gate that does not look. Measured, and it is a concrete
    instance of invariant 8.

11. **An allow is not an authentication; the tree checks what the Policy API
    says still has to be proven**. The
    allow branch accepts on its own only when `Foyer-Identity-Kind` (defined at
    3000 in the generated `dictionary`, never encoded into a packet) is
    `voucher`. For `account` it reads the bcrypt verifier ITSELF —
    `%{sql:SELECT verifier FROM radius_pap_verifiers WHERE username = ...}`
    with the User-Name spliced as `%{hex:&User-Name}` and decoded by
    PostgreSQL, because rlm_sql's escape rewrites any byte outside
    safe_characters as `=XX` and the lookup then asks about a different name
    (measured: `o'brien` refused with its right password on both images,
    `nguyễn` on the NetworkRADIUS one),
    the view `foyer_radius` reads without a tenant GUC — into `Crypt-Password`,
    checks its shape, and sets `Auth-Type := PAP`; no verifier or no
    `User-Password` is a reject. Any other kind, or none, is a reject. This is
    the tree's one credential lookup and `TestNothingEmitsPasswordWithHeader`
    pins its shape. Without it, an allow would be `Auth-Type := Accept` and a
    guest account's username would be enough.

12. **Every value in the Policy API question is `%{jsonquote:}`d, and `invalid`
    is a reject**. Measured on 3.2.10: a bare
    `%{User-Name}` escapes `"` and `\` but prints a control byte as `\001` and a
    non-UTF-8 byte as `\377` — not JSON — and the Policy API's 400 for that maps
    to `invalid`. Sending `invalid` to the failure branch would be an accept at a
    fail-open site, so the failure branch is `fail` alone and `invalid` rejects.
    **Except the two identities, which are `%{hex:&Attr}`** (`user_name_hex`,
    `outer_user_name_hex`): on the amd64 NetworkRADIUS build jsonquote turns
    every byte >= 0x80 into its own `\u00XX`, so `nguyễn` reached the Policy API
    as a different name (measured on the image built for linux/amd64; an arm64
    build passes the bytes through, so a test on one architecture cannot see it).
    Hex is the same on every build.

13. **radiusd is not root, and a reject carries a reason and nothing else.**
    `security { user = freerad, group = freerad }` (`ServerUser`,
    `ServerGroup`); measured, `/proc/1/status` reads uid 995 in all four slots
    and `CapEff: 0`. The ports are above 1024, so no capability is granted.
    Modules instantiate after the switch, so key files must be readable by
    `freerad`: both images add it to `foyer-radius-tls` at gid 1812
    (`TLSReadGID`). `Post-Auth-Type REJECT` empties the reply after `sql` has
    logged it and restores one `Reply-Message` — measured, a wrong password's
    reject otherwise carries the allow's `Session-Timeout`. `auth_badpass` and
    `auth_goodpass` are `no`. `preacct` computes `Acct-Unique-Session-Id` (the
    stock policy's hash, inlined); left empty, `radacct`'s UNIQUE would admit only
    one session.

## What this package does not do, stated rather than implied

- **The TLS session cache is off by default, and it has no Redis backend.**
  `docs/rules/radius.md` wants it to survive the restarts that do happen.
  Measured on 3.2.10: **3.2.x has no Redis backend for it.** It is internal to
  `rlm_eap_tls` rather than an `rlm_cache` instance, and an unrecognised item
  written into the section is discarded while `radiusd -XC` still exits 0.
  `persist_dir` is the mechanism that exists, it works, and
  `EAPConfig.TLSSessionCacheDir` turns it on.

  It stays off unless asked for: a resumed session skips the method, so a cached entry
  re-admits a supplicant without the certificate being presented again; and the
  directory holds the TLS master secret and belongs on the same footing as a
  private key.

- **EAP is off by default and only EAP-TLS is emitted.** `Validate` rejects
  `ttls` and `peap` **by name**, with the fact that blocks each:

  | Method | What blocks it |
  |---|---|
  | EAP-TTLS/PAP | `rlm_pap` in 3.2.10 implements no argon2 scheme, and `Crypt-Password` does not reach one either — libxcrypt hashes bcrypt, yescrypt and sha512crypt and returns its failure token for `$argon2id$`. Measured. The opted-in bcrypt verifier (`radius_pap_verifiers`) is the way through; the generator does not yet emit a TTLS method that uses it. |
  | PEAP-MSCHAPv2 | MSCHAPv2 needs the NT hash in the clear at request time, and Foyer's is envelope-encrypted under a key that is deliberately not in the database. Supplying it costs a **second** declared cleartext projection, which `docs/rules/database.md` calls a defect in advance. |

  Both are credential-storage decisions under `docs/rules/security.md` §1, so
  neither is a generator setting. Emitting either would claim a method no
  `eapol_test` case can pass.

- **`Password-With-Header` is never emitted, and exactly one thing here fetches
  a verifier** — the `Crypt-Password` lookup of invariant 11, from
  `radius_pap_verifiers` only. `rlm_pap` rewrites an unrecognised header to `Cleartext-Password`,
  so an argon2 hash placed there **becomes the password** — proven with an
  `eapol_test` run that authenticated by sending the stored verifier. There is a
  test asserting the generated tree contains neither the attribute nor any
  credential query other than that one.

- **No certificate is generated here and none is shipped in an image.** A fixed
  key means every deployment on earth shares one private key. `EAPConfig` takes
  paths; `lab/blue-green/make-eap-certs.sh` makes throwaway ones at test time.

- **The blue/green procedure exists twice.** `Applier` is for a host install
  where the control plane and radiusd share a filesystem;
  `lab/blue-green/gates.sh` is for the lab, where they are separate containers
  and only one of them has the binaries. `TestShellGatesMatchTheGoGates` pins the
  gates, their numbers and their order across the two.

## What this package needs from the schema

FreeRADIUS reads the `nas` **projection** (`Config.ClientTable`, `nas` by
default), which migration `00002` creates and `docs/rules/database.md`
declares as holding the one cleartext copy of a shared secret. `radius_clients`
is the system of record; `nas` is written from it.

`rlm_sql` reads six columns, **positionally**:

```
id         unused by rlm_sql, but the column must be selected
nasname    the client's address; the client identifier
shortname
type       'other' unless the vendor needs otherwise
secret     cleartext, because FreeRADIUS validates Message-Authenticator itself
server     virtual server; NULL or empty means the default
```

Reordering them hands FreeRADIUS the shortname as a shared secret and it starts
anyway. Four consequences worth being explicit about:

- **`nas.require_message_authenticator` and `nas.limit_proxy_state` are inert.**
  There is no seventh column and `client_query` cannot carry them, so a `false`
  in either column changes nothing about how the server treats that client. The
  guarantee is held by the three places in invariant 2 instead. The columns are
  not wrong to have — they are what `radius_clients` declares, and the console
  reads them — but nothing downstream of this package enforces them.

- **A range is not discoverable.** `radius_clients.address` is an `inet` and may
  be a range covering a fleet (Aruba's dynamic-radius-proxy shape). Dynamic
  discovery matches `nasname` against one `Packet-Src-IP-Address`, so a range
  only ever enters through the startup load. `Classify` says `restart` for those
  and gives that as the reason rather than "outside every dynamic network",
  which would be true and misleading.

- **Every access point is its own RADIUS client and they share one secret**, so
  the common case is N rows carrying the same `secret` value.

- **PostgreSQL is a start-time dependency.** `read_clients = yes` reads the
  table before the first packet, so a database FreeRADIUS cannot reach is a
  server that does not start.

## `ClassifySync`

The restart question a client-table sync leaves open, answered in one place for
both callers: `foyerctl sync-nas` and the control plane's automatic sync after every
RADIUS-client write. Redacted on both sides, with the removed names supplied.
No snapshot is `ErrNoStartupSnapshot` — an unknown class, never `none`.

## `Handoff` (console Apply reaches FreeRADIUS)

The control plane's side of the directory it shares with the FreeRADIUS container
(`handoff.go`); the other side is `lab/blue-green/supervise.sh`, shipped in both
FreeRADIUS images. Invariants:

- **A generation is written whole, then requested.** `Stage` writes under a
  temporary name and renames; `Request` is written last and atomically. The
  supervisor never reads a half-written tree.
- **The answer is the supervisor's, never assumed.** `Await` returns only a status
  for the generation asked about; a later generation answered first is
  `ErrSuperseded`, and no answer is `ErrNoSupervisor` — which the caller reports as
  a failure whose outcome is unknown, never as applied.
- **`Prune` never touches a newer generation**: one may be in flight.
- **No command crosses the boundary.** The protocol is file names and key=value
  lines; nothing in it is executed. `detail` is one sanitised line, with any line
  naming a password or secret dropped by the supervisor.
- **`mode=startup` restarts only for a different tree; `mode=apply` always does**,
  because an operator may be asking FreeRADIUS to re-read a client table whose tree
  is unchanged. `TestTheSupervisorSpeaksThisProtocol` drives the real script against
  a fake radiusd; the lab drives it against the real one.
- **radiusd never starts as root, whatever the tree says.** The
  supervisor starts the `-XC` check and the server through `setpriv` as `freerad`,
  its groups, no capabilities, no_new_privs; a root supervisor that cannot do that
  refuses to start. `TestTheSupervisorNeverStartsRadiusdAsRoot`,
  `TestASupervisorThatCannotDropPrivilegesDoesNotStart`, and in the image
  `TestTheSupervisorInTheFreeRADIUSImage` (tier 2).
- **A generation holds only what the generator writes.** The supervisor refuses any
  other file, directory, name or link (`TREE_FILES`, `TREE_DIRS`) before copying
  anything; the running server is untouched. **A new file this package generates
  must be added to that list in the same change** —
  `TestTheSupervisorAllowsExactlyTheGeneratedTree` is red until it is, in both
  directions. `TestTheSupervisorRefusesAFileTheGeneratorNeverWrites`.

## The rest module sends `client_ip`

Every authorize body carries `%{Packet-Src-IP-Address}` (IPv6 fallback), from which
the Policy API resolves a voucher's venue (`internal/aaa`, site binding). A tree
without it makes every voucher a refusal. `TestTheRestModuleSendsThePacketSource`.

## The database password may be named rather than written

- **`Config.PostgresPasswordEnv`** makes `mods-available/sql` read
  `password = $ENV{NAME}` — unquoted, because radiusd escape-processes a quoted
  expansion — so the tree carries no password and the control plane that generates
  it holds none. Exclusive with `PostgresPassword`; the name must be
  `[A-Z_][A-Z0-9_]*`. A move between the two forms is a restart (`configDiffs`).
  `TestATreeCanNameItsDatabasePasswordWithoutHoldingIt`,
  `TestPasswordEnvIsRefusedWhenAmbiguousOrNotAName`.
- **The supervisor supplies it**: `supervise.sh` exports `FOYER_RADIUS_DB_PASSWORD`
  from `FOYER_RADIUS_DB_PASSWORD_FILE` (default `/run/secrets/radius_db_password`)
  unless the environment already has it, before `radiusd -XC` and `radiusd`.
  `TestTheSupervisorHandsRadiusdItsDatabasePassword`. MEASURED on 3.2.10.
