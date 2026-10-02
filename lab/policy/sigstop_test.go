// Package policy_test is the SIGSTOP harness: what a real FreeRADIUS does to a
// real Access-Request while the Go Policy API is wedged.
//
// `docs/rules/radius.md` names this test and says what it is for:
//
//	There is a lab/ test that SIGSTOPs the Go process and asserts the defined
//	behaviour -- it is the test that proves this architecture was worth choosing.
//
// The architecture's claim is that putting policy in a Go process beside FreeRADIUS
// does not make the network fail in an undefined way when that process dies. Until
// something makes it happen, that is a hope.
//
// **SIGSTOP rather than SIGKILL, deliberately.** A killed process's socket is
// closed by the kernel, so the connection is refused instantly and FreeRADIUS finds
// out in microseconds -- the easy case, and not the one that happens. A *stopped*
// process still has its listening socket: the kernel completes the TCP handshake
// into the accept backlog and nothing ever answers. That is what a wedged
// goroutine, an exhausted connection pool, a stop-the-world pause and a swapping
// host all look like from FreeRADIUS, and it is the case where `rest`'s timeout,
// the worker it is holding, and the site's declared failure branch all matter at
// once.
//
// What this harness cannot do, stated because `docs/rules/testing.md` requires
// it: it runs `radclient`, so it says nothing about EAP -- the 802.1X case here is
// an Access-Request carrying an EAP-Message attribute, not a supplicant. Proving
// the fail-closed rule for a real EAP conversation needs `eapol_test`, and that is
// `lab/eap`'s job.
package policy_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/aaa"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

// The address radclient sends from. It runs inside the FreeRADIUS container, so
// the client FreeRADIUS matches is the loopback one.
//
// There is no client in the generated clients.conf to match against, and that is
// the correct state rather than a gap. Clients live in the SQL client table --
// `read_clients = yes`, plus a dynamic-clients lookup for rows added after startup
// -- because the alternative, regenerate and restart on every access point an
// operator adds, drops every in-flight EAP conversation and the in-memory TLS
// session cache on the site. So this harness inserts its own client row, with a
// secret it generates, and the generated tree stays free of shared secrets.
const labNASAddress = "127.0.0.1"

// waitForRadiusd is generous because the first run pulls or builds an image.
const (
	radiusdReadyTimeout = 90 * time.Second
	radclientTimeout    = 20 * time.Second
)

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(base64.RawURLEncoding.EncodeToString(b)))
}

func requireDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("FOYER_LAB_SKIP") != "" {
		t.Skip("FOYER_LAB_SKIP is set: this guarantee is UNPROVEN on this run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker is not on PATH, so the SIGSTOP guarantee is UNPROVEN on this machine: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker is present but not usable, so the SIGSTOP guarantee is UNPROVEN on this machine: %v: %s", err, out)
	}
}

func run(t *testing.T, timeout time.Duration, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func mustRun(t *testing.T, timeout time.Duration, name string, args ...string) string {
	t.Helper()
	out, err := run(t, timeout, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

// lab is one isolated stack: its own docker network, its own PostgreSQL, its own
// FreeRADIUS, and a Policy API running on the host as a separate OS process so that
// there is something to stop.
//
// Isolated on purpose. `lab/docker-compose.yml` has fixed container names and
// published ports and is not this test's to manage; stopping the backend in it
// would interfere with whoever is using it, and a test that can only run when
// nothing else is running is a test that does not run.
type lab struct {
	network   string
	pgName    string
	frName    string
	raddbDir  string
	policyCmd *exec.Cmd
	policyURL string
	secret    string

	// ownerURL reaches this stack's PostgreSQL from the host, as the schema
	// owner, for migrating and seeding. storeBacked makes the Policy API serve
	// internal/store over appURL instead of the fixtures; see policystub's
	// -database-url.
	ownerURL string
	// appURL is the control plane's own login, foyer_app. The Policy API is
	// served over it, as in deploy/: store.Open refuses the schema owner
	// (migration 00019).
	appURL      string
	storeBacked bool
}

func TestSIGSTOPOnThePolicyAPI(t *testing.T) {
	requireDocker(t)
	l := startLab(t)

	// Baseline first, and it is not a formality: everything below is an assertion
	// about a *changed* answer, and an answer that was never right in the first
	// place would satisfy most of them.
	//
	// This one has already earned it once. When RADIUS clients moved out of the
	// generated clients.conf and into the SQL client table, this harness lost its
	// client -- and every "Access-Reject while the Policy API is stopped"
	// assertion below would have gone on passing, because a packet from an
	// unknown client is silently dropped and a drop is not an Access-Accept.
	t.Run("baseline: the stack authorizes a valid voucher", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(labVoucher(), false))
		if !strings.Contains(out, "Received Access-Accept") {
			t.Fatalf("the harness is not wired: a valid voucher did not produce an Access-Accept.\n"+
				"If there was no reply at all, suspect the client row before the policy engine -- "+
				"FreeRADIUS discards a packet from an address the client table has no row for, without answering.\n"+
				"radclient said:\n%s\nradiusd said:\n%s", out, l.radiusdLogs(t, 120))
		}
	})

	t.Run("baseline: a revoked voucher is refused", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(revokedVoucher(), false))
		if !strings.Contains(out, "Received Access-Reject") {
			t.Fatalf("a revoked voucher was not refused, so a refusal below would prove nothing.\n%s", out)
		}
	})

	// The third baseline guards a precondition that is easy to break from the
	// harness side.
	//
	// RFC 3579 §3.1 forbids an Access-Request carrying both EAP-Message and
	// User-Password, and FreeRADIUS refuses such a packet at the socket:
	//
	//	Receive - Bad packet from host 127.0.0.1: Packet contains
	//	EAP-Message and non-EAP authentication attribute
	//
	// Before any virtual server runs -- so the request trace is empty, even at
	// `-xx`, and an empty trace reads like evidence about the generated
	// configuration when it is not. Drop the attribute and the same packet is
	// answered in under a second, `eap` module or no `eap` module; eapol_test
	// completes EAP-TLS against this generator's output.
	//
	// Two things follow:
	//
	//   - **A test that constructs the input constructs the failure too.**
	//   - **"No line in the trace" is not evidence about the config.** It is
	//     evidence that the packet did not reach the config, and those are
	//     different claims.
	//
	// So the check guards the precondition: every 802.1X assertion below is about
	// the failure model only if the packet is answerable at all, and "no
	// Access-Reject while the Policy API is stopped" is satisfied perfectly by a
	// server that never answers the packet.
	eapReachesTheServer := true
	t.Run("baseline: an EAP-shaped request is answered at all", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(labVoucher(), true))
		// `Access-` is not the assertion: radclient echoes its own outgoing
		// packet, so `strings.Contains(out, "Access-")` matches "Sent
		// Access-Request" and reports a pass on a request that got no reply for
		// ten seconds.
		if strings.Contains(out, "No reply from server") || !answered(out) {
			eapReachesTheServer = false
			t.Logf("FINDING (measured, this run): an Access-Request carrying EAP-Message got no reply and "+
				"no line in radiusd's trace at -xx, with the Policy API running and healthy. "+
				"Suspect the packet before the configuration: RFC 3579 §3.1 forbids EAP-Message beside a "+
				"non-EAP authentication attribute and FreeRADIUS refuses that at the socket, which is a "+
				"mistake a harness can make itself. Check authRequest first, then radiusd's own -X output "+
				"for a `Bad packet from host` line.\nradclient said:\n%s", out)
		}
	})

	// Stop the Policy API. From here to SIGCONT, every request FreeRADIUS makes
	// opens a TCP connection the kernel accepts and nothing answers.
	l.stopPolicyAPI(t)

	t.Run("802.1X is fail-closed and it is not the operator's to change", func(t *testing.T) {
		if !eapReachesTheServer {
			// UNPROVEN, and for a precondition rather than a policy defect -- the
			// same class of unrunnable as docker being absent, and reported in the
			// same voice. This is not a pass: this run has not demonstrated that an
			// 802.1X supplicant is refused while the control plane is down.
			t.Skip("UNPROVEN: an EAP-shaped Access-Request is not being answered at all (see the baseline " +
				"above), so a refusal here would prove nothing. Fix the packet or the configuration; this " +
				"subtest starts asserting again by itself once one is answered.")
		}
		out := l.radclient(t, l.authRequest(labVoucher(), true))
		if !strings.Contains(out, "Received Access-Reject") {
			t.Errorf("an Access-Request carrying EAP was not rejected while the Policy API was stopped.\n"+
				"Failing open on 802.1X puts unauthenticated devices on a corporate network; it is not a venue preference.\n"+
				"radclient said:\n%s\nradiusd said:\n%s", out, l.radiusdLogs(t, 400))
		}
	})

	t.Run("a guest at a fail-closed site is rejected", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(labVoucher(), false))
		if !strings.Contains(out, "Received Access-Reject") {
			t.Errorf("a guest was authorized while the Policy API was stopped, at a site whose declared mode is fail-closed.\n%s", out)
		}
	})

	t.Run("the answer arrives before the NAS gives up", func(t *testing.T) {
		// Two numbers, because they are two different claims and only one of them
		// is a hard rule.
		//
		//   target  ~3 s   -- a NAS retransmits at about three seconds. An answer
		//                     later than that means the client has already sent
		//                     the request again, so every degraded authentication
		//                     costs the server two or three requests instead of
		//                     one, and the pool that rlm_rest is blocking is the
		//                     one paying for it.
		//   ceiling  8 s   -- a NAS gives up after a handful of retransmits.
		//                     Beyond here the supplicant sees an authentication
		//                     *timeout*, and on iOS and Android that is a
		//                     materially worse and more confusing outcome than a
		//                     refusal, which at least prompts for the password.
		//
		// MEASURED, three consecutive runs: 5.10 s, 5.11 s, 5.11 s.
		// Inside the ceiling and outside the target, consistently. The arithmetic
		// that produces it is `rest`'s `timeout = 2.0` plus a retry plus
		// `reject_delay = 1`, all of which live in `internal/raddb`'s templates.
		const target = 3 * time.Second
		const ceiling = 8 * time.Second

		start := time.Now()
		out := l.radclient(t, l.authRequest(labVoucher(), false))
		elapsed := time.Since(start)

		if !answered(out) {
			t.Fatalf("no answer at all in %s while the Policy API was stopped: this is the undefined behaviour the whole file is about.\n%s", elapsed, out)
		}
		if elapsed > ceiling {
			t.Errorf("FreeRADIUS took %s to answer while the Policy API was stopped, past the point a NAS gives up", elapsed)
		}
		if elapsed > target {
			t.Logf("FINDING (measured, this run): the answer took %s, past the ~3 s NAS retransmit interval. "+
				"Every degraded authentication therefore costs two or three requests. The knobs are `rest`'s "+
				"connect_timeout/timeout and `reject_delay`, in internal/raddb's templates.",
				elapsed.Round(time.Millisecond))
		}
		t.Logf("MEASURED: answer while the Policy API is SIGSTOPped arrived in %s", elapsed.Round(time.Millisecond))
	})

	t.Run("Status-Server still answers while the Policy API is wedged", func(t *testing.T) {
		// `docs/rules/security.md` §2 names this exact failure: rlm_rest blocks
		// a FreeRADIUS worker, the pool is fixed, and when it is exhausted
		// Status-Server is blocked too -- so the health check dies with the thing
		// it checks, and the monitoring says nothing is wrong right up until
		// somebody phones.
		//
		// One request cannot exhaust a pool, so this is the weak version of that
		// claim. It is here because it is the half that can be run without a load
		// generator, and because a Status-Server that is *already* broken with one
		// request in flight would make the strong version pointless to write.
		out := l.statusServer(t)
		if !strings.Contains(out, "Received Access-Accept") {
			t.Errorf("Status-Server did not answer while the Policy API was stopped. The health check is dying with the thing it checks.\n%s", out)
		}
	})

	// And back. A recovery that does not happen is the same outage.
	l.startPolicyAPI(t)

	t.Run("the network recovers when the Policy API does", func(t *testing.T) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			out := l.radclient(t, l.authRequest(labVoucher(), false))
			if strings.Contains(out, "Received Access-Accept") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the Policy API is answering again and FreeRADIUS is still refusing.\n%s", out)
			}
			time.Sleep(time.Second)
		}
	})
}

// TestSIGSTOPAtAFailOpenSite is the other half of the file above, and it is a
// separate test function because it needs a separate stack.
//
// A site's failure mode is resolved when the configuration is GENERATED, not when
// the request arrives -- `internal/aaa/failure.go` explains why, and it is forced:
// at the moment the failure branch runs, the thing that would resolve a rule is the
// thing that is down. So fail-open and fail-closed are two trees, and two trees are
// two servers.
//
// What this asserts is deliberately asymmetric, and the asymmetry is the point. A
// setting that changes the guest answer and *also* changes the 802.1X answer has
// not implemented this rule, it has removed it.
func TestSIGSTOPAtAFailOpenSite(t *testing.T) {
	requireDocker(t)

	// Built through `aaa` rather than as an aaa.Advice literal. A literal here
	// would be a second copy of failure.go's resolution rules -- the 802.1X
	// override, the grace clamp, which attributes a degraded grant may carry --
	// and it would keep passing on the day one of them changed, describing a
	// policy `aaa` no longer produces.
	site := aaa.SiteFailurePolicy{
		SiteID:            "lab-fail-open",
		GuestFailureMode:  aaa.FailOpen,
		GuestGraceSeconds: 600,
	}
	l := startLabWith(t, []aaa.Advice{site.Advice(300)})

	// The same baseline as the file above, and for the same reason: every
	// assertion below is about a *changed* answer, and this harness has already
	// lost its client once, in a way that left every "rejected while stopped"
	// assertion passing because an unknown client is silently discarded.
	t.Run("baseline: the fail-open stack authorizes a valid voucher normally", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(labVoucher(), false))
		if !strings.Contains(out, "Received Access-Accept") {
			t.Fatalf("the fail-open stack is not wired: a valid voucher did not produce an Access-Accept "+
				"with the Policy API running.\nIf there was no reply at all, suspect the client row before "+
				"the policy engine.\nradclient said:\n%s\nradiusd said:\n%s", out, l.radiusdLogs(t, 120))
		}
	})

	// And the baseline that this test needs and the other does not: an accept
	// while the Policy API is HEALTHY must not already look like a degraded one.
	// Without this, "Class = Foyer:degraded appears in the reply" is satisfied by
	// a configuration that marks every accept degraded, and the assertion below
	// would be measuring nothing.
	t.Run("baseline: a healthy accept is not marked degraded", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(labVoucher(), false))
		if carriesDegradedClass(out) {
			t.Fatalf("an accept made while the Policy API was healthy already carries Class = %q, "+
				"so finding it below would prove nothing.\n%s", aaa.DegradedClass, out)
		}
	})

	eapAnswered := true
	t.Run("baseline: an EAP-shaped request is answered at all", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(labVoucher(), true))
		if strings.Contains(out, "No reply from server") || !answered(out) {
			eapAnswered = false
			t.Logf("an Access-Request carrying EAP-Message got no reply on the fail-open stack. "+
				"Suspect the packet before the configuration -- RFC 3579 §3.1 forbids EAP-Message beside a "+
				"non-EAP authentication attribute and FreeRADIUS refuses that at the socket.\n%s", out)
		}
	})

	l.stopPolicyAPI(t)

	t.Run("a guest at a fail-open site is let on, bounded and marked", func(t *testing.T) {
		out := l.radclient(t, l.authRequest(labVoucher(), false))
		if !strings.Contains(out, "Received Access-Accept") {
			t.Fatalf("a guest was refused while the Policy API was stopped, at a site whose declared mode "+
				"is FAIL-OPEN. The operator's setting is not reaching the generated configuration, which is "+
				"the whole defect this subtest exists for -- a per-site setting that produces identical "+
				"behaviour on both settings.\nradclient said:\n%s\nradiusd said:\n%s",
				out, l.radiusdLogs(t, 400))
		}

		// Marked. Without Class the sessions a site let on while blind are
		// indistinguishable from authorized ones in radacct afterwards, which is
		// the difference between an operator who can answer "who got on while we
		// were down" and one who cannot. Read from `aaa` rather than typed here.
		if !carriesDegradedClass(out) {
			t.Errorf("the degraded accept does not carry Class = %q, so nothing downstream can tell it "+
				"from a real authorization.\n%s", aaa.DegradedClass, out)
		}

		// Bounded. An unbounded grace grant means a thirty-second blip mints a
		// session that outlives the outage by hours; the device has to come back
		// and ask again, so that the first request after recovery is a real
		// decision rather than another free pass.
		st := sessionTimeout(out)
		if st == 0 {
			t.Errorf("the degraded accept carries no Session-Timeout at all: it is a free pass with no end.\n%s", out)
		}
		if st > aaa.MaxGraceSeconds {
			t.Errorf("the degraded accept carries Session-Timeout = %d, past aaa.MaxGraceSeconds (%d). "+
				"A grace period longer than the outages it covers is a site that authorizes nobody and "+
				"lets everybody on.", st, aaa.MaxGraceSeconds)
		}
		t.Logf("MEASURED: guest at a fail-open site, Policy API SIGSTOPped -- Access-Accept, Class = %q, Session-Timeout = %d",
			aaa.DegradedClass, st)
	})

	// The half that must not follow the other. This is the assertion that makes
	// the subtest above safe to have written at all.
	t.Run("802.1X is still refused at a fail-open site", func(t *testing.T) {
		if !eapAnswered {
			t.Skip("UNPROVEN: an EAP-shaped Access-Request is not being answered at all on this stack " +
				"(see the baseline above), so a refusal here would prove nothing.")
		}
		out := l.radclient(t, l.authRequest(labVoucher(), true))
		if !strings.Contains(out, "Received Access-Reject") {
			t.Errorf("an Access-Request carrying EAP was not rejected at a FAIL-OPEN site while the Policy "+
				"API was stopped.\nThe operator may choose to wave guests through while the control plane is "+
				"blind; they may not choose that for a corporate supplicant, and there is no degraded 802.1X "+
				"accept in any case -- the supplicant expects keying material this server cannot invent.\n"+
				"radclient said:\n%s\nradiusd said:\n%s", out, l.radiusdLogs(t, 400))
		}
		if carriesDegradedClass(out) {
			t.Errorf("an EAP-shaped request at a fail-open site came back carrying the degraded Class.\n%s", out)
		}
	})

	l.startPolicyAPI(t)

	t.Run("the fail-open site stops handing out degraded grants when the Policy API returns", func(t *testing.T) {
		// The recovery assertion that matters at a fail-open site, and it is not
		// the same one the fail-closed file makes. There, recovery is visible
		// because a Reject becomes an Accept. Here it was an Accept throughout, so
		// an unrecovered server and a recovered one both say "Access-Accept" --
		// and the only thing that distinguishes them is the mark.
		deadline := time.Now().Add(30 * time.Second)
		for {
			out := l.radclient(t, l.authRequest(labVoucher(), false))
			if strings.Contains(out, "Received Access-Accept") && !carriesDegradedClass(out) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the Policy API is answering again and the site is still handing out degraded "+
					"grants. Every guest is being let on unauthorized while the control plane is healthy.\n%s", out)
			}
			time.Sleep(time.Second)
		}
	})
}

// carriesDegradedClass reports whether radclient's printed reply carries the
// degraded mark.
//
// **Not a substring search for the string itself, and that was measured rather
// than foreseen.** `Class` is an `octets` attribute (RFC 2865 §5.25), so radclient
// prints it as hex:
//
//	Class = 0x466f7965723a6465677261646564
//
// Asserting `strings.Contains(out, aaa.DegradedClass)` goes red against a
// configuration that emits the attribute correctly. That is the cheap direction
// of the mistake. The expensive one is green: a baseline asserting a healthy
// accept is **not** marked degraded could never find that string either, so it
// would pass whether or not the mark was there -- an assertion aimed somewhere
// the subject does not appear (`docs/rules/testing.md`, Principles). Repairing
// the defect would not change its result.
//
// Both renderings are accepted, and the hex one is derived from aaa.DegradedClass
// rather than written out, so the day that constant changes this follows it instead
// of describing a mark nothing emits.
func carriesDegradedClass(radclientOutput string) bool {
	if strings.Contains(radclientOutput, "Class = "+strconv.Quote(aaa.DegradedClass)) {
		return true
	}
	return strings.Contains(radclientOutput, "0x"+hex.EncodeToString([]byte(aaa.DegradedClass)))
}

// sessionTimeout reads Session-Timeout out of radclient's printed reply, or 0.
//
// A regexp over the reply rather than a substring check: the assertion is about the
// value being bounded, and "Session-Timeout is present" is satisfied by a
// configuration that hands out eight hours.
func sessionTimeout(radclientOutput string) int {
	m := sessionTimeoutRE.FindStringSubmatch(radclientOutput)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

var sessionTimeoutRE = regexp.MustCompile(`Session-Timeout\s*=\s*(\d+)`)

// No reflection guard on `raddb.Config`'s field names decides whether the
// assertions below run. A test that consults a predicate to decide whether to
// test is a test that can go quiet -- on a rename, say -- and a guard on a *name*
// stands in for a fact about a *capability*: any field containing "fail" would
// arm it. The assertions below read the generated configuration instead, which
// is the thing the claim is actually about.

// answered reports whether radclient received a reply, as opposed to echoing the
// request it sent.
//
// radclient prints "Sent Access-Request ..." for its own outgoing packet and
// "Received Access-Accept ..." for a reply, so any assertion that greps for
// "Access-" is satisfied by a packet nothing ever answered -- a pass on a request
// that timed out after ten seconds -- which is why this is a function rather than
// a substring.
func answered(radclientOutput string) bool {
	return strings.Contains(radclientOutput, "Received Access-")
}

func labVoucher() string     { return "LAB-VOUCHER-OK" }
func revokedVoucher() string { return "LAB-VOUCHER-REVOKED" }

// authRequest builds the attribute list radclient reads on stdin.
//
// Message-Authenticator is present because every generated client carries
// `require_message_authenticator = yes` (BlastRADIUS, CVE-2024-3596). A request
// without it is dropped before any of this is exercised -- which would look
// identical to a rejection, and would have made every assertion here vacuous.
func (l *lab) authRequest(user string, withEAP bool) string {
	attrs := []string{
		fmt.Sprintf("User-Name = %q", user),
		`NAS-IP-Address = 127.0.0.1`,
		`NAS-Identifier = "lab-policy"`,
		`Calling-Station-Id = "02:11:22:33:44:55"`,
		`Called-Station-Id = "00:11:22:33:44:00:foyer-lab"`,
		`Message-Authenticator = 0x00`,
	}
	if withEAP {
		// An EAP-Message attribute is what makes a supplicant a supplicant, and
		// its presence is the whole of the 802.1X classification (see
		// internal/aaa/failure.go). This is an EAP-Response/Identity for `linh`;
		// nothing here completes an EAP conversation, and it is not meant to --
		// see the package comment.
		//
		// **No User-Password on this branch.** RFC 3579 §3.1 forbids an Access-Request from
		// carrying both EAP-Message and a non-EAP authentication attribute, and
		// FreeRADIUS enforces it at the socket, before any virtual server runs:
		//
		//	Receive - Bad packet from host 127.0.0.1: Packet contains
		//	EAP-Message and non-EAP authentication attribute
		//
		// Sending both would discard every EAP-shaped request in this file
		// before it reached a virtual server -- and the absence of any line in
		// radiusd's request trace would read as evidence about the generated
		// configuration when it is that discard. Without this one attribute the
		// packet is answered in under a second.
		attrs = append(attrs,
			`EAP-Message = 0x02000009016c696e68`,
			`Service-Type = Framed-User`)
	} else {
		attrs = append(attrs, `User-Password = "irrelevant"`)
	}
	return strings.Join(attrs, "\n") + "\n"
}

func (l *lab) radclient(t *testing.T, request string) string {
	t.Helper()
	// -t 10 -r 1: one try, waited out. radclient's default 3-second timeout sits
	// exactly on the boundary this test measures -- FreeRADIUS answers a stopped
	// Policy API in about three seconds -- so a retransmitting client turns "the
	// answer arrived at 3.0 s" into "5.1 s", and the number stops being about
	// FreeRADIUS at all.
	return l.exec(t, request, "radclient", "-x", "-t", "10", "-r", "1", "127.0.0.1:1812", "auth", l.secret)
}

func (l *lab) statusServer(t *testing.T) string {
	t.Helper()
	return l.exec(t, "Message-Authenticator = 0x00\n",
		"radclient", "-x", "-t", "10", "-r", "1", "127.0.0.1:18120", "status", l.secret)
}

// radiusdLogs is what a failure in this package is actually about. An assertion
// that says "no Access-Reject" and stops has thrown away the only evidence there
// is: the server's own trace of which unlang branch it took.
func (l *lab) radiusdLogs(t *testing.T, lines int) string {
	t.Helper()
	out, _ := run(t, 20*time.Second, "docker", "logs", "--tail", fmt.Sprintf("%d", lines), l.frName)
	return out
}

func (l *lab) exec(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), radclientTimeout)
	defer cancel()

	full := append([]string{"exec", "-i", l.frName}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Stdin = strings.NewReader(stdin)
	out, _ := cmd.CombinedOutput() // radclient exits non-zero on Access-Reject, which is an outcome
	return string(out)
}

func startLab(t *testing.T) *lab { return startLabWith(t, nil) }

// startLabWithStore is a stack whose Policy API answers from the real schema, for
// the questions a fixture cannot answer: whether an account's password and a
// voucher's state are honoured end to end.
func startLabWithStore(t *testing.T) *lab { return startLabOn(t, nil, true) }

// startLabWith brings up one isolated stack whose generated tree carries `advice`.
//
// The parameter is the whole reason the fail-open subtest below can assert
// anything. A site's failure mode is resolved at GENERATION time -- by the time
// the failure branch runs, the thing that would resolve a rule is the thing that is
// down -- so two failure modes are two generated trees and therefore two stacks.
// A harness that could only build one tree could only ever observe one branch, and
// "the setting produces the same behaviour either way" is precisely the defect
// worth catching.
func startLabWith(t *testing.T, advice []aaa.Advice) *lab { return startLabOn(t, advice, false) }

func startLabOn(t *testing.T, advice []aaa.Advice, storeBacked bool) *lab {
	t.Helper()
	tag := randomToken(t)[:10]
	l := &lab{
		network:     "foyer-policy-net-" + tag,
		pgName:      "foyer-policy-pg-" + tag,
		frName:      "foyer-policy-fr-" + tag,
		storeBacked: storeBacked,
	}

	mustRun(t, 30*time.Second, "docker", "network", "create", l.network)
	t.Cleanup(func() { _, _ = run(t, 30*time.Second, "docker", "network", "rm", l.network) })

	// --- PostgreSQL -------------------------------------------------------
	//
	// Present because the generated virtual server includes `sql` in accounting
	// and post-auth and rlm_sql instantiates its pool at startup. A stack without
	// it does not start, so there would be nothing to SIGSTOP against.
	pgPassword := randomToken(t)
	mustRun(t, 120*time.Second, "docker", "run", "-d",
		"--name", l.pgName, "--network", l.network,
		"-e", "POSTGRES_DB=foyer", "-e", "POSTGRES_USER=foyer",
		"-e", "POSTGRES_PASSWORD="+pgPassword,
		"-p", "127.0.0.1:0:5432",
		"postgres:16-alpine")
	t.Cleanup(func() { _, _ = run(t, 60*time.Second, "docker", "rm", "-f", l.pgName) })

	pgPort := publishedPort(t, l.pgName, "5432/tcp")

	ownerURL := fmt.Sprintf("postgres://foyer:%s@127.0.0.1:%s/foyer?sslmode=disable", pgPassword, pgPort)
	l.ownerURL = ownerURL
	waitForPostgres(t, l.pgName, ownerURL)
	migCtx, migCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer migCancel()
	if err := store.Migrate(migCtx, ownerURL); err != nil {
		t.Fatalf("migrating the harness database: %v", err)
	}
	// FreeRADIUS logs in as its own role, as it does in deploy/ (migration 00019).
	// A harness that gave it the superuser would pass
	// whatever foyer_radius's grants said.
	radiusPassword := randomToken(t)
	if err := store.SetLoginPassword(migCtx, ownerURL, store.RadiusRole, radiusPassword); err != nil {
		t.Fatalf("setting the FreeRADIUS login's password: %v", err)
	}
	appPassword := randomToken(t)
	if err := store.SetLoginPassword(migCtx, ownerURL, store.DefaultAppRole, appPassword); err != nil {
		t.Fatalf("setting the control plane login's password: %v", err)
	}
	l.appURL = fmt.Sprintf("postgres://%s:%s@127.0.0.1:%s/foyer?sslmode=disable", store.DefaultAppRole, appPassword, pgPort)

	// --- the RADIUS client, as a database row -----------------------------
	//
	// Before FreeRADIUS starts, because `read_clients = yes` loads the client
	// table once at startup. The dynamic-clients lookup would find a row added
	// later, but a harness that depends on the slower of two paths is a harness
	// whose failures are ambiguous.
	//
	// The secret is generated here and exists in exactly two places: this process
	// and one row. `nas.secret` is the one declared cleartext copy of a shared
	// secret in the schema -- FreeRADIUS validates Message-Authenticator itself
	// and cannot use an encrypted one -- and `docs/rules/database.md` records
	// that exception. This uses it; it does not create a second one.
	l.secret = randomToken(t)
	insertNASRow(t, ownerURL, labNASAddress, l.secret)

	// --- the Policy API, as its own OS process ----------------------------
	l.startPolicyAPI(t)

	// --- the generated configuration --------------------------------------
	//
	// Generated by internal/raddb, not written here. A hand-written raddb in this
	// directory would be a second source of truth for the thing under test: the
	// day the generator's failure branch changes, this test has to follow it, and
	// it can only do that by being the generator's output.
	dir, err := os.MkdirTemp("", "foyer-policy-raddb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l.raddbDir = dir

	cfg := raddb.DefaultConfig()
	cfg.PolicyAPIURL = l.policyURL
	cfg.PostgresHost = l.pgName
	cfg.PostgresPort = 5432
	cfg.PostgresUser = store.RadiusRole
	cfg.PostgresPassword = radiusPassword
	cfg.PostgresDB = "foyer"
	cfg.GuestFailureAdvice = advice
	if err := raddb.NewGenerator(cfg).Generate(dir); err != nil {
		t.Fatalf("generating the FreeRADIUS configuration: %v", err)
	}

	// --- FreeRADIUS --------------------------------------------------------
	image := freeradiusImage(t)
	mustRun(t, 60*time.Second, "docker", "run", "-d",
		"--name", l.frName, "--network", l.network,
		"--add-host", "host.docker.internal:host-gateway",
		"-v", dir+":/etc/raddb:ro",
		image, "radiusd", "-f", "-l", "stdout", "-xx")
	t.Cleanup(func() { _, _ = run(t, 60*time.Second, "docker", "rm", "-f", l.frName) })

	waitForRadiusd(t, l.frName)
	assertFreeRADIUSDialect(t, l.frName)
	assertRadiusdIsNotRoot(t, l.frName)
	return l
}

// startPolicyAPI builds and starts the stub as a child process of the test, and
// records the PID that SIGSTOP will land on.
func (l *lab) startPolicyAPI(t *testing.T) {
	t.Helper()
	if l.policyCmd != nil && l.policyCmd.Process != nil {
		// Resuming an existing one.
		if err := l.policyCmd.Process.Signal(syscall.SIGCONT); err != nil {
			t.Fatalf("SIGCONT to the Policy API: %v", err)
		}
		return
	}

	bin := filepath.Join(t.TempDir(), "policystub")
	mustRun(t, 180*time.Second, "go", "build", "-o", bin, "github.com/kiennt048/foyer/lab/policy/policystub")

	args := []string{"-addr", "0.0.0.0:0"}
	if l.storeBacked {
		args = append(args, "-database-url", l.appURL)
	}
	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the Policy API: %v", err)
	}
	t.Cleanup(func() {
		// SIGCONT first: a stopped process cannot handle SIGKILL's cleanup and a
		// test that leaves one behind leaves it forever.
		_ = cmd.Process.Signal(syscall.SIGCONT)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			line <- sc.Text()
		}
		close(line)
	}()

	select {
	case first, ok := <-line:
		if !ok {
			t.Fatal("the Policy API exited before it announced a port")
		}
		// "policystub listening on [::]:54321 pid 1234"
		fields := strings.Fields(first)
		if len(fields) < 3 {
			t.Fatalf("unexpected announcement from the Policy API: %q", first)
		}
		addr := fields[3]
		port := addr[strings.LastIndex(addr, ":")+1:]
		l.policyURL = fmt.Sprintf("http://host.docker.internal:%s/api/v1/aaa/authorize", port)
		l.policyCmd = cmd
		t.Logf("Policy API on %s, pid %d", l.policyURL, cmd.Process.Pid)
	case <-time.After(30 * time.Second):
		t.Fatal("the Policy API never announced a port")
	}
}

// stopPolicyAPI is the whole point of this file.
func (l *lab) stopPolicyAPI(t *testing.T) {
	t.Helper()
	if err := l.policyCmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP to the Policy API: %v", err)
	}
	t.Logf("SIGSTOP sent to pid %d: the listening socket is still open and nothing will answer on it",
		l.policyCmd.Process.Pid)
}

func publishedPort(t *testing.T, container, port string) string {
	t.Helper()
	out := mustRun(t, 30*time.Second, "docker", "port", container, port)
	// "127.0.0.1:54321"
	first := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	return first[strings.LastIndex(first, ":")+1:]
}

// waitForPostgres waits until a connection through the PUBLISHED PORT succeeds.
//
// **Not `pg_isready` inside the container: the two disagree.** The PostgreSQL image runs a temporary server on a unix socket
// while its entrypoint scripts run; `pg_isready` defaults to that socket, answers
// yes to the temporary server, and the temporary server then stops and the real one
// starts. A migration started on its word is reset by a peer that is about to
// restart — measured here as an intermittent `failed to receive message: unexpected
// EOF` on the first connection, which `go test -race ./...` makes easy to hit and
// this package alone rarely does.
//
// The probe is the transport the caller uses, which is the only thing that can
// answer the question `store.Migrate` is about to ask.
// `internal/store/nassync_lab_test.go` makes the same choice.
func waitForPostgres(t *testing.T, container, dsn string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			pool.Close()
		}
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			logs, _ := run(t, 20*time.Second, "docker", "logs", "--tail", "40", container)
			t.Fatalf("PostgreSQL never became reachable through the published port (%v):\n%s", err, logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func waitForRadiusd(t *testing.T, container string) {
	t.Helper()
	deadline := time.Now().Add(radiusdReadyTimeout)
	for {
		logs, _ := run(t, 20*time.Second, "docker", "logs", "--tail", "200", container)
		if strings.Contains(logs, "Ready to process requests") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("FreeRADIUS never became ready. Its own output is the finding here:\n%s", logs)
		}
		time.Sleep(time.Second)
	}
}

// insertNASRow registers this harness's RADIUS client the way the product does:
// a plain database write, not a line in a generated file.
//
// `docs/rules/radius.md` is explicit about why that is the shape --
// "adding an access point must be a plain database write", because regenerate-and-
// restart drops every in-flight EAP conversation and the in-memory TLS session
// cache on the site. So the harness registers a client the same way an operator
// would, and gets the same code path.
//
// The column list is not free-form. rlm_sql's client_query reads
// `id, nasname, shortname, type, secret, server` positionally; `server` stays NULL
// so the client lands in the default virtual server.
func insertNASRow(t *testing.T, ownerURL, address, secret string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatalf("connecting to the harness database: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		INSERT INTO nas (nasname, shortname, type, secret,
		                 require_message_authenticator, limit_proxy_state)
		VALUES ($1, 'lab-policy', 'other', $2, true, true)`,
		address, secret)
	if err != nil {
		t.Fatalf("registering the harness RADIUS client: %v", err)
	}
}

// freeradiusImage builds lab/Dockerfile.freeradius if the tag is not already
// present. The Dockerfile is not this test's to change; it is read, never written.
//
// **The cached tag is checked for its version, not merely for existing**, and
// that is not caution. A tag built from an older `lab/Dockerfile.freeradius` —
// alpine's package rather than debian:sid's — still satisfies `docker image
// inspect`, and a harness that trusted it would run against **FreeRADIUS
// 3.0.27**: a different major dialect from the 3.2.x unlang `internal/raddb`
// generates. Every measurement in `lab/policy/README.md` taken on such a machine
// would be about the wrong server.
//
// The dialects are close enough that the generated configuration starts and
// answers, so nothing else would notice. A cache makes a version pin in
// a Dockerfile advisory, and `docs/rules/testing.md` says the general form:
// **a green run is somebody else's tree until proven otherwise.**
func freeradiusImage(t *testing.T) string {
	t.Helper()
	if tag := os.Getenv("FOYER_LAB_FR_IMAGE"); tag != "" {
		return tag
	}
	// Any cached image that is the right dialect will do, and this harness's own
	// tag is only the first candidate. A second stale tag is
	// then not a rebuild, and a rebuild is not free: it needs a base image the
	// daemon may have to pull.
	//
	// `docker image inspect` FIRST, and never `docker run` on a tag that is not
	// present locally: `docker run` on an absent tag tries to pull it, and a
	// daemon whose registry path is unreachable **hangs there without erroring**.
	for _, tag := range []string{
		"foyer-lab-freeradius:policy-harness",
		"foyer-lab-freeradius:test",
		"foyer-nas-sync-freeradius:test",
	} {
		if _, err := run(t, 20*time.Second, "docker", "image", "inspect", tag); err != nil {
			continue
		}
		out, err := run(t, 40*time.Second, "docker", "run", "--rm", "--entrypoint", "radiusd", tag, "-v")
		if err != nil {
			continue
		}
		if strings.Contains(out, "FreeRADIUS Version 3.2.") {
			t.Logf("MEASURED against: %s", strings.SplitN(out, "\n", 2)[0])
			return tag
		}
		t.Logf("cached image %s is %s — skipping it, because the generated tree is 3.2.x unlang",
			tag, strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]))
	}

	const tag = "foyer-lab-freeradius:policy-harness"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", tag, "-f", "../Dockerfile.freeradius", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the FreeRADIUS image from lab/Dockerfile.freeradius: %v\n%s", err, out)
	}
	return tag
}

// assertFreeRADIUSDialect is the backstop for the above, and it is separate on
// purpose: freeradiusImage chooses, this one refuses. An image passed in by hand
// or by an environment variable never goes through the chooser.
func assertFreeRADIUSDialect(t *testing.T, container string) {
	t.Helper()
	out := mustRun(t, 30*time.Second, "docker", "exec", container, "radiusd", "-v")
	line := strings.SplitN(out, "\n", 2)[0]
	if !strings.Contains(line, "FreeRADIUS Version 3.2.") {
		t.Fatalf("this harness is running against the wrong FreeRADIUS dialect, so every measurement it makes is "+
			"about a different server. The generated tree is 3.2.x unlang and the range is >= 3.2.9, < 3.3.\n"+
			"radiusd -v said: %s", line)
	}
}
