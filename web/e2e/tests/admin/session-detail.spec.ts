import { expect, test, type Page } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";

/**
 * The session detail dock.
 *
 * Three rules, all of them the kind that erode quietly. There are unit tests for the
 * state machine behind the first two (`admin/src/list/detail.ts`); what is missing is
 * the proof that the state machine is actually wired to the URL and the DOM, which is
 * the only part an operator experiences.
 *
 * The third — that nothing in this surface mutates — is the one worth the most. It is
 * the rule that erodes one helpful button at a time, and the panel is new surface with
 * a lot of room on it.
 */

/** The row keys currently rendered, in order. `data-row-key` is the stable identity. */
async function rowKeys(page: Page): Promise<string[]> {
  return page
    .locator(`${admin.rows}[data-row-key]`)
    .evaluateAll((els) => els.map((el) => el.getAttribute("data-row-key")!));
}

function sessionParam(page: Page): string | null {
  return new URL(page.url()).searchParams.get("session");
}

test.describe("the session detail dock", () => {
  test("a session is a link: ?session= survives a reload", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);

    const keys = await rowKeys(page);
    expect(keys.length, "The sessions table rendered no identified rows.").toBeGreaterThan(0);
    const target = keys[0]!;

    await page.locator(`${admin.rows}[data-row-key="${target}"]`).click();
    await expect(page.locator(admin.detailDock)).toBeVisible();
    expect(
      sessionParam(page),
      "Opening the panel did not put the session in the URL. A triage view an " +
        "operator cannot send to a colleague is a view they will screenshot instead.",
    ).toBe(target);

    /*
     * The reload is the whole test. A panel held in component state alone looks
     * identical until the moment somebody shares the link, and then it is empty.
     */
    await page.reload();
    await tableReady(page);
    await expect(page.locator(admin.detailDock)).toBeVisible();
    expect(sessionParam(page)).toBe(target);

    /* And it came back about the same session, not merely open. */
    await expect(page.locator(`${admin.rows}[data-row-key="${target}"]`)).toHaveAttribute(
      "data-selected",
      "true",
    );
  });

  test("stays closed once dismissed, however far the selection moves", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);

    const keys = await rowKeys(page);
    expect(keys.length, "Not enough rows to move the cursor.").toBeGreaterThan(2);

    await page.locator(`${admin.rows}[data-row-key="${keys[0]}"]`).click();
    const dock = page.locator(admin.detailDock);
    await expect(dock).toBeVisible();

    /* Dismiss. `Escape` rather than the close button: it is the gesture with no
       label, so it is the one a copy change cannot break. */
    await page.keyboard.press("Escape");
    await expect(dock).toHaveCount(0);

    /*
     * Now move the cursor a long way. The operator has said they do not want the
     * panel; re-opening it on the next arrow key overrules them, and it is exactly
     * what makes an otherwise good panel feel like it is fighting you.
     */
    for (let i = 0; i < 3; i++) {
      await page.keyboard.press("ArrowDown");
      await expect(dock, "The dismissed panel came back when the cursor moved.").toHaveCount(0);
    }

    /* The cursor really did move — otherwise this passes because nothing happened. */
    expect(
      sessionParam(page),
      "The cursor never moved, so the dismissal was not actually tested.",
    ).not.toBe(keys[0]);

    /* And a deliberate open still works. Without this the test would pass just as
       well against a panel that can never be opened again. */
    await page.keyboard.press("Enter");
    await expect(dock).toBeVisible();
  });

  test("nothing in the panel mutates anything", async ({ page }) => {
    /*
     * `radacct` and `radpostauth` belong to FreeRADIUS. Go never writes them, so
     * every field on this panel is read-only forever and there is no version of it
     * that is not.
     *
     * Asserted from two sides, the same way the sessions list is:
     *
     *  • **Structure** — the dock contains no data-entry control of any kind. Not a
     *    disabled one, not a read-only one: none. A field an operator can put a
     *    caret into is a promise the storage layer cannot keep.
     *  • **Effect** — every button in the dock is clicked, and not one write leaves
     *    the browser. This is the half that survives a refactor of the markup: a
     *    mutating affordance has to issue a write to prove it exists, and the
     *    contract gives it nothing to issue (`contract/src/read-only.ts` forbids the
     *    operation at the other end).
     *
     * **One control here does act, and it is not an edit**: *Disconnect guest* asks
     * the device to end the session. Pressing it opens a confirmation and sends
     * nothing, which is what this loop sees; confirming is what sends, and that is
     * asserted — exactly one request, with its options — in
     * `session-disconnect.spec.ts`. The confirmation lives outside the dock, so the
     * no-data-entry check above still holds of the dock itself.
     */
    const writes: string[] = [];
    page.on("request", (r) => {
      if (!r.url().includes("/api/v0/")) return;
      if (["POST", "PUT", "PATCH", "DELETE"].includes(r.method())) {
        writes.push(`${r.method()} ${r.url()}`);
      }
    });

    await page.goto("/sessions");
    await tableReady(page);
    const target = (await rowKeys(page))[0]!;

    const open = async () => {
      await page.goto(`/sessions?session=${encodeURIComponent(target)}`);
      await tableReady(page);
      await expect(page.locator(admin.detailDock)).toBeVisible();
    };

    await open();

    /* No way in. */
    await expect(
      page.locator(`${admin.detailDock} input, ${admin.detailDock} select, ` +
        `${admin.detailDock} textarea, ${admin.detailDock} form, ` +
        `${admin.detailDock} [contenteditable]`),
      "The session panel contains a data-entry control. Accounting is read-only, " +
        "forever — an edit affordance here can never be implemented.",
    ).toHaveCount(0);

    /* Every link goes somewhere inside the console. A panel is a good place to put a
       "look this up" link, and a bad place to discover an off-site one. */
    const hrefs = await page
      .locator(`${admin.detailDock} a[href]`)
      .evaluateAll((els) => els.map((el) => el.getAttribute("href")!));
    for (const href of hrefs) {
      expect(href, `The panel links off-console: ${href}`).toMatch(/^[/#]/);
    }

    /* And press everything. Re-opened before each click so a control that closes or
       navigates does not hide the rest of them. */
    const total = await page.locator(`${admin.detailDock} button`).count();
    expect(total, "The panel offers no controls at all; this test would be vacuous.")
      .toBeGreaterThan(0);

    for (let i = 0; i < total; i++) {
      await open();
      const button = page.locator(`${admin.detailDock} button`).nth(i);
      if (!(await button.isVisible()) || (await button.isDisabled())) continue;
      await button.click();
      /* Give a write time to leave, rather than asserting on an empty microtask. */
      await page.waitForTimeout(150);
      expect(
        writes,
        `Clicking control ${i} in the session panel issued a write to the API. ` +
          "Sessions and auth attempts are read-only, forever.",
      ).toEqual([]);
    }
  });
});
