// Package eap_test is the eapol_test suite: real EAP conversations against the
// configuration `internal/raddb` generates, on a real FreeRADIUS.
//
// `docs/rules/testing.md` says why it is not optional:
//
//	radclient and radtest do not speak EAP. A project whose entire architectural
//	rationale is "EAP is too hard to implement ourselves" cannot have zero EAP
//	coverage in CI.
//
// and what it is for:
//
//	Every EAP method Foyer claims to support has an eapol_test case, and the
//	claim is removed if the case is removed.
//
// **One method is claimed and one method has a case: EAP-TLS.** The other two the
// design names — EAP-TTLS/PAP and PEAP-MSCHAPv2 — are blocked on a
// credential-storage decision rather than on FreeRADIUS, and that was measured.
// `internal/raddb.Generator.Validate` refuses both by name, so a tree that claims
// a method with no case here cannot be written at all.
//
// # Why blue/green gate 3 rather than gate 1 or 2
//
// Measured on 3.2.10: a candidate tree naming an inner virtual server
// that does not exist **passes `radiusd -XC`**, which exits 0 and echoes the name
// back, **and passes a Status-Server probe**, because Status-Server never enters
// the `eap` module. It fails per request, with `No such virtual server`. Gates 1
// and 2 cannot see an EAP configuration error. This file is the only thing that
// can.
package eap_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

// The address eapol_test sends from. It runs inside the FreeRADIUS container, so
// the client row FreeRADIUS matches is the loopback one.
const labNASAddress = "127.0.0.1"

// The two identities `lab/policy/policystub` knows, reused here as **certificate
// Common Names**.
//
// That reuse is the point rather than a shortcut: it means this file drives the
// real `internal/aaa` engine over the same fixtures the SIGSTOP harness uses, and
// the authorization answer for an EAP-TLS supplicant comes from the same code
// that answers for a voucher. A stub written here would have been a second source
// of truth for what "allowed" means.
const (
	allowedCN = "LAB-VOUCHER-OK"
	deniedCN  = "LAB-VOUCHER-REVOKED"
)

const (
	radiusdReadyTimeout = 120 * time.Second
	eapolTestTimeout    = 60 * time.Second
)

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(base64.RawURLEncoding.EncodeToString(b)))
}

func run(t *testing.T, timeout time.Duration, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func mustRun(t *testing.T, timeout time.Duration, name string, args ...string) string {
	t.Helper()
	out, err := run(t, timeout, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func requireDocker(t *testing.T) {
	t.Helper()
	// Loud, and not a pass. An unrun EAP suite that says it is unrun is worth a
	// great deal; one that implies it passed is worth less than nothing.
	if os.Getenv("FOYER_LAB_SKIP") != "" {
		t.Skip("FOYER_LAB_SKIP is set: EAP coverage is UNPROVEN on this run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker is not on PATH, so EAP coverage is UNPROVEN on this machine: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker is present but not usable, so EAP coverage is UNPROVEN on this machine: %v: %s", err, out)
	}
}

type lab struct {
	network   string
	pgName    string
	frName    string
	raddbDir  string
	ownerURL  string
	policyCmd *exec.Cmd
	policyURL string
	secret    string
}

// TestEAPMethodsAgainstTheGeneratedConfiguration is the whole suite. One stack,
// several conversations: bringing a FreeRADIUS and a PostgreSQL up per subtest
// would turn a two-minute run into a ten-minute one for no extra coverage.
func TestEAPMethodsAgainstTheGeneratedConfiguration(t *testing.T) {
	requireDocker(t)
	l := startLab(t)

	// ── the claim, and the control for everything below it ───────────────────
	//
	// EAP-TLS, end to end, against the generated tree. This is the case
	// `docs/rules/testing.md` requires for the one method Foyer claims, and
	// removing it removes the claim.
	//
	// It is also the positive control, and that is load-bearing rather than
	// tidy. **Every refusal asserted below is satisfied by a server that refuses
	// everything** — and that is not hypothetical: a sabotage run that deleted
	// the policy call from the generated post-auth left `if (!ok && !updated)`
	// reading the previous statement's rcode, so the denial branch fired for
	// every supplicant, and the refusal subtests passed while the guarantee was
	// gone. They were being run alone. The flag below is what stops that being
	// possible, whether the suite is run whole or a subtest at a time.
	acceptWorks := false
	t.Run("eap-tls completes for an authorized identity", func(t *testing.T) {
		out := l.eapolTest(t, eapTLSConfig(allowedCN))
		if !strings.Contains(out, "CTRL-EVENT-EAP-SUCCESS") {
			t.Fatalf("EAP-TLS did not complete against the generated configuration.\n"+
				"If the failure is `No such virtual server`, the tree names one it does not emit — and note that "+
				"radiusd -XC and a Status-Server probe both pass in that state.\n"+
				"eapol_test said:\n%s\nradiusd said:\n%s", tail(out, 25), l.radiusdLogs(t, 120))
		}
		acceptWorks = true
	})

	// requireAcceptWorks refuses to report a pass for a refusal when nothing has
	// been shown to be accepted. UNPROVEN, loudly, rather than green.
	requireAcceptWorks := func(t *testing.T) {
		t.Helper()
		if !acceptWorks {
			t.Skip("UNPROVEN: the authorized identity did not authenticate either, so a refusal here says " +
				"nothing about authorization — a server that refuses every supplicant satisfies it perfectly. " +
				"Fix the accept case first; this subtest starts asserting again by itself.")
		}
	}

	// ── the hole the EAP-path policy call closes ─────────────────────────────
	//
	// Without the policy engine on the EAP path, a certificate for a REVOKED
	// identity gets an Access-Accept, because `eap { ok = return; updated =
	// return }` returns from authorize before `rest` and nothing downstream asks
	// anything. Against such a generator this subtest fails with SUCCESS for
	// deniedCN.
	t.Run("a certificate the CA signed is not by itself authorization", func(t *testing.T) {
		requireAcceptWorks(t)
		out := l.eapolTest(t, eapTLSConfig(deniedCN))
		if strings.Contains(out, "CTRL-EVENT-EAP-SUCCESS") {
			t.Fatalf("a revoked identity authenticated with a valid certificate. Signing is not authorization: "+
				"revocation, a disabled account and every per-site rule are decided by the policy engine, and if it is "+
				"not consulted then a certificate is a permanent credential.\neapol_test said:\n%s\nradiusd said:\n%s",
				tail(out, 25), l.radiusdLogs(t, 200))
		}

		// Both halves, because they fail independently and one of them is
		// invisible from the supplicant's own verdict.
		//
		// Measured: rejecting in post-auth does not tear down the
		// reply built on the way there, so the Access-Reject went out carrying
		// an EAP-Success and both MS-MPPE keys — the supplicant reported
		// success for a session the NAS had been told to refuse, and session
		// keying material travelled in a packet that must carry none.
		logs := l.radiusdLogs(t, 400)
		reject := lastPacket(logs, "Sent Access-Reject")
		if reject == "" {
			t.Fatalf("no Access-Reject in radiusd's trace, so it is not established that the refusal was a refusal "+
				"rather than a timeout:\n%s", tail(logs, 40))
		}
		for _, forbidden := range []string{"MS-MPPE-Recv-Key", "MS-MPPE-Send-Key"} {
			if strings.Contains(reject, forbidden) {
				t.Errorf("the Access-Reject carries %s. A refusal must carry no session keying material.\n%s", forbidden, reject)
			}
		}
		if strings.Contains(reject, "EAP-Message") {
			t.Errorf("the Access-Reject carries an EAP-Message. Measured: the one built on the accept path is an "+
				"EAP-Success, and a supplicant reads it as one.\n%s", reject)
		}
	})

	// ── security.md §8, on the method that ships ─────────────────────────────
	//
	// The named test in `docs/rules/testing.md` is "a mismatched outer
	// identity does not influence the authorization decision". `internal/aaa`
	// has that as a unit test over `AuthorizationSubject`. This is the other
	// half: that the *generated configuration* gives that function the right
	// value, which no Go test can establish.
	//
	// Measured, and it is the reason this subtest exists: a supplicant
	// presenting the certificate for one account while sending the `User-Name`
	// of another **real** account authenticates, and the outer server logs the
	// second name. If the generator keyed the policy request on `%{User-Name}`
	// this would return SUCCESS.
	t.Run("the outer identity does not choose the authorization subject", func(t *testing.T) {
		requireAcceptWorks(t)
		cfg := eapTLSConfig(deniedCN)
		// The certificate says LAB-VOUCHER-REVOKED. The claim says otherwise.
		cfg = strings.Replace(cfg, `identity="`+deniedCN+`"`, `identity="`+allowedCN+`"`, 1)

		out := l.eapolTest(t, cfg)
		if strings.Contains(out, "CTRL-EVENT-EAP-SUCCESS") {
			t.Fatalf("a supplicant presenting a revoked identity's certificate authenticated by CLAIMING to be an "+
				"allowed one. That is security.md §8 exactly: send a name, get whatever that name is allowed.\n"+
				"eapol_test said:\n%s\nradiusd said:\n%s", tail(out, 25), l.radiusdLogs(t, 200))
		}
	})

	// ── the audit trail ──────────────────────────────────────────────────────
	//
	// radpostauth is the one table an operator turns to when somebody asks who
	// got on the network, and the console reads it. The stock query writes
	// %{User-Name}, which on any EAP method is a name the supplicant chose.
	t.Run("radpostauth records the certificate, not the claim", func(t *testing.T) {
		cfg := eapTLSConfig(allowedCN)
		const claim = "somebody-else@example.invalid"
		cfg = strings.Replace(cfg, `identity="`+allowedCN+`"`, `identity="`+claim+`"`, 1)

		out := l.eapolTest(t, cfg)
		if !strings.Contains(out, "CTRL-EVENT-EAP-SUCCESS") {
			t.Fatalf("the precondition failed: the authorized certificate did not authenticate, so what lands in "+
				"radpostauth proves nothing.\n%s", tail(out, 25))
		}

		names := l.radpostauthUsernames(t)
		if contains(names, claim) {
			t.Errorf("radpostauth recorded %q — a name the supplicant chose and nothing checked. Every 802.1X row "+
				"would be an audit record written by whoever authenticated.\nrows: %v", claim, names)
		}
		if !contains(names, allowedCN) {
			t.Errorf("radpostauth did not record the certificate's Common Name %q, so the authentication record cannot say who got "+
				"on the network at all.\nrows: %v", allowedCN, names)
		}
	})

	// ── the CA is the trust boundary ─────────────────────────────────────────
	//
	// A baseline rather than a discovery: it is here because every subtest above
	// is about a certificate the configured CA signed, and a server that
	// accepted any certificate at all would satisfy most of them.
	t.Run("a certificate from another CA is refused", func(t *testing.T) {
		requireAcceptWorks(t)
		out := l.eapolTest(t, eapTLSForeignConfig(allowedCN))
		if strings.Contains(out, "CTRL-EVENT-EAP-SUCCESS") {
			t.Fatalf("a certificate issued by a CA this server does not trust authenticated. Every other assertion "+
				"in this file is then about a server that accepts anything.\n%s", tail(out, 25))
		}
	})
}

// TestOnlyClaimedMethodsCanBeGenerated is the mechanical half of "the claim is
// removed if the case is removed", and it needs no Docker.
//
// The suite above has one method in it. This asserts the generator cannot be
// asked for a second one — so the two cannot drift apart by somebody adding a
// method to the emitter and forgetting to add a case here.
func TestOnlyClaimedMethodsCanBeGenerated(t *testing.T) {
	for _, method := range []string{"ttls", "peap", "md5", "gtc", "fast", "pwd"} {
		t.Run(method, func(t *testing.T) {
			cfg := raddb.DefaultConfig()
			cfg.PostgresPassword = "pg-password-for-this-test-only"
			cfg.EAP = raddb.EAPConfig{
				Enabled: true, DefaultEAPType: method,
				CertDir: "/c", ServerCertFile: "/c/s", PrivateKeyFile: "/c/k", CAFile: "/c/ca",
			}
			if err := raddb.NewGenerator(cfg).Generate(t.TempDir()); err == nil {
				t.Fatalf("the generator emitted EAP method %q, and there is no eapol_test case for it in this "+
					"package. Add the case, or the claim comes out.", method)
			}
		})
	}
}

// ---------------------------------------------------------------- eapol_test

// eapTLSConfig is one wpa_supplicant network block. The identity defaults to the
// certificate's own Common Name; subtests that are about a mismatch override it.
func eapTLSConfig(cn string) string {
	return fmt.Sprintf(`network={
	key_mgmt=WPA-EAP
	eapol_flags=0
	eap=TLS
	identity="%s"
	ca_cert="/etc/raddb/certs/ca.pem"
	client_cert="/etc/raddb/certs/%s.pem"
	private_key="/etc/raddb/certs/%s.key"
	private_key_passwd=""
}
`, cn, cn, cn)
}

// eapTLSForeignConfig presents a certificate from a CA the server does not know,
// while still trusting the server's own CA — so a failure here is about the
// client certificate and not about the supplicant refusing the server.
func eapTLSForeignConfig(cn string) string {
	return fmt.Sprintf(`network={
	key_mgmt=WPA-EAP
	eapol_flags=0
	eap=TLS
	identity="%s"
	ca_cert="/etc/raddb/certs/ca.pem"
	client_cert="/etc/raddb/certs/foreign.pem"
	private_key="/etc/raddb/certs/foreign.key"
	private_key_passwd=""
}
`, cn)
}

func (l *lab) eapolTest(t *testing.T, networkBlock string) string {
	t.Helper()
	name := "case-" + randomToken(t)[:8]
	path := "/tmp/" + name + ".conf"

	writeInContainer(t, l.frName, path, networkBlock)

	ctx, cancel := context.WithTimeout(context.Background(), eapolTestTimeout)
	defer cancel()
	// -t 10: one conversation, waited out. eapol_test exits non-zero on a
	// refusal, which is an outcome here rather than an error.
	cmd := exec.CommandContext(ctx, "docker", "exec", l.frName,
		"eapol_test", "-c", path, "-a", labNASAddress, "-p", "1812", "-s", l.secret, "-t", "10")
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// ---------------------------------------------------------------- the stack

func startLab(t *testing.T) *lab {
	t.Helper()
	tag := randomToken(t)[:10]
	l := &lab{
		network: "foyer-eap-net-" + tag,
		pgName:  "foyer-eap-pg-" + tag,
		frName:  "foyer-eap-fr-" + tag,
	}

	mustRun(t, 30*time.Second, "docker", "network", "create", l.network)
	t.Cleanup(func() { _, _ = run(t, 30*time.Second, "docker", "network", "rm", l.network) })

	// PostgreSQL. Present because rlm_sql instantiates its pool at startup and
	// read_clients reads the client table before the first packet, so a stack
	// without it does not come up — and because the radpostauth assertion reads
	// radpostauth out of it.
	pgPassword := randomToken(t)
	mustRun(t, 120*time.Second, "docker", "run", "-d",
		"--name", l.pgName, "--network", l.network,
		"-e", "POSTGRES_DB=foyer", "-e", "POSTGRES_USER=foyer",
		"-e", "POSTGRES_PASSWORD="+pgPassword,
		"-p", "127.0.0.1:0:5432",
		"postgres:16-alpine")
	t.Cleanup(func() { _, _ = run(t, 60*time.Second, "docker", "rm", "-f", l.pgName) })

	pgPort := publishedPort(t, l.pgName, "5432/tcp")

	l.ownerURL = fmt.Sprintf("postgres://foyer:%s@127.0.0.1:%s/foyer?sslmode=disable", pgPassword, pgPort)
	waitForPostgres(t, l.pgName, l.ownerURL)
	migCtx, migCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer migCancel()
	if err := store.Migrate(migCtx, l.ownerURL); err != nil {
		t.Fatalf("migrating the harness database: %v", err)
	}
	// FreeRADIUS logs in as its own role, as it does in deploy/ (migration 00019).
	// The suite's radpostauth assertion reads what that role wrote, so this
	// is also what proves foyer_radius may write it.
	radiusPassword := randomToken(t)
	if err := store.SetLoginPassword(migCtx, l.ownerURL, store.RadiusRole, radiusPassword); err != nil {
		t.Fatalf("setting the FreeRADIUS login's password: %v", err)
	}

	// The RADIUS client, as a database row, before FreeRADIUS starts.
	// `nas.secret` is the schema's one declared cleartext copy of a shared
	// secret; this uses that exception and does not create a second one.
	l.secret = randomToken(t)
	insertNASRow(t, l.ownerURL, labNASAddress, l.secret)

	l.startPolicyAPI(t)

	// The configuration is generated, never written here. A hand-written raddb
	// in a test directory is a second source of truth for the thing under test.
	dir, err := os.MkdirTemp("", "foyer-eap-raddb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l.raddbDir = dir

	cfg := raddb.DefaultConfig()
	cfg.PolicyAPIURL = l.policyURL
	cfg.PostgresHost = l.pgName
	cfg.PostgresPort = 5432
	cfg.PostgresUser = store.RadiusRole
	cfg.PostgresPassword = radiusPassword
	cfg.PostgresDB = "foyer"
	cfg.EAP = raddb.EAPConfig{
		Enabled:        true,
		DefaultEAPType: "tls",
		CertDir:        "/etc/raddb/certs",
		ServerCertFile: "/etc/raddb/certs/server.pem",
		PrivateKeyFile: "/etc/raddb/certs/server.key",
		CAFile:         "/etc/raddb/certs/ca.pem",
		// Cache deliberately left off: a resumed session skips the handshake,
		// and the subtests above are about what the handshake proves.
	}
	if err := raddb.NewGenerator(cfg).Generate(dir); err != nil {
		t.Fatalf("generating the FreeRADIUS configuration: %v", err)
	}

	image := freeradiusImage(t)
	mustRun(t, 60*time.Second, "docker", "run", "-d",
		"--name", l.frName, "--network", l.network,
		"--add-host", "host.docker.internal:host-gateway",
		"-v", dir+":/etc/raddb",
		"--entrypoint", "sleep", image, "7200")
	t.Cleanup(func() { _, _ = run(t, 60*time.Second, "docker", "rm", "-f", l.frName) })

	assertFreeRADIUSDialect(t, l.frName)
	makeCertificates(t, l.frName)

	// radiusd is started after the certificates exist, because rlm_eap_tls opens
	// them at instantiation and a server that cannot find one does not start.
	mustRun(t, 30*time.Second, "docker", "exec", "-d", l.frName,
		"sh", "-c", "radiusd -f -l stdout -X > /tmp/radiusd.log 2>&1")
	waitForRadiusd(t, l.frName)
	return l
}

// assertFreeRADIUSDialect refuses to run against the wrong major version.
//
// Not defensive programming. `lab/policy` reuses a cached image tag if
// `docker image inspect` finds one, and on a development machine that tag had
// been built before `lab/Dockerfile.freeradius` moved from alpine to
// debian:sid — so the harness `docs/rules/radius.md` calls "the test that
// proves this architecture was worth choosing" had been passing against
// **FreeRADIUS 3.0.27**, a different major dialect, silently.
//
// The generated tree is 3.2.x unlang. A stale tag has to be a red run with the
// version in the message, not a green run about a different server.
func assertFreeRADIUSDialect(t *testing.T, container string) {
	t.Helper()
	out := mustRun(t, 30*time.Second, "docker", "exec", container, "radiusd", "-v")
	line := strings.SplitN(out, "\n", 2)[0]
	if !strings.Contains(line, "FreeRADIUS Version 3.2.") {
		t.Fatalf("this image is not the dialect internal/raddb emits for. The generated tree is 3.2.x unlang "+
			"(authorize/post-auth, 3.2 attribute references), and the range is >= 3.2.9, < 3.3.\n"+
			"radiusd -v said: %s\n\n"+
			"If the image tag is cached, it is stale: rebuild it with\n"+
			"    docker build -t %s -f lab/Dockerfile.freeradius lab/\n"+
			"A close-enough dialect starts and answers, which is exactly why nobody notices.",
			line, freeradiusImageTag)
	}
	t.Logf("MEASURED against: %s", line)
}

// makeCertificates issues everything at test time, inside the container, into
// the generated tree's own certs directory.
//
// Nothing here is committed and no image carries a CA: a fixed key means every
// deployment on earth shares one private key, and `docs/rules/security.md` §7
// has no lab exception.
//
// The client Common Names are the identities the policy engine knows, which is
// what makes an EAP-TLS supplicant's authorization answer come from the same
// code that answers for a voucher.
func makeCertificates(t *testing.T, container string) {
	t.Helper()
	script := fmt.Sprintf(`
set -eu
cd /etc/raddb/certs
umask 077
cat >ext.cnf <<'EOF'
[server]
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:foyer-lab-radius.invalid
[client]
basicConstraints = CA:FALSE
keyUsage = digitalSignature
extendedKeyUsage = clientAuth
EOF

issue_ca() {
	openssl req -x509 -newkey rsa:2048 -nodes -days 30 -keyout "$1.key" -out "$1.pem" \
		-subj "/O=Foyer lab/CN=$2" >/dev/null 2>&1
}
issue() {
	openssl req -newkey rsa:2048 -nodes -keyout "$1.key" -out "$1.csr" \
		-subj "/O=Foyer lab/CN=$1" >/dev/null 2>&1
	openssl x509 -req -in "$1.csr" -CA "$3.pem" -CAkey "$3.key" -CAcreateserial -days 30 \
		-extfile ext.cnf -extensions "$2" -out "$1.pem" >/dev/null 2>&1
	rm -f "$1.csr"
}

issue_ca ca "Foyer lab throwaway CA"
issue_ca otherca "Foyer lab CA nobody trusts"

mv server.key server.key.tmp 2>/dev/null || true
openssl req -newkey rsa:2048 -nodes -keyout server.key -out server.csr \
	-subj "/O=Foyer lab/CN=foyer-lab-radius.invalid" >/dev/null 2>&1
openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 30 \
	-extfile ext.cnf -extensions server -out server.pem >/dev/null 2>&1
rm -f server.csr server.key.tmp

issue %q client ca
issue %q client ca
issue foreign client otherca

chmod 644 *.pem *.key
`, allowedCN, deniedCN)

	if out, err := run(t, 180*time.Second, "docker", "exec", container, "sh", "-c", script); err != nil {
		t.Fatalf("issuing the harness certificates: %v\n%s", err, out)
	}
}

func (l *lab) startPolicyAPI(t *testing.T) {
	t.Helper()

	// FOYER_POLICYSTUB names an already-built binary.
	//
	// The default is to build one, which is right for CI. The override exists
	// because `go build` from inside a test is a second compiler running beside
	// whatever started the test, and on a machine already running several it is
	// the step that falls over — with the test process dying rather than the
	// build reporting an error, which looks like nothing at all in the log.
	bin := os.Getenv("FOYER_POLICYSTUB")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "policystub")
		mustRun(t, 180*time.Second, "go", "build", "-o", bin, "github.com/kiennt048/foyer/lab/policy/policystub")
	}
	t.Logf("Policy API binary: %s", bin)

	cmd := exec.Command(bin, "-addr", "0.0.0.0:0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the Policy API: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			line <- sc.Text()
		}
		close(line)
	}()

	select {
	case first, ok := <-line:
		if !ok {
			t.Fatal("the Policy API exited before it announced a port")
		}
		fields := strings.Fields(first) // "policystub listening on [::]:54321 pid 1234"
		if len(fields) < 4 {
			t.Fatalf("unexpected announcement from the Policy API: %q", first)
		}
		addr := fields[3]
		port := addr[strings.LastIndex(addr, ":")+1:]
		l.policyURL = fmt.Sprintf("http://host.docker.internal:%s/api/v1/aaa/authorize", port)
		l.policyCmd = cmd
	case <-time.After(30 * time.Second):
		t.Fatal("the Policy API never announced a port")
	}
}

// radpostauthUsernames reads radpostauth, the authentication record the console
// reads.
func (l *lab) radpostauthUsernames(t *testing.T) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, l.ownerURL)
	if err != nil {
		t.Fatalf("connecting to the harness database: %v", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `SELECT username FROM radpostauth ORDER BY id`)
	if err != nil {
		t.Fatalf("reading radpostauth: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatalf("scanning radpostauth: %v", err)
		}
		out = append(out, u)
	}
	return out
}

func insertNASRow(t *testing.T, ownerURL, address, secret string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatalf("connecting to the harness database: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		INSERT INTO nas (nasname, shortname, type, secret,
		                 require_message_authenticator, limit_proxy_state)
		VALUES ($1, 'lab-eap', 'other', $2, true, true)`,
		address, secret); err != nil {
		t.Fatalf("registering the harness RADIUS client: %v", err)
	}
}

const freeradiusImageTag = "foyer-lab-freeradius:eap-harness"

// freeradiusImage prefers a cached 3.2.x image and builds one otherwise.
//
// The candidates are checked for their VERSION, not merely for existing —
// `assertFreeRADIUSDialect` is the backstop, and this is the part that stops a
// stale tag being chosen in the first place.
func freeradiusImage(t *testing.T) string {
	t.Helper()
	if tag := os.Getenv("FOYER_LAB_FR_IMAGE"); tag != "" {
		return tag
	}
	for _, tag := range []string{freeradiusImageTag, "foyer-lab-freeradius:test", "foyer-nas-sync-freeradius:test"} {
		// `docker image inspect` FIRST, and never `docker run` on a tag that is
		// not present locally.
		//
		// `docker run` on an absent tag tries to pull it, and a daemon whose
		// registry path is unreachable **hangs there without erroring** — no
		// output, no timeout of its own, and a probe loop written the obvious
		// way then stops the whole harness at a step that reads like nothing
		// happened.
		if _, err := run(t, 20*time.Second, "docker", "image", "inspect", tag); err != nil {
			continue
		}
		out, err := run(t, 40*time.Second, "docker", "run", "--rm", "--entrypoint", "radiusd", tag, "-v")
		if err == nil && strings.Contains(out, "FreeRADIUS Version 3.2.") {
			return tag
		}
		t.Logf("cached image %s is not the 3.2.x dialect; skipping it", tag)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", freeradiusImageTag, "-f", "../Dockerfile.freeradius", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("no cached FreeRADIUS 3.2.x image and building lab/Dockerfile.freeradius failed: %v\n%s", err, out)
	}
	return freeradiusImageTag
}

// ---------------------------------------------------------------- plumbing

func writeInContainer(t *testing.T, container, path, content string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", container, "sh", "-c", "cat > "+path)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writing %s: %v\n%s", path, err, out)
	}
}

func (l *lab) radiusdLogs(t *testing.T, lines int) string {
	t.Helper()
	out, _ := run(t, 20*time.Second, "docker", "exec", l.frName,
		"sh", "-c", fmt.Sprintf("tail -%d /tmp/radiusd.log", lines))
	return out
}

// lastPacket returns the block of lines from the last occurrence of marker up to
// the following blank-ish boundary. radiusd prints a packet's attributes
// underneath the "Sent Access-..." line, indented, and an assertion that greps
// the whole log cannot tell which packet an attribute belonged to.
func lastPacket(logs, marker string) string {
	i := strings.LastIndex(logs, marker)
	if i < 0 {
		return ""
	}
	rest := logs[i:]
	var b strings.Builder
	for n, line := range strings.Split(rest, "\n") {
		if n > 0 && (strings.Contains(line, "Finished request") || strings.Contains(line, "Waking up")) {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func publishedPort(t *testing.T, container, port string) string {
	t.Helper()
	out := mustRun(t, 30*time.Second, "docker", "port", container, port)
	first := strings.TrimSpace(strings.Split(strings.TrimSpace(out), "\n")[0])
	return first[strings.LastIndex(first, ":")+1:]
}

// waitForPostgres waits until a connection through the PUBLISHED PORT succeeds.
//
// **Not `pg_isready` inside the container: the two disagree.** The PostgreSQL image runs a temporary server on a unix socket
// while its entrypoint scripts run; `pg_isready` defaults to that socket, answers
// yes to the temporary server, and the temporary server then stops and the real one
// starts. A migration started on its word is reset by a peer that is about to
// restart — measured here as an intermittent `failed to receive message: unexpected
// EOF` on the first connection, which `go test -race ./...` makes easy to hit and
// this package alone rarely does.
//
// The probe is the transport the caller uses, which is the only thing that can
// answer the question `store.Migrate` is about to ask.
// `internal/store/nassync_lab_test.go` makes the same choice.
func waitForPostgres(t *testing.T, container, dsn string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			pool.Close()
		}
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			logs, _ := run(t, 20*time.Second, "docker", "logs", "--tail", "40", container)
			t.Fatalf("PostgreSQL never became reachable through the published port (%v):\n%s", err, logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func waitForRadiusd(t *testing.T, container string) {
	t.Helper()
	deadline := time.Now().Add(radiusdReadyTimeout)
	for {
		logs, _ := run(t, 20*time.Second, "docker", "exec", container, "sh", "-c", "tail -200 /tmp/radiusd.log")
		if strings.Contains(logs, "Ready to process requests") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("FreeRADIUS never became ready. Its own output is the finding here:\n%s", logs)
		}
		time.Sleep(time.Second)
	}
}
