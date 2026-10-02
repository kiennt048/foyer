import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import type {
  DataSubjectErasure,
  DataSubjectKind,
  RetentionCategoryId,
  RetentionCategoryPolicy,
} from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { relativeTime } from "../format.js";
import { t } from "../i18n/privacy.js";
import { fieldMessages } from "../problems.js";
import { ConfirmDialog, ErrorNotice, Notice } from "../ui/primitives.js";
import { Maintenance } from "./Maintenance.js";

/**
 * Retention periods and erasing one guest's data.
 *
 * Administrators only (`privacy:manage`): the route is in `NAV` with that permission,
 * so a staff member never sees it, and the server refuses them regardless.
 *
 * **Every period is shown with what it removes and when the sweep last ran.** A
 * retention policy an operator cannot see being enforced is a promise; `lastSweep` is
 * the sweep's own record, and a category that has never been swept says so rather
 * than showing nothing.
 */
export function PrivacyScreen() {
  return (
    <div className="page">
      <PageHead title={t("privacy.title")} description={t("privacy.description")} />
      <Retention />
      <Erase />
      {/* Technical, so last and collapsed: the retention
          periods and the erasure are what a venue comes here for. */}
      <Maintenance />
    </div>
  );
}

const CATEGORY_NAME: Record<RetentionCategoryId, string> = {
  get accounting() {
    return t("privacy.category.accounting");
  },
  get authAttempts() {
    return t("privacy.category.authAttempts");
  },
  get portalGrants() {
    return t("privacy.category.portalGrants");
  },
  get audit() {
    return t("privacy.category.audit");
  },
  get guestAccounts() {
    return t("privacy.category.guestAccounts");
  },
  get marketingConsents() {
    return t("privacy.category.marketingConsents");
  },
};

const CATEGORY_WHAT: Record<RetentionCategoryId, string> = {
  get accounting() {
    return t("privacy.category.accounting.what");
  },
  get authAttempts() {
    return t("privacy.category.authAttempts.what");
  },
  get portalGrants() {
    return t("privacy.category.portalGrants.what");
  },
  get audit() {
    return t("privacy.category.audit.what");
  },
  get guestAccounts() {
    return t("privacy.category.guestAccounts.what");
  },
  get marketingConsents() {
    return t("privacy.category.marketingConsents.what");
  },
};

function Retention() {
  const qc = useQueryClient();
  const policy = useQuery({ queryKey: ["privacy", "retention"], queryFn: api.retentionPolicy });
  /* The operator's edits, keyed by category: a number typed, or `null` for
     "restore the default". Absent means untouched. */
  const [draft, setDraft] = useState<Partial<Record<RetentionCategoryId, string | null>>>({});
  const [saved, setSaved] = useState(false);

  const save = useMutation({
    mutationFn: (changes: { category: RetentionCategoryId; days: number | null }[]) =>
      api.updateRetentionPolicy({ changes }),
    onSuccess: (next) => {
      qc.setQueryData(["privacy", "retention"], next);
      setDraft({});
      setSaved(true);
    },
  });
  const errors = fieldMessages(save.error);

  if (policy.error) return <ErrorNotice error={policy.error} title={t("privacy.retention.title")} />;
  const categories = policy.data?.categories ?? [];

  type Change = { category: RetentionCategoryId; days: number | null };
  const changes = categories.flatMap((c): Change[] => {
    const d = draft[c.category];
    if (d === undefined) return [];
    if (d === null) return c.isDefault ? [] : [{ category: c.category, days: null }];
    const n = Number(d);
    return d.trim() === "" || !Number.isInteger(n) || n === c.days ? [] : [{ category: c.category, days: n }];
  });

  return (
    <section className="card" aria-labelledby="retention-title" style={{ marginBottom: "1.5rem" }}>
      <h2 id="retention-title">{t("privacy.retention.title")}</h2>
      <p className="field-hint">{t("privacy.retention.body")}</p>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          setSaved(false);
          if (changes.length === 0 || save.isPending) return;
          save.mutate(changes);
        }}
      >
        {save.error ? (
          <div style={{ margin: "0.875rem 0" }}>
            <ErrorNotice error={save.error} title={t("privacy.retention.failed")} />
          </div>
        ) : null}
        {saved ? (
          <div style={{ margin: "0.875rem 0" }}>
            <Notice level="positive" role="status" title={t("privacy.retention.saved")} />
          </div>
        ) : null}

        <div className="form-grid">
          {categories.map((c) => (
            <CategoryRow
              key={c.category}
              policy={c}
              value={draft[c.category]}
              error={errors[`changes.${c.category}`]}
              onChange={(v) => {
                setSaved(false);
                setDraft((prev) => ({ ...prev, [c.category]: v }));
              }}
            />
          ))}
          <button
            type="submit"
            className="btn btn-primary"
            aria-disabled={save.isPending || changes.length === 0 || undefined}
          >
            {t("privacy.retention.save")}
          </button>
          {changes.length === 0 && Object.keys(draft).length > 0 ? (
            <p className="field-hint">{t("privacy.retention.unchanged")}</p>
          ) : null}
        </div>
      </form>
    </section>
  );
}

function CategoryRow({
  policy: c,
  value,
  error,
  onChange,
}: {
  policy: RetentionCategoryPolicy;
  value: string | null | undefined;
  error?: string;
  onChange: (v: string | null) => void;
}) {
  const id = useId();
  const shown = value === undefined ? String(c.days) : value === null ? String(c.defaultDays) : value;
  const sweep = c.lastSweep;
  const when = sweep ? (relativeTime(sweep.at) ?? sweep.at) : "";
  return (
    <div className="field">
      <label htmlFor={id}>{CATEGORY_NAME[c.category]}</label>
      <p className="field-hint">{CATEGORY_WHAT[c.category]}</p>
      <input
        id={id}
        type="number"
        inputMode="numeric"
        min={c.minimumDays}
        max={c.maximumDays}
        value={shown}
        aria-invalid={Boolean(error) || undefined}
        aria-describedby={`${id}-hint${error ? ` ${id}-error` : ""}`}
        onChange={(e) => onChange(e.target.value)}
        style={{ maxWidth: "8rem" }}
      />
      <p className="field-hint" id={`${id}-hint`}>
        {t("privacy.retention.days")} · {t("privacy.retention.bounds", c.minimumDays, c.maximumDays)}{" "}
        {c.minimumDays === 0 ? t("privacy.retention.keep") : null} {t("privacy.retention.default", c.defaultDays)}{" "}
        {t("privacy.retention.tables")}: <code>{c.tables.join(", ")}</code>.{" "}
        {!sweep
          ? t("privacy.retention.neverSwept")
          : sweep.failed
            ? t("privacy.retention.sweepFailed", when)
            : t("privacy.retention.swept", when, sweep.removed)}
      </p>
      {error ? (
        <p className="field-error" role="alert" id={`${id}-error`}>
          {error}
        </p>
      ) : null}
      {!c.isDefault && value !== null ? (
        <button type="button" className="btn" onClick={() => onChange(null)}>
          {t("privacy.retention.restore")}
        </button>
      ) : null}
    </div>
  );
}

function Erase() {
  const qc = useQueryClient();
  const [kind, setKind] = useState<DataSubjectKind>("mac");
  const [value, setValue] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [receipt, setReceipt] = useState<DataSubjectErasure | null>(null);
  const inputId = useId();

  const erase = useMutation({
    mutationFn: () =>
      api.eraseDataSubject({
        subject:
          kind === "mac"
            ? { kind, mac: value.trim() }
            : kind === "phone"
              ? { kind, phone: value.trim() }
              : kind === "email"
                ? { kind, email: value.trim() }
                : { kind, accountId: value.trim() },
      }),
    onSuccess: (r) => {
      setConfirming(false);
      setReceipt(r);
      setValue("");
      // Sessions, attempts and accounts on every other screen may have lost rows.
      void qc.invalidateQueries();
    },
    onError: () => setConfirming(false),
  });
  const errors = fieldMessages(erase.error);
  const fieldError = errors[kind === "account" ? "subject.accountId" : `subject.${kind}`];

  return (
    <section className="card" aria-labelledby="erase-title" style={{ marginBottom: "1.5rem" }}>
      <h2 id="erase-title">{t("privacy.erase.title")}</h2>
      <p className="field-hint">{t("privacy.erase.body")}</p>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          setReceipt(null);
          if (value.trim() === "" || erase.isPending) return;
          setConfirming(true);
        }}
      >
        {erase.error && !fieldError ? (
          <div style={{ margin: "0.875rem 0" }}>
            <ErrorNotice error={erase.error} title={t("privacy.erase.failed")} />
          </div>
        ) : null}
        {receipt ? (
          <div style={{ margin: "0.875rem 0" }}>
            <Notice
              level="positive"
              role="status"
              title={t("privacy.erase.done", receipt.subject.masked, receipt.total)}
            >
              <table>
                <thead>
                  <tr>
                    <th scope="col">{t("privacy.erase.table")}</th>
                    <th scope="col">{t("privacy.erase.rows")}</th>
                  </tr>
                </thead>
                <tbody>
                  {receipt.removed.map((r) => (
                    <tr key={r.table}>
                      <td>
                        <code>{r.table}</code>
                      </td>
                      <td>{r.rows}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {receipt.pseudonymised.map((p) =>
                p.rows > 0 ? (
                  <p key={p.table} className="field-hint">
                    {t("privacy.erase.pseudonymised", p.rows)}
                  </p>
                ) : null,
              )}
            </Notice>
          </div>
        ) : null}

        <div className="form-grid">
          <fieldset className="field">
            <legend>{t("privacy.erase.kind")}</legend>
            {(["mac", "phone", "email", "account"] as const).map((k) => (
              <label key={k} style={{ display: "block" }}>
                <input
                  type="radio"
                  name="erase-kind"
                  value={k}
                  checked={kind === k}
                  onChange={() => {
                    setKind(k);
                    erase.reset();
                  }}
                />{" "}
                {t(`privacy.erase.kind.${k}`)}
              </label>
            ))}
          </fieldset>
          <div className="field">
            <label htmlFor={inputId}>{t(`privacy.erase.${kind}`)}</label>
            <input
              id={inputId}
              value={value}
              autoComplete="off"
              spellCheck={false}
              aria-invalid={Boolean(fieldError) || undefined}
              aria-describedby={`${inputId}-hint${fieldError ? ` ${inputId}-error` : ""}`}
              onChange={(e) => setValue(e.target.value)}
            />
            <p className="field-hint" id={`${inputId}-hint`}>
              {t(`privacy.erase.${kind}Hint`)}
            </p>
            {fieldError ? (
              <p className="field-error" role="alert" id={`${inputId}-error`}>
                {fieldError}
              </p>
            ) : null}
          </div>
          <button type="submit" className="btn btn-danger" aria-disabled={erase.isPending || undefined}>
            {t("privacy.erase.submit")}
          </button>
        </div>
      </form>

      <ConfirmDialog
        open={confirming}
        title={t("privacy.erase.confirmTitle")}
        verb={t("privacy.erase.confirmVerb")}
        consequence={t("privacy.erase.consequence")}
        recovery={t("privacy.erase.recovery")}
        busy={erase.isPending}
        error={erase.error}
        onConfirm={() => erase.mutate()}
        onCancel={() => setConfirming(false)}
      />
    </section>
  );
}
