import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Site } from "@foyer/contract";
import { buildWorld } from "@foyer/fixtures";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { SiteGate } from "../App.js";
import { SiteProvider } from "../site.js";
import SOURCE from "./CreateVenue.tsx?raw";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  A fresh deployment had nowhere to create its first venue
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `createSite` exists because `createNas` requires a `siteId`, a migrated database
 * holds zero sites, and nothing else in the product could put the first row in. The
 * Go server implements it, `web/mock` implements it, the contract documents it — and
 * `grep -rn createSite web/admin/src` returned **two comments and no call**. The
 * release bar is *a stranger installs it and a phone gets online*, timed; a first
 * run that requires `curl` fails it.
 *
 * ── WHAT THIS FILE ASSERTS THAT A COMPONENT TEST WOULD NOT ──────────────────
 *
 * It drives `SiteGate`, not `CreateVenue`. That is the whole point. A test that
 * renders the form directly proves a component renders, which is not in doubt —
 * the question is whether **anything an operator can do reaches it.** `SiteGate` is what
 * decides whether the form is ever on screen, so `SiteGate` is what is driven, from
 * an address, with a venue list, exactly as the shell does it.
 *
 * ── THE WORLD REALLY HAS NO VENUE, WHICH IS NOT FREE ────────────────────────
 *
 * `docs/rules/frontend.md`: *a world that cannot express a property makes every
 * check of that property vacuous* — when every fixture row lives in one of three
 * sites, a dropped site scope changes no answer. The
 * hazard here is sharper than that, because there are **two** ways to get an empty
 * screen out of this gate and only one of them is the state under test:
 * `setQueryData(["sites"], [])` is *asked and answered none* → `none`, while passing
 * no data at all is *not asked yet* → `resolving`, which renders a skeleton and no
 * form and would satisfy a bare `not.toContain` just as well. So `NONE` asserts the
 * status it is standing on before it asserts anything about the form, and `POPULATED`
 * is the same gate answering the other way.
 *
 * ── HOW THIS GOES RED ───────────────────────────────────────────────────────
 *
 * Delete `<CreateVenue />` from `SiteGate`'s `none` branch in `App.tsx` — a page that
 * states *"No venue yet"* and offers nothing to do about it. **Four of
 * the seven cases go red**, and which three stay green is the part worth reading,
 * because it says the sabotage reached the wiring rather than the component:
 *
 * ```
 * × puts a create-venue control on a deployment that has no venue
 *     expected '<div class="page"><div class="page-he…' to contain 'data-create-venue'
 * × names the field, and ties the name to the input
 *     expected '…' to contain '<label for="venue-name">'
 * × states the one-venue-per-install price before the operator commits
 *     expected '…' to contain 'one venue per installation'
 * × says what the next step is, so the two first-run surfaces read as one flow
 *     expected '…' to contain 'data-create-venue-next'
 *
 *   Tests  4 failed | 232 passed (236)
 * ```
 *
 * The three that stay green are each green for a different and correct reason:
 * *has two worlds that differ* is about the fixtures and not about the console;
 * *asks the server nothing scoped to a venue* reads `CreateVenue.tsx`, which the
 * sabotage does not touch; and **_does not offer it on a deployment that already has
 * one_ is satisfied by a console with no form at all** — which is exactly why it is
 * here. It is the negative half, and without it "render the form unconditionally"
 * would pass this file, that being the cheapest wrong fix available.
 *
 * So that half was driven too, with the opposite sabotage — `CreateVenue` rendered
 * ahead of `children` in the gate's `ready` branch — and it is the only one that
 * moves:
 *
 * ```
 * × does not offer it on a deployment that already has one
 *     expected '<section class="section" data-create-…' not to contain 'data-create-venue'
 *
 *   Tests  1 failed | 235 passed (236)
 * ```
 *
 * Two sabotages in opposite directions, four cases red under one and one under the
 * other, and no case red under both. A file where every case moves together is
 * measuring one thing under several names.
 *
 * ── WHAT THIS FILE DELIBERATELY DOES NOT ASSERT ─────────────────────────────
 *
 * That the submit button uses `aria-disabled` rather than `disabled` while its request
 * is in flight. `screens/screens.test.ts` globs every component file in this directory
 * — this one included — and already owns that rule. Restating it here would be a
 * second copy of a constraint whose authority is sitting next to it:
 * `docs/rules/database.md`'s restated-constraint defect, in a test.
 *
 * `@foyer/admin` has no jsdom, so the mutation actually firing, the pending state and
 * the gate opening on success are not observable here; that half is `web/e2e`'s
 * `tests/admin/create-first-venue.spec.ts`.
 */

/**
 * A populated deployment, derived from the fixtures rather than named.
 *
 * `site.test.tsx` states the reason and it applies unchanged: a literal id goes on
 * passing the day somebody reorders or renames the fixture venues, while measuring a
 * world it no longer describes.
 */
const POPULATED: readonly Site[] = buildWorld().sites;

/** A deployment that has been migrated and first-booted and nothing else. */
const NONE: readonly Site[] = [];

function gateAt(sites: readonly Site[] | undefined): string {
  const client = new QueryClient();
  if (sites) client.setQueryData(["sites"], sites);
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/"]}>
        <SiteProvider>
          <SiteGate>
            <output data-screen="">the venue-scoped screen</output>
          </SiteGate>
        </SiteProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("the gate on a deployment with no venue", () => {
  /**
   * Guards every case below. If the fixtures ever ship zero venues, `POPULATED` and
   * `NONE` become the same world and the negative case starts agreeing with the
   * positive one for the wrong reason.
   */
  it("has two worlds that differ", () => {
    expect(POPULATED.length).toBeGreaterThan(0);
    expect(NONE.length).toBe(0);
  });

  it("puts a create-venue control on a deployment that has no venue", () => {
    const markup = gateAt(NONE);
    /* The state, before anything about the form. A `resolving` gate also has no
       screen in it and would pass the assertions below by rendering nothing. */
    expect(markup).toContain("No venue yet");
    expect(markup).not.toContain("data-screen");
    expect(markup).toContain("data-create-venue");
    expect(markup).toContain("data-create-venue-submit");
  });

  it("names the field, and ties the name to the input", () => {
    const markup = gateAt(NONE);
    /* Both halves. An input with no associated label is announced as "edit text" and
       nothing else, which is the same defect `dialogs-are-named` was opened for one
       element up. */
    expect(markup).toContain('<label for="venue-name">');
    expect(markup).toContain('id="venue-name"');
  });

  /**
   * The refusal is `409` and the console has had copy for it since before it had a
   * screen. That copy is what an operator reads *after* being refused; this is the
   * sentence that means most of them never are.
   */
  it("states the one-venue-per-install price before the operator commits", () => {
    expect(gateAt(NONE)).toContain("one venue per installation");
  });

  /**
   * The continuity between the two first-run surfaces, as a sentence on the screen.
   *
   * Without it this is a form in a void: the operator names a venue, the page they
   * were refused disappears, and a dashboard they have not seen before appears asking
   * for something else. `FirstRun` is the other half and it cannot introduce itself.
   */
  it("says what the next step is, so the two first-run surfaces read as one flow", () => {
    expect(gateAt(NONE)).toContain("data-create-venue-next");
  });

  it("does not offer it on a deployment that already has one", () => {
    const markup = gateAt(POPULATED);
    expect(markup).toContain("data-screen");
    expect(markup).not.toContain("data-create-venue");
  });

  /**
   * A known trap, asserted on the source because it cannot be seen in a rendering.
   *
   * In the `none` state `siteId` is `""`, and `api.ts`'s `qs()` drops an empty value —
   * so `listNas({siteId})` becomes `GET /nas` with **no** venue scope, and the mock
   * answers for `site-hcm`. That is a seeded-fixture-venue defect one layer down, and it would look exactly like this screen working. This surface is
   * the one place in the console that renders with no venue, so it is the one place
   * where a venue-scoped request cannot be right.
   */
  it("asks the server nothing that is scoped to a venue", () => {
    /*
     * Matched on the calls rather than on the word `siteId`, because a word ban is
     * **red against a correct component**: this file's own header
     * names `listNas({siteId})` while explaining why the surface must not make that
     * call, so a word ban cannot tell the prose from the code. Requiring a `(` also
     * keeps the `../api.js` import specifier out of the match.
     *
     * Worth naming, because it is the mirror of a species already recorded in
     * `docs/rules/testing.md`. That one is *an assertion aimed where the subject
     * does not appear* — a `toContainText` that could not reach a `title` attribute,
     * green with the defect present and with it repaired. This is the same fault in
     * the other direction: the assertion's surface reached text the subject was not
     * **in**, so instead of going quiet it went loud, against code that was right.
     * A vacuous check costs you the defect it was meant to catch; this one costs
     * somebody a day fixing a defect that is not there.
     *
     * The repair is the same move either way, and it is the general one: **derive the assertion from the authority**. `api.<name>(` is what
     * the module actually does, the way `sites[0]` is what `listSites` actually
     * ordered — neither is a restatement that can drift from its subject.
     */
    const calls = [...SOURCE.matchAll(/\bapi\.([a-zA-Z]\w*)\s*\(/g)].map((m) => m[1]);
    expect(calls, "no api call found in CreateVenue.tsx — this guard is checking nothing").not.toEqual(
      [],
    );
    expect([...new Set(calls)]).toEqual(["createSite"]);
  });
});
