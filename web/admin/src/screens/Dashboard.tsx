import { withCopy } from "../i18n/catalogue.js";
import { useQuery } from "@tanstack/react-query";
import { Suspense, lazy, useCallback, useState } from "react";
import { Link } from "react-router";
import type { Metric } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead, useAlarms } from "../App.js";
import { freshnessNote, intervalText, relativeTime } from "../format.js";
import { alarmCopy, groupAlarms } from "../alarms.js";
import { t } from "../i18n/index.js";
import { useSite } from "../site.js";
import { Check } from "../ui/icons.js";
import { Foot, FootSkeleton, type FootAct, Notice } from "../ui/primitives.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE FIRST-RUN PANEL IS A STATIC IMPORT, AND THAT IS THE MEASURED ANSWER
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `web/tools/check-admin-budget.mjs` gates the *entry set* and deliberately does not
 * gate lazy chunks, and it says so in its own failure message: *"check whether the
 * addition can be a lazy route instead: those are reported here and not gated."*
 * This panel is the perfect candidate — absent on every deployment past its first
 * run — so lazy is the obvious first choice. **Lazy makes the budget worse by 3 115 B.**
 *
 * Measured on this tree, same component both ways:
 *
 *   eager   142 071 B    one entry chunk
 *   lazy    145 186 B    entry split into index 81 263 + primitives 33 542 + site 21 791
 *
 * A `lazy()` edge out of a module that is already in the entry makes Rollup hoist
 * everything the new chunk shares with the entry — `ui/primitives`, `site`, `api` —
 * into separate shared chunks. Those chunks are still fetched before the first
 * screen, so **nothing is deferred**; all that changes is that one gzip stream
 * becomes three, and three small streams compress worse than one large one. Lazy, the
 * budget goes from 1 289 B under to 1 826 B over on a component that is 985 B.
 *
 * So the tool's advice is right about a *route* — `/design`, `/audit`, `/portal` are
 * whole screens that pull in their own dependencies — and wrong about a component
 * that shares its dependencies with the shell. Both are recorded here rather than
 * only the winner, because anyone adding a panel to the dashboard will reach for
 * `lazy()` for exactly this reason.
 */
import { FirstRun } from "./FirstRun.js";

/*
 * **The trends panel is lazy, and the paragraph above is one step short of
 * why a lazy panel split the entry.** Measured on this tree with Vite 8 (rolldown):
 *
 *   panel lazy, importing only shared modules     138 312 B   index + primitives + site
 *   panel lazy, importing `metricFoot` from here  134 578 B   one entry chunk
 *   no panel at all                               134 548 B   one entry chunk
 *
 * The bundler groups modules by the set of chunks that reach them. Every lazy
 * *screen* imports `PageHead` from `App.tsx`, and `App.tsx` reaches the whole
 * shell, so each screen reaches everything the entry does and nothing splits. A
 * panel that imports only `ui/primitives`, `site` and `api` reaches a **subset**,
 * and that subset is carved out of the entry into chunks of its own. Where the
 * `lazy()` is written made no difference (tried in both files); whether the chunk
 * reaches back into the shell did. `Trends.tsx` imports `metricFoot` from here —
 * which it wants anyway, so the chart's freshness line is the cards' sentence —
 * and the entry stays one stream. `FirstRun` is very likely the same case, and stays
 * eager until a build measures it lazy.
 */
const TrendsPanel = lazy(() => withCopy(() => import("./Trends.js")).then((m) => ({ default: m.TrendsPanel })));

/**
 * The dashboard.
 *
 * There is no bandwidth graph, and there will not be one. Interim updates arrive
 * every five minutes at best and on many deployments never — a line chart drawn
 * from that data is a picture of an interval, not of traffic. What is here instead
 * is a small number of figures, each carrying the freshness of the data behind it,
 * and the list of things an operator has to act on.
 */
export function DashboardScreen() {
  const { siteId, site } = useSite();
  /*
   * Whether the first-run panel is showing steps.
   *
   * It is lifted out of the child because it decides something in this file:
   * "Nothing needs action." is a true statement about *alarms* and a false one to
   * print directly beneath a list of things to do. `useCallback` so the child's
   * effect does not re-run on every render of this screen.
   */
  const [setupPending, setSetupPending] = useState(false);
  const onFirstRunPending = useCallback((p: boolean) => setSetupPending(p), []);
  const overview = useQuery({
    queryKey: ["overview", siteId],
    queryFn: () => api.overview(siteId),
    refetchInterval: 20_000,
  });
  const alarms = useAlarms();

  const o = overview.data;
  /*
   * The failure this screen must not hide.
   *
   * With the backend down, `o` is `undefined` — and every expression of the form
   * `o && o.thing > 0 ? bad : good` collapses to the reassuring branch. The card
   * would then render a skeleton over the figure and "All reachable" underneath it,
   * which is the product's own thesis — absence is not zero — failing on the
   * landing screen, in the one scenario where an operator most needs the truth.
   *
   * So the query's error state is carried explicitly into every card, and a card
   * with no data says nothing rather than saying something good.
   */
  const failed = overview.isError;
  /** The `Metric`, not the number. */
  const orphaned = o?.orphanedSessions;

  return (
    <div className="page">
      <PageHead title={t("dashboard.title")} description={site ? `${site.name}` : undefined} />

      <div className="card-grid">
        <MetricCard
          label={t("dashboard.clientsOnline")}
          metric={o?.clientsOnline}
          loading={overview.isLoading}
          failed={failed}
        />
        <MetricCard
          label={t("dashboard.vouchersToday")}
          metric={o?.vouchersRedeemedToday}
          loading={overview.isLoading}
          failed={failed}
        />
        <PlainCard
          label={t("dashboard.accessPoints")}
          value={o ? `${o.nasTotal - o.nasUnhealthy}/${o.nasTotal}` : null}
          // Nullable, and only ever computed from a figure that arrived. "All
          // reachable" is a claim about the estate, not a default.
          //
          // The two branches are different *acts*, and that is the whole point: one is a
          // count of devices an operator has to go and look at, the other says what
          // "12/12" means. They were one muted 11px line, so the alarm read as a
          // footnote — on the console's landing screen.
          foot={
            o
              ? o.nasTotal === 0
                ? /* The branch that was missing, and it read `All reachable` over
                     `0/0` on the landing screen of every new deployment. An estate of
                     nothing is not a healthy estate — the same absence-is-not-zero
                     rule the `failed` flag above carries, one condition along.
                     `definition` rather than `alarm`: it says what the figure means,
                     and the first-run panel below says what to do about it. */
                  { text: t("dashboard.nasNone"), act: "definition" as const }
                : o.nasUnhealthy > 0
                  ? { text: t("dashboard.nasUnhealthy", o.nasUnhealthy), act: "alarm" as const }
                  : { text: t("dashboard.nasHealthy"), act: "definition" as const }
              : null
          }
          loading={overview.isLoading}
          failed={failed}
        />
        {/*
         * **This was a `PlainCard` and it could not date itself.**
         *
         * `orphanedSessions` is a `Metric`, so the one figure whose entire meaning is
         * *these have not been heard from* can say when the oldest of them last
         * was — and `docs/rules/frontend.md` requires any
         * figure derived from accounting to travel with its freshness, which this one
         * did not.
         *
         * It renders through `orphanFoot` rather than `metricFoot`, and that is the
         * decision rather than an inconsistency. The freshness object is derived by
         * one rule on both servers; what differs is what it *claims*. See `orphanFoot`.
         *
         * Still optional in the contract, so the "did not report it" branch stays: a
         * `MetricCard` handed `undefined` with `loading` false would hold a skeleton
         * for ever, which is the permanent-skeleton failure the `failed` flag above
         * exists to prevent, one field along.
         */}
        {o && orphaned == null ? (
          <FailedCard label={t("dashboard.orphaned")} note={t("dashboard.orphanedUnknown")} />
        ) : (
          <MetricCard
            label={t("dashboard.orphaned")}
            metric={orphaned}
            describe={orphanFoot}
            loading={overview.isLoading}
            failed={failed}
          />
        )}
      </div>

      <FirstRun onPending={onFirstRunPending} />

      {/*
        * The whole section goes, heading included.
        *
        * "Nothing needs action." is a claim about *alarms* — device faults — and it is
        * true on a site that has no devices to have faults. But printed directly
        * beneath the first-run panel it reads as a flat contradiction of the list of
        * things to do immediately above it, which is what an operator meets on the
        * landing screen of a deployment that has just been installed.
        *
        * Withholding only the card would leave `NEEDS ACTION` standing over nothing — a
        * heading with no content, which is the empty frame `ProbeWarnings` avoids and
        * the reason `Controllers` returns `null` for a whole cell rather than blanking
        * it. On the Lab site that is exactly what it would render.
        *
        * Only the *quiet* branch is suppressed. A real alarm and an unanswered alarm
        * request both still render, first-run or not: those are facts the panel above
        * does not carry.
        */}
      {setupPending && !alarms.isLoading && !alarms.isError && (alarms.data?.length ?? 0) === 0 ? null : (
      <section className="section">
        <h2 className="section-title">{t("dashboard.needsAction")}</h2>
        {alarms.isLoading ? (
          <div className="card">
            <span className="skeleton" style={{ width: "60%" }} />
          </div>
        ) : alarms.isError ? (
          /*
           * The fourth branch, and the reason this is not a ternary pair.
           *
           * `(alarms.data?.length ?? 0) === 0` is true both when the server said
           * "nothing to do" and when the server said nothing at all — so a console
           * that could not reach its own backend drew a green tick reading "Nothing
           * needs action." An empty list and an unanswered request are opposite
           * facts and must never share a rendering.
           */
          <Notice level="urgent" title={t("dashboard.alarmsFailed.title")}>
            {t("dashboard.alarmsFailed.body")}
          </Notice>
        ) : (alarms.data?.length ?? 0) === 0 ? (
          <div className="card" style={{ display: "flex", gap: "0.625rem", alignItems: "center" }}>
            <Check size={16} />
            <span>{t("dashboard.nothingToDo")}</span>
          </div>
        ) : (
          <div className="notice-stack">
            {groupAlarms(alarms.data!).map((group) => {
              const first = group.alarms[0]!;
              const copy = alarmCopy(first);
              /* One device is not a group, and it renders exactly as it always
                 has: the same card, the same body, the same link. Grouping is
                 for the case where the repetition is the problem, and a fix that
                 changed the common case to fix the rare one would be a trade. */
              const many = group.alarms.length > 1;
              /* Named inside the card, each still a link to its own page. The
                 ids are the point of the per-device alarm and grouping must not
                 cost them, so nothing an operator needs to act on is merged
                 away. Capped because a venue with forty hand-registered devices
                 has one fault, not forty sentences — and the overflow says how
                 many it swallowed rather than hiding it. */
              const NAMED = 5;
              const named = group.alarms.slice(0, NAMED);
              const rest = group.alarms.length - named.length;
              return (
              <Notice
                key={group.key}
                level={group.level}
                title={copy.title}
                action={
                  /* `/controllers` has no per-id route, so a controller subject links
                     to the list. Better than no link at all on the one alarm whose
                     subject is not an access point: the row it names is on that
                     screen and nothing else on the dashboard points there. */
                  !many && first.subject?.kind === "nas" ? (
                    <Link className="btn btn-sm" to={`/access-points/${first.subject.id}`}>
                      {t("common.view")}
                    </Link>
                  ) : !many && first.subject?.kind === "controller" ? (
                    <Link className="btn btn-sm" to="/controllers">
                      {t("common.view")}
                    </Link>
                  ) : undefined
                }
              >
                {copy.body}
                {!many && first.subject?.name ? (
                  <>
                    {" "}
                    <strong>{first.subject.name}</strong>
                  </>
                ) : null}
                {many ? (
                  <>
                    {" "}
                    <strong>{t("dashboard.alarmGroupCount", group.alarms.length)}</strong>
                    {` ${t("dashboard.alarmGroupOf")} `}
                    {named.map((a, i) => (
                      <span key={a.id}>
                        {i > 0 ? ", " : ""}
                        {a.subject?.kind === "nas" ? (
                          <Link to={`/access-points/${a.subject.id}`}>{a.subject.name}</Link>
                        ) : (
                          a.subject?.name
                        )}
                      </span>
                    ))}
                    {rest > 0 ? ` (${t("dashboard.alarmMore", NAMED, group.alarms.length)})` : ""}
                  </>
                ) : null}
              </Notice>
              );
            })}
          </div>
        )}
      </section>
      )}

      {/* Below the alarms: history is read after what needs doing now. `null` while
          the chunk loads — the panel's own skeleton takes over once it has code. */}
      <Suspense fallback={null}>
        <TrendsPanel />
      </Suspense>
    </div>
  );
}

/**
 * A figure that cannot be shown without its freshness.
 *
 * The freshness comes from the *oldest* sample behind the number, not the newest.
 * Reporting the newest would flatter the figure into looking live, which is exactly
 * the lie the rest of this design is built to avoid.
 */
function MetricCard({
  label,
  metric,
  loading,
  failed,
  describe = metricFoot,
}: {
  label: string;
  metric: Metric | undefined;
  loading: boolean;
  /** The query errored. Without this a dead backend renders as a permanent skeleton. */
  failed?: boolean;
  /**
   * How this figure's freshness reads as a sentence, defaulting to the general
   * rule.
   *
   * It is a prop because **two figures can share a derivation and not share a
   * claim** — see `orphanFoot`. The alternative was to branch inside `metricFoot`
   * on which card is calling, which is the same fact in two places and drifts the
   * day a third figure arrives.
   */
  describe?: (m: Metric) => { text: string; act: FootAct; title?: string };
}) {
  if (failed) return <FailedCard label={label} />;
  const foot = metric ? describe(metric) : null;
  return (
    <div className="card">
      <div className="metric-label">{label}</div>
      {/* `1lh`, not `1em`, for the reason `.metric-foot` carries a `1lh` floor: the
          value's line box is 33px and `1em` is 30px, so closing the foot's 16px of
          the jump left 3px of it on all four cards — a smaller version of the same
          defect, one element up. Measured at 1440 on a 279px card: 107.89 → 110.89,
          against 110.89 arrived. */}
      {loading || !metric ? (
        <div className="metric-value">
          <span className="skeleton" style={{ width: "3ch", height: "1lh" }} />
        </div>
      ) : (
        <div className="metric-value">{metric.value ?? "–"}</div>
      )}
      {/* The skeleton branch is not an omission: the foot was
          simply absent while loading, so every card grew 84px → 111px on arrival
          and moved whatever was under it. */}
      {foot && metric ? (
        <Foot act={foot.act} title={foot.title ?? freshnessNote(metric.freshness)}>
          {foot.text}
        </Foot>
      ) : (
        <FootSkeleton />
      )}
    </div>
  );
}

/**
 * A card whose figure did not arrive.
 *
 * It says so rather than holding a skeleton forever, and it never carries the
 * caption that belongs to a figure — there is no figure.
 */
function FailedCard({ label, note }: { label: string; note?: string }) {
  return (
    <div className="card">
      <div className="metric-label">{label}</div>
      <div className="metric-value">
        <span className="empty-value">–</span>
      </div>
      {/* `definition`, not `provenance`: there is no sample to be old. The line
          says what the empty figure means, which is the one thing left to say.
          `note` is for the other way a figure goes missing — the query answered and
          this field was not in it — because "the backend did not answer" would then
          be false about a response that arrived. */}
      <Foot act="definition">{note ?? t("metric.unavailable")}</Foot>
    </div>
  );
}

/**
 * The freshness line under a metric, with the act it is performing.
 *
 * The act is derived here rather than passed in because it is a property of the
 * freshness, not of the card: a figure sampled 25 minutes ago and a live one are
 * different *kinds of claim*, and they were rendered in one 11px muted style that
 * made them equally trustworthy.
 *
 * `absent` is `definition` and not `provenance`. "No data" is not a statement about
 * how old the sample is — there is no sample — and a clock beside it would date a
 * measurement nobody took.
 */
export function metricFoot(metric: Metric): { text: string; act: FootAct } {
  if (metric.freshness.state === "live") return { text: t("metric.live"), act: "live" };
  if (metric.freshness.state === "absent") return { text: t("metric.absent"), act: "definition" };
  const sampled = relativeTime(metric.freshness.sampledAt) ?? t("common.unknown");
  const every = intervalText(metric.freshness);
  return {
    text: every ? t("metric.sampledEvery", sampled, every) : t("metric.sampled", sampled),
    act: "provenance",
  };
}

/**
 * The orphan count's line — and it is deliberately not `metricFoot`'s.
 *
 * The two accounting figures on this screen are derived by
 * one rule: the sample is the oldest of exactly the rows the value counts. What is
 * not shared is what an old sample *means*, and rendering them identically is how
 * the console would say something false about one of them.
 *
 * On `clientsOnline`, `stale` means *this number may be wrong* — the instrument
 * stopped reporting and the count is behind the network. On `orphanedSessions` the
 * count is exact at the instant it is computed; what is old is its **subject**.
 * `metricFoot` would print `metric.sampled` here, and on a dashboard sitting beside
 * the online card that reads as "this count is forty days behind", which is not
 * true and is not the sentence worth having. The one worth having is *the oldest of
 * these was last heard from forty days ago*: it is what separates an access point
 * that rebooted ten minutes ago from rows nothing has reaped since July, and those
 * are the same integer and different faults.
 *
 * `title` is overridden for the same reason. `freshnessNote` would say *"These
 * figures are old"*, which is `clientsOnline`'s claim wearing this figure's badge.
 *
 * **The state is not spent on.** The contract states out loud that this figure's
 * `state` is `stale` exactly when its value is non-zero — true of every counted row,
 * and redundant with the number already on the card. A badge that never varies
 * against a figure beside it is one an operator learns to skip, so the line carries
 * the timestamp instead, which is the part that varies and the part that decides
 * what to do.
 */
export function orphanFoot(metric: Metric): { text: string; act: FootAct; title?: string } {
  const since = relativeTime(metric.freshness.sampledAt);
  // No orphans: the freshness is `live`, dated now, and there is no age to report.
  // "No stop record" says what the figure counts, which is all there is to say
  // about a zero — and `definition` rather than `live`, because a heartbeat here
  // would claim updates are arriving for a set with no rows in it.
  if (metric.value === 0 || since === null) {
    return { text: t("dashboard.orphanedFoot"), act: "definition", title: t("dashboard.orphanedNote") };
  }
  return {
    text: t("dashboard.orphanedOldest", since),
    act: "provenance",
    title: t("dashboard.orphanedNote"),
  };
}

/**
 * A figure that is not accounting-derived, so it carries a caption rather than a
 * freshness line.
 *
 * **`foot` is nullable, and that is the point of the signature.** A required string
 * would make the caller hand it *something* even when the data behind it had not
 * arrived — and the something a caller reaches for in that
 * position is the healthy branch of the ternary it already wrote. A card with no
 * data still renders no caption — only the box one will occupy.
 *
 * **The caption and its act are one value, not two props.** `Foot` requires an
 * `act`, and the obvious wiring — `foot: string` beside `footAct: FootAct` — lets a
 * caller change the sentence and leave the act behind, which is how "12 devices need
 * attention" ends up drawn as a footnote. Nullable-together also keeps a second
 * property: there is no way to spell a caption for data that has not arrived.
 */
function PlainCard({
  label,
  value,
  foot,
  loading,
  failed,
}: {
  label: string;
  value: string | null;
  foot: { text: string; act: FootAct } | null;
  loading: boolean;
  failed?: boolean;
}) {
  if (failed) return <FailedCard label={label} />;
  return (
    <div className="card">
      <div className="metric-label">{label}</div>
      <div className="metric-value">
        {loading || value === null ? (
          <span className="skeleton" style={{ width: "3ch", height: "1lh" }} />
        ) : (
          value
        )}
      </div>
      {foot ? <Foot act={foot.act}>{foot.text}</Foot> : <FootSkeleton />}
    </div>
  );
}
