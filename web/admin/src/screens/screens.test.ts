import { describe, expect, it } from "vitest";

/**
 * Three call-site invariants, pinned in source rather than in a browser.
 *
 * Each one was a real defect measured with a keyboard against the running console,
 * and each one is a *call site* problem that the primitive it goes through cannot
 * detect from where it sits — which is precisely why reading `ui/primitives.tsx`
 * alone finds none of the three.
 *
 * **`@foyer/admin` has no jsdom.** Its unit tests are `renderToStaticMarkup`, so focus
 * is not observable here at all and the behavioural half of these lives in `web/e2e`.
 * What *is* observable here is the shape of the call, and the shape is what regresses:
 * a new screen copies the nearest existing dialog, and the nearest existing dialog is
 * the one that was wrong. These fail on the copy rather than on the consequence.
 *
 * `import.meta.glob` with `?raw`, the same mechanism the Vietnamese-string guard in
 * `admin.test.ts` uses — the admin has no `node:fs`, and Vite hands the source over
 * as text.
 */

const SOURCES = import.meta.glob("./**/*.tsx", {
  query: "?raw",
  eager: true,
  import: "default",
}) as Record<string, string>;

/** Guards every guard below: a glob that matched nothing would pass silently. */
const files = Object.entries(SOURCES);

describe("the screens' call-site invariants", () => {
  it("has screens to read at all", () => {
    expect(files.length).toBeGreaterThan(6);
  });

  /**
   * `ConfirmDialog` is mounted always and toggled with `open`, never mounted
   * conditionally.
   *
   * The primitive closes itself — `el.close()` when `open` goes false — and the
   * platform's return of focus to whatever opened the dialog rides on that call. A
   * conditionally mounted one never lets `open` go false while it is still in the
   * document: the element is removed *while still open*, `close()` never runs, and the
   * return never happens.
   *
   * Measured on the three call shapes, Escape from the delete confirmation: always
   * mounted returns focus to its Delete button; `{confirmDelete ? <ConfirmDialog…> :
   * null}` leaves it on `<body>`. **Red and green against one primitive** says the
   * call site, not `ui/`, is where the fix belongs.
   *
   * A bare `open` — `<ConfirmDialog open …>` — is the tell, because it is what you
   * write when the ternary above is doing the opening.
   */
  it("never mounts a ConfirmDialog conditionally", () => {
    const bare = files.flatMap(([path, body]) =>
      [...body.matchAll(/<ConfirmDialog\b(?<props>[^>]*)/g)]
        .filter((m) => !/\bopen=\{/.test(m.groups?.props ?? ""))
        .map(() => path),
    );

    expect(
      files.filter(([, b]) => b.includes("<ConfirmDialog")).length,
      "No screen renders a ConfirmDialog — the pattern below is being checked against nothing.",
    ).toBeGreaterThan(2);
    expect(
      bare,
      "A ConfirmDialog with a bare `open` is one the parent mounts and unmounts, so " +
        "`el.close()` never runs and focus is left on `<body>`. Mount it always and " +
        "toggle `open={…}`.",
    ).toEqual([]);
  });

  /**
   * Every `<dialog>` says what it is.
   *
   * A `<dialog>` with no accessible name is announced as "dialog" and nothing else.
   * Five of the console's dialogs had none; two of them were `Modal` call sites that
   * had a visible `<h2 className="dialog-title">` sitting right there and simply did
   * not point at it. (The other three were `ConfirmDialog`, which names itself now.)
   *
   * `labelledBy` or `label` — both are real answers. The first is right wherever there
   * is a visible title, because the announced name is then the same string on screen
   * and cannot drift from it; the second exists for the command palette, whose first
   * row is a search field and which has no heading to point at.
   */
  it("gives every Modal an accessible name", () => {
    const unnamed = files.flatMap(([path, body]) =>
      [...body.matchAll(/<Modal\b(?<props>[^>]*)/g)]
        .filter((m) => !/\b(?:labelledBy|label)=/.test(m.groups?.props ?? ""))
        .map(() => path),
    );

    expect(
      files.filter(([, b]) => b.includes("<Modal")).length,
      "No screen renders a Modal — the pattern below is being checked against nothing.",
    ).toBeGreaterThan(3);
    expect(
      unnamed,
      'A <dialog> with no accessible name is announced as "dialog" and nothing else. ' +
        "Point `labelledBy` at the dialog's own `.dialog-title`.",
    ).toEqual([]);
  });

  /**
   * No control goes `disabled` because its own request is in flight.
   *
   * The user agent blurs a focused element the moment `disabled` is set on it —
   * `relatedTarget: null`, focus on `<body>` — and nothing puts it back when the
   * request settles. Instrumented with the request slowed to 2.5 s on `/accounts`:
   * focus was on `<body>` 600 ms in and still on `<body>` after it finished. These are
   * by definition the controls an operator *presses*, so they are by definition the
   * ones that have focus at the moment they are disabled. `aria-disabled` announces
   * the same unavailability, keeps the element focusable, and moves the enforcement
   * into the handler, where it belongs.
   *
   * **The inverse is not a defect and is not asserted.** `disabled` expressing a
   * precondition — `Print` before the printable set is counted, `Discover` before the
   * controller answers, `Continue` before the warning is acknowledged, `Delete site`
   * before the name is typed — is correct: the control has never had focus in that
   * state, and `disabled` is what keeps a tab stop that does nothing out of the order.
   * So this matches on the *pending* half only, which is the half that can eat focus.
   */
  it("never disables a control on its own request being in flight", () => {
    /* `(?<![-\w])` and not `\b`: a word boundary sits between the hyphen and the `d`
       of `aria-disabled`, so the obvious pattern matches every control this fix
       already repaired and reports the repair as the defect. */
    const offenders = files.flatMap(([path, body]) =>
      [...body.matchAll(/(?<![-\w])disabled=\{(?<expr>[^}]*)\}/g)]
        .filter((m) => /isPending|\bbusy\b/.test(m.groups?.expr ?? ""))
        .map((m) => `${path}: disabled={${m.groups?.expr}}`),
    );

    expect(
      files.filter(([, b]) => /(?<![-\w])disabled=\{/.test(b)).length,
      "No screen sets `disabled` at all — the pattern below is being checked against nothing.",
    ).toBeGreaterThan(2);
    expect(
      offenders,
      "`disabled` on the control that started the request blurs it to `<body>` for the " +
        "whole request. Use `aria-disabled` and guard the handler.",
    ).toEqual([]);
  });
});
