import type { GuestDataField, Language, PortalContext, PortalDesign, PortalStatus } from "@foyer/contract";
import type { Translate } from "../i18n/index.js";

/**
 * The terms, resolved to exactly one language.
 *
 * The venue supplies a map keyed by language; a page receives one string and the
 * language it is actually written in. The two are inseparable, which is the point:
 * terms are legal text, so the portal may fall back to another language but may
 * never let the document claim that text is in a language it is not.
 */
export interface ResolvedTerms {
  summary: string;
  /**
   * Differs from the page's language when the venue supplied terms only in some
   * other language. The renderer turns that difference into a `lang` attribute.
   */
  lang: Language;
}

/** Everything a page is allowed to know. Resolved server-side, never client-side. */
export interface PageContext {
  lang: Language;
  t: Translate;
  venueName: string;
  networkName: string | null;
  /**
   * The venue's logo, inlined. Capped at 8 kB by the admin API because it lands
   * inside the document and is spent from the code budget.
   */
  logoDataUri: string | null;
  /**
   * The venue's photograph, as a same-origin path — **never a data URI.**
   *
   * Measured: at 900 px, the macOS CNA's full window width, a real photograph is
   * 89,697 B, and base64 + gzip lands *larger than the raw file* at 1.75× the whole
   * document budget. Linked, the document renders at 4 kB and the image follows;
   * inlined, the guest waits for the whole photograph before seeing a word. The path
   * is same-origin, which is reachable pre-authentication by construction because
   * the portal's FQDN is already required to be in the walled garden.
   */
  heroImageUrl: string | null;
  methods: NonNullable<PortalContext["methods"]>;
  /**
   * The languages the switcher may offer, already intersected with the catalogue
   * and guaranteed to contain `lang`. Never the venue's raw array — see
   * `resolveLanguages`.
   */
  languages: Language[];
  terms: ResolvedTerms;
  /**
   * The request's query string, including the leading `?`, or `""`.
   *
   * Relayed, never parsed. It is what the NAS appended — for UniFi `ap`, `id`, `t`,
   * `ssid`, `url` — plus whatever a failure redirect added, and it is the only
   * evidence `VerifyPresence` has to work from. Every one of those parameters is
   * unsigned and attacker-controlled and stays untrusted at the far end; carrying it
   * across a same-origin language link is transport, exactly as the root redirect is.
   */
  query: string;
  /** A machine-readable failure carried through the 303, re-rendered as human copy. */
  error: string | null;
  /**
   * The voucher code the guest just submitted and had rejected, normalised to the
   * wire form and length-capped. Rendered back into the field so a guest who
   * mistyped one of twelve characters does not retype all twelve — there is no
   * client state to hold it in, so it comes back through the redirect.
   *
   * Never a password, and never a username: see the note in `render.ts`.
   */
  submittedCode: string | null;
  /**
   * `?sent=1` on the phone page: a code is on its way, so the code form renders. It
   * chooses a form and nothing else — the code is checked against the session.
   */
  phoneSent: boolean;
  /**
   * How long this venue's phone codes work, in minutes — `PortalContext.phoneCodeMinutes`,
   * or the product default where the context has none. The code form says it,
   * rather than a fixed *5 minutes* that would be wrong once the lifetime is
   * configured.
   */
  phoneCodeMinutes: number;
  status: PortalStatus | null;
  /**
   * The venue's page — accent, headline and blocks — already narrowed to what a
   * guest may read today. Null for a venue that never designed one, which renders
   * exactly the page Foyer rendered before designs existed.
   */
  design: PortalDesign | null;
  /** What the sign-in forms ask for. Null asks for nothing. */
  dataCollection: ResolvedDataCollection | null;
  /** An allowlisted `?n=` — a success to read — or null. */
  notice: "consent-withdrawn" | null;
}

/**
 * What the sign-in forms ask for, resolved: the fields in the
 * venue's order, and the one consent sentence this page renders with its language
 * and version — or none, where the venue is not asking or wrote no sentence.
 */
export interface ResolvedDataCollection {
  fields: { field: GuestDataField; required: boolean }[];
  consent: { text: string; lang: Language; version: string } | null;
  /** Whether the venue asks for marketing consent at all: the connected page's way out. */
  consentAsked: boolean;
}

export interface PageDefinition {
  /** Path under the language prefix. The empty string is the welcome page. */
  path: string;
  title: (ctx: PageContext) => string;
  render: (ctx: PageContext) => import("preact").VNode;
  /** Whether this document needs the enhancement script at all. */
  enhance?: boolean;
}
