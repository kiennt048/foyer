import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useMemo, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import {
  can,
  type AuthSession,
  type OperatorPermission,
  type OperatorRole,
} from "@foyer/contract";
import { api, setCsrfToken, whenSignedOut } from "./api.js";
import { t } from "./i18n/index.js";

/**
 * Who is signed in, and what the console is allowed to offer them.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The gate is `AuthSession.permissions` and nothing else.** Not
 * `session.operator.role`, and not `ROLE_PERMISSIONS` — that table is the *server's*
 * v1 derivation and lives in `@foyer/contract` for the same reason `DRIVER_SHAPES`
 * does. A console that gated on the derivation keeps gating on it after a deployment
 * scopes a role per site or adds a third one, and the drift presents as a control
 * that renders and then `403`s, which is the exact defect capability gating exists to
 * prevent. The role is a *label* here: it is rendered on the operators screen because
 * an administrator assigns it, and it decides nothing.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **With no session at all, nothing is gated, and that is the honest answer rather
 * than the convenient one.**
 *
 * A permission gate answers *may this operator do this*. Where there is no operator
 * there is no such question, and answering it "no" would be the console inventing a
 * refusal the server has not made — on a deployment where the server is not asking
 * for a session, that hides the entire product from everyone.
 *
 * It is also exactly where the server's own gate is: `web/mock` refuses on role
 * *whenever a session exists* and requires one for nothing yet (`REQUIRE_SESSION`).
 * So the console gates precisely where the server gates, which is the property that
 * makes throwing that switch a one-line change rather than a second migration. Under
 * enforcement a signed-out console never reaches a gated control anyway: the screen's
 * own query answers `401` and `whenSignedOut` routes to the sign-in form.
 *
 * The window where this is visible is the boot probe — a cold load renders ungated
 * for one request. A later end-to-end task should lock that a *signed-in staff*
 * console settles with the administrator-only controls absent; there is no state in
 * which it settles ungated with a session.
 */
export const SIGN_IN_PATH = "/sign-in";
/** Your own password, which is the only one this product has an operation for. */
export const PASSWORD_PATH = "/password";

/**
 * The role, as a word an operator reads. **It is a label and it gates nothing.**
 *
 * A `Record<OperatorRole, …>` over the contract's enum, so a third role added to the
 * contract fails to build here rather than rendering as a raw identifier. That is the
 * only property this table has, and it is deliberately not the shape of
 * `ROLE_PERMISSIONS` — a client-side map from role to *what you may do* is precisely
 * the drift `useCan` exists to avoid.
 */
export const ROLE_LABEL: Record<OperatorRole, string> = {
  get administrator() {
    return t("operator.role.administrator");
  },
  get staff() {
    return t("operator.role.staff");
  },
};

/** The one query key for the session, so a sign-in can seed it and a sign-out clear it. */
export const AUTH_KEY = ["auth"] as const;

interface AuthState {
  /** The signed-in operator and their permissions, or `null` for nobody. */
  session: AuthSession | null;
  /** True until the boot probe has answered. Nothing is known either way yet. */
  isLoading: boolean;
}

const Ctx = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const navigate = useNavigate();

  /*
   * The boot probe. `retry: false` because `api.me` has already turned the one
   * expected refusal into `null`: anything left is a real failure, and retrying it
   * delays the sign-in screen an operator is waiting for.
   */
  const { data, isLoading } = useQuery({
    queryKey: AUTH_KEY,
    queryFn: api.me,
    retry: false,
    staleTime: 60_000,
  });
  const session = data ?? null;

  /* The token travels with the session, so it is set from the same place. */
  useEffect(() => setCsrfToken(session?.csrfToken ?? null), [session]);

  /*
   * Route on the refusal, rather than render it.
   *
   * `replace`, because the page that just answered `401` is not a page to go back
   * to — the browser's Back would re-issue the request that failed. The cached
   * session is dropped first so the console's own idea of who is signed in cannot
   * outlive the server's.
   */
  useEffect(
    () =>
      whenSignedOut(() => {
        qc.setQueryData(AUTH_KEY, null);
        /*
         * Read from the browser rather than from `useLocation`, because this closure
         * outlives the render that made it: a refusal arrives whenever a request
         * happens to finish, and the URL at that moment is the one the operator is
         * actually looking at. Depending on the location instead would re-register
         * this handler on every navigation to keep one string fresh.
         */
        const { pathname, search } = window.location;
        if (pathname === SIGN_IN_PATH) return;
        navigate(SIGN_IN_PATH, {
          replace: true,
          /* Why they are here, and where they were going. A form that says nothing
             about either leaves an operator wondering what happened to their page. */
          state: { signedOut: true, from: `${pathname}${search}` },
        });
      }),
    [qc, navigate],
  );

  /*
   * The forced password change, routed here and ENFORCED ON THE SERVER.
   *
   * An operator on a temporary password somebody else set (`foyerctl create-operator
   * --must-change-password`) chooses their own first. (No install ships a password —
   * a fresh install shows the first-access setup instead, `screens/Setup.tsx`.) This
   * effect is the pleasant half — an
   * operator lands on the screen that clears it instead of watching every panel
   * answer `403`. It is deliberately **not** the mechanism: `internal/api`'s gate and
   * `web/mock`'s gate both refuse every operation but this one, so a second browser,
   * a stale tab or `curl` with the session cookie gets nowhere. A forced change that
   * lived only here would be a redirect in front of an open door.
   *
   * `replace`, for the same reason the signed-out redirect uses it: the page that is
   * about to be refused is not a page to go back to.
   *
   * No `from` in the state. The signed-out redirect carries one because signing in
   * again resumes what the operator was doing; this one does not, because the
   * password change is the beginning of the session rather than an interruption to
   * it, and offering to return to a dashboard nobody had reached yet is an offer
   * about a page that does not exist for them.
   */
  const mustChangePassword = session?.mustChangePassword ?? false;
  /* `useLocation` here and `window.location` in the signed-out effect above, and the
     difference is not an inconsistency: that one runs inside a closure registered
     once, which outlives the render that made it, so it has to read the URL at the
     moment a refusal arrives. This one re-runs on every render, so the router's own
     location is both fresher and the thing that makes navigating away pull the
     operator back. */
  const location = useLocation();
  useEffect(() => {
    if (!mustChangePassword) return;
    if (location.pathname === PASSWORD_PATH) return;
    navigate(PASSWORD_PATH, { replace: true, state: { forced: true } });
  }, [mustChangePassword, navigate, location.pathname]);

  /*
   * Memoised, and it is necessary rather than a tidy-up.
   *
   * The forced-password-change effect above reads `useLocation`, so this provider
   * now re-renders on **every navigation** — where before it re-rendered only when
   * the session query settled. A fresh object literal here would hand every
   * `useAuth()` consumer a new context value on each of those renders, which is a
   * re-render of every gated control in the console for a value that did not change.
   *
   * The alternative was to drop `useLocation` and read `window.location` inside the
   * effect, as the signed-out handler above does. That one has a reason this one does
   * not: it runs inside a closure registered once, which outlives the render that
   * made it. This effect wants to re-run on navigation — that is what pulls an
   * operator back when they try to leave the screen they are being held on.
   *
   * **Measured, because "probably faster" is not a reason to write anything.** Same
   * machine, back to back, `dialog-focus-return` and `dialogs-are-named`:
   *
   *     with this useMemo      15 passed   2.4 min
   *     without it             13 passed   5.4 min
   *
   * So it is not a tidy-up. `useLocation` above makes this provider re-render on every
   * navigation, and this is what makes that safe: two focus locks go red without it,
   * which is the console losing track of what an operator was on.
   */
  const value = useMemo(() => ({ session, isLoading }), [session, isLoading]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth(): AuthState {
  const value = useContext(Ctx);
  if (!value) throw new Error("useAuth outside AuthProvider");
  return value;
}

/**
 * Whether to offer this control. **The only role gate in the console.**
 *
 * `can()` is the contract's own reader over `AuthSession.permissions`; the `true`
 * for a missing session is argued at the top of this file.
 */
export function useCan(permission: OperatorPermission): boolean {
  return usePermitted()(permission);
}

/**
 * The same gate as a function, for a *list* of controls rather than one.
 *
 * The navigation is the case that needs it: eleven destinations, two of them
 * permissioned, and a `useCan` per row would be eleven hooks whose count depends on
 * an array. One hook, one predicate, same answer.
 */
export function usePermitted(): (permission: OperatorPermission) => boolean {
  const { session } = useAuth();
  return (permission) => (session ? can(session, permission) : true);
}
