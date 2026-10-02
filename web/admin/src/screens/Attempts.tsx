import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import type { AuthAttempt, AuthBlindSpot, AuthEventKind, AuthSource } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { t } from "../i18n/attempts.js";
import { useSite } from "../site.js";
import { Alert, Info, Search } from "../ui/icons.js";
import { Empty, Notice, Pill } from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Absent, Mono, Stack, Table, TimeCell } from "../ui/table.js";

/**
 * Why a guest is not online.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The screen exists because the question had no answer anywhere in the product.**
 * A guest who cannot get online has no session, and sessions were the only thing the
 * console showed — so the most common support call in this product's life ended at a
 * table that was correct, complete, and silent about the person standing at the
 * counter.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The blind-spot panel is the load-bearing part, not the table.**
 *
 * Several ways of failing to get online leave no row here at all — most importantly
 * a shared-secret mismatch, which FreeRADIUS answers by discarding the request
 * before anything is written. That is the single most common real cause of *nobody
 * can get online*, and on this screen it looks exactly like *nobody tried*.
 *
 * So the panel renders **always**, and is promoted above the table when a search
 * returns nothing. An empty table with no explanation beside it is not a neutral
 * outcome — it actively tells the operator the guest is mistaken, which is the one
 * conclusion this data cannot support. The strings come from the server
 * (`blindSpots`) rather than from this file, so a backend that learns a new way of
 * seeing nothing can say so to a console older than the discovery.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Absence is per row, not per deployment.** `macCoverage` says whether this
 * deployment records client MACs at all; a row can still arrive without one inside
 * a deployment that does. Both are rendered, in different places, because a console
 * that states coverage once at the top of the page and stops is wrong for exactly
 * the row an operator is hunting.
 */

/**
 * Kind to sentence.
 *
 * A `Record` over the union rather than a lookup, so **adding a kind to the contract
 * fails this file to compile** rather than rendering a raw enum value in front of an
 * operator. The server sends kinds and never sentences —
 * this screen adopts that from its first commit instead of inheriting the debt, and
 * this map is the whole of the reason it can.
 */
const KIND_LABEL: Record<AuthEventKind, string> = {
  get "radius-accept"() {
    return t("attempts.kind.radius-accept");
  },
  get "radius-reject"() {
    return t("attempts.kind.radius-reject");
  },
  get "voucher-unknown"() {
    return t("attempts.kind.voucher-unknown");
  },
  get "voucher-expired"() {
    return t("attempts.kind.voucher-expired");
  },
  get "voucher-exhausted"() {
    return t("attempts.kind.voucher-exhausted");
  },
  get "voucher-revoked"() {
    return t("attempts.kind.voucher-revoked");
  },
  get "account-unknown"() {
    return t("attempts.kind.account-unknown");
  },
  get "account-disabled"() {
    return t("attempts.kind.account-disabled");
  },
  get "account-bad-password"() {
    return t("attempts.kind.account-bad-password");
  },
  get "rate-limited"() {
    return t("attempts.kind.rate-limited");
  },
  get "presence-unverified"() {
    return t("attempts.kind.presence-unverified");
  },
  get "authorize-failed"() {
    return t("attempts.kind.authorize-failed");
  },
  get "grant-created"() {
    return t("attempts.kind.grant-created");
  },
};

/**
 * The four kinds where the label alone sends an operator to the wrong place.
 *
 * Partial on purpose: most kinds say everything in three words, and a note beside
 * every row is a note nobody reads. These four each change what the operator should
 * do next — including `radius-reject`, whose note exists to stop them hunting for a
 * reason that was never recorded.
 */
const KIND_WHY: Partial<Record<AuthEventKind, string>> = {
  get "radius-reject"() {
    return t("attempts.why.radius-reject");
  },
  get "presence-unverified"() {
    return t("attempts.why.presence-unverified");
  },
  get "authorize-failed"() {
    return t("attempts.why.authorize-failed");
  },
  get "rate-limited"() {
    return t("attempts.why.rate-limited");
  },
};

const SOURCE_LABEL: Record<AuthSource, string> = {
  get radius() {
    return t("attempts.source.radius");
  },
  get portal() {
    return t("attempts.source.portal");
  },
  get driver() {
    return t("attempts.source.driver");
  },
};

export const BLIND_SPOT_TEXT = (b: AuthBlindSpot): string => {
  switch (b.kind) {
    case "shared-secret-mismatch":
      return t("attempts.blind.shared-secret-mismatch");
    case "unknown-radius-client":
      return t("attempts.blind.unknown-radius-client");
    case "never-associated":
      return t("attempts.blind.never-associated");
    case "no-radius-configured": {
      /*
       * **One kind, two situations, and the count is what tells them apart.**
       *
       * A site whose device list is empty must not be told *"This deployment
       * authorizes through a controller API"* — an architecture asserted only because
       * nothing has been registered yet. `AuthBlindSpot.kind` is a closed enum with no
       * member for *no devices*, so the server sends the device count in `params` and
       * this picks the sentence from it.
       *
       * A count that is not there is not zero. `params` is optional in the contract
       * and a server may send none, so the bare key is the fallback and says only what
       * a missing count still supports. Coercing `undefined` to `0` would put *no
       * access point is registered here* on a site full of them — the same kind of
       * unfounded claim this branch exists to avoid.
       */
      const devices = b.params?.deviceCount;
      if (typeof devices !== "number") return t("attempts.blind.no-radius-configured");
      return devices === 0
        ? t("attempts.blind.no-radius-configured.noDevices")
        : t("attempts.blind.no-radius-configured.noSecret");
    }
    case "before-retention":
      return t("attempts.blind.before-retention", Number(b.params?.retentionDays ?? 0));
  }
};

/**
 * The panel, split out of the screen so it can be rendered on its own.
 *
 * It is what a test has to be able to reach: the sentence an operator reads is
 * decided here, and a test that drove the whole screen would be asserting over a
 * page rather than over this list. `docs/rules/testing.md` records what that
 * costs — an assertion aimed at a whole page cannot tell rendered text from a
 * `title` attribute and stays green either way — an easy mistake on a panel like
 * this one.
 */
export function BlindSpotPanel({ spots }: { spots: readonly AuthBlindSpot[] }) {
  return (
    <section className="blind-spots" aria-labelledby="blind-spots-title">
      <h2 className="section-title" id="blind-spots-title">
        {t("attempts.blind.title")}
      </h2>
      <p className="section-lede">{t("attempts.blind.lede")}</p>
      <ul className="blind-list">
        {spots.map((b) => (
          <li key={b.kind}>{BLIND_SPOT_TEXT(b)}</li>
        ))}
      </ul>
    </section>
  );
}

export function AttemptsScreen() {
  const { siteId } = useSite();
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const outcome = params.get("outcome") ?? "";
  const source = params.get("source") ?? "";

  const query = useQuery({
    queryKey: ["auth-attempts", siteId, q, outcome, source],
    queryFn: () => api.listAuthAttempts({ siteId, q, outcome, source }),
  });

  /*
   * Names, not identifiers.
   *
   * `AuthAttempt.nasId` is an opaque id and the table rendered it raw, so the column
   * read `nas-omada-1` on the one screen an operator opens while a guest is standing
   * in front of them. They know the device as "Omada EAP660 — Kho"; the id appears
   * nowhere else in the console and nowhere on the hardware.
   *
   * Deliberately a *separate* query rather than a field the server denormalises onto
   * every row: the name is mutable and the id is not, so joining here keeps a renamed
   * access point from showing its old name in history. An id with no matching device
   * still renders — see the cell — because that row is the finding.
   */
  const nasQuery = useQuery({
    queryKey: ["nas", siteId, ""],
    queryFn: () => api.listNas({ siteId }),
  });
  const nasName = new Map((nasQuery.data?.items ?? []).map((n) => [n.id, n.name]));

  const rows = query.data?.items ?? [];
  const macCoverage = query.data?.macCoverage;
  const blindSpots = query.data?.blindSpots ?? [];
  const filtered = q !== "" || outcome !== "" || source !== "";

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  };

  const columns: Column<AuthAttempt>[] = [
    {
      key: "outcome",
      narrow: "keep",
      header: t("attempts.col.outcome"),
      cell: (a) =>
        a.outcome === "accepted" ? (
          <Pill level="positive" label={t("attempts.outcome.accepted")} />
        ) : (
          <Pill level="negative" label={t("attempts.outcome.rejected")} />
        ),
    },
    {
      key: "what",
      /*
       * Kept, and it is the column this screen exists for.
       *
       * A phone view of `OUTCOME` + `MAC ADDRESS` alone, measured against the
       * fixtures, is **seven consecutive rows reading `Rejected /
       * a4:3d:0d:da:72:bb`, byte-identical to each other**, plus two reading
       * `Rejected / –` which carry no information at all: 21 rows, 13 distinct
       * strings. *Wrong password*, *Voucher expired* and *The access point refused
       * the authorization* would each be one tap away.
       *
       * The point of this screen is the difference between a source that can never
       * say why and one that knows exactly why, and at 390px it must say why without
       * a tap — on the screen an operator opens *because* a guest cannot
       * get online, on the device they are holding while standing under the access
       * point.
       *
       * The narrow-layout rule is that the column carrying the row's *identity* is
       * kept. On this table the MAC does not identify a row — it repeats, and it is
       * blank on two — so keeping it and dropping this one would apply the rule to
       * the wrong column. Both are kept; `narrow-columns-fit` says the three share
       * the 356px port.
       */
      narrow: "keep",
      header: t("attempts.col.what"),
      cell: (a) => (
        <Stack
          primary={
            <span>
              {KIND_LABEL[a.kind]}
              {KIND_WHY[a.kind] ? (
                <span className="why" title={KIND_WHY[a.kind]}>
                  <Info size={12} />
                  <span className="visually-hidden">{KIND_WHY[a.kind]}</span>
                </span>
              ) : null}
            </span>
          }
          secondary={SOURCE_LABEL[a.source]}
        />
      ),
    },
    {
      key: "mac",
      narrow: "keep",
      header: t("attempts.col.mac"),
      /*
       * The row-level absence. `Absent` carries its own hint rather than rendering a
       * dash, because a dash here reads as "no MAC involved" when what happened is
       * "this row can never be found by the search an operator will use".
       */
      cell: (a) =>
        a.callingStationId ? (
          <Mono>{a.callingStationId}</Mono>
        ) : (
          <Absent hint={t("attempts.noMacWhy")} />
        ),
    },
    {
      key: "identity",
      narrow: "drop",
      header: t("attempts.col.identity"),
      cell: (a) => (a.username ? <Mono>{a.username}</Mono> : <Absent />),
    },
    {
      key: "nas",
      narrow: "drop",
      header: t("attempts.col.nas"),
      /*
       * `nasId: null` is not a missing join to tidy away — it is the symptom of the
       * most common controller-deployment fault, an access point in no client entry.
       * It renders as a named state with the explanation attached.
       */
      cell: (a) =>
        a.nasId ? (
          /* A registered device shows its name; an id that resolves to nothing shows
             the id, because "the device this came from is gone" is information and
             a blank cell is not. */
          <span>{nasName.get(a.nasId) ?? <Mono>{a.nasId}</Mono>}</span>
        ) : (
          <Pill
            level="caution-minor"
            label={t("attempts.noNas")}
            icon={<Alert size={11} />}
            title={t("attempts.noNasWhy")}
          />
        ),
    },
    {
      key: "time",
      narrow: "drop",
      header: t("attempts.col.time"),
      cell: (a) => <TimeCell iso={a.at} />,
    },
  ];

  const blindPanel = <BlindSpotPanel spots={blindSpots} />;

  return (
    <div className="page">
      <PageHead title={t("attempts.title")} description={t("attempts.description")} />

      {/*
        Deployment-wide coverage. Rendered only when it is *not* `recorded`, because
        a notice that is always on screen is a notice nobody reads — and the two
        states it does render for are both cases where every MAC search on this page
        is answering a different question from the one the operator asked.
      */}
      {macCoverage === "not-recorded" ? (
        <div className="stack-sm">
          <Notice level="urgent" title={t("attempts.coverage.notRecorded.title")}>
            {t("attempts.coverage.notRecorded.body")}
          </Notice>
        </div>
      ) : null}
      {macCoverage === "unknown" ? (
        <div className="stack-sm">
          <Notice level="caution-minor" title={t("attempts.coverage.unknown.title")}>
            {t("attempts.coverage.unknown.body")}
          </Notice>
        </div>
      ) : null}

      <div className="toolbar">
        <label className="search">
          <Search size={14} />
          <input
            type="search"
            placeholder={t("attempts.search")}
            defaultValue={q}
            onChange={(e) => setParam("q", e.target.value)}
          />
          <span className="visually-hidden">{t("attempts.searchLabel")}</span>
        </label>

        <div className="chip-group">
          <button
            type="button"
            className="chip"
            aria-pressed={outcome === "rejected"}
            onClick={() => setParam("outcome", outcome === "rejected" ? "" : "rejected")}
          >
            {t("attempts.filter.rejected")}
          </button>
          <button
            type="button"
            className="chip"
            aria-pressed={outcome === "accepted"}
            onClick={() => setParam("outcome", outcome === "accepted" ? "" : "accepted")}
          >
            {t("attempts.filter.accepted")}
          </button>
          {(["radius", "portal", "driver"] as const).map((s) => (
            <button
              key={s}
              type="button"
              className="chip"
              aria-pressed={source === s}
              onClick={() => setParam("source", source === s ? "" : s)}
            >
              {t(
                s === "radius"
                  ? "attempts.filter.source.radius"
                  : s === "portal"
                    ? "attempts.filter.source.portal"
                    : "attempts.filter.source.driver",
              )}
            </button>
          ))}
        </div>
      </div>

      {/*
        Promoted above the table when there is nothing in it. This ordering is the
        screen's whole argument: an empty table followed by nothing tells the
        operator the guest never tried, and that is the one conclusion this data
        cannot support.
      */}
      {/* `!isError` is the load-bearing half. With the query failed there are no
          `blindSpots` either, so this rendered its heading, its lede and an **empty
          list** — which reads as *we looked and found nothing*, on the one panel
          whose whole job is saying what was never looked at. */}
      {!query.isLoading && !query.isError && rows.length === 0 ? blindPanel : null}

      <Table
        columns={columns}
        rows={rows}
        rowKey={(a) => a.id}
        rowLabel={(a) => a.callingStationId ?? a.username ?? a.id}
        isLoading={query.isLoading}
        error={query.error}
        refreshing={query.isFetching && !query.isLoading}
        updatedAt={query.dataUpdatedAt}
        filtered={filtered}
        onClearFilters={() => setParams(new URLSearchParams(), { replace: true })}
        /* Only reached when nothing is filtered — `Table` renders its own
           "no rows match these filters" otherwise, as it does on every other
           screen. The blind-spot panel above carries the part that matters in
           both cases. */
        empty={
          <Empty
            title={t("attempts.empty.title")}
            description={t("attempts.empty.description")}
          />
        }
      />

      {rows.length > 0 ? blindPanel : null}
    </div>
  );
}
