package raddb

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRadiusd stands in for radiusd, reading its instructions from the tree's
// radiusd.conf — the one file every tree must hold, since the supervisor refuses
// any file the generator does not write: `-XC` refuses a tree whose radiusd.conf
// says BROKEN, a serving start exits at once on NOSTART (after printing an Info
// line, which is not a reason, and a line that names a password, which must never
// reach the console), and otherwise it announces "Ready to process requests",
// records the tree's first line as the tree it is serving, and serves until it is
// stopped.
//
// Under the rig that plays root (rigOptions.root), it also refuses to do anything at all
// unless the fake setpriv started it: that is "radiusd was started as root", seen
// from the only place a test without root can see it.
const fakeRadiusd = `#!/bin/sh
[ -n "${FAKE_ENV:-}" ] && printf '%s\n' "${FOYER_RADIUS_DB_PASSWORD:-}" >>"$FAKE_ENV"
dir=""
check=0
while [ $# -gt 0 ]; do
	case $1 in
	-d) dir=$2; shift ;;
	-XC) check=1 ;;
	esac
	shift
done
if [ -n "$FAKE_REQUIRE_DROP" ] && [ "$FAKE_DROPPED" != 1 ]; then
	echo "Error: radiusd was started as root"
	exit 1
fi
if [ "$check" = 1 ]; then
	if grep -q BROKEN "$dir/radiusd.conf"; then echo "/etc/raddb/mods-enabled/rest[3]: Invalid location for 'nope'"; exit 1; fi
	echo "Configuration appears to be OK"
	exit 0
fi
if grep -q NOSTART "$dir/radiusd.conf"; then
	echo "Info: Debug state unknown (cap_sys_ptrace capability not set)"
	echo "rlm_sql (sql): password = hunter2 was refused"
	echo "Error: rlm_sql_postgresql: Connection failed"
	exit 1
fi
echo "$(head -1 "$dir/radiusd.conf")" >>"$FAKE_SERVED"
echo "Info: Ready to process requests"
trap 'exit 0' TERM
while :; do sleep 1; done
`

// The root rig's PATH: `id` says root (and that freerad is 995), `chown` is a
// no-op because the rig cannot give files to root, and `setpriv` records its
// arguments and runs the command after `--`, marked as dropped.
const (
	fakeID = `#!/bin/sh
case "$*" in
"-u") echo 0 ;;
"-u freerad") echo 995 ;;
*) exit 1 ;;
esac
`
	fakeChown   = "#!/bin/sh\nexit 0\n"
	fakeSetpriv = `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_SETPRIV"
while [ $# -gt 0 ] && [ "$1" != -- ]; do shift; done
shift
FAKE_DROPPED=1 exec "$@"
`
)

type supervisorRig struct {
	h       *Handoff
	served  string
	setpriv string
	cmd     *exec.Cmd
	done    chan struct{}
	exit    error
}

// rigOptions: root plays a supervisor running as root (fake id, chown and,
// unless noSetpriv, setpriv on PATH); env is appended to the supervisor's
// environment.
type rigOptions struct {
	root, noSetpriv bool
	env             []string
}

func startSupervisor(t *testing.T, extraEnv ...string) *supervisorRig {
	return startSupervisorWith(t, rigOptions{env: extraEnv})
}

func launchSupervisor(t *testing.T, o rigOptions) *supervisorRig {
	t.Helper()
	root := t.TempDir()
	fake := filepath.Join(root, "radiusd")
	if err := os.WriteFile(fake, []byte(fakeRadiusd), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &supervisorRig{
		h:       NewHandoff(filepath.Join(root, "handoff")),
		served:  filepath.Join(root, "served"),
		setpriv: filepath.Join(root, "setpriv.log"),
	}
	script, err := filepath.Abs("../../lab/blue-green/supervise.sh")
	if err != nil {
		t.Fatal(err)
	}
	env := os.Environ()
	if o.root {
		bin := filepath.Join(root, "bin")
		fakes := map[string]string{"id": fakeID, "chown": fakeChown}
		if !o.noSetpriv {
			fakes["setpriv"] = fakeSetpriv
		}
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range fakes {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// PATH is this directory alone, holding the fakes and links to the real
		// tools the script and the fake radiusd use, so a real setpriv on the host
		// cannot answer for an absent fake one.
		for _, tool := range []string{"sh", "mkdir", "date", "chmod", "mv", "grep", "tr", "cut", "tail", "head",
			"readlink", "basename", "ln", "rm", "mkfifo", "sleep", "cp", "diff", "find", "sed", "cat"} {
			p, err := exec.LookPath(tool)
			if err != nil {
				t.Fatalf("the root rig needs %s: %v", tool, err)
			}
			if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
		env = append(env, "PATH="+bin, "FAKE_REQUIRE_DROP=1", "FAKE_SETPRIV="+r.setpriv)
	}
	r.cmd = exec.Command("sh", script)
	r.cmd.Env = append(env,
		"FOYER_RADDB_HANDOFF="+r.h.Root,
		"FOYER_RADDB_LIVE="+filepath.Join(root, "live"),
		"FOYER_RADDB_LINK="+filepath.Join(root, "raddb"),
		"FOYER_SUPERVISOR_RUN="+filepath.Join(root, "run"),
		"FOYER_SUPERVISOR_POLL=0.1",
		"FOYER_SUPERVISOR_READY_SECONDS=5",
		"RADIUSD="+fake,
		"FAKE_SERVED="+r.served,
	)
	r.cmd.Env = append(r.cmd.Env, o.env...)
	if testing.Verbose() {
		r.cmd.Stdout, r.cmd.Stderr = os.Stdout, os.Stderr
	}
	if err := r.cmd.Start(); err != nil {
		t.Fatalf("starting the supervisor: %v", err)
	}
	r.done = make(chan struct{})
	go func() { r.exit = r.cmd.Wait(); close(r.done) }()
	t.Cleanup(func() {
		_ = r.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			_ = r.cmd.Process.Kill()
		}
	})
	return r
}

func startSupervisorWith(t *testing.T, o rigOptions) *supervisorRig {
	t.Helper()
	r := launchSupervisor(t, o)
	deadline := time.Now().Add(5 * time.Second)
	for !r.h.SupervisorPresent() {
		if time.Now().After(deadline) {
			t.Fatal("the supervisor never announced itself")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return r
}

func (r *supervisorRig) generation(t *testing.T, mode HandoffMode, files map[string]string) HandoffStatus {
	t.Helper()
	gen, err := r.h.Stage(func(dir string) error {
		for name, body := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				return err
			}
			// "-> target" is a symbolic link.
			if target, ok := strings.CutPrefix(body, "-> "); ok {
				if err := os.Symlink(target, p); err != nil {
					return err
				}
				continue
			}
			if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("staging: %v", err)
	}
	return r.awaitMode(t, gen, mode)
}

// await requests a staged generation with mode=apply and returns the answer.
func (r *supervisorRig) await(t *testing.T, gen uint64) HandoffStatus {
	t.Helper()
	return r.awaitMode(t, gen, HandoffApply)
}

func (r *supervisorRig) awaitMode(t *testing.T, gen uint64, mode HandoffMode) HandoffStatus {
	t.Helper()
	if err := r.h.Request(gen, mode); err != nil {
		t.Fatalf("requesting: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := r.h.Await(ctx, gen, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("generation %d: %v", gen, err)
	}
	r.h.Prune(gen)
	return st
}

func (r *supervisorRig) servedTrees(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(r.served)
	return strings.Fields(string(b))
}

// TestTheSupervisorSpeaksThisProtocol drives lab/blue-green/supervise.sh — the
// thing that ships in both FreeRADIUS images — through every branch the console
// can report: applied, refused by `radiusd -C` with the old server untouched,
// failed to start and rolled back, and a startup request for the tree already
// running that restarts nothing.
func TestTheSupervisorSpeaksThisProtocol(t *testing.T) {
	r := startSupervisor(t)

	st := r.generation(t, HandoffApply, map[string]string{"radiusd.conf": "one"})
	if !st.Applied() || !st.Restarted {
		t.Fatalf("first generation: %+v, want applied and started", st)
	}

	st = r.generation(t, HandoffApply, map[string]string{"radiusd.conf": "two"})
	if !st.Applied() || !st.Restarted {
		t.Fatalf("second generation: %+v, want applied and restarted", st)
	}

	st = r.generation(t, HandoffApply, map[string]string{"radiusd.conf": "broken\nBROKEN"})
	if st.Applied() || st.Restarted || !strings.Contains(st.Detail, "radiusd -C refused") ||
		!strings.Contains(st.Detail, "Invalid location") {
		t.Fatalf("a tree radiusd -C refuses: %+v, want failed, not restarted, with radiusd's reason", st)
	}

	st = r.generation(t, HandoffApply, map[string]string{"radiusd.conf": "nostart\nNOSTART"})
	if st.Applied() || !strings.Contains(st.Detail, "previous one is running again") {
		t.Fatalf("a tree that does not start: %+v, want failed and rolled back", st)
	}
	if strings.Contains(st.Detail, "hunter2") || strings.Contains(strings.ToLower(st.Detail), "password") {
		t.Fatalf("the detail the console shows carries a credential line: %q", st.Detail)
	}
	// radiusd's Info lines are not reasons: 3.2.10 prints "Debug state unknown"
	// at every start, and matching on the word "unknown" would put it in the
	// operator's detail ahead of the real error.
	if strings.Contains(st.Detail, "Debug state") || !strings.Contains(st.Detail, "Connection failed") {
		t.Fatalf("the detail carries an Info line or lost the reason: %q", st.Detail)
	}

	st = r.generation(t, HandoffStartup, map[string]string{"radiusd.conf": "two"})
	if !st.Applied() || st.Restarted {
		t.Fatalf("a startup request for the running tree: %+v, want applied without a restart", st)
	}

	// one, two, then two again after the rollback — never "broken", never
	// "nostart" (which exits before recording), and no restart for the last
	// request.
	if got, want := strings.Join(r.servedTrees(t), " "), "one two two"; got != want {
		t.Fatalf("radiusd served %q, want %q", got, want)
	}

	entries, _ := os.ReadDir(r.h.Root)
	var gens int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gen-") {
			gens++
		}
	}
	if gens != 1 {
		t.Errorf("the handoff directory holds %d generations after pruning, want 1", gens)
	}
}

// An apply nobody answers is a timeout, never an applied.
func TestAnApplyNobodyAnswersIsNotApplied(t *testing.T) {
	h := NewHandoff(t.TempDir())
	if h.SupervisorPresent() {
		t.Fatal("a supervisor is present in an empty directory")
	}
	gen, err := h.Stage(func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Request(gen, HandoffApply); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := h.Await(ctx, gen, 20*time.Millisecond); err != ErrNoSupervisor {
		t.Fatalf("Await = %v, want ErrNoSupervisor", err)
	}
}
