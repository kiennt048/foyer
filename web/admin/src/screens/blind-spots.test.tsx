import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { AuthBlindSpot, Nas } from "@foyer/contract";
import { authBlindSpotsFor, buildWorld, type World } from "@foyer/fixtures";
import { t } from "../i18n/attempts.js";
import { BlindSpotPanel } from "./Attempts.js";

/**
 * One blind-spot kind was carrying two situations and asserting the
 * architecture of whichever one it was not looking at.
 *
 * `authBlindSpotsFor` keys on *no device in this site has a shared secret*, which is
 * true of a site with **no devices at all** — `site-lab`, the only site that reaches
 * the branch. The console's sentence for the kind read *"This deployment authorizes
 * through a controller API and has no RADIUS accounting attached, so this stream is
 * empty by design"*: a claim about how a venue was built, produced because a list was
 * empty. Nobody had chosen a controller API. Nobody had chosen anything.
 *
 * The two situations end differently. *No access point is registered here* is the
 * first-run state and the answer is to register one; *devices registered, none with a
 * RADIUS secret* is either a deployment that meant it or the most common real fault
 * in this product, and no screen can tell those two apart — so that sentence names
 * both readings rather than picking one.
 *
 * The **kind** is unchanged: `AuthBlindSpot.kind` is a closed enum with no member for
 * *no devices*, and widening it is a contract change. The count travels in `params`, which is what `before-retention` already does with
 * `retentionDays`.
 *
 * ── HOW THIS WAS MADE TO FAIL ───────────────────────────────────────────────
 *
 * `authBlindSpotsFor` restored to `return [{ kind: "no-radius-configured" }, { kind:
 * "never-associated" }]` and `BLIND_SPOT_TEXT` to its single `t("attempts.blind
 * .no-radius-configured")`:
 *
 *   × a site with no devices at all is not told how it authorizes
 *       → the empty site is told it authorizes through a controller API:
 *         expected '… This deployment authorizes through a controller API …'
 *         not to contain 'controller API'
 *   × the empty site and the secretless site do not get the same sentence
 *       → one sentence is still doing two jobs: expected 'No RADIUS client is
 *         configured for this site…' not to be 'No RADIUS client is configured
 *         for this site…'
 *   × the count that decides the sentence comes from the world
 *       → the predicate cannot tell an empty site from a secretless one:
 *         expected undefined to be 0
 *
 * ── AND THE TRAP THIS PANEL IS EXACTLY THE SHAPE OF ─────────────────────────
 *
 * `docs/rules/testing.md`: an assertion aimed where the subject does not appear is
 * green with the defect present *and* with it repaired. On a panel like this one the
 * sentence can live in a `title` attribute, where a whole-page text assertion can
 * neither find it nor fail to. So every case here
 * reads the `<li>` text with the tags — and every attribute inside them — stripped
 * out, and separately forbids any `title` from carrying the sentence.
 */

/** The `<li>` text, with tags and therefore every attribute removed. */
function items(spots: readonly AuthBlindSpot[]): string[] {
  const html = renderToStaticMarkup(<BlindSpotPanel spots={spots} />);
  return [...html.matchAll(/<li[^>]*>(.*?)<\/li>/g)].map((m) =>
    (m[1] ?? "")
      .replace(/<[^>]+>/g, " ")
      .replace(/&#x27;/g, "'")
      .replace(/&quot;/g, '"')
      .replace(/&amp;/g, "&")
      .trim(),
  );
}

/** Everything any `title` attribute on the panel carries. */
function titles(spots: readonly AuthBlindSpot[]): string[] {
  const html = renderToStaticMarkup(<BlindSpotPanel spots={spots} />);
  return [...html.matchAll(/title="([^"]*)"/g)].map((m) => m[1] ?? "");
}

/** The sentence rendered for the one kind under test, whatever else is in the list. */
function noRadiusText(spots: readonly AuthBlindSpot[]): string {
  const i = spots.findIndex((b) => b.kind === "no-radius-configured");
  expect(i, "this world does not produce `no-radius-configured` at all").toBeGreaterThanOrEqual(0);
  return items(spots)[i]!;
}

/**
 * A world holding one site, with the devices given.
 *
 * Derived from `buildWorld()` rather than typed here, so the predicate is asked
 * about the same shape the mock serves. The **secretless** case cannot be read off
 * the shipped fixtures at all — every site there either speaks RADIUS or is empty —
 * and `docs/rules/frontend.md` forbids adding a device row to `site-lab`, which is
 * the only site that produces this kind. So the case is constructed, and named, and
 * the constructed device is the fixtures' own secretless one rather than an invention.
 */
function worldOf(devices: Nas[]): World {
  return { ...buildWorld(), nas: devices };
}

const SITE = "site-under-test";

/** The fixtures' own registered-but-never-seen device: `secretSet: false`. */
function secretlessDevice(): Nas {
  const w = buildWorld();
  const found = w.nas.find((n) => !n.secretSet);
  expect(found, "no fixture device has `secretSet: false`, so this case is invented").toBeTruthy();
  return { ...found!, siteId: SITE };
}

describe("what an empty attempt stream is allowed to say about why", () => {
  it("the two situations are two different worlds, and the shipped fixtures hold one", () => {
    /*
     * Non-vacuity, and the reason the second world is built by hand. `site-lab` is
     * kept empty of devices on purpose — it is the only site that produces this kind
     * at all — so *devices here, none with a secret* is a state the fixture world
     * cannot express. A world that cannot express a property makes every check of it
     * vacuous, and this is the case being declared rather than assumed.
     */
    const shipped = buildWorld();
    const empty = shipped.sites.find((s) => !shipped.nas.some((n) => n.siteId === s.id));
    expect(empty, "no site in the fixtures is empty of devices").toBeTruthy();
    expect(shipped.nas.some((n) => n.siteId === SITE)).toBe(false);
  });

  it("a site with no devices at all is not told how it authorizes", () => {
    const spots = authBlindSpotsFor(worldOf([]), SITE);
    const text = noRadiusText(spots);

    expect(text, "the empty site is told it authorizes through a controller API").not.toContain(
      "controller API",
    );
    /* The positive half, so the repair cannot be "say nothing". An operator landing
       on an empty deployment has to be told what the first thing to do is. */
    expect(text).toBe(t("attempts.blind.no-radius-configured.noDevices"));
    expect(text.toLowerCase()).toContain("register");
  });

  it("a site whose devices have no secret is still told that is what happened", () => {
    const spots = authBlindSpotsFor(worldOf([secretlessDevice()]), SITE);
    const text = noRadiusText(spots);

    expect(text).toBe(t("attempts.blind.no-radius-configured.noSecret"));
    /* This one may name the controller-API reading, because there are devices and
       one of the two readings of a secretless device is exactly that. It may not
       claim it: the other reading is the most common real fault in the product, and
       the sentence carries both. */
    expect(text).toContain("controller API");
    expect(text.toLowerCase(), "only the by-design reading survived").toContain(
      "nobody can get online",
    );
  });

  it("the empty site and the secretless site do not get the same sentence", () => {
    const empty = noRadiusText(authBlindSpotsFor(worldOf([]), SITE));
    const secretless = noRadiusText(authBlindSpotsFor(worldOf([secretlessDevice()]), SITE));
    expect(empty, "one sentence is still doing two jobs").not.toBe(secretless);
  });

  it("the count that decides the sentence comes from the world", () => {
    /*
     * The predicate, asserted directly. The rendering above is satisfied by any two
     * distinct sentences; this is what says the thing choosing between them is the
     * device list rather than a flag somebody set.
     */
    const none = authBlindSpotsFor(worldOf([]), SITE).find(
      (b) => b.kind === "no-radius-configured",
    );
    expect(
      none?.params?.deviceCount,
      "the predicate cannot tell an empty site from a secretless one",
    ).toBe(0);

    const two = [secretlessDevice(), { ...secretlessDevice(), id: "nas-second" }];
    const some = authBlindSpotsFor(worldOf(two), SITE).find(
      (b) => b.kind === "no-radius-configured",
    );
    expect(some?.params?.deviceCount).toBe(2);

    /* And the site scope is still the scope. A count that included another site's
       devices would pick the wrong sentence for both. */
    const elsewhere = authBlindSpotsFor(
      worldOf([{ ...secretlessDevice(), siteId: "site-somewhere-else" }]),
      SITE,
    ).find((b) => b.kind === "no-radius-configured");
    expect(elsewhere?.params?.deviceCount, "the count leaked across sites").toBe(0);
  });

  it("a site that does speak RADIUS keeps the four it always had", () => {
    /* The other branch, untouched, driven from the shipped world so this cannot be
       satisfied by a predicate that answers `no-radius-configured` for everything. */
    const w = buildWorld();
    const speaking = w.nas.find((n) => n.secretSet)!;
    const kinds = authBlindSpotsFor(w, speaking.siteId).map((b) => b.kind);
    expect(kinds).toContain("shared-secret-mismatch");
    expect(kinds, "a site with working RADIUS is told it has none").not.toContain(
      "no-radius-configured",
    );
  });

  it("the sentence is the list item's own text, never only a tooltip", () => {
    /*
     * The green-either-way failure, closed. `title` is where this defect hides: a
     * whole-page assertion can neither see the sentence nor miss it, so repairing the
     * product changes nothing about the result.
     */
    const spots = authBlindSpotsFor(worldOf([]), SITE);
    expect(items(spots).some((s) => s.includes(t("attempts.blind.no-radius-configured.noDevices"))))
      .toBe(true);
    for (const title of titles(spots)) {
      expect(title, "the sentence reached the screen as a tooltip").not.toContain(
        t("attempts.blind.no-radius-configured.noDevices"),
      );
    }
  });
});
