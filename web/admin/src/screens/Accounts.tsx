import { withCopy } from "../i18n/catalogue.js";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import type { Account } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { t } from "../i18n/index.js";
import { BatchBar, BatchReport } from "../list/BatchBar.jsx";
import { runBatch, useRefocusAfterBatch, type BatchOutcome } from "../list/batch.js";
import { scopeToken } from "../list/selection.js";
import { useSelection } from "../list/useSelection.js";
import { errorCopy, fieldMessages } from "../problems.js";
import { useSite } from "../site.js";
import { Alert, Plus, Search } from "../ui/icons.js";
import { ConfirmDialog, Empty, ErrorNotice, Modal, Notice, Pill, type Toast, Toasts } from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Stack, Table, TimeCell } from "../ui/table.js";
import { useRefocusAfterRemoval } from "./refocus.js";

/* The two account dialogs are a lazy chunk (`AccountCredentials.tsx` says why): the
   list stays eager, and what is opened only to add an account or change one's
   credentials loads on the first press. */
const CreateAccountDialog = lazy(() =>
  withCopy(() => import("./AccountCredentials.js")).then((m) => ({ default: m.CreateAccountDialog })),
);
const AccountCredentialsDialog = lazy(() =>
  withCopy(() => import("./AccountCredentials.js")).then((m) => ({ default: m.AccountCredentialsDialog })),
);

/**
 * The credential column's cell, exported so a test can read what an operator
 * reads rather than what the column passes to it.
 *
 * **Two independent opt-ins, two pills, and never one standing in for the other.**
 * This was `argon2id` or else the NT pill, which was right while `credentialKind`
 * had two members. With `argon2id+bcrypt` in the enum that `else` labelled an
 * 802.1X identity *"argon2id + NT hash"* — asserting that password-equivalent
 * material exists where none does, in the one column of this screen whose whole
 * job is to be believed.
 *
 * The levels differ because the risks do, and the difference is measured rather
 * than aesthetic:
 *
 *   `+nt`      an NT hash is PASSWORD-EQUIVALENT. Reading it authenticates.
 *              `warning`, and it stays the loud one on this screen.
 *   `+bcrypt`  a bcrypt verifier is ONE-WAY. Reading it does not authenticate,
 *              which is exactly why `rlm_sql` is allowed to read it at all.
 *              `neutral` — visible, because the operator must know 802.1X is on
 *              for this identity and that the network does not verify against
 *              argon2id, but not amber, because amber spent on a one-way hash is
 *              amber taken from the row that needs it.
 */
export function credentialCell(kind: Account["credentialKind"]) {
  const nt = kind.includes("+nt");
  const bcrypt = kind.includes("+bcrypt");
  if (!nt && !bcrypt) {
    return <span className="muted">{t("accounts.credential.argon2id")}</span>;
  }
  return (
    <span className="pill-row">
      {nt ? (
        <Pill
          level="warning"
          label={t("accounts.credential.nt")}
          icon={<Alert size={11} />}
          title={t("accounts.credential.ntWhy")}
        />
      ) : null}
      {bcrypt ? (
        <Pill
          level="neutral"
          label={t("accounts.credential.bcrypt")}
          title={t("accounts.credential.bcryptWhy")}
        />
      ) : null}
    </span>
  );
}

/**
 * Local accounts.
 *
 * Every account here is argon2id. The one thing this screen has to make
 * unmissable is the difference between that and `argon2id+nt` — an identity opted
 * in to PEAP-MSCHAPv2, which forces the server to store an NT hash. An NT hash is
 * password-equivalent: it is the sole input to the MSCHAPv2 response, so a stolen
 * hash authenticates directly without being cracked. That is the most consequential
 * per-identity decision an operator can make, and it should not look like a
 * checkbox.
 */
export function AccountsScreen() {
  const { siteId } = useSite();
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const [creating, setCreating] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Account | null>(null);
  const [editing, setEditing] = useState<Account | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);

  const query = useQuery({
    queryKey: ["accounts", siteId, q],
    queryFn: () => api.listAccounts({ siteId, q }),
  });

  const del = useMutation({
    mutationFn: (id: string) => api.deleteAccount(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["accounts"] });
      setConfirmDelete(null);
    },
  });

  /*
   * Enable and disable — the actions this screen was missing.
   *
   * The table rendered an Enabled/Disabled pill and offered no way to change it, so
   * the only thing an operator could do to a compromised staff account was destroy
   * the record. That is the escalation backwards: **the reversible action must be
   * the easy one.** Disabling is therefore low-friction — immediate, with undo, no
   * dialog — exactly as revoking a voucher is, and for the same reason: it costs one
   * click to put back.
   *
   * The undo is a second write rather than a rollback, which is the honest shape: the
   * server has already been told, and the toast offers to tell it the opposite.
   */
  const setEnabled = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      api.updateAccount(id, { enabled }),
    onSuccess: (account) => {
      void qc.invalidateQueries({ queryKey: ["accounts"] });
      setToasts((current) => [
        ...current,
        {
          id: Date.now(),
          message: account.enabled
            ? t("accounts.toast.enabled", account.username)
            : t("accounts.toast.disabled", account.username),
          undo: () => setEnabled.mutate({ id: account.id, enabled: !account.enabled }),
        },
      ]);
    },
    /* Without this, a `503` here silently re-enables the button and the pill snaps
       back, which reads as *it did not take* rather than as *the server refused*. A low-friction action still has to report a refusal. */
    onError: (e) =>
      setToasts((current) => [
        ...current,
        { id: Date.now(), message: errorCopy(e).title, detail: errorCopy(e).body },
      ]),
  });

  const rows = query.data?.items ?? [];
  /* Deleting a row destroys the control that had focus, so the platform has nothing
     to hand it back to. One rule for all five destructive surfaces — `refocus.ts`. */
  const armRefocus = useRefocusAfterRemoval(rows);
  /*
   * **Substring, not equality, and that is the whole point.**
   *
   * `credentialKind` names every verifier an identity has, so it is one of four
   * values and two of them contain `+nt`. The equality test that stood here —
   * `=== "argon2id+nt"` — was correct while the enum had two members and became a
   * silent under-count the day it had four: an identity opted in to BOTH
   * MSCHAPv2 and 802.1X is `argon2id+nt+bcrypt`, and the notice above the table
   * would have stopped counting it while the risk was strictly greater.
   */
  const withNt = rows.filter((a) => a.credentialKind.includes("+nt"));
  /*
   * The 802.1X price, put somewhere it cannot be missed.
   *
   * The pill in the row carries it in a `title`, and a `title` is a tooltip: it
   * does not exist on a touch device and it is not read by somebody scanning the
   * table. The price must be visible, and the sentence
   * being priced — *the network verifies against bcrypt, not argon2id* — is a
   * standing fact about the install rather than a per-row status.
   *
   * `info` rather than `warning`, and the distinction is the same one the pills
   * make: the NT notice above is amber because an NT hash is password-equivalent.
   * This one is not a hazard, it is a thing the operator has to know.
   */
  const with8021X = rows.filter((a) => a.credentialKind.includes("+bcrypt"));

  /* ── a shift ends, and eight accounts stop working ──────────────────────── */

  /*
   * **The batch here is disable, and there is deliberately no batch delete.**
   *
   * The single delete is `type-name` — the operator types the username, because two
   * rows look identical and destroying the wrong one destroys a sign-in history that
   * nothing restores. A *set* has no name to type. The two ways out of that are both
   * wrong: drop a batch delete to the `confirm` tier and the batch is protected
   * *less* than the single action it repeats, which is the escalation running
   * backwards; ask for one name out of eight and the check certifies a row the
   * operator did not look at. So the destructive verb stays a one-row action and the
   * batch offers the reversible pair instead.
   *
   * That pair is also its own undo, which is why there is no toast with an Undo on
   * it: the rows do not go anywhere when they are disabled, the selection survives
   * (nothing in `scope` changed and no row was pruned), and **Enable** is standing in
   * the same bar with the same eight still ticked.
   *
   * Gated through `useCan` on `accounts:manage`, the permission the write itself
   * needs — not on the role, and not on `ROLE_PERMISSIONS`.
   */
  const canManage = useCan("accounts:manage");
  const accountKeys = useMemo(() => rows.map((a) => a.id), [rows]);
  const scope = scopeToken([siteId, q]);
  const picked = useSelection(scope, accountKeys, !query.isLoading);

  const [progress, setProgress] = useState<number | null>(null);
  const [report, setReport] = useState<BatchOutcome | null>(null);
  const tableRef = useRef<HTMLDivElement>(null);
  const barRef = useRef<HTMLDivElement>(null);
  const armBarRefocus = useRefocusAfterBatch(rows, barRef);

  /*
   * Only the rows the verb would change. Eight ticked with three already disabled is
   * five writes, and the button says five — sending three no-ops to the server and
   * reporting "8 accounts disabled" is a count of requests dressed up as a count of
   * changes.
   */
  const wouldChange = (enabled: boolean) =>
    rows.filter((a) => picked.checked.has(a.id) && a.enabled !== enabled);

  const runSetEnabled = async (enabled: boolean) => {
    const targets = wouldChange(enabled);
    if (targets.length === 0) return;
    setReport(null);
    setProgress(0);
    const outcome = await runBatch(
      targets.map((a) => ({
        key: a.id,
        label: a.username,
        /* The updated account is discarded on purpose, and the signature is what
           forces the decision: a `run` that resolves to a value is a response this
           console has not read, and on the one operation whose `200` carries a
           reported failure that is the defect `BatchOutcome.noted` exists for.
           `PATCH /accounts/{id}` carries none — the row comes back from the refetch. */
        run: async () => {
          await api.updateAccount(a.id, { enabled });
        },
      })),
      setProgress,
    );
    setProgress(null);
    setReport(outcome.failed.length > 0 ? outcome : null);
    void qc.invalidateQueries({ queryKey: ["accounts"] });

    const done = outcome.total - outcome.failed.length;
    if (done > 0) {
      setToasts((current) => [
        ...current,
        {
          id: Date.now(),
          message: enabled ? t("bulk.accounts.enabled", done) : t("bulk.accounts.disabled", done),
        },
      ]);
    }
    /* Failures stay ticked so the same press retries them; everything else stays
       ticked too, because the inverse verb is what an operator reaches for next and
       it needs the same set. */
    if (outcome.failed.length > 0) picked.keep(outcome.failed.map((f) => f.key));
    /*
     * **The bar survives and the pressed verb does not.** Disabling the last enabled
     * row leaves *Disable* with nothing to do, so it removes itself — taking the
     * focus of the operator who just pressed it, straight to `<body>`. Measured, not
     * reasoned about. Focus lands on the counterpart, which is `refocus.ts`'s rule
     * and also the undo.
     */
    armBarRefocus();
  };

  const columns: Column<Account>[] = [
    {
      key: "username",
      narrow: "keep",
      header: t("accounts.col.username"),
      cell: (a) => (
        <Stack
          primary={<span className="row-link">{a.username}</span>}
          secondary={a.displayName ?? undefined}
          absentText={t("accounts.noDisplayName")}
        />
      ),
    },
    {
      key: "state",
      narrow: "keep",
      header: t("accounts.col.status"),
      cell: (a) =>
        a.enabled ? (
          <Pill level="positive" label={t("accounts.enabled")} />
        ) : (
          <Pill level="neutral" label={t("accounts.disabled")} />
        ),
    },
    {
      key: "credential",
      narrow: "drop",
      header: t("accounts.col.credential"),
      /*
       * The normal case is not a status, so it is not a pill.
       *
       * Every account is `argon2id` unless somebody opted it into MSCHAPv2, so this
       * column's job — stated at the top of this screen — is making `argon2id+nt`
       * **unmissable**. A pill on the default made that harder rather than easier:
       * twenty-two chips of two kinds, where the eye has to read every one to find
       * the two that matter. Twenty quiet text cells beside two amber chips does the
       * same job by contrast instead of by inspection.
       *
       * It was also the same defect as `Page 0.0 kB of 50 kB`, removed the same day:
       * a tick asserting a **pass on the normal case**. Colour and a glyph are spent
       * on the deviation or they are not spent — `docs/rules/frontend.md`, and the
       * quality bar's *a console that colours its own furniture has no colour left
       * for the row that needs attention*.
       */
      /*
       * ── two opt-ins, two pills, and never one standing in for the other ────
       *
       * `argon2id` or else the NT pill would be right only while the enum had two
       * members. With `argon2id+bcrypt` in it, an `else` branch would label an
       * 802.1X identity *"argon2id + NT hash"* — telling an
       * operator that a password-equivalent hash exists where none does, in the
       * one column of this screen whose stated job is to be believed.
       *
       * So each opt-in renders its own pill and both can appear. The levels
       * differ because the risks do, and the difference is measured rather than
       * aesthetic:
       *
       *   +nt      an NT hash is PASSWORD-EQUIVALENT. Reading it authenticates.
       *            `warning`, and it stays the loud one.
       *   +bcrypt  a bcrypt verifier is ONE-WAY. Reading it does not authenticate,
       *            which is exactly why rlm_sql is allowed to read it at all.
       *            `neutral` — visible because the operator must know 802.1X is on
       *            for this identity and that the network path does not verify
       *            against argon2id, but not amber, because amber on a one-way
       *            hash spends the colour this table saves for the NT row.
       */
      cell: (a) => credentialCell(a.credentialKind),
    },
    {
      key: "created",
      narrow: "drop",
      header: t("accounts.col.created"),
      cell: (a) => <TimeCell iso={a.createdAt} />,
    },
    {
      key: "lastLogin",
      narrow: "drop",
      header: t("accounts.col.lastLogin"),
      cell: (a) => <TimeCell iso={a.lastLoginAt} absent={t("accounts.neverSignedIn")} />,
    },
    {
      key: "actions",
      narrow: "keep",
      header: "",
      align: "end",
      width: "1%",
      cell: (a) => (
        <span className="row-actions">
          {/*
            `aria-disabled`, not `disabled`, and this row action is the smallest
            reproduction of why.

            It is the control the operator is standing on: pressing it is what starts
            the request. Setting the DOM `disabled` attribute on the focused element
            makes the user agent blur it to `<body>` with a null `relatedTarget`, and
            nothing puts it back. Instrumented with the request slowed to 2.5 s: focus
            was on `<body>` 600 ms in and still on `<body>` after it settled — the
            operator is nowhere, on a screen where the thing they just did may have
            failed. The element is never removed; React simply disables it.

            `aria-disabled` announces the same unavailability and keeps the element
            focusable, so there is no blur to recover from. The guard in the handler is
            what makes the attribute mean anything, because an `aria-disabled` button
            is still clickable — the attribute is a claim about the control and the
            handler has to honour it. `.btn[aria-disabled="true"]` already renders
            identically to `:disabled` and takes `pointer-events: none`, so only the
            keyboard can still reach it, which is the entire point.

            The same change is made everywhere a control an operator *presses* goes
            unavailable while its own request is in flight. It is deliberately not
            made where `disabled` expresses a precondition the control has never had
            focus under — `Print` before the printable set is counted, `Discover`
            before the controller answers, `Continue` before the warning is
            acknowledged. There `disabled` is also what keeps a useless tab stop out
            of the order, and there is no focus to lose.
          */}
          <button
            type="button"
            className="btn btn-sm"
            aria-disabled={setEnabled.isPending || undefined}
            title={a.enabled ? t("accounts.action.disableWhy") : t("accounts.action.enableWhy")}
            onClick={(e) => {
              e.stopPropagation();
              if (setEnabled.isPending) return;
              setEnabled.mutate({ id: a.id, enabled: !a.enabled });
            }}
          >
            {a.enabled ? t("accounts.action.disable") : t("accounts.action.enable")}
          </button>
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            title={t("accounts.action.credentialsWhy")}
            onClick={(e) => {
              e.stopPropagation();
              setEditing(a);
            }}
          >
            {t("accounts.action.credentials")}
          </button>
          <button
            type="button"
            className="btn btn-sm btn-ghost btn-danger"
            onClick={(e) => {
              e.stopPropagation();
              armRefocus(e.currentTarget);
              setConfirmDelete(a);
            }}
          >
            {t("common.delete")}
          </button>
        </span>
      ),
    },
  ];

  return (
    <div className="page">
      <PageHead
        title={t("accounts.title")}
        description={t("accounts.description")}
        actions={
          <button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>
            <Plus size={14} />
            {t("accounts.add")}
          </button>
        }
      />

      {withNt.length > 0 ? (
        <div style={{ marginBottom: "1rem" }}>
          <Notice level="warning" title={t("accounts.nt.title", withNt.length)}>
            {t("accounts.nt.body")}
          </Notice>
        </div>
      ) : null}

      {with8021X.length > 0 ? (
        <div style={{ marginBottom: "1rem" }}>
          <Notice level="info" title={t("accounts.dot1x.title", with8021X.length)}>
            {t("accounts.dot1x.body")}
          </Notice>
        </div>
      ) : null}

      <div className="toolbar">
        <label className="search">
          <Search size={14} />
          <input
            type="search"
            placeholder={t("accounts.search")}
            defaultValue={q}
            onChange={(e) => {
              const next = new URLSearchParams(params);
              if (e.target.value) next.set("q", e.target.value);
              else next.delete("q");
              setParams(next, { replace: true });
            }}
          />
          <span className="visually-hidden">{t("accounts.searchLabel")}</span>
        </label>
      </div>

      <BatchReport outcome={report} />

      <div ref={tableRef}>
      <Table
        columns={columns}
        rows={rows}
        rowKey={(a) => a.id}
        rowLabel={(a) => a.username}
        isLoading={query.isLoading}
        error={query.error}
        refreshing={query.isFetching && !query.isLoading}
        updatedAt={query.dataUpdatedAt}
        filtered={q !== ""}
        onClearFilters={() => setParams(new URLSearchParams(), { replace: true })}
        selection={
          canManage
            ? {
                checked: picked.checked,
                onToggle: picked.onToggle,
                onToggleAll: picked.onToggleAll,
                rowLabel: (a) => t("bulk.selectAccount", a.username),
                allLabel: t("bulk.selectAll"),
              }
            : undefined
        }
        empty={
          <Empty
            title={t("accounts.empty.title")}
            description={t("accounts.empty.description")}
            action={
              <button type="button" className="btn" onClick={() => setCreating(true)}>
                {t("accounts.add")}
              </button>
            }
          />
        }
      />

      </div>

      {canManage && picked.count > 0 ? (
        <BatchBar count={picked.count} onClear={picked.clear} barRef={barRef}>
          <span className="batch-note">{t("bulk.accounts.note")}</span>
          {/* `aria-disabled` with a guarded handler, never `disabled`: this is the
              control the operator is standing on, and the DOM attribute would blur it
              to `<body>` the instant the batch starts. */}
          <BatchVerb
            label={t("bulk.accounts.disable", wouldChange(false).length)}
            count={wouldChange(false).length}
            progress={progress}
            total={picked.count}
            onRun={() => void runSetEnabled(false)}
          />
          <BatchVerb
            label={t("bulk.accounts.enable", wouldChange(true).length)}
            count={wouldChange(true).length}
            progress={progress}
            total={picked.count}
            onRun={() => void runSetEnabled(true)}
          />
        </BatchBar>
      ) : null}

      <Suspense fallback={null}>
        {creating ? <CreateAccountDialog onClose={() => setCreating(false)} /> : null}
        {editing ? (
          <AccountCredentialsDialog
            account={editing}
            onClose={() => setEditing(null)}
            onSaved={(message) => setToasts((current) => [...current, { id: Date.now(), message }])}
          />
        ) : null}
      </Suspense>

      {/*
        Typed-name, now that disabling exists.

        The escalation only means anything as a pair: an operator who wants the
        account to stop working has a one-click, undoable control, so the one that
        destroys the sign-in history can afford to ask them to prove which account
        they are looking at. Typing the username proves that; typing "confirm" only
        proves deliberation, and the two rows above and below look identical.
      */}
      <ConfirmDialog
        open={confirmDelete !== null}
        tier="type-name"
        title={t("accounts.delete.title")}
        verb={t("accounts.delete.verb")}
        confirmName={confirmDelete?.username}
        busy={del.isPending}
        error={del.error}
        consequence={t("accounts.delete.consequence", confirmDelete?.username ?? "")}
        recovery={t("accounts.delete.recovery")}
        onConfirm={() => confirmDelete && del.mutate(confirmDelete.id)}
        onCancel={() => setConfirmDelete(null)}
      />

      <Toasts
        toasts={toasts}
        onDismiss={(id) => setToasts((current) => current.filter((x) => x.id !== id))}
      />
    </div>
  );
}

/**
 * One half of the reversible pair, and the reason it can vanish.
 *
 * A verb with nothing to do — *Disable* over eight rows that are all already
 * disabled — is not disabled, it is **absent**. A control that is present and inert
 * still advertises an action, and here the action is not merely unavailable: for this
 * selection it does not exist. The counterpart is beside it and says how many rows it
 * would change, so nothing is hidden by the removal.
 */
function BatchVerb({
  label,
  count,
  progress,
  total,
  onRun,
}: {
  label: string;
  count: number;
  progress: number | null;
  total: number;
  onRun: () => void;
}) {
  if (count === 0) return null;
  return (
    <button
      type="button"
      className="btn btn-sm"
      data-role="batch-action"
      aria-disabled={progress !== null || undefined}
      onClick={() => {
        if (progress !== null) return;
        onRun();
      }}
    >
      {progress !== null ? t("bulk.running", progress, total) : label}
    </button>
  );
}
