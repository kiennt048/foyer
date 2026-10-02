import { describe, expect, it } from "vitest";
import type { PortalContext } from "@foyer/contract";
import { en } from "./i18n/en.js";
import { vi } from "./i18n/vi.js";
import { renderPage } from "./render.js";

const context = (methods: PortalContext["methods"]): PortalContext => ({
  venueName: "Cà phê Số 7",
  networkName: null,
  logoDataUri: null,
  heroImageUrl: null,
  languages: ["vi", "en"],
  methods,
  terms: { summary: {} },
});

describe("phone sign-in page", () => {
  it("is offered on the welcome page only where the venue offers it", () => {
    const on = renderPage({ lang: "vi", path: "", context: context(["phone"]), status: null, error: null })!;
    expect(on.body).toContain('href="/vi/phone/"');
    expect(on.body).toContain(vi["welcome.phone"]);
    const off = renderPage({ lang: "vi", path: "", context: context(["voucher"]), status: null, error: null })!;
    expect(off.body).not.toContain("/vi/phone/");
  });

  it("renders the number form, and the code form only with ?sent=1", () => {
    const number = renderPage({ lang: "en", path: "phone/", context: context(["phone"]), status: null, error: null })!;
    expect(number.body).toContain('action="/api/v0/portal/phone"');
    expect(number.body).toContain('type="tel"');
    expect(number.body).not.toContain("portal/phone/code");

    const code = renderPage({
      lang: "en", path: "phone/", context: context(["phone"]), status: null, error: "code-wrong", phoneSent: true,
    })!;
    expect(code.body).toContain('action="/api/v0/portal/phone/code"');
    expect(code.body).toContain('autocomplete="one-time-code"');
    expect(code.body).toContain(en["error.code-wrong"]);
  });

  it("gives the code field its own class, never the voucher's", () => {
    const body = renderPage({
      lang: "en", path: "phone/", context: context(["phone"]), status: null, error: null, phoneSent: true,
    })!.body;
    const tag = /<input[^>]*id="otp"[^>]*>/.exec(body)![0];
    // `code-input` is what the enhancer groups into XXXX-XXXX-XXXX.
    expect(tag).not.toContain("code-input");
    expect(tag).toContain('class="otp-input"');
    const pattern = new RegExp(`^(?:${/pattern="([^"]*)"/.exec(tag)![1]})$`, "v");
    for (const typed of ["231086", "231 086", "231-086", "231 086"]) expect(pattern.test(typed)).toBe(true);
    expect(pattern.test("23108a")).toBe(false);
  });

  it("states the venue's code lifetime, not a hard-coded five minutes", () => {
    const at = (m: number | null, lang: "en" | "vi") =>
      renderPage({
        lang, path: "phone/", context: { ...context(["phone"]), phoneCodeMinutes: m }, status: null, error: null, phoneSent: true,
      })!.body;
    expect(at(2, "en")).toContain("It works for 2 min.");
    expect(at(2, "en")).not.toContain("5 min");
    expect(at(2, "vi")).toContain("hiệu lực trong 2 phút");
    expect(at(null, "en")).toContain("It works for 5 min.");
    expect(at(2, "en")).not.toContain("{minutes}");
  });

  it("has copy for every phone refusal in both languages, and they are not each other", () => {
    for (const k of ["error.phone-invalid", "error.code-wrong", "error.code-expired", "error.code-limit", "error.code-unavailable"] as const) {
      expect(en[k]).toBeTruthy();
      expect(vi[k]).toBeTruthy();
      expect(vi[k]).not.toBe(en[k]);
    }
    // The two refusals a guest acts on differently must not read the same.
    expect(en["error.code-wrong"]).not.toBe(en["error.code-expired"]);
    expect(vi["error.code-wrong"]).not.toBe(vi["error.code-expired"]);
  });
});
