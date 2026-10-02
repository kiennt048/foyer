import { expect, type Locator, type Page } from "@playwright/test";

/*
 * The console's overlays, as a list rather than as six copies of the same test.
 *
 * The console's overlays share one focus-and-keyboard contract, and it is a list
 * because focus falling to `<body>` is one structural cause, not one bug per screen.
 * Three locks drive this list —
 * `dialog-focus-return`, `dialogs-are-named` and
 * `shortcuts-do-not-reach-through-a-modal` — so a seventh overlay is covered by all
 * three the moment it is added here, and an overlay that is added to the console and
 * not to this list is the only way to lose that.
 *
 * **Nothing here is a count.** Every assertion that wants a number derives it from
 * this array, because the one number a reader would be tempted to type — "five
 * overlays can be closed by a control of their own" — is wrong for a reason that has
 * nothing to do with focus: `/controllers`'s probe result has no cancel control at
 * all. `deriveCancelControl` below finds that out by driving the
 * dialog instead of being told.
 */

export interface Overlay {
  /** Names it in a test title and in a failure message. Never asserted against. */
  id: string;
  route: string;
  /**
   * The control that opens it, and therefore the control focus must come back to.
   *
   * A selector rather than a role or a label: the admin's copy is still being
   * rewritten, and `README.md`'s rule is that no assertion here reads a word of it.
   */
  trigger: string;
}

export const OVERLAYS: readonly Overlay[] = [
  { id: "vouchers · create batch", route: "/vouchers", trigger: ".page-actions button" },
  { id: "accounts · add account", route: "/accounts", trigger: ".page-actions button" },
  { id: "access-points · register", route: "/access-points", trigger: ".page-actions button" },
  { id: "controllers · add controller", route: "/controllers", trigger: ".page-actions button" },
  /* The one opened from a row rather than from the page header, and the one with no
     cancel control. Both facts are the point: an overlay whose trigger disappears
     when the table refetches is the case `Modal`'s `isConnected` guard exists for. */
  {
    id: "controllers · probe result",
    route: "/controllers",
    trigger: "tbody tr:not(.empty-row) .row-actions button",
  },
  { id: "garden · allow a destination", route: "/garden", trigger: ".page-actions button" },
];

/**
 * The three `ConfirmDialog` call sites, which are a *different* `<dialog>`.
 *
 * The focus-and-keyboard design records why they are a separate list rather than three more rows above: the
 * primitive already closes itself — `el.close()` when `open` goes false — so the
 * platform's return works, and what differs is the call site: mounted always, it
 * returns focus; mounted conditionally, it leaves focus on `<body>`. **Red and green
 * against one primitive** says the call site, not `ui/`, is where the fix belongs.
 *
 * Their trigger is the row's own delete control, so the row must be found rather than
 * assumed: `/garden`'s first four rows are `origin: "required"` and deliberately have
 * no delete button at all.
 */
export interface ConfirmSite {
  id: string;
  route: string;
}

export const CONFIRM_SITES: readonly ConfirmSite[] = [
  { id: "accounts · delete", route: "/accounts" },
  { id: "controllers · delete", route: "/controllers" },
  { id: "garden · delete", route: "/garden" },
];

/**
 * The row control that opens a destructive confirmation.
 *
 * `.btn-ghost.btn-danger` — the console's quietest button, marked destructive — is
 * the row's delete control on all three of these screens. `.btn-ghost` alone is not
 * enough: `/accounts` rows carry a second ghost button (*Credentials*, the 802.1X
 * opt-in), which a bare `.btn-ghost` finds first. It is a structural claim and could
 * drift, so every caller checks it reached a real confirmation by asserting the dialog it opens has a `.btn-danger`
 * in its foot — the selector proves itself rather than being trusted.
 */
export const DELETE_TRIGGER =
  "tbody tr:not(.empty-row):has(button.btn-ghost.btn-danger) button.btn-ghost.btn-danger";

/** Opens a row's delete confirmation from the keyboard, and proves it is one. */
export async function openConfirm(
  page: Page,
  site: ConfirmSite,
): Promise<{ trigger: Locator; dialog: Locator }> {
  await page.goto(site.route);
  await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

  const trigger = page.locator(DELETE_TRIGGER).first();
  await expect(
    trigger,
    `${site.id}: no row offers a delete control. On /garden the required entries have ` +
      "none by design, so this looks for the first row that does.",
  ).toBeVisible();
  await trigger.focus();
  await expect(trigger).toBeFocused();
  await trigger.press("Enter");

  const dialog = openDialog(page);
  await expect(dialog).toHaveCount(1);
  await expect(
    dialog.locator(".dialog-foot .btn-danger"),
    `${site.id}: the dialog that opened has no destructive button in its foot, so ` +
      "`DELETE_TRIGGER` found some other control and this is not a confirmation.",
  ).toHaveCount(1);

  return { trigger, dialog };
}

/** Every request the console makes to the mock. Blocked while a control is *probed*. */
const API_GLOB = "**/api/v0/**";

/** The dialog that is currently up. Every flow in this console renders at most one. */
export function openDialog(page: Page): Locator {
  return page.locator("dialog[open]");
}

/**
 * The controls in a dialog's foot that could be a cancel.
 *
 * Defined by what they are *not*: not the affirmative action (`[type=submit]`,
 * `.btn-primary`) and not a destructive one (`.btn-danger`). That exclusion is a rule
 * about consequences rather than about copy, which matters because the derivation
 * below **clicks** every candidate — and the two buttons excluded here are the ones
 * that create a voucher batch, register a device, adopt access points off a
 * controller, or delete something.
 */
export function cancelCandidates(page: Page): Locator {
  return page.locator(
    "dialog[open] .dialog-foot button:not([type=submit]):not(.btn-primary):not(.btn-danger)",
  );
}

/** Puts focus on the trigger and opens the overlay from the keyboard. */
export async function openOverlay(
  page: Page,
  overlay: Overlay,
): Promise<{ trigger: Locator; dialog: Locator }> {
  await page.goto(overlay.route);
  await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

  const trigger = page.locator(overlay.trigger).first();
  await expect(trigger, `${overlay.id}: nothing matches \`${overlay.trigger}\``).toBeVisible();
  await trigger.focus();
  await expect(
    trigger,
    `${overlay.id}: the trigger refused focus, so there is nothing for the overlay ` +
      "to hand focus back to and the test below would be measuring nothing.",
  ).toBeFocused();

  /* `Enter` on the focused control rather than `click()`. A click moves focus too, so
     it would satisfy the whole of this file by accident; the operator these locks are
     about never touches the mouse. */
  await trigger.press("Enter");

  const dialog = openDialog(page);
  await expect(dialog, `${overlay.id}: pressing Enter on the trigger opened no dialog.`).toHaveCount(
    1,
  );
  expect(
    await dialog.evaluate((el) => el.matches(":modal")),
    `${overlay.id}: the dialog is open but not modal — \`<dialog open>\` rather than ` +
      "`showModal()`. `overlays.spec.ts` is the lock for that; this file assumes it.",
  ).toBe(true);

  return { trigger, dialog };
}

/**
 * Which candidate in a dialog's foot actually closes it — found by pressing them.
 *
 * There is no copy-free attribute that says "this one cancels", and a test helper
 * should not add one to the product. So the property is *driven*: a
 * cancel control is one that closes the dialog and changes nothing. Every candidate
 * is clicked with the API aborted, so the one case where a candidate turns out to be
 * an action — `/controllers`'s probe result, whose only non-primary foot button
 * **runs the probe** — cannot reach the mock, and the world is the same afterwards
 * either way.
 *
 * Returns the candidate's index, or `null` for an overlay with no such control.
 * `null` is a real answer here rather than a failure: an overlay such as the probe
 * result may be dismissable by nothing but Escape and the backdrop.
 */
export async function deriveCancelControl(page: Page, overlay: Overlay): Promise<number | null> {
  const { dialog } = await openOverlay(page, overlay);
  const count = await cancelCandidates(page).count();
  await dialog.press("Escape");
  await expect(openDialog(page)).toHaveCount(0);

  for (let i = 0; i < count; i++) {
    await openOverlay(page, overlay);
    await page.route(API_GLOB, (route) => route.abort());
    try {
      await cancelCandidates(page).nth(i).click();
      if (await closedWithin(page, 1_500)) return i;
    } finally {
      await page.unroute(API_GLOB);
    }
  }
  return null;
}

/** Polled rather than asserted: "it did not close" is an answer, not a failure. */
async function closedWithin(page: Page, ms: number): Promise<boolean> {
  const deadline = Date.now() + ms;
  do {
    if ((await openDialog(page).count()) === 0) return true;
    await page.waitForTimeout(100);
  } while (Date.now() < deadline);
  return false;
}

/**
 * Where focus is, in words a failure message can use.
 *
 * `<body>` is the answer this whole item is about — it is what the platform leaves
 * behind when the focused element is removed — so it is named rather than reported as
 * an empty string.
 */
export async function focusReport(page: Page): Promise<string> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    if (!el || el === document.body) return "<body>";
    const cls = typeof el.className === "string" && el.className ? `.${el.className}` : "";
    const row = el.closest("tr[data-row-key]")?.getAttribute("data-row-key");
    const dialog = el.closest("dialog") ? " (inside a dialog)" : "";
    return `${el.tagName.toLowerCase()}${cls}${row ? ` in row ${row}` : ""}${dialog}`;
  });
}
