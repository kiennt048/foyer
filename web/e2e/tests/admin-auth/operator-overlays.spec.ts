import { expect, test, type Locator, type Page } from "@playwright/test";
import { ADMINISTRATOR, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `/operators`' two overlays — the seventh and eighth in the console
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `helpers/overlays.ts` is a list of six, driven by three locks — `dialog-focus-return`,
 * `dialogs-are-named` and `shortcuts-do-not-reach-through-a-modal`. It is the shape it
 * is because focus returning to `<body>` is one structural cause across every overlay,
 * not one bug per screen.
 *
 * `/operators` has two more and they are in none of the three. They are covered here
 * rather than by appending to that array, because they are not the same kind of thing
 * as the other six and each of the two carries a trap the shared list would flatten:
 *
 *  - the **create dialog** is a `Modal`, like four of the six;
 *  - the **delete confirmation** is a `ConfirmDialog` at the `type-name` tier, which
 *    **renders no cancel control at all** — deliberately. There is no symmetric pair of
 *    buttons to mis-click; Escape and the backdrop are the exits.
 *
 * ── TWO ASSERTIONS THAT ARE RED AGAINST CORRECT CODE ────────────────────────
 *
 * Both have an obvious way to write them, and the obvious way is wrong:
 *
 * **1. "Every Tab press stays inside the dialog."** Inside a native `:modal`
 * `<dialog>`, Chromium's tab cycle passes through `<body>` between wraps — the
 * platform's own sequential focus navigation lands on the document once per lap. The
 * dialog is trapped and correct, and this assertion fails. **The negative is what is
 * true and what matters**: no press may land on a control *outside* the dialog. That
 * is the property an operator has — they cannot tab into the inert page behind — and
 * it is the one a hand-written trap gets wrong.
 *
 * **2. "Every overlay has a reachable cancel."** Red on the typed-delete dialog by
 * design, per above. Scoped to `tier="confirm"`, which is a rule about consequences
 * rather than about copy: a dialog whose foot has a `.btn-danger` and no non-primary
 * sibling is the escalated tier, and its exit is Escape.
 */

interface OperatorOverlay {
  id: string;
  /** The control that opens it — and therefore where focus must come back to. */
  trigger: string;
  /** Present once it is genuinely open, rather than merely mounted. */
  ready: string;
  /** `type-name` renders no cancel button; `confirm` must. */
  tier: "confirm" | "type-name";
}

const OVERLAYS: readonly OperatorOverlay[] = [
  {
    id: "operators · create",
    trigger: "main .page-head .page-actions button",
    ready: "dialog[open] #operator-password",
    /* Not a `ConfirmDialog` at all — a `Modal` with a form in it. Its foot has a
       Cancel and a submit, so it is held to the `confirm` rule. */
    tier: "confirm",
  },
  {
    id: "operators · delete",
    /* Found rather than assumed: the last enabled administrator's controls are
       `disabled` by design, so this picks a row that has a live one. */
    trigger: "tbody tr:not(.empty-row) .row-actions button.btn-ghost:not([disabled])",
    ready: "dialog[open] input[type=text]",
    tier: "type-name",
  },
];

const CANCEL_CANDIDATES =
  "dialog[open] .dialog-foot button:not([type=submit]):not(.btn-primary):not(.btn-danger)";

test.afterEach(async ({ context }) => {
  await signOut(context);
});

/** Opens the overlay **from the keyboard**, and proves the trigger held focus first. */
async function open(
  page: Page,
  overlay: OperatorOverlay,
): Promise<{ trigger: Locator; dialog: Locator }> {
  await page.goto("/operators");
  await shellSettled(page);
  await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

  const trigger = page.locator(overlay.trigger).first();
  await expect(trigger, `${overlay.id}: nothing matches \`${overlay.trigger}\``).toBeVisible();
  await trigger.focus();
  await expect(
    trigger,
    `${overlay.id}: the trigger refused focus, so there is nothing for the overlay to ` +
      "hand focus back to and every assertion below would be measuring nothing.",
  ).toBeFocused();

  /* `Enter` rather than `click()`: a click moves focus too, so it would satisfy this
     whole file by accident. The operator these locks are about never uses a mouse. */
  await trigger.press("Enter");

  const dialog = page.locator("dialog[open]");
  await expect(dialog, `${overlay.id}: Enter on the trigger opened no dialog.`).toHaveCount(1);
  await expect(page.locator(overlay.ready)).toBeVisible();
  expect(
    await dialog.evaluate((el) => el.matches(":modal")),
    `${overlay.id}: the dialog is open but not modal — \`<dialog open>\` rather than ` +
      "`showModal()`. The page behind a non-modal dialog is not inert and the trap " +
      "asserted below is the platform's only because it is modal.",
  ).toBe(true);

  return { trigger, dialog };
}

/** Where focus is, in words a failure message can use. `<body>` is the answer. */
async function focusReport(page: Page): Promise<string> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    if (!el || el === document.body) return "<body>";
    const cls = typeof el.className === "string" && el.className ? `.${el.className}` : "";
    const inside = el.closest("dialog") ? " (inside a dialog)" : "";
    return `${el.tagName.toLowerCase()}${cls}${inside}`;
  });
}

for (const overlay of OVERLAYS) {
  test.describe(overlay.id, () => {
    test("has an accessible name", async ({ page, context }) => {
      await signInAs(context, ADMINISTRATOR);
      const { dialog } = await open(page, overlay);

      const name = await dialog.evaluate((el) => {
        const labelledBy = el.getAttribute("aria-labelledby");
        if (labelledBy) {
          return labelledBy
            .split(/\s+/)
            .map((id) => document.getElementById(id)?.textContent?.trim() ?? "")
            .join(" ")
            .trim();
        }
        return el.getAttribute("aria-label")?.trim() ?? "";
      });

      expect(
        name,
        `${overlay.id} resolves no accessible name, so it is announced as "dialog" and ` +
          "nothing else. Five of the console's dialogs had none; `dialogs-are-named` " +
          "holds the other six. Both of these are questions about a specific " +
          "operator, and which operator is the entire content of the name.",
      ).not.toBe("");
      await dialog.press("Escape");
    });

    /**
     * The trap — asserted as **the negative**, which is the version that is true.
     *
     * Chromium's tab cycle inside a `:modal` dialog passes through `<body>` between
     * wraps. A test demanding that every press land inside the dialog is red against a
     * perfectly trapped one. What must never happen is a press landing on a control
     * *outside* it, which is what a hand-written scrim gets wrong and what leaves an
     * operator typing into a page they cannot see.
     *
     * Both directions, because a trap can hold forwards and leak backwards — the
     * command palette did exactly that: `Tab` walked out of it and one `Shift+Tab`
     * left entirely.
     */
    test("Tab never reaches a control outside it, in either direction", async ({
      page,
      context,
    }) => {
      await signInAs(context, ADMINISTRATOR);
      const { dialog } = await open(page, overlay);

      const escapees: string[] = [];
      const laps = 14;
      for (const key of ["Tab", "Shift+Tab"] as const) {
        for (let i = 0; i < laps; i++) {
          await page.keyboard.press(key);
          const outside = await page.evaluate(() => {
            const el = document.activeElement as HTMLElement | null;
            /* `<body>` is the platform's own waypoint between wraps and is not an
               escape: it is not a control and nothing can be typed into it. */
            if (!el || el === document.body) return null;
            if (el.closest("dialog[open]")) return null;
            const cls = typeof el.className === "string" && el.className ? `.${el.className}` : "";
            return `${key} #${1} → ${el.tagName.toLowerCase()}${cls}`;
          });
          if (outside) escapees.push(outside.replace("#1", `#${i + 1}`));
        }
      }

      expect(
        escapees,
        `${overlay.id}: focus reached a control outside the modal.\n` +
          "  The page behind a `:modal` dialog is inert, so this is either a dialog " +
          "that is not modal or a hand-written overlay pretending to be one. Note " +
          "what is **not** asserted here: that every press lands inside. Chromium's " +
          "tab cycle passes through `<body>` between wraps, so that version is red " +
          "against a correctly trapped dialog — the negative is the true statement.",
      ).toEqual([]);

      await dialog.press("Escape");
    });

    test("Escape closes it and hands focus back to the control that opened it", async ({
      page,
      context,
    }) => {
      await signInAs(context, ADMINISTRATOR);
      const { trigger, dialog } = await open(page, overlay);

      await dialog.press("Escape");
      await expect(page.locator("dialog[open]"), `${overlay.id}: Escape did not close it.`)
        .toHaveCount(0);

      await expect(
        trigger,
        `${overlay.id}: after Escape, focus was on ${await focusReport(page)}.\n` +
          "  Without an explicit return, a dismissed overlay leaves focus on `<body>`, " +
          "and the two on this screen are in none of `dialog-focus-return`'s lists. A " +
          "keyboard operator who dismisses a dialog and is put nowhere has lost their " +
          "place in a 21-row table.",
      ).toBeFocused();
    });
  });
}

/**
 * The cancel rule, **scoped to the tier it applies to**.
 *
 * Not *"every overlay has a reachable cancel"*, which is red on the typed-delete
 * dialog, because that dialog renders none by design: there is no symmetric pair of buttons to
 * mis-click on the one action with no recovery path, and Escape and the backdrop are
 * the exits. That is a decision in `ConfirmDialog`, not an omission — so the rule is
 * scoped rather than softened, and the `type-name` half is asserted as the *absence*
 * so the decision cannot be reversed silently either.
 *
 * "Cancel" is defined by consequence rather than by copy: a foot button that is neither
 * the affirmative action (`[type=submit]`, `.btn-primary`) nor a destructive one
 * (`.btn-danger`). `README.md`'s rule is that nothing in this suite reads a word of the
 * console's prose.
 */
test.describe("the cancel control, per tier", () => {
  for (const overlay of OVERLAYS) {
    test(`${overlay.id} (${overlay.tier})`, async ({ page, context }) => {
      await signInAs(context, ADMINISTRATOR);
      const { trigger, dialog } = await open(page, overlay);
      const candidates = page.locator(CANCEL_CANDIDATES);

      if (overlay.tier === "type-name") {
        await expect(
          candidates,
          `${overlay.id} grew a cancel control. The \`type-name\` tier renders none on ` +
            "purpose: it is reserved for what has no recovery path, and a symmetric " +
            "pair of buttons on that question is a mis-click waiting to happen. If " +
            "this is now wanted, it is a product decision in `ConfirmDialog` — change " +
            "the tier's contract, not this assertion.",
        ).toHaveCount(0);
        await dialog.press("Escape");
      } else {
        await expect(
          candidates,
          `${overlay.id} offers no way out but Escape, and it is a \`confirm\`-tier ` +
            "overlay, which must.",
        ).toHaveCount(1);
        await candidates.first().click();
        await expect(page.locator("dialog[open]")).toHaveCount(0);
      }

      /* Either exit returns focus. The cancel path is a separate arm from Escape, and
         `confirm-dialog-focus-return` stays asymmetric for exactly this reason: a
         focus defect lives in the call site, not the primitive. */
      await expect(trigger, `${overlay.id}: focus was on ${await focusReport(page)}.`).toBeFocused();
    });
  }
});
