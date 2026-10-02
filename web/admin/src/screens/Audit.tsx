import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import type { AuditEntry } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { auditCopy } from "../audit.js";
import { t } from "../i18n/audit.js";
import { useSite } from "../site.js";
import { Empty, Notice } from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Absent, Stack, Table, TimeCell } from "../ui/table.js";

/**
 * What was changed, by whom, and when.
 *
 * **Read-only, forever — and there is nothing on this screen to make read-only.**
 * The contract declares no create, update or delete under `/audit` and never may:
 * `contract/src/read-only.ts` fails the *typecheck* if one is added, which is the
 * moment the decision is being made, rather than waiting for somebody to build a
 * button. So there is deliberately no edit affordance here, no "hide this entry",
 * and no delete — a log an administrator can edit is not a log, and the entry most
 * worth removing is the one that would be removed.
 *
 * A correction is a later entry describing the correction. That is what append-only
 * means and it is the only honest way to fix a wrong row.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The actor is rendered from the entry and never looked up.**
 *
 * `AuditEntry.actorUsername` is denormalised on purpose, and the fixture world
 * proves why: one entry's `actorId` is `op-gone`, which is in `operators` nowhere at
 * all, and the entry still names `tran.quoc.bao`. Joining to the operators list at
 * read time is the obvious implementation and the wrong one — it renders *"deleted
 * operator"* on the one row somebody is reading the log to understand.
 *
 * The near-miss worth naming: annotating a row with *"no longer an operator"* is
 * tempting and needs exactly the join this member exists to avoid. It would also be
 * wrong while the operators list was still loading, where every actor would read as
 * deleted for a moment. So the actor is a name, from the entry, full stop.
 *
 * Same argument one level down for `subject.name`: the access point whose secret was
 * rotated in March may not exist in June, and the row still has to say which one.
 */
export function AuditScreen() {
  const { sites } = useSite();
  const [params, setParams] = useSearchParams();
  const actorId = params.get("actorId") ?? "";
  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";

  const query = useQuery({
    /* No `siteId`. Entries are the deployment's, and half of them have none — an
       operator was created, and operators are not site-scoped. Filtering this list by
       the header's switcher would hide exactly the entries an administrator opens it
       for. */
    queryKey: ["audit", actorId, from, to],
    queryFn: () =>
      api.listAudit({
        actorId: actorId || undefined,
        /* The contract's bounds are instants; these inputs are days. Inclusive lower
           bound, exclusive upper — so `to` is midnight *after* the day chosen, or
           the day itself would be missing from its own filter. */
        from: from ? new Date(`${from}T00:00:00Z`).toISOString() : undefined,
        to: to ? new Date(`${to}T00:00:00Z`).toISOString() : undefined,
      }),
  });

  const rows = query.data?.items ?? [];

  /*
   * The actors to filter by, taken from **the log** rather than from `/operators` —
   * and from an **unfiltered** read of it.
   *
   * Sourcing it from the log is the same argument as the actor column, and it is what
   * makes the deleted operator filterable at all: they are not in the operators list
   * and their entries are exactly the ones somebody is looking for. `actorId` rather
   * than the username, because a username can be reused after a deletion.
   *
   * What matters is which read it draws from. Built from `rows`, which is the
   * *already filtered* result, the moment an operator chose one of the five actors
   * the other four would leave the dropdown — five options down to one — and
   * switching from one actor to another would become two steps, with no *Clear
   * filters* control on screen in that state: `Table` offers one only from its
   * no-matches row, and a filter that matched something has rows instead. The date
   * window would do the same thing more quietly, dropping whichever actor happened to
   * fall outside the chosen days.
   *
   * A second query rather than a second collection. When nothing is filtered its key
   * is identical to the one above, so react-query serves both from one cache entry and
   * one request; a filter is what makes it a second read, which is the only moment it
   * is needed. It answers `403` for a staff session exactly as the list does, and the
   * filter is then empty rather than wrong.
   */
  const everyEntry = useQuery({
    queryKey: ["audit", "", "", ""],
    queryFn: () => api.listAudit({}),
  });

  const known = new Map((everyEntry.data?.items ?? []).map((e) => [e.actorId, e.actorUsername]));
  /* A chosen actor whose entries all sit past the first page of the unfiltered read
     would otherwise leave the select holding a value it does not offer, which renders
     blank and reads as *no filter* over a filtered table. The name comes from the rows
     that are on screen, which are by definition that actor's own. */
  if (actorId && !known.has(actorId)) known.set(actorId, rows[0]?.actorUsername ?? actorId);
  const actors = [...known.entries()].sort((a, b) => a[1].localeCompare(b[1]));

  const filtered = Boolean(actorId || from || to);

  const columns: Column<AuditEntry>[] = [
    {
      key: "at",
      narrow: "keep",
      header: t("audit.col.when"),
      /* One fixture entry is timestamped three minutes in the future — the clock skew
         that already exists in the session fixtures, here in the one table that is
         read strictly in time order. `TimeCell` renders it as the server sent it
         rather than clamping, because a row that claims to be from the future is a
         fact about somebody's clock and hiding it fixes nothing. */
      cell: (e) => <TimeCell iso={e.at} />,
    },
    {
      key: "actor",
      narrow: "keep",
      header: t("audit.col.actor"),
      cell: (e) => <span className="mono">{e.actorUsername}</span>,
    },
    {
      key: "what",
      narrow: "keep",
      header: t("audit.col.change"),
      cell: (e) => {
        const copy = auditCopy(e);
        return (
          <Stack
            primary={copy.what}
            secondary={[e.subject?.name, copy.detail].filter(Boolean).join(" · ") || undefined}
          />
        );
      },
    },
    {
      key: "site",
      narrow: "drop",
      header: t("audit.col.site"),
      cell: (e) =>
        e.siteId ? (
          (sites.find((s) => s.id === e.siteId)?.name ?? <span className="mono">{e.siteId}</span>)
        ) : (
          /* Not a missing value. Operators are deployment-wide, so the entry has no
             site and inventing one would be inventing a fact. */
          <Absent hint={t("audit.noSite")} />
        ),
    },
  ];

  return (
    <div className="page">
      <PageHead title={t("audit.title")} description={t("audit.description")} />

      <div style={{ marginBottom: "1rem" }}>
        <Notice level="info" title={t("audit.readOnly.title")}>
          {t("audit.readOnly.body")}
        </Notice>
      </div>

      {/* Each control is wrapped by its own `<label>`, whose only text is the
          visually-hidden name. One accessible name per field, from the element the
          platform already uses for it — an `aria-label` beside a wrapping label is a
          second name that can drift from the first. */}
      <div className="toolbar">
        <label className="field">
          <span className="visually-hidden">{t("audit.filter.actor")}</span>
          <select
            value={actorId}
            onChange={(e) => setParam(params, setParams, "actorId", e.target.value)}
          >
            <option value="">{t("audit.filter.anyActor")}</option>
            {actors.map(([id, username]) => (
              <option key={id} value={id}>
                {username}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="visually-hidden">{t("audit.filter.from")}</span>
          <input
            type="date"
            value={from}
            onChange={(e) => setParam(params, setParams, "from", e.target.value)}
          />
        </label>
        <label className="field">
          <span className="visually-hidden">{t("audit.filter.to")}</span>
          <input
            type="date"
            value={to}
            onChange={(e) => setParam(params, setParams, "to", e.target.value)}
          />
        </label>
      </div>

      <Table
        columns={columns}
        rows={rows}
        rowKey={(e) => e.id}
        rowLabel={(e) => auditCopy(e).what}
        isLoading={query.isLoading}
        error={query.error}
        refreshing={query.isFetching && !query.isLoading}
        updatedAt={query.dataUpdatedAt}
        filtered={filtered}
        onClearFilters={() => setParams(new URLSearchParams(), { replace: true })}
        empty={<Empty title={t("audit.empty.title")} description={t("audit.empty.description")} />}
      />
    </div>
  );
}

function setParam(
  params: URLSearchParams,
  setParams: (next: URLSearchParams, opts: { replace: boolean }) => void,
  key: string,
  value: string,
) {
  const next = new URLSearchParams(params);
  if (value) next.set(key, value);
  else next.delete(key);
  setParams(next, { replace: true });
}
