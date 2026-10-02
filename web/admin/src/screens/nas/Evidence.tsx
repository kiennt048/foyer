import type { DriverEvidence, EvidenceFact, EvidenceStanding } from "@foyer/contract";
import { t } from "../../i18n/nas.js";
import { Pill } from "../../ui/primitives.js";
import type { StatusLevel } from "../../format.js";

/**
 * **How this device's claims are known.** The UNVERIFIED label on the CoA family
 * has to reach an operator rather than living in the Go source.
 *
 * ── two lists, because they disagree, and the disagreement is the fact ────────
 *
 * The easy summary is *every wire encoding is DOCUMENTED, every capability is
 * UNVERIFIED*. **The first half is not true and the narrower claim is better**:
 *
 *   * the STANDARD attributes are `documented` — `Session-Timeout`, `Class` and
 *     `Acct-Interim-Interval` are defined by RFC, and a screen that marked those
 *     unverified would be telling an operator that RADIUS itself is a guess;
 *   * the VENDOR redirect attributes are `unverified` — `Cisco-AVPair`'s command
 *     strings are a reading of a vendor table that no measured device has
 *     confirmed;
 *   * every CAPABILITY of the CoA family is `unverified`, without exception.
 *
 * So a device gets three registers on one screen rather than one badge, and the
 * sentence an operator can take away is **we know what we will send, we half know
 * how it is spelled, and we do not know what it will do**.
 *
 * ── the levels, which are the same argument `findings.ts` already makes ──────
 *
 * `unverified` is `neutral`, never `positive` and never `warning`. The value is
 * not known to be wrong; it is known not to be known, and a capability decides
 * whether a control exists at all (`docs/rules/nas-drivers.md`).
 *
 * `documented` is `info` rather than `positive` for the reason
 * `docs/protocol-reality.md` keeps three words instead of two: a citation says
 * what a vendor wrote down, and only a measurement says what a device did. A green
 * tick on a citation is the same mistake as *a green tick over unmeasured claims*.
 */

const LEVEL: Record<EvidenceStanding, StatusLevel> = {
  measured: "positive",
  documented: "info",
  unverified: "neutral",
};

const LABEL: Record<EvidenceStanding, string> = {
  get measured() {
    return t("nas.evidence.standing.measured");
  },
  get documented() {
    return t("nas.evidence.standing.documented");
  },
  get unverified() {
    return t("nas.evidence.standing.unverified");
  },
};

function FactRow({ fact }: { fact: EvidenceFact }) {
  return (
    <li className="evidence-fact">
      <span className="evidence-subject">
        {/* Verbatim, and monospaced. These are the names in the vendor's own
            documentation and in FreeRADIUS's dictionaries — an operator searching
            for `Mikrotik-Rate-Limit` wants the string they will find there, not a
            prettified one. */}
        <code>{fact.subject}</code>
      </span>
      <Pill level={LEVEL[fact.evidence]} label={LABEL[fact.evidence]} />
      {/* The note is rendered, not a tooltip. For an unverified fact it names THE
          MEASUREMENT THAT WOULD SETTLE IT, which is the only thing on this screen
          an operator can act on, and a `title` does not exist on a touch device.
          It was being dropped entirely: `NasProbe.unverified` carries names and the
          console rendered a generic "believed, not measured" under each. */}
      <span className="evidence-note">{fact.note}</span>
    </li>
  );
}

export function EvidencePanel({ evidence }: { evidence: DriverEvidence }) {
  if (evidence.profile === "vendor-not-chosen") {
    /* Not an empty list. Cisco and Aruba identify a session differently and a
       wrong guess is an ACK with no effect — the one failure a fake NAS cannot
       produce — so "nobody said which vendor" is a state with a sentence, and
       empty lists with nothing beside them would read as *this driver sends
       nothing*. */
    return (
      <p className="evidence-empty">{t("nas.evidence.vendorNotChosen")}</p>
    );
  }

  return (
    <>
      <p className="evidence-lead">{t("nas.evidence.lead")}</p>

      <h3 className="evidence-heading">{t("nas.evidence.capabilities")}</h3>
      <p className="evidence-sub">{t("nas.evidence.capabilitiesWhy")}</p>
      <ul className="evidence-list">
        {evidence.capabilities.map((f) => (
          <FactRow key={`cap-${f.subject}`} fact={f} />
        ))}
      </ul>

      <h3 className="evidence-heading">{t("nas.evidence.attributes")}</h3>
      <p className="evidence-sub">{t("nas.evidence.attributesWhy")}</p>
      <ul className="evidence-list">
        {evidence.attributes.map((f) => (
          <FactRow key={`att-${f.subject}`} fact={f} />
        ))}
      </ul>
    </>
  );
}
