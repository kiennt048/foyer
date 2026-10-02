import { withCopy } from "../i18n/catalogue.js";
import { useQuery } from "@tanstack/react-query";
import { Suspense, lazy, useEffect, useState } from "react";
import { Link, Route, Routes, useLocation, useNavigate } from "react-router";
import type { Nas } from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { t } from "../i18n/index.js";
import { useSite } from "../site.js";
import { Plus, Search } from "../ui/icons.js";
import { Empty } from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Absent, Mono, Stack, Table, TimeCell } from "../ui/table.js";
import { DRIVER_LABEL } from "../drivers.js";
import { AccountingPill, GardenPill, MessageAuthPill, ReachabilityPill } from "./nas/bits.js";

/**
 * The two surfaces behind this list, split out of the entry chunk.
 *
 * The list is what the alarm strip sends an operator to and it stays eager. Neither
 * of these is: a device's detail page is reached by choosing a row, and the wizard is
 * opened when hardware is added and then not again for months. Together with
 * `FindingsPanel` and `findings.ts`, which only these two use, they are 5 906 B gzip
 * that every operator would otherwise download before the dashboard renders. `App.tsx` holds
 * the whole measurement and the rule it came from.
 *
 * **Moving the list out as well was measured and rejected** — 247 further bytes, for
 * a loading state on the screen an alarm points at.
 */
const NasDetail = lazy(() => withCopy(() => import("./nas/NasDetail.js")).then((m) => ({ default: m.NasDetail })));
const RegisterNasWizard = lazy(() =>
  withCopy(() => import("./nas/RegisterWizard.js")).then((m) => ({ default: m.RegisterNasWizard })),
);

/**
 * The access-point screen: a list, a detail page, and the registration wizard.
 *
 * **The wizard is owned here rather than by the list.** It has to outlive a change
 * of route: `/access-points/new` opens it, and the moment the device is created the
 * URL is replaced with `/access-points` so that history entry is gone before the
 * shared secret — a value no operation returns — is on screen. If the list owned the
 * dialog, that replacement would remount the list and take the secret with it.
 *
 * The route exists for two reasons beyond neatness. It is linkable, so "register a
 * device" is something an operator can be sent. And the accessibility sweep walks a
 * list of URLs, so a dialog with no route of its own is a surface axe never sees —
 * which is the state the session-detail dock is already in.
 */
export function AccessPointsScreen() {
  const location = useLocation();
  const navigate = useNavigate();
  /* `/access-points/new` is a typeable URL, and the button that normally opens the
     wizard is gone for a staff session. Gating the render rather than the state,
     because the permission arrives one request after the first paint: a guard on
     `useState` would open a wizard that never closed itself when the answer arrived. */
  const canRegister = useCan("nas:manage");
  const atNew = location.pathname.endsWith("/new");
  const [registering, setRegistering] = useState(atNew);

  /* Arriving at `/access-points/new` opens the wizard; leaving the URL does not
     close it, because the URL is deliberately replaced mid-flow. */
  useEffect(() => {
    if (atNew) setRegistering(true);
  }, [atNew]);

  const open = () => {
    setRegistering(true);
    if (!atNew) navigate("/access-points/new");
  };

  /*
   * The navigation is unconditional, and an `if (atNew)` guard here would be the
   * whole defect.
   *
   * `atNew` inside this handler is the value from the render that *created* the
   * handler, and `open()` navigates to `/new` without waiting for the router to
   * commit. So with a guard, a dismissal fast enough to beat that commit reads
   * `false`, skips the navigation, sets `registering` to false — and then the `/new`
   * URL arrives, the `[atNew]` effect above fires, and the wizard the operator had
   * just dismissed comes back. Measured with the guard, five runs at each timing:
   * Escape at 0 ms left it open **5 of 5**, at 100 ms and 250 ms **0 of 5**. A race,
   * so it is a flake source for anything that opens a dialog and closes it
   * immediately — which is exactly what a focus-return sweep does.
   *
   * Replacing `/access-points` with itself is a no-op, so a guard would buy nothing
   * and cost that. Kept as a *replace* for the reason `onCreated` gives:
   * `/new` must not survive in the history stack.
   */
  const close = () => {
    setRegistering(false);
    navigate("/access-points", { replace: true });
  };

  return (
    <>
      <Routes>
        <Route index element={<NasList onRegister={open} />} />
        <Route path="new" element={<NasList onRegister={open} />} />
        {/* The fallback is byte-identical to `NasDetail`'s own loading branch, on
            purpose: the chunk fetch and the device fetch are one wait to the operator,
            and identical markup means React reconciles the two into the same node
            rather than flashing between two spellings of "not yet". The heading
            cannot be drawn early here the way `ScreenFallback` draws one — this page
            titles itself with the device's name, which arrives from the fetch.

            Driven with this chunk held for 1 200 ms, Enter on a row: `Loading…` and
            focus on `<body>` for the wait — which is where the row's own link left it
            and is what the data fetch does anyway — then `h1.page-title` once it
            arrives. */}
        <Route
          path=":nasId"
          element={
            <Suspense fallback={<div className="page">{t("common.loading")}</div>}>
              <NasDetail />
            </Suspense>
          }
        />
      </Routes>

      {/*
        `null`, and it is an argument rather than an omission.

        An overlay that has not opened is *nothing on screen* — that is what a dialog
        looks like before you open it, so there is no half-drawn dialog worth
        rendering. A skeleton one would be worse than nothing: `Modal` takes focus
        when it mounts, so the operator would be moved into an empty dialog and then
        moved again when its content arrived — two focus moves for one gesture, on the
        surface where `dialog-focus-return` pins exactly where focus goes.

        It also costs nothing in practice, and that was driven rather than assumed.
        `open()` navigates, and react-router runs that in a transition, so React holds
        the update until the chunk resolves. With this chunk held for 1 200 ms, Enter
        on *Add access point*:

          during   0 dialogs, the list still rendering its 7 rows, focus still on the
                   button that was pressed, URL already at /access-points/new
          opened   1 dialog, focus inside it
          Escape   0 dialogs, focus back on "Add access point", URL back at
                   /access-points

        So the list stays interactive, the dialog opens when it opens, and
        `dialog-focus-return`'s guarantee survives a slow chunk.
      */}
      {registering && canRegister ? (
        <Suspense fallback={null}>
          <RegisterNasWizard
            onClose={close}
            /* Called the moment the device exists, before the secret is rendered.
               `replace` rather than `push`: the point is to *remove* `/new` from the
               history stack, so Back cannot land on a step that no longer holds a
               value it can never show again. */
            onCreated={() => {
              if (atNew) navigate("/access-points", { replace: true });
            }}
          />
        </Suspense>
      ) : null}
    </>
  );
}

/* ── list ─────────────────────────────────────────────────────────────────── */

function NasList({ onRegister }: { onRegister: () => void }) {
  const { siteId } = useSite();
  const navigate = useNavigate();
  const canManage = useCan("nas:manage");
  const [q, setQ] = useState("");

  const query = useQuery({
    queryKey: ["nas", siteId, q],
    queryFn: () => api.listNas({ siteId, q }),
  });

  const rows = query.data?.items ?? [];

  const columns: Column<Nas>[] = [
    {
      key: "name",
      narrow: "keep",
      header: t("nas.col.name"),
      cell: (n) => (
        <Stack
          primary={
            <Link className="row-link" to={`/access-points/${n.id}`} onClick={(e) => e.stopPropagation()}>
              {n.name}
            </Link>
          }
          secondary={DRIVER_LABEL[n.driver]}
        />
      ),
    },
    { key: "state", narrow: "keep", header: t("nas.col.status"), cell: (n) => <ReachabilityPill nas={n} /> },
    {
      key: "security",
      narrow: "drop",
      header: t("nas.col.messageAuthenticator"),
      cell: (n) => <MessageAuthPill nas={n} />,
    },
    {
      key: "garden",
      narrow: "drop",
      header: t("nas.col.garden"),
      cell: (n) => <GardenPill capabilities={n.capabilities} />,
    },
    {
      key: "accounting",
      narrow: "drop",
      header: t("nas.col.accounting"),
      cell: (n) => <AccountingPill nas={n} />,
    },
    {
      key: "address",
      narrow: "drop",
      // Categorical, not quantitative: an IP address is a label, so it stays left.
      header: t("nas.col.address"),
      cell: (n) =>
        n.ipAddress ? <Mono>{n.ipAddress}</Mono> : <Absent hint={t("nas.address.absent")} />,
    },
    {
      key: "lastSeen",
      narrow: "drop",
      header: t("nas.col.lastSeen"),
      cell: (n) => <TimeCell iso={n.lastSeenAt} absent={t("nas.lastSeen.absent")} />,
    },
  ];

  return (
    <div className="page">
      <PageHead
        title={t("nas.title")}
        description={t("nas.description")}
        actions={
          /* `createNas` is `nas:manage`. The list itself is `nas:read` and stays:
             a staff member who can see that an access point is unreachable is a
             staff member who can tell an administrator to go and look at it. */
          canManage ? (
            <button type="button" className="btn btn-primary" onClick={onRegister}>
              <Plus size={14} />
              {t("nas.add")}
            </button>
          ) : null
        }
      />

      <div className="toolbar">
        <label className="search">
          <Search size={14} />
          <input
            type="search"
            placeholder={t("nas.search")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <span className="visually-hidden">{t("nas.searchLabel")}</span>
        </label>
      </div>

      <Table
        columns={columns}
        rows={rows}
        rowKey={(n) => n.id}
        rowLabel={(n) => n.name}
        isLoading={query.isLoading}
        error={query.error}
        refreshing={query.isFetching && !query.isLoading}
        updatedAt={query.dataUpdatedAt}
        filtered={q !== ""}
        onClearFilters={() => setQ("")}
        onRowClick={(n) => navigate(`/access-points/${n.id}`)}
        empty={
          // Action text mirrors the title: "No access points." → "Add access point".
          <Empty
            title={t("nas.empty.title")}
            description={t("nas.empty.description")}
            action={
              canManage ? (
                <button type="button" className="btn" onClick={onRegister}>
                  {t("nas.add")}
                </button>
              ) : null
            }
          />
        }
      />
    </div>
  );
}
