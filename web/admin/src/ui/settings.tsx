import { useEffect, useId, useState, type ReactNode } from "react";
import { t } from "../i18n/settings.js";

/**
 * The two shapes every settings screen shares. Laid flat, the Network screen is a
 * wall of 31 inputs — sign-in limits in milliseconds, request sizes in bytes,
 * FreeRADIUS threads — for a customer who runs a café; what it needs is an Advanced
 * section with safe defaults.
 *
 * ── what the field does, and what was taken ────────────────────────────────
 *
 *   - **UniFi Network** puts the handful of settings a site owner changes on the
 *     first screen of each section and every protocol knob under an "Advanced"
 *     toggle that is **off by default** and remembered per section; the knobs show
 *     their defaults and speak in seconds and minutes.
 *   - **Omada** does the same with "Advanced Settings" disclosures under each
 *     form, collapsed, with the range printed under every number field
 *     ("(1-3600) seconds").
 *   - **Meraki** exposes almost none of these at all and ships safe defaults; where
 *     it does (RADIUS timeouts) it speaks seconds and offers "Reset to default".
 *   - **authentik** and **Keycloak** keep every flow/brute-force figure an
 *     administrator may tune, grouped, each with its default beside it and a unit
 *     in words; Keycloak's brute-force detection speaks minutes and seconds, never
 *     milliseconds.
 *
 * So: the technical knobs are **not removed** — an operator setting lives in the
 * console, and the narrow reading of the problem is *a flat wall*, not
 * *configurability*.
 * They move behind one collapsed `Advanced` per screen, each number in the unit a
 * person thinks in, with its range and default **from the server** and a reset.
 *
 * `Advanced` opens itself when a field inside it is refused, so a refusal is never
 * hidden behind a disclosure the operator has to think to open.
 */
export function Advanced({
  title,
  lede,
  hasError = false,
  children,
}: {
  title?: string;
  lede?: string;
  hasError?: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (hasError) setOpen(true);
  }, [hasError]);
  return (
    <details className="advanced" open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>
        <span className="advanced-title">{title ?? t("settings.advanced.title")}</span>
        <span className="field-hint">{lede ?? t("settings.advanced.lede")}</span>
      </summary>
      <div className="advanced-body">{children}</div>
    </details>
  );
}

/** A setting's range and shipped figure, as the server sends it. */
export interface Bound {
  minimum: number;
  maximum: number;
  default: number;
}

/**
 * How a figure the server counts in one unit is shown in another. `factor` is how
 * many server units make one shown unit: 1 000 ms in a second, 1 024 bytes in a KB.
 */
export interface Unit {
  factor: number;
  name: (n: number) => string;
}

export const UNITS = {
  count: { factor: 1, name: () => "" },
  msAsSeconds: { factor: 1000, name: (n: number) => t("settings.unit.seconds", n) },
  seconds: { factor: 1, name: (n: number) => t("settings.unit.seconds", n) },
  secondsAsMinutes: { factor: 60, name: (n: number) => t("settings.unit.minutes", n) },
  minutes: { factor: 1, name: (n: number) => t("settings.unit.minutes", n) },
  bytesAsKb: { factor: 1024, name: () => t("settings.unit.kb") },
  bytesAsMb: { factor: 1024 * 1024, name: () => t("settings.unit.mb") },
} satisfies Record<string, Unit>;

/** Bytes are shown in MB where the smallest allowed value is at least one. */
export const bytesUnit = (b: Bound): Unit => (b.minimum >= 1024 * 1024 ? UNITS.bytesAsMb : UNITS.bytesAsKb);

/** A server figure in the shown unit, to at most three decimals, as a person writes it. */
export function shown(value: number, unit: Unit): string {
  if (Number.isNaN(value)) return "";
  return String(Math.round((value / unit.factor) * 1000) / 1000);
}

/** What the operator typed, back in the server's unit; `NaN` for nothing or not a number. */
export function parsed(text: string, unit: Unit): number {
  const v = text.trim() === "" ? Number.NaN : Number(text.replace(",", "."));
  return Number.isFinite(v) ? Math.round(v * unit.factor) : Number.NaN;
}

/** "Between 0.1 and 600 seconds. Default 1 second." — the hint every setting carries. */
export function boundHint(b: Bound, unit: Unit): string {
  const min = shown(b.minimum, unit);
  const max = shown(b.maximum, unit);
  const dflt = shown(b.default, unit);
  const u = unit.name(b.maximum / unit.factor);
  return u === ""
    ? `${t("settings.rangeCount", min, max)} ${t("settings.defaultCount", dflt)}`
    : `${t("settings.range", min, max, u)} ${t("settings.default", dflt, unit.name(b.default / unit.factor))}`;
}

/**
 * A number setting in the unit a person thinks in, with its range and default
 * stated, the range enforced as `min`/`max`, and a reset when it has moved.
 *
 * The text is the operator's until it parses to the value held, so typing "0." on
 * the way to "0.5" is not rewritten under the cursor.
 */
export function SettingNumber({
  label,
  hint,
  bound,
  unit = UNITS.count,
  value,
  error,
  readOnly = false,
  onChange,
}: {
  label: string;
  hint?: string;
  bound: Bound;
  unit?: Unit;
  value: number;
  error?: string | undefined;
  readOnly?: boolean;
  onChange: (next: number) => void;
}) {
  const id = useId();
  const [text, setText] = useState<string | null>(null);
  /* `value` is the only dependency, and the narrowness is the point. The box holds
     the operator's own characters until the figure they *parse to* moves away from
     the figure the server holds — a refusal, a reset, another tab's save.

     `text` is left out on purpose. In the list, this effect re-runs on every
     keystroke, and a keystroke that does not parse to a finite number makes `parsed`
     return `NaN`, and `NaN !== value` is true for every value including `NaN`. So the
     first character of an unfinished number — the `-` of `-1`, the stray letter in a
     field that should hold a digit — would be dropped as it was typed, and the box
     would empty itself under the cursor.

     `unit` is out for a measured reason: `Network.tsx` passes `unit={bytesUnit(bound)}`,
     a fresh object on every render, so a list naming it re-runs this effect on every
     render and the guard stops being a check on change. It comes back the day a
     screen can switch a field's unit — the text is then typed in the old one and has
     to be re-read — and the call site has to stop building a new `Unit` each time,
     which is what `UNITS.*` already does. */
  useEffect(() => {
    if (text !== null && parsed(text, unit) !== value) setText(null);
  }, [value]);
  const moved = !Number.isNaN(value) && value !== bound.default;
  const u = unit.name(Number.isNaN(value) ? 2 : value / unit.factor);
  const describedBy = [`${id}-hint`, error ? `${id}-error` : ""].filter(Boolean).join(" ");
  return (
    <div className="field setting">
      <label htmlFor={id}>{label}</label>
      <span className="setting-input">
        <input
          id={id}
          type="number"
          inputMode={unit.factor === 1 ? "numeric" : "decimal"}
          step={unit.factor === 1 ? 1 : "any"}
          min={bound.minimum / unit.factor}
          max={bound.maximum / unit.factor}
          value={text ?? shown(value, unit)}
          readOnly={readOnly}
          aria-invalid={Boolean(error) || undefined}
          aria-describedby={describedBy}
          onChange={(e) => {
            setText(e.target.value);
            onChange(parsed(e.target.value, unit));
          }}
        />
        {u ? <span className="setting-unit">{u}</span> : null}
        {moved && !readOnly ? (
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={() => {
              setText(null);
              onChange(bound.default);
            }}
          >
            {t("settings.reset")}
          </button>
        ) : null}
      </span>
      <p className="field-hint" id={`${id}-hint`}>
        {hint ? `${hint} ` : ""}
        {boundHint(bound, unit)}
        {moved ? ` ${t("settings.changed")}` : ""}
      </p>
      {error ? (
        <p className="field-error" role="alert" id={`${id}-error`}>
          {error}
        </p>
      ) : null}
    </div>
  );
}
