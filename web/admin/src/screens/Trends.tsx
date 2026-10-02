import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { TrendBucket, TrendGranularity } from "@foyer/contract";
import { api } from "../api.js";
import { useCan } from "../auth.js";
import { bytes, freshnessNote } from "../format.js";
import { LOCALE } from "../i18n/index.js";
import { t } from "../i18n/reports.js";
import { useSite } from "../site.js";
import { ErrorNotice, Foot, type FootAct } from "../ui/primitives.js";
import { metricFoot } from "./Dashboard.js";

/**
 * The dashboard's trends panel and the CSV exports.
 *
 * ── why bars, and why this is not the bandwidth graph the dashboard refuses ──
 *
 * `Dashboard.tsx` says there is no bandwidth graph and there will not be one: a
 * line drawn from five-minute interim updates is a picture of an interval, not of
 * traffic. That refusal stands. What this draws is a **total per day or per week**,
 * which is a different claim — the bucket is 288 intervals wide — and it is drawn
 * as bars because a line between two totals claims a continuity nothing measured
 * (`getTrends` says so in the contract). The byte figure still travels with its
 * freshness, in the same words the cards use, because a total over an open session
 * is still a sample.
 *
 * ── why hand-rolled SVG ─────────────────────────────────────────────────────
 *
 * A chart library is tens of kilobytes for a single-series bar chart, and the
 * console's entry chunk is at 97 % of its budget. This is a lazy chunk, so the
 * budget does not gate it — but the operator on a venue's back-office laptop still
 * downloads it, and one series of bars is forty lines of geometry.
 *
 * One series at a time, chosen by a select, so there is one axis: guests, vouchers
 * and bytes have nothing like a common scale, and two y-axes on one chart is the
 * first thing every chart guide says not to do.
 */

type Figure = "guestDevices" | "vouchersRedeemed" | "bytes";

const FIGURES: Figure[] = ["guestDevices", "vouchersRedeemed", "bytes"];
const DATASETS = ["sessions", "vouchers", "guests"] as const;

/**
 * 14 days or 12 weeks back from today, as the `[from, to)` the server floors and ceils.
 *
 * **Not called `window`.** A module-level `window` shadows the global that
 * `@vitejs/plugin-react` reads in the refresh header it prepends to every component
 * module, so in `vite dev` the chunk throws *"can't detect preamble"* while the
 * production build — which has no refresh header — is fine. Only the e2e run starts
 * the dev server, so only it can see this.
 */
function trendRange(g: TrendGranularity): { from: string; to: string } {
  const now = new Date();
  const from = new Date(now);
  from.setDate(from.getDate() - (g === "week" ? 7 * 11 : 13));
  return { from: from.toISOString(), to: now.toISOString() };
}

/** A date input's value — the operator's local calendar day. */
function day(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** A calendar day as the instant its local midnight is. */
function midnight(value: string, plusDays = 0): string {
  const [y, m, d] = value.split("-").map(Number);
  return new Date(y!, m! - 1, d! + plusDays).toISOString();
}

function valueOf(b: TrendBucket, f: Figure): number {
  return f === "bytes" ? b.bytes : f === "guestDevices" ? b.guestDevices : b.vouchersRedeemed;
}

function show(b: TrendBucket, f: Figure): string {
  if (f !== "bytes") return String(valueOf(b, f));
  const v = bytes(b.bytes) ?? "–";
  return b.bytesLowerBound ? t("trends.atLeast", v) : v;
}

const label = (iso: string, g: TrendGranularity) =>
  new Date(iso).toLocaleDateString(LOCALE, g === "week" ? { day: "numeric", month: "short" } : {
    weekday: "short",
    day: "numeric",
  });

/**
 * The freshness line for the byte figure: the oldest sample over every bucket, in
 * the cards' own words. `live` everywhere is the only case that says *live*; one
 * stale bucket makes the whole line stale, because a total is only as current as its
 * least current part — `Metric`'s second clause, over the chart.
 */
function bytesFoot(buckets: TrendBucket[]): { text: string; act: FootAct; title: string } {
  const sampled = buckets
    .map((b) => b.bytesFreshness)
    .filter((f) => f.state !== "live" && f.sampledAt)
    .sort((a, b) => Date.parse(a.sampledAt!) - Date.parse(b.sampledAt!));
  const freshness = sampled[0] ?? buckets[0]?.bytesFreshness ?? { state: "live" as const };
  /* The cards' own renderer, so the chart and the `clientsOnline` card say one
     sentence for one freshness. Importing it from `Dashboard.tsx` is also what
     keeps the entry one chunk — see the note above `TrendsPanel` in `Dashboard.tsx`. */
  return { ...metricFoot({ value: null, freshness }), title: freshnessNote(freshness) };
}

export function TrendsPanel() {
  const { siteId } = useSite();
  const [figure, setFigure] = useState<Figure>("guestDevices");
  const [granularity, setGranularity] = useState<TrendGranularity>("day");
  const query = useQuery({
    queryKey: ["trends", siteId, granularity],
    queryFn: () => api.trends({ siteId, granularity, ...trendRange(granularity) }),
    refetchInterval: 60_000,
  });
  const buckets = query.data?.buckets ?? [];
  const figureName = t(`trends.metric.${figure}`);
  const period = t(`trends.range.${granularity}`);

  return (
    <section className="section" aria-labelledby="trends-title">
      <h2 className="section-title" id="trends-title">
        {t("trends.title")}
      </h2>
      <div className="card">
        <div className="toolbar">
          <label className="field">
            <span className="visually-hidden">{t("trends.metric")}</span>
            <select value={figure} onChange={(e) => setFigure(e.target.value as Figure)}>
              {FIGURES.map((f) => (
                <option key={f} value={f}>
                  {t(`trends.metric.${f}`)}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span className="visually-hidden">{t("trends.range")}</span>
            <select
              value={granularity}
              onChange={(e) => setGranularity(e.target.value as TrendGranularity)}
            >
              <option value="day">{t("trends.range.day")}</option>
              <option value="week">{t("trends.range.week")}</option>
            </select>
          </label>
        </div>

        {query.isError ? (
          <ErrorNotice error={query.error} title={t("trends.failed.title")} />
        ) : query.isLoading ? (
          <span className="skeleton" style={{ display: "block", height: 160 }} />
        ) : (
          <>
            <Bars buckets={buckets} figure={figure} granularity={granularity}
              name={t("trends.chart", figureName, period)} />
            <p style={{ margin: "0.5rem 0 0", color: "var(--text-muted)", fontSize: 12 }}>
              {t(`trends.note.${figure}`)}
              {figure === "bytes" && buckets.some((b) => b.bytesLowerBound)
                ? ` ${t("trends.lowerBound")}`
                : ""}
            </p>
            {figure === "bytes" ? (
              <BytesFoot buckets={buckets} />
            ) : (
              <Foot act="live">{t("metric.live")}</Foot>
            )}
          </>
        )}
      </div>
      <ExportPanel />
    </section>
  );
}

function BytesFoot({ buckets }: { buckets: TrendBucket[] }) {
  const foot = bytesFoot(buckets);
  return (
    <Foot act={foot.act} title={foot.title}>
      {foot.text}
    </Foot>
  );
}

const W = 1000;
const H = 160;
const PAD_TOP = 18;

/**
 * One series of bars, a baseline, the highest value named, and the first and last
 * periods labelled. Each bar carries an SVG `<title>`, which every browser shows on
 * hover and a screen reader reads on focus; the same figures are in a table beside it
 * for anybody who would rather read than point.
 */
export function Bars({
  buckets,
  figure,
  granularity,
  name,
}: {
  buckets: TrendBucket[];
  figure: Figure;
  granularity: TrendGranularity;
  name: string;
}) {
  const max = Math.max(1, ...buckets.map((b) => valueOf(b, figure)));
  const slot = W / Math.max(1, buckets.length);
  const peak = buckets.reduce<TrendBucket | null>(
    (best, b) => (!best || valueOf(b, figure) > valueOf(best, figure) ? b : best),
    null,
  );
  return (
    <figure style={{ margin: 0 }}>
      <svg
        viewBox={`0 0 ${W} ${H + 20}`}
        width="100%"
        role="img"
        aria-label={name}
        data-testid="trends-chart"
        style={{ display: "block" }}
      >
        {peak ? (
          <text x={0} y={11} fontSize={11} fill="var(--text-muted)">
            {t("trends.max", show(peak, figure))}
          </text>
        ) : null}
        <line x1={0} x2={W} y1={H} y2={H} stroke="var(--line)" strokeWidth={1} />
        {buckets.map((b) => {
          const v = valueOf(b, figure);
          const h = v === 0 ? 0 : Math.max(2, ((H - PAD_TOP) * v) / max);
          return (
            <rect
              key={b.start}
              data-testid="trends-bar"
              x={buckets.indexOf(b) * slot + 1}
              y={H - h}
              width={Math.max(1, slot - 2)}
              height={h}
              rx={Math.min(4, (slot - 2) / 2)}
              fill="var(--accent-line)"
            >
              <title>{`${label(b.start, granularity)}: ${show(b, figure)}`}</title>
            </rect>
          );
        })}
        {buckets.length > 0 ? (
          <>
            <text x={0} y={H + 15} fontSize={11} fill="var(--text-muted)">
              {label(buckets[0]!.start, granularity)}
            </text>
            <text x={W} y={H + 15} fontSize={11} fill="var(--text-muted)" textAnchor="end">
              {label(buckets[buckets.length - 1]!.start, granularity)}
            </text>
          </>
        ) : null}
      </svg>
      <table className="visually-hidden">
        <caption>{name}</caption>
        <thead>
          <tr>
            <th scope="col">{t("trends.col.period")}</th>
            <th scope="col">{t("trends.col.value")}</th>
          </tr>
        </thead>
        <tbody>
          {buckets.map((b) => (
            <tr key={b.start}>
              <th scope="row">{label(b.start, granularity)}</th>
              <td>{show(b, figure)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}

/**
 * The three downloads. **Hidden, not disabled, for an operator without
 * `reports:export`** — the console's rule for a control the server will refuse
 * (`docs/rules/frontend.md`): staff read every session on screen and do not take
 * the table home, and a greyed button would advertise a door that is locked.
 *
 * The document is fetched and handed to the browser as a Blob rather than linked,
 * so a refusal — a range too long, a session that expired — renders as the console's
 * own sentence instead of a raw problem document in a new tab.
 */
function ExportPanel() {
  const allowed = useCan("reports:export");
  const { siteId } = useSite();
  const today = new Date();
  const monthAgo = new Date(today);
  monthAgo.setDate(monthAgo.getDate() - 30);
  const [from, setFrom] = useState(day(monthAgo));
  const [to, setTo] = useState(day(today));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  if (!allowed) return null;

  const download = async (dataset: (typeof DATASETS)[number]) => {
    setError(null);
    setBusy(dataset);
    try {
      // `to` is the last day included, so the exclusive bound is the midnight after it.
      const blob = await api.exportCsv(dataset, { siteId, from: midnight(from), to: midnight(to, 1) });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `foyer-${dataset}-${siteId}-${from}-${to}.csv`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="card" style={{ marginTop: "0.75rem" }}>
      <div className="metric-label">{t("export.title")}</div>
      <p style={{ margin: "0.25rem 0 0.75rem" }}>{t("export.body")}</p>
      <div className="toolbar">
        <label className="field">
          <span className="visually-hidden">{t("export.from")}</span>
          <input type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label className="field">
          <span className="visually-hidden">{t("export.to")}</span>
          <input type="date" value={to} min={from} onChange={(e) => setTo(e.target.value)} />
        </label>
        {DATASETS.map((d) => (
          <button
            key={d}
            type="button"
            className="btn btn-sm"
            aria-disabled={busy !== null}
            onClick={() => (busy === null ? void download(d) : undefined)}
          >
            {t(`export.${d}`)}
          </button>
        ))}
      </div>
      {error ? <ErrorNotice error={error} title={t("export.failed")} /> : null}
    </div>
  );
}
