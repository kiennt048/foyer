import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import type {
  PhoneAbuseControls,
  PhoneAbuseControlsUpdate,
  PhoneSignInLimits,
  PhoneSpendCap,
} from "@foyer/contract";
import { request } from "../../api.js";
import { t } from "../../i18n/phoneabuse.js";
import { fieldMessages } from "../../problems.js";
import { ErrorNotice } from "../../ui/primitives.js";
import { Advanced, SettingNumber, UNITS } from "../../ui/settings.js";

/**
 * Phone sign-in's abuse limits and the deployment-wide SMS cap, under the provider
 * on the sign-in tab of `/portal`, because every operator setting is configured in
 * the console.
 *
 * Imported statically by `PhoneSignIn.tsx`, so it travels in the portal screen's lazy
 * chunk and costs the entry nothing; its requests are made here with `request`
 * rather than added to `api`, which *is* the entry.
 *
 * **Bounded, not free.** Each input carries the server's range as `min`/`max` and
 * says it, because the limits are the product's defence against SMS toll fraud: the
 * server refuses anything outside them, and the form should not invite it. The
 * ranges and defaults arrive in `bounds` rather than being restated here, which would
 * be a second copy of the contract's that nothing held to the first.
 *
 * **The limits are `Advanced`**: collapsed under the
 * provider, each with its default and a reset. The deployment-wide cap stays in view,
 * because it is money rather than a protocol figure.
 */
const LIMIT_ORDER: (keyof PhoneSignInLimits)[] = [
  "perNumberPerHour",
  "perNumberPerDay",
  "perDevicePerHour",
  "perAddressPerHour",
  "codeMinutes",
  "maxAttempts",
];
export const PREFIXES = ["03", "05", "07", "08", "09"] as const;

export interface AbuseDraft {
  limits: Record<keyof PhoneSignInLimits, number>;
  prefixes: string[];
}

export const toAbuseDraft = (c: PhoneAbuseControls): AbuseDraft => ({
  limits: { ...c.limits },
  prefixes: [...c.mobilePrefixes],
});

/** What a save sends: every limit as a number, the ranges in the plan's order. */
export function abuseBody(d: AbuseDraft): PhoneAbuseControlsUpdate {
  return {
    limits: Object.fromEntries(LIMIT_ORDER.map((k) => [k, d.limits[k]])) as PhoneSignInLimits,
    mobilePrefixes: PREFIXES.filter((p) => d.prefixes.includes(p)),
  };
}

/** Empty is *no cap*; anything else is the number typed. */
export const capBody = (v: string) => ({ dailyLimit: v.trim() === "" ? null : Number(v) });

export function PhoneAbusePanel({ siteId, canManage }: { siteId: string; canManage: boolean }) {
  const qc = useQueryClient();
  const path = `/sites/${siteId}/portal/phone/abuse-controls`;
  const query = useQuery({ queryKey: ["phone-abuse", siteId], queryFn: () => request<PhoneAbuseControls>(path) });
  const [draft, setDraft] = useState<AbuseDraft | null>(() => (query.data ? toAbuseDraft(query.data) : null));
  useEffect(() => {
    if (query.data) setDraft(toAbuseDraft(query.data));
  }, [siteId, query.data]);
  const save = useMutation({
    mutationFn: (d: AbuseDraft) =>
      request<PhoneAbuseControls>(path, { method: "PUT", body: JSON.stringify(abuseBody(d)) }),
    onSuccess: (next) => {
      setDraft(toAbuseDraft(next));
      void qc.invalidateQueries({ queryKey: ["phone-abuse", siteId] });
      // The provider panel states the limits in force; it must not state the old ones.
      void qc.invalidateQueries({ queryKey: ["phone-sign-in", siteId] });
    },
  });

  if (query.isError) return <ErrorNotice error={query.error} />;
  if (!query.data || !draft) return null;
  const errors = fieldMessages(save.error);

  return (
    <div className="phone-abuse">
      <Advanced title={t("abuse.title")} lede={t("abuse.lede")} hasError={Object.keys(errors).length > 0}>
        {query.data.customised ? null : <p className="field-hint">{t("abuse.defaults")}</p>}
        {LIMIT_ORDER.map((k) => (
          <SettingNumber
            key={k}
            label={t(`abuse.${k}` as const)}
            hint={k === "perAddressPerHour" ? t("abuse.perAddressHint") : undefined}
            bound={query.data.bounds[k]}
            unit={k === "codeMinutes" ? UNITS.minutes : UNITS.count}
            value={draft.limits[k]}
            readOnly={!canManage}
            error={errors[`limits.${k}`]}
            onChange={(v) => setDraft({ ...draft, limits: { ...draft.limits, [k]: v } })}
          />
        ))}
        <fieldset className="field">
          <legend>{t("abuse.prefixes")}</legend>
          {PREFIXES.map((p) => (
            <label className="check" key={p}>
              <input
                type="checkbox"
                checked={draft.prefixes.includes(p)}
                disabled={!canManage}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    prefixes: e.target.checked ? [...draft.prefixes, p] : draft.prefixes.filter((x) => x !== p),
                  })
                }
              />{" "}
              {p}x
            </label>
          ))}
          <p className="field-hint">{t("abuse.prefixesHint")}</p>
          {errors.mobilePrefixes ? (
            <p className="field-error" role="alert">
              {errors.mobilePrefixes}
            </p>
          ) : null}
        </fieldset>
        {save.isError && Object.keys(errors).length === 0 ? (
          <ErrorNotice error={save.error} title={t("abuse.saveFailed")} />
        ) : null}
        {save.isSuccess ? (
          <p className="field-hint" role="status">
            {t("abuse.saved")}
          </p>
        ) : null}
        {canManage ? (
          <button
            type="button"
            className="btn"
            aria-disabled={save.isPending || undefined}
            onClick={() => {
              if (!save.isPending) save.mutate(draft);
            }}
          >
            {t("abuse.save")}
          </button>
        ) : null}
      </Advanced>
      <SpendCapPanel canManage={canManage} />
    </div>
  );
}

function SpendCapPanel({ canManage }: { canManage: boolean }) {
  const qc = useQueryClient();
  const path = "/phone-sign-in/spend-cap";
  const query = useQuery({ queryKey: ["phone-spend-cap"], queryFn: () => request<PhoneSpendCap>(path) });
  const [value, setValue] = useState<string | null>(() =>
    query.data ? (query.data.dailyLimit == null ? "" : String(query.data.dailyLimit)) : null,
  );
  useEffect(() => {
    if (query.data) setValue(query.data.dailyLimit == null ? "" : String(query.data.dailyLimit));
  }, [query.data]);
  const save = useMutation({
    mutationFn: (v: string) => request<PhoneSpendCap>(path, { method: "PUT", body: JSON.stringify(capBody(v)) }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["phone-spend-cap"] }),
  });
  if (query.isError) return <ErrorNotice error={query.error} />;
  if (!query.data || value === null) return null;
  const errors = fieldMessages(save.error);

  return (
    <div className="phone-spend-cap">
      <h3 className="section-title">{t("cap.title")}</h3>
      <p className="section-lede">{t("cap.lede")}</p>
      {query.data.dailyLimit == null ? <p className="field-hint">{t("cap.none")}</p> : null}
      <div className="field">
        <label htmlFor="phone-spend-cap">{t("cap.label")}</label>
        <input
          id="phone-spend-cap"
          type="number"
          min={query.data.bounds.minimum}
          max={query.data.bounds.maximum}
          value={value}
          disabled={!canManage}
          aria-invalid={Boolean(errors.dailyLimit) || undefined}
          aria-describedby="phone-spend-cap-hint"
          onChange={(e) => setValue(e.target.value)}
        />
        <p className="field-hint" id="phone-spend-cap-hint">
          {t("cap.hint")} {t("abuse.range", query.data.bounds.minimum, query.data.bounds.maximum)}
        </p>
        {errors.dailyLimit ? (
          <p className="field-error" role="alert">
            {errors.dailyLimit}
          </p>
        ) : null}
      </div>
      {save.isError && Object.keys(errors).length === 0 ? (
        <ErrorNotice error={save.error} title={t("cap.saveFailed")} />
      ) : null}
      {save.isSuccess ? (
        <p className="field-hint" role="status">
          {t("cap.saved")}
        </p>
      ) : null}
      {canManage ? (
        <button
          type="button"
          className="btn"
          aria-disabled={save.isPending || undefined}
          onClick={() => {
            if (!save.isPending) save.mutate(value);
          }}
        >
          {t("cap.save")}
        </button>
      ) : null}
    </div>
  );
}
