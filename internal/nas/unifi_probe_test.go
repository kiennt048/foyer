package nas

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Whether §4's access-point clause can be satisfied on the UniFi path, and from
// which API — the legacy device list's vap_table, or the Integration API's client
// object — is a question about a real controller. These tests cover the
// instrument that settles which reading is true — they do not settle it, and
// nothing here is evidence about a real controller.

const testLocalAdmin = "local-admin"

// a passphrase, not a shared secret: it exists so the output can be searched
// for it and found absent.
const testLocalPassword = "correct horse battery staple"

func legacyDriver(t *testing.T, fc *fixtureController, apiKey string) *UniFiDriver {
	t.Helper()
	return fc.driver(t, func(c *UniFiConfig) {
		c.APIKey = apiKey
		c.Username = testLocalAdmin
		c.Password = testLocalPassword
		c.Timeout = 5 * time.Second
	})
}

// TestLegacyDeviceDump is the legacy device list's instrument: does the BSSID → access point
// mapping the redirect needs come back, and does the tool say so plainly?
func TestLegacyDeviceDump(t *testing.T) {
	cases := []struct {
		name         string
		flavour      UniFiFlavour
		devicesFile  string
		trailingOnly bool
		wantPath     string
		wantFinding  string
		wantAbsent   string
	}{
		{
			name: "unifi os, vap_table present", flavour: FlavourUniFiOS,
			devicesFile: "unifi-legacy-devices.json",
			wantPath:    "/proxy/network/api/s/default/stat/device",
			wantFinding: "2 of 3 devices carry a vap_table, 5 BSSIDs in total",
		},
		{
			name: "standalone, vap_table present", flavour: FlavourStandalone,
			devicesFile: "unifi-legacy-devices.json",
			wantPath:    "/api/s/default/stat/device",
			wantFinding: "2 of 3 devices carry a vap_table",
		},
		{
			name: "no device carries a vap_table — the finding that matters most", flavour: FlavourUniFiOS,
			devicesFile: "unifi-legacy-devices-no-vap.json",
			wantPath:    "/proxy/network/api/s/default/stat/device",
			wantFinding: "NO device carries a vap_table",
			wantAbsent:  "BSSIDs in total",
		},
		{
			name: "only the trailing-slash form answers", flavour: FlavourUniFiOS,
			devicesFile: "unifi-legacy-devices.json", trailingOnly: true,
			wantPath:    "/proxy/network/api/s/default/stat/device/",
			wantFinding: "2 of 3 devices carry a vap_table",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{
				flavour:                 tc.flavour,
				issueCSRF:               testCSRFToken,
				legacyDevicesFile:       tc.devicesFile,
				deviceTrailingSlashOnly: tc.trailingOnly,
			})
			d := legacyDriver(t, fc, testAPIKey)

			steps := d.RunProbe(context.Background(), UniFiProbePlan{SiteID: "default", ClientMAC: testMAC})

			var dump *ProbeStep
			for i := range steps {
				if steps[i].Status == 200 && strings.Contains(steps[i].URL, "/stat/device") {
					dump = &steps[i]
				}
			}
			if dump == nil {
				t.Fatalf("no successful legacy device dump; steps: %v", stepURLs(steps))
			}
			if !strings.HasSuffix(dump.URL, tc.wantPath) {
				t.Errorf("device dump URL = %q, want it to end in %q", dump.URL, tc.wantPath)
			}
			if dump.Body == "" {
				t.Errorf("the device list was not recorded raw; a summary loses the field nobody predicted")
			}
			if !strings.Contains(dump.Body, "\"vap_table\"") && tc.devicesFile == "unifi-legacy-devices.json" {
				t.Errorf("the raw body does not contain vap_table, so it was not printed verbatim")
			}
			joined := strings.Join(dump.Findings, "\n")
			if !strings.Contains(joined, tc.wantFinding) {
				t.Errorf("findings do not say %q:\n%s", tc.wantFinding, joined)
			}
			if tc.wantAbsent != "" && strings.Contains(joined, tc.wantAbsent) {
				t.Errorf("findings claim %q for a controller that returned no vap_table:\n%s", tc.wantAbsent, joined)
			}
		})
	}
}

// TestLegacyProbe_NeverRecordsTheCredential: the login body carries a local
// admin password, and the output is meant to be shared.
func TestLegacyProbe_NeverRecordsTheCredential(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour: FlavourUniFiOS, issueCSRF: testCSRFToken,
		legacyDevicesFile: "unifi-legacy-devices.json",
	})
	d := legacyDriver(t, fc, testAPIKey)

	steps := d.RunProbe(context.Background(), UniFiProbePlan{SiteID: "default", ClientMAC: testMAC})

	var sawLogin bool
	for _, s := range steps {
		if strings.Contains(s.URL, "/api/auth/login") {
			sawLogin = true
		}
		for field, value := range map[string]string{
			"body": s.Body, "requestBody": s.RequestBody, "url": s.URL,
			"note": s.Note, "findings": strings.Join(s.Findings, "\n"),
		} {
			if strings.Contains(value, testLocalPassword) {
				t.Fatalf("the local admin password is in step %q's %s", s.What, field)
			}
			if strings.Contains(value, testAPIKey) {
				t.Fatalf("the API key is in step %q's %s", s.What, field)
			}
			if strings.Contains(value, testCSRFToken) {
				t.Fatalf("the CSRF token is in step %q's %s", s.What, field)
			}
		}
	}
	if !sawLogin {
		t.Fatalf("no login step was recorded; steps: %v", stepURLs(steps))
	}
}

// TestLegacyProbe_RunsWithoutAnAPIKey is the case that matters most: if
// the Integrations tab does not exist on this build, the legacy path is the
// primary measurement and must not be lost with the Integration API phase.
func TestLegacyProbe_RunsWithoutAnAPIKey(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour: FlavourUniFiOS, issueCSRF: testCSRFToken,
		legacyDevicesFile: "unifi-legacy-devices.json",
	})
	d := legacyDriver(t, fc, "") // no API key at all

	steps := d.RunProbe(context.Background(), UniFiProbePlan{SiteID: "default", ClientMAC: testMAC})

	var sawIntegrationCall, sawDeviceDump bool
	for _, s := range steps {
		if strings.Contains(s.URL, "/integration/v1/") {
			sawIntegrationCall = true
		}
		if s.Status == 200 && strings.Contains(s.URL, "/stat/device") {
			sawDeviceDump = true
		}
	}
	if sawIntegrationCall {
		t.Errorf("the Integration API was called with no key; steps: %v", stepURLs(steps))
	}
	if !sawDeviceDump {
		t.Fatalf("the legacy phase did not run without an API key, which is the case it exists for; steps: %v",
			stepURLs(steps))
	}
	if !strings.Contains(joinNotes(steps), "no API key") {
		t.Errorf("the output never says why the Integration API steps were skipped")
	}
}

// TestLegacyProbe_SkippedStepNamesItsCredential: a skipped step that does not
// say what it needed is a step nobody comes back to.
func TestLegacyProbe_SkippedStepNamesItsCredential(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS})
	d := fc.driver(t) // API key only, no local admin

	steps := d.RunProbe(context.Background(), UniFiProbePlan{SiteID: "default", ClientMAC: testMAC})
	notes := joinNotes(steps)

	for _, want := range []string{"LOCAL ADMIN username and password", "-username", "FOYER_UNIFI_PASSWORD"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the skip note never mentions %q", want)
		}
	}
	for _, s := range steps {
		if strings.Contains(s.URL, "/stat/device") {
			t.Errorf("the device list was requested with no credential for it")
		}
	}
}

// TestAPIdentityFindings answers the access-point question mechanically, and has to be able to say
// both things: a body with no access-point field must be reported as having
// none, and one that carries a candidate must have it named. A detector that
// only ever produces one of those answers is not a detector.
func TestAPIdentityFindings(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantAny    []string
		wantAbsent []string
	}{
		{
			name: "the documented client object names no access point",
			body: string(readFixture(t, "unifi-client-detail.json")),
			wantAny: []string{
				"NO field in this answer identifies an access point",
				"the legacy device list is the only known source",
			},
			wantAbsent: []string{"could identify an access point"},
		},
		{
			name: "a client object carrying an uplink names it",
			body: string(readFixture(t, "unifi-client-detail-with-uplink.json")),
			wantAny: []string{
				"could identify an access point or uplink",
				"uplinkDeviceId",
				"a device id is not a MAC",
			},
			wantAbsent: []string{"NO field in this answer"},
		},
		{
			name: "a nested field is found, not only a top-level one",
			body: `{"id":"x","access":{"type":"GUEST","uplink":{"apMac":"94:2a:6f:d0:30:57"}}}`,
			wantAny: []string{
				"access.uplink.apMac",
				`"94:2a:6f:d0:30:57"`,
			},
		},
		{
			name: "an answer that is not JSON produces no claim either way",
			body: "<html>login required</html>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(apIdentityFindings(200, tc.body), "\n")
			if len(tc.wantAny) == 0 && got != "" {
				t.Fatalf("expected no findings, got:\n%s", got)
			}
			for _, want := range tc.wantAny {
				if !strings.Contains(got, want) {
					t.Errorf("findings do not contain %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("findings wrongly contain %q:\n%s", absent, got)
				}
			}
		})
	}
}

// TestClientDetailStepCarriesTheAPQuestion: the access-point scan has to be
// attached to the step that reads the whole client object, or it answers a
// question nobody sees.
func TestClientDetailStepCarriesTheAPQuestion(t *testing.T) {
	cases := []struct {
		name       string
		detailFile string
		wantIn     string
	}{
		{"no AP field", "unifi-client-detail.json", "NO field in this answer identifies an access point"},
		{"an AP candidate", "unifi-client-detail-with-uplink.json", "uplinkDeviceId"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{
				flavour: FlavourUniFiOS, detailFile: tc.detailFile,
			})
			d := fc.driver(t)

			steps := d.RunProbe(context.Background(), UniFiProbePlan{SiteID: "default", ClientMAC: testMAC})

			var clientDetail *ProbeStep
			for i := range steps {
				if steps[i].Name == "client-detail" {
					clientDetail = &steps[i]
					break
				}
			}
			if clientDetail == nil {
				t.Fatalf("no client-detail step; steps: %v", stepURLs(steps))
			}
			if !strings.Contains(clientDetail.Note, "identify an access point") {
				t.Errorf("the client-detail step's note does not raise the access-point question: %q", clientDetail.Note)
			}
			if !strings.Contains(strings.Join(clientDetail.Findings, "\n"), tc.wantIn) {
				t.Errorf("client-detail findings do not contain %q:\n%s", tc.wantIn, strings.Join(clientDetail.Findings, "\n"))
			}
		})
	}
}

func stepURLs(steps []ProbeStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		if s.URL != "" {
			out = append(out, s.Method+" "+s.URL)
		}
	}
	return out
}

func joinNotes(steps []ProbeStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Note)
		b.WriteString("\n")
		b.WriteString(strings.Join(s.Findings, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
