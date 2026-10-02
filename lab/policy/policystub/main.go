// Command policystub is the Policy API as a standalone process, for the one test
// that has to stop it.
//
// It exists because `cmd/foyer` refuses to start without a database (correctly --
// a control plane without its store is not a degraded control plane, it is an open
// one), and because SIGSTOP has to land on a process whose *only* job is answering
// `rlm_rest`. Stopping the whole backend would also stop the portal and the admin
// API, and the resulting FreeRADIUS behaviour would be evidence about a different
// thing.
//
// What it runs is the real `internal/aaa` engine, the real handler and the real
// bounds. What it does not run by default is PostgreSQL, and that is deliberate:
// the subject of lab/policy is "the Go process is wedged", and a wedged process
// looks the same to FreeRADIUS whatever it would have queried.
//
// `-database-url` is the exception, and it exists for one question the fixtures
// cannot answer: whether an account and a voucher that live in the REAL schema are
// authenticated correctly end to end. With it the stub
// serves the same engine over `internal/store` -- the store `cmd/foyer` wires --
// and FreeRADIUS reads the password verifier from the same database through
// rlm_sql, which is the whole point: the claim under test is that the two halves
// agree about which identity has to prove what.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/kiennt048/foyer/internal/aaa"
	"github.com/kiennt048/foyer/internal/store"
)

// LabVoucher is the code the harness authenticates with. It is a lab fixture and
// carries no secret: the shared secret and the database password in this stack are
// generated per run by the test that starts it.
const LabVoucher = "LAB-VOUCHER-OK"

// RevokedVoucher exists so the harness can tell an authoritative denial from a
// failure. Both produce Access-Reject on the wire; only one of them is supposed to
// keep producing it while the Policy API is stopped.
const RevokedVoucher = "LAB-VOUCHER-REVOKED"

type fixtureStore struct{}

func (fixtureStore) LookupVoucher(_ context.Context, code string) (*aaa.VoucherInfo, error) {
	switch code {
	case LabVoucher:
		return &aaa.VoucherInfo{Code: code, State: "unused", DurationMinutes: 60}, nil
	case RevokedVoucher:
		return &aaa.VoucherInfo{Code: code, State: "revoked"}, nil
	default:
		return nil, aaa.ErrIdentityNotFound
	}
}

// ClaimVoucher never consumes the fixture: the SIGSTOP harness authorizes
// LabVoucher again and again, and its subject is the failure model rather than
// single use. Single use is asserted against the real store in credentials_test.go.
func (fixtureStore) ClaimVoucher(_ context.Context, _, code string, _ time.Time) (*aaa.VoucherInfo, error) {
	if code != LabVoucher {
		return nil, aaa.ErrIdentityNotFound
	}
	return &aaa.VoucherInfo{Code: code, State: "in-use", DurationMinutes: 60}, nil
}

// RADIUSClientSite places every client at one venue. The fixtures carry no site
// (LookupVoucher leaves SiteID empty), and site binding is asserted against the
// real store — internal/store's TestRADIUSVoucherIsClaimedOnlyAtItsOwnSite and
// credentials_test.go — not against this harness, whose subject is the failure
// model.
func (fixtureStore) RADIUSClientSite(context.Context, string) (string, error) {
	return "lab-site", nil
}

func (fixtureStore) LookupGuestAccount(context.Context, string) (*aaa.GuestAccountInfo, error) {
	return nil, aaa.ErrIdentityNotFound
}

func (fixtureStore) IsMACAllowed(context.Context, string) (bool, error) { return true, nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	route := flag.String("route", "/api/v1/aaa/authorize", "authorize route")
	dsn := flag.String("database-url", "", "serve the real internal/store instead of the fixtures")
	flag.Parse()

	var ps aaa.PolicyStore = fixtureStore{}
	if *dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		st, err := store.NewStore(ctx, *dsn)
		cancel()
		if err != nil {
			log.Fatalf("policystub: opening the store: %v", err)
		}
		defer st.Close()
		ps = st
	}
	engine := aaa.NewEngine(ps, 300)

	mux := http.NewServeMux()
	mux.Handle("POST "+*route, engine.Handler(aaa.HandlerOptions{}))
	// A liveness route on the same listener as the thing it reports on, because a
	// health check on a different listener answers happily from a stopped process
	// -- no, it does not, and that is the point: SIGSTOP stops every thread, so
	// this route goes quiet with the authorize route. Anything that stays up while
	// the Policy API is wedged is not checking the Policy API.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("policystub: %v", err)
	}
	// The harness reads this line to learn the port and to know the process is
	// serving before it starts asserting.
	fmt.Printf("policystub listening on %s pid %d\n", ln.Addr().String(), os.Getpid())
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("policystub: %v", err)
	}
}
