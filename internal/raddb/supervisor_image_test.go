package raddb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTheSupervisorInTheFreeRADIUSImage is the supervisor's privilege drop against a real
// FreeRADIUS 3.2.x, run by `make lab-test` (FOYER_LAB_E2E=1, FOYER_LAB_FR_IMAGE):
// this checkout's supervise.sh, as root in the image, taking a generation whose
// radiusd.conf has NO `security { user = }` line at all. If the line were the
// only thing that dropped privileges, that tree would be a root radiusd. The server must be running as freerad, with no capabilities and
// no_new_privs, and a tree holding a file the generator never writes must be
// refused with the running server untouched.
//
// It uses a self-contained tree (radiusd.conf and dictionary, both on the
// allowlist) rather than the generated one, whose rlm_sql will not start
// without a database; the generated tree's agreement with the allowlist is
// TestTheSupervisorAllowsExactlyTheGeneratedTree.
func TestTheSupervisorInTheFreeRADIUSImage(t *testing.T) {
	if os.Getenv("FOYER_LAB_E2E") != "1" {
		t.Skip("tier 2: set FOYER_LAB_E2E=1 and FOYER_LAB_FR_IMAGE, or run `make lab-test`")
	}
	image := os.Getenv("FOYER_LAB_FR_IMAGE")
	if image == "" {
		t.Fatal("FOYER_LAB_E2E=1 with no FOYER_LAB_FR_IMAGE")
	}
	docker := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return string(out), err
	}
	script, err := filepath.Abs("../../lab/blue-green/supervise.sh")
	if err != nil {
		t.Fatal(err)
	}
	dict, err := filepath.Abs("../../lab/raddb/dictionary")
	if err != nil {
		t.Fatal(err)
	}
	dictionary, err := os.ReadFile(dict)
	if err != nil {
		t.Fatalf("the lab tree's dictionary (make lab-raddb writes it): %v", err)
	}

	handoff := t.TempDir()
	if err := os.Chmod(handoff, 0o777); err != nil {
		t.Fatal(err)
	}
	name := "foyer-supervisor-image-" + randomHex(t, 4)
	// An image cached before the Dockerfiles added the group gets it here, the
	// way the Dockerfile adds it; the supervisor, not the image, is under test.
	boot := `getent group foyer-radius-tls >/dev/null || { groupadd --system --gid 1812 foyer-radius-tls && usermod -a -G foyer-radius-tls freerad; }
exec /supervise.sh`
	if out, err := docker("run", "-d", "--name", name,
		"-v", handoff+":/var/lib/foyer/raddb",
		"-v", script+":/supervise.sh:ro",
		"-e", "FOYER_SUPERVISOR_POLL=0.2",
		"--entrypoint", "sh", image, "-c", boot); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = docker("rm", "-f", name) })

	h := NewHandoff(handoff)
	deadline := time.Now().Add(60 * time.Second)
	for !h.SupervisorPresent() {
		if time.Now().After(deadline) {
			logs, _ := docker("logs", name)
			t.Fatalf("the supervisor never announced itself:\n%s", logs)
		}
		time.Sleep(200 * time.Millisecond)
	}

	secret := randomHex(t, 16)
	conf := `prefix = /usr
exec_prefix = /usr
sysconfdir = /etc
localstatedir = /var
logdir = /tmp
raddbdir = /etc/raddb
confdir = ${raddbdir}
run_dir = /tmp
libdir = /usr/lib/freeradius
name = radiusd
log {
  destination = stdout
}
modules {
  always ok {
    rcode = ok
  }
}
server default {
  listen {
    type = auth
    ipaddr = 127.0.0.1
    port = 1812
  }
  authorize {
    ok
  }
}
client localhost {
  ipaddr = 127.0.0.1
  secret = ` + secret + `
}
`
	apply := func(extra map[string]string) HandoffStatus {
		t.Helper()
		gen, err := h.Stage(func(dir string) error {
			files := map[string]string{"radiusd.conf": conf, "dictionary": string(dictionary)}
			for k, v := range extra {
				files[k] = v
			}
			for n, body := range files {
				p := filepath.Join(dir, n)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Request(gen, HandoffApply); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		st, err := h.Await(ctx, gen, 200*time.Millisecond)
		if err != nil {
			logs, _ := docker("logs", "--tail", "40", name)
			t.Fatalf("no answer: %v\n%s", err, logs)
		}
		return st
	}
	server := func() string {
		t.Helper()
		// The pattern is split in two so this script's own command line, which
		// holds it, never matches; and the match is anywhere in the line because
		// an emulated amd64 image runs `/run/rosetta/rosetta /usr/sbin/radiusd
		// radiusd -f ...` (the deploy image under an emulating container runtime).
		out, err := docker("exec", name, "sh", "-c", `for d in /proc/[0-9]*; do
	c=$( { tr '\0' ' ' < $d/cmdline; } 2>/dev/null)
	case "$c" in *"radiusd -f -l"" stdout "*) echo "pid ${d#/proc/}"; grep -E '^(Uid|Gid|CapEff|CapPrm|CapBnd|NoNewPrivs):' $d/status ;; esac
done`)
		if err != nil {
			t.Fatalf("reading the server's credentials: %v\n%s", err, out)
		}
		return out
	}

	st := apply(nil)
	if !st.Applied() {
		logs, _ := docker("logs", "--tail", "40", name)
		t.Fatalf("a tree with no security section was not applied: %+v\n%s", st, logs)
	}
	running := server()
	t.Logf("MEASURED — the server the supervisor started, on a tree with no `user =` line:\n%s", running)
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(running), "\n") {
		k, v, _ := strings.Cut(line, ":")
		if strings.HasPrefix(line, "pid ") {
			k, v = "pid", strings.TrimPrefix(line, "pid ")
		}
		fields[k] = strings.Join(strings.Fields(v), " ")
	}
	if fields["pid"] == "" || strings.Contains(fields["pid"], " ") {
		procs, _ := docker("exec", name, "sh", "-c", `for d in /proc/[0-9]*; do printf '%s: ' "${d#/proc/}"; tr '\0' ' ' < $d/cmdline; echo; done`)
		logs, _ := docker("logs", "--tail", "30", name)
		t.Fatalf("want exactly one radiusd, found:\n%s\nprocesses:\n%s\nlogs:\n%s", running, procs, logs)
	}
	for _, id := range strings.Fields(fields["Uid"] + " " + fields["Gid"]) {
		if id == "0" {
			t.Errorf("radiusd runs with a root id: Uid %q Gid %q", fields["Uid"], fields["Gid"])
			break
		}
	}
	for _, k := range []string{"CapEff", "CapPrm", "CapBnd"} {
		if strings.Trim(fields[k], "0") != "" {
			t.Errorf("radiusd holds capabilities: %s %s", k, fields[k])
		}
	}
	if fields["NoNewPrivs"] != "1" {
		t.Errorf("NoNewPrivs %q, want 1", fields["NoNewPrivs"])
	}

	st = apply(map[string]string{"mods-available/exec": "exec {\n}\n"})
	if st.Applied() || st.Restarted || !strings.Contains(st.Detail, "mods-available/exec") {
		t.Errorf("a tree holding an exec module: %+v, want refused with the server untouched", st)
	}
	if again := server(); !strings.Contains(again, "pid "+fields["pid"]) {
		t.Errorf("the running server changed after a refused tree:\nbefore\n%s\nafter\n%s", running, again)
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
