import { expect, test } from "@playwright/test";
import {
  OVERLAYS,
  cancelCandidates,
  deriveCancelControl,
  focusReport,
  openDialog,
  openOverlay,
} from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `dialog-focus-return` — every overlay hands focus back to what opened it
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Design authority: `docs/architecture.md`. The measurement it locks was **`<body>`
 * six times out of six**:
 *
 *     /vouchers       Create batch          → <body>
 *     /accounts       Add account           → <body>
 *     /access-points  Add access point      → <body>
 *     /controllers    Add controller        → <body>
 *     /controllers    Probe result          → <body>
 *     /garden         Allow a destination   → <body>
 *
 * One number six times is a structural cause rather than six screen bugs, and the
 * cause is worth stating because the primitive
 * looks correct from every angle. `Modal` calls `showModal()`, so the trap and the
 * inert document are the platform's and they work. A native `<dialog>` returns focus
 * to its opener **when `close()` is called**, and these dialogs are never closed: the
 * parent renders `null`, the element leaves the DOM while still open, and the
 * platform's return never runs. The bug is in the closing, which is why every review
 * of the opening passed.
 *
 * Two things about how this is written, both of which are easy to get wrong:
 *
 * - **`toBeFocused()`, never a raw `evaluate` of `document.activeElement`.** The
 *   restore runs in React's passive cleanup, one task after `dialog[open]` hits zero.
 *   Reading the active element the instant the dialog goes reads the old node and
 *   reports a fix that is working as a failure. `toBeFocused()` retries; the raw read
 *   does not.
 * - **The stops are asserted, never the count.** Every modal `<dialog>` tab cycle in
 *   Chromium includes exactly one press landing on `<body>` before it re-enters. That
 *   is the platform, not a defect, and it is why nothing here counts presses.
 */

test.describe("every overlay returns focus to the control that opened it", () => {
  for (const overlay of OVERLAYS) {
    test(`${overlay.id} — Escape`, async ({ page }) => {
      const { trigger, dialog } = await openOverlay(page, overlay);

      /* Escape pressed on the dialog itself, which is where a keyboard operator's
         focus is: `showModal()` put it inside. */
      await dialog.press("Escape");
      await expect(openDialog(page), `${overlay.id}: Escape did not close it.`).toHaveCount(0);

      await expect(
        trigger,
        `${overlay.id}: focus is on ${await focusReport(page)} after Escape. ` +
          "It was `<body>` on all six of these before the focus fix — the top of the " +
          "document, and a keyboard operator's place in the page gone.",
      ).toBeFocused();
    });
  }

  /**
   * The other half of the requirement: *closed by `Escape` **and** by its own
   * cancel control*.
   *
   * Driven as one pass over the list because the interesting number is not six.
   *
   * The overlays without a cancel control are **named, not counted.** A dialog that
   * offers only actions — `/controllers`'s probe result with **Probe** and **Discover**
   * and no dismissal would be one — is reported by identity. A `toEqual(5)` would go
   * red saying *six is not five*, which is true and tells you nothing.
   *
   * The list is empty, and if an overlay ships without a way out, this goes red
   * **naming that one**.
   */
  test("and by its own cancel control, on every overlay that has one", async ({ page }) => {
    const withCancel: string[] = [];
    const withoutCancel: string[] = [];

    for (const overlay of OVERLAYS) {
      const index = await deriveCancelControl(page, overlay);
      if (index === null) {
        withoutCancel.push(overlay.id);
        continue;
      }
      withCancel.push(overlay.id);

      const { trigger } = await openOverlay(page, overlay);
      await cancelCandidates(page).nth(index).click();
      await expect(openDialog(page)).toHaveCount(0);
      await expect(
        trigger,
        `${overlay.id}: focus is on ${await focusReport(page)} after its own cancel ` +
          "control. Escape and Cancel are two paths through the same unmount and both " +
          "have to land in the same place.",
      ).toBeFocused();
    }

    expect(
      withoutCancel,
      "An overlay here has no way out but Escape — a gesture a pointer user has to " +
        "already know, and one of only two a phone offers at all. This list must stay " +
        "empty. Whatever is named here is a dialog somebody can " +
        "open and cannot close.",
    ).toEqual([]);

    /* Derived, so it cannot drift from the list above: everything else was covered. */
    expect(withCancel.length).toBe(OVERLAYS.length - withoutCancel.length);
    expect(
      withCancel.length,
      "No overlay was closed by a control of its own, so the assertions in the loop " +
        "above ran zero times and this test proves nothing.",
    ).toBeGreaterThan(1);
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The seven assertions above are worth exactly as much as this one case, and the
   * defect they lock can be put back **from the page**, which is the honest version:
   * the evidence re-derives on every run instead of living in a commit message.
   *
   * The failure mechanism is precisely "the element leaves the DOM while still
   * open", so that is what this does — `dialog.remove()`, which is what the parent's
   * `null` does unless `Modal` captures the opener and restores it by hand. React's
   * cleanup does not run, because nothing unmounted.
   *
   * **Baseline without the fix:** `/vouchers` · Create batch, opened from the
   * page action with focus on it:
   *
   *     as it stands, Escape          →  button.btn.btn-primary  (the trigger)
   *     the open dialog removed       →  <body>
   *
   * So `<body>` is what this lock is distinguishing from, the platform does not hand
   * focus back on its own, and `toBeFocused()` on the trigger can go red.
   */
  test("the lock is not vacuous: a dialog removed while open leaves focus on <body>", async ({
    page,
  }) => {
    const overlay = OVERLAYS[0]!;
    const { trigger } = await openOverlay(page, overlay);

    await openDialog(page).evaluate((el) => el.remove());
    await expect(openDialog(page)).toHaveCount(0);

    /*
     * Polled, because the assertion this mirrors is polled: if focus were going to
     * arrive on the trigger a task later, `toBeFocused()` would find it and so must
     * this. It does not arrive — nothing is restoring it — and that is the finding.
     */
    await expect
      .poll(() => focusReport(page), {
        timeout: 2_000,
        message:
          "Removing an open <dialog> from the document did **not** leave focus on " +
          "<body>. Either Chromium now restores focus for a removed-while-open " +
          "dialog — in which case the six measurements have a different cause and " +
          "the whole `Modal` focus fix needs re-reading — or this sabotage no longer " +
          "reaches the element it thinks it does.",
      })
      .toBe("<body>");

    await expect(
      trigger,
      "The trigger has focus after the dialog was torn out from under it, which " +
        "would mean the assertions above pass without `Modal` doing anything.",
    ).not.toBeFocused();
  });
});
