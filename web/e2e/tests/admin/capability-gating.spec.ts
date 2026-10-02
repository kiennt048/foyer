import { expect, test } from "@playwright/test";
import { admin, actionControlCount, tableReady } from "../helpers/admin.js";
import {
  driversThatCannotPushASecret,
  expectedGatedControls,
  getNas,
  listNas,
  listSessions,
  oneNasPerSecretState,
  ratesEveryDriverCanApply,
  ratesNoDriverCanApply,
  rotationRefused,
} from "../helpers/mock.js";

/**
 * The one control on this screen that is deliberately **not** capability-gated.
 *
 * `.secret-rotate-note` is the standing consequence rendered beside the button, and
 * `data-manual` says which of the two sentences it is carrying — `true` when the
 * driver cannot push, so the operator is being told the price before they commit.
 * A semantic attribute, not copy: the admin catalogue is being edited while this
 * suite exists, and an assertion on a label would fail on a rewording.
 */
const rotateSection = {
  /** The consequence, standing on the page. Never rendered inside the dialog. */
  note: "p.secret-rotate-note",
  manualNote: 'p.secret-rotate-note[data-manual="true"]',
  /** "Generate new secret" — located as the button in the card the note sits in. */
  card: ".card:has(p.secret-rotate-note)",
  /** The confirmation the button opens, and its irreversible half. */
  openDialog: "dialog[open]",
  dialogBody: "dialog[open] .dialog-body",
  dialogConfirm: "dialog[open] .dialog-foot button.btn-danger",
  /** The new value, shown exactly once. It must never appear after a refusal. */
  revealedSecret: ".secret",
} as const;

/**
 * Collapses whitespace so two renderings of the same sentence compare equal.
 *
 * Used only to compare the page against itself — never against a literal. Nothing
 * in this suite may assert user-facing copy; the admin catalogue is being rewritten
 * while these tests run.
 */
const normalise = (s: string) => s.replace(/\s+/g, " ").trim();

/**
 * Capability-conditional UI — the highest-value assertion in the admin suite.
 *
 * `docs/rules/frontend.md` names this as "the single easiest thing to get wrong
 * in this phase and the most expensive to discover later", and the reason is
 * mechanical: **a mocked admin UI will happily render every control for every NAS.**
 * Nothing pushes back during development, because nothing is actually talking to
 * hardware that would refuse. The bug ships and is found on a customer's Omada
 * controller, months later, as "the VLAN field does nothing".
 *
 * So the assertion is comparative and computed, not a number typed into a test:
 *
 *   • `nas-omada-1` — the low-capability driver every fixture set is required to
 *     contain. `walledGarden: "none"`, everything else false.
 *   • `nas-mt-1` — MikroTik: live reauthorization, VLANs, bandwidth limits, and an
 *     IP-range garden.
 *
 * The expected control count for each is derived from the NAS's own `Capabilities`
 * via the same rule `<IfCapable>` uses. If the gate and the capability model ever
 * disagree, this fails and says which way.
 *
 * **Hide, not disable** is the rule under test. A disabled control with a tooltip
 * still advertises a feature the hardware does not have, and an operator will go
 * hunting for the setting that turns it on.
 */
test.describe("capability gating on the access-point detail screen", () => {
  test("a minimal-capability NAS renders strictly fewer gated controls", async ({
    page,
    context,
  }) => {
    const minimal = await getNas(context.request, "nas-omada-1");
    const capable = await getNas(context.request, "nas-mt-1");

    /* Sanity: the fixtures still are what this test is about. If someone makes the
       Omada driver capable, this test should say so rather than quietly weaken. */
    expect(
      expectedGatedControls(minimal),
      "nas-omada-1 is no longer the low-capability fixture. Every fixture set must " +
        "contain at least one driver that can do nothing (docs/rules/frontend.md).",
    ).toBe(0);
    expect(expectedGatedControls(capable)).toBeGreaterThan(0);

    await page.goto("/access-points/nas-omada-1");
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();
    const minimalGated = await page.locator(admin.gatedControls).count();
    const minimalActions = await actionControlCount(page);

    await page.goto("/access-points/nas-mt-1");
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();
    const capableGated = await page.locator(admin.gatedControls).count();
    const capableActions = await actionControlCount(page);

    expect(
      minimalGated,
      `The minimal-capability NAS rendered ${minimalGated} capability-gated controls; ` +
        `its capabilities say it should render ${expectedGatedControls(minimal)}. ` +
        `Controls the driver cannot honour must not exist — not be disabled.`,
    ).toBe(expectedGatedControls(minimal));

    expect(
      capableGated,
      `The high-capability NAS rendered ${capableGated} capability-gated controls; ` +
        `its capabilities say it should render ${expectedGatedControls(capable)}.`,
    ).toBe(expectedGatedControls(capable));

    expect(
      minimalGated,
      "The minimal-capability NAS does not render strictly fewer gated controls.",
    ).toBeLessThan(capableGated);

    /* The coarse backstop, independent of the gated-control locator. */
    expect(
      minimalActions,
      "The minimal-capability NAS offers no fewer actions overall than the capable one.",
    ).toBeLessThan(capableActions);
  });

  /**
   * The other half of the rule, and the one a naive implementation gets backwards.
   *
   * A capability that exists but is *currently* unavailable — a NAS whose controller
   * cannot be reached — is **disabled with a stated reason**, not hidden. The
   * feature is real; only the moment is wrong. Hiding it would tell the operator
   * their hardware cannot do something it can.
   */
  test("a capable but unreachable NAS disables its controls with a reason, rather than hiding them", async ({
    page,
    context,
  }) => {
    const nas = await getNas(context.request, "nas-unifi-3");
    expect(nas.reachability, "nas-unifi-3 is no longer the unreachable fixture.").not.toBe("ok");
    const expected = expectedGatedControls(nas);
    expect(expected, "nas-unifi-3 has no capabilities left to gate.").toBeGreaterThan(0);

    await page.goto("/access-points/nas-unifi-3");
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();

    const gated = page.locator(admin.gatedControls);
    await expect(gated).toHaveCount(expected);

    for (let i = 0; i < expected; i++) {
      const control = gated.nth(i);
      await expect(control, "A supported control on an unreachable NAS is enabled.").toBeDisabled();
      await expect(
        control,
        "A control disabled for unavailability carries no reason.",
      ).toHaveAttribute("title", /.+/);
    }

    /* And the reason is stated at page level too, as an urgent notice. */
    await expect(page.locator(`${admin.notice}[data-level="urgent"]`).first()).toBeVisible();
  });

  /**
   * `WalledGarden: none` is not a missing feature, it is a security posture, and the
   * screen has to say so standing rather than in a tooltip.
   */
  /**
   * ── THE EXEMPTION ────────────────────────────────────────────────────────────
   *
   * **If you are here because "Generate new secret" looks like it should be hidden
   * on a driver whose `rotateSecret` is false — this test is the argument against
   * that, and it is deliberate. Read it before changing it.**
   *
   * The test above derives its expectation from the capability model, which is what
   * makes it worth having: it fails when the gate and the model diverge instead of
   * passing against a number somebody typed. But that same reasoning, applied one
   * row further down the capability table, produces a defect. The model-derived test
   * cannot catch it, because the model-derived test never mentions this control at
   * all. **The one control exempt from the gate is the one control the gating test
   * does not pin.** This is that pin.
   *
   * `rotateSecret` is the driver's ability to **push a new secret onto the device**.
   * *Generating* one is Foyer's own operation and works everywhere. So a false
   * capability here does not make the action impossible, only **manual** — somebody
   * walks to the access point and types the value in. Hiding the button on that
   * basis leaves the site with **no way to rotate a shared secret at all**, and a
   * secret that cannot be rotated is a secret that never is: it outlives the staff
   * who knew it, the laptop it was pasted into and the contractor who set it up.
   *
   * The rule, in the form that survives the next capability someone reads too
   * broadly: a capability says what the **driver** can do on the operator's behalf,
   * not what the product may offer. When it is false, ask whether the action becomes
   * *impossible* — hide it — or merely *manual* — keep it, and state the price
   * beside the control, before the operator commits.
   *
   * Which is why the second assertion below is not optional decoration. A test that
   * only checked the button existed would pass against the version of this screen
   * that had the button and buried the consequence inside the confirmation dialog —
   * and that version *was* the defect. The correction was moving the sentence out to
   * where it is read before the dialog opens, so that is what gets asserted.
   *
   * `docs/architecture.md`, `docs/rules/frontend.md`
   * and the `rotateNasSecret` description in `openapi.yaml` all now say this.
   */
  test("a driver that cannot push a secret keeps the button, and states the price beside it", async ({
    page,
    context,
  }) => {
    const all = await listNas(context.request);
    const cannotPush = driversThatCannotPushASecret(oneNasPerSecretState(all));

    expect(
      cannotPush.map((n) => n.id),
      "No fixture driver has `rotateSecret: false`, so nothing here is under test. " +
        "This assertion exists because the fixture set is what makes the exemption " +
        "observable at all — see `web/fixtures`.",
    ).not.toHaveLength(0);

    for (const nas of cannotPush) {
      await page.goto(`/access-points/${nas.id}`);
      await expect(page.locator(admin.pageTitle)).not.toBeEmpty();

      const card = page.locator(rotateSection.card);
      const button = card.locator("button");

      await expect(
        button,
        `${nas.id}: its driver cannot push a secret, and the console offers no way to ` +
          "generate one. Generating is Foyer's own operation and works on every driver; " +
          "`rotateSecret` gates the *push*, not the *generate*. Hiding this button leaves " +
          "this device unable to rotate its shared secret at all, which is a worse " +
          "product than the contradiction hiding it would remove. Keep it and state the " +
          "consequence — see the comment above this test.",
      ).toHaveCount(1);
      await expect(button).toBeVisible();

      /*
       * Beside the control, on the page, and not only inside the confirmation.
       * `data-manual="true"` is the screen saying *which* consequence it is stating —
       * that rotation here is a manual procedure — without this test reading a word
       * of the copy that says it.
       */
      const note = card.locator(rotateSection.manualNote);
      await expect(
        note,
        `${nas.id}: the manual-rotation consequence is not standing beside the button. ` +
          "It was moved out of the confirmation dialog on purpose: a price the operator " +
          "only learns after committing is not a price they were told.",
      ).toHaveCount(1);
      await expect(note).toBeVisible();
      await expect(note).not.toBeEmpty();

      /* And it really is on the page rather than in a dialog that happens to be open. */
      expect(
        await note.evaluate((el) => el.closest("dialog") !== null),
        `${nas.id}: the consequence renders inside a dialog. It has to be readable ` +
          "before anything is opened.",
      ).toBe(false);

      /*
       * The exemption stated relative to the gate rather than restated on its own:
       * this control is not a member of the set the test above counts. That is the
       * link between the two — the count stays model-derived, and this one control
       * is pinned as being outside it.
       */
      expect(
        await button.evaluate((el, selector) => el.matches(selector), admin.gatedControls),
        `${nas.id}: "Generate new secret" is now inside the capability-gated group. ` +
          "It is exempt on purpose; putting it back under the gate re-creates the defect.",
      ).toBe(false);

      /*
       * Where the device is still reachable, the rotation is merely manual, so the
       * control must actually be usable. Where it is *not* reachable the contract
       * refuses with 409 and the console is meant to disable it with that reason —
       * a different state, and not one this assertion should force.
       */
      if (!rotationRefused(nas)) {
        await expect(
          button,
          `${nas.id}: reachable, so rotating is a manual procedure rather than an ` +
            "impossible one. The control must be usable.",
        ).toBeEnabled();
      }
    }
  });

  /**
   * Two things the test above cannot say on its own.
   *
   * That `data-manual` discriminates — a screen that hardcoded `true` would satisfy
   * every assertion above — and that the standing note is the *same* consequence the
   * confirmation states, rather than a softer paraphrase left in front of a harsher
   * dialog. Neither assertion names a string; the second compares the page's own two
   * renderings of it to each other.
   */
  test("the standing consequence discriminates, and matches what the confirmation says", async ({
    page,
    context,
  }) => {
    const all = await listNas(context.request);
    const canPush = all.filter((n) => n.capabilities.rotateSecret === true);
    const manual = driversThatCannotPushASecret(all).filter((n) => !rotationRefused(n));

    expect(canPush.map((n) => n.id), "No fixture driver can push a secret.").not.toHaveLength(0);
    expect(
      manual.map((n) => n.id),
      "No fixture driver is reachable *and* unable to push — the case where rotation " +
        "is manual but possible, which is the whole point of the exemption.",
    ).not.toHaveLength(0);

    /* A driver that can push states a different consequence, so the attribute is
       carrying information rather than being decoration. */
    await page.goto(`/access-points/${canPush[0]!.id}`);
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();
    await expect(page.locator(rotateSection.note)).toHaveAttribute("data-manual", "false");

    await page.goto(`/access-points/${manual[0]!.id}`);
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();
    const standing = normalise(await page.locator(rotateSection.manualNote).innerText());
    expect(standing.length, "the standing consequence is empty").toBeGreaterThan(20);

    await page.locator(rotateSection.card).locator("button").click();
    await expect(page.locator(rotateSection.openDialog)).toHaveCount(1);

    expect(
      normalise(await page.locator(rotateSection.dialogBody).innerText()),
      "The confirmation states a different consequence from the one standing beside " +
        "the button. They were made the same sentence deliberately — the standing copy " +
        "is the dialog's, lifted to where it is read before the operator commits.",
    ).toContain(standing);
  });

  /**
   * The refusal, driven through the console rather than asserted from the document.
   *
   * `rotateNasSecret` answers 409 only where the driver cannot push **and** the
   * device is unreachable — the one combination with no route back, since the new
   * value could then be entered only by hand on a device nothing can reach. Neither
   * condition refuses alone; that is what keeps the exemption above coherent.
   *
   * It is a branch that is easy never to execute. The mock validates responses against
   * the schema at runtime, so a `Problem` body no request produces is a body no schema
   * has ever seen — and this branch is reached exactly when an operator is already having a
   * bad day. `web/mock/src/server.test.ts` drives it at the unit level; this asserts
   * the console can reach it and does not lie about the outcome.
   */
  test("rotating where there is no route back is refused, and no secret is shown", async ({
    page,
    context,
  }) => {
    const refused = (await listNas(context.request)).filter(rotationRefused);

    expect(
      refused.map((n) => n.id),
      "No fixture NAS has `rotateSecret: false` *and* an unreachable state, so the " +
        "409 in `rotateNasSecret` is unreachable end to end. Adding one is a change " +
        "to `web/fixtures`.",
    ).not.toHaveLength(0);

    const nas = refused[0]!;
    await page.goto(`/access-points/${nas.id}`);
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();

    const answer = page.waitForResponse(
      (r) => r.url().includes(`/nas/${nas.id}/secret`) && r.request().method() === "POST",
    );

    await page.locator(rotateSection.card).locator("button").click();
    await expect(page.locator(rotateSection.openDialog)).toHaveCount(1);
    await page.locator(rotateSection.dialogConfirm).click();

    const res = await answer;
    expect(
      res.status(),
      `Rotating ${nas.id} was not refused. Its driver cannot push a secret and nothing ` +
        "can reach it, so a new value could only be entered by hand on a device nobody " +
        "can talk to — see the `rotateNasSecret` description in openapi.yaml.",
    ).toBe(409);
    /* RFC 9457's media type, which is what the contract declares for every 4xx. */
    expect(res.headers()["content-type"]).toContain("application/problem+json");

    const body = (await res.json()) as { title?: unknown; status?: unknown };
    expect(typeof body.title, "the refusal carries no problem document").toBe("string");
    expect(body.status).toBe(409);

    /* And the console does not put a secret on screen for a rotation that did not
       happen. A shown-once value that was never applied is worse than no value. */
    await expect(page.locator(rotateSection.revealedSecret)).toHaveCount(0);
  });

  /**
   * The rotation reveals the value, and the reveal is the same one registration has.
   *
   * **Without the reveal, every other suite in the repository stays green.**
   *
   * The scenario is a real product one: a UniFi controller registered through the
   * console, discovery adopts a `U6 Lite`, it becomes an enabled RADIUS client — and
   * without the reveal it can never authenticate. Foyer generates the shared secret,
   * the API is write-only, and a console that does not display what the server has
   * just generated leaves nobody able to enter it. Rotating warns that the device
   * will stop authenticating until somebody enters the new value, and then must
   * show what the new value is.
   *
   * Why nothing else catches it:
   *
   *   - The mutation receives `sharedSecret` and puts it in state, and an unused
   *     `useState` is not a type error.
   *   - `revealedSecret` above is otherwise only asserted **absent** — in the refusal
   *     test, which is the one case where nothing is shown.
   *   - The API is right, so every Go test is right; the console is where the
   *     defect would be.
   *
   * So this asserts the flow as a person performs it: press the control, confirm,
   * and the value is on screen with a copy control and an acknowledgement that has
   * to be ticked before the dialog will let go.
   */
  test("rotating a discovered device shows the new secret once, and asks for it back", async ({
    page,
    context,
  }) => {
    /*
     * A device whose driver **cannot** push, and which is reachable so the rotation
     * is permitted. That is the case where a missing reveal is impossible rather than
     * merely awkward: the value has to be carried to the device by hand, and the
     * console is the only place it exists. A driver that can push would not expose the
     * dead end even with the reveal missing, so a test on one of those would pass
     * against a broken screen.
     *
     * **Not filtered on `source`.** A discovered UniFi is the motivating case, and
     * the manual sentence is the one that matters — but nothing in the reveal
     * branches on how the device was registered, only on whether its driver pushed
     * the value, and a test that pinned `source: "discovered"` would be asserting a
     * coincidence of the fixture set rather than the behaviour. The discovered
     * devices in the fixtures all push or are unreachable; if one ever stops doing
     * either, this picks it up without a change here.
     */
    const manual = driversThatCannotPushASecret(await listNas(context.request)).filter(
      (n) => !rotationRefused(n),
    );
    expect(
      manual.map((n) => n.id),
      "No fixture driver is reachable *and* unable to push, so a rotation that is " +
        "manual but possible — the case this defect made impossible — is not under " +
        "test. That is a change to `web/fixtures`.",
    ).not.toHaveLength(0);
    const nas = manual[0]!;

    await page.goto(`/access-points/${nas.id}`);
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();

    await page.locator(rotateSection.card).locator("button").click();
    await expect(page.locator(rotateSection.openDialog)).toHaveCount(1);
    await page.locator(rotateSection.dialogConfirm).click();

    const shown = page.locator(rotateSection.revealedSecret);
    await expect(
      shown,
      `${nas.id}: the rotation produced a secret and the console showed nothing. Foyer ` +
        "generates this value, the API is write-only, and the device is told to stop " +
        "authenticating until somebody enters it by hand — so an operator who cannot " +
        "read it here can never configure this device at all.",
    ).toBeVisible();

    const value = (await shown.innerText()).trim();
    expect(value.length, "the reveal is empty or a placeholder, not a secret").toBeGreaterThan(16);

    /* A copy control, because 40-odd characters read off a screen and typed by hand
       is how a value ends up wrong on a device. */
    await expect(page.locator("dialog[open] .secret-actions button")).toBeVisible();

    /* And the acknowledgement, which is what stops the value being scrolled past. */
    const ack = page.locator("dialog[open] .ack input[type=checkbox]");
    await expect(ack).toBeVisible();
    await expect(ack).not.toBeChecked();
    await expect(page.locator("dialog[open] .dialog-foot .btn-primary")).toBeDisabled();

    /* Escape is the other exit and it goes through the same refusal. */
    await page.keyboard.press("Escape");
    await expect(
      shown,
      "Escape dismissed the reveal without the acknowledgement. The dialog and the " +
        "primary button are meant to be the same function, and a secret this surface " +
        "cannot hold has no second chance.",
    ).toBeVisible();

    await ack.check();
    await expect(page.locator("dialog[open] .dialog-foot .btn-primary")).toBeEnabled();
    await page.locator("dialog[open] .dialog-foot .btn-primary").click();
    await expect(page.locator("dialog[open] .secret")).toHaveCount(0);

    /*
     * Once. **The value is not on the page afterwards, and no operation can put it
     * back** — the API is write-only, which is the property this whole change had to
     * keep. Reloading is the honest test of "not retrievable": it re-runs every read
     * the screen makes, and none of them answers with the value.
     */
    await page.reload();
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();
    expect(
      await page.locator("body").innerText(),
      "The rotated secret is still on the page after a reload. No operation returns a " +
        "stored secret, so nothing should have rendered it again.",
    ).not.toContain(value);
  });

  /**
   * A second rotation produces a **different** value, and shows it again.
   *
   * Without this, a console that rendered a stale value — from the first rotation,
   * out of a cache, out of a variable it never reassigned — would satisfy every
   * assertion above. Two identical reveals are indistinguishable from one reveal
   * held over; two different ones are not.
   */
  test("a second rotation shows a different value", async ({ page, context }) => {
    const manual = driversThatCannotPushASecret(await listNas(context.request)).filter(
      (n) => !rotationRefused(n),
    );
    expect(manual.map((n) => n.id), "the reachable, cannot-push fixture is gone").not.toHaveLength(0);
    const nas = manual[0]!;

    const revealed = async (): Promise<string> => {
      await page.goto(`/access-points/${nas.id}`);
      await expect(page.locator(admin.pageTitle)).not.toBeEmpty();
      await page.locator(rotateSection.card).locator("button").click();
      await expect(page.locator(rotateSection.openDialog)).toHaveCount(1);
      await page.locator(rotateSection.dialogConfirm).click();
      const shown = page.locator(rotateSection.revealedSecret);
      await expect(shown, "the rotation showed nothing").toBeVisible();
      const value = (await shown.innerText()).trim();
      await page.locator("dialog[open] .ack input[type=checkbox]").check();
      await page.locator("dialog[open] .dialog-foot .btn-primary").click();
      await expect(page.locator("dialog[open] .secret")).toHaveCount(0);
      return value;
    };

    const first = await revealed();
    const second = await revealed();

    expect(second.length, "the second reveal is empty").toBeGreaterThan(16);
    expect(
      second,
      "Two rotations produced the same value. A rotation that re-shows what it " +
        "already had is a console rendering a stale secret — the device would never " +
        "authenticate and nothing on the page would say why.",
    ).not.toBe(first);
  });

  /**
   * The three-way question, stated **before** the press.
   *
   * The server asks two things — can the driver push the value, and has the device
   * ever answered a probe — and refuses only where both are no. A console that asked
   * the first alone would, on a discovered `U6 Lite` that had never been probed,
   * promise *"the device stops authenticating until someone enters the new secret"*
   * and then have the press refused with a `409`. A warning that predicts a path the
   * server will not take teaches the reader that the screen's words are decoration.
   *
   * Asserted as the *table*, because asserting the broken row alone is satisfied by
   * the derivation that shipped. The push row is the other half: a rule that
   * refused on `credentials-expired` regardless of the capability would take a
   * working rotation away from a device whose controller key had merely expired.
   */
  test("the standing note answers both halves of the question the server asks", async ({
    page,
    context,
  }) => {
    const all = await listNas(context.request);
    const refused = all.filter(rotationRefused);
    const manual = driversThatCannotPushASecret(all).filter((n) => !rotationRefused(n));
    const canPush = all.filter((n) => n.capabilities.rotateSecret);

    expect(
      refused.map((n) => n.id),
      "No fixture reaches the 409, so the refused state is not under test.",
    ).not.toHaveLength(0);
    expect(manual.map((n) => n.id), "no manual fixture").not.toHaveLength(0);
    expect(canPush.map((n) => n.id), "no push fixture").not.toHaveLength(0);

    for (const nas of refused) {
      await page.goto(`/access-points/${nas.id}`);
      await expect(page.locator(admin.pageTitle)).not.toBeEmpty();

      /*
       * The refused state is a **notice**, not the standing `p.secret-rotate-note`,
       * and that difference is the assertion: rendering the manual note here would
       * describe a rotation that cannot happen.
       */
      const notice = page.locator(".card .notice");
      await expect(
        notice,
        `${nas.id}: no standing refusal. Its driver cannot push a secret and the device ` +
          "has never answered a probe, so the server answers 409 — the operator has to " +
          "be told that before the press, not after it.",
      ).toBeVisible();
      await expect(
        page.locator(rotateSection.manualNote),
        `${nas.id}: the page still shows the *manual* warning, which promises a rotation ` +
          "the server is about to refuse.",
      ).toHaveCount(0);
    }

    /* Manual and push keep their own notes, and they differ — a screen that hardcoded
       one of them would satisfy the refused assertions above. */
    await page.goto(`/access-points/${manual[0]!.id}`);
    await expect(page.locator(rotateSection.manualNote)).toBeVisible();
    await expect(page.locator(".card .notice")).toHaveCount(0);

    await page.goto(`/access-points/${canPush[0]!.id}`);
    await expect(page.locator(rotateSection.note)).toHaveAttribute("data-manual", "false");
    await expect(page.locator(".card .notice")).toHaveCount(0);
  });

  test("a driver with no walled garden shows a standing warning", async ({ page, context }) => {
    const nas = await getNas(context.request, "nas-omada-1");
    expect(nas.capabilities.walledGarden ?? "none").toBe("none");

    await page.goto("/access-points/nas-omada-1");
    await expect(page.locator(`${admin.notice}[data-level="warning"]`).first()).toBeVisible();
  });

  /**
   * The fixture that arms the one gate nothing is holding.
   *
   * Everything above is about the access-point detail screen, where `<IfCapable>` is
   * applied and the assertions are comparative. **The session detail dock is the one
   * surface that states a capability-dependent figure with no capability in the
   * query at all**: it renders `plan.downKbps` because the plan has one, and it is
   * handed a `Session` and nothing else, so it could not check `bandwidthLimits`
   * even if it wanted to. Verified on screen against `sess-unenforceable-rate`,
   * which reads "5 Mbps down · 2 Mbps up" with no qualifier on a device whose driver
   * reports `bandwidthLimits: false`.
   *
   * **This test does not assert that.** Asserting the current rendering would pin
   * the defect; asserting the corrected one would be red, and a suite that is red on
   * purpose is a suite people learn to ignore. What it does assert is that the
   * *case still exists and is still reachable through the console* — because without
   * `sess-unenforceable-rate` there is no such session anywhere in the fixtures, and a
   * screen with no capability check looks correct on every row it is ever shown. A
   * fixture that quietly disappears takes the evidence with it, which is why
   * `docs/rules/frontend.md` requires the fixtures to be ugly on purpose.
   *
   * The pairing is computed, never named: any session on any driver that cannot
   * apply a rate satisfies it, so the guard survives the fixtures being rearranged
   * and fails only when the case itself is gone.
   */
  test("a session states a rate limit its access point cannot apply, and the dock opens on it", async ({
    page,
    context,
  }) => {
    const [sessions, nas] = await Promise.all([
      listSessions(context.request),
      listNas(context.request),
    ]);
    const armed = ratesNoDriverCanApply(sessions, nas);

    expect(
      armed.length,
      "No session anywhere in the fixtures states a rate limit on a driver whose " +
        "`bandwidthLimits` is false. Nothing can catch a rate rendered with no " +
        "capability check, because every session it is ever shown against belongs " +
        "to a device that could have applied it.",
    ).toBeGreaterThan(0);

    /* Reachable through the console, not merely present in a JSON payload. A trap
       nobody can open is a trap nobody will fall into, and nobody will fix. */
    const target = armed[0]!;
    await page.goto(`/sessions?session=${encodeURIComponent(target.id)}`);
    await tableReady(page);
    await expect(page.locator(admin.detailDock)).toBeVisible();

    const field = page.locator(`${admin.detailDock} .detail-field[data-field="rate-limit"]`);
    await expect(field).toBeVisible();

    /*
     * The qualifier, asserted semantically rather than by copy — the catalogue is
     * still being edited and a test that fails on a rewording is a test that gets
     * deleted.
     */
    await expect(field).toHaveAttribute("data-applied", "unapplied");
    await expect(field).toHaveAttribute("data-emphasis", "true");

    /*
     * **And the number is still on the page.** This is the assertion that guards
     * against a future "fix": hiding a rate the driver cannot apply looks like
     * capability gating and is the wrong answer, because the figure is *what the
     * plan grants* and not *a control the operator filled in*. What the guest was
     * granted is a record; removing it makes the discrepancy unprovable from the
     * console. It needs to stay wrong.
     */
    /*
     * A figure, not *the* figure. The dock renders kbps as Mbps, and reproducing
     * that conversion here would duplicate the formatter — a test that fails when
     * the unit changes is testing the copy, which this suite does not do. What must
     * hold is that a number is still on screen: a "fix" that hid the rate would
     * render the no-limit state or nothing at all, and neither has a digit in it.
     */
    expect(
      /\d/.test(await field.innerText()),
      `The dock qualified the rate but no longer shows a figure. The number is what ` +
        `the plan granted; hiding it is not gating, it is deleting the evidence — ` +
        `"the plan promises 5 Mbps and this device applies nothing" needs both halves.`,
    ).toBe(true);
  });

  /**
   * The other half, and the reason the half above is not vacuous.
   *
   * A console that stamped `data-applied="unapplied"` on every rate it rendered
   * would satisfy every assertion in the previous test while being exactly as wrong
   * as the bug it guards — a screen that always says "nothing is enforcing this" is
   * as useless as one that never does. So the qualifier has to *discriminate*, and
   * the only way to show that is a session whose driver can apply what it states.
   */
  test("a session whose access point can apply its rate is not qualified the same way", async ({
    page,
    context,
  }) => {
    const [sessions, nas] = await Promise.all([
      listSessions(context.request),
      listNas(context.request),
    ]);
    const applied = ratesEveryDriverCanApply(sessions, nas);

    expect(
      applied.length,
      "No session in the fixtures states a rate on a driver that *can* apply one, so " +
        "a console that hardcoded the unapplied qualifier would pass the test above.",
    ).toBeGreaterThan(0);

    const target = applied[0]!;
    await page.goto(`/sessions?session=${encodeURIComponent(target.id)}`);
    await tableReady(page);

    const field = page.locator(`${admin.detailDock} .detail-field[data-field="rate-limit"]`);
    await expect(field).toBeVisible();
    await expect(
      field,
      "The dock reports the same enforcement for a driver that can apply a rate as " +
        "for one that cannot, so the qualifier is decoration.",
    ).not.toHaveAttribute("data-applied", "unapplied");
    await expect(field).not.toHaveAttribute("data-emphasis", "true");
  });
});
