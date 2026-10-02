import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import type { PhoneSignIn, PhoneSignInProvider, PhoneSignInUpdate, PhoneTestMessageResult } from "@foyer/contract";
import { api, request } from "../../api.js";
import { t } from "../../i18n/phone.js";
import { fieldMessages } from "../../problems.js";
import { ErrorNotice, Notice } from "../../ui/primitives.js";
import { PhoneAbusePanel } from "./PhoneAbuse.js";

/**
 * Phone sign-in's provider, on the sign-in tab of `/portal`.
 *
 * A panel with a copy namespace of its own (`i18n/phone.ts`), imported statically by
 * `Portal.tsx` and so carried in that screen's lazy chunk: it costs the admin entry
 * chunk — at 97 % of its budget — nothing. A nested `lazy()` was tried and measured
 * worse; `Portal.tsx` says why.
 *
 * **The keys are write-only.** The read carries `credentialsSet` and never a key, so
 * the inputs start empty whatever is stored and an empty input on save means *keep
 * the stored one*. There is no way to see a key again, which is the point.
 */

export interface Draft {
  provider: PhoneSignInProvider;
  brandname: string;
  zaloOaId: string;
  zaloTemplateId: string;
  apiKey: string;
  secretKey: string;
  sandbox: boolean;
  dailyLimit: string;
  sessionMinutes: string;
  messageTemplate: string;
}

export const toDraft = (p: PhoneSignIn): Draft => ({
  provider: p.provider ?? "esms-sms",
  brandname: p.brandname ?? "",
  zaloOaId: p.zaloOaId ?? "",
  zaloTemplateId: p.zaloTemplateId ?? "",
  apiKey: "",
  secretKey: "",
  sandbox: p.sandbox,
  dailyLimit: p.dailyLimit == null ? "" : String(p.dailyLimit),
  sessionMinutes: p.sessionMinutes == null ? "60" : String(p.sessionMinutes),
  // Unaccented so it is one 160-character segment; the venue edits it to match the
  // template eSMS approved.
  messageTemplate: p.messageTemplate ?? "{code} la ma dang nhap Wi-Fi cua ban",
});

/** What a save sends. Exported for the test that holds the write-only rule. */
export function updateBody(d: Draft): PhoneSignInUpdate {
  return {
    provider: d.provider,
    dailyLimit: Number(d.dailyLimit),
    sessionMinutes: Number(d.sessionMinutes),
    messageTemplate: d.messageTemplate,
    sandbox: d.sandbox,
    ...(d.provider === "esms-sms"
      ? { brandname: d.brandname }
      : { zaloOaId: d.zaloOaId, zaloTemplateId: d.zaloTemplateId }),
    // Omitted is unchanged. An empty box never overwrites a stored key.
    ...(d.apiKey ? { apiKey: d.apiKey } : {}),
    ...(d.secretKey ? { secretKey: d.secretKey } : {}),
  };
}

export function PhoneSignInPanel({ siteId, canManage }: { siteId: string; canManage: boolean }) {
  const qc = useQueryClient();
  const query = useQuery({ queryKey: ["phone-sign-in", siteId], queryFn: () => api.getPhoneSignIn(siteId) });
  const [draft, setDraft] = useState<Draft | null>(null);
  /* Seeded once per *site*, not per payload. `siteId` is the screen moving between
     sites, and `query.data?.siteId` is a different site's settings arriving. A
     refetch that answers with a new object for the same site — the save's own
     invalidation, a refocus, a second console saving — must not reseed the draft,
     because reseeding is exactly what discards the half-typed key beside it, and
     an empty key box means *keep the stored one* rather than *clear the key*.

     What would change it: a field the server may change underneath the operator
     which they must see land. There is none — every other field is what the form
     itself sent, and `onSuccess` seeds from the save's own response. */
  useEffect(() => {
    if (query.data) setDraft(toDraft(query.data));
  }, [siteId, query.data?.siteId]);

  const save = useMutation({
    mutationFn: (d: Draft) => api.updatePhoneSignIn(siteId, updateBody(d)),
    onSuccess: (next) => {
      setDraft(toDraft(next));
      void qc.invalidateQueries({ queryKey: ["phone-sign-in", siteId] });
    },
  });

  if (query.isError) return <ErrorNotice error={query.error} />;
  const p = query.data;
  if (!p || !draft) return <p className="muted">{t("common.loading")}</p>;

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d));
  const errors = fieldMessages(save.error);
  const keysHint = p.credentialsSet ? t("phone.keysHint.set") : t("phone.keysHint.unset");
  const text = (id: keyof Draft, label: string, hint?: string, type = "text") => (
    <div className="field">
      <label htmlFor={`phone-${id}`}>{label}</label>
      <input
        id={`phone-${id}`}
        type={type}
        value={String(draft[id])}
        disabled={!canManage}
        autoComplete={type === "password" ? "new-password" : "off"}
        aria-invalid={Boolean(errors[id]) || undefined}
        aria-describedby={errors[id] ? `phone-${id}-error` : undefined}
        onChange={(e) => set(id, e.target.value as never)}
      />
      {hint ? <p className="field-hint">{hint}</p> : null}
      {errors[id] ? (
        <p className="field-error" role="alert" id={`phone-${id}-error`}>
          {errors[id]}
        </p>
      ) : null}
    </div>
  );

  return (
    <div className="phone-sign-in">
      <p className="section-lede">{t("phone.lede")}</p>
      <Notice level="caution-minor" title={t("phone.unverified.title")}>
        {t("phone.unverified.body")}
      </Notice>
      {p.provider ? (
        <p className="field-hint">{t("phone.sentToday", p.sentToday, p.dailyLimit ?? 0)}</p>
      ) : (
        <p className="field-hint">{t("phone.notConfigured")}</p>
      )}

      <div className="field">
        <label htmlFor="phone-provider">{t("phone.provider")}</label>
        <select
          id="phone-provider"
          value={draft.provider}
          disabled={!canManage}
          onChange={(e) => set("provider", e.target.value as PhoneSignInProvider)}
        >
          <option value="esms-sms">{t("phone.provider.esms-sms")}</option>
          <option value="esms-zalo">{t("phone.provider.esms-zalo")}</option>
        </select>
      </div>
      {draft.provider === "esms-sms"
        ? text("brandname", t("phone.brandname"), t("phone.brandnameHint"))
        : (
          <>
            {text("zaloOaId", t("phone.zaloOaId"))}
            {text("zaloTemplateId", t("phone.zaloTemplateId"), t("phone.zaloTemplateHint"))}
          </>
        )}
      {text("apiKey", t("phone.apiKey"), keysHint, "password")}
      {text("secretKey", t("phone.secretKey"), keysHint, "password")}
      {text("messageTemplate", t("phone.template"), t("phone.templateHint"))}
      {text("dailyLimit", t("phone.dailyLimit"), t("phone.dailyLimitHint"), "number")}
      {text("sessionMinutes", t("phone.sessionMinutes"), undefined, "number")}
      <label className="check">
        <input
          type="checkbox"
          checked={draft.sandbox}
          disabled={!canManage}
          onChange={(e) => set("sandbox", e.target.checked)}
        />{" "}
        {t("phone.sandbox")}
      </label>
      <p className="field-hint">
        {t(
          "phone.limits",
          p.limits.perNumberPerHour,
          p.limits.perNumberPerDay,
          p.limits.perDevicePerHour,
          p.limits.perAddressPerHour,
          p.limits.codeMinutes,
          p.limits.maxAttempts,
        )}
      </p>
      {save.isError && Object.keys(errors).length === 0 ? (
        <ErrorNotice error={save.error} title={t("phone.saveFailed")} />
      ) : null}
      {save.isSuccess ? <p className="field-hint" role="status">{t("phone.saved")}</p> : null}
      {canManage ? (
        <button
          type="button"
          className="btn btn-primary"
          aria-disabled={save.isPending || undefined}
          onClick={() => {
            if (!save.isPending) save.mutate(draft);
          }}
        >
          {t("phone.save")}
        </button>
      ) : null}
      {canManage && p.provider ? <TestMessage siteId={siteId} /> : null}
      <PhoneAbusePanel siteId={siteId} canManage={canManage} />
    </div>
  );
}

/** The sentence for a test message's outcome. Exported for its test. */
export function testOutcomeCopy(r: PhoneTestMessageResult): string {
  switch (r.outcome) {
    case "sent":
      return t("phone.test.sent");
    case "phone-invalid":
      return t("phone.test.phone-invalid");
    case "not-configured":
      return t("phone.test.not-configured");
    case "ceiling-reached":
      return t("phone.test.ceiling-reached");
    case "provider-refused":
      return t("phone.test.provider-refused", r.providerCode ?? "?");
    case "provider-unreachable":
      return t("phone.test.provider-unreachable");
  }
}

/**
 * One real message through the saved provider, to the operator's own number — how a
 * venue learns its brandname template is registered before a guest learns it is not.
 * It is counted against today's limits like any code, which the hint says.
 */
function TestMessage({ siteId }: { siteId: string }) {
  const qc = useQueryClient();
  const [phone, setPhone] = useState("");
  const send = useMutation({
    mutationFn: (n: string) =>
      request<PhoneTestMessageResult>(`/sites/${siteId}/portal/phone/test-message`, {
        method: "POST",
        body: JSON.stringify({ phone: n }),
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["phone-sign-in", siteId] }),
  });
  return (
    <div className="phone-test">
      <h3 className="section-title">{t("phone.test.title")}</h3>
      <div className="field">
        <label htmlFor="phone-test-number">{t("phone.test.label")}</label>
        <input
          id="phone-test-number"
          type="tel"
          inputMode="tel"
          autoComplete="off"
          maxLength={32}
          value={phone}
          aria-describedby="phone-test-hint"
          onChange={(e) => setPhone(e.target.value)}
        />
        <p className="field-hint" id="phone-test-hint">
          {t("phone.test.hint")}
        </p>
      </div>
      {send.isError ? <ErrorNotice error={send.error} title={t("phone.test.failed")} /> : null}
      {send.data ? (
        <p className="field-hint" role="status">
          {testOutcomeCopy(send.data)}
        </p>
      ) : null}
      <button
        type="button"
        className="btn"
        aria-disabled={send.isPending || !phone.trim() || undefined}
        onClick={() => {
          if (!send.isPending && phone.trim()) send.mutate(phone);
        }}
      >
        {t("phone.test.send")}
      </button>
    </div>
  );
}
