import { type RefObject, useEffect, useRef } from "react";
import { errorCopy, problemCopy, type ProblemCopy, type ProblemLike } from "../problems.js";

/**
 * Running one action over a set of rows, and reporting honestly on what happened.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **A batch has three outcomes, not two**, and collapsing the third is a defect at
 * single-row scale as much as at batch scale.
 *
 * - The write happened. Nothing to say beyond a count.
 * - **The write did not happen.** The row is unchanged, so the operator can press the
 *   same control again — which is why these rows stay selected rather than being
 *   cleared with the rest.
 * - **The write happened and something else did not.** Revoking a voucher stops
 *   further redemption *and* ends the session the code is carrying; the second half is
 *   a driver call to real hardware and it fails on its own. The code is dead and the
 *   guest is still online. Retrying that is a no-op, so it is named and never
 *   retried — a separate category, not a failure and not a success.
 *
 * Vouchers.tsx records what happens when the third is folded into the first: a failed
 * revoke said only *"Revoked"*, which is a confident answer about a different
 * outcome. At ten rows that is ten wrong answers at once.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Sequential, deliberately.** Firing forty writes at once is a stampede on a
 * controller that answers one device at a time, it makes progress unreportable, and
 * it scrambles the order the failures come back in — and the whole value of a partial
 * report is that it reads down the screen in the same order as the rows it is about.
 * A batch is bounded by what is on screen, which is at most one page.
 */

/** One row's outcome, with the sentence this console wrote for it. */
export interface BatchNote {
  key: string;
  /** What the operator calls this row — a code, a username, a destination. */
  label: string;
  copy: ProblemCopy;
}

export interface BatchOutcome {
  /** How many rows were attempted. The denominator in every sentence about this run. */
  total: number;
  /** Rows whose write did not happen. Still actionable, so still selected. */
  failed: BatchNote[];
  /** Rows whose write happened and whose *consequence* did not. Never retried. */
  noted: BatchNote[];
}

/**
 * One row of work.
 *
 * `run` may report a failure two ways, because the server does: by rejecting, and by
 * answering `200` with a `Failure` in the body. Resolving to a `ProblemLike` is the
 * second — the write succeeded, so it becomes a note rather than a failure.
 */
export interface BatchItem {
  key: string;
  label: string;
  run: () => Promise<ProblemLike | null | undefined | void>;
}

export async function runBatch(
  items: readonly BatchItem[],
  onProgress: (done: number) => void,
): Promise<BatchOutcome> {
  const out: BatchOutcome = { total: items.length, failed: [], noted: [] };
  let done = 0;
  for (const item of items) {
    try {
      const reported = await item.run();
      if (reported) out.noted.push({ key: item.key, label: item.label, copy: problemCopy(reported) });
    } catch (e) {
      out.failed.push({ key: item.key, label: item.label, copy: errorCopy(e) });
    }
    onProgress(++done);
  }
  return out;
}

/**
 * The rows named, grouped by the reason they were named for.
 *
 * *Which three* is the question a partial report exists to answer, and the table
 * answers it visually — the three that failed are the three still ticked. That is not
 * an answer for somebody who is not looking at it, so the identifiers are in the
 * sentence too. Grouped rather than listed one per line because a batch usually fails
 * for one reason, and repeating that sentence twelve times buries it.
 */
export function groupedNotes(notes: readonly BatchNote[]): string {
  const by = new Map<string, string[]>();
  for (const n of notes) {
    const reason = n.copy.body || n.copy.title;
    by.set(reason, [...(by.get(reason) ?? []), n.label]);
  }
  return [...by].map(([reason, labels]) => `${labels.join(", ")} — ${reason}`).join(" ");
}

/**
 * Where focus goes when a batch finishes and the bar it was pressed from is gone.
 *
 * The same question `refocus.ts` answers for a row control, one scale up, and the
 * same rule: *the first remaining control in the group the removed one belonged to* —
 * except that a batch clears the selection, so the whole group goes with it. The next
 * honest target is the control that starts the next selection, which is the table's
 * own select-all box, and it is inside the table where the operator already is.
 *
 * Called on a timeout rather than inline: a confirmed batch closes a `<dialog>`, and
 * the platform's own return-to-invoker runs inside `close()` during the effect after
 * the commit. Focusing before that means being overwritten by it — and overwritten
 * with `<body>`, because the invoker is the button that has just gone.
 */
export function focusSelectAll(root: HTMLElement | null): void {
  after(() => root?.querySelector<HTMLInputElement>('thead input[data-role="row-select"]'));
}

/**
 * Where focus goes when the bar survives a batch but **the verb that was pressed does
 * not**.
 *
 * Only driving it shows this one: on `/accounts`
 * *Disable 3* removes itself the moment there is nothing left to disable, so a
 * *successful* press destroys the control the operator's finger and focus were both
 * on, and the user agent drops them on `<body>`. Measured, and it is the same one
 * number the whole `refocus.ts` family is about.
 *
 * The rule is `refocus.ts`'s sibling clause unchanged — *the first remaining action in
 * the group the removed control belonged to* — and here that is the counterpart verb,
 * which is also what an operator reaches for next: *Enable* over the same rows is the
 * undo for the *Disable* they just ran.
 *
 * **It waits for the rows.** A timeout after the batch settles would focus the right
 * button and then lose it again a few milliseconds later:
 * the verbs are derived from the rows, the rows arrive on the invalidated refetch, and
 * that render is what swaps one button for the other — focus lands on `<body>` either way.
 * So the anchor is spent on the render where `rows` changes identity — exactly what
 * `useRefocusAfterRemoval` does, and for exactly the same reason.
 *
 * Keyed on `data-role` rather than on a class, because the class that separates an
 * action from *Clear selection* is a visual one and would stop matching the day a bar
 * wants a ghost-styled verb.
 */
export function useRefocusAfterBatch(
  rows: readonly unknown[],
  bar: RefObject<HTMLElement | null>,
): () => void {
  const armed = useRef(false);
  useEffect(() => {
    if (!armed.current) return;
    armed.current = false;
    bar.current?.querySelector<HTMLElement>('[data-role="batch-action"]')?.focus();
  }, [rows, bar]);
  return () => {
    armed.current = true;
  };
}

/**
 * On a timeout, not inline.
 *
 * A confirmed batch closes a `<dialog>`, and the platform's own return-to-invoker runs
 * inside `close()` during the effect after the commit. Focusing before that means
 * being overwritten by it — and overwritten with `<body>`, because the invoker is the
 * button that has just gone.
 */
function after(find: () => HTMLElement | null | undefined): void {
  setTimeout(() => find()?.focus(), 0);
}
