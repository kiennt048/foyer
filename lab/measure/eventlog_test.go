package main

import (
	"strings"
	"testing"
)

// The distinction this test pins is the most valuable thing in the kit.
//
// The HTTPS question's likely failure is a blank screen. "The phone never asked" and "the phone
// asked and refused the answer" produce the same blank screen and point at
// opposite conclusions: the first says something about the network, the second
// says the iOS CNA will not display a self-signed page and the on-prem
// deployment shape needs its fallback branch. Only the log tells them apart, and
// only if a failed TLS handshake is captured rather than printed and lost.
func TestARefusedHandshakeIsRecordedAsSuch(t *testing.T) {
	l := newEventLog(10, nil, false)

	// Verbatim from net/http: this is the exact line the server writes to its
	// ErrorLog when a client aborts the handshake over the certificate.
	_, _ = l.Write([]byte("http: TLS handshake error from 192.0.2.31:51001: remote error: tls: unknown certificate authority\n"))

	events := l.events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.Kind != "tls-error" {
		t.Fatalf("kind is %q, want tls-error — a refusal that reads as a generic server message is one nobody finds in the log", e.Kind)
	}
	if e.Peer != "192.0.2.31:51001" {
		t.Fatalf("peer is %q; the address is what ties the refusal to the phone", e.Peer)
	}
	if !strings.Contains(e.Detail, "unknown certificate authority") {
		t.Fatalf("detail is %q; the client's own alert is the finding", e.Detail)
	}
}

func TestOtherServerMessagesAreNotMisreadAsRefusals(t *testing.T) {
	l := newEventLog(10, nil, false)
	_, _ = l.Write([]byte("http: superfluous response.WriteHeader call\n"))

	if got := l.events()[0].Kind; got != "server" {
		t.Fatalf("kind is %q, want server: labelling every server message a TLS refusal would answer the HTTPS question wrongly and confidently", got)
	}
}

func TestNewestFirstAndBounded(t *testing.T) {
	l := newEventLog(3, nil, false)
	for _, d := range []string{"one", "two", "three", "four"} {
		l.add(event{Kind: "http", Detail: d})
	}
	events := l.events()
	if len(events) != 3 {
		t.Fatalf("kept %d events, want the last 3", len(events))
	}
	if events[0].Detail != "four" {
		t.Fatalf("first event is %q; a log read while standing up is read newest first", events[0].Detail)
	}
}

func TestQuietDropsOnlyConnections(t *testing.T) {
	l := newEventLog(10, nil, true)
	l.add(event{Kind: "conn", Detail: "accepted"})
	l.add(event{Kind: "hello", Detail: "a ClientHello"})

	events := l.events()
	if len(events) != 1 || events[0].Kind != "hello" {
		t.Fatalf("quiet mode must drop bare connections and keep everything else, got %+v", events)
	}
}
