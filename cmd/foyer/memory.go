package main

import (
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/identity"
)

// The memory budget, decided once before anything is served.
//
// Every argon2id verification costs 64 MiB and the container is capped at 320M
// (`deploy/compose.yaml`). With nothing bounding how many run at once, about five
// parallel sign-ins exhaust the box, and the process that dies is also the one
// answering FreeRADIUS's Policy API.
//
// Two numbers come out of here and they are the same budget read two ways:
//
//   - **The Go soft limit** (`GOMEMLIMIT`). A Go process under a cgroup limit
//     with no soft limit does not collect harder as it approaches the wall — it is
//     OOM-killed. `deploy/compose.yaml` sets `GOMEMLIMIT=256MiB` inside its 320M
//     and that is honoured as given; a deployment that sets neither but runs in a
//     cgroup gets 80 % of the cgroup's limit, which is the same ratio.
//   - **The argon2id concurrency** (`identity.SetHashConcurrency`): half of the
//     soft limit, in 64 MiB slots, between one and four. 256 MiB → two. The other
//     half is the rest of the process — pools, TLS, the policy engine, and the
//     garbage a finished hash leaves before the collector returns it.
//
// No new configuration variable. The container's own limit is the fact, and a
// knob beside it would be a second copy of it that can disagree.

// maxHashSlots caps the concurrency on a large box: past four, a flood is a CPU
// problem rather than a memory one, and the Policy API shares the CPU.
const maxHashSlots = 4

// cgroupLimitFiles are where a container's memory ceiling is written: cgroup v2,
// then v1. "max" (v2) and an absurd v1 value both mean "no limit".
var cgroupLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

// readCgroupLimit returns the container's memory ceiling in bytes, or 0.
func readCgroupLimit(read func(string) ([]byte, error)) int64 {
	for _, f := range cgroupLimitFiles {
		b, err := read(f)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "" || s == "max" {
			return 0
		}
		n, err := strconv.ParseInt(s, 10, 64)
		// cgroup v1 reports "unlimited" as a page-rounded MaxInt64.
		if err != nil || n <= 0 || n >= math.MaxInt64/2 {
			return 0
		}
		return n
	}
	return 0
}

// memoryPlan is the decision, kept as data so it can be tested without a cgroup.
type memoryPlan struct {
	softLimit    int64 // bytes; 0 = none in force
	setSoftLimit bool  // this process set it, rather than GOMEMLIMIT
	hashSlots    int
}

// planMemory decides. `envLimit` is whether GOMEMLIMIT was given, `current` is
// what the runtime reports in force (math.MaxInt64 when nothing is).
func planMemory(cgroup int64, envLimit bool, current int64) memoryPlan {
	p := memoryPlan{hashSlots: identity.DefaultHashConcurrency}
	switch {
	case envLimit && current > 0 && current < math.MaxInt64:
		p.softLimit = current
	case cgroup > 0:
		p.softLimit = cgroup / 10 * 8
		p.setSoftLimit = true
	}
	if p.softLimit > 0 {
		slots := int(p.softLimit / 2 / int64(identity.HashMemoryBytes))
		if slots < 1 {
			slots = 1
		}
		if slots > maxHashSlots {
			slots = maxHashSlots
		}
		p.hashSlots = slots
	}
	return p
}

// applyMemoryPlan decides, applies, and says what it did. envLimit is whether
// GOMEMLIMIT was given (config.GoMemLimitSet); the runtime has already read it.
// It also records the plan for `api.Config.Memory` (processMemory): the body
// budgets and the highest body ceiling an operator may set are the same budget read
// a third way (internal/api/memplan.go).
func applyMemoryPlan(envLimit bool, logf func(string, ...any)) {
	p := planMemory(readCgroupLimit(os.ReadFile), envLimit, debug.SetMemoryLimit(-1))
	if p.setSoftLimit {
		debug.SetMemoryLimit(p.softLimit)
	}
	identity.SetHashConcurrency(p.hashSlots, identity.DefaultHashWait)
	processMemory = p.forAPI()
	logf("%s", p.describe())
	logf("memory: %s", processMemory.Describe())
}

// processMemory is the plan applyMemoryPlan decided, for `portalServices` to hand
// to `api.New`. Zero before it runs, which `internal/api` plans as the shipped
// container.
var processMemory api.MemoryPlan

// forAPI is the plan as `internal/api` reads it: the soft limit, and what the
// argon2id slots hold at once.
func (p memoryPlan) forAPI() api.MemoryPlan {
	return api.MemoryPlan{SoftLimit: p.softLimit, HashBytes: int64(p.hashSlots) * int64(identity.HashMemoryBytes)}
}

func (p memoryPlan) describe() string {
	limit := "none"
	if p.softLimit > 0 {
		limit = fmt.Sprintf("%d MiB", p.softLimit>>20)
		if p.setSoftLimit {
			limit += " (80% of the container's limit)"
		} else {
			limit += " (GOMEMLIMIT)"
		}
	}
	return fmt.Sprintf("memory: Go soft limit %s; at most %d argon2id verification(s) at once (%d MiB each, waiting at most %s for a slot)",
		limit, p.hashSlots, identity.HashMemoryBytes>>20, identity.DefaultHashWait.Round(time.Second))
}
