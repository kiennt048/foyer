package deploycheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A lab database migrated under an older numbering of the migrations is a trap: goose
// records versions by number, so it refuses the gap or skips a renumbered file as
// done (lab/README.md). The way out has to be one obvious command, and the failure
// has to name it. Run through make itself, with lab-raddb taken as up to date (-o)
// and a docker that records what it was asked and fails `up`.
func TestLabResetDropsTheDatabaseAndAFailedUpNamesIt(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	write(t, filepath.Join(dir, "docker"), `#!/bin/sh
echo "docker $*" >> "$LOG"
case "$*" in *" up -d"*) echo "service \"migrate\" didn't complete successfully: exit 1" >&2; exit 1 ;; esac
exit 0
`, 0o755)
	cmd := exec.Command("make", "-o", "lab-raddb", "lab-reset")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "LOG="+log)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("lab-reset succeeded although `up` failed:\n%s", out)
	}
	calls := strings.Split(strings.TrimSpace(read(t, log)), "\n")
	down, up := index(calls, "lab/docker-compose.yml down -v --remove-orphans"), index(calls, "lab/docker-compose.yml up -d")
	if down < 0 || up < 0 || down > up {
		t.Fatalf("lab-reset must remove the volumes and then bring the lab up; docker was asked:\n%s", strings.Join(calls, "\n"))
	}
	if !strings.Contains(string(out), "make lab-reset") || !strings.Contains(string(out), "missing migrations") {
		t.Errorf("a failed lab-up does not name the renumbering or lab-reset:\n%s", out)
	}
	if b, err := os.ReadFile(filepath.Join(repoRoot, "lab", "README.md")); err != nil || !strings.Contains(string(b), "make lab-reset") {
		t.Errorf("lab/README.md does not document lab-reset (%v)", err)
	}
}
