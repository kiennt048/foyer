import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Site } from "@foyer/contract";
import { buildWorld } from "@foyer/fixtures";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { SITE_PARAM, SiteProvider, SiteSwitcher, resolveSite, useSite } from "./site.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The venue is a fact about the address, not about this browser's memory
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The failure this guards is one line: `useState("site-hcm")`. Two things follow and
 * the second is worse than the first — every document load returns the operator to
 * the first venue, **and** the value it returns them to is a fixture id, so a
 * first-booted deployment opens on a venue that does not exist.
 *
 * ── HOW THIS GOES RED ───────────────────────────────────────────────────────
 *
 * `docs/rules/testing.md` warns about the reload assertion whose fixture world's
 * first site *is* the site you selected: it is green against the defect and against
 * the fix, because both render the same screen. So the case below asks for `ELSEWHERE`
 * — **derived** as a venue that is not `sites[0]`, never named — and reads the venue
 * back out.
 *
 * Driven, not claimed. Replacing the provider with `useState("site-hcm")`, the
 * address ignored, turns **three** of these red and leaves the ten resolver cases
 * green, which is the shape that says the sabotage reached the wiring rather than the
 * decision:
 *
 * ```
 * × reports the venue the address names
 *     expected '<output data-status="ready">site-hcm<…' to contain '>site-hanoi<'
 * × renders no venue-scoped id at all before the venue list arrives
 *     expected '<output data-status="ready">site-hcm<…' to contain 'data-status="resolving"'
 * × names the venue in the switcher, and says so when the address names one it has not got
 *     expected '<div style="position:relative"><div c…' to contain 'Lab'
 * ```
 *
 * The fourth — *says 'no venue yet'* — stays green under that sabotage and is red
 * against the *switcher* it replaced, which rendered `site.failed` for it: it is a lock
 * on the second change rather than on the first.
 *
 * ── WHAT THIS FILE CANNOT SEE ───────────────────────────────────────────────
 *
 * `@foyer/admin` has no jsdom. `renderToStaticMarkup` runs no effect, so the address
 * being *rewritten* — the canonicalisation, and the parameter surviving an in-app link
 * that dropped it — is not observable here. That half is `web/e2e`'s
 * `tests/admin/site-survives-a-reload.spec.ts`, which reloads a real browser. What is
 * observable here is the derivation, which is the half that decides what a screen
 * asks the server for.
 */

/**
 * The venues the fixture world actually has, rather than a second copy of them.
 *
 * `admin.test.ts` refuses a Vietnamese string anywhere in `src/`, and the venue names
 * here are Vietnamese — which is the useful accident: importing them is also
 * `docs/rules/testing.md`'s *derive the stand-in from what it stands in for*, so
 * this file cannot go on describing a world the fixtures have moved on from.
 */
const SITES: readonly Site[] = buildWorld().sites;

/** The deployment's venue: whatever `listSites` puts first, asked and not assumed. */
const FIRST = SITES[0]!;

/**
 * A venue that is **not** the default, derived.
 *
 * Naming one would make every case below green against the defect the day somebody
 * reorders the fixtures, because `useState("site-hcm")` and a correct provider agree
 * about `sites[0]` and disagree about nothing else.
 */
const ELSEWHERE = SITES.find((s) => s.id !== FIRST.id)!;

/** The one venue whose name is not Vietnamese, so it can be asserted on by eye. */
const LAB = SITES.find((s) => s.environment === "lab")!;

describe("resolveSite", () => {
  const settled = { sites: SITES, isLoading: false, failed: false };

  it("takes the venue the address names", () => {
    expect(resolveSite({ named: ELSEWHERE.id, held: "", ...settled })).toMatchObject({
      status: "ready",
      siteId: ELSEWHERE.id,
    });
  });

  /**
   * The default is asked of the server, never typed here.
   *
   * That is the whole of the second fact: `site-hcm` is a `web/fixtures` id, and a
   * deployment that has never seen the fixtures has no venue by that name. The first
   * row of `listSites` is a fact about *this* deployment — `ORDER BY name` on the Go
   * side, and the mock inserts in name order to agree with it.
   */
  it("falls back to the deployment's first venue and not to a literal", () => {
    const r = resolveSite({ named: "", held: "", ...settled });
    expect(r.status).toBe("ready");
    expect(r.siteId).toBe(FIRST.id);

    /* Derived, not asserted: the same call against a deployment whose venues are
       different answers differently. A hardcoded `site-hcm` above would pass here too
       and would be measuring the fixture rather than the rule. */
    const elsewhere: Site[] = [{ id: "site-xyz", name: "Somewhere else", environment: "live" }];
    expect(resolveSite({ named: "", held: "", ...settled, sites: elsewhere }).siteId).toBe(
      "site-xyz",
    );
  });

  /**
   * An in-app link that says nothing about the venue does not change it.
   *
   * There are thirty-one internal link targets in this console and none of them
   * carries `?site=`. The provider holds the answer instead — `held` — so a `PUSH` to
   * `/sessions` keeps the venue the operator is on rather than silently moving them
   * to the deployment's first one.
   */
  it("keeps the venue when a navigation names none", () => {
    expect(resolveSite({ named: "", held: ELSEWHERE.id, ...settled }).siteId).toBe(ELSEWHERE.id);
  });

  /**
   * …and a `POP` is the exception, which is why the caller passes `held: ""` for one.
   *
   * Back and forward are the operator navigating. The address they are returning to is
   * authoritative *including its silence*, so *switch venue, then Back* undoes the
   * switch instead of being swallowed by a memory of it.
   */
  it("returns to the deployment's venue on a POP to an address that names none", () => {
    expect(resolveSite({ named: "", held: "", ...settled }).siteId).toBe(FIRST.id);
  });

  /**
   * A stale bookmark is the ordinary case, not the exotic one.
   *
   * It must not resolve to the first venue: an operator who is shown another venue's
   * sessions under a URL they trusted acts on them believing they are theirs.
   */
  it("reports a venue this deployment does not have, rather than choosing one", () => {
    const r = resolveSite({ named: "site-gone", held: "", ...settled });
    expect(r.status).toBe("unknown");
    expect(r.siteId).toBe("site-gone");
    expect(r.site).toBeNull();
  });

  /** A first boot: migrated, an administrator, and nothing else. `FOYER_MOCK_FRESH=1`. */
  it("says a deployment with no venue has no venue", () => {
    const r = resolveSite({ named: "", held: "", sites: [], isLoading: false, failed: false });
    expect(r.status).toBe("none");
    expect(r.siteId).toBe("");
  });

  /**
   * …and it outranks what the address says.
   *
   * *No venue exists* and *that venue does not exist* are different sentences, and only
   * the first one is true here. Reporting `unknown` would send the operator looking for
   * a venue to switch to on a deployment that has none.
   */
  it("prefers 'no venue' over 'unknown venue' when the deployment is empty", () => {
    expect(
      resolveSite({ named: "site-gone", held: "", sites: [], isLoading: false, failed: false })
        .status,
    ).toBe("none");
  });

  /**
   * A named venue is asked about immediately, before the list confirms it exists.
   *
   * The address is the fact; the list is a lookup. Waiting for it would make a reload
   * of a shared link slower than ignoring the address would be, for no more certainty than the URL
   * already carries.
   */
  it("does not wait for the venue list when the address names one", () => {
    const r = resolveSite({
      named: ELSEWHERE.id,
      held: "",
      sites: [],
      isLoading: true,
      failed: false,
    });
    expect(r.status).toBe("ready");
    expect(r.siteId).toBe(ELSEWHERE.id);
    expect(r.site).toBeNull();
  });

  it("has nothing to ask about while the list is in flight and the address names none", () => {
    expect(
      resolveSite({ named: "", held: "", sites: [], isLoading: true, failed: false }).status,
    ).toBe("resolving");
  });

  /**
   * A failed lookup is not an empty deployment.
   *
   * Rendering both as *"Cannot load sites"* would describe the one that did not
   * happen. They are two statuses and the gate says a different sentence for each.
   */
  it("tells a venue list that failed apart from a deployment with no venue", () => {
    expect(
      resolveSite({ named: "", held: "", sites: [], isLoading: false, failed: true }).status,
    ).toBe("failed");
  });
});

/* ── the wiring, which is the half a hard-coded default breaks ──────────────── */

function Probe() {
  const { siteId, status } = useSite();
  return <output data-status={status}>{siteId}</output>;
}

function markupAt(
  entry: string,
  sites: readonly Site[] | undefined,
  children: React.ReactNode,
): string {
  const client = new QueryClient();
  if (sites) client.setQueryData(["sites"], sites);
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <SiteProvider>{children}</SiteProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("the provider reads the venue out of the address", () => {
  /**
   * **`ELSEWHERE`, and never the first venue.** A case that selects `sites[0]` is
   * green whether or not the defect is present — the same rendering either way — which
   * is `docs/rules/testing.md`'s *assertion aimed where the subject does not
   * appear*, in its cheapest form.
   */
  it("reports the venue the address names", () => {
    expect(markupAt(`/sessions?${SITE_PARAM}=${ELSEWHERE.id}`, SITES, <Probe />)).toContain(
      `>${ELSEWHERE.id}<`,
    );
  });

  it("reports the deployment's venue when the address names none", () => {
    expect(markupAt("/sessions", SITES, <Probe />)).toContain(`>${FIRST.id}<`);
  });

  it("renders no venue-scoped id at all before the venue list arrives", () => {
    expect(markupAt("/sessions", undefined, <Probe />)).toContain('data-status="resolving"');
  });

  /**
   * The switcher's three sentences, which were one.
   *
   * A deployment with no venue answered *"Cannot load sites"* — a request that
   * succeeded, reported as one that failed — and an address naming a venue that is not
   * here could not be rendered at all, because the seed always matched a fixture.
   */
  it("says 'no venue yet' for a deployment that has none", () => {
    const markup = markupAt("/", [], <SiteSwitcher />);
    expect(markup).toContain("No venue yet");
    expect(markup).not.toContain("Cannot load sites");
  });

  it("names the venue in the switcher, and says so when the address names one it has not got", () => {
    expect(markupAt(`/?${SITE_PARAM}=${LAB.id}`, SITES, <SiteSwitcher />)).toContain(LAB.name);
    expect(markupAt(`/?${SITE_PARAM}=site-gone`, SITES, <SiteSwitcher />)).toContain(
      "Unknown venue",
    );
  });
});
