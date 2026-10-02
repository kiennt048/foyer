# internal/obs — CONTRACT

What a running Foyer can be asked about itself: is it alive, is it ready, what has
it done, and which request is this line about. The package comment has the survey —
Kubernetes, authentik, Prometheus, nginx, Envoy — and why each decision is what it
is. This file is the invariants, which the tests hold.

Imports the standard library and `internal/redact` only. It is a leaf by
construction: nothing here may import `internal/api`, `internal/store` or
`cmd/foyer`, because every one of those needs to ask it a question.

## Invariants

1. **`/healthz` and `/readyz` are different handlers and share no code.** Liveness
   consults nothing and its failure means kill the process; readiness consults
   PostgreSQL and the driver's configuration and its failure means stop sending
   traffic here. Kubernetes' documentation is explicit that a liveness probe
   which fails on a dependency restart-loops the container every time that
   dependency restarts, and this process answers every Wi-Fi sign-in on site.
   `TestLivenessAndReadinessShareNoCode` reads this package's source rather than
   its behaviour, because the failure is a *refactor*: a change that wires the
   checks into `Liveness` preserves every behaviour and is the outage.

2. **Liveness answers 200 and the body `ok`, always.** Three mount sites and two
   deployment probes depend on that body, and `TestLivenessConsultsNothing` drives
   it with a cancelled context to prove nothing is consulted.

3. **Readiness checks reachability, never latency.** A check that takes 1.9s and
   succeeds is `ready: true`. A probe that fails on slowness takes a working node
   out of rotation during exactly the spike where it is needed.
   `TestASlowDatabaseIsNotADownDatabase` holds it, and
   `TestASlowCheckPastTheDeadlineIsNotReady` holds the other side of the boundary.
   The duration is **published** in the body so a human can judge what the probe
   deliberately does not.

4. **`ReadyTimeout` is 2.5s, and it bounds the answer rather than judging it.** It
   is below `deploy/compose.yaml`'s healthcheck `timeout: 3s` on purpose: a probe
   that outlives its caller's timeout is a probe whose answer arrives after the
   caller stopped listening.

5. **The checks run concurrently.** Sequentially, one slow check consumes the
   budget the others need out of a single shared deadline and reports a failure
   that is a scheduling artifact.
   `TestTheChecksRunConcurrently` asserts two 400ms checks answer in under 800ms.

6. **A check that is not configured is ready, not failed.** A caller with no
   database has no database to be unready about, and a probe that invented a
   failure there would be one nobody could act on.

7. **The driver's configuration is checked locally and no controller is called.**
   See the package comment: a controller round trip per probe is a denial of
   service the venue pays for, the same cost a controller call on the guest
   portal's front door would have. `cmd/foyer`'s `networkCheck` supplies the predicate.

8. **A request ID is 128 bits of `crypto/rand`, 32 lowercase hex characters.**
   Never `math/rand`: a value an attacker may read must not be predictable.
   `TestTheRequestIDIsNotPredictable` is the guarantee.

9. **An inbound `X-Request-Id` is adopted only from a trusted peer, and only if it
   is exactly 32 lowercase hex characters.** The portal and the console are
   reachable by an unauthenticated device on the SSID and by anything on the
   venue's LAN respectively; a correlation ID a guest chooses is one that can be
   made to collide with, repeat, or be absent.
   `TestAGuestCannotChooseTheCorrelationID` and
   `TestAMalformedInboundIDIsRefusedEvenFromATrustedPeer` hold both halves.

10. **The ID lives in the `context.Context` and is lifted onto every record by
    `ContextHandler`.** Not a logger argument: `internal/api`, `internal/store`,
    `internal/session` and `internal/jobs` all log from several layers below the
    handler and none of them passes a logger down. A record with no ID in its
    context carries **no** `request_id` field rather than an empty one — a
    maintenance sweep on a timer has no request, and `"request_id":""` looks like
    it correlates with something and does not.

11. **Every log line is a structured line, including the ones `net/http` insists
    on writing through a `*log.Logger`.** `Install` sets both `slog.SetDefault`
    and `log.SetOutput`, clears the stdlib flags and prefix, and **masks through
    `redact.Line`**. A date in front of a JSON object is a line no parser accepts.
    `TestTheStdlibLoggerBecomesAStructuredLine` and
    `TestTheStdlibLineIsMasked` drive `log.Default()` — the object `net/http`
    actually writes a failed handshake to — rather than a helper.

12. **The metric label set is exactly four names, and no label value can come from
    a request.** `listener` is a constant per listener, `method` is one of eight
    HTTP methods or `other`, `status` is a three-digit code, and `route` is the
    **matched `http.Request.Pattern`** or `unmatched`. A scrape target's series are
    not deletable by the retention rules that implement Nghị định 356/2025, so a
    MAC address, voucher code, username, IP or path in a label is permanent.

13. **`RouteLabel` cannot recognise a pattern, and does not pretend to.** A UUID in
    a path segment and a route segment are the same length and the same alphabet;
    a validator that guesses wrong fails **open**. So the guarantee is structural:
    every call site passes `r.Pattern`, and
    `TestNoCallerEverPassesARequestPathToALabel` walks every non-test `.go` file in
    the repository with `go/ast` and fails on anything else. That fails closed.

14. **The exposition is Prometheus text format 0.0.4, written out rather than
    taken from a library** — the project keeps its dependency set small, and a new
    dependency is discussed in an issue first. `TestTheExpositionIsWellFormedTextFormat` checks the output
    against the specification's requirements, not against this implementation, and
    `TestTheExpositionContentTypeIsTheOnePrometheus3Requires` holds the
    `Content-Type` — from Prometheus 3.0 a scrape **fails** without it, and an
    endpoint with the wrong one looks perfect to `curl` and collects nothing.

15. **The label set is closed: there is no histogram.** Bucket boundaries would
    have to come from a measured request-duration distribution, and none has been
    measured. Invented buckets produce a chart that looks like evidence. A histogram
    added here brings its reasoning into this file with it.

16. **Every listener records exactly one access line and one metric per request.**
    A handler that writes a header twice, or a client that disconnects mid-body,
    must not produce two: a log that disagrees with a metric cannot be reconciled
    after the fact.

17. **The access line is at Debug on the portal and the console, and at INFO on the
    Policy API.** The Captive Network Assistant probes the portal repeatedly while
    a guest reads a page, and `deploy/compose.yaml` bounds the container log at
    50 MB; at INFO the portal reaches that bound with noise. An authorization
    decision is the product, and it is a handful of lines a minute.

18. **`/metrics` is mounted on the policy listener and nowhere else.** The other
    two are reachable by an unauthenticated device on the SSID and by the venue's
    LAN respectively.
    `cmd/foyer`'s `TestTheMetricsEndpointIsOnThePolicyListenerAlone` holds it, and
    `deploy/verify.sh` holds it on the real box.

## What it does not do

- **Trace a portal request to the RADIUS accounting that follows it.** FreeRADIUS
  is a separate process whose configuration this project generates; `rlm_rest` is
  not instrumented from here and `radacct` is FreeRADIUS's table. The join across
  that boundary is the masked MAC and the session id, both of which
  `internal/redact` and `store.Subject.Masked` already produce. Claiming otherwise
  would be a false correlation in a log.
- **Restart anything, or take a listener out of rotation.** In Compose Docker does
  not restart an unhealthy container, so `/readyz` on the healthcheck is a report.
  Were this a Kubernetes Pod, the liveness probe would point at `/healthz`; that
  is invariant 1 and it is why the two are separate functions.
- **Read a configuration file or a console setting.** The one environment variable
  is `FOYER_LOG_LEVEL`, and its reason is in `LevelFromEnv`: the console is one of
  the things that can be broken, so a control plane too noisy or too quiet to
  diagnose is not diagnosable from inside itself.
