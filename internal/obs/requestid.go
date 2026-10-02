package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
)

// HeaderRequestID is the header an ID travels in, in both directions: inbound from
// a trusted peer, outbound to one, and echoed in the response so an operator
// holding a browser can quote the ID they are looking for.
//
// The spelling is nginx's and Envoy's (`X-Request-Id`); Go canonicalises it, so
// reading is case-insensitive either way.
const HeaderRequestID = "X-Request-Id"

// requestIDKey is private so no other package can put a different value under the
// name [FromContext] reads.
type requestIDKey struct{}

// idBytes is 128 bits. The ID is a correlation handle inside one deployment's
// logs, not a secret, so the requirement is that two requests never collide and
// that a guest cannot choose one — both of which 128 bits of crypto/rand give
// for a request rate a venue can physically produce.
const idBytes = 16

// NewRequestID returns a fresh 32-character lowercase hex ID.
//
// crypto/rand, never math/rand: the value is attacker-chosen in the sense that an
// attacker may read it, and a predictable sequence is one where a guest can
// collide with a request they did not make. `docs/rules/security.md` §9 and the
// repository's write-time guard against `math/rand` say the same thing for the
// same reason.
//
// A UUID would be the conventional spelling, and this is deliberately not one: a
// UUID v4 is 122 random bits in a hyphenated string, and the only property this
// needs is "128 bits nobody can predict, in a string that is a legal HTTP header
// value and a legal label value". `crypto/rand` and `encoding/hex` are both
// already here; a UUID dependency is not (`go.mod` is frozen).
func NewRequestID() string {
	var b [idBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand's Read on Linux and macOS does not fail except on a broken
		// kernel entropy source, in which case a process that cannot produce an ID
		// has nothing useful to log. Panicking is louder than logging a constant:
		// a constant ID would silently merge every request's lines into one.
		panic("obs: crypto/rand failed while generating a request ID: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// WithRequestID returns a copy of ctx carrying id, for [FromContext] and
// [ContextHandler] to read. An empty id stores nothing rather than an empty
// string, so "no ID" and "the empty ID" cannot be confused.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// FromContext is the ID of the request being served, or "" when there is none —
// which is the honest answer for a maintenance job on its own timer, and is why
// [ContextHandler] leaves such a line alone rather than writing `"request_id":""`.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// Lookup adopts an inbound ID, but only from a peer that proved itself.
//
// **This is the whole reason the parameter exists.** The header is honoured on the
// policy listener and nowhere else, because that is the one listener whose callers
// authenticate with a client certificate (`docs/rules/security.md` §2's mTLS) and
// the one `deploy/compose.yaml` does not publish. The portal and the console are
// reachable by an unauthenticated device on the SSID and by anything on the venue's
// LAN respectively, and an ID a guest chooses is an ID that can be made to collide
// with another request's, to repeat, or to be absent — at which point an operator
// correlating a support report is reading a value the guest wrote.
//
// The syntax check is not decoration. A header value reaches a log line, and
// `internal/redact` masks MACs and IP addresses in *text*; it is not a defence
// against a value chosen to be awkward. So a candidate must be exactly 32
// lowercase hex characters, which is what [NewRequestID] produces and nothing
// else can.
//
// trusted is the caller's assertion about its own listener, not a decision made
// per request, so a handler that is mounted on two listeners must be mounted as
// two handlers. `cmd/foyer` does that deliberately.
func Lookup(r *http.Request, trusted bool) string {
	if !trusted {
		return ""
	}
	return validID(r.Header.Get(HeaderRequestID))
}

func validID(v string) string {
	v = strings.TrimSpace(v)
	if len(v) != 2*idBytes {
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return v
}

// ContextHandler puts the request ID on every record that has one.
//
// The alternative — passing a `*slog.Logger` down through every call that logs —
// is the shape this package exists to avoid: `internal/api`, `internal/session`
// and `internal/jobs` all log from methods several layers below the handler, and
// the logger would have to become a parameter on repositories, or a package
// global, or both. The context is already threaded through all of them, and this
// is the Go-standard answer to "how do I correlate a request through code that was
// not written for it": `r.Context()` carries the value and the handler lifts it
// onto the record.
//
// A record with no ID in its context is logged with no `request_id` attribute at
// all, rather than an empty one. A maintenance sweep that runs on a timer has no
// request, and `"request_id":""` on every one of its lines is a field that looks
// like it correlates with something and does not.
type ContextHandler struct{ inner slog.Handler }

var _ slog.Handler = ContextHandler{}

// NewContextHandler wraps h so every record carries its context's request ID.
func NewContextHandler(h slog.Handler) ContextHandler { return ContextHandler{inner: h} }

func (h ContextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := FromContext(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.inner.Handle(ctx, r)
}

func (h ContextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return ContextHandler{inner: h.inner.WithAttrs(as)}
}

func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{inner: h.inner.WithGroup(name)}
}

// responseRecorder is the minimum a status line needs: the code and the byte
// count. It deliberately does not implement `http.Flusher`, `http.Hijacker` or
// `io.ReaderFrom`.
//
// That is a decision with a reason. A wrapper that does not implement Flusher
// makes `http.ResponseWriter` lose the optional interfaces underneath it, and
// `net/http` decides streaming on the *outer* type: the guest portal serves
// `/en/` and `/vi/` through `GuestDocumentsHandler`, and a venue photograph is a
// multi-megabyte file, so a wrapper that silently disabled streaming on that path
// would be a regression nobody would attribute to this package. So the recorder
// carries `Unwrap`, which is how Go 1.20+ `http.ResponseController` reaches
// through a wrapper, and the portal's streaming keeps working.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (w *responseRecorder) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseRecorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController find the real writer, so Flush, Hijack and
// SetReadDeadline keep working through this wrapper.
func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseRecorder) code() int {
	if !w.wrote {
		return http.StatusOK
	}
	return w.status
}
