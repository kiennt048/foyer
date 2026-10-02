/**
 * The voucher print sheet: one slip per printable code, in the venue's language,
 * with the network name and a QR code that joins it.
 *
 * ── two halves, and why ──────────────────────────────────────────────────────
 *
 * This file is the React half and travels with the batch screen that renders it
 * (the `/vouchers` chunk). The QR encoder, both languages of guest copy, the operator copy and
 * the sheet's stylesheet are `kit.ts`, loaded lazily the moment a batch page opens —
 * in parallel with the count of printable codes, so the sheet is in the document by
 * the time Print is enabled. `kit.ts` says why the split is on this line and not on
 * the component boundary (measured: a lazy *component* grew the entry by 2.9 kB).
 *
 * ── what a slip carries, and where each part comes from ──────────────────────
 *
 * | Part | Source | Absent when |
 * |---|---|---|
 * | Venue | `PortalSettings.venueName`, else the site's name | never |
 * | QR code | `WIFI:` join string for `networkName` (`wifi.ts`) | no network name, or one longer than an SSID's 32 bytes |
 * | Network name | `PortalSettings.networkName` — "the SSID as the guest sees it printed" | no network name |
 * | Wi-Fi password | `getVoucherSlipWifi` — the one read that returns it; also in the QR | an open network |
 * | Code | the voucher, grouped `XXXX-XXXX-XXXX` | never |
 * | Plan, validity | the batch's plan and expiry mode, in the slip language | plan/expiry unknown |
 *
 * **Not the batch label.** The create dialog calls it staff-only ("Daily print —
 * counter") and it is the page heading. The sheet is not given the label at all, so
 * it cannot print one by mistake.
 *
 * **Language is the venue's**, from `PortalSettings.languages`: the first one it
 * advertises, with a choice beside Print when it advertises more than one. Never the
 * console's — the console speaks its operator's language, and the person reading the
 * slip is a guest.
 */
import { withCopy } from "../i18n/catalogue.js";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import type { Language, Voucher, VoucherBatch } from "@foyer/contract";
import { groupVoucherCode } from "@foyer/contract";
import { api } from "../api.js";
import { t } from "../i18n/vouchers.js";
import { Notice, PlanName } from "../ui/primitives.js";
import type { PrintKit, SlipLanguage } from "./kit.js";

/** A new contract language must come with slip copy, or this does not compile. */
type Covered<L extends SlipLanguage> = L;
export type EveryLanguageHasSlipCopy = Covered<Language>;

export interface SheetState {
  ready: boolean;
  /** Why Print is not ready, in the operator's words. Null when it is. */
  reason: string | null;
}

export interface VoucherSlipsProps {
  siteId: string;
  /** Used only where the venue has no guest-facing name of its own. */
  siteName: string | undefined;
  batch: VoucherBatch | undefined;
  slips: Voucher[];
  onState: (state: SheetState) => void;
  /** Tests hand the kit in; the console loads it. */
  kit?: PrintKit;
}

const loadKit = () =>
  withCopy(() =>
    import("./kit.js").then(async (kit) => {
      await kit.printCopyReady;
      return kit;
    }),
  );

export function VoucherSlips({ siteId, siteName, batch, slips, onState, kit: given }: VoucherSlipsProps) {
  const loaded = useQuery({
    queryKey: ["print-kit"],
    queryFn: loadKit,
    enabled: !given,
    staleTime: Infinity,
    gcTime: Infinity,
  });
  // Same key as the portal settings screen, so an edit there is seen here.
  const settings = useQuery({
    queryKey: ["portal-settings", siteId],
    queryFn: () => api.getPortalSettings(siteId),
  });
  /* The guest network's password, when the venue set one: the QR joins a WPA network
     with it and the slip prints it. Its own read, because the settings read is
     write-only (`getGuestWifiKey`). */
  const wifi = useQuery({
    queryKey: ["slip-wifi", siteId],
    queryFn: () => api.voucherSlipWifi(siteId),
  });
  const kit = given ?? loaded.data;

  const offered: SlipLanguage[] = settings.data?.languages ?? [];
  const [chosen, setChosen] = useState<SlipLanguage | null>(null);
  const lang: SlipLanguage = chosen && offered.includes(chosen) ? chosen : (offered[0] ?? "en");

  const ssid = settings.data?.networkName ?? null;
  const psk = wifi.data?.passphrase ?? null;
  const qr = useMemo(() => (kit ? kit.joinSymbol(ssid, psk) : null), [kit, ssid, psk]);

  /* Not ready without the password read either: a slip printed while it failed would
     carry an open-network QR for a network that is not open. */
  const ready = Boolean(kit) && settings.isSuccess && wifi.isSuccess;
  const reason = ready
    ? null
    : loaded.isError
      ? t("vouchers.batch.printSheetFailed")
      : !kit
        ? t("vouchers.batch.printPreparing")
        : settings.isError || wifi.isError
          ? kit.printText("print.settings.failed")
          : kit.printText("print.settings.loading");
  useEffect(() => onState({ ready, reason }), [ready, reason, onState]);

  if (!kit || !ready) return null;

  const venue = settings.data.venueName.trim() || siteName || "";
  const tt = kit.slipT(lang);
  const validity = kit.slipValidity(batch, lang);
  const slip = (v: Voucher) => (
    <div className="voucher-cut" key={v.id}>
      <div className="voucher-venue">{venue}</div>
      {qr && ssid ? (
        <figure className="voucher-qr">
          <svg
            viewBox={`0 0 ${qr.extent} ${qr.extent}`}
            role="img"
            aria-label={tt("slip.qrLabel", ssid)}
            shapeRendering="crispEdges"
          >
            <rect width={qr.extent} height={qr.extent} className="voucher-qr-paper" />
            <path d={qr.d} className="voucher-qr-ink" />
          </svg>
          <figcaption>{tt("slip.scan")}</figcaption>
        </figure>
      ) : null}
      {ssid ? (
        <div className="voucher-network">
          <span className="voucher-label">{tt("slip.network")}</span>
          <span className="voucher-ssid">{ssid}</span>
          {psk ? (
            <>
              <span className="voucher-label">{tt("slip.password")}</span>
              <span className="voucher-ssid">{psk}</span>
            </>
          ) : null}
        </div>
      ) : null}
      <div className="voucher-label">{tt("slip.code")}</div>
      <div className="voucher-code">{groupVoucherCode(v.code)}</div>
      <div className="voucher-steps">{tt("slip.steps")}</div>
      <div className="voucher-terms">
        <PlanName plan={batch?.plan} />
        {batch?.plan?.name && validity ? <br /> : null}
        {validity}
      </div>
    </div>
  );

  return (
    <>
      <div className="no-print slip-controls">
        {offered.length > 1 ? (
          <div className="chip-group" role="group" aria-label={kit.printText("print.language.label")}>
            <span className="slip-controls-label" aria-hidden="true">
              {kit.printText("print.language.label")}
            </span>
            {offered.map((l) => (
              <button
                key={l}
                type="button"
                className="chip"
                lang={l}
                aria-pressed={l === lang}
                onClick={() => setChosen(l)}
              >
                {kit.slipLanguageName(l)}
              </button>
            ))}
          </div>
        ) : null}
        {!qr ? (
          <Notice level="caution-minor" title={kit.printText("print.qr.missing.title")}>
            {kit.printText(kit.ssidTooLong(ssid) ? "print.qr.tooLong.body" : "print.qr.missing.body")}
          </Notice>
        ) : null}
        {slips.length > 0 ? (
          <details className="slip-preview">
            <summary>{kit.printText("print.preview.title", slips.length)}</summary>
            <div className="voucher-sheet slip-preview-sheet" lang={lang}>
              {slip(slips[0]!)}
            </div>
          </details>
        ) : null}
      </div>

      {/* `slips`, not the table's rows: the batch, not the view. */}
      <div className="print-only">
        <div className="voucher-sheet" lang={lang}>
          {slips.map(slip)}
        </div>
      </div>
    </>
  );
}
