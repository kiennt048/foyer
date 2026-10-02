import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  accentCss,
  accentTheme,
  ACCENT_PRESETS,
  contrast,
  designFault,
  narrowDesign,
  PORTAL_SURFACES,
} from "@foyer/contract";
import type { PortalBlock, PortalContext, PortalDesign, PortalStatus } from "@foyer/contract";
import { renderPage } from "./render.js";

/**
 * The venue's page, rendered — the portal page builder (`docs/architecture.md`).
 *
 * The Go renderer is held to the same markup and the same colour vectors, so a
 * change here that is not a change there is a guest who sees two different pages
 * depending on which binary answered.
 */

const TODAY = "2026-09-23";

const block = (b: Partial<PortalBlock> & Pick<PortalBlock, "id">): PortalBlock => ({
  kind: "text",
  placement: "welcome",
  visible: true,
  content: {},
  ...b,
});

const context = (design: PortalDesign): PortalContext => ({
  venueName: "Cà phê Số 7",
  methods: ["voucher"],
  languages: ["vi", "en"],
  terms: { summary: {} },
  design,
});

const connected: PortalStatus = {
  state: "connected",
  plan: { name: "1 giờ", durationMinutes: 60, enforcedBy: "nas" } as PortalStatus["plan"],
};

const vectors = JSON.parse(
  readFileSync(new URL("./theme.vectors.json", import.meta.url), "utf8"),
) as Array<{ accent: string; light: object; dark: object; css: string }>;

/** A colour that must be darkened on the light page, taken from the vectors. */
const PALE = vectors.find((v) => (v.light as { accent: string }).accent !== v.accent)!.accent;

function welcome(design: PortalDesign, lang: "en" | "vi" = "vi"): string {
  return renderPage({ lang, path: "", context: context(design), status: null, error: null, today: TODAY })!.body;
}

function success(design: PortalDesign, lang: "en" | "vi" = "vi"): string {
  return renderPage({
    lang,
    path: "connected/",
    context: context(design),
    status: connected,
    error: null,
    today: TODAY,
  })!.body;
}

describe("the brand colour", () => {
  it("matches the vectors the Go renderer is held to", () => {
    expect(vectors.length).toBeGreaterThan(10);
    for (const v of vectors) {
      const theme = accentTheme(v.accent);
      expect(theme.light, v.accent).toEqual(v.light);
      expect(theme.dark, v.accent).toEqual(v.dark);
      expect(accentCss(v.accent), v.accent).toBe(v.css);
    }
  });

  it("is legible whatever colour the owner picks, presets included", () => {
    const picks = [...vectors.map((v) => v.accent), ...ACCENT_PRESETS.flatMap((p) => (p.hex ? [p.hex] : []))];
    for (const hex of picks) {
      const t = accentTheme(hex);
      expect(contrast(t.light.accent, PORTAL_SURFACES.lightSurface), hex).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.light.accent, PORTAL_SURFACES.lightBg), hex).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.dark.accent, PORTAL_SURFACES.darkBg), hex).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.dark.accent, PORTAL_SURFACES.darkSurface), hex).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.light.accent, t.light.contrast), hex).toBeGreaterThanOrEqual(4.5);
      // The "Connected" badge: accent text on the accent's own wash.
      expect(contrast(t.light.accent, t.light.wash), hex).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.dark.accent, t.dark.wash), hex).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.dark.accent, t.dark.contrast), hex).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("is emitted before the shell on every page, and not at all without a colour", () => {
    const design: PortalDesign = { accent: PALE, headline: {}, blocks: [] };
    for (const path of ["", "voucher/", "account/", "problem/"]) {
      const body = renderPage({ lang: "en", path, context: context(design), status: null, error: null })!.body;
      expect(body.startsWith(`<style>${accentCss(PALE)}</style><div class="shell">`), path).toBe(true);
    }
    expect(welcome({ accent: null, headline: {}, blocks: [] })).not.toContain("<style>");
  });

  it("refuses a colour that is not a colour, because it lands inside <style>", () => {
    const body = welcome({ accent: "red}body{display:none", headline: {}, blocks: [] });
    expect(body).not.toContain("<style>");
  });
});

describe("the headline", () => {
  it("replaces Foyer's heading, and says which language it is in when it falls back", () => {
    const design: PortalDesign = { accent: null, headline: { vi: "Chào mừng" }, blocks: [] };
    expect(welcome(design, "vi")).toContain("<h1>Chào mừng</h1>");
    expect(welcome(design, "en")).toContain('<h1 lang="vi">Chào mừng</h1>');
    expect(welcome({ accent: null, headline: { vi: "  " }, blocks: [] }, "en")).toContain("<h1>Get online</h1>");
  });
});

describe("the blocks", () => {
  const design: PortalDesign = {
    accent: null,
    headline: {},
    blocks: [
      block({ id: "n", kind: "notice", content: { vi: { title: "Wi-Fi miễn phí" } } }),
      block({ id: "o", kind: "offer", code: "LY2", content: { vi: { title: "Giảm 20%", body: "Dòng 1\nDòng 2" } } }),
      block({
        id: "l",
        kind: "list",
        content: { vi: { title: "Món", items: [{ label: "Cà phê", value: "29.000đ" }, { label: " " }] } },
      }),
      block({ id: "hidden", visible: false, content: { vi: { title: "BẢN NHÁP" } } }),
      block({ id: "old", endsOn: "2026-09-22", content: { vi: { title: "HẾT HẠN" } } }),
      block({ id: "later", startsOn: "2026-09-24", content: { vi: { title: "CHƯA TỚI" } } }),
      block({ id: "empty", content: { vi: { title: "  " } } }),
      block({
        id: "b",
        kind: "button",
        placement: "connected",
        url: "https://g.page/r/x",
        content: { vi: { label: "Đánh giá" } },
      }),
      block({
        id: "s",
        kind: "social",
        placement: "connected",
        links: [
          { network: "facebook", url: "https://facebook.com/x" },
          { network: "phone", url: "tel:+84900000000" },
          { network: "website", url: "javascript:alert(1)" },
        ],
        content: {},
      }),
    ],
  };

  it("puts a notice above the ways in, and everything else below them", () => {
    const body = welcome(design);
    const notice = body.indexOf("block-notice");
    const label = body.indexOf('class="section-label"');
    const methods = body.indexOf('class="methods"');
    const offer = body.indexOf("block-offer");
    const terms = body.indexOf('class="terms"');
    expect(notice).toBeGreaterThan(0);
    expect(notice).toBeLessThan(label);
    expect(offer).toBeGreaterThan(methods);
    expect(offer).toBeLessThan(terms);
    expect(body).toContain('<div class="blocks blocks-notice">');
  });

  it("renders the spec's markup", () => {
    const body = welcome(design);
    expect(body).toContain(
      '<section class="block block-offer"><h2 class="block-title">Giảm 20%</h2><p class="block-body">Dòng 1\nDòng 2</p>' +
        '<p class="block-code"><span class="block-code-label">Mã</span><span class="block-code-value">LY2</span></p></section>',
    );
    expect(body).toContain(
      '<ul class="block-items"><li><span class="block-item-label">Cà phê</span><span class="block-item-value">29.000đ</span></li></ul>',
    );
  });

  it("never shows a guest a hidden, finished, future or empty block", () => {
    const body = welcome(design);
    for (const word of ["BẢN NHÁP", "HẾT HẠN", "CHƯA TỚI"]) expect(body).not.toContain(word);
    expect(body.match(/class="block /g)?.length).toBe(3);
  });

  it("keeps links off the page a guest reads before they have internet", () => {
    const body = welcome(design);
    expect(body).not.toContain("block-button");
    expect(body).not.toContain("block-social");
  });

  it("puts links on the connected page, and only real ones", () => {
    const body = success(design);
    expect(body).toContain(
      '<a class="block block-button" href="https://g.page/r/x" rel="noopener noreferrer">Đánh giá</a>',
    );
    expect(body).toContain('href="tel:+84900000000"');
    expect(body).toContain(">Facebook</a>");
    expect(body).not.toContain("javascript:");
    expect(body.indexOf("block-button")).toBeLessThan(body.indexOf("close-hint"));
  });

  it("shows no venue content to a guest who is not connected", () => {
    const body = renderPage({
      lang: "vi",
      path: "connected/",
      context: context(design),
      status: { ...connected, state: "revoked" },
      error: null,
      today: TODAY,
    })!.body;
    expect(body).not.toContain("block-button");
  });

  it("escapes what the venue typed", () => {
    const body = welcome({
      accent: null,
      headline: { vi: "<script>alert(1)</script>" },
      blocks: [block({ id: "x", content: { vi: { title: '<img src=x onerror="alert(1)">' } } })],
    });
    expect(body).not.toContain("<script>");
    expect(body).not.toContain("<img src=x");
    expect(body).toContain("&lt;script>");
  });

  it("marks a block written only in the other language", () => {
    expect(welcome(design, "en")).toContain('<section class="block block-notice" lang="vi">');
  });
});

describe("the rules the server applies", () => {
  const d = (blocks: PortalBlock[]): PortalDesign => ({ accent: null, headline: {}, blocks });

  it("refuses what the spec refuses, in its order", () => {
    expect(designFault(d([block({ id: "a" }), block({ id: "a" })]))?.fault).toBe("duplicate");
    expect(designFault(d([block({ id: "a", kind: "social", links: [] })]))?.fault).toBe("link-before-sign-in");
    expect(designFault(d([block({ id: "a", kind: "button", placement: "connected" })]))?.fault).toBe("required");
    for (const url of ["ftp://x", "mailto:a@b.c", "javascript:alert(1)", "//evil.example", "https://u@evil.example"]) {
      expect(designFault(d([block({ id: "a", kind: "button", placement: "connected", url })]))?.fault, url).toBe(
        "not-a-link",
      );
    }
    expect(designFault(d([block({ id: "a", startsOn: "2026-10-02", endsOn: "2026-10-01" })]))?.fault).toBe(
      "ends-before-start",
    );
    expect(designFault(d([block({ id: "a" })]))).toBeNull();
  });

  it("narrows inclusively on both dates", () => {
    expect(narrowDesign(d([block({ id: "a", startsOn: TODAY, endsOn: TODAY })]), TODAY).blocks).toHaveLength(1);
  });
});

describe("the templates an owner starts from", () => {
  it("are valid designs the server would accept, within the contract's limits", async () => {
    const { TEMPLATES } = await import("./templates.js");
    expect(TEMPLATES.length).toBeGreaterThanOrEqual(6);
    for (const tpl of TEMPLATES) {
      const d = tpl.build("Một cái tên quán rất dài để thử giới hạn sáu mươi ký tự của tiêu đề");
      expect(designFault(d), tpl.id).toBeNull();
      expect(d.blocks.length, tpl.id).toBeLessThanOrEqual(12);
      for (const l of ["en", "vi"] as const) expect((d.headline[l] ?? "").length, tpl.id).toBeLessThanOrEqual(60);
      for (const b of d.blocks) {
        for (const text of Object.values(b.content)) {
          expect((text?.title ?? "").length, `${tpl.id} ${b.id}`).toBeLessThanOrEqual(80);
          expect((text?.body ?? "").length, `${tpl.id} ${b.id}`).toBeLessThanOrEqual(500);
          expect((text?.label ?? "").length, `${tpl.id} ${b.id}`).toBeLessThanOrEqual(40);
          for (const item of text?.items ?? []) {
            expect(item.label.length).toBeLessThanOrEqual(60);
            expect((item.value ?? "").length).toBeLessThanOrEqual(30);
          }
        }
        expect(b.id, tpl.id).toMatch(/^[a-z0-9-]{1,40}$/);
      }
    }
  });

  it("never publish a link the owner has not written", async () => {
    const { TEMPLATES } = await import("./templates.js");
    for (const tpl of TEMPLATES) {
      for (const b of tpl.build("Quán").blocks) {
        if (b.kind === "button" || b.kind === "social") {
          expect(b.visible, `${tpl.id}: a template link is shown before it has an address`).toBe(false);
          expect(b.placement, tpl.id).toBe("connected");
        }
      }
    }
  });

  it("speak both of the portal's languages, and render something before sign-in", async () => {
    const { TEMPLATES } = await import("./templates.js");
    for (const tpl of TEMPLATES.filter((x) => x.id !== "blank")) {
      const d = tpl.build("Quán");
      for (const b of d.blocks) {
        expect(Object.keys(b.content).sort(), `${tpl.id} ${b.kind}`).toEqual(["en", "vi"]);
      }
      for (const lang of ["en", "vi"] as const) {
        const body = welcome(d, lang);
        expect(body.match(/class="block /g)?.length ?? 0, `${tpl.id} ${lang}`).toBeGreaterThan(0);
        // Nothing falls back to the other language: every block and the headline are
        // written in both. (The language switcher's own `lang` is not venue text.)
        expect(body, `${tpl.id} ${lang}`).not.toMatch(/class="block[^"]*" lang=|<h1 lang=/);
      }
    }
  });
});
