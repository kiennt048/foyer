package raddb

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	const real = `radiusd: FreeRADIUS Version 3.2.10, for host aarch64-unknown-linux-gnu, built on Jul 24 2026 at 06:21:14
FreeRADIUS Version 3.2.10
Copyright (C) 1999-2026 The FreeRADIUS server project and contributors
`
	v, err := ParseVersion(real)
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if v != (Version{3, 2, 10}) {
		t.Errorf("got %s, want 3.2.10", v)
	}

	if _, err := ParseVersion("bash: radiusd: command not found"); err == nil {
		t.Errorf("ParseVersion accepted output with no version in it")
	}
}

// TestCheckDialectIsARangeNotAPin is the point of the whole file.
//
// An equality check is simpler and is wrong: 3.2.10 and 3.2.11 exist, and
// pinning one patch release turns every upstream security fix into a Foyer
// outage while the operator waits for a constant to be bumped.
func TestCheckDialectIsARangeNotAPin(t *testing.T) {
	cases := []struct {
		v       Version
		ok      bool
		mention string
	}{
		{Version{3, 0, 27}, false, "3.2 dialect"},   // what alpine ships
		{Version{3, 1, 0}, false, "3.2 dialect"},    //
		{Version{3, 2, 5}, false, "security"},       // what Ubuntu 24.04 ships
		{Version{3, 2, 7}, false, "security"},       // what Debian trixie ships
		{Version{3, 2, 9}, true, ""},                // the floor, inclusive
		{Version{3, 2, 10}, true, ""},               // the release this was built against
		{Version{3, 2, 11}, true, ""},               // a future security fix must not need a code change
		{Version{3, 2, 99}, true, ""},               //
		{Version{3, 3, 0}, false, "not known"},      //
		{Version{4, 0, 0}, false, "second emitter"}, // a different configuration language
	}

	for _, tc := range cases {
		err := CheckDialect(tc.v)
		if tc.ok {
			if err != nil {
				t.Errorf("CheckDialect(%s) rejected a supported version: %v", tc.v, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("CheckDialect(%s) accepted an unsupported version", tc.v)
			continue
		}
		if !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("CheckDialect(%s) = %q, does not say why (%q)", tc.v, err, tc.mention)
		}
	}
}

// TestCheckVersionOutputAgainstWhatTheLabActuallyRan pins the case this gate
// exists for. Alpine's freeradius package is 3.0.27 on every alpine release from
// 3.19 to 3.23, while Foyer targets FreeRADIUS 3.2.x. The dialects are close
// enough that the generated configuration runs, so without this gate nothing
// would notice.
func TestCheckVersionOutputAgainstWhatTheLabActuallyRan(t *testing.T) {
	const alpine = "radiusd: FreeRADIUS Version 3.0.27, for host aarch64-alpine-linux-musl, built on Jan  4 2025 at 00:07:13"
	v, err := CheckVersionOutput(alpine)
	if v != (Version{3, 0, 27}) {
		t.Errorf("got %s, want 3.0.27", v)
	}
	if err == nil {
		t.Fatalf("3.0.27 passed the dialect gate; the gate is not a gate")
	}
	var de *DialectError
	if !asDialectError(err, &de) {
		t.Fatalf("error is not a DialectError: %T", err)
	}
	if de.Found != (Version{3, 0, 27}) {
		t.Errorf("DialectError names %s, not the version found", de.Found)
	}
}

func asDialectError(err error, target **DialectError) bool {
	de, ok := err.(*DialectError)
	if ok {
		*target = de
	}
	return ok
}
