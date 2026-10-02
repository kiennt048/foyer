import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { NavLink } from "react-router";
import { PASSWORD_MIN_LENGTH } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { SIGN_IN_PATH, useAuth } from "../auth.js";
import { t } from "../i18n/password.js";
import { fieldMessages } from "../problems.js";
import { ErrorNotice, Notice } from "../ui/primitives.js";

/**
 * Change your own password. **Your own, and there is no operation for anybody
 * else's** — see `OperatorUpdate` in `openapi.yaml` for why that absence is a
 * decision rather than a gap, and `/operators` for where an administrator is told.
 *
 * A route rather than a dialog in the shell: it is reached from the sidebar once in a
 * while, it is the operator's own account rather than something they are doing to a
 * row, and as a route it is lazily loaded and gets the console's route-change focus
 * handling from `PageHead` for free.
 *
 * **The rule is stated before anything is typed.** `PASSWORD_MIN_LENGTH` is exported
 * by `@foyer/contract` for exactly this: a length requirement an operator can only
 * discover by being refused is a form that rejects work already done, and the one
 * they had in mind is now half-typed into a field they have to clear. The server
 * refuses below it as well — this is the first check, not the only one.
 */
export function PasswordScreen() {
  const { session } = useAuth();
  const [currentPassword, setCurrent] = useState("");
  const [newPassword, setNext] = useState("");
  const [done, setDone] = useState(false);

  const change = useMutation({
    mutationFn: () => api.changePassword({ currentPassword, newPassword }),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setDone(true);
    },
  });

  const fieldErrors = fieldMessages(change.error);

  return (
    <div className="page">
      <PageHead title={t("password.title")} description={t("password.description")} />

      <div className="card" style={{ maxWidth: "26rem" }}>
        {/* Nobody is signed in, so there is no password of *yours* to change. Said
            rather than shown as an empty form that can only answer `401`. */}
        {!session ? (
          <Notice level="info" title={t("password.signedOut.title")}>
            {t("password.signedOut.body")}{" "}
            <NavLink to={SIGN_IN_PATH}>{t("signIn.title")}</NavLink>
          </Notice>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (change.isPending) return;
              setDone(false);
              change.mutate();
            }}
          >
            {change.error ? (
              <div style={{ marginBottom: "0.875rem" }}>
                <ErrorNotice error={change.error} title={t("password.failed")} />
              </div>
            ) : null}
            {/* Mounted with its text, and `status` rather than `alert`: a completed
                change is not an interruption. It is removed the moment the form is
                submitted again, so it can never describe the request in flight. */}
            {done ? (
              <div style={{ marginBottom: "0.875rem" }}>
                <Notice level="positive" role="status" title={t("password.done")} />
              </div>
            ) : null}

            <div className="form-grid">
              <div className="field">
                <label htmlFor="current-password">{t("password.current")}</label>
                <input
                  id="current-password"
                  type="password"
                  autoComplete="current-password"
                  value={currentPassword}
                  aria-invalid={Boolean(fieldErrors.currentPassword) || undefined}
                  /*
                   * Both, and the error second.
                   *
                   * This swapped the hint *for* the error, while the new-password field
                   * four lines down concatenated them; the asymmetry inside one form is
                   * the tell that it was an accident rather than a decision. The
                   * sentence it dropped is the one explaining why the current password
                   * is asked for at all when the browser is already signed in — and it
                   * stopped being announced at exactly the moment the operator got the
                   * password wrong and most needed to know what was being asked. The
                   * hint stayed on screen throughout, so nothing in a browser and
                   * nothing in axe could see it.
                   */
                  aria-describedby={
                    fieldErrors.currentPassword
                      ? "current-password-hint current-password-error"
                      : "current-password-hint"
                  }
                  onChange={(e) => setCurrent(e.target.value)}
                />
                {/* Why it is asked for at all, since the browser is already signed
                    in: an unattended console is the ordinary case behind a counter. */}
                <p className="field-hint" id="current-password-hint">
                  {t("password.currentHint")}
                </p>
                {fieldErrors.currentPassword ? (
                  <p className="field-error" role="alert" id="current-password-error">
                    {fieldErrors.currentPassword}
                  </p>
                ) : null}
              </div>

              <div className="field">
                <label htmlFor="new-password">{t("password.new")}</label>
                <input
                  id="new-password"
                  type="password"
                  autoComplete="new-password"
                  value={newPassword}
                  aria-invalid={Boolean(fieldErrors.newPassword) || undefined}
                  aria-describedby={
                    fieldErrors.newPassword ? "new-password-hint new-password-error" : "new-password-hint"
                  }
                  onChange={(e) => setNext(e.target.value)}
                />
                <p className="field-hint" id="new-password-hint">
                  {t("password.rule", PASSWORD_MIN_LENGTH)}
                </p>
                {fieldErrors.newPassword ? (
                  <p className="field-error" role="alert" id="new-password-error">
                    {fieldErrors.newPassword}
                  </p>
                ) : null}
              </div>

              <button
                type="submit"
                className="btn btn-primary"
                aria-disabled={change.isPending || undefined}
              >
                {t("password.submit")}
              </button>
            </div>
          </form>
        )}
      </div>

      {/* The consequence of there being no reset channel, stated where the operator
          is choosing a password rather than after they have forgotten it. */}
      <p className="signin-foot" style={{ maxWidth: "26rem" }}>
        {t("password.noReset")}
      </p>
    </div>
  );
}
