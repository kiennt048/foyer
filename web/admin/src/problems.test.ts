import { afterEach, describe, expect, it, vi } from "vitest";
import type { FieldFault, Problem, ProblemType } from "@foyer/contract";
import { ApiError, api } from "./api.js";
import { budgetBytes } from "./format.js";
import { errorCopy, fieldMessages, problemCopy } from "./problems.js";

/**
 * The refusal catalogue.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **What this file cannot prove, said before the assertions rather than after.**
 * Calling `problemCopy({ type })` and reading the sentence back proves the
 * catalogue compiles and interpolates. It proves nothing about whether any code
 * path in the console ever produces that type — which is the shape of every
 * mechanism in this repository that shipped looking like a check. Driving each
 * type against the mock is `web/e2e`'s job and is not substitutable by anything
 * here.
 *
 * What these assertions are for is the half a drive cannot see cheaply: that a
 * `params`-carrying entry actually *reads* its params, that two values of one
 * param produce two different sentences, and that the fallback ladder is ordered
 * the way the contract says. A catalogue whose branches all render the same
 * confident sentence passes a drive and fails here.
 */

const ALL: ProblemType[] = [
  "/problems/validation",
  "/problems/not-found",
  "/problems/duplicate",
  "/problems/too-large",
  "/problems/garden-entry-required",
  "/problems/controller-in-use",
  "/problems/controller-unreachable",
  "/problems/needs-controller",
  "/problems/rotation-refused",
  "/problems/device-not-verified",
  "/problems/method-not-offered",
  "/problems/no-grant",
  "/problems/internal",
  "/problems/session-end-failed",
  "/problems/not-signed-in",
  "/problems/bad-credentials",
  "/problems/forbidden",
  "/problems/last-administrator",
  "/problems/unreachable",
];

/*
 * The list above is exhaustive at compile time, in the direction the catalogue's
 * own `Record<ProblemType, …>` does not cover. That record fails to build when a
 * value is *added* to the contract with no copy; this fails to build when a value
 * is added and the sweep below quietly keeps testing fourteen of fifteen.
 */
type Untested = Exclude<ProblemType, (typeof ALL)[number]>;
const EXHAUSTIVE: Untested extends never ? true : Untested = true;

describe("the problem catalogue", () => {
  it("has a sentence for every type in the contract", () => {
    expect(EXHAUSTIVE).toBe(true);
    expect(ALL).toHaveLength(new Set(ALL).size);

    for (const type of ALL) {
      const copy = problemCopy({ type });
      expect(copy.fromServer).toBe(false);
      expect(copy.title.length).toBeGreaterThan(0);
      expect(copy.body.length).toBeGreaterThan(0);
      expect(copy.title).not.toBe(copy.body);
      /* The three ways a catalogue entry is wrong while being present: it echoes the
         wire value, or it interpolates a param that was not sent. */
      expect(`${copy.title} ${copy.body}`).not.toContain("/problems/");
      expect(`${copy.title} ${copy.body}`).not.toMatch(/undefined|NaN|\[object/);
    }
  });

  it("says something different for each reachability, which is the whole reason it is sent", () => {
    const of = (nasReachability: string) =>
      problemCopy({
        type: "/problems/session-end-failed",
        params: { nasName: "UniFi U6 Mesh", nasReachability },
      }).body;

    const unreachable = of("unreachable");
    const expired = of("credentials-expired");
    const unknown = of("unknown");

    expect(unreachable).toContain("UniFi U6 Mesh");
    expect(new Set([unreachable, expired, unknown]).size).toBe(3);
    /* The headline is the part an operator reads at a glance, and it has one job. */
    expect(problemCopy({ type: "/problems/session-end-failed" }).title).toMatch(/still online/i);
  });

  it("names the device even when the server could not", () => {
    // `nasName` is `nas?.name ?? null` on the wire: a session on a NAS row that has
    // gone away has no name to carry, and the sentence still has to be a sentence.
    const body = problemCopy({
      type: "/problems/session-end-failed",
      params: { nasName: null, nasReachability: "unknown" },
    }).body;
    expect(body).not.toContain("null");
    expect(body.length).toBeGreaterThan(20);
  });

  it("counts, and does not say '1 access points'", () => {
    const one = problemCopy({ type: "/problems/controller-in-use", params: { count: 1 } }).body;
    const nine = problemCopy({ type: "/problems/controller-in-use", params: { count: 9 } }).body;
    expect(one).toContain("1 access point came");
    expect(nine).toContain("9 access points came");
  });

  /*
   * The figures moved, on purpose, and the pin moved with them.
   *
   * `102_400` is `100 * 1024`: the portal's image ceiling, and the number `/portal`
   * puts on the field beside the upload. This refusal rendered it as **102 kB**
   * while that field rendered **100 kB**, one press apart, because the two used
   * different bases and both wrote "kB". Both go through `budgetBytes` now.
   *
   * The exact strings stay pinned rather than relaxed to a substring: the point of
   * this file is that the client owns every sentence, and "some number followed by
   * kB" is not a sentence anybody owns.
   */
  it("renders both byte figures in the unit the screen beside them uses", () => {
    const body = problemCopy({
      type: "/problems/too-large",
      params: { bytes: 143_000, limitBytes: 102_400 },
    }).body;
    expect(body).toContain("140 kB");
    expect(body).toContain("100 kB");
    /* Derived, so a change of base here cannot pass by moving the two lines above
       together: this is the same call `/portal`'s own ceiling is rendered by. */
    expect(body).toContain(budgetBytes(102_400));
  });

  it("falls back to the server's label for a type it predates, and never to blank", () => {
    const newer = { type: "/problems/quota-exhausted" as ProblemType, title: "Quota exhausted" };
    const copy = problemCopy(newer);
    expect(copy.fromServer).toBe(true);
    expect(copy.title).toBe("Quota exhausted");
    expect(copy.body.length).toBeGreaterThan(0);

    const bare = problemCopy({ type: "/problems/quota-exhausted" as ProblemType });
    expect(bare.title).not.toContain("/problems/");
    expect(bare.title.length).toBeGreaterThan(0);
    expect(bare.body.length).toBeGreaterThan(0);
  });

  it("never renders the server's own words for a type it knows", () => {
    /*
     * The Vietnamese here is written as escapes on purpose. `admin.test.ts` sweeps
     * every source file in `src/` for a Vietnamese character and this file is not
     * exempt from it — nor should it be, since the sweep guards against server-written
     * copy and the code under test is the fix for it. Escapes keep both true: the guard sees ASCII,
     * and the assertion is still about the string a server actually sent.
     */
    const vietnameseTitle = "Kh\u00f4ng t\u00ecm th\u1ea5y";
    const copy = problemCopy({ type: "/problems/not-found", title: vietnameseTitle });
    expect(copy.fromServer).toBe(false);
    expect(copy.title).not.toBe(vietnameseTitle);
    expect(copy.body).not.toContain(vietnameseTitle);
  });
});

describe("a refused field", () => {
  const refusal = (fields: Record<string, FieldFault>, errors: Record<string, string>, params = {}) =>
    fieldMessages(
      new ApiError(422, {
        type: "/problems/validation",
        title: "Not accepted",
        status: 422,
        params: { fields, ...params },
        errors,
      } as Problem),
    );

  it("prefers the triple over the fault, and the fault over the server's prose", () => {
    /* `methods` is a set of chips: "Required." is true and useless, because the
       operator has to be told that choosing one is enough. */
    const chips = refusal({ methods: "required" }, { methods: "Ch\u1ecdn \u00edt nh\u1ea5t m\u1ed9t." });
    expect(chips.methods).toBe("Choose at least one.");

    /* No triple for this one, so the fault's own sentence wins — and the server's
       prose, which says the same thing in the server's language, does not. */
    const text = refusal({ venueName: "required" }, { venueName: "B\u1eaft bu\u1ed9c." });
    expect(text.venueName).toBe("Required.");
  });

  it("tells the three ways one field is refused apart", () => {
    // The pair `(type, field)` that made `FieldFault` necessary.
    const seen = new Set([
      refusal({ value: "required" }, { value: "x" }).value,
      refusal({ value: "not-a-hostname" }, { value: "x" }).value,
      refusal({ value: "not-a-cidr" }, { value: "x" }).value,
    ]);
    expect(seen.size).toBe(3);
  });

  it("falls back to the server's prose for a fault it has no copy for", () => {
    // A drifted server: `errors` carries the field, `params.fields` does not.
    const out = refusal({}, { surname: "Not a surname." });
    expect(out.surname).toBe("Not a surname.");
  });

  it("marks every field the server named, from either half", () => {
    const out = refusal({ username: "duplicate" }, { username: "x", password: "y" });
    expect(Object.keys(out).sort()).toEqual(["password", "username"]);
    expect(out.username).not.toBe("x");
  });

  /* `512_000`: the logo ceiling — so it reads as the round number the
     project calls it everywhere else. It was `8_192` before it was raised. */
  it("carries the byte figures into the field's own sentence", () => {
    const out = refusal({ logoDataUri: "too-large" }, { logoDataUri: "x" }, {
      bytes: 600_000,
      limitBytes: 512_000,
    });
    expect(out.logoDataUri).toContain("586 kB");
    expect(out.logoDataUri).toContain("500 kB");
  });

  it("is empty for anything that is not a refusal", () => {
    expect(fieldMessages(new TypeError("Failed to fetch"))).toEqual({});
    expect(fieldMessages(undefined)).toEqual({});
  });
});

describe("classification in the data layer", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("turns a rejected fetch into /problems/unreachable rather than a TypeError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );

    const error = await api.sites().then(
      () => null,
      (e: unknown) => e,
    );

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).problem?.type).toBe("/problems/unreachable");
    expect((error as ApiError).status).toBe(0);

    /* The point of classifying at all: the surfaces above see a type they have copy
       for, instead of falling through `instanceof ApiError` to `String(error)`. */
    const copy = errorCopy(error);
    expect(copy.fromServer).toBe(false);
    expect(copy.body).not.toContain("TypeError");
    expect(copy.body).not.toContain("fetch");
  });

  it("gives an unparseable failure a type instead of no type at all", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response("<html>502 Bad Gateway</html>", {
            status: 502,
            statusText: "Bad Gateway",
          }),
        ),
      ),
    );

    const error = (await api.sites().catch((e: unknown) => e)) as ApiError;
    expect(error.problem?.type).toBe("/problems/internal");
    expect(errorCopy(error).body).not.toContain("html");
  });

  it("passes a real problem document through untouched", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              type: "/problems/controller-in-use",
              title: "Access points still adopted",
              status: 409,
              params: { count: 9 },
            }),
            { status: 409, headers: { "content-type": "application/problem+json" } },
          ),
        ),
      ),
    );

    const error = (await api.sites().catch((e: unknown) => e)) as ApiError;
    expect(errorCopy(error).body).toContain("9 access points came");
  });
});
