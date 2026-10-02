/**
 * The print kit: everything the voucher slip needs that the rest of the console does
 * not — the QR encoder, the guest copy in both languages, the operator copy beside
 * the sheet, and the sheet's stylesheet. One lazily loaded chunk.
 *
 * **It must import nothing the entry chunk also imports.** Measured: a lazy module
 * importing `api`, `ui/primitives` and `@tanstack/react-query` makes Rolldown hoist
 * those out of the entry into three shared chunks that are still fetched before the
 * first screen, and the entry grows by 2 923 B gzip instead of shrinking — the same effect `screens/Dashboard.tsx` records for `FirstRun`. So the
 * React half of the sheet (`VoucherSlips.tsx`) rides in the `/vouchers` chunk, and
 * this half is pure functions and data. `check-admin-budget.mjs` is where a regression shows.
 */
import type { VoucherBatch } from "@foyer/contract";
import { type SlipLanguage, slipT } from "../i18n/print.js";
import { encodeQr } from "./qr.js";
import { qrPath } from "./qrsvg.js";
import { wifiJoinPayload } from "./wifi.js";

export { ssidTooLong } from "./wifi.js";
import "./slips.css";

export { printCopyReady, printText, SLIP_COPY, slipLanguageName, slipT, type SlipLanguage } from "../i18n/print.js";

/** The QR code that joins this network — WPA with a passphrase, open without — as
    an SVG path, or `null` with no SSID. */
export function joinSymbol(
  ssid: string | null | undefined,
  passphrase?: string | null,
): { d: string; extent: number } | null {
  const payload = wifiJoinPayload(ssid, passphrase);
  return payload ? qrPath(encodeQr(payload)) : null;
}

/** `Intl` locale for a slip's dates. `en-GB` for the reason `LOCALE` is. */
const SLIP_LOCALE: Record<SlipLanguage, string> = { en: "en-GB", vi: "vi-VN" };

/** How long, in the slip's language. `null` where the plan does not say. */
export function slipSpan(minutes: number | null | undefined, lang: SlipLanguage): string | null {
  if (!minutes || minutes <= 0) return null;
  const tt = slipT(lang);
  const d = Math.floor(minutes / 1440);
  const h = Math.floor((minutes % 1440) / 60);
  const m = minutes % 60;
  // A zero component is dropped, as `format.ts` drops it on screen.
  if (d > 0) return h > 0 ? `${tt("slip.days", d)} ${tt("slip.hours", h)}` : tt("slip.days", d);
  if (h > 0) return m > 0 ? `${tt("slip.hours", h)} ${tt("slip.minutes", m)}` : tt("slip.hours", h);
  return tt("slip.minutes", m);
}

/** The validity line, or `""` where the batch states none. Never a guessed length. */
export function slipValidity(batch: VoucherBatch | undefined, lang: SlipLanguage): string {
  const tt = slipT(lang);
  if (batch?.expiryMode === "from-first-use") {
    const span = slipSpan(batch.plan?.durationMinutes, lang);
    return span ? tt("slip.fromFirstUse", span) : tt("slip.fromFirstUseUnknown");
  }
  if (batch?.expiresAt) {
    const date = new Date(batch.expiresAt).toLocaleDateString(SLIP_LOCALE[lang], {
      day: "numeric",
      month: "short",
      year: "numeric",
    });
    return tt("slip.until", date);
  }
  return "";
}

export type PrintKit = typeof import("./kit.js");
