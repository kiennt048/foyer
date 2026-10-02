package raddb

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// isSingleAddress reports whether a client identifier is one address rather
// than a range. radius_clients.address is an inet and may legitimately be a
// range covering a fleet — Aruba's dynamic-radius-proxy shape — and a range
// cannot be discovered, because the lookup matches one source address.
func isSingleAddress(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

// inDynamicNetwork reports whether an address would be discoverable from the
// client table without a restart.
func inDynamicNetwork(cfg Config, addr string) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	for _, n := range cfg.DynamicClientNetworks {
		p, err := netip.ParsePrefix(n.CIDR)
		if err != nil {
			continue
		}
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Gate is one step of the blue/green procedure.
type Gate struct {
	Number int
	Name   string
}

// Gates is the procedure, in order. It is exported because the lab runs the
// same four steps from a shell script where radiusd lives, and a test asserts
// the two agree rather than trusting that they do.
//
// Gate 0 is a precondition rather than a gate: it decides whether the rest of
// the procedure is even meaningful, because a server outside the dialect range
// may accept a configuration whose meaning has changed.
var Gates = []Gate{
	{0, "dialect"},
	{1, "config-check"},
	{2, "second-server"},
	{3, "smoke"},
	{4, "cutover"},
}

// GateResult is what one gate did.
type GateResult struct {
	Gate    Gate
	OK      bool
	Detail  string
	Err     error
	Elapsed time.Duration
}

// Report is the whole run.
type Report struct {
	Version Version
	Results []GateResult
	// RolledBack is true when gate 4 put the previous tree back because the
	// live server did not answer after the swap.
	RolledBack bool
}

// Failed returns the first failing gate, or nil.
func (r Report) Failed() *GateResult {
	for i := range r.Results {
		if !r.Results[i].OK {
			return &r.Results[i]
		}
	}
	return nil
}

// Process is a running radiusd.
type Process interface {
	Stop(ctx context.Context) error
}

// Runner executes commands. The Applier never touches os/exec directly, which
// is what lets the four gates be tested for order, for abort-on-failure and for
// rollback without a FreeRADIUS install.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	Start(ctx context.Context, name string, args ...string) (Process, error)
}

// Target is where a probe sends packets.
type Target struct {
	Host     string
	AuthPort int
	AcctPort int
	Secret   string
}

// Probe is one case in the gate-3 suite.
//
// The suite has to contain both kinds. radclient and radtest do not speak EAP
// at all, so a project whose whole architectural argument is "EAP is too hard
// to implement ourselves" cannot prove anything about EAP with them.
type Probe struct {
	Name  string
	Build func(t Target) (name string, args []string)
}

// RadclientProbe sends one request file with radclient.
func RadclientProbe(name, radclientPath, requestFile, packetType string) Probe {
	return Probe{
		Name: name,
		Build: func(t Target) (string, []string) {
			return radclientPath, []string{
				"-x", "-t", "3", "-r", "1",
				"-f", requestFile,
				fmt.Sprintf("%s:%d", t.Host, t.AuthPort),
				packetType,
				t.Secret,
			}
		},
	}
}

// EapolTestProbe runs one eapol_test configuration.
//
// eapol_test links an EAP peer and a RADIUS client into one binary precisely so
// that EAP methods can be tested without an access point, and it is built to be
// scripted. It is the only EAP coverage that does not need hardware.
func EapolTestProbe(name, eapolTestPath, confFile string) Probe {
	return Probe{
		Name: name,
		Build: func(t Target) (string, []string) {
			return eapolTestPath, []string{
				"-c", confFile,
				"-a", t.Host,
				"-p", strconv.Itoa(t.AuthPort),
				"-s", t.Secret,
				"-t", "10",
			}
		},
	}
}

// Applier runs the blue/green procedure.
type Applier struct {
	Runner Runner

	// Root contains raddb.blue, raddb.green, raddb.next and the raddb symlink.
	Root string

	RadiusdPath string

	// AltAuthPort and AltAcctPort are where the candidate server listens for
	// gates 2 and 3. They must not be the live ports.
	AltAuthPort int
	AltAcctPort int

	// ProbeTarget is where gate 3 sends packets. Its ports are overwritten
	// with the alternate ports.
	ProbeTarget Target

	// Probes is the gate-3 suite.
	Probes []Probe

	// Ready waits until the candidate server answers, bounded. When nil, the
	// Applier waits StartupSettle instead and says so, because sleeping is a
	// guess and a guess should be visible in the report.
	Ready func(ctx context.Context, t Target) error

	// StartupSettle is the fallback wait when Ready is nil.
	StartupSettle time.Duration

	// Restart cuts the live server over to the new tree. Restarting is the
	// caller's job because the supervisor differs — systemd, an entrypoint, a
	// container runtime — and guessing wrong here means the swap succeeds and
	// the server keeps running the old configuration.
	Restart func(ctx context.Context) error

	// VerifyLive confirms the live server answers after the swap. If it
	// returns an error, gate 4 rolls the symlink back and restarts again.
	VerifyLive func(ctx context.Context) error

	// SnapshotPath is where gate 4 records what the restarted server will load.
	// Empty means no snapshot is written, and the gate says so in its detail:
	// without one, the next Classify cannot tell a client the server loaded at
	// startup from one it can discover, and every answer it gives is a guess.
	SnapshotPath string

	// StartupClients returns the client table as it stands at cutover. It is a
	// function rather than a slice because the rows have to be read at the
	// moment the server is restarted, not at the moment the Applier was built —
	// an apply can take minutes and an access point can be registered inside
	// one.
	//
	// It is called immediately before Restart, deliberately: see
	// WriteStartupSnapshot for which way that ordering is wrong when it is
	// wrong.
	StartupClients func(ctx context.Context) ([]ClientRow, error)
}

const (
	nextDir  = "raddb.next"
	blueDir  = "raddb.blue"
	greenDir = "raddb.green"
	liveLink = "raddb"
)

// Apply generates cfg into raddb.next and runs the four gates.
//
// It stops at the first failure and leaves the live tree untouched. That is the
// whole point of generating into a directory nothing reads.
func (a *Applier) Apply(ctx context.Context, cfg Config) (Report, error) {
	var rep Report

	if a.AltAuthPort == cfg.AuthPort || a.AltAcctPort == cfg.AcctPort {
		return rep, fmt.Errorf("raddb: alternate ports collide with the live ports; gate 2 would fight the running server for the socket")
	}

	// Gate 0 — dialect.
	start := time.Now()
	out, err := a.Runner.Run(ctx, a.RadiusdPath, "-v")
	if err != nil {
		rep.Results = append(rep.Results, GateResult{Gate: Gates[0], Err: fmt.Errorf("radiusd -v: %w", err), Elapsed: time.Since(start)})
		return rep, rep.Results[len(rep.Results)-1].Err
	}
	v, err := CheckVersionOutput(string(out))
	rep.Version = v
	if err != nil {
		rep.Results = append(rep.Results, GateResult{Gate: Gates[0], Err: err, Elapsed: time.Since(start)})
		return rep, err
	}
	rep.Results = append(rep.Results, GateResult{Gate: Gates[0], OK: true, Detail: "FreeRADIUS " + v.String(), Elapsed: time.Since(start)})

	next := filepath.Join(a.Root, nextDir)
	if err := os.RemoveAll(next); err != nil {
		return rep, fmt.Errorf("raddb: clear %s: %w", next, err)
	}
	if err := NewGenerator(cfg).Generate(next); err != nil {
		return rep, err
	}

	// Gate 1 — radiusd -XC.
	//
	// Necessary and not sufficient, per FreeRADIUS's own man page: the server
	// can return zero here and still exit with an error when run normally, and
	// some modules are skipped entirely. Gates 2 and 3 carry the real load.
	start = time.Now()
	out, err = a.Runner.Run(ctx, a.RadiusdPath, "-XC", "-d", next)
	if err != nil {
		res := GateResult{Gate: Gates[1], Err: fmt.Errorf("radiusd -XC: %w", err), Detail: tail(string(out), 2000), Elapsed: time.Since(start)}
		rep.Results = append(rep.Results, res)
		return rep, res.Err
	}
	rep.Results = append(rep.Results, GateResult{Gate: Gates[1], OK: true, Detail: "configuration parses", Elapsed: time.Since(start)})

	// Gate 2 — a second radiusd, on alternate ports, against the candidate
	// tree. This is the gate that catches what -XC cannot: a module that fails
	// to instantiate, a listener that cannot bind, a database it cannot reach.
	start = time.Now()
	//
	// No -n. It does not name an instance the way it reads: it makes radiusd
	// read <name>.conf instead of radiusd.conf, and the candidate has to run
	// the same file the live server will run or this gate is testing a
	// different configuration from the one about to go live. -f writes no
	// pidfile, so the two servers do not collide over one.
	proc, err := a.Runner.Start(ctx, a.RadiusdPath,
		"-f", "-d", next,
		"-i", a.ProbeTarget.Host, "-p", strconv.Itoa(a.AltAuthPort),
	)
	if err != nil {
		res := GateResult{Gate: Gates[2], Err: fmt.Errorf("start candidate server: %w", err), Elapsed: time.Since(start)}
		rep.Results = append(rep.Results, res)
		return rep, res.Err
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = proc.Stop(stopCtx)
	}
	defer stop()

	target := a.ProbeTarget
	target.AuthPort = a.AltAuthPort
	target.AcctPort = a.AltAcctPort

	readyDetail := "candidate server answered"
	if a.Ready != nil {
		if err := a.Ready(ctx, target); err != nil {
			res := GateResult{Gate: Gates[2], Err: fmt.Errorf("candidate server never answered: %w", err), Elapsed: time.Since(start)}
			rep.Results = append(rep.Results, res)
			return rep, res.Err
		}
	} else {
		settle := a.StartupSettle
		if settle <= 0 {
			settle = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			res := GateResult{Gate: Gates[2], Err: ctx.Err(), Elapsed: time.Since(start)}
			rep.Results = append(rep.Results, res)
			return rep, ctx.Err()
		case <-time.After(settle):
		}
		readyDetail = "waited " + settle.String() + "; no readiness probe was configured, so this is a guess"
	}
	rep.Results = append(rep.Results, GateResult{Gate: Gates[2], OK: true, Detail: readyDetail, Elapsed: time.Since(start)})

	// Gate 3 — the smoke suite against the candidate.
	start = time.Now()
	if len(a.Probes) == 0 {
		res := GateResult{Gate: Gates[3], Err: errors.New("no probes configured; an empty suite is not a passing suite"), Elapsed: time.Since(start)}
		rep.Results = append(rep.Results, res)
		return rep, res.Err
	}
	var ran []string
	for _, p := range a.Probes {
		name, args := p.Build(target)
		out, err := a.Runner.Run(ctx, name, args...)
		if err != nil {
			res := GateResult{
				Gate:    Gates[3],
				Err:     fmt.Errorf("probe %s: %w", p.Name, err),
				Detail:  tail(string(out), 2000),
				Elapsed: time.Since(start),
			}
			rep.Results = append(rep.Results, res)
			return rep, res.Err
		}
		ran = append(ran, p.Name)
	}
	rep.Results = append(rep.Results, GateResult{Gate: Gates[3], OK: true, Detail: strings.Join(ran, ", "), Elapsed: time.Since(start)})

	stop()

	// Gate 4 — swap the symlink and cut over.
	start = time.Now()

	// The client table is read **before** anything moves.
	//
	// The rows are the same either side of the swap — nothing between here and
	// the restart writes that table — and the failure is not. The likely failure
	// is a database that has gone away, and there is no reason for that to leave
	// the symlink pointing at a tree no server has been restarted onto. Reading
	// it after the swap put a return path in this gate that skipped the rollback
	// entirely: `rolledBack: false`, and the running server still serving the
	// previous configuration while the link said otherwise, so the next restart
	// for any reason at all cut over to a configuration nobody applied.
	var pendingClients []ClientRow
	if a.SnapshotPath != "" {
		pendingClients, err = a.startupClients(ctx)
		if err != nil {
			res := GateResult{
				Gate:    Gates[4],
				Err:     fmt.Errorf("reading the client table for the startup snapshot: %w", err),
				Detail:  "nothing was swapped: the client table is read before the cutover",
				Elapsed: time.Since(start),
			}
			rep.Results = append(rep.Results, res)
			return rep, res.Err
		}
	}

	prev, err := a.currentColour()
	if err != nil {
		res := GateResult{Gate: Gates[4], Err: err, Elapsed: time.Since(start)}
		rep.Results = append(rep.Results, res)
		return rep, err
	}
	target2 := otherColour(prev)
	dst := filepath.Join(a.Root, target2)
	if err := os.RemoveAll(dst); err != nil {
		res := GateResult{Gate: Gates[4], Err: fmt.Errorf("clear %s: %w", dst, err), Elapsed: time.Since(start)}
		rep.Results = append(rep.Results, res)
		return rep, res.Err
	}
	if err := os.Rename(next, dst); err != nil {
		res := GateResult{Gate: Gates[4], Err: fmt.Errorf("move candidate into %s: %w", target2, err), Elapsed: time.Since(start)}
		rep.Results = append(rep.Results, res)
		return rep, res.Err
	}
	if err := a.swapTo(target2); err != nil {
		res := GateResult{Gate: Gates[4], Err: err, Elapsed: time.Since(start)}
		rep.Results = append(rep.Results, res)
		return rep, err
	}
	// The record of what the restarted server is about to load, written before
	// it starts. A snapshot taken afterwards records rows the server may have
	// discovered rather than loaded, and the two are the whole of the
	// difference between `sql-only` and `restart`.
	snapshotDetail := "no snapshot path configured; the next classification cannot tell a startup-loaded client from a discoverable one, so it is a guess"
	snapshotWritten := false
	if a.SnapshotPath != "" {
		if err := WriteStartupSnapshot(a.SnapshotPath, Snapshot{Config: cfg, Clients: pendingClients}); err != nil {
			// Past the swap, so the swap comes back. Nothing has been restarted
			// yet, so there is nothing to restart back onto.
			_ = a.swapTo(prev)
			rep.RolledBack = true
			res := GateResult{
				Gate:    Gates[4],
				Err:     err,
				Detail:  "symlink put back to " + prev + "; the server had not been restarted, so it is still serving " + prev,
				Elapsed: time.Since(start),
			}
			rep.Results = append(rep.Results, res)
			return rep, res.Err
		}
		snapshotWritten = true
		snapshotDetail = fmt.Sprintf("startup snapshot: %d clients recorded", len(pendingClients))
	}

	if a.Restart != nil {
		if err := a.Restart(ctx); err != nil {
			_ = a.swapTo(prev)
			rep.RolledBack = true
			res := GateResult{
				Gate:    Gates[4],
				Err:     fmt.Errorf("restart after swap: %w", err),
				Detail:  "symlink put back to " + prev + a.discardSnapshot(snapshotWritten),
				Elapsed: time.Since(start),
			}
			rep.Results = append(rep.Results, res)
			return rep, res.Err
		}
	}
	if a.VerifyLive != nil {
		if err := a.VerifyLive(ctx); err != nil {
			_ = a.swapTo(prev)
			rep.RolledBack = true
			if a.Restart != nil {
				_ = a.Restart(context.WithoutCancel(ctx))
			}
			res := GateResult{
				Gate:    Gates[4],
				Err:     fmt.Errorf("live server did not answer after cutover: %w", err),
				Detail:  "rolled back to " + prev + a.discardSnapshot(snapshotWritten),
				Elapsed: time.Since(start),
			}
			rep.Results = append(rep.Results, res)
			return rep, res.Err
		}
	}
	rep.Results = append(rep.Results, GateResult{Gate: Gates[4], OK: true, Detail: prev + " -> " + target2 + "; " + snapshotDetail, Elapsed: time.Since(start)})

	return rep, nil
}

// discardSnapshot removes a snapshot that describes a server which never ran, and
// returns what to append to the gate's detail.
//
// Both rollback paths below it write the snapshot and then fail to reach a server
// running that configuration. Leaving the file behind is the one direction the
// whole snapshot mechanism is built to avoid: it claims the running server loaded
// this client list at startup, and where the restart never happened the running
// server is still on the *previous* one. A client registered just before a failed
// apply would then read as loaded-and-unchanged, `Classify` would answer `none`,
// and an operator would be told there is nothing to apply about a NAS whose every
// packet the server discards.
//
// Deleting it is right rather than merely safe. ReadStartupSnapshot already defines
// a missing file as "we do not know what the server loaded", and the caller must
// treat that as unknown rather than empty -- which over-reports `restart`, which is
// the direction that costs a restart nobody needed instead of an outage nobody can
// see. A rolled-back cutover is exactly a state where nobody knows.
func (a *Applier) discardSnapshot(written bool) string {
	if !written {
		return ""
	}
	if err := os.Remove(a.SnapshotPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Said rather than swallowed: a stale snapshot that could not be removed
		// is the dangerous state, and the operator is the only one who can act.
		return "; WARNING: the startup snapshot could not be removed and now describes a server that never ran (" + err.Error() + ")"
	}
	return "; startup snapshot discarded, because it described a server that never ran"
}

// startupClients reads the client table for the snapshot.
//
// A configured SnapshotPath with no StartupClients is an error rather than an
// empty snapshot, because the two are opposite claims: an empty snapshot says the
// server loaded no clients, and every client in the table then classifies as new
// and discoverable. "We did not look" has to be the absence of a file, which the
// reader already treats as "we do not know".
func (a *Applier) startupClients(ctx context.Context) ([]ClientRow, error) {
	if a.StartupClients == nil {
		return nil, errors.New("SnapshotPath is set but StartupClients is nil; an empty snapshot would claim the server loaded no clients, which is not the same as not knowing")
	}
	return a.StartupClients(ctx)
}

// currentColour reads the live symlink, creating it on first run.
func (a *Applier) currentColour() (string, error) {
	link := filepath.Join(a.Root, liveLink)
	dest, err := os.Readlink(link)
	if errors.Is(err, os.ErrNotExist) {
		// First run: nothing is live yet. Treat green as the notional previous
		// colour so the first cutover lands on blue.
		return greenDir, nil
	}
	if err != nil {
		return "", fmt.Errorf("raddb: read %s: %w", link, err)
	}
	switch base := filepath.Base(dest); base {
	case blueDir, greenDir:
		return base, nil
	default:
		return "", fmt.Errorf("raddb: %s points at %q, which is neither colour; refusing to guess", link, dest)
	}
}

func otherColour(c string) string {
	if c == blueDir {
		return greenDir
	}
	return blueDir
}

// swapTo repoints the live symlink atomically. A symlink that is removed and
// recreated has a window where radiusd restarting would find no configuration
// at all; rename over it does not.
func (a *Applier) swapTo(colour string) error {
	link := filepath.Join(a.Root, liveLink)
	tmp := link + ".swap"
	_ = os.Remove(tmp)
	if err := os.Symlink(colour, tmp); err != nil {
		return fmt.Errorf("raddb: stage symlink: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("raddb: swap symlink to %s: %w", colour, err)
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// ExecRunner runs commands for real.
type ExecRunner struct{}

// Run executes a command and returns its combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// Start launches a long-running command.
func (ExecRunner) Start(ctx context.Context, name string, args ...string) (Process, error) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

type execProcess struct{ cmd *exec.Cmd }

// Stop asks the process to exit, then waits for it. A candidate server left
// running holds the alternate port and the next run's gate 2 fails on a socket.
func (p *execProcess) Stop(ctx context.Context) error {
	if p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		<-done
		return ctx.Err()
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil
		}
		return err
	}
}
