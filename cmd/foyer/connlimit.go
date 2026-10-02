package main

import (
	"net"
	"sync"
)

// Listener-level bound on concurrent connections.
//
// `internal/api` bounds one request's body and, for signed-in bodies over the
// anonymous ceiling, how many bytes are decoded at once. Without a bound on how many
// connections a listener holds, thousands of synchronised 16 KiB anonymous
// bodies — each ~640 KiB of heap once decoded — or simply thousands of idle TLS
// connections were a way to spend the memory of the process that answers
// FreeRADIUS. `golang.org/x/net/netutil.LimitListener` is the standard answer and
// the shape here (go.mod is frozen, and it is thirty lines): `Accept` blocks at the
// bound, so the kernel's backlog queues the excess rather than the process holding
// it, and every connection held is already bounded in time by the listener's
// Read/Write timeouts. nginx's `worker_connections` is the same idea one layer out.
//
// **Not console settings.** An operator has no way to know the right figure and no
// symptom that would lead them to it; the figures follow from the container's
// memory, which the deployment sets. Sized so the bound is never the first thing a
// real venue meets:
//
//   - the guest portal, each listener: 1024 — every device in radio range, most
//     of them a CNA probe that is gone in a second. 1024 × ~640 KiB worst case is
//     the ceiling that keeps a flood under the container's limit.
//   - the console: 256 — a handful of operators' browsers, six connections each.
//   - the Policy API: 1024 — FreeRADIUS's `rlm_rest` pool is bounded by
//     `max_servers` (at most 256) already; this only stops something else that
//     reached the port.
const (
	portalMaxConns = 1024
	adminMaxConns  = 256
	policyMaxConns = 1024
)

// limitListener returns a listener that holds at most n connections open at once.
func limitListener(l net.Listener, n int) net.Listener {
	return &limitedListener{Listener: l, sem: make(chan struct{}, n), done: make(chan struct{})}
}

type limitedListener struct {
	net.Listener
	sem       chan struct{}
	closeOnce sync.Once
	done      chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

func (l *limitedListener) Close() error {
	err := l.Listener.Close()
	l.closeOnce.Do(func() { close(l.done) })
	return err
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
