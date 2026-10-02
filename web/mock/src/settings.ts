/**
 * The maintenance schedule and the guest network's Wi-Fi password — the oracle's
 * world for the rule that every operator setting is configured in the console.
 *
 * `internal/store/maintenance.go` and `guestwifi.go` are the real thing and
 * `internal/api/handlers_settings.go` holds the refusals; this is the copy
 * `make diff-api` compares the Go server against, so its limits, defaults and
 * refusals are the store's exactly.
 */
import type {
  GuestWifiKeyStatus,
  MaintenanceSettingLimit,
  MaintenanceSettings,
  MaintenanceSettingsUpdate,
} from "@foyer/contract";

type Field = keyof MaintenanceSettingsUpdate;

/** `store.MaintenanceLimits`, field for field. */
export const MAINTENANCE_LIMITS: Record<Field, MaintenanceSettingLimit> = {
  intervalSeconds: { minimum: 60, maximum: 86400, default: 300 },
  staleAfterMissedUpdates: { minimum: 3, maximum: 48, default: 4 },
  staleCeilingPerPass: { minimum: 1, maximum: 100000, default: 200 },
  retentionBatchSize: { minimum: 50, maximum: 10000, default: 500 },
};

const FIELDS = Object.keys(MAINTENANCE_LIMITS) as Field[];

let stored: { values: MaintenanceSettingsUpdate; at: string; by: string | null } | null = null;
const wifi = new Map<string, { passphrase: string; at: string; by: string | null }>();

export function maintenanceSettings(): MaintenanceSettings {
  const values =
    stored?.values ??
    (Object.fromEntries(FIELDS.map((f) => [f, MAINTENANCE_LIMITS[f].default])) as MaintenanceSettingsUpdate);
  return {
    ...values,
    isDefault: stored === null,
    updatedAt: stored?.at ?? null,
    updatedBy: stored?.by ?? null,
    limits: structuredClone(MAINTENANCE_LIMITS),
  };
}

/** Each field outside its bounds, with the figure the refusal carries. */
type MaintenanceFault = { field: Field; fault: "below-minimum" | "above-maximum"; figure: number };

export function maintenanceFaults(body: MaintenanceSettingsUpdate): MaintenanceFault[] {
  return FIELDS.flatMap((f): MaintenanceFault[] => {
    const v = body[f];
    const l = MAINTENANCE_LIMITS[f];
    if (v < l.minimum) return [{ field: f, fault: "below-minimum", figure: l.minimum }];
    if (v > l.maximum) return [{ field: f, fault: "above-maximum", figure: l.maximum }];
    return [];
  });
}

/** Stores the values and answers what changed, `from` and `to`, for the audit entry. */
export function setMaintenanceSettings(
  body: MaintenanceSettingsUpdate,
  by: string | null,
): Record<string, { from: number; to: number }> {
  const before = maintenanceSettings();
  const values = Object.fromEntries(FIELDS.map((f) => [f, body[f]])) as MaintenanceSettingsUpdate;
  stored = { values, at: new Date().toISOString(), by };
  const changed: Record<string, { from: number; to: number }> = {};
  for (const f of FIELDS) if (before[f] !== values[f]) changed[f] = { from: before[f], to: values[f] };
  return changed;
}

/** IEEE 802.11 Annex J's passphrase: 8–63 printable ASCII. `api.ValidWifiPassphrase`. */
export function validWifiPassphrase(p: string): boolean {
  return /^[\x20-\x7e]{8,63}$/.test(p);
}

export function guestWifiKeyStatus(siteId: string): GuestWifiKeyStatus {
  const k = wifi.get(siteId);
  return k ? { set: true, updatedAt: k.at, updatedBy: k.by } : { set: false, updatedAt: null, updatedBy: null };
}

export function setGuestWifiKey(siteId: string, passphrase: string | null, by: string | null): void {
  if (passphrase === null) wifi.delete(siteId);
  else wifi.set(siteId, { passphrase, at: new Date().toISOString(), by });
}

/** The voucher slip's one read of the passphrase. */
export function slipPassphrase(siteId: string): string | null {
  return wifi.get(siteId)?.passphrase ?? null;
}

/** For tests: back to a fresh deployment. */
export function resetSettings(): void {
  stored = null;
  wifi.clear();
}
