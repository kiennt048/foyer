# lab/ — a real FreeRADIUS against the Go control plane, on a laptop

Real FreeRADIUS 3.2.x, PostgreSQL 16 and the control plane, in Docker. It is how
this repository measures RADIUS behaviour rather than remembering it
(`docs/rules/testing.md`). It is **not a deployment**: its passwords are
published in `docker-compose.yml`, it says so to the control plane with
`FOYER_LAB_INSECURE`, and `deploy/` is where the host lives.

```bash
make lab-up         # the lab's TLS material and its key-encryption key, then
                     # PostgreSQL, migrate, backend, FreeRADIUS
make lab-nas-auth   # be a NAS: one Access-Request that must be accepted, and one
                     # that must be refused
make lab-nas-acct   # the same allow, then Accounting-Start and -Stop, and the
                     # radacct row FreeRADIUS wrote
make lab-test       # the RADIUS round trips, as Go tests rather than by hand
make lab-down       # stop and remove the containers; the database volume is kept
make lab-reset      # remove the database volume too, and bring the lab back on an empty one
```

## Being a NAS: `make lab-nas-auth` and `make lab-nas-acct`

The Go suites under `lab/` answer this in CI. These two targets are the same
thing driven by hand, because the question — *did the data plane decide, or did
it merely not complain?* — is one a test answers for you and a person does
not. `lab/nas.sh` sends a real Access-Request into the lab's FreeRADIUS 3.2.10,
which asks the Go control plane over the real mTLS boundary and answers.

```bash
make lab-nas-auth
```

runs **two** probes and shows both: a valid voucher code, which must come back
`Access-Accept` and must leave the row `in-use`, and a well-formed code that is
in no batch, which must come back `Access-Reject`. One command, both answers,
because a run that only ever sees accepts has proved nothing.

`make lab-nas-acct` does one whole session: an Access-Request, then
Accounting-Start and Accounting-Stop for it, then the `radacct` row FreeRADIUS
wrote — a table Foyer never writes to, so its presence is the evidence that
both halves of the system met.

**No secret is ever typed, printed or written.** The shared secret is read from
`nas` as `foyer_radius` — the login FreeRADIUS itself uses, which holds SELECT
on that table and nothing else — rather than off the console's one-time dialog,
where a 40-character unbroken string acquires dashes on its way through a human.
It travels on the `docker exec` standard input rather than on a command line, so
it is not in the host's process list, and the tool prints its length and nothing
else. What it *does* print, loudly, is a silence: an unanswered request never
reached the Policy API, and radiusd's own log is the only thing that separates
"no client row" from "the secret did not verify" — so the tool quotes that line,
names which of the two it found, and points at the query that confirms it.

Both targets exit non-zero when a probe does not answer as described. A lab
affordance that cannot fail is a screenshot.

The only thing the lab needs from you first is a **device registered in the
console at `127.0.0.1`** (**Access Points**), because that is what puts a row
in the client table, and a row is what radiusd matches a packet against — being
inside a network in `clients.conf` is not being a client. If you would rather
not register one, `lab/nas.sh` says so and stops, with the console action
named, rather than sending a packet it already knows the answer to.

It mints a voucher if the lab holds no unused one, and says so. A voucher is a
guest's bearer credential printed on a slip; it is not one of the things this
tool refuses to print.

## The key-encryption key, and why it is generated

`make lab-up` writes `lab/.state/kek` on the first run and reuses it after
that, then hands it to the control plane as `FOYER_KEK`. It is what seals RADIUS
shared secrets, controller credentials and NT hashes.

Without one, the control plane warns at start-up that "registering a controller,
storing an NT hash and rotating a shared secret will be refused, because there is
nothing to seal them under", and then works perfectly — the FreeRADIUS path, the
policy engine and accounting are all unaffected — so nothing is red. What *is*
refused is the first thing an operator does: registering a device, because
creating its shared secret is a seal. An operator whose first two clicks do
nothing, on a lab whose console looks healthy, is the failure this removes.

The key is generated on the machine and never committed, and there is no
default value in `lab/docker-compose.yml` to commit one *to*: a key in this file
is a key every deployment on earth would share, which is the same failure as a
CA shipped in an image (`docs/rules/security.md` §7). `deploy/first-boot.sh` does the same
thing for a real install, from `/opt/foyer/secrets/kek`. Export your own
`FOYER_KEK` and the lab uses that instead. `make lab-reset` deliberately keeps
the file: regenerating it would make anything already sealed under it
unreadable.

## When the lab will not come up after a pull: `make lab-reset`

The lab's database is disposable. Reset it whenever `make lab-up` fails in
`migrate`, or the backend fails on a relation or column that the tree says exists.

The usual cause is a database migrated by a different tree — for example a
branch whose migrations are numbered differently from the ones in your checkout. goose records what it has applied **by number**, so such a database is wrong in
one of two ways:

- **a gap below its latest version** — goose refuses it with *"found N missing
  migrations before current version"*, correctly, since it cannot know what the gap
  was meant to be;
- **a number already applied whose file is now a different migration** — goose
  skips it as done, and the schema is silently missing that file's tables; the
  failure appears later, in whatever first reads them.

Neither is worth repairing by hand on a lab. `make lab-reset` runs
`docker compose down -v --remove-orphans` and `make lab-up`; `migrate` then applies
the whole current sequence to an empty database. `lab/.state` — the generated
certificates — is kept, because nothing in it depends on the schema.

A deployment only ever runs released migrations, so none of this applies to it; an
upgrade there is `deploy/upgrade.sh`, which migrates forward from whatever release
it last ran.

## What else lives here

| Path | What |
|---|---|
| `docker-compose.yml` | the lab's services; its header says why the schema arrives through a `migrate` one-shot |
| `Dockerfile.backend`, `Dockerfile.freeradius` | the lab's images (not the deployment's; `deploy/` has those) |
| `raddb/` | **generated** by `make lab-raddb` from the database — never hand-edited |
| `eap/`, `policy/`, `blue-green/`, `measure/` | measurement harnesses; `eap/README.md` has the EAP one |
| `nas.sh` | the lab's own NAS: real Access-Request and Accounting packets, driven by hand — `make lab-nas-auth`, `make lab-nas-acct` |
| `.state/` | generated certificates and the key-encryption key, git-ignored |
