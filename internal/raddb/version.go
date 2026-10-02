package raddb

import (
	"fmt"
	"regexp"
	"strconv"
)

// Version is a FreeRADIUS server version.
type Version struct {
	Major, Minor, Patch int
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// less reports whether v sorts before o.
func (v Version) less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// MinVersion and MaxVersionExclusive bound the dialect this generator emits.
//
// This is a *range*, deliberately, and it is the one thing about the version
// check worth being careful with.
//
// The lower bound is 3.2.9 because that release carried security fixes. The
// upper bound is 3.3 because FreeRADIUS 4 is a different configuration
// language — recv Access-Request { } rather than authorize { }, different
// attribute references, different update sections, a different dictionary
// model — so supporting it means a second emitter, never an edit to this one.
//
// An equality check would be simpler and would be wrong: 3.2.10 and 3.2.11
// already exist, and pinning to one patch release turns every upstream security
// fix into a Foyer outage while the operator waits for us to bump a constant.
// Pin an exact version on the container image tag, where rolling it back is a
// deployment decision, not a code change.
var (
	MinVersion          = Version{3, 2, 9}
	MaxVersionExclusive = Version{3, 3, 0}
)

// versionRE matches the version in `radiusd -v` output, which looks like:
//
//	radiusd: FreeRADIUS Version 3.2.10, for host aarch64-unknown-linux-gnu, built on ...
//
// and, on the second line, `FreeRADIUS Version 3.2.10`.
var versionRE = regexp.MustCompile(`FreeRADIUS Version (\d+)\.(\d+)\.(\d+)`)

// ParseVersion extracts the server version from `radiusd -v` output.
func ParseVersion(out string) (Version, error) {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return Version{}, fmt.Errorf("raddb: no FreeRADIUS version in %q", truncate(out, 120))
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return Version{maj, min, pat}, nil
}

// DialectError says which way the installed server falls outside the range,
// because "wrong version" and "wrong configuration language" are different
// problems with different fixes.
type DialectError struct {
	Found Version
	Why   string
}

func (e *DialectError) Error() string {
	return fmt.Sprintf("raddb: FreeRADIUS %s is outside the supported dialect range >= %s, < %s: %s",
		e.Found, MinVersion, MaxVersionExclusive, e.Why)
}

// CheckDialect asserts that v is a server this generator's output is valid for.
func CheckDialect(v Version) error {
	switch {
	case v.Major >= 4:
		return &DialectError{Found: v, Why: "FreeRADIUS 4 uses a different configuration language and needs a second emitter, not a wider range here"}
	case v.Major == 3 && v.Minor < 2:
		return &DialectError{Found: v, Why: "3.0.x and 3.1.x predate the 3.2 dialect this generator emits"}
	case v.less(MinVersion):
		return &DialectError{Found: v, Why: "3.2.9 carried security fixes and is the floor"}
	case !v.less(MaxVersionExclusive):
		return &DialectError{Found: v, Why: "beyond 3.2.x the configuration language is not known to be the same"}
	}
	return nil
}

// CheckVersionOutput parses `radiusd -v` output and applies CheckDialect.
func CheckVersionOutput(out string) (Version, error) {
	v, err := ParseVersion(out)
	if err != nil {
		return Version{}, err
	}
	return v, CheckDialect(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
