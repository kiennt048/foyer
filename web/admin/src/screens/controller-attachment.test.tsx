import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Controller } from "@foyer/contract";
import { t } from "../i18n/controllers.js";
import { ACTION_LABEL, SitesCell, controllerAction, controllerAttachment } from "./Controllers.js";

/**
 * **An unattached controller, the half the server cannot answer for.**
 *
 * `internal/apistore` lists a controller that has been probed and adopted by nobody,
 * so this screen has to render that state. Said as `0 of 1 adopted`, in a column that
 * drops on a phone, beside a green *Reachable* pill and a button labelled *Probe*,
 * every signal on the row would read fine and the one step that makes the controller
 * useful would be named nowhere.
 *
 * These cases assert the **sentences an operator reads**, not the branch that chose
 * them, because a test of the discriminator alone is satisfied by a screen that
 * discriminates and then prints the same thing twice.
 *
 * `renderToStaticMarkup` for the reason `gallery.test.tsx` gives: these components
 * hold no data, make no request and read nothing from the browser, so the first frame
 * is the whole of the answer.
 */

/** A controller row, with only the fields these two functions read. */
function controller(over: Partial<Controller> = {}): Controller {
  return {
    id: "ctl-1",
    name: "UniFi — Cloud Key",
    vendor: "unifi",
    baseUrl: "https://10.20.0.2",
    controllerId: null,
    authMode: "api-key",
    tlsMode: "trust-on-first-use",
    credentialSet: true,
    certificateFingerprint: null,
    reachability: "ok",
    version: "9.4.19",
    versionSupported: true,
    vendorSites: [],
    lastContactAt: null,
    createdAt: new Date(0).toISOString(),
    ...over,
  };
}

const site = (id: string) => ({ id: "default", name: "Default", boundSiteId: id });
const free = (id = "annexe") => ({ id, name: "Annexe", boundSiteId: null });

/** The markup as an operator reads it: tags out, entities back. */
const reads = (node: React.ReactElement) =>
  renderToStaticMarkup(node)
    .replace(/<[^>]+>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/\s+/g, " ")
    .trim();

describe("a controller that is attached to nothing says so", () => {
  it("distinguishes sites-not-read from adopted-by-nobody", () => {
    // Two absences, two afternoons. Nothing has read the sites — press Probe — and
    // read, adopted by nobody — press Attach. Collapsing them sends an operator to
    // the wrong button, which is the same three-valued rule the Version column
    // follows for `null` against `false`.
    expect(controllerAttachment(controller({ vendorSites: [] }))).toBe("sites-not-read");
    expect(controllerAttachment(controller({ vendorSites: [free("default")] }))).toBe("unattached");
    expect(controllerAttachment(controller({ vendorSites: [site("site-hcm")] }))).toBe("attached");
    // Partly adopted is adopted. The list only ever carries controllers bound here
    // or bound nowhere, so any binding on a row is a binding to this venue.
    expect(controllerAttachment(controller({ vendorSites: [site("site-hcm"), free()] }))).toBe(
      "attached",
    );
  });

  it("renders a state and not a count", () => {
    const unattached = reads(<SitesCell c={controller({ vendorSites: [free("default")] })} />);
    expect(unattached).toContain(t("controllers.sites.unattached"));
    // A count is what would make the row look finished.
    expect(
      unattached,
      "the unattached row still reads as a count, which is the figure that made a controller one step short of working look complete",
    ).not.toContain(t("controllers.sites.bound", 0, 1));

    // And the two states either side of it, so the assertion above is not satisfied
    // by a cell that renders the same thing for everything.
    //
    // `Absent`'s sentence lives in a `title` attribute, so it is asserted on the
    // markup and not on the text. Read off the rendered text this would be green
    // whether the reason was there or not — `docs/rules/testing.md`'s assertion
    // aimed where the subject does not appear.
    const notRead = renderToStaticMarkup(<SitesCell c={controller({ vendorSites: [] })} />);
    expect(notRead).toContain(`title="${t("controllers.sites.none")}"`);
    expect(reads(<SitesCell c={controller({ vendorSites: [] })} />)).toBe("–");
    expect(reads(<SitesCell c={controller({ vendorSites: [site("site-hcm"), free()] })} />)).toContain(
      t("controllers.sites.bound", 1, 2),
    );
  });

  it("offers the step, in the column that survives a phone", () => {
    // The Sites column is `narrow: "drop"`, so on a phone this label is the only
    // place the state appears at all.
    const attachable = controller({ vendorSites: [free("default")], reachability: "ok" });
    expect(ACTION_LABEL[controllerAction(attachable)]).toBe(t("controllers.attach"));

    // Never probed: the next step is a probe, and the Status pill already says so.
    expect(
      ACTION_LABEL[controllerAction(controller({ vendorSites: [], reachability: "unknown" }))],
    ).toBe(t("controllers.probe"));

    // Adopted: nothing to attach.
    expect(
      ACTION_LABEL[controllerAction(controller({ vendorSites: [site("site-hcm")] }))],
    ).toBe(t("controllers.probe"));

    // **The precondition, and it is not decoration.** `discoverFromController` is
    // refused for anything but `ok`, so a button promising to attach a controller
    // whose credential has expired opens a dialog with the control disabled — a
    // label that states an outcome the server will refuse.
    for (const reachability of ["credentials-expired", "unreachable", "unknown"] as const) {
      expect(
        ACTION_LABEL[controllerAction(controller({ vendorSites: [free("default")], reachability }))],
        `a ${reachability} controller was offered an attach the server would refuse`,
      ).toBe(t("controllers.probe"));
    }
  });

  it("says what discovery is about to attach, and names the venue", () => {
    // Binding is a side effect of reading access points, and nothing in the probe
    // dialog had ever named it. The sentence has to survive the venue name being
    // absent while `GET /sites` is in flight, so both forms make the same claim.
    expect(t("controllers.discover.attaches", "The Riverside")).toContain("The Riverside");
    for (const copy of [t("controllers.discover.attaches", "X"), t("controllers.discover.attachesHere")]) {
      expect(copy).toMatch(/not attached/i);
      expect(copy).toMatch(/attaches it/i);
    }
  });
});
