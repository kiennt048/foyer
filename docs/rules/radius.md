# RADIUS rules

> Governs `internal/raddb`, `internal/aaa` and `internal/session`.

## The generator owns raddb; nothing else does

`raddb/` is build output. If you are editing a file in it, you are editing the
wrong thing — change `internal/raddb/`.

### Version is a *dialect* contract, not an exact pin

Emit **FreeRADIUS 3.2.x unlang**: `authorize { }`, `post-auth { }`,
`accounting { }`. FreeRADIUS 4 uses `recv Access-Request { }` and also changes
attribute references, `update` sections, module return codes and the dictionary
model — so v4 support means a **second emitter**, never an edit to this one.

Assert **`>= 3.2.9, < 3.3`**, not equality. 3.2.9 was a security-fix release and
3.2.10/3.2.11 already exist; an equality check turns every upstream security fix
into a Foyer outage. Pin the exact version only on the container image tag, and
run the generated-config test suite against the binary that is actually
installed.

### NAS clients come from SQL, not from generated config

`read_clients = yes` with `client_table`. Adding an access point must be a plain
database write.

**Why this matters more than it looks:** the naive alternative — regenerate and
restart — **drops every in-flight EAP conversation and the in-memory TLS session
cache, so every corporate laptop on site re-authenticates whenever an admin adds
an AP.** SIGHUP does not reliably reload clients, listeners or virtual servers in
3.2.x, so "just HUP it" is not available. To make the restarts that *do* happen
survivable, the EAP TLS session cache can persist to disk with `persist_dir`: in
FreeRADIUS 3.2.x that cache is internal to `rlm_eap_tls` and has no Redis or other
external backend. It is off unless enabled, because a resumed session skips the
certificate check and the directory holds TLS master secrets.

Classify every change as `none` / `sql-only` / `restart` and show the operator
which one they are about to trigger. That classification is a tested contract.

### Applying config: blue/green, four gates

```
generate → raddb.next
  ├─ 1. radiusd -XC -d raddb.next
  ├─ 2. start a SECOND radiusd on alternate ports against raddb.next
  ├─ 3. radclient smoke suite  +  eapol_test EAP suite
  └─ 4. swap symlink, cut over
```

**`radiusd -XC` is necessary but not sufficient**, per FreeRADIUS's own man page:
the `-C` checks are necessarily incomplete, and the server can pass them and still
fail when run normally — and some modules are skipped entirely. Gate 1 alone is not
a safety net. Gates 2 and 3 carry the real load.

### The cutover has to restart something

A gate 4 that moves the symlink without restarting the server reports success while
the running server keeps serving its old tree. Three things prevent that, and each
covers a different way the same silence can occur:

- **`--restart-command`**, a developer's flag on `foyerctl apply-raddb` for a host
  install driven by hand. With none given, **no snapshot is written at all**, and the
  reason is printed: a snapshot written before a restart that never happens claims a
  client list the running server never read. It is never read from the environment;
  there is no `FOYER_RADIUSD_RESTART_COMMAND`.
- **`Ready`** polls the `status-server` probe instead of sleeping two seconds and
  calling it a guess.
- **`VerifyLive`** sends a probe at the **live** ports after the restart. Without it
  gate 4 reports success for a server that died on startup, and the rollback path is
  unreachable — which is worse than no rollback path, because it looks like one.

### A deployment takes the console's tree through the handoff, never a command

`deploy/` and `lab/` run no `raddb-generate` one-shot and store no restart command.
The control plane generates the tree from what the console stored into
`FOYER_RADDB_TARGET`, a directory it shares with the FreeRADIUS container, and the
supervisor in that image (`lab/blue-green/supervise.sh`) takes each generation: copy
into the idle colour, `radiusd -XC`, restart, wait for *Ready to process requests*,
roll back on any failure, answer in `status`. `internal/raddb/handoff.go` has the
protocol and the survey (PacketFence's own service manager; FreeRADIUS 3.2's HUP
reloading only HUP-safe modules, so `max_servers` and an EAP certificate need a
restart). The rules that follow:

- **Never regenerate the live tree from flags.** A tree built from CLI defaults
  overwrites what the console saved.
- **The supervisor, not the tree, decides who radiusd runs as.** It starts every
  radiusd through `setpriv` as `freerad` with no capabilities, and refuses a tree
  holding anything outside `TREE_FILES`/`TREE_DIRS`. A new generated file goes on
  that list in the same change; `TestTheSupervisorAllowsExactlyTheGeneratedTree` is
  red until it does. radiusd 3.2 has no `-u`/`-g` (measured).
- **Never add a command string to the mechanism** — not as a console field, not as a
  deployment variable. A command the control plane executes makes `nas:manage` a
  shell. The supervisor's answer, including its sanitised reason, is what the
  console shows.

### BlastRADIUS is the generator's job

Emit `require_message_authenticator = yes` and `limit_proxy_state = yes`. The
FreeRADIUS default is `auto`, which silently leaves legacy gear unprotected. Where
they are emitted — globally, in every network section and in the dynamic-clients
site, because a per-client setting has nowhere to live once clients come from SQL —
is [`security.md`](security.md) §3.

## Policy API (internal/aaa)

- Answers authorization only. **Never returns credential material** — see
  [`security.md`](security.md) §2. That rule is what extends "Go never sees the
  credential exchange" from EAP to every method.
- Authorization keys on the **inner** identity only.
- **The identity is the User-Name's own bytes — never trimmed.** `strings.TrimSpace`
  trims Unicode White_Space, so a voucher followed by NBSP or NEL would be accepted
  as the voucher. A C0/C1 control, DEL or U+FFFD is a denial. The one normalisation
  is a voucher's typed form (`voucherKey`: ASCII case, ASCII `-` and space — the
  portal's rule for a typed code); an account is compared exactly, as rlm_sql
  compares its PAP verifier.
- `rlm_rest` blocks a FreeRADIUS worker. Keep timeouts under the NAS retransmit
  interval and size the connection pool against `max_servers`. An `rlm_cache` in
  front, keyed on `Calling-Station-Id` + NAS, is planned and not generated yet.
- **`max_servers` is a PostgreSQL connection count.** The rlm_sql pool is sized at
  it, so the console's ceiling is what `max_connections` leaves after every other
  login's pool (`api.RadiusMaxServersCeiling`, 44 against 80). Raising either number
  means changing both, with `TestTheWorkerCeilingFitsThePostgresBudget` green.
- **Fail behaviour is explicit**: fail closed for 802.1X, configurable fail-open
  for guest, per site. The generator carries one failure model for the whole
  tree today and refuses sites that disagree; per-site routing through virtual
  servers is planned (`internal/raddb/config_failure.go`). There is a `lab/` test that SIGSTOPs the Go process and
  asserts the defined behaviour — it is the test that proves this architecture
  was worth choosing.
- Rendering the answer into vendor attributes is **`VendorProfile`'s** job, not
  the policy engine's. See [`nas-drivers.md`](nas-drivers.md).

## Accounting and quota (internal/session)

### Table ownership

FreeRADIUS owns `radacct` and `radpostauth`. **Go never writes them.** The stock
column set is kept verbatim — it is also the project's exit hatch, since the data
stays readable by stock FreeRADIUS if Foyer disappears. Indexes, partitioning and
retention *are* ours; the column set is not.

**Two audited exceptions, and nothing else.** The stale-session reaper
(single-purpose, logged, a four-column `UPDATE`), and retention/erasure in
`internal/store/retention.go` — `DELETE` only, as `foyer_retention`, so that a
retention period and a data subject's erasure can be honoured
([`docs/operator/personal-data.md`](../operator/personal-data.md)). Neither may
`INSERT`; neither lives anywhere else.

### Quota cannot be enforced precisely, and the UI must not pretend otherwise

- `radacct` byte counters advance only on Interim-Update or Stop.
- **They are 32-bit and they wrap at 4 GiB.** `Acct-Input-Gigawords` /
  `Acct-Output-Gigawords` carry the high-order bits (RFC 2866 §5.3–5.4, RFC 2869
  §5.1–5.2). A server that ignores them reports a session's total *dropping by
  4 294 967 296* mid-session. **Never subtract two counters without reconstructing
  the 64-bit value**: "used" goes negative, "remaining" hands the guest 4 GiB of
  free quota, and a delta-based rate reads as wildly negative throughput. The
  `sess-counter-wrap` fixture exists to catch the first such figure anyone adds.
- `Acct-Interim-Interval` has a widely-implemented **300-second floor**, so
  best-case sampling is 5 minutes — up to ~3.7 GB overshoot on a 100 Mbps link.
- Many NAS send **no** interim updates unless the server asks. Always emit
  `Acct-Interim-Interval` in Access-Accept.
- **On the controller-API path there is no Access-Accept at all**, so there is no
  channel to ask, and `radacct` rows exist only if the operator separately
  attached an accounting profile. Assert this at NAS registration and fail loudly.

Therefore:

1. Where `Capabilities.HardQuota` is true, **delegate enforcement to the NAS**
   (UniFi accepts `timeLimitMinutes`, `dataUsageLimitMBytes`, rate limits).
   `radacct` becomes reporting, not enforcement.
2. Prefer NAS-enforced `Session-Timeout` / `Idle-Timeout` over polling.
3. Byte quota is a backstop, and the UI says "sampled every N minutes".
4. Missing accounting means **session ended**, never "unlimited".
5. Alarm when no interim update arrives within 2× the requested interval.

A session with no `Acct-Stop` (NAS rebooted) stays open forever if nothing reaps
it. That is the classic RADIUS accounting failure — do not rediscover it.
