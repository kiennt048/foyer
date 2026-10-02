package nas

import (
	"strings"
	"testing"
)

// A negative access-point finding must never be drawn from a body that did not
// answer the question.
//
// Given the legacy site name `default`, the Integration API answers
// `400 'default' is not a valid 'siteId' value`, and a field scan over that ERROR
// body would print "NO field in this answer identifies an access point". That is
// the opposite of the truth — `uplinkDeviceId` is present on the client — and the
// question would be recorded as answered NO on the strength of it.
//
// The distinction this pins is between "measured absent" and "not measured".
func TestAPIdentityFindingsRefusesToConcludeFromAnErrorBody(t *testing.T) {
	// The exact 400 a controller returns for the legacy site name.
	errBody := `{"statusCode":400,"statusName":"BAD_REQUEST",` +
		`"code":"api.request.argument-type-mismatch",` +
		`"message":"'default' is not a valid 'siteId' value",` +
		`"requestPath":"/integration/v1/sites/default/clients"}`

	got := apIdentityFindings(400, errBody)
	if len(got) == 0 {
		t.Fatal("a non-2xx body produced no finding at all; it must say the question was not answered")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "NOT ANSWERED") {
		t.Errorf("a 400 must be reported as NOT ANSWERED, got:\n%s", joined)
	}
	if strings.Contains(joined, "NO field in this answer identifies") {
		t.Errorf("a 400 body produced the MEASURED-ABSENT sentence — this is the error-body defect:\n%s", joined)
	}
}

// The positive path must still work, or the guard above is satisfied by breaking
// the tool. This body is the one the controller actually returned once the site id
// was a UUID.
func TestAPIdentityFindingsStillReportsUplinkDeviceIDOn200(t *testing.T) {
	ok := `{"data":[{"macAddress":"02:00:5e:00:53:11",` +
		`"uplinkDeviceId":"00000000-0000-4000-8000-0000000000a1"}]}`

	joined := strings.Join(apIdentityFindings(200, ok), "\n")
	if !strings.Contains(joined, "uplinkDeviceId") {
		t.Fatalf("the field that answers the access-point question was not reported on a 200:\n%s", joined)
	}
	if strings.Contains(joined, "NOT ANSWERED") {
		t.Errorf("a 200 must be answered, not refused:\n%s", joined)
	}
}

// And an empty-but-valid answer must still be able to say "measured absent",
// because that is a real finding for a controller that genuinely carries no such
// field. Refusing every body would be the opposite over-correction.
func TestAPIdentityFindingsCanStillReportMeasuredAbsence(t *testing.T) {
	joined := strings.Join(apIdentityFindings(200, `{"data":[{"macAddress":"aa:bb:cc:dd:ee:ff"}]}`), "\n")
	if !strings.Contains(joined, "NO field in this answer identifies") {
		t.Errorf("a valid 200 with no identifying field must report measured absence:\n%s", joined)
	}
}
