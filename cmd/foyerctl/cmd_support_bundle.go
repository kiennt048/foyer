package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/version"
)

func init() {
	Register(&Command{
		Name:    "support-bundle",
		Usage:   "[--out <file.tar.gz>] [--add <label>=<path|->]... [--redact-value <v>]... [--no-env]",
		Summary: "Collect diagnostics into a redacted archive",
		Run:     runSupportBundle,
	})
}

// `foyerctl support-bundle` exists because there will be a "send us your logs"
// button, and `radiusd -X` prints every RADIUS shared secret in cleartext
// (docs/rules/security.md §7). **Redaction is the feature; collection is the
// scaffolding around it.**
//
// ── the rule that shapes the redactor ────────────────────────────────────────
//
// Enumerate *shapes*, not values. The write-time guard that should have caught
// two hardcoded secrets in clients.conf.tmpl matched a list of known-bad values
// — FreeRADIUS's shipped example and four placeholders — so two secrets with
// names somebody had invented sat there until somebody read the file. A redactor
// built the same way removes the secrets you already thought of, and **a secret
// you have never seen is the one that leaks.**
//
// So the rules below key on the *form* of a line: an assignment whose key names
// a credential, a URL carrying userinfo, an HTTP authorization header, a PEM
// private key block, a JWT. None of them needs to have seen the value.
//
// There is one value-keyed rule and it is deliberate rather than a relapse:
// `--redact-value`, plus every environment value whose variable name is itself
// credential-shaped. That covers what shapes cannot — a secret echoed into a log
// line with nothing beside it to recognise. It is a second layer over the shape
// rules, never a substitute for them.
//
// ── what is deliberately not claimed ─────────────────────────────────────────
//
// A credential under a key nobody would call a credential is not redacted by
// shape, and no amount of regex fixes that in general. The manifest therefore
// reports how many substitutions each rule class made, so a reader can see what
// was done rather than trust that something was.

// redactionMark is what replaces a secret. It carries the length of what it
// replaced, because "there was a 20-byte value here" is diagnostically useful
// and is not the value.
func redactionMark(n int) string {
	return fmt.Sprintf("«redacted:%d bytes»", n)
}

// secretWord is the class of key names that make the value beside them a
// credential. It is a class of *words*, so `Tunnel-Password`, `db_password` and
// `"apiKey"` all land in it without being listed.
const secretWord = `(?:secret|password|passwd|pwd|token|api[-_]?key|apikey|kek|credential|passphrase|private[-_]?key|bearer)`

// valueToken is how far a redaction reaches: a quoted string, or a run of
// characters that stops at whitespace and at the punctuation that ends a value
// in JSON, YAML and unlang. Reaching to end-of-line instead would eat the rest
// of a single-line JSON log record — over-redaction that costs the reader the
// context the bundle was collected for.
const valueToken = `(?:"[^"\n]*"|'[^'\n]*'|[^\s,;}\]\n]+)`

type redactRule struct {
	name string
	re   *regexp.Regexp
	// repl builds the replacement from the submatch. Every rule replaces only
	// the credential-bearing group and puts everything else back verbatim.
	repl func(m []string) string
}

var redactRules = []redactRule{
	{
		// A PEM key block, whole, and replaced by a mark rather than by empty
		// delimiters — first, because it spans lines and the line-oriented rules
		// below would only nibble at it.
		name: "pem-private-key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		repl: func(m []string) string { return "«redacted PEM key block: " + fmt.Sprint(len(m[0])) + " bytes»" },
	},
	{
		// `Authorization: Bearer …` and `Proxy-Authorization: Basic …`. This one
		// reaches to end of line on purpose: the scheme and the credential are
		// separated by a space, so a value-token rule would redact "Bearer" and
		// leave the token.
		name: "authorization-header",
		re:   regexp.MustCompile(`(?i)((?:proxy-)?authorization\s*:\s*)(\S.*)`),
		repl: func(m []string) string { return m[1] + redactionMark(len(m[2])) },
	},
	{
		// A URL carrying userinfo. `postgres://foyer:pw@host/db` is how the
		// database password reaches this process, so it is how the database
		// password reaches a log line.
		name: "url-userinfo",
		re:   regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^\s:/@]+):([^\s/@]+)@`),
		repl: func(m []string) string { return m[1] + m[2] + ":" + redactionMark(len(m[3])) + "@" },
	},
	{
		// The assignment shape, and the rule that carries most of the load.
		// A key is any run of identifier characters — optionally quoted, so JSON
		// keys match — that *contains* a word from the credential class.
		name: "credential-assignment",
		re: regexp.MustCompile(`(?i)(["']?[A-Za-z0-9_.\[\]-]*` + secretWord +
			`[A-Za-z0-9_.\[\]-]*["']?\s*(?:=>|=|:)\s*)(` + valueToken + `)`),
		repl: func(m []string) string { return m[1] + redactionMark(len(m[2])) },
	},
	{
		// A JWT, which is a credential with no key beside it by construction:
		// three base64url segments joined by dots, the first the base64 of a
		// JSON header and therefore starting `eyJ`.
		name: "jwt",
		re:   regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b`),
		repl: func(m []string) string { return redactionMark(len(m[0])) },
	},
}

// Redactor rewrites bytes on their way into the bundle and counts what it did.
//
// It is the only path into the archive. There is no "add this file raw" flag,
// because a flag like that is the one somebody reaches for at 2 a.m.
type Redactor struct {
	// literals are exact values to remove wherever they appear, whatever is
	// beside them. See the note above: a second layer, not a substitute.
	literals []string
	counts   map[string]int
}

// NewRedactor builds a redactor over the shape rules, plus any exact values the
// caller holds.
func NewRedactor(literals []string) *Redactor {
	r := &Redactor{counts: map[string]int{}}
	for _, l := range literals {
		// A short literal would redact half the document. Four characters is
		// already generous for something claiming to be a credential.
		if len(strings.TrimSpace(l)) >= 4 {
			r.literals = append(r.literals, l)
		}
	}
	// Longest first, so a literal that contains another does not leave the
	// shorter one's mark embedded in the longer one's replacement.
	sort.Slice(r.literals, func(i, j int) bool { return len(r.literals[i]) > len(r.literals[j]) })
	return r
}

// Redact returns the redacted text and the number of substitutions made.
func (r *Redactor) Redact(in []byte) ([]byte, int) {
	made := 0
	out := in

	for _, lit := range r.literals {
		n := bytes.Count(out, []byte(lit))
		if n > 0 {
			out = bytes.ReplaceAll(out, []byte(lit), []byte(redactionMark(len(lit))))
			r.counts["known-value"] += n
			made += n
		}
	}

	for _, rule := range redactRules {
		n := 0
		out = rule.re.ReplaceAllFunc(out, func(match []byte) []byte {
			sub := rule.re.FindStringSubmatch(string(match))
			if sub == nil {
				return match
			}
			n++
			return []byte(rule.repl(sub))
		})
		if n > 0 {
			r.counts[rule.name] += n
			made += n
		}
	}
	return out, made
}

// Counts reports substitutions per rule class, for the manifest.
func (r *Redactor) Counts() map[string]int { return r.counts }

// redactField runs one manifest field through the same rules.
func redactField(r *Redactor, s string) string {
	if s == "" {
		return s
	}
	out, _ := r.Redact([]byte(s))
	return string(out)
}

// bundleSource is one thing that was asked for, and what became of it.
type bundleSource struct {
	Label       string `json:"label"`
	From        string `json:"from"`
	Bytes       int    `json:"bytes"`
	Redactions  int    `json:"redactions"`
	Skipped     string `json:"skipped,omitempty"`
	ArchivePath string `json:"archive_path,omitempty"`
}

type bundleManifest struct {
	Format int    `json:"format"`
	Tool   string `json:"tool"`
	// Version is the release; Revision is the build. A bundle that names only the
	// release names the same thing for every build ever made of it, which is no
	// answer at all to the first question anybody asks of a bundle.
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	CreatedAt string `json:"created_at"`

	Sources    []bundleSource `json:"sources"`
	Redactions map[string]int `json:"redactions_by_rule"`
	Note       string         `json:"note"`
}

const manifestNote = "Every byte in this archive passed through foyerctl's redactor. " +
	"The rules match the SHAPE of a credential — an assignment whose key names one, a URL with userinfo, " +
	"an authorization header, a PEM key block, a JWT — plus any exact values the operator supplied. " +
	"A credential under a key nobody would call a credential is not covered by shape alone; " +
	"redactions_by_rule is here so you can see what was actually done rather than assume."

func runSupportBundle(args []string) error {
	fs := flag.NewFlagSet("support-bundle", flag.ExitOnError)
	out := fs.String("out", "", "Archive to write (default foyer-support-<timestamp>.tar.gz)")
	noEnv := fs.Bool("no-env", false, "Do not include the process environment, redacted, in the bundle")
	force := fs.Bool("force", false, "Overwrite the archive if it already exists")
	maxBytes := fs.Int64("max-bytes", 8<<20, "Skip any single file larger than this")
	var adds stringList
	var literals stringList
	fs.Var(&adds, "add", "label=path to collect; path may be - for standard input. Repeatable")
	fs.Var(&literals, "redact-value", "An exact value to remove wherever it appears. Repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *out == "" {
		*out = fmt.Sprintf("foyer-support-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
	}
	if _, err := os.Stat(*out); err == nil && !*force {
		return fmt.Errorf("%s already exists; pass --force to overwrite", *out)
	}

	// Everything credential-shaped in this process's own environment becomes a
	// literal to remove. FOYER_KEK and the password inside FOYER_DATABASE_URL
	// are the two that matter, and they are the two most likely to appear in a
	// log line with no key beside them.
	lits := append([]string{}, literals...)
	lits = append(lits, environmentSecrets(os.Environ())...)
	red := NewRedactor(lits)

	man := bundleManifest{
		Format:    1,
		Tool:      "foyerctl support-bundle",
		Version:   Version,
		Revision:  version.Revision(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Note:      manifestNote,
	}

	// Assembled in memory before anything is written, so that a failure part way
	// through leaves no half-written archive that looks complete.
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	tw := tar.NewWriter(gz)

	if !*noEnv {
		man.Sources = append(man.Sources, addEnvironment(tw, red, os.Environ()))
	}

	for _, a := range adds {
		label, target, ok := strings.Cut(a, "=")
		if !ok || label == "" || target == "" {
			return fmt.Errorf("--add wants label=path, got %q", a)
		}
		srcs, err := addTarget(tw, red, label, target, *maxBytes)
		if err != nil {
			return err
		}
		man.Sources = append(man.Sources, srcs...)
	}

	// The manifest carries operator-supplied strings — the labels and the paths
	// — and a path can be a credential, so those go through the redactor too.
	//
	// Field by field rather than over the marshalled document, and that is not a
	// nicety: run over the whole JSON, the credential-assignment rule matches the
	// manifest's *own* keys. `"credential-assignment": 3` and `"pem-private-key":
	// 1` are assignments whose key names a credential, so the counts were
	// redacted and the manifest stopped being JSON. Caught by the test that
	// parses it.
	for i := range man.Sources {
		man.Sources[i].Label = redactField(red, man.Sources[i].Label)
		man.Sources[i].From = redactField(red, man.Sources[i].From)
		man.Sources[i].ArchivePath = redactField(red, man.Sources[i].ArchivePath)
	}
	man.Redactions = red.Counts()
	blob, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	if err := writeEntry(tw, "manifest.json", blob); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	// 0600: a support bundle is a redacted document, not a public one. Nothing in
	// it should be a credential, and it is still the operator's diagnostics.
	if err := os.WriteFile(*out, body.Bytes(), 0o600); err != nil {
		return err
	}

	total := 0
	for _, n := range man.Redactions {
		total += n
	}
	fmt.Printf("Wrote %s (%d bytes, %d source(s), %d redaction(s))\n", *out, body.Len(), len(man.Sources), total)
	for _, s := range man.Sources {
		if s.Skipped != "" {
			fmt.Printf("  skipped %s: %s\n", s.From, s.Skipped)
		}
	}
	if total == 0 {
		fmt.Println("  no redactions fired — that is either a clean bundle or the wrong sources; check manifest.json")
	}
	return nil
}

// addTarget collects one --add. A directory becomes one entry per file.
func addTarget(tw *tar.Writer, red *Redactor, label, target string, maxBytes int64) ([]bundleSource, error) {
	if target == "-" {
		blob, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading standard input for %q: %w", label, err)
		}
		return []bundleSource{addBlob(tw, red, label, "<stdin>", path.Join(label, "stdin.txt"), blob)}, nil
	}

	info, err := os.Stat(target)
	if err != nil {
		// A named source that is not there is reported in the bundle rather than
		// failing the command: the operator asked for six things and got five,
		// and which one is missing is itself diagnostic.
		return []bundleSource{{Label: label, From: target, Skipped: "does not exist or is unreadable"}}, nil
	}

	if !info.IsDir() {
		return []bundleSource{addFile(tw, red, label, target, path.Join(label, filepath.Base(target)), maxBytes)}, nil
	}

	var srcs []bundleSource
	walkErr := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			srcs = append(srcs, bundleSource{Label: label, From: p, Skipped: "unreadable"})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(target, p)
		if rerr != nil {
			rel = filepath.Base(p)
		}
		srcs = append(srcs, addFile(tw, red, label, p, path.Join(label, filepath.ToSlash(rel)), maxBytes))
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return srcs, nil
}

func addFile(tw *tar.Writer, red *Redactor, label, from, archivePath string, maxBytes int64) bundleSource {
	info, err := os.Stat(from)
	if err != nil {
		return bundleSource{Label: label, From: from, Skipped: "unreadable"}
	}
	if info.Size() > maxBytes {
		return bundleSource{Label: label, From: from, Skipped: fmt.Sprintf("%d bytes, over the --max-bytes limit", info.Size())}
	}
	blob, err := os.ReadFile(from)
	if err != nil {
		return bundleSource{Label: label, From: from, Skipped: "unreadable"}
	}
	return addBlob(tw, red, label, from, archivePath, blob)
}

// addBlob is the single door into the archive, and it redacts before it writes.
//
// Binary content is refused rather than included: the rules are line- and
// token-shaped, so a credential inside a database file, a core dump or a gzipped
// log is not something this can claim to have removed. Saying so is worth more
// than shipping the file.
func addBlob(tw *tar.Writer, red *Redactor, label, from, archivePath string, blob []byte) bundleSource {
	if isBinary(blob) {
		return bundleSource{Label: label, From: from, Skipped: "not text; the redactor cannot scan it, so it is left out"}
	}
	clean, n := red.Redact(blob)
	if err := writeEntry(tw, archivePath, clean); err != nil {
		return bundleSource{Label: label, From: from, Skipped: "could not be written to the archive"}
	}
	return bundleSource{Label: label, From: from, Bytes: len(clean), Redactions: n, ArchivePath: archivePath}
}

func addEnvironment(tw *tar.Writer, red *Redactor, environ []string) bundleSource {
	sorted := append([]string{}, environ...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("# The process environment as foyerctl saw it.\n")
	b.WriteString("# Values are redacted by the same rules as everything else in this bundle:\n")
	b.WriteString("# a variable whose NAME is credential-shaped has its value removed, whatever the value is.\n\n")
	for _, kv := range sorted {
		b.WriteString(kv)
		b.WriteString("\n")
	}
	return addBlob(tw, red, "environment", "<environ>", "environment/environ.txt", []byte(b.String()))
}

// environmentSecrets harvests the values this process holds that are credentials
// by the name they arrived under, so they can be removed from *other* sources
// too — a log line quoting the KEK does not carry `KEK=` in front of it.
func environmentSecrets(environ []string) []string {
	key := regexp.MustCompile(`(?i)^[A-Za-z0-9_.\[\]-]*` + secretWord + `[A-Za-z0-9_.\[\]-]*$`)
	var out []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if key.MatchString(name) {
			out = append(out, value)
			continue
		}
		// A URL-shaped value carries its credential inside it. The userinfo rule
		// rewrites the URL wherever it appears, but the bare password may also
		// appear on its own, so lift it out as a literal as well.
		if pw := urlPassword(value); pw != "" {
			out = append(out, pw)
		}
	}
	return out
}

var urlUserinfo = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s:/@]+:([^\s/@]+)@`)

func urlPassword(value string) string {
	m := urlUserinfo.FindStringSubmatch(value)
	if m == nil {
		return ""
	}
	return m[1]
}

// isBinary is the same heuristic git uses: a NUL byte near the start.
func isBinary(blob []byte) bool {
	head := blob
	if len(head) > 8192 {
		head = head[:8192]
	}
	return bytes.IndexByte(head, 0) >= 0
}

func writeEntry(tw *tar.Writer, name string, blob []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(blob)),
		ModTime: time.Now().UTC(),
		Format:  tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(blob)
	return err
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	if v == "" {
		return errors.New("empty value")
	}
	*s = append(*s, v)
	return nil
}
