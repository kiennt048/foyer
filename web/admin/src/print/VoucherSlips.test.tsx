import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PortalSettings, VoucherBatch } from "@foyer/contract";
import { buildWorld } from "@foyer/fixtures";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SLIP_COPY } from "../i18n/print.js";
import { decodeGrid } from "./qrdecode.js";
import { gridFromPath } from "./qrsvg.js";
import * as kit from "./kit.js";
import { slipSpan, slipValidity } from "./kit.js";
import { VoucherSlips } from "./VoucherSlips.js";

/**
 * Without these, a hotel whose portal speaks only Vietnamese prints slips reading
 * "Valid for 1 day from first use", with no network name and no QR code. These pin
 * the three halves of that at the component, where they are cheap; `web/e2e/tests/admin/voucher-print.spec.ts`
 * pins them in the running console, where they are true.
 */

const world = buildWorld();
const hanoiBatch = world.batches.find((b) => b.id === "batch-hanoi-rooms")!;
const hanoiSlips = world.vouchers.filter((v) => v.batchId === hanoiBatch.id).slice(0, 3);
const settingsFor = (siteId: string) => world.portalSettings.find((s) => s.siteId === siteId)!;

function render(settings: PortalSettings, batch: VoucherBatch = hanoiBatch, passphrase: string | null = null) {
  const qc = new QueryClient();
  qc.setQueryData(["portal-settings", settings.siteId], settings);
  qc.setQueryData(["slip-wifi", settings.siteId], { passphrase });
  return renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <VoucherSlips
        siteId={settings.siteId}
        siteName="Site name that must not print when the venue has one"
        batch={batch}
        slips={hanoiSlips}
        onState={() => {}}
        kit={kit}
      />
    </QueryClientProvider>,
  );
}

/** The print sheet only — the on-screen preview is a copy of one slip. */
const sheetOf = (html: string) => html.slice(html.indexOf('<div class="print-only">'));

describe("the voucher slip", () => {
  it("prints a Vietnamese-only venue's slips in Vietnamese", () => {
    const hanoi = settingsFor("site-hanoi");
    expect(hanoi.languages).toEqual(["vi"]);
    const sheet = sheetOf(render(hanoi));
    expect(sheet).toContain('lang="vi"');
    expect(sheet).toContain(SLIP_COPY.vi["slip.code"]);
    expect(sheet).toContain(SLIP_COPY.vi["slip.steps"]);
    expect(sheet).toContain("Dùng được 1 ngày kể từ lần dùng đầu tiên");
    expect(sheet).not.toContain("Valid for");
    expect(sheet).not.toContain(SLIP_COPY.en["slip.code"]);
  });

  it("prints the network name and a QR code that joins it, on every slip", () => {
    const sheet = sheetOf(render(settingsFor("site-hanoi")));
    const paths = [...sheet.matchAll(/<svg viewBox="0 0 (\d+) \1"[^>]*>.*?<path d="([^"]+)"/g)];
    expect(paths).toHaveLength(hanoiSlips.length);
    for (const [, extent, d] of paths) {
      const grid = gridFromPath(d!, Number(extent));
      const { text } = decodeGrid(grid.size, (r, c) => grid.dark[r * grid.size + c]!);
      expect(text).toBe("WIFI:S:HoGuom-Guest;T:nopass;;");
    }
    expect(sheet.match(/class="voucher-ssid">HoGuom-Guest</g)).toHaveLength(hanoiSlips.length);
  });

  /*
   * Every operator setting is set in the console: a venue whose guest network
   * has a password set in the console prints slips whose QR joins it as WPA, with the
   * password beside the network name. Before, the QR was always `T:nopass`, and no
   * phone could use it on such a network.
   */
  it("joins a password-protected network with the password the venue set", () => {
    const sheet = sheetOf(render(settingsFor("site-hanoi"), hanoiBatch, "Pho;Bo:2026"));
    const paths = [...sheet.matchAll(/<svg viewBox="0 0 (\d+) \1"[^>]*>.*?<path d="([^"]+)"/g)];
    expect(paths).toHaveLength(hanoiSlips.length);
    for (const [, extent, d] of paths) {
      const grid = gridFromPath(d!, Number(extent));
      const { text } = decodeGrid(grid.size, (r, c) => grid.dark[r * grid.size + c]!);
      expect(text).toBe("WIFI:S:HoGuom-Guest;T:WPA;P:Pho\\;Bo\\:2026;;");
    }
    expect(sheet.match(/class="voucher-ssid">Pho;Bo:2026</g)).toHaveLength(hanoiSlips.length);
    expect(sheet).toContain(SLIP_COPY.vi["slip.password"]);
    // And an open network's slip says nothing about a password.
    expect(sheetOf(render(settingsFor("site-hanoi")))).not.toContain(SLIP_COPY.vi["slip.password"]);
  });

  it("never prints the staff-only batch label", () => {
    const html = render(settingsFor("site-hanoi"));
    expect(hanoiBatch.label).toBe("Phòng 401–460 — tháng 8");
    expect(html).not.toContain(hanoiBatch.label);
  });

  it("uses the venue's guest-facing name, not the site's internal one", () => {
    const sheet = sheetOf(render(settingsFor("site-hanoi")));
    expect(sheet).toContain("Khách sạn Hồ Gươm");
    expect(sheet).not.toContain("Site name that must not print");
  });

  it("offers the venue's languages and nothing else, defaulting to the first", () => {
    const hcm = settingsFor("site-hcm");
    expect(hcm.languages.length).toBeGreaterThan(1);
    const html = render(hcm);
    const chips = [...html.matchAll(/<button type="button" class="chip" lang="(\w+)" aria-pressed="(\w+)"/g)];
    expect(chips.map((c) => c[1])).toEqual(hcm.languages);
    expect(chips.map((c) => c[2])).toEqual(hcm.languages.map((_, i) => String(i === 0)));
    expect(sheetOf(html)).toContain(`lang="${hcm.languages[0]}"`);

    // One language is no choice, and a control for a choice that does not exist lies.
    expect(render(settingsFor("site-hanoi"))).not.toContain('class="chip"');
  });

  it("says so, on screen, when there is no network name to put in a QR code", () => {
    const lab = settingsFor("site-lab");
    expect(lab.networkName).toBeNull();
    const html = render(lab);
    expect(html).not.toContain("voucher-qr");
    expect(html).toContain("These slips print without a Wi-Fi QR code");
    // The notice is for the operator, never for the paper.
    expect(sheetOf(html)).not.toContain("without a Wi-Fi QR code");
  });

  /*
   * `networkName` has no length limit in the contract,
   * the console or the store, and the encoder throws past version 10. A venue that
   * typed a descriptive name ("Khách sạn Hồ Gươm — …", ~200 bytes of UTF-8) took the
   * whole batch page down with an uncaught RangeError, so no slip could be printed
   * at all. An SSID is at most 32 octets (IEEE 802.11), so a longer name is not
   * something a phone could join: it prints, without a QR, and the operator is told.
   */
  it("prints a network name longer than an SSID can be without a QR, and says why", () => {
    const long = "Khách sạn Hồ Gươm — Wi-Fi miễn phí cho khách lưu trú, nhà hàng, hồ bơi và sảnh chính";
    expect(new TextEncoder().encode(long).length).toBeGreaterThan(32);
    const html = render({ ...settingsFor("site-hanoi"), networkName: long });
    const sheet = sheetOf(html);
    expect(sheet).not.toContain("voucher-qr");
    expect(sheet.match(/class="voucher-ssid">/g)).toHaveLength(hanoiSlips.length);
    expect(html).toContain("longer than a Wi-Fi network name can be");
    expect(html).not.toContain("This venue has no network name set");
    expect(sheet).not.toContain("longer than a Wi-Fi network name can be");
  });

  it("does not take the batch page down when the network name is longer than a QR code holds", () => {
    // 250-odd bytes of UTF-8: past version 10, where the encoder throws by design.
    const huge = "Khách sạn Hồ Gươm, Hoàn Kiếm, Hà Nội — ".repeat(5);
    expect(new TextEncoder().encode(huge).length).toBeGreaterThan(213);
    const html = render({ ...settingsFor("site-hanoi"), networkName: huge });
    expect(sheetOf(html)).not.toContain("voucher-qr");
    expect(sheetOf(html).match(/class="voucher-code">/g)).toHaveLength(hanoiSlips.length);
  });

  it("still carries a QR for an SSID of exactly 32 bytes", () => {
    const ssid = "Quán Phở Hà Nội — Khách";
    expect(new TextEncoder().encode(ssid).length).toBe(32);
    const sheet = sheetOf(render({ ...settingsFor("site-hanoi"), networkName: ssid }));
    expect(sheet.match(/class="voucher-qr"/g)).toHaveLength(hanoiSlips.length);
  });

  it("falls back to the site's name where the venue name is blank", () => {
    const lab = settingsFor("site-lab");
    expect(lab.venueName).toBe("");
    expect(sheetOf(render(lab))).toContain("Site name that must not print");
  });
});

describe("slip durations and dates, in the slip's language", () => {
  it("drops zero components and never invents a length", () => {
    expect(slipSpan(90, "en")).toBe("1 hour 30 minutes");
    expect(slipSpan(90, "vi")).toBe("1 giờ 30 phút");
    expect(slipSpan(1440, "vi")).toBe("1 ngày");
    expect(slipSpan(1500, "en")).toBe("1 day 1 hour");
    expect(slipSpan(null, "en")).toBeNull();
    expect(slipSpan(0, "vi")).toBeNull();
  });

  it("states the clock without a length when the plan has none", () => {
    const noLength = { ...hanoiBatch, plan: { ...hanoiBatch.plan!, durationMinutes: null } };
    expect(slipValidity(noLength as VoucherBatch, "vi")).toBe("Tính thời gian từ lần dùng đầu tiên");
  });

  it("writes a fixed expiry date in the slip's locale", () => {
    const fixed = { ...hanoiBatch, expiryMode: "from-creation", expiresAt: "2026-09-01T12:00:00Z" };
    // ICU data differs between runtimes ("Sep" or "Sept", a comma or none), so the
    // shape is pinned rather than one runtime's spelling of it.
    expect(slipValidity(fixed as VoucherBatch, "vi")).toMatch(/^Dùng trước ngày 1 thg 9,? 2026$/);
    expect(slipValidity(fixed as VoucherBatch, "en")).toMatch(/^Use before 1 Sept? 2026$/);
  });

  it("has a Vietnamese sentence for every English one", () => {
    expect(Object.keys(SLIP_COPY.vi).sort()).toEqual(Object.keys(SLIP_COPY.en).sort());
  });
});
