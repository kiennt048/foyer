package main

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAListenerHoldsNoMoreThanItsBound(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := limitListener(raw, 2)
	defer ln.Close()

	accepted := make(chan net.Conn, 3)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	var clients []net.Conn
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", raw.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		clients = append(clients, c)
	}
	var held []net.Conn
	for i := 0; i < 2; i++ {
		select {
		case c := <-accepted:
			held = append(held, c)
		case <-time.After(2 * time.Second):
			t.Fatalf("connection %d inside the bound was not accepted", i+1)
		}
	}
	select {
	case <-accepted:
		t.Fatal("a third connection was accepted with two held and a bound of two")
	case <-time.After(200 * time.Millisecond):
	}
	held[0].Close()
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("closing a held connection did not let the waiting one in")
	}
	held[1].Close()
}

// TestEveryListenerIsBounded: the bound is only real where the listeners are
// built. A namedServer with no bound would serve the unbounded listener.
func TestEveryListenerIsBounded(t *testing.T) {
	srv, svc, _ := portalFor(t)
	rt, err := newPortalRuntime(testConfig(), srv, svc, testObservability(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*namedServer{
		adminListener(testConfig(), srv, testObservability(t)),
		policyListener(testConfig(), nil, testObservability(t)),
		rt.plainListener(),
		rt.tlsListener(),
	} {
		if s.maxConns <= 0 {
			t.Errorf("%s has no connection bound", s.name)
		}
	}
	// The run loop serves through serve(), which refuses to start unbounded.
	s := &namedServer{name: "unbounded", srv: &http.Server{Addr: "127.0.0.1:0"}}
	if err := s.serve(); err == nil || !strings.Contains(err.Error(), "connection bound") {
		t.Fatalf("an unbounded listener started: %v", err)
	}
}
