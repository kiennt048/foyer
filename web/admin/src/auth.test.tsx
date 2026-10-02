import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { SignInScreen } from "./screens/SignIn.jsx";

/**
 * The console's identity, and the two things about it that regress silently.
 *
 * **`@foyer/admin` has no jsdom**, so nothing here observes a request, a redirect or
 * a focus move — those belong to `web/e2e`. What is observable is the *shape* of the
 * seam: which module is allowed to answer "may this operator", and what a sign-in
 * form contains before anybody types into it. Both are call-site properties, and a
 * call site is what someone reading `auth.tsx` cannot see.
 */

const SOURCES = import.meta.glob("./**/*.{ts,tsx}", {
  query: "?raw",
  eager: true,
  import: "default",
}) as Record<string, string>;

const files = Object.entries(SOURCES).filter(([path]) => !path.endsWith(".test.ts") && !path.endsWith(".test.tsx"));

describe("role gating has exactly one seam", () => {
  it("has console source to read at all", () => {
    expect(files.length).toBeGreaterThan(10);
  });

  /**
   * **`ROLE_PERMISSIONS` is the server's derivation and this console may not read it.**
   *
   * It lives in `@foyer/contract` for the same reason `DRIVER_SHAPES` does — it
   * follows from a product decision and is what a *server* implements. A client that
   * gated on it keeps gating on it after a deployment scopes a role per site or adds
   * a third one, and the drift presents as a control that renders and then `403`s,
   * which is the defect capability gating exists to prevent. What a client reads is
   * `AuthSession.permissions`, always.
   */
  it("never reads the server's role table", () => {
    /*
     * A **use**, not the word. `auth.tsx` names `ROLE_PERMISSIONS` in prose — it has
     * to, since explaining why the console does not read it is half of what that file
     * is for — and a guard that fired on the explanation would be a guard whose only
     * fix is to stop writing the explanation down.
     *
     * So: importing it, or indexing it. Both are what reading it looks like; neither
     * is what talking about it looks like. Checked against samples below rather than
     * trusted, because a regex that matches nothing passes this test perfectly.
     */
    const READS_IT = /import[^;]*\bROLE_PERMISSIONS\b[^;]*from|\bROLE_PERMISSIONS\s*\[/;
    expect(READS_IT.test('import { ROLE_PERMISSIONS } from "@foyer/contract";')).toBe(true);
    expect(READS_IT.test("const p = ROLE_PERMISSIONS[session.operator.role];")).toBe(true);
    expect(READS_IT.test(" * `ROLE_PERMISSIONS` is the server's derivation.")).toBe(false);

    const offenders = files.filter(([, body]) => READS_IT.test(body)).map(([p]) => p);
    expect(
      offenders,
      "`ROLE_PERMISSIONS` is the v1 derivation the *server* implements. Gate on " +
        "`useCan`, which reads `AuthSession.permissions` — the answer for this subject, " +
        "measured and on the wire.",
    ).toEqual([]);
  });

  /**
   * **`can()` is imported in exactly one file**, and every control goes through the
   * hook that wraps it.
   *
   * Not tidiness. `useCan` carries a decision `can()` cannot: with no session at all
   * nothing is gated, because a permission gate answers *may this operator* and there
   * is no operator to ask about. A screen that reached for `can()` directly would
   * answer that question "no" and hide the whole product from a deployment whose
   * server is not asking for a session — and it would do it on one screen, so the
   * console would disagree with itself about who is signed in.
   */
  it("routes every gate through useCan", () => {
    /* The import statement, not the word: `can` is three letters that occur inside
       ordinary prose and inside `canRotate`. `[^}]*` keeps the match inside one
       braced import list, and `\b` on both sides keeps `Capabilities` out. Checked
       against samples first — `auth.tsx` is excluded below, so with a regex that
       matched nothing this assertion would be green for every possible tree. */
    const IMPORTS_CAN = /import[^;]*\{[^}]*\bcan\b[^}]*\}[^;]*"@foyer\/contract"/;
    expect(IMPORTS_CAN.test('import { can, type Nas } from "@foyer/contract";')).toBe(true);
    expect(IMPORTS_CAN.test('import { canRotate } from "@foyer/contract";')).toBe(false);
    expect(IMPORTS_CAN.test('import { Capabilities } from "@foyer/contract";')).toBe(false);

    const direct = files
      .filter(([path]) => path !== "./auth.tsx")
      .filter(([, body]) => IMPORTS_CAN.test(body))
      .map(([p]) => p);
    expect(
      direct,
      "`can()` imported outside `auth.tsx`. Use `useCan`, which is the same check plus " +
        "the answer for a console with no session at all.",
    ).toEqual([]);
  });
});

describe("the sign-in form", () => {
  const markup = renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/sign-in"]}>
        <SignInScreen />
      </MemoryRouter>
    </QueryClientProvider>,
  );

  /**
   * **No credential, in any form, anywhere near it.**
   *
   * `docs/rules/security.md` §7 forbids a static credential in this repository,
   * and a sign-in form is where one looks most natural — a placeholder username, a
   * `defaultValue`, a "try …" hint. `web/mock` prints usable sign-ins in its startup
   * banner instead, which is the right place: it is the thing that knows it verifies
   * nothing.
   *
   * Asserted on the rendered markup rather than on the source, because a default
   * reaches the operator through the DOM however it was written.
   */
  it("suggests no username and no password", () => {
    expect(markup).not.toMatch(/placeholder=/);
    /* Both fields render `value=""`. Anything between those quotes is a default
       somebody typed in to make the form easier to try. Inputs only: the language
       switch under the form is a `<select>` whose options carry their language. */
    expect(markup).not.toMatch(/<input[^>]*value="[^"]+"/);
    /* The fixture operators, by name — the most likely thing to be helpfully filled
       in, since they are the two usernames that actually work against the mock. */
    expect(markup).not.toMatch(/dang\.quoc\.viet|le\.minh\.tuan/i);
  });

  /**
   * Password managers fill a form they can identify, and only that one.
   *
   * Case-insensitive, and the reason is worth stating rather than rediscovering:
   * React serialises this prop with its camelCase spelling — `autoComplete="username"`
   * appears verbatim in the string below — and an HTML parser lowercases every
   * attribute name, so the browser reads `autocomplete`. Confirmed against the
   * running console: `getAttributeNames()` returns `autocomplete`. An exact-case
   * assertion here would be red against markup that is correct.
   */
  it("names its fields for a password manager", () => {
    expect(markup).toMatch(/autocomplete="username"/i);
    expect(markup).toMatch(/autocomplete="current-password"/i);
  });

  /**
   * Nothing is announced until something has been refused.
   *
   * A `role="alert"` that is present and empty and later filled announces nothing in
   * most screen readers — which is how a console can carry a live region on every
   * route and still be silent. Both of this form's alerts are inside `{x ? … : null}`
   * and therefore mount with their text; the empty render proves only that they are
   * not there yet, and `web/e2e` has to prove the other half by driving a refusal.
   */
  it("mounts no empty live region", () => {
    expect(markup).not.toMatch(/role="alert"|role="status"|aria-live/);
  });

  /** The absent feature, said where somebody looks for it. */
  it("says there is no password reset", () => {
    expect(markup).toContain("There is no password reset");
  });
});
