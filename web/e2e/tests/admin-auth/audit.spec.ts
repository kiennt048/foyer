import { expect, test } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";
import { ADMINISTRATOR, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `/audit` — the actor who no longer exists, and the filter that eats itself
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `AuditEntry.actorUsername` is denormalised at write time, and the fixture world
 * proves why: one entry's `actorId` is `op-gone`, which is in `operators` **nowhere at
 * all**, and the entry still names `tran.quoc.bao`. Joining to the operators list at
 * read time is the obvious implementation and the wrong one — it renders *"deleted
 * operator"* on the one row somebody opened the log to understand.
 *
 * That is the single most load-bearing property of this screen, and this file locks it.
 * It is also the property most likely to be "tidied away" by somebody adding a nice
 * *no longer an operator* annotation, which needs exactly the join the member exists
 * to avoid — and which would additionally be wrong for the whole time the operators
 * list was loading, where every actor reads as deleted.
 */

const ACTOR_SELECT = ".toolbar .field select";
const ROWS = "tbody tr:not(.empty-row)";
/** The actor column is the second `keep` column: when, actor, change. */
const ACTOR_CELL = "td:nth-child(2)";

/** The fixture actor with no operator record. */
const GHOST_ID = "op-gone";
const GHOST_USERNAME = "tran.quoc.bao";

test.afterEach(async ({ context }) => {
  await signOut(context);
});

test.describe("the audit log names actors from the entry", () => {
  /**
   * The world really is the one this file assumes, asked of the server directly.
   *
   * Without it every assertion below is one fixture edit away from being green and
   * meaningless: an `op-gone` that quietly became a real operator would satisfy the
   * page assertions and prove nothing at all about denormalisation.
   */
  test("the fixture world still contains an actor with no operator record", async ({ request }) => {
    const audit = await request.get(`${MOCK_URL}/api/v0/audit?limit=100`);
    expect(audit.status()).toBe(200);
    const entries = (await audit.json()).items as { actorId: string; actorUsername: string }[];

    const ghost = entries.filter((e) => e.actorId === GHOST_ID);
    expect(
      ghost.length,
      `No audit entry has \`actorId: "${GHOST_ID}"\`. That row is the whole argument ` +
        "for denormalising `AuditEntry.actorUsername`, and without it every " +
        "assertion in this file is satisfied by an implementation that joins to " +
        "`/operators` at read time. See `buildAudit` in `web/fixtures/src/index.ts`.",
    ).toBeGreaterThan(0);
    expect(ghost.every((e) => e.actorUsername === GHOST_USERNAME)).toBe(true);

    const operators = await request.get(`${MOCK_URL}/api/v0/operators`);
    const ids = ((await operators.json()).items as { id: string }[]).map((o) => o.id);
    expect(
      ids,
      `\`${GHOST_ID}\` is now a real operator, so the log can be joined to the ` +
        "operators list without anybody noticing and this screen's central decision " +
        "is no longer measurable.",
    ).not.toContain(GHOST_ID);
  });

  test("a deleted operator is still named on their own entries", async ({ page, context }) => {
    await signInAs(context, ADMINISTRATOR);
    await page.goto("/audit");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    const ghostRows = page.locator(ROWS).filter({ hasText: GHOST_USERNAME });
    await expect(
      ghostRows,
      `No row names \`${GHOST_USERNAME}\`. Their \`actorId\` matches no operator, so ` +
        "a screen that looked the actor up would render *deleted operator*, or a " +
        "blank, or the id — on the one row somebody is reading the log to understand.",
    ).not.toHaveCount(0);

    /* The cell, not the row: a row that merely *mentions* the name somewhere in its
       change description would satisfy the locator above. */
    await expect(ghostRows.first().locator(ACTOR_CELL)).toHaveText(GHOST_USERNAME);
  });

  /**
   * And they are **filterable**, which is the half that could not work if the actor
   * list came from `/operators`.
   *
   * `Audit.tsx`'s docstring is right about this and it must be kept: the list has to
   * come from the log, because the operator somebody is looking for is precisely the
   * one who is no longer in the operators list.
   */
  test("a deleted operator can be filtered to", async ({ page, context }) => {
    await signInAs(context, ADMINISTRATOR);
    await page.goto("/audit");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    const select = page.locator(ACTOR_SELECT).first();
    await expect(
      select.locator(`option[value="${GHOST_ID}"]`),
      `The actor filter does not offer \`${GHOST_ID}\`. That is what happens when the ` +
        "list is sourced from `/operators` instead of from the log — see " +
        "`Audit.tsx`'s docstring, which is correct and must be kept.",
    ).toHaveCount(1);

    await select.selectOption(GHOST_ID);
    await expect(page).toHaveURL(new RegExp(`actorId=${GHOST_ID}`));
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    const cells = page.locator(`${ROWS} ${ACTOR_CELL}`);
    await expect(cells, "Filtering to the deleted actor emptied the log.").not.toHaveCount(0);
    const names = await cells.allTextContents();
    expect(
      [...new Set(names.map((n) => n.trim()))],
      "Filtering by actor returned entries belonging to somebody else.",
    ).toEqual([GHOST_USERNAME]);
  });
});

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE ACTOR FILTER — it must not collapse when it is used
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `Audit.tsx` sources the actor list from an unfiltered fetch. Building it from
 * `rows`, which is the **already-filtered** result:
 *
 * ```ts
 * const rows = query.data?.items ?? [];
 * const actors = [...new Map(rows.map((e) => [e.actorId, e.actorUsername])).entries()]…
 * ```
 *
 * would make selecting an actor remove the other four from the dropdown, and
 * switching from one actor to another a two-step operation — back to *Any actor*,
 * then to the new one — with **no *Clear filters* control on screen in that state**:
 * `Table` offers one only from the no-matches empty row, and a filter that matched
 * something has rows rather than an empty row.
 *
 * The list comes from an *unfiltered* fetch. It does **not** come
 * from `/operators`: that would make `op-gone` unfilterable, which is the property the
 * three tests above lock, and which is why this test still counts the options rather
 * than comparing them to an operator list.
 */
test.describe("the audit filters", () => {
  test("choosing an actor leaves the other actors selectable", async ({
    page,
    context,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    await page.goto("/audit");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    const select = page.locator(ACTOR_SELECT).first();
    /* Derived from the unfiltered log rather than typed: a fixture that gains a
       sixth actor moves this number on its own. */
    const before = await select.locator("option").evaluateAll((els) =>
      els.map((el) => (el as HTMLOptionElement).value).filter(Boolean),
    );
    expect(
      before.length,
      "The unfiltered log offers fewer than two actors, so nothing can collapse and " +
        "this test cannot see the defect.",
    ).toBeGreaterThan(1);

    await select.selectOption(GHOST_ID);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
    await expect(page.locator(ROWS)).not.toHaveCount(0);

    const after = await select.locator("option").evaluateAll((els) =>
      els.map((el) => (el as HTMLOptionElement).value).filter(Boolean),
    );

    expect(
      after,
      "Choosing an actor removed the other actors from the actor filter.\n" +
        "  Switching from " +
        "one actor to another is two " +
        "steps — back to *Any actor*, then to the new one — and there is no *Clear " +
        "filters* control on screen in this state: `Table` renders one only from the " +
        "no-matches empty row, and a filter that matched something has rows.\n" +
        "  The fix is to source the list from an **unfiltered** fetch. It is not to " +
        `source it from \`/operators\`: \`${GHOST_ID}\` is in no operators list, and ` +
        "the three tests above this one lock the fact that they remain filterable.\n" +
        `  Offered before: ${JSON.stringify(before)}\n` +
        `  Offered after:  ${JSON.stringify(after)}`,
    ).toEqual(before);
  });
});
