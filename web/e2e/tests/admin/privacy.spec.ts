import { expect, test } from "@playwright/test";

/**
 * `/privacy` — retention periods and erasing one guest's data, driven in a browser
 * against the stateful mock.
 *
 * Both drives put the world back: the retention change is restored to its default,
 * and the erasure names a MAC no fixture holds, so neither leaves a mark another
 * spec running beside it could trip on.
 */
test.describe("/privacy", () => {
  test("a retention period is shown with its bounds, saved, and restored", async ({ page }) => {
    await page.goto("/privacy");
    const accounting = page.getByLabel("Session history");
    await expect(accounting).toHaveValue("90");
    /* One field per `RetentionCategoryId` — six, including `marketingConsents` —
       counted inside the retention card: the maintenance schedule on the same screen
       has number fields of its own. */
    const periods = page.locator('section[aria-labelledby="retention-title"] input[type=number]');
    await expect(periods).toHaveCount(6);
    await expect(page.getByText("Between 1 and 3650 days.").first()).toBeVisible();

    await accounting.fill("60");
    await page.getByRole("button", { name: "Save periods" }).click();
    await expect(page.getByText("Retention periods saved.")).toBeVisible();

    await page.reload();
    await expect(page.getByLabel("Session history")).toHaveValue("60");

    await page.getByRole("button", { name: "Restore default" }).first().click();
    await page.getByRole("button", { name: "Save periods" }).click();
    await expect(page.getByText("Retention periods saved.")).toBeVisible();
    await expect(page.getByLabel("Session history")).toHaveValue("90");
  });

  test("an out-of-range period is stopped before it reaches the server", async ({ page }) => {
    /* The field carries the category's own bounds as `min`/`max`, as the voucher
       count does, so the browser refuses first; the server's refusal with its figure
       is driven in web/mock and internal/api. Nothing is saved either way. */
    await page.goto("/privacy");
    const accounting = page.getByLabel("Session history");
    await accounting.fill("0");
    await page.getByRole("button", { name: "Save periods" }).click();
    expect(await accounting.evaluate((el) => (el as HTMLInputElement).validity.valid)).toBe(false);
    await expect(page.getByText("Retention periods saved.")).toHaveCount(0);
    await page.reload();
    await expect(page.getByLabel("Session history")).toHaveValue("90");
  });

  test("an erasure is confirmed, and answers with every table it searched", async ({ page }) => {
    await page.goto("/privacy");
    await page.getByLabel("MAC address", { exact: true }).fill("02-00-5E-10-00-95");
    await page.getByRole("button", { name: "Erase…" }).click();

    const dialog = page.locator("dialog[open]");
    await expect(dialog).toContainText("This cannot be undone.");
    await dialog.getByRole("button", { name: "Erase" }).click();

    const receipt = page.locator("[role=status]", { hasText: "records erased" });
    await expect(receipt).toContainText("••:••:••:••:00:95");
    for (const table of ["radacct", "radpostauth", "authorization_events", "portal_grants", "portal_sessions"]) {
      await expect(receipt.locator("code", { hasText: table })).toHaveCount(1);
    }
  });

  test("something that is not a MAC is refused before anything is erased", async ({ page }) => {
    await page.goto("/privacy");
    await page.getByLabel("MAC address", { exact: true }).fill("user-cafe00112233");
    await page.getByRole("button", { name: "Erase…" }).click();
    await page.locator("dialog[open]").getByRole("button", { name: "Erase" }).click();
    await expect(page.locator(".field-error", { hasText: "Enter a MAC address" })).toBeVisible();
  });
});
