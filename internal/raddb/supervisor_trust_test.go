package raddb

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The supervisor half of the privilege drop: a supervisor that runs radiusd as
// root on a tree the control plane wrote drops to `freerad` only if that tree's
// own `security { user = }` says so. So it starts every radiusd as freerad itself, and refuses a tree holding
// anything the generator does not write.

// TestTheSupervisorNeverStartsRadiusdAsRoot plays a supervisor running as root.
// The fake radiusd refuses to run unless the fake setpriv started it, so a
// supervisor that ran it directly — as it did — answers `failed`.
func TestTheSupervisorNeverStartsRadiusdAsRoot(t *testing.T) {
	r := startSupervisorWith(t, rigOptions{root: true})

	st := r.generation(t, HandoffApply, map[string]string{"radiusd.conf": "one"})
	if !st.Applied() || !st.Restarted {
		t.Fatalf("a root supervisor: %+v, want applied -- radiusd was started without dropping privileges", st)
	}

	b, err := os.ReadFile(r.setpriv)
	if err != nil {
		t.Fatalf("setpriv was never run: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	var check, serve bool
	for _, c := range calls {
		for _, want := range []string{"--reuid=freerad", "--regid=freerad", "--init-groups", "--no-new-privs",
			"--inh-caps=-all", "--bounding-set=-all", "-- "} {
			if !strings.Contains(c, want) {
				t.Errorf("setpriv %q lacks %s", c, want)
			}
		}
		check = check || strings.Contains(c, " -XC ")
		serve = serve || strings.Contains(c, " -f -l stdout ")
	}
	if !check || !serve {
		t.Errorf("setpriv ran %q; want both the -XC check and the server under it", calls)
	}
}

// TestASupervisorThatCannotDropPrivilegesDoesNotStart: root with no setpriv is a
// supervisor that would have to run radiusd as root, and it refuses to start.
func TestASupervisorThatCannotDropPrivilegesDoesNotStart(t *testing.T) {
	r := launchSupervisor(t, rigOptions{root: true, noSetpriv: true})
	select {
	case <-r.done:
		if r.exit == nil {
			t.Fatal("the supervisor exited 0")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a root supervisor with no setpriv is still running")
	}
	if r.h.SupervisorPresent() {
		t.Error("it announced itself before refusing")
	}
}

// TestTheSupervisorRefusesAFileTheGeneratorNeverWrites: each tree is the one
// that applied, plus one thing the generator does not write. The running server
// keeps running, nothing restarts, and the detail names the entry.
func TestTheSupervisorRefusesAFileTheGeneratorNeverWrites(t *testing.T) {
	r := startSupervisor(t)
	if st := r.generation(t, HandoffApply, map[string]string{"radiusd.conf": "good"}); !st.Applied() {
		t.Fatalf("the control tree: %+v", st)
	}

	for what, tc := range map[string]struct {
		files map[string]string
		names string
	}{
		"an exec module":                 {map[string]string{"mods-available/exec": "exec {}"}, "mods-available/exec"},
		"a link to an unlisted module":   {map[string]string{"mods-enabled/exec": "-> ../mods-available/exec"}, "mods-enabled/exec"},
		"an enabled module never listed": {map[string]string{"mods-enabled/exec": "exec {}"}, "mods-enabled/exec"},
		"a link out of the tree":         {map[string]string{"mods-enabled/sql": "-> /etc/shadow"}, "mods-enabled/sql"},
		"a link to another module":       {map[string]string{"mods-available/sql": "sql {}", "mods-enabled/rest": "-> ../mods-available/sql"}, "mods-enabled/rest"},
		"a site linked as a module":      {map[string]string{"sites-available/default": "server {}", "mods-enabled/default": "-> ../sites-available/default"}, "mods-enabled/default"},
		"an unknown top-level file":      {map[string]string{"policy.conf": "x"}, "policy.conf"},
		"an unknown directory":           {map[string]string{"mods-config/files/authorize": "x"}, "mods-config/files"},
		"a name with a space":            {map[string]string{"mods-available/sql x": "x"}, "mods-available/sql x"},
		"a hidden file":                  {map[string]string{".radiusd.conf": "x"}, ".radiusd.conf"},
		"radiusd.conf as a link":         {map[string]string{"radiusd.conf": "-> /etc/passwd"}, "radiusd.conf"},
	} {
		t.Run(what, func(t *testing.T) {
			files := map[string]string{"radiusd.conf": "evil"}
			for k, v := range tc.files {
				files[k] = v
			}
			st := r.generation(t, HandoffApply, files)
			if st.Applied() || st.Restarted {
				t.Fatalf("%+v: want failed, and the running server untouched", st)
			}
			if !strings.Contains(st.Detail, "holds "+tc.names+",") || !strings.Contains(st.Detail, "not a file Foyer generates") {
				t.Errorf("detail %q does not name %s", st.Detail, tc.names)
			}
		})
	}

	// No radiusd.conf at all is a tree with nothing to serve.
	if st := r.generation(t, HandoffApply, map[string]string{"clients.conf": "x"}); st.Applied() {
		t.Errorf("a tree with no radiusd.conf: %+v", st)
	}
	if got := strings.Join(r.servedTrees(t), " "); got != "good" {
		t.Errorf("radiusd served %q; want only the control tree, never one refused", got)
	}
}

// TestTheSupervisorAllowsExactlyTheGeneratedTree holds supervise.sh's allowlist
// to the generator, in both directions: every file, directory and link a tree
// can hold (EAP-TLS on, with the material cmd/foyer writes beside it) is on the
// list, and nothing is on the list that no tree holds. It then applies that real
// tree, and the EAP-off one, through the supervisor.
func TestTheSupervisorAllowsExactlyTheGeneratedTree(t *testing.T) {
	cfg := testConfig()
	eapConfig(&cfg)
	dir := t.TempDir()
	if err := NewGenerator(cfg).Generate(dir); err != nil {
		t.Fatal(err)
	}
	// cmd/foyer's generateRaddbInto writes the EAP-TLS material here (eapDir).
	eap := filepath.Join(dir, "certs", "foyer-eap")
	if err := os.MkdirAll(eap, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"server.pem", "server.key", "ca.pem"} {
		if err := os.WriteFile(filepath.Join(eap, n), []byte("material"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	var files, dirs, links []string
	links2 := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			links = append(links, rel)
			links2[rel] = target
		case d.IsDir():
			dirs = append(dirs, rel)
		default:
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile("../../lab/blue-green/supervise.sh")
	if err != nil {
		t.Fatal(err)
	}
	list := func(name string) []string {
		m := regexp.MustCompile(`(?s)\n` + name + `="([^"]*)"`).FindSubmatch(script)
		if m == nil {
			t.Fatalf("supervise.sh has no %s", name)
		}
		return strings.Fields(string(m[1]))
	}
	same := func(what string, got, want []string) {
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: supervise.sh allows %q, the generator writes %q", what, got, want)
		}
	}
	same("files", list("TREE_FILES"), files)
	same("directories", list("TREE_DIRS"), dirs)
	for _, l := range links {
		kind, name, _ := strings.Cut(l, "/")
		want := "../" + strings.TrimSuffix(kind, "-enabled") + "-available/" + name
		if links2[l] != want || !slices.Contains(files, strings.TrimPrefix(want, "../")) {
			t.Errorf("the generator writes %s -> %s, which is not the one link shape supervise.sh allows", l, links2[l])
		}
	}

	r := startSupervisor(t)
	for _, eapOn := range []bool{true, false} {
		c := testConfig()
		if eapOn {
			eapConfig(&c)
		}
		gen, err := r.h.Stage(func(d string) error {
			if err := NewGenerator(c).Generate(d); err != nil {
				return err
			}
			if !eapOn {
				return nil
			}
			return os.CopyFS(filepath.Join(d, "certs", "foyer-eap"), os.DirFS(eap))
		})
		if err != nil {
			t.Fatal(err)
		}
		st := r.await(t, gen)
		if !st.Applied() {
			t.Errorf("a generated tree (EAP-TLS %v) was refused: %+v", eapOn, st)
		}
	}
}
