import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo, useRef, useState } from "react";
import type {
  GardenDriverCoverage,
  GardenEntry,
  GardenEntryKind,
  WalledGarden,
} from "@foyer/contract";
import { honoursGardenEntry } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { t } from "../i18n/garden.js";
import { BatchBar, BatchReport } from "../list/BatchBar.jsx";
import { focusSelectAll, runBatch, type BatchOutcome } from "../list/batch.js";
import { scopeToken } from "../list/selection.js";
import { useSelection } from "../list/useSelection.js";
import { fieldMessages } from "../problems.js";
import { useSite } from "../site.js";
import { Alert, Plus } from "../ui/icons.js";
import { ConfirmDialog, Empty, ErrorNotice, Modal, Notice, Pill } from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Absent, Mono, Stack, Table, TimeCell } from "../ui/table.js";
import { useRefocusAfterRemoval } from "./refocus.js";

/**
 * The walled garden — what a guest may reach before they sign in.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Not a feature. A precondition.** `docs/rules/security.md` §5: the garden must
 * contain the portal's own FQDN, its certificate chain's OCSP/CRL endpoints and the
 * RFC 8908 endpoint, *or the portal cannot serve itself* — and that failure reaches
 * a guest as a network that is simply broken. Those entries are marked `required`
 * and have **no delete control at all**, with the server refusing as a second gate.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The hard part of this screen is that a site is not one device.**
 *
 * Capability gating everywhere else in the console is per access point, because
 * every other control acts on one. A garden is a *site* property and the site
 * contains devices with different capabilities — so the honest rendering is not one
 * verdict but a per-entry count of what actually enforces it, and a site-level
 * statement pinned to the **weakest** device.
 *
 * Weakest, not best. A site is only as enclosed as its most permissive device: one
 * driver reporting `GardenKind: "none"` means a guest associated to that radio
 * reaches the whole internet without signing in, whatever the other eleven do.
 * Reporting the best case is how an operator concludes a garden is protecting
 * something it is not, so `effectiveKind` comes from the server, derived from the
 * devices rather than stored beside them.
 *
 * This is also why the entries are not gated behind `<IfCapable>`. The narrow
 * reading applies: a driver that cannot garden makes the entry
 * *ineffective on that device*, not *impossible for the site*. Hiding the screen
 * would leave a mixed site unable to configure the devices that can. So the control
 * stays and the price is stated beside it — before the operator commits, in the add
 * dialog, and after, in the table.
 */
/**
 * Why a set of devices will not enforce an entry — split by *which* reason.
 *
 * There are two, they need different fixes, and merging them into "cannot restrict
 * pre-sign-in traffic" would be wrong. That is true of a driver
 * reporting `none` and **false** of an `ip-range` driver, which restricts pre-sign-in
 * traffic perfectly well and simply cannot match a hostname. Telling an operator
 * their MikroTik cannot garden sends them shopping; telling them it needs a CIDR
 * gets the entry working in a minute.
 *
 * Prefer the narrower reading: the true claim is narrower than the broad sentence
 * that comes to mind first.
 */
function ignoredReasons(entry: GardenEntryKind, drivers: readonly GardenDriverCoverage[]) {
  const ignoring = drivers.filter((d) => !honoursGardenEntry(d.walledGarden, entry));
  return {
    /** Cannot restrict anything before sign-in. */
    none: ignoring.filter((d) => d.walledGarden === "none"),
    /** Can restrict, but only by address — so a name entry does nothing. */
    byAddressOnly: ignoring.filter((d) => d.walledGarden !== "none"),
    all: ignoring,
  };
}

const names = (ds: readonly GardenDriverCoverage[]) => ds.map((d) => d.name).join(", ");

/** The two sentences, joined only where both apply. */
function ignoredTitle(entry: GardenEntryKind, drivers: readonly GardenDriverCoverage[]) {
  const { none, byAddressOnly } = ignoredReasons(entry, drivers);
  return [
    none.length > 0 ? t("garden.ignoredNoneBy", names(none)) : null,
    byAddressOnly.length > 0 ? t("garden.ignoredByNameBy", names(byAddressOnly)) : null,
  ]
    .filter(Boolean)
    .join(" ");
}

export function GardenScreen() {
  const { siteId } = useSite();
  const qc = useQueryClient();
  /* `createGardenEntry` and `deleteGardenEntry` are `garden:manage`; reading the
     garden is `garden:read`, which staff have — and the site-level verdict below is
     the thing they most need to be able to see and report. */
  const canManage = useCan("garden:manage");
  const [adding, setAdding] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<GardenEntry | null>(null);

  const query = useQuery({
    queryKey: ["garden", siteId],
    queryFn: () => api.getWalledGarden(siteId),
  });

  const del = useMutation({
    mutationFn: (entryId: string) => api.deleteGardenEntry(siteId, entryId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["garden"] });
      setConfirmDelete(null);
    },
  });

  const garden = query.data;
  const drivers = garden?.drivers ?? [];
  const rows = garden?.entries ?? [];
  /*
   * The query failed and there is nothing cached to fall back on.
   *
   * Every verdict on this screen is of the form `garden?.effectiveKind === …`, which
   * collapses to the *reassuring* branch when `garden` is `undefined` — so with the
   * backend down the urgent "Nothing is enforcing a walled garden in this site"
   * disappeared while the permanent caution stayed, and **a failing /garden looked
   * calmer than a healthy one**. It is the dashboard's recorded
   * *absence is not zero* defect, one screen along, and it is carried explicitly here
   * for the same reason it is carried into every dashboard card.
   *
   * `!garden` rather than `query.isError` alone: a refetch that fails while entries
   * are on screen must not replace them — the table's *Last updated* line carries
   * that case, and saying "unknown" over rows an operator can read would be its own
   * kind of lie.
   */
  const unreachable = query.isError && !garden;
  /* See `refocus.ts`: the trigger is gone by the time the row is, so the landing
     target is armed at the press. */
  const armRefocus = useRefocusAfterRemoval(rows);

  /* ── clearing out a list of destinations ────────────────────────────────── */

  /*
   * **The screen that proves the gate is real.** `garden:manage` is the one
   * permission a staff member does not hold — they read the garden, and this whole
   * surface is absent for them: no checkbox column, no bar, no confirmation. That is
   * `canManage` above, already deciding the actions column, and it is read through
   * `useCan` rather than from the role.
   *
   * If every action in a bar is gated away the bar must not render at all. Here that
   * falls out of there being one action: an operator who cannot remove an entry is
   * not offered a way to select seven of them.
   */

  /*
   * A required entry has no delete control — the portal's own FQDN and its OCSP
   * endpoints, which an operator hunting for the setting that removes them would be
   * hunting for a way to break their own portal. So it gets no checkbox either, and
   * the same predicate feeds the selectable set: otherwise select-all reports seven,
   * *Remove 7* removes five, and the two that stayed look like a failure nobody
   * reported.
   */
  const removable = (e: GardenEntry) => e.origin !== "required";
  const removableKeys = useMemo(() => rows.filter(removable).map((e) => e.id), [rows]);
  const picked = useSelection(scopeToken([siteId]), removableKeys, !query.isLoading);

  const [confirmBatch, setConfirmBatch] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [report, setReport] = useState<BatchOutcome | null>(null);
  const tableRef = useRef<HTMLDivElement>(null);

  /*
   * The same tier as the single delete, with the count as the fact being confirmed.
   *
   * Not escalated to `type-name`: removing an entry is recoverable — it can be added
   * back, and the dialog says so — which is exactly the property that tier is *not*
   * for. It is not dropped below `confirm` either, because the single one is there
   * and a batch is never less protected than the action it repeats.
   */
  const runRemove = async () => {
    const targets = rows.filter((e) => picked.checked.has(e.id));
    setReport(null);
    setProgress(0);
    const outcome = await runBatch(
      targets.map((e) => ({
        key: e.id,
        label: e.value,
        run: () => api.deleteGardenEntry(siteId, e.id),
      })),
      setProgress,
    );
    setProgress(null);
    setConfirmBatch(false);
    setReport(outcome.failed.length > 0 ? outcome : null);
    void qc.invalidateQueries({ queryKey: ["garden"] });

    /* No success toast, and that matches the single delete beside it: the rows
       leaving the table *is* the confirmation, and the table's own live region names
       the new count for anyone not watching it happen. A toast repeating a fact the
       screen already carries is one an operator learns to dismiss unread. */
    /* The rows that failed are still on screen and stay ticked, so the same press
       retries exactly them. The rest are gone and `reconcile` would drop them anyway;
       doing it here means the bar is right on the render before the refetch lands. */
    picked.keep(outcome.failed.map((f) => f.key));
    if (outcome.failed.length === 0) focusSelectAll(tableRef.current);
  };

  const allColumns: Column<GardenEntry>[] = [
    {
      key: "value",
      narrow: "keep",
      header: t("garden.col.destination"),
      /*
       * `Required` is a property of the entry, so it is said **here**, once.
       *
       * Not again in the action column, at exactly the x-position `Delete` occupies on
       * every other row: muted grey text where a control belongs is what a *disabled*
       * control looks like, so the console would appear to be withholding a delete it
       * would give back under some condition. There is no delete control at all for
       * these, which is why that cell is empty: there is no action, and an empty cell
       * says so without pretending to be one.
       */
      cell: (e) => (
        <Stack
          primary={<Mono>{e.value}</Mono>}
          secondary={
            e.origin === "required" ? (
              <span title={t("garden.origin.requiredWhy")}>{t("garden.origin.required")}</span>
            ) : undefined
          }
        />
      ),
    },
    {
      key: "kind",
      narrow: "drop",
      /*
       * **A taxonomy, so both values are `neutral`.**
       *
       * This column answers *how does this entry match*, never *is anything wrong*.
       * It rendered `fqdn` green and `ip-range` amber with an alert glyph — so the
       * console accused a working configuration, and on a MikroTik site it accused
       * the only configuration that site can have: a MikroTik restricts pre-sign-in traffic perfectly well and simply cannot
       * match a hostname.
       *
       * The fact the amber was reaching for is real and is said twice already, in
       * both the places where it can be acted on: the site verdict above this table
       * (`garden.effective.ipRange` — a range hosts many services), and the add
       * dialog, which computes the same warning from the kind being chosen *before*
       * the operator commits. A third copy, on every row, forever, is not a warning
       * any more; it is the column's own background colour.
       *
       * The sentences stay as `title`, because *what this kind matches* is exactly
       * what an operator wants from this cell.
       */
      header: t("garden.col.kind"),
      cell: (e) =>
        e.kind === "fqdn" ? (
          <Pill level="neutral" label={t("garden.kind.fqdn")} title={t("garden.kind.fqdnWhy")} />
        ) : (
          <Pill
            level="neutral"
            label={t("garden.kind.ip-range")}
            title={t("garden.kind.ipRangeWhy")}
          />
        ),
    },
    {
      key: "honoured",
      narrow: "keep",
      header: t("garden.col.honoured"),
      /*
       * Computed per entry from the same predicate the drivers themselves are
       * described by, rather than from a stored count. An `fqdn` entry needs
       * `fqdn-sni`; an `ip-range` entry works on either mechanism; `none` honours
       * nothing. A count of zero is the whole point of the column and renders as a
       * named negative state, never as "0".
       */
      cell: (e) => <Honoured entry={e} drivers={drivers} />,
    },
    {
      key: "note",
      narrow: "drop",
      header: t("garden.col.note"),
      /* `Absent`, not `null`. In a `<td>` a bare null is a harmless blank; this
         column is dropped on a narrow viewport, and in the disclosure list the same
         null is a `<dt>` with an empty `<dd>` under it — a term with no definition,
         which reads as a rendering fault rather than as "no note". */
      cell: (e) => (e.note ? <span className="muted">{e.note}</span> : <Absent />),
    },
    {
      key: "added",
      narrow: "drop",
      header: t("garden.col.added"),
      cell: (e) => <TimeCell iso={e.createdAt} />,
    },
    {
      key: "actions",
      narrow: "keep",
      header: "",
      align: "end",
      width: "1%",
      /*
       * No delete control for a required entry, rather than a disabled one. The
       * console's rule is that a disabled control still advertises an action —
       * here the action does not exist, and an operator hunting for the setting
       * that enables it would be hunting for a way to break their own portal.
       *
       * And nothing else either: repeating `Required` as muted text here, in the slot
       * every other row fills with `Delete`, would read as a disabled control rather
       * than as an absent one. The word belongs
       * to the destination column, next to the destination it describes.
       */
      cell: (e) =>
        e.origin === "required" ? null : (
          <button
            type="button"
            className="btn btn-sm btn-ghost btn-danger"
            onClick={(ev) => {
              ev.stopPropagation();
              armRefocus(ev.currentTarget);
              setConfirmDelete(e);
            }}
          >
            {t("common.delete")}
          </button>
        ),
    },
  ];

  /*
   * The actions column leaves with its controls.
   *
   * Blanking each cell instead would keep an empty column and an empty header on
   * every row — dead space that makes a staff console look like a broken
   * administrator console rather than a different one. A control that disappears
   * takes its column with it.
   */
  const columns = allColumns.filter((c) => canManage || c.key !== "actions");

  /*
   * The actions column leaves with its controls.
   *
   * Returning `null` per cell would keep an empty column and an empty header on
   * every row — dead space that says a staff console is a broken administrator
   * console rather than a different one. A control that disappears takes its column
   * with it.
   */

  return (
    <div className="page">
      <PageHead
        title={t("garden.title")}
        description={t("garden.description")}
        actions={
          canManage ? (
            /*
             * The write follows the read.
             *
             * With every request aborted this stayed fully enabled and full-colour
             * over a table reading *Could not load this data*, so an operator could
             * open the dialog and submit against a backend that is not answering.
             *
             * `aria-disabled` and a guarded handler rather than `disabled`, which is
             * the console's pattern for the same reason everywhere else: a control
             * that goes `disabled` while it has focus is blurred to `<body>` by the
             * user agent, with no dialog involved at all (`pending-does-not-eat-
             * focus`). And it is *disabled* rather than removed because the rule for
             * a capability that exists but is currently unavailable is exactly that —
             * disabled, with the reason stated. The reason is `garden.unknown.body`,
             * on screen beside it and not in a tooltip.
             */
            <button
              type="button"
              className="btn btn-primary"
              aria-disabled={unreachable || undefined}
              onClick={() => {
                if (unreachable) return;
                setAdding(true);
              }}
            >
              <Plus size={14} />
              {t("garden.add.open")}
            </button>
          ) : null
        }
      />

      {/*
        The site-level verdict, keyed on the weakest device. `none` is urgent
        because it means the list below does nothing for at least one radio, and
        that is not a degraded state — it is an open network.
      */}
      {/* A site with no devices is not a site with a broken garden. The `none`
          verdict below is derived from the weakest device, and with no devices at
          all it would fire on a site that has nothing to protect yet — an alarm an
          operator cannot act on teaches them to ignore the ones they can. */}
      {/*
        The fourth branch, and the reason the three below it are not enough.

        `garden?.effectiveKind === "none"` is false both when the site is properly
        enclosed and when nothing answered, and those are opposite facts. `role="alert"`
        because it mounts with its text, at the moment something failed.
      */}
      {unreachable ? (
        <div className="stack-sm">
          <Notice level="urgent" title={t("garden.unknown.title")} role="alert">
            {t("garden.unknown.body")} {canManage ? t("garden.unknown.cannotAdd") : null}
          </Notice>
        </div>
      ) : null}

      {garden && drivers.length === 0 ? (
        <div className="stack-sm">
          <Notice level="info" title={t("garden.noDevices.title")}>
            {t("garden.noDevices.body")}
          </Notice>
        </div>
      ) : null}

      {drivers.length > 0 && garden?.effectiveKind === "none" ? (
        <div className="stack-sm">
          <Notice level="urgent" title={t("garden.effective.none.title")}>
            {t("garden.effective.none.body")}
          </Notice>
        </div>
      ) : null}
      {drivers.length > 0 && garden?.effectiveKind === "ip-range" ? (
        <div className="stack-sm">
          <Notice level="warning" title={t("garden.effective.ipRange.title")}>
            {t("garden.effective.ipRange.body")}
          </Notice>
        </div>
      ) : null}

      <BatchReport outcome={report} />

      <div ref={tableRef}>
        <Table
          columns={columns}
          rows={rows}
          rowKey={(e) => e.id}
          rowLabel={(e) => e.value}
          isLoading={query.isLoading}
          error={query.error}
          refreshing={query.isFetching && !query.isLoading}
          updatedAt={query.dataUpdatedAt}
          selection={
            canManage
              ? {
                  checked: picked.checked,
                  onToggle: picked.onToggle,
                  onToggleAll: picked.onToggleAll,
                  selectable: removable,
                  rowLabel: (e) => t("bulk.selectEntry", e.value),
                  allLabel: t("bulk.selectAll"),
                }
              : undefined
          }
          empty={
            <Empty
              title={t("garden.empty.title")}
              description={t("garden.empty.description")}
            />
          }
        />
      </div>

      {canManage && picked.count > 0 ? (
        <BatchBar count={picked.count} onClear={picked.clear}>
          {/* `aria-disabled` and a guarded handler, never `disabled` — the operator is
              standing on this control when the batch starts. */}
          <button
            type="button"
            className="btn btn-sm"
            data-role="batch-action"
            aria-disabled={progress !== null || undefined}
            onClick={() => {
              if (progress !== null) return;
              setConfirmBatch(true);
            }}
          >
            {progress !== null
              ? t("bulk.running", progress, picked.count)
              : t("bulk.garden.remove", picked.count)}
          </button>
        </BatchBar>
      ) : null}

      {/*
        Stated rather than implied. There is no identity before sign-in, so nothing
        bounds what a device sends through these destinations — `docs/rules/security.md` §5
        requires a per-MAC cap and it is not built. Saying so on the screen that
        creates the exposure is cheaper than discovering it as a bill.
      */}
      <div className="stack-sm">
        <Notice level="caution-minor" title={t("garden.preAuthQuota.title")}>
          {t("garden.preAuthQuota.body")}
        </Notice>
      </div>

      {adding ? (
        <AddEntryDialog
          siteId={siteId}
          drivers={drivers}
          onClose={() => setAdding(false)}
          onCreated={() => {
            void qc.invalidateQueries({ queryKey: ["garden"] });
            setAdding(false);
          }}
        />
      ) : null}

      {/* Mounted always, `open` toggled. The same call-site defect as `/controllers`
          had, and the reasoning is written out there: a conditionally mounted
          `ConfirmDialog` is removed from the document while still open, so the
          `el.close()` that hands focus back to the trigger never runs. */}
      {/* Mounted always, for the reason written on its neighbour below. */}
      <ConfirmDialog
        open={confirmBatch}
        tier="confirm"
        title={t("bulk.garden.confirm.title")}
        verb={t("bulk.garden.confirm.verb")}
        busy={progress !== null}
        consequence={t("bulk.garden.confirm.consequence", picked.count)}
        recovery={t("bulk.garden.confirm.recovery")}
        onCancel={() => setConfirmBatch(false)}
        onConfirm={() => void runRemove()}
      />

      <ConfirmDialog
        open={confirmDelete !== null}
        tier="confirm"
        title={t("garden.delete.title")}
        verb={t("garden.delete.verb")}
        consequence={t("garden.delete.consequence", confirmDelete?.value ?? "")}
        recovery={t("garden.delete.recovery")}
        busy={del.isPending}
        error={del.error}
        onCancel={() => setConfirmDelete(null)}
        onConfirm={() => confirmDelete && del.mutate(confirmDelete.id)}
      />
    </div>
  );
}

/**
 * How many of the site's devices actually enforce this entry.
 *
 * Zero is a named state with the ignoring devices listed, not the number 0. An
 * operator reading "0 of 7" has to do the subtraction themselves and will read it as
 * a counter that has not loaded; an operator reading "No device" has been told.
 */
function Honoured({
  entry,
  drivers,
}: {
  entry: GardenEntry;
  drivers: readonly GardenDriverCoverage[];
}) {
  const honours = drivers.filter((d) => honoursGardenEntry(d.walledGarden, entry.kind));
  const { all: ignores } = ignoredReasons(entry.kind, drivers);
  const title = ignoredTitle(entry.kind, drivers);

  if (honours.length === 0) {
    return (
      <Pill
        level="urgent"
        label={t("garden.honouredNone")}
        icon={<Alert size={11} />}
        title={title}
      />
    );
  }
  /*
   * **A count is not a verdict.**
   *
   * Partial coverage was `caution-minor`, which put an amber alert pill on **every
   * row** of the authored site — six reading `4 of 7`, one reading `5 of 7`, and the
   * one that is *better* enforced was the same colour as the six. Four of them are
   * `Required` entries, so the console was flagging the portal's own address as a
   * problem the operator should do something about.
   *
   * It is also the same fact seven times: which devices ignore an entry follows from
   * the entry's *kind*, not from the entry, and the site-level verdict above this
   * table already names it once. So the colour marks the exception — **nothing here
   * enforces this at all**, above — and the rest is a number, read as a number. The
   * number still discriminates `5 of 7` from `4 of 7`, which is more than the amber
   * ever did, and the names of the devices that ignore it stay in the tooltip.
   */
  return (
    <Pill
      level={ignores.length > 0 ? "neutral" : "positive"}
      label={t("garden.honoured", honours.length, drivers.length)}
      title={ignores.length > 0 ? title : undefined}
    />
  );
}

/**
 * Adding an entry, with the price stated before the operator commits.
 *
 * The warning is computed from the kind currently selected, so switching between
 * *Name* and *Address range* changes it live — which is the only way an operator
 * discovers that a site with a `none` driver gains nothing from either choice
 * *before* typing a hostname and pressing a button.
 */
function AddEntryDialog({
  siteId,
  drivers,
  onClose,
  onCreated,
}: {
  siteId: string;
  drivers: readonly GardenDriverCoverage[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const titleId = useId();
  const [kind, setKind] = useState<GardenEntryKind>("fqdn");
  const [value, setValue] = useState("");
  const [note, setNote] = useState("");

  const create = useMutation({
    mutationFn: () =>
      api.createGardenEntry(siteId, {
        kind,
        value: value.trim(),
        ...(note.trim() ? { note: note.trim() } : {}),
      }),
    onSuccess: onCreated,
  });

  const fieldErrors = fieldMessages(create.error);
  const ignored = ignoredReasons(kind, drivers);

  return (
    <Modal labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {t("garden.add.title")}
          </h2>
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (create.isPending) return;
            create.mutate();
          }}
        >
          <div className="dialog-body">
            {create.error ? (
              <ErrorNotice error={create.error} title={t("garden.add.failed.title")} />
            ) : null}

            <div className="form-grid">
              <div className="field">
                <label htmlFor="garden-kind">{t("garden.add.kind")}</label>
                <select
                  id="garden-kind"
                  value={kind}
                  onChange={(e) => setKind(e.target.value as GardenEntryKind)}
                >
                  <option value="fqdn">{t("garden.kind.fqdn")}</option>
                  <option value="ip-range">{t("garden.kind.ip-range")}</option>
                </select>
                <p className="field-hint">
                  {kind === "fqdn" ? t("garden.kind.fqdnWhy") : t("garden.kind.ipRangeWhy")}
                </p>
              </div>

              <div className="field">
                <label htmlFor="garden-value">{t("garden.add.value")}</label>
                <input
                  id="garden-value"
                  value={value}
                  autoComplete="off"
                  aria-invalid={Boolean(fieldErrors.value) || undefined}
                  /* The pair, not half of it. `aria-invalid` on its own says *this
                     is wrong* and points at nothing, so a screen reader lands on a
                     field that announces a problem and never says what it is. */
                  aria-describedby={fieldErrors.value ? "garden-value-error" : "garden-value-hint"}
                  onChange={(e) => setValue(e.target.value)}
                />
                {fieldErrors.value ? (
                  <p className="field-error" role="alert" id="garden-value-error">
                    {fieldErrors.value}
                  </p>
                ) : (
                  <p className="field-hint" id="garden-value-hint">
                    {kind === "fqdn"
                      ? t("garden.add.valueHintFqdn")
                      : t("garden.add.valueHintIpRange")}
                  </p>
                )}
              </div>

              <div className="field">
                <label htmlFor="garden-note">{t("garden.add.note")}</label>
                <input
                  id="garden-note"
                  value={note}
                  autoComplete="off"
                  onChange={(e) => setNote(e.target.value)}
                />
                <p className="field-hint">{t("garden.add.noteHint")}</p>
              </div>
            </div>

            {/* Before the button, not after it — and one notice per reason, because
                an operator can act on "rewrite it as a range" and cannot act on
                "this hardware does not do gardens". */}
            {ignored.none.length > 0 ? (
              <Notice level="warning" title={t("garden.add.ignoredNone", names(ignored.none))} />
            ) : null}
            {ignored.byAddressOnly.length > 0 ? (
              <Notice
                level="caution-minor"
                title={t("garden.add.ignoredByName", names(ignored.byAddressOnly))}
              />
            ) : null}
          </div>

          <div className="dialog-foot">
            <button type="button" className="btn" onClick={onClose}>
              {t("common.cancel")}
            </button>
            {/* `aria-disabled` rather than `disabled`: the operator pressed this, so it
                has focus, and `disabled` on a focused element blurs it to `<body>` for the
                whole request. Measured and written out at `Accounts.tsx`'s row action. */}
            <button
              type="submit"
              className="btn btn-primary"
              aria-disabled={create.isPending || undefined}
            >
              {t("garden.add.submit")}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}

export type { WalledGarden };
