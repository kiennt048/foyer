package api

import (
	"fmt"
	"sync"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity/otp"
)

// Bounds the console is sent rather than compiling in.
//
// The settings screens put a technical knob in a collapsed "Advanced" section with
// its safe default, a "reset to default" and its range stated. A range restated in
// the console is a second copy that drifts, and a "reset" that guesses a default
// resets to the wrong figure — one `default` for four throttle buckets whose shipped
// figures differ is exactly that. So every figure an operator can edit arrives with
// its range and default from the server.
//
// Where the range is a **schema** fact — `PhoneAbuseLimitsUpdate` and
// `PhoneSpendCapUpdate` declare `minimum`/`maximum` and request validation enforces
// them — it is read here from the embedded contract, the one authority, rather than
// restated in Go: the answer and the refusal cannot disagree because they are the
// same number read from the same document.

var (
	contractRangesOnce sync.Once
	contractRangesErr  error
	contractRanges     map[string][2]int
)

// contractRange is `schema.property`'s declared integer range. It panics on a
// property the contract does not declare with both bounds: that is a contract edit
// that removed a bound the console relies on, and the handler test for each caller
// fails on it long before a request could.
func contractRange(schema, property string) (minimum, maximum int) {
	contractRangesOnce.Do(func() {
		spec, err := gen.GetSwagger()
		if err != nil {
			contractRangesErr = err
			return
		}
		contractRanges = map[string][2]int{}
		for name, ref := range spec.Components.Schemas {
			if ref == nil || ref.Value == nil {
				continue
			}
			for prop, p := range ref.Value.Properties {
				if p == nil || p.Value == nil || p.Value.Min == nil || p.Value.Max == nil {
					continue
				}
				contractRanges[name+"."+prop] = [2]int{int(*p.Value.Min), int(*p.Value.Max)}
			}
		}
	})
	if contractRangesErr != nil {
		panic(fmt.Sprintf("api: reading the embedded contract: %v", contractRangesErr))
	}
	r, ok := contractRanges[schema+"."+property]
	if !ok {
		panic(fmt.Sprintf("api: the contract declares no integer range for %s.%s", schema, property))
	}
	return r[0], r[1]
}

// phoneAbuseBounds is each abuse limit's range, from the contract, and its product
// default, from the package that enforces it.
func phoneAbuseBounds() gen.PhoneAbuseLimitBounds {
	d := PhoneLimits(otp.DefaultLimits)
	b := func(prop string, def int) gen.NetworkBound {
		lo, hi := contractRange("PhoneAbuseLimitsUpdate", prop)
		return gen.NetworkBound{Minimum: lo, Maximum: hi, Default: def}
	}
	return gen.PhoneAbuseLimitBounds{
		PerNumberPerHour:  b("perNumberPerHour", d.PerNumberPerHour),
		PerNumberPerDay:   b("perNumberPerDay", d.PerNumberPerDay),
		PerDevicePerHour:  b("perDevicePerHour", d.PerDevicePerHour),
		PerAddressPerHour: b("perAddressPerHour", d.PerAddressPerHour),
		CodeMinutes:       b("codeMinutes", d.CodeMinutes),
		MaxAttempts:       b("maxAttempts", d.MaxAttempts),
	}
}

// withAbuseBounds is the controls a store returned, with the ranges attached. The
// store keeps what was saved; the ranges are the API's to state.
func withAbuseBounds(c gen.PhoneAbuseControls) gen.PhoneAbuseControls {
	c.Bounds = phoneAbuseBounds()
	return c
}

// withSpendCapBounds is the cap a store returned, with `PhoneSpendCapUpdate`'s range.
func withSpendCapBounds(c gen.PhoneSpendCap) gen.PhoneSpendCap {
	lo, hi := contractRange("PhoneSpendCapUpdate", "dailyLimit")
	c.Bounds.Minimum, c.Bounds.Maximum = lo, hi
	return c
}

// networkDefaults is every throttle's and ceiling's shipped figure, member by member,
// with the ceilings held to this process's memory plan (memplan.go) exactly as
// networkBounds' defaults are: a "reset to default" the plan would refuse is not a
// default.
func (s *Server) networkDefaults() gen.NetworkSettingDefaults {
	return gen.NetworkSettingDefaults{Throttles: DefaultThrottles(), BodyCeilings: s.bodyPlan.clamp(DefaultBodyCeilings())}
}

// coaBounds is what `updateNasCoaTiming` accepts — the same constants its validation
// reads — with the defaults a device without its own timing uses.
func coaBounds() gen.CoaTimingBounds {
	d := coaDefaults()
	return gen.CoaTimingBounds{
		TimeoutMs: gen.NetworkBound{Minimum: coaTimeoutMinMs, Maximum: coaTimeoutMaxMs, Default: d.TimeoutMs},
		Retries:   gen.NetworkBound{Minimum: coaRetriesMin, Maximum: coaRetriesMax, Default: d.Retries},
	}
}
