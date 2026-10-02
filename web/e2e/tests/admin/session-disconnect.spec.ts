import { expect, test } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";
import { listNas, listSessions } from "../helpers/mock.js";

/**
 * The disconnect control, proven against a control **with behaviour**.
 *
 * `docs/rules/frontend.md`'s capability rule was learned the hard way: a gate
 * proven entirely against controls that did nothing proved nothing. So this does not
 * stop at "the button is there on a capable device and absent on an incapable one" —
 * it presses it, confirms, and asserts that **exactly one** `POST
 * /sessions/{id}/disconnect` left the browser, carrying the options the operator
 * ticked. The request is answered by the route here rather than by the mock, so the
 * shared mock world keeps its sessions for every spec after this one; the mock's own
 * behaviour is `web/mock/src/disconnect.test.ts`.
 *
 * The devices are selected from the live model by capability, never named, so a
 * fixture that moves a session moves the test with it.
 */

test.describe("disconnecting a guest from the session dock", () => {
  test("a capable device offers the control, and pressing it sends one disconnect", async ({
    page,
    request,
  }) => {
    const nas = await listNas(request);
    const sessions = await listSessions(request);
    const target = sessions.find(
      (s) => s.state === "online" && nas.find((n) => n.id === s.nasId)?.capabilities.canDeauthGuest === true,
    );
    expect(target, "no online session on a disconnect-capable device in the fixtures").toBeTruthy();

    const posts: { url: string; body: unknown }[] = [];
    await page.route("**/api/v0/sessions/*/disconnect", async (route) => {
      posts.push({ url: route.request().url(), body: route.request().postDataJSON() });
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ disconnected: true, grantsRevoked: 0, voucherRevoked: true }),
      });
    });

    await page.goto(`/sessions?session=${encodeURIComponent(target!.id)}`);
    await tableReady(page);
    const dock = page.locator(admin.detailDock);
    await expect(dock).toBeVisible();

    const control = dock.locator('[data-role="disconnect"] button');
    await expect(control, "a disconnect-capable device shows no disconnect control").toHaveCount(1);
    await control.click();

    /* The confirmation, outside the dock: its two options are the only inputs. */
    const dialog = page.locator("dialog[open]");
    await expect(dialog).toBeVisible();
    expect(posts, "opening the confirmation already sent a request").toEqual([]);
    await dialog.locator('input[data-role="disconnect-revoke-voucher"]').check();
    await dialog.locator(".dialog-foot button.btn-danger").click();

    await expect.poll(() => posts.length).toBe(1);
    expect(posts[0]!.url).toContain(`/sessions/${encodeURIComponent(target!.id)}/disconnect`);
    expect(posts[0]!.body).toEqual({ revokeVoucher: true, revokeGrant: false });

    /* And the dock says what happened, not what was intended. */
    await expect(dock.locator('[data-role="disconnect"] .notice')).toBeVisible();
  });

  test("a device that cannot end a session shows why, and no control", async ({ page, request }) => {
    const nas = await listNas(request);
    const sessions = await listSessions(request);
    const target = sessions.find(
      (s) => s.state !== "ended" && nas.find((n) => n.id === s.nasId)?.capabilities.canDeauthGuest === false,
    );
    expect(target, "no open session on a device that cannot disconnect in the fixtures").toBeTruthy();

    await page.goto(`/sessions?session=${encodeURIComponent(target!.id)}`);
    await tableReady(page);
    const panel = page.locator(`${admin.detailDock} [data-role="disconnect"]`);
    await expect(panel.locator(".notice"), "the missing control is not explained").toBeVisible();
    await expect(panel.locator("button")).toHaveCount(0);
  });
});
