import { withCopy } from "./i18n/catalogue.js";
import { LanguageSwitch } from "./ui/LanguageSwitch.js";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Suspense,
  lazy,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { NavLink, Navigate, Outlet, Route, Routes, useLocation, useNavigate } from "react-router";
import type { AdminSetupState } from "@foyer/contract";
import { api, request } from "./api.js";
import {
  AuthProvider,
  PASSWORD_PATH,
  ROLE_LABEL,
  SIGN_IN_PATH,
  useAuth,
  useCan,
  usePermitted,
} from "./auth.js";
import { t } from "./i18n/index.js";
import { BINDINGS, type BindingSpec } from "./keys/bindings.js";
import { BindingKeys, Keys } from "./keys/Keys.jsx";
import { KeyboardProvider, useKeyboardSuspend, useShortcut, useShortcuts } from "./keys/registry.js";
import { ShortcutSheet } from "./keys/ShortcutSheet.jsx";
import { useNarrow } from "./layout.js";
import { SiteProvider, SiteSwitcher, useSite } from "./site.js";
import { Modal, Notice } from "./ui/primitives.js";
import {
  Clock,
  DoorBarred,
  Doorway,
  Gauge,
  IdCard,
  Key,
  Ledger,
  People,
  Router as RouterIcon,
  Server,
  Shield,
  Ticket,
  Wifi,
} from "./ui/icons.js";
import { useFocusOnNavigation } from "./screens/refocus.js";
import { AccessPointsScreen } from "./screens/AccessPoints.js";
import { AccountsScreen } from "./screens/Accounts.js";
/* Static, like `FirstRun` and for the reason recorded in `Dashboard.tsx`'s header: a
   `lazy()` edge out of a module already in the entry chunk makes Rollup hoist the
   shared dependencies into separate chunks that are still fetched first, and three
   small gzip streams compress worse than the one they replaced. */
import { CreateVenue } from "./screens/CreateVenue.js";
import { DashboardScreen } from "./screens/Dashboard.js";
import { SignInScreen } from "./screens/SignIn.js";

/**
 * The design-system gallery, at `/design`.
 *
 * **Deliberately absent from `NAV`**, and therefore from the command palette, which
 * builds itself from `NAV` plus the current screen's registered shortcuts. An
 * operator has no use for a page of swatches, and a navigation item is a promise
 * that the thing behind it is part of the job. It is a team surface: where a design
 * change is reviewed and where contrast regressions show up, reached by typing the
 * path.
 *
 * Lazily loaded for the same reason — an operator should never download it.
 */
const DesignGallery = lazy(() =>
  withCopy(() => import("./screens/Gallery.js")).then((m) => ({ default: m.DesignGallery })),
);

/**
 * The screens an operator visits rather than lives on.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The question this answers is not "what is big", it is "what does an operator
 * download before the first screen they actually look at".** The entry chunk is
 * everything a browser must fetch before the console renders anything at all, and
 * it was 143 336 B of a 143 360 B ratchet — twenty-four bytes, which means the next
 * sentence added anywhere in this console breaks the build. `tools/check-admin-budget.mjs`
 * reports lazy chunks and deliberately does not count them, precisely so that the
 * answer to a full budget is this and not a larger number.
 *
 * The daily screens are the dashboard, sessions and vouchers: a shift is spent on
 * those three. The four below are each opened for a reason and then not again —
 * `/controllers` and the registration wizard when hardware is added, `/portal` when
 * the venue's branding is set, `/attempts` and `/garden` when something is being
 * diagnosed. A visit is not a habit, and a habit is what the entry chunk is for.
 *
 * **Measured with `pnpm -F @foyer/admin build`, gzip, entry total, one stage at a
 * time** — because "split the small screens" and "split the ones nobody visits" are
 * different answers and only a measurement says which is which:
 *
 * ```
 * baseline                                       143 336 B      24 B left
 * + /portal, /attempts, /garden                  138 731 B   4 629 B left
 * + NasDetail, RegisterNasWizard                 135 350 B   8 010 B left
 * + /controllers, and these fallbacks            133 061 B  10 299 B left   ← shipped
 * ```
 *
 * The last row is the built artefact and includes what `ScreenFallback` below costs;
 * the two above it were staged with a throwaway fallback and are the shape of the
 * curve rather than shippable figures.
 *
 * **The access-points *list* stays eager, and the number is stated rather than the
 * instinct.** Splitting it as well — the whole `/access-points/*` route rather than
 * its two inner surfaces — measures 131 934 B, so it is worth **1 127 B**. It is
 * declined at that price because it is the screen an unreachable-NAS alarm sends an
 * operator to, and a split costs a loading state on the way. That is a judgement and
 * not a measurement, so it is the first thing to revisit if the budget binds again.
 *
 * `Accounts` stays eager for a different reason: it is where a staff account is
 * disabled the hour somebody leaves, which is not a screen to make anyone wait for,
 * and at 6 451 B of source it is the cheapest of the nine.
 *
 * **The copy is split the same way.** A single catalogue object would put every
 * screen's copy in the entry — Rollup drops unused *modules*, not unused *properties* —
 * so it is `i18n/core.ts` plus one namespace per lazy feature, each imported by the
 * screen that needs it and therefore travelling in that screen's chunk. That is worth
 * about 10 kB of entry (142 234 B → 131 843 B, measured the same way as the table
 * above), and the compile-time guarantee is sharper for it — a shell module reaching
 * for a screen's key is a type error.
 *
 * That decides what the budget is a gate on: `tools/check-admin-budget.mjs` binds
 * growth in the *shell's* copy, because adding a sentence to a screen costs the entry
 * nothing at all, and shell growth is the growth that should be deliberate.
 *
 * **So the two levers recorded above are the next ones**, and they are declined on
 * product grounds rather than byte grounds: the access-points list
 * (1 127 B, where an unreachable-NAS alarm sends an operator) and `/accounts`.
 */
/*
 * **`/sessions` and `/vouchers` are lazy too**, and that reverses the
 * "daily screens stay eager" line above on purpose.
 *
 * The entry had 621 B left of its 138 240 B ratchet and every new feature was being
 * rationed to 150 B of it. The two cheaper levers the budget
 * file names were spent: the remaining copy in `core.ts` was the shell's plus these two
 * screens', and a lazy *panel* inside an eager screen is the trap `Dashboard.tsx`
 * records — the dock alone, split out of `/sessions`, measured 136 010 B because
 * Rolldown carved the shell's shared modules into a second entry stream.
 *
 * What the field does settled the product question. Grafana wraps every route in
 * `SafeDynamicImport`, authentik's admin loads each route's module on navigation,
 * Keycloak's admin console is lazily routed page by page, and none of them keeps a
 * "daily" page in the entry: **the entry is the shell and the landing screen**. A
 * habit is served by the browser cache after the first visit, not by the entry. The
 * dashboard is the landing screen and stays; so do sign-in, the access-points list and
 * `/accounts`, for the reasons recorded above, which are about the first screen an
 * alarm or a departure sends somebody to.
 *
 * Each screen still reaches `PageHead` in this file, so each reaches the whole shell
 * and nothing is hoisted (`Dashboard.tsx` explains why that is the condition). Their
 * copy moved with them into `i18n/sessions.ts` and `i18n/vouchers.ts`; the titles
 * stay in core because the fallbacks below print them.
 *
 * **And both are fetched as soon as the shell has painted** (`Shell`, `preloadDaily`),
 * which is Next.js's and Gatsby's answer to the same trade: the bytes leave the first
 * screen's critical path without the first visit to a daily screen waiting on them.
 * It is also what keeps `web/e2e`'s offline case honest — a screen whose code was
 * never fetched cannot render its "not answered" state with the network cut, and an
 * operator whose Wi-Fi drops mid-shift should still reach the list and be told the
 * truth about it.
 *
 * ```
 * before                                   137 619 B      621 B left of 138 240
 * + session dock lazy, alone               136 010 B      hoisted: index + primitives
 * + /sessions and /vouchers lazy instead   125 138 B   one stream; ratchet now 131 072
 * ```
 */
const loadSessions = () => withCopy(() => import("./screens/Sessions.js"));
const loadVouchers = () => withCopy(() => import("./screens/Vouchers.js"));
const SessionsScreen = lazy(() => loadSessions().then((m) => ({ default: m.SessionsScreen })));
const VouchersScreen = lazy(() => loadVouchers().then((m) => ({ default: m.VouchersScreen })));
/** Fetch the two daily screens once the shell has painted (an effect runs after it).
    A failure is ignored: the route's own `lazy()` asks again on navigation and
    reports it there. */
function preloadDaily() {
  for (const load of [loadSessions, loadVouchers]) load().catch(() => undefined);
}
const AttemptsScreen = lazy(() =>
  withCopy(() => import("./screens/Attempts.js")).then((m) => ({ default: m.AttemptsScreen })),
);
const ControllersScreen = lazy(() =>
  withCopy(() => import("./screens/Controllers.js")).then((m) => ({ default: m.ControllersScreen })),
);
const GardenScreen = lazy(() =>
  withCopy(() => import("./screens/Garden.js")).then((m) => ({ default: m.GardenScreen })),
);
const PortalScreen = lazy(() =>
  withCopy(() => import("./screens/Portal.js")).then((m) => ({ default: m.PortalScreen })),
);
/* The first-access setup: Foyer ships no credential, so a fresh install's first
   visitor creates the administrator with the installer's one-time code. Shown in
   place of the sign-in form while the server says a setup is open, once in an
   install's life — so split, and the lazy edge is here with the others rather than
   in the eager sign-in screen, where it made Rollup hoist a shared chunk. */
const SetupScreen = lazy(() =>
  withCopy(() => import("./screens/Setup.js")).then((m) => ({ default: m.SetupScreen })),
);
/* The same chunk: an install with no administrator and no setup code. The sign-in
   form cannot succeed there, so it must not be what a stranger's first screen is. */
const SetupUnreadyScreen = lazy(() =>
  withCopy(() => import("./screens/Setup.js")).then((m) => ({ default: m.SetupUnreadyScreen })),
);

export function SignInOrSetup() {
  const { data, isPending } = useQuery({
    queryKey: ["setup"],
    queryFn: () => request<AdminSetupState>("/auth/setup", undefined, true),
    retry: false,
  });
  /* While the answer is in flight there is no screen to show. Falling through to the
     sign-in form would show a bare login on every load, including on the fresh install
     whose whole first screen is this route. A failed
     request is not that case: the console does not know, and the sign-in form is the
     only thing it can honestly offer. */
  if (data === undefined) {
    return isPending ? null : <SignInScreen />;
  }
  if (data.required) {
    return (
      <Suspense fallback={null}>
        <SetupScreen />
      </Suspense>
    );
  }
  /* `noAdministrator` is what a closed setup means on an install that has never had
     an administrator — the bootstrap that opens the code did not run, or its row was
     lost with a restore. Measured: this answer was indistinguishable from
     a finished install's, and the console met a stranger with a login form that can
     never work. It names the dead end instead, and opens no form. */
  if (data.noAdministrator) {
    return (
      <Suspense fallback={null}>
        <SetupUnreadyScreen />
      </Suspense>
    );
  }
  return <SignInScreen />;
}

/* Your own password, opened perhaps twice in an account's life. Split on the same
   rule as the four above, and stated separately because the reason is stronger: it
   is not a screen an operator visits, it is one they are sent to. */
const PasswordScreen = lazy(() =>
  withCopy(() => import("./screens/Password.js")).then((m) => ({ default: m.PasswordScreen })),
);
/* The two administrator surfaces. A staff session never opens either, so neither
   belongs in what every operator downloads before the first screen. */
const OperatorsScreen = lazy(() =>
  withCopy(() => import("./screens/Operators.js")).then((m) => ({ default: m.OperatorsScreen })),
);
const AuditScreen = lazy(() =>
  withCopy(() => import("./screens/Audit.js")).then((m) => ({ default: m.AuditScreen })),
);
const PrivacyScreen = lazy(() =>
  withCopy(() => import("./screens/Privacy.js")).then((m) => ({ default: m.PrivacyScreen })),
);
/* What a venue asks its guests for, and the consents they gave. Opened when a
   venue changes its form, which is rarely, so it is not in the entry chunk. */
/* The network screen: the portal's address and certificate, the sign-in
   limits, FreeRADIUS. Opened when the network changes, which is rarely. */
const NetworkScreen = lazy(() =>
  withCopy(() => import("./screens/Network.js")).then((m) => ({ default: m.NetworkScreen })),
);
const GuestDataScreen = lazy(() =>
  withCopy(() => import("./screens/GuestData.js")).then((m) => ({ default: m.GuestDataScreen })),
);

// Title case, because this is main navigation. In-page headings are sentence case,
// which is why this reads "Access Points" and the page itself reads "Access points".
const NAV = [
  { to: "/", get label() {
    return t("nav.dashboard");
  }, icon: Gauge, end: true, binding: BINDINGS.goDashboard },
  { to: "/sessions", get label() {
    return t("nav.sessions");
  }, icon: Wifi, end: false, binding: BINDINGS.goSessions },
  {
    to: "/controllers",
    get label() {
    return t("nav.controllers");
  },
    icon: Server,
    end: false,
    binding: BINDINGS.goControllers,
  },
  {
    to: "/access-points",
    get label() {
    return t("nav.accessPoints");
  },
    icon: RouterIcon,
    end: false,
    binding: BINDINGS.goAccessPoints,
  },
  { to: "/vouchers", get label() {
    return t("nav.vouchers");
  }, icon: Ticket, end: false, binding: BINDINGS.goVouchers },
  { to: "/accounts", get label() {
    return t("nav.accounts");
  }, icon: People, end: false, binding: BINDINGS.goAccounts },
  { to: "/portal", get label() {
    return t("nav.portal");
  }, icon: Doorway, end: false, binding: BINDINGS.goPortal },
  {
    to: "/attempts",
    get label() {
    return t("nav.attempts");
  },
    icon: DoorBarred,
    end: false,
    binding: BINDINGS.goAttempts,
  },
  { to: "/garden", get label() {
    return t("nav.garden");
  }, icon: Shield, end: false, binding: BINDINGS.goGarden },
  { to: "/guest-data", get label() {
    return t("nav.guestData");
  }, icon: People, end: false, binding: BINDINGS.goGuestData },
  /*
   * The two deployment-wide destinations, and the only two that carry a permission.
   *
   * Every other row above is *this site's* — the switcher in the header applies to
   * all nine. Operators and the audit log are the deployment's, which their own page
   * descriptions say, and they are the two an administrator has and a staff member
   * does not. A control a staff member may not use disappears rather than being
   * disabled, so these leave the sidebar, the drawer, the command palette and the
   * shortcut sheet together — those four are one list, which is what makes that a
   * single rule rather than four places to remember.
   */
  {
    to: "/operators",
    get label() {
    return t("nav.operators");
  },
    icon: IdCard,
    end: false,
    binding: BINDINGS.goOperators,
    permission: "operators:manage",
  },
  {
    to: "/audit",
    get label() {
    return t("nav.audit");
  },
    icon: Ledger,
    end: false,
    binding: BINDINGS.goAudit,
    permission: "audit:read",
  },
  /* Retention periods and erasing one guest's data. The deployment's, and an
     administrator's only, like the two above. */
  {
    to: "/privacy",
    get label() {
    return t("nav.privacy");
  },
    icon: Clock,
    end: false,
    binding: BINDINGS.goPrivacy,
    permission: "privacy:manage",
  },
  /* The deployment's network. Staff read it (`nas:read`); only an administrator's
     session is offered the writes, and the server refuses anyone else's. */
  { to: "/network", get label() {
    return t("nav.network");
  }, icon: Key, end: false, binding: BINDINGS.goNetwork, permission: "nas:read" },
] as const;

/** The destinations this operator may reach. See `usePermitted` for the `null` case. */
function useNavItems(): readonly (typeof NAV)[number][] {
  const permitted = usePermitted();
  return NAV.filter((n) => !("permission" in n) || permitted(n.permission));
}

/**
 * The route table, and the one split in it that is not about a screen.
 *
 * `/sign-in` is **outside the shell**, as a sibling rather than a child: an operator
 * who is not signed in has no site to switch, no destinations that answer anything
 * but `401`, and no use for a command palette. Everything else is a child of `Shell`,
 * which is therefore a layout route with an `<Outlet/>` — the same component instance
 * across every navigation, which is what keeps the drawer, the density preference and
 * the keyboard registry alive while the screen under them changes.
 */
export function App() {
  return (
    <AuthProvider>
      <SiteProvider>
        <KeyboardProvider>
          <Routes>
            <Route path={SIGN_IN_PATH} element={<SignInOrSetup />} />
            <Route element={<Shell />}>
              <Route path="/" element={<DashboardScreen />} />
              <Route
                path="/sessions"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback title={t("sessions.title")} description={t("sessions.description")} />
                    }
                  >
                    <SessionsScreen />
                  </Suspense>
                }
              />
              <Route
                path="/controllers"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback
                        title={t("controllers.title")}
                        description={t("controllers.description")}
                      />
                    }
                  >
                    <ControllersScreen />
                  </Suspense>
                }
              />
              <Route path="/access-points/*" element={<AccessPointsScreen />} />
              <Route
                path="/vouchers/*"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback title={t("vouchers.title")} description={t("vouchers.description")} />
                    }
                  >
                    <VouchersScreen />
                  </Suspense>
                }
              />
              <Route path="/accounts" element={<AccountsScreen />} />
              <Route
                path="/portal"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback
                        title={t("portal.title")}
                        description={t("portal.description")}
                      />
                    }
                  >
                    <PortalScreen />
                  </Suspense>
                }
              />
              <Route
                path="/attempts"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback
                        title={t("attempts.title")}
                        description={t("attempts.description")}
                      />
                    }
                  >
                    <AttemptsScreen />
                  </Suspense>
                }
              />
              <Route
                path="/garden"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback title={t("garden.title")} description={t("garden.description")} />
                    }
                  >
                    <GardenScreen />
                  </Suspense>
                }
              />
              <Route
                path="/operators"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback
                        title={t("operators.title")}
                        description={t("operators.description")}
                      />
                    }
                  >
                    <OperatorsScreen />
                  </Suspense>
                }
              />
              <Route
                path="/audit"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback title={t("audit.title")} description={t("audit.description")} />
                    }
                  >
                    <AuditScreen />
                  </Suspense>
                }
              />
              <Route
                path="/guest-data"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback title={t("guestData.title")} description={t("guestData.description")} />
                    }
                  >
                    <GuestDataScreen />
                  </Suspense>
                }
              />
              <Route
                path="/privacy"
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback title={t("privacy.title")} description={t("privacy.description")} />
                    }
                  >
                    <PrivacyScreen />
                  </Suspense>
                }
              />
              <Route
                path="/network"
                element={
                  <Suspense
                    fallback={<ScreenFallback title={t("network.title")} description={t("network.description")} />}
                  >
                    <NetworkScreen />
                  </Suspense>
                }
              />
              <Route
                path={PASSWORD_PATH}
                element={
                  <Suspense
                    fallback={
                      <ScreenFallback
                        title={t("password.title")}
                        description={t("password.description")}
                      />
                    }
                  >
                    <PasswordScreen />
                  </Suspense>
                }
              />
              <Route
                path="/design"
                element={
                  <Suspense fallback={<GalleryFallback />}>
                    <DesignGallery />
                  </Suspense>
                }
              />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </KeyboardProvider>
      </SiteProvider>
    </AuthProvider>
  );
}

function Shell() {
  const navigate = useNavigate();
  useEffect(preloadDaily, []);
  const navItems = useNavItems();
  const canOperators = useCan("operators:manage");
  const canAudit = useCan("audit:read");
  const canPrivacy = useCan("privacy:manage");
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [density, setDensity] = useState<"comfortable" | "compact">(
    () => (localStorage.getItem("foyer.density") as "comfortable" | "compact") ?? "comfortable",
  );

  /*
   * The navigation drawer.
   *
   * `drawerOpen` is deliberately derived rather than stored: a drawer is only a
   * drawer while the viewport is narrow, and deriving it means the wide layout has
   * no state that can be wrong. The sidebar's lower half is one element at every
   * width — CSS decides whether it is the rest of a column or a panel over the page.
   */
  const narrow = useNarrow();
  const [menuOpen, setMenuOpen] = useState(false);
  const drawerOpen = narrow && menuOpen;
  const menuButton = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const panelId = useId();

  const closeMenu = useCallback(() => {
    setMenuOpen(false);
    // Focus returns to the control that opened it. Optional-chained rather than
    // asserted: at full width the button is not rendered, and this same callback is
    // what every nav item calls on the way out.
    menuButton.current?.focus();
  }, []);

  // A rotation is a resize. Closed without restoring focus, because on this path the
  // button that opened the drawer has gone with the layout that had one — while the
  // nav links inside the panel are the same elements the sidebar now shows, so a
  // keyboard operator who was in the list is still in the list.
  useEffect(() => {
    if (!narrow) setMenuOpen(false);
  }, [narrow]);

  // Plain keys belong to the drawer while it owns the screen; ⌘K still reaches past it.
  useKeyboardSuspend(drawerOpen);

  /*
   * The focus trap, Escape, and the initial focus — the three things that make this
   * a dialog rather than a `div` that happens to be on top. Written here rather than
   * borrowed from `Modal`, because `<dialog>` cannot be used: this panel has to be
   * the *same element* as the sidebar's lower half at full width, and `showModal()`
   * is not something an element can stop being.
   *
   * Capture phase, so Escape is answered before the keyboard registry sees it.
   */
  useEffect(() => {
    const el = panel.current;
    if (!drawerOpen || !el) return;

    const focusable = () =>
      [...el.querySelectorAll<HTMLElement>("a[href], button:not([disabled])")].filter(
        // A hidden control is not a tab stop. The keyboard hints are `display: none`
        // under a coarse pointer, which is exactly where this drawer is used.
        (node) => node.offsetParent !== null,
      );

    focusable()[0]?.focus();

    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        closeMenu();
        return;
      }
      if (event.key !== "Tab") return;
      const items = focusable();
      const first = items[0];
      const last = items[items.length - 1];
      if (!first || !last) return;
      const edge = event.shiftKey ? first : last;
      const wrap = event.shiftKey ? last : first;
      if (document.activeElement === edge || !el.contains(document.activeElement)) {
        event.preventDefault();
        wrap.focus();
      }
    };

    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [drawerOpen, closeMenu]);

  // Density is a persisted preference, not a per-visit toggle. An operator who
  // works in a dense table wants it dense tomorrow as well.
  useEffect(() => {
    document.documentElement.dataset.density = density;
    localStorage.setItem("foyer.density", density);
  }, [density]);

  /*
   * The shell's own shortcuts go through the registry like everything else — there
   * is no window listener left in this file. Opening one overlay closes the other,
   * so ⌘K and ⌘/ never stack.
   */
  useShortcut(BINDINGS.commandPalette, () => {
    setSheetOpen(false);
    setPaletteOpen((o) => !o);
  });
  useShortcut(BINDINGS.shortcutSheet, () => {
    setPaletteOpen(false);
    setSheetOpen((o) => !o);
  });

  /*
   * A command runs *after* the palette has gone, never while it is still on top.
   *
   * The palette is a real modal `<dialog>` now, so everything behind it is inert —
   * and `.focus()` on an inert element is refused silently. *Search this list* is
   * `searchRef.current?.focus()` on the screen underneath, so running it from inside
   * the open palette moved focus nowhere at all and left the operator on `<body>`:
   * the palette gained a focus trap and lost the two commands that exist to move
   * focus out of it. Holding the command in a ref and running it from the effect that
   * observes the close is what makes the inertness free.
   *
   * It also settles the order against `Modal`'s focus restore without either side
   * knowing about the other. React runs every passive cleanup before any passive
   * effect, so the restore-to-opener has already happened by the time this fires, and
   * a command that moves focus wins — which is the right way round.
   */
  const pendingCommand = useRef<(() => void) | null>(null);
  const runCommand = useCallback((command: () => void) => {
    pendingCommand.current = command;
    setPaletteOpen(false);
  }, []);
  useEffect(() => {
    if (paletteOpen) return;
    const command = pendingCommand.current;
    pendingCommand.current = null;
    command?.();
  }, [paletteOpen]);

  useShortcut(BINDINGS.goDashboard, () => navigate("/"));
  useShortcut(BINDINGS.goSessions, () => navigate("/sessions"));
  useShortcut(BINDINGS.goAccessPoints, () => navigate("/access-points"));
  useShortcut(BINDINGS.goVouchers, () => navigate("/vouchers"));
  useShortcut(BINDINGS.goAccounts, () => navigate("/accounts"));
  useShortcut(BINDINGS.goControllers, () => navigate("/controllers"));
  useShortcut(BINDINGS.goPortal, () => navigate("/portal"));
  useShortcut(BINDINGS.goAttempts, () => navigate("/attempts"));
  useShortcut(BINDINGS.goGarden, () => navigate("/garden"));
  /*
   * The two permissioned sequences are registered only when the destination is
   * offered, which is also what keeps them out of the shortcut sheet: the sheet
   * lists what the screen has actually registered. A shortcut that navigated to a
   * screen the sidebar has hidden would be the gate leaking through the keyboard.
   */
  useShortcut(BINDINGS.goOperators, () => navigate("/operators"), canOperators);
  useShortcut(BINDINGS.goAudit, () => navigate("/audit"), canAudit);
  useShortcut(BINDINGS.goPrivacy, () => navigate("/privacy"), canPrivacy);
  useShortcut(BINDINGS.goGuestData, () => navigate("/guest-data"));
  useShortcut(BINDINGS.goNetwork, () => navigate("/network"));

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <Doorway size={18} />
          Foyer
        </div>

        {/* Wrapped so the narrow header can hand the switcher the row's spare width.
            It is the one thing in the sidebar that never moves into the drawer:
            which site you are looking at is the context every figure on the screen
            is relative to, and a figure whose site is one tap away is a figure read
            against the wrong site. */}
        <div className="sidebar-site">
          <SiteSwitcher />
        </div>

        {narrow ? (
          <button
            type="button"
            ref={menuButton}
            className="btn btn-sm menu-button"
            aria-expanded={drawerOpen}
            aria-controls={panelId}
            onClick={() => (drawerOpen ? closeMenu() : setMenuOpen(true))}
          >
            {t("shell.menu")}
          </button>
        ) : null}

        {drawerOpen ? <div className="drawer-scrim" onClick={closeMenu} /> : null}

        {/*
          One element, one navigation, at every width.

          At full width this is simply the lower two thirds of the sidebar. Below the
          breakpoint the stylesheet turns it into a panel over the page. Nothing here
          is rendered twice: a second copy of `NAV` is eighteen destinations in the
          accessibility tree, and it is how the phone's navigation and the desk
          browser's stop being the same list.
        */}
        <div
          id={panelId}
          ref={panel}
          className="sidebar-panel"
          data-open={drawerOpen}
          role={drawerOpen ? "dialog" : undefined}
          aria-modal={drawerOpen ? true : undefined}
          aria-label={drawerOpen ? t("shell.menu") : undefined}
        >
          {drawerOpen ? (
            <div className="drawer-head">
              <button type="button" className="btn btn-sm btn-ghost" onClick={closeMenu}>
                {t("shell.menuClose")}
              </button>
            </div>
          ) : null}

          <nav className="nav">
            {navItems.map(({ to, label, icon: Icon, end, binding }) => (
              <NavLink key={to} to={to} end={end} className="nav-item" onClick={closeMenu}>
                <Icon size={15} />
                {label}
                {/* The navigation sequence, printed on the thing it navigates to. */}
                <span className="nav-key">
                  <Keys sequences={binding.sequences} />
                </span>
              </NavLink>
            ))}
          </nav>

          <div className="sidebar-foot">
            <AccountArea onNavigate={closeMenu} />
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              style={{ justifyContent: "flex-start" }}
              onClick={() => setDensity((d) => (d === "compact" ? "comfortable" : "compact"))}
            >
              {density === "compact"
                ? t("shell.density.toComfortable")
                : t("shell.density.toCompact")}
            </button>
            <LanguageSwitch />
            {/* Every shortcut must be discoverable from visible UI. A shortcut that
                exists only in a keydown handler is a shortcut nobody uses — so these
                are drawn from the same declaration the matcher reads. They are the
                one thing in here that hides on a touch device, and on input
                capability rather than on width: see the `(hover: none)` block. */}
            <button
              type="button"
              className="kbd-hint kbd-hint-button"
              onClick={() => {
                setSheetOpen(false);
                setPaletteOpen(true);
              }}
            >
              <BindingKeys spec={BINDINGS.commandPalette} />
              <span>{t("shell.commands")}</span>
            </button>
            <button
              type="button"
              className="kbd-hint kbd-hint-button"
              onClick={() => {
                setPaletteOpen(false);
                setSheetOpen(true);
              }}
            >
              <BindingKeys spec={BINDINGS.shortcutSheet} />
              <span>{t("keys.shortcuts")}</span>
            </button>
          </div>
        </div>
      </aside>

      <main className="main">
        <SiteGate>
          <Outlet />
        </SiteGate>
      </main>

      {paletteOpen ? (
        <CommandPalette
          onClose={() => setPaletteOpen(false)}
          onRun={runCommand}
          density={density}
          onToggleDensity={() => setDensity((d) => (d === "compact" ? "comfortable" : "compact"))}
        />
      ) : null}
      {sheetOpen ? <ShortcutSheet onClose={() => setSheetOpen(false)} /> : null}
    </div>
  );
}

/**
 * Whose console this is, and how to stop being them.
 *
 * A line of text and ordinary controls rather than an avatar with a menu behind it:
 * the sidebar foot is already a column of controls, a menu would be a third overlay
 * pattern in a console that has two, and *who am I signed in as* is a question worth
 * answering without a click on a machine several people share.
 *
 * **The signed-out branch is not dead code.** `web/mock` requires a session for
 * nothing yet, so this is the state the console boots into today and the only way to
 * reach the sign-in screen deliberately rather than by being refused.
 */
function AccountArea({ onNavigate }: { onNavigate: () => void }) {
  const { session } = useAuth();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const signOut = useMutation({
    mutationFn: () => api.signOut(),
    /* `204` whether or not there was a session to end, so there is no failure branch
       worth a different treatment — and if the request never arrives, the cache is
       cleared anyway. Leaving a console showing an ex-operator's data because their
       sign-out did not reach the server is the worse of the two outcomes. */
    onSettled: () => {
      navigate(SIGN_IN_PATH, { replace: true });
      /* Everything in the cache was fetched as this operator. */
      qc.clear();
    },
  });

  if (!session) {
    return (
      <NavLink to={SIGN_IN_PATH} className="btn btn-sm btn-ghost" onClick={onNavigate}>
        {t("signIn.title")}
      </NavLink>
    );
  }

  return (
    <>
      <p className="sidebar-who">
        <strong>{session.operator.displayName ?? session.operator.username}</strong>
        {ROLE_LABEL[session.operator.role]}
      </p>
      <NavLink
        to={PASSWORD_PATH}
        className="btn btn-sm btn-ghost"
        style={{ justifyContent: "flex-start" }}
        onClick={onNavigate}
      >
        {t("password.title")}
      </NavLink>
      {/* `aria-disabled`: the operator pressed this, so it has focus. */}
      <button
        type="button"
        className="btn btn-sm btn-ghost"
        style={{ justifyContent: "flex-start" }}
        aria-disabled={signOut.isPending || undefined}
        onClick={() => {
          if (signOut.isPending) return;
          signOut.mutate();
        }}
      >
        {t("signOut")}
      </button>
    </>
  );
}

/**
 * What a route-split screen shows while its chunk is in flight.
 *
 * **The heading is real, and only the body is a skeleton.** A screen's title and
 * description are catalogue strings — they are known without the chunk that renders
 * them — so a skeleton in their place would be pretending not to know something the
 * entry bundle already contains. The operator sees the destination they asked for
 * immediately, and the part that genuinely is not here yet, the rows, is the part
 * drawn as skeletons. Never a spinner: the same rule `ui/table.tsx` states for a
 * loading table, for the same reason.
 *
 * **It is deliberately not `PageHead`, and that is measured rather than reasoned
 * about.** `useFocusOnNavigation` fires on a `PageHead` *mounting* — see
 * `screens/refocus.js`, where a route change is defined as exactly that and nothing
 * else. A fallback built from `PageHead` therefore *consumes* the navigation, and the
 * screen's own heading then sees `previous === pathname` and does nothing. Driven from
 * the keyboard with the `/portal` chunk held for 1 200 ms, `document.activeElement`
 * read during the fetch and once it arrived:
 *
 * ```
 * this markup       during: a.nav-item "Portal"   after: h1.page-title "Portal"
 * with PageHead     during: h1.page-title         after: <body>
 * ```
 *
 * The second row is the exact defect `refocus.ts` exists to fix, reintroduced by a
 * loading state. Plain markup here means the mount that counts is still the screen's
 * own, and the first row is what the operator gets: focus stays where they left it
 * until the page they asked for is really there.
 *
 * The skeleton block is `aria-hidden`: there is nothing in it to announce, and the
 * announcement that matters happens once, when focus reaches the real heading.
 */
/**
 * The four routes that are the deployment's rather than a venue's.
 *
 * `NAV` already says so about two of them — *"Every other row above is this site's;
 * operators and the audit log are the deployment's"* — and the reason it matters here
 * is narrow: when the address names a venue this deployment does not have, the screens
 * that cannot answer are the ones that would have asked about it. Your own password
 * and the swatch gallery are neither, and blocking them would read that claim wider
 * than it is.
 */
const DEPLOYMENT_WIDE: readonly string[] = ["/operators", "/audit", "/privacy", "/network", PASSWORD_PATH, "/design"];

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  A VENUE-SCOPED SCREEN DOES NOT RENDER BEFORE THERE IS A VENUE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Every screen under this outlet asks the server a question with a `siteId` in it.
 * Before this gate the id was always present — `useState("site-hcm")` — so there was no
 * moment at which one was missing and therefore no state to render. That was the
 * defect rather than a simplification: the id came from `web/fixtures`, so a migrated
 * first-booted deployment asked about a venue that does not exist, and `api.ts`'s `qs`
 * drops an empty value, so answering *"we do not know yet"* with `""` would have the
 * console asking `/sessions` with **no** site scope and the mock silently defaulting
 * to `site-hcm` again — the same literal, one layer down.
 *
 * So there are four not-ready states and each renders its own sentence:
 *
 *  - **resolving** — a document load whose address names no venue, waiting on
 *    `listSites`. One round trip to the origin that served the page. No `PageHead`
 *    here on purpose: `useFocusOnNavigation` keys on a heading *mounting* under a
 *    pathname, so a heading in this state would consume the route change and the
 *    screen's own heading would arrive to `previous === pathname` and never focus.
 *  - **none** — the deployment has no venue. A first boot. This is a real state that
 *    nothing in this console could render before, because the seed always matched.
 *  - **unknown** — the address names one that is not here: a bookmark that outlived a
 *    rename or a removal, which is the ordinary case rather than the exotic one. It
 *    names the id in text and offers the venues that do exist, because a silent
 *    redirect to the first one is how an operator ends up acting on another venue's
 *    data believing it is theirs.
 *
 * **`failed` is deliberately not one of them, and it was, for one run.** When
 * `listSites` cannot be fetched this gate rendered *"Cannot load sites"* over the
 * screen — and turned two existing locks red, correctly: `absence-is-not-zero`'s
 * *`/garden` states the verdict it cannot reach* and `wide-table-says-what-is-hidden`'s
 * *with the backend down `/sessions` fits* both cut the transport and require the screen
 * to **state its own failure**. That is this console's architecture — a failure is
 * stated by the surface that has it, per query, never by a shell that swallows the
 * surface — and a whole-page substitute is a second, contradictory answer to the same
 * outage.
 *
 * So an unreachable venue list renders the screens, with an empty `siteId`, and every
 * query on them fails the same way and says so; the switcher states the venue list's
 * own failure in place, which is where it belongs. The residue is a partial outage —
 * `/sites` failing while `/sessions` answers — where a list would be fetched with no
 * venue scope. It is named rather than hidden, and it is strictly better than what it
 * replaced, which asked about a venue id from `web/fixtures`.
 */
/*
 * Exported for `screens/create-venue.test.tsx`, which is the point rather than a
 * convenience: what that file has to prove is that the create-venue control is
 * **reachable on a deployment with no venue**, and rendering `CreateVenue` directly
 * would prove only that a component renders. The gate is what decides whether an
 * operator ever sees it, so the gate is what the test drives.
 */
export function SiteGate({ children }: { children: ReactNode }) {
  const { status, siteId, sites, setSiteId } = useSite();
  const { pathname } = useLocation();

  if (status === "ready" || status === "failed" || DEPLOYMENT_WIDE.includes(pathname)) {
    return <>{children}</>;
  }

  if (status === "resolving") {
    return (
      <div className="page">
        <span className="skeleton" style={{ width: "12rem", height: "1.2em" }} />
      </div>
    );
  }

  if (status === "none") {
    return (
      <div className="page">
        <PageHead title={t("site.gate.noneTitle")} description={t("site.gate.noneBody")} />
        {/*
         * Stating the situation and offering nothing to do about it would be a worse
         * place to stop than confusion: the first run would dead-end at step one with a
         * `curl` as the only way past it.
         *
         * It goes **here** rather than in `FirstRun` because this is the one branch
         * that renders when there is no venue: every step `FirstRun` measures asks the
         * server a question with a `siteId` in it, and this gate exists precisely
         * because there is no id to ask with. `CreateVenue`'s header carries the whole
         * of how the two first-run surfaces relate.
         */}
        <CreateVenue />
      </div>
    );
  }

  return (
    <div className="page">
      <PageHead title={t("site.gate.unknownTitle")} description={t("site.gate.unknownBody")} />
      <Notice level="warning" title={t("site.gate.unknownNamed", siteId)} role="alert">
        <div style={{ display: "flex", flexWrap: "wrap", gap: "0.5rem" }}>
          {sites.map((s) => (
            <button key={s.id} type="button" className="btn btn-sm" onClick={() => setSiteId(s.id)}>
              {s.name}
            </button>
          ))}
        </div>
      </Notice>
    </div>
  );
}

function ScreenFallback({ title, description }: { title: string; description: string }) {
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">{title}</h1>
          <p className="page-desc">{description}</p>
        </div>
      </div>
      <div className="card" aria-hidden="true">
        {[70, 100, 85, 100, 60].map((width, i) => (
          <span
            key={i}
            className="skeleton"
            style={{ width: `${width}%`, marginBlockEnd: i === 4 ? 0 : "1.1rem" }}
          />
        ))}
      </div>
    </div>
  );
}

/* The gallery's own loading state, in the language the gallery is about: skeletons,
   never a spinner. It is on screen for one chunk fetch on a local dev server. */
function GalleryFallback() {
  return (
    <div className="page">
      <span className="skeleton" style={{ width: "12rem", height: "1.2em" }} />
    </div>
  );
}

/* ── command palette ──────────────────────────────────────────────────────── */

interface Command {
  id: string;
  label: string;
  hint?: string;
  /** The binding that does the same thing, so the palette teaches the shortcut. */
  binding?: BindingSpec;
  run: () => void;
}

/**
 * The palette, which spent its whole life not being an overlay.
 *
 * It was a `div.palette-backdrop` with a scrim painted on it, and every consequence
 * was driven rather than deduced: `Tab` from the filter walked all twenty commands
 * and then straight out into the sidebar *behind* the scrim; one `Shift+Tab` left it
 * entirely; a control in `main` could still be focused programmatically; and `Escape`
 * answered only while the input had focus, because the handler was the input's own
 * `onKeyDown` and the registry had been deliberately silenced for plain keys. ⌘K was
 * the only way out from anywhere else.
 *
 * All four are one missing mechanism, so all four are one fix: it goes through
 * `Modal` like the console's nine other dialogs. The trap, the inert document and the
 * return of focus are then the platform's, and they are the same platform's on every
 * overlay here rather than a second hand-written trap that agrees with the first
 * until someone edits one of them.
 *
 * The ARIA is the standard combobox-with-listbox relationship, not a new one. What
 * was there was `data-active`, which is a *colour*: the highlight moved on every
 * arrow key and a screen reader was told nothing at all about which command was
 * selected. `aria-activedescendant` is what makes the highlight a fact rather than a
 * shade, and it is also why the options carry `tabIndex={-1}` — in this pattern focus
 * stays in the field and the arrow keys move the selection, which is what stops `Tab`
 * from walking twenty commands one at a time.
 */
function CommandPalette({
  onClose,
  onRun,
  onToggleDensity,
  density,
}: {
  onClose: () => void;
  /** Hand the command up, to be run once the palette is off the screen. */
  onRun: (command: () => void) => void;
  onToggleDensity: () => void;
  density: "comfortable" | "compact";
}) {
  const navigate = useNavigate();
  const { siteId, sites, setSiteId } = useSite();
  const registrations = useShortcuts();
  /* The same list the sidebar draws. A palette that offered a destination the
     sidebar has hidden would be a second, contradictory answer to the same gate. */
  const navItems = useNavItems();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const listId = useId();
  const optionId = (index: number) => `${listId}-${index}`;

  /* The highlighted command is kept on screen. `aria-activedescendant` is a claim
     that one option is selected, and a selected option below the fold is half a
     claim — the list scrolls at twenty commands on a laptop. `nearest`, so a command
     already visible does not move under the operator. */
  useEffect(() => {
    document.getElementById(`${listId}-${active}`)?.scrollIntoView({ block: "nearest" });
  }, [active, listId, query]);

  const commands: Command[] = [
    ...navItems.map((n) => ({
      id: `go-${n.to}`,
      label: t("palette.goTo", n.label),
      hint: t("palette.navigate"),
      binding: n.binding as BindingSpec,
      run: () => navigate(n.to),
    })),
    // The site switcher is registered here too, because a control that exists only
    // in the sidebar is a control a keyboard user has to reach for the mouse to use.
    //
    // The site you are already on is **not** offered. "Switch to ⟨the site you are
    // looking at⟩" is a command that does nothing, on a console whose stated rule is
    // to remove controls rather than disable them — and in a palette, where the
    // first row is one Enter away, an inert entry is worse than a disabled button.
    ...sites
      .filter((s) => s.id !== siteId)
      .map((s) => ({
        id: `site-${s.id}`,
        label: t("palette.switchSite", s.name),
        hint: s.environment === "live" ? t("site.env.live") : t("site.env.lab"),
        run: () => setSiteId(s.id),
      })),
    // A real action rather than another way to navigate. Density is a persisted
    // preference with no shortcut, so before this the only way to reach it was the
    // mouse — which is exactly the gap a command palette exists to close.
    {
      id: "density",
      label: density === "compact" ? t("shell.density.toComfortable") : t("shell.density.toCompact"),
      hint: t("palette.action"),
      run: onToggleDensity,
    },
    // Anything the current screen registers is reachable here too, so the palette
    // and the shortcut sheet describe one set of actions rather than two.
    ...registrations
      .filter((r) => r.enabled && r.spec.group !== "navigation" && r.spec.id !== "command-palette")
      .map((r) => ({
        id: `key-${r.spec.id}`,
        label: r.spec.label,
        binding: r.spec,
        run: r.run,
      })),
  ];

  const filtered = commands.filter((c) => c.label.toLowerCase().includes(query.toLowerCase()));

  const run = useCallback(
    (c: Command | undefined) => {
      if (c) onRun(c.run);
    },
    [onRun],
  );

  return (
    <Modal className="palette" label={t("palette.title")} dismissOnBackdrop onClose={onClose}>
      <div className="palette-card">
        <input
          autoFocus
          role="combobox"
          aria-expanded
          aria-controls={listId}
          aria-autocomplete="list"
          /* Undefined rather than a dangling id when nothing matches: an
             `aria-activedescendant` pointing at no element is a claim that there is a
             selected option, made at exactly the moment there is not. */
          aria-activedescendant={filtered.length ? optionId(active) : undefined}
          aria-label={t("palette.placeholder")}
          /* The staged Escape is `Modal`'s to arbitrate — it sees the press first,
             from the dialog element, where React's delegated handler below cannot.
             This declares the field wants the first press; the handler still does the
             clearing, because only this component owns the query. */
          data-escape-clears=""
          placeholder={t("palette.placeholder")}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
          onKeyDown={(e) => {
            // Clear first, keep focus, and let the second press — on an empty field —
            // close the palette. Focus and escape-to-blur are always a pair.
            if (e.key === "Escape" && query) setQuery("");
            // Claimed from the user agent: an arrow key in a text field moves the
            // caret, and the caret is not what these keys mean here.
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setActive((a) => Math.min(a + 1, filtered.length - 1));
            }
            if (e.key === "ArrowUp") {
              e.preventDefault();
              setActive((a) => Math.max(a - 1, 0));
            }
            if (e.key === "Enter") run(filtered[active]);
          }}
        />
        {/* The empty message is a sibling of the listbox, not a child of it. A
            `role="listbox"` may contain options and nothing else, so putting the
            sentence inside is an accessibility-tree violation on the one state where
            there is nothing else to read. */}
        {/* `tabIndex={-1}` because Chromium makes any scrolling container a tab stop
            of its own, and this one scrolls at twenty commands. Without it, `Tab` from
            the field lands on the list *box* — a stop with no behaviour, where the
            arrow keys that move the selection are the field's and so do nothing.
            The scroll region stays reachable by the keys that matter, because the
            highlighted option is scrolled into view as it moves. */}
        <div
          className="palette-list"
          id={listId}
          role="listbox"
          tabIndex={-1}
          aria-label={t("palette.title")}
        >
          {filtered.map((c, i) => (
            <button
              key={c.id}
              type="button"
              id={optionId(i)}
              role="option"
              aria-selected={i === active}
              tabIndex={-1}
              className="popover-item"
              data-active={i === active}
              onMouseEnter={() => setActive(i)}
              onClick={() => run(c)}
            >
              <span>{c.label}</span>
              {c.hint ? <span className="popover-item-sub">{c.hint}</span> : null}
              {c.binding ? (
                <span className="popover-item-keys">
                  <BindingKeys spec={c.binding} />
                </span>
              ) : null}
            </button>
          ))}
        </div>
        {filtered.length === 0 ? (
          <div style={{ padding: "1rem", color: "var(--text-muted)", fontSize: "0.8125rem" }}>
            {t("palette.none")}
          </div>
        ) : null}
      </div>
    </Modal>
  );
}

/**
 * Shared page header — and the console's one piece of route-change focus management,
 * because it is the one component every screen has.
 *
 * The heading takes focus when this header mounts under a pathname different from
 * the one the last header mounted under. `useFocusOnNavigation` carries the rule and
 * the measurements behind it, including why *mount* rather than *pathname changed*
 * is the signal: `/access-points/new` and `/sessions?session=…` move the URL without
 * moving the page, and a comparison would steal focus from the wizard and from every
 * arrow key.
 *
 * `tabIndex={-1}` makes the heading focusable programmatically without adding a tab
 * stop, which is also what gets it announced. There is no live region beside it: see
 * the hook.
 */
export function PageHead({
  title,
  description,
  actions,
}: {
  title: string;
  /** A node, not a string: venue-authored text inside it needs its own `lang`. */
  description?: React.ReactNode;
  actions?: React.ReactNode;
}) {
  const heading = useFocusOnNavigation(useLocation().pathname);
  return (
    <div className="page-head">
      <div>
        <h1 className="page-title" tabIndex={-1} ref={heading}>
          {title}
        </h1>
        {description ? <p className="page-desc">{description}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </div>
  );
}

/** The alarm strip, shared by the dashboard and any screen that wants it. */
export function useAlarms() {
  const { siteId } = useSite();
  return useQuery({ queryKey: ["alarms", siteId], queryFn: () => api.alarms(siteId) });
}
