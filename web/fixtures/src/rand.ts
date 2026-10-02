/**
 * A seeded PRNG, so the ugly world is the *same* ugly world on every run.
 *
 * Fixtures that shuffle between runs make a review unrepeatable — you
 * cannot say "look at row 4" and you cannot diff a screenshot.
 */
export function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function pick<T>(rng: () => number, xs: readonly T[]): T {
  return xs[Math.floor(rng() * xs.length)]!;
}

/**
 * A deterministic value in `[0, 1)` for a `(key, n)` pair.
 *
 * `mulberry32` above is a *stream*: it gives the same sequence only if you draw
 * from it in the same order. That is fine for building the world once, and useless
 * for advancing it, because the mock draws per session per tick and any change to
 * the order would reshuffle everything after it.
 *
 * This hashes the pair instead. Tick 7 of session X is the same number in every
 * process, on every machine, forever — which is what makes a screenshot of a moving
 * world something two people can argue about.
 *
 * `Math.random()` at tick time is the thing this exists to avoid.
 */
export function noise(key: string, n: number): number {
  // FNV-1a over the key, then mixed with n through mulberry32's avalanche.
  let h = 0x811c9dc5;
  for (let i = 0; i < key.length; i++) {
    h ^= key.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  h = (h ^ Math.imul(n + 1, 0x9e3779b9)) >>> 0;
  let t = Math.imul(h ^ (h >>> 15), 1 | h);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
}

/** `noise`, scaled into `[lo, hi]` and rounded. */
export function noiseInt(key: string, n: number, lo: number, hi: number): number {
  return lo + Math.floor(noise(key, n) * (hi - lo + 1));
}

export function int(rng: () => number, lo: number, hi: number): number {
  return lo + Math.floor(rng() * (hi - lo + 1));
}

const HEX = "0123456789abcdef";

/**
 * The two low bits of a MAC's first octet, which are the only two that mean
 * anything on their own.
 *
 * IEEE 802 numbers the bits of the first octet from the least significant:
 * **bit 0 is I/G** (individual / group — a group address is multicast and can never
 * be a station address) and **bit 1 is U/L** (universal / local — set means locally
 * administered, which is what `Session.macIsRandomised` reports).
 *
 * They are declared here as named constants because getting them wrong is invisible:
 * see `mac()` below.
 */
const MAC_MULTICAST_BIT = 0b0000_0001;
const MAC_LOCALLY_ADMINISTERED_BIT = 0b0000_0010;

/**
 * A MAC address. When `randomised`, the locally-administered bit (bit 1 of the
 * first octet) is set and the universal/local OUI is meaningless — which is what
 * iOS 14+ and Android 10+ present by default. The UI has to cope with a column of
 * these carrying no vendor and nothing else to identify the device by — there is no
 * hostname to fall back on, in this fixture or anywhere: `Session.hostname` is gone
 * from the contract because `docs/protocol-reality.md` says it does not exist.
 *
 * **Mask the low nibble, not the high one.** The first octet is written as two hex
 * digits and the code holds them in two variables, `hi` and `lo` — so `hi | 0b0010`
 * would set bit **5**, not bit 1, and `hi & 0b1110` would clear bit **4**, not bit 0,
 * leaving both bits that actually matter to chance in `lo`. That mistake produces
 * sessions carrying `macIsRandomised: true` over an address whose locally-administered
 * bit is clear, and station addresses that are multicast, which no NAS can ever
 * report in `Calling-Station-Id`.
 *
 * `docs/rules/security.md` §6 requires the bit to be *detected*, and the Go side
 * detects it, so a fixture that disagrees makes the oracle wrong and the
 * implementation right. A fixture that claims a real Apple OUI is a pseudonym tells
 * an operator the opposite of the truth about the device standing in front of them.
 */
export function mac(rng: () => number, randomised: boolean): string {
  const octets: string[] = [];
  for (let i = 0; i < 6; i++) {
    const hi = Math.floor(rng() * 16);
    let lo = Math.floor(rng() * 16);
    if (i === 0) {
      // Both bits live in the *low* nibble of the first octet. A station address is
      // never multicast, so bit 0 is cleared either way; bit 1 is the claim
      // `macIsRandomised` makes, so it is set or cleared to match.
      lo = randomised
        ? (lo | MAC_LOCALLY_ADMINISTERED_BIT) & ~MAC_MULTICAST_BIT
        : lo & ~(MAC_LOCALLY_ADMINISTERED_BIT | MAC_MULTICAST_BIT);
    }
    octets.push(HEX[hi]! + HEX[lo]!);
  }
  return octets.join(":");
}

/**
 * Whether an address is locally administered — the one thing a MAC says about
 * itself, and the definition `Session.macIsRandomised` carries.
 *
 * The admin console has its own copy in `admin/src/format.ts`, because a component
 * may not import the fixtures. This one is what the fixture guard in
 * `web/mock/src/validate.test.ts` measures the built world against, so a row whose
 * flag and whose address disagree is a red test rather than a rendering nobody
 * checks.
 */
export function macIsLocallyAdministered(address: string): boolean {
  const first = Number.parseInt(address.slice(0, 2), 16);
  return Number.isFinite(first) && (first & MAC_LOCALLY_ADMINISTERED_BIT) !== 0;
}

/** Whether an address is a group address, which a station address may never be. */
export function macIsMulticast(address: string): boolean {
  const first = Number.parseInt(address.slice(0, 2), 16);
  return Number.isFinite(first) && (first & MAC_MULTICAST_BIT) !== 0;
}

/** A handful of real OUIs, so not every row looks synthetic. */
export const REAL_OUIS = [
  "3c:22:fb", // Apple
  "a4:83:e7", // Apple
  "dc:a6:32", // Raspberry Pi
  "50:eb:f6", // ASUSTek
  "f0:9f:c2", // Ubiquiti
] as const;

export function macWithOui(rng: () => number, oui: string): string {
  const tail: string[] = [];
  for (let i = 0; i < 3; i++) {
    tail.push(HEX[Math.floor(rng() * 16)]! + HEX[Math.floor(rng() * 16)]!);
  }
  return `${oui}:${tail.join(":")}`;
}
