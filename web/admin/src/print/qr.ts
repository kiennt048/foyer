/**
 * A QR Code encoder, byte mode, versions 1–10. ISO/IEC 18004:2015.
 *
 * ── why this is written here rather than installed ─────────────────────────
 *
 * The admin entry chunk sits at 97 % of its byte gate, and the only thing that needs
 * a QR symbol is the voucher print sheet, which is behind a `lazy()` boundary. The
 * libraries the field uses — `qrcode` (unifi-voucher-site), `qrcode-generator` — are
 * general encoders with every mode, every version and a canvas/PNG back end; this
 * console needs one payload shape (a Wi-Fi join string of well under 100 bytes)
 * rendered as SVG. `docs/rules/frontend.md` asks for a justification before a new
 * dependency, and the honest one does not exist: this file is a couple of hundred
 * lines, it has no I/O, and it is checked against published vectors and against an
 * independent decoder (`qr.test.ts`).
 *
 * The structure follows the specification's own order, and Project Nayuki's
 * reference encoder (MIT) where the specification leaves a choice — the block
 * layout arithmetic and the penalty rules are the same as its.
 *
 * ── what it deliberately does not do ───────────────────────────────────────
 *
 *   - **Byte mode only.** Numeric and alphanumeric modes would shorten some payloads
 *     by a version; a Wi-Fi join string is mixed-case with punctuation, so they would
 *     not shorten this one.
 *   - **No ECI header.** UTF-8 text is written as bytes with no `ECI 26` designator.
 *     That is what `qrcode`, `qrcode-generator` and Nayuki all do by default, because
 *     an ECI header is the part older readers mis-handle; iOS and Android detect UTF-8
 *     in byte mode. A Vietnamese SSID is therefore UNVERIFIED on a real camera.
 *   - **Versions 1–10 only.** 213 bytes at level M, and the longest Wi-Fi join string
 *     an 802.11 SSID can produce (32 bytes, every one escaped) is 82. Longer input
 *     throws rather than produce a symbol nothing here tested.
 */

export type Ecl = "L" | "M" | "Q" | "H";

/** The modules of one symbol, row-major; `true` is dark. No quiet zone included. */
export interface QrSymbol {
  version: number;
  size: number;
  ecl: Ecl;
  mask: number;
  /** `modules[y * size + x]`. */
  modules: boolean[];
}

export const MAX_VERSION = 10;

const ECL_ORDER: Ecl[] = ["L", "M", "Q", "H"];
/** The two format-information bits for each level (Table 12). Not the ordinal. */
const ECL_BITS: Record<Ecl, number> = { L: 1, M: 0, Q: 3, H: 2 };

/*
 * Table 9, versions 1–10: error-correction codewords per block, and number of
 * blocks, per level. Index 0 is unused so the version is the index.
 */
const ECC_PER_BLOCK: Record<Ecl, number[]> = {
  L: [0, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18],
  M: [0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26],
  Q: [0, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24],
  H: [0, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28],
};
const BLOCKS: Record<Ecl, number[]> = {
  L: [0, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4],
  M: [0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5],
  Q: [0, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8],
  H: [0, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8],
};

/** Modules available for data and ECC once every function pattern is placed. */
export function rawDataModules(version: number): number {
  let n = (16 * version + 128) * version + 64;
  if (version >= 2) {
    const align = Math.floor(version / 7) + 2;
    n -= (25 * align - 10) * align - 55;
    if (version >= 7) n -= 36;
  }
  return n;
}

/** Data codewords (bytes) a symbol of this version and level holds. */
export function dataCodewords(version: number, ecl: Ecl): number {
  return (
    Math.floor(rawDataModules(version) / 8) - ECC_PER_BLOCK[ecl][version]! * BLOCKS[ecl][version]!
  );
}

/** Bytes of payload byte mode can carry: header is 4 mode bits + 8 or 16 count bits. */
export function byteCapacity(version: number, ecl: Ecl): number {
  const countBits = version <= 9 ? 8 : 16;
  return Math.floor((dataCodewords(version, ecl) * 8 - 4 - countBits) / 8);
}

/** Centre coordinates of the alignment patterns on each axis (Annex E). */
export function alignmentPositions(version: number): number[] {
  if (version === 1) return [];
  const count = Math.floor(version / 7) + 2;
  const size = version * 4 + 17;
  const step = Math.ceil((version * 4 + 4) / (count * 2 - 2)) * 2;
  const out = [6];
  for (let pos = size - 7; out.length < count; pos -= step) out.splice(1, 0, pos);
  return out;
}

/* ── Reed–Solomon over GF(2^8), primitive polynomial 0x11D ─────────────────── */

function gfMul(x: number, y: number): number {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z & 0xff;
}

/** The generator polynomial of this degree, highest coefficient implicit. */
function rsDivisor(degree: number): number[] {
  const out = new Array<number>(degree).fill(0);
  out[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < degree; j++) {
      out[j] = gfMul(out[j]!, root);
      if (j + 1 < degree) out[j] = out[j]! ^ out[j + 1]!;
    }
    root = gfMul(root, 0x02);
  }
  return out;
}

/** The error-correction codewords for one block of data codewords. */
export function reedSolomon(data: readonly number[], degree: number): number[] {
  const divisor = rsDivisor(degree);
  const out = new Array<number>(degree).fill(0);
  for (const b of data) {
    const factor = b ^ out.shift()!;
    out.push(0);
    for (let i = 0; i < degree; i++) out[i] = out[i]! ^ gfMul(divisor[i]!, factor);
  }
  return out;
}

/* ── the format and version words (Annex C, Annex D) ───────────────────────── */

/** 15 bits: level and mask, BCH(15,5), XOR 0x5412. */
export function formatBits(ecl: Ecl, mask: number): number {
  const data = (ECL_BITS[ecl] << 3) | mask;
  let rem = data;
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  return ((data << 10) | rem) ^ 0x5412;
}

/** 18 bits: the version, BCH(18,6). Only versions 7 and above carry one. */
export function versionBits(version: number): number {
  let rem = version;
  for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
  return (version << 12) | rem;
}

/** The eight data masks (Table 10), `x` the column and `y` the row. */
export function maskBit(mask: number, x: number, y: number): boolean {
  switch (mask) {
    case 0:
      return (x + y) % 2 === 0;
    case 1:
      return y % 2 === 0;
    case 2:
      return x % 3 === 0;
    case 3:
      return (x + y) % 3 === 0;
    case 4:
      return (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0;
    case 5:
      return ((x * y) % 2) + ((x * y) % 3) === 0;
    case 6:
      return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0;
    case 7:
      return (((x + y) % 2) + ((x * y) % 3)) % 2 === 0;
    default:
      throw new RangeError(`mask ${mask}`);
  }
}

/* ── the symbol ────────────────────────────────────────────────────────────── */

export class Grid {
  readonly modules: boolean[];
  readonly reserved: boolean[];
  readonly size: number;
  constructor(size: number) {
    this.size = size;
    this.modules = new Array<boolean>(size * size).fill(false);
    this.reserved = new Array<boolean>(size * size).fill(false);
  }
  fn(x: number, y: number, dark: boolean) {
    const i = y * this.size + x;
    this.modules[i] = dark;
    this.reserved[i] = true;
  }
}

/** Timing, finders, alignment, dark module, and the reserved format/version areas. */
export function functionPatterns(version: number): Grid {
  const size = version * 4 + 17;
  const g = new Grid(size);
  for (let i = 0; i < size; i++) {
    g.fn(6, i, i % 2 === 0);
    g.fn(i, 6, i % 2 === 0);
  }
  const finder = (cx: number, cy: number) => {
    for (let dy = -4; dy <= 4; dy++) {
      for (let dx = -4; dx <= 4; dx++) {
        const x = cx + dx;
        const y = cy + dy;
        if (x < 0 || y < 0 || x >= size || y >= size) continue;
        const d = Math.max(Math.abs(dx), Math.abs(dy));
        g.fn(x, y, d !== 2 && d !== 4);
      }
    }
  };
  finder(3, 3);
  finder(size - 4, 3);
  finder(3, size - 4);
  const pos = alignmentPositions(version);
  const last = pos.length - 1;
  for (let i = 0; i < pos.length; i++) {
    for (let j = 0; j < pos.length; j++) {
      // The three corners already hold a finder.
      if ((i === 0 && j === 0) || (i === 0 && j === last) || (i === last && j === 0)) continue;
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          g.fn(pos[i]! + dx, pos[j]! + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
        }
      }
    }
  }
  drawFormat(g, "M", 0); // reserves the area; overwritten once the mask is chosen
  if (version >= 7) {
    const bits = versionBits(version);
    for (let i = 0; i < 18; i++) {
      const dark = ((bits >>> i) & 1) === 1;
      const a = size - 11 + (i % 3);
      const b = Math.floor(i / 3);
      g.fn(a, b, dark);
      g.fn(b, a, dark);
    }
  }
  return g;
}

function drawFormat(g: Grid, ecl: Ecl, mask: number) {
  const bits = formatBits(ecl, mask);
  const bit = (i: number) => ((bits >>> i) & 1) === 1;
  const s = g.size;
  // Around the top-left finder.
  for (let i = 0; i <= 5; i++) g.fn(8, i, bit(i));
  g.fn(8, 7, bit(6));
  g.fn(8, 8, bit(7));
  g.fn(7, 8, bit(8));
  for (let i = 9; i < 15; i++) g.fn(14 - i, 8, bit(i));
  // The second copy, split between the other two finders.
  for (let i = 0; i < 8; i++) g.fn(s - 1 - i, 8, bit(i));
  for (let i = 8; i < 15; i++) g.fn(8, s - 15 + i, bit(i));
  g.fn(8, s - 8, true); // the dark module
}

/** The order data modules are filled in: two-column zigzag from the bottom right. */
export function dataModuleOrder(g: { size: number; reserved: boolean[] }): number[] {
  const s = g.size;
  const out: number[] = [];
  for (let right = s - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5; // skip the vertical timing column
    for (let v = 0; v < s; v++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? s - 1 - v : v;
        if (!g.reserved[y * s + x]) out.push(y * s + x);
      }
    }
  }
  return out;
}

/** Data codewords → blocks → ECC → interleaved final sequence (§7.6). */
export function interleave(data: readonly number[], version: number, ecl: Ecl): number[] {
  const blocks = BLOCKS[ecl][version]!;
  const eccLen = ECC_PER_BLOCK[ecl][version]!;
  const raw = Math.floor(rawDataModules(version) / 8);
  const shortBlocks = blocks - (raw % blocks);
  const shortLen = Math.floor(raw / blocks); // data + ecc of a short block
  const split: number[][] = [];
  const eccs: number[][] = [];
  for (let i = 0, k = 0; i < blocks; i++) {
    const len = shortLen - eccLen + (i < shortBlocks ? 0 : 1);
    const d = data.slice(k, k + len);
    k += len;
    split.push(d);
    eccs.push(reedSolomon(d, eccLen));
  }
  const out: number[] = [];
  const longest = shortLen - eccLen + 1;
  for (let i = 0; i < longest; i++) {
    for (const b of split) if (i < b.length) out.push(b[i]!);
  }
  for (let i = 0; i < eccLen; i++) for (const e of eccs) out.push(e[i]!);
  return out;
}

/** The data codewords for a byte-mode segment, terminated and padded (§7.4). */
export function dataCodewordsFor(bytes: Uint8Array, version: number, ecl: Ecl): number[] {
  const bits: number[] = [];
  const put = (value: number, len: number) => {
    for (let i = len - 1; i >= 0; i--) bits.push((value >>> i) & 1);
  };
  put(0b0100, 4);
  put(bytes.length, version <= 9 ? 8 : 16);
  for (const b of bytes) put(b, 8);
  const capacity = dataCodewords(version, ecl) * 8;
  put(0, Math.min(4, capacity - bits.length));
  put(0, (8 - (bits.length % 8)) % 8);
  const out: number[] = [];
  for (let i = 0; i < bits.length; i += 8) {
    let b = 0;
    for (let j = 0; j < 8; j++) b = (b << 1) | bits[i + j]!;
    out.push(b);
  }
  for (let pad = 0xec; out.length < capacity / 8; pad ^= 0xec ^ 0x11) out.push(pad);
  return out;
}

/* ── mask selection (§7.8.3) ───────────────────────────────────────────────── */

const N1 = 3;
const N2 = 3;
const N3 = 40;
const N4 = 10;

/** The penalty score of a finished symbol. Lower is better; any mask is decodable. */
export function penalty(modules: readonly boolean[], size: number): number {
  let score = 0;
  const at = (x: number, y: number) => modules[y * size + x]!;
  // Rule 1: runs of five or more of one colour, in rows and in columns.
  for (let pass = 0; pass < 2; pass++) {
    for (let a = 0; a < size; a++) {
      let run = 1;
      for (let b = 1; b < size; b++) {
        const same = pass === 0 ? at(b, a) === at(b - 1, a) : at(a, b) === at(a, b - 1);
        if (same) run++;
        else {
          if (run >= 5) score += N1 + run - 5;
          run = 1;
        }
      }
      if (run >= 5) score += N1 + run - 5;
    }
  }
  // Rule 2: 2×2 blocks of one colour.
  for (let y = 0; y < size - 1; y++) {
    for (let x = 0; x < size - 1; x++) {
      const c = at(x, y);
      if (c === at(x + 1, y) && c === at(x, y + 1) && c === at(x + 1, y + 1)) score += N2;
    }
  }
  // Rule 3: 1:1:3:1:1 finder look-alikes with four light modules on either side.
  const A = [true, false, true, true, true, false, true, false, false, false, false];
  const B = [false, false, false, false, true, false, true, true, true, false, true];
  for (let pass = 0; pass < 2; pass++) {
    for (let a = 0; a < size; a++) {
      for (let b = 0; b + 11 <= size; b++) {
        let ma = true;
        let mb = true;
        for (let k = 0; k < 11 && (ma || mb); k++) {
          const v = pass === 0 ? at(b + k, a) : at(a, b + k);
          if (v !== A[k]) ma = false;
          if (v !== B[k]) mb = false;
        }
        if (ma) score += N3;
        if (mb) score += N3;
      }
    }
  }
  // Rule 4: proportion of dark modules, in 5 % steps away from 50 %.
  const dark = modules.reduce((n, m) => n + (m ? 1 : 0), 0);
  const total = size * size;
  const k = Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1;
  score += Math.max(0, k) * N4;
  return score;
}

/* ── the entry point ───────────────────────────────────────────────────────── */

export interface EncodeOptions {
  /** Minimum level; raised for free where the chosen version has room. Default M. */
  ecl?: Ecl;
  /** Force a mask rather than choose one by penalty. Tests use it. */
  mask?: number;
}

/**
 * Encode text as UTF-8 bytes into the smallest symbol that holds it.
 *
 * Level M by default — 15 % recovery, the level the field's voucher tools pick for a
 * slip that gets folded, pocketed and printed on a thermal head that fades.
 */
export function encodeQr(text: string, options: EncodeOptions = {}): QrSymbol {
  const bytes = new TextEncoder().encode(text);
  const minEcl = options.ecl ?? "M";
  let version = 0;
  for (let v = 1; v <= MAX_VERSION; v++) {
    if (byteCapacity(v, minEcl) >= bytes.length) {
      version = v;
      break;
    }
  }
  if (version === 0) {
    throw new RangeError(
      `QR payload of ${bytes.length} bytes exceeds version ${MAX_VERSION} at level ${minEcl}`,
    );
  }
  let ecl = minEcl;
  for (const higher of ECL_ORDER.slice(ECL_ORDER.indexOf(minEcl) + 1)) {
    if (byteCapacity(version, higher) >= bytes.length) ecl = higher;
  }

  const codewords = interleave(dataCodewordsFor(bytes, version, ecl), version, ecl);
  const base = functionPatterns(version);
  const order = dataModuleOrder(base);
  const placed = base.modules.slice();
  // Remainder bits (0, 3, 4 or 7 of them) are left light, as §7.7.3 requires.
  for (let i = 0; i < order.length && i < codewords.length * 8; i++) {
    placed[order[i]!] = ((codewords[i >>> 3]! >>> (7 - (i & 7))) & 1) === 1;
  }

  const build = (mask: number): Grid => {
    const g = new Grid(base.size);
    for (let i = 0; i < placed.length; i++) {
      g.modules[i] = placed[i]!;
      g.reserved[i] = base.reserved[i]!;
    }
    for (const i of order) {
      const x = i % g.size;
      const y = Math.floor(i / g.size);
      if (maskBit(mask, x, y)) g.modules[i] = !g.modules[i];
    }
    drawFormat(g, ecl, mask);
    return g;
  };

  let best: { mask: number; grid: Grid; score: number } | null = null;
  const masks = options.mask === undefined ? [0, 1, 2, 3, 4, 5, 6, 7] : [options.mask];
  for (const mask of masks) {
    const grid = build(mask);
    const score = penalty(grid.modules, grid.size);
    if (!best || score < best.score) best = { mask, grid, score };
  }
  return { version, size: best!.grid.size, ecl, mask: best!.mask, modules: best!.grid.modules };
}
