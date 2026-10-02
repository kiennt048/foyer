package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/raddb"
)

// The console's Apply reaches FreeRADIUS through the handoff, and its answer is
// the supervisor's — never a guess. internal/raddb's
// TestTheSupervisorSpeaksThisProtocol runs the real supervisor script; this is
// the control plane's half, against a supervisor played by the test.

func handoffConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		DatabaseURL:           "postgres://foyer_app:pw@db.internal:5432/foyer",
		RaddbPostgresPassword: "freeradius-own-generated-password",
		PolicyAddr:            "127.0.0.1:8080",
		RaddbTarget:           t.TempDir(),
	}
}

// announce plays the supervisor's start.
func announce(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, raddb.HandoffSupervisorFile), []byte("protocol=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// answerNext plays the supervisor for one request: it checks the tree it was
// handed is the one the console's settings produce, then answers.
func answerNext(t *testing.T, root, state, restarted, detail string, check func(tree string)) {
	t.Helper()
	requested := func() string {
		b, _ := os.ReadFile(filepath.Join(root, raddb.HandoffRequestFile))
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "generation="); ok {
				return v
			}
		}
		return ""
	}
	// Only a request made after this call is answered: an earlier one was the
	// previous apply's.
	before := requested()
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if gen := requested(); gen != "" && gen != before {
				if check != nil {
					check(filepath.Join(root, "gen-"+gen))
				}
				body := fmt.Sprintf("protocol=1\ngeneration=%s\nstate=%s\nrestarted=%s\nat=now\ndetail=%s\n", gen, state, restarted, detail)
				_ = os.WriteFile(filepath.Join(root, raddb.HandoffStatusFile), []byte(body), 0o644)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

func TestConsoleApplyReachesFreeRADIUSThroughTheSupervisor(t *testing.T) {
	cfg := handoffConfig(t)
	a := newRaddbApplier(cfg, nil)
	a.poll = 20 * time.Millisecond
	a.wait = 10 * time.Second

	if a.Available() {
		t.Fatal("apply is available before any supervisor announced itself")
	}
	res, err := a.Apply(context.Background(), apistore.RadiusGeneratorSettings{MaxServers: 48})
	if err != nil || res.Outcome != gen.RadiusApplyOutcomeRestartUnavailable {
		t.Fatalf("with no supervisor: %+v, %v; want restart-unavailable, said rather than guessed", res, err)
	}

	announce(t, cfg.RaddbTarget)
	if !a.Available() {
		t.Fatal("apply is unavailable with a supervisor announced")
	}

	var sawWorkers atomic.Bool
	answerNext(t, cfg.RaddbTarget, "applied", "no", "", func(tree string) {
		b, _ := os.ReadFile(filepath.Join(tree, "radiusd.conf"))
		sawWorkers.Store(strings.Contains(string(b), "max_servers = 48"))
	})
	res, err = a.Apply(context.Background(), apistore.RadiusGeneratorSettings{MaxServers: 48})
	if err != nil || res.Outcome != gen.RadiusApplyOutcomeApplied {
		t.Fatalf("an answered apply: %+v, %v", res, err)
	}
	if !sawWorkers.Load() {
		t.Error("the tree the supervisor was handed does not carry the console's worker count")
	}

	answerNext(t, cfg.RaddbTarget, "failed", "no", "radiusd -C refused the configuration", nil)
	res, err = a.Apply(context.Background(), apistore.RadiusGeneratorSettings{MaxServers: 48})
	if err != nil || res.Outcome != gen.RadiusApplyOutcomeFailed || !strings.Contains(res.Detail, "radiusd -C refused") {
		t.Fatalf("a refused apply: %+v, %v; want failed carrying the supervisor's reason", res, err)
	}

	// A supervisor that announced itself and then went away: a timeout, reported
	// as a failure whose outcome is unknown — never as applied.
	a.wait = 200 * time.Millisecond
	res, err = a.Apply(context.Background(), apistore.RadiusGeneratorSettings{MaxServers: 48})
	if err != nil || res.Outcome != gen.RadiusApplyOutcomeFailed || !strings.Contains(res.Detail, "did not answer") {
		t.Fatalf("an unanswered apply: %+v, %v", res, err)
	}
}

// The environment variable that held a shell command is not read by anything.
func TestNoRestartCommandIsReadFromTheEnvironment(t *testing.T) {
	src, err := os.ReadFile("radius.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`os.Getenv("FOYER_RADIUSD_RESTART_COMMAND")`, `exec.Command`} {
		if strings.Contains(string(src), forbidden) {
			t.Errorf("radius.go contains %s: the restart is the supervisor's, and no command runs from here", forbidden)
		}
	}
}
