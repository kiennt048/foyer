import { expect, test } from "@playwright/test";
import { admin } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `attempts-says-why` — the phone view of the screen an operator opens *because*
 *  a guest cannot get online
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `/attempts` keeps `outcome`, `what` and `mac` at 390. Keeping only `outcome` and
 * `mac` would give, against the fixtures, a phone view of:
 *
 *   21 rows · 13 distinct strings
 *   seven consecutive rows reading `Rejected / a4:3d:0d:da:72:bb`, byte-identical
 *   two more reading `Rejected / –`, which carry no information at all
 *
 * *Wrong password*, *Voucher expired*, *Too many attempts* and *The access point
 * refused the authorization* would each be one tap away — on the device an operator
 * is holding while standing under the access point.
 *
 * The attempts log exists to show the difference between a source that can never say
 * why and one that knows exactly why, so at phone width it must still say why. The
 * narrow layout's rule is that the column carrying the row's **identity** is kept; here
 * the MAC repeats and is blank on two rows, so it identifies nothing, and the reason
 * is what tells the rows apart.
 *
 * ── HOW THIS IS ASSERTED WITHOUT READING A WORD OF THE COPY ─────────────────
 *
 * The reason column is found by **structure**: it is the one whose cells render
 * `Stack` — a primary line with a source beneath it — which no other column on this
 * table does. Nothing here matches a label, a header or a sentence, so the lock
 * survives the copy being rewritten and fails when the column leaves.
 *
 * The second case is that argument as arithmetic rather than as a count somebody
 * typed: the rows an operator can tell apart **with** the reason column, against the
 * rows they could tell apart without it. Both numbers come from the same
 * render at the same width, so neither can be stale.
 */

interface NarrowRows {
  /** Every visible row, as the operator reads it. */
  full: string[];
  /** The same rows with the reason cell removed — the phone view without it, measured. */
  withoutReason: string[];
  /** Rows whose visible cells carry no reason at all. */
  missingReason: number;
}

test.describe("the phone view of /attempts says why", () => {
  test("the column that says what happened is on screen, and it adds information", async ({
    page,
  }) => {
    await page.goto("/attempts");
    await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
    await expect(page.locator(admin.rows).first()).toBeVisible();

    /* The narrow layout is a different column set, not a smaller one — assert we are
       actually in it, or everything below measures the desk view. */
    await expect(
      page.locator('[data-role="row-disclose"]').first(),
      "no per-row disclosure, so this is not the narrow layout",
    ).toBeVisible();

    const rows: NarrowRows = await page.evaluate(() => {
      const out = { full: [] as string[], withoutReason: [] as string[], missingReason: 0 };
      const trs = document.querySelectorAll<HTMLElement>("tbody tr:not(.empty-row):not(.row-detail)");
      for (const tr of trs) {
        const cells = [...tr.querySelectorAll<HTMLElement>("td")];
        /* `.stack` is the reason cell: a primary line and the source beneath it.
           The disclosure and checkbox cells render neither. */
        const reasonIndex = cells.findIndex((td) => td.querySelector(".stack"));
        const text = (td: HTMLElement) => (td.textContent ?? "").trim();
        out.full.push(cells.map(text).join(" / "));
        if (reasonIndex < 0) {
          out.missingReason++;
          out.withoutReason.push(cells.map(text).join(" / "));
        } else {
          out.withoutReason.push(cells.filter((_, i) => i !== reasonIndex).map(text).join(" / "));
        }
      }
      return out;
    });

    /* Vacuity: the argument above rests on 21 rows, and a table of three would pass
       everything below while measuring nothing. */
    expect(rows.full.length, "too few attempts on screen to measure anything").toBeGreaterThan(10);

    expect(
      rows.missingReason,
      `${rows.missingReason} of ${rows.full.length} rows show no reason at 390px. The ` +
        "column that says *what happened* is dropped again, which is the " +
        "phone view an operator gets while the guest is standing in front of them.",
    ).toBe(0);

    const distinct = (xs: string[]) => new Set(xs).size;
    expect(
      distinct(rows.full),
      `Keeping the reason column adds nothing: ${distinct(rows.full)} distinct rows with ` +
        `it and ${distinct(rows.withoutReason)} without. It was 16 against 13 when this ` +
        "was written, and the 13 included seven byte-identical `Rejected / <one MAC>` " +
        "rows.",
    ).toBeGreaterThan(distinct(rows.withoutReason));
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * `narrow: "keep"` cannot be reverted from the browser, so the reason column is
   * removed from the DOM instead — which is exactly what dropping it does — and the
   * case above must then be unsatisfiable. Without this, "16 is greater than 13"
   * would be a sentence about a table nobody has ever seen fail.
   */
  test("the lock is not vacuous: without the reason column the rows repeat", async ({ page }) => {
    await page.goto("/attempts");
    await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
    await expect(page.locator(admin.rows).first()).toBeVisible();

    const measured = await page.evaluate(() => {
      const trs = [...document.querySelectorAll<HTMLElement>("tbody tr:not(.empty-row):not(.row-detail)")];
      const strings = trs.map((tr) => {
        const cells = [...tr.querySelectorAll<HTMLElement>("td")];
        const reasonIndex = cells.findIndex((td) => td.querySelector(".stack"));
        return cells
          .filter((_, i) => i !== reasonIndex)
          .map((td) => (td.textContent ?? "").trim())
          .join(" / ");
      });
      const counts = new Map<string, number>();
      for (const s of strings) counts.set(s, (counts.get(s) ?? 0) + 1);
      return {
        rows: strings.length,
        distinct: counts.size,
        worstGroup: Math.max(...counts.values()),
      };
    });

    expect(
      measured.worstGroup,
      `With the reason column dropped, the largest group of byte-identical rows is ` +
        `${measured.worstGroup} of ${measured.rows}. It was seven when this was ` +
        "written, and a table whose rows are all distinct without the reason column " +
        "would make the case above green for a reason that has nothing to do with the " +
        "fix — the fixtures would have healed, and `frontend.md` says what that costs.",
    ).toBeGreaterThan(2);
    expect(measured.distinct).toBeLessThan(measured.rows);
  });
});
