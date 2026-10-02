/**
 * Phone sign-in, in the mock.
 *
 * The oracle's twin of `internal/identity/otp`: the same destination allowlist, the
 * same limits and the same order, so a screen driven against this server fails the
 * way the product fails. What it deliberately does **not** copy is the storage — a
 * keyed hash is a property of the database the product writes, and a fake that
 * pretended to have one would be asserting nothing. The limits and the refusals are
 * what a screen can see, and those are copied exactly.
 *
 * **The outbox is the fake provider.** Nothing here sends a message. `GET
 * /mock/phone-outbox` lists what would have been sent, outside `/api/v0`, so someone
 * running `pnpm dev` can complete the flow — the same kind of review
 * affordance as `?present=0` and `/venues`. It exists only in this server; the Go
 * binary has no route that returns a code, and `sms.Fake` cannot be selected by any
 * configuration an operator can save.
 */
import { randomInt, randomUUID } from "node:crypto";
import type {
  FieldFault,
  PhoneAbuseControls,
  PhoneAbuseControlsUpdate,
  PhoneSignIn,
  PhoneSignInLimits,
  PhoneSignInUpdate,
  PhoneSpendCap,
  PhoneTestMessageResult,
} from "@foyer/contract";
import { siteDay } from "./sitetime.js";

/** `otp.DefaultLimits`, restated for the one server that cannot import Go. */
export const PHONE_LIMITS = {
  perNumberPerHour: 3,
  perNumberPerDay: 5,
  perDevicePerHour: 5,
  perAddressPerHour: 60,
  codeMinutes: 5,
  maxAttempts: 5,
} as const;

/**
 * Each limit's range, as `PhoneAbuseLimitsUpdate` declares it, and its default.
 * `internal/api` reads the ranges from the embedded contract; the mock states them
 * once here and `messaging-settings.test.ts` holds both to the schema.
 */
export const PHONE_LIMIT_RANGES = {
  perNumberPerHour: [1, 10],
  perNumberPerDay: [1, 20],
  perDevicePerHour: [1, 20],
  perAddressPerHour: [1, 2000],
  codeMinutes: [1, 10],
  maxAttempts: [1, 10],
} as const;

/** `PhoneSpendCapUpdate.dailyLimit`'s range. */
export const SPEND_CAP_RANGE = { minimum: 1, maximum: 1_000_000 } as const;

const abuseBounds = (): PhoneAbuseControls["bounds"] =>
  Object.fromEntries(
    (Object.keys(PHONE_LIMIT_RANGES) as (keyof typeof PHONE_LIMIT_RANGES)[]).map((k) => [
      k,
      { minimum: PHONE_LIMIT_RANGES[k][0], maximum: PHONE_LIMIT_RANGES[k][1], default: PHONE_LIMITS[k] },
    ]),
  ) as PhoneAbuseControls["bounds"];

interface Config {
  settings: PhoneSignIn;
  apiKey: string;
  secretKey: string;
}

/** `otp.MobilePrefixes`: the Vietnamese mobile plan's ranges, national form. */
export const MOBILE_PREFIXES = ["03", "05", "07", "08", "09"] as const;

interface Code {
  maxAttempts: number;
  id: string;
  siteId: string;
  clientId: string;
  mac: string;
  phone: string;
  code: string;
  createdAt: number;
  expiresAt: number;
  attempts: number;
  consumed: boolean;
}

export interface OutboxMessage {
  to: string;
  code: string;
  text: string;
  at: string;
  siteId: string;
}

const configs = new Map<string, Config>();
const codes: Code[] = [];
const sends = new Map<string, number>(); // site|day -> count
const totals = new Map<string, number>(); // day -> count across every site
const outbox: OutboxMessage[] = [];
const abuse = new Map<string, Omit<PhoneAbuseControls, "bounds">>();
let spendCap: Omit<PhoneSpendCap, "bounds"> = { dailyLimit: null, updatedAt: null };

const HOUR = 3_600_000;
/** The site's calendar day — `sites.time_zone`, not this process's clock. */
const localDay = (siteId: string, t: number) => siteDay(siteId, t);

/** The limits in force at a venue: its tuned ones, or the defaults. */
function limitsFor(siteId: string): PhoneSignInLimits {
  return abuse.get(siteId)?.limits ?? { ...PHONE_LIMITS };
}

/** `Server.phoneCodeMinutes`: how long a code works here, for the code form's sentence. */
export function phoneCodeMinutes(siteId: string): number {
  return limitsFor(siteId).codeMinutes;
}

/** `otp.InRanges`. */
function inRanges(e164: string, siteId: string): boolean {
  const open = abuse.get(siteId)?.mobilePrefixes ?? [...MOBILE_PREFIXES];
  return open.includes(`0${e164.slice(3, 4)}`);
}

/** Both ceilings, one message: the site's daily limit and the deployment's cap. */
function charge(siteId: string, dailyLimit: number, now: number): boolean {
  const day = localDay(siteId, now);
  const key = `${siteId}|${day}`;
  if ((sends.get(key) ?? 0) >= dailyLimit) return false;
  if (spendCap.dailyLimit !== null && (totals.get(day) ?? 0) >= spendCap.dailyLimit) return false;
  sends.set(key, (sends.get(key) ?? 0) + 1);
  totals.set(day, (totals.get(day) ?? 0) + 1);
  return true;
}

export function phoneAbuseControlsFor(siteId: string): PhoneAbuseControls {
  return {
    ...(abuse.get(siteId) ?? {
      siteId,
      limits: { ...PHONE_LIMITS },
      mobilePrefixes: [...MOBILE_PREFIXES],
      country: "VN" as const,
      customised: false,
      updatedAt: null,
    }),
    bounds: abuseBounds(),
  };
}

/** `updatePhoneAbuseControls`' rules beyond the schema, in `internal/api`'s order. */
export function setPhoneAbuseControls(
  siteId: string,
  body: PhoneAbuseControlsUpdate,
):
  | { ok: true; controls: PhoneAbuseControls }
  | {
      ok: false;
      faults: Record<string, FieldFault>;
      errors: Record<string, string>;
      fieldParams?: Record<string, Record<string, number>>;
    } {
  const l = body.limits;
  if (l.perNumberPerDay < l.perNumberPerHour) {
    return {
      ok: false,
      faults: { "limits.perNumberPerDay": "below-minimum" },
      errors: { "limits.perNumberPerDay": `At least ${l.perNumberPerHour}.` },
      fieldParams: { "limits.perNumberPerDay": { minimumCount: l.perNumberPerHour } },
    };
  }
  if (new Set(body.mobilePrefixes).size !== body.mobilePrefixes.length) {
    return {
      ok: false,
      faults: { mobilePrefixes: "duplicate" },
      errors: { mobilePrefixes: "Choose from 03, 05, 07, 08 and 09, each once." },
    };
  }
  abuse.set(siteId, {
    siteId,
    limits: { ...l },
    mobilePrefixes: [...body.mobilePrefixes],
    country: "VN",
    customised: true,
    updatedAt: new Date().toISOString(),
  });
  return { ok: true, controls: phoneAbuseControlsFor(siteId) };
}

export function phoneSpendCap(): PhoneSpendCap {
  return { ...spendCap, bounds: { ...SPEND_CAP_RANGE } };
}

export function setPhoneSpendCap(dailyLimit: number | null): PhoneSpendCap {
  spendCap = { dailyLimit, updatedAt: new Date().toISOString() };
  return phoneSpendCap();
}

/** The operator's view. Never the keys — `credentialsSet` is all a console gets. */
export function phoneSignInFor(siteId: string): PhoneSignIn {
  const c = configs.get(siteId);
  const base: PhoneSignIn = c
    ? { ...c.settings, limits: limitsFor(siteId), sentToday: sends.get(`${siteId}|${localDay(siteId, Date.now())}`) ?? 0 }
    : {
        siteId,
        provider: null,
        credentialsSet: false,
        sandbox: false,
        dailyLimit: null,
        sentToday: 0,
        sessionMinutes: null,
        messageTemplate: null,
        country: "VN",
        limits: limitsFor(siteId),
      };
  return base;
}

/** Validates and stores, in `internal/api`'s order. */
export function setPhoneSignIn(
  siteId: string,
  body: PhoneSignInUpdate,
):
  | { ok: true; settings: PhoneSignIn; credentialsChanged: boolean }
  | { ok: false; faults: Record<string, FieldFault>; errors: Record<string, string> } {
  const faults: Record<string, FieldFault> = {};
  const errors: Record<string, string> = {};
  if ((body.messageTemplate.match(/\{code\}/g) ?? []).length !== 1) {
    faults.messageTemplate = "missing-code-placeholder";
    errors.messageTemplate = "Put {code} in the message exactly once.";
  }
  const blank = (v: string | null | undefined) => !v || !v.trim();
  if (body.provider === "esms-sms" && blank(body.brandname)) {
    faults.brandname = "required";
    errors.brandname = "Required for SMS.";
  }
  if (body.provider === "esms-zalo") {
    if (blank(body.zaloOaId)) {
      faults.zaloOaId = "required";
      errors.zaloOaId = "Required for Zalo.";
    }
    if (blank(body.zaloTemplateId)) {
      faults.zaloTemplateId = "required";
      errors.zaloTemplateId = "Required for Zalo.";
    }
  }
  for (const f of ["apiKey", "secretKey"] as const) {
    const v = body[f];
    if (v !== undefined && !/^[\x21-\x7e]+$/.test(v)) {
      faults[f] = "required";
      errors[f] = "Paste the key without spaces or line breaks.";
    }
  }
  if (Object.keys(faults).length) return { ok: false, faults, errors };

  const prev = configs.get(siteId);
  if (!prev && (body.apiKey === undefined || body.secretKey === undefined)) {
    return {
      ok: false,
      faults: { apiKey: "required", secretKey: "required" },
      errors: { apiKey: "Required the first time.", secretKey: "Required the first time." },
    };
  }
  const settings: PhoneSignIn = {
    siteId,
    provider: body.provider,
    credentialsSet: true,
    sandbox: body.sandbox ?? false,
    dailyLimit: body.dailyLimit,
    sentToday: 0,
    sessionMinutes: body.sessionMinutes,
    messageTemplate: body.messageTemplate,
    country: "VN",
    limits: { ...PHONE_LIMITS },
    ...(body.provider === "esms-sms"
      ? { brandname: body.brandname ?? null }
      : { zaloOaId: body.zaloOaId ?? null, zaloTemplateId: body.zaloTemplateId ?? null }),
  };
  configs.set(siteId, {
    settings,
    apiKey: body.apiKey ?? prev!.apiKey,
    secretKey: body.secretKey ?? prev!.secretKey,
  });
  return {
    ok: true,
    settings: phoneSignInFor(siteId),
    credentialsChanged: body.apiKey !== undefined || body.secretKey !== undefined,
  };
}

/** Whether the site can send, and for how long a phone guest is granted. */
export function phoneOffered(siteId: string): number | null {
  const c = configs.get(siteId);
  return c?.settings.sessionMinutes ?? null;
}

/** `otp.NormaliseVN`: Vietnamese mobile ranges only, or null. */
export function normaliseVN(raw: string): string | null {
  let digits = "";
  let plus = false;
  const s = raw.trim();
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!;
    if (ch >= "0" && ch <= "9") digits += ch;
    else if (ch === "+" && i === 0) plus = true;
    else if (" .-() ".includes(ch)) continue;
    else return null;
    if (digits.length > 16) return null;
  }
  let national: string;
  if (plus && digits.startsWith("84")) national = digits.slice(2).replace(/^0/, "");
  else if (plus) return null;
  else if (digits.startsWith("0084")) national = digits.slice(4);
  else if (digits.startsWith("84") && digits.length === 11) national = digits.slice(2);
  else if (digits.startsWith("0") && digits.length === 10) national = digits.slice(1);
  else return null;
  if (national.length !== 9 || !"35789".includes(national[0]!)) return null;
  return `+84${national}`;
}

export type SendFailure = "phone-invalid" | "code-limit" | "code-unavailable";

const deviceWindow = new Map<string, { start: number; n: number }>();
/**
 * `otp.DeviceKey`: the MAC on the connection it was claimed from,
 * so a stranger claiming a present guest's MAC spends their own budget. `source`
 * is the socket peer where the mock has one; `app.request` in a unit test has none.
 */
function allowDevice(mac: string, source: string, now: number, limit: number): boolean {
  const key = source ? `${mac}|${source}` : mac;
  const w = deviceWindow.get(key);
  if (!w || now - w.start >= HOUR) {
    deviceWindow.set(key, { start: now, n: 1 });
    return true;
  }
  if (w.n >= limit) return false;
  w.n++;
  return true;
}

/** Issues and "sends" a code. The caller has confirmed presence. */
export function sendPhoneCode(
  client: { id: string; mac: string; siteId: string },
  rawPhone: string,
  source = "",
): { ok: true } | { ok: false; reason: SendFailure } {
  const c = configs.get(client.siteId);
  if (!c) return { ok: false, reason: "code-unavailable" };
  const phone = normaliseVN(rawPhone);
  if (!phone || !inRanges(phone, client.siteId)) return { ok: false, reason: "phone-invalid" };
  const limits = limitsFor(client.siteId);
  const now = Date.now();
  if (!allowDevice(client.mac, source, now, limits.perDevicePerHour)) return { ok: false, reason: "code-limit" };
  const toNumber = codes.filter((k) => k.siteId === client.siteId && k.phone === phone);
  if (
    toNumber.filter((k) => now - k.createdAt < HOUR).length >= limits.perNumberPerHour ||
    toNumber.filter((k) => now - k.createdAt < 24 * HOUR).length >= limits.perNumberPerDay
  ) {
    return { ok: false, reason: "code-limit" };
  }
  if (!charge(client.siteId, c.settings.dailyLimit ?? 0, now)) return { ok: false, reason: "code-unavailable" };
  for (const k of codes) if (k.clientId === client.id) k.consumed = true;
  const code = String(randomInt(0, 1_000_000)).padStart(6, "0");
  codes.push({
    id: `otp_${randomUUID().replace(/-/g, "")}`,
    siteId: client.siteId,
    clientId: client.id,
    mac: client.mac,
    phone,
    code,
    createdAt: now,
    expiresAt: now + limits.codeMinutes * 60_000,
    maxAttempts: limits.maxAttempts,
    attempts: 0,
    consumed: false,
  });
  outbox.push({
    to: phone,
    code,
    text: (c.settings.messageTemplate ?? "{code}").replace("{code}", code),
    at: new Date(now).toISOString(),
    siteId: client.siteId,
  });
  return { ok: true };
}

export type CheckFailure = "code-wrong" | "code-expired";

/** Spends one attempt on this browser's newest live code. */
export function checkPhoneCode(
  clientId: string,
  submitted: string,
): { ok: true; phone: string } | { ok: false; reason: CheckFailure } {
  const code = submitted.replace(/[\s-]/g, "");
  if (!/^\d{6}$/.test(code)) return { ok: false, reason: "code-wrong" };
  const now = Date.now();
  const live = codes
    .filter((k) => k.clientId === clientId && !k.consumed && k.expiresAt > now)
    .sort((a, b) => b.createdAt - a.createdAt)[0];
  if (!live || live.attempts >= live.maxAttempts) return { ok: false, reason: "code-expired" };
  live.attempts++;
  if (live.code !== code) {
    return { ok: false, reason: live.attempts >= live.maxAttempts ? "code-expired" : "code-wrong" };
  }
  live.consumed = true;
  return { ok: true, phone: live.phone };
}

/**
 * `sendPhoneTestMessage`: one real, counted message to the operator's own number —
 * the template with a random code that is stored nowhere. The fake provider always
 * accepts, so the two provider outcomes are the Go adapter's to produce.
 */
export function sendTestMessage(siteId: string, rawPhone: string): PhoneTestMessageResult {
  const c = configs.get(siteId);
  const sentToday = () => sends.get(`${siteId}|${localDay(siteId, Date.now())}`) ?? 0;
  if (!c) return { outcome: "not-configured", providerCode: null, sentToday: sentToday() };
  const phone = normaliseVN(rawPhone);
  if (!phone || !inRanges(phone, siteId)) return { outcome: "phone-invalid", providerCode: null, sentToday: sentToday() };
  const now = Date.now();
  if (!charge(siteId, c.settings.dailyLimit ?? 0, now)) {
    return { outcome: "ceiling-reached", providerCode: null, sentToday: sentToday() };
  }
  const code = String(randomInt(0, 1_000_000)).padStart(6, "0");
  outbox.push({
    to: phone,
    code,
    text: (c.settings.messageTemplate ?? "{code}").replace("{code}", code),
    at: new Date(now).toISOString(),
    siteId,
  });
  return { outcome: "sent", providerCode: null, sentToday: sentToday() };
}

/** The fake provider's outbox, newest last. Review affordance only. */
export function phoneOutbox(): OutboxMessage[] {
  return [...outbox];
}

/**
 * A data-subject erasure by MAC: every code row the device requested, the mock's
 * `phone_codes` (`internal/store/phone.go` registers the same eraser). The caller
 * owns MAC normalisation, so this module does not import `state.ts`.
 */
export function erasePhoneCodes(isSubject: (mac: string, phone: string) => boolean): number {
  let n = 0;
  for (let i = codes.length - 1; i >= 0; i--) {
    if (isSubject(codes[i]!.mac, codes[i]!.phone)) {
      codes.splice(i, 1);
      n += 1;
    }
  }
  return n;
}

/** For tests: forget every configuration, code and send. */
export function resetPhone(): void {
  configs.clear();
  codes.length = 0;
  sends.clear();
  totals.clear();
  outbox.length = 0;
  deviceWindow.clear();
  abuse.clear();
  spendCap = { dailyLimit: null, updatedAt: null };
}
