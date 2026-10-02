import { withCopy } from "../i18n/catalogue.js";
import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useCallback, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import type { Session } from "@foyer/contract";
import { api, ApiError } from "../api.js";
import { PageHead } from "../App.js";
import { t } from "../i18n/sessions.js";
import { BINDINGS } from "../keys/bindings.js";
import { BindingKeys, Hint } from "../keys/Keys.jsx";
import { useRunShortcut } from "../keys/registry.js";
import { useListKeyboard } from "../keys/useListKeyboard.js";
import { panelIsOpen, panelReducer, step, type PanelState } from "../list/detail.js";
import { BatchBar } from "../list/BatchBar.jsx";
import { scopeToken } from "../list/selection.js";
import { useSelection } from "../list/useSelection.js";
import { useSite } from "../site.js";
import { Check, Copy, Search } from "../ui/icons.js";
import { Empty } from "../ui/primitives.js";
import { DurationCell, SessionStatus, StartTimeCell } from "../ui/session.jsx";
import type { Column } from "../ui/table.js";
import { Absent, BytesCell, IdCell, Stack, Table, TimeCell } from "../ui/table.js";
import { SessionDock } from "./SessionDetail.jsx";

// review2:disconnect — see PortalGrants.tsx. Its own chunk and copy.
//
// Fetched when THIS module is, not when the card first renders. `App.tsx`'s
// `preloadDaily` loads this screen as soon as the shell has painted so that an
// operator whose connection drops still reaches the list and is told the truth about
// it; a card whose chunk had not been fetched could not render with the network cut,
// and took the list's "could not load" state down with it (web/e2e
// absence-is-not-zero, `/sessions offline`). A failed warm-up is not an error here: the lazy import below
// asks again, and a chunk that did arrive is answered from the module map.
const loadPortalGrants = () => withCopy(() => import("./PortalGrants.js"));
loadPortalGrants().catch(() => undefined);
const PortalGrants = lazy(loadPortalGrants);

// The filter chip for `unknown` names the cause — no accounting arrived — while the
// pill on the row names the consequence: the session's state is unknown. They are
// deliberately different words for the same rows.
const STATES = [
  { value: "online", get label() {
    return t("sessions.state.online");
  } },
  { value: "ended", get label() {
    return t("sessions.state.ended");
  } },
  { value: "orphaned", get label() {
    return t("sessions.state.orphaned");
  } },
  { value: "unknown", get label() {
    return t("sessions.filter.noAccounting");
  } },
] as const;

/**
 * The two anomalies that are not states, and therefore had no filter.
 *
 * `orphaned` is deliberately not here: it is one of the four chips above, and giving
 * it a second control that narrows the same rows by a different mechanism is how a
 * toolbar starts lying about what it does.
 *
 * One predicate per flag, used for **both** the count on the chip and the narrowing
 * it performs — so a chip cannot offer to show rows it then filters differently.
 */
const FLAGS: Record<string, (s: Session) => boolean> = {
  stale: (s) => s.state === "online" && s.freshness.state === "stale",
  wrapped: (s) => s.countersWrapped,
};

const matchesFlag = (s: Session, flag: string): boolean => FLAGS[flag]?.(s) ?? true;

/**
 * The sessions screen. Read-only, forever — `radacct` belongs to FreeRADIUS, and
 * an "edit session" affordance can never be built.
 *
 * This is where the product either tells the truth or does not. Every incumbent
 * renders accounting data as though it were live; the fixtures behind this screen
 * contain a session orphaned for forty days, one twenty minutes stale, one with a
 * negative duration from clock skew, two live sessions sharing a MAC and a
 * non-unique `Acct-Session-Id`, and one from a NAS that sends no accounting at all.
 * If the screen reads correctly against those, it reads correctly.
 */
export function SessionsScreen() {
  const { siteId } = useSite();
  const [params, setParams] = useSearchParams();
  const runShortcut = useRunShortcut();

  // Filters live in the URL. A filtered view is a link an operator can send to a
  // colleague, and the back button means what it looks like it means.
  const states = params.get("state")?.split(",").filter(Boolean) ?? [];
  const q = params.get("q") ?? "";
  const mac = params.get("mac") ?? "";
  /* "Show only these" pins a set of rows into the URL. It is a client-side narrowing
     of what has already been fetched, not a server query — which is why it is the
     one filter that cannot go stale against the list it came from. */
  const pinned = params.get("only")?.split(",").filter(Boolean) ?? [];
  /*
   * The anomaly axis.
   *
   * This screen carried three stacked notices — orphaned, overdue, wrapped counter —
   * which is 300 px before any data at 390 px, on facts that concern three rows out
   * of the page. Two of them repeated a dashboard alarm word for word, and none of
   * the three could **reach** the rows it was about. The wrapped-counter one had no
   * filter and no link anywhere in the console.
   *
   * So they became what they were describing: a way to see those rows. `orphaned`
   * already had a filter — it is one of the four state chips — so only the two with
   * none are here, and each chip renders **only when its anomaly exists**, which is
   * the whole difference between marking an exception and colouring in a column.
   *
   * A named flag rather than `only=<ids>`: an id list in a URL goes stale, and this
   * screen's rule is that a filtered view is a link an operator can send.
   */
  const flag = params.get("flag") ?? "";
  const filtered = states.length > 0 || q !== "" || mac !== "" || pinned.length > 0 || flag !== "";

  const query = useQuery({
    queryKey: ["sessions", siteId, states, q, mac],
    queryFn: () => api.listSessions({ siteId, state: states, q, mac, limit: 100 }),
    refetchInterval: 15_000,
  });

  const pinnedKey = params.get("only") ?? "";
  const fetched = useMemo(() => query.data?.items ?? [], [query.data]);
  const listed = useMemo(() => {
    const only = pinnedKey.split(",").filter(Boolean);
    return only.length > 0 ? fetched.filter((s) => only.includes(s.id)) : fetched;
  }, [fetched, pinnedKey]);
  /* The counts on the chips are taken from `listed`, before the flag narrows it —
     otherwise pressing a chip rewrites its own label to the number it just produced
     and there is no way back to knowing how many there were. */
  const rows = useMemo(
    () => (flag ? listed.filter((s) => matchesFlag(s, flag)) : listed),
    [listed, flag],
  );
  const rowKeys = useMemo(() => rows.map((s) => s.id), [rows]);

  const setParam = useCallback(
    (key: string, value: string) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (value) next.set(key, value);
          else next.delete(key);
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );

  const toggleState = (value: string) => {
    const next = states.includes(value) ? states.filter((s) => s !== value) : [...states, value];
    setParam("state", next.join(","));
  };

  /* ── bulk selection ─────────────────────────────────────────────────────── */

  /*
   * The scope a selection belongs to. Every input that decides *which rows are on
   * screen* is in here, so changing any of them empties the selection. That is the
   * one rule bulk actions cannot be safe without: an operator must never act on rows
   * they cannot see.
   */
  const scope = scopeToken([siteId, states.join(","), q, mac, pinned.join(","), flag]);
  /* Every row is selectable: a session is read-only, so what the selection is *for*
     is the same for all of them. See the bar below for what that turns out to be. */
  const picked = useSelection(scope, rowKeys, !query.isLoading);

  /* ── the detail panel ───────────────────────────────────────────────────── */

  const cursor = params.get("session");
  const [dismissed, setDismissed] = useState(false);
  /*
   * A URL that names a session is a deliberate open — a link somebody followed, or
   * this page reloaded on the record it was showing. So the panel it mounts behaves
   * the way `Enter` does and takes focus. Only a cursor move clears it, which means
   * the very first `ArrowDown` on a fresh list clears it before the panel exists.
   */
  const [deliberate, setDeliberate] = useState(true);
  const panel: PanelState = { cursor, dismissed, deliberate };
  const open = panelIsOpen(panel);

  const tableRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const applyPanel = useCallback(
    (action: Parameters<typeof panelReducer>[1]) => {
      const next = panelReducer({ cursor: params.get("session"), dismissed, deliberate }, action);
      setDismissed(next.dismissed);
      setDeliberate(next.deliberate);
      setParam("session", next.cursor ?? "");
    },
    [params, dismissed, deliberate, setParam],
  );

  /** Clearing filters keeps the open record: it is a different axis from the list. */
  const clearFilters = useCallback(() => {
    setParams(
      (prev) => {
        const next = new URLSearchParams();
        const session = prev.get("session");
        if (session) next.set("session", session);
        return next;
      },
      { replace: true },
    );
  }, [setParams]);

  const keyboard = useListKeyboard({
    rootRef: tableRef,
    keys: rowKeys,
    cursor,
    onMove: (key) => applyPanel({ type: "move", key }),
    onOpen: (key) => applyPanel({ type: "open", key }),
    onClose: () => applyPanel({ type: "dismiss" }),
    canClose: open,
    onToggle: (key) => picked.onToggle(key, false),
    onExtend: picked.onExtend,
    onFocusSearch: () => searchRef.current?.focus(),
    onClearFilters: clearFilters,
    canClearFilters: filtered,
  });

  /*
   * A cursor can point at a row the current filters exclude — that is what makes a
   * session a link. The row is then fetched on its own rather than the panel
   * pretending the session does not exist.
   */
  const inList = rows.find((s) => s.id === cursor) ?? null;
  const detached = useQuery({
    queryKey: ["session", cursor],
    queryFn: () => api.getSession(cursor!),
    enabled: open && Boolean(cursor) && !inList,
    retry: false,
  });

  const shown = inList ?? (open ? (detached.data ?? null) : null);
  /*
   * The error itself, not `status === 404`.
   *
   * A boolean here is a boolean that has to be false for every failure that is not a
   * 404 — so a `500` fell through to the dock's empty state and read *"No session
   * selected."* while a row was selected. The dock decides what to say from the
   * type, which is the only thing that can tell *gone* from *the server failed*.
   */
  const detachedError = open && !inList ? detached.error : null;

  /*
   * The access points of this site, for one figure in the dock.
   *
   * The dock states the plan's rate limit, and whether that figure is a fact or only
   * something written down is answerable **only** from `Capabilities.bandwidthLimits`
   * on the session's NAS. `Plan.enforcedBy` does not cover it — it describes the time
   * and data limits — so the join has to come from a screen that can see both, and
   * this is that screen.
   *
   * Fetched here rather than per-session inside the dock, for two reasons. `J`/`K`
   * walk the cursor down the list, so a per-session `getNas` would fire a request per
   * keystroke against an answer that is the same for most rows. And the dock would
   * then own a loading state for a caption, which is a lot of machinery to make one
   * sentence appear late. One list, cached for the life of the screen, is a single
   * request that answers every row.
   *
   * Not `enabled: open`: the request would then start at the moment the operator
   * clicks, and the caption would resolve after they are already reading. Issued with
   * the list so it has arrived before any row is opened.
   *
   * `limit` is the mock's ceiling. A site with more access points than that resolves
   * to `null` for the overflow, which the dock renders as *not established* rather
   * than as *nothing is applying it* — the honest degradation, and the reason
   * `rateApplication` has a fourth state.
   */
  const nasQuery = useQuery({
    queryKey: ["nas", siteId, "capabilities"],
    queryFn: () => api.listNas({ siteId, limit: 200 }),
    staleTime: 60_000,
  });

  const shownCapabilities = useMemo(() => {
    if (!shown) return null;
    return nasQuery.data?.items.find((n) => n.id === shown.nasId)?.capabilities ?? null;
  }, [nasQuery.data, shown]);

  /* Duplicate MACs are surfaced rather than de-duplicated. Two live sessions for
     one address is a roam that never closed or a spoof; either way the operator
     needs to see both rows. */
  const duplicateMacs = useMemo(() => {
    const seen = new Map<string, number>();
    for (const s of fetched) {
      if (s.state !== "online") continue;
      seen.set(s.callingStationId, (seen.get(s.callingStationId) ?? 0) + 1);
    }
    return new Set([...seen].filter(([, n]) => n > 1).map(([m]) => m));
  }, [fetched]);

  const stale = listed.filter((s) => matchesFlag(s, "stale")).length;
  /* A 32-bit counter that has gone backwards, which makes every byte figure for that
     session a floor rather than a measurement — and until this chip existed there was
     no filter and no link to those rows anywhere in the console. */
  const wrapped = listed.filter((s) => matchesFlag(s, "wrapped")).length;

  const columns: Column<Session>[] = [
    {
      key: "state",
      narrow: "keep",
      header: t("sessions.col.status"),
      width: "1%",
      cell: (s) => <SessionStatus session={s} />,
    },
    {
      key: "mac",
      narrow: "keep",
      header: t("sessions.col.mac"),
      /*
       * **Mark the exception, not the rule.**
       *
       * `randomised` beside the address on every row that has a locally-administered
       * MAC — on a fresh world, **23 of the 34 sessions in `site-hcm`** — is not a
       * qualifier, it is the column's second word. `.id-cell` is a flex row, so it
       * would also spend that width in the narrowest column of the narrowest table in
       * the console, and at 390 px wrap and spend the height too.
       *
       * Meanwhile `duplicate` — one address with two sessions open, which is a roam
       * that never closed **or a spoof**, and the only fact in this cell an operator
       * can act on — would sit on 2 rows in the identical tier, one modifier class apart.
       * A flag on two rows in three is not a flag.
       *
       * The rule is not lost, it is said where it is asked: the dock carries
       * `randomisedWhy` as the note on the MAC field for whichever session is open,
       * which is where an operator goes to ask *is this a device*. Adding a sentence
       * to this screen in place of 23 per-row ones would be putting the noise back
       * with better manners. `docs/rules/security.md` §6 is unaffected — the console does not
       * claim a MAC is an identity, in either direction.
       */
      cell: (s) => (
        <IdCell
          value={s.callingStationId}
          subject={t("sessions.mac.subject")}
          keepTail={8}
          onFilter={(v) => setParam("mac", v)}
          suffix={
            duplicateMacs.has(s.callingStationId) ? (
              <span className="stack-sub stack-sub-caution" title={t("sessions.mac.duplicateWhy")}>
                {t("sessions.mac.duplicate")}
              </span>
            ) : null
          }
        />
      ),
    },
    {
      key: "identity",
      narrow: "drop",
      header: t("sessions.col.identity"),
      /*
       * No secondary line, and no `hostname`, which the contract deliberately does not
       * carry: recovering one needs DHCP fingerprinting, which is NAC territory and out
       * of scope, so the field would be null on real hardware and populated only in
       * fixtures. A column that shows `iPhone` for three rows in a demo teaches anyone
       * watching that Foyer can show device names, and every screen
       * designed afterwards assumes a column that will be empty in the field.
       */
      cell: (s) =>
        s.username ??
        (s.identityKind === "voucher" ? (
          t("sessions.identity.voucher")
        ) : (
          <Absent hint={t("sessions.detail.usernameNone")} />
        )),
    },
    {
      key: "nas",
      narrow: "drop",
      header: t("sessions.col.nas"),
      cell: (s) => <Stack primary={s.nasName} secondary={s.ssid ?? undefined} />,
    },
    {
      key: "started",
      narrow: "drop",
      header: t("sessions.col.startTime"),
      // Flagged where it appears, not only where it poisons the duration: a start
      // time in the future renders "in 4 minutes", which in a neutral cell is
      // indistinguishable from "2 hours ago" at a glance.
      cell: (s) => <StartTimeCell session={s} />,
    },
    {
      key: "duration",
      narrow: "drop",
      header: t("sessions.col.duration"),
      align: "end",
      cell: (s) => <DurationCell session={s} />,
    },
    /*
     * ─────────────────────────────────────────────────────────────────────────
     *  Freshness comes **before** the figures it qualifies, and that is a rule
     * ─────────────────────────────────────────────────────────────────────────
     *
     * `docs/rules/frontend.md`: *any figure derived from accounting travels with its
     * freshness*. Columns fall off the right-hand edge in declaration order, and
     * this one was last — so at 1440 the table needed 1376px in a 1150px scroller
     * and **the freshness column was the first thing hidden**; at 1280, 386px was
     * hidden and `Downloaded` was on screen while the thing saying how old it is
     * was not.
     *
     * Moving it above the two byte columns makes the rule structural rather than
     * remembered: whatever width the console is read at, a byte figure cannot be
     * visible unless the column that dates it is visible too. It costs nothing and
     * it is checked — see `wide-table-says-what-is-hidden`.
     */
    {
      key: "lastUpdate",
      narrow: "drop",
      header: t("sessions.col.lastUpdateTime"),
      cell: (s) =>
        // Suppressed while the session is live and healthy: "last seen" measures
        // nothing when updates are arriving on schedule, and showing it invites
        // trust in a number that is not tracking anything.
        s.state === "online" && s.freshness.state === "sampled" ? (
          <Absent hint={t("sessions.lastUpdate.healthy")} />
        ) : (
          <TimeCell iso={s.lastUpdateAt} absent={t("freshness.noAccounting")} />
        ),
    },
    {
      key: "down",
      narrow: "drop",
      header: t("sessions.col.down"),
      align: "end",
      cell: (s) => (
        <BytesCell
          value={s.outputOctets}
          total={s.outputOctetsTotal}
          wrapped={s.countersWrapped}
        />
      ),
    },
    {
      key: "up",
      narrow: "drop",
      header: t("sessions.col.up"),
      align: "end",
      cell: (s) => (
        <BytesCell value={s.inputOctets} total={s.inputOctetsTotal} wrapped={s.countersWrapped} />
      ),
    },
  ];

  const cursorIndex = cursor ? rowKeys.indexOf(cursor) : -1;

  return (
    <div className="page" data-dock={open}>
      <PageHead
        title={t("sessions.title")}
        description={t("sessions.description")}
      />

      <div className="toolbar">
        <label className="search">
          <Search size={14} />
          <input
            ref={searchRef}
            type="search"
            placeholder={t("sessions.search")}
            defaultValue={q}
            onChange={(e) => setParam("q", e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "Escape") return;
              e.stopPropagation();
              const el = e.currentTarget;
              if (el.value) {
                el.value = "";
                setParam("q", "");
              } else {
                el.blur();
              }
            }}
          />
          <span className="visually-hidden">{t("sessions.searchLabel")}</span>
          {/* The shortcut is printed on the control it operates. */}
          <span className="search-key" aria-hidden="true">
            <BindingKeys spec={BINDINGS.focusSearch} />
          </span>
        </label>

        <div className="chip-group">
          {STATES.map((s) => (
            <button
              key={s.value}
              type="button"
              className="chip"
              aria-pressed={states.includes(s.value)}
              onClick={() => toggleState(s.value)}
            >
              {s.label}
            </button>
          ))}
          {/*
            The two anomaly chips, present only while their anomaly is. See `FLAGS`
            and the note on `flag` above: this is what three stacked notices became.

            The sentence each notice carried is the chip's `title` rather than being
            dropped — on a control that takes focus, which is more than the notice
            body was reachable by on a phone. The rows themselves still explain
            themselves: an orphaned session's pill and a wrapped byte figure both
            carry their own why, and the dashboard still raises the standing alarm.
          */}
          {stale > 0 ? (
            <button
              type="button"
              className="chip"
              aria-pressed={flag === "stale"}
              title={t("sessions.stale.body")}
              onClick={() => setParam("flag", flag === "stale" ? "" : "stale")}
            >
              {t("sessions.filter.stale", stale)}
            </button>
          ) : null}
          {wrapped > 0 ? (
            <button
              type="button"
              className="chip"
              aria-pressed={flag === "wrapped"}
              title={t("sessions.wrapped.body")}
              onClick={() => setParam("flag", flag === "wrapped" ? "" : "wrapped")}
            >
              {t("sessions.filter.wrapped", wrapped)}
            </button>
          ) : null}
        </div>

        <div className="toolbar-right">
          {mac ? (
            <button type="button" className="chip" onClick={() => setParam("mac", "")}>
              MAC: <span className="mono">{mac}</span> ✕
            </button>
          ) : null}
          {pinned.length > 0 ? (
            <button type="button" className="chip" onClick={() => setParam("only", "")}>
              {t("sessions.filter.selection", pinned.length)} ✕
            </button>
          ) : null}
          {/* "0 rows" during the first load, and "0 rows" again during a total
              outage. Neither of those is a count, and one of them is the number an
              operator would act on. */}
          <span className="toolbar-count">
            {query.isLoading
              ? t("common.loading")
              : query.isError
                ? t("table.rowsUnavailable")
                : t("table.rows", rows.length)}
          </span>
        </div>
      </div>

      <div ref={tableRef}>
        <Table
          columns={columns}
          rows={rows}
          rowKey={(s) => s.id}
          rowLabel={(s) => s.callingStationId}
          isLoading={query.isLoading}
          error={query.error}
          refreshing={query.isFetching && !query.isLoading}
          // The rule that keeps rows on screen through a refetch is the same rule
          // that requires this: without it a failed refresh leaves stale rows with
          // nothing saying so, which is worse than the rows vanishing.
          updatedAt={query.dataUpdatedAt}
          filtered={filtered}
          onClearFilters={clearFilters}
          selectedKey={cursor}
          // A click is a deliberate open, so it clears a previous dismissal. Moving
          // the cursor with the keyboard is not, and does not.
          onRowClick={(s) => applyPanel({ type: "open", key: s.id })}
          selection={{
            checked: picked.checked,
            onToggle: picked.onToggle,
            onToggleAll: picked.onToggleAll,
            rowLabel: (s) => t("bulk.selectRow", s.callingStationId),
            allLabel: t("bulk.selectAll"),
          }}
          empty={
            <Empty
              title={t("sessions.empty.title")}
              description={t("sessions.empty.description")}
            />
          }
        />
      </div>

      {/* Every shortcut on this screen is legible without pressing anything. */}
      <div className="list-legend">
        <Hint spec={BINDINGS.listNext} label={t("keys.listNext")} />
        <Hint spec={BINDINGS.listOpen} />
        <Hint spec={BINDINGS.listToggle} />
        {filtered ? <Hint spec={BINDINGS.clearFilters} /> : null}
        <button
          type="button"
          className="btn btn-sm btn-ghost"
          onClick={() => runShortcut(BINDINGS.shortcutSheet)}
        >
          {t("keys.shortcuts")}
        </button>
      </div>

      <p className="sampling-note">{t("sessions.samplingNote")}</p>

      <Suspense fallback={null}>
        <PortalGrants />
      </Suspense>

      {picked.count > 0 ? (
        <SessionBatchBar
          sessions={rows.filter((s) => picked.checked.has(s.id))}
          onPin={() => setParam("only", picked.selection.keys.join(","))}
          onClear={picked.clear}
        />
      ) : null}

      {open ? (
        <SessionDock
          session={shown}
          capabilities={shownCapabilities}
          deliberate={deliberate}
          hidden={Boolean(cursor) && !inList && Boolean(shown)}
          error={detachedError}
          isLoading={detached.isLoading && !inList}
          position={cursorIndex >= 0 ? { index: cursorIndex + 1, total: rowKeys.length } : null}
          duplicateMac={shown ? duplicateMacs.has(shown.callingStationId) : false}
          onPrevious={() => {
            const next = step(rowKeys, cursor, -1);
            if (next) {
              applyPanel({ type: "open", key: next });
              keyboard.focusRow(next);
            }
          }}
          onNext={() => {
            const next = step(rowKeys, cursor, 1);
            if (next) {
              applyPanel({ type: "open", key: next });
              keyboard.focusRow(next);
            }
          }}
          onClose={() => {
            applyPanel({ type: "dismiss" });
            keyboard.focusRow(cursor);
          }}
        />
      ) : null}
    </div>
  );
}

/* ── batch actions ────────────────────────────────────────────────────────── */

/**
 * The batch bar for sessions, and the argument for why it has no verb in it.
 *
 * **`radacct` belongs to FreeRADIUS and session history is read-only forever**
 * (`docs/rules/frontend.md`), so there is no destructive bulk action here and
 * none may be invented — not "end these sessions", which is a driver call against a
 * NAS and belongs to the record that authorised them, and certainly not a delete.
 *
 * What is left is what an operator genuinely does with a set of rows they have picked
 * out of forty-five: take the addresses somewhere else, and narrow the list to just
 * these. Both are reads. That is the whole honest surface, and a screen with two
 * read affordances is exactly why selection is not the same feature everywhere.
 */
function SessionBatchBar({
  sessions,
  onPin,
  onClear,
}: {
  sessions: Session[];
  onPin: () => void;
  onClear: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const macs = [...new Set(sessions.map((s) => s.callingStationId))];

  return (
    <BatchBar count={sessions.length} onClear={onClear}>
      <button
        type="button"
        className="btn btn-sm"
        onClick={() => {
          // Clipboard first in the handler, nothing awaited before it — Safari
          // drops the permission once the call leaves the user-gesture task.
          void navigator.clipboard.writeText(macs.join("\n"));
          setCopied(true);
          setTimeout(() => setCopied(false), 2000);
        }}
      >
        {copied ? <Check size={13} /> : <Copy size={13} />}
        {copied ? t("bulk.copied", macs.length) : t("bulk.copyMacs")}
      </button>
      <button type="button" className="btn btn-sm" onClick={onPin}>
        {t("bulk.filterToSelection")}
      </button>
    </BatchBar>
  );
}
