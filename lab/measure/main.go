package main

// lab/measure — a host that serves the guest-facing surface to a phone on the LAN.
//
// It exists for two measurements that need a person holding a device and cannot
// be taken any other way:
//
//   - the HTTPS question: does the iOS Captive Network Assistant display an
//     HTTPS page whose certificate it does not trust, and does it fetch the
//     RFC 8908 endpoint over that connection at all
//   - the device walk: the guest flow on a real phone, end to end, including
//     the on-device unknowns docs/protocol-reality.md lists as blank
//
// It takes no measurement itself. It removes every excuse for not taking one:
// a certificate iOS cannot refuse for the wrong reason, both schemes served at
// once so the fallback branch is measured beside the main one, and a request log
// that tells "the phone never asked" apart from "the phone asked and refused the
// answer". Those two produce the same blank screen and mean opposite things.
//
//	go run ./lab/measure
//	go run ./lab/measure -dir ../web/portal/dist -http :80
//
// Standard library only: a measurement tool adds no dependency to go.mod.

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func main() {
	httpAddr := flag.String("http", ":8080", "listen address for the plain-HTTP listener")
	httpsAddr := flag.String("https", ":8443", "listen address for the HTTPS listener")
	dir := flag.String("dir", "", "a directory to serve under /site/ (for example web/portal/dist)")
	proxyTo := flag.String("proxy", "", "reverse-proxy anything unmatched to this origin (for example http://localhost:5171)")
	stateDir := flag.String("state", defaultStateDir(), "where the generated certificate and key are kept")
	advertise := flag.String("host", "", "the LAN address to advertise, if this machine has more than one")
	quiet := flag.Bool("quiet", false, "do not log bare TCP connections, only TLS and HTTP")
	probeDir := flag.String("probe", "", "the portal build's measurement output, served at /{lang}/probe/ (web/portal/dist-probe)")
	flag.Parse()

	if err := run(*httpAddr, *httpsAddr, *dir, *probeDir, *proxyTo, *stateDir, *advertise, *quiet); err != nil {
		fmt.Fprintf(os.Stderr, "measure: %v\n", err)
		os.Exit(1)
	}
}

func run(httpAddr, httpsAddr, dir, probeDir, proxyTo, stateDir, advertise string, quiet bool) error {
	lan, err := lanAddresses()
	if err != nil {
		return err
	}
	primary := lan[0]
	if advertise != "" {
		ip := net.ParseIP(advertise)
		if ip == nil {
			return fmt.Errorf("-host %q is not an IP address; the phone has no name for this machine", advertise)
		}
		primary = ip
		lan = append([]net.IP{ip}, lan...)
	}

	// Loopback goes in the SAN so the same certificate works when the page is
	// opened on this machine, but it is never what gets advertised: a URL the
	// phone cannot reach is the most expensive kind of typo at an access point.
	sanIPs := append(append([]net.IP{}, lan...), net.ParseIP("127.0.0.1"), net.ParseIP("::1"))

	tlsm, err := ensureTLS(stateDir, sanIPs, []string{"localhost"})
	if err != nil {
		return err
	}

	elog := newEventLog(1000, os.Stdout, quiet)

	ttf := buildProbeTTF()
	srv := &server{
		log:       elog,
		tlsm:      tlsm,
		captive:   true,
		staticDir: dir,
		probeDir:  probeDir,
		httpHost:  net.JoinHostPort(primary.String(), portOf(httpAddr)),
		httpsHost: net.JoinHostPort(primary.String(), portOf(httpsAddr)),
		ttf:       ttf,
		woff:      wrapWOFF(ttf),
	}
	if proxyTo != "" {
		p, err := newProxy(proxyTo, elog)
		if err != nil {
			return fmt.Errorf("-proxy: %w", err)
		}
		srv.proxy = p
		srv.proxyName = proxyTo
	}

	// A named directory with nothing in it is the one -probe failure worth
	// catching before a phone is in range. The banner below lists a URL to type;
	// if that URL is a 404 the measurement spends ten minutes on a missing build step
	// before anybody reads the log.
	if probeDir != "" {
		for _, lang := range portalProbeLangs {
			if _, err := os.Stat(filepath.Join(probeDir, lang, "probe", "index.html")); err != nil {
				return fmt.Errorf("-probe %s: no %s/probe/index.html. Build it with: pnpm -F @foyer/portal build",
					probeDir, lang)
			}
		}
	}

	handler := srv.handler()
	errorLog := log.New(elog, "", 0)

	plain := &http.Server{
		Addr:              httpAddr,
		Handler:           handler,
		ErrorLog:          errorLog,
		ReadHeaderTimeout: 10 * time.Second,
		ConnState:         connStateLogger(elog, "http"),
	}
	secure := &http.Server{
		Addr:              httpsAddr,
		Handler:           handler,
		ErrorLog:          errorLog,
		ReadHeaderTimeout: 10 * time.Second,
		ConnState:         connStateLogger(elog, "tls"),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsm.server},
			MinVersion:   tls.VersionTLS12,
			// GetConfigForClient fires on the ClientHello, before any certificate
			// is offered. It is the only record that survives a client which
			// starts a handshake and abandons it over the certificate — which is
			// exactly the outcome the HTTPS question has to be able to name.
			GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				elog.add(event{Kind: "hello", Peer: h.Conn.RemoteAddr().String(), Detail: describeHello(h)})
				return nil, nil
			},
		},
	}

	// Both listeners are bound before anything is printed. A banner full of
	// addresses followed by a failure to bind port 8080 is precisely the shape of
	// instruction nobody should be reading while holding a phone.
	plainLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("cannot bind %s for HTTP: %w — pass -http with a free port", httpAddr, err)
	}
	secureLn, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		_ = plainLn.Close()
		return fmt.Errorf("cannot bind %s for HTTPS: %w — pass -https with a free port", httpsAddr, err)
	}

	printBanner(srv, tlsm, lan, stateDir, dir, probeDir, proxyTo)

	errs := make(chan error, 2)
	go func() {
		if err := plain.Serve(plainLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("http listener: %w", err)
		}
	}()
	go func() {
		if err := secure.ServeTLS(secureLn, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("https listener: %w", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		return err
	case <-stop:
		fmt.Println("\n  stopping. The log above is the measurement; keep it.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = plain.Shutdown(ctx)
	_ = secure.Shutdown(ctx)
	return nil
}

func connStateLogger(elog *eventLog, kind string) func(net.Conn, http.ConnState) {
	return func(c net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		elog.add(event{Kind: "conn", Peer: c.RemoteAddr().String(), Detail: "accepted on the " + kind + " listener"})
	}
}

// lanAddresses returns the machine's routable IPv4 addresses, most-likely first.
// A machine with none of them cannot serve a phone, and saying so at startup is
// far cheaper than discovering it while standing under an access point.
//
// The ordering is not cosmetic. A laptop can offer a VPN address in
// 100.64.0.0/10 ahead of its Wi-Fi address in 192.168.0.0/16, because the VPN
// interface sorts first — and 100.64.0.0/10 is carrier-grade NAT, which a phone
// standing next to the access point cannot reach. Advertising it would send the
// measurement chasing a firewall that is not there. Private LAN ranges rank
// first, and every candidate is printed so -host can override.
func lanAddresses() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("this machine has no routable IPv4 address, so no phone can reach it. Join the same network as the access point first")
	}
	sort.SliceStable(out, func(i, j int) bool { return reachabilityRank(out[i]) < reachabilityRank(out[j]) })
	return out, nil
}

// reachabilityRank orders an address by how likely a phone on the same Wi-Fi is
// to reach it. Lower is better.
func reachabilityRank(ip net.IP) int {
	switch {
	case ip[0] == 192 && ip[1] == 168:
		return 0
	case ip[0] == 10:
		return 1
	case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
		return 2
	case ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127:
		return 4 // carrier-grade NAT: nearly always a VPN, and never the local Wi-Fi
	default:
		return 3
	}
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.TrimPrefix(addr, ":")
	}
	return port
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "foyer-measure")
	}
	return filepath.Join(home, ".foyer", "measure")
}

func printBanner(srv *server, tlsm *tlsMaterial, lan []net.IP, stateDir, dir, probeDir, proxyTo string) {
	line := strings.Repeat("─", 58)
	fmt.Printf("\n  ┌%s┐\n", line)
	fmt.Printf("  │ %-56s │\n", "Type one of these on the phone")
	fmt.Printf("  │ %-56s │\n", "")
	fmt.Printf("  │   %-54s │\n", "http://"+srv.httpHost+"/probe")
	fmt.Printf("  │   %-54s │\n", "https://"+srv.httpsHost+"/probe")
	if probeDir != "" {
		fmt.Printf("  │ %-56s │\n", "")
		fmt.Printf("  │   %-54s │\n", "http://"+srv.httpHost+"/en/probe/   the portal's own page")
		fmt.Printf("  │   %-54s │\n", "https://"+srv.httpsHost+"/en/probe/  and /vi/probe/")
		fmt.Printf("  │ %-56s │\n", "      its values land in this log, not in a photograph")
	}
	fmt.Printf("  └%s┘\n\n", line)

	fmt.Println("  There is no QR code. A QR encoder written here could not be verified")
	fmt.Println("  without a camera, and an unverifiable QR code costs the first ten")
	fmt.Println("  minutes of a measurement that is hard to repeat.")
	fmt.Println()

	fmt.Printf("  RFC 8908 endpoint     https://%s/captive-portal/api\n", srv.httpsHost)
	fmt.Printf("  DHCP option 114       %s\n", "https://"+srv.httpsHost+"/captive-portal/api")
	fmt.Printf("  root certificate      http://%s/ca.crt   (the install-it branch)\n", srv.httpHost)
	fmt.Printf("  request log           http://%s/log\n", srv.httpHost)
	fmt.Println()

	fmt.Printf("  certificate           %s .. %s  (%.0f days)\n",
		tlsm.leaf.NotBefore.Format("2006-01-02"), tlsm.leaf.NotAfter.Format("2006-01-02"),
		tlsm.leaf.NotAfter.Sub(tlsm.leaf.NotBefore).Hours()/24)
	fmt.Printf("  IP SAN                %v\n", tlsm.leaf.IPAddresses)
	if tlsm.regenerated {
		fmt.Printf("  REGENERATED           %s\n", tlsm.reason)
		fmt.Println("                        A phone that installed the previous root must install this one.")
	}
	fmt.Printf("  state                 %s\n", stateDir)
	if len(lan) > 1 {
		others := make([]string, 0, len(lan)-1)
		for _, ip := range lan[1:] {
			others = append(others, ip.String())
		}
		fmt.Printf("  other addresses       %s\n", strings.Join(others, "  "))
		fmt.Println("                        If the phone cannot reach the one above, pass -host with one of these.")
	}
	if dir != "" {
		fmt.Printf("  serving               %s under /site/\n", dir)
	}
	if proxyTo != "" {
		fmt.Printf("  proxying              anything unmatched to %s\n", proxyTo)
	}
	fmt.Println()
	fmt.Println("  Log kinds, and the distinction the whole thing is for:")
	fmt.Println("    conn       a TCP connection arrived")
	fmt.Println("    hello      a TLS ClientHello arrived")
	fmt.Println("    tls-error  the handshake failed, with the client's own alert")
	fmt.Println("    http       a request completed, with its User-Agent")
	fmt.Println()
	fmt.Println("  hello with no http after it  = the client refused the certificate.")
	fmt.Println("  no hello at all              = the client never asked. Different answers.")
	fmt.Println()
	fmt.Println("  Procedure: lab/measure/README.md")
	fmt.Println(strings.Repeat("─", 62))
	fmt.Println()
}
