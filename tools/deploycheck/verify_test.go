package deploycheck

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// verify.sh runs on the host against Docker and PostgreSQL. Here it runs against
// stubs that answer the handful of questions two of its sections ask — what the
// plain portal says, what ports 443 and 8843 present, what each database role is
// allowed, and what the control plane logged — so that each of those sections can
// be made to pass AND made to fail. The rest of verify.sh drifts in the sandbox,
// as it should with no host behind it; only the named lines are asserted.

// verifyDocker answers `docker` as the running deployment would, per scenario:
//
//	STUB_ENV     the control plane's environment, one VAR=value per line
//	STUB_LOGS    the control plane's log
//	STUB_WIDE    non-empty: foyer_retention may UPDATE radacct (a grant too wide)
const verifyDocker = `#!/bin/bash
args="$*"
case "${args}" in
  *" ps -q foyer"*)      echo cid-foyer; exit 0 ;;
  *" ps -q maintenance"*) echo cid-maint; exit 0 ;;
  *" ps -q postgres"*)   echo cid-pg; exit 0 ;;
  *" ps -q freeradius"*) exit 0 ;;
  "inspect -f {{range .Config.Env}}{{println .}}{{end}} cid-foyer") printf '%s\n' "${STUB_ENV}"; exit 0 ;;
  "inspect -f {{range .Mounts}}{{println .Destination}}{{end}} cid-foyer") printf '%s\n' "${STUB_MOUNTS}"; exit 0 ;;
  "inspect -f {{range .Config.Env}}{{println .}}{{end}} cid-maint") printf '%s\n' "${STUB_MAINT_ENV}"; exit 0 ;;
  "logs cid-maint")     printf '%s\n' "${STUB_LOGS}"; exit 0 ;;
esac
if [[ "$1" == exec && "${args}" == *" psql "* ]]; then
  role="" sql=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -U) role="$2"; shift ;;
      -c) [[ "$2" == BEGIN || "$2" == ROLLBACK ]] || sql="$2"; shift ;;
    esac
    shift
  done
  deny() { echo "psql:<stdin>:1: ERROR:  permission denied for table $1"; exit 3; }
  case "${role}|${sql}" in
    *"FROM pg_authid"*) echo 100001 ;;
    "foyer_reaper|UPDATE radacct SET acctstoptime"*) ;;
    "foyer_reaper|"*) deny radacct ;;
    "foyer_retention|DELETE FROM radacct"*) ;;
    "foyer_retention|UPDATE radacct"*) [[ -n "${STUB_WIDE}" ]] || deny radacct ;;
    "foyer_retention|INSERT INTO radpostauth"*) deny radpostauth ;;
    "foyer_app|DELETE FROM radacct"*) deny radacct ;;
  esac
  exit 0
fi
exit 0
`

// verifyCurl answers the plain portal's /healthz with STUB_PLAIN ("<code> <redirect>").
const verifyCurl = `#!/bin/sh
case "$*" in
  *"http://127.0.0.1:80/healthz"*) printf '%s' "${STUB_PLAIN}"; exit 0 ;;
esac
exit 7
`

// verifyOpenSSL presents STUB_PEM on 443 and 8843 (nothing when it is empty) and is
// the real openssl for everything else, x509 parsing included.
const verifyOpenSSL = `#!/bin/sh
if [ "$1" = s_client ]; then
  case "$*" in
    *127.0.0.1:443*|*127.0.0.1:8843*)
      [ -n "${STUB_PEM}" ] || { echo "connect: Connection refused" >&2; exit 1; }
      echo "CONNECTED(00000003)"; cat "${STUB_PEM}"; exit 0 ;;
  esac
  exit 1
fi
exec "${REAL_OPENSSL}" "$@"
`

type verifyScenario struct {
	plain string // what 80/healthz answers: "200 " or "302 https://…"
	pem   string // a PEM file 443/8843 present, or ""
	env   string // the control plane's environment
	logs  string // the maintenance container's log
	wide  bool
	// maintEnv and mounts are the maintenance container's environment and the
	// control plane's mounts. Empty maintEnv is the shipped shape.
	maintEnv string
	mounts   string
}

func portalCert(t *testing.T, dir, name string, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".pem")
	write(t, path, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), 0o644)
	return path
}

func runVerify(t *testing.T, sc verifyScenario) string {
	t.Helper()
	if sc.maintEnv == "" {
		sc.maintEnv = goodMaintEnv
	}
	realSSL, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not installed; verify.sh's certificate checks need it")
	}
	sb := newSandbox(t, "verify.sh", "compose.sh", "images.conf", "compose.yaml", "Dockerfile.freeradius")
	bin := filepath.Join(sb.root, "bin")
	write(t, filepath.Join(bin, "docker"), verifyDocker, 0o755)
	write(t, filepath.Join(bin, "curl"), verifyCurl, 0o755)
	write(t, filepath.Join(bin, "openssl"), verifyOpenSSL, 0o755)
	write(t, filepath.Join(bin, "sleep"), "#!/bin/sh\nexit 0\n", 0o755)
	write(t, filepath.Join(bin, "ss"), "#!/bin/sh\nexit 0\n", 0o755)
	env := []string{"REAL_OPENSSL=" + realSSL, "STUB_PLAIN=" + sc.plain, "STUB_PEM=" + sc.pem,
		"STUB_ENV=" + sc.env, "STUB_LOGS=" + sc.logs, "STUB_MOUNTS=" + sc.mounts, "STUB_MAINT_ENV=" + sc.maintEnv}
	if sc.wide {
		env = append(env, "STUB_WIDE=1")
	}
	out, err := sb.run(t, "verify.sh", env...) // it drifts in a sandbox; the lines are the result
	if out == "" {
		t.Fatalf("verify.sh printed nothing: %v", err)
	}
	return out
}

func wantLines(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("verify.sh did not print %q", w)
		}
	}
	if t.Failed() {
		t.Logf("output:\n%s", out)
	}
}

const (
	// The shipped shapes: the control plane is told where the
	// maintenance container is and holds no maintenance login; that container
	// holds the two and no KEK.
	goodEnv      = "FOYER_DATABASE_URL_FILE=/run/secrets/database_url\nFOYER_KEK_FILE=/run/secrets/kek\nFOYER_MAINTENANCE_URL=http://maintenance:8090\nFOYER_MAINTENANCE_TOKEN_FILE=/run/secrets/maintenance_token"
	goodMaintEnv = "FOYER_DATABASE_URL_FILE=/run/secrets/database_url\nFOYER_REAPER_DATABASE_URL_FILE=/run/secrets/reaper_database_url\nFOYER_RETENTION_DATABASE_URL_FILE=/run/secrets/retention_database_url\nFOYER_MAINTENANCE_TOKEN_FILE=/run/secrets/maintenance_token"
	goodLogs     = "2026/09/23 10:00:01 INFO maintenance job ran job=stale-sessions affected=0 duration=3ms runs=1\n" +
		"2026/09/23 10:00:02 INFO maintenance job ran job=retention affected=0 duration=9ms runs=1"
)

func TestVerifyPortalHTTPSWithNoCertificate(t *testing.T) {
	out := runVerify(t, verifyScenario{plain: "200 ", env: goodEnv, logs: goodLogs})
	wantLines(t, out,
		"ok    the plain portal serves guests itself (200) — no portal certificate, HTTP by default",
		"ok    port 443 completes no TLS handshake, as it should with no portal certificate",
		"ok    port 8843 completes no TLS handshake, as it should with no portal certificate")
}

func TestVerifyPortalHTTPSWithACertificate(t *testing.T) {
	pemPath := portalCert(t, t.TempDir(), "wifi.example", time.Now().Add(90*24*time.Hour))
	out := runVerify(t, verifyScenario{plain: "302 https://wifi.example/healthz", pem: pemPath, env: goodEnv, logs: goodLogs})
	wantLines(t, out,
		"ok    the plain portal sends guests to https://wifi.example/healthz — a portal certificate is configured",
		"ok    port 443 serves an unexpired portal certificate that covers wifi.example",
		"ok    port 8843 serves an unexpired portal certificate that covers wifi.example")
}

// The failures are the point: each state's check must be able to say no.
func TestVerifyPortalHTTPSDrifts(t *testing.T) {
	dir := t.TempDir()
	t.Run("redirected to an HTTPS origin nothing serves", func(t *testing.T) {
		out := runVerify(t, verifyScenario{plain: "302 https://wifi.example/healthz", env: goodEnv, logs: goodLogs})
		wantLines(t, out, "DRIFT port 443 completes no TLS handshake, and the plain portal is redirecting every guest to HTTPS")
	})
	t.Run("a certificate for another name", func(t *testing.T) {
		pemPath := portalCert(t, dir, "other.example", time.Now().Add(90*24*time.Hour))
		out := runVerify(t, verifyScenario{plain: "302 https://wifi.example/healthz", pem: pemPath, env: goodEnv, logs: goodLogs})
		wantLines(t, out, "DRIFT the portal certificate on 443 does not cover wifi.example")
	})
	t.Run("an expired certificate", func(t *testing.T) {
		pemPath := portalCert(t, dir, "old.wifi.example", time.Now().Add(-time.Hour))
		out := runVerify(t, verifyScenario{plain: "302 https://old.wifi.example/healthz", pem: pemPath, env: goodEnv, logs: goodLogs})
		wantLines(t, out, "DRIFT the portal certificate on 443 has expired")
	})
	t.Run("TLS answered with the plain portal serving guests", func(t *testing.T) {
		pemPath := portalCert(t, dir, "stray.example", time.Now().Add(90*24*time.Hour))
		out := runVerify(t, verifyScenario{plain: "200 ", pem: pemPath, env: goodEnv, logs: goodLogs})
		wantLines(t, out, "DRIFT port 443 presents a certificate while the plain portal is not redirecting to it")
	})
}

func TestVerifyMaintenanceLogins(t *testing.T) {
	out := runVerify(t, verifyScenario{plain: "200 ", env: goodEnv, logs: goodLogs})
	wantLines(t, out,
		"ok    the control plane holds neither maintenance login nor FreeRADIUS's database password",
		"ok    the control plane asks the maintenance container for erasure (FOYER_MAINTENANCE_URL)",
		"ok    the maintenance container is given FOYER_REAPER_DATABASE_URL_FILE",
		"ok    the maintenance container is given FOYER_RETENTION_DATABASE_URL_FILE",
		"ok    the maintenance container holds no KEK",
		"ok    foyer_reaper can close a stale session in radacct",
		"ok    foyer_reaper is refused a byte counter in radacct",
		"ok    foyer_reaper is refused DELETE on radacct",
		"ok    foyer_retention can DELETE expired rows from radacct and radpostauth",
		"ok    foyer_retention is refused UPDATE on radacct",
		"ok    foyer_retention is refused INSERT into radpostauth",
		"ok    foyer_app is refused DELETE on radacct",
		"ok    the stale-sessions job's last run succeeded, as foyer_reaper",
		"ok    the retention job's last run succeeded, as foyer_retention")
}

func TestVerifyMaintenanceLoginsDrift(t *testing.T) {
	logs := "2026/09/23 10:00:01 ERROR the retention connection did not open; retention and erasure will fail until it does role=foyer_retention error=\"password authentication failed\"\n" +
		"2026/09/23 10:00:01 INFO maintenance job ran job=stale-sessions affected=0 duration=3ms runs=1\n" +
		"2026/09/23 10:00:02 ERROR maintenance job failed job=retention affected=0 duration=1ms runs=1 failures=1 error=\"retention unavailable\""
	maintEnv := "FOYER_REAPER_DATABASE_URL_FILE=/run/secrets/reaper_database_url\nFOYER_KEK_FILE=/run/secrets/kek"
	out := runVerify(t, verifyScenario{plain: "200 ", env: goodEnv, maintEnv: maintEnv, logs: logs, wide: true})
	wantLines(t, out,
		"DRIFT the running maintenance container has no FOYER_RETENTION_DATABASE_URL_FILE",
		"DRIFT the maintenance container is given the KEK",
		"DRIFT foyer_retention was not refused UPDATE on radacct",
		"DRIFT the maintenance container logged that a maintenance login did not open",
		"DRIFT the retention job's last run FAILED (it runs as foyer_retention)",
		"ok    the stale-sessions job's last run succeeded, as foyer_reaper")
}

// TestVerifyTheControlPlaneHoldsNoMaintenanceLogin is the secret boundary on the host: a
// control plane started from an older compose.yaml still holds the reaper's and the
// retention role's logins and FreeRADIUS's password, as variables and as mounted
// secrets, and verify.sh must say so for each.
func TestVerifyTheControlPlaneHoldsNoMaintenanceLogin(t *testing.T) {
	old := "FOYER_DATABASE_URL_FILE=/run/secrets/database_url\nFOYER_REAPER_DATABASE_URL_FILE=/run/secrets/reaper_database_url\n" +
		"FOYER_RETENTION_DATABASE_URL=postgres://foyer_retention:pw@postgres/foyer\nFOYER_POSTGRES_PASSWORD_FILE=/run/secrets/radius_db_password"
	mounts := "/run/secrets/database_url\n/run/secrets/reaper_database_url\n/run/secrets/radius_db_password"
	out := runVerify(t, verifyScenario{plain: "200 ", env: old, mounts: mounts, logs: goodLogs})
	wantLines(t, out,
		"DRIFT FOYER_RETENTION_DATABASE_URL is an environment variable on the running control plane",
		"DRIFT the running control plane is given FOYER_REAPER_DATABASE_URL_FILE",
		"DRIFT the running control plane is given FOYER_POSTGRES_PASSWORD_FILE",
		"DRIFT the running control plane has the reaper_database_url secret mounted",
		"DRIFT the running control plane has the radius_db_password secret mounted",
		"DRIFT the running control plane has no FOYER_MAINTENANCE_URL")
	if strings.Contains(out, "ok    the control plane holds neither maintenance login") {
		t.Fatalf("verify.sh called a control plane holding the maintenance logins clean:\n%s", out)
	}
}
