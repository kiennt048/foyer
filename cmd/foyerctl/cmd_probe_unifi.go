package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
)

// probe-unifi measures what a real UniFi controller does: client lookup and AP
// resolution, guest authorization, and re-authorization with new limits.
//
// It prints raw responses rather than a summary, because the point is to learn
// what the controller actually sends — including fields nobody predicted. A fact
// belongs in docs/protocol-reality.md only once a transcript has actually
// observed it.

const (
	apiKeyEnv   = "FOYER_UNIFI_API_KEY"
	passwordEnv = "FOYER_UNIFI_PASSWORD"
)

func init() {
	Register(&Command{
		Name:    "probe-unifi",
		Summary: "Measure a real UniFi controller and print raw responses",
		Usage:   "-url https://host[:port] [-site ID] [-mac MAC] [-authorize] [-reauthorize]",
		Run:     runProbeUniFi,
	})
}

func runProbeUniFi(args []string) error {
	fs := flag.NewFlagSet("probe-unifi", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		baseURL  = fs.String("url", "", "controller base URL, e.g. https://192.168.1.1:11443 (required)")
		site     = fs.String("site", "", "site id — the UUID from /v1/sites, NOT the legacy name `default`.\n\tThe Integration API answers 400 for `default`, and a 400 body has no client fields in it,\n\twhich reads as a confident and wrong negative for the client lookup. Left empty on purpose:\n\tthe sites step prints the id to use.")
		mac      = fs.String("mac", "", "client MAC to look up; without it only the site and version steps run")
		keyFile  = fs.String("key-file", "", "file containing the API key; defaults to the "+apiKeyEnv+" environment variable")
		username = fs.String("username", "", "local admin username, for the legacy device-list step")
		pwFile   = fs.String("password-file", "", "file containing the local admin password; defaults to the "+passwordEnv+" environment variable")
		tlsMode  = fs.String("tls", string(nas.TLSVerify), "verify | pinned-sha256 | insecure-skip-verify")
		pin      = fs.String("pin", "", "certificate SHA-256 fingerprint, required with -tls pinned-sha256")
		timeout  = fs.Duration("timeout", 10*time.Second, "per-request timeout")
		doAuth   = fs.Bool("authorize", false, "issue AUTHORIZE_GUEST_ACCESS — CHANGES A LIVE NETWORK")
		doReauth = fs.Bool("reauthorize", false, "issue a second AUTHORIZE_GUEST_ACCESS with the *2 limits; implies -authorize")
		minutes  = fs.Int("minutes", 60, "timeLimitMinutes for the first authorize")
		megabyte = fs.Int("mb", 500, "dataUsageLimitMBytes for the first authorize")
		rx       = fs.Int("rx", 5000, "rxRateLimitKbps for the first authorize")
		tx       = fs.Int("tx", 2000, "txRateLimitKbps for the first authorize")
		minutes2 = fs.Int("minutes2", 15, "timeLimitMinutes for the -reauthorize re-authorize")
		mb2      = fs.Int("mb2", 100, "dataUsageLimitMBytes for the -reauthorize re-authorize")
		rx2      = fs.Int("rx2", 1000, "rxRateLimitKbps for the -reauthorize re-authorize")
		tx2      = fs.Int("tx2", 500, "txRateLimitKbps for the -reauthorize re-authorize")
		asJSON   = fs.Bool("json", false, "emit the transcript as JSON")
		out      = fs.String("out", "", "also write the transcript to this file")
	)

	fs.Usage = func() {
		fmt.Fprint(os.Stderr, probeUsage)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *baseURL == "" {
		fs.Usage()
		return errors.New("-url is required")
	}

	apiKey, err := readSecret(*keyFile, apiKeyEnv)
	if err != nil {
		return fmt.Errorf("reading the API key: %w", err)
	}
	password, err := readSecret(*pwFile, passwordEnv)
	if err != nil {
		return fmt.Errorf("reading the local admin password: %w", err)
	}

	// Two credentials, two phases, and either one alone is a useful session.
	// A controller with no Integrations tab makes the legacy phase the primary
	// measurement rather than the supplement.
	if apiKey == "" && *username == "" {
		return fmt.Errorf("no credential: set %s (or -key-file) for the Integration API steps, "+
			"and/or pass -username with %s (or -password-file) for the legacy device list. "+
			"Neither is taken on the command line, where it would land in shell history",
			apiKeyEnv, passwordEnv)
	}
	if *username != "" && password == "" {
		return fmt.Errorf("-username was given but no password: set %s or pass -password-file", passwordEnv)
	}

	driver, err := nas.NewUniFiDriverWithError(nas.UniFiConfig{
		BaseURL:      *baseURL,
		APIKey:       apiKey,
		Username:     *username,
		Password:     password,
		Site:         *site,
		TLSMode:      nas.TLSMode(*tlsMode),
		PinnedSHA256: *pin,
		Timeout:      *timeout,
	})
	if err != nil {
		return err
	}

	if *doReauth {
		*doAuth = true
	}
	plan := nas.UniFiProbePlan{
		SiteID:         *site,
		ClientMAC:      *mac,
		Authorize:      *doAuth,
		RunReauthorize: *doReauth,
		First: nas.AuthorizeRequest{
			TimeLimitMinutes: *minutes,
			DataUsageLimitMB: *megabyte,
			RxRateLimitKbps:  *rx,
			TxRateLimitKbps:  *tx,
		},
		Second: nas.AuthorizeRequest{
			TimeLimitMinutes: *minutes2,
			DataUsageLimitMB: *mb2,
			RxRateLimitKbps:  *rx2,
			TxRateLimitKbps:  *tx2,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	steps := driver.RunProbe(ctx, plan)

	var rendered string
	if *asJSON {
		b, err := json.MarshalIndent(steps, "", "  ")
		if err != nil {
			return err
		}
		rendered = string(b) + "\n"
	} else {
		rendered = renderSteps(steps, *tlsMode)
	}

	fmt.Print(rendered)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(rendered), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", *out, err)
		}
		fmt.Fprintf(os.Stderr, "\ntranscript written to %s\n", *out)
	}
	return nil
}

const probeUsage = `foyerctl probe-unifi — measure a real UniFi controller.

Two phases, two credentials, and either one alone is a useful session:

  Integration API      an API key, from ` + apiKeyEnv + ` or -key-file
  Legacy device list   -username plus ` + passwordEnv + ` or -password-file

Neither credential is ever a flag: a flag lands in shell history. Give both and
one run answers everything; give only the local admin and you still get the AP
resolution question answered, which is what matters on a controller whose
Integrations tab does not exist.

Read-only by default. -authorize and -reauthorize change a live network.

  export ` + apiKeyEnv + `='…'
  export ` + passwordEnv + `='…'
  foyerctl probe-unifi -url https://192.0.2.20:11443 -tls insecure-skip-verify
  foyerctl probe-unifi -url … -mac 1c:71:25:63:e4:24 -username <local-admin>
  foyerctl probe-unifi -url … -mac 1c:71:25:63:e4:24 -authorize
  foyerctl probe-unifi -url … -mac 1c:71:25:63:e4:24 -reauthorize

Keep the transcript with whatever you record from it, and do not call anything
measured that this run did not actually observe.

Flags:
`

// readSecret takes a credential from a file or an environment variable, never
// from a flag: a flag lands in shell history.
func readSecret(path, env string) (string, error) {
	if path == "" {
		return strings.TrimSpace(os.Getenv(env)), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func renderSteps(steps []nas.ProbeStep, tlsMode string) string {
	var b strings.Builder
	rule := strings.Repeat("─", 78)

	fmt.Fprintf(&b, "foyerctl probe-unifi — %s\n", time.Now().Format(time.RFC3339))
	if nas.TLSMode(tlsMode) == nas.TLSInsecureSkipVerify {
		b.WriteString("TLS: certificate verification DISABLED for this run.\n")
	} else {
		fmt.Fprintf(&b, "TLS: %s\n", tlsMode)
	}
	b.WriteString(rule + "\n")

	for _, s := range steps {
		if s.Method == "" {
			// A conclusion step: no request, just what the run established.
			fmt.Fprintf(&b, "\n[%s] %s\n      %s\n", s.Name, s.What, wrapNote(s.Note))
			writeFindings(&b, s.Findings)
			continue
		}
		fmt.Fprintf(&b, "\n[%s] %s\n", s.Name, s.What)
		fmt.Fprintf(&b, "  %s %s\n", s.Method, s.URL)
		if s.RequestBody != "" {
			fmt.Fprintf(&b, "  request body:\n%s\n", indent(s.RequestBody, "    "))
		}
		if s.Err != "" {
			fmt.Fprintf(&b, "  ERROR: %s\n", s.Err)
		}
		if s.Status != 0 {
			fmt.Fprintf(&b, "  → HTTP %d in %s\n", s.Status, s.Duration.Round(time.Millisecond))
		}
		if s.Body != "" {
			fmt.Fprintf(&b, "%s\n", indent(s.Body, "    "))
		} else if s.Status != 0 {
			b.WriteString("    (empty body)\n")
		}
		if s.Note != "" {
			fmt.Fprintf(&b, "  note: %s\n", wrapNote(s.Note))
		}
		writeFindings(&b, s.Findings)
	}

	b.WriteString("\n" + rule + "\n")
	b.WriteString("Keep this transcript with whatever you record from it. Only a line this run\n")
	b.WriteString("actually observed may be called measured, and only an observed line belongs in\n")
	b.WriteString("docs/protocol-reality.md.\n")
	return b.String()
}

// writeFindings prints what the tool could read out of the raw body. They sit
// under the body rather than replacing it: the raw JSON is the measurement and
// these are a finger pointing at the line that answers the question.
func writeFindings(b *strings.Builder, findings []string) {
	for _, f := range findings {
		fmt.Fprintf(b, "  · %s\n", f)
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// wrapNote keeps a long note readable in a terminal without depending on the
// terminal's width, which is not knowable when the output is redirected.
func wrapNote(note string) string {
	const width = 74
	words := strings.Fields(note)
	var lines []string
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
			continue
		}
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n        ")
}
