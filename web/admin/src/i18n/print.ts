/**
 * Copy for the voucher print sheet, loaded with it.
 *
 * Two audiences in one file, and the split between them is the point of it.
 *
 * `print` is the **operator's** copy — the controls and notices beside the sheet on
 * screen. In the console's language, like the rest of the console (the Vietnamese is
 * `vi/print.ts`), and swept by the house-rule tests with every other catalogue.
 *
 * `slip` is the **guest's** copy — what is printed on paper and handed across a
 * counter. It is in the *venue's* language, not the console's: a Vietnamese hotel's
 * slip must not say "Valid for 1 day from first use", and paper is the one surface
 * Foyer can never take back. So it is a per-language map the
 * way the portal's is — English defines the keys and Vietnamese is typed as a total
 * map of them, so a forgotten translation is a compile error. The Vietnamese follows
 * the portal's own vocabulary (`web/portal/src/i18n/vi.ts`: *mã phiếu*, *mạng*,
 * *trang đăng nhập*), because the guest reads the slip and then the portal, and two
 * words for one thing is a question at the counter.
 *
 * Nothing outside the lazily loaded print kit (`print/kit.ts`) imports this file, so
 * neither audience's copy is in the entry chunk. `core.ts` has the rule.
 *
 * **It imports nothing, not even `makeT`, and that is measured.** A lazy chunk that
 * imports a module the entry also uses makes Rolldown hoist the shared modules out
 * of the entry into chunks of their own, which are still fetched first: the first
 * build of this sheet, importing `api`, `ui/primitives` and `useQuery`, split the
 * entry into three and grew it by 2 923 B gzip — the effect `Dashboard.tsx` records
 * for `FirstRun`. So the lookups below are written out rather than borrowed.
 */
import type { Entry } from "./catalogue.js";

export const print = {
  "print.language.label": "Slip language",
  "print.preview.title": (n: number) => `Slip 1 of ${n}, as it prints`,
  "print.qr.missing.title": "These slips print without a Wi-Fi QR code",
  "print.qr.missing.body":
    "This venue has no network name set. Once an administrator sets it in Portal settings, every slip carries the name and a QR code that joins the network.",
  "print.qr.tooLong.body":
    "The network name in Portal settings is longer than a Wi-Fi network name can be (32 bytes), so no phone could join it from a QR code. The slips print it as text. Set it to the SSID exactly as broadcast to add the QR code.",
  "print.settings.loading": "Loading the network name and language these slips print with…",
  "print.settings.failed":
    "Cannot load the venue’s network name and language, so the slips cannot be printed correctly yet.",
};

type PrintKey = keyof typeof print;
type Params<C, K extends keyof C> = C[K] extends (...args: infer A) => string ? A : [];

function lookup(entry: Entry, params: unknown[]): string {
  return typeof entry === "function" ? (entry as (...a: unknown[]) => string)(...params) : entry;
}

/**
 * The operator half in the console's language. `lang.ts` has already written that
 * language to `<html lang>` before anything lazy loads, and it is read from there
 * rather than imported, for the reason at the top of this file: this module imports
 * nothing. `kit.ts` re-exports `printCopyReady` and the sheet awaits it, the way every
 * other lazy module's copy is awaited by `withCopy`.
 */
let operatorVi: Partial<Record<PrintKey, Entry>> = {};
export const printCopyReady: Promise<void> =
  typeof document !== "undefined" && document.documentElement.lang === "vi"
    ? import("./vi/print.js").then(
        (m) => void (operatorVi = m.vi),
        () => undefined,
      )
    : Promise.resolve();

/** The operator half of this module's copy. Total and typed, as `makeT` is. */
export function printText<K extends PrintKey>(key: K, ...params: Params<typeof print, K>): string {
  return lookup(operatorVi[key] ?? print[key], params);
}

/* ── the guest's copy ─────────────────────────────────────────────────────── */

const unit = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

export const slipEn = {
  "slip.network": "Wi-Fi network",
  "slip.password": "Wi-Fi password",
  "slip.scan": "Scan to join",
  "slip.code": "Voucher code",
  "slip.steps": "Join the network, then enter this code on the sign-in page.",
  "slip.fromFirstUse": (span: string) => `Valid for ${span} from first use`,
  // What a plan with no stated duration prints. The clock starting at first use is
  // true and known; a length is not, and a default argument would put "Valid for
  // 1 hour from first use" onto a slip Foyer can never take back.
  "slip.fromFirstUseUnknown": "Valid from first use",
  "slip.until": (date: string) => `Use before ${date}`,
  "slip.qrLabel": (ssid: string) => `QR code that joins the Wi-Fi network ${ssid}`,
  "slip.days": (n: number) => unit(n, "day", "days"),
  "slip.hours": (n: number) => unit(n, "hour", "hours"),
  "slip.minutes": (n: number) => unit(n, "minute", "minutes"),
};

type SlipKey = keyof typeof slipEn;
type SlipCatalogue = { [K in SlipKey]: (typeof slipEn)[K] };

const slipVi: SlipCatalogue = {
  "slip.network": "Mạng Wi-Fi",
  "slip.password": "Mật khẩu Wi-Fi",
  "slip.scan": "Quét để kết nối",
  "slip.code": "Mã phiếu",
  "slip.steps": "Kết nối vào mạng, rồi nhập mã này ở trang đăng nhập.",
  "slip.fromFirstUse": (span: string) => `Dùng được ${span} kể từ lần dùng đầu tiên`,
  "slip.fromFirstUseUnknown": "Tính thời gian từ lần dùng đầu tiên",
  "slip.until": (date: string) => `Dùng trước ngày ${date}`,
  "slip.qrLabel": (ssid: string) => `Mã QR kết nối mạng Wi-Fi ${ssid}`,
  "slip.days": (n: number) => `${n} ngày`,
  "slip.hours": (n: number) => `${n} giờ`,
  "slip.minutes": (n: number) => `${n} phút`,
};

/** The languages a slip can be printed in: exactly the contract's `Language`. */
export const SLIP_COPY = { en: slipEn, vi: slipVi } as const;
export type SlipLanguage = keyof typeof SLIP_COPY;

/**
 * Each language named in itself, the way the portal's switcher names them.
 *
 * A function rather than an exported map: the house-rule sweeps in `admin.test.ts`
 * read every exported object of strings as a catalogue, and would take `en` and
 * `vi` for message keys.
 */
export function slipLanguageName(lang: SlipLanguage): string {
  return lang === "vi" ? "Tiếng Việt" : "English";
}

/** The lookup for one slip language. */
export function slipT(lang: SlipLanguage) {
  const catalogue: SlipCatalogue = SLIP_COPY[lang];
  return <K extends SlipKey>(key: K, ...params: Params<SlipCatalogue, K>): string =>
    lookup(catalogue[key], params);
}
