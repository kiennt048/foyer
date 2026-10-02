package raddb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheSupervisorHandsRadiusdItsDatabasePassword is the data-plane half of the
// named database password: the tree names FreeRADIUS's password as `$ENV{FOYER_RADIUS_DB_PASSWORD}`
// and the control plane holds none, so the supervisor — the one process in the
// FreeRADIUS container that is given the docker secret — must export it to both the
// `radiusd -XC` check and the serving radiusd. A supervisor that forgot would give
// radiusd an empty password, which fails closed and looks like a database outage.
func TestTheSupervisorHandsRadiusdItsDatabasePassword(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "radius_db_password")
	if err := os.WriteFile(secret, []byte("0123abcdef-from-the-docker-secret\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(dir, "env-seen")
	r := startSupervisor(t,
		"FOYER_RADIUS_DB_PASSWORD=",
		"FOYER_RADIUS_DB_PASSWORD_FILE="+secret,
		"FAKE_ENV="+seen,
	)
	if st := r.generation(t, HandoffApply, map[string]string{"radiusd.conf": "one"}); !st.Applied() {
		t.Fatalf("the generation was not applied: %+v", st)
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("radiusd was never run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		t.Fatalf("radiusd ran %d time(s), want the check and the start: %q", len(lines), b)
	}
	for i, l := range lines {
		if l != "0123abcdef-from-the-docker-secret" {
			t.Fatalf("run %d of radiusd saw FOYER_RADIUS_DB_PASSWORD = %q, want the secret file's contents without its newline", i+1, l)
		}
	}
}
