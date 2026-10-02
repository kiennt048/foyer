import { expect, test } from "@playwright/test";
import { MOCK_ACCOUNT_PASSWORD } from "../helpers/env.js";
import { freshVoucherCode } from "../helpers/mock.js";
import { describeTrace, recordNetwork, scriptedApiCalls } from "../helpers/network.js";
import {
  enabledAccountUsername,
  formField,
  openPortal,
  portal,
  ungroup,
} from "../helpers/portal.js";

/**
 * ════════════════════════════════════════════════════════════════════════════
 *  THE ONE TEST THIS SUITE EXISTS FOR — READ BEFORE SIMPLIFYING IT
 * ════════════════════════════════════════════════════════════════════════════
 *
 * `docs/protocol-reality.md` records the failure this file is built around:
 *
 *   > The iOS/macOS Captive Network Assistant re-checks captivity **only after a
 *   > full URL navigation, or on a timer**. A `fetch`/XHR call does not trigger a
 *   > re-check. If no full navigation occurs the CNA's button stays **"Cancel"** —
 *   > and Cancel **disassociates the device from the SSID**.
 *
 * So the canonical modern build — submit by `fetch`, re-render the success state —
 * authenticates the guest and then throws them off the network, offering only the
 * button that does it. Android reaches the same conclusion from the other side: it
 * calls `reevaluateNetwork()` from `onPageStarted`/`onPageFinished` only.
 *
 * ── Why the obvious test would be worthless ──────────────────────────────────
 *
 * The obvious test is:
 *
 *     await page.fill('input.code-input', code);
 *     await page.click('button[type=submit]');
 *     await expect(page.locator('.badge-ok')).toBeVisible();
 *
 * That assertion passes **identically** whether the portal did a real form POST or
 * a `fetch()` followed by a client-side re-render. Both end with the success
 * markup on screen. The first ships; the second is broken on every iPhone in the
 * building — and Playwright reports it green, because Playwright is not a CNA and
 * has no opinion about captivity at all.
 *
 * A test that cannot distinguish the working product from the broken one is not a
 * weak test. It is a **negative-value** test: it converts the most expensive bug in
 * the project into a green tick.
 *
 * ── So this test asserts the mechanism, not the outcome ──────────────────────
 *
 *   1. The request to the authenticating endpoint is a **document-type navigation
 *      request** (`resourceType === "document"`, `isNavigationRequest()`), not a
 *      subresource. A `fetch()` is `resourceType === "fetch"` and is never a
 *      navigation — that single field is the whole difference.
 *   2. It is answered **303**, with a `Location` — the redirect is what produces the
 *      second document, and the second document is what re-triggers the captivity
 *      check.
 *   3. A **`framenavigated` and a `load`** follow it. A 303 nobody followed proves
 *      nothing.
 *   4. **No `fetch`, XHR or `sendBeacon` touches the API at all.** `fetch` and
 *      friends are wrapped from inside the page before any page script runs (see
 *      `helpers/network.ts`), so an enhancement script that "helpfully" intercepts
 *      the submit is caught even if it also happens to trigger a navigation.
 *
 * ── The honest limit ─────────────────────────────────────────────────────────
 *
 * **Playwright cannot emulate the Captive Network Assistant.** This test proves the
 * portal still submits the way the CNA needs. It does not prove the CNA is happy —
 * only a real device inside a real CNA does that, and that test is not skippable. This test's job is to stop a regression in something already
 * understood, not to substitute for the measurement.
 *
 * ════════════════════════════════════════════════════════════════════════════
 */

const AUTHENTICATING_ENDPOINTS = ["/api/v0/portal/voucher", "/api/v0/portal/account"];

test.describe("the portal authenticates by navigation, never by XHR", () => {
  test("voucher submit is a document navigation answered 303", async ({ page, context }) => {
    const trace = await recordNetwork(page);
    const code = await freshVoucherCode(context.request);

    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);
    await page.locator(portal.codeInput).fill(code);

    await Promise.all([
      page.waitForURL(new RegExp(`/${lang}/connected/?$`)),
      page.locator(portal.submit).click(),
    ]);

    const endpoint = "/api/v0/portal/voucher";
    const dump = () => `\n\nNetwork trace:\n${describeTrace(trace)}\n`;

    /* 1 — the request itself. `resourceType` is the load-bearing field: a form POST
       is "document", a fetch() is "fetch". Nothing else in the trace tells them
       apart, which is exactly why a DOM assertion cannot see this failure. */
    const request = trace.requests.find((r) => r.method === "POST" && r.url.includes(endpoint));
    expect(request, `No POST to ${endpoint} was made at all.${dump()}`).toBeDefined();
    expect(
      request!.resourceType,
      `The login POST was issued as "${request!.resourceType}", not a document ` +
        `navigation. If this is "fetch" or "xhr", the portal has become a single-page ` +
        `app and the CNA will never re-check captivity: the guest is authenticated and ` +
        `then offered "Cancel", which disassociates them from the SSID.${dump()}`,
    ).toBe("document");
    expect(
      request!.isNavigation,
      `The login POST was not a navigation request.${dump()}`,
    ).toBe(true);

    /* The form carried the code, ungrouped. Proof it was a real form submission and
       not a hand-built body. */
    expect(formField(request!.postData, "code")).toBe(ungroup(code));

    /* 2 — answered 303 with a Location. */
    const response = trace.responses.find((r) => r.method === "POST" && r.url.includes(endpoint));
    expect(response, `No response recorded for the login POST.${dump()}`).toBeDefined();
    expect(
      response!.status,
      `The login POST was answered ${response!.status}, not 303. The success path has ` +
        `to be POST → 303 → new document; anything else is not a navigation the CNA ` +
        `can observe.${dump()}`,
    ).toBe(303);
    expect(response!.location, `The 303 carried no Location header.${dump()}`).toBeTruthy();
    expect(response!.location).toContain("/connected/");

    /* 3 — and something actually navigated afterwards. A 303 nobody followed is not
       a navigation; it is a status code. */
    const navigatedAfter = trace.frames.filter(
      (f) => f.seq > response!.seq && f.kind === "framenavigated",
    );
    const loadedAfter = trace.frames.filter((f) => f.seq > response!.seq && f.kind === "load");
    expect(
      navigatedAfter.length,
      `No frame navigation followed the 303.${dump()}`,
    ).toBeGreaterThan(0);
    expect(loadedAfter.length, `No document load followed the 303.${dump()}`).toBeGreaterThan(0);
    expect(loadedAfter.some((f) => /\/connected\/?$/.test(new URL(f.url).pathname))).toBe(true);

    /* 4 — and nothing in the page asked for the API by script. */
    const scripted = scriptedApiCalls(trace);
    expect(
      scripted,
      `Page JavaScript called the API directly. The portal's flow must not depend on ` +
        `— or be intercepted by — script.${dump()}`,
    ).toEqual([]);

    /* The success document is the one that rendered. */
    await expect(page.locator(portal.connectedBadge)).toBeVisible();
  });

  test("account submit is a document navigation answered 303", async ({ page, context }) => {
    const trace = await recordNetwork(page);
    const username = await enabledAccountUsername(context.request);

    const lang = await openPortal(page);
    await page.goto(`/${lang}/account/`);
    await page.locator(portal.usernameInput).fill(username);
    /* The mock verifies this, so the value has to be the right one. It was
       `whatever-the-mock-ignores`, and the name was accurate. */
    await page.locator(portal.passwordInput).fill(MOCK_ACCOUNT_PASSWORD);

    await Promise.all([
      page.waitForURL(new RegExp(`/${lang}/connected/?$`)),
      page.locator(portal.submit).click(),
    ]);

    const endpoint = "/api/v0/portal/account";
    const dump = () => `\n\nNetwork trace:\n${describeTrace(trace)}\n`;

    const request = trace.requests.find((r) => r.method === "POST" && r.url.includes(endpoint));
    expect(request, `No POST to ${endpoint}.${dump()}`).toBeDefined();
    expect(request!.resourceType, `Account login was not a document navigation.${dump()}`).toBe(
      "document",
    );
    expect(request!.isNavigation).toBe(true);

    const response = trace.responses.find((r) => r.method === "POST" && r.url.includes(endpoint));
    expect(response!.status, `Account login was answered ${response!.status}.${dump()}`).toBe(303);
    expect(response!.location).toContain("/connected/");

    expect(scriptedApiCalls(trace)).toEqual([]);
    await expect(page.locator(portal.connectedBadge)).toBeVisible();
  });

  /**
   * The negative half, stated separately so it survives a refactor of the tests
   * above: across a complete walk of the flow, page script never touches an
   * authenticating endpoint.
   */
  test("no fetch or XHR is ever issued to an authenticating endpoint", async ({
    page,
    context,
  }) => {
    const trace = await recordNetwork(page);
    const code = await freshVoucherCode(context.request);

    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);
    await page.locator(portal.codeInput).fill(code);
    await Promise.all([
      page.waitForURL(new RegExp(`/${lang}/connected/?$`)),
      page.locator(portal.submit).click(),
    ]);

    const offenders = trace.scripted.filter((c) =>
      AUTHENTICATING_ENDPOINTS.some((e) => c.url.includes(e)),
    );
    expect(
      offenders,
      `Page script called an authenticating endpoint. Even if a navigation also ` +
        `happens, an intercepted submit is how this regresses.\n${describeTrace(trace)}`,
    ).toEqual([]);

    /* Nothing scripted at all, in fact — the enhancement script is decoration over
       a form that already works. It calls `preventDefault` nowhere. */
    expect(scriptedApiCalls(trace)).toEqual([]);
  });
});
