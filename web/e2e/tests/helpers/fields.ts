import type { Page } from "@playwright/test";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  What it means for a refused field to be *actionable*
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Two rules live here, one rung apart, and they are here rather than in a spec file
 * because two specs need them and a second copy of a rule is a copy that drifts.
 *
 *   - `fieldPairFaults` — **the mark and the message are joined.** `aria-invalid`
 *     alone was already green on `/garden` and `/portal` before the field-error
 *     work: it says *this is wrong* and points at nothing. What has to hold is the
 *     pair, every id in `aria-describedby` resolving to a node really in the
 *     document, and one of them carrying the sentence the console chose.
 *
 *   - `unreachableRefusal` — **the control the sentence is about is on screen.** A
 *     pair can be perfect and still useless if the form has no control for the field
 *     the server named. That defect happened: `createNas` began refusing
 *     `coa` registrations that name no `coaVendor`, and the registration wizard had
 *     no such control — so an operator read *"Correct the fields marked below"* with
 *     nothing marked and nothing to correct.
 *
 * The second is the reason this file exists. It is deliberately phrased over the
 * **sentence** rather than over an element id: a map from a wire field name to a DOM
 * id would be a third copy of a rule the console already states, and the sentence is
 * what the operator is actually looking for. Its faults name *the refusal that could
 * not be acted on*, never *an element that was not found* — a spec that reports the
 * latter tells you a selector is stale, which is the one thing that is not wrong.
 */

export interface FieldPair {
  invalid: string | null;
  ids: string[];
  /** `null` for an id that resolves to nothing — the half `aria-invalid` cannot say. */
  resolved: (string | null)[];
}

export async function fieldPair(page: Page, selector: string): Promise<FieldPair> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) return { invalid: null, ids: [], resolved: [] };
    const ids = (el.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean);
    return {
      invalid: el.getAttribute("aria-invalid"),
      ids,
      resolved: ids.map((id) => {
        const node = document.getElementById(id);
        return node ? (node.textContent ?? "").trim() : null;
      }),
    };
  }, selector);
}

/**
 * The pair rule, stated once, so both a real field and a re-injected defect are
 * judged by the same predicate.
 */
export function fieldPairFaults(pair: FieldPair, message: string): string[] {
  const faults: string[] = [];
  if (pair.invalid !== "true") faults.push(`aria-invalid is ${String(pair.invalid)}, not "true"`);
  if (pair.ids.length === 0) {
    faults.push("aria-describedby is absent, so the mark points at nothing");
  }
  pair.ids.forEach((id, i) => {
    if (pair.resolved[i] === null) {
      faults.push(`aria-describedby names ${id}, which is not in the document`);
    }
  });
  if (!pair.resolved.some((text) => text === message)) {
    faults.push(
      `no described node says ${JSON.stringify(message)}; they say ${JSON.stringify(pair.resolved)}`,
    );
  }
  return faults;
}

/**
 * Why the operator could not act on this refusal — or `[]` if they could.
 *
 * `field` is the name **the server used**, read off the wire by the caller rather
 * than typed in, and `sentence` is the **console's** copy for it. Both halves matter:
 * the first says which refusal is being judged, the second is what the operator hunts
 * for on screen.
 *
 * Searched inside `root` (an open dialog, usually) for a control that is marked
 * invalid, is really rendered, and whose `aria-describedby` reaches the sentence.
 * Each failure is phrased as a fact about the *refusal*, because that is what the
 * reader has to act on:
 *
 *   - the sentence is nowhere → the server named a field the form does not have;
 *   - the sentence is on screen but no control points at it → the operator is told to
 *     correct something and not told what;
 *   - a control points at it but is not rendered → the same defect one layer down,
 *     which is what removing the field from the page produces.
 */
export async function unreachableRefusal(
  page: Page,
  root: string,
  field: string,
  sentence: string,
): Promise<string[]> {
  const seen = await page.evaluate(
    ([sel, text]) => {
      const scope = document.querySelector(sel!);
      if (!scope) return { scope: false, anywhere: false, marks: [] as { described: boolean; rendered: boolean }[] };
      const rendered = (el: Element) => el.getClientRects().length > 0;
      const marks = [...scope.querySelectorAll("[aria-invalid='true']")].map((el) => ({
        described: (el.getAttribute("aria-describedby") ?? "")
          .split(/\s+/)
          .filter(Boolean)
          .some((id) => ((document.getElementById(id)?.textContent ?? "").trim() === text)),
        rendered: rendered(el),
      }));
      return {
        scope: true,
        anywhere: (scope.textContent ?? "").includes(text!),
        marks,
      };
    },
    [root, sentence] as const,
  );

  if (!seen.scope) return [`${root} is not on screen at all, so no refusal could be read from it`];

  const pointing = seen.marks.filter((m) => m.described);
  if (pointing.length === 0) {
    return [
      seen.anywhere
        ? `the server refused \`${field}\` and its sentence is on screen with no control ` +
          `pointing at it — the operator is told to correct something and not told which control`
        : `the server refused \`${field}\` and no control in ${root} carries its sentence ` +
          `${JSON.stringify(sentence)} — the operator was told to correct a field that is not on screen`,
    ];
  }
  if (!pointing.some((m) => m.rendered)) {
    return [
      `the server refused \`${field}\` and the only control carrying its sentence is not ` +
        `rendered, so the refusal names a field the operator cannot see`,
    ];
  }
  return [];
}
