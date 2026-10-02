package portal

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/redact"
)

// Asking the network to let a guest on, after the guest has already been told
// they are on.
//
// ── the shape of the problem ─────────────────────────────────────────────────
//
// Invariant 4 requires the `303` to be written and flushed **before** the driver
// is called, because Android destroys the login WebView the instant the network
// validates and the iOS Captive Network Assistant only re-checks captivity after
// a document navigation. That ordering is not negotiable and it is not free: by
// the time the controller answers, the status line is on the wire, the response
// is closed, and there is nothing left to tell this client.
//
// So the failure had nowhere to go. `internal/api` swallowed the error and said
// so in a comment; the grant existed, the voucher was spent, and a guest stood in
// a café reading *You're connected* with no internet while the audit log, the
// console and their own screen all said nothing. It is the worst failure this
// product can have, because **the guest has been told they succeeded** and the
// operator has no record to look at when they come to the counter.
//
// Three things had to exist and this file is where two of them are decided:
// where it is recorded (`Authorizations`, and see there for why not the audit
// log), and whether it is retried first.

// authorizeAttempts is how many times the controller is asked before the guest is
// declared not on the network.
//
// **Three, and only for a controller that did not answer.** The retry exists for
// one real cause and is scoped to it: a controller restarting refuses TCP for a
// second or two, which is a failure a guest cannot recover from on their own
// because their voucher is already spent. Asking again costs one round trip on a
// path where nothing is waiting on the answer.
//
// It is deliberately not a general retry. A controller that **answered** has an
// opinion — an expired API key answers `401` three times just as fast — and
// hammering it converts one operator's problem into three.
const authorizeAttempts = 3

// authorizeBackoff is the pause before each retry, one entry per retry.
//
// Short, because the whole loop has to finish inside `internal/api`'s
// `authorizeTimeout` with room left over to record the result, and because the
// failure it targets resolves in seconds rather than minutes. A backoff long
// enough to outlast a real outage would only mean the record arrives later.
var authorizeBackoff = []time.Duration{250 * time.Millisecond, 1 * time.Second}

// recordTimeout is the budget the recording gets, separate from the driver call's.
//
// **Its own context, and this is not defensive dressing.** The driver call runs
// under a deadline the caller set; a controller that hangs until that deadline
// leaves nothing for the write that says it hung — so the one case where the
// record matters most is the one where it would silently not happen.
// `context.WithoutCancel` plus a fresh budget is what stops the report dying with
// the thing it is reporting.
const recordTimeout = 5 * time.Second

// deliver runs the driver call, retries what is worth retrying, and records the
// outcome.
//
// It returns the driver's error so the caller can still see it. Nothing upstream
// can act on it — the response is written — which is exactly why the recording
// happens here rather than being left to a caller who has no channel left.
func (s *Service) deliver(ctx context.Context, place Placement, g Grant) error {
	var err error
	attempts := 0

	for attempt := 1; attempt <= authorizeAttempts; attempt++ {
		attempts = attempt
		err = s.ports.Drivers.Authorize(ctx, place, g)
		if err == nil {
			break
		}
		if attempt == authorizeAttempts || !worthRetrying(err) {
			break
		}
		// A context that is already done will not become undone by waiting, and
		// the record still has to be written. Stop rather than spend the
		// remaining budget sleeping.
		select {
		case <-ctx.Done():
			attempt = authorizeAttempts
		case <-time.After(authorizeBackoff[attempt-1]):
		}
		if ctx.Err() != nil {
			break
		}
	}

	s.record(ctx, g, err, attempts)
	return err
}

// record writes what happened, under its own context and budget.
func (s *Service) record(ctx context.Context, g Grant, err error, attempts int) {
	a := Authorization{
		GrantID:   g.ID,
		SiteID:    g.SiteID,
		NASID:     g.NASID,
		ClientMAC: g.ClientMAC,
		APMAC:     g.APMAC,
		Method:    g.Method,
		VoucherID: g.VoucherID,
		// A phone guest's identity is the number they verified; it is what an
		// operator searching `listAuthAttempts` for a guest at the counter has.
		Username: identityOf(g),
		At:       s.now(),
		Delivery: DeliveryOpen,
		Attempts: attempts,
	}
	if err != nil {
		a.Delivery = DeliveryFailed
		a.Failure, a.Status = classify(err)
	}

	rc, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if rerr := s.ports.Authorizations.Record(rc, a); rerr != nil {
		// The recorder is the thing that reports failures, so its own failure
		// has nowhere better to go than here. Logged rather than swallowed: a
		// deployment whose event store is refusing writes is one where the
		// console is quietly missing rows, and the process log is the last
		// place that can say so.
		s.log.Error("portal: could not record the authorization outcome",
			slog.String("grant", g.ID),
			slog.String("delivery", string(a.Delivery)),
			slog.String("error", rerr.Error()))
	}
}

// worthRetrying separates the controller that has no opinion from the one that
// has one.
//
// `*nas.APIError` is the shape a controller produces when it **answered**, and
// `nas.MockAPIRefusal` exists so both sides of this branch are drivable. A 4xx is
// a decision — a revoked API key, a wrong vendor site id — and repeating the
// question repeats the answer. A 5xx or a `429` is the controller saying *not
// now*, which is the one answered case where asking again is the fix.
func worthRetrying(err error) bool {
	var api *nas.APIError
	if !errors.As(err, &api) {
		// Nothing answered. This is the case the retry exists for.
		return true
	}
	return api.Status >= 500 || api.Status == 429
}

// classify turns a driver error into the two facts an operator can act on, and
// nothing else.
//
// **The error's own text never travels.** `nas.APIError.Error()` embeds the
// controller URL and up to 512 bytes of its response body; the URL can carry a
// credential in its query string and the body is text somebody else wrote. Both
// would land in a log read months later by a person who was not there
// (`docs/rules/security.md` §3).
func classify(err error) (FailureKind, int) {
	var api *nas.APIError
	if errors.As(err, &api) {
		return FailureRefused, api.Status
	}
	return FailureUnreachable, 0
}

// ── the recorder a deployment gets when it wires none ────────────────────────

// logAuthorizations writes to the process log and cannot mark the grant.
//
// It is the default for the same reason `internal/api` supplies a `TokenBucket`
// when a deployment supplies no `Limiter`: there is no safe version of *nothing*.
// A failed authorization with no recorder wired is a failure nobody sees,
// and a line in the log is not a good answer but it is an answer — an operator
// with a support call and a MAC has somewhere to grep.
//
// **What it cannot do is the reason the port exists.** It cannot write
// `listAuthAttempts`, so the console shows nothing; it cannot set the grant's
// `Delivery`, so the guest's connected page still reads `connected`. A deployment
// running on this is reporting to one reader out of three, and it says so on the
// way past.
type logAuthorizations struct{ log *slog.Logger }

func (l logAuthorizations) Record(_ context.Context, a Authorization) error {
	if a.Delivery != DeliveryFailed {
		return nil
	}
	l.log.Error("portal: the network was not opened for a guest who was told it was",
		slog.String("grant", a.GrantID),
		slog.String("site", a.SiteID),
		slog.String("nas", a.NASID),
		slog.String("clientMac", redact.MAC(a.ClientMAC)),
		slog.String("failure", string(a.Failure)),
		slog.Int("status", a.Status),
		slog.Int("attempts", a.Attempts))
	return nil
}
