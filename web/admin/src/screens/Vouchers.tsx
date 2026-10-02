import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo, useRef, useState } from "react";
import { Link, Route, Routes, useNavigate, useParams, useSearchParams } from "react-router";
import type { Voucher, VoucherBatch } from "@foyer/contract";
import { groupVoucherCode } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { duration } from "../format.js";
import { t } from "../i18n/vouchers.js";
import { BatchBar, BatchReport } from "../list/BatchBar.jsx";
import { focusSelectAll, runBatch, type BatchOutcome } from "../list/batch.js";
import { scopeToken } from "../list/selection.js";
import { useSelection } from "../list/useSelection.js";
import { errorCopy, fieldMessages, problemCopy } from "../problems.js";
import { useSite } from "../site.js";
import { Plus, Printer, Search } from "../ui/icons.js";
import { ConfirmDialog, Empty, ErrorNotice, Modal, Notice, Pill, PlanName, type Toast, Toasts } from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Absent, Mono, Stack, Table, TimeCell } from "../ui/table.js";
import { useRefocusAfterRemoval } from "./refocus.js";
import { type SheetState, VoucherSlips } from "../print/VoucherSlips.jsx";

const STATE_META: Record<
  NonNullable<Voucher["state"]>,
  { label: string; level: "positive" | "neutral" | "info" | "negative" | "caution-minor" }
> = {
  unused: { get label() {
    return t("vouchers.state.unused");
  }, level: "info" },
  "in-use": { get label() {
    return t("vouchers.state.inUse");
  }, level: "positive" },
  exhausted: { get label() {
    return t("vouchers.state.exhausted");
  }, level: "neutral" },
  expired: { get label() {
    return t("vouchers.state.expired");
  }, level: "neutral" },
  revoked: { get label() {
    return t("vouchers.state.revoked");
  }, level: "negative" },
};

export function VouchersScreen() {
  return (
    <Routes>
      <Route index element={<BatchList />} />
      <Route path=":batchId" element={<BatchDetail />} />
    </Routes>
  );
}

/* ── batches ──────────────────────────────────────────────────────────────── */

function BatchList() {
  const { siteId } = useSite();
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);

  const query = useQuery({
    queryKey: ["batches", siteId],
    queryFn: () => api.listBatches({ siteId }),
  });

  const columns: Column<VoucherBatch>[] = [
    {
      key: "label",
      narrow: "keep",
      header: t("vouchers.col.batch"),
      cell: (b) => (
        <Stack
          primary={
            <Link className="row-link" to={`/vouchers/${b.id}`} onClick={(e) => e.stopPropagation()}>
              {b.label}
            </Link>
          }
          // Venue-authored, and at a Vietnamese venue it is often Vietnamese inside an
          // English console. Marked rather than translated.
          secondary={b.plan?.name ? <PlanName plan={b.plan} /> : undefined}
        />
      ),
    },
    {
      /*
       * The only quantity on the row, and the only one there was ever a reason to
       * read.
       *
       * `count` was a column of its own — right-aligned, `narrow: "drop"` — printing
       * the number that is already the **denominator here**: `500` beside `164/500`,
       * on every row. Two number columns, one of them redundant, and they disagreed
       * about which edge a number sits on. The narrow rendering had already decided
       * which of the two carries the fact: this one is `keep` and `count` was `drop`.
       *
       * `align: "end"` is what the column doc asks for — a quantity, not a label —
       * and with `count` gone there is nothing left to disagree with.
       */
      key: "usage",
      narrow: "keep",
      align: "end",
      header: t("vouchers.col.used"),
      cell: (b) => {
        const c = b.counts ?? {};
        const used = (c.exhausted ?? 0) + (c.inUse ?? 0);
        return (
          <Stack
            primary={<span className="num">{`${used}/${b.count}`}</span>}
            secondary={
              (c.revoked ?? 0) > 0 ? t("vouchers.revokedCount", c.revoked ?? 0) : undefined
            }
          />
        );
      },
    },
    {
      key: "expiry",
      narrow: "keep",
      header: t("vouchers.col.expiry"),
      cell: (b) => <ExpiryCell batch={b} />,
    },
    { key: "created", narrow: "drop", header: t("vouchers.col.created"), cell: (b) => <TimeCell iso={b.createdAt} /> },
    {
      key: "by",
      narrow: "drop",
      header: t("vouchers.col.createdBy"),
      cell: (b) => b.createdBy ?? <Absent />,
    },
  ];

  return (
    <div className="page">
      <PageHead
        title={t("vouchers.title")}
        description={t("vouchers.description")}
        actions={
          <button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>
            <Plus size={14} />
            {t("vouchers.create")}
          </button>
        }
      />

      <Table
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(b) => b.id}
        rowLabel={(b) => b.label}
        isLoading={query.isLoading}
        error={query.error}
        refreshing={query.isFetching && !query.isLoading}
        updatedAt={query.dataUpdatedAt}
        onRowClick={(b) => navigate(`/vouchers/${b.id}`)}
        empty={
          <Empty
            title={t("vouchers.empty.title")}
            description={t("vouchers.empty.description")}
            action={
              <button type="button" className="btn" onClick={() => setCreating(true)}>
                {t("vouchers.create")}
              </button>
            }
          />
        }
      />

      {creating ? <CreateBatchDialog onClose={() => setCreating(false)} /> : null}
    </div>
  );
}

/**
 * Expiry stated in words, never as a bare date.
 *
 * "Valid until 1 September" and "valid for 24 hours from first use" produce
 * completely different guest experiences, and a guest handed a printed code cannot
 * tell which one applies from a date alone.
 */
function ExpiryCell({ batch }: { batch: VoucherBatch }) {
  if (batch.expiryMode === "from-first-use") {
    const span = validitySpan(batch);
    return (
      <span style={{ color: "var(--text-secondary)" }}>
        {span
          ? t("vouchers.expiry.fromFirstUse", span)
          : t("vouchers.expiry.fromFirstUseUnknown")}
      </span>
    );
  }
  if (!batch.expiresAt) return <Absent />;
  const past = Date.parse(batch.expiresAt) < Date.now();
  /*
   * Expired is a *state*, and it says so in the vocabulary the console keeps states
   * in.
   *
   * Rendering both branches as the same `--text-secondary` run of text would make
   * "Expired 160 days ago" and "Expires in 24 days" differ by one tense of one word —
   * the whole column would read as prose, and the one row whose codes are dead would
   * look exactly like the four whose codes work. That is the inverse of colour spent
   * on the normal case: the deviation left with no mark at all. So the level is
   * *chosen* here, from the status vocabulary, rather than a text token applied to
   * both answers.
   *
   * ── WHY `neutral`, WHICH IS TO SAY WHY NOT AMBER ────────────────────────────
   *
   * `caution-minor` is this console's "a fault requiring attention", and an
   * expired batch is not a fault — it is the designed end of every batch's life.
   * Colouring it would put an amber row in `/vouchers` for ever, growing by one
   * each time a batch runs its course, which is precisely the furniture-colouring
   * the console avoids. So the mark is the **chip**, not the hue: shape separates it
   * from the prose beside it at a glance, and colour stays spent on the rows an
   * operator can still do something about.
   *
   * `neutral` is also the level this fact already carries twice — `STATE_META`
   * for a single expired voucher, and the `Batch expired` notice one screen in.
   * A third voice for one fact would be a defect of its own.
   */
  if (past) {
    return (
      <span style={{ color: "var(--text-secondary)" }}>
        <Pill level="neutral" label={t("vouchers.expiry.expired")} />{" "}
        <TimeCell iso={batch.expiresAt} />
      </span>
    );
  }
  return (
    <span style={{ color: "var(--text-secondary)" }}>
      {t("vouchers.expiry.expires")} <TimeCell iso={batch.expiresAt} />
    </span>
  );
}

/**
 * How long a voucher is good for, in words, or `null` when the plan does not say.
 *
 * `null` is the whole reason this is a function. The previous form was
 * `Math.round(durationMinutes / 60)` with a `?? 1` at the print site, so a plan with
 * no duration printed **“Valid for 1 hour from first use”** onto a physical slip
 * handed to a guest — a limit invented by a default argument, on the one surface
 * Foyer can never retract. A plan that states no duration must produce no duration.
 */
/** The paging loop hit its bound. Distinguished so its own sentence survives. */
class PrintSetTooLarge extends Error {}

/**
 * Why Print is unavailable, in words an operator can act on.
 *
 * Never a raw error string. `PrintSetTooLarge` is this console's own bound and owns
 * its sentence; everything else is a refusal with a `type`, and `errorCopy` is where
 * the sentence for one lives — including for a `fetch` that never arrived, which is
 * classified as `/problems/unreachable` in `api.ts` rather than shown as a `TypeError`.
 */
function printFailureReason(error: unknown): string {
  if (error instanceof PrintSetTooLarge) return error.message;
  return errorCopy(error).body;
}

function validitySpan(batch: VoucherBatch | undefined): string | null {
  const minutes = batch?.plan?.durationMinutes;
  if (!minutes) return null;
  // `duration()` rather than hours-rounded-to-integer: 90 minutes is not "2 hours",
  // and 1440 reads as "1 day" rather than as a number nobody converts in their head.
  return duration(minutes * 60)?.text ?? null;
}

/* ── batch detail ─────────────────────────────────────────────────────────── */

function BatchDetail() {
  const { batchId = "" } = useParams();
  const { siteId, site } = useSite();
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [sheet, setSheet] = useState<SheetState>({ ready: false, reason: null });
  const q = params.get("q") ?? "";
  const states = params.get("state")?.split(",").filter(Boolean) ?? [];

  const batch = useQuery({ queryKey: ["batch", batchId], queryFn: () => api.getBatch(batchId) });
  const vouchers = useQuery({
    queryKey: ["vouchers", siteId, batchId, q, states],
    queryFn: () => api.listVouchers({ siteId, batchId, q, state: states, limit: 200 }),
  });

  /*
   * ── what printing means ──────────────────────────────────────────────────
   *
   * **Printing produces the whole batch, and the table's filters do not touch it.**
   *
   * Mapping the loaded, filtered rows would make three different numbers on one
   * screen: a header saying 500, a table holding the first 200 with nothing saying
   * so, and a printer producing 118. Under the "In use" filter it would produce none
   * at all, with the button fully enabled and silent.
   *
   * Of the three defensible answers — print the batch, print the view and say so,
   * or refuse — this is the first, because a voucher slip is a physical artefact and
   * an operator pressing Print on a batch page means "print this batch". Search and
   * the status chips are how you *find one code*; they are not a print selector, and
   * a tool that quietly reinterprets them as one is how 382 slips go missing.
   *
   * Used, expired and revoked codes are still excluded: a printed slip that cannot
   * be redeemed is worse than no slip. That is a property of the voucher, not of the
   * view, so it is stated on the control rather than inferred from a chip.
   *
   * The count is in the button's own label, so it is on screen *before* the print
   * dialog opens — the point at which an operator stops being able to check.
   */
  const printable = useQuery({
    queryKey: ["vouchers-printable", siteId, batchId],
    queryFn: async () => {
      const out: Voucher[] = [];
      let cursor: string | undefined;
      // The contract paginates by cursor with no total, so the whole batch is a
      // loop. Bounded: the create dialog caps a batch at 2000 and the page size is
      // 200, so ten times that is head-room, not a limit anyone reaches. A loop with
      // no stop hangs on a server bug instead of reporting one.
      for (let i = 0; i < 100; i++) {
        const p = await api.listVouchers({
          siteId,
          batchId,
          state: ["unused"],
          limit: 200,
          cursor,
        });
        out.push(...p.items);
        if (!p.nextCursor) return out;
        cursor = p.nextCursor;
      }
      throw new PrintSetTooLarge(t("vouchers.batch.printTooMany"));
    },
  });

  const revoke = useMutation({
    mutationFn: (id: string) => api.revokeVoucher(id),
    onSuccess: (r) => {
      const failure = r.failure ? problemCopy(r.failure) : null;
      void qc.invalidateQueries({ queryKey: ["vouchers"] });
      void qc.invalidateQueries({ queryKey: ["batch"] });
      // The printable set is a separate query with a separate key, so it does not
      // come along for free — and a revoked code that stays on the print sheet is
      // the defect this screen exists to prevent, one step later.
      void qc.invalidateQueries({ queryKey: ["vouchers-printable"] });
      // The result reports what actually happened, not what was intended:
      // revoking a voucher does not by itself end a live session — that needs a
      // driver call, and it can fail.
      setToasts((current) => [
        ...current,
        {
          id: Date.now(),
          /*
           * Three outcomes, not two. `failure` is the one that matters: the code is
           * dead and **the guest is still online**, which is the whole reason this
           * action reports anything at all. Falling through to `t("vouchers.revoked")`
           * would make a failed revoke say only "Revoked" — a confident answer about a
           * different outcome. The type is what the console keys on, and the sentence
           * for it is written here, never taken from the server.
           */
          message: r.sessionEnded
            ? t("vouchers.revokedAndEnded")
            : (failure?.title ?? t("vouchers.revoked")),
          detail: failure?.body,
        },
      ]);
    },
    /* The other failure this action has: the revoke itself. `onSuccess` covers a
       `200` that reports a failed session end; a `409` or a dropped connection never
       reached it, and said nothing at all. */
    onError: (e) =>
      setToasts((current) => [
        ...current,
        { id: Date.now(), message: errorCopy(e).title, detail: errorCopy(e).body },
      ]),
  });

  const rows = vouchers.data?.items ?? [];
  /* Revoking does not remove the row — it removes the row's only control, which
     costs the operator their place in exactly the same way. Same rule, no special
     case: the scan walks on to the next row that still has one. `refocus.ts`. */
  const armRefocus = useRefocusAfterRemoval(rows);

  /* ── revoking a set of codes ────────────────────────────────────────────── */

  /*
   * **The one screen in the console where a batch is the unit of work.** Codes are
   * created in batches, printed in batches and handed out in batches; a stack of
   * slips that went to the wrong event is revoked in a batch or one at a time for
   * forty minutes.
   *
   * Gated on `vouchers:manage` through `useCan` — never on the role, and never on
   * `ROLE_PERMISSIONS`, which is the server's derivation and drifts from the server's
   * answer (`auth.tsx`). It is the *selection* that is gated, not just the button: a
   * checkbox column above a bar that cannot render is furniture with a tab stop.
   */
  const canManage = useCan("vouchers:manage");

  /*
   * `revoked` and `expired` rows have no Revoke control, so they get no checkbox, so
   * they are not in the selectable set — one predicate feeding both, or `Shift`-extend
   * puts back exactly what the missing checkbox left out.
   */
  const revocable = (v: Voucher) => v.state !== "revoked" && v.state !== "expired";
  const revocableKeys = useMemo(() => rows.filter(revocable).map((v) => v.id), [rows]);
  const scope = scopeToken([siteId, batchId, q, states.join(",")]);
  const picked = useSelection(scope, revocableKeys, !vouchers.isLoading);

  const [confirmBatch, setConfirmBatch] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [report, setReport] = useState<BatchOutcome | null>(null);
  const tableRef = useRef<HTMLDivElement>(null);

  const selected = rows.filter((v) => picked.checked.has(v.id));

  /*
   * **A confirmation, where one code has none — the escalation runs the other way.**
   *
   * A single revoke is deliberately frictionless: one code is cheap to reissue, and
   * the toast reports what happened. A set of forty is not one code forty times.
   * There is no un-revoke operation in the contract, the slips are already printed
   * and in guests' hands, and the mis-press is a select-all away. A batch of
   * irreversible actions is *more* dangerous than one, not less.
   *
   * `confirm`, not `type-name`, and that is the deliberate half. The typed-name tier
   * proves *which record* — it exists for `/accounts`, where two rows look identical
   * and the wrong one destroys a sign-in history. A set has no name to type: the
   * batch label is the page heading, so typing it proves an operator can read the
   * top of the screen, not that they know which forty are ticked. What they need
   * checked is the count and what it costs, and both are in the consequence.
   */
  const runRevoke = async () => {
    setReport(null);
    setProgress(0);
    const outcome = await runBatch(
      selected.map((v) => ({
        key: v.id,
        label: groupVoucherCode(v.code),
        /* A `200` carrying a `Failure` is the third outcome: the code is dead and the
           guest is still online. Returning it makes it a *note* rather than a
           failure — the write succeeded, and retrying it does nothing. */
        run: async () => (await api.revokeVoucher(v.id)).failure,
      })),
      setProgress,
    );
    setProgress(null);
    setConfirmBatch(false);
    setReport(outcome.failed.length > 0 || outcome.noted.length > 0 ? outcome : null);

    void qc.invalidateQueries({ queryKey: ["vouchers"] });
    void qc.invalidateQueries({ queryKey: ["batch"] });
    /* The print sheet is a separate query with a separate key. A revoked code left on
       it is this screen's own recorded defect, one step later. */
    void qc.invalidateQueries({ queryKey: ["vouchers-printable"] });

    const done = outcome.total - outcome.failed.length;
    if (done > 0) {
      setToasts((current) => [...current, { id: Date.now(), message: t("bulk.vouchers.done", done) }]);
    }
    /* The rows that failed stay ticked, so the same press retries exactly them. When
       none did, the bar goes — and the button focus was standing on goes with it. */
    picked.keep(outcome.failed.map((f) => f.key));
    if (outcome.failed.length === 0) focusSelectAll(tableRef.current);
  };

  const columns: Column<Voucher>[] = [
    {
      key: "code",
      narrow: "keep",
      header: t("vouchers.col.code"),
      cell: (v) => (
        // Grouped for display, ungrouped on the wire. The alphabet excludes
        // 0/O/1/I/L because this is read off a receipt and typed on a phone.
        <Mono>{groupVoucherCode(v.code)}</Mono>
      ),
    },
    {
      key: "state",
      narrow: "keep",
      header: t("vouchers.col.state"),
      cell: (v) => {
        const meta = STATE_META[v.state ?? "unused"];
        return <Pill level={meta.level} label={meta.label} />;
      },
    },
    {
      key: "firstUsed",
      narrow: "drop",
      header: t("vouchers.col.firstUsed"),
      cell: (v) => <TimeCell iso={v.firstUsedAt} absent={t("vouchers.firstUsed.absent")} />,
    },
    {
      key: "expires",
      narrow: "drop",
      header: t("vouchers.col.expires"),
      cell: (v) => <TimeCell iso={v.expiresAt} absent={t("vouchers.expires.absent")} />,
    },
    {
      key: "actions",
      narrow: "keep",
      header: "",
      align: "end",
      width: "1%",
      /*
       * Raised in the gate-3 review as a control with no effect. It is not, and the
       * distinction is worth writing down because it will be raised again.
       *
       * Revoking does two things: it stops further redemption, and it **ends any
       * session the voucher is currently carrying**. `exhausted` means no redemption
       * is left, not that nobody is online — the `revoked mid-session` fixture is
       * exactly that case — so Revoke on an exhausted voucher is the only way to put
       * a guest off the network, and it stays.
       *
       * `revoked` is genuinely a no-op and is hidden. `expired` is hidden too, and
       * that one is the questionable half: a voucher redeemed before its expiry can
       * still have a live session after it, and an operator looking at that row has
       * no control at all. Left as it is rather than guessed at — whether an expired
       * voucher can carry a session is a backend semantic that does not exist yet.
       */
      cell: (v) =>
        v.state === "revoked" || v.state === "expired" ? null : (
          <button
            type="button"
            className="btn btn-sm btn-ghost btn-danger"
            onClick={(e) => {
              e.stopPropagation();
              armRefocus(e.currentTarget);
              // Low-friction destructive action with a reported outcome, not a
              // confirmation dialog. A voucher is cheap to reissue.
              revoke.mutate(v.id);
            }}
          >
            {t("vouchers.revoke")}
          </button>
        ),
    },
  ];

  const b = batch.data;

  const slips = printable.data ?? [];
  /* The sheet says when it can print: it needs the venue's network name and
     language, and a slip printed before they arrive is an English slip with no QR. */
  const printReady = printable.isSuccess && slips.length > 0 && sheet.ready;
  /* A reason, always — including when the control is enabled, because "what does
     Print actually print" is the question this screen was answering wrongly. */
  const printReason = printable.isLoading
    ? t("vouchers.batch.printCounting")
    : printable.isError
      ? printFailureReason(printable.error)
      : slips.length === 0
        ? t("vouchers.batch.printNothing")
        : !sheet.ready
          ? (sheet.reason ?? t("vouchers.batch.printCounting"))
          : t("vouchers.batch.printWhat");

  /* The table shows one page. Saying which page is the difference between a list
     and a list that lies by omission. */
  const truncated = Boolean(vouchers.data?.nextCursor);
  const filteredView = q !== "" || states.length > 0;

  return (
    <div className="page">
      <PageHead
        title={b?.label ?? t("vouchers.batch.fallbackTitle")}
        description={
          b ? (
            <>
              {t("vouchers.batch.summary", b.count)}
              {b.plan?.name ? (
                <>
                  {" · "}
                  <PlanName plan={b.plan} />
                </>
              ) : null}
            </>
          ) : undefined
        }
        actions={
          <button
            type="button"
            className="btn"
            disabled={!printReady}
            title={printReason}
            onClick={() => window.print()}
          >
            <Printer size={14} />
            {printable.isSuccess ? t("vouchers.batch.print", slips.length) : t("vouchers.batch.printIdle")}
          </button>
        }
      />

      {/* Stated standing, not in a tooltip, and only where the two numbers actually
          diverge — an operator looking at a complete, unfiltered batch is not being
          misled and does not need telling. */}
      {printable.isSuccess && (filteredView || truncated) ? (
        <div className="no-print" style={{ marginBottom: "1rem" }}>
          <Notice level="info" title={t("vouchers.batch.printScope.title", slips.length)}>
            {t("vouchers.batch.printScope.body")}
          </Notice>
        </div>
      ) : null}

      {/*
        The print sheet, and the controls that decide what it prints. CSS only — no
        PDF library and no server round-trip. Its QR encoder and guest copy load
        lazily; `print/VoucherSlips.tsx` has what a slip carries and `print/kit.ts`
        why the split is where it is.
      */}
      <VoucherSlips siteId={siteId} siteName={site?.name} batch={b} slips={slips} onState={setSheet} />

      {b && b.expiryMode === "from-creation" && b.expiresAt && Date.parse(b.expiresAt) < Date.now() ? (
        <div style={{ marginBottom: "1rem" }}>
          <Notice level="neutral" title={t("vouchers.batch.expired.title")}>
            {t("vouchers.batch.expired.body")}
          </Notice>
        </div>
      ) : null}

      <div className="toolbar no-print">
        <label className="search">
          <Search size={14} />
          <input
            type="search"
            placeholder={t("vouchers.batch.search")}
            defaultValue={q}
            onChange={(e) => {
              const next = new URLSearchParams(params);
              if (e.target.value) next.set("q", e.target.value);
              else next.delete("q");
              setParams(next, { replace: true });
            }}
          />
          <span className="visually-hidden">{t("vouchers.batch.searchLabel")}</span>
        </label>
        <div className="chip-group">
          {Object.entries(STATE_META).map(([value, meta]) => (
            <button
              key={value}
              type="button"
              className="chip"
              aria-pressed={states.includes(value)}
              onClick={() => {
                const next = states.includes(value)
                  ? states.filter((s) => s !== value)
                  : [...states, value];
                const p = new URLSearchParams(params);
                if (next.length) p.set("state", next.join(","));
                else p.delete("state");
                setParams(p, { replace: true });
              }}
            >
              {meta.label}
            </button>
          ))}
        </div>

        <div className="toolbar-right">
          {/* "0 rows" is what this said during the first load and during an outage.
              Neither is a count. */}
          <span className="toolbar-count">
            {vouchers.isLoading
              ? t("common.loading")
              : vouchers.isError
                ? t("table.rowsUnavailable")
                : truncated
                  ? t("vouchers.batch.listTruncated", rows.length)
                  : t("table.rows", rows.length)}
          </span>
        </div>
      </div>

      <div className="no-print">
        <BatchReport outcome={report} notedTitle={(n) => t("bulk.noted.title", n)} />
      </div>

      <div className="no-print" ref={tableRef}>
        <Table
          columns={columns}
          rows={rows}
          rowKey={(v) => v.id}
          rowLabel={(v) => groupVoucherCode(v.code)}
          isLoading={vouchers.isLoading}
          error={(vouchers.error as Error) ?? null}
          refreshing={vouchers.isFetching && !vouchers.isLoading}
          updatedAt={vouchers.dataUpdatedAt}
          filtered={q !== "" || states.length > 0}
          onClearFilters={() => setParams(new URLSearchParams(), { replace: true })}
          selection={
            canManage
              ? {
                  checked: picked.checked,
                  onToggle: picked.onToggle,
                  onToggleAll: picked.onToggleAll,
                  selectable: revocable,
                  rowLabel: (v) => t("bulk.selectVoucher", groupVoucherCode(v.code)),
                  allLabel: t("bulk.selectAll"),
                }
              : undefined
          }
          empty={<Empty title={t("vouchers.batch.empty.title")} />}
        />
      </div>

      {canManage && picked.count > 0 ? (
        <BatchBar count={picked.count} onClear={picked.clear}>
          {/*
            `aria-disabled` and a guarded handler, never `disabled`. This is the
            control the operator is standing on when the batch starts, and setting the
            DOM attribute on a focused element blurs it to `<body>` with nothing to put
            it back — `pending-does-not-eat-focus`, fourteen controls, same shape.
          */}
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
              : t("bulk.vouchers.revoke", picked.count)}
          </button>
        </BatchBar>
      ) : null}

      {/* Mounted always, `open` toggled — a conditionally mounted `ConfirmDialog` is
          removed from the document while still open, so the `close()` that hands focus
          back never runs. `confirm-dialog-focus-return` carries that asymmetry. */}
      <ConfirmDialog
        open={confirmBatch}
        tier="confirm"
        title={t("bulk.vouchers.confirm.title")}
        verb={t("bulk.vouchers.confirm.verb")}
        busy={progress !== null}
        consequence={t("bulk.vouchers.confirm.consequence", picked.count)}
        recovery={t("bulk.vouchers.confirm.recovery")}
        onConfirm={() => void runRevoke()}
        onCancel={() => setConfirmBatch(false)}
      />

      <Toasts
        toasts={toasts}
        onDismiss={(id) => setToasts((current) => current.filter((x) => x.id !== id))}
      />
    </div>
  );
}

/* ── create ───────────────────────────────────────────────────────────────── */

/**
 * Creating a batch, and the part that is not a form.
 *
 * A batch is redeemable on **any** access point in the site, so "who applies this
 * limit" has no single answer — which is why a batch rate cannot be qualified with
 * the session dock's mechanism. It needs this one: the **weakest**
 * enabled device decides what a guest actually gets, and the dialog names the
 * devices that will ignore what is being typed, before it is typed.
 *
 * The rate fields are hidden where **no** device in the site can apply one, and kept
 * with the price stated where only some can — the narrow reading, again. The data
 * allowance is never hidden: a device that cannot enforce it does not make it
 * impossible, only *sampled*, and a sampled cap with its overshoot stated is a real
 * thing an operator may want.
 *
 * `enforcedBy` is not sent: a literal `"nas"` on every batch would be the client
 * asserting a fact about hardware it has never looked at. The request is a
 * `PlanRequest`, and the server derives it.
 */
function CreateBatchDialog({ onClose }: { onClose: () => void }) {
  const { siteId } = useSite();
  const qc = useQueryClient();
  const navigate = useNavigate();
  /* Announced as "dialog" and nothing else until this existed. See `CreateAccountDialog`. */
  const titleId = useId();
  const [label, setLabel] = useState("");
  const [count, setCount] = useState(40);
  const [hours, setHours] = useState(1);
  const [dataMb, setDataMb] = useState("");
  const [downKbps, setDownKbps] = useState("");
  const [upKbps, setUpKbps] = useState("");
  const [expiryMode, setExpiryMode] = useState<VoucherBatch["expiryMode"]>("from-first-use");

  const nas = useQuery({ queryKey: ["nas", siteId, ""], queryFn: () => api.listNas({ siteId }) });
  const devices = (nas.data?.items ?? []).filter((n) => n.enabled);
  const noRate = devices.filter((n) => !n.capabilities.bandwidthLimits);
  const noQuota = devices.filter((n) => !n.capabilities.hardQuota);
  const anyRate = devices.some((n) => n.capabilities.bandwidthLimits);
  const names = (ns: typeof devices) => ns.map((n) => n.name).join(", ");
  const num = (v: string) => (v.trim() === "" ? null : Number(v));

  const create = useMutation({
    mutationFn: () =>
      api.createBatch({
        siteId,
        label,
        count,
        expiryMode,
        validForHours: hours,
        plan: {
          name: label || t("vouchers.createDialog.planFallback"),
          durationMinutes: hours * 60,
          dataAllowanceMb: num(dataMb),
          downKbps: anyRate ? num(downKbps) : null,
          upKbps: anyRate ? num(upKbps) : null,
        },
      }),
    onSuccess: (b) => {
      void qc.invalidateQueries({ queryKey: ["batches"] });
      onClose();
      navigate(`/vouchers/${b.id}`);
    },
  });

  const fieldErrors = fieldMessages(create.error);

  return (
    <Modal labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {t("vouchers.createDialog.title")}
          </h2>
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            /* An `aria-disabled` submit still submits, and Enter in a field submits
               without touching the button at all — so the guard lives here. */
            if (create.isPending) return;
            create.mutate();
          }}
        >
          <div className="dialog-body">
            {/* A refusal in here had nowhere to be: the page behind an open dialog is
                inert, so a `422` left this form byte-for-byte identical — no notice,
                no field marks, and the operator pressing Create again. */}
            {create.error ? (
              <ErrorNotice error={create.error} title={t("vouchers.createDialog.failed.title")} />
            ) : null}
            <div className="form-grid">
              <div className="field">
                <label htmlFor="b-label">{t("vouchers.createDialog.label")}</label>
                <input
                  id="b-label"
                  value={label}
                  aria-invalid={Boolean(fieldErrors.label) || undefined}
                  aria-describedby={fieldErrors.label ? "b-label-error" : "b-label-hint"}
                  onChange={(e) => setLabel(e.target.value)}
                />
                {fieldErrors.label ? (
                  <p className="field-error" role="alert" id="b-label-error">
                    {fieldErrors.label}
                  </p>
                ) : (
                  <p className="field-hint" id="b-label-hint">
                    {t("vouchers.createDialog.labelHint")}
                  </p>
                )}
              </div>
              <div className="field">
                <label htmlFor="b-count">{t("vouchers.createDialog.count")}</label>
                <input
                  id="b-count"
                  type="number"
                  min={1}
                  max={2000}
                  value={count}
                  aria-invalid={Boolean(fieldErrors.count) || undefined}
                  aria-describedby={fieldErrors.count ? "b-count-error" : undefined}
                  onChange={(e) => setCount(Number(e.target.value))}
                />
                {fieldErrors.count ? (
                  <p className="field-error" role="alert" id="b-count-error">
                    {fieldErrors.count}
                  </p>
                ) : null}
              </div>
              <div className="field">
                <label htmlFor="b-hours">{t("vouchers.createDialog.hours")}</label>
                <input
                  id="b-hours"
                  type="number"
                  min={1}
                  value={hours}
                  onChange={(e) => setHours(Number(e.target.value))}
                />
              </div>
              <div className="field">
                <label htmlFor="b-data">{t("vouchers.createDialog.data")}</label>
                <input
                  id="b-data"
                  type="number"
                  min={1}
                  value={dataMb}
                  onChange={(e) => setDataMb(e.target.value)}
                />
                <p className="field-hint">{t("vouchers.createDialog.dataUnit")}</p>
              </div>

              {/*
                Hidden entirely where no device in the site can apply a rate: the
                control would not be merely disabled, it would be a promise nothing
                in the building can keep. Where *some* can, it stays and the price is
                stated below — a driver that cannot apply one makes the limit
                ineffective there, not impossible here.
              */}
              {anyRate ? (
                <>
                  <div className="field">
                    <label htmlFor="b-down">{t("vouchers.createDialog.rateDown")}</label>
                    <input
                      id="b-down"
                      type="number"
                      min={1}
                      value={downKbps}
                      onChange={(e) => setDownKbps(e.target.value)}
                    />
                    <p className="field-hint">{t("vouchers.createDialog.rateUnit")}</p>
                  </div>
                  <div className="field">
                    <label htmlFor="b-up">{t("vouchers.createDialog.rateUp")}</label>
                    <input
                      id="b-up"
                      type="number"
                      min={1}
                      value={upKbps}
                      onChange={(e) => setUpKbps(e.target.value)}
                    />
                    <p className="field-hint">{t("vouchers.createDialog.rateUnit")}</p>
                  </div>
                </>
              ) : null}

              <div className="field">
                <label htmlFor="b-expiry">{t("vouchers.createDialog.expiry")}</label>
                <select
                  id="b-expiry"
                  value={expiryMode}
                  onChange={(e) => setExpiryMode(e.target.value as VoucherBatch["expiryMode"])}
                >
                  <option value="from-first-use">{t("vouchers.createDialog.fromFirstUse")}</option>
                  <option value="from-creation">{t("vouchers.createDialog.fromCreation")}</option>
                </select>
                <p className="field-hint">{t("vouchers.createDialog.expiryHint")}</p>
              </div>
            </div>

            {/*
              Before the button. Each of these is a consequence of the site's
              hardware that an operator cannot see from this screen, and every one of
              them is discovered otherwise as "the limit did nothing".
            */}
            {devices.length === 0 ? (
              <Notice level="warning" title={t("vouchers.createDialog.noDevices")} />
            ) : null}
            {!anyRate && devices.length > 0 ? (
              <Notice level="caution-minor" title={t("vouchers.createDialog.rateNowhere")} />
            ) : null}
            {anyRate && noRate.length > 0 && (downKbps || upKbps) ? (
              <Notice
                level="warning"
                title={t("vouchers.createDialog.rateIgnored", names(noRate))}
              />
            ) : null}
            {dataMb && noQuota.length > 0 ? (
              <Notice
                level="caution-minor"
                title={t("vouchers.createDialog.dataSampled", names(noQuota))}
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
              {t("vouchers.createDialog.submit", count)}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}
