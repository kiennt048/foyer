package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/store"
)

func insert(name string) store.NasPlanRow {
	return store.NasPlanRow{ClientID: "rc-" + name, NASName: name}
}

func update(name string, differs ...string) store.NasPlanRow {
	return store.NasPlanRow{ClientID: "rc-" + name, NASName: name, Differs: differs}
}

// The check that carries the unprojected-client symptom: a client that is enabled and not in the
// table is a device whose every packet FreeRADIUS discards as coming from an unknown
// NAS, and nothing else in the product says so.
func TestDoctorNamesAnEnabledClientThatIsNotProjected(t *testing.T) {
	res := renderProjection(store.NasPlan{
		Insert:          []store.NasPlanRow{insert("10.0.0.7")},
		Unchanged:       1,
		SecretsCompared: true,
	})
	if res.status != statusFail {
		t.Fatalf("status = %s, want %s", res.status, statusFail)
	}
	advice := strings.Join(res.advice, "\n")
	if !strings.Contains(advice, "10.0.0.7") {
		t.Fatalf("the missing client is not named: %q", advice)
	}
	if !strings.Contains(advice, "sync-nas") {
		t.Fatalf("the advice does not say what to run: %q", advice)
	}
}

// The drift a prefix-only comparison cannot see.
//
// It compared both sides as `netip.Prefix`, which cannot disagree about anything
// except the address. Against this world it answered
//
//	status = ok, detail = "1 enabled client(s) for this tenant, 1 row(s) in nas
//	(every tenant)", advice = []
//
// while the running server refused every packet from that access point with
// "Shared secret is incorrect".
func TestDoctorNamesAProjectionWhoseSecretHasDrifted(t *testing.T) {
	res := renderProjection(store.NasPlan{
		Update:          []store.NasPlanRow{update("10.0.0.7", "shared secret")},
		SecretsCompared: true,
	})
	if res.status != statusFail {
		t.Fatalf("status = %s, want %s", res.status, statusFail)
	}
	advice := strings.Join(res.advice, "\n")
	for _, want := range []string{"10.0.0.7", "Shared secret is incorrect", "sync-nas"} {
		if !strings.Contains(advice, want) {
			t.Errorf("the advice does not mention %q: %q", want, advice)
		}
	}
}

// A shortname that has drifted is not an outage, and reporting it as one trains an
// operator to ignore the check that also carries the outage.
func TestDoctorSeparatesCosmeticDriftFromAnOutage(t *testing.T) {
	res := renderProjection(store.NasPlan{
		Update:          []store.NasPlanRow{update("10.0.0.7", "shortname")},
		SecretsCompared: true,
	})
	if res.status != statusWarn {
		t.Fatalf("status = %s, want %s", res.status, statusWarn)
	}
	if !strings.Contains(strings.Join(res.advice, "\n"), "shortname") {
		t.Fatalf("the column that drifted is not named: %v", res.advice)
	}
}

// A row nothing produces is a RADIUS client no operator can revoke — the half of a
// projection that is easy to leave out and impossible to notice.
func TestDoctorNamesARowNothingProduces(t *testing.T) {
	res := renderProjection(store.NasPlan{Remove: []string{"10.0.0.9"}, SecretsCompared: true})
	if res.status != statusWarn {
		t.Fatalf("status = %s, want %s", res.status, statusWarn)
	}
	if !strings.Contains(strings.Join(res.advice, "\n"), "10.0.0.9") {
		t.Fatalf("the orphaned row is not named: %v", res.advice)
	}
}

func TestDoctorWarnsAboutAClientWithNoSharedSecret(t *testing.T) {
	res := renderProjection(store.NasPlan{
		Skipped: []store.NasSkip{{
			ClientID: "rc-1", Address: "10.0.0.9",
			Reason: "no shared secret assigned; FreeRADIUS discards every packet from this address until one is",
		}},
		SecretsCompared: true,
	})
	if res.status != statusWarn {
		t.Fatalf("status = %s, want %s", res.status, statusWarn)
	}
	if !strings.Contains(strings.Join(res.advice, "\n"), "10.0.0.9") {
		t.Fatalf("the client with no secret is not named: %v", res.advice)
	}
}

// A check that could not run reads exactly like one that passed, and the half a
// KEK-less process cannot compare is the one this check is mostly asked about.
func TestDoctorSaysWhenItCouldNotCompareSecrets(t *testing.T) {
	res := renderProjection(store.NasPlan{Unchanged: 3, SecretsCompared: false})
	if res.status != statusWarn {
		t.Fatalf("status = %s, want %s — silence here is indistinguishable from agreement", res.status, statusWarn)
	}
	if !strings.Contains(strings.Join(res.advice, "\n"), "FOYER_KEK") {
		t.Fatalf("the advice does not say why secrets were not compared: %v", res.advice)
	}
}

// A projection that is current says so, or every other assertion here is satisfied
// by a renderer that always warns.
func TestDoctorIsQuietWhenTheProjectionIsCurrent(t *testing.T) {
	res := renderProjection(store.NasPlan{Unchanged: 4, SecretsCompared: true})
	if res.status != statusOK {
		t.Fatalf("status = %s (%v), want %s", res.status, res.advice, statusOK)
	}
}

// TestDoctorAgreesWithTheStartupAssertionOnThePolicyListener.
//
// The name is literal: `policyExposureCheck` calls
// `config.PolicyBoundaryError`, so the two cannot disagree. What is pinned here is
// the rendering of each state.
//
// **"TLS on a routable address" is statusFail**: encrypted, no client
// certificate required, on a bridge network. A row asserting statusOK there would
// be the worst kind of test — a suite holding a defect in place, red the moment
// somebody removed the exposure. The row that is OK is the one carrying a
// client CA.
func TestDoctorAgreesWithTheStartupAssertionOnThePolicyListener(t *testing.T) {
	cases := []struct {
		name       string
		cfg        config.Config
		wantStatus checkStatus
	}{
		{"loopback is fine", config.Config{PolicyAddr: "127.0.0.1:8080"}, statusOK},
		{"localhost is loopback", config.Config{PolicyAddr: "localhost:8080"}, statusOK},
		{
			"TLS on a routable address is not mTLS",
			config.Config{PolicyAddr: "10.0.0.5:8080", PolicyTLSCert: "c.pem", PolicyTLSKey: "k.pem"},
			statusFail,
		},
		{
			"mTLS on a routable address",
			config.Config{PolicyAddr: "10.0.0.5:8080", PolicyTLSCert: "c.pem", PolicyTLSKey: "k.pem", PolicyClientCA: "ca.pem"},
			statusOK,
		},
		{
			"TLS on loopback with nobody checked is worth saying",
			config.Config{PolicyAddr: "127.0.0.1:8080", PolicyTLSCert: "c.pem", PolicyTLSKey: "k.pem"},
			statusWarn,
		},
		{"plaintext on a routable address", config.Config{PolicyAddr: "10.0.0.5:8080"}, statusFail},
		{"plaintext bound wide", config.Config{PolicyAddr: "0.0.0.0:8080"}, statusFail},
		{"the lab says so out loud", config.Config{PolicyAddr: "10.0.0.5:8080", LabInsecure: true}, statusWarn},
		{
			"and the lab acknowledgement does not lift mTLS",
			config.Config{PolicyAddr: "0.0.0.0:8080", PolicyTLSCert: "c.pem", PolicyTLSKey: "k.pem", LabInsecure: true},
			statusFail,
		},
		{"not host:port", config.Config{PolicyAddr: "8080"}, statusFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policyExposureCheck(tc.cfg)
			if got.status != tc.wantStatus {
				t.Fatalf("status = %s, want %s (%s)", got.status, tc.wantStatus, got.detail)
			}
		})
	}
}

// A warning is not a failure and a check that could not run is neither. `skip` is
// counted separately on purpose: a check that did not run reads exactly like one
// that passed.
func TestDoctorExitsNonZeroOnlyForFailures(t *testing.T) {
	var buf bytes.Buffer
	failed := renderChecks(&buf, []checkResult{
		{"a", statusOK, "fine", nil},
		{"b", statusWarn, "worth knowing", nil},
		{"c", statusSkip, "radiusd is not on PATH", nil},
	})
	if failed != 0 {
		t.Fatalf("failed = %d, want 0", failed)
	}
	if !strings.Contains(buf.String(), "1 not checked") {
		t.Fatalf("a check that did not run is counted as passing:\n%s", buf.String())
	}

	buf.Reset()
	if failed := renderChecks(&buf, []checkResult{{"a", statusFail, "broken", nil}}); failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
}

// Doctor and the control plane read one configuration.
//
// A private copy of the three variable names, their defaults and the lab
// acknowledgement drifts in the direction that matters: `cmd/foyer` admits to
// being a lab only for the exact sentence `config.LabAcknowledgement` names, and a
// copy that accepts any non-empty value, with FOYER_LAB_INSECURE=yes and a
// routable Policy API, says:
//
//	doctor says warn: plaintext on a non-loopback address, permitted only by
//	FOYER_LAB_INSECURE
//
// while `cmd/foyer` refuses to start on exactly that configuration. A diagnostic
// disagreeing with the thing it diagnoses, about a §2 boundary.
func TestDoctorReadsTheSameConfigurationTheControlPlaneStartsFrom(t *testing.T) {
	policyTLS := func(t *testing.T) checkResult {
		t.Helper()
		for _, c := range listenerChecks() {
			if c.name == "policy-tls" {
				return c
			}
		}
		t.Fatal("no policy-tls check was produced")
		return checkResult{}
	}

	t.Run("a near miss is not an acknowledgement", func(t *testing.T) {
		t.Setenv("FOYER_POLICY_ADDR", "10.0.0.5:8080")
		t.Setenv("FOYER_POLICY_TLS_CERT", "")
		t.Setenv("FOYER_POLICY_TLS_KEY", "")
		for _, near := range []string{"true", "1", "yes", "i-am-a-lab"} {
			t.Setenv(config.LabInsecureVar, near)
			if got := policyTLS(t); got.status != statusFail {
				t.Errorf("FOYER_LAB_INSECURE=%q reports %s, and the control plane refuses to start on it",
					near, got.status)
			}
		}
	})

	t.Run("the acknowledgement is honoured", func(t *testing.T) {
		t.Setenv("FOYER_POLICY_ADDR", "10.0.0.5:8080")
		t.Setenv("FOYER_POLICY_TLS_CERT", "")
		t.Setenv("FOYER_POLICY_TLS_KEY", "")
		t.Setenv(config.LabInsecureVar, config.LabAcknowledgement)
		if got := policyTLS(t); got.status != statusWarn {
			t.Errorf("the acknowledged lab reports %s, want %s — a doctor that fails on a lab is one nobody runs",
				got.status, statusWarn)
		}
	})

	// And the addresses come from the same place: a doctor dialling a port nobody
	// is bound to reports "nothing is listening" for a control plane that is
	// running perfectly well.
	//
	// Asserted against three addresses nothing will be listening on rather than
	// against the defaults, because a machine that happens to have something on
	// 8080 would satisfy a defaults-based assertion without checking anything.
	t.Run("the addresses come from the control plane's own configuration", func(t *testing.T) {
		addrs := map[string]string{
			"FOYER_PORTAL_ADDR": "127.0.0.1:9481",
			"FOYER_ADMIN_ADDR":  "127.0.0.1:9482",
			"FOYER_POLICY_ADDR": "127.0.0.1:9483",
		}
		for v, a := range addrs {
			t.Setenv(v, a)
		}
		checks := listenerChecks()
		for i, v := range []string{"FOYER_PORTAL_ADDR", "FOYER_ADMIN_ADDR", "FOYER_POLICY_ADDR"} {
			if checks[i].status == statusOK {
				t.Fatalf("%s answered on %s, so this case is measuring something that is running", checks[i].name, addrs[v])
			}
			if !strings.Contains(checks[i].detail, addrs[v]) {
				t.Errorf("%s reports %q, which does not name %s = %q",
					checks[i].name, checks[i].detail, v, addrs[v])
			}
		}
	})
}
