import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ControllerProbe } from "@foyer/contract";
import { t } from "../i18n/nas.js";
import { controllerProbeWarnings } from "./Controllers.js";
import { ProbeWarnings } from "./nas/FindingsPanel.js";

/**
 * `Controllers.tsx` renders `ControllerProbe.warnings`.
 *
 * This is the screen where the driver layer actually produces them: a UniFi probe
 * returns six green check rows, and the two warnings beside them — the vendor's API
 * cannot confirm an access point, and some capability values are believed rather than
 * measured — must reach the operator. A screen that renders
 * a green tick over a check that did not happen is telling an operator it did.
 *
 * The rendering is `ProbeWarnings`, the component the access-point screens already
 * use, so this file asserts what an operator ends up reading rather than what the
 * screen passes to it. The one thing this screen decides for itself is the
 * `version-below-floor` overlap, and that is what the first two cases are about.
 *
 * `renderToStaticMarkup` for the reason `gallery.test.tsx` gives: these components
 * hold no data, make no request and read nothing from the browser, so the first
 * frame is the whole of the answer and is also what a screenshot captures.
 */

/** A probe answer in the shape the mock's UniFi route produces. */
function probe(over: Partial<ControllerProbe> = {}): ControllerProbe {
  return {
    reachability: "ok",
    warnings: [
      { kind: "access-point-unconfirmed", params: { vendor: "unifi" } },
      { kind: "capabilities-unverified", params: { count: 3 } },
    ],
    unverified: ["LiveReauthorize", "HardQuota", "DynamicVLAN"],
    version: "9.4.19",
    versionSupported: true,
    minimumVersion: "9.1.105",
    flavour: "UniFi OS console",
    vendorSites: [{ id: "default", name: "Default" }],
    certificateFingerprint: null,
    checks: [],
    ...over,
  };
}

const render = (p: ControllerProbe) =>
  renderToStaticMarkup(<ProbeWarnings warnings={controllerProbeWarnings(p)} />);

/**
 * The markup as an operator reads it: tags out, entities back.
 *
 * Asserting against raw markup looks like it works and quietly cannot match half of
 * this console's copy — `renderToStaticMarkup` escapes an apostrophe to `&#x27;`, so
 * every sentence with a possessive in it fails a `toContain` for reasons that have
 * nothing to do with the screen. The two that matter here both have one.
 */
const reads = (p: ControllerProbe) =>
  render(p)
    .replace(/<[^>]+>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&");

describe("the controller probe dialog shows what the probe warned about", () => {
  it("renders the standing pair a healthy UniFi probe always carries", () => {
    const markup = reads(probe());

    // The two constants, and the notice that says outright that this is what the
    // driver is rather than what is wrong today.
    expect(markup).toContain(t("nas.probeWarning.standingTitle"));
    expect(markup).toContain(t("nas.probeWarning.accessPointUnconfirmed"));

    // And no *Found on this device* heading, because nothing was. A healthy device
    // that reports a fault is the failure the split was built to prevent.
    expect(markup, "a healthy controller reported a fault").not.toContain(
      t("nas.probeWarning.foundTitle"),
    );
  });

  it("does not repeat the version floor the dedicated notice already states", () => {
    /*
     * The dedup decision, and the reason this screen filters at all. The urgent
     * notice above the check rows says *"Needs 9.1.105 or newer"* with the target in
     * it; the same fact as the third bullet of a warning list, in the same run, is
     * one operator reading one thing twice and trusting neither.
     */
    const below = probe({ version: "8.0.24", versionSupported: false });
    below.warnings = [
      ...below.warnings,
      { kind: "version-below-floor", params: { version: "8.0.24", minimumVersion: "9.1.105" } },
    ];

    expect(controllerProbeWarnings(below).map((w) => w.kind)).toEqual([
      "access-point-unconfirmed",
      "capabilities-unverified",
    ]);

    const markup = reads(below);
    expect(markup).toContain(t("nas.probeWarning.standingTitle"));
    expect(markup).not.toContain("9.1.105");
    // The rest is not collateral. Dropping the version bullet must not drop the two
    // that were never being shown in the first place.
    expect(markup).toContain(t("nas.probeWarning.accessPointUnconfirmed"));
  });

  it("keeps the version warning when the dedicated notice cannot render", () => {
    /*
     * `minimumVersion` is nullable, and the urgent notice needs it: a verdict with no
     * target sends an operator to find out for themselves, which is why that notice
     * requires both fields. A probe that answers `versionSupported: false` with no
     * floor therefore renders no notice — and an unconditional filter would take the
     * only statement of the problem off the screen in exactly the case where the
     * screen says least.
     */
    const noFloor = probe({ version: "8.0.24", versionSupported: false, minimumVersion: null });
    noFloor.warnings = [
      ...noFloor.warnings,
      { kind: "version-below-floor", params: { version: "8.0.24", minimumVersion: null } },
    ];

    expect(controllerProbeWarnings(noFloor).map((w) => w.kind)).toContain("version-below-floor");

    const markup = reads(noFloor);
    expect(markup, "the one actionable warning vanished with the notice that carries it")
      .toContain(t("nas.probeWarning.foundTitle"));
  });

  it("files a kind this console has never heard of as found, not as standing", () => {
    /*
     * The default `splitProbeWarnings` sets, asserted again through this screen's own
     * filter because the filter is the only thing between the answer and that split.
     * A condition nobody has decided about, rendered under *Always true of this
     * driver, nothing to do*, is the one mistake that cannot be noticed from the
     * screen.
     */
    const odd = probe();
    odd.warnings = [
      {
        kind: "coa-acknowledged-unknown-session" as never,
        detail: "The controller acknowledged a session id it cannot have issued.",
      },
    ];

    const markup = reads(odd);
    expect(markup).toContain(t("nas.probeWarning.foundTitle"));
    expect(markup).toContain("cannot have issued");
  });

  it("renders nothing at all when the probe warned about nothing", () => {
    // A driver with nothing standing about it — the notice stack must not become a
    // permanent empty frame under the check rows.
    expect(render(probe({ warnings: [] }))).toBe("");
  });
});
