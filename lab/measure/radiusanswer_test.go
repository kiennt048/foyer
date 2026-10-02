package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestRadiusAnswerClassification holds radius-answer.sh — what login-flood.sh
// counts as "the Policy API answered" — against radclient transcripts of each
// answer the generated tree can give.
//
// Counting any `Received Access-*` would report the tree's own reject for a
// Policy API that did not answer ("policy engine unavailable") as the Policy API
// answering during the flood. The strings are
// internal/raddb/templates/default.site.tmpl's.
func TestRadiusAnswerClassification(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not on PATH; login-flood.sh is a bash script and so is its classifier")
	}
	const head = "Sent Access-Request Id 7 from 0.0.0.0:40000 to 127.0.0.1:1812 length 60\n"
	for _, tc := range []struct{ what, transcript, want string }{
		{"no answer", head + "(0) No reply from server for ID 7 socket 3\n", "none"},
		{"the Policy API is down", head +
			"Received Access-Reject Id 7 from 127.0.0.1:1812 to 127.0.0.1:40000 length 47\n" +
			"\tReply-Message = \"policy engine unavailable\"\n", "unavailable"},
		{"the Policy API denied", head +
			"Received Access-Reject Id 7 from 127.0.0.1:1812 to 127.0.0.1:40000 length 38\n" +
			"\tReply-Message = \"denied by policy\"\n", "policy"},
		{"the Policy API allowed", head +
			"Received Access-Accept Id 7 from 127.0.0.1:1812 to 127.0.0.1:40000 length 32\n" +
			"\tSession-Timeout = 3600\n", "policy"},
		{"a fail-open grant", head +
			"Received Access-Accept Id 7 from 127.0.0.1:1812 to 127.0.0.1:40000 length 48\n" +
			"\tSession-Timeout = 600\n\tClass = 0x466f7965723a6465677261646564\n", "degraded"},
		{"a reject that says nothing", head +
			"Received Access-Reject Id 7 from 127.0.0.1:1812 to 127.0.0.1:40000 length 20\n", "unexplained"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `. ./radius-answer.sh && radius_answer`)
			cmd.Stdin = strings.NewReader(tc.transcript)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("radius_answer: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("radius_answer = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTheFloodCountsOnlyThePolicyAPIsAnswers: the classifier is only evidence if
// the flood script uses it for the number it reports.
func TestTheFloodCountsOnlyThePolicyAPIsAnswers(t *testing.T) {
	b, err := os.ReadFile("login-flood.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `. "$ROOT/lab/measure/radius-answer.sh"`) {
		t.Error("login-flood.sh does not source radius-answer.sh")
	}
	if !regexp.MustCompile(`echo "\$\(\(t1 - t0\)\) \$\(radius_answer <<< "\$out"\)"`).MatchString(s) {
		t.Error("probe() does not report radius_answer's classification")
	}
	if !regexp.MustCompile(`(?m)^ANSWERED=\$\(awk '\$2=="policy"'`).MatchString(s) {
		t.Error(`ANSWERED counts something other than the "policy" class`)
	}
}
