import { expect, test, type Page } from "@playwright/test";
import { OVERLAYS } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `fields-are-styled` — the sixteen controls the console never actually styled
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `admin.css` selected `.field input[type="text"]`. **The JSX emits `<input>` with
 * no `type` attribute** — `text` is the default and nobody writes it — and an
 * attribute selector does not match a missing attribute. `textarea` was not in the
 * list at all. So sixteen controls rendered in the user agent's own styling, in a
 * console whose whole colour discipline is a token file.
 *
 * Measured unstyled, dark theme:
 *
 *   intended                          actual
 *   width: 100%                       154px in a dialog, 162px on /portal
 *   height: 34px                      26px
 *   border: 1px solid --field-border  2px inset rgb(118 118 118)
 *   border-radius: 5px                0
 *   background: --surface             #ffffff light, rgb(59 59 59) dark
 *
 * ── WHY THIS IS AN E2E TEST AND NOT A GATE ──────────────────────────────────
 *
 * **`tools/check-tokens.mjs` cannot see this and neither can axe.** The colour lint
 * fails a build on a hex outside `tokens.css`, and there is no hex here — the wrong
 * grey came from the browser. The accessibility sweep is green because the user
 * agent's border measures 4.54:1 light and 4.40:1 dark, *higher* than the token it
 * was supposed to be. The only thing that catches a control the stylesheet never
 * reached is a run that reads what the browser computed, which is this file.
 *
 * ── HOW THE EXPECTATIONS ARE DERIVED ────────────────────────────────────────
 *
 * Nothing here types a colour. Two probe elements are painted with `var(--surface)`
 * and `var(--field-border)` and the fields are compared against what those resolve
 * to, so the lock keeps working when a token moves and fails when a *field* stops
 * reading the token. The geometry is the same idea: a field's width is compared with
 * its own `.field` wrapper rather than with a number, which is what `width: 100%`
 * means.
 */

/** Every text control a `.field` can hold. `file` inputs are 1×1 and hidden. */
const TEXT_CONTROLS =
  '.field input:not([type="file"]):not([type="checkbox"]):not([type="radio"]), .field textarea';

interface FieldShape {
  id: string;
  tag: string;
  /** Its own width against the `.field` that wraps it — `width: 100%`, measured. */
  fillsWrapper: boolean;
  width: number;
  height: number;
  borderWidth: string;
  borderStyle: string;
  borderColor: string;
  radius: string;
  background: string;
}

interface Probe {
  surface: string;
  fieldBorder: string;
  radiusSm: string;
  fields: FieldShape[];
}

async function shapes(page: Page, scope: string): Promise<Probe> {
  return page.evaluate((sel) => {
    /* A dialog autofocuses its first field, and `:focus` deliberately repaints the
       border with `--accent`. That is the console working; measuring it would make
       this lock assert the focus ring. Focus is dropped for the measurement only. */
    (document.activeElement as HTMLElement | null)?.blur();

    /* What the tokens resolve to *here*, painted rather than parsed: a custom
       property's value is the unresolved `light-dark(...)` text, and the whole
       question is what the browser finally painted. */
    const paint = (declaration: string): string => {
      const el = document.createElement("div");
      el.setAttribute("style", declaration);
      document.body.appendChild(el);
      const cs = getComputedStyle(el);
      const value = declaration.startsWith("border-radius")
        ? cs.borderTopLeftRadius
        : declaration.startsWith("border")
          ? cs.borderTopColor
          : cs.backgroundColor;
      el.remove();
      return value;
    };

    const fields = [...document.querySelectorAll<HTMLElement>(sel)].map((el) => {
      const cs = getComputedStyle(el);
      const wrapper = el.closest<HTMLElement>(".field");
      return {
        id: el.id || el.getAttribute("name") || el.tagName.toLowerCase(),
        tag: el.tagName.toLowerCase(),
        fillsWrapper: Boolean(wrapper) && Math.abs(el.offsetWidth - wrapper!.clientWidth) <= 1,
        width: Math.round(el.getBoundingClientRect().width),
        height: Math.round(el.getBoundingClientRect().height),
        borderWidth: cs.borderTopWidth,
        borderStyle: cs.borderTopStyle,
        borderColor: cs.borderTopColor,
        radius: cs.borderTopLeftRadius,
        background: cs.backgroundColor,
      };
    });

    return {
      surface: paint("background: var(--surface)"),
      fieldBorder: paint("border: 1px solid var(--field-border)"),
      radiusSm: paint("border-radius: var(--radius-sm)"),
      fields,
    };
  }, scope);
}

function assertStyled(probe: Probe, where: string): void {
  expect(probe.fields.length, `${where} rendered no text field to measure`).toBeGreaterThan(0);

  for (const f of probe.fields) {
    const at = `${where} · ${f.id} (${f.tag}, ${f.width}×${f.height})`;

    expect(f.fillsWrapper, `${at} is not the width of its own .field — width: 100% did not apply`)
      .toBe(true);
    expect(f.borderWidth, `${at} has the user agent's border width`).toBe("1px");
    expect(f.borderStyle, `${at} has the user agent's border style (inset)`).toBe("solid");
    expect(f.borderColor, `${at} does not paint --field-border`).toBe(probe.fieldBorder);
    expect(f.radius, `${at} is a square-cornered browser default`).toBe(probe.radiusSm);
    expect(f.background, `${at} does not paint --surface`).toBe(probe.surface);

    /* A single-line control is 34px; a textarea's height is its rows and its
       min-height, and pinning it to 34 is what made the terms editor a 22-character
       window in the first place. */
    if (f.tag === "textarea") {
      expect(f.height, `${at} is shorter than the min-height for prose`).toBeGreaterThan(90);
    } else {
      expect(f.height, `${at} is not the console's 34px control height`).toBe(34);
    }
  }
}

test.describe("every text field is the console's, not the browser's", () => {
  /**
   * The worst case, and the reason it is named on its own: `/portal`'s terms editor
   * is where a venue's legal text is typed, and it measured **168 × 87px inside a
   * 720px card** — 76% of the card unused on the one field where the quality bar's
   * *generous where reading matters* matters most.
   */
  test("/portal, including both terms editors", async ({ page }) => {
    await page.goto("/portal?tab=terms");
    await expect(page.locator("textarea#terms-en")).toBeVisible();

    const probe = await shapes(page, TEXT_CONTROLS);
    assertStyled(probe, "/portal");

    const terms = probe.fields.filter((f) => f.tag === "textarea");
    expect(terms.length, "the terms editors are not on this screen any more").toBe(2);
    for (const t of terms) {
      expect(
        t.width,
        `The terms editor is ${t.width}px wide. Unstyled it is 168px of a 720px card; ` +
          "anything this narrow means `width: 100%` is not " +
          "reaching a `textarea` again.",
      ).toBeGreaterThan(400);
    }
  });

  for (const overlay of OVERLAYS) {
    test(`${overlay.id}`, async ({ page }) => {
      await page.goto(overlay.route);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
      await page.locator(overlay.trigger).first().click();
      await expect(page.locator("dialog[open]")).toBeVisible();

      const probe = await shapes(page, `dialog[open] ${TEXT_CONTROLS.split(", ").join(", dialog[open] ")}`);
      /* Not every overlay holds a text field — the probe result dialog is a report.
         Skipping those is honest; asserting zero fields are styled is not a check. */
      if (probe.fields.length === 0) {
        test.info().annotations.push({ type: "no text field", description: overlay.id });
        return;
      }
      assertStyled(probe, overlay.id);
    });
  }

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The fix is a selector, so the revert is one too: a rule at higher specificity
   * that hands these controls back to the user agent's box. Every assertion above
   * must be reachable, or the lock is measuring a browser that would have passed
   * either way.
   *
   * `border: 2px inset` and `padding: 1px 2px` are what Chromium actually renders
   * for an unstyled `input`, which is what the console shipped.
   */
  test("the lock is not vacuous: handing the fields back to the browser fails it", async ({
    page,
  }) => {
    await page.goto("/portal?tab=terms");
    await expect(page.locator("textarea#terms-en")).toBeVisible();
    await page.addStyleTag({
      content: `
        .field input, .field textarea {
          width: auto;
          height: auto;
          min-height: 0;
          padding: 1px 2px;
          border: 2px inset;
          border-radius: 0;
          background: Field;
        }`,
    });
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r(null))));

    const probe = await shapes(page, TEXT_CONTROLS);
    expect(() => assertStyled(probe, "/portal (reverted)")).toThrow();
  });
});
