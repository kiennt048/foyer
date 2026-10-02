import type { Language } from "@foyer/contract";
import { en, type MessageKey } from "./en.js";
import { vi } from "./vi.js";

export type { MessageKey };

/** Ordered: the source-of-truth language first. */
export const LANGUAGES: readonly Language[] = ["en", "vi"];
export const DEFAULT_LANGUAGE: Language = "en";

const catalogues: Record<Language, Record<MessageKey, string>> = { en, vi };

export interface Translate {
  (key: MessageKey): string;
  lang: Language;
}

/**
 * The lookup is total by construction — `MessageKey` comes from the English
 * catalogue and Vietnamese is typed as a complete map of it — so there is no
 * missing-key path and therefore no silent fallback to another language.
 */
export function translator(lang: Language): Translate {
  const catalogue = catalogues[lang];
  const t = ((key: MessageKey) => catalogue[key]) as Translate;
  t.lang = lang;
  return t;
}

export function isLanguage(x: unknown): x is Language {
  return LANGUAGES.includes(x as Language);
}

export function otherLanguage(lang: Language): Language {
  return lang === "en" ? "vi" : "en";
}

/**
 * A language's name in its own language.
 *
 * The only label that works on a switcher, because the person who needs it is by
 * definition reading a page in a language they may not know. "Vietnamese" is no use
 * to somebody looking for Tiếng Việt.
 */
export function languageName(lang: Language): string {
  return catalogues[lang]["lang.name"];
}

/**
 * Which languages this portal will actually offer, given what the venue advertises.
 *
 * Two rules, and they are the whole function:
 *
 *  - **Intersect with the catalogue.** A venue may advertise a language the portal
 *    has no strings for. Offering it would produce a 404 or, worse, a page of
 *    `undefined`. What the venue advertises is a wish; what the portal can render
 *    is a fact.
 *  - **The language being read is always in the set**, even if the venue does not
 *    advertise it. The guest is looking at it; a switcher that implies otherwise is
 *    lying about the page it is on.
 *
 * Order follows the catalogue, not the venue's array, so the control does not
 * reshuffle between deployments or between two calls with the same content.
 */
export function resolveLanguages(
  current: Language,
  advertised: readonly Language[] | undefined,
): Language[] {
  const wanted = new Set<Language>(advertised?.filter(isLanguage) ?? LANGUAGES);
  wanted.add(current);
  return LANGUAGES.filter((l) => wanted.has(l));
}

/**
 * Which language to greet a guest in, from their browser and the venue's list.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Intersected with what the venue advertises, and that is the whole rule.**
 * A venue that supplies only Vietnamese has only Vietnamese to
 * give: sending an English-speaking guest to `/en/` there lands them on a page whose
 * terms are Vietnamese anyway, and on the one screen where a language switcher
 * appears — which is precisely the screen a single-language venue exists not to
 * have. Preferring the guest's language over the venue's stock is a courtesy that
 * produces a worse page.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `Accept-Language` is parsed rather than string-matched, because the header is
 * quality-ordered and the order it arrives in is not the order it means:
 * `en;q=0.4, vi;q=0.9` prefers Vietnamese and `startsWith("en")` would not notice.
 * Tags are matched on their primary subtag, so `vi-VN` and `en-GB` resolve — a guest
 * whose phone says `en-GB` is not an unknown language.
 *
 * **This runs in the Go binary in production.** The dev middleware implements it so
 * that manual review and the E2E suite exercise the real rule rather than a stand-in that
 * always answers with the default, which would make a single-language venue
 * untestable from the running product.
 */
export function negotiateLanguage(
  acceptLanguage: string | null | undefined,
  advertised: readonly Language[] | undefined,
): Language {
  /* What the venue actually has, in the catalogue's order. An empty or unusable
     advertisement is a venue that told us nothing, not a venue with no languages. */
  const offered = (advertised ?? []).filter(isLanguage);
  const stock: Language[] = offered.length > 0 ? LANGUAGES.filter((l) => offered.includes(l)) : [...LANGUAGES];

  const ranked = (acceptLanguage ?? "")
    .split(",")
    .map((part) => {
      const [tag, ...params] = part.trim().split(";");
      const q = params
        .map((p) => /^\s*q\s*=\s*([\d.]+)\s*$/i.exec(p))
        .find(Boolean)?.[1];
      return {
        /* Primary subtag, lowercased. `en-GB` → `en`. */
        tag: (tag ?? "").trim().toLowerCase().split("-")[0] ?? "",
        /* A tag with no `q` is q=1 by RFC 9110. */
        q: q === undefined ? 1 : Number(q),
      };
    })
    .filter((x) => x.tag && Number.isFinite(x.q) && x.q > 0)
    .sort((a, b) => b.q - a.q);

  for (const { tag } of ranked) {
    /* `*` means "anything", so it is the venue's first language rather than ours. */
    if (tag === "*") return stock[0] ?? DEFAULT_LANGUAGE;
    const hit = stock.find((l) => l === tag);
    if (hit) return hit;
  }

  /* Nothing matched. The venue's first language beats the portal's default: a guest
     who reads neither is better served by the one the venue can actually support. */
  return stock[0] ?? DEFAULT_LANGUAGE;
}
