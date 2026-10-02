import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import { SignInOrSetup } from "../App.jsx";
import { SetupScreen, SetupUnreadyScreen } from "./Setup.jsx";

/**
 * The first-access setup, as far as a console with no jsdom can see it: what the
 * form contains, and that the sign-in screen gives way to it only when the server
 * says a setup is open. The server's half — once, the code, the race — is locked in
 * `internal/api/setup_test.go`, `internal/apistore/setup_test.go` and the mock's
 * `first-run.test.ts`.
 */

function render(node: React.ReactNode, setup?: { required: boolean; noAdministrator?: boolean }) {
  const qc = new QueryClient();
  if (setup) qc.setQueryData(["setup"], setup);
  return renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("the first-access setup", () => {
  it("asks for the installer's code, a username and a password of the operator's own", () => {
    const html = render(<SetupScreen />);
    expect(html).toContain('id="setup-setupCode"');
    expect(html).toContain('autoComplete="one-time-code"');
    expect(html).toContain('id="setup-username"');
    expect(html).toMatch(/id="setup-password"[^>]*type="password"|type="password"[^>]*id="setup-password"/);
    expect(html).toContain('autoComplete="new-password"');
    /* No credential anywhere in it: Foyer ships none, and a form is where one would
       look most natural. */
    expect(html).not.toMatch(/Foyer@123|value="admin"/);
  });

  it("replaces the sign-in form only while the server says a setup is open", () => {
    const open = render(<SignInOrSetup />, { required: true });
    expect(open, "the sign-in form rendered while a setup is open").not.toContain('id="signin-username"');
    const closed = render(<SignInOrSetup />, { required: false });
    expect(closed).toContain('id="signin-username"');
  });

  /**
   * A fresh install whose bootstrap never ran — an interrupted installer, a restore
   * that lost `console_setup`, a container started without `foyerctl open-setup`.
   * The install has **no administrator and no code**; the sign-in form would be wrong
   * there, because its own copy promises a recovery ("an administrator will disable
   * this account") that needs an administrator this install does not have. The server's half is `internal/api/setup_test.go` and
   * `internal/apistore/setup_test.go`; this is the half a stranger looks at.
   *
   * **Two renders, and the split between them is the point.** `SignInOrSetup` is
   * where the choice is made, and it is driven here — but both of its non-sign-in branches
   * are lazy, and `renderToStaticMarkup` does not resolve a suspended chunk (it
   * renders the `null` fallback), so this render can only assert **which screen it
   * did not choose**: not the sign-in form, and not the setup form. A second render
   * of `SetupUnreadyScreen` itself is where the copy is checked, eagerly, with no
   * suspense in the way. Rendering the branch here and asserting strings in it would
   * pass against an empty string — a green suite that is not evidence the thing works.
   */
  it("takes the sign-in form away from an install that has no administrator", () => {
    const broken = render(<SignInOrSetup />, { required: false, noAdministrator: true });
    expect(broken, "a sign-in form on an install with no accounts").not.toContain(
      'id="signin-username"',
    );
    /* Not the setup form either: no code, so there is nothing to submit. */
    expect(broken, "a code form where no code exists").not.toContain('id="setup-setupCode"');
  });

  it("says what is wrong and what to do, and offers no way in", () => {
    const html = render(<SetupUnreadyScreen />);
    expect(html).toContain("This installation is not finished");
    expect(html).toContain("no administrator account yet");
    expect(html).toContain("Run the installer again");
    /* The guard is the installer's 80-bit code. A form here would turn it into
       decoration for whoever opens the console first. */
    expect(html).not.toContain("<form");
    expect(html).not.toContain("<input");
  });

  /* The negative half, and the reason the cases above are not satisfied by "always
     show the dead end": a finished install still gets its sign-in form. */
  it("still offers the sign-in form on an install that has an administrator", () => {
    const html = render(<SignInOrSetup />, { required: false, noAdministrator: false });
    expect(html).toContain('id="signin-username"');
  });

  /* And nothing is shown before the answer arrives. Falling through to the sign-in
     form while `data` is undefined would flash a bare login on every load of this
     route — on the very screen whose job is to replace it. */
  it("shows no screen at all while the answer is in flight", () => {
    expect(render(<SignInOrSetup />), "a sign-in form before the server answered").not.toContain(
      'id="signin-username"',
    );
  });
});
