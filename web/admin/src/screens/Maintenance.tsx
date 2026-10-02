import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { MaintenanceSettings, MaintenanceSettingsUpdate } from "@foyer/contract";
import { api } from "../api.js";
import { relativeTime } from "../format.js";
import { t } from "../i18n/maintenance.js";
import { fieldMessages } from "../problems.js";
import { ErrorNotice, Notice } from "../ui/primitives.js";
import { Advanced, SettingNumber, UNITS, type Unit } from "../ui/settings.js";

/**
 * The control plane's own maintenance schedule, on `/privacy` below the retention
 * periods it enforces, because every operator setting is configured in the console:
 * the interval is not an environment variable read once at start, and the other
 * three are not constants.
 *
 * In `/privacy`'s lazy chunk (a static import of `Privacy.tsx`), and administrators
 * only for the same reason: `privacy:manage`.
 *
 * **All four are technical, so all four are `Advanced`**:
 * collapsed by default, the interval in minutes rather than seconds, and each field
 * with its range, its default and a reset. A venue that never opens the section runs
 * the defaults, which is what the defaults are for.
 *
 * **Bounds and defaults are the server's**, read from `limits`, the way a retention
 * category's are; the inputs carry them as `min`/`max` and the hint states them, and
 * the server's refusal carries the figure when a value is out of range anyway.
 */
type Field = keyof MaintenanceSettingsUpdate;
const FIELDS: Field[] = ["intervalSeconds", "staleAfterMissedUpdates", "staleCeilingPerPass", "retentionBatchSize"];

const LABEL = (f: Field): string =>
  f === "intervalSeconds"
    ? t("maintenance.interval")
    : f === "staleAfterMissedUpdates"
      ? t("maintenance.stale")
      : f === "staleCeilingPerPass"
        ? t("maintenance.ceiling")
        : t("maintenance.batch");

/** The interval is held in seconds and read in minutes; the rest are counts. */
const UNIT: Record<Field, Unit> = {
  intervalSeconds: UNITS.secondsAsMinutes,
  staleAfterMissedUpdates: UNITS.count,
  staleCeilingPerPass: UNITS.count,
  retentionBatchSize: UNITS.count,
};

/** The reporting-interval floor the reaper counts against (radius.md): five minutes. */
const FLOOR_MINUTES = 5;

const hint = (f: Field, value: number): string =>
  f === "intervalSeconds"
    ? t("maintenance.interval.hint")
    : f === "staleAfterMissedUpdates"
      ? t("maintenance.stale.hint", value * FLOOR_MINUTES)
      : f === "staleCeilingPerPass"
        ? t("maintenance.ceiling.hint")
        : t("maintenance.batch.hint");

/** What a save sends: every figure the form holds, in the server's units. */
export const maintenanceBody = (s: MaintenanceSettings, draft: Partial<Record<Field, number>>) =>
  Object.fromEntries(FIELDS.map((f) => [f, draft[f] ?? s[f]])) as MaintenanceSettingsUpdate;

export function Maintenance() {
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: ["privacy", "maintenance"], queryFn: api.maintenanceSettings });
  const [draft, setDraft] = useState<Partial<Record<Field, number>>>({});
  const [saved, setSaved] = useState(false);

  const save = useMutation({
    mutationFn: (body: MaintenanceSettingsUpdate) => api.updateMaintenanceSettings(body),
    onSuccess: (next) => {
      qc.setQueryData(["privacy", "maintenance"], next);
      setDraft({});
      setSaved(true);
    },
  });
  const errors = fieldMessages(save.error);

  if (settings.error) return <ErrorNotice error={settings.error} title={t("maintenance.title")} />;
  const s: MaintenanceSettings | undefined = settings.data;
  const value = (f: Field) => draft[f] ?? (s ? s[f] : Number.NaN);
  const when = s?.updatedAt ? (relativeTime(s.updatedAt) ?? s.updatedAt) : "";

  return (
    <Advanced title={t("maintenance.title")} lede={t("maintenance.body")} hasError={Object.keys(errors).length > 0}>
      <section className="card" aria-label={t("maintenance.title")} style={{ marginBottom: "1.5rem" }}>
        {s ? (
          <p className="field-hint">
            {s.isDefault ? t("maintenance.default") : t("maintenance.updated", s.updatedBy ?? "", when)}
          </p>
        ) : null}

        <form
          onSubmit={(e) => {
            e.preventDefault();
            setSaved(false);
            if (!s || save.isPending) return;
            save.mutate(maintenanceBody(s, draft));
          }}
        >
          {save.error ? (
            <div style={{ margin: "0.875rem 0" }}>
              <ErrorNotice error={save.error} title={t("maintenance.failed")} />
            </div>
          ) : null}
          {saved ? (
            <div style={{ margin: "0.875rem 0" }}>
              <Notice level="positive" role="status" title={t("maintenance.saved")} />
            </div>
          ) : null}

          <div className="form-grid">
            {s
              ? FIELDS.map((f) => (
                  <SettingNumber
                    key={f}
                    label={LABEL(f)}
                    hint={hint(f, Number.isNaN(value(f)) ? s.limits[f].default : value(f))}
                    bound={s.limits[f]}
                    unit={UNIT[f]}
                    value={value(f)}
                    error={errors[f]}
                    onChange={(v) => {
                      setSaved(false);
                      setDraft((prev) => ({ ...prev, [f]: v }));
                    }}
                  />
                ))
              : null}
            <div className="row-actions">
              <button type="submit" className="btn btn-primary" aria-disabled={save.isPending || !s || undefined}>
                {t("maintenance.save")}
              </button>
              <button
                type="button"
                className="btn"
                onClick={() => {
                  if (!s) return;
                  setSaved(false);
                  setDraft(Object.fromEntries(FIELDS.map((f) => [f, s.limits[f].default])));
                }}
              >
                {t("maintenance.restore")}
              </button>
            </div>
          </div>
        </form>
      </section>
    </Advanced>
  );
}
