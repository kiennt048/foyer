import { expect, test, type Page } from "@playwright/test";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `drawer-focus` — the navigation drawer below the 900px breakpoint
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Below the breakpoint the sidebar becomes a header and the nine destinations move
 * into a drawer. It is **not** a `<dialog>`, and could not be: the panel has to be
 * the *same element* as the sidebar's lower half at full width, and `showModal()` is
 * not something an element can stop being (`App.tsx`). So everything `<dialog>` would
 * have given for free — the focus trap, `Escape`, the return of focus — is hand-written
 * in a `useEffect`, and hand-written is what this file exists to hold.
 *
 * The rules, from the responsive admin design (`docs/architecture.md`):
 *
 *  - opens from `.menu-button`, which only exists below the breakpoint;
 *  - focus starts inside the panel and cannot leave it with Tab;
 *  - `Escape` closes it;
 *  - focus **returns to the button that opened it**;
 *  - it closes when the viewport stops being narrow, so a rotation cannot leave an
 *    invisible overlay holding the focus trap;
 *  - above the breakpoint there is no menu button, no scrim and no dialog role — the
 *    panel is simply the rest of the sidebar.
 *
 * Selectors only, never copy: the admin catalogue is still being edited and a test
 * that asserted "Menu" would fail on a rewording rather than on a regression.
 */

const drawer = {
  button: ".menu-button",
  panel: ".sidebar-panel",
  open: '.sidebar-panel[data-open="true"]',
  closed: '.sidebar-panel[data-open="false"]',
  scrim: ".drawer-scrim",
  head: ".drawer-head",
  /* The App's own rule for what is in the trap, so this test does not invent a
     second definition of "focusable" that could agree with the product by luck. */
  focusable: ".sidebar-panel a[href], .sidebar-panel button:not([disabled])",
} as const;

/** Where focus is, described well enough for a failure message to be actionable. */
async function activeElement(page: Page): Promise<{ inPanel: boolean; tag: string; cls: string }> {
  return page.evaluate((panelSelector) => {
    const el = document.activeElement as HTMLElement | null;
    const panel = document.querySelector(panelSelector);
    return {
      inPanel: Boolean(el && panel && panel.contains(el) && el !== document.body),
      tag: el?.tagName.toLowerCase() ?? "none",
      cls: typeof el?.className === "string" ? el.className : "",
    };
  }, drawer.panel);
}

async function openDrawer(page: Page): Promise<void> {
  await page.goto("/sessions");
  await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
  await expect(
    page.locator(drawer.button),
    "No menu button at 390px, so there is no drawer to test. Either the breakpoint " +
      "moved or `useNarrow` is not seeing this viewport — `admin/src/layout.ts` " +
      "declares it once and `layout.test.ts` pins the stylesheet to the same string.",
  ).toBeVisible();
  await page.locator(drawer.button).click();
  await expect(page.locator(drawer.open)).toBeVisible();
}

test.describe("the drawer, below the breakpoint", () => {
  test("opens, announces itself as a dialog, and puts focus inside", async ({ page }) => {
    await openDrawer(page);

    await expect(page.locator(drawer.button)).toHaveAttribute("aria-expanded", "true");
    await expect(page.locator(drawer.panel)).toHaveAttribute("role", "dialog");
    await expect(page.locator(drawer.panel)).toHaveAttribute("aria-modal", "true");
    /* Named, or a screen reader announces "dialog" and nothing else. */
    await expect(page.locator(drawer.panel)).toHaveAttribute("aria-label", /.+/);
    await expect(page.locator(drawer.scrim)).toHaveCount(1);
    await expect(page.locator(drawer.head)).toBeVisible();

    const focus = await activeElement(page);
    expect(
      focus.inPanel,
      `Focus is on ${focus.tag}.${focus.cls}, outside the drawer. A panel that opens ` +
        "over the page and leaves focus behind it is a panel a keyboard operator has " +
        "to Tab into past the whole header.",
    ).toBe(true);

    /* One navigation in the accessibility tree, not two. The panel is the same
       element at every width precisely so `NAV` is never rendered twice — eighteen
       destinations announced is the failure the design names first. */
    await expect(page.locator("nav.nav")).toHaveCount(1);
  });

  test("Tab cannot leave it", async ({ page }) => {
    await openDrawer(page);

    const count = await page.locator(drawer.focusable).count();
    expect(
      count,
      "The drawer has fewer than two focusable controls, so wrapping is not observable " +
        "and this test would pass against no trap at all.",
    ).toBeGreaterThan(1);

    /* One more press than there are stops, so at least one wrap must happen. Each
       press is checked rather than only the last: a trap that fails on the eleventh
       Tab and recovers is still a trap that dropped the operator onto the browser
       chrome, and asserting only the endpoint would miss it. */
    for (let i = 0; i <= count; i++) {
      await page.keyboard.press("Tab");
      const focus = await activeElement(page);
      expect(
        focus.inPanel,
        `Tab #${i + 1} of ${count + 1} left the drawer — focus is on ${focus.tag}.${focus.cls}. ` +
          "The panel is not a <dialog> and gets no trap for free; the handler is in " +
          "`App.tsx` and answers in the capture phase.",
      ).toBe(true);
    }

    /* And backwards, which is the half a naive wrap forgets. */
    for (let i = 0; i <= count; i++) {
      await page.keyboard.press("Shift+Tab");
      const focus = await activeElement(page);
      expect(
        focus.inPanel,
        `Shift+Tab #${i + 1} left the drawer — focus is on ${focus.tag}.${focus.cls}.`,
      ).toBe(true);
    }
  });

  test("Escape closes it and hands focus back to the button that opened it", async ({ page }) => {
    await openDrawer(page);
    await page.keyboard.press("Escape");

    await expect(page.locator(drawer.closed)).toHaveCount(1);
    await expect(page.locator(drawer.scrim)).toHaveCount(0);
    await expect(page.locator(drawer.button)).toHaveAttribute("aria-expanded", "false");
    /* The role goes with the state. A panel that is the sidebar's lower half must not
       keep announcing itself as a modal dialog once it is one again. */
    expect(await page.locator(drawer.panel).getAttribute("role")).toBeNull();

    expect(
      await page.evaluate(
        (sel) => document.activeElement === document.querySelector(sel),
        drawer.button,
      ),
      "Focus did not return to the menu button. It is the control the operator pressed " +
        "and the only way back into the drawer; dropping focus on <body> puts a keyboard " +
        "operator back at the top of the document.",
    ).toBe(true);
  });

  /**
   * ── THE ONE THE HARNESS ALMOST TOOK AWAY ────────────────────────────────────
   *
   * A rotation is a resize, and a drawer that survives one is an invisible overlay
   * holding a focus trap over a layout that no longer renders the button to close it.
   *
   * `admin/src/layout.ts` carries a comment saying this could not be verified —
   * that driving the viewport from CDP updates `innerWidth` and re-evaluates every
   * `@media` rule while **delivering no event at all**, measured at 1280 → 420 with
   * both listeners attached and both counts staying at zero. That is the trap this
   * task was warned about, so it was measured again before anything was written here.
   *
   * **Through `page.setViewportSize` on Playwright 1.62.1's Chromium it does fire.**
   * Measured, listeners attached after load and counters zeroed immediately before:
   *
   *   1280 → 420   resize 1, change 1, `matches` false → true, menu button 0 → 1
   *   1280 → 390   resize 1, change 1
   *   390 → 1280   resize 1, change 1, menu button 1 → 0
   *
   * So the assertion is written, and the harness's delivery is asserted *first* — if
   * a future Playwright or a different driver stops sending the event, this fails
   * naming the harness instead of accusing the console of a bug it does not have.
   * The counters are the difference between a test and a coincidence here: without
   * them a drawer that never closes and a harness that never notified would be the
   * same green tick.
   */
  test("closes when the viewport stops being narrow", async ({ page }) => {
    await openDrawer(page);

    await page.evaluate((query) => {
      const w = window as unknown as { __foyerResize: { resize: number; change: number } };
      w.__foyerResize = { resize: 0, change: 0 };
      window.addEventListener("resize", () => w.__foyerResize.resize++);
      window.matchMedia(query).addEventListener("change", () => w.__foyerResize.change++);
    }, "(max-width: 900px)");

    await page.setViewportSize({ width: 1280, height: 800 });

    /* Polled, not read once: `setViewportSize` resolves when the override is applied
       and the event is dispatched on a later turn — read synchronously after it, the
       counters are still zero and this reports "the harness delivered nothing" for a
       harness that was about to. That mistake is one line and looks exactly like the
       finding it would be reporting. */
    await expect
      .poll(
        async () =>
          page.evaluate(() => {
            const w = window as unknown as {
              __foyerResize: { resize: number; change: number };
            };
            return w.__foyerResize.resize + w.__foyerResize.change;
          }),
        {
          timeout: 5_000,
          message:
            "Widening the viewport delivered neither a `resize` nor a `MediaQueryList` " +
            "`change` event within five seconds, so nothing could have told `useNarrow` " +
            "the layout had changed. **This is the harness, not the console** — see the " +
            "comment above this test and the one in `admin/src/layout.ts`. Do not 'fix' " +
            "the drawer for it; assert the wide state after a fresh load instead.",
        },
      )
      .toBeGreaterThan(0);

    await expect(page.locator(drawer.closed)).toHaveCount(1);
    await expect(page.locator(drawer.scrim)).toHaveCount(0);
    await expect(
      page.locator(drawer.button),
      "The menu button is still rendered at 1280px, so the drawer state and the layout " +
        "disagree about what width this is.",
    ).toHaveCount(0);
    /* Restoring focus here would be wrong and is deliberately not asserted: the button
       that opened the drawer has gone with the layout that had one, and the nav links
       inside the panel are the same elements the sidebar now shows. */
  });
});

/**
 * Above the breakpoint the drawer does not exist — it is not hidden, not disabled,
 * and not a dialog anyone could reach. Same rule as capability gating: a control
 * that is present-but-inert still advertises something, and here it would also be
 * nine extra tab stops behind a scrim nobody can see.
 *
 * Asserted after a **fresh load at 1280**, not by widening a narrow page. Even though
 * the resize event does arrive here (see above), a fresh mount is what a desk browser
 * actually does, and it is the assertion that stays meaningful if the event ever stops.
 */
test.describe("above the breakpoint there is no drawer", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("no menu button, no scrim, no dialog", async ({ page }) => {
    await page.goto("/sessions");
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    await expect(page.locator(drawer.button)).toHaveCount(0);
    await expect(page.locator(drawer.scrim)).toHaveCount(0);
    await expect(page.locator(drawer.head)).toHaveCount(0);
    await expect(page.locator("aside.sidebar [role=dialog]")).toHaveCount(0);

    /* The panel is still there, because it is the sidebar's lower half — the nine
       destinations are on screen without anything being opened. */
    await expect(page.locator(drawer.closed)).toHaveCount(1);
    await expect(page.locator(drawer.panel)).toBeVisible();
    expect(await page.locator(drawer.panel).getAttribute("role")).toBeNull();
    expect(await page.locator(drawer.panel).getAttribute("aria-modal")).toBeNull();
    await expect(page.locator("nav.nav a.nav-item").first()).toBeVisible();
  });
});
