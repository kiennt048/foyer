import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { ADMINISTRATOR, STAFF, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The dashboard's trends chart and the CSV exports
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Driven against the seeded mock, so what is asserted is what an operator opening
 * the dashboard gets: a bar per day, the byte figure's freshness in the cards' own
 * words, a download whose first bytes are a byte-order mark and whose header is the
 * contract's, and — for a staff session — the chart with no export control at all.
 *
 * In `admin-auth/` because the export is gated on a permission the fixture staff
 * operator does not hold, and that half needs a real session to be observable.
 */

test.afterEach(async ({ context }) => {
  await signOut(context);
});

test("the dashboard draws a bar per day from the seeded world", async ({ page, context }) => {
  await signInAs(context, ADMINISTRATOR);
  await page.goto("/");
  await shellSettled(page);

  const chart = page.getByTestId("trends-chart");
  await expect(chart).toBeVisible();
  await expect(chart).toHaveAttribute("role", "img");
  await expect(page.getByTestId("trends-bar"), "fourteen days, every one present").toHaveCount(14);
  // At least one day has guests on it in the fixture world, so a bar has height.
  const heights = await page
    .getByTestId("trends-bar")
    .evaluateAll((bars) => bars.map((b) => Number(b.getAttribute("height"))));
  expect(heights.some((h) => h > 0), "every bar is empty — the chart draws nothing").toBe(true);

  // The byte figure travels with its freshness, as every accounting figure does.
  await page.getByRole("combobox", { name: "Figure" }).selectOption("bytes");
  await expect(page.locator("#trends-title ~ .card .metric-foot").first()).toBeVisible();
  await expect(page.getByText(/Data from devices that send RADIUS accounting/)).toBeVisible();

  // Twelve weeks, each a Monday.
  await page.getByRole("combobox", { name: "Period" }).selectOption("week");
  await expect(page.getByTestId("trends-bar")).toHaveCount(12);
});

test("an administrator downloads a CSV that opens as the contract says", async ({ page, context }) => {
  await signInAs(context, ADMINISTRATOR);
  await page.goto("/");
  await shellSettled(page);

  const [download] = await Promise.all([
    page.waitForEvent("download"),
    page.getByRole("button", { name: "Export sessions" }).click(),
  ]);
  expect(download.suggestedFilename()).toMatch(/^foyer-sessions-site-hcm-\d{4}-\d{2}-\d{2}-\d{4}-\d{2}-\d{2}\.csv$/);
  const bytes = readFileSync((await download.path())!);
  expect([...bytes.subarray(0, 3)], "no byte-order mark").toEqual([0xef, 0xbb, 0xbf]);
  const text = bytes.toString("utf8").slice(1);
  expect(text.split("\r\n")[0]).toMatch(/^"sessionId","acctSessionId","state","startedAt",/);
  expect(text.split("\r\n").length, "a header and no rows").toBeGreaterThan(2);
});

test("a staff member sees the chart and is offered no export", async ({ page, context }) => {
  const session = await signInAs(context, STAFF);
  expect(
    session.permissions,
    "The fixture staff operator has gained `reports:export`, so this no longer measures a gate.",
  ).not.toContain("reports:export");
  await page.goto("/");
  await shellSettled(page);

  await expect(page.getByTestId("trends-chart")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Export / })).toHaveCount(0);
});
