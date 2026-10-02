import { describe, expect, it } from "vitest";
import {
  alignmentPositions,
  byteCapacity,
  type Ecl,
  encodeQr,
  formatBits,
  MAX_VERSION,
  rawDataModules,
  reedSolomon,
  versionBits,
} from "./qr.js";
import { decodeGrid, SPEC_TABLES } from "./qrdecode.js";
import { gridFromPath, qrPath, QUIET_ZONE } from "./qrsvg.js";
import { escapeWifiField, ssidTooLong, wifiJoinPayload } from "./wifi.js";
import { GOLDEN } from "./qr.golden.js";

/**
 * The QR encoder, checked three ways, none of which is "the encoder agrees with
 * itself":
 *
 *  1. **Published vectors** — the format and version words, a Reed–Solomon block
 *     and the byte capacity of every version and level, all typed in from
 *     ISO/IEC 18004 tables rather than computed.
 *  2. **An independent reader** (`qrdecode.ts`) that shares no code with the
 *     encoder, run over every level, every mask and every version boundary.
 *  3. **Golden symbols that Apple's decoder read** (`qr.golden.ts`): the exact
 *     module grids this encoder produced for the slip payloads, each decoded by the
 *     Vision framework on macOS.
 */

const LEVELS: Ecl[] = ["L", "M", "Q", "H"];

const decode = (text: string, options: Parameters<typeof encodeQr>[1] = {}) => {
  const s = encodeQr(text, options);
  return { symbol: s, decoded: decodeGrid(s.size, (r, c) => s.modules[r * s.size + c]!) };
};

describe("QR encoder — published vectors", () => {
  /* ISO/IEC 18004 Annex C, Table C.1 — all 32 format information words. */
  const FORMAT: Record<Ecl, string[]> = {
    L: [
      "111011111000100", "111001011110011", "111110110101010", "111100010011101",
      "110011000101111", "110001100011000", "110110001000001", "110100101110110",
    ],
    M: [
      "101010000010010", "101000100100101", "101111001111100", "101101101001011",
      "100010111111001", "100000011001110", "100111110010111", "100101010100000",
    ],
    Q: [
      "011010101011111", "011000001101000", "011111100110001", "011101000000110",
      "010010010110100", "010000110000011", "010111011011010", "010101111101101",
    ],
    H: [
      "001011010001001", "001001110111110", "001110011100111", "001100111010000",
      "000011101100010", "000001001010101", "000110100001100", "000100000111011",
    ],
  };

  it("produces every format information word in Table C.1", () => {
    for (const ecl of LEVELS) {
      for (let mask = 0; mask < 8; mask++) {
        expect(formatBits(ecl, mask).toString(2).padStart(15, "0"), `${ecl} mask ${mask}`).toBe(
          FORMAT[ecl][mask],
        );
      }
    }
  });

  it("produces the version information words of Table D.1", () => {
    expect(versionBits(7).toString(2).padStart(18, "0")).toBe("000111110010010100");
    expect(versionBits(8).toString(2).padStart(18, "0")).toBe("001000010110111100");
    expect(versionBits(9).toString(2).padStart(18, "0")).toBe("001001101010011001");
    expect(versionBits(10).toString(2).padStart(18, "0")).toBe("001010010011010011");
  });

  it("computes the Reed–Solomon block of the worked 1-M example", () => {
    // "HELLO WORLD", version 1-M: the sixteen data codewords and the ten EC
    // codewords that every QR tutorial since Thonky's works through by hand.
    const data = [32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17];
    expect(reedSolomon(data, 10)).toEqual([196, 35, 39, 119, 235, 215, 231, 226, 93, 23]);
  });

  it("holds exactly the byte-mode capacity of Table 7 at every version and level", () => {
    const TABLE_7: Record<Ecl, number[]> = {
      L: [17, 32, 53, 78, 106, 134, 154, 192, 230, 271],
      M: [14, 26, 42, 62, 84, 106, 122, 152, 180, 213],
      Q: [11, 20, 32, 46, 60, 74, 86, 108, 130, 151],
      H: [7, 14, 24, 34, 44, 58, 64, 84, 98, 119],
    };
    for (const ecl of LEVELS) {
      for (let v = 1; v <= MAX_VERSION; v++) {
        expect(byteCapacity(v, ecl), `${v}-${ecl}`).toBe(TABLE_7[ecl][v - 1]);
      }
    }
  });

  it("agrees with the reader's own copy of Table 9 and Annex E", () => {
    for (let v = 1; v <= MAX_VERSION; v++) {
      expect(Math.floor(rawDataModules(v) / 8), `codewords, version ${v}`).toBe(
        SPEC_TABLES.TOTAL_CODEWORDS[v],
      );
      expect(alignmentPositions(v), `alignment, version ${v}`).toEqual(SPEC_TABLES.ALIGN[v]);
    }
  });
});

describe("QR encoder — read back by an independent decoder", () => {
  it("round-trips every level under every forced mask", () => {
    const text = "WIFI:S:HoGuom-Guest;T:nopass;;";
    for (const ecl of LEVELS) {
      for (let mask = 0; mask < 8; mask++) {
        const { symbol, decoded } = decode(text, { ecl, mask });
        expect(decoded.text).toBe(text);
        expect(decoded.mask).toBe(mask);
        expect(decoded.ecl).toBe(symbol.ecl);
      }
    }
  });

  it("round-trips at both sides of every version boundary, versions 1–10", () => {
    for (const ecl of LEVELS) {
      for (let v = 1; v <= MAX_VERSION; v++) {
        const cap = byteCapacity(v, ecl);
        for (const n of [cap - 1, cap]) {
          const text = "x".repeat(n);
          const { symbol, decoded } = decode(text, { ecl });
          expect(symbol.version, `${n} bytes at ${ecl}`).toBe(v);
          expect(decoded.text).toBe(text);
        }
        if (v < MAX_VERSION) {
          expect(encodeQr("x".repeat(cap + 1), { ecl }).version).toBe(v + 1);
        }
      }
    }
  });

  it("carries UTF-8: a Vietnamese SSID arrives as written", () => {
    const text = wifiJoinPayload("Quán Cà Phê Sữa Đá")!;
    expect(decode(text).decoded.text).toBe(text);
  });

  it("round-trips pseudo-random payloads with the chosen mask", () => {
    let seed = 20260923;
    const rand = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;
    const alphabet = "abcXYZ019 ;:,\\\"-_.ăâđêôơưÀÁẠ";
    for (let i = 0; i < 300; i++) {
      const len = Math.floor(rand() * 90);
      const text = Array.from({ length: len }, () => alphabet[Math.floor(rand() * alphabet.length)]).join("");
      expect(decode(text).decoded.text).toBe(text);
    }
  });

  it("refuses a payload larger than version 10 rather than emit something untested", () => {
    expect(() => encodeQr("x".repeat(214), { ecl: "M" })).toThrow(RangeError);
  });

  it("raises the level for free where the version has room, never lowers it", () => {
    // Seven bytes fit version 1 even at H, so a level-M request is boosted to H.
    expect(encodeQr("1234567").ecl).toBe("H");
    expect(encodeQr("x".repeat(14)).ecl).toBe("M");
  });
});

describe("QR on the slip — the SVG it draws", () => {
  it("draws the symbol the decoder reads, with a four-module quiet zone", () => {
    const s = encodeQr("WIFI:S:Foyer Guest;T:nopass;;");
    const { d, extent } = qrPath(s);
    expect(extent).toBe(s.size + 2 * QUIET_ZONE);
    const grid = gridFromPath(d, extent);
    expect(grid.dark).toEqual(s.modules);
    expect(decodeGrid(grid.size, (r, c) => grid.dark[r * grid.size + c]!).text).toBe(
      "WIFI:S:Foyer Guest;T:nopass;;",
    );
  });

  it("matches the golden symbols Apple's Vision framework decoded", () => {
    expect(GOLDEN.length).toBeGreaterThan(0);
    for (const g of GOLDEN) {
      const s = encodeQr(g.text);
      const bits = s.modules.map((m) => (m ? "1" : "0")).join("");
      expect({ version: s.version, ecl: s.ecl, mask: s.mask, bits }, g.text).toEqual({
        version: g.version,
        ecl: g.ecl,
        mask: g.mask,
        bits: g.bits,
      });
    }
  });
});

describe("the Wi-Fi join string", () => {
  it("is the ZXing WIFI: form for an open network", () => {
    expect(wifiJoinPayload("HoGuom-Guest")).toBe("WIFI:S:HoGuom-Guest;T:nopass;;");
  });

  it("escapes the five reserved characters and nothing else", () => {
    expect(escapeWifiField(`a\\b;c,d:e"f g`)).toBe(`a\\\\b\\;c\\,d\\:e\\"f g`);
    expect(wifiJoinPayload("Café; Guest")).toBe("WIFI:S:Café\\; Guest;T:nopass;;");
  });

  it("is absent where there is no network name, and keeps a name's own spaces", () => {
    expect(wifiJoinPayload(null)).toBeNull();
    expect(wifiJoinPayload("   ")).toBeNull();
    expect(wifiJoinPayload(" Lobby ")).toBe("WIFI:S: Lobby ;T:nopass;;");
  });

  it("is absent for a name longer than an SSID can be, counted in UTF-8 bytes", () => {
    // 32 bytes: 11 two- or three-byte letters and ASCII. Joinable, so encoded.
    expect(wifiJoinPayload("Quán Phở Hà Nội — Khách")).not.toBeNull();
    // 33 bytes, 24 characters: a character count would let it through.
    expect(wifiJoinPayload("Quán Phở Hà Nội — Khách1")).toBeNull();
    expect(ssidTooLong("Quán Phở Hà Nội — Khách1")).toBe(true);
    expect(ssidTooLong("a".repeat(32))).toBe(false);
    expect(ssidTooLong(null)).toBe(false);
    expect(ssidTooLong("   ")).toBe(false);
    // What made the batch page throw: past version 10 at level M.
    expect(wifiJoinPayload("Khách sạn Hồ Gươm — ".repeat(12))).toBeNull();
  });

  it("fits the longest SSID 802.11 allows, every byte escaped, inside version 10", () => {
    const worst = wifiJoinPayload(";".repeat(32))!;
    expect(new TextEncoder().encode(worst).length).toBe(82);
    expect(decode(worst).decoded.text).toBe(worst);
  });
});
