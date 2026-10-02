package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"testing"
)

// A malformed font is rejected by the browser's own sanitiser, and on a phone
// with no console that rejection is indistinguishable from "the CNA refuses
// webfonts" — which is the unknown this font exists to settle. So the font is
// parsed back here rather than assumed, table by table.

type parsedFont struct {
	tables map[string][]byte
	order  []string
}

func parseSFNT(t *testing.T, b []byte) parsedFont {
	t.Helper()
	if len(b) < 12 {
		t.Fatal("too short to be a font")
	}
	if v := binary.BigEndian.Uint32(b); v != 0x00010000 {
		t.Fatalf("sfntVersion is %#x, want 0x00010000 for TrueType outlines", v)
	}
	n := int(binary.BigEndian.Uint16(b[4:]))
	out := parsedFont{tables: map[string][]byte{}}
	for i := range n {
		rec := b[12+16*i:]
		tag := string(rec[0:4])
		checksum := binary.BigEndian.Uint32(rec[4:])
		off := int(binary.BigEndian.Uint32(rec[8:]))
		length := int(binary.BigEndian.Uint32(rec[12:]))
		if off+length > len(b) {
			t.Fatalf("table %s runs past the end of the file", tag)
		}
		data := b[off : off+length]

		want := checksum
		if tag == "head" {
			// head's own checksum is computed with checkSumAdjustment zeroed.
			zeroed := append([]byte{}, data...)
			binary.BigEndian.PutUint32(zeroed[8:], 0)
			if got := tableChecksum(zeroed); got != want {
				t.Errorf("head checksum is %#x, want %#x", got, want)
			}
		} else if got := tableChecksum(data); got != want {
			t.Errorf("table %s checksum is %#x, want %#x", tag, got, want)
		}
		out.tables[tag] = data
		out.order = append(out.order, tag)
	}
	return out
}

func TestProbeFontCarriesEveryRequiredTable(t *testing.T) {
	f := parseSFNT(t, buildProbeTTF())

	// Omitting any one of these is a font a browser's sanitiser rejects outright.
	for _, tag := range []string{"OS/2", "cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "name", "post"} {
		if _, ok := f.tables[tag]; !ok {
			t.Errorf("no %s table", tag)
		}
	}

	// The directory must be sorted by tag; a sanitiser reads it as sorted.
	for i := 1; i < len(f.order); i++ {
		if f.order[i-1] >= f.order[i] {
			t.Fatalf("table directory is not sorted: %s before %s", f.order[i-1], f.order[i])
		}
	}

	head := f.tables["head"]
	if magic := binary.BigEndian.Uint32(head[12:]); magic != 0x5F0F3CF5 {
		t.Errorf("head magicNumber is %#x", magic)
	}
	if em := binary.BigEndian.Uint16(head[18:]); em != unitsPerEm {
		t.Errorf("unitsPerEm is %d, want %d", em, unitsPerEm)
	}
	if loc := int16(binary.BigEndian.Uint16(head[50:])); loc != 1 {
		t.Errorf("indexToLocFormat is %d; the loca table is written in the long form", loc)
	}
}

func TestProbeFontChecksumAdjustment(t *testing.T) {
	font := buildProbeTTF()
	f := parseSFNT(t, font)
	head := f.tables["head"]
	stored := binary.BigEndian.Uint32(head[8:])

	zeroed := append([]byte{}, font...)
	headOffset := bytes.Index(font, head)
	binary.BigEndian.PutUint32(zeroed[headOffset+8:], 0)

	if want := 0xB1B0AFBA - tableChecksum(zeroed); stored != want {
		t.Fatalf("checkSumAdjustment is %#x, want %#x", stored, want)
	}
}

func TestEveryPrintableCharacterMapsToTheMark(t *testing.T) {
	f := parseSFNT(t, buildProbeTTF())
	lookup := cmapLookup(t, f.tables["cmap"])

	if got := lookup(' '); got != glyphSpace {
		t.Errorf("the space maps to glyph %d, want the blank glyph %d", got, glyphSpace)
	}
	// "NOT LOADED" is the string the page renders; every character in it has to
	// resolve to the triangle or the measurement reads as a partial failure.
	for _, r := range "NOT LOADED" {
		want := glyphMark
		if r == ' ' {
			want = glyphSpace
		}
		if got := lookup(r); got != want {
			t.Errorf("%q maps to glyph %d, want %d", r, got, want)
		}
	}
	if got := lookup('é'); got != 0 {
		t.Errorf("a character outside the covered range maps to %d, want .notdef", got)
	}
}

// cmapLookup walks the format 4 subtable the way an engine does, rather than
// trusting the bytes this package wrote a moment ago.
func cmapLookup(t *testing.T, cmap []byte) func(rune) int {
	t.Helper()
	if n := binary.BigEndian.Uint16(cmap[2:]); n != 1 {
		t.Fatalf("cmap has %d subtables, want 1", n)
	}
	off := int(binary.BigEndian.Uint32(cmap[8:]))
	sub := cmap[off:]
	if format := binary.BigEndian.Uint16(sub); format != 4 {
		t.Fatalf("cmap subtable format is %d, want 4", format)
	}
	segX2 := int(binary.BigEndian.Uint16(sub[6:]))
	seg := segX2 / 2
	endBase, startBase := 14, 14+segX2+2
	deltaBase, rangeBase := startBase+segX2, startBase+2*segX2

	return func(r rune) int {
		c := uint16(r)
		for i := range seg {
			end := binary.BigEndian.Uint16(sub[endBase+2*i:])
			if c > end {
				continue
			}
			start := binary.BigEndian.Uint16(sub[startBase+2*i:])
			if c < start {
				return 0
			}
			delta := binary.BigEndian.Uint16(sub[deltaBase+2*i:])
			rangeOffset := binary.BigEndian.Uint16(sub[rangeBase+2*i:])
			if rangeOffset == 0 {
				return int(c + delta)
			}
			at := rangeBase + 2*i + int(rangeOffset) + 2*int(c-start)
			g := binary.BigEndian.Uint16(sub[at:])
			if g == 0 {
				return 0
			}
			return int(g + delta)
		}
		return 0
	}
}

func TestTheMarkIsWiderThanAMonospaceCell(t *testing.T) {
	f := parseSFNT(t, buildProbeTTF())
	hmtx := f.tables["hmtx"]
	mark := binary.BigEndian.Uint16(hmtx[8:])

	// The page's fallback test compares the rendered width of one string in this
	// font against the same string in the user agent's monospace face. A typical
	// monospace advance is around 0.6 em, so an advance near that would make the
	// two indistinguishable and the fallback would silently report "not applied".
	if mark < unitsPerEm {
		t.Fatalf("the mark glyph advances %d/%d em; the width comparison needs a full em to stay decisive", mark, unitsPerEm)
	}
}

func TestWOFFRoundTripsEveryTable(t *testing.T) {
	sfnt := buildProbeTTF()
	woff := wrapWOFF(sfnt)

	if sig := string(woff[0:4]); sig != "wOFF" {
		t.Fatalf("signature is %q", sig)
	}
	if flavor := binary.BigEndian.Uint32(woff[4:]); flavor != 0x00010000 {
		t.Fatalf("flavor is %#x", flavor)
	}
	if total := binary.BigEndian.Uint32(woff[8:]); int(total) != len(woff) {
		t.Fatalf("declared length %d, actual %d", total, len(woff))
	}

	origin := parseSFNT(t, sfnt)
	n := int(binary.BigEndian.Uint16(woff[12:]))
	if n != len(origin.order) {
		t.Fatalf("WOFF declares %d tables, the sfnt has %d", n, len(origin.order))
	}

	for i := range n {
		rec := woff[44+20*i:]
		tag := string(rec[0:4])
		off := int(binary.BigEndian.Uint32(rec[4:]))
		compLen := int(binary.BigEndian.Uint32(rec[8:]))
		origLen := int(binary.BigEndian.Uint32(rec[12:]))
		stored := woff[off : off+compLen]

		got := stored
		if compLen != origLen {
			zr, err := zlib.NewReader(bytes.NewReader(stored))
			if err != nil {
				t.Fatalf("table %s: %v", tag, err)
			}
			got, err = io.ReadAll(zr)
			if err != nil {
				t.Fatalf("table %s: %v", tag, err)
			}
			_ = zr.Close()
		}
		if !bytes.Equal(got, origin.tables[tag]) {
			t.Errorf("table %s does not survive the WOFF round trip", tag)
		}
	}
}

func TestTheFontIsReproducible(t *testing.T) {
	// Two runs must produce identical bytes, or a diff of what was served stops
	// being evidence of anything.
	if !bytes.Equal(buildProbeTTF(), buildProbeTTF()) {
		t.Fatal("two builds of the same font differ")
	}
}
