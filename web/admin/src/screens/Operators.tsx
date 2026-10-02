import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useSearchParams } from "react-router";
import { PASSWORD_MIN_LENGTH, type Operator, type OperatorRole } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { ROLE_LABEL, useCan } from "../auth.js";
import { t } from "../i18n/operators.js";
import { errorCopy, fieldMessages } from "../problems.js";
import { Plus, Search } from "../ui/icons.js";
import {
  ConfirmDialog,
  Empty,
  ErrorNotice,
  Modal,
  Notice,
  Pill,
  type Toast,
  Toasts,
} from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Stack, Table, TimeCell } from "../ui/table.js";
import { useRefocusAfterRemoval } from "./refocus.js";

/**
 * The people who can sign in to this console.
 *
 * **Not site-scoped, and that is the whole distinction from `Account`.** A guest's
 * identity carries a `siteId` and may carry an NT hash; an operator works across
 * sites — the site switcher in the header is the evidence — and is always argon2id.
 * So there is no site filter here and the switcher above does not apply to this list.
 *
 * Administrator-only, and **lazily loaded**: a staff session never opens it, and the
 * entry chunk is what an operator downloads before the first screen they actually
 * look at.
 */

/** The operators who can still administer this deployment. */
export function enabledAdministrators(rows: readonly Operator[]): Operator[] {
  return rows.filter((o) => o.role === "administrator" && o.enabled);
}

/**
 * Would taking this operator's administrator access away leave nobody with it?
 *
 * **Enabled administrators, not administrators.** A disabled one cannot sign in, so a
 * deployment whose only administrator is disabled is already locked out — and there
 * is no password-reset channel and no recovery short of the Go binary's first-boot
 * path. The fixture world has exactly one enabled administrator and one disabled one
 * *on purpose*: a control that counted rows would be green against those fixtures and
 * would offer a delete the server refuses on a real deployment.
 *
 * Disabling, demoting and deleting all reach the same state, so one predicate covers
 * all three. It is narrow on purpose and matches the server's: an administrator
 * demoting themselves while another enabled administrator exists is allowed, because
 * that other administrator can put it back.
 *
 * **This is a courtesy, not the rule.** The list is one page, so a deployment with
 * more operators than fit in it could disagree with the server — which answers `409`
 * `/problems/last-administrator` regardless, and whose refusal is what every one of
 * these controls renders when it arrives.
 */
export function wouldOrphanTheDeployment(rows: readonly Operator[], operator: Operator): boolean {
  const administrators = enabledAdministrators(rows);
  return administrators.length === 1 && administrators[0]?.id === operator.id;
}

export function OperatorsScreen() {
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const [creating, setCreating] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Operator | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);

  /*
   * The screen still asks the server, even when this console already believes the
   * answer is no. The gate below removes the *controls*; it does not pre-empt the
   * request, because the server is the authority on what this operator may do and a
   * client that refused on its own behalf would be guessing. A staff member who
   * types this URL gets the real `403`, and the table renders its catalogue copy.
   */
  const canManage = useCan("operators:manage");

  const query = useQuery({
    queryKey: ["operators", q],
    queryFn: () => api.listOperators({ q }),
  });

  const rows = query.data?.items ?? [];
  const armRefocus = useRefocusAfterRemoval(rows);

  const toast = (message: string, extra?: Partial<Toast>) =>
    setToasts((current) => [...current, { id: Date.now(), message, ...extra }]);
  const failed = (e: unknown) => toast(errorCopy(e).title, { detail: errorCopy(e).body });

  const del = useMutation({
    mutationFn: (id: string) => api.deleteOperator(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["operators"] });
      setConfirmDelete(null);
    },
  });

  /*
   * Enable, disable and change role are one mutation, because they are one operation
   * — `PATCH /operators/{id}` — and because all three are reversible in one click.
   * Low friction with an undo, exactly as disabling a guest account is: **the
   * reversible action must be the easier one**, or an operator dealing with a
   * compromised login reaches for the one that destroys the record.
   *
   * The undo is a second write rather than a rollback, which is the honest shape: the
   * server has already been told, and the toast offers to tell it the opposite. An
   * undo can itself be refused — putting somebody back to `staff` may be the change
   * that would orphan the deployment — and it lands in the same error path.
   */
  const update = useMutation({
    mutationFn: ({ id, body }: { id: string; body: { enabled?: boolean; role?: OperatorRole } }) =>
      api.updateOperator(id, body),
    onSuccess: (operator, sent) => {
      void qc.invalidateQueries({ queryKey: ["operators"] });
      const undo = (body: { enabled?: boolean; role?: OperatorRole }) => () =>
        update.mutate({ id: operator.id, body });
      if (sent.body.role !== undefined) {
        toast(t("operators.toast.role", operator.username, ROLE_LABEL[operator.role]), {
          undo: undo({ role: operator.role === "administrator" ? "staff" : "administrator" }),
        });
      } else {
        toast(
          operator.enabled
            ? t("operators.toast.enabled", operator.username)
            : t("operators.toast.disabled", operator.username),
          { undo: undo({ enabled: !operator.enabled }) },
        );
      }
    },
    /* A refusal on a low-friction action still has to be reported: without this a
       `409` silently re-enabled the button and the pill snapped back, which reads as
       *it did not take* rather than as *the server refused*. */
    onError: failed,
  });

  /* One row, at most, and it is the row whose three actions are unavailable. */
  const lastAdministrator = rows.find((o) => wouldOrphanTheDeployment(rows, o));

  const allColumns: Column<Operator>[] = [
    {
      key: "username",
      narrow: "keep",
      header: t("operators.col.username"),
      cell: (o) => (
        <Stack
          primary={<span className="row-link">{o.username}</span>}
          secondary={o.displayName ?? undefined}
          absentText={t("operators.noDisplayName")}
        />
      ),
    },
    {
      key: "role",
      narrow: "keep",
      header: t("operators.col.role"),
      /* A label, not a status: `neutral` for both, because neither role is a
         condition an operator has to act on. The word carries the meaning. */
      cell: (o) => <Pill level="neutral" label={ROLE_LABEL[o.role]} />,
    },
    {
      key: "state",
      narrow: "drop",
      header: t("operators.col.status"),
      cell: (o) =>
        o.enabled ? (
          <Pill level="positive" label={t("operators.enabled")} />
        ) : (
          <Pill level="neutral" label={t("operators.disabled")} />
        ),
    },
    {
      key: "created",
      narrow: "drop",
      header: t("operators.col.created"),
      cell: (o) => <TimeCell iso={o.createdAt} />,
    },
    {
      key: "lastLogin",
      narrow: "drop",
      header: t("operators.col.lastLogin"),
      /* `null` is *never*, which is not "a long time ago": formatting it as a date
         renders the epoch, and the shared front-desk account in the fixtures is
         exactly that row. */
      cell: (o) => <TimeCell iso={o.lastLoginAt} absent={t("operators.neverSignedIn")} />,
    },
    {
      key: "actions",
      narrow: "keep",
      header: "",
      align: "end",
      width: "1%",
      cell: (o) => {
        /*
         * The three controls that would leave nobody able to administer this
         * deployment. **Disabled with the reason stated**, not hidden: the capability
         * is real and it is available again the moment another operator has the
         * administrator role, and a control that vanished would send an administrator
         * looking for a setting that does not exist. The reason is on screen above
         * the table, permanently and visibly, rather than only in these tooltips —
         * `title` on a disabled control is not announced.
         *
         * Plain `disabled` rather than `aria-disabled`: this is a precondition the
         * control has never had focus under, so there is no focus to lose, and it
         * keeps a tab stop that does nothing out of the order. `screens.test.ts`
         * carries the distinction.
         *
         * ─────────────────────────────────────────────────────────────────────
         *
         * **And the other half, which was missing entirely.**
         *
         * The two controls below that send a `PATCH` said nothing at all while one was
         * in flight: no `disabled`, no `aria-disabled`, no guard in the handler. Two
         * presses sent two `PATCH`es, and the second is a write nobody asked for —
         * enable/disable toggles, so a double press on *Disable* re-enables the
         * operator it was meant to lock out, and each one is a permanent entry in an
         * append-only audit log.
         *
         * `aria-disabled` and a guarded handler, never `disabled`: the operator is
         * standing on this control — pressing it is what started the request — and
         * setting the DOM `disabled` attribute on a focused element blurs it to
         * `<body>` for the whole request with nothing to put it back. That is the
         * defect measured and written out at `Accounts.tsx`'s row action, and
         * this control is the smallest reproduction of it. The attribute is a claim
         * about the control; the guard in the handler is what makes it true, because
         * an `aria-disabled` button is still clickable.
         *
         * It is the mutation's pending state rather than this row's, which is the same
         * shape `/accounts` has: one `PATCH` in flight anywhere in the list stills the
         * list, and that is the honest reading — the operator has a write outstanding
         * and the rows they are looking at are about to be refetched under them.
         *
         * *Delete* is deliberately not gated on it. It sends no `PATCH`; it opens a
         * confirmation, which has its own `busy` and its own typed name.
         */
        const orphans = wouldOrphanTheDeployment(rows, o);
        const why = orphans ? t("operators.lastAdmin.why") : undefined;
        return (
          <span className="row-actions">
            <button
              type="button"
              className="btn btn-sm"
              disabled={orphans && o.enabled}
              aria-disabled={update.isPending || undefined}
              title={o.enabled ? (why ?? t("operators.action.disableWhy")) : t("operators.action.enableWhy")}
              onClick={(e) => {
                e.stopPropagation();
                if (update.isPending) return;
                update.mutate({ id: o.id, body: { enabled: !o.enabled } });
              }}
            >
              {o.enabled ? t("operators.action.disable") : t("operators.action.enable")}
            </button>
            <button
              type="button"
              className="btn btn-sm"
              disabled={orphans}
              aria-disabled={update.isPending || undefined}
              title={why ?? t("operators.action.roleWhy")}
              onClick={(e) => {
                e.stopPropagation();
                if (update.isPending) return;
                update.mutate({
                  id: o.id,
                  body: { role: o.role === "administrator" ? "staff" : "administrator" },
                });
              }}
            >
              {o.role === "administrator"
                ? t("operators.action.makeStaff")
                : t("operators.action.makeAdministrator")}
            </button>
            <button
              type="button"
              className="btn btn-sm btn-ghost btn-danger"
              disabled={orphans}
              title={why}
              onClick={(e) => {
                e.stopPropagation();
                armRefocus(e.currentTarget);
                setConfirmDelete(o);
              }}
            >
              {t("common.delete")}
            </button>
          </span>
        );
      },
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
        title={t("operators.title")}
        description={t("operators.description")}
        actions={
          canManage ? (
            <button type="button" className="btn btn-primary" onClick={() => setCreating(true)}>
              <Plus size={14} />
              {t("operators.add")}
            </button>
          ) : null
        }
      />

      {/*
        The absent feature, said where an administrator looks for the control.

        Resetting somebody else's password needs a delivery channel and this product
        has none — no SMTP sender, no one-time-link store — and the alternative, an
        administrator typing a new password and reading it out, is a credential
        travelling over whatever channel is to hand for an account that can rotate a
        RADIUS shared secret. The recovery path that does exist is honest and is named
        here, so nobody adds a reset button that silently does nothing.
      */}
      <div className="notice-stack" style={{ marginBottom: "1rem" }}>
        <Notice level="info" title={t("operators.noReset.title")}>
          {t("operators.noReset.body")}
        </Notice>

        {/* Only while it is true, and immediately above the rows it explains. */}
        {lastAdministrator ? (
          <Notice level="warning" title={t("operators.lastAdmin.title", lastAdministrator.username)}>
            {t("operators.lastAdmin.body")}
          </Notice>
        ) : null}
      </div>

      <div className="toolbar">
        <label className="search">
          <Search size={14} />
          <input
            type="search"
            placeholder={t("operators.search")}
            defaultValue={q}
            onChange={(e) => {
              const next = new URLSearchParams(params);
              if (e.target.value) next.set("q", e.target.value);
              else next.delete("q");
              setParams(next, { replace: true });
            }}
          />
          <span className="visually-hidden">{t("operators.searchLabel")}</span>
        </label>
      </div>

      <Table
        columns={columns}
        rows={rows}
        rowKey={(o) => o.id}
        rowLabel={(o) => o.username}
        isLoading={query.isLoading}
        error={query.error}
        refreshing={query.isFetching && !query.isLoading}
        updatedAt={query.dataUpdatedAt}
        filtered={q !== ""}
        onClearFilters={() => setParams(new URLSearchParams(), { replace: true })}
        empty={<Empty title={t("operators.empty.title")} description={t("operators.empty.description")} />}
      />

      {creating ? <CreateOperatorDialog onClose={() => setCreating(false)} /> : null}

      {/*
        Typed-name, and the pair is what earns it: disabling is one click and undoable,
        so the control that destroys the record can afford to ask which row this is.
        Mounted always and toggled with `open` — a conditionally mounted `<dialog>`
        leaves the document while still open, so `close()` never runs and focus never
        returns. `screens.test.ts` holds that rule for every screen.
      */}
      <ConfirmDialog
        open={confirmDelete !== null}
        tier="type-name"
        title={t("operators.delete.title")}
        verb={t("operators.delete.verb")}
        confirmName={confirmDelete?.username}
        busy={del.isPending}
        error={del.error}
        consequence={t("operators.delete.consequence", confirmDelete?.username ?? "")}
        recovery={t("operators.delete.recovery")}
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

function CreateOperatorDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const titleId = useId();
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [role, setRole] = useState<OperatorRole>("staff");
  const [password, setPassword] = useState("");

  const create = useMutation({
    /*
     * `enabled: true`, sent rather than left to the schema's default.
     *
     * The contract allows creating an operator who cannot sign in — legitimate, and
     * not the default — and this console does not offer it: an administrator sitting
     * with somebody, typing them a password, is making an account for use now, and a
     * checkbox for the other case would be a control nobody in a café reaches for.
     * The row's Disable control covers it a second later if it is really wanted.
     */
    mutationFn: () => api.createOperator({ username, displayName, role, password, enabled: true }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["operators"] });
      onClose();
    },
  });

  const fieldErrors = fieldMessages(create.error);

  return (
    <Modal className="no-print" labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {t("operators.create.title")}
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
              <ErrorNotice error={create.error} title={t("operators.create.failed.title")} />
            ) : null}
            <div className="form-grid">
              <div className="field">
                <label htmlFor="operator-username">{t("operators.create.username")}</label>
                <input
                  id="operator-username"
                  type="text"
                  autoComplete="off"
                  value={username}
                  aria-invalid={Boolean(fieldErrors.username) || undefined}
                  aria-describedby={
                    fieldErrors.username
                      ? "operator-username-hint operator-username-error"
                      : "operator-username-hint"
                  }
                  onChange={(e) => setUsername(e.target.value)}
                />
                {/* Immutable, and said at the moment it is chosen rather than
                    discovered later: the audit log denormalises it at write time, so
                    renaming would leave one person under two names across a log that
                    is read in time order. */}
                <p className="field-hint" id="operator-username-hint">
                  {t("operators.create.usernameHint")}
                </p>
                {fieldErrors.username ? (
                  <p className="field-error" role="alert" id="operator-username-error">
                    {fieldErrors.username}
                  </p>
                ) : null}
              </div>

              <div className="field">
                <label htmlFor="operator-display-name">{t("operators.create.displayName")}</label>
                <input
                  id="operator-display-name"
                  type="text"
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                />
              </div>

              <div className="field">
                <label htmlFor="operator-role">{t("operators.create.role")}</label>
                <select
                  id="operator-role"
                  value={role}
                  onChange={(e) => setRole(e.target.value as OperatorRole)}
                >
                  <option value="staff">{ROLE_LABEL.staff}</option>
                  <option value="administrator">{ROLE_LABEL.administrator}</option>
                </select>
                <p className="field-hint" id="operator-role-hint">
                  {t("operators.create.roleHint")}
                </p>
              </div>

              <div className="field">
                <label htmlFor="operator-password">{t("operators.create.password")}</label>
                <input
                  id="operator-password"
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  aria-invalid={Boolean(fieldErrors.password) || undefined}
                  aria-describedby={
                    fieldErrors.password
                      ? "operator-password-hint operator-password-error"
                      : "operator-password-hint"
                  }
                  onChange={(e) => setPassword(e.target.value)}
                />
                {/* The length rule, stated before anything is typed, and the one
                    thing about this password that is not obvious: it is handed over
                    in person because that is the only delivery channel there is. */}
                <p className="field-hint" id="operator-password-hint">
                  {t("operators.create.passwordHint", PASSWORD_MIN_LENGTH)}
                </p>
                {fieldErrors.password ? (
                  <p className="field-error" role="alert" id="operator-password-error">
                    {fieldErrors.password}
                  </p>
                ) : null}
              </div>
            </div>
          </div>
          <div className="dialog-foot">
            <button type="button" className="btn" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button
              type="submit"
              className="btn btn-primary"
              aria-disabled={create.isPending || undefined}
            >
              {t("operators.create.submit")}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}
