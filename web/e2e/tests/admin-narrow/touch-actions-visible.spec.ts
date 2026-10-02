import { expect, test, type Page } from "@playwright/test";
import { admin } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `touch-actions-visible` — the actions that did not exist on the device they
 *  are for
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Copy-this-value and filter-by-this-value sit at `opacity: 0` until `td:hover`. A
 * finger produces no hover, so on a phone they were not merely hard to find, they were
 * unreachable — and copying a MAC while standing in front of the access point it
 * belongs to is the single most common thing an operator does with an identifier, on
 * the device this console's stated use is held in.
 *
 * The rule they are restored under is **input capability, not width**
 * (see the responsive design): `@media (hover: none) and (pointer: coarse)`. Layout asks how wide;
 * this asks whether there is a hand or a mouse. A tablet at 880px with a keyboard case
 * is narrow and still hovers; a wall-mounted touchscreen at 1400px is wide and never
 * will. Putting these under the width query answers the second question with the
 * first one's evidence, and on a phone the two answers coincide, which is exactly why
 * that mistake survives review.
 *
 * ── WHICH KNOB MAKES THE QUERY MATCH ────────────────────────────────────────
 *
 * **`hasTouch: true`, and only that.** Measured across four context configurations
 * against this Chromium, reading `matchMedia` inside the page:
 *
 *   viewport 390 alone                → hover:none false, pointer:coarse false
 *   viewport 390 + `isMobile: true`   → hover:none false, pointer:coarse false
 *   viewport 390 + `hasTouch: true`   → **both true**
 *   `devices["Pixel 7"]`              → both true (it sets `hasTouch`)
 *
 * `isMobile` on its own changes the viewport meta and the user agent and does not
 * touch the pointer model at all, so a test that reached for it — the more obvious
 * name — would have asserted the *fine* pointer rendering while believing it was
 * asserting the coarse one, and passed against a console with none of this in it.
 * That is why the query is asserted as a precondition below rather than assumed.
 *
 * Playwright's own guidance is `hasTouch`/`isMobile` as context options; the fine
 * half of this file is deliberately the same page with neither, so the two describe
 * blocks differ in one variable.
 */

/** The first identifier cell with actions in the table body, at any width. */
const idActions = "tbody .id-cell .id-actions";

async function openSessions(page: Page): Promise<void> {
  await page.goto("/sessions");
  await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
  await expect(page.locator(idActions).first()).toHaveCount(1);
}

/** `opacity`, because `toBeVisible()` cannot see this failure — see below. */
async function opacityOf(page: Page, selector: string): Promise<string> {
  return page
    .locator(selector)
    .first()
    .evaluate((el) => getComputedStyle(el).opacity);
}

test.describe("a coarse pointer with no hover", () => {
  test.use({ hasTouch: true });

  test("the identifier actions are visible and hittable without hovering", async ({ page }) => {
    await openSessions(page);

    /* The precondition, asserted rather than assumed. If this goes false the rest of
       the file is measuring the desk browser and would pass for the wrong reason. */
    expect(
      await page.evaluate(() => matchMedia("(hover: none) and (pointer: coarse)").matches),
      "`hasTouch: true` no longer makes `(hover: none) and (pointer: coarse)` match in " +
        "this Chromium, so every assertion below is being made against the fine-pointer " +
        "rendering. Re-measure the context options before changing anything in the " +
        "console — `isMobile` alone does not do it.",
    ).toBe(true);

    /*
     * **`toBeVisible()` would pass here without the fix.** Playwright's visibility is
     * a non-empty bounding box and a computed `visibility`; `opacity: 0` is visible to
     * it and invisible to a person. This is the same shape as the portal's
     * `navigation-not-xhr` problem — the obvious assertion passes identically for the
     * build that is broken — so the assertion is the computed value.
     */
    expect(
      await opacityOf(page, idActions),
      "The identifier actions are still transparent under a coarse pointer, so copying " +
        "a MAC is impossible on the device the console is used from. The rule is in the " +
        "`(hover: none) and (pointer: coarse)` block in admin.css, and it is deliberately " +
        "not in the width block.",
    ).toBe("1");

    /*
     * And nothing is on top of them. A control that is painted but covered is the
     * same failure one layer along, and the tap lands on the row instead.
     *
     * ── ONE ROUND TRIP, AND THAT IS A COST DECISION ─────────────────────────
     *
     * This was four calls per button — `toBeVisible`, `scrollIntoViewIfNeeded`,
     * `boundingBox`, `elementFromPoint`. `/sessions` renders whatever the mock's world
     * holds, the screen fetches `limit: 100`, and a full page is two hundred buttons
     * is eight hundred round trips. Measured at 64ms each, this test sat against the
     * 30s cap and went over it under a full project run — a red that says nothing
     * about the console and everything about how many rows the mock was holding.
     *
     * **Nothing is dropped.** Every button is still measured, and `painted` below is
     * stricter than the `toBeVisible()` it replaces: it adds computed `opacity`, which
     * is the one thing this file exists to check and the one thing Playwright's
     * visibility does not look at.
     */
    const hits = await page.evaluate((selector) => {
      return [...document.querySelectorAll<HTMLElement>(`${selector} button`)].map((el) => {
        /* Scroll then measure, per button: the MAC column's actions start life off the
           right-hand edge of a scroll port narrower than the table, and
           `elementFromPoint` takes *viewport* coordinates — it returns null for them
           otherwise. That null is the harness measuring nothing, not the console
           occluding anything, and asserting on it would pin a scroll position rather
           than a hit test. Scrolling is what an operator does; hovering is what they
           cannot. */
        el.scrollIntoView({ block: "nearest", inline: "nearest" });
        const box = el.getBoundingClientRect();
        const style = getComputedStyle(el);
        const at = document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2);
        return {
          row: el.closest("tr")?.getAttribute("data-row-key") ?? "(no row)",
          width: Math.round(box.width),
          height: Math.round(box.height),
          painted:
            box.width > 0 &&
            box.height > 0 &&
            style.visibility !== "hidden" &&
            style.opacity !== "0",
          tag: at?.tagName.toLowerCase() ?? "none",
          isTheButton: Boolean(at?.closest("button")),
          inIdActions: Boolean(at?.closest(".id-actions")),
        };
      });
    }, idActions);

    expect(hits.length, "the identifier cell offers no actions at all").toBeGreaterThan(0);

    for (const hit of hits) {
      expect(
        hit.painted,
        `An identifier action on ${hit.row} is not painted: ${hit.width}×${hit.height}px, ` +
          "or hidden, or transparent. The rule is in the `(hover: none) and " +
          "(pointer: coarse)` block in admin.css.",
      ).toBe(true);

      /* WCAG 2.2 §2.5.8 Target Size (Minimum). On every real screen these are
         transparent until revealed and axe skips them, so this is where the size is
         held. */
      expect(
        Math.min(hit.width, hit.height),
        `An identifier action on ${hit.row} is ${hit.width}×${hit.height}px — under ` +
          "WCAG 2.2 §2.5.8's 24px floor, and this is the pointer that has to hit it.",
      ).toBeGreaterThanOrEqual(24);

      expect(
        hit.isTheButton && hit.inIdActions,
        `A tap at the centre of an identifier action on ${hit.row} lands on ` +
          `<${hit.tag}> instead. The control is painted and something else is taking ` +
          "the press.",
      ).toBe(true);
    }
  });

  test("the keyboard hints are gone, and Compact rows is not", async ({ page }) => {
    await openSessions(page);
    await page.locator(".menu-button").click();
    await expect(page.locator('.sidebar-panel[data-open="true"]')).toBeVisible();

    /*
     * Two chips advertising chords that cannot be typed, removed rather than disabled —
     * the same rule capability gating follows, applied to input rather than to hardware.
     */
    await expect(
      page.locator(".kbd-hint-button:visible"),
      "The ⌘K and ⌘/ hints are still on screen under a coarse pointer. They advertise " +
        "a keyboard the device does not have.",
    ).toHaveCount(0);

    /*
     * And the discrimination that makes that assertion worth having: `Compact rows` is
     * a real control, not a keyboard hint, and hiding the two must not have taken it.
     * The drawer's footer holds all three; a block that emptied it would satisfy the
     * assertion above and be a worse product.
     */
    const footControls = page.locator(".sidebar-foot button:visible");
    expect(
      await footControls.count(),
      "The drawer's footer has no visible controls left. `Compact rows` lives there and " +
        "is a real control — it moves into the drawer, it does not disappear.",
    ).toBeGreaterThan(0);
    await expect(footControls.first()).not.toHaveClass(/kbd-hint/);
  });

  test("the row disclosure is sized for a thumb, not for the WCAG floor", async ({ page }) => {
    await openSessions(page);
    const box = (await page.locator('[data-role="row-disclose"]').first().boundingBox())!;

    /* 28×28 is the base size and clears §2.5.8's 24px with four to spare. Under this
       query it is `36 × var(--row-h)` — 36×44 comfortable, 36×32 compact. Asserting
       *strictly greater* than the base is what proves the coarse block reached the
       disclosure at all, rather than the base size being enough on its own. */
    expect(
      Math.round(box.width),
      `The disclosure is ${Math.round(box.width)}px wide under a coarse pointer, the ` +
        "same as its base size. The responsive design records that 24px is a floor for not being a " +
        "violation rather than a size for a thumb reaching across a phone held in one " +
        "hand; the enlargement is in the `(hover: none)` block.",
    ).toBeGreaterThan(28);
    expect(Math.round(box.height)).toBeGreaterThanOrEqual(24);
  });
});

/**
 * The fine-pointer half, and the reason the coarse half is not vacuous.
 *
 * A console that simply rendered `.id-actions` at `opacity: 1` everywhere would pass
 * every assertion above while having thrown away the reveal-on-hover design the desk
 * layout is built on. So the same page, same viewport, one context option different,
 * has to behave the other way.
 */
test.describe("a fine pointer, for contrast", () => {
  test("the same actions stay hidden until the cell is hovered", async ({ page }) => {
    await openSessions(page);

    expect(
      await page.evaluate(() => matchMedia("(hover: none) and (pointer: coarse)").matches),
      "This context matches the coarse-pointer query without asking for touch, so it " +
        "is not the contrast case it is meant to be.",
    ).toBe(false);

    expect(
      await opacityOf(page, idActions),
      "The identifier actions are permanently visible under a fine pointer too, so the " +
        "coarse-pointer assertions prove nothing — they would pass against a console " +
        "with no media query in it at all.",
    ).toBe("0");

    /* The cell, because the rule is `td:hover .id-actions` — hovering the transparent
       control itself would work today and would fail obscurely the day it becomes
       `display: none` rather than saying which rule stopped applying. */
    await page.locator("tbody .id-cell").first().hover();
    expect(
      await opacityOf(page, idActions),
      "Hovering the cell does not reveal the identifier actions, so they are now " +
        "unreachable with a mouse as well.",
    ).toBe("1");
  });
});
