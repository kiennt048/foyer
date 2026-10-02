import { describe, expect, it } from "vitest";
import { ApiError } from "../api.js";
import { groupedNotes, runBatch, type BatchNote } from "./batch.js";

/**
 * What a batch of ten where three failed is allowed to say.
 *
 * The rules under test are the single-row rules, multiplied by a batch:
 *
 * - **A write that did not happen and a consequence that did not happen are different
 *   facts.** `Vouchers.tsx` keeps them apart on one row; confused on forty, it would be
 *   forty confident answers about a different outcome.
 * - **The rows that failed are named**, because *which three* is the only question a
 *   partial report exists to answer.
 * - **A failure does not stop the batch.** Ten rows means ten attempts; a run that
 *   halts on row four leaves six rows in a state nobody has been told about.
 */

const failWith = (type: string) =>
  new ApiError(503, { type, title: "t" } as never);

describe("a batch has three outcomes and keeps them apart", () => {
  it("separates a write that did not happen from a consequence that did not", async () => {
    const outcome = await runBatch(
      [
        { key: "a", label: "AAAA", run: async () => undefined },
        /* A `200` whose body carries a `Failure`: revoked, session still up. */
        { key: "b", label: "BBBB", run: async () => ({ type: "/problems/session-end-failed" }) },
        { key: "c", label: "CCCC", run: async () => Promise.reject(failWith("/problems/internal")) },
      ],
      () => {},
    );

    expect(outcome.total).toBe(3);
    expect(outcome.failed.map((f) => f.key)).toEqual(["c"]);
    expect(outcome.noted.map((n) => n.key)).toEqual(["b"]);
  });

  it("attempts every row, and reports progress once per row", async () => {
    const seen: number[] = [];
    const outcome = await runBatch(
      ["a", "b", "c", "d"].map((k) => ({
        key: k,
        label: k,
        run: async () => (k === "b" ? Promise.reject(failWith("/problems/internal")) : undefined),
      })),
      (done) => seen.push(done),
    );

    expect(seen).toEqual([1, 2, 3, 4]);
    expect(outcome.total).toBe(4);
    expect(outcome.failed).toHaveLength(1);
  });

  it("runs in order, so the report reads down the screen the way the rows do", async () => {
    const order: string[] = [];
    await runBatch(
      ["a", "b", "c"].map((k) => ({
        key: k,
        label: k,
        run: async () => {
          await Promise.resolve();
          order.push(k);
        },
      })),
      () => {},
    );
    expect(order).toEqual(["a", "b", "c"]);
  });
});

describe("the report names which rows, grouped by why", () => {
  const note = (label: string, body: string): BatchNote => ({
    key: label,
    label,
    copy: { title: "T", body, fromServer: false },
  });

  it("puts every label in the sentence", () => {
    const out = groupedNotes([note("AAAA", "Down."), note("BBBB", "Down."), note("CCCC", "Down.")]);
    for (const label of ["AAAA", "BBBB", "CCCC"]) expect(out).toContain(label);
  });

  it("says the shared reason once rather than once per row", () => {
    const out = groupedNotes([note("AAAA", "Down."), note("BBBB", "Down.")]);
    expect(out.match(/Down\./g)).toHaveLength(1);
    expect(out).toBe("AAAA, BBBB — Down.");
  });

  it("keeps two different reasons apart instead of picking one", () => {
    const out = groupedNotes([note("AAAA", "Down."), note("BBBB", "Refused.")]);
    expect(out).toContain("AAAA — Down.");
    expect(out).toContain("BBBB — Refused.");
  });

  it("falls back to the title where a type has no sentence", () => {
    expect(
      groupedNotes([{ key: "a", label: "AAAA", copy: { title: "Gone", body: "", fromServer: true } }]),
    ).toBe("AAAA — Gone");
  });
});
