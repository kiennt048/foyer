/**
 * The `reports` operations' documents: the CSV writer, the three row mappings, and
 * the trend buckets.
 *
 * **The same rules as `internal/export` and `internal/api/handlers_reports.go`, and
 * the differential harness compares the bytes.** Two things are read from
 * `openapi.yaml` rather than restated: each export's `x-csv-columns`, and each
 * operation's `x-max-range-days`. Everything else — the neutralisation, the quoting,
 * the spelling of a timestamp — is written in both languages, and it is written to
 * match `internal/export.Writer` exactly: every field quoted, and a field's own `\n`
 * written as `\r\n` with its `\r` dropped.
 */
import type { MarketingConsent, Session, TrendBucket, TrendGranularity, Voucher } from "@foyer/contract";
import type { Grant } from "./state.js";
import { contractDocument } from "./validate.js";

// ── the contract's declarations ──────────────────────────────────────────────

type Operation = { operationId?: string; "x-csv-columns"?: string[]; "x-max-range-days"?: unknown };

function operation(operationId: string): Operation {
  for (const item of Object.values(contractDocument().paths)) {
    const op = (item as Record<string, Operation>).get;
    if (op?.operationId === operationId) return op;
  }
  throw new Error(`openapi.yaml declares no ${operationId}`);
}

/** The header an export writes, read from the document on every call it is used. */
export function csvColumns(operationId: string): string[] {
  const columns = operation(operationId)["x-csv-columns"];
  if (!columns?.length) throw new Error(`${operationId} declares no x-csv-columns`);
  return columns;
}

/** An export's range bound, or a trend's for one granularity. */
export function maxRangeDays(operationId: string, granularity?: TrendGranularity): number {
  const raw = operation(operationId)["x-max-range-days"];
  const n = typeof raw === "number" ? raw : (raw as Record<string, number> | undefined)?.[granularity ?? "day"];
  if (typeof n !== "number") throw new Error(`${operationId} declares no x-max-range-days`);
  return n;
}

// ── cells ────────────────────────────────────────────────────────────────────

/**
 * OWASP's triggers, the full-width forms, and the small-form variants
 * U+FE66 FE62 FE63 FE6B and the minus sign U+2212. `internal/export.triggers`.
 */
const TRIGGERS = new Set(["=", "+", "-", "@", "\t", "\r", "\n", "＝", "＋", "－", "＠", "﹦", "﹢", "﹣", "﹫", "−"]);

/** A cell a spreadsheet will display rather than evaluate. `export.Neutralise`. */
export function neutralise(cell: string): string {
  if (cell === "" || /^-?[0-9]+(\.[0-9]+)?$/.test(cell)) return cell;
  return TRIGGERS.has(firstSignificant(cell)) ? `'${cell}` : cell;
}

/**
 * The first rune a spreadsheet looks at: leading spaces and format characters (Cf:
 * BOM, zero-width, bidi marks) skipped, TAB / CR / LF ending the skip because they
 * are triggers themselves. `export.firstSignificant`.
 */
function firstSignificant(cell: string): string {
  for (const ch of cell) {
    if (ch === "\t" || ch === "\r" || ch === "\n") return ch;
    if (GO_SPACE.test(ch) || /\p{Cf}/u.test(ch)) continue;
    return ch;
  }
  return "";
}

/** `unicode.IsSpace` — the leading blanks a spreadsheet looks past. */
const GO_SPACE = /[\t\n\v\f\r \u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]/;

/**
 * One field as `export.Writer` writes it: **always quoted** (a spreadsheet
 * whose separator is `;` cannot split a quoted field), `"` doubled, `\n` written as
 * `\r\n`, a lone `\r` dropped.
 */
function field(value: string): string {
  let out = '"';
  for (const ch of value) {
    if (ch === '"') out += '""';
    else if (ch === "\r") continue;
    else if (ch === "\n") out += "\r\n";
    else out += ch;
  }
  return out + '"';
}

/** A whole document: byte-order mark, header, rows, every cell neutralised. */
export function csvDocument(columns: string[], rows: string[][]): string {
  const record = (cells: string[]) => cells.map((c) => field(neutralise(c))).join(",") + "\r\n";
  return "\uFEFF" + record(columns) + rows.map(record).join("");
}

/** RFC 3339, UTC, second precision — `export.Time`. */
export function time(iso: string | number | null | undefined): string {
  if (iso === null || iso === undefined) return "";
  return new Date(iso).toISOString().replace(/\.\d{3}Z$/, "Z");
}

const text = (v: string | null | undefined) => v ?? "";
const int = (v: number | null | undefined) => (v === null || v === undefined ? "" : String(v));
const bool = (v: boolean | null | undefined) => (v === null || v === undefined ? "" : String(v));

/** The last four characters, never the code. `export.CodeHint`. */
export function codeHint(code: string | null | undefined): string {
  const chars = [...(code ?? "")];
  return chars.length <= 4 ? "" : "…" + chars.slice(-4).join("");
}

/** The locally-administered bit. `export.LocallyAdministered`. */
function locallyAdministered(mac: string): boolean {
  const hex = mac.toLowerCase().replace(/[^0-9a-f]/g, "").slice(0, 2);
  return hex.length === 2 && (parseInt(hex, 16) & 0x02) !== 0;
}

// ── the three row mappings, by column name ───────────────────────────────────

const SESSION_CELLS: Record<string, (s: Session) => string> = {
  sessionId: (s) => s.id,
  acctSessionId: (s) => text(s.acctSessionId),
  state: (s) => s.state,
  startedAt: (s) => time(s.startedAt),
  endedAt: (s) => time(s.endedAt),
  lastUpdateAt: (s) => time(s.lastUpdateAt),
  sessionSeconds: (s) => int(s.sessionSeconds),
  identityKind: (s) => text(s.identityKind),
  username: (s) => (s.identityKind === "voucher" ? codeHint(s.username) : text(s.username)),
  callingStationId: (s) => s.callingStationId,
  macIsRandomised: (s) => bool(s.macIsRandomised),
  ipAddress: (s) => text(s.ipAddress),
  nasId: (s) => s.nasId,
  nasName: (s) => text(s.nasName),
  apName: (s) => text(s.apName),
  ssid: (s) => text(s.ssid),
  inputOctets: (s) => int(s.inputOctets),
  outputOctets: (s) => int(s.outputOctets),
  inputOctetsTotal: (s) => int(s.inputOctetsTotal),
  outputOctetsTotal: (s) => int(s.outputOctetsTotal),
  countersWrapped: (s) => String(s.countersWrapped),
  terminateCause: (s) => text(s.terminateCause),
  freshnessState: (s) => s.freshness.state,
  freshnessSampledAt: (s) => time(s.freshness.sampledAt),
  freshnessIntervalSeconds: (s) => int(s.freshness.intervalSeconds),
};

const VOUCHER_CELLS: Record<string, (v: Voucher) => string> = {
  voucherId: (v) => v.id,
  batchId: (v) => v.batchId,
  codeHint: (v) => codeHint(v.code),
  state: (v) => v.state,
  createdAt: (v) => time(v.createdAt),
  firstUsedAt: (v) => time(v.firstUsedAt),
  expiresAt: (v) => time(v.expiresAt),
  revokedAt: (v) => time(v.revokedAt),
};

/** A grant's method, which the mock's `Grant` implies rather than stores. */
function method(g: Grant): "voucher" | "account" | "click-through" {
  return g.voucherId ? "voucher" : g.username ? "account" : "click-through";
}

const GUEST_CELLS: Record<string, (g: Grant) => string> = {
  grantId: (g) => g.id,
  grantedAt: (g) => time(g.grantedAt),
  method: (g) => method(g),
  clientMac: (g) => g.mac,
  macIsRandomised: (g) => String(locallyAdministered(g.mac)),
  accountUsername: (g) => (method(g) === "account" ? text(g.username) : ""),
  voucherId: (g) => text(g.voucherId),
  expiresAt: (g) => time(g.expiresAt),
  /* The mock records revocation as a state and not as a time, so the column is
     empty here where Go's has a timestamp. */
  revokedAt: () => "",
};

/**
 * Binds a mapping to the contract's columns, refusing a column either side lacks —
 * `readReports` in Go refuses to start on the same disagreement.
 */
function bind<T>(operationId: string, cells: Record<string, (row: T) => string>) {
  const columns = csvColumns(operationId);
  const missing = columns.filter((c) => !(c in cells));
  const extra = Object.keys(cells).filter((c) => !columns.includes(c));
  if (missing.length || extra.length) {
    throw new Error(
      `${operationId}: x-csv-columns and web/mock disagree — declared without a value: ` +
        `${missing.join(", ") || "none"}; a value not declared: ${extra.join(", ") || "none"}`,
    );
  }
  return { columns, row: (v: T) => columns.map((c) => cells[c]!(v)) };
}

/** `consentCells`. A withdrawn row has no contact to write, by construction. */
const CONSENT_CELLS: Record<string, (c: MarketingConsent) => string> = {
  consentId: (c) => c.id,
  consentedAt: (c) => time(c.consentedAt),
  state: (c) => c.state,
  withdrawnAt: (c) => time(c.withdrawnAt),
  withdrawnBy: (c) => text(c.withdrawnBy),
  name: (c) => text(c.name),
  phone: (c) => text(c.phone),
  email: (c) => text(c.email),
  lang: (c) => c.lang,
  consentVersion: (c) => c.consentVersion,
  clientMac: (c) => c.clientMac,
};

export const DATASETS = {
  sessions: bind<Session>("exportSessions", SESSION_CELLS),
  vouchers: bind<Voucher>("exportVouchers", VOUCHER_CELLS),
  guests: bind<Grant>("exportGuests", GUEST_CELLS),
  consents: bind<MarketingConsent>("exportMarketingConsents", CONSENT_CELLS),
};

/** `internal/api`'s `exportFilename`. */
export function exportFilename(dataset: string, siteId: string, from: Date, to: Date): string {
  const day = (d: Date) => d.toISOString().slice(0, 10).replace(/-/g, "");
  return `foyer-${dataset}-${siteId.replace(/[^A-Za-z0-9_-]/g, "")}-${day(from)}-${day(to)}.csv`;
}

// ── trends ───────────────────────────────────────────────────────────────────

export interface Range {
  from: number;
  to: number;
}

/** How far `zone`'s wall clock is ahead of UTC at instant `t`, in milliseconds. */
function zoneOffset(zone: string, t: number): number {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    })
      .formatToParts(new Date(t))
      .map((p) => [p.type, p.value]),
  );
  const wall = Date.UTC(+parts.year!, +parts.month! - 1, +parts.day!, +parts.hour!, +parts.minute!, +parts.second!);
  return wall - Math.floor(t / 1000) * 1000;
}

/** The instant `zone`'s calendar day `y-m-d` begins (month 0-based, day may overflow). */
function zoneMidnight(zone: string, y: number, m: number, d: number): number {
  const guess = Date.UTC(y, m, d);
  const first = guess - zoneOffset(zone, guess);
  /* Once more at the answer: an offset that changed between the guess and midnight
     (a clock moved that day) is the offset in force at midnight. */
  return guess - zoneOffset(zone, first);
}

/**
 * Buckets on the **site's** calendar (`getSiteTimeZone`), Monday-start weeks, `from`
 * floored and `to` ceiled — `internal/api`'s `trendBuckets`. It used the mock
 * process's own days, which is the defect the site time zone exists to remove.
 */
export function trendBuckets(from: Date, to: Date, granularity: TrendGranularity, zone = "UTC"): Range[] {
  const wall = new Date(from.getTime() + zoneOffset(zone, from.getTime()));
  const y = wall.getUTCFullYear();
  const m = wall.getUTCMonth();
  let d = wall.getUTCDate();
  const days = granularity === "week" ? 7 : 1;
  if (granularity === "week") d -= (wall.getUTCDay() + 6) % 7;
  const out: Range[] = [];
  for (let b = zoneMidnight(zone, y, m, d); b < to.getTime() && out.length <= 1000; ) {
    const next = zoneMidnight(zone, y, m, d + days);
    out.push({ from: b, to: next });
    d += days;
    b = next;
  }
  return out;
}

const contains = (r: Range, t: number) => t >= r.from && t < r.to;

/** Twelve hex digits or nothing — a device counted once whatever the spelling. */
function macKey(mac: string | null | undefined): string {
  const hex = (mac ?? "").toLowerCase().replace(/[^0-9a-f]/g, "");
  return hex.length === 12 ? hex : "";
}

/** The four rules `internal/api/memory/reports.go` and `internal/store` apply. */
export function countBucket(
  b: Range,
  sessions: readonly Session[],
  grants: readonly Grant[],
  redeemedAt: readonly number[],
  now: number,
): Omit<TrendBucket, "start" | "end"> {
  const macs = new Set<string>();
  let bytes = 0;
  let wrapped = false;
  let oldest: number | null = null;
  for (const s of sessions) {
    if (!contains(b, Date.parse(s.startedAt))) continue;
    const m = macKey(s.callingStationId);
    if (m) macs.add(m);
    if (s.inputOctetsTotal == null && s.outputOctetsTotal == null) continue;
    bytes += (s.inputOctetsTotal ?? 0) + (s.outputOctetsTotal ?? 0);
    wrapped ||= s.countersWrapped;
    if (!s.endedAt) {
      const heard = Date.parse(s.lastUpdateAt ?? s.startedAt);
      if (oldest === null || heard < oldest) oldest = heard;
    }
  }
  for (const g of grants) {
    if (!contains(b, g.grantedAt)) continue;
    const m = macKey(g.mac);
    if (m) macs.add(m);
  }
  return {
    guestDevices: macs.size,
    vouchersRedeemed: redeemedAt.filter((t) => contains(b, t)).length,
    bytes,
    bytesLowerBound: wrapped,
    bytesFreshness:
      oldest === null
        ? { state: "live", sampledAt: new Date(now).toISOString(), intervalSeconds: null }
        : {
            state: now - oldest > 600_000 ? "stale" : "sampled",
            sampledAt: new Date(oldest).toISOString(),
            intervalSeconds: 300,
          },
  };
}
