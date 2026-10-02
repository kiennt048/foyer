package api

import "slices"

// The operations this package has not built, named.
//
// ── why a gap is louder than a success here ──────────────────────────────────
//
// `internal/api` was replaced rather than repaired because eight of its handlers
// returned convincing JSON that had never touched a store. `overview`, `sites`,
// `controllers`, `nas`, `vouchers`, `voucher-batches`, `accounts` and `sessions`
// all answered `200` with a hardcoded literal, and nothing about the response
// said so. A caller — a person clicking through the console, a test, the next
// reader — could not tell a working endpoint from a decorated stub.
//
// So the rule stands: `501` and a problem document, never a plausible body.
//
// ── the list is empty, and that is the change ────────────────────────────────
//
// It held five names — `getPortalContext`, `redeemVoucher`, `acceptTerms`,
// `loginWithAccount` and `getPortalStatus` — with a reason that was specific and
// correct at the time:
//
//	Each needs a real `VerifyPresence`, a MAC-bound grant store, and the
//	portal's own listener — a fake answering "present" would make the security
//	test pass.
//
// All three exist now. `internal/nas` verifies presence against a real UniFi
// controller including the still-unauthorized check; `internal/portal` holds the
// grant store behind an interface with the schema it needs written down;
// `PortalHandler` is the second surface, split from the console's by the
// contract's own `portal` tag. `handlers_portal.go` implements the five.
//
// **The refusal has moved rather than disappeared.** A `Server` whose portal
// ports are not wired still answers `501` on all five — the same honest gap every
// other unwired port produces — and `New` refuses outright to build a *half*-wired
// one, because a grant store with no driver seam is a portal recording grants for
// devices nothing confirmed. `notimplemented_test.go` pins the empty list in both
// directions, so a name rejoining it is a visible change rather than a quiet one.
var notImplementedOperations = []string{}

// NotImplemented reports whether an operationId is one this package refuses
// unconditionally.
//
// Exported so the differential harness can list the gap rather than discover it
// as a diff. It answers about the *package*, not about one server: an operation
// that answers `501` only because a port is nil is not listed here, because
// wiring the port makes it work and nothing about the code changed.
func NotImplemented(operationID string) bool {
	return slices.Contains(notImplementedOperations, operationID)
}

// NotImplementedOperations is the set, for a caller that wants to print it.
func NotImplementedOperations() []string { return slices.Clone(notImplementedOperations) }
