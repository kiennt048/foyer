import type { QrSymbol } from "./qr.js";

/** The light margin a reader needs around a symbol, in modules (§9.1: four). */
export const QUIET_ZONE = 4;

/**
 * One SVG path for the dark modules, in module units, quiet zone included.
 *
 * One horizontal run per `M…z` rather than one square per module: a Wi-Fi join
 * symbol is a few hundred dark modules, and a batch prints the same symbol five
 * hundred times. Runs keep each copy to a couple of kilobytes of DOM.
 *
 * The shape is fixed — `M<x>,<y>h<n>v1h-<n>z` — because the E2E test reads it back
 * into a module grid and decodes it. That is the point of the test: it decodes what
 * the page *drew*, not a value the page claims to have encoded.
 */
export function qrPath(symbol: QrSymbol): { d: string; extent: number } {
  const { size, modules } = symbol;
  const parts: string[] = [];
  for (let y = 0; y < size; y++) {
    let x = 0;
    while (x < size) {
      if (!modules[y * size + x]) {
        x++;
        continue;
      }
      const start = x;
      while (x < size && modules[y * size + x]) x++;
      const n = x - start;
      parts.push(`M${start + QUIET_ZONE},${y + QUIET_ZONE}h${n}v1h-${n}z`);
    }
  }
  return { d: parts.join(""), extent: size + QUIET_ZONE * 2 };
}

/** The inverse of `qrPath`, for tests: the module grid a path draws, quiet zone removed. */
export function gridFromPath(d: string, extent: number): { size: number; dark: boolean[] } {
  const size = extent - QUIET_ZONE * 2;
  const dark = new Array<boolean>(size * size).fill(false);
  for (const m of d.matchAll(/M(\d+),(\d+)h(\d+)v1h-(\d+)z/g)) {
    const x0 = Number(m[1]) - QUIET_ZONE;
    const y = Number(m[2]) - QUIET_ZONE;
    const n = Number(m[3]);
    if (Number(m[4]) !== n) throw new Error(`malformed run ${m[0]}`);
    for (let x = x0; x < x0 + n; x++) dark[y * size + x] = true;
  }
  return { size, dark };
}
