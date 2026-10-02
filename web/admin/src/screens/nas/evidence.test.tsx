import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { DriverEvidence } from "@foyer/contract";
import { VENDOR_BY_DRIVER, VENDOR_EVIDENCE } from "@foyer/fixtures";
import { t } from "../../i18n/nas.js";
import { EvidencePanel } from "./Evidence.js";

/**
 * **How a device's claims are known, reaching a screen.**
 *
 * The CoA family ships with every capability UNVERIFIED and the label has to be
 * visible in the console, not only in the code. This file is what says it arrived.
 *
 * Two things it is careful about, and the second is the whole point:
 *
 *   * **The inputs come from the generated tables**, not from objects typed here.
 *     `web/fixtures/src/generated/vendor-evidence.ts` is produced from
 *     `internal/nas` by `tools/codegen/vendorevidence`, so these cases are driven
 *     by what the shipped drivers actually claim. A hand-written fixture would be
 *     an input that cannot be wrong — a fake asserting a Cisco encoding is
 *     documented, in a suite whose premise is that nothing here has met a Cisco
 *     device.
 *   * **Absence is asserted as hard as presence.** The failure this guards is a
 *     screen that flattens two disagreeing lists into one badge, and a test that
 *     only looked for the word *Unverified* would be green against exactly that.
 */

const html = (evidence: DriverEvidence) => renderToStaticMarkup(<EvidencePanel evidence={evidence} />);

/** The evidence a real device of this make would carry, read from the generated table. */
function evidenceFor(driverKey: string): DriverEvidence {
  const vendor = VENDOR_BY_DRIVER[driverKey];
  expect(vendor, `no vendor for ${driverKey} — the generated table has changed shape`).toBeTruthy();
  const entry = VENDOR_EVIDENCE[vendor!];
  expect(entry, `no evidence for ${vendor}`).toBeTruthy();
  return { profile: "resolved", capabilities: entry!.capabilities, attributes: entry!.attributes };
}

describe("how a device's claims are known", () => {
  /*
   * Non-vacuity first. Every case below reads the generated tables, and a table
   * that had lost its rows would make all of them pass by rendering nothing.
   */
  it("the generated tables carry the CoA family at all", () => {
    for (const key of ["coa:cisco", "coa:aruba", "mikrotik"]) {
      const e = evidenceFor(key);
      expect(e.capabilities.length, `${key} has no capabilities`).toBeGreaterThan(0);
      expect(e.attributes.length, `${key} has no attributes`).toBeGreaterThan(0);
    }
  });

  it("every capability of a CoA driver is rendered Unverified", () => {
    const e = evidenceFor("coa:cisco");
    // The claim first, against the data: if a capability were ever promoted
    // without a measurement, this is where it stops being a rendering test.
    for (const c of e.capabilities) {
      expect(c.evidence, `capability ${c.subject} is ${c.evidence}`).toBe("unverified");
    }
    const out = html(e);
    expect(out).toContain(t("nas.evidence.standing.unverified"));
    // `positive` is the level a green tick is drawn at, and a green tick over
    // unmeasured claims is the defect. No capability on this panel may reach it.
    expect(out).not.toContain('data-level="positive"');
  });

  /*
   * The correction. It is tempting to say *every wire encoding is DOCUMENTED*; it is not,
   * and the narrower claim is the better one — the standard attributes are, the
   * vendor redirect attributes are not. Both must be on screen, or the panel is
   * telling an operator that `Cisco-AVPair`'s command strings are as well
   * established as `Session-Timeout`.
   */
  it("the attribute list carries BOTH standings, because they disagree", () => {
    const e = evidenceFor("coa:cisco");
    const standings = new Set(e.attributes.map((a) => a.evidence));
    expect(standings.has("documented"), "no attribute is documented").toBe(true);
    expect(standings.has("unverified"), "every attribute is documented, including the redirect").toBe(
      true,
    );

    const out = html(e);
    expect(out).toContain(t("nas.evidence.standing.documented"));
    expect(out).toContain(t("nas.evidence.standing.unverified"));
  });

  /*
   * The note is what an operator can act on — for an unverified fact it names the
   * measurement that would settle it — and it is easy to drop entirely:
   * `NasProbe.unverified` carries names, and without the note the console renders
   * a generic "believed, not measured" under each.
   *
   * ── why the rendered HTML is not enough ─────────────────────────────────────
   *
   * Asserting that the note appears in the rendered HTML is not enough. Replacing
   * `{fact.note}` with `title={fact.note}` leaves that **passing**: a `title` puts the
   * same characters in the same document, so `toContain` cannot tell a sentence
   * from a tooltip. That is the species `docs/rules/testing.md` records — an
   * assertion aimed where it cannot distinguish the thing it is about — and the
   * distinction matters, because `title` does not exist under `(hover: none) and
   * (pointer: coarse)`, which is the environment `touch-actions-visible` exists
   * for.
   *
   * So it reads the TEXT CONTENT of the note elements, and separately requires
   * that no `title` carries the note instead.
   */
  it("every fact renders its note as text, not as a tooltip", () => {
    const e = evidenceFor("coa:aruba");
    const out = html(e);

    const rendered = [...out.matchAll(/<span class="evidence-note">([^<]*)<\/span>/g)].map(
      (m) => m[1] ?? "",
    );
    expect(
      rendered.length,
      "no note elements have any text content at all — the notes are somewhere else in this document",
    ).toBe(e.capabilities.length + e.attributes.length);

    /* Decoded rather than re-escaped: React escapes five characters and an
       apostrophe becomes `&#x27;`, so a hand-written escaper drifts from the
       renderer and the comparison starts failing for a reason that has nothing to
       do with what is being asserted. */
    const decode = (fragment: string) =>
      fragment
        .replace(/&lt;/g, "<")
        .replace(/&gt;/g, ">")
        .replace(/&quot;/g, '"')
        .replace(/&#x27;/g, "'")
        .replace(/&#39;/g, "'")
        .replace(/&amp;/g, "&");
    const text = rendered.map(decode);

    for (const fact of [...e.capabilities, ...e.attributes]) {
      expect(fact.note, `${fact.subject} carries no note`).not.toBe("");
      expect(
        text,
        `the note for ${fact.subject} is not the text of any note element`,
      ).toContain(fact.note);
    }

    expect(
      out,
      "a note is being carried in a title attribute, which does not exist on a touch device",
    ).not.toMatch(/<span class="evidence-note"[^>]*title=/);
  });

  it("the subjects are rendered verbatim, in a monospaced element", () => {
    const e = evidenceFor("mikrotik");
    const out = html(e);
    // `Mikrotik-Rate-Limit` is the string in MikroTik's documentation and in
    // FreeRADIUS's dictionary. An operator searching for it wants that spelling.
    const rate = e.attributes.find((a) => a.subject.includes("Rate-Limit"));
    expect(rate, "the MikroTik profile no longer emits a rate attribute").toBeTruthy();
    expect(out).toContain(`<code>${rate!.subject}</code>`);
  });

  /*
   * A `coa` row with no vendor. Answered with a sentence rather than two empty
   * lists: Cisco and Aruba identify a session differently and a wrong guess is an
   * ACK with no effect — the one failure a fake NAS cannot produce — so the
   * emptiness has to say why it is empty.
   */
  it("a device with no vendor says so instead of rendering nothing", () => {
    const out = html({ profile: "vendor-not-chosen", capabilities: [], attributes: [] });
    expect(out).toContain(t("nas.evidence.vendorNotChosen"));
    expect(out).not.toContain(t("nas.evidence.capabilities"));
    expect(out).not.toContain(t("nas.evidence.attributes"));
  });
});
