// Package obs is what a running Foyer can be asked about itself: is it alive, is
// it ready, what has it done, and which request is this line about.
//
// ── the survey, and why this is four files and not a framework ────────────────
//
// The job fits in one line — *structured logging everywhere with request-ID
// correlation, a /metrics endpoint, and a /readyz that actually checks Postgres
// and the driver path* — and every piece of it has an established answer in the
// field.
//
// **Liveness and readiness are different questions and are answered by different
// handlers.** The Kubernetes documentation is unambiguous and the failure it
// describes is a classic outage: a liveness probe that consults a dependency
// restart-loops the container every time that dependency bounces, and a readiness
// probe that does not consult it keeps routing traffic into a process that can
// only return errors. authentik ships the same split — `/-/health/live/` answers
// 200 while the process runs and `/-/health/ready/` answers 200 when PostgreSQL
// accepts a connection — as two paths and two handlers. So [Liveness] consults
// nothing and [Readiness] consults exactly what it names. They do not share a
// function, and `TestLivenessAndReadinessShareNoCode` is what holds that.
//
// **The healthcheck in `deploy/compose.yaml` is a readiness signal, not a liveness
// one, and that is a property of Compose rather than of this package.** Docker
// does not restart an unhealthy container; `restart: unless-stopped` acts on the
// process exiting. A readiness check there is reported to `docker ps` and to
// `depends_on: service_healthy`, and a control plane whose database is gone
// becomes `(unhealthy)` — which is the truth, and the thing the deployment's
// healthcheck could not say before. Were this Compose service a Kubernetes Pod,
// the liveness probe would point at `/healthz` and this would be a restart loop
// waiting to happen. It is the same reason the maintenance container's pools are
// allowed to fail open (`cmd/foyer/maintenance.go`): a retention misconfiguration
// must not become a venue with no network.
//
// **The driver path is checked by *not* calling a controller.** "Readiness checks
// the driver path", read quickly, sounds like a round trip to the UniFi or Cisco
// controller on every probe. The portal front door's own comment says why that is
// wrong: the
// Captive Network Assistant probes the portal repeatedly while a guest reads a
// page, and a controller round trip per probe is a denial of service the venue pays
// for. A readiness probe runs on a timer forever, which is the same shape with
// worse manners. So [Readiness] checks the thing that is cheap and local and whose
// absence really does mean a guest cannot be served: **the stored network settings
// have been read into this process.** A controller being down is a degraded
// deployment, not an unready process, and the console, the reaper and the retention
// sweep all keep working through one.
//
// **The label set is a closed vocabulary, and the closure is enforced rather than
// documented.** Prometheus's own guidance is that cardinality is the cost of a
// label and that a value nobody can enumerate is a time series nobody can afford.
// The privacy half is the same rule: a guest's MAC is personal data under
// `docs/operator/personal-data.md` and the retention rules that implement it, and
// a scrape target's storage is the last place an erasure reaches. So [Metrics]
// accepts only label values drawn from sets this package declares, and
// `routeLabel` collapses anything it does not recognise to `other` rather than
// passing a path through. `TestTheExpositionCarriesNoGuestIdentifier` reads a real
// exposition back after a request carrying a MAC, a voucher code and a username
// and asserts none of them are in it — a test that asserts a metric is *absent*,
// which is the only kind that holds a decision like this in place.
//
// **`/metrics` is on the policy listener, and the reason is who can reach it.**
// There are three listeners and three trust levels. The guest portal is bound wide
// because every unauthenticated device on the SSID has to reach it; a metrics
// endpoint there publishes a venue's capacity, sign-in rate and error rate to
// anyone in radio range, which is a finding rather than a feature. The admin
// console is published on the LAN and is an operator's browser origin. The policy
// API is the one `deploy/compose.yaml` does not publish — "NOT published 8080 the
// Policy API answers FreeRADIUS and nothing else" — sits on the internal
// backplane, and requires a client certificate. authentik reaches the same
// isolation the same way, with a dedicated port no load balancer fronts. Foyer
// does not add a fourth listener for it: a new port is a new published surface in
// `deploy/compose.yaml` and a new setting for an operator to get wrong, and the
// policy listener's documented purpose is already "FreeRADIUS, and nothing else",
// which is the trust level a scrape needs.
//
// **The request ID is generated, not adopted, on the two listeners anyone can
// reach.** nginx's `$request_id` and Envoy's `x-request-id` both propagate an
// upstream's value, which is right when the upstream is inside your trust
// boundary. The portal's is not: any device on the SSID can send
// `X-Request-Id: <someone else's id>`, and a correlation ID an unauthenticated
// guest chooses is a correlation ID that can be made to collide, to be absent, or
// to point an operator at the wrong device. So an inbound header is honoured only
// where the peer proved itself with a client certificate — the policy listener —
// and is replaced with 128 bits of `crypto/rand` everywhere else. The ID then
// lives in the `context.Context` and a [ContextHandler] lifts it onto every
// record, so the log line a repository method writes deep inside a request
// carries that request's ID without any call site passing a logger down.
//
// **What this package does not claim.** A portal request is not traceable to the
// RADIUS accounting that follows it by request ID, and no amount of code in this
// repository makes it so: FreeRADIUS is a separate process whose configuration
// this project generates, `rlm_rest` is not instrumented from here, and
// `radacct` is FreeRADIUS's table. The join across that boundary is the masked
// MAC and the session id, both of which `internal/redact` already produces. The
// guarantee this package makes is the one it can keep: one ID per inbound
// request, on every listener, on every line, in the response, and on the
// outbound call this process makes to another Foyer listener.
package obs
