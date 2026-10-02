import { expect, test } from "@playwright/test";

/**
 * The console in Vietnamese.
 *
 * Driven in a browser for the three things a unit test cannot see: the choice
 * survives a reload (it is this browser's), a lazily loaded screen arrives already
 * in the language rather than English first, and an English reader downloads no
 * Vietnamese at all — the translations are separate modules fetched only when
 * asked for, which is what keeps the entry chunk inside its budget.
 */
test.describe("the console's language", () => {
  test.afterEach(async ({ page }) => {
    await page.evaluate(() => localStorage.removeItem("foyer.admin.lang"));
  });

  test("an English reader downloads no Vietnamese", async ({ page }) => {
    const fetched: string[] = [];
    page.on("request", (r) => fetched.push(r.url()));
    await page.goto("/network");
    await expect(page.getByRole("heading", { level: 1, name: "Network" })).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
    expect(fetched.filter((u) => u.includes("/i18n/vi/"))).toEqual([]);
  });

  test("chosen once, kept by this browser, and every screen arrives in it", async ({ page }) => {
    await page.goto("/");
    await page.getByLabel("Language").selectOption("vi");
    // The choice reloads the page; the shell comes back in Vietnamese.
    await expect(page.locator("html")).toHaveAttribute("lang", "vi");
    await expect(page.getByRole("link", { name: /Tổng quan/ })).toBeVisible();
    await expect(page.getByLabel("Ngôn ngữ")).toHaveValue("vi");

    // A lazily loaded screen, and its own namespace, in Vietnamese on first paint.
    await page.getByRole("link", { name: /Mạng/ }).first().click();
    await expect(page.getByRole("heading", { level: 1, name: "Mạng" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Chứng chỉ cổng đăng nhập" })).toBeVisible();
    await expect(page.getByText("Cài đặt nâng cao")).toBeVisible();
    // Nothing of the English screen copy is left behind a Vietnamese heading.
    await expect(page.getByText("Portal certificate", { exact: true })).toHaveCount(0);

    await page.reload();
    await expect(page.getByRole("heading", { level: 1, name: "Mạng" })).toBeVisible();

    await page.getByLabel("Ngôn ngữ").selectOption("en");
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
    await expect(page.getByRole("heading", { level: 1, name: "Network" })).toBeVisible();
  });
});
