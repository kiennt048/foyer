/**
 * The portal page design — the rules every implementation shares.
 *
 * The console validates and previews with these, the mock refuses with them, and
 * the portal renders with them. The Go backend implements the same rules in
 * `internal/api` and `internal/portal`; the design both sides are written to is
 * described in `docs/architecture.md`, and the colour
 * derivation is pinned across the two languages by `web/portal/src/theme.vectors.json`.
 */
import type { components } from "./generated/types.js";

type S = components["schemas"];

export type PortalDesign = S["PortalDesign"];
export type PortalBlock = S["PortalBlock"];
export type PortalBlockKind = S["PortalBlockKind"];
export type PortalBlockPlacement = S["PortalBlockPlacement"];
export type PortalBlockText = S["PortalBlockText"];
export type PortalSocialNetwork = S["PortalSocialNetwork"];
export type PortalSocialLink = S["PortalSocialLink"];

type Lang = S["Language"];

/** Twelve at most — the contract's `maxItems`, restated once here for the console. */
export const DESIGN_MAX_BLOCKS = 12;

export const EMPTY_DESIGN: PortalDesign = { accent: null, headline: {}, blocks: [] };

/** Kinds that carry a link, and therefore exist only after sign-in. */
export const LINK_KINDS: readonly PortalBlockKind[] = ["button", "social"];

export function isLinkKind(kind: PortalBlockKind): boolean {
  return LINK_KINDS.includes(kind);
}

/*
 * The http pattern is `PortalStatus.originalUrl`'s: no userinfo, no whitespace, a
 * host. `tel:` because "call us" is the link a small venue most often wants that is
 * not a web address, and it is the one non-http scheme Android's captive-portal
 * WebView opens — `mailto:` is hard-blocked there (`docs/rules/frontend.md`).
 */
const HTTP_LINK = /^https?:\/\/[^/?#@\s]+(?:[/?#][^\s]*)?$/;
const TEL_LINK = /^tel:\+?[0-9][0-9 .()-]{2,19}$/;

export function isLink(url: string | null | undefined): boolean {
  if (!url) return false;
  return HTTP_LINK.test(url) || TEL_LINK.test(url);
}

export interface DesignFault {
  fault: "duplicate" | "link-before-sign-in" | "required" | "not-a-link" | "ends-before-start";
  /** Index into `blocks`. */
  block: number;
}

/**
 * The semantic checks beyond the schema, in the order the server applies them.
 * First failure wins, which is what the server answers too.
 */
export function designFault(design: PortalDesign): DesignFault | null {
  const seen = new Set<string>();
  for (const [i, b] of design.blocks.entries()) {
    if (seen.has(b.id)) return { fault: "duplicate", block: i };
    seen.add(b.id);
  }
  for (const [i, b] of design.blocks.entries()) {
    if (isLinkKind(b.kind) && b.placement === "welcome") return { fault: "link-before-sign-in", block: i };
  }
  /* Links are checked only on blocks a guest could see. A hidden block is a draft,
     and a template's "Review us" button starts hidden with no link in it — refusing
     that would refuse the template. It can never reach a guest: hidden blocks are
     narrowed out before rendering, and the renderer checks every link again. */
  for (const [i, b] of design.blocks.entries()) {
    if (b.visible && b.kind === "button" && !b.url?.trim()) return { fault: "required", block: i };
  }
  for (const [i, b] of design.blocks.entries()) {
    if (!b.visible) continue;
    if (b.kind === "button" && !isLink(b.url)) return { fault: "not-a-link", block: i };
    if (b.links?.some((l) => !isLink(l.url))) return { fault: "not-a-link", block: i };
  }
  for (const [i, b] of design.blocks.entries()) {
    if (b.startsOn && b.endsOn && b.endsOn < b.startsOn) return { fault: "ends-before-start", block: i };
  }
  return null;
}

/**
 * `YYYY-MM-DD` for the calendar day in `timeZone` — the **site's** (`getSiteTimeZone`),
 * which is the day the Go server compares a block's dates against. Without one it is
 * this process's local day, which is right only where the process runs at the venue.
 */
export function localDate(now: Date = new Date(), timeZone?: string): string {
  if (timeZone) {
    try {
      /* en-CA formats as YYYY-MM-DD. */
      return new Intl.DateTimeFormat("en-CA", { timeZone, year: "numeric", month: "2-digit", day: "2-digit" }).format(now);
    } catch {
      /* A zone this runtime does not know falls back to the local day. */
    }
  }
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

/** Whether a block is shown on `today`: visible, and inside its dates. */
export function isLive(block: PortalBlock, today: string): boolean {
  if (!block.visible) return false;
  if (block.startsOn && block.startsOn > today) return false;
  if (block.endsOn && block.endsOn < today) return false;
  return true;
}

/**
 * The design a guest may read. Hidden and out-of-date blocks are not sent at all,
 * so a promotion an owner switched off is not readable pre-authentication.
 */
export function narrowDesign(design: PortalDesign, today: string): PortalDesign {
  return { ...design, blocks: design.blocks.filter((b) => isLive(b, today)) };
}

export function hasText(text: PortalBlockText | undefined): boolean {
  if (!text) return false;
  return Boolean(
    text.title?.trim() ||
      text.body?.trim() ||
      text.label?.trim() ||
      text.items?.some((i) => i.label.trim()),
  );
}

/**
 * The block's words for `lang`: its own if it has any, else the other language's,
 * carrying that language. Never a translation and never a merge.
 */
export function resolveBlockText(
  block: PortalBlock,
  lang: Lang,
  order: readonly Lang[],
): { text: PortalBlockText; lang: Lang } | null {
  for (const candidate of [lang, ...order.filter((l) => l !== lang)]) {
    const text = block.content[candidate];
    if (hasText(text)) return { text: text!, lang: candidate };
  }
  return null;
}

export function resolveHeadline(
  design: PortalDesign | null | undefined,
  lang: Lang,
  order: readonly Lang[],
): { text: string; lang: Lang } | null {
  if (!design) return null;
  for (const candidate of [lang, ...order.filter((l) => l !== lang)]) {
    const text = design.headline[candidate]?.trim();
    if (text) return { text, lang: candidate };
  }
  return null;
}

// ── colour ──────────────────────────────────────────────────────────────────

type RGB = [number, number, number];

function parse(hex: string): RGB {
  return [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)) as RGB;
}

function format(rgb: RGB): string {
  return `#${rgb.map((c) => c.toString(16).padStart(2, "0")).join("")}`;
}

function mix(a: RGB, b: RGB, t: number): RGB {
  return a.map((c, i) => Math.round(c * (1 - t) + b[i]! * t)) as RGB;
}

function luminance([r, g, b]: RGB): number {
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

export function contrast(a: string, b: string): number {
  const la = luminance(parse(a));
  const lb = luminance(parse(b));
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

const BLACK: RGB = [0, 0, 0];
const WHITE: RGB = [255, 255, 255];
/**
 * The four portal surfaces an accent is painted on — `--c-bg` and `--c-surface`
 * in `web/portal/src/styles/portal.css`, light and dark. Restated because the
 * derivation must run where that stylesheet is not (the Go renderer, the console).
 */
export const PORTAL_SURFACES = {
  lightBg: "#fbf9f7",
  lightSurface: "#ffffff",
  darkBg: "#16130f",
  darkSurface: "#201c18",
} as const;
const LIGHT_BG = PORTAL_SURFACES.lightBg;
const LIGHT_SURFACE = PORTAL_SURFACES.lightSurface;
const DARK_BG = PORTAL_SURFACES.darkBg;
const DARK_SURFACE = PORTAL_SURFACES.darkSurface;

/*
 * The accent is text on three backgrounds, not two: the page, a card, and its own
 * wash — the "Connected" badge is accent text on accent wash. The wash is derived
 * from the candidate, so it is recomputed per step. Measured, not anticipated: a
 * café's amber passed against the page and the card and failed axe on the badge.
 */
function towards(hex: string, target: RGB, against: string[], wash: (c: RGB) => RGB): RGB {
  const base = parse(hex);
  for (let step = 0; step <= 20; step++) {
    const candidate = mix(base, target, step / 20);
    const backgrounds = [...against, format(wash(candidate))];
    if (backgrounds.every((bg) => contrast(format(candidate), bg) >= 4.5)) return candidate;
  }
  return target;
}

const lightWash = (c: RGB): RGB => mix(c, parse(LIGHT_BG), 0.9);
const darkWash = (c: RGB): RGB => mix(c, parse(DARK_BG), 0.8);

export interface AccentTheme {
  light: { accent: string; hover: string; contrast: string; wash: string };
  dark: { accent: string; hover: string; contrast: string; wash: string };
}

/**
 * The shades actually painted for a chosen brand colour. An owner picks a colour,
 * not a contrast ratio: a pale brand is darkened on the light page and a deep one
 * lightened on the dark page until text in it and on it clears WCAG AA.
 */
export function accentTheme(hex: string): AccentTheme {
  const light = towards(hex, BLACK, [LIGHT_BG, LIGHT_SURFACE], lightWash);
  const dark = towards(hex, WHITE, [DARK_BG, DARK_SURFACE], darkWash);
  return {
    light: {
      accent: format(light),
      hover: format(mix(light, BLACK, 0.15)),
      contrast: "#ffffff",
      wash: format(lightWash(light)),
    },
    dark: {
      accent: format(dark),
      hover: format(mix(dark, WHITE, 0.15)),
      contrast: DARK_BG,
      wash: format(darkWash(dark)),
    },
  };
}

/** The `<style>` text the renderers emit. Values are validated hex, so no escaping. */
export function accentCss(hex: string): string {
  const { light: l, dark: d } = accentTheme(hex);
  return (
    `:root{--c-accent:${l.accent};--c-accent-hover:${l.hover};--c-accent-contrast:${l.contrast};--c-accent-wash:${l.wash}}` +
    `@media (prefers-color-scheme: dark){:root{--c-accent:${d.accent};--c-accent-hover:${d.hover};--c-accent-contrast:${d.contrast};--c-accent-wash:${d.wash}}}`
  );
}

export const ACCENT_PATTERN = /^#[0-9a-f]{6}$/;

/** Foyer's own teal — `--c-accent` in the portal's light theme, what `accent: null` paints. */
export const DEFAULT_ACCENT = "#12655c";

/**
 * Brand colours an owner can pick with one tap, named for what they look like.
 *
 * **Data, not styling** — which is why they live here rather than in the console.
 * A venue's colour is content the venue chooses, like its name; the console's own
 * colours come from its tokens and none of these ever styles the console itself.
 * `null` is Foyer's own teal.
 */
export const ACCENT_PRESETS: readonly { id: string; hex: string | null }[] = [
  { id: "foyer", hex: null },
  { id: "coffee", hex: "#6b4f3a" },
  { id: "amber", hex: "#c8873a" },
  { id: "red", hex: "#c0392b" },
  { id: "rose", hex: "#d6336c" },
  { id: "purple", hex: "#7048e8" },
  { id: "blue", hex: "#1c64d8" },
  { id: "green", hex: "#2b8a3e" },
  { id: "slate", hex: "#2c3e50" },
];
