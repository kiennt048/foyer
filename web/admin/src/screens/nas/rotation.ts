import type { Nas } from "@foyer/contract";

/**
 * What a rotation of *this* device's shared secret will actually do — derived once,
 * so the page, the confirmation and the refusal cannot disagree.
 *
 * The rule is `rotateNasSecret`'s, and it is a three-way question that the console
 * was answering with two. The server refuses only where the driver **cannot push**
 * the value *and* the device has **never answered a probe** — the one combination
 * with no route back, because the new value could then be entered only by hand, by
 * hand, on a device nobody can reach. Neither condition refuses on its own:
 *
 *   | can push | answers a probe | outcome                                   |
 *   |----------|------------------|------------------------------------------|
 *   | yes      | any              | `push` — Foyer writes it, nobody types   |
 *   | no       | yes              | `manual` — typed on the device by a person |
 *   | no       | no               | `refused` — the press returns 409         |
 *
 * **The `refused` row is why this exists.** Without it the page would say *"the
 * device stops authenticating until someone enters the new secret on it"* beside the
 * button — a warning describing a rotation that the server is about to refuse, on a
 * device that has never answered. A warning which predicts a path the server will not take is
 * worse than no warning, because it teaches the reader that this screen's words are
 * decoration. On a real UniFi controller that is a discovered `U6 Lite` whose driver
 * cannot push, never probed: pressing the button produces a `409`.
 *
 * Nothing here decides anything. The server is the authority and reachability
 * changes under the operator's feet, so the button stays — this is what tells the
 * operator which of the three they are about to get, *before* they press.
 */
export type RotationOffer =
  /** The driver writes the value onto the device. Nobody types anything. */
  | { kind: "push" }
  /** The value has to be typed on the device by a person. Possible, if someone can reach it. */
  | { kind: "manual" }
  /** The server answers `409`. Stated before the press, not discovered after it. */
  | { kind: "refused" };

export function rotationOffer(
  nas: Pick<Nas, "capabilities" | "reachability">,
): RotationOffer {
  if (nas.capabilities.rotateSecret) return { kind: "push" };
  /*
   * `reachability !== "ok"` is the server's test verbatim, and it is the *whole* of
   * it: `credentials-expired` and `unreachable` refuse alongside `unknown`, because
   * "Foyer cannot talk to the controller" and "nothing has answered" both mean the
   * value cannot be carried there by a person standing in front of it today.
   */
  return nas.reachability === "ok" ? { kind: "manual" } : { kind: "refused" };
}

/** True when the press will be refused. Kept as a named question, used by two callers. */
export const rotationRefused = (
  nas: Pick<Nas, "capabilities" | "reachability">,
): boolean => rotationOffer(nas).kind === "refused";
