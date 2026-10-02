package main

// The log is the other half of the instrument.
//
// The likely failure mode of the HTTPS question is a blank screen with no explanation, and a
// blank screen is produced by at least three different things: the phone never
// reached this host, the phone opened a TLS connection and abandoned it over the
// certificate, or the phone fetched the page and refused to render it. Those
// three point at different answers, and only one of them says anything about
// self-signed certificates.
//
// So three kinds of event are recorded, and the difference between them is the
// whole point:
//
//	conn      a TCP connection was accepted — the phone reached this host
//	hello     a TLS ClientHello arrived — the phone started a TLS handshake
//	tls-error the handshake failed, with the alert the client sent
//	http      a request completed, with its User-Agent
//
// "No hello at all" and "hello followed by tls-error: unknown ca" are the two
// answers the measurement has to tell apart, and no amount of looking at the phone does it.

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type event struct {
	When   time.Time
	Kind   string
	Peer   string
	Detail string
	UA     string
}

type eventLog struct {
	mu    sync.Mutex
	buf   []event
	max   int
	out   io.Writer
	quiet bool // suppress "conn" events, which keep-alives make noisy
}

func newEventLog(max int, out io.Writer, quiet bool) *eventLog {
	return &eventLog{max: max, out: out, quiet: quiet}
}

func (l *eventLog) add(e event) {
	if e.When.IsZero() {
		e.When = time.Now()
	}
	if l.quiet && e.Kind == "conn" {
		return
	}
	l.mu.Lock()
	l.buf = append(l.buf, e)
	if len(l.buf) > l.max {
		l.buf = l.buf[len(l.buf)-l.max:]
	}
	line := format(e)
	out := l.out
	l.mu.Unlock()

	if out != nil {
		fmt.Fprintln(out, line)
	}
}

// events returns the buffer newest first, which is the order somebody reads a
// log in when they are standing up holding a phone.
func (l *eventLog) events() []event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]event, len(l.buf))
	for i, e := range l.buf {
		out[len(l.buf)-1-i] = e
	}
	return out
}

func format(e event) string {
	line := fmt.Sprintf("%s  %-9s %-21s %s", e.When.Format("15:04:05.000"), e.Kind, e.Peer, e.Detail)
	if e.UA != "" {
		line += fmt.Sprintf("  ua=%q", e.UA)
	}
	return line
}

// Write makes the log usable as http.Server.ErrorLog's sink. That is the only
// place Go reports a failed TLS handshake, and a failed handshake is precisely
// what the measurement is trying to observe — so it is captured rather than printed and lost.
func (l *eventLog) Write(p []byte) (int, error) {
	text := strings.TrimSpace(string(p))
	kind, peer, detail := "server", "", text

	if rest, ok := strings.CutPrefix(text, "http: TLS handshake error from "); ok {
		kind = "tls-error"
		if i := strings.Index(rest, ": "); i >= 0 {
			peer, detail = rest[:i], rest[i+2:]
		} else {
			detail = rest
		}
	}
	l.add(event{Kind: kind, Peer: peer, Detail: detail})
	return len(p), nil
}
