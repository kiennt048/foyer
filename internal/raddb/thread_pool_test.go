package raddb

import (
	"fmt"
	"strings"
	"testing"
)

// The worker pool, and the two connection pools that have to be sized against it.
//
// ── why this is one test and not three ───────────────────────────────────────
//
// `docs/rules/security.md` §2 says what exhausting FreeRADIUS's **fixed**
// thread pool costs: *"Status-Server is blocked too, so the health check dies
// with the thing it checks."* A `radiusd.conf.tmpl` that emits no
// `thread pool { }` section at all leaves the number at FreeRADIUS's built-in
// default, where nothing in `deploy/` can reach it.
//
// But a reachable `max_servers` on its own moves the failure rather than fixing
// it. A worker that has to wait for an `rlm_sql` or `rlm_rest` connection is a
// worker not answering Status-Server, exactly as if the thread pool were full —
// so if either connection pool is smaller than the worker count, the pool becomes
// the queue and the number an operator set is not the number in force.
//
// One field drives all three. This asserts that it does, by changing it and
// requiring all three to move: three literals that happen to be equal today are
// a coincidence a later edit breaks silently.
func TestTheWorkerCountReachesEveryPoolSizedAgainstIt(t *testing.T) {
	const unusual = 17
	dir := generate(t, func(c *Config) { c.MaxServers = unusual })

	radiusd := configOnly(readGenerated(t, dir, "radiusd.conf"))
	pool := sectionBody(t, radiusd, "thread pool")
	if !strings.Contains(pool, fmt.Sprintf("max_servers = %d", unusual)) {
		t.Errorf("radiusd.conf does not carry the configured worker count:\n%s", pool)
	}

	for _, mod := range []struct{ file, why string }{
		{"sql", "a worker waiting for a database connection is a worker not answering Status-Server"},
		{"rest", "a worker waiting for a Policy API connection is the §2 failure exactly"},
	} {
		body := sectionBody(t, configOnly(readGenerated(t, dir, "mods-available", mod.file)), "pool")
		if !strings.Contains(body, fmt.Sprintf("max = %d", unusual)) {
			t.Errorf("the %s module's connection pool is not sized against the worker count — %s:\n%s",
				mod.file, mod.why, body)
		}
	}
}

// TestTheThreadPoolNumbersAreConsistent.
//
// FreeRADIUS does not check these against each other and starts with a pool it
// cannot honour. The generator does, because a `start_servers` above
// `max_servers` is a configuration nobody meant and nothing reports.
func TestTheThreadPoolNumbersAreConsistent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "no workers at all",
			mutate: func(c *Config) { c.MaxServers = 0; c.StartServers = 1 },
			want:   "MaxServers",
		},
		{
			name:   "more workers at startup than the ceiling",
			mutate: func(c *Config) { c.StartServers = 40 },
			want:   "StartServers",
		},
		{
			name:   "more spare workers than workers",
			mutate: func(c *Config) { c.MaxSpareServers = 90 },
			want:   "MaxSpareServers",
		},
		{
			name:   "a minimum spare above the maximum spare",
			mutate: func(c *Config) { c.MinSpareServers = 20; c.MaxSpareServers = 10 },
			want:   "MinSpareServers",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PostgresPassword = "pg-password-for-this-test-only"
			tc.mutate(&cfg)
			err := NewGenerator(cfg).Validate()
			if err == nil {
				t.Fatal("a thread pool that cannot be honoured was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal was %q, which does not name %s", err, tc.want)
			}
		})
	}
}
