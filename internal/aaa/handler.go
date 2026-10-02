package aaa

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// The HTTP surface `rlm_rest` calls, and the three properties that make it safe to
// put on the request path of a RADIUS server.
//
// `docs/rules/security.md` §2 says why none of this is optional:
//
//	rlm_rest blocks a FreeRADIUS worker thread; the pool is fixed (max_servers),
//	and when it is exhausted Status-Server is blocked too, so the health check
//	dies with the thing it checks.
//
// A slow Policy API therefore does not degrade gracefully. It consumes FreeRADIUS's
// thread pool, and the monitoring that would have told somebody goes down with it.
// Three things follow, and they are the whole of this file:
//
//   - **A deadline that is actually enforced.** Not a documented intention: a
//     context with a timeout, checked, answered.
//   - **A concurrency ceiling, refusing rather than queueing.** Queueing behind a
//     full pool is precisely how a slow Policy API becomes a wedged one, and the
//     host this deploys to has 1 966 MB shared with PostgreSQL and FreeRADIUS.
//   - **A bounded body.** A request that is never finished must not hold a
//     goroutine, a buffer, or a FreeRADIUS worker.
//
// Every one of them answers `503` when it fires, and that is the load-bearing
// detail: `rlm_rest` maps 5xx to `fail`, and `fail` is the rcode the generated
// virtual server routes into the site's declared failure branch. A Policy API that
// cannot answer in time is indistinguishable from one that is down, and it must be
// treated identically -- otherwise a fail-open site quietly becomes fail-closed
// under load, which is the failure nobody notices until a lobby full of guests
// cannot connect.

// HandlerOptions bounds what one Policy API request may cost.
type HandlerOptions struct {
	// Timeout is the deadline placed on evaluating one request. It must stay
	// below `rest`'s own `timeout` (2 s in the generated config), which must in
	// turn stay below the NAS retransmit interval (~3 s), so that the layer that
	// gives up first is the one that can log why.
	Timeout time.Duration

	// MaxConcurrent is the number of requests evaluated at once. Beyond it,
	// requests are refused immediately with 503 rather than queued.
	//
	// The arithmetic, not a round number: FreeRADIUS's `rest` connection pool is
	// capped at `max` (30 in the generated config), and each connection blocks one
	// worker, so one data plane cannot present more than that. 64 leaves room for
	// a second FreeRADIUS during a blue/green cutover and for `foyerctl doctor`,
	// on a host with under 2 GB of RAM.
	MaxConcurrent int

	// MaxBodyBytes caps the request body. An authorize request is a few hundred
	// bytes; 16 KiB is four hundred times that and still nothing.
	MaxBodyBytes int64

	// Logger receives the refusals. Nil means slog.Default().
	Logger *slog.Logger
}

const (
	defaultHandlerTimeout = 1500 * time.Millisecond
	defaultMaxConcurrent  = 64
	defaultMaxBodyBytes   = 16 << 10
)

func (o HandlerOptions) withDefaults() HandlerOptions {
	if o.Timeout <= 0 {
		o.Timeout = defaultHandlerTimeout
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = defaultMaxConcurrent
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = defaultMaxBodyBytes
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// HTTPHandler serves the `rlm_rest` authorize endpoint with the default bounds.
//
// The signature is unchanged from the shape `cmd/foyer` already wires, so this
// stays a drop-in. Handler is the one to call when the bounds need naming.
func (e *Engine) HTTPHandler() http.HandlerFunc {
	h := e.Handler(HandlerOptions{})
	return h.ServeHTTP
}

// Handler serves the `rlm_rest` authorize endpoint with explicit bounds.
func (e *Engine) Handler(opts HandlerOptions) http.Handler {
	o := opts.withDefaults()
	// Buffered channel as the ceiling. A token taken is a request in flight; an
	// empty buffer is a refusal, never a wait.
	slots := make(chan struct{}, o.MaxConcurrent)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			// Refused, not queued. See MaxConcurrent.
			o.Logger.Warn("policy api refusing request: at concurrency ceiling",
				"limit", o.MaxConcurrent)
			writeUndecided(w)
			return
		}

		defer func() {
			// A panic here would otherwise close the connection with no status,
			// which FreeRADIUS reads as a transport failure -- the right rcode by
			// accident, with nothing written down. Make it deliberate and logged.
			if rec := recover(); rec != nil {
				o.Logger.Error("policy api panicked while evaluating", "panic", rec)
				writeUndecided(w)
			}
		}()

		req, err := decodeRequest(http.MaxBytesReader(w, r.Body, o.MaxBodyBytes))
		if err != nil {
			// A question that cannot be read is refused, and it is refused as
			// the CLIENT's, not as an outage.
			//
			// It is tempting to read an unreadable body as "a generated-config
			// bug" and route rlm_rest's `invalid` (what 400 maps to) into the
			// failure branch beside `fail`. Measured on 3.2.10, the body is built
			// from values the client chose, and a User-Name carrying a control
			// character or a byte that is not UTF-8 arrived as `\001` / `\377`
			// -- invalid JSON -- so the client would decide which branch ran,
			// and on a fail-open site that branch is Access-Accept. The
			// generated module sends the two identities as %{hex:} and
			// JSON-encodes every other value with %{jsonquote:} (see
			// decodeRequest), and the virtual server sends `invalid` to the
			// REJECT branch; this status is the same 400 so the two halves
			// agree about what it means. The log line is what still tells an
			// operator a generated tree has gone wrong.
			o.Logger.Warn("policy api refused a request it could not read", "error", err.Error())
			http.Error(w, "Invalid Request Body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), o.Timeout)
		defer cancel()

		decision, err := e.Authorize(ctx, req)
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
			o.Logger.Error("policy api exceeded its own deadline",
				"timeout", o.Timeout.String(), "radiusClient", req.RADIUSClient)
			writeUndecided(w)
			return
		case err != nil:
			o.Logger.Error("policy api could not evaluate", "error", err.Error())
			writeUndecided(w)
			return
		}

		e.writeDecision(w, o.Logger, decision)
	})
}

// writeUndecided answers "I could not decide".
//
// 503 rather than 500 so that a human reading a `radiusd -X` trace sees the word
// unavailable, and 5xx rather than 4xx so `rlm_rest` returns `fail` and the site's
// declared failure behaviour applies. The body is empty: there is nothing to say,
// and anything said here is said on the path that runs when things are already
// wrong.
func writeUndecided(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
}

// writeDecision serialises an authorization outcome, and is the last gate the reply
// passes through.
//
// The gate is here rather than in Authorize on purpose. Authorize returns a
// Decision whose ReplyAttributes map is public and in-process: a VendorProfile, a
// voucher batch's vendor-attribute column, or any future caller can write to it
// after the decision is made and before it is written. Checking intent at the point
// the decision is formed would not see any of that. Checking the bytes about to
// leave does.
//
// On violation the whole reply is dropped -- not the offending key. A reply that
// carried credential material is a reply whose provenance is unknown, and serving
// the rest of it is serving a decision nobody can account for.
func (e *Engine) writeDecision(w http.ResponseWriter, logger *slog.Logger, decision *Decision) {
	if decision == nil {
		writeUndecided(w)
		return
	}

	if !decision.Allow {
		w.Header().Set("Content-Type", "application/json")
		// 403 maps to a FreeRADIUS rcode that is neither `fail` nor `invalid`, so
		// a denial reaches the reject branch and never the failure branch. That
		// distinction is the whole reason the Policy API uses three status
		// classes rather than two.
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "Access-Reject",
			"reason": decision.Reason,
		})
		return
	}

	if bad := ForbiddenReplyAttributes(decision.ReplyAttributes); len(bad) > 0 {
		// The alarm names the attributes and never their values, because a log
		// line written at this moment is a log line containing the credential the
		// gate just stopped.
		logger.Error("policy api refused to serve a reply carrying forbidden attributes",
			"attributes", bad)
		writeUndecided(w)
		return
	}

	// What the vendor profile could not say, said once, here.
	//
	// A warning rather than an error: the reply is correct for this vendor, and
	// the request is served. But something an operator configured did not reach
	// the NAS -- a rate limit with no attribute to carry it, or the second of
	// two Cisco AV-pairs that a single-valued reply cannot hold -- and this is
	// the only place in the running system that knows it. Silence here is the
	// admin console offering a control that reaches nothing, which is the
	// failure `docs/rules/frontend.md` is written about.
	//
	// Names only, never values: this log line is written on the authorization
	// path and everything on it came from a decision about a real guest.
	if len(decision.Unrenderable) > 0 || len(decision.DroppedAttributes) > 0 {
		var vendor string
		if e.profile != nil {
			vendor = string(e.profile.Vendor())
		}
		logger.Warn("policy api served a reply that could not carry part of the decision",
			"vendor", vendor,
			"unrenderable", decision.Unrenderable,
			"dropped_attributes", decision.DroppedAttributes)
	}

	// FreeRADIUS rlm_rest consumes a JSON dictionary of reply pairs.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(decision.ReplyAttributes)
}

// requestKeys is the exact set of JSON keys Request declares, read from its own
// tags so that a field added to Request is a key the handler accepts without a
// second list to keep in step.
var requestKeys = func() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(Request{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}()

// decodeRequest reads one authorization question, strictly.
//
// `encoding/json`'s Decoder is lenient in four ways, and each of them is a way for
// a question to mean something other than what the Policy API reads:
//
//   - **Trailing data is ignored.** Decode reads the first value and stops, so
//     `{...} {...}` and `{...}garbage` both decode.
//   - **A duplicate key's last value wins.** A body carrying `user_name` twice is
//     read as whichever came second; anything that builds the body by splicing a
//     client-chosen string between quotes has handed that client the second one.
//   - **Unknown fields are dropped.** A field this struct does not name is not an
//     error, so a generated tree that sends a field under a new name is silently
//     answering a different question.
//   - **Keys match fields case-insensitively.** `USER_NAME` and `User_Name` are
//     both user_name, so neither is an unknown field, and `user_name` followed
//     by `USER_NAME` is a duplicate key that a byte-for-byte duplicate check does
//     not see. Only the exact names in the struct's tags are keys.
//   - **Invalid UTF-8 is replaced, not refused.** `"alice\xff"` decodes to
//     `"alice�"`, so two different byte strings arrive as one identity.
//
// So the body must be valid UTF-8, exactly one JSON object whose values are all
// strings, with no key twice and no key this package does not know, and nothing
// after it. Anything else is an error, and the handler answers 400.
//
// The two identities may arrive hex-encoded, as `user_name_hex` and
// `outer_user_name_hex`, and the generated tree sends them that way: rlm_rest's
// jsonquote turns every byte >= 0x80 into \u00XX on x86_64, so
// a non-ASCII name arrived as a different name on the amd64 image (measured;
// see rest.conf.tmpl). A hex field must decode, must be
// UTF-8 once decoded, and must not arrive beside its plain twin.
func decodeRequest(body io.Reader) (Request, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return Request{}, err
	}
	if !utf8.Valid(raw) {
		return Request{}, errors.New("request body is not valid UTF-8")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return Request{}, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return Request{}, errors.New("request body is not a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return Request{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return Request{}, errors.New("request body has a non-string key")
		}
		if !requestKeys[key] {
			// EXACT names only. `encoding/json` matches a key to a field
			// case-insensitively, so `USER_NAME` is not an unknown field to
			// DisallowUnknownFields below -- it is user_name -- and a body
			// carrying `user_name` and then `USER_NAME` passed the duplicate
			// check here (two different strings) and was read as the second.
			// The name is not logged: an
			// unknown key is text the sender chose.
			return Request{}, errors.New("request body carries a key this package does not know")
		}
		if seen[key] {
			// A known key is one of requestKeys, never a value, so it is safe to
			// put in a log line.
			return Request{}, fmt.Errorf("request body repeats the key %q", key)
		}
		seen[key] = true
		valTok, err := dec.Token()
		if err != nil {
			return Request{}, err
		}
		if _, ok := valTok.(string); !ok {
			return Request{}, fmt.Errorf("request body's %q is not a string", key)
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return Request{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return Request{}, errors.New("request body carries data after the JSON object")
	}

	var req Request
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&req); err != nil {
		return Request{}, err
	}
	for _, id := range []struct {
		plain, hexKey string
		value, hexed  *string
	}{
		{"user_name", "user_name_hex", &req.UserName, &req.UserNameHex},
		{"outer_user_name", "outer_user_name_hex", &req.OuterUserName, &req.OuterUserNameHex},
	} {
		if !seen[id.hexKey] {
			continue
		}
		if seen[id.plain] {
			// One identity, two spellings: exactly the question that means two
			// things.
			return Request{}, fmt.Errorf("request body carries both %q and %q", id.plain, id.hexKey)
		}
		b, err := hex.DecodeString(*id.hexed)
		if err != nil {
			return Request{}, fmt.Errorf("request body's %q is not hex", id.hexKey)
		}
		if !utf8.Valid(b) {
			return Request{}, fmt.Errorf("request body's %q is not UTF-8 once decoded", id.hexKey)
		}
		*id.value, *id.hexed = string(b), ""
	}
	return req, nil
}
