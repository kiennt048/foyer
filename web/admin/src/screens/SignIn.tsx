import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { api } from "../api.js";
import { AUTH_KEY } from "../auth.js";
import { t } from "../i18n/index.js";
import { fieldMessages } from "../problems.js";
import { Doorway } from "../ui/icons.js";
import { ErrorNotice, Notice } from "../ui/primitives.js";
import { LanguageSwitch } from "../ui/LanguageSwitch.js";
import { useRouteWithoutPageHead } from "./refocus.js";

/**
 * The way in.
 *
 * **Eager, and the only screen in this console that is.** It is the first thing an
 * enforcing deployment renders, so a lazily-loaded sign-in form would put a chunk
 * fetch in front of the one page an operator cannot get past — while `/operators` and
 * `/audit` are administrator surfaces a staff session never opens and are split.
 *
 * It renders **outside the shell**: no sidebar, no site switcher, no command palette.
 * Nothing in the shell is meaningful to somebody who is not signed in, and a nav
 * whose destinations all answer `401` is a menu of dead ends.
 *
 * **Nothing here hints at a credential.** No placeholder username, no default, no
 * "try admin/admin". `docs/rules/security.md` §7 forbids a static credential
 * anywhere in the repository, and a sign-in form is where one would look most
 * natural. `web/mock` prints usable sign-ins in its startup banner, which is the
 * right place: it is the thing that knows it verifies nothing.
 */
export function SignInScreen() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  /*
   * This screen is a page, and it is the only one with no `PageHead` to say so.
   *
   * Without this line the console's route-change focus rule never learns that
   * `/sign-in` was on screen, and the transition out of it is exempted twice over —
   * `refocus.ts` carries the measurements. It records where the
   * console is; it moves no focus, because the username field's `autoFocus` has
   * already put it in the right place.
   */
  useRouteWithoutPageHead(location.pathname);

  /*
   * Why the operator is here, and where they were going.
   *
   * `whenSignedOut` in `auth.tsx` sends both. Arriving because a request was refused
   * is a different thing from arriving because somebody pressed *Sign in*, and a form
   * that says nothing about it leaves an operator wondering what happened to the page
   * they were on.
   */
  const from = location.state as { signedOut?: boolean; from?: string } | null;

  /*
   * **A path this console produced, never one it was handed.** The value comes from
   * our own router, but the check costs one line and the portal's rule applies here
   * too: a redirect target that starts `//` or carries a scheme is somebody else's
   * origin. `docs/rules/security.md` §4.
   */
  const back = from?.from;
  const destination = back && back.startsWith("/") && !back.startsWith("//") ? back : "/";

  const signIn = useMutation({
    mutationFn: () => api.signIn({ username, password }),
    onSuccess: (session) => {
      qc.setQueryData(AUTH_KEY, session);
      /* Everything already in the cache was fetched as nobody, or as somebody else.
         A staff member signing in after an administrator must not read their list. */
      void qc.invalidateQueries();
      navigate(destination, { replace: true });
    },
  });

  const fieldErrors = fieldMessages(signIn.error);

  return (
    <div className="signin">
      <div className="signin-card">
        <div className="brand" style={{ padding: "0 0 1rem" }}>
          <Doorway size={18} />
          Foyer
        </div>

        <div className="card">
          <h1 className="dialog-title" style={{ marginBottom: "0.875rem" }}>
            {t("signIn.title")}
          </h1>

          {/* Mounted with its text, both of them. A `role="alert"` that is present and
              empty and later filled announces nothing in most screen readers — which
              is how a console carries a live region on every route and stays silent. */}
          {from?.signedOut ? (
            <div style={{ marginBottom: "0.875rem" }}>
              <Notice level="neutral" role="status" title={t("problem.not-signed-in.title")}>
                {t("problem.not-signed-in.body")}
              </Notice>
            </div>
          ) : null}

          <form
            onSubmit={(e) => {
              e.preventDefault();
              /* On the form rather than the button: an `aria-disabled` submit still
                 submits, and Enter in either field submits without touching it. */
              if (signIn.isPending) return;
              signIn.mutate();
            }}
          >
            {signIn.error ? (
              <div style={{ marginBottom: "0.875rem" }}>
                <ErrorNotice error={signIn.error} />
              </div>
            ) : null}

            <div className="form-grid">
              <div className="field">
                <label htmlFor="signin-username">{t("signIn.username")}</label>
                <input
                  id="signin-username"
                  type="text"
                  /* The operator's next action is to type here, and this screen has no
                     other content to read first. */
                  autoFocus
                  autoComplete="username"
                  value={username}
                  aria-invalid={Boolean(fieldErrors.username) || undefined}
                  aria-describedby={fieldErrors.username ? "signin-username-error" : undefined}
                  onChange={(e) => setUsername(e.target.value)}
                />
                {fieldErrors.username ? (
                  <p className="field-error" role="alert" id="signin-username-error">
                    {fieldErrors.username}
                  </p>
                ) : null}
              </div>

              <div className="field">
                <label htmlFor="signin-password">{t("signIn.password")}</label>
                <input
                  id="signin-password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  aria-invalid={Boolean(fieldErrors.password) || undefined}
                  aria-describedby={fieldErrors.password ? "signin-password-error" : undefined}
                  onChange={(e) => setPassword(e.target.value)}
                />
                {fieldErrors.password ? (
                  <p className="field-error" role="alert" id="signin-password-error">
                    {fieldErrors.password}
                  </p>
                ) : null}
              </div>

              {/* `aria-disabled`, not `disabled`: the operator pressed this, so it has
                  focus, and setting `disabled` on a focused control blurs it to
                  `<body>` for the whole request. `screens.test.ts` holds the rule. */}
              <button
                type="submit"
                className="btn btn-primary"
                aria-disabled={signIn.isPending || undefined}
              >
                {t("signIn.submit")}
              </button>
            </div>
          </form>
        </div>

        {/* There is no reset channel, and the sign-in form is where somebody looks for
            one. Said here so nobody adds a "forgot your password" link that can only
            lead to a page explaining that it does nothing. */}
        <p className="signin-foot">{t("signIn.noReset")}</p>
        <LanguageSwitch className="field language-switch signin-foot" />
      </div>
    </div>
  );
}
