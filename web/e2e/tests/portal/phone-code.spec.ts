import { expect, test, type APIRequestContext } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";
import { recordNetwork } from "../helpers/network.js";
import { formField, openPortal } from "../helpers/portal.js";

/**
 * Phone sign-in's code field with JavaScript on.
 *
 * When the code field shared the voucher field's class, the enhancement script
 * regrouped six digits as a voucher (`231086` → `2310-86`), the field's `pattern`
 * refused the result, and the browser blocked the submit: a guest typing the right
 * code could not send it, and phone sign-in worked only with JavaScript off. No unit
 * test could see it — the markup was right and the server was right; what failed was
 * the browser running the one against the other. So the acceptance is a browser:
 * the code typed, pasted with a space, or pasted with a hyphen reaches "connected".
 *
 * Playwright is not the Captive Network Assistant (see `playwright.config.ts`), and
 * autofill from an SMS is not something it can produce; `fill` is the closest it
 * has — the value arriving whole, in one input event, as autofill delivers it.
 */

const SITE = "site-hcm";

async function json(request: APIRequestContext, method: "PUT" | "PATCH", path: string, data: unknown) {
  const res = await request.fetch(`${MOCK_URL}/api/v0${path}`, { method, data });
  if (!res.ok()) throw new Error(`${method} ${path} → ${res.status()} ${await res.text()}`);
  return res.json();
}

/** A number nobody else in this run has asked for, so the per-number limit is not what answers. */
function freshNumber(): string {
  return `09${String(Math.floor(Math.random() * 1e8)).padStart(8, "0")}`;
}

async function lastCodeTo(request: APIRequestContext, phone: string): Promise<string> {
  const res = await request.get(`${MOCK_URL}/mock/phone-outbox`);
  const outbox = (await res.json()) as { to: string; code: string }[];
  const e164 = `+84${phone.slice(1)}`;
  const mine = outbox.filter((m) => m.to === e164);
  const last = mine[mine.length - 1];
  if (!last) throw new Error(`no code was sent to ${e164}`);
  return last.code;
}

test.describe("portal, phone sign-in with JavaScript enabled", () => {
  let restoreMethods: string[] | null = null;

  test.beforeAll(async ({ request }) => {
    await json(request, "PUT", `/sites/${SITE}/portal/phone`, {
      provider: "esms-sms",
      brandname: "FOYER",
      dailyLimit: 100000,
      sessionMinutes: 60,
      messageTemplate: "{code} la ma Wi-Fi",
      apiKey: "e2e-api-key",
      secretKey: "e2e-secret-key",
    });
    const settings = (await (await request.get(`${MOCK_URL}/api/v0/sites/${SITE}/portal`)).json()) as {
      methods: string[];
    };
    if (!settings.methods.includes("phone")) {
      restoreMethods = settings.methods;
      await json(request, "PATCH", `/sites/${SITE}/portal`, { methods: [...settings.methods, "phone"] });
    }
  });

  test.afterAll(async ({ request }) => {
    // The mock is stateful and other specs count the welcome page's methods.
    if (restoreMethods) await json(request, "PATCH", `/sites/${SITE}/portal`, { methods: restoreMethods });
  });

  for (const [how, enter] of [
    ["typed digit by digit", (code: string) => code],
    ["pasted with a space", (code: string) => `${code.slice(0, 3)} ${code.slice(3)}`],
    ["pasted with a hyphen", (code: string) => `${code.slice(0, 3)}-${code.slice(3)}`],
  ] as const) {
    test(`a code ${how} reaches the connected page`, async ({ page, context }) => {
      const trace = await recordNetwork(page);
      const lang = await openPortal(page);
      await page.goto(`/${lang}/phone/`);

      const phone = freshNumber();
      await page.locator("input#phone").fill(phone);
      await Promise.all([
        page.waitForURL(new RegExp(`/${lang}/phone/\\?sent=1$`)),
        page.locator("form[data-portal-form] button[type=submit]").click(),
      ]);

      const code = await lastCodeTo(context.request, phone);
      const field = page.locator("input#otp");
      if (how === "typed digit by digit") {
        // Typed, so the enhancer's `input` handler runs on every keystroke — the
        // path that would regroup the digits as a voucher code.
        await field.pressSequentially(code, { delay: 10 });
      } else {
        await field.fill(enter(code));
      }
      // Never grouped for display: what is in the field is what the guest can send.
      await expect(field).toHaveValue(code);

      await Promise.all([
        page.waitForURL(new RegExp(`/${lang}/connected/?$`)),
        page.locator("form[data-portal-form] button[type=submit]").click(),
      ]);
      await expect(page.locator(".badge-ok")).toBeVisible();

      const post = trace.requests.find((r) => r.method === "POST" && r.url.includes("/api/v0/portal/phone/code"));
      expect(post, "the browser never submitted the code form").toBeDefined();
      expect(formField(post!.postData, "code")).toBe(code);
    });
  }
});
