package api

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The body path's share of the memory plan.
//
// ── the failure it prevents ──────────────────────────────────────────────────
//
// A console body ceiling configurable up to a fixed 16 MiB, in front of a constant
// 4 MiB in-flight budget that *clamps* any larger request to 4 MiB "so it runs
// alone", lets one staff member with a raised ceiling send one request that holds
// 437 MiB live, in a container capped at 320M. Two separate failures: a
// configurable value that the container cannot hold, and a request weighed at a
// figure smaller than what it costs.
//
// ── what the field does ───────────────────────────────────────────────────────
//
// Nobody ships a body limit that is a function of the box it runs on *and* lets an
// operator raise it past that: nginx's `client_max_body_size` is a flat figure, but
// nginx streams bodies to a temp file rather than decoding them; Keycloak and
// authentik put their request-size knobs behind the deployment (env / proxy), not
// the admin UI. What the Go runtime itself converged on is the shape here: one
// memory limit (`GOMEMLIMIT`, or the cgroup's) and every consumer sized from it.
// `cmd/foyer/memory.go` does that for argon2id; this does it for bodies.
//
// ── the plan ─────────────────────────────────────────────────────────────────
//
// From the soft limit S (GOMEMLIMIT, else 80 % of the cgroup, else the shipped
// 256 MiB) and what the argon2id slots hold (`HashBytes`, half of S by the memory
// plan):
//
//   - a tenth of S is left for the rest of the process — pools, TLS, the policy
//     engine;
//   - **the body path gets the rest**, as decoded bytes: a small budget (at most
//     8 MiB) for bodies within the anonymous ceiling — the portal's and every
//     sign-in — and a large one for signed-in bodies over it;
//   - **no ceiling may be configured above what its budget can decode**: the
//     console's at most the large budget over the per-byte cost of a string body
//     (rounded down to a whole MiB, never above 16 MiB), the portal's and
//     the anonymous at most the small budget over the same (never above 128 KiB).
//     The bounds the console is offered are these, and a stored value from a
//     larger container is clamped when it is read.
//
// For the shipped container (320M, GOMEMLIMIT 256 MiB, two slots): 102 MiB of
// decoded bodies, 8 small and 94 large, a console ceiling of at most 7 MiB, and the
// contract's largest hero photograph (6.7 MiB of base64, ~88 MiB decoded) fits the
// large budget alone. `TestTheShippedPlanHoldsTheContractsLargestBody` pins that.
//
// ── weighing a body at its real size ─────────────────────────────────────────
//
// Decoded cost depends on shape, not only on length. Measured in this package
// (peak live heap through the validator and the generated decoder, one request):
// a 1 MiB JSON string 11.9–12.7×; the hero photograph 11.5–13.2× (the memory
// store keeps the image); 1 MiB of `[0,0,…]` 34–36.5×; of `[{},{},…]` 37.4×. So
// the weight is `decodePerByte` per byte plus `decodePerValue` per JSON value
// (every `,` `:` `[` `{` outside a string; `&` and `=` in a form), which is at or
// above every figure measured. `limitBody` reserves the worst case the length
// allows — `decodeWorstPerByte` per byte, capped at the budget — so a request never
// waits holding part of what it needs; once the validator has read the body it is
// weighed, the excess is handed back, and **a body whose real weight exceeds its
// whole budget is refused `413`**, never run "alone" at a smaller figure.

// MemoryPlan is the process's memory budget as `cmd/foyer` decided it.
type MemoryPlan struct {
	// SoftLimit is the Go soft limit in bytes; zero is none known, which plans
	// for the shipped 256 MiB.
	SoftLimit int64
	// HashBytes is what the argon2id slots may hold at once; zero is half of
	// SoftLimit, the memory plan's own ratio.
	HashBytes int64
}

const (
	shippedSoftLimit int64 = 256 << 20

	decodePerByte      int64 = 13
	decodePerValue     int64 = 64
	decodeWorstPerByte       = decodePerByte + decodePerValue // one value per byte: `[[[[`

	smallBodyShareMax int64 = 8 << 20
	minBodyShare      int64 = 2 << 20
)

// bodyPlan is the decision, as data.
type bodyPlan struct {
	large, small int64 // decoded bytes in flight

	consoleMax, consoleMin int64 // bounds of the console ceiling
	smallMax               int64 // bound of the portal and anonymous ceilings
}

func planBodies(m MemoryPlan) bodyPlan {
	s := m.SoftLimit
	if s <= 0 {
		s = shippedSoftLimit
	}
	hash := m.HashBytes
	if hash <= 0 {
		hash = s / 2
	}
	share := s - hash - s/10
	if share < minBodyShare {
		share = minBodyShare
	}
	small := share / 4
	if small > smallBodyShareMax {
		small = smallBodyShareMax
	}
	p := bodyPlan{large: share - small, small: small}

	p.consoleMax = min(int64(consoleBytesMax), (p.large/decodePerByte)&^(1<<20-1))
	if p.consoleMax < AnonymousBodyLimit {
		p.consoleMax = AnonymousBodyLimit
	}
	p.consoleMin = min(int64(consoleBytesMin), p.consoleMax)
	p.smallMax = min(int64(portalBytesMax), (p.small/decodePerByte)&^(1<<10-1))
	if p.smallMax < portalBytesMin {
		p.smallMax = portalBytesMin
	}
	return p
}

// clamp is a stored or default set of ceilings held to what this plan can decode.
func (p bodyPlan) clamp(c gen.BodyCeilings) gen.BodyCeilings {
	c.ConsoleBytes = int(min(int64(c.ConsoleBytes), p.consoleMax))
	c.PortalBytes = int(min(int64(c.PortalBytes), p.smallMax))
	c.AnonymousBytes = int(min(int64(c.AnonymousBytes), p.smallMax))
	return c
}

// bodyWeight is a body's decoded cost, estimated from its bytes: see above.
func bodyWeight(body []byte, contentType string) int64 {
	values := int64(0)
	if strings.HasPrefix(strings.ToLower(contentType), "application/json") || strings.Contains(strings.ToLower(contentType), "+json") {
		inString, escaped := false, false
		for _, c := range body {
			switch {
			case escaped:
				escaped = false
			case inString:
				if c == '\\' {
					escaped = true
				} else if c == '"' {
					inString = false
				}
			case c == '"':
				inString = true
			case c == ',' || c == ':' || c == '[' || c == '{':
				values++
			}
		}
	} else {
		values = int64(bytes.Count(body, []byte{'&'}) + bytes.Count(body, []byte{'='}))
	}
	// values ≤ len(body), so the weight is never above the worst case limitBody
	// reserved for this length.
	return int64(len(body))*decodePerByte + values*decodePerValue
}

// Describe is the body plan in one line, for the start-up log beside the memory
// plan it is derived from.
func (m MemoryPlan) Describe() string {
	p := planBodies(m)
	hero := "fits"
	if heroImageBodyLimit*decodePerByte > p.large {
		hero = "DOES NOT FIT: hero photograph uploads will be refused 413 on this container"
	}
	return fmt.Sprintf("bodies: %d MiB decoded in flight (%d MiB signed-in, %d MiB every other); "+
		"console ceiling at most %d KiB, portal and anonymous at most %d KiB; the contract's largest hero photograph %s",
		(p.large+p.small)>>20, p.large>>20, p.small>>20, p.consoleMax>>10, p.smallMax>>10, hero)
}
