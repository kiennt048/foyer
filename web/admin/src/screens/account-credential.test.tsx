import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { components } from "@foyer/contract";
import { buildWorld } from "@foyer/fixtures";
import { t } from "../i18n/index.js";
import { credentialCell } from "./Accounts.js";

type Kind = components["schemas"]["Account"]["credentialKind"];

/**
 * **The credential column has to be believed, so it is the one that gets a lock.**
 *
 * `credentialKind` names every verifier an identity has. It had two members —
 * `argon2id` and `argon2id+nt` — and the cell was an equality test with an `else`.
 * Adding a second opt-in (802.1X) broke it: that `else` then
 * labelled an identity opted in to **802.1X** as *"argon2id + NT hash"*: it
 * asserted password-equivalent material where none exists, on the screen whose
 * stated job is making the NT case unmissable.
 *
 * Two things this file is careful about, both from `docs/rules/testing.md`:
 *
 *   * **The kinds are derived from the contract type, not typed here.** `EVERY_KIND`
 *     is checked against the schema union at compile time, so a fifth member added
 *     to the enum without a case in this file is a type error rather than a silent
 *     gap. A list typed into a test is a second source of truth for an enum.
 *   * **Absence is asserted as hard as presence.** The defect this exists for was a
 *     label that was *present and wrong*; a test that only looked for the bcrypt
 *     label would have been green against it.
 */

/*
 * Exhaustive by construction: `Record<Kind, true>` will not compile if a member is
 * missing, and a member that is not in `Kind` will not compile either.
 */
const EVERY_KIND: Record<Kind, true> = {
  argon2id: true,
  "argon2id+nt": true,
  "argon2id+bcrypt": true,
  "argon2id+nt+bcrypt": true,
};

const html = (kind: Kind) => renderToStaticMarkup(credentialCell(kind));

describe("the credential column names every verifier and invents none", () => {
  it("plain argon2id is quiet text and carries no pill", () => {
    const out = html("argon2id");
    expect(out).toContain(t("accounts.credential.argon2id"));
    expect(out).not.toContain(t("accounts.credential.nt"));
    expect(out).not.toContain(t("accounts.credential.bcrypt"));
    expect(out).not.toContain("pill");
  });

  it("an MSCHAPv2 identity says NT hash and does not claim 802.1X", () => {
    const out = html("argon2id+nt");
    expect(out).toContain(t("accounts.credential.nt"));
    expect(out).not.toContain(t("accounts.credential.bcrypt"));
    expect(out).toContain('data-level="warning"');
  });

  /*
   * The case that matters most. If `argon2id+bcrypt` fell into the `else` it would be
   * rendered with the NT label and an amber level — a screen telling an operator
   * that a stolen value from this row would authenticate directly, which is false
   * for a bcrypt verifier and is the difference that makes the 802.1X opt-in
   * acceptable at all.
   */
  it("an 802.1X identity says bcrypt and NEVER says NT hash", () => {
    const out = html("argon2id+bcrypt");
    expect(out).toContain(t("accounts.credential.bcrypt"));
    expect(out).not.toContain(t("accounts.credential.nt"));
    expect(out).not.toContain('data-level="warning"');
    expect(out).toContain('data-level="neutral"');
  });

  it("both opt-ins render both pills rather than one standing in for the other", () => {
    const out = html("argon2id+nt+bcrypt");
    expect(out).toContain(t("accounts.credential.nt"));
    expect(out).toContain(t("accounts.credential.bcrypt"));
    expect(out).toContain('data-level="warning"');
    expect(out).toContain('data-level="neutral"');
  });

  /*
   * The scheme is named, and naming it is the price that has to be written where it
   * cannot be missed: an identity opted in to 802.1X authenticates against
   * a bcrypt verifier, not argon2id. Measured — `rlm_pap` in FreeRADIUS 3.2.10 has
   * no argon2 scheme, and libxcrypt answers its failure token for `$argon2id$`.
   *
   * This asserts the copy does not call it argon2, which is a thing a well-meaning
   * edit does: "argon2id + 802.1X" reads better and is a lie about the data plane.
   */
  it("the 802.1X copy names bcrypt and does not describe it as argon2", () => {
    const label = t("accounts.credential.bcrypt");
    const why = t("accounts.credential.bcryptWhy");
    expect(label.toLowerCase()).toContain("bcrypt");
    expect(label.toLowerCase()).not.toContain("argon2");
    expect(why.toLowerCase()).toContain("bcrypt");
    // The explanation MAY mention argon2 — it has to, to say what it is not — but
    // never as the thing 802.1X verifies against.
    expect(why).toContain("not argon2id");
  });

  /*
   * A world that cannot express a property makes every check of it vacuous.
   *
   * The four cases above would pass against a fixture set holding only `argon2id`
   * and `argon2id+nt`, because they build their own input. This is the assertion
   * that the SHIPPED world can still produce the rows — the E2E suite and the
   * screenshots read fixtures, not this file.
   */
  it("the fixture world holds an identity for each kind the enum can express", () => {
    const present = new Set(buildWorld().accounts.map((a) => a.credentialKind));
    for (const kind of Object.keys(EVERY_KIND) as Kind[]) {
      expect(present, `no fixture account is ${kind}`).toContain(kind);
    }
  });
});
