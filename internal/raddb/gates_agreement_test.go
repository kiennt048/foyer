package raddb

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The blue/green procedure exists twice: here, for a host install where the
// control plane and radiusd share a filesystem, and in lab/blue-green/gates.sh,
// for the lab where they are separate containers and only one of them has the
// binaries. Two implementations of one procedure drift, so the drift is what is
// tested.
//
// This does not check that the two behave identically — a shell script and a Go
// type cannot be compared that way. It checks the one thing that would make a
// report about "the four gates" mean different things in the two places: the
// gates themselves, their numbers, their names and their order.
var shellGateRE = regexp.MustCompile(`(?m)^say (\d+) (\S+)$`)

func TestShellGatesMatchTheGoGates(t *testing.T) {
	const path = "../../lab/blue-green/gates.sh"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	found := shellGateRE.FindAllStringSubmatch(string(b), -1)
	if len(found) != len(Gates) {
		var got []string
		for _, f := range found {
			got = append(got, fmt.Sprintf("%s %s", f[1], f[2]))
		}
		t.Fatalf("%s announces %d gates %v, Go has %d %v", path, len(found), got, len(Gates), Gates)
	}
	for i, f := range found {
		n, err := strconv.Atoi(f[1])
		if err != nil {
			t.Fatalf("gate number %q: %v", f[1], err)
		}
		if n != Gates[i].Number || f[2] != Gates[i].Name {
			t.Errorf("gate %d in the shell script is %d/%s, in Go it is %d/%s",
				i, n, f[2], Gates[i].Number, Gates[i].Name)
		}
	}
}

// TestShellProcedureRefusesWithoutASecret. The script has no default shared
// secret and must not acquire one; a default in a lab script is a default that
// gets copied into a deployment.
func TestShellProcedureRefusesWithoutASecret(t *testing.T) {
	b, err := os.ReadFile("../../lab/blue-green/gates.sh")
	if err != nil {
		t.Fatalf("read gates.sh: %v", err)
	}
	s := string(b)
	if !regexp.MustCompile(`\$\{PROBE_SECRET:\?`).MatchString(s) {
		t.Errorf("gates.sh does not refuse to run without PROBE_SECRET")
	}
	if regexp.MustCompile(`PROBE_SECRET:-`).MatchString(s) {
		t.Errorf("gates.sh defaults PROBE_SECRET to something")
	}
	for _, def := range knownDefaultSecrets {
		if regexp.MustCompile(regexp.QuoteMeta(def)).MatchString(s) {
			t.Errorf("gates.sh contains a known default secret")
		}
	}
}
