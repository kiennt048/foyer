import { describe, expect, it } from "vitest";
import type { PortalContext } from "@foyer/contract";
import { renderPage } from "./render.js";

/**
 * What a venue asks for at sign-in, rendered. The Go twin is
 * held to the same rules in `internal/portal/render_guestdata_test.go`.
 */

const VERSION = "ab".repeat(32);

const context = (consent: boolean): PortalContext => ({
  venueName: "Cà phê Số 7",
  methods: ["voucher", "account", "click-through"],
  languages: ["vi", "en"],
  terms: { summary: {} },
  dataCollection: {
    fields: [
      { field: "name", required: false },
      { field: "email", required: true },
    ],
    marketingConsent: consent ? { text: { vi: "Gửi ưu đãi cho tôi qua email." }, versions: { vi: VERSION } } : null,
  },
});

const render = (path: string, ctx: PortalContext, extra: { notice?: string; connected?: boolean } = {}) =>
  renderPage({
    lang: "en",
    path,
    context: ctx,
    status: extra.connected
      ? { state: "connected", plan: { enforcedBy: "nas", durationMinutes: 60 } }
      : null,
    error: null,
    notice: extra.notice ?? null,
  })!.body;

describe("guest details and marketing consent", () => {
  it("never pre-ticks the box, and names the sentence it shows", () => {
    for (const path of ["", "voucher/", "account/"]) {
      const body = render(path, context(true));
      const box = /<input[^>]*name="marketingConsent"[^>]*>/.exec(body)?.[0];
      expect(box, path).toBeDefined();
      expect(box, `${path}: the box is pre-ticked`).not.toMatch(/checked/);
      expect(box).toContain('value="yes"');
      expect(body).toContain(`name="consentVersion" value="${VERSION}"`);
      // Vietnamese sentence on an English page: marked with its own language.
      expect(body).toMatch(/<label for="g-consent" lang="vi">Gửi ưu đãi cho tôi qua email\.<\/label>/);
      expect(/<input[^>]*name="email"[^>]*>/.exec(body)?.[0]).toMatch(/required/);
      expect(/<input[^>]*name="name"[^>]*>/.exec(body)?.[0]).not.toMatch(/required/);
    }
  });

  it("renders no box where the venue does not ask", () => {
    expect(render("voucher/", context(false))).not.toContain("marketingConsent");
    expect(render("connected/", context(false), { connected: true })).not.toContain("marketing-consent/withdraw");
  });

  it("puts the way out on the connected page and confirms it", () => {
    const body = render("connected/", context(true), { connected: true, notice: "consent-withdrawn" });
    expect(body).toContain('action="/api/v0/portal/marketing-consent/withdraw"');
    expect(body).toContain('role="status"');
    expect(render("connected/", context(true), { connected: true, notice: "<b>" })).not.toContain('role="status"');
  });
});

describe("phone sign-in's code form", () => {
  it("asks for the venue's details and its box, but never for the number the code proved", () => {
    const ctx = context(true);
    ctx.methods = ["phone"];
    ctx.dataCollection!.fields = [
      { field: "name", required: true },
      { field: "phone", required: true },
    ];
    const body = renderPage({ lang: "en", path: "phone/", context: ctx, status: null, error: null, phoneSent: true })!.body;
    const form = body.slice(body.indexOf('action="/api/v0/portal/phone/code"'));
    const codeForm = form.slice(0, form.indexOf("</form>"));
    expect(codeForm).toContain('name="name"');
    expect(codeForm).toContain('name="marketingConsent"');
    expect(codeForm).not.toContain('name="phone"');
  });
});
