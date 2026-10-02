package obs

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The label vocabulary. These are the ONLY label names that may appear in an
// exposition, and [RouteLabel] and [MethodLabel] are the only functions that
// produce their values.
//
// **This is a closed set on purpose, and it is the design rather than a
// convention.** Two independent pressures point the same way:
//
//   - *Prometheus.* Every distinct combination of label values is a distinct time
//     series, stored for as long as the target is scraped. A label whose values
//     nobody can enumerate is a series count nobody can budget, and the bill
//     arrives at scrape time rather than at review time.
//   - *This project's retention rules.* A guest's MAC address, phone number,
//     email address, IP address, username and voucher code are personal data
//     under `docs/operator/personal-data.md` and the rules implementing Nghị
//     định 356/2025. A scrape target's storage is a database no erasure reaches
//     and a support bundle copies wholesale, so a value that lands in a label
//     lands where the product cannot take it back from. That is the argument
//     `internal/redact` makes about log lines, and a metrics endpoint is exactly
//     the sort of thing that ends up in a scrape log forever.
//
// So: three labels, all drawn from fixed sets. `listener` is a constant per
// listener. `method` is one of eight HTTP methods or `other`. `status` is a
// three-digit code. `route` is a **matched pattern** or `unmatched` — see
// [RouteLabel], which is where the privacy guarantee actually lives.
// `TestTheExpositionCarriesNoGuestIdentifier` asserts it by reading a real
// exposition back after a request carrying a MAC, a voucher code, a username and
// an IP, and `TestNoCallerEverPassesARequestPathToALabel`,
// `TestMethodLabelIsAClosedSet` and `TestStatusLabelIsBounded` hold the shape of
// the values the labels can take.
const (
	// LabelListener is which of the three listeners served the request.
	LabelListener = "listener"
	// LabelMethod is the HTTP method.
	LabelMethod = "method"
	// LabelRoute is the matched route pattern.
	LabelRoute = "route"
	// LabelStatus is the response status code.
	LabelStatus = "status"
)

// methods is the closed set [MethodLabel] draws from. Go's `net/http` accepts any
// token as a method, so an unbounded method is a client-supplied string arriving
// in a label — the same hazard as a path, and the reason this is a set and not
// `r.Method` passed through.
var methods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"PATCH": true, "DELETE": true, "OPTIONS": true, "CONNECT": true,
}

// MethodLabel is `method`, or `other` for anything outside the closed set. One
// bucket, so the worst case is one extra series rather than one per request.
func MethodLabel(m string) string {
	if methods[m] {
		return m
	}
	return other
}

// other is what an unrecognised method becomes.
const other = "other"

// unmatched is what a request with no recognisable route pattern becomes.
//
// It says something a monitoring system can act on where `other` would not:
// series appearing under it are requests reaching a path this binary does not
// have a pattern for, which is either a client bug or a probe for one.
const unmatched = "unmatched"

// maxPattern bounds a route pattern before it is accepted.
//
// A `net/http` pattern in this codebase is a compile-time constant, and the
// longest is the API base path plus an operation — well under 80 characters, and
// comfortably below anything a caller could push in through a request.
const maxPattern = 80

// RouteLabel is the `route` value for a request, and it is the reason this
// package's metrics cannot leak an identifier.
//
// It takes the **`http.Request.Pattern`** Go's `ServeMux` fills in as it matches
// — `GET /healthz`, `POST /api/v1/aaa/authorize`, `GET /api/v0/sites/{siteId}` —
// and never the request path. A pattern is one of a fixed set of strings compiled
// into this binary, one per route, so it is bounded and it is not
// attacker-influenced. A path is whatever a guest typed, and the guest portal's
// front door puts the device's MAC address and the access point's name in the
// query string of the URL a NAS redirects to (`portal.EntryHandler`), so a
// path-derived label would carry exactly the value the retention rules exist to
// erase.
//
// **What this function does NOT do.** It
// does not decide whether its argument is a path or a pattern, and it cannot: a
// UUID in a path segment is the same length and the same alphabet as a route
// segment, so no byte allowlist and no length bound can tell
// `/api/v0/sites/{siteId}` from `/api/v0/sites/6f1c9d2e-4b7a-4c11-9f3e-2d8a5b6c7e01`.
// A validator that rejected the second is not implementable, because once both
// are strings they are genuinely indistinguishable.
//
// So the guarantee is structural rather than lexical, and it has two parts. This
// function takes one argument and callers pass `r.Pattern`; and the only way a
// path reaches a label is a caller choosing to pass one, so
// `TestNoCallerEverPassesARequestPathToALabel` reads every call site in the
// repository and fails if the argument is anything but `r.Pattern`. That is a
// better guarantee than a heuristic: a validator that guesses wrong fails open,
// and a call-site check fails closed.
//
// The syntax check below stays, because it costs nothing and it rejects the
// genuinely hostile shapes — a query string, a newline, a percent escape — even
// though it cannot catch a well-formed identifier. Its comment says what it can
// and cannot do rather than implying it is the guarantee.
//
// Every byte allowed below appears in a `mux.Handle` or `mux.HandleFunc` pattern
// in `cmd/foyer` or in the generated API handler, and `$` and `*` are Go's own
// `{$}` and wildcard syntax. An empty pattern is `unmatched`: Go sets `Pattern`
// only when a `ServeMux` matched, and rather than fall back to the path — which
// is the leak this function exists to prevent — a handler mounted without a mux
// is named honestly. Every listener in `cmd/foyer` routes through a `ServeMux`,
// so that case is a handler added later without one.
func RouteLabel(pattern string) string {
	if pattern == "" || len(pattern) > maxPattern {
		return unmatched
	}
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == ' ', c == '/', c == '{', c == '}', c == '.', c == '-', c == '_', c == '*', c == '$', c == '[':
		default:
			return unmatched
		}
	}
	return pattern
}

// statusLabel is the `status` value: a three-digit code, or `other` for anything
// outside that range.
//
// A code is bounded — 500 of them, and a server emits a handful — so it is passed
// through with a range check rather than mapped to a fixed set, because the exact
// code is what an operator reads to tell a 401 from a 403. The range check is not
// paranoia about cardinality: it is there so that a status this package did not
// expect, from a handler that wrote something odd, becomes one `other` series
// rather than a label value read out of a struct.
func statusLabel(code int) string {
	if code < 100 || code > 599 {
		return other
	}
	return strconv.Itoa(code)
}

// seriesKey is the label set of one counter sample. It is a struct rather than a
// map so that equality is a comparison and the exposition's label order is fixed
// by the field order.
type seriesKey struct {
	listener string
	method   string
	route    string
	status   string
}

// Metrics is the process's exposition: one counter and one gauge.
//
// **No histogram.** A histogram needs bucket boundaries chosen against a measured
// request-duration distribution, and there is no measurement of one to derive
// them from. Publishing invented buckets produces
// a chart that looks like evidence and is not, which is worse than publishing
// nothing. The count is the honest metric; latency belongs with a
// `foyer_http_request_duration_seconds` histogram added against a real
// measurement, and the day it is added this comment is what it should be written
// next to.
type Metrics struct {
	mu       sync.Mutex
	requests map[seriesKey]uint64
	// started is the process start time, for `foyer_process_start_time_seconds`.
	// A fixed instant rather than a counter, so a dashboard can compute uptime and
	// a restart shows as a jump rather than as a reset.
	started time.Time
}

// NewMetrics returns a registry stamped with the current time.
func NewMetrics() *Metrics { return NewMetricsAt(time.Now()) }

// NewMetricsAt is [NewMetrics] with the clock supplied, so a test can make the
// start time and a scrape's age deterministic.
func NewMetricsAt(now time.Time) *Metrics {
	return &Metrics{requests: map[seriesKey]uint64{}, started: now}
}

// The metric names. `foyer_` rather than `go_` or `app_`: the exposition is
// scraped per job, and a job that also runs the maintenance container would
// otherwise carry two `go_goroutines` series with different meanings in one
// dashboard.
const (
	metricRequests = "foyer_http_requests_total"
	metricStart    = "foyer_process_start_time_seconds"
)

// helpRequests is the docstring. It is a sentence rather than a word because
// `HELP` is the only place a reader learns what a counter counts — and because
// the second sentence is the privacy guarantee, written where somebody reading
// the raw endpoint will find it.
const helpRequests = "HTTP requests served, by listener, method, route pattern and status code. " +
	"Route is the matched pattern and never the request path, so no client-supplied value is a label."

// Observe records one served request. Every argument passes through a label
// function first, so no caller can put an arbitrary string into the exposition
// even by accident — which is the property that makes the privacy claim a
// mechanism rather than a promise.
func (m *Metrics) Observe(listener, method, route string, status int) {
	k := seriesKey{
		listener: listener,
		method:   MethodLabel(method),
		route:    route,
		status:   statusLabel(status),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.requests == nil {
		m.requests = map[seriesKey]uint64{}
	}
	m.requests[k]++
}

func renderLabels(k seriesKey) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range [][2]string{
		{LabelListener, k.listener},
		{LabelMethod, k.method},
		{LabelStatus, k.status},
		{LabelRoute, k.route},
	} {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[0])
		b.WriteString(`="`)
		b.WriteString(escapeLabel(kv[1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// escapeLabel applies the Prometheus text format's escaping for a label value:
// backslash, double quote and line feed, which the format's specification
// requires all three of.
//
// A route pattern cannot contain any of them — [RouteLabel] rejects those bytes
// — so this is a guard against a future change to the label set rather than a
// path values travel today. It is here because a label value that is not escaped
// is a broken exposition, and a broken exposition fails silently at scrape time.
func escapeLabel(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// escapeHelp applies the format's own rules to a `HELP` docstring, which escape
// backslash and line feed but not the double quote.
func escapeHelp(v string) string {
	if !strings.ContainsAny(v, "\\\n") {
		return v
	}
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(v)
}

// WriteText renders the exposition in the Prometheus text format, version 0.0.4,
// which is the format every Prometheus since 2.0 accepts by default and the one
// [Metrics.Handler] declares.
//
// **The format is written out rather than taken from a client library** because the
// project keeps its dependency list fixed: `prometheus/client_golang` would be a
// fifth dependency, and none is added for this. The part of the format this needs
// is small and fully specified — `# HELP`, `# TYPE`, one sample per line, and a
// line feed ending the last line — and `TestTheExpositionIsWellFormedTextFormat`
// checks the output against the specification's requirements rather than against
// this implementation, so a mistake in the renderer is a red test rather than a
// scrape that collects nothing.
func (m *Metrics) WriteText() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make([]seriesKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	// Sorted, because the format's specification asks for reproducible exposition
	// where the cost is not prohibitive. An exposition that reorders itself between
	// two scrapes reads to an operator watching the raw endpoint as a change.
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		switch {
		case a.listener != b.listener:
			return a.listener < b.listener
		case a.method != b.method:
			return a.method < b.method
		case a.route != b.route:
			return a.route < b.route
		case a.status != b.status:
			return a.status < b.status
		}
		return false
	})

	var b strings.Builder
	fmt.Fprintf(&b, "# HELP %s %s\n", metricRequests, escapeHelp(helpRequests))
	fmt.Fprintf(&b, "# TYPE %s counter\n", metricRequests)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s%s %d\n", metricRequests, renderLabels(k), m.requests[k])
	}
	fmt.Fprintf(&b, "# HELP %s Start of the process, as a Unix timestamp in seconds.\n", metricStart)
	fmt.Fprintf(&b, "# TYPE %s gauge\n", metricStart)
	fmt.Fprintf(&b, "%s %d\n", metricStart, m.started.Unix())
	return b.String()
}

// Handler serves the exposition.
//
// **The content type is load-bearing and is not decoration.** From Prometheus 3.0
// a scrape target **must** return a valid `Content-Type` or the scrape fails; the
// canonical value for this format is
// `text/plain; version=0.0.4; charset=utf-8`. An endpoint that returns the wrong
// one looks perfect to `curl` and collects nothing at all.
//
// `Cache-Control: no-store` is on it for the same reason it is on `/healthz`: a
// cached scrape is a stale scrape, and a stale count is worse than none.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := m.WriteText()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}
