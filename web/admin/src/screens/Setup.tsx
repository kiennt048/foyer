import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { PASSWORD_MIN_LENGTH, type AdminSetupRequest, type AuthSession } from "@foyer/contract";
import { request } from "../api.js";
/* Measured, not ornamental: without an edge back to `App`, Rollup hoisted the
   modules this screen shares with the entry into two extra chunks the entry still
   fetches first — +3.9 kB gzip on the entry (`check-admin-budget.mjs`).
   With it, the lazy chunk depends on the entry like every other screen does, and
   the entry grows by the lazy edge alone. `Password.tsx` has the same edge through
   `PageHead`. */
import "../App.js";
import { AUTH_KEY } from "../auth.js";
import { t } from "../i18n/setup.js";
import { fieldMessages } from "../problems.js";
import { Doorway } from "../ui/icons.js";
import { ErrorNotice, Notice } from "../ui/primitives.js";

/**
 * The first-access setup (`completeAdminSetup`), shown on `/sign-in` in place of the
 * sign-in form while `getAdminSetup` says one is open (`SignInOrSetup` in `App.tsx`).
 *
 * **Foyer ships no credential.** A fresh install has no operator; the installer
 * prints a one-time setup code, and this form turns that code, a username and a
 * password into the first administrator — once — and signs this browser in. The
 * server decides everything: whether the setup is open, the code, the password
 * floor and the race between two browsers. `internal/api/handlers_setup.go` has why
 * it is a code and not a first-come page.
 *
 * **Lazy**: an install shows it once in its life.
 */
export function SetupScreen() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [form, setForm] = useState<AdminSetupRequest>({ setupCode: "", username: "", password: "" });

  const complete = useMutation({
    mutationFn: () =>
      request<AuthSession>("/auth/setup", { method: "POST", body: JSON.stringify(form) }),
    onSuccess: (session) => {
      qc.setQueryData(AUTH_KEY, session);
      void qc.invalidateQueries();
      navigate("/", { replace: true });
    },
    onError: () => {
      /* Somebody else finished it: the sign-in form is the way forward, and asking
         again is what shows it. */
      void qc.invalidateQueries({ queryKey: ["setup"] });
    },
  });

  const errors = fieldMessages(complete.error);
  const field = (
    name: keyof AdminSetupRequest,
    label: string,
    type: string,
    autoComplete: string,
    hint?: string,
  ) => (
    <div className="field">
      <label htmlFor={`setup-${name}`}>{label}</label>
      <input
        id={`setup-${name}`}
        type={type}
        autoComplete={autoComplete}
        autoFocus={name === "setupCode"}
        spellCheck={false}
        value={form[name]}
        aria-invalid={Boolean(errors[name]) || undefined}
        aria-describedby={errors[name] ? `setup-${name}-error` : hint ? `setup-${name}-hint` : undefined}
        onChange={(e) => setForm({ ...form, [name]: e.target.value })}
      />
      {hint && !errors[name] ? (
        <p className="field-hint" id={`setup-${name}-hint`}>
          {hint}
        </p>
      ) : null}
      {errors[name] ? (
        <p className="field-error" role="alert" id={`setup-${name}-error`}>
          {errors[name]}
        </p>
      ) : null}
    </div>
  );

  return (
    <div className="signin">
      <div className="signin-card">
        <div className="brand" style={{ padding: "0 0 1rem" }}>
          <Doorway size={18} />
          Foyer
        </div>
        <div className="card">
          <h1 className="dialog-title" style={{ marginBottom: "0.5rem" }}>
            {t("setup.title")}
          </h1>
          <p style={{ marginBottom: "0.875rem" }}>{t("setup.lede")}</p>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (complete.isPending) return;
              complete.mutate();
            }}
          >
            {complete.error ? (
              <div style={{ marginBottom: "0.875rem" }}>
                <ErrorNotice error={complete.error} title={t("setup.failed")} />
              </div>
            ) : null}
            <div className="form-grid">
              {field("setupCode", t("setup.code"), "text", "one-time-code", t("setup.codeHint"))}
              {field("username", t("setup.username"), "text", "username")}
              {field(
                "password",
                t("setup.password"),
                "password",
                "new-password",
                t("setup.passwordRule", PASSWORD_MIN_LENGTH),
              )}
              <button
                type="submit"
                className="btn btn-primary"
                aria-disabled={complete.isPending || undefined}
              >
                {t("setup.submit")}
              </button>
            </div>
          </form>
          <div style={{ marginTop: "0.875rem" }}>
            <Notice level="neutral" title={t("setup.once")} />
          </div>
        </div>
      </div>
    </div>
  );
}

/**
 * The state that is **not** a form: no administrator, no code. On a fresh install
 * whose bootstrap never ran, the server answers `{"required":false}`; the sign-in
 * form would be wrong there — it cannot succeed on an install with no accounts, and
 * it promises a recovery ("an administrator will disable this account") that needs
 * an administrator this install does not have.
 *
 * **It offers no form, and mints nothing.** The guard is the installer's 80-bit
 * code (`internal/api/handlers_setup.go` has the survey and the decision); a
 * screen that said "create the administrator" here would turn that code into
 * decoration and hand the RADIUS server to whoever reaches the console. So the
 * honest thing is the whole of it: what is wrong, and what the person has to do
 * about it — run the installer again, which is the only thing that can open a
 * setup. The one thing this must never become is a working way in.
 *
 * **Same chunk as the form.** An install shows one or the other, once, and the
 * lazy edge is already there (`App.tsx`).
 */
export function SetupUnreadyScreen() {
  return (
    <div className="signin">
      <div className="signin-card">
        <div className="brand" style={{ padding: "0 0 1rem" }}>
          <Doorway size={18} />
          Foyer
        </div>
        <div className="card">
          <h1 className="dialog-title" style={{ marginBottom: "0.5rem" }}>
            {t("setup.unready.title")}
          </h1>
          <p style={{ marginBottom: "0.875rem" }}>{t("setup.unready.lede")}</p>
          <p style={{ marginBottom: "0.875rem" }}>{t("setup.unready.fix")}</p>
          <Notice level="warning" title={t("setup.unready.guard")}>
            {t("setup.unready.noCode")}
          </Notice>
        </div>
      </div>
    </div>
  );
}
