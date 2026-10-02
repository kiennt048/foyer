import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import type {
  GuestDataField,
  GuestDataSettings,
  GuestDataSettingsUpdate,
  MarketingConsent,
  MarketingConsentState,
} from "@foyer/contract";
import { SUGGESTED_CONSENT } from "@foyer/portal/src/consent.js";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { relativeTime } from "../format.js";
import { t } from "../i18n/guestdata.js";
import { fieldMessages } from "../problems.js";
import { useSite } from "../site.js";
import { ConfirmDialog, Empty, ErrorNotice, Notice } from "../ui/primitives.js";

/**
 * What a venue asks its guests for at sign-in, and the marketing consents they
 * gave.
 *
 * Three parts, and the first two are kept visibly apart on purpose: **the details a
 * form asks for** (optional unless required) and **marketing consent** (a separate,
 * unticked box that is never required). The third — the consents themselves, their
 * withdrawal and their export — is an administrator's: a list of people's contact
 * details is `privacy:manage`, and taking it home is `reports:export`. A control the
 * server would refuse is hidden rather than disabled.
 */
export function GuestDataScreen() {
  const mayRead = useCan("privacy:manage");
  return (
    <div className="page">
      <PageHead title={t("guestData.title")} description={t("guestData.description")} />
      <Settings />
      {mayRead ? <Consents /> : null}
    </div>
  );
}

const FIELDS: GuestDataField[] = ["name", "phone", "email"];
const FIELD_NAME: Record<GuestDataField, string> = {
  get name() {
    return t("guestData.field.name");
  },
  get phone() {
    return t("guestData.field.phone");
  },
  get email() {
    return t("guestData.field.email");
  },
};

type Draft = {
  asked: Record<GuestDataField, boolean>;
  required: Record<GuestDataField, boolean>;
  enabled: boolean;
  vi: string;
  en: string;
};

function draftOf(s: GuestDataSettings): Draft {
  const asked = { name: false, phone: false, email: false };
  const required = { name: false, phone: false, email: false };
  for (const f of s.fields) {
    asked[f.field] = true;
    required[f.field] = f.required;
  }
  return {
    asked,
    required,
    enabled: s.marketingConsent.enabled,
    vi: s.marketingConsent.text.vi ?? "",
    en: s.marketingConsent.text.en ?? "",
  };
}

function bodyOf(d: Draft): GuestDataSettingsUpdate {
  return {
    fields: FIELDS.filter((f) => d.asked[f]).map((f) => ({ field: f, required: d.required[f] })),
    marketingConsent: {
      enabled: d.enabled,
      text: {
        ...(d.vi.trim() ? { vi: d.vi.trim() } : {}),
        ...(d.en.trim() ? { en: d.en.trim() } : {}),
      },
    },
  };
}

function Settings() {
  const { siteId } = useSite();
  const qc = useQueryClient();
  const mayManage = useCan("portal:manage");
  const key = ["guest-data", siteId];
  const current = useQuery({ queryKey: key, queryFn: () => api.guestData(siteId) });
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saved, setSaved] = useState(false);
  const ids = { vi: useId(), en: useId() };

  useEffect(() => {
    if (current.data) setDraft(draftOf(current.data));
  }, [current.data]);

  const save = useMutation({
    mutationFn: (body: GuestDataSettingsUpdate) => api.updateGuestData(siteId, body),
    onSuccess: (next) => {
      qc.setQueryData(key, next);
      setSaved(true);
    },
  });
  const errors = fieldMessages(save.error);

  if (current.error) return <ErrorNotice error={current.error} title={t("guestData.fields.title")} />;
  if (!draft || !current.data) return null;
  const edit = (patch: Partial<Draft>) => {
    setSaved(false);
    setDraft({ ...draft, ...patch });
  };
  const versions = current.data.marketingConsent.versions;

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!mayManage || save.isPending) return;
        setSaved(false);
        save.mutate(bodyOf(draft));
      }}
    >
      {save.error ? (
        <div style={{ margin: "0.875rem 0" }}>
          <ErrorNotice error={save.error} title={t("guestData.failed")} />
        </div>
      ) : null}
      {saved ? (
        <div style={{ margin: "0.875rem 0" }}>
          <Notice level="positive" role="status" title={t("guestData.saved")} />
        </div>
      ) : null}

      <section className="card" aria-labelledby="gd-fields" style={{ marginBottom: "1.5rem" }}>
        <h2 id="gd-fields">{t("guestData.fields.title")}</h2>
        <p className="field-hint">{t("guestData.fields.body")}</p>
        <fieldset className="field" disabled={!mayManage}>
          <legend className="visually-hidden">{t("guestData.fields.title")}</legend>
          {FIELDS.map((f) => (
            <div key={f} style={{ display: "flex", gap: "1.5rem", alignItems: "center", margin: "0.5rem 0" }}>
              <strong style={{ minWidth: "9rem" }}>{FIELD_NAME[f]}</strong>
              <label>
                <input
                  type="checkbox"
                  checked={draft.asked[f]}
                  onChange={(e) =>
                    edit({
                      asked: { ...draft.asked, [f]: e.target.checked },
                      required: { ...draft.required, [f]: e.target.checked && draft.required[f] },
                    })
                  }
                />{" "}
                {t("guestData.field.ask")}
              </label>
              <label>
                <input
                  type="checkbox"
                  checked={draft.required[f]}
                  disabled={!draft.asked[f]}
                  onChange={(e) => edit({ required: { ...draft.required, [f]: e.target.checked } })}
                />{" "}
                {t("guestData.field.require")}
              </label>
            </div>
          ))}
          {errors.fields ? (
            <p className="field-error" role="alert">
              {errors.fields}
            </p>
          ) : null}
        </fieldset>
      </section>

      <section className="card" aria-labelledby="gd-consent" style={{ marginBottom: "1.5rem" }}>
        <h2 id="gd-consent">{t("guestData.consent.title")}</h2>
        <p className="field-hint">{t("guestData.consent.body")}</p>
        <fieldset className="form-grid" disabled={!mayManage}>
          <label className="field">
            <span>
              <input type="checkbox" checked={draft.enabled} onChange={(e) => edit({ enabled: e.target.checked })} />{" "}
              {t("guestData.consent.enabled")}
            </span>
          </label>
          {(["vi", "en"] as const).map((lang) => (
            <div className="field" key={lang}>
              <label htmlFor={ids[lang]}>{t(`guestData.consent.text.${lang}`)}</label>
              <textarea
                id={ids[lang]}
                lang={lang}
                rows={3}
                maxLength={500}
                value={draft[lang]}
                aria-describedby={`${ids[lang]}-hint`}
                aria-invalid={Boolean(errors["marketingConsent.text"]) || undefined}
                onChange={(e) => edit({ [lang]: e.target.value })}
              />
              <p className="field-hint" id={`${ids[lang]}-hint`}>
                {t("guestData.consent.textHint")}{" "}
                {versions[lang] ? t("guestData.consent.version", lang, versions[lang]!) : null}
              </p>
            </div>
          ))}
          {errors["marketingConsent.text"] ? (
            <p className="field-error" role="alert">
              {errors["marketingConsent.text"]}
            </p>
          ) : null}
          <div>
            <button
              type="button"
              className="btn"
              onClick={() =>
                edit({ vi: SUGGESTED_CONSENT.vi, en: SUGGESTED_CONSENT.en })
              }
            >
              {t("guestData.consent.suggest")}
            </button>
          </div>
        </fieldset>
      </section>

      {mayManage ? (
        <button type="submit" className="btn btn-primary" aria-disabled={save.isPending || undefined}>
          {t("guestData.save")}
        </button>
      ) : null}
    </form>
  );
}

function Consents() {
  const { siteId } = useSite();
  const qc = useQueryClient();
  const [state, setState] = useState<MarketingConsentState | "">("");
  const [target, setTarget] = useState<MarketingConsent | null>(null);
  const [done, setDone] = useState(false);
  const filterId = useId();
  const list = useInfiniteQuery({
    queryKey: ["marketing-consents", siteId, state],
    queryFn: ({ pageParam }) =>
      api.listMarketingConsents(siteId, { state: state || undefined, cursor: pageParam, limit: 50 }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });
  const withdraw = useMutation({
    mutationFn: (id: string) => api.withdrawMarketingConsent(siteId, id),
    onSuccess: () => {
      setTarget(null);
      setDone(true);
      void qc.invalidateQueries({ queryKey: ["marketing-consents", siteId] });
    },
  });
  const rows = list.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <section className="card" aria-labelledby="gd-list" style={{ marginBottom: "1.5rem" }}>
      <h2 id="gd-list">{t("guestData.list.title")}</h2>
      <p className="field-hint">{t("guestData.list.body")}</p>
      <div className="toolbar">
        <label htmlFor={filterId}>{t("guestData.list.filter")}</label>
        <select
          id={filterId}
          value={state}
          onChange={(e) => setState(e.target.value as MarketingConsentState | "")}
        >
          <option value="">{t("guestData.list.all")}</option>
          <option value="active">{t("guestData.list.active")}</option>
          <option value="withdrawn">{t("guestData.list.withdrawn")}</option>
        </select>
      </div>
      {done ? <Notice level="positive" role="status" title={t("guestData.withdrawn")} /> : null}
      {list.error ? <ErrorNotice error={list.error} title={t("guestData.list.failed")} /> : null}
      {!list.isPending && rows.length === 0 && !list.error ? <Empty title={t("guestData.list.empty")} /> : null}
      {rows.length > 0 ? (
        <table>
          <thead>
            <tr>
              <th scope="col">{t("guestData.col.given")}</th>
              <th scope="col">{t("guestData.col.contact")}</th>
              <th scope="col">{t("guestData.col.text")}</th>
              <th scope="col">{t("guestData.col.state")}</th>
              <th scope="col">
                <span className="visually-hidden">{t("guestData.withdraw.verb")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={c.id}>
                <td title={c.consentedAt}>{relativeTime(c.consentedAt) ?? c.consentedAt}</td>
                <td>
                  {c.state === "withdrawn" ? (
                    <span className="field-hint">{t("guestData.cleared")}</span>
                  ) : (
                    [c.name, c.email, c.phone].filter(Boolean).join(" · ")
                  )}
                </td>
                <td lang={c.lang}>{c.consentText}</td>
                <td>
                  {c.state === "active"
                    ? t("guestData.state.active")
                    : t(
                        "guestData.state.withdrawn",
                        relativeTime(c.withdrawnAt) ?? c.withdrawnAt ?? "",
                        t(c.withdrawnBy === "guest" ? "guestData.by.guest" : "guestData.by.operator"),
                      )}
                </td>
                <td>
                  {c.state === "active" ? (
                    <button
                      type="button"
                      className="btn btn-sm"
                      onClick={() => {
                        setDone(false);
                        withdraw.reset();
                        setTarget(c);
                      }}
                    >
                      {t("guestData.withdraw")}
                    </button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {list.hasNextPage ? (
        <button type="button" className="btn" onClick={() => void list.fetchNextPage()}>
          {t("guestData.more")}
        </button>
      ) : null}
      <ExportConsents />
      <ConfirmDialog
        open={target !== null}
        title={t("guestData.withdraw.title")}
        verb={t("guestData.withdraw.verb")}
        consequence={t("guestData.withdraw.consequence")}
        recovery={t("guestData.withdraw.recovery")}
        busy={withdraw.isPending}
        error={withdraw.error}
        onConfirm={() => (target ? withdraw.mutate(target.id) : undefined)}
        onCancel={() => setTarget(null)}
      />
    </section>
  );
}

/** The contract's bound for this export, stated in the sentence beside it. */
const EXPORT_MAX_DAYS = 92;

function day(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

function midnight(value: string, plusDays = 0): string {
  const [y, m, d] = value.split("-").map(Number);
  return new Date(y!, m! - 1, d! + plusDays).toISOString();
}

function ExportConsents() {
  const allowed = useCan("reports:export");
  const { siteId } = useSite();
  const today = new Date();
  const monthAgo = new Date(today);
  monthAgo.setDate(monthAgo.getDate() - 30);
  const [from, setFrom] = useState(day(monthAgo));
  const [to, setTo] = useState(day(today));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (!allowed) return null;

  const download = async () => {
    setError(null);
    setBusy(true);
    try {
      const blob = await api.exportCsv("marketing-consents", { siteId, from: midnight(from), to: midnight(to, 1) });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `foyer-marketing-consents-${siteId}-${from}-${to}.csv`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ marginTop: "1rem" }}>
      <h3>{t("guestData.export.title")}</h3>
      <p className="field-hint">{t("guestData.export.body", EXPORT_MAX_DAYS)}</p>
      <div className="toolbar">
        <label className="field">
          <span className="visually-hidden">{t("guestData.export.from")}</span>
          <input type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="field">
          <span className="visually-hidden">{t("guestData.export.to")}</span>
          <input type="date" value={to} min={from} onChange={(e) => setTo(e.target.value)} />
        </label>
        <button
          type="button"
          className="btn btn-sm"
          aria-disabled={busy || undefined}
          onClick={() => (busy ? undefined : void download())}
        >
          {t("guestData.export.go")}
        </button>
      </div>
      {error ? <ErrorNotice error={error} title={t("guestData.export.failed")} /> : null}
    </div>
  );
}
