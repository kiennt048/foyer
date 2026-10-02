package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// Every FreeRADIUS worker is an rlm_sql connection (the generator sizes the pool at
// the worker count), so the console's worker ceiling is a PostgreSQL budget. A
// ceiling of 256 against `max_connections = 80` would exhaust it.
//
// The figures below are restated as literals, deliberately: the red proof sets the
// ceiling to 256, and a test computed from the same constants would move with
// it.

// TestTheWorkerCeilingFitsThePostgresBudget reads the deployment's PostgreSQL limit
// from the file that sets it and holds the ceiling inside what is left of it.
func TestTheWorkerCeilingFitsThePostgresBudget(t *testing.T) {
	b, err := os.ReadFile("../../deploy/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*-\s*max_connections=(\d+)\s*$`).FindAllSubmatch(b, -1)
	if len(m) != 1 {
		t.Fatalf("deploy/compose.yaml sets max_connections %d times; want exactly once", len(m))
	}
	maxConnections, _ := strconv.Atoi(string(m[0][1]))
	if maxConnections != api.PostgresMaxConnections {
		t.Fatalf("deploy/compose.yaml sets max_connections=%d, internal/api budgets against %d",
			maxConnections, api.PostgresMaxConnections)
	}

	// superuser_reserved_connections 3, the control plane's pool 25, the reaper and
	// the retention sweep 2 each, two foyerctl runs at 2 each.
	others := 3 + 25 + 2 + 2 + 2*2
	if room := maxConnections - others; api.RadiusMaxServersCeiling > room {
		t.Fatalf("the console allows %d FreeRADIUS workers, and so %d rlm_sql connections; PostgreSQL has %d left",
			api.RadiusMaxServersCeiling, api.RadiusMaxServersCeiling, room)
	}
	if api.RadiusMaxServersCeiling < api.DefaultRadiusMaxServers {
		t.Fatalf("the ceiling %d is below the default %d", api.RadiusMaxServersCeiling, api.DefaultRadiusMaxServers)
	}
}

// TestTheConsoleRefusesAWorkerCountPostgresCannotServe is the same bound at the
// operation: advertised, and enforced.
func TestTheConsoleRefusesAWorkerCountPostgresCannotServe(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)

	var rs gen.RadiusServerSettings
	_ = json.Unmarshal([]byte(readAll(t, r.do(http.MethodGet, "/network/radius", nil))), &rs)
	if rs.MaxServersBound.Maximum != 44 {
		t.Errorf("the console advertises at most %d workers, want 44 (80 − 3 − 25 − 4 − 4)", rs.MaxServersBound.Maximum)
	}

	for _, n := range []int{45, 80, 256} {
		resp := r.do(http.MethodPut, "/network/radius", map[string]any{"maxServers": n, "eapTlsEnabled": false})
		if f, _ := faultOf(t, readAll(t, resp), "maxServers"); f != gen.FieldFaultAboveMaximum {
			t.Errorf("%d workers: %d %q, want above-maximum", n, resp.StatusCode, f)
		}
	}
	if resp := r.do(http.MethodPut, "/network/radius", map[string]any{"maxServers": 44, "eapTlsEnabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("44 workers, the ceiling itself: %d %s", resp.StatusCode, readAll(t, resp))
	}
}
