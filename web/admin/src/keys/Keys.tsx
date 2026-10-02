import { Fragment } from "react";
import { chordCaps, type BindingSpec, type Sequence } from "./bindings.js";
import { t } from "../i18n/index.js";

/**
 * A shortcut, drawn.
 *
 * Every cap comes out of the same `Chord` the matcher compares against, so a hint
 * printed under a table cannot disagree with the key that actually works. Sequences
 * read `G then D`; alternatives read `J or ↓`, because they are one binding with two
 * spellings rather than two bindings.
 */
export function Keys({ sequences }: { sequences: readonly Sequence[] }) {
  return (
    <span className="keys">
      {sequences.map((sequence, s) => (
        <Fragment key={s}>
          {s > 0 ? <span className="keys-join">{t("keys.cap.or")}</span> : null}
          {sequence.map((chord, c) => (
            <Fragment key={c}>
              {c > 0 ? <span className="keys-join">{t("keys.cap.then")}</span> : null}
              {chordCaps(chord).map((cap) => (
                <kbd key={cap}>{cap}</kbd>
              ))}
            </Fragment>
          ))}
        </Fragment>
      ))}
    </span>
  );
}

/** The same thing, for a binding — the common case, and one import instead of two. */
export function BindingKeys({ spec }: { spec: BindingSpec }) {
  return <Keys sequences={spec.sequences} />;
}

/**
 * An inline hint: what the key does, and the key.
 *
 * Used in the strip under a table and inside the detail panel. It is the mechanism
 * behind the rule that every shortcut must be discoverable from visible UI — a
 * shortcut with no `Hint` anywhere on screen is one nobody will find.
 */
export function Hint({ spec, label }: { spec: BindingSpec; label?: string }) {
  return (
    <span className="hint">
      <BindingKeys spec={spec} />
      <span>{label ?? spec.label}</span>
    </span>
  );
}
