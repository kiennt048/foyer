// Package export writes the CSV documents the `reports` operations stream, and it
// is the one place a cell is made safe to open in a spreadsheet.
//
// ── what the field does, and what this follows ───────────────────────────────
//
// OWASP's *CSV Injection* page is the reference, and it is short enough to state
// whole: a cell a spreadsheet will evaluate is one that begins with `=`, `+`, `-`,
// `@`, a tab, a carriage return or a line feed, and the full-width `＝ ＋ － ＠`
// behave the same in some locales. It offers three defences — quote every field,
// prefix a single quote, escape embedded quotes — and warns that separators inside
// a value can start a new cell mid-value. This package does all three, and it does
// them for every cell rather than only the ones Go's `encoding/csv` happened to
// quote:
//
//   - **Every field is quoted**, header and empty cells included, with `"` doubled.
//     That is what closes the `;`-locale case: Excel on a machine whose list
//     separator is `;` (much of Europe, and any Windows whose region says so) splits
//     an unquoted `a;=cmd|…` into two cells and evaluates the second. Inside quotes
//     the `;` is data in every reader, so no value can begin a cell of its own,
//     whatever the reader thinks the separator is.
//   - **Every trigger-led cell is prefixed with `'`**, which Excel, LibreOffice and
//     Google Sheets all render as text. OWASP also offers a tab inside the quotes
//     for Excel; the quote was chosen because it is visible — an operator who sees
//     `'=HYPERLINK(…)` in a username cell can tell something was defused, where an
//     invisible leading tab only makes the value look subtly wrong.
//   - **"Trigger-led" looks past what a spreadsheet looks past.** Spreadsheets trim
//     leading blanks before deciding whether a value is a formula, so ` =1+1`,
//     NBSP-`=1+1` and a byte-order mark or zero-width space in front of `=` are
//     formulas a first-character check lets through. The check skips every leading
//     Unicode space (`unicode.IsSpace`, which covers NBSP and NEL) and every format
//     character (category Cf: BOM, ZWSP, ZWJ, bidi marks) and tests the first rune
//     left. The trigger set includes the small-form variants `﹦ ﹢ ﹣ ﹫` (U+FE66,
//     FE62, FE63, FE6B) and the minus sign `−` (U+2212), which a
//     compatibility-normalising reader folds onto the same operators.
//
// ── the one divergence, and why ──────────────────────────────────────────────
//
// **A cell that is wholly a decimal number is left alone.** OWASP's list has `-` on
// it and a naive sanitiser turns `-12` into the text `'-12`. Foyer exports a signed
// figure on purpose — `sessionSeconds` goes negative when a NAS clock is skewed,
// and the contract calls that the anomaly worth seeing — so quoting it would turn
// the one column an operator sorts on to find a broken NAS into text that does not
// sort. A string that matches `^-?[0-9]+(\.[0-9]+)?$` cannot be a formula: there is
// no operator after the sign and no function name. It is still written inside CSV
// quotes like every other field — RFC 4180 quotes are syntax, not a type, and a
// spreadsheet reads `"-12"` as the number −12 — but it gains no `'`.
//
// Anything else that merely *looks* numeric — `-1+1`, `+5`, `-2e3`, ` -12` — is not
// a plain decimal and is prefixed, because each of those is evaluated.
package export

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// byteOrderMark opens every document. Excel reads a CSV without one in the system
// code page, and a Vietnamese venue's, guest's and SSID's names are not ASCII:
// without it `Cà Phê Sữa` opens as mojibake. RFC 4180 neither requires nor forbids
// it; the contract declares it.
const byteOrderMark = "\uFEFF"

// triggers are the leading runes OWASP names, the full-width forms it warns about,
// and the small-form variants and minus sign a compatibility-normalising reader
// folds onto the same four operators.
var triggers = []rune{
	'=', '+', '-', '@', '\t', '\r', '\n',
	'＝', '＋', '－', '＠', // full-width, U+FF1D FF0B FF0D FF20
	'﹦', '﹢', '﹣', '﹫', // small-form, U+FE66 FE62 FE63 FE6B
	'−', // MINUS SIGN, U+2212
}

// Neutralise returns a cell a spreadsheet will display rather than evaluate.
func Neutralise(cell string) string {
	if cell == "" || plainDecimal(cell) {
		return cell
	}
	if isTrigger(firstSignificant(cell)) {
		return "'" + cell
	}
	return cell
}

// firstSignificant is the first rune a spreadsheet looks at when it decides whether
// a cell is a formula: leading spaces (NBSP and NEL included) and format characters
// (BOM, zero-width, bidi marks) are skipped. TAB, CR and LF are spaces to
// `unicode.IsSpace` but are triggers themselves, so they end the skip.
func firstSignificant(cell string) rune {
	for _, r := range cell {
		if r == '\t' || r == '\r' || r == '\n' {
			return r
		}
		if unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		return r
	}
	return 0
}

func isTrigger(r rune) bool {
	for _, t := range triggers {
		if r == t {
			return true
		}
	}
	return false
}

// plainDecimal is `^-?[0-9]+(\.[0-9]+)?$`, written out so the rule reads where it
// is applied.
func plainDecimal(s string) bool {
	s = strings.TrimPrefix(s, "-")
	whole, frac, dotted := strings.Cut(s, ".")
	if !digits(whole) {
		return false
	}
	return !dotted || digits(frac)
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Writer streams one CSV document: byte-order mark, header, rows, CRLF.
//
// It neutralises **every** cell it writes, header included. There is no way to
// write a cell past it, which is the point: a column added later by somebody who
// did not read this package is safe by construction rather than by review.
type Writer struct {
	// out buffers the byte-order mark with the first records, so nothing reaches
	// the underlying writer until a buffer's worth is ready. A caller streaming to
	// an HTTP response relies on that: a failure before the first flush can still
	// be answered with a status, and one after it cannot.
	out     *bufio.Writer
	started bool
	columns int
}

// NewWriter wraps w. Nothing is written until the header.
func NewWriter(w io.Writer) *Writer {
	return &Writer{out: bufio.NewWriter(w)}
}

// Header writes the byte-order mark and the column names.
func (w *Writer) Header(names []string) error {
	if _, err := io.WriteString(w.out, byteOrderMark); err != nil {
		return err
	}
	w.started = true
	w.columns = len(names)
	return w.write(names)
}

// Row writes one record. A record whose width is not the header's is refused: a
// short row shifts every later cell under the wrong heading, and in a spreadsheet
// that is a MAC under `username` with nothing to say so.
func (w *Writer) Row(cells []string) error {
	if !w.started {
		return errNoHeader
	}
	if len(cells) != w.columns {
		return errWidth
	}
	return w.write(cells)
}

// write emits one record: every cell neutralised, then quoted with `"` doubled, and
// the record ended with CRLF. **Every field is quoted, whatever it holds** — that is
// the defence against a reader whose separator is not ours (see the package
// comment). An embedded line break is written as CRLF and a lone CR is dropped: the
// spelling Go's `encoding/csv` used in CRLF mode before this writer replaced it,
// kept so a document's line breaks did not change along with its quoting.
func (w *Writer) write(cells []string) error {
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range Neutralise(c) {
			switch r {
			case '"':
				b.WriteString(`""`)
			case '\r':
			case '\n':
				b.WriteString("\r\n")
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
	}
	b.WriteString("\r\n")
	_, err := w.out.WriteString(b.String())
	return err
}

// Flush pushes buffered records to the underlying writer.
func (w *Writer) Flush() error { return w.out.Flush() }

type exportError string

func (e exportError) Error() string { return string(e) }

const (
	errNoHeader = exportError("export: a row before the header")
	errWidth    = exportError("export: a row whose width is not the header's")
)

// ── cell formatting ──────────────────────────────────────────────────────────
//
// One spelling per kind, because the mock writes the same documents and a
// differential harness compares them byte for byte. An absent value is an empty
// cell and never a zero: `docs/rules/frontend.md`'s "absence is not zero"
// applies to a spreadsheet as much as to a card.

// Time is RFC 3339 in UTC at second precision, or empty.
func Time(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// At is Time for a value that is always present.
func At(t time.Time) string { return Time(&t) }

// Text is the value, or empty.
func Text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Int is the decimal value, or empty.
func Int(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

// Bool is `true` / `false`, or empty.
func Bool(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}

// CodeHint is the last four characters of a voucher code, behind an ellipsis.
//
// **A voucher code never leaves in full.** An unused code is a bearer credential and
// an in-use one still opens the network; four characters are what an operator needs
// to match a row to the slip in front of them. A code of four or fewer characters
// is not a code this product generates and is withheld entirely rather than
// exported whole.
func CodeHint(code string) string {
	r := []rune(code)
	if len(r) <= 4 {
		return ""
	}
	return "…" + string(r[len(r)-4:])
}

// LocallyAdministered reports the IEEE locally-administered bit of a MAC.
//
// **A second copy of `store.MACIsLocallyAdministered`, and a test holds them
// together.** `internal/api` may not import `internal/store` (its CONTRACT
// invariant 8), and the grant export is the one column in the API layer that has
// to derive it: a portal grant carries the MAC and not the flag. The copy is
// deliberate and its drift is a red test rather than a silence.
func LocallyAdministered(mac string) bool {
	var hex []byte
	for i := 0; i < len(mac) && len(hex) < 2; i++ {
		c := mac[i] | 0x20
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			hex = append(hex, c)
		}
	}
	if len(hex) < 2 {
		return false
	}
	first, err := strconv.ParseUint(string(hex), 16, 8)
	return err == nil && first&0x02 != 0
}
