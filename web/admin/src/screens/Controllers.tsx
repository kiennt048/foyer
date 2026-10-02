import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import type {
  Controller,
  ControllerAuthMode,
  ControllerProbe,
  ControllerVendor,
  ProbeWarning,
  TlsMode,
} from "@foyer/contract";
import { api } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { t } from "../i18n/controllers.js";
import { fieldMessages } from "../problems.js";
import { useSite } from "../site.js";
import { Alert, Check, Plus, Refresh, Slash } from "../ui/icons.js";
import { ConfirmDialog, Empty, ErrorNotice, Modal, Notice, Pill, type Toast, Toasts } from "../ui/primitives.js";
import type { Column } from "../ui/table.js";
import { Absent, Mono, Stack, Table, TimeCell } from "../ui/table.js";
import { ProbeWarnings } from "./nas/FindingsPanel.js";
import { useRefocusAfterRemoval } from "./refocus.js";

/**
 * Controllers — the object an operator actually possesses.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **This screen is the answer to one operator goal**: *connect a UniFi controller
 * from scratch*. Asking for an access point's name, driver kind and IP address
 * cannot do it, because none of those can reach a controller — Ubiquiti's own
 * external-portal API is entirely controller- and site-scoped, and the access point
 * appears in it only as a MAC in a query string.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Three things here exist because getting them wrong is invisible.**
 *
 * *The credential field changes with the vendor, and says which account it wants.*
 * Omada's is a **hotspot operator** created in the Hotspot Manager, and TP-Link is
 * explicit that it is not the controller login — an operator who types the obvious
 * thing gets a refusal that says nothing. UniFi's API key needs Network Application
 * 9.1.105 or newer, and on an older console the screen that generates it does not
 * exist, so the honest advice is "use a local administrator instead" rather than
 * "the key is wrong".
 *
 * *There is no "ignore certificates" option.* Controllers ship self-signed
 * certificates and every shipped integration answers that by turning verification
 * off — PacketFence sets `verify_hostname => 0`, TP-Link's own sample sets
 * `CURLOPT_SSL_VERIFYPEER = FALSE`. Pinning is the same single click for the
 * operator and is not silently equivalent to no TLS at all.
 *
 * *Probe before discover, and both are separate buttons.* A probe that fails tells
 * an operator which of four things is wrong; discovery that fails tells them
 * nothing. The probe's `presence-check` row is the one that decides whether a driver
 * may be used at all — `docs/rules/security.md` §4 forbids a grant for a device
 * nothing confirmed was present, so a controller that cannot answer that question
 * cannot let guests on, whatever else passes.
 */

const VENDOR_LABEL: Record<ControllerVendor, string> = {
  get unifi() {
    return t("controllers.vendor.unifi");
  },
  get omada() {
    return t("controllers.vendor.omada");
  },
  get ruckus() {
    return t("controllers.vendor.ruckus");
  },
};

/** Which credential each vendor's integration takes. Not a free choice. */
const VENDOR_AUTH: Record<ControllerVendor, ControllerAuthMode[]> = {
  unifi: ["api-key", "local-admin"],
  omada: ["hotspot-operator"],
  ruckus: ["local-admin"],
};

const AUTH_LABEL: Record<ControllerAuthMode, string> = {
  get "api-key"() {
    return t("controllers.create.authMode.api-key");
  },
  get "local-admin"() {
    return t("controllers.create.authMode.local-admin");
  },
  get "hotspot-operator"() {
    return t("controllers.create.authMode.hotspot-operator");
  },
};

const AUTH_HINT: Record<ControllerAuthMode, string> = {
  get "api-key"() {
    return t("controllers.create.apiKeyHint");
  },
  get "local-admin"() {
    return t("controllers.create.localAdminHint");
  },
  get "hotspot-operator"() {
    return t("controllers.create.hotspotOperatorHint");
  },
};

const CHECK_LABEL: Record<string, string> = {
  get reachable() {
    return t("controllers.probe.check.reachable");
  },
  get tls() {
    return t("controllers.probe.check.tls");
  },
  get credential() {
    return t("controllers.probe.check.credential");
  },
  get version() {
    return t("controllers.probe.check.version");
  },
  get sites() {
    return t("controllers.probe.check.sites");
  },
  get "guest-authorization"() {
    return t("controllers.probe.check.guest-authorization");
  },
  get "presence-check"() {
    return t("controllers.probe.check.presence-check");
  },
};

/**
 * Whether this controller is attached to a venue — a **state**, not a count.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why this exists.** A controller that has been probed and adopted by nobody is
 * still listed — `internal/apistore`'s `controllerServesSite` lists a controller
 * bound here or bound nowhere — and the row carries a fact the screen has to say out
 * loud: *this controller is not attached to a venue yet*.
 *
 * That is a thing for the operator to do. Said as `0 of 1 adopted` — a count, in a
 * column that drops on a phone, beside a green *Reachable* pill — every signal on the
 * row would read fine and the one step that makes the controller useful would be
 * named nowhere.
 *
 * Three values because the two absences are different afternoons, which is the same
 * three-valued rule the Version column already follows: **nothing has read its
 * sites** is a probe that has not happened, and **read, and none adopted** is a
 * discovery that has not happened. Collapsing them sends an operator to press the
 * wrong button.
 *
 * `attached` is derived from *any* binding rather than from a binding to the site
 * the console is showing, and that is safe because of the filter above it: this list
 * only ever contains controllers bound here or bound nowhere. Asking about "this
 * site" here would be a second copy of the scope rule, one layer further out.
 */
export type ControllerAttachment = "sites-not-read" | "unattached" | "attached";

export function controllerAttachment(c: Pick<Controller, "vendorSites">): ControllerAttachment {
  if (c.vendorSites.length === 0) return "sites-not-read";
  return c.vendorSites.some((v) => v.boundSiteId) ? "attached" : "unattached";
}

/**
 * The row's first action, and its label is the whole of the fix an operator sees.
 *
 * *Probe* is the next step for a controller nothing has read yet, and it is what the
 * Status pill already tells them to do. For one that answered, whose sites are known
 * and whose sites are adopted by nobody, the next step is **attach**, and the button
 * says so rather than making the operator guess that a dialog called *Probe* is
 * where adoption lives.
 *
 * It is the label that changes and not the colour. The state is drawn in the Sites
 * column too, and that column is `narrow: "drop"` — so on a phone this label is the
 * only place the state appears, and `docs/rules/frontend.md` spends colour on
 * status and one primary action per page rather than one per row.
 *
 * `reachability === "ok"` is a precondition and not decoration: `discoverFromController`
 * is refused for anything else, so a button promising to attach a controller whose
 * credential has expired would open a dialog with the control disabled. In that state
 * the Status pill carries the more urgent instruction and this stays *Probe*.
 */
export function controllerAction(c: Pick<Controller, "vendorSites" | "reachability">): "probe" | "attach" {
  return controllerAttachment(c) === "unattached" && c.reachability === "ok" ? "attach" : "probe";
}

/** What that control says, and its hover text. Exported so a test reads the sentence
    an operator reads rather than the branch that chose it. */
export const ACTION_LABEL: Record<ReturnType<typeof controllerAction>, string> = {
  get probe() {
    return t("controllers.probe");
  },
  get attach() {
    return t("controllers.attach");
  },
};

const ACTION_TITLE: Record<ReturnType<typeof controllerAction>, string | undefined> = {
  probe: undefined,
  get attach() {
    return t("controllers.attachWhy");
  },
};

/**
 * The Sites cell, and it is a component rather than an inline expression so that a
 * test can read what an operator reads.
 *
 * Three-valued, for the reason `controllerAttachment` gives. `Absent` where nothing
 * has read the sites — the console has one way of saying a value is not there and
 * this column had a second. A **pill** where they have been read and adopted by
 * nobody, because that is not a missing value at all: it is a state with a next step
 * in it, and it was drawn as the count `0 of 1 adopted` beside a green *Reachable*,
 * which is the half of the problem the server-side listing does not fix.
 */
export function SitesCell({ c }: { c: Pick<Controller, "vendorSites"> }) {
  switch (controllerAttachment(c)) {
    case "sites-not-read":
      return <Absent hint={t("controllers.sites.none")} />;
    case "unattached":
      return (
        <Pill
          level="caution-minor"
          label={t("controllers.sites.unattached")}
          title={t("controllers.sites.unattachedWhy")}
        />
      );
    default:
      return (
        <>
          {t(
            "controllers.sites.bound",
            c.vendorSites.filter((v) => v.boundSiteId).length,
            c.vendorSites.length,
          )}
        </>
      );
  }
}

function StatusPill({ c }: { c: Controller }) {
  if (c.reachability === "ok") {
    /*
     * **The verdict belongs to the verdict column.**
     *
     * A controller that answers on a version this driver is not tested against was
     * `Reachable`, in green, beside an urgent pill in the *Version* column whose
     * label was the number `8.6.9`. Without colour that pill says nothing is wrong —
     * which is the one thing `primitives.tsx` forbids — and the Version column is
     * `narrow: "drop"`, so on a phone the whole verdict disappeared and left the
     * green behind.
     *
     * On a controller-API vendor the version *is* the path onto the network — the
     * alarm this repeats says so — so this is not a footnote to reachability, it is
     * the status. The number goes back to the tabular monospace column where an
     * operator can compare it row to row.
     *
     * The target version is deliberately not named here: `Controller` does not carry
     * `minimumVersion` — only `ControllerProbe` does — and inventing a floor to fill
     * a sentence is how a wrong one gets written. The probe dialog states it.
     */
    return c.versionSupported === false ? (
      <Pill
        level="urgent"
        label={t("controllers.status.versionUnsupported")}
        title={t("alarm.controller-version-unsupported.title")}
      />
    ) : (
      <Pill level="positive" label={t("controllers.status.ok")} />
    );
  }
  if (c.reachability === "credentials-expired") {
    return (
      <Pill
        level="urgent"
        label={t("controllers.status.credentials-expired")}
        icon={<Alert size={11} />}
      />
    );
  }
  if (c.reachability === "unreachable") {
    return <Pill level="urgent" label={t("controllers.status.unreachable")} icon={<Alert size={11} />} />;
  }
  /* `unknown` is not a failure and must not read as one. Nothing has looked yet, and
     an operator who sees red for a controller they registered ten seconds ago starts
     debugging a network that is fine. */
  return (
    <Pill
      level="neutral"
      label={t("controllers.status.unknown")}
      title={t("controllers.status.unknownWhy")}
    />
  );
}

export function ControllersScreen() {
  const { siteId, site } = useSite();
  const qc = useQueryClient();
  /* Every write on this screen — create, probe, discover, delete — declares
     `nas:manage` in the contract. Reading the list is `nas:read`, which staff have. */
  const canManage = useCan("nas:manage");
  const [adding, setAdding] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Controller | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);

  const query = useQuery({
    queryKey: ["controllers", siteId],
    queryFn: () => api.listControllers(siteId),
  });
  const nasQuery = useQuery({ queryKey: ["nas", siteId, ""], queryFn: () => api.listNas({ siteId }) });

  const del = useMutation({
    mutationFn: (id: string) => api.deleteController(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["controllers"] });
      setConfirmDelete(null);
    },
  });

  const rows = query.data ?? [];
  /* See `refocus.ts`. */
  const armRefocus = useRefocusAfterRemoval(rows);
  /*
   * `null` when the access points could not be read, and never `0`.
   *
   * `(nasQuery.data?.items ?? [])` counts an empty array on a failed `GET /nas` and
   * puts **0** in this column for every controller on the screen — a figure, stated
   * with the same confidence as a true one, saying the thing an operator is most
   * likely to act on. A number nobody could measure has to read as *not known*.
   */
  const adoptedCount = (id: string): number | null =>
    nasQuery.data ? nasQuery.data.items.filter((n) => n.controllerId === id).length : null;

  const allColumns: Column<Controller>[] = [
    {
      key: "name",
      narrow: "keep",
      header: t("controllers.col.name"),
      cell: (c) => (
        <Stack
          primary={<span className="row-link">{c.name}</span>}
          secondary={VENDOR_LABEL[c.vendor]}
        />
      ),
    },
    { key: "status", narrow: "keep", header: t("controllers.col.status"), cell: (c) => <StatusPill c={c} /> },
    {
      key: "version",
      narrow: "drop",
      header: t("controllers.col.version"),
      /*
       * Still three-valued, and still rendered as three — the third value just moved.
       * `null` is *not established*, which is a different fact from *established and
       * too old*, and collapsing them sends an operator to upgrade software that may
       * be perfectly fine. So: `–` for never established, the number for a version
       * that is fine, and the number **plus an urgent status beside it** for one that
       * is not. See `StatusPill`.
       *
       * `Absent`, not a muted sentence. The console has one way of saying a value is
       * not there and this column had a second — and the row already
       * says *why* once, in the Status column, whose own tooltip is that probing is
       * what establishes the version and the sites.
       */
      cell: (c) =>
        c.version == null ? (
          <Absent hint={t("controllers.version.unknown")} />
        ) : (
          <Mono>{c.version}</Mono>
        ),
    },
    {
      key: "sites",
      narrow: "drop",
      header: t("controllers.col.sites"),
      cell: (c) => <SitesCell c={c} />,
    },
    {
      key: "aps",
      narrow: "drop",
      header: t("controllers.col.accessPoints"),
      cell: (c) => {
        const n = adoptedCount(c.id);
        return n === null ? <Absent /> : <span>{n}</span>;
      },
    },
    { key: "address", narrow: "drop", header: t("controllers.col.address"), cell: (c) => <Mono>{c.baseUrl}</Mono> },
    {
      key: "lastContact",
      narrow: "drop",
      header: t("controllers.col.lastContact"),
      cell: (c) => <TimeCell iso={c.lastContactAt} />,
    },
    {
      key: "actions",
      narrow: "keep",
      header: "",
      align: "end",
      width: "1%",
      /* `probeController` and `deleteController` both require `nas:manage`, so both
         disappear for a staff member — the row itself does not, because `nas:read`
         is theirs and being able to see that a controller's key has expired is how
         they tell an administrator to renew it. */
      cell: (c) => (
        <span className="row-actions">
          {/* One control, two labels. See `controllerAction`: for a controller whose
              sites are known and adopted by nobody, the next step is attaching it to
              this venue, and the dialog that does it is the same one. This column is
              `narrow: "keep"`, so on a phone the label is the only place the state
              appears at all. */}
          <button
            type="button"
            className="btn btn-sm"
            title={ACTION_TITLE[controllerAction(c)]}
            onClick={(e) => {
              e.stopPropagation();
              setOpen(c.id);
            }}
          >
            {ACTION_LABEL[controllerAction(c)]}
          </button>
          <button
            type="button"
            className="btn btn-sm btn-ghost btn-danger"
            onClick={(e) => {
              e.stopPropagation();
              armRefocus(e.currentTarget);
              setConfirmDelete(c);
            }}
          >
            {t("common.delete")}
          </button>
        </span>
      ),
    },
  ];

  /*
   * The actions column leaves with its controls.
   *
   * Blanking each cell instead would keep an empty column and an empty header on
   * every row — dead space that makes a staff console look like a broken
   * administrator console rather than a different one. A control that disappears
   * takes its column with it.
   */
  const columns = allColumns.filter((c) => canManage || c.key !== "actions");

  /*
   * The actions column leaves with its controls.
   *
   * Returning `null` per cell would keep an empty column and an empty header on
   * every row — dead space that says a staff console is a broken administrator
   * console rather than a different one. A control that disappears takes its column
   * with it.
   */

  const openController = rows.find((c) => c.id === open) ?? null;

  return (
    <div className="page">
      <PageHead
        title={t("controllers.title")}
        description={t("controllers.description")}
        actions={
          canManage ? (
            <button type="button" className="btn btn-primary" onClick={() => setAdding(true)}>
              <Plus size={14} />
              {t("controllers.add")}
            </button>
          ) : null
        }
      />

      <Table
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        rowLabel={(c) => c.name}
        isLoading={query.isLoading}
        error={query.error}
        refreshing={query.isFetching && !query.isLoading}
        updatedAt={query.dataUpdatedAt}
        empty={
          <Empty
            title={t("controllers.empty.title")}
            description={t("controllers.empty.description")}
            action={
              canManage ? (
                <button type="button" className="btn btn-primary" onClick={() => setAdding(true)}>
                  {t("controllers.add")}
                </button>
              ) : null
            }
          />
        }
      />

      {adding ? (
        <AddControllerDialog
          onClose={() => setAdding(false)}
          onCreated={(c) => {
            void qc.invalidateQueries({ queryKey: ["controllers"] });
            setAdding(false);
            /* Straight into the probe. A controller that has been registered and not
               probed is the state every operator leaves one in, and it is the state
               where nothing works and nothing says why. */
            setOpen(c.id);
          }}
        />
      ) : null}

      {openController ? (
        <ProbeDialog
          controller={openController}
          siteId={siteId}
          siteName={site?.name ?? ""}
          onClose={() => setOpen(null)}
          onChanged={(message) => {
            void qc.invalidateQueries({ queryKey: ["controllers"] });
            void qc.invalidateQueries({ queryKey: ["nas"] });
            if (message) setToasts((c) => [...c, { id: Date.now(), message }]);
          }}
        />
      ) : null}

      {/*
        Mounted always, `open` toggled — not `{confirmDelete ? … : null}`.

        The primitive is innocent and the mounting is the whole bug. `ConfirmDialog`
        calls `el.close()` when `open` goes false, and the platform's own return of
        focus to whatever opened the dialog then works. A conditionally mounted one
        never lets `open` go false while it is still in the document: the element is
        removed *while still open*, `close()` never runs, and the return never
        happens. Measured with Escape from the delete confirmation: an always-mounted
        dialog returns focus to its Delete button, a conditionally mounted one leaves
        it on `<body>`. Red and green against one primitive is what says the call
        site is the defect.
      */}
      <ConfirmDialog
        open={confirmDelete !== null}
        tier="confirm"
        title={t("controllers.delete.title")}
        verb={t("controllers.delete.verb")}
        consequence={t("controllers.delete.consequence", confirmDelete?.name ?? "")}
        recovery={t("controllers.delete.recovery")}
        busy={del.isPending}
        error={del.error}
        onCancel={() => setConfirmDelete(null)}
        onConfirm={() => confirmDelete && del.mutate(confirmDelete.id)}
      />

      <Toasts toasts={toasts} onDismiss={(id) => setToasts((c) => c.filter((x) => x.id !== id))} />
    </div>
  );
}

/* ── register ─────────────────────────────────────────────────────────────── */

function AddControllerDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (c: Controller) => void;
}) {
  const titleId = useId();
  const [vendor, setVendor] = useState<ControllerVendor>("unifi");
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [controllerId, setControllerId] = useState("");
  const [authMode, setAuthMode] = useState<ControllerAuthMode>("api-key");
  const [apiKey, setApiKey] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [tlsMode, setTlsMode] = useState<TlsMode>("trust-on-first-use");

  const create = useMutation({
    mutationFn: () =>
      api.createController({
        name,
        vendor,
        baseUrl,
        ...(vendor === "omada" ? { controllerId: extractControllerId(controllerId) } : {}),
        authMode,
        tlsMode,
        ...(authMode === "api-key" ? { apiKey } : { username, password }),
      }),
    onSuccess: onCreated,
  });

  const fieldErrors = fieldMessages(create.error);
  const authModes = VENDOR_AUTH[vendor];

  const chooseVendor = (v: ControllerVendor) => {
    setVendor(v);
    /* The credential a vendor takes is not the operator's choice, so switching
       vendor moves it rather than leaving a UniFi API key selected on an Omada. */
    setAuthMode(VENDOR_AUTH[v][0]!);
  };

  return (
    <Modal labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {t("controllers.create.title")}
          </h2>
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (create.isPending) return;
            create.mutate();
          }}
        >
          <div className="dialog-body" tabIndex={0} role="group" aria-label={t("controllers.create.title")}>
            {create.error ? (
              <ErrorNotice error={create.error} title={t("controllers.create.failed.title")} />
            ) : null}

            <div className="form-grid">
              <div className="field">
                <label htmlFor="ctl-vendor">{t("controllers.create.vendor")}</label>
                <select
                  id="ctl-vendor"
                  value={vendor}
                  onChange={(e) => chooseVendor(e.target.value as ControllerVendor)}
                >
                  {(Object.keys(VENDOR_LABEL) as ControllerVendor[]).map((v) => (
                    <option key={v} value={v}>
                      {VENDOR_LABEL[v]}
                    </option>
                  ))}
                </select>
              </div>

              <div className="field">
                <label htmlFor="ctl-name">{t("controllers.create.name")}</label>
                <input id="ctl-name" value={name} autoComplete="off" onChange={(e) => setName(e.target.value)} />
                {fieldErrors.name ? (
                  <p className="field-error" role="alert">{fieldErrors.name}</p>
                ) : (
                  <p className="field-hint">{t("controllers.create.nameHint")}</p>
                )}
              </div>

              <div className="field">
                <label htmlFor="ctl-url">{t("controllers.create.baseUrl")}</label>
                <input
                  id="ctl-url"
                  value={baseUrl}
                  autoComplete="off"
                  placeholder="https://192.0.2.10"
                  onChange={(e) => setBaseUrl(e.target.value)}
                />
                {fieldErrors.baseUrl ? (
                  <p className="field-error" role="alert">{fieldErrors.baseUrl}</p>
                ) : (
                  <p className="field-hint">{t("controllers.create.baseUrlHint")}</p>
                )}
              </div>

              {/*
                Omada only, and asked for as the thing an operator can actually
                produce: their controller's address bar. The 32-character id is
                extracted from it, because nobody can be expected to know which part
                of the URL is the identifier.
              */}
              {vendor === "omada" ? (
                <div className="field">
                  <label htmlFor="ctl-cid">{t("controllers.create.controllerId")}</label>
                  <input
                    id="ctl-cid"
                    value={controllerId}
                    autoComplete="off"
                    onChange={(e) => setControllerId(e.target.value)}
                  />
                  {fieldErrors.controllerId ? (
                    <p className="field-error" role="alert">{fieldErrors.controllerId}</p>
                  ) : (
                    <p className="field-hint">{t("controllers.create.controllerIdHint")}</p>
                  )}
                </div>
              ) : null}

              {/* Only where the vendor has more than one. A select with one option is
                  a decision the operator does not have. */}
              {authModes.length > 1 ? (
                <div className="field">
                  <label htmlFor="ctl-auth">{t("controllers.create.authMode")}</label>
                  <select
                    id="ctl-auth"
                    value={authMode}
                    onChange={(e) => setAuthMode(e.target.value as ControllerAuthMode)}
                  >
                    {authModes.map((m) => (
                      <option key={m} value={m}>
                        {AUTH_LABEL[m]}
                      </option>
                    ))}
                  </select>
                </div>
              ) : null}

              {authMode === "api-key" ? (
                <div className="field">
                  <label htmlFor="ctl-key">{t("controllers.create.apiKey")}</label>
                  <input
                    id="ctl-key"
                    type="password"
                    value={apiKey}
                    autoComplete="off"
                    onChange={(e) => setApiKey(e.target.value)}
                  />
                  {fieldErrors.apiKey ? (
                    <p className="field-error" role="alert">{fieldErrors.apiKey}</p>
                  ) : (
                    <p className="field-hint">{AUTH_HINT[authMode]}</p>
                  )}
                </div>
              ) : (
                <>
                  <div className="field">
                    <label htmlFor="ctl-user">{t("controllers.create.username")}</label>
                    <input
                      id="ctl-user"
                      value={username}
                      autoComplete="off"
                      onChange={(e) => setUsername(e.target.value)}
                    />
                    {fieldErrors.username ? (
                      <p className="field-error" role="alert">{fieldErrors.username}</p>
                    ) : (
                      <p className="field-hint">{AUTH_HINT[authMode]}</p>
                    )}
                  </div>
                  <div className="field">
                    <label htmlFor="ctl-pass">{t("controllers.create.password")}</label>
                    <input
                      id="ctl-pass"
                      type="password"
                      value={password}
                      autoComplete="off"
                      onChange={(e) => setPassword(e.target.value)}
                    />
                    {fieldErrors.password ? <p className="field-error" role="alert">{fieldErrors.password}</p> : null}
                  </div>
                </>
              )}

              <div className="field">
                <label htmlFor="ctl-tls">{t("controllers.create.tlsMode")}</label>
                <select
                  id="ctl-tls"
                  value={tlsMode}
                  onChange={(e) => setTlsMode(e.target.value as TlsMode)}
                >
                  <option value="trust-on-first-use">{t("controllers.create.tls.trust-on-first-use")}</option>
                  <option value="verify">{t("controllers.create.tls.verify")}</option>
                </select>
                <p className="field-hint">{t("controllers.create.tlsHint")}</p>
              </div>
            </div>

            <p className="field-hint">{t("controllers.create.credentialWhy")}</p>
          </div>

          <div className="dialog-foot">
            <button type="button" className="btn" onClick={onClose}>
              {t("common.cancel")}
            </button>
            {/* `aria-disabled` rather than `disabled`: the operator pressed this, so it
                has focus, and `disabled` on a focused element blurs it to `<body>` for the
                whole request. Measured and written out at `Accounts.tsx`'s row action. */}
            <button
              type="submit"
              className="btn btn-primary"
              aria-disabled={create.isPending || undefined}
            >
              {t("controllers.create.submit")}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}

/**
 * `https://host:8043/a1b2…f90/login` → `a1b2…f90`.
 *
 * The operator is asked for their address bar rather than for an identifier, because
 * nobody can be expected to know which 32 characters of a URL are the id. A value
 * that is already just the id passes through.
 */
/**
 * The warnings the probe answer carries, minus the one this screen already states
 * with more context.
 *
 * `Controllers.tsx` rendered none of `ControllerProbe.warnings` — zero references —
 * so a UniFi probe showed six green check rows and nothing else, while the answer
 * beside them said the driver could not confirm an access point and that some
 * capability values were believed rather than measured. Both are conditions
 * `docs/rules/security.md` §4 cares about and neither reached the operator.
 *
 * The reason it was left is real and is not "nobody noticed": `version-below-floor`
 * has a **dedicated urgent notice** on this screen, derived from `versionSupported`
 * and `minimumVersion`, and passing the list straight through would say the same
 * thing twice in two registers. So it is suppressed here — on this screen only; the
 * access-point screens have no such notice and must keep it.
 *
 * **Suppressed only while the dedicated notice is actually rendering.** That notice
 * needs `minimumVersion` as well as `versionSupported === false`, because a verdict
 * with no target sends an operator to find out for themselves; the field is
 * nullable, and a probe that answers `false` with no floor renders no notice at all.
 * Filtering unconditionally would take the only statement of the problem off the
 * screen in exactly the case where the screen says least — the shape of the defect
 * this function exists to repair, arrived at from the other side.
 *
 * Everything else passes through untouched, to `ProbeWarnings`, which is where the
 * standing/found split lives. Not a second renderer: two screens that filter this
 * separately drift apart, and the one that drifts shows no warnings at all.
 */
export function controllerProbeWarnings(probe: ControllerProbe): ProbeWarning[] {
  const versionNoticeShown = probe.versionSupported === false && Boolean(probe.minimumVersion);
  return probe.warnings.filter((w) => !(versionNoticeShown && w.kind === "version-below-floor"));
}

export function extractControllerId(input: string): string {
  const trimmed = input.trim();
  const match = /[0-9a-f]{32}/i.exec(trimmed);
  return match ? match[0] : trimmed;
}

/* ── probe and discover ───────────────────────────────────────────────────── */

function ProbeDialog({
  controller,
  siteId,
  siteName,
  onClose,
  onChanged,
}: {
  controller: Controller;
  siteId: string;
  /** The venue's own name, so the dialog can say what discovery is about to attach
      this controller to rather than "the current site". Empty while `GET /sites` is
      in flight, which is the one case the sentence below drops the name. */
  siteName: string;
  onClose: () => void;
  onChanged: (message?: string) => void;
}) {
  const titleId = useId();
  const [result, setResult] = useState<ControllerProbe | null>(null);
  const [vendorSiteId, setVendorSiteId] = useState<string>(controller.vendorSites[0]?.id ?? "");

  const probe = useMutation({
    mutationFn: () => api.probeController(controller.id),
    onSuccess: (r) => {
      setResult(r);
      if (!vendorSiteId && r.vendorSites?.length) setVendorSiteId(r.vendorSites[0]!.id);
      onChanged();
    },
  });

  const discover = useMutation({
    mutationFn: () => api.discoverFromController(controller.id, { siteId, vendorSiteId }),
    onSuccess: (r) => onChanged(t("controllers.discover.result", r.adopted, r.updated)),
  });

  const checks = result?.checks ?? [];
  const presence = checks.find((c) => c.kind === "presence-check");
  /*
   * The freshest answer there is, and it is not always this dialog's.
   *
   * This read `result?.reachability === "ok"` — only a probe run *since the dialog
   * opened* — while `sites` on the next line already preferred fresh-then-stored. The
   * two lines describe the same controller and only one of them remembered what the
   * list is displaying one row away: a controller the table pills **Reachable** opened
   * with *Discover access points* disabled, and the operator had to re-run a probe to
   * be allowed to ask a question the screen had already answered.
   *
   * It also made `/problems/controller-unreachable` unreachable by construction. The
   * server refuses discovery when the controller is
   * not reachable; the console offered the control only when a probe had just said it
   * was. Every path to the button ran through a fresh probe that would have disabled it
   * first, so the refusal could not be produced from this screen at all — its
   * `ErrorNotice` sat inside the same branch that hid the control which would raise it.
   *
   * The precondition is unchanged and still real: `unknown` — never probed — stays
   * disabled, as do `unreachable` and `credentials-expired`. What changes is that a
   * probe's answer is allowed to outlive the dialog it was run in, which is the only
   * form in which this refusal exists in production: the probe said `ok`, and by the
   * time the operator pressed Discover the controller had gone.
   */
  const reachable = (result?.reachability ?? controller.reachability) === "ok";
  const sites = result?.vendorSites ?? controller.vendorSites;
  /* Not `controllerAttachment(...) === "unattached"`: a controller registered
     seconds ago has no site list yet either, and discovery attaches that one too.
     The three-valued distinction is what the *list* needs, to choose between telling
     the operator to probe and telling them to attach; here both states have the same
     answer and asking the narrower question would put the sentence on one of them. */
  const unattached = !controller.vendorSites.some((v) => v.boundSiteId);

  return (
    <Modal labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {controller.name}
          </h2>
        </div>

        <div className="dialog-body" tabIndex={0} role="group" aria-label={controller.name}>
          {probe.error ? (
            <ErrorNotice error={probe.error} title={t("controllers.probe.failed.title")} />
          ) : null}

          {result?.flavour ? (
            <p className="field-hint">{t("controllers.probe.flavour", result.flavour)}</p>
          ) : null}

          {/* The version failure states the target, not just the verdict. An operator
              told "unsupported" has to go and find out what would be supported. */}
          {result && result.versionSupported === false && result.minimumVersion ? (
            <Notice
              level="urgent"
              title={t("controllers.version.tooOld", result.minimumVersion)}
            />
          ) : null}

          {checks.length > 0 ? (
            <ul className="findings">
              {checks.map((c) => (
                <li key={c.kind} className="finding" data-result={c.result}>
                  <span className="finding-icon">
                    {c.result === "pass" ? (
                      <Check size={13} />
                    ) : c.result === "fail" ? (
                      <Alert size={13} />
                    ) : (
                      <Slash size={13} />
                    )}
                  </span>
                  <span className="finding-body">
                    <span className="finding-title">{CHECK_LABEL[c.kind] ?? c.kind}</span>
                    {c.detail ? <span className="finding-detail">{c.detail}</span> : null}
                  </span>
                </li>
              ))}
            </ul>
          ) : null}

          {/*
            The one finding that decides whether this controller may be used at all.
            Rendered as an urgent notice rather than a row in the list, because a
            failed row among six reads as "mostly fine".
          */}
          {presence?.result === "fail" ? (
            <Notice level="urgent" title={t("controllers.probe.check.presence-check")}>
              {t("controllers.probe.presenceWhy")}
            </Notice>
          ) : null}

          {/*
           * What the probe warned about, and it is the same component the
           * access-point screens use rather than a second one written here.
           *
           * Below the check rows and below both urgent notices, because it is the
           * quietest of the three registers on this screen: the rows are the answer,
           * the urgent notices are what stops this controller being used, and these
           * are what to know before relying on the answer. `controllerProbeWarnings`
           * is where the one overlap with the version notice is resolved and says
           * why.
           */}
          {result ? <ProbeWarnings warnings={controllerProbeWarnings(result)} /> : null}

          {reachable ? (
            <>
              {/*
               * **What this button is also going to do**, said before it is pressed.
               *
               * `discoverFromController` binds the chosen vendor site to the site the
               * console is showing, as a side effect of reading access points. That
               * binding is the step a hidden unattached controller made unreachable,
               * and it was never named anywhere in this dialog — an operator pressing *Discover access points*
               * had no way to know it was also the act of attaching the controller to
               * their venue.
               *
               * Read off `controller` rather than off the probe result, because
               * `ControllerProbe.vendorSites` carries no `boundSiteId` at all: only the
               * stored list knows what is adopted, and asking the probe would be asking
               * a question its shape cannot answer.
               */}
              {unattached ? (
                <p className="field-hint">
                  {siteName
                    ? t("controllers.discover.attaches", siteName)
                    : t("controllers.discover.attachesHere")}
                </p>
              ) : null}
              <p className="field-hint">{t("controllers.discover.why")}</p>
              <p className="field-hint">{t("controllers.discover.secret")}</p>
              {sites.length > 1 ? (
                <div className="field">
                  <label htmlFor="ctl-site">{t("controllers.discover.vendorSite")}</label>
                  <select
                    id="ctl-site"
                    value={vendorSiteId}
                    onChange={(e) => setVendorSiteId(e.target.value)}
                  >
                    {sites.map((v) => (
                      <option key={v.id} value={v.id}>
                        {v.name}
                      </option>
                    ))}
                  </select>
                </div>
              ) : null}
            </>
          ) : null}

          {/*
           * Outside the `reachable` branch, and that is the whole of the second half.
           *
           * Driven: with the notice inside it, a refusal appeared and then **vanished
           * on the operator's next move** — Discover fails, the operator presses Probe
           * to find out why, the probe answers `unreachable`, `reachable` flips false
           * and the sentence explaining the failure is unmounted along with the
           * control. The one action the refusal invites is the one that destroys it.
           *
           * A refusal is never conditional on the state that produced it still holding.
           */}
          {discover.error ? (
            <ErrorNotice error={discover.error} title={t("controllers.discover.failed.title")} />
          ) : null}
        </div>

        <div className="dialog-foot">
          {/*
           * A way out that an operator can see.
           *
           * This foot held *Probe* and *Discover access points* and nothing else, so
           * `Escape` and a backdrop click were the only exits — gestures you have to
           * already know, and on a phone only one of them exists at all. Every other
           * overlay in this console is dismissible by a visible control; a dialog
           * whose exit is invisible is not a minimal design, it is a dead end.
           *
           * *Close*, not *Cancel*: a probe that has run has already been recorded and
           * the list behind this dialog already shows its answer. There is nothing
           * here to take back, and offering to take it back would say there is.
           */}
          <button type="button" className="btn" onClick={onClose}>
            {t("common.close")}
          </button>
          {/* Probing is the reason an operator opened this dialog, so this is the
              button they press and the one that must not blur. `aria-disabled` and a
              guarded handler — see `Accounts.tsx`'s row action. */}
          <button
            type="button"
            className="btn"
            onClick={() => {
              if (!probe.isPending) probe.mutate();
            }}
            aria-disabled={probe.isPending || undefined}
          >
            <Refresh size={14} />
            {probe.isPending ? t("controllers.probing") : t("controllers.probe")}
          </button>
          {/*
            Two conditions, and they are not the same kind of thing, so they are not
            expressed the same way.

            `!reachable` is a **precondition**: discovery is meaningless until the probe
            has answered, and the button has never had focus in that state, because it
            has never been focusable in it. A real `disabled` is what keeps a control
            that cannot be used out of the tab order, and that is worth keeping.

            `discover.isPending` is the operator's own press, on a control that had
            focus a millisecond earlier. That half is `aria-disabled`.
          */}
          <button
            type="button"
            className="btn btn-primary"
            disabled={!reachable}
            aria-disabled={discover.isPending || undefined}
            onClick={() => {
              if (!discover.isPending) discover.mutate();
            }}
          >
            {discover.isPending ? t("controllers.discovering") : t("controllers.discover")}
          </button>
        </div>
      </div>
    </Modal>
  );
}
