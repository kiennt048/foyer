import { expect, test, type Page } from "@playwright/test";
import { PORTAL_URL } from "../helpers/env.js";

/**
 * The page builder, driven the way a venue owner would drive it — and then the
 * guest's page, fetched from the portal, to prove what they typed reached it.
 *
 * The design is in `docs/architecture.md`. The requirement was
 * that a non-technical owner can make the page their own; the assertion that
 * matters is therefore not that the form accepts input but that **a guest's
 * phone receives what was typed**, which is why every test here ends on the
 * portal's own document rather than on the console.
 *
 * The café's design is the fixture world's, and other specs read the café's
 * portal, so each test puts back exactly what it found.
 */

const SETTINGS = "/api/v0/sites/site-hcm/portal";

async function snapshot(page: Page): Promise<unknown> {
  const res = await page.request.get(SETTINGS);
  expect(res.ok()).toBe(true);
  return ((await res.json()) as { design: unknown }).design;
}

async function restore(page: Page, design: unknown): Promise<void> {
  const res = await page.request.patch(SETTINGS, { data: { design } });
  expect(res.ok(), "could not put the café's design back").toBe(true);
}

/** The guest's welcome page, as the portal serves it — not the console's preview of it. */
async function guestPage(page: Page, lang: "en" | "vi"): Promise<string> {
  const res = await page.request.get(`${PORTAL_URL}/${lang}/`);
  expect(res.ok()).toBe(true);
  return res.text();
}

test.describe("the portal page builder", () => {
  test("an owner starts from a template, makes it theirs, and a guest reads it", async ({ page }) => {
    const before = await snapshot(page);
    try {
      await page.goto("/portal");
      await expect(page.locator(".block-list")).toBeVisible();

      // A page already exists, so a template is two deliberate steps, not one.
      await page.getByRole("button", { name: "Templates" }).click();
      await page.locator('dialog[open] [data-template="hotel"]').click();
      await expect(page.getByRole("heading", { name: "Replace your page with this template?" })).toBeVisible();
      await page.getByRole("button", { name: "Use this template" }).click();

      // The preview is the draft, before anything is saved.
      const preview = page.locator(".live-preview iframe");
      await expect(preview).toHaveAttribute("srcdoc", /15% off at the spa|Spa giảm 15%/);

      // The owner's own headline, written in Vietnamese.
      await page.locator(".writing-in").getByRole("button", { name: "Vietnamese" }).click();
      const headline = page.getByLabel("Headline in Vietnamese");
      await headline.fill("Chào mừng quý khách");
      await expect(preview).toHaveAttribute("srcdoc", /Chào mừng quý khách/);

      // A notice of their own, added from the block menu.
      await page.getByRole("button", { name: /Add a block/ }).click();
      await page.locator('.kind-option[data-kind="notice"]').click();
      await expect(page.locator(".block-editor input").first()).toBeFocused();
      await page.locator(".block-editor input").first().fill("Hồ bơi đóng cửa hôm nay");

      await page.getByRole("button", { name: "Save changes" }).click();
      await expect(page.getByText("Changes saved")).toBeVisible();

      /* The guest's document, from the portal. The Vietnamese reader gets the
         Vietnamese headline; the English reader keeps the template's English one —
         two languages, two headlines, never a merge. The notice exists only in
         Vietnamese, so both readers get it, and the English page says which
         language it is in. */
      const vi = await guestPage(page, "vi");
      expect(vi).toContain("Chào mừng quý khách");
      expect(vi).toContain("Hồ bơi đóng cửa hôm nay");
      expect(vi.indexOf("Hồ bơi đóng cửa hôm nay"), "a notice sits above the ways in").toBeLessThan(
        vi.indexOf('class="methods"'),
      );
      const en = await guestPage(page, "en");
      expect(en).not.toContain("Chào mừng quý khách");
      expect(en).toContain("Welcome to");
      expect(en).toMatch(/<section class="block block-notice" lang="vi"><p class="block-title">Hồ bơi đóng cửa hôm nay/);
    } finally {
      await restore(page, before);
    }
  });

  test("a link is offered only after sign-in, and a bad one cannot be saved", async ({ page }) => {
    const before = await snapshot(page);
    try {
      await page.goto("/portal");
      await expect(page.locator(".block-list")).toBeVisible();

      // Before sign-in the menu does not offer links at all.
      await page.getByRole("button", { name: /Add a block/ }).click();
      await expect(page.locator('.kind-option[data-kind="button"]')).toHaveCount(0);
      await expect(page.locator('.kind-option[data-kind="social"]')).toHaveCount(0);
      await page.getByRole("button", { name: /Add a block/ }).click();

      // After sign-in it does, and it says plainly who may never see the page.
      await page.locator('.segment[data-place="connected"]').click();
      await expect(page.getByText("Not every guest sees this page")).toBeVisible();
      await page.getByRole("button", { name: /Add a block/ }).click();
      await page.locator('.kind-option[data-kind="button"]').click();
      await page.getByLabel("Button text").fill("Đặt bàn");
      const link = page.getByLabel("Link", { exact: true });
      await link.fill("javascript:alert(1)");
      await link.blur();
      await expect(page.getByText("Use a web address starting https:// or a phone number starting tel:").first()).toBeVisible();

      // Save refuses, says why, and nothing reaches the server.
      let patched = false;
      page.on("request", (r) => {
        if (r.method() === "PATCH" && r.url().endsWith(SETTINGS)) patched = true;
      });
      await page.getByRole("button", { name: "Save changes" }).click();
      await expect(page.getByText("One block needs fixing before this can be saved.")).toBeVisible();
      expect(patched, "the console sent a design it knew was invalid").toBe(false);

      // A bare host is repaired to a link on blur — the one repair that cannot change what was meant.
      await link.fill("facebook.com/quan-cafe");
      await link.blur();
      await expect(link).toHaveValue("https://facebook.com/quan-cafe");
      await page.getByRole("button", { name: "Save changes" }).click();
      await expect(page.getByText("Changes saved")).toBeVisible();
    } finally {
      await restore(page, before);
    }
  });

  test("the server refuses what the console refuses, for a client that is not the console", async ({ page }) => {
    const refused = async (design: unknown, fault: string) => {
      const res = await page.request.patch(SETTINGS, { data: { design } });
      expect(res.status(), fault).toBe(422);
      const body = (await res.json()) as { params?: { fields?: Record<string, string> } };
      expect(Object.values(body.params?.fields ?? {}), fault).toContain(fault);
    };
    const one = (block: Record<string, unknown>) => ({
      accent: null,
      headline: {},
      blocks: [{ id: "x", kind: "text", placement: "welcome", visible: true, content: {}, ...block }],
    });
    await refused(one({ kind: "social", links: [] }), "link-before-sign-in");
    await refused(one({ kind: "button", placement: "connected", url: "javascript:alert(1)" }), "not-a-link");
    await refused(one({ kind: "button", placement: "connected", url: "mailto:a@b.c" }), "not-a-link");
    await refused(one({ startsOn: "2026-10-02", endsOn: "2026-10-01" }), "ends-before-start");
  });
});
