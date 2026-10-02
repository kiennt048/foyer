/**
 * The console's sign-in defences, as `internal/api` has them. Four things, each the
 * mock's copy of one Go file:
 *
 *   * **the first-access setup** (`handlers_setup.go`) — Foyer ships no credential;
 *     a fresh install opens a setup with a one-time code;
 *   * **the trusted-device cookie** (`devicecookie.go`) — the per-account bucket is
 *     split so a stranger cannot keep the account's holder out;
 *   * **the sign-in buckets, reading the console's settings** (`hardening.go`,
 *     `netsettings.go`) — not constants, because Go reads what
 *     `updateNetworkSettings` stored;
 *   * **the body ceilings** (`bodyLimitFor`) — a `413` past the ceiling, and the
 *     anonymous 16 KiB rule.
 *
 * **One deliberate difference:** no per-address bucket on
 * `signIn`. Every client of this mock is `localhost`, so an address bucket would be
 * one bucket for the whole e2e suite and would refuse the suite rather than a
 * guesser. The setup's address bucket is kept, because nothing drives the setup
 * more than a handful of times.
 */
import { createHash, createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import type { SignInThrottle } from "@foyer/contract";
import { ceilingsInForce, throttlesInForce } from "./network.js";

// ── the first-access setup ────────────────────────────────────────────────────

/** Crockford's base32, as `internal/identity/setupcode.go`: no I, L, O or U. */
const SETUP_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
const SETUP_CODE_LENGTH = 16;

export function newSetupCode(): string {
  const raw = randomBytes(SETUP_CODE_LENGTH);
  let out = "";
  raw.forEach((v, i) => {
    if (i > 0 && i % 4 === 0) out += "-";
    out += SETUP_ALPHABET[v & 31];
  });
  return out;
}

/** `identity.NormaliseSetupCode`: case, spaces and dashes ignored, O/I/L read as 0/1/1. */
export function normaliseSetupCode(typed: string): string {
  let out = "";
  for (let ch of typed.toUpperCase()) {
    if (ch === " " || ch === "-" || ch === "\t") continue;
    if (ch === "O") ch = "0";
    if (ch === "I" || ch === "L") ch = "1";
    if (!SETUP_ALPHABET.includes(ch)) return "";
    out += ch;
  }
  return out.length === SETUP_CODE_LENGTH ? out : "";
}

const digestOf = (typed: string): Buffer | null => {
  const n = normaliseSetupCode(typed);
  return n ? createHash("sha256").update(n).digest() : null;
};

let setupDigest: Buffer | null = null;

/** `foyerctl open-setup`: open, or re-open with a new code. Nothing in the API calls it. */
export function openSetup(code: string): void {
  setupDigest = digestOf(code);
}

export function setupOpen(): boolean {
  return setupDigest !== null;
}

/** Compare, and on a match close the setup — the one transaction Go runs, in one call. */
export function claimSetup(code: string): "closed" | "wrong" | "ok" {
  if (!setupDigest) return "closed";
  const got = digestOf(code);
  if (!got || !timingSafeEqual(got, setupDigest)) return "wrong";
  setupDigest = null;
  return "ok";
}

/** Re-open after a claim the caller could not complete (a taken username). */
export function reopenSetup(code: string): void {
  setupDigest = digestOf(code);
}

// ── token buckets, reading the settings in force ──────────────────────────────

interface Bucket {
  tokens: number;
  seen: number;
}

/** `ratelimit.Limiter.Allow`: whole seconds to wait, or 0 having spent a token. */
function spend(buckets: Map<string, Bucket>, key: string, p: SignInThrottle, now: number): number {
  const b = buckets.get(key) ?? { tokens: p.burst, seen: now };
  b.tokens = Math.min(p.burst, b.tokens + (now - b.seen) / p.intervalMs);
  b.seen = now;
  buckets.set(key, b);
  if (b.tokens < 1) return Math.max(1, Math.ceil(((1 - b.tokens) * p.intervalMs) / 1000));
  b.tokens -= 1;
  return 0;
}

const accountBuckets = new Map<string, Bucket>();
const deviceBuckets = new Map<string, Bucket>();
const setupBuckets = new Map<string, Bucket>();

/** `accountDigest`: SHA-256 of the normalised username, 128 bits, hex. */
export function accountDigest(username: string): string {
  return createHash("sha256").update(username.trim().toLowerCase()).digest("hex").slice(0, 32);
}

/**
 * The console's per-account bucket for this attempt: the trusted device's own when
 * the request carries a device cookie signed for this username, the account's shared
 * one otherwise. Returns the seconds to wait (0 = go ahead) and the key it spent, so
 * a success clears that bucket and no other.
 */
export function throttleSignIn(
  username: string,
  deviceCookie: string | undefined,
  now = Date.now(),
): { wait: number; spent: () => void } {
  const acct = accountDigest(username);
  const device = deviceFor(deviceCookie, acct);
  const policy = throttlesInForce().consoleAccount;
  const [buckets, key] = device ? [deviceBuckets, `${acct}|dev:${device}`] : [accountBuckets, acct];
  return { wait: spend(buckets, key, policy, now), spent: () => buckets.delete(key) };
}

/** The setup's per-address bucket — the console's address policy, as Go spends it. */
export function throttleSetup(address: string, now = Date.now()): number {
  return spend(setupBuckets, address, throttlesInForce().consoleAddress, now);
}

// ── the trusted-device cookie ─────────────────────────────────────────────────

export const DEVICE_COOKIE = "foyer_device";
const deviceKey = randomBytes(32);

const mac = (acct: string, nonce: string) =>
  createHmac("sha256", deviceKey).update(`v1|${acct}|${nonce}`).digest("base64url");

export function issueDeviceCookie(username: string): string {
  const acct = accountDigest(username);
  const nonce = randomBytes(16).toString("base64url");
  return ["v1", acct, nonce, mac(acct, nonce)].join(".");
}

function deviceFor(value: string | undefined, acct: string): string | null {
  if (!value || value.length > 256) return null;
  const parts = value.split(".");
  if (parts.length !== 4 || parts[0] !== "v1" || parts[1] !== acct) return null;
  const want = Buffer.from(mac(acct, parts[2]!));
  const got = Buffer.from(parts[3]!);
  return want.length === got.length && timingSafeEqual(want, got) ? parts[2]! : null;
}

// ── body ceilings ─────────────────────────────────────────────────────────────

/** `envelopeAllowance`: room for the JSON around a base64 field. */
const ENVELOPE_ALLOWANCE = 64 << 10;

/**
 * `bodyLimitFor`: the guest portal's ceiling on its own operations; the anonymous
 * ceiling for a public console operation or a caller with no session; the console's
 * for a signed-in one, and the hero photograph's own, derived from its contract
 * figure, for `putPortalHeroImage`.
 */
export function bodyCeilingFor(opts: {
  portal: boolean;
  isPublic: boolean;
  signedIn: boolean;
  heroImage: boolean;
  imageMaxBytes: number;
}): number {
  const c = ceilingsInForce();
  if (opts.portal) return c.portalBytes;
  if (opts.isPublic || !opts.signedIn) return c.anonymousBytes;
  if (opts.heroImage) return Math.ceil(opts.imageMaxBytes / 3) * 4 + ENVELOPE_ALLOWANCE;
  return c.consoleBytes;
}
