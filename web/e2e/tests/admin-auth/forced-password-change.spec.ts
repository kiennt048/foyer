import { expect, test } from "@playwright/test";
import { ADMINISTRATOR, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The console's half of the forced password change
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * An operator on a temporary password somebody else set (`foyerctl create-operator
 * --must-change-password`) chooses their own before anything else. (No password ships
 * with the install — a fresh install shows the first-access setup.) The **server** half is locked twice and
 * neither lock is here: `internal/api`'s gate answers
 * `403 /problems/password-change-required` to every operation but three
 * (`temporary_password_test.go`), and `web/mock` carries the identical gate, driven in
 * `server.test.ts`.
 *
 * The **console** half is this file. `auth.tsx` routes to `/password` while
 * `session.mustChangePassword` is true and pulls the operator back on every
 * navigation — and nothing else drives it, because no fixture operator is on a
 * temporary password.
 *
 * ── what this proves, and what it deliberately does not ──────────────────────
 *
 * The session response is answered with one field altered, for this browser only.
 * That is the technique `create-first-venue.spec.ts` already uses for the no-venue
 * world, and it is honest about the same asymmetry:
 *
 *   * **What it proves.** That a console told it is on a temporary password sends
 *     the operator to `/password` on arrival, renders the change form, and puts them
 *     back there when they try to navigate anywhere else. The last is the whole point
 *     of the effect: a console that redirected once would let the operator click any
 *     destination and leave.
 *   * **What it does not.** That the server refuses, that the change lands, or that
 *     `mustChangePassword` stops being true afterwards. Those are the server's, they
 *     are already locked, and a browser driving them here would be re-testing the
 *     mock against itself.
 *
 * `mustChangePassword: false` is asserted first, on the real response, so this file
 * cannot quietly become a test of its own interception if a fixture operator is ever
 * marked: the day that happens the assertion fails and asks for the drive instead.
 */

test.describe("a console told it is on a temporary password", () => {
  /** Ends whatever session a test created. The mock's session map is shared. */
  test.afterEach(async ({ context }) => {
    await signOut(context);
  });

  test("is sent to /password, and pulled back there from every other route", async ({
    page,
    context,
  }) => {
    const session = await signInAs(context, ADMINISTRATOR);
    expect(
      session.mustChangePassword,
      "the fixture administrator is already on a temporary password, so the " +
        "interception below is now a double of the real state — drive this properly " +
        "instead of overriding it.",
    ).toBe(false);

    await page.route("**/api/v0/auth/session", async (route) => {
      /* Reads only: the `DELETE` in `afterEach` and the sign-in `POST` are the
         server's, and answering them here would make this a test of Playwright. */
      if (route.request().method() !== "GET") return route.fallback();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ...session, mustChangePassword: true }),
      });
    });

    await page.goto("/");
    await expect(
      page,
      "a console on a temporary password did not route to the screen that " +
        "exists to end that state — the operator meets the dashboard the server will " +
        "refuse every call from",
    ).toHaveURL(/\/password$/);
    await expect(page.locator("#current-password")).toBeVisible();
    await expect(page.locator("#new-password")).toBeVisible();

    /* Every destination, not one: the effect keys on `location.pathname`, and a
       single case would pass against a console that special-cased the route it
       was first sent to. */
    for (const destination of ["/sessions", "/vouchers", "/operators"]) {
      await page.goto(destination);
      await expect(
        page,
        `the console let the operator leave for ${destination} instead of holding them ` +
          "on the password screen",
      ).toHaveURL(/\/password$/);
    }
  });
});
