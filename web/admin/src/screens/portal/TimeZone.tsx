import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { localDate, type SiteTimeZone } from "@foyer/contract";
import { request } from "../../api.js";
import { t } from "../../i18n/sitetime.js";
import { LOCALE } from "../../i18n/index.js";
import { fieldMessages } from "../../problems.js";
import { ErrorNotice } from "../../ui/primitives.js";

/**
 * The site's time zone (`getSiteTimeZone`), a section of its own on the sign-in tab
 * of `/portal` — where the other venue-wide settings an owner changes already are.
 *
 * In the portal screen's lazy chunk, with its own copy namespace and its requests
 * made through `request` here rather than on `api`, so the entry chunk carries only
 * the one refusal sentence (`fault.not-a-time-zone`) every screen's error path needs.
 *
 * **The list is the browser's own zone database** (`Intl.supportedValuesOf`), so the
 * console carries no copy of it; the server checks the choice against its own and
 * refuses what it does not know. The stored zone is added if the browser's list
 * lacks it, so a select never silently shows a different value from the one saved.
 */

/** Every zone this browser knows, with `current` guaranteed to be among them. */
export function zoneOptions(current: string): string[] {
  let zones: string[] = [];
  try {
    zones = (Intl as unknown as { supportedValuesOf(k: string): string[] }).supportedValuesOf("timeZone");
  } catch {
    zones = [];
  }
  if (!zones.includes("UTC")) zones = [...zones, "UTC"];
  return zones.includes(current) ? zones : [current, ...zones];
}

/** The time now in a zone, for the sentence under the select. */
export function nowIn(zone: string, at = new Date()): string {
  try {
    return new Intl.DateTimeFormat(LOCALE, { timeZone: zone, hour: "2-digit", minute: "2-digit", weekday: "short" }).format(at);
  } catch {
    return "";
  }
}

/**
 * The site's calendar day, `YYYY-MM-DD` — what a block's dates are compared against
 * on the guest's page (`Server.siteToday`). The operator's browser may be anywhere;
 * the venue's day is the one that decides whether today's offer is live.
 */
export function useSiteToday(siteId: string): string {
  const query = useQuery({
    queryKey: ["site-time-zone", siteId],
    queryFn: () => request<SiteTimeZone>(`/sites/${siteId}/time-zone`),
  });
  return localDate(new Date(), query.data?.timeZone);
}

export function TimeZonePanel({ siteId, canManage }: { siteId: string; canManage: boolean }) {
  const qc = useQueryClient();
  const path = `/sites/${siteId}/time-zone`;
  const query = useQuery({ queryKey: ["site-time-zone", siteId], queryFn: () => request<SiteTimeZone>(path) });
  const [zone, setZone] = useState<string | null>(null);
  useEffect(() => {
    if (query.data) setZone(query.data.timeZone);
  }, [siteId, query.data]);
  const save = useMutation({
    mutationFn: (z: string) => request<SiteTimeZone>(path, { method: "PUT", body: JSON.stringify({ timeZone: z }) }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["site-time-zone", siteId] });
      // `sentToday` counts the site's day, which just moved.
      void qc.invalidateQueries({ queryKey: ["phone-sign-in", siteId] });
    },
  });

  if (query.isError) return <ErrorNotice error={query.error} />;
  if (!query.data || zone === null) return null;
  const errors = fieldMessages(save.error);

  return (
    <section className="card portal-section">
      <h2 className="section-title">{t("siteTime.title")}</h2>
      <p className="section-lede">{t("siteTime.lede")}</p>
      <div className="field">
        <label htmlFor="site-time-zone">{t("siteTime.label")}</label>
        <select
          id="site-time-zone"
          value={zone}
          disabled={!canManage}
          aria-invalid={Boolean(errors.timeZone) || undefined}
          aria-describedby="site-time-zone-hint"
          onChange={(e) => setZone(e.target.value)}
        >
          {zoneOptions(query.data.timeZone).map((z) => (
            <option key={z} value={z}>
              {z}
            </option>
          ))}
        </select>
        <p className="field-hint" id="site-time-zone-hint">
          {t("siteTime.now", nowIn(zone))}
        </p>
        {errors.timeZone ? (
          <p className="field-error" role="alert">
            {errors.timeZone}
          </p>
        ) : null}
      </div>
      {save.isError && Object.keys(errors).length === 0 ? (
        <ErrorNotice error={save.error} title={t("siteTime.saveFailed")} />
      ) : null}
      {save.isSuccess ? (
        <p className="field-hint" role="status">
          {t("siteTime.saved")}
        </p>
      ) : null}
      {canManage ? (
        <button
          type="button"
          className="btn"
          aria-disabled={save.isPending || undefined}
          onClick={() => {
            if (!save.isPending) save.mutate(zone);
          }}
        >
          {t("siteTime.save")}
        </button>
      ) : null}
    </section>
  );
}
