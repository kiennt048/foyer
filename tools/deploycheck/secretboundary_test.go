package deploycheck

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Owning the control plane's container must not mean holding every database login —
// the stale-session reaper's and the retention role's, and FreeRADIUS's own
// password, which the control plane only needs to write into the tree it generates.
// So the maintenance jobs run in a `maintenance` service and the tree names
// FreeRADIUS's password as $ENV{…}. These tests read the two compose
// files the way compose does — service by service — and hold each container to the
// logins it needs.

type composeService struct {
	secrets []string
	env     map[string]string
	command string
}

var (
	serviceRE = regexp.MustCompile(`^  ([a-z][a-z0-9-]*):\s*$`)
	envRE     = regexp.MustCompile(`^      ([A-Z_][A-Z0-9_]*):\s*(.*)$`)
	itemRE    = regexp.MustCompile(`^      - ([a-z_]+)\s*$`)
	keyRE     = regexp.MustCompile(`^    ([a-z_]+):(.*)$`)
)

// services reads a compose file's `services:` block: each service's secrets, its
// environment and its command. Comments are skipped; nothing else is interpreted.
func services(t *testing.T, rel string) map[string]*composeService {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*composeService{}
	var cur *composeService
	section, in := "", false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
			continue
		}
		if line == "services:" {
			in = true
			continue
		}
		if !in {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			in = false // the next top-level key
			continue
		}
		if m := serviceRE.FindStringSubmatch(line); m != nil {
			cur = &composeService{env: map[string]string{}}
			out[m[1]] = cur
			section = ""
			continue
		}
		if cur == nil {
			continue
		}
		if m := keyRE.FindStringSubmatch(line); m != nil {
			section = m[1]
			if section == "command" {
				cur.command = strings.TrimSpace(m[2])
			}
			continue
		}
		switch section {
		case "secrets":
			if m := itemRE.FindStringSubmatch(line); m != nil {
				cur.secrets = append(cur.secrets, m[1])
			}
		case "environment":
			if m := envRE.FindStringSubmatch(line); m != nil {
				cur.env[m[1]] = strings.TrimSpace(m[2])
			}
		}
	}
	if len(out) < 3 {
		t.Fatalf("%s: read %d services; the reader is not reading this file", rel, len(out))
	}
	return out
}

func TestTheControlPlaneHoldsNoLoginItDoesNotNeed(t *testing.T) {
	s := services(t, "deploy/compose.yaml")
	foyer, maint, fr := s["foyer"], s["maintenance"], s["freeradius"]
	if foyer == nil || maint == nil || fr == nil {
		t.Fatalf("deploy/compose.yaml has services %v; want foyer, maintenance and freeradius", keys(s))
	}

	for _, forbidden := range []string{"reaper_database_url", "retention_database_url", "radius_db_password",
		"reaper_db_password", "retention_db_password", "postgres_password", "owner_database_url"} {
		if slices.Contains(foyer.secrets, forbidden) {
			t.Errorf("the control plane is given the %s secret: that login is not its own", forbidden)
		}
	}
	for _, forbidden := range []string{"FOYER_REAPER_DATABASE_URL_FILE", "FOYER_RETENTION_DATABASE_URL_FILE", "FOYER_POSTGRES_PASSWORD_FILE", "FOYER_POSTGRES_PASSWORD"} {
		if _, ok := foyer.env[forbidden]; ok {
			t.Errorf("the control plane's environment carries %s", forbidden)
		}
	}
	for k, want := range map[string]string{
		"FOYER_MAINTENANCE_URL":             "http://maintenance:8090",
		"FOYER_RADDB_POSTGRES_PASSWORD_ENV": "FOYER_RADIUS_DB_PASSWORD",
		"FOYER_MAINTENANCE_TOKEN_FILE":      "/run/secrets/maintenance_token",
	} {
		if got := foyer.env[k]; got != want {
			t.Errorf("the control plane's %s = %q, want %q", k, got, want)
		}
	}

	if maint.command != `["foyer", "maintenance"]` {
		t.Errorf("the maintenance service runs %s, want the same image's `foyer maintenance`", maint.command)
	}
	for _, need := range []string{"reaper_database_url", "retention_database_url", "database_url", "maintenance_token"} {
		if !slices.Contains(maint.secrets, need) {
			t.Errorf("the maintenance service is not given %s", need)
		}
	}
	for _, forbidden := range []string{"kek", "radius_db_password", "owner_database_url", "postgres_password"} {
		if slices.Contains(maint.secrets, forbidden) {
			t.Errorf("the maintenance service is given %s, which it does not need", forbidden)
		}
	}
	if _, ok := maint.env["FOYER_KEK_FILE"]; ok {
		t.Error("the maintenance service is given FOYER_KEK_FILE")
	}

	if !slices.Contains(fr.secrets, "radius_db_password") {
		t.Error("FreeRADIUS is not given its own database password, and the tree now reads it from FreeRADIUS's environment")
	}
}

// The lab is the model for the shape (deploy/compose.yaml's header), so it splits
// the same way, with literals where the deployment has secrets.
func TestTheLabSplitsTheMaintenanceJobsTheSameWay(t *testing.T) {
	s := services(t, "lab/docker-compose.yml")
	backend, maint, fr := s["foyer-backend"], s["foyer-maintenance"], s["freeradius"]
	if backend == nil || maint == nil || fr == nil {
		t.Fatalf("lab/docker-compose.yml has services %v", keys(s))
	}
	for _, forbidden := range []string{"FOYER_REAPER_DATABASE_URL", "FOYER_RETENTION_DATABASE_URL", "FOYER_POSTGRES_PASSWORD"} {
		if _, ok := backend.env[forbidden]; ok {
			t.Errorf("the lab control plane carries %s", forbidden)
		}
	}
	if backend.env["FOYER_MAINTENANCE_URL"] == "" || backend.env["FOYER_RADDB_POSTGRES_PASSWORD_ENV"] != "FOYER_RADIUS_DB_PASSWORD" {
		t.Errorf("the lab control plane is not wired to the maintenance container and the named password: %v", backend.env)
	}
	for _, need := range []string{"FOYER_REAPER_DATABASE_URL", "FOYER_RETENTION_DATABASE_URL", "FOYER_MAINTENANCE_TOKEN"} {
		if maint.env[need] == "" {
			t.Errorf("the lab maintenance container has no %s", need)
		}
	}
	if maint.env["FOYER_MAINTENANCE_TOKEN"] != backend.env["FOYER_MAINTENANCE_TOKEN"] {
		t.Error("the lab's two containers carry different maintenance tokens")
	}
	if fr.env["FOYER_RADIUS_DB_PASSWORD"] == "" {
		t.Error("the lab FreeRADIUS has no FOYER_RADIUS_DB_PASSWORD for the tree to read")
	}
}

func keys(m map[string]*composeService) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
