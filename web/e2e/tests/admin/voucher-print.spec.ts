import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { decodeGrid } from "../../../admin/src/print/qrdecode.js";
import { gridFromPath } from "../../../admin/src/print/qrsvg.js";
import { admin, tableReady } from "../helpers/admin.js";
import { MOCK_URL } from "../helpers/env.js";

/**
 * Printing a voucher batch produces the batch, not the view.
 *
 * The design (`docs/architecture.md`) names this fixture for exactly this reason — "a
 * voucher batch of 500 — pagination and printing must survive it" — and it did not.
 * The print sheet mapped the *loaded, filtered* rows, so one screen carried three
 * different numbers: a header saying 500, a table holding the first 200 with nothing
 * saying so, and a printer producing 118. With the "In use" chip pressed it produced
 * none at all, with the button fully enabled and silent.
 *
 * It is worth a lock rather than a fix because the failure is silent by construction:
 * the print sheet is `display: none` until the print dialog opens, so nothing on
 * screen ever disagrees with itself. The operator finds out at the printer.
 *
 * The expected count is computed from the server, never typed in. The mock is
 * stateful and other specs redeem out of this batch, so a constant would pass once.
 */

const BATCH = "batch-event";

/** The slips that print. The on-screen preview repeats one of them outside it. */
const SHEET_SLIPS = ".print-only .voucher-sheet .voucher-cut";

/** Every unused voucher in the batch, counted the way the screen has to count it. */
async function unusedCount(request: APIRequestContext, batchId: string, siteId?: string): Promise<number> {
  let total = 0;
  let cursor: string | null = null;
  do {
    const url =
      `${MOCK_URL}/api/v0/vouchers?batchId=${batchId}&state=unused&limit=200` +
      (siteId ? `&siteId=${siteId}` : "") +
      (cursor ? `&cursor=${cursor}` : "");
    const res = await request.get(url);
    if (!res.ok()) throw new Error(`GET ${url} → ${res.status()}`);
    const page = (await res.json()) as { items: unknown[]; nextCursor: string | null };
    total += page.items.length;
    cursor = page.nextCursor;
  } while (cursor);
  return total;
}

test.describe("printing a voucher batch", () => {
  test("prints every unused voucher, states the count before the dialog, and ignores the filters", async ({
    page,
    context,
  }) => {
    const expected = await unusedCount(context.request, BATCH);
    const batch = (await (
      await context.request.get(`${MOCK_URL}/api/v0/voucher-batches/${BATCH}`)
    ).json()) as { count: number };

    /* Sanity: this is still the big batch, and it still exceeds one page. A batch
       that fitted on one page would make the whole test vacuous. */
    expect(
      batch.count,
      `${BATCH} is no longer the large batch the design requires. Printing and ` +
        "pagination have to be exercised against one.",
    ).toBeGreaterThan(200);
    expect(expected, "The batch has no unused vouchers left to print.").toBeGreaterThan(0);

    await page.goto(`/vouchers/${BATCH}`);
    await tableReady(page);

    const printButton = page.locator(".page-actions button");
    // The sheet, not the one-slip preview beside Print: `.print-only` is what prints.
    const slips = page.locator(SHEET_SLIPS);

    /*
     * The count is on the control, before anything opens. That placement is the
     * requirement, not a detail: once the print dialog is up the interface has
     * stopped talking to the operator and the next thing that happens is a walk to
     * the printer.
     */
    await expect(
      printButton,
      "The print control does not say how many slips it will produce.",
    ).toContainText(String(expected));
    await expect(printButton).toBeEnabled();

    await expect(
      slips,
      `The print sheet holds ${await slips.count()} slips for a batch with ${expected} ` +
        "unused vouchers. Printing must produce the batch, not the page that happens " +
        "to be loaded.",
    ).toHaveCount(expected);

    /* The table is one page of the batch, and it says so rather than looking whole. */
    const rows = await page.locator(admin.rows).count();
    expect(rows, "The table is no longer truncated, so it cannot be lying by omission.")
      .toBeLessThan(batch.count);
    await expect(
      page.locator(".toolbar-count"),
      "The table shows a fraction of the batch and the toolbar does not say so.",
    ).toContainText(String(rows));

    /*
     * Now the case that produced zero slips in silence. The filter is a way to find
     * one code, not a print selector, and reinterpreting it as one is how 382 slips
     * go missing between the click and the printer.
     */
    await page.goto(`/vouchers/${BATCH}?state=in-use`);
    await tableReady(page);

    await expect(
      slips,
      "A status filter changed what would come out of the printer.",
    ).toHaveCount(expected);
    await expect(printButton).toContainText(String(expected));

    /* And the divergence is stated standing, not left in a tooltip: the table and
       the printer now disagree, and the operator is told which one Print obeys. */
    await expect(
      page.locator(`${admin.notice}[data-level="info"]`).first(),
      "The filtered view prints something different from what it lists, and says nothing.",
    ).toBeVisible();
  });

  /**
   * A batch with nothing printable disables the control and says why.
   *
   * `batch-old` is entirely spent — every voucher exhausted or expired. Before, the
   * button was fully enabled and printing produced a blank sheet.
   */
  test("a fully-used batch disables Print with a stated reason", async ({ page, context }) => {
    const expected = await unusedCount(context.request, "batch-old");
    expect(expected, "batch-old is no longer the fully-spent fixture.").toBe(0);

    await page.goto("/vouchers/batch-old");
    await tableReady(page);

    const printButton = page.locator(".page-actions button");
    await expect(printButton, "Print is enabled on a batch with nothing to print.").toBeDisabled();
    await expect(
      printButton,
      "The disabled control carries no reason. A capability that is real but " +
        "currently unavailable is disabled *with* a reason, never bare.",
    ).toHaveAttribute("title", /.+/);
    await expect(page.locator(SHEET_SLIPS)).toHaveCount(0);
  });
});

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  What a slip says, and to whom
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The hotel at `site-hanoi` advertises Vietnamese only, so its slips must not read
 * "Valid for 1 day from first use", and each carries the network name and a QR code.
 * The batch label does **not** reach paper — `.page-head` is hidden in print — and
 * the test below keeps it that way.
 *
 * The QR assertion decodes what the page **drew**: the SVG path is read back into a
 * module grid and decoded by `qrdecode.ts`, which shares no code with the encoder.
 * Where the platform has a `BarcodeDetector` (Chromium on macOS — Apple's Vision
 * framework underneath), the rendered pixels are decoded by it as well. Neither is a
 * phone camera, and decoding with a real phone camera is UNVERIFIED until somebody
 * runs it.
 */
test.describe("the slip a guest is handed", () => {
  test.use({ deviceScaleFactor: 2 });

  const HANOI = "batch-hanoi-rooms";

  async function openBatch(page: Page, batch: string, site: string) {
    await page.goto(`/vouchers/${batch}?site=${site}`);
    await tableReady(page);
    await expect(page.locator(".page-actions button"), "Print never became ready.").toBeEnabled();
  }

  /** Every QR code on the sheet, decoded from the path the page rendered. */
  async function decodedSheet(page: Page): Promise<string[]> {
    const svgs = await page.locator(`${SHEET_SLIPS} .voucher-qr svg`).evaluateAll((els) =>
      els.map((svg) => ({
        extent: Number(svg.getAttribute("viewBox")!.split(" ")[2]),
        d: svg.querySelector("path")!.getAttribute("d")!,
      })),
    );
    return svgs.map(({ extent, d }) => {
      const grid = gridFromPath(d, extent);
      return decodeGrid(grid.size, (r, c) => grid.dark[r * grid.size + c]!).text;
    });
  }

  test("a Vietnamese venue's slips are Vietnamese, name the network, and carry a QR code that joins it", async ({
    page,
    context,
  }) => {
    const expected = await unusedCount(context.request, HANOI, "site-hanoi");
    expect(expected, `${HANOI} has nothing left to print.`).toBeGreaterThan(0);
    await openBatch(page, HANOI, "site-hanoi");

    const sheet = page.locator(".print-only .voucher-sheet");
    await expect(sheet, "The sheet is not in the venue's language.").toHaveAttribute("lang", "vi");
    await expect(page.locator(SHEET_SLIPS)).toHaveCount(expected);

    const decoded = await decodedSheet(page);
    expect(decoded, "Not every slip carries a QR code.").toHaveLength(expected);
    expect(new Set(decoded), "The QR code does not join the venue's network.").toEqual(
      new Set(["WIFI:S:HoGuom-Guest;T:nopass;;"]),
    );
    await expect(page.locator(`${SHEET_SLIPS} .voucher-ssid`)).toHaveText(
      Array.from({ length: expected }, () => "HoGuom-Guest"),
    );

    await page.emulateMedia({ media: "print" });
    const printed = await page.locator("body").innerText();
    // Vietnamese copy, and none of the English console copy.
    // `innerText` applies the labels' `text-transform`, so compare case-folded.
    expect(printed.toLowerCase()).toContain("mã phiếu");
    expect(printed).toContain("Dùng được 1 ngày kể từ lần dùng đầu tiên");
    expect(printed, "English slip copy reached a Vietnamese venue's paper.").not.toMatch(
      /Valid for|Voucher code|from first use/i,
    );

    // The staff-only label is not on paper, anywhere on the printed page.
    const res = await context.request.get(`${MOCK_URL}/api/v0/voucher-batches/${HANOI}`);
    const { label } = (await res.json()) as { label: string };
    expect(label.length).toBeGreaterThan(0);
    expect(printed, "The staff-only batch label is on the printed page.").not.toContain(label);

    // The pixels, where the platform can read them.
    const png = await page.locator(`${SHEET_SLIPS} .voucher-qr svg`).first().screenshot();
    const seen = await page.evaluate(async (bytes) => {
      type Detector = { detect(i: ImageBitmap): Promise<{ rawValue: string }[]> };
      const W = window as unknown as { BarcodeDetector?: new (o: object) => Detector };
      if (!W.BarcodeDetector) return null;
      const bmp = await createImageBitmap(new Blob([new Uint8Array(bytes)], { type: "image/png" }));
      const found = await new W.BarcodeDetector({ formats: ["qr_code"] }).detect(bmp);
      return found.map((f) => f.rawValue);
    }, [...png]);
    if (seen === null) {
      test.info().annotations.push({
        type: "unverified",
        description: "No BarcodeDetector on this platform; the rendered pixels were not decoded.",
      });
    } else {
      expect(seen, "The platform decoder could not read the printed QR code.").toEqual([
        "WIFI:S:HoGuom-Guest;T:nopass;;",
      ]);
    }

    // A screenshot of the printed slips, written only when asked for.
    const dir = process.env.FOYER_EVIDENCE_DIR;
    if (dir) {
      mkdirSync(dir, { recursive: true });
      // A4 width at 96 dpi, so the two-across layout is the one a printer produces.
      await page.setViewportSize({ width: 794, height: 1123 });
      const cuts = page.locator(SHEET_SLIPS);
      const a = (await cuts.nth(0).boundingBox())!;
      const b = (await cuts.nth(3).boundingBox())!;
      await page.screenshot({
        path: join(dir, "voucher-slip-vi.png"),
        clip: { x: a.x, y: a.y, width: b.x + b.width - a.x, height: b.y + b.height - a.y },
      });
    }
  });

  test("a bilingual venue prints in its first language, and the operator can choose the other", async ({
    page,
  }) => {
    await openBatch(page, "batch-daily", "site-hcm");
    const sheet = page.locator(".print-only .voucher-sheet");
    const chips = page.locator(".slip-controls .chip");

    // `site-hcm` advertises en, vi — in that order — so English first.
    await expect(chips).toHaveCount(2);
    await expect(sheet).toHaveAttribute("lang", "en");
    await expect(chips.first()).toHaveAttribute("aria-pressed", "true");

    await chips.filter({ hasText: "Tiếng Việt" }).click();
    await expect(sheet).toHaveAttribute("lang", "vi");
    await expect(sheet).toContainText("Kết nối vào mạng, rồi nhập mã này ở trang đăng nhập.", {
      useInnerText: false,
    });
    expect(new Set(await decodedSheet(page))).toEqual(new Set(["WIFI:S:Foyer Guest;T:nopass;;"]));
  });

  test("a single-language venue shows no language choice", async ({ page }) => {
    await openBatch(page, HANOI, "site-hanoi");
    await expect(page.locator(".slip-controls .chip")).toHaveCount(0);
  });
});
