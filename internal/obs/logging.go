package obs

import (
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/kiennt048/foyer/internal/redact"
)

// Logger is the process logger: JSON, one object per line, to stdout.
//
// **JSON, and the reason is that a log line has to be a value.** A container log
// is the only diagnostic a venue operator has when something is wrong at 23:00 and
// nobody is at the console, and `grep` over a text format answers "what happened
// to the request that failed" only if the operator knows the exact string the code
// happened to produce. With JSON, every field is queryable and a request ID is
// something to search for rather than something to reconstruct. That is the same
// argument Prometheus makes about metrics, applied to logs.
//
// **stdout, not stderr.** Docker captures both, so it makes no difference to a
// container — but `docker logs` and a `json-file` driver treat them the same, and
// stdout is where a process's own output conventionally goes. What matters more is
// that the choice is one and written down, because the stdlib `log` package
// defaults to stderr and a process that half-migrated would interleave two streams
// with two formats.
//
// The [ContextHandler] is in the chain, so every record carries its request's ID
// without any call site passing a logger down.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(NewContextHandler(h))
}

// Level is the log level, from `FOYER_LOG_LEVEL`.
//
// **An environment variable, and that is a departure from the rule that every
// setting an operator touches lives in the console**. The rule exists so that
// configuring Foyer is a thing an operator does in the console rather than in a
// terminal on the box. A log level is not configuration of the product: it changes
// nothing about what Foyer does, it changes how much of what it did is written
// down, and — the reason it cannot go in the console — **the console is one of the
// things that can be broken.** A control plane so noisy it fills its disk, or so
// quiet that the failure is invisible, is diagnosable only with a level set from
// outside it.
//
// The default is INFO, which is what the composition root used before this
// package existed for anything but `internal/jobs`. DEBUG is available for a
// support session and is the level at which the portal's per-request lines
// appear (see [Listener.accessLogLevel]).
const defaultLogLevel = "INFO"

// ParseLevel reads a `FOYER_LOG_LEVEL` value, falling back to INFO for anything
// unrecognised.
//
// A malformed level is INFO and not an error because the alternative is a control
// plane that refuses to start over a log level, on the same day an operator typed
// it. An unrecognised level is a typo; the process has a dozen other things to
// refuse to start over and this is not one of them.
func ParseLevel(v string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LevelFromEnv is [ParseLevel] over `FOYER_LOG_LEVEL`, which is the whole of this
// package's configuration surface.
func LevelFromEnv() slog.Level { return ParseLevel(os.Getenv("FOYER_LOG_LEVEL")) }

// Install makes logger the process's default for both logging systems.
//
// **`slog.SetDefault` and `log.SetOutput` together, and the second one is the
// point.** Fourteen non-test files under `internal/` call `slog.Default()`, and
// `internal/api` logs from `surface.go` deep inside a request. Those pick up the
// JSON handler and the request ID from `slog.SetDefault` alone. The four `log.*`
// call sites in `cmd/foyer` and one in `internal/apistore` do not, and
// `redact.ErrorLog()` — which is every `http.Server`'s error log, so every failed
// TLS handshake a phone produces on the guest portal — does not either. Routing
// the stdlib logger's output through the same handler is what makes "every log
// line is structured" true rather than nearly true, and it is a two-line change
// rather than an argument with `net/http` about a `*log.Logger` it insists on
// having.
//
// The stdlib logger's flags and prefix are cleared for the same reason: a
// `2009/11/10 23:00:00 ` prefix in front of a JSON object is a line no JSON parser
// accepts.
//
// The returned function restores the previous defaults, for a test.
func Install(logger *slog.Logger) (restore func()) {
	prevSlog := slog.Default()
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	prevPrefix := log.Prefix()

	slog.SetDefault(logger)
	log.SetOutput(&slogBridge{logger: logger})
	log.SetFlags(0)
	log.SetPrefix("")
	return func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	}
}

// slogBridge turns a stdlib `log` line into a structured record.
//
// **Every line passes through `redact.Line` before it is written.** A bridge that
// captured the stdlib logger's output and handed it to slog without it would leave
// `redact.ErrorLog()` masking on a writer nothing uses — so the guarantee
// `internal/redact`'s contract makes about "every `http.Server`'s error log" would
// quietly stop being true, and a guest's address would go into a JSON line in
// cleartext.
//
// The masking test in `internal/redact` exercises `lineWriter` directly, so it
// would stay green on a code path no production line takes. `TestTheStdlibLineIsMasked`
// in this package is what notices, because it drives `log.Default()` — the object
// `net/http` actually writes a failed handshake to — rather than a helper. A test
// that stays green after the thing it guarded has moved is a check that looks like
// it is checking something and is not, which is why the assertion lives here.
//
// The level is ERROR, and it is a choice with a cost worth naming: a line from
// `net/http`'s error log is a TLS handshake a phone refused, which is routine and
// happens several times an hour on a venue with a strict certificate policy. Those
// belong at WARN or below. But this writer cannot tell a handshake error from a
// `log.Fatalf`, and a bridge that guessed would be worse than one that does not:
// an operator filtering at `level=error` must not lose a process that refused to
// start. So everything routed through here is ERROR, and the `log.Printf` sites
// in `cmd/foyer` that are not errors are converted to `slog` directly rather than
// left to be misfiled here.
//
// A write that is not newline-terminated — `log.Logger` always terminates, but a
// caller writing to `os.Stderr` and then to `log` can interleave — is trimmed
// rather than rejected, because a partial JSON object in a log is a line nobody
// can parse, and dropping information is the lesser fault only when the
// information was never structured to begin with.
type slogBridge struct{ logger *slog.Logger }

func (b *slogBridge) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line == "" {
		return len(p), nil
	}
	b.logger.Error("stdlib", slog.String("message", redact.Line(line)))
	return len(p), nil
}

var _ io.Writer = (*slogBridge)(nil)

// onceWriter serialises writes to a single underlying writer.
//
// `slog`'s handlers already hold a mutex per handler, so this is not needed for
// them. It is here because the stdlib logger has its own lock and the two do not
// share one, and a JSON exposition interleaved mid-object in a container log is a
// file no `jq` will read.
type onceWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *onceWriter) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.w.Write(p)
}

// Writer returns a writer that serialises everything written to it, for a caller
// that composes two logging systems onto one destination.
func Writer(w io.Writer) io.Writer { return &onceWriter{w: w} }
