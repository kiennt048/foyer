package raddb

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The two FreeRADIUS images this repository builds, and the guard that keeps
// them two.
//
// `docs/rules/radius.md` says the version assertion in the code is a RANGE
// and the exact pin belongs on the image. That leaves the exact pin with nothing
// checking it, which is how `lab/Dockerfile.freeradius` ran alpine's 3.0.27 for
// as long as it did: the dialects were close enough that the generated
// configuration started and answered.
//
// So this file reads both Dockerfiles and asserts the pin story against
// CheckDialect — the same function `foyerctl apply-raddb` calls at gate 0 —
// rather than against a version typed into a test. Per
// `docs/rules/testing.md`: derive the stand-in from what it stands in for. A
// version number written here would be a second source of truth for the range,
// and it would go on passing the day somebody widened the real one.
//
// What it cannot do is prove the image builds; that needs an amd64 host and a
// registry, and neither is available on the machine this usually runs on. It
// proves the two files still disagree for the two measured reasons, which is the
// thing that decays silently.

const (
	deployDockerfile = "../../deploy/Dockerfile.freeradius"
	labDockerfile    = "../../lab/Dockerfile.freeradius"
)

// The packages the deployment image installs from the vendor repository. Every
// one of them must carry the pin: apt resolves a dependency to whatever
// satisfies it, and `freeradius-config` arriving from Ubuntu's archive at 3.2.5
// while `freeradius` came from the vendor at 3.2.10 is a mixed install that
// starts.
var pinnedPackages = []string{
	"freeradius",
	"freeradius-common",
	"freeradius-config",
	"freeradius-postgresql",
	"freeradius-rest",
	"freeradius-utils",
}

var argVersionRE = regexp.MustCompile(`(?m)^ARG FREERADIUS_VERSION=(\S+)\s*$`)

// debianRevisionRE matches a Debian package version of the form `3.2.10-1`.
// The upstream part is what CheckDialect judges; the revision is the
// packaging's own and means nothing to the dialect.
var debianRevisionRE = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)-\d+$`)

func readDockerfile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// logicalLines joins shell line continuations, so an `apt-get install` spread
// over twelve backslash-terminated lines is examined as the one command it is.
func logicalLines(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			cur.WriteString(strings.TrimSuffix(trimmed, "\\"))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// installCommands returns just the `apt-get install` commands, with the rest of
// their RUN block left out.
//
// The narrowing matters: a whole RUN block also contains the `for p in
// freeradius freeradius-common …` pre-flight loop and an apt preferences stanza
// naming `freeradius*`, and a check that examined the block would report both of
// those as unpinned installs. That is a test failing on its own scaffolding —
// noise that gets silenced by loosening the assertion, which is how a check
// stops checking.
func installCommands(src string) []string {
	var out []string
	for _, line := range logicalLines(src) {
		for _, seg := range strings.Split(line, ";") {
			if strings.Contains(seg, "apt-get install") {
				out = append(out, seg)
			}
		}
	}
	return out
}

// TestDeployImagePinIsInsideTheDialectRange is the load-bearing half: the exact
// version the deployment image installs is judged by the generator's own
// dialect check.
func TestDeployImagePinIsInsideTheDialectRange(t *testing.T) {
	src := readDockerfile(t, deployDockerfile)

	m := argVersionRE.FindAllStringSubmatch(src, -1)
	if len(m) != 1 {
		t.Fatalf("deploy/Dockerfile.freeradius must declare exactly one ARG FREERADIUS_VERSION; found %d", len(m))
	}
	pin := m[0][1]

	rev := debianRevisionRE.FindStringSubmatch(pin)
	if rev == nil {
		t.Fatalf("FREERADIUS_VERSION=%q is not an exact Debian version like 3.2.10-1.\n"+
			"An exact pin is the whole point of this file: `radius.md` puts the range in the code and the\n"+
			"pin on the image, so a pin that is not exact leaves the version decided by whatever apt found.", pin)
	}
	maj, _ := strconv.Atoi(rev[1])
	min, _ := strconv.Atoi(rev[2])
	pat, _ := strconv.Atoi(rev[3])
	v := Version{maj, min, pat}

	if err := CheckDialect(v); err != nil {
		t.Fatalf("the deployment image pins FreeRADIUS %s, which the generator refuses to emit for:\n  %v\n\n"+
			"This is the check `foyerctl apply-raddb` runs at gate 0, asked at build time instead of at\n"+
			"apply time — where the answer is a failed deployment rather than a failed rollout.", v, err)
	}
}

// TestDeployImagePinsEveryFreeRADIUSPackage covers the mixed install: apt will
// happily satisfy a pinned `freeradius` with an unpinned `freeradius-config`
// from a different origin.
func TestDeployImagePinsEveryFreeRADIUSPackage(t *testing.T) {
	src := readDockerfile(t, deployDockerfile)

	for _, p := range pinnedPackages {
		want := `"` + p + `=${FREERADIUS_VERSION}"`
		if !strings.Contains(src, want) {
			t.Errorf("deploy/Dockerfile.freeradius does not install %s at the pinned version (looked for %s)", p, want)
		}
	}

	// And nothing in the FreeRADIUS family reaches an install line unpinned.
	// `eapoltest`, `postgresql-client` and `openssl` are Ubuntu's own and are
	// deliberately not pinned: they are not the thing the dialect range is about,
	// and pinning them would make a security update to the base image a build
	// failure.
	for _, cmd := range installCommands(src) {
		for _, tok := range strings.Fields(cmd) {
			tok = strings.Trim(tok, `"'`)
			if !strings.HasPrefix(tok, "freeradius") {
				continue
			}
			if !strings.Contains(tok, "=") {
				t.Errorf("deploy/Dockerfile.freeradius installs %q with no version pin.\n"+
					"Ubuntu's own archive answers with 3.2.5, which is below the 3.2.9 security floor and\n"+
					"which installs, starts and answers packets. An unpinned FreeRADIUS package here is a\n"+
					"silent downgrade, not a build failure.", tok)
			}
		}
	}
}

// TestDeployImageRefusesRatherThanFallsBack pins the shape of the failure, not
// only the success. The requirement is "fail loudly if the pinned version is
// unavailable rather than falling back", and a fallback would be invisible in
// a green build.
func TestDeployImageRefusesRatherThanFallsBack(t *testing.T) {
	src := readDockerfile(t, deployDockerfile)

	// The pre-flight check that turns apt's "Version ... was not found" into a
	// sentence about a vendor dropping a release.
	if !strings.Contains(src, "apt-cache madison") {
		t.Error("deploy/Dockerfile.freeradius no longer checks the index before installing;\n" +
			"without it the failure is apt's, and apt's reads like a typo")
	}
	// The post-flight one: what apt actually installed, and what the binary says
	// about itself. dpkg answers the first; only the binary answers the second.
	if !strings.Contains(src, "dpkg-query -W -f='${Version}' freeradius") {
		t.Error("deploy/Dockerfile.freeradius no longer asserts the installed version after apt ran")
	}
	if !strings.Contains(src, "/usr/sbin/freeradius -v") {
		t.Error("deploy/Dockerfile.freeradius no longer asks the built binary what version it is.\n" +
			"dpkg's answer is about the package; this one is about the thing that will answer RADIUS.")
	}
}

// TestDeployImageVerifiesTheRepositoryKey — the key is fetched over TLS and then
// checked against a fingerprint in the tree, so who signs the data plane's
// packages is reviewable in a diff, the way deploy/authorized_keys makes access
// reviewable in a diff.
func TestDeployImageVerifiesTheRepositoryKey(t *testing.T) {
	src := readDockerfile(t, deployDockerfile)

	fpr := regexp.MustCompile(`(?m)^ARG NETWORKRADIUS_KEY_FINGERPRINT=([0-9A-F]{40})\s*$`)
	if !fpr.MatchString(src) {
		t.Fatal("deploy/Dockerfile.freeradius does not pin a 40-hex-digit NetworkRADIUS key fingerprint.\n" +
			"Without it the key is trusted because the TLS handshake worked, which is a\n" +
			"trust-on-first-use decision taken by a build with nobody watching.")
	}
	if !strings.Contains(src, "--with-fingerprint") || !strings.Contains(src, "exit 1") {
		t.Error("the fingerprint is pinned but nothing compares it and refuses")
	}
}

// TestLabAndDeployDisagreeForTheMeasuredReasons is the one that decays quietly.
//
// Both files are correct today. The failure this guards is somebody "tidying up"
// by pointing lab/ at the vendor repository — which answers HTTP 404 for arm64
// and would break every arm64 developer's lab with a network error — or pointing
// deploy/ at debian:sid, which is a moving base for a security product on the
// only machine that faces real access points.
func TestLabAndDeployDisagreeForTheMeasuredReasons(t *testing.T) {
	deploy := readDockerfile(t, deployDockerfile)
	lab := readDockerfile(t, labDockerfile)

	if !strings.Contains(deploy, "packages.networkradius.com") {
		t.Error("deploy/Dockerfile.freeradius no longer takes the vendor repository;\n" +
			"Ubuntu 24.04's own archive is 3.2.5 and that is below the floor")
	}
	if !strings.Contains(deploy, "arch=amd64") {
		t.Error("the vendor repository line does not say arch=amd64.\n" +
			"That repository declares `all amd64` and answers 404 for arm64; without the\n" +
			"restriction, an arm64 build fails inside `apt-get update` with a fetch error\n" +
			"rather than at the line that made the decision.")
	}
	if !strings.HasPrefix(firstFrom(deploy), "ubuntu:noble@sha256:") {
		t.Errorf("deploy/Dockerfile.freeradius must build on a digest-pinned ubuntu:noble; FROM is %q", firstFrom(deploy))
	}

	if !strings.HasPrefix(firstFrom(lab), "debian:sid") {
		t.Errorf("lab/Dockerfile.freeradius must stay on debian:sid — it is the only base with a\n"+
			"3.2.9+ package for arm64, which is what developers build on; FROM is %q", firstFrom(lab))
	}
	if strings.Contains(lab, "networkradius") {
		t.Error("lab/Dockerfile.freeradius now references the NetworkRADIUS repository.\n" +
			"That repository is amd64 only — measured, arm64 answers HTTP 404 —\n" +
			"so this makes `make lab-up` fail on every arm64 development machine.")
	}
}

var fromRE = regexp.MustCompile(`(?m)^FROM\s+(\S+)`)

func firstFrom(src string) string {
	m := fromRE.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	return m[1]
}
