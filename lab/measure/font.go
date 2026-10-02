package main

// A synthetic webfont, built here rather than shipped as a binary.
//
// One of the on-device questions the probe page answers is whether a
// **same-origin `@font-face`** loads inside the Captive Network Assistant. Answering it needs a real font
// file, and there are two bad ways to get one: commit a binary blob to the
// repository, or fetch one at run time — which would be an external origin, the
// single thing the portal's own rules forbid outright.
//
// So the font is generated. It has exactly two visible glyphs: a blank for the
// space, and a solid triangle for every printable ASCII character. That shape is
// chosen so the measurement is readable from a photograph with no tooling and no
// console: the probe page renders the literal words "NOT LOADED" in this font, so
// a reader sees either a row of triangles (the font loaded) or the sentence
// explaining that it did not. A hollow box would have been ambiguous with the
// user agent's own missing-glyph tofu; a filled triangle is not.
//
// It is served in two container formats. TrueType is the floor. WOFF is what a
// real webfont ships as, and it is reachable from the standard library because
// WOFF 1 compresses with zlib. WOFF 2 needs Brotli, which the standard library
// does not have, and this tool adds no dependency — so WOFF 2 is **not** measured
// here, and this says so rather than implying coverage.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"sort"
)

// unitsPerEm is the font's design grid. 1000 is the PostScript convention and is
// legal in TrueType; the exact number does not matter to the measurement.
const unitsPerEm = 1000

// The three glyphs: .notdef, space, triangle. Every printable ASCII character
// except the space maps to the triangle.
const (
	glyphSpace = 1
	glyphMark  = 2
	numGlyphs  = 3
)

type sfntTable struct {
	tag  string
	data []byte
}

// buildProbeTTF assembles the font. The table set is the minimum a browser's
// font sanitiser accepts for a TrueType outline font: OS/2, cmap, glyf, head,
// hhea, hmtx, loca, maxp, name, post. Omitting any one of them is a rejected
// font, which would look identical on the phone to "the CNA refuses webfonts" —
// the exact confusion this whole kit exists to prevent.
func buildProbeTTF() []byte {
	glyf, glyphLen := buildGlyf()

	tables := []sfntTable{
		{"OS/2", buildOS2()},
		{"cmap", buildCmap()},
		{"glyf", glyf},
		{"head", buildHead()},
		{"hhea", buildHhea()},
		{"hmtx", buildHmtx()},
		{"loca", buildLoca(glyphLen)},
		{"maxp", buildMaxp()},
		{"name", buildName()},
		{"post", buildPost()},
	}
	return assembleSFNT(tables)
}

func buildHead() []byte {
	b := &bytes.Buffer{}
	writeU32(b, 0x00010000) // version 1.0
	writeU32(b, 0x00010000) // fontRevision 1.0
	writeU32(b, 0)          // checkSumAdjustment, patched after assembly
	writeU32(b, 0x5F0F3CF5) // magicNumber
	writeU16(b, 0x000B)     // flags: baseline at y=0, lsb at x=0, scale differs
	writeU16(b, unitsPerEm)
	// created / modified, seconds since 1904-01-01. A fixed value keeps the build
	// reproducible: two runs of this program must produce byte-identical fonts,
	// or a diff of the served bytes stops meaning anything.
	writeU32(b, 0)
	writeU32(b, 0xD0000000)
	writeU32(b, 0)
	writeU32(b, 0xD0000000)
	writeI16(b, 100) // xMin
	writeI16(b, 0)   // yMin
	writeI16(b, 900) // xMax
	writeI16(b, 700) // yMax
	writeU16(b, 0)   // macStyle
	writeU16(b, 8)   // lowestRecPPEM
	writeI16(b, 2)   // fontDirectionHint
	writeI16(b, 1)   // indexToLocFormat: long
	writeI16(b, 0)   // glyphDataFormat
	return b.Bytes()
}

func buildHhea() []byte {
	b := &bytes.Buffer{}
	writeU32(b, 0x00010000)
	writeI16(b, 800)  // ascender
	writeI16(b, -200) // descender
	writeI16(b, 0)    // lineGap
	writeU16(b, 1000) // advanceWidthMax
	writeI16(b, 0)    // minLeftSideBearing
	writeI16(b, 0)    // minRightSideBearing
	writeI16(b, 900)  // xMaxExtent
	writeI16(b, 1)    // caretSlopeRise
	writeI16(b, 0)    // caretSlopeRun
	writeI16(b, 0)    // caretOffset
	for range 4 {
		writeI16(b, 0) // reserved
	}
	writeI16(b, 0) // metricDataFormat
	writeU16(b, numGlyphs)
	return b.Bytes()
}

func buildMaxp() []byte {
	b := &bytes.Buffer{}
	writeU32(b, 0x00010000)
	writeU16(b, numGlyphs)
	writeU16(b, 3) // maxPoints
	writeU16(b, 1) // maxContours
	writeU16(b, 0) // maxCompositePoints
	writeU16(b, 0) // maxCompositeContours
	writeU16(b, 2) // maxZones
	writeU16(b, 0) // maxTwilightPoints
	writeU16(b, 0) // maxStorage
	writeU16(b, 0) // maxFunctionDefs
	writeU16(b, 0) // maxInstructionDefs
	writeU16(b, 0) // maxStackElements
	writeU16(b, 0) // maxSizeOfInstructions
	writeU16(b, 0) // maxComponentElements
	writeU16(b, 0) // maxComponentDepth
	return b.Bytes()
}

func buildOS2() []byte {
	b := &bytes.Buffer{}
	writeU16(b, 4)   // version
	writeI16(b, 950) // xAvgCharWidth
	writeU16(b, 400) // usWeightClass
	writeU16(b, 5)   // usWidthClass
	writeU16(b, 0)   // fsType: installable
	writeI16(b, 650) // ySubscriptXSize
	writeI16(b, 600) // ySubscriptYSize
	writeI16(b, 0)   // ySubscriptXOffset
	writeI16(b, 75)  // ySubscriptYOffset
	writeI16(b, 650) // ySuperscriptXSize
	writeI16(b, 600) // ySuperscriptYSize
	writeI16(b, 0)   // ySuperscriptXOffset
	writeI16(b, 350) // ySuperscriptYOffset
	writeI16(b, 50)  // yStrikeoutSize
	writeI16(b, 300) // yStrikeoutPosition
	writeI16(b, 0)   // sFamilyClass
	b.Write(make([]byte, 10))
	writeU32(b, 0x00000003) // ulUnicodeRange1: Basic Latin + Latin-1
	writeU32(b, 0)
	writeU32(b, 0)
	writeU32(b, 0)
	b.WriteString("FOYR") // achVendID
	writeU16(b, 0x0040)   // fsSelection: REGULAR
	writeU16(b, 0x0020)   // usFirstCharIndex
	writeU16(b, 0x007E)   // usLastCharIndex
	writeI16(b, 800)      // sTypoAscender
	writeI16(b, -200)     // sTypoDescender
	writeI16(b, 0)        // sTypoLineGap
	writeU16(b, 800)      // usWinAscent
	writeU16(b, 200)      // usWinDescent
	writeU32(b, 1)        // ulCodePageRange1: Latin 1
	writeU32(b, 0)
	writeI16(b, 500) // sxHeight
	writeI16(b, 700) // sCapHeight
	writeU16(b, 0)   // usDefaultChar
	writeU16(b, 32)  // usBreakChar
	writeU16(b, 1)   // usMaxContext
	return b.Bytes()
}

func buildPost() []byte {
	b := &bytes.Buffer{}
	writeU32(b, 0x00030000) // version 3.0: no glyph names
	writeU32(b, 0)          // italicAngle
	writeI16(b, -100)       // underlinePosition
	writeI16(b, 50)         // underlineThickness
	writeU32(b, 0)          // isFixedPitch
	writeU32(b, 0)          // minMemType42
	writeU32(b, 0)          // maxMemType42
	writeU32(b, 0)          // minMemType1
	writeU32(b, 0)          // maxMemType1
	return b.Bytes()
}

func buildHmtx() []byte {
	b := &bytes.Buffer{}
	writeU16(b, 1000) // .notdef advance
	writeI16(b, 0)
	writeU16(b, 500) // space advance
	writeI16(b, 0)
	writeU16(b, 1000) // triangle advance
	writeI16(b, 100)
	return b.Bytes()
}

// buildGlyf returns the glyph table and the length of the one non-empty glyph.
// .notdef and the space have zero-length entries, which is how TrueType says
// "no outline" — the loca table's two equal offsets carry that.
func buildGlyf() (data []byte, markLen int) {
	g := &bytes.Buffer{}
	writeI16(g, 1)   // numberOfContours
	writeI16(g, 100) // xMin
	writeI16(g, 0)   // yMin
	writeI16(g, 900) // xMax
	writeI16(g, 700) // yMax
	writeU16(g, 2)   // endPtsOfContours[0]: last point index
	writeU16(g, 0)   // instructionLength
	// flags: three on-curve points, each coordinate a signed 16-bit delta.
	g.Write([]byte{0x01, 0x01, 0x01})
	writeI16(g, 100) // x deltas
	writeI16(g, 800)
	writeI16(g, -400)
	writeI16(g, 0) // y deltas
	writeI16(g, 0)
	writeI16(g, 700)
	for g.Len()%4 != 0 {
		g.WriteByte(0)
	}
	return g.Bytes(), g.Len()
}

func buildLoca(markLen int) []byte {
	b := &bytes.Buffer{}
	writeU32(b, 0)               // .notdef starts at 0
	writeU32(b, 0)               // ...and is empty, so the space starts at 0 too
	writeU32(b, 0)               // ...and is empty, so the triangle starts at 0
	writeU32(b, uint32(markLen)) // end of the triangle
	return b.Bytes()
}

// buildCmap maps U+0020 to the blank glyph and U+0021..U+007E to the triangle.
// Format 4, the segment mapping every engine reads.
func buildCmap() []byte {
	const (
		firstMark = 0x21
		lastMark  = 0x7E
		segCount  = 3
	)
	sub := &bytes.Buffer{}
	writeU16(sub, 4) // format
	writeU16(sub, 0) // length, patched below
	writeU16(sub, 0) // language
	writeU16(sub, segCount*2)
	searchRange := 2 * largestPowerOfTwoAtMost(segCount)
	writeU16(sub, uint16(searchRange))
	writeU16(sub, uint16(log2(largestPowerOfTwoAtMost(segCount))))
	writeU16(sub, uint16(segCount*2-searchRange))

	writeU16(sub, 0x0020) // endCode
	writeU16(sub, lastMark)
	writeU16(sub, 0xFFFF)
	writeU16(sub, 0)      // reservedPad
	writeU16(sub, 0x0020) // startCode
	writeU16(sub, firstMark)
	writeU16(sub, 0xFFFF)
	writeU16(sub, uint16(0x10000+glyphSpace-0x0020)) // idDelta, taken modulo 2^16 as the format requires
	writeU16(sub, 0)                                 // the triangle range uses idRangeOffset instead
	writeU16(sub, 1)                                 // 0xFFFF maps to glyph 0
	writeU16(sub, 0)                                 // idRangeOffset for the space segment
	// Bytes from this entry to the start of glyphIdArray: two remaining entries.
	writeU16(sub, 4)
	writeU16(sub, 0)
	for range lastMark - firstMark + 1 {
		writeU16(sub, glyphMark)
	}
	body := sub.Bytes()
	binary.BigEndian.PutUint16(body[2:], uint16(len(body)))

	b := &bytes.Buffer{}
	writeU16(b, 0) // version
	writeU16(b, 1) // numTables
	writeU16(b, 3) // platformID: Windows
	writeU16(b, 1) // encodingID: Unicode BMP
	writeU32(b, 12)
	b.Write(body)
	return b.Bytes()
}

func buildName() []byte {
	type record struct {
		id    uint16
		value string
	}
	records := []record{
		{1, "Foyer Probe"},
		{2, "Regular"},
		{3, "FoyerProbe-Regular-1.0"},
		{4, "Foyer Probe Regular"},
		{5, "Version 1.0"},
		{6, "FoyerProbe-Regular"},
	}
	sort.Slice(records, func(i, j int) bool { return records[i].id < records[j].id })

	strs := &bytes.Buffer{}
	dir := &bytes.Buffer{}
	for _, r := range records {
		off := strs.Len()
		for _, c := range r.value {
			writeU16(strs, uint16(c)) // UTF-16BE; every name here is ASCII
		}
		writeU16(dir, 3)      // platformID: Windows
		writeU16(dir, 1)      // encodingID: Unicode BMP
		writeU16(dir, 0x0409) // languageID: en-US
		writeU16(dir, r.id)
		writeU16(dir, uint16(strs.Len()-off))
		writeU16(dir, uint16(off))
	}

	b := &bytes.Buffer{}
	writeU16(b, 0) // format
	writeU16(b, uint16(len(records)))
	writeU16(b, uint16(6+12*len(records))) // stringOffset
	b.Write(dir.Bytes())
	b.Write(strs.Bytes())
	return b.Bytes()
}

// assembleSFNT writes the offset table, the directory and the padded table data,
// then patches head.checkSumAdjustment, which is defined against the finished
// file and therefore cannot be computed before there is one.
func assembleSFNT(tables []sfntTable) []byte {
	sort.Slice(tables, func(i, j int) bool { return tables[i].tag < tables[j].tag })

	n := len(tables)
	searchRange := 16 * largestPowerOfTwoAtMost(n)

	out := &bytes.Buffer{}
	writeU32(out, 0x00010000) // sfntVersion: TrueType outlines
	writeU16(out, uint16(n))
	writeU16(out, uint16(searchRange))
	writeU16(out, uint16(log2(largestPowerOfTwoAtMost(n))))
	writeU16(out, uint16(n*16-searchRange))

	offset := 12 + 16*n
	headOffset := 0
	for _, t := range tables {
		out.WriteString(t.tag)
		writeU32(out, tableChecksum(t.data))
		writeU32(out, uint32(offset))
		writeU32(out, uint32(len(t.data)))
		if t.tag == "head" {
			headOffset = offset
		}
		offset += padded4(len(t.data))
	}
	for _, t := range tables {
		out.Write(t.data)
		for range padded4(len(t.data)) - len(t.data) {
			out.WriteByte(0)
		}
	}

	file := out.Bytes()
	adjustment := 0xB1B0AFBA - tableChecksum(file)
	binary.BigEndian.PutUint32(file[headOffset+8:], adjustment)
	return file
}

// wrapWOFF repackages an sfnt as WOFF 1. Each table is deflated independently
// and stored raw whenever compression does not pay, which is what the format
// requires rather than an optimisation.
func wrapWOFF(sfnt []byte) []byte {
	n := int(binary.BigEndian.Uint16(sfnt[4:]))

	type entry struct {
		tag          string
		compData     []byte
		offset       int
		origChecksum uint32
		origLength   int
		compLen      int
	}
	entries := make([]entry, 0, n)
	totalSfnt := 12 + 16*n
	for i := range n {
		rec := sfnt[12+16*i:]
		tag := string(rec[0:4])
		checksum := binary.BigEndian.Uint32(rec[4:])
		off := int(binary.BigEndian.Uint32(rec[8:]))
		length := int(binary.BigEndian.Uint32(rec[12:]))
		data := sfnt[off : off+length]

		var zbuf bytes.Buffer
		zw := zlib.NewWriter(&zbuf)
		_, _ = zw.Write(data)
		_ = zw.Close()
		comp := zbuf.Bytes()
		if len(comp) >= len(data) {
			comp = data // equal lengths signal "stored uncompressed"
		}
		entries = append(entries, entry{
			tag: tag, compData: comp,
			origChecksum: checksum, origLength: length, compLen: len(comp),
		})
		totalSfnt += padded4(length)
	}

	cursor := 44 + 20*n
	for i := range entries {
		entries[i].offset = cursor
		cursor += padded4(entries[i].compLen)
	}

	out := &bytes.Buffer{}
	out.WriteString("wOFF")
	writeU32(out, 0x00010000) // flavor: TrueType
	writeU32(out, uint32(cursor))
	writeU16(out, uint16(n))
	writeU16(out, 0) // reserved
	writeU32(out, uint32(totalSfnt))
	writeU16(out, 1) // majorVersion
	writeU16(out, 0) // minorVersion
	writeU32(out, 0) // metaOffset
	writeU32(out, 0) // metaLength
	writeU32(out, 0) // metaOrigLength
	writeU32(out, 0) // privOffset
	writeU32(out, 0) // privLength
	for _, e := range entries {
		out.WriteString(e.tag)
		writeU32(out, uint32(e.offset))
		writeU32(out, uint32(e.compLen))
		writeU32(out, uint32(e.origLength))
		writeU32(out, e.origChecksum)
	}
	for _, e := range entries {
		out.Write(e.compData)
		for range padded4(e.compLen) - e.compLen {
			out.WriteByte(0)
		}
	}
	return out.Bytes()
}

func tableChecksum(b []byte) uint32 {
	var sum uint32
	for i := 0; i < len(b); i += 4 {
		var word [4]byte
		copy(word[:], b[i:])
		sum += binary.BigEndian.Uint32(word[:])
	}
	return sum
}

func padded4(n int) int { return (n + 3) &^ 3 }

func largestPowerOfTwoAtMost(n int) int {
	p := 1
	for p*2 <= n {
		p *= 2
	}
	return p
}

func log2(n int) int {
	k := 0
	for n > 1 {
		n /= 2
		k++
	}
	return k
}

func writeU16(b *bytes.Buffer, v uint16) { _ = binary.Write(b, binary.BigEndian, v) }
func writeI16(b *bytes.Buffer, v int16)  { _ = binary.Write(b, binary.BigEndian, v) }
func writeU32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.BigEndian, v) }
