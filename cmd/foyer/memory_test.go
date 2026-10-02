package main

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// TestTheMemoryPlanFitsTheDeployedContainer: the two ways the deployment can
// state its budget — `GOMEMLIMIT=256MiB` inside 320M, as `deploy/compose.yaml`
// does, or only the cgroup — both come to two argon2id slots, 128 MiB, which
// is half of the soft limit and leaves the rest for the process.
func TestTheMemoryPlanFitsTheDeployedContainer(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name      string
		cgroup    int64
		env       bool
		current   int64
		wantSlots int
		wantSet   bool
	}{
		{"deploy: GOMEMLIMIT 256MiB in 320M", 320 * mib, true, 256 * mib, 2, false},
		{"a cgroup of 320M and no GOMEMLIMIT", 320 * mib, false, math.MaxInt64, 2, true},
		{"a tiny container still gets one", 64 * mib, false, math.MaxInt64, 1, true},
		{"a large box is capped at four", 16 << 30, false, math.MaxInt64, 4, true},
		{"no limit anywhere keeps the default", 0, false, math.MaxInt64, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planMemory(tc.cgroup, tc.env, tc.current)
			if p.hashSlots != tc.wantSlots || p.setSoftLimit != tc.wantSet {
				t.Fatalf("plan = %+v, want %d slots, setSoftLimit=%v", p, tc.wantSlots, tc.wantSet)
			}
		})
	}
}

func TestReadCgroupLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  int64
	}{
		{"v2 limit", map[string]string{"/sys/fs/cgroup/memory.max": "335544320\n"}, 335544320},
		{"v2 unlimited", map[string]string{"/sys/fs/cgroup/memory.max": "max\n"}, 0},
		{"v1 limit", map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "335544320"}, 335544320},
		{"v1 unlimited", map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712"}, 0},
		{"neither", map[string]string{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(f string) ([]byte, error) {
				if s, ok := tc.files[f]; ok {
					return []byte(s), nil
				}
				return nil, errors.New("absent")
			}
			if got := readCgroupLimit(read); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTheBodyPlanIsTheMemoryPlan: `internal/api` sizes its body budgets and the
// highest ceiling an operator may set from what this file decided,
// so the plan it is handed must be the soft limit and the argon2id slots' bytes —
// not zero, which would plan for the shipped container whatever this one is.
func TestTheBodyPlanIsTheMemoryPlan(t *testing.T) {
	const mib = 1 << 20
	p := planMemory(160*mib, false, math.MaxInt64).forAPI()
	if p.SoftLimit != 128*mib || p.HashBytes != 64*mib {
		t.Fatalf("a 160M cgroup hands internal/api %+v, want a 128 MiB soft limit and one 64 MiB slot", p)
	}
	if d := planMemory(320*mib, true, 256*mib).forAPI().Describe(); !strings.Contains(d, "photograph fits") {
		t.Fatalf("the deployed container's body plan does not fit the contract's hero photograph: %s", d)
	}
}
