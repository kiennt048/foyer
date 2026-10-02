import { expect, test } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";

/**
 * The settings screens: what a venue
 * sets is up front, and every technical knob is behind one collapsed "Advanced"
 * section, in seconds and minutes rather than milliseconds, with its own default a
 * click away. Driven in a browser because *collapsed* and *visible* are browser
 * facts — markup alone says a `<details>` exists, not that nobody can see inside it.
 *
 * Each drive puts the mock back: a moved figure is reset to its default and saved.
 */
test.describe("settings, with the technical knobs behind Advanced", () => {
  test("/network opens on the portal's address, and the limits are one disclosure away, in seconds", async ({
    page,
    request,
  }) => {
    await page.goto("/network");
    await expect(page.getByRole("textbox", { name: "Portal address", exact: true })).toBeVisible();

    const guests = page.getByRole("group", { name: "Guest portal, per address" });
    const every = guests.getByLabel("Then one more every");
    // Collapsed: in the document, and not visible to anyone.
    await expect(every).toBeHidden();
    await expect(page.locator("details.advanced")).not.toHaveAttribute("open", "");

    await page.locator("details.advanced > summary").click();
    await expect(every).toBeVisible();
    // The mock ships one attempt a second on this bucket: 1 000 ms, read as 1 second.
    await expect(every).toHaveValue("1");
    await expect(guests.getByText("Between 0.1 and 600 seconds.")).toBeVisible();

    await every.fill("2.5");
    await expect(guests.getByRole("button", { name: "Reset to default" })).toBeVisible();
    await page.getByRole("button", { name: "Save limits and sizes" }).click();
    await expect(page.getByText("Saved. In force from the next request; nothing restarted.")).toBeVisible();

    // What the server holds is milliseconds: the console converted, it did not relabel.
    const held = await (await request.get(`${MOCK_URL}/api/v0/network/settings`)).json();
    expect(held.throttles.portalAddress.intervalMs).toBe(2500);

    await page.reload();
    await page.locator("details.advanced > summary").click();
    await expect(every).toHaveValue("2.5");

    // Reset goes to *this* bucket's default, which is not the per-account one.
    await guests.getByRole("button", { name: "Reset to default" }).click();
    await expect(every).toHaveValue("1");
    await page.getByRole("button", { name: "Save limits and sizes" }).click();
    await expect(page.getByText("Saved. In force from the next request; nothing restarted.")).toBeVisible();
    const back = await (await request.get(`${MOCK_URL}/api/v0/network/settings`)).json();
    expect(back.throttles.portalAddress).toEqual(back.defaults.throttles.portalAddress);
  });

  test("/privacy keeps retention and erasure up front and the maintenance schedule collapsed, in minutes", async ({
    page,
  }) => {
    await page.goto("/privacy");
    await expect(page.getByLabel("Session history")).toBeVisible();
    await expect(page.getByLabel("MAC address", { exact: true })).toBeVisible();

    const runEvery = page.getByLabel("Run every");
    await expect(runEvery).toBeHidden();
    await page.getByText("Maintenance schedule", { exact: true }).click();
    await expect(runEvery).toBeVisible();
    // 300 seconds, as the five minutes it is.
    await expect(runEvery).toHaveValue("5");
    await expect(page.getByText("Between 1 and 1440 minutes.")).toBeVisible();
  });
});
