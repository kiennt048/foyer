import { expect, test } from "@playwright/test";
import { admin, groupCode, tableReady } from "../helpers/admin.js";
import { createVoucherBatch, voucherByCode } from "../helpers/mock.js";

/** The session states the contract defines. Used to check a URL, not a label. */
const SESSION_STATES = ["online", "ended", "orphaned", "unknown"];

test.describe("admin, the paths an operator actually walks", () => {
  test("the dashboard renders its figures, and every figure carries its freshness", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();

    const cards = page.locator(admin.cards);
    await expect(cards).not.toHaveCount(0);

    /* Loading is skeleton-shaped, so "rendered" means the skeleton is gone. */
    await expect(page.locator(`${admin.cards} .skeleton`)).toHaveCount(0);

    const count = await cards.count();
    for (let i = 0; i < count; i++) {
      const card = cards.nth(i);
      await expect(card.locator(admin.metricValue)).not.toBeEmpty();
      /*
       * The rule this enforces: no figure derived from RADIUS accounting is rendered
       * without saying how fresh it is. Byte counters advance only on Interim-Update
       * or Stop, with a widely-implemented 300-second floor, and on the controller-API
       * path they may never arrive at all. A bare number here would be a lie with a
       * five-minute half-life.
       */
      await expect(
        card.locator(admin.metricFoot),
        "A dashboard figure rendered with no freshness line.",
      ).not.toBeEmpty();
    }

    /* And no chart. There is no data to draw one from, and there will not be. */
    await expect(page.locator("main svg.chart, main canvas")).toHaveCount(0);
  });

  test("the sessions table renders, filters by state, and the filter survives a reload", async ({
    page,
  }) => {
    const sessionRequests: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/api/v0/sessions")) sessionRequests.push(r.url());
    });

    await page.goto("/sessions");
    const rows = await tableReady(page);
    const unfiltered = await rows.count();
    expect(unfiltered, "The sessions table rendered no rows at all.").toBeGreaterThan(0);

    /* Filter by the first state chip. Which state that is comes from the URL the app
       writes, not from the chip's label — the labels are being translated right now. */
    const chip = page.locator(admin.chips).first();
    await chip.click();
    await expect(chip).toHaveAttribute("aria-pressed", "true");

    await expect(page).toHaveURL(/[?&]state=/);
    const state = new URL(page.url()).searchParams.get("state");
    expect(SESSION_STATES, `Filter wrote an unknown state "${state}" into the URL.`).toContain(
      state,
    );

    /* The filter is a query, not a client-side slice: the server was asked. */
    await expect
      .poll(() => sessionRequests.some((u) => u.includes(`state=${state}`)))
      .toBe(true);

    await tableReady(page);
    const filtered = await page.locator(admin.rows).count();
    expect(filtered).toBeLessThanOrEqual(unfiltered);

    /*
     * A filtered view is a link an operator can send to a colleague, and the back
     * button has to mean what it looks like it means. So: reload, and the state
     * comes back — both in the URL and in the pressed chip.
     */
    await page.reload();
    await tableReady(page);
    expect(new URL(page.url()).searchParams.get("state")).toBe(state);
    await expect(page.locator(admin.chips).first()).toHaveAttribute("aria-pressed", "true");
    expect(await page.locator(admin.rows).count()).toBe(filtered);
  });

  test("free-text search is reflected in the URL and survives a reload", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);

    await page.locator(admin.search).fill("00:");
    await expect(page).toHaveURL(/[?&]q=/);
    const q = new URL(page.url()).searchParams.get("q");

    await page.reload();
    await tableReady(page);
    expect(new URL(page.url()).searchParams.get("q")).toBe(q);
    await expect(page.locator(admin.search)).toHaveValue(q!);
  });

  test("the access-point list and detail render", async ({ page }) => {
    await page.goto("/access-points");
    const rows = await tableReady(page);
    await expect(rows).not.toHaveCount(0);

    await page.goto("/access-points/nas-unifi-1");
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();

    /* Device, shared secret, capabilities — the three things the screen is for. */
    await expect(page.locator(admin.sections)).not.toHaveCount(0);
    await expect(page.locator(admin.keyValues).first()).toBeVisible();

    /*
     * The secret is write-only. It is never returned by the API, so it must not
     * appear anywhere on a detail screen that was merely *read*.
     */
    await expect(page.locator(".secret")).toHaveCount(0);
  });

  test("a voucher can be revoked, and the row states the outcome", async ({ page, context }) => {
    const { batchId, vouchers } = await createVoucherBatch(context.request, "e2e revoke");
    const target = vouchers[0]!;

    await page.goto(`/vouchers/${batchId}`);
    await tableReady(page);

    const row = page.locator(admin.rows).filter({ hasText: groupCode(target.code) });
    await expect(row, `Voucher ${target.code} is not in the rendered batch.`).toHaveCount(1);

    /* A voucher is cheap to reissue, so revocation is a low-friction destructive
       action: immediate, with the outcome reported — not a confirmation dialog. */
    const revoke = row.locator("button");
    await expect(revoke).toHaveCount(1);
    await revoke.click();

    /*
     * The outcome is *reported*, in a live region. What it says depends on what
     * actually happened — revoking a voucher does not by itself end a live session;
     * that needs a driver call, and it can fail. The toast is asserted for existence
     * and non-emptiness only: the wording is being translated as this is written.
     */
    const toast = page.locator(`${admin.toasts} .card`).first();
    await expect(toast).toBeVisible();
    await expect(toast).not.toBeEmpty();

    /* Honest means the row says what the server says. `data-level` is semantic —
       `negative` is "a bad outcome that needs no action", which is what revoked is. */
    await expect(row.locator(`${admin.pill}[data-level="negative"]`)).toBeVisible();

    const after = await voucherByCode(context.request, target.code);
    expect(after?.state).toBe("revoked");
    expect(after?.revokedAt).toBeTruthy();

    /* And the control is gone: a revoked voucher cannot be revoked again. */
    await expect(row.locator("button")).toHaveCount(0);
  });

  test("accounting is read-only: no session offers a mutating control", async ({ page }) => {
    /*
     * `radacct` and `radpostauth` belong to FreeRADIUS. Go never writes them, so an
     * "edit session" affordance is a control that can never be built.
     *
     * "A session row contains no `input` at all" does not hold: bulk selection puts a
     * leading checkbox on each row. The *rule* does, because a selection control
     * changes nothing about a session; it decides which rows the operator is looking at.
     *
     * Rather than exempt an attribute and move on, the assertion is made from
     * two sides. The DOM side says what may exist. The network side says what may
     * happen, and that half is what survives any future refactor of the markup: if a
     * mutating affordance is ever added, it has to issue a write to prove it, and
     * there is no session write operation in the contract to issue.
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

    /*
     * The session table's rows, by the key the list gives each one. Not every row on
     * the screen: the *Signed in through the portal* card below the list
     * (`PortalGrants.tsx`) is a table of the portal's own authorizations — Foyer's
     * `portal_grants`, not FreeRADIUS's `radacct` — and ending one is a write Foyer
     * owns (`disconnectPortalGrant`). Its rows carry no `data-row-key`.
     */
    const sessionRows = `${admin.rows}[data-row-key]`;
    await expect(page.locator(sessionRows).first()).toBeVisible();

    /* Buttons in a row are copy-this-value and filter-by-this-value. Nothing else. */
    const rowButtons = page.locator(`${sessionRows} button`);
    const total = await rowButtons.count();
    for (let i = 0; i < total; i++) {
      const cls = (await rowButtons.nth(i).getAttribute("class")) ?? "";
      expect(
        cls,
        "A session row carries a control that is not a copy/filter affordance. " +
          "Accounting is read-only, forever.",
      ).toContain("id-action");
    }

    /* The only input permitted in a row is the selection checkbox, and it has to
       actually be a checkbox — the exemption is on the element, not just a name. */
    const rowInputs = page.locator(`${sessionRows} input`);
    const inputCount = await rowInputs.count();
    for (let i = 0; i < inputCount; i++) {
      const input = rowInputs.nth(i);
      const [type, role] = await Promise.all([
        input.getAttribute("type"),
        input.getAttribute("data-role"),
      ]);
      expect(
        `${type}/${role}`,
        "A session row carries an input that is not the bulk-selection checkbox.",
      ).toBe("checkbox/row-select");
    }
    await expect(page.locator(`${sessionRows} select, ${sessionRows} textarea`)).toHaveCount(0);

    /* Select rows and open the batch bar: still nothing mutating on offer. */
    const boxes = page.locator(`${sessionRows} ${admin.rowSelect}`);
    if ((await boxes.count()) > 0) {
      await boxes.first().check();
      await expect(page.locator(admin.batchBar)).toBeVisible();
      const barButtons = page.locator(`${admin.batchBar} button`);
      await expect(barButtons).not.toHaveCount(0);
    }

    /* And across all of it, not one write left the browser. */
    expect(
      writes,
      "Browsing and selecting sessions issued a write to the API. Sessions and auth " +
        "attempts are read-only forever — FreeRADIUS owns radacct and radpostauth.",
    ).toEqual([]);
  });

  test("every primary nav destination renders without a client-side error", async ({ page }) => {
    await page.goto("/");
    const links = page.locator(admin.navItems);
    const hrefs = await links.evaluateAll((els) =>
      els.map((el) => (el as HTMLAnchorElement).getAttribute("href")!),
    );
    expect(hrefs.length).toBeGreaterThan(0);

    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(String(e)));

    for (const href of hrefs) {
      await page.goto(href);
      await expect(page.locator(admin.pageTitle), `${href} rendered no page title.`).not.toBeEmpty();
    }
    expect(errors, `Uncaught errors while walking the nav:\n${errors.join("\n")}`).toEqual([]);
  });
});
