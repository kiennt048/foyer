/**
 * Guest details and marketing consent — the oracle's world.
 *
 * `internal/store/privacy_guestdata.go` is the real thing and `internal/api/
 * handlers_guestdata.go` holds the rules; this is the copy `make diff-api` compares
 * the Go server against, so its versions, refusals and rows are the contract's
 * shapes exactly.
 *
 * **The rule both copies keep: consent is a ticked box and nothing else.** The venue
 * is asking, the box says exactly `yes`, the version beside it is one the venue
 * published, and there is a phone number or an e-mail address to contact — or no
 * consent row exists. Accepting the terms is never read as one.
 */
import { createHash } from "node:crypto";
import type {
  ConsentWithdrawnBy,
  GuestDataSettings,
  GuestDataSettingsUpdate,
  Language,
  MarketingConsent,
  PortalDataCollection,
} from "@foyer/contract";
import { normaliseMac } from "./state.js";

/** The SHA-256 of the trimmed text, lower-case hex — `store.ConsentVersion`. */
export function consentVersion(text: string): string {
  return createHash("sha256").update(text.trim(), "utf8").digest("hex");
}

const settings = new Map<string, GuestDataSettings>();
/** Every text ever published, by site then version. */
const texts = new Map<string, Map<string, { lang: Language; body: string }>>();
const consents: MarketingConsent[] = [];
/** What guests typed, by grant — never a mailing list, and never exported. */
const details = new Map<string, { name?: string; phone?: string; email?: string }>();
let seq = 0;

const EMPTY: GuestDataSettings = {
  fields: [],
  marketingConsent: { enabled: false, text: {}, versions: {} },
};

export function guestDataSettings(siteId: string): GuestDataSettings {
  return structuredClone(settings.get(siteId) ?? EMPTY);
}

/** The three rules beyond the schema, first failure wins — `guestDataFault`. */
export function guestDataFault(
  body: GuestDataSettingsUpdate,
): { field: string; fault: "duplicate" | "required" | "needs-contact-field"; message: string } | null {
  const seen = new Set<string>();
  for (const f of body.fields) {
    if (seen.has(f.field)) return { field: "fields", fault: "duplicate", message: "Each field once." };
    seen.add(f.field);
  }
  if (!body.marketingConsent.enabled) return null;
  const t = body.marketingConsent.text ?? {};
  if (!(t.en ?? "").trim() && !(t.vi ?? "").trim()) {
    return { field: "marketingConsent.text", fault: "required", message: "Required." };
  }
  if (!seen.has("phone") && !seen.has("email")) {
    return { field: "fields", fault: "needs-contact-field", message: "Collect a phone number or an e-mail address." };
  }
  return null;
}

export function putGuestDataSettings(siteId: string, body: GuestDataSettingsUpdate): GuestDataSettings {
  const out: GuestDataSettings = {
    fields: body.fields.map((f) => ({ field: f.field, required: f.required })),
    marketingConsent: { enabled: body.marketingConsent.enabled, text: {}, versions: {} },
  };
  const published = texts.get(siteId) ?? new Map();
  texts.set(siteId, published);
  for (const lang of ["en", "vi"] as const) {
    const body0 = body.marketingConsent.text?.[lang]?.trim();
    if (!body0) continue;
    const v = consentVersion(body0);
    out.marketingConsent.text[lang] = body0;
    out.marketingConsent.versions[lang] = v;
    if (!published.has(v)) published.set(v, { lang, body: body0 });
  }
  settings.set(siteId, out);
  return structuredClone(out);
}

/** `portalDataCollection`: the texts only while the venue is asking. */
export function portalDataCollection(siteId: string): PortalDataCollection {
  const s = guestDataSettings(siteId);
  return {
    fields: s.fields,
    marketingConsent: s.marketingConsent.enabled
      ? { text: s.marketingConsent.text, versions: s.marketingConsent.versions }
      : null,
  };
}

const validName = (v: string) => [...v].length <= 120 && !/\p{Cc}/u.test(v);
function validPhone(v: string): boolean {
  if (v.length > 32 || !/^\+?[0-9 .()-]*$/.test(v)) return false;
  const digits = v.replace(/[^0-9]/g, "").length;
  return digits >= 8 && digits <= 15;
}
function validEmail(v: string): boolean {
  if (v.length > 254 || /[\s\p{Cc}<>"]/u.test(v)) return false;
  const at = v.indexOf("@");
  if (at < 1 || at !== v.lastIndexOf("@")) return false;
  const domain = v.slice(at + 1);
  const dot = domain.indexOf(".");
  return dot > 0 && dot < domain.length - 1 && !domain.endsWith(".");
}
const VALID = { name: validName, phone: validPhone, email: validEmail } as const;

export interface GuestInput {
  details: { name?: string; phone?: string; email?: string };
  consentVersion: string | null;
}

/** `readGuestForm`: a reason sends the guest back before anything is claimed. */
export function readGuestForm(
  siteId: string,
  form: Record<string, unknown>,
  opts: { omitPhone?: boolean } = {},
): { ok: true; input: GuestInput } | { ok: false; reason: "details-required" | "details-invalid" } {
  const s = guestDataSettings(siteId);
  const got: GuestInput["details"] = {};
  for (const f of s.fields) {
    /* Phone sign-in's code form: the number the code proved is the phone. */
    if (opts.omitPhone && f.field === "phone") continue;
    const v = String(form[f.field] ?? "").trim();
    if (!v) {
      if (f.required) return { ok: false, reason: "details-required" };
      continue;
    }
    if (!VALID[f.field](v)) return { ok: false, reason: "details-invalid" };
    got[f.field] = v;
  }
  const version = String(form.consentVersion ?? "");
  const ticked =
    s.marketingConsent.enabled && form.marketingConsent === "yes" && version !== "" && (got.phone || got.email);
  return { ok: true, input: { details: got, consentVersion: ticked ? version : null } };
}

/**
 * `withVerifiedPhone`: a phone guest's verified number completes the
 * venue's `phone` field and is the consent's contact — only where the venue
 * collects a phone number, and only for a box ticked `yes` with a version.
 */
export function withVerifiedPhone(
  siteId: string,
  form: Record<string, unknown>,
  input: GuestInput,
  phone: string,
): GuestInput {
  const s = guestDataSettings(siteId);
  if (!s.fields.some((f) => f.field === "phone")) return input;
  const details = { ...input.details, phone };
  const version = String(form.consentVersion ?? "");
  const ticked = s.marketingConsent.enabled && form.marketingConsent === "yes" && version !== "";
  return { details, consentVersion: input.consentVersion ?? (ticked ? version : null) };
}

/** `keepGuestInput`: called only for a guest who is now online. */
export function keepGuestInput(siteId: string, mac: string, grantId: string | null, input: GuestInput): void {
  const d = input.details;
  if (grantId && (d.name || d.phone || d.email)) details.set(grantId, { ...d });
  if (!input.consentVersion) return;
  const text = texts.get(siteId)?.get(input.consentVersion);
  if (!text) return;
  seq++;
  consents.push({
    id: `mc-${String(seq).padStart(8, "0")}`,
    siteId,
    clientMac: normaliseMac(mac),
    consentedAt: new Date().toISOString(),
    state: "active",
    lang: text.lang,
    consentVersion: input.consentVersion,
    consentText: text.body,
    ...(d.name ? { name: d.name } : {}),
    ...(d.phone ? { phone: d.phone } : {}),
    ...(d.email ? { email: d.email } : {}),
  });
}

/** The one rule both withdrawals share: the contact goes with it. */
function withdraw(c: MarketingConsent, by: ConsentWithdrawnBy): void {
  if (c.state === "withdrawn") return;
  c.state = "withdrawn";
  c.withdrawnAt = new Date().toISOString();
  c.withdrawnBy = by;
  delete c.name;
  delete c.phone;
  delete c.email;
}

export function marketingConsents(siteId: string, state?: string): MarketingConsent[] {
  return consents
    .filter((c) => c.siteId === siteId && (!state || c.state === state))
    .reverse()
    .map((c) => structuredClone(c));
}

export function withdrawConsent(siteId: string, id: string): MarketingConsent | null {
  const c = consents.find((x) => x.siteId === siteId && x.id === id);
  if (!c) return null;
  withdraw(c, "operator");
  return structuredClone(c);
}

export function withdrawDevice(siteId: string, mac: string): number {
  const want = normaliseMac(mac);
  let n = 0;
  for (const c of consents) {
    if (c.siteId === siteId && c.state === "active" && normaliseMac(c.clientMac) === want) {
      withdraw(c, "guest");
      n++;
    }
  }
  return n;
}

/** Given or withdrawn in `[from, to)`, by when given — `ConsentsBetween`. */
export function consentsBetween(siteId: string, from: number, to: number): MarketingConsent[] {
  const within = (iso: string | null | undefined) => iso != null && Date.parse(iso) >= from && Date.parse(iso) < to;
  return consents
    .filter((c) => c.siteId === siteId && (within(c.consentedAt) || within(c.withdrawnAt)))
    .sort((a, b) => Date.parse(a.consentedAt) - Date.parse(b.consentedAt))
    .map((c) => structuredClone(c));
}

/** The erasure's share: one device's consents. Returns how many went. */
export function eraseConsents(mac: string): number {
  let n = 0;
  for (let i = consents.length - 1; i >= 0; i--) {
    if (normaliseMac(consents[i]!.clientMac) === mac) {
      consents.splice(i, 1);
      n++;
    }
  }
  return n;
}

/** An erasure by phone number's share: the details and the consents that give it. */
export function eraseByPhone(isNumber: (phone: string | null | undefined) => boolean): {
  details: number;
  consents: number;
} {
  let d = 0;
  for (const [grantId, row] of details) if (isNumber(row.phone)) (details.delete(grantId), d++);
  let c = 0;
  for (let i = consents.length - 1; i >= 0; i--) {
    if (isNumber(consents[i]!.phone)) {
      consents.splice(i, 1);
      c++;
    }
  }
  return { details: d, consents: c };
}

/** An erasure by e-mail address's share: the details and the consents that give it,
 * compared without regard to case (`store.EmailSubject`). */
export function eraseByEmail(email: string): { details: number; consents: number } {
  const is = (v: string | null | undefined) => Boolean(v) && v!.toLowerCase() === email.toLowerCase();
  let d = 0;
  for (const [grantId, row] of details) if (is(row.email)) (details.delete(grantId), d++);
  let c = 0;
  for (let i = consents.length - 1; i >= 0; i--) {
    if (is(consents[i]!.email)) {
      consents.splice(i, 1);
      c++;
    }
  }
  return { details: d, consents: c };
}

/** For a test: the details kept for one grant. */
export function detailsFor(grantId: string) {
  return details.get(grantId);
}
