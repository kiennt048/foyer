package api

// SetBodyBudget replaces the in-flight body budget, so a test can make it small
// enough to fill with two requests.
func (s *Server) SetBodyBudget(n int64) { s.bodies = newBodyBudget(n) }

// SetSmallBodyBudget replaces the budget every other body draws on.
func (s *Server) SetSmallBodyBudget(n int64) { s.smallBodies = newBodyBudget(n) }

// PlanBodies exposes the body plan's figures (memplan.go) to the external tests.
func PlanBodies(m MemoryPlan) (large, small, consoleMax, smallMax int64) {
	p := planBodies(m)
	return p.large, p.small, p.consoleMax, p.smallMax
}

// HeroImageBodyLimit is the contract-derived ceiling of putPortalHeroImage.
func HeroImageBodyLimit() int64 { return heroImageBodyLimit }

// DecodePerByte is the weight of one body byte, the per-byte cost of a string.
const DecodePerByte = decodePerByte
