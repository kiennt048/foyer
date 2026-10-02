import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test, type Locator, type Page } from "@playwright/test";
import { tableReady } from "../helpers/admin.js";
import { fieldPair, fieldPairFaults, unreachableRefusal } from "../helpers/fields.js";
import { expectNasRemoved, listNas, mockTick } from "../helpers/mock.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `coa-vendor` — the question the console offered no way to answer
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `coa` is the one `NasDriverKind` that names a *family* rather than a
 * vendor: Cisco IOS-XE identifies a session by a `Cisco-AVPair audit-session-id` VSA,
 * Aruba by `Calling-Station-Id` in its own spelling, and RFC 5176 §2.3 makes the NAS
 * match on every session-identification attribute in the request. So the wrong
 * dialect matches nothing — a careful device answers `Error-Cause 503` and **a
 * careless one ignores the request and acknowledges it**, which is a revocation that
 * reports success and revoked nobody. `docs/rules/testing.md`: *"ACKing a CoA is
 * trivial; faithfully rejecting one is not"*, so neither the lab nor a typed error
 * can catch a wrong guess. `createNas` therefore refuses a `coa` registration that
 * names no vendor.
 *
 * The console did not catch up, and the shape of the gap is the point:
 *
 *   - `RegisterWizard` derives its driver list from `DRIVER_SHAPES`, so it **offered
 *     `coa`** — correctly — and rendered no vendor control. Choosing it produced a
 *     `422` marking `coaVendor`, and the form's own page-level sentence, *"Correct
 *     the fields marked below and try again"*, was then a lie: nothing was marked,
 *     and there was nothing to correct.
 *   - `NasDetail` had the mirror gap. `Nas.coaVendor` was on the wire and no screen
 *     rendered it, so the value that decides whether a revocation reaches anything at
 *     all could not be read back.
 *
 * ── WHAT THIS FILE REFUSES TO ASSERT ─────────────────────────────────────────
 *
 *  1. *That a `#nas-coa-vendor` element exists.* Green for a control the refusal
 *     never reaches, which is most of the defect. `unreachableRefusal` in
 *     `helpers/fields.ts` judges the refusal, not the element, and its faults say so
 *     in those words — see the sabotage in the second test, which removes the control
 *     while leaving `coa` on offer and requires the report to name a refusal the
 *     operator could not act on rather than a selector that missed.
 *  2. *That the field appears for `coa`.* A hardcoded driver name is a second copy of
 *     `DRIVER_SHAPES`, which is the exact class of defect this whole change is about.
 *     The first test reads the table out of `contract/src/index.ts` and requires the
 *     console's answer to match it **for every driver the console offers**, so a
 *     driver that gains a required field is a red run here rather than a `422` an
 *     operator meets.
 *  3. *That the wizard can register a `coa` device.* On its own that is satisfied by
 *     a form that asks for everything under the sun. `everyOfferedDriverRegisters`
 *     fills only **what the form renders** and requires the server not to refuse a
 *     field — which is the generalisation of the defect rather than a lock on its one
 *     instance.
 *  4. *That "Aruba" is on the detail screen.* No fixture carries `aruba`, and the
 *     third test proves that from the API before it looks for the word — otherwise
 *     the assertion would pass against a screen rendering somebody else's device.
 *
 * ── HOW THE REFUSAL IS DRIVEN ────────────────────────────────────────────────
 *
 * By pressing what an operator would press: the form sends the body the operator
 * filled in, so *Not chosen* really does reach the server and the server really does
 * refuse it. That is deliberate on the console's side too — a local pre-check would
 * have made this refusal unreachable from the console's own controls, and the whole
 * finding is that the refusal had nowhere to land. See the comment on the submit
 * handler in `RegisterWizard.tsx`.
 */

/* ── the catalogue, quoted ─────────────────────────────────────────────────── */

/**
 * The console's own sentences, copied from `admin/src/i18n/` with the key. A `nas.*`
 * key beside a device page lives in `nas.ts`, everything else in `core.ts`.
 *
 * Typed in rather than imported, for the reason `error-architecture.spec.ts` gives at
 * length: a test that reads the string from the module the console renders it from
 * passes for any two strings that happen to match, including the server's. The mock's
 * prose for this fault is a bare *"Required."*, so an assertion that found that
 * sentence on screen would be finding the server's words in the console's mouth.
 */
const COPY = {
  /** `fault.coaVendor` */
  faultCoaVendor: "Required — Foyer will not guess which of the two this is.",
  /** `problem.validation.body` */
  validation: "Correct the fields marked below and try again.",
  /** `nas.create.coaVendorUnset` — the option that means nobody said */
  vendorUnset: "Not chosen",
  /** `nas.coaVendor.aruba` */
  aruba: "Aruba",
  /** `nas.device.coaVendor` */
  vendorLabel: "CoA vendor",
  /** `nas.device.coaVendorUnset` */
  vendorNobodySaid: "Not chosen",
  /** `nas.device.coaVendorUnsetWhy` */
  vendorNobodySaidWhy:
    "Foyer refuses to drive this device rather than guess a dialect. Naming one means registering it again.",
  /** `nas.device.coaPortAbsent` */
  portAbsent: "Does not use CoA.",
  /** `nas.device.coaPortDefault` */
  portDefault: "Not set — Foyer sends to 3799. Cisco often uses 1700.",
} as const;

/* ── the authority, parsed rather than restated ────────────────────────────── */

interface Shape {
  controller: string;
  coaVendor: string;
}

/**
 * `DRIVER_SHAPES`, read out of the module that declares it.
 *
 * `@foyer/e2e` does not depend on `@foyer/contract` and should not start: this suite
 * drives a browser and must stay able to run against a build it did not compile. What
 * it *can* do is read the source of the one table three other places already agree
 * about — the same move `error-architecture.spec.ts` makes on `openapi.yaml`'s
 * `ProblemType` enum, and for the same reason. A copy of the table in this file would
 * be the fourth, and the fourth is the one that drifts.
 */
function driverShapes(): Record<string, Shape> {
  const path = fileURLToPath(new URL("../../../contract/src/index.ts", import.meta.url));
  const source = readFileSync(path, "utf8");
  const start = source.indexOf("export const DRIVER_SHAPES");
  expect(start, "DRIVER_SHAPES is not declared in contract/src/index.ts").toBeGreaterThan(-1);
  const table = source.slice(start, source.indexOf("\n};", start));

  const out: Record<string, Shape> = {};
  for (const row of table.matchAll(/^ {2}(\w+): \{([^}]*)\}/gm)) {
    const body = row[2] ?? "";
    const controller = /controller: "([a-z-]+)"/.exec(body)?.[1];
    const coaVendor = /coaVendor: "([a-z-]+)"/.exec(body)?.[1];
    expect(controller, `DRIVER_SHAPES.${row[1]} declares no controller`).toBeTruthy();
    expect(coaVendor, `DRIVER_SHAPES.${row[1]} declares no coaVendor`).toBeTruthy();
    out[row[1]!] = { controller: controller!, coaVendor: coaVendor! };
  }
  expect(Object.keys(out).length, "no driver rows were parsed out of DRIVER_SHAPES").toBeGreaterThan(
    3,
  );
  return out;
}

/** The drivers a device is registered under by hand — the wizard's own derivation. */
const manualDrivers = (shapes: Record<string, Shape>) =>
  Object.keys(shapes).filter((d) => shapes[d]!.controller !== "required");

/* ── driving the wizard ────────────────────────────────────────────────────── */

/** Every registration this run made, so `afterAll` can prove it took them back out. */
const registered: { name: string; tick: number }[] = [];
let registrations = 0;
const nasName = () => `coa-vendor ${Date.now()}-${++registrations}`;

test.afterAll(async ({ request }) => {
  /* The mock keeps its world across runs: a device left behind is in every later row
     count, dashboard figure and screenshot until somebody restarts it by hand. */
  for (const { name, tick } of registered.splice(0)) {
    await expectNasRemoved(request, name, tick);
  }
});

/** `/access-points/new` renders the list with the wizard open on step one. */
async function openWizard(page: Page): Promise<Locator> {
  await page.goto("/access-points/new");
  await tableReady(page);
  const dialog = page.locator("dialog[open]");
  await expect(dialog.locator("#nas-driver")).toBeVisible();
  return dialog;
}

const VENDOR = "#nas-coa-vendor";

/**
 * The refusal the server sent, or `null` if it accepted.
 *
 * Read off the wire in the same test that drives it, so *which* field was refused is
 * a measurement rather than an assumption — everything downstream keys on it.
 */
async function submitRegistration(page: Page, dialog: Locator) {
  const answered = page.waitForResponse(
    (r) => r.request().method() === "POST" && /\/api\/v0\/nas$/.test(r.url()),
  );
  await dialog.locator('form button[type="submit"]').click();
  const res = await answered;
  const body = (await res.json()) as {
    params?: { fields?: Record<string, string> };
    nas?: { id: string };
  };
  return { status: res.status(), fields: body.params?.fields ?? {}, nas: body.nas };
}

/**
 * Fill in everything the form renders, and nothing it does not.
 *
 * The whole point of `everyOfferedDriverRegisters`: a helper that knew a `coa` device
 * needs a vendor would be asserting its own knowledge. This one answers whatever
 * questions are on screen — every empty `<select>` gets its first real option — so a
 * driver whose required field the form does not render fails at the server, which is
 * exactly the defect this file guards.
 */
async function answerEveryQuestion(dialog: Locator, name: string): Promise<void> {
  await dialog.locator("#nas-name").fill(name);
  await dialog.locator("#nas-ip").fill("10.90.0.253");
  for (const select of await dialog.locator("form select").all()) {
    if ((await select.inputValue()) !== "") continue;
    const options = await select.locator("option").all();
    const real = options[1];
    expect(real, "a select opens on an empty value and offers nothing else").toBeTruthy();
    await select.selectOption(await real!.getAttribute("value") ?? "");
  }
}

/* ══ the vendor ═══════════════════════════════════════════════════════════════ */

test.describe("a CoA device is asked which vendor it is", () => {
  test("the vendor is asked for exactly the drivers the table says need one", async ({ page }) => {
    const shapes = driverShapes();
    const dialog = await openWizard(page);

    const offered = await dialog.locator("#nas-driver option").evaluateAll((os) =>
      os.map((o) => (o as HTMLOptionElement).value),
    );
    expect(
      offered.sort(),
      "the wizard's driver list has drifted from DRIVER_SHAPES — it offers exactly the " +
        "drivers whose controller is not `required`",
    ).toEqual(manualDrivers(shapes).sort());

    /* Not vacuous: the offered set has to contain one driver of each kind, or the
       loop below asserts the same answer every time and would be green against a
       console that renders the field always, or never. */
    const needs = offered.filter((d) => shapes[d]!.coaVendor === "required");
    expect(needs.length, "no offered driver requires a CoA vendor").toBeGreaterThan(0);
    expect(
      offered.length - needs.length,
      "every offered driver requires a CoA vendor, so this case cannot tell a " +
        "conditional field from an unconditional one",
    ).toBeGreaterThan(0);

    for (const driver of offered) {
      await dialog.locator("#nas-driver").selectOption(driver);
      const wanted = shapes[driver]!.coaVendor === "required";
      await expect(
        dialog.locator(VENDOR),
        `DRIVER_SHAPES.${driver}.coaVendor is "${shapes[driver]!.coaVendor}" and the form ` +
          `${wanted ? "does not ask" : "asks"} for one`,
      ).toBeVisible({ visible: wanted });

      if (!wanted) continue;
      /* Nobody-said is the initial state, not a vendor. Whichever one a form
         pre-selected would be the guess this field exists to prevent, and the
         database can hold *unset* — so the form has to be able to. */
      await expect(dialog.locator(VENDOR)).toHaveValue("");
      await expect(dialog.locator(`${VENDOR} option[value=""]`)).toHaveText(COPY.vendorUnset);
    }
  });

  test("the refusal lands on the control that answers it", async ({ page }) => {
    const dialog = await openWizard(page);
    await dialog.locator("#nas-driver").selectOption("coa");
    await dialog.locator("#nas-name").fill(nasName());
    await dialog.locator("#nas-ip").fill("10.90.0.251");
    /*
     * The vendor is left exactly as the form opened it — *Not chosen*, the state the
     * column holds — and **nothing here touches that control before submitting**. That
     * is deliberate: a `fill` or a `toHaveValue` on it first would make this test fail
     * as a missing selector the moment the field is taken away, which is precisely the
     * report that tells a reader nothing. The first thing asserted below is that the
     * refusal reached somebody. `#nas-coa-vendor` opening on `""` is test one's
     * business.
     */
    const refusal = await submitRegistration(page, dialog);
    expect(refusal.status).toBe(422);
    expect(
      refusal.fields,
      "the server refused something other than the vendor, so what follows would be " +
        "measuring a different refusal",
    ).toEqual({ coaVendor: "required" });

    /* The rung that matters: the refusal reaches a control that is really on screen. */
    expect(
      await unreachableRefusal(page, "dialog[open]", "coaVendor", COPY.faultCoaVendor),
    ).toEqual([]);
    /* And the pair beneath it — the mark and the message are joined. */
    expect(fieldPairFaults(await fieldPair(page, `dialog[open] ${VENDOR}`), COPY.faultCoaVendor))
      .toEqual([]);

    /* The page-level sentence, which was the lie. It said *"Correct the fields marked
       below"* over a form with no such field; it is true now because of the two
       assertions above, not on its own. */
    await expect(dialog).toContainText(COPY.validation);
    /* The server's own prose for this fault is a bare "Required.", and the console
       owns the sentence — so the two must differ and only the console's may show. */
    await expect(dialog.locator("#nas-coa-vendor-error")).toHaveText(COPY.faultCoaVendor);

    /* Nothing was registered: a refused registration must not leave a device behind,
       and the flow is still on step one rather than at the shared secret. */
    await expect(page.locator("dialog .secret")).toHaveCount(0);

    /*
     * ── the sabotage ──────────────────────────────────────────────────────────
     *
     * The defect, put back into the page: `coa` still on offer, and no control to
     * answer with. The request still goes out — the console does not pre-check this
     * field, which is what keeps the server's refusal drivable — so the server refuses
     * exactly as before and the console renders the sentence into a subtree that is no
     * longer in the document.
     *
     * The requirement is on the *wording* as much as on the redness. A predicate that
     * reported "no element matched #nas-coa-vendor" would be telling a reader their
     * selector is stale, which is the one thing that is not wrong here.
     */
    await page.evaluate(() => {
      document.querySelector("dialog[open] #nas-coa-vendor")?.closest(".field")?.remove();
    });
    await dialog.locator("#nas-name").fill(nasName());
    const again = await submitRegistration(page, dialog);
    expect(again.status, "the sabotage stopped the request rather than the control").toBe(422);
    expect(again.fields).toEqual({ coaVendor: "required" });

    const faults = await unreachableRefusal(page, "dialog[open]", "coaVendor", COPY.faultCoaVendor);
    expect(
      faults,
      "the vendor control was removed while `coa` stayed on offer — the unfixed state — " +
        "and the predicate called the refusal actionable, which means it is not " +
        "checking that the refusal reaches a control",
    ).not.toEqual([]);
    expect(
      faults.join(" "),
      "the report names a missing element rather than a refusal nobody can act on",
    ).toContain("coaVendor");
    expect(faults.join(" ")).toContain("not on screen");
  });

  test("every driver the wizard offers can be registered with what the form asks", async ({
    page,
  }) => {
    /*
     * The generalisation, and the reason this file is not only about `coaVendor`.
     *
     * A missing vendor control becomes visible only when a server-side refusal names
     * it. A driver whose required field nobody has tried is invisible in exactly the
     * same way — so
     * this walks the list the console itself offers, answers only the questions the
     * console itself asks, and requires the server to accept. A field the form does
     * not render is a `422` here, named, with the driver beside it.
     */
    const shapes = driverShapes();
    for (const driver of manualDrivers(shapes)) {
      const dialog = await openWizard(page);
      await dialog.locator("#nas-driver").selectOption(driver);
      const name = nasName();
      const tick = await mockTick(page.context().request);
      await answerEveryQuestion(dialog, name);

      const result = await submitRegistration(page, dialog);
      expect(
        result.fields,
        `registering a \`${driver}\` device was refused for fields the registration form ` +
          `never asked about — the form and DRIVER_SHAPES disagree about what this driver needs`,
      ).toEqual({});
      expect(result.status).toBe(201);
      registered.push({ name, tick });
      await expect(page.locator("dialog .secret")).toBeVisible();
    }
  });

  test("the vendor chosen in the wizard is the vendor the device screen states", async ({
    page,
    context,
  }) => {
    /* `aruba` first has to be a word this world does not already contain, or finding
       it on a screen proves nothing about the round trip. */
    const before = await listNas(context.request);
    expect(
      before.filter((n) => n.coaVendor === "aruba"),
      "the fixtures already hold an Aruba device, so the assertion below would pass " +
        "against a screen showing somebody else's",
    ).toEqual([]);

    const dialog = await openWizard(page);
    await dialog.locator("#nas-driver").selectOption("coa");
    const name = nasName();
    const tick = await mockTick(context.request);
    await dialog.locator("#nas-name").fill(name);
    await dialog.locator("#nas-ip").fill("10.90.0.252");
    await dialog.locator(VENDOR).selectOption("aruba");

    const created = await submitRegistration(page, dialog);
    expect(created.status).toBe(201);
    registered.push({ name, tick });

    /* Through the secret step — the checkbox rather than the clipboard, which is a
       permission the browser may refuse — and on to the device screen. */
    await expect(page.locator("dialog .secret")).toBeVisible();
    await page.locator("dialog[open] .ack input[type=checkbox]").check();
    await page.locator("dialog[open] .dialog-foot .btn-primary").click();
    await page.locator("dialog[open] .dialog-foot .btn-primary").click();
    await expect(page).toHaveURL(new RegExp(`/access-points/${created.nas!.id}$`));

    const device = page.locator("section.section", { hasText: COPY.vendorLabel }).first();
    await expect(
      device,
      "the device screen does not say which vendor the operator registered it as",
    ).toContainText(COPY.aruba);
  });

  test("a device nobody named a vendor for says so, and says what it costs", async ({
    page,
    context,
  }) => {
    /*
     * The state `createNas` now prevents and the world still contains: the contract
     * says in as many words that *"a `coa` row predating this field has no vendor and
     * never will until somebody says"*. No fixture holds one — registration is the
     * only writer and it refuses — so the branch that renders it has no other way to
     * be executed, and a branch that has never executed is the species this repository
     * keeps finding one rung below where it looks.
     *
     * Rewriting the response is the narrowest way to reach it from a package that owns
     * neither the fixtures nor the mock.
     */
    const coaDevice = (await listNas(context.request)).find((n) => n.driver === "coa");
    expect(coaDevice, "no `coa` device in the world to take the vendor away from").toBeTruthy();

    await page.goto(`/access-points/${coaDevice!.id}`);
    const cell = page
      .locator("dl.kv dt", { hasText: /^CoA vendor$/ })
      .locator("xpath=following-sibling::dd[1]");
    /* Named first, so the absence below is a change rather than the only state seen. */
    await expect(cell).not.toContainText(COPY.vendorNobodySaid);

    await context.route(`**/api/v0/nas/${coaDevice!.id}`, async (route) => {
      const res = await route.fetch();
      route.fulfill({ response: res, json: { ...(await res.json()), coaVendor: null } });
    });
    await page.reload();
    /* A pill rather than the `–` used one row up: an absent port is a value Foyer
       fills in, an absent vendor is a device it will not drive. */
    await expect(cell.locator(".pill")).toHaveText(COPY.vendorNobodySaid);
    await expect(
      cell,
      "the screen says the vendor is missing and not what that costs, which is the " +
        "half an operator has to act on",
    ).toContainText(COPY.vendorNobodySaidWhy);
  });
});

/* ══ the port the vendor is reached on ════════════════════════════════════════ */

/**
 * The same defect one row up, found while auditing what else reads `DRIVER_SHAPES`.
 *
 * *"Does not use CoA."* is right for Ruckus and Omada and false for the two drivers
 * whose only enforcement mechanism is CoA — and those two are exactly the ones the
 * registration wizard offers, which sends no port at all. So every device registered
 * by hand against the Go server reaches this row with a null and is told it does not
 * use the mechanism it is driven by.
 *
 * **Nothing in this project could see it.** The mock fills the port in from a table of
 * its own where the Go adapter writes the operator's value or nothing, and no fixture
 * pairs a CoA driver with a null port — so the false sentence is unreachable in the
 * only world the console is ever driven against. That is the species
 * `docs/rules/testing.md` calls *a world that cannot express a property*, and the
 * response is the one that section prescribes: make the world able to express it.
 * Rewriting the response is the narrowest way to do that from a package that owns
 * neither the fixtures nor the mock.
 *
 * Both branches, or the "fix" is satisfied by deleting the sentence.
 */
test.describe("a CoA device is not told it does not use CoA", () => {
  /**
   * The CoA-port value cell, found through its own label rather than by position.
   *
   * A `.page`-wide text assertion cannot see the shape of this row, and it is worth
   * writing down: **`Absent`'s sentence is a `title` attribute**, so `toContainText`
   * can neither find it nor fail to find it, and both halves of the check would be
   * green whatever the screen said. Two of the three assertions below are on the
   * attribute, because that is where the sentence actually lives — and where a false
   * sentence goes unnoticed.
   */
  const coaPortCell = (page: Page) =>
    page.locator("dl.kv dt", { hasText: /^CoA port$/ }).locator("xpath=following-sibling::dd[1]");

  test("an absent port means the default for a CoA driver and no CoA for the others", async ({
    page,
    context,
  }) => {
    const all = await listNas(context.request);
    const withPort = all.find((n) => n.driver === "mikrotik" && Boolean(n.coaPort));
    const withoutCoa = all.find((n) => n.driver === "omada");
    expect(withPort, "no CoA-driven device in the world carries a port").toBeTruthy();
    expect(withoutCoa, "no controller-API device in the world to hold the other branch")
      .toBeTruthy();

    /* The healthy render first, so what the drive produces below is a second
       measurement rather than a measurement and a memory. */
    await page.goto(`/access-points/${withPort!.id}`);
    await expect(coaPortCell(page)).toHaveText(String(withPort!.coaPort));

    /* The state the Go server produces on every manual registration and the mock
       cannot: a CoA driver with no port. */
    await context.route(`**/api/v0/nas/${withPort!.id}`, async (route) => {
      const res = await route.fetch();
      route.fulfill({ response: res, json: { ...(await res.json()), coaPort: null } });
    });
    await page.reload();
    await expect(
      coaPortCell(page),
      "a CoA-driven device with no port says nothing about the port Foyer will use",
    ).toContainText(COPY.portDefault);
    await expect(
      coaPortCell(page).locator(".empty-value"),
      "a device whose only enforcement mechanism is CoA is told it does not use CoA",
    ).not.toHaveAttribute("title", COPY.portAbsent);

    /* And the sentence still stands where it is true, or this was a deletion rather
       than a repair. */
    await context.unroute(`**/api/v0/nas/${withPort!.id}`);
    await page.goto(`/access-points/${withoutCoa!.id}`);
    await expect(coaPortCell(page).locator(".empty-value")).toHaveAttribute(
      "title",
      COPY.portAbsent,
    );
    await expect(coaPortCell(page)).not.toContainText(COPY.portDefault);
  });
});
