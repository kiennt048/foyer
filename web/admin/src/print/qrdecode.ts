/**
 * A QR Code *reader* for clean module grids. **Test support only** — nothing in the
 * console imports it, so it is in no chunk (`check-admin-budget.mjs` lists what is).
 *
 * It exists so a test can ask the question that matters — *does the symbol on the
 * slip say what we meant?* — instead of comparing the encoder's output to itself.
 * For that to mean anything it must not share the encoder's mistakes, so it shares
 * none of its code: the tables below are the specification's (ISO/IEC 18004:2015
 * Table 9 totals, Annex E alignment rows) typed in again, the masks are written in
 * the specification's `i`/`j` notation, and Reed–Solomon is checked by evaluating
 * syndromes rather than by recomputing a remainder.
 *
 * It is still one author reading one specification twice, which is why the
 * independent check is a decoder somebody else wrote: the golden symbols in
 * `qr.golden.ts` were run through Apple's Vision framework, and the E2E test uses
 * Chromium's `BarcodeDetector` wherever the platform provides one.
 *
 * Input is a grid with no quiet zone and no noise: no sampling, no perspective, no
 * error *correction* — a symbol with any error is reported as one, which is the
 * right answer for an encoder test.
 */

export interface Decoded {
  version: number;
  ecl: "L" | "M" | "Q" | "H";
  mask: number;
  text: string;
}

/* Table 9, versions 1–10: total error-correction codewords, and blocks, per level. */
const TOTAL_CODEWORDS = [0, 26, 44, 70, 100, 134, 172, 196, 242, 292, 346];
const EC_TOTAL: Record<string, number[]> = {
  L: [0, 7, 10, 15, 20, 26, 36, 40, 48, 60, 72],
  M: [0, 10, 16, 26, 36, 48, 64, 72, 88, 110, 130],
  Q: [0, 13, 22, 36, 52, 72, 96, 108, 132, 160, 192],
  H: [0, 17, 28, 44, 64, 88, 112, 130, 156, 192, 224],
};
const EC_BLOCKS: Record<string, number[]> = {
  L: [0, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4],
  M: [0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5],
  Q: [0, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8],
  H: [0, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8],
};
/* Annex E, Table E.1 — row/column coordinates of alignment pattern centres. */
const ALIGN: number[][] = [
  [],
  [],
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
];

export const SPEC_TABLES = { TOTAL_CODEWORDS, EC_TOTAL, EC_BLOCKS, ALIGN };

/* Format information: every valid 15-bit word, computed from the definition. */
function bchFormat(data5: number): number {
  let v = data5 << 10;
  const g = 0b10100110111;
  for (let i = 14; i >= 10; i--) if ((v >> i) & 1) v ^= g << (i - 10);
  return ((data5 << 10) | v) ^ 0b101010000010010;
}
const LEVEL_OF_BITS: Record<number, Decoded["ecl"]> = { 0b01: "L", 0b00: "M", 0b11: "Q", 0b10: "H" };

/* GF(256) by log tables, a different construction from the encoder's shift-multiply. */
const EXP = new Array<number>(512);
const LOG = new Array<number>(256);
{
  let x = 1;
  for (let i = 0; i < 255; i++) {
    EXP[i] = x;
    LOG[x] = i;
    x <<= 1;
    if (x & 0x100) x ^= 0x11d;
  }
  for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255]!;
}
const mul = (a: number, b: number) => (a === 0 || b === 0 ? 0 : EXP[LOG[a]! + LOG[b]!]!);

/** True when every syndrome S_0 … S_{n-1} of the block is zero. */
function syndromesZero(block: number[], ecLen: number): boolean {
  for (let i = 0; i < ecLen; i++) {
    const a = EXP[i]!;
    let s = 0;
    for (const c of block) s = mul(s, a) ^ c; // Horner, highest degree first
    if (s !== 0) return false;
  }
  return true;
}

/** Spec mask conditions, `i` the row and `j` the column (Table 10). */
function masked(m: number, i: number, j: number): boolean {
  switch (m) {
    case 0b000:
      return (i + j) % 2 === 0;
    case 0b001:
      return i % 2 === 0;
    case 0b010:
      return j % 3 === 0;
    case 0b011:
      return (i + j) % 3 === 0;
    case 0b100:
      return (Math.floor(i / 2) + Math.floor(j / 3)) % 2 === 0;
    case 0b101:
      return ((i * j) % 2) + ((i * j) % 3) === 0;
    case 0b110:
      return (((i * j) % 2) + ((i * j) % 3)) % 2 === 0;
    default:
      return (((i + j) % 2) + ((i * j) % 3)) % 2 === 0;
  }
}

/**
 * Decode a square grid, `dark(row, col)`. Throws with a reason on anything that is
 * not a valid, error-free, byte-mode symbol of versions 1–10.
 */
export function decodeGrid(size: number, dark: (row: number, col: number) => boolean): Decoded {
  const version = (size - 17) / 4;
  if (!Number.isInteger(version) || version < 1 || version > 10) {
    throw new Error(`size ${size} is not a version 1–10 symbol`);
  }

  // Finder patterns: the 7×7 ring structure at the three corners.
  for (const [r0, c0] of [
    [0, 0],
    [0, size - 7],
    [size - 7, 0],
  ] as const) {
    for (let r = 0; r < 7; r++) {
      for (let c = 0; c < 7; c++) {
        const ring = Math.max(Math.abs(r - 3), Math.abs(c - 3));
        if (dark(r0 + r, c0 + c) !== (ring !== 2)) throw new Error("finder pattern damaged");
      }
    }
  }

  // Format information, both copies; they must agree on one valid codeword.
  let a = 0;
  const colA = [0, 1, 2, 3, 4, 5, 7, 8]; // row 8, left
  for (let k = 0; k < 8; k++) a = (a << 1) | (dark(8, colA[k]!) ? 1 : 0);
  for (const r of [7, 5, 4, 3, 2, 1, 0]) a = (a << 1) | (dark(r, 8) ? 1 : 0);
  let b = 0;
  for (let k = 0; k < 7; k++) b = (b << 1) | (dark(size - 1 - k, 8) ? 1 : 0);
  for (let k = 0; k < 8; k++) b = (b << 1) | (dark(8, size - 8 + k) ? 1 : 0);
  // Both were read most significant bit first: bit 14 is at row 8 column 0 in the
  // first copy and at column 8, bottom row, in the second.
  const words = [a, b];
  let format = -1;
  for (let data5 = 0; data5 < 32; data5++) {
    const w = bchFormat(data5);
    if (words.every((x) => x === w)) format = data5;
  }
  if (format < 0) throw new Error("format information is not a valid, agreeing codeword");
  const ecl = LEVEL_OF_BITS[format >> 3]!;
  const mask = format & 0b111;
  if (!dark(size - 8, 8)) throw new Error("dark module missing");

  // Which modules are function patterns, from the spec's description of each.
  const fn = (r: number, c: number): boolean => {
    if (r <= 8 && c <= 8) return true; // finder, separator, format (top-left)
    if (r <= 8 && c >= size - 8) return true; // top-right
    if (r >= size - 8 && c <= 8) return true; // bottom-left (incl. dark module)
    if (r === 6 || c === 6) return true; // timing
    for (const ar of ALIGN[version]!) {
      for (const ac of ALIGN[version]!) {
        const corner =
          (ar === 6 && ac === 6) ||
          (ar === 6 && ac === size - 7) ||
          (ar === size - 7 && ac === 6);
        if (!corner && Math.abs(r - ar) <= 2 && Math.abs(c - ac) <= 2) return true;
      }
    }
    if (version >= 7) {
      if (r < 6 && c >= size - 11 && c < size - 8) return true;
      if (c < 6 && r >= size - 11 && r < size - 8) return true;
    }
    return false;
  };

  // Read the codeword stream: column pairs right to left, alternating direction.
  const bits: number[] = [];
  let upward = true;
  for (let col = size - 1; col > 0; col -= 2) {
    if (col === 6) col--;
    for (let n = 0; n < size; n++) {
      const r = upward ? size - 1 - n : n;
      for (const c of [col, col - 1]) {
        if (fn(r, c)) continue;
        bits.push(dark(r, c) !== masked(mask, r, c) ? 1 : 0);
      }
    }
    upward = !upward;
  }
  const total = TOTAL_CODEWORDS[version]!;
  const stream: number[] = [];
  for (let i = 0; i < total; i++) {
    let v = 0;
    for (let k = 0; k < 8; k++) v = (v << 1) | bits[i * 8 + k]!;
    stream.push(v);
  }

  // De-interleave: short blocks first, long blocks carry one more data codeword.
  const nBlocks = EC_BLOCKS[ecl]![version]!;
  const ecPer = EC_TOTAL[ecl]![version]! / nBlocks;
  const dataTotal = total - EC_TOTAL[ecl]![version]!;
  const shortData = Math.floor(dataTotal / nBlocks);
  const longCount = dataTotal % nBlocks;
  const lens = Array.from({ length: nBlocks }, (_, i) => shortData + (i >= nBlocks - longCount ? 1 : 0));
  const blocks: number[][] = lens.map(() => []);
  let p = 0;
  for (let i = 0; i < shortData + 1; i++) {
    for (let k = 0; k < nBlocks; k++) if (i < lens[k]!) blocks[k]!.push(stream[p++]!);
  }
  for (let i = 0; i < ecPer; i++) for (let k = 0; k < nBlocks; k++) blocks[k]!.push(stream[p++]!);
  const data: number[] = [];
  blocks.forEach((blk, k) => {
    if (!syndromesZero(blk, ecPer)) throw new Error(`block ${k} fails its Reed–Solomon check`);
    data.push(...blk.slice(0, lens[k]!));
  });

  // Byte-mode segment.
  let bit = 0;
  const read = (n: number) => {
    let v = 0;
    for (let i = 0; i < n; i++, bit++) v = (v << 1) | ((data[bit >> 3]! >> (7 - (bit & 7))) & 1);
    return v;
  };
  const mode = read(4);
  if (mode !== 0b0100) throw new Error(`mode ${mode.toString(2)} is not byte mode`);
  const count = read(version <= 9 ? 8 : 16);
  const bytes = new Uint8Array(count);
  for (let i = 0; i < count; i++) bytes[i] = read(8);
  const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  return { version, ecl, mask, text };
}
