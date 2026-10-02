package raddb

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The handoff: how a tree the control plane generated reaches a running
// FreeRADIUS and takes effect, with no operator command and no shell command
// stored anywhere.
//
// ── what it is not ───────────────────────────────────────────────────────────
//
// Not a restart command stored in the environment for the control plane to run:
// a deployment that never sets one gets a console Apply that writes a tree
// nothing loads. And not a one-shot that regenerates the live tree from CLI flags
// on every `up`, which would overwrite the worker count and EAP settings the
// console has saved.
//
// ── the survey, and what was taken from it ───────────────────────────────────
//
//   - **PacketFence** generates FreeRADIUS's configuration in its own process and
//     restarts radiusd through its own service manager (`pfcmd service radiusd
//     restart`, systemd units it owns). The web admin never supplies a command.
//     Taken: the restart is a fixed mechanism the product ships, not a setting.
//   - **daloRADIUS** writes only SQL and leaves every radiusd restart to the
//     administrator's shell. That is the gap this closes, not a model.
//   - **FreeRADIUS 3.2** reloads on SIGHUP only the modules marked HUP-safe; the
//     client list, listeners, the thread pool and EAP's TLS context are read at
//     start. `max_servers` and an EAP certificate — the two things the console
//     sets — need a restart. `radiusd -C` is the documented pre-flight, and its
//     man page says it is "necessarily incomplete", so readiness after the
//     restart is checked as well. Taken: validate, restart, confirm, roll back.
//
// ── the shape ────────────────────────────────────────────────────────────────
//
// A directory both containers mount. The control plane writes; a small supervisor
// that ships in the FreeRADIUS image (`lab/blue-green/supervise.sh`, PID 1's child
// under tini) reads, and answers:
//
//	gen-<N>/     a complete generated tree, written under a temporary name
//	             and renamed into place, so it is never seen half-written
//	request      generation=<N>, mode=apply|startup — written last, atomically
//	status       the supervisor's answer: generation, state=applied|failed,
//	             restarted=yes|no, at, detail
//	supervisor   written by the supervisor when it starts: protocol=1
//
// The supervisor copies gen-<N> into a colour (blue/green) inside its own
// container, points /etc/raddb at it, runs `radiusd -XC`, restarts radiusd and
// waits for "Ready to process requests"; on any failure it points /etc/raddb back
// and restarts the previous tree. `mode=startup` restarts only when the tree
// differs from the one running, so a control-plane restart does not bounce every
// 802.1X supplicant on site; `mode=apply` always restarts, because the operator
// pressing Apply may be asking FreeRADIUS to re-read a client table whose tree is
// unchanged.
//
// The protocol is small on purpose: the other side is POSIX sh, and
// TestTheSupervisorSpeaksThisProtocol runs it against a fake radiusd.
//
// ── what the supervisor trusts, stated ───────────────────────────────────────
//
// The tree's CONTENT, exactly as FreeRADIUS always has: a generated configuration
// is code radiusd runs, and the control plane that writes it already holds the KEK
// and every secret it unseals. **Not the tree's say over privilege, and not its
// shape.** If radiusd started as root and dropped to `freerad` only because the
// tree's own `security { user = }` said so, a control plane that wrote a tree
// without the line — the thing a compromised `foyer` container would do — would
// have a root radiusd in the FreeRADIUS container. So:
//
//   - every radiusd the supervisor starts, the `-XC` check included, starts as
//     `freerad` with its supplementary groups, no capabilities and no_new_privs
//     (`setpriv`, util-linux, in both images), whatever the tree says; a root
//     supervisor with no setpriv or no such user refuses to start at all;
//   - the tree is checked against the files, directories and links this package
//     writes (`TREE_FILES`/`TREE_DIRS` in supervise.sh; a link only as
//     `<kind>-enabled/<name> -> ../<kind>-available/<name>`) before any of it is
//     copied into place, so `mods-enabled/exec` or a link out of the tree is a
//     refusal and the running server is untouched.
//
// The field does what this replaced, and why it is safe there is the point.
// PacketFence's `packetfence-radiusd-auth.service` runs `/usr/sbin/radiusd -d
// /usr/local/pf/raddb -n auth -fm` from systemd and leaves the drop to the generated
// configuration's `security` section — but its generator runs as root on the same
// host, so the tree's author is already as privileged as radiusd. Here the author
// is the `foyer` container, deliberately less privileged than the FreeRADIUS one,
// so the drop has to live on the side that holds root. radiusd 3.2 has no
// command-line user option (its `-u`/`-g` do not exist; what `-u freerad -g
// freerad` would do is setpriv's job here). TestTheSupervisorAllowsExactlyTheGeneratedTree holds the
// allowlist to the generator in both directions; TestTheSupervisorNeverStarts-
// RadiusdAsRoot and TestTheSupervisorInTheFreeRADIUSImage (tier 2) hold the drop.
//
// What the supervisor also does NOT trust is the request: the generation must be
// digits, the mode is compared and never interpolated, symlinks are copied as links
// (`cp -RP`) and never followed by the ownership change, and nothing in the
// directory is ever executed. A command string has no place to go.

// Handoff file names, shared with lab/blue-green/supervise.sh.
const (
	HandoffRequestFile    = "request"
	HandoffStatusFile     = "status"
	HandoffSupervisorFile = "supervisor"
	handoffGenPrefix      = "gen-"
	handoffTmpPrefix      = ".tmp-"

	// HandoffProtocol is the version both sides speak. A supervisor announcing
	// another is not one this control plane may hand a tree to.
	HandoffProtocol = "1"
)

// HandoffMode is what a request asks of the supervisor.
type HandoffMode string

const (
	// HandoffApply always restarts: the operator asked for it.
	HandoffApply HandoffMode = "apply"
	// HandoffStartup restarts only if the tree differs from the running one.
	HandoffStartup HandoffMode = "startup"
)

// HandoffStatus is the supervisor's answer to one generation.
type HandoffStatus struct {
	Generation uint64
	// State is "applied" or "failed".
	State     string
	Restarted bool
	At        string
	// Detail is one sanitised line: why a generation failed, or a note on one
	// that applied. The supervisor drops any line naming a password or secret.
	Detail string
}

// Applied reports whether the generation is the one running.
func (s HandoffStatus) Applied() bool { return s.State == "applied" }

// ErrNoSupervisor is an apply nobody answered.
var ErrNoSupervisor = errors.New("raddb: FreeRADIUS's supervisor did not answer")

// Handoff is the control plane's side of the directory.
type Handoff struct {
	Root string

	mu   sync.Mutex
	last uint64
}

// NewHandoff returns the handoff rooted at root.
func NewHandoff(root string) *Handoff { return &Handoff{Root: root} }

// SupervisorPresent reports whether a supervisor speaking this protocol has
// announced itself. It is a statement about the mechanism, not a liveness check:
// a stopped container leaves its announcement behind, and an apply then times out
// and says so rather than claiming to have restarted anything.
func (h *Handoff) SupervisorPresent() bool {
	kv, err := readKV(filepath.Join(h.Root, HandoffSupervisorFile))
	return err == nil && kv["protocol"] == HandoffProtocol
}

// Stage writes a tree through write into a fresh generation directory and
// returns the generation. Nothing is requested yet.
func (h *Handoff) Stage(write func(dir string) error) (uint64, error) {
	h.mu.Lock()
	gen := uint64(time.Now().UnixNano())
	if gen <= h.last {
		gen = h.last + 1
	}
	h.last = gen
	h.mu.Unlock()

	if err := os.MkdirAll(h.Root, 0o750); err != nil {
		return 0, fmt.Errorf("raddb: handoff %s: %w", h.Root, err)
	}
	name := handoffGenPrefix + strconv.FormatUint(gen, 10)
	tmp := filepath.Join(h.Root, handoffTmpPrefix+name)
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o750); err != nil {
		return 0, fmt.Errorf("raddb: handoff %s: %w", tmp, err)
	}
	if err := write(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, filepath.Join(h.Root, name)); err != nil {
		_ = os.RemoveAll(tmp)
		return 0, fmt.Errorf("raddb: handoff: %w", err)
	}
	return gen, nil
}

// Request asks the supervisor to take generation gen.
func (h *Handoff) Request(gen uint64, mode HandoffMode) error {
	body := fmt.Sprintf("protocol=%s\ngeneration=%d\nmode=%s\n", HandoffProtocol, gen, mode)
	return writeAtomic(filepath.Join(h.Root, HandoffRequestFile), []byte(body))
}

// Status reads the supervisor's latest answer. A missing file is (zero, nil).
func (h *Handoff) Status() (HandoffStatus, error) {
	kv, err := readKV(filepath.Join(h.Root, HandoffStatusFile))
	if errors.Is(err, os.ErrNotExist) {
		return HandoffStatus{}, nil
	}
	if err != nil {
		return HandoffStatus{}, err
	}
	gen, _ := strconv.ParseUint(kv["generation"], 10, 64)
	return HandoffStatus{
		Generation: gen, State: kv["state"], Restarted: kv["restarted"] == "yes",
		At: kv["at"], Detail: kv["detail"],
	}, nil
}

// ErrSuperseded is a generation the supervisor never answered because a later
// one was requested first; the later one's answer is the one that counts.
var ErrSuperseded = errors.New("raddb: a later generation was requested before this one was taken up")

// Await polls for the supervisor's answer to gen until ctx ends.
func (h *Handoff) Await(ctx context.Context, gen uint64, every time.Duration) (HandoffStatus, error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if st, err := h.Status(); err == nil && st.State != "" {
			switch {
			case st.Generation == gen:
				return st, nil
			case st.Generation > gen:
				return HandoffStatus{}, ErrSuperseded
			}
		}
		select {
		case <-ctx.Done():
			return HandoffStatus{}, ErrNoSupervisor
		case <-t.C:
		}
	}
}

// Prune removes every generation older than keep, finished or abandoned. The
// previous tree lives on inside the supervisor's own container, which is what a
// rollback restarts, so nothing here needs more than the generation just
// answered. **Newer generations are never touched**: one may be in flight from a
// concurrent apply, and deleting it would be an apply that fails for a reason of
// this process's own making.
func (h *Handoff) Prune(keep uint64) {
	entries, err := os.ReadDir(h.Root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() {
			continue
		}
		num := strings.TrimPrefix(strings.TrimPrefix(n, handoffTmpPrefix), handoffGenPrefix)
		if num == n {
			continue
		}
		if g, err := strconv.ParseUint(num, 10, 64); err == nil && g < keep {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		_ = os.RemoveAll(filepath.Join(h.Root, n))
	}
}

// GenerationDir is where generation gen lives.
func (h *Handoff) GenerationDir(gen uint64) string {
	return filepath.Join(h.Root, handoffGenPrefix+strconv.FormatUint(gen, 10))
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return fmt.Errorf("raddb: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("raddb: rename %s: %w", path, err)
	}
	return nil
}

// readKV reads key=value lines. The value is everything after the first '='.
func readKV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out, sc.Err()
}
