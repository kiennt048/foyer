package obs

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ReadyTimeout bounds the whole readiness answer.
//
// **Two and a half seconds, and the number is not arbitrary.** It is below
// `deploy/compose.yaml`'s healthcheck `timeout: 3s` on purpose: a probe that can
// outlive the timeout its caller enforces is a probe whose answer arrives after
// the caller has stopped listening, and the observable effect is a container that
// flaps `unhealthy` while the thing being checked is fine. It is also above the
// 90-second `connect_timeout` PostgreSQL is given in any real deployment, so a
// probe never reports "not ready" for a connection that was merely slow to
// establish.
const ReadyTimeout = 2500 * time.Millisecond

// Check is one named dependency question.
//
// It is a function rather than an interface because every check in this
// repository is one closure over something that already exists — a pgxpool, a
// server's loaded-network predicate — and an interface here would be a second
// spelling of the same thing with no third implementation in sight.
type Check func(ctx context.Context) error

// Readiness is the set of named checks, and the handler that answers for them.
//
// **What it checks, and why each one is here:** PostgreSQL accepts a connection,
// and the process has read the stored network settings into memory. Those are the
// two facts whose absence means a request arriving now cannot be served.
//
// **What it deliberately does not check**, each with the reason it would be wrong:
//
//   - *Latency.* A database answering in 1.9 seconds is serving. A readiness
//     probe that fails on slowness takes a working node out of rotation during
//     exactly the spike where you need it most, and the Kubernetes documentation
//     is explicit that readiness is about whether traffic should be *sent*, not
//     about how fast. This probe's own deadline is the only time-based rule in
//     it, and it bounds how long the answer takes — not how fast the answer is.
//   - *A controller round trip.* See the package comment: a controller call per
//     probe is a denial of service the venue pays for, and a readiness probe runs
//     on a timer forever.
//   - *The reaper's and the retention role's pools.* `cmd/foyer/maintenance.go`
//     makes them fail every cycle rather than refuse to start, deliberately,
//     because a retention misconfiguration must not become a venue with no
//     network. A readiness check that failed on them would silently reverse that
//     decision, and it would do it from a file nobody reads.
//   - *FreeRADIUS.* The honest probe is a Status-Server packet, and
//     `deploy/compose.yaml` says in as many words why there is no healthcheck on
//     that service: the status listener binds loopback inside its container and
//     reaching it needs a client row and its shared secret. A readiness check
//     that cannot be built is not one to fake.
//
// **Why it is a separate handler from [Liveness]** is in that function's comment,
// and it is the decision the Kubernetes docs are most emphatic about. The two
// answer opposite questions, take opposite actions when they fail, and must never
// be one function with a parameter.
type Readiness struct {
	// Postgres is the database check. Nil means "not configured", which is
	// reported as ready rather than as a failure: a caller that supplies no
	// database has no database to be unready about, and a probe that invented a
	// failure there would be one nobody could act on.
	Postgres Check
	// Network is the driver path, and it is a *local* question. See the package
	// comment for why it is not a controller round trip.
	Network Check
	// Now is the clock, for the test that makes a slow database look slow.
	Now func() time.Time
}

// Result is the body of a readiness answer.
//
// Field names are the wire contract and stay stable; a monitoring system reads
// them. The values it carries are booleans and a duration, and that is the point:
// a readiness endpoint is reachable by whatever is watching, so it says whether
// the process can serve and nothing about what it is serving.
type Result struct {
	// Ready is the answer. The HTTP status says the same thing — 200 or 503 —
	// because a probe that has to parse a body to learn whether it failed is a
	// probe somebody will configure wrong.
	Ready bool `json:"ready"`
	// Checks is every named check and whether it passed, so an operator reading
	// `curl /readyz` learns *which* dependency is gone rather than being told
	// "not ready" and left to guess.
	Checks map[string]CheckResult `json:"checks"`
	// Duration is how long the answer took to produce. It is reported and NOT
	// judged: see the latency paragraph above.
	DurationMS int64 `json:"durationMs"`
}

// CheckResult is one named check's answer.
type CheckResult struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// Handler is the readiness endpoint.
//
// 200 with `ready:true` when every configured check passed, 503 with
// `ready:false` when any did not. It never returns 500: a probe that cannot
// answer is not the same as a probe answering "not ready", and Docker and
// Kubernetes treat a 5xx from a readiness endpoint identically to a 503 anyway —
// which is fine here, but for the wrong reason, and the wrong reason is how a
// liveness probe ends up pointed at the wrong endpoint.
func (r Readiness) Handler() http.Handler {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := now()
		ctx, cancel := context.WithTimeout(req.Context(), ReadyTimeout)
		defer cancel()

		res := Result{Ready: true, Checks: map[string]CheckResult{}}

		// The checks run concurrently. Sequentially, the answer's latency would be
		// the sum of every check's, against a single shared deadline — so one slow
		// check would consume the budget the others needed and report a failure
		// that is really a scheduling artifact. Concurrently, the answer takes as
		// long as its slowest check, and every check gets the full budget.
		type outcome struct {
			name string
			res  CheckResult
		}
		results := make(chan outcome, 2)
		run := func(name string, c Check) {
			if c == nil {
				// Not configured. Reported, and counted as ready: see Readiness.Postgres.
				results <- outcome{name, CheckResult{OK: true}}
				return
			}
			cstart := now()
			err := c(ctx)
			out := CheckResult{OK: err == nil, DurationMS: now().Sub(cstart).Milliseconds()}
			if err != nil {
				out.Error = err.Error()
			}
			results <- outcome{name, out}
		}
		go run("postgres", r.Postgres)
		go run("network", r.Network)

		for i := 0; i < 2; i++ {
			o := <-results
			res.Checks[o.name] = o.res
			if !o.res.OK {
				res.Ready = false
			}
		}
		res.DurationMS = now().Sub(start).Milliseconds()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		// The header is set on every readiness answer, not only the 503, so that
		// an operator can quote the ID of the probe that failed.
		if id := FromContext(req.Context()); id != "" {
			w.Header().Set(HeaderRequestID, id)
		}
		if !res.Ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(res)
	})
}
