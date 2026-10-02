import { useQuery } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useLocation, useNavigationType, useSearchParams } from "react-router";
import type { Site } from "@foyer/contract";
import { api } from "./api.js";
import { SIGN_IN_PATH } from "./auth.js";
import { t } from "./i18n/index.js";
import { useKeyboardSuspend } from "./keys/registry.js";
import { ChevronDown } from "./ui/icons.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  WHICH VENUE THE OPERATOR IS LOOKING AT LIVES IN THE ADDRESS
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Not `useState("site-hcm")`, because both halves of that are wrong.
 *
 *  1. **Component state does not survive a document load.** Reload, back button, a
 *     bookmark, a link pasted to a colleague — every one of them would land on the
 *     first site rather than the one the operator was on. A support call that says
 *     *"look at this session"* could not be answered with a URL, and a test would have
 *     to re-select the venue after every `goto` to avoid registering a device into
 *     the wrong one.
 *  2. **A seed is a fixture id.** `site-hcm` exists in `web/fixtures` and in nothing
 *     else, so on a migrated, first-booted deployment — `FOYER_MOCK_FRESH=1`, or a
 *     real install — the console would open on a venue that does not exist and ask
 *     the server about it, on every screen, before it had asked which venues there
 *     are.
 *
 * **The venue is a `?site=` query parameter**, and that is the console's own existing
 * decision rather than a new one: `SessionsScreen` says *"Filters live in the URL. A
 * filtered view is a link an operator can send to a colleague, and the back button
 * means what it looks like it means."* The venue is the outermost of those filters —
 * every figure on every screen is relative to it — so it belongs in the address too.
 *
 * A path segment was the other candidate, because `docs/rules/frontend.md` records
 * the portal choosing one for language. The property that rule is about is *"in the
 * URL, not in a cookie"*, which either form satisfies; what decides between them is
 * that this console already spells view scope as a query and that a prefix would move
 * all nine routes, which is what the named E2E locks are pinned to.
 *
 * ── THE PARAMETER IS PRESENT EXACTLY WHEN IT SAYS SOMETHING ──────────────────
 *
 * Absent means *the deployment's venue* — `listSites()[0]`, which the server orders by
 * name — and that is a fact asked of the server rather than a literal typed here. The
 * address is canonicalised to that form: choosing a different venue writes the
 * parameter, choosing the first one removes it again.
 *
 * The alternative — stamping `?site=…` onto every URL — was rejected on the product's
 * own shape. `createSite` refuses the second venue (*one venue per install*,
 * `web/contract/openapi.yaml`), so on a shipped deployment that parameter
 * would be a constant, in every address, disambiguating something that cannot be
 * ambiguous. It is the same rule `?state=` follows on `/sessions`: the URL carries
 * what is not the default.
 *
 * ── AN IN-APP LINK THAT SAYS NOTHING ABOUT THE VENUE DOES NOT CHANGE IT ──────
 *
 * There are thirty-one internal link targets in this console and they are spread over
 * eight files, one of which is a driver-detail screen nobody thinks about when adding
 * a venue-scoped feature. Requiring each of them to carry `?site=` would be
 * `docs/rules/testing.md`'s *derive the stand-in from what it stands in for*
 * inverted — thirty-one copies of one rule, where the first copy anybody forgets
 * moves the operator to another venue's data without saying so.
 *
 * So the provider holds the answer instead: a `PUSH` or `REPLACE` to an address with
 * no `site` keeps the venue and rewrites the address to say so. **A `POP` does not** —
 * back and forward are the operator navigating, so the address they are returning to
 * is authoritative including its silence, which is what makes *switch venue, then
 * Back* undo the switch rather than being swallowed.
 */
export const SITE_PARAM = "site";

/**
 * What the address, the server and the operator's choice add up to.
 *
 * `ready` is the only one a venue-scoped screen may render under, and the four others
 * are each a different sentence — which is the point. The state this replaced said
 * *"Cannot load sites"* for a deployment that simply has none, and rendered every
 * screen against a venue id from the fixtures.
 */
export type SiteStatus =
  /** The address names no venue and the list has not arrived. Nothing can be asked yet. */
  | "resolving"
  /** A venue is settled. `siteId` is safe to put in a query. */
  | "ready"
  /** The address names a venue this deployment does not have — a renamed or removed one. */
  | "unknown"
  /** The deployment has no venue at all. A first boot, before `createSite`. */
  | "none"
  /** The venue list could not be fetched and the address named nothing to fall back on. */
  | "failed";

export interface SiteResolution {
  status: SiteStatus;
  /** `""` unless the status is `ready` or `unknown`. Never a literal from the fixtures. */
  siteId: string;
  site: Site | null;
}

const NO_SITES: readonly Site[] = [];

/**
 * The whole of the decision, as a function, so the cases that are hard to reach in a
 * browser are cheap to pin: `admin/src/site.test.tsx` drives all five.
 *
 * `held` is what this browser last resolved, and it is **not** a second source of
 * truth — its value came from the address in the first place, and the caller passes
 * `""` for it on a `POP`. See the header.
 */
export function resolveSite(input: {
  /** What the address says, `""` for nothing. */
  named: string;
  /** The venue this browser is already on, `""` on a fresh load or a `POP`. */
  held: string;
  sites: readonly Site[];
  isLoading: boolean;
  /** The venue list could not be fetched **and** there is no earlier answer to use. */
  failed: boolean;
}): SiteResolution {
  const { named, held, sites, isLoading, failed } = input;
  const settled = !isLoading && !failed;

  /* Asked first, because it outranks everything the address can say: on a deployment
     with no venue, a `?site=` is not an unknown venue, it is a deployment that has not
     been set up. Those are different sentences and only one of them is actionable. */
  if (settled && sites.length === 0) return { status: "none", siteId: "", site: null };

  const wanted = named || held;
  if (wanted) {
    const site = sites.find((s) => s.id === wanted) ?? null;
    if (site) return { status: "ready", siteId: wanted, site };
    /* Named, but the list is still in flight or did not arrive. The address is the
       fact; the list is what is missing. Screens ask about it rather than waiting,
       which is what makes a reload of a shared link as fast as it was before. */
    if (!settled) return { status: "ready", siteId: wanted, site: null };
    return { status: "unknown", siteId: wanted, site: null };
  }

  if (failed) return { status: "failed", siteId: "", site: null };
  if (isLoading) return { status: "resolving", siteId: "", site: null };
  /* The deployment's venue. `listSites` is `ORDER BY name` on the Go side and the mock
     inserts in name order to match, so this is one answer and not two. */
  const first = sites[0]!;
  return { status: "ready", siteId: first.id, site: first };
}

interface SiteState extends SiteResolution {
  sites: readonly Site[];
  setSiteId: (id: string) => void;
  isLoading: boolean;
  error: Error | null;
}

const Ctx = createContext<SiteState | null>(null);

export function SiteProvider({ children }: { children: ReactNode }) {
  const [params, setParams] = useSearchParams();
  const navigationType = useNavigationType();
  const { pathname } = useLocation();
  const { data, isLoading, error } = useQuery({ queryKey: ["sites"], queryFn: api.sites });
  /* A stable identity for the empty case: this array reaches effect dependency lists. */
  const sites = data ?? NO_SITES;
  const named = params.get(SITE_PARAM) ?? "";

  /*
   * Written during render on purpose, and idempotent: it only ever records what the
   * address just said, so a double render under StrictMode writes the same value
   * twice. An effect would be a commit too late — the render after a link dropped the
   * parameter is the one that has to keep the venue.
   */
  const held = useRef("");
  if (named) held.current = named;

  const resolved = resolveSite({
    named,
    held: navigationType === "POP" ? "" : held.current,
    sites,
    isLoading,
    /* `data` survives a failed refetch, so a venue list that was fetched once is not
       "failed" the moment a later poll misses. Only never having had one is. */
    failed: Boolean(error) && sites.length === 0,
  });
  const { status, siteId } = resolved;
  const defaultSiteId = sites[0]?.id ?? "";

  const setSiteId = useCallback(
    (id: string) => {
      held.current = id;
      setParams((prev) => {
        const next = new URLSearchParams(prev);
        if (id && id !== defaultSiteId) next.set(SITE_PARAM, id);
        else next.delete(SITE_PARAM);
        return next;
      });
      /* A push, not a replace, and it is the one place in this file that differs from
         how `/sessions` writes its filters. The operator chose a venue; Back is how a
         choice is undone, and undoing it must not also leave the console. */
    },
    [defaultSiteId, setParams],
  );

  /*
   * The address, brought into line with the answer.
   *
   * `null` means *do not touch it*: while the venue is unresolved there is nothing
   * true to write, and writing a guess into the address bar is the defect this file
   * exists to remove rather than a smaller version of it.
   */
  const want =
    status === "ready" || status === "unknown" ? (siteId === defaultSiteId ? "" : siteId) : null;

  useEffect(() => {
    if (want === null || named === want) return;
    /* The sign-in screen has no venue and no venue-scoped anything on it. Stamping a
       venue onto `/sign-in` would put one in the address of the one route that is
       outside the shell. */
    if (pathname === SIGN_IN_PATH) return;
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (want) next.set(SITE_PARAM, want);
        else next.delete(SITE_PARAM);
        return next;
      },
      { replace: true },
    );
  }, [want, named, pathname, setParams]);

  return (
    <Ctx.Provider
      value={{
        ...resolved,
        sites,
        setSiteId,
        isLoading,
        error: (error as Error) ?? null,
      }}
    >
      {children}
    </Ctx.Provider>
  );
}

export function useSite(): SiteState {
  const v = useContext(Ctx);
  if (!v) throw new Error("useSite outside SiteProvider");
  return v;
}

/**
 * The site switcher: two controls in one.
 *
 * The name is a button that goes to the site's overview; the adjacent caret opens
 * the picker. It loads as a skeleton sized like itself so the sidebar never
 * reflows, and if the list cannot be fetched the failure renders **here**, in
 * place, rather than the control silently vanishing — an operator needs to know
 * which site they are about to change, especially when it is the live one.
 *
 * **The picker was the command palette's bug in a smaller shape, on every screen.**
 * Driven rather than read: `Escape` did not close it, `Tab` walked out of it and left
 * it hanging open behind whatever had focus next, and the only way to dismiss it was
 * a `position: fixed` div — a pointer-only affordance, on the control that decides
 * which site every figure in the console is relative to.
 *
 * It is a menu button now, which is the pattern it was already shaped like: the caret
 * says `aria-haspopup`, the popover is a `menu`, and each site is a
 * `menuitemradio` carrying whether it is the one you are looking at, which the bare
 * `data-active` highlight said only in colour. `Escape` closes it and hands focus back
 * to whichever of the two buttons opened it; arrow keys move within it; and focus
 * leaving it at all closes it, which is what `Tab` is supposed to do to a menu and is
 * also every other way focus can get out. The fixed div is gone — a `pointerdown`
 * listener dismisses on a click outside *and* lets that click do what it was aimed at,
 * instead of being eaten by an invisible sheet over the whole page.
 *
 * **Three answers, not one.** *No venue in this deployment* is not *"Cannot load
 * sites"* — that would be a request that succeeded, reported as one that failed —
 * and a venue the deployment does not have gets its own sentence too. The unknown one
 * keeps the picker, because the operator's next act is to choose a venue
 * that exists.
 */
export function SiteSwitcher() {
  const { site, siteId, sites, setSiteId, isLoading, error } = useSite();
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  /* Whichever button opened it, because both do. Focus and escape-to-blur are a
     pair, and the pair is only right if it returns to where the operator was. */
  const opener = useRef<HTMLButtonElement | null>(null);
  const menuId = useId();

  // Plain keys belong to the popover while it is open; ⌘K still reaches past it.
  useKeyboardSuspend(open);

  const close = useCallback((restoreFocus: boolean) => {
    setOpen(false);
    if (restoreFocus) opener.current?.focus();
  }, []);

  const items = () => [
    ...(menu.current?.querySelectorAll<HTMLElement>('[role="menuitemradio"]') ?? []),
  ];

  /* Focus moves into the menu on open, onto the site you are on rather than the top
     of the list — the same rule the highlight already followed by eye. */
  useEffect(() => {
    if (!open) return;
    const nodes = items();
    (nodes.find((n) => n.getAttribute("aria-checked") === "true") ?? nodes[0])?.focus();
  }, [open]);

  /* Capture, so a click on a control elsewhere dismisses the menu before that
     control acts on it — without absorbing the click entirely, as a fixed overlay
     div would, which would make the control need a second one. */
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!wrap.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => document.removeEventListener("pointerdown", onPointerDown, true);
  }, [open]);

  const toggle = (event: React.MouseEvent<HTMLButtonElement>) => {
    opener.current = event.currentTarget;
    setOpen((o) => !o);
  };

  if (isLoading && sites.length === 0) {
    return (
      <div className="site-switch" style={{ padding: "0.5rem 0.625rem" }}>
        <span className="skeleton" style={{ width: "70%", height: "1.4em" }} />
      </div>
    );
  }

  /* Nothing to pick between: the list failed, or the deployment has no venue. Two
     different sentences and neither of them offers a menu. */
  if (sites.length === 0) {
    return (
      <div className="site-switch" style={{ padding: "0.5rem 0.625rem" }}>
        <div>
          <div className="site-switch-label">{t("site.label")}</div>
          <div style={{ fontSize: "0.8125rem", color: "var(--danger)", fontWeight: 600 }}>
            {error ? t("site.failed") : t("site.none")}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div
      ref={wrap}
      style={{ position: "relative" }}
      onKeyDown={(event) => {
        if (!open) return;
        if (event.key === "Escape") {
          /* Claimed, so the registry does not also act on it — the window listener
             checks `defaultPrevented` precisely so a press answered here is answered
             once. */
          event.preventDefault();
          close(true);
          return;
        }
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        const nodes = items();
        const at = nodes.indexOf(document.activeElement as HTMLElement);
        const next = event.key === "ArrowDown" ? at + 1 : at - 1;
        nodes[((next % nodes.length) + nodes.length) % nodes.length]?.focus();
      }}
      /* Focus leaving the switcher closes the menu. That is `Tab` and `Shift+Tab`,
         and it is also every other way out — a click that lands on something
         focusable, a screen reader's own navigation, a script. The alternative was a
         `Tab` branch, which handles the one exit anybody thought to press. */
      onBlur={(event) => {
        if (!open) return;
        if (event.currentTarget.contains(event.relatedTarget)) return;
        setOpen(false);
      }}
    >
      <div className="site-switch">
        <button
          type="button"
          className="site-switch-name"
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={toggle}
        >
          <span className="site-switch-label">
            {t("site.label")}
            {site ? (
              <span
                className="env-badge"
                data-env={site.environment}
                style={{ marginLeft: "0.375rem" }}
              >
                {site.environment === "live" ? t("site.env.live") : t("site.env.lab")}
              </span>
            ) : null}
          </span>
          {/* The unknown venue's own id is *not* the value here. It is on the page,
              in text, where an operator can read and correct it — a `title` is a
              place a fact can be true and unreachable. */}
          <span className="site-switch-value" title={site ? site.name : siteId}>
            {site ? site.name : t("site.unknown")}
          </span>
        </button>
        <button
          type="button"
          className="site-switch-caret"
          aria-label={t("site.choose")}
          aria-haspopup="menu"
          aria-expanded={open}
          aria-controls={open ? menuId : undefined}
          onClick={toggle}
        >
          <ChevronDown size={14} />
        </button>
      </div>

      {open ? (
        <div
          ref={menu}
          id={menuId}
          role="menu"
          aria-label={t("site.choose")}
          className="popover"
          style={{ left: "0.625rem", right: "0.625rem", top: "100%" }}
        >
          {sites.map((s) => (
            <button
              key={s.id}
              type="button"
              role="menuitemradio"
              aria-checked={s.id === siteId}
              /* Roving focus, which is what makes `Tab` *leave* the menu rather than
                 step to the next site. Measured with the items as ordinary tab stops:
                 `Tab` walked the list one site at a time and the menu stayed open the
                 whole way, which is the defect this was opened to fix. */
              tabIndex={-1}
              className="popover-item"
              data-active={s.id === siteId}
              onClick={() => {
                setSiteId(s.id);
                // The row that had focus is about to unmount. Hand it back rather
                // than letting the platform drop it on `<body>`.
                close(true);
              }}
            >
              <span style={{ flex: 1, minWidth: 0 }}>{s.name}</span>
              <span className="env-badge" data-env={s.environment}>
                {s.environment === "live" ? t("site.env.live") : t("site.env.lab")}
              </span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
