/** @jsxImportSource preact */
import type { ComponentChildren } from "preact";
import type { PageContext } from "../pages/types.js";
import { languageName } from "../i18n/index.js";
import { Theme } from "../pages/blocks.js";
import { Mark } from "./icons.js";

/**
 * The masthead and column every page sits in.
 *
 * The language switch is a set of plain links to the same page under another
 * language prefix. It has to be links rather than a control that stores a
 * preference, because the CNA keeps no state at all — its data store is ephemeral
 * and is destroyed when the window closes — so the language has to live in the URL.
 *
 * **What it offers comes from the venue, not from a hardcoded pair.** The switcher
 * renders one link per language the venue advertises *and* the portal has strings
 * for, minus the one being read, each labelled with its own endonym. Three
 * consequences, all deliberate:
 *
 *  - A venue that advertises **one** language gets **no switcher at all**, rather
 *    than a control that does nothing. This is the rule the admin UI already follows
 *    for driver capabilities: remove the control, never disable it.
 *  - A venue that advertises **two** renders exactly what it always did — one link,
 *    same markup, same bytes.
 *  - A venue that advertises **three or more** renders a row of them, and the row
 *    wraps. There is no count-based special case and no dropdown: a `<select>` needs
 *    script to navigate, and this portal's flow may not.
 *
 * **Each link carries the query string.** Switching language is a navigation to the
 * same document under another prefix, so everything the current document was given
 * has to survive it: the NAS parameters `VerifyPresence` needs, the `url` the success
 * page offers, and the `?e=` and `?code=` a rejected voucher came back with. Dropping
 * them would mean a guest who could not read the error loses both the error and the
 * twelve characters they had just typed by trying to read it in their own language —
 * the exact failure the root redirect guards against, in the one control a guest reaches
 * for *because* they are struggling.
 *
 * The one thing this deliberately does *not* do is mark which language is current.
 * The page itself is the answer, and adding a selected-state item would put a
 * branch in the markup that no deployment can exercise while `Language` has two
 * members. If a venue ever offers more than about four, this control stops being
 * right — and that is the point at which somebody should measure a real one rather
 * than guess now.
 */
export function Shell({
  ctx,
  path,
  children,
}: {
  ctx: PageContext;
  /** Path within the language prefix, e.g. `voucher/`. Used by the language links. */
  path: string;
  children: ComponentChildren;
}) {
  const others = ctx.languages.filter((l) => l !== ctx.lang);
  return (
    <>
    {/* The venue's brand colour, on every page — the voucher and account buttons
        are the venue's buttons too. Nothing is emitted for a venue with none. */}
    <Theme ctx={ctx} />
    <div class="shell">
      <header class="masthead">
        <div class="venue">
          {/*
            The venue's logo where it has one, Foyer's mark where it does not.
            Inlined as a data URI and capped at 8 kB by the admin API, because it
            lands inside the document and is therefore spent from the 50 kB code
            budget — `tools/check-budget.mjs` weighs the whole page view.

            `alt=""` and `aria-hidden`: the venue's name is already the next node,
            and a logo that reads its own alt text makes a screen reader say the
            venue twice. This is decoration beside a label, not a label.
          */}
          {ctx.logoDataUri ? (
            <img class="venue-mark" src={ctx.logoDataUri} alt="" aria-hidden="true" />
          ) : (
            <Mark class="venue-mark" />
          )}
          <span class="venue-name">{ctx.venueName}</span>
        </div>
        {others.length ? (
          <div class="langs">
            {others.map((l) => (
              <a
                key={l}
                class="lang-switch"
                href={`/${l}/${path}${ctx.query}`}
                lang={l}
                hrefLang={l}
              >
                {languageName(l)}
              </a>
            ))}
          </div>
        ) : null}
      </header>
      {children}
    </div>
    </>
  );
}
