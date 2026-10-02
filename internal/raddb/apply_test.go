package raddb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records every command and answers from a script.
type fakeRunner struct {
	// version is what `radiusd -v` prints.
	version string
	// failOn makes Run fail for the first command whose argv joins to a string
	// containing this substring.
	failOn string
	// startFails makes Start fail.
	startFails bool

	ran     []string
	started int
	stopped int
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.ran = append(f.ran, line)
	if f.failOn != "" && strings.Contains(line, f.failOn) {
		return []byte("boom"), errors.New("command failed")
	}
	if len(args) > 0 && args[0] == "-v" {
		return []byte(f.version), nil
	}
	return []byte("ok"), nil
}

func (f *fakeRunner) Start(_ context.Context, name string, args ...string) (Process, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.ran = append(f.ran, "start: "+line)
	if f.startFails {
		return nil, errors.New("cannot start")
	}
	f.started++
	return &fakeProcess{f}, nil
}

type fakeProcess struct{ f *fakeRunner }

func (p *fakeProcess) Stop(context.Context) error { p.f.stopped++; return nil }

func testApplier(t *testing.T, r *fakeRunner) *Applier {
	t.Helper()
	return &Applier{
		Runner:      r,
		Root:        t.TempDir(),
		RadiusdPath: "radiusd",
		AltAuthPort: 11812,
		AltAcctPort: 11813,
		ProbeTarget: Target{Host: "127.0.0.1", Secret: "probe-secret-for-this-test"},
		Probes: []Probe{
			RadclientProbe("status-server", "radclient", "/probe/status.request", "status"),
			EapolTestProbe("eap-tls", "eapol_test", "/probe/eap-tls.conf"),
		},
		Ready: func(context.Context, Target) error { return nil },
	}
}

func testConfig() Config {
	c := DefaultConfig()
	c.PostgresPassword = "pg-password-for-this-test-only"
	return c
}

const goodVersion = "radiusd: FreeRADIUS Version 3.2.10, for host aarch64-unknown-linux-gnu"

// TestApplyRunsAllFourGatesInOrder pins the shape of the procedure. Gate 1 is
// necessary and not sufficient — FreeRADIUS's own man page says the server can
// return zero from -XC and still fail to start — so a run that stops after gate
// 1 has proved almost nothing, and the test says so by requiring all four.
func TestApplyRunsAllFourGatesInOrder(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)

	rep, err := a.Apply(context.Background(), testConfig())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := len(rep.Results); got != len(Gates) {
		t.Fatalf("ran %d gates, want %d: %+v", got, len(Gates), rep.Results)
	}
	for i, res := range rep.Results {
		if res.Gate != Gates[i] {
			t.Errorf("gate %d is %v, want %v", i, res.Gate, Gates[i])
		}
		if !res.OK {
			t.Errorf("gate %d (%s) failed: %v", res.Gate.Number, res.Gate.Name, res.Err)
		}
	}
	if rep.Version != (Version{3, 2, 10}) {
		t.Errorf("report version %s", rep.Version)
	}

	// The second server was started against the candidate tree, on the
	// alternate port, and was stopped again.
	var startLine string
	for _, l := range r.ran {
		if strings.HasPrefix(l, "start: ") {
			startLine = l
		}
	}
	if startLine == "" {
		t.Fatal("gate 2 never started a second server")
	}
	for _, want := range []string{"raddb.next", "-p 11812"} {
		if !strings.Contains(startLine, want) {
			t.Errorf("candidate server command is missing %q: %s", want, startLine)
		}
	}
	// -n does not name an instance. It makes radiusd read <name>.conf instead
	// of radiusd.conf, so a candidate started with it runs a file that does not
	// exist in the tree being tested — and gate 2 then reports on nothing.
	if strings.Contains(startLine, " -n ") {
		t.Errorf("candidate server was started with -n: %s", startLine)
	}
	if r.stopped != 1 {
		t.Errorf("candidate server stopped %d times, want 1; one left running holds the alternate port", r.stopped)
	}

	// Gate 3 ran both kinds. radclient does not speak EAP, so a suite made only
	// of radclient cases proves nothing about the EAP surface at all.
	var sawRadclient, sawEapol bool
	for _, l := range r.ran {
		if strings.HasPrefix(l, "radclient ") && strings.Contains(l, "127.0.0.1:11812") {
			sawRadclient = true
		}
		if strings.HasPrefix(l, "eapol_test ") && strings.Contains(l, "-p 11812") {
			sawEapol = true
		}
	}
	if !sawRadclient {
		t.Error("gate 3 ran no radclient case against the candidate")
	}
	if !sawEapol {
		t.Error("gate 3 ran no eapol_test case against the candidate")
	}
}

// TestApplyStopsAtTheFirstFailingGate: every gate must abort the ones after it,
// or a failure at gate 1 is followed by a cutover.
func TestApplyStopsAtTheFirstFailingGate(t *testing.T) {
	cases := []struct {
		name     string
		runner   *fakeRunner
		wantGate int
	}{
		{"dialect", &fakeRunner{version: "radiusd: FreeRADIUS Version 3.0.27, for host aarch64-alpine-linux-musl"}, 0},
		{"config check", &fakeRunner{version: goodVersion, failOn: "-XC"}, 1},
		{"second server", &fakeRunner{version: goodVersion, startFails: true}, 2},
		{"smoke", &fakeRunner{version: goodVersion, failOn: "eapol_test"}, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApplier(t, tc.runner)
			rep, err := a.Apply(context.Background(), testConfig())
			if err == nil {
				t.Fatalf("Apply succeeded with %s broken", tc.name)
			}
			failed := rep.Failed()
			if failed == nil {
				t.Fatalf("report has no failing gate")
			}
			if failed.Gate.Number != tc.wantGate {
				t.Errorf("failed at gate %d, want %d", failed.Gate.Number, tc.wantGate)
			}
			if len(rep.Results) != tc.wantGate+1 {
				t.Errorf("ran %d gates after failing at %d; the ones after must not run", len(rep.Results), tc.wantGate)
			}
			if _, err := os.Lstat(filepath.Join(a.Root, liveLink)); err == nil {
				t.Errorf("a failed run cut over anyway")
			}
			if tc.wantGate >= 2 && tc.runner.started > 0 && tc.runner.stopped != tc.runner.started {
				t.Errorf("candidate server left running after a failure")
			}
		})
	}
}

// TestApplyRefusesAnEmptyProbeSuite. An empty suite passes every assertion in
// it, which is the fifth mechanism in this repository that looked like a check.
func TestApplyRefusesAnEmptyProbeSuite(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)
	a.Probes = nil

	rep, err := a.Apply(context.Background(), testConfig())
	if err == nil {
		t.Fatal("Apply accepted a suite with no cases in it")
	}
	if f := rep.Failed(); f == nil || f.Gate.Number != 3 {
		t.Errorf("empty suite failed at %v, want gate 3", f)
	}
}

// TestCutoverAlternatesColoursAndRollsBack.
func TestCutoverAlternatesColoursAndRollsBack(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)
	var restarts int
	a.Restart = func(context.Context) error { restarts++; return nil }

	if _, err := a.Apply(context.Background(), testConfig()); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	first, err := os.Readlink(filepath.Join(a.Root, liveLink))
	if err != nil {
		t.Fatalf("no live symlink after cutover: %v", err)
	}
	if first != blueDir {
		t.Errorf("first cutover landed on %s, want %s", first, blueDir)
	}

	if _, err := a.Apply(context.Background(), testConfig()); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	second, _ := os.Readlink(filepath.Join(a.Root, liveLink))
	if second != greenDir {
		t.Errorf("second cutover landed on %s, want %s; the colours must alternate or the live tree is rewritten under the running server", second, greenDir)
	}
	if restarts != 2 {
		t.Errorf("restarted %d times, want 2", restarts)
	}

	// Now a cutover whose live server does not answer.
	a.VerifyLive = func(context.Context) error { return errors.New("no answer on 1812") }
	rep, err := a.Apply(context.Background(), testConfig())
	if err == nil {
		t.Fatal("Apply succeeded although the live server never answered")
	}
	if !rep.RolledBack {
		t.Error("report does not say it rolled back")
	}
	after, _ := os.Readlink(filepath.Join(a.Root, liveLink))
	if after != second {
		t.Errorf("live symlink is %s after a failed cutover, want the previous %s", after, second)
	}
	if restarts != 4 {
		t.Errorf("restarted %d times; a rollback has to restart again or the swap is cosmetic", restarts)
	}
}

// TestApplyRefusesAlternatePortsThatCollideWithTheLiveOnes. Gate 2 starts a
// second server; if it is pointed at the live ports it fights the running one
// for the socket, and the failure looks like a bad configuration.
func TestApplyRefusesAlternatePortsThatCollideWithTheLiveOnes(t *testing.T) {
	r := &fakeRunner{version: goodVersion}
	a := testApplier(t, r)
	cfg := testConfig()
	a.AltAuthPort = cfg.AuthPort

	if _, err := a.Apply(context.Background(), cfg); err == nil {
		t.Fatal("Apply accepted alternate ports equal to the live ones")
	} else if !strings.Contains(err.Error(), "collide") {
		t.Errorf("error does not say why: %v", err)
	}
}
