import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState, type ReactNode } from "react";
import type {
  BodyCeilings,
  NasCoaTiming,
  NetworkBound,
  NetworkSettings,
  NetworkSettingsUpdate,
  PortalCertificateStatus,
  PortalCertificateUpload,
  EapCertificateUpload,
  RadiusApply,
  RadiusClientSync,
  RadiusServerSettings,
  RadiusServerSettingsUpdate,
  SignInThrottles,
  NasCoaTimingUpdate,
  CoaTimingValues,
  CoaTimingBounds,
} from "@foyer/contract";
import { ApiError, request } from "../api.js";
import { PageHead } from "../App.js";
import { useCan } from "../auth.js";
import { absoluteTime, relativeTime } from "../format.js";
import { t } from "../i18n/network.js";
import { fieldMessages } from "../problems.js";
import { ConfirmDialog, Empty, ErrorNotice, Notice } from "../ui/primitives.js";
import { Advanced, SettingNumber, UNITS, boundHint, bytesUnit, parsed, shown } from "../ui/settings.js";

/**
 * `/network` — the transport and RADIUS-plane settings (every operator setting is
 * set here, and none needs a restart of Foyer).
 *
 * Four surfaces, each one of the contract's `network` operations:
 *
 *   - the portal's address, the sign-in limits and the request sizes, one form,
 *     because `updateNetworkSettings` replaces the whole set;
 *   - the portal certificate, uploaded as PEM, **the key write-only** — this screen
 *     never holds one it did not just read from the operator's own file;
 *   - FreeRADIUS: the worker count and EAP-TLS, saved and then applied, with the
 *     restart said before it is triggered and the client-table sync's state shown;
 *   - per-device disconnect timing.
 *
 * Staff read everything (`nas:read`) and change nothing: the forms render read-only
 * with a line saying so, rather than disappearing — a staff member who can see a
 * certificate is about to expire can tell somebody who can replace it.
 *
 * The calls live here rather than in `api.ts`, which is in the entry chunk: a screen
 * opened when the network changes is not a reason for every operator to download
 * eleven more methods first.
 */
const net = {
  settings: () => request<NetworkSettings>("/network/settings"),
  updateSettings: (body: NetworkSettingsUpdate) =>
    request<NetworkSettings>("/network/settings", { method: "PUT", body: JSON.stringify(body) }),
  putCertificate: (body: PortalCertificateUpload) =>
    request<PortalCertificateStatus>("/network/portal-certificate", { method: "PUT", body: JSON.stringify(body) }),
  deleteCertificate: () => request<PortalCertificateStatus>("/network/portal-certificate", { method: "DELETE" }),
  radius: () => request<RadiusServerSettings>("/network/radius"),
  updateRadius: (body: RadiusServerSettingsUpdate) =>
    request<RadiusServerSettings>("/network/radius", { method: "PUT", body: JSON.stringify(body) }),
  putEap: (body: EapCertificateUpload) =>
    request<RadiusServerSettings>("/network/radius/eap-certificate", { method: "PUT", body: JSON.stringify(body) }),
  sync: () => request<RadiusClientSync>("/network/radius/sync", { method: "POST" }),
  apply: () => request<RadiusApply>("/network/radius/apply", { method: "POST" }),
  coa: () =>
    request<{ items: NasCoaTiming[]; defaults: CoaTimingValues; bounds: CoaTimingBounds }>("/network/coa-timing"),
  updateCoa: (nasId: string, body: NasCoaTimingUpdate) =>
    request<NasCoaTiming>(`/nas/${encodeURIComponent(nasId)}/coa-timing`, { method: "PUT", body: JSON.stringify(body) }),
};

const KEY = ["network"] as const;

export function NetworkScreen() {
  const canManage = useCan("nas:manage");
  return (
    <div className="page">
      <PageHead title={t("network.title")} description={t("network.description")} />
      {!canManage ? (
        <div style={{ marginBottom: "1rem" }}>
          <Notice level="info" title={t("network.readOnly")} />
        </div>
      ) : null}
      <AddressForm canManage={canManage} />
      <Certificate canManage={canManage} />
      <Advanced lede={t("network.advanced.lede")}>
        <LimitsForm canManage={canManage} />
        <Radius canManage={canManage} />
        <CoaTiming canManage={canManage} />
      </Advanced>
    </div>
  );
}

/**
 * The refusal's sentence for a field, preferring this screen's copy for a
 * `certificate-refused` reason over the console-wide fallback.
 */
export function networkFieldMessages(error: unknown): Record<string, string> {
  const out = fieldMessages(error);
  if (!(error instanceof ApiError)) return out;
  const fieldParams = (error.problem?.params as { fieldParams?: Record<string, { reason?: unknown }> } | undefined)
    ?.fieldParams;
  for (const [field, fault] of Object.entries(error.fieldFaults)) {
    const reason = fieldParams?.[field]?.reason;
    if (fault === "certificate-refused" && typeof reason === "string" && isRefusalReason(reason)) {
      out[field] = t(`network.cert.refused.${reason}`);
    }
  }
  return out;
}

const REFUSAL_REASONS = [
  "not-pem",
  "key-mismatch",
  "expired",
  "not-yet-valid",
  "chain-broken",
  "name-not-covered",
  "portal-url-not-https",
] as const;
type RefusalReason = (typeof REFUSAL_REASONS)[number];
const isRefusalReason = (r: string): r is RefusalReason => (REFUSAL_REASONS as readonly string[]).includes(r);

function Card({ id, title, body, children }: { id: string; title: string; body: string; children: ReactNode }) {
  return (
    <section className="card" aria-labelledby={id} style={{ marginBottom: "1.5rem" }}>
      <h2 id={id}>{title}</h2>
      <p className="field-hint">{body}</p>
      {children}
    </section>
  );
}

function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: string;
  error?: string;
  children: (id: string, describedBy: string) => ReactNode;
}) {
  const id = useId();
  const describedBy = [hint ? `${id}-hint` : "", error ? `${id}-error` : ""].filter(Boolean).join(" ");
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children(id, describedBy)}
      {hint ? (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      ) : null}
      {error ? (
        <p className="field-error" role="alert" id={`${id}-error`}>
          {error}
        </p>
      ) : null}
    </div>
  );
}

const SOURCE = {
  console: () => t("network.source.console"),
  environment: () => t("network.source.environment"),
  derived: () => t("network.source.derived"),
  unset: () => t("network.source.unset"),
};

// ── the portal's address, the limits and the sizes ────────────────────────────

type Draft = { portalUrl: string; venueUrl: string; throttles: SignInThrottles; bodyCeilings: BodyCeilings };

const THROTTLES = ["portalAddress", "portalAccount", "consoleAddress", "consoleAccount"] as const;
const CEILINGS = ["portalBytes", "consoleBytes", "anonymousBytes", "headerBytes"] as const;

function draftOf(s: NetworkSettings): Draft {
  return {
    portalUrl: s.portalUrl.source === "console" ? (s.portalUrl.value ?? "") : "",
    venueUrl: s.venueUrl.source === "console" ? (s.venueUrl.value ?? "") : "",
    throttles: structuredClone(s.throttles),
    bodyCeilings: { ...s.bodyCeilings },
  };
}

/** The update the draft stands for: an empty address goes back to the installation's. */
export function updateFromDraft(d: Draft): NetworkSettingsUpdate {
  return {
    portalUrl: d.portalUrl.trim() === "" ? null : d.portalUrl.trim(),
    venueUrl: d.venueUrl.trim() === "" ? null : d.venueUrl.trim(),
    throttles: d.throttles,
    bodyCeilings: d.bodyCeilings,
  };
}

/**
 * The bound one throttle figure is held to, with **that throttle's own** default.
 *
 * `bounds.throttleBurst.default` is one figure for four buckets whose shipped values
 * differ by two orders of magnitude, so a reset read from it would set the console's
 * per-account bucket to the portal's per-address figure. `defaults.throttles` is the
 * figure per bucket, sent for exactly this (`NetworkSettingDefaults`).
 */
export function throttleBound(
  s: Pick<NetworkSettings, "bounds" | "defaults">,
  name: (typeof THROTTLES)[number],
  figure: "burst" | "intervalMs",
): NetworkBound {
  const range = figure === "burst" ? s.bounds.throttleBurst : s.bounds.throttleIntervalMs;
  return { ...range, default: s.defaults.throttles[name][figure] };
}

/** A ceiling's bound, with its own default — the same rule as `throttleBound`. */
export function ceilingBound(s: Pick<NetworkSettings, "bounds" | "defaults">, name: (typeof CEILINGS)[number]) {
  return { ...s.bounds[name], default: s.defaults.bodyCeilings[name] };
}

/** One `updateNetworkSettings` save: the whole set, as the operation replaces it. */
function useSettingsSave() {
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: [...KEY, "settings"], queryFn: net.settings });
  const save = useMutation({
    mutationFn: net.updateSettings,
    onSuccess: (next) => qc.setQueryData([...KEY, "settings"], next),
  });
  return { settings, save };
}

function SaveState({ error, saved }: { error: unknown; saved: boolean }) {
  return (
    <>
      {error ? (
        <div style={{ margin: "0.875rem 0" }}>
          <ErrorNotice error={error} title={t("network.failed")} />
        </div>
      ) : null}
      {saved ? (
        <div style={{ margin: "0.875rem 0" }}>
          <Notice level="positive" role="status" title={t("network.saved")} />
        </div>
      ) : null}
    </>
  );
}

/**
 * The portal's address — what a venue sets. Up front, and saved on its own: the
 * limits and sizes go back as the server holds them, since the operation replaces
 * the whole set.
 */
function AddressForm({ canManage }: { canManage: boolean }) {
  const { settings, save } = useSettingsSave();
  const [draft, setDraft] = useState<Pick<Draft, "portalUrl" | "venueUrl"> | null>(null);
  const [saved, setSaved] = useState(false);
  const errors = networkFieldMessages(save.error);

  if (settings.error) return <ErrorNotice error={settings.error} title={t("network.url.title")} />;
  const s = settings.data;
  if (!s) return null;
  const d = draft ?? draftOf(s);
  const edit = (next: Partial<Pick<Draft, "portalUrl" | "venueUrl">>) => {
    setSaved(false);
    setDraft({ portalUrl: d.portalUrl, venueUrl: d.venueUrl, ...next });
  };

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!canManage || save.isPending) return;
        save.mutate(updateFromDraft({ ...draftOf(s), portalUrl: d.portalUrl, venueUrl: d.venueUrl }), {
          onSuccess: () => {
            setDraft(null);
            setSaved(true);
          },
        });
      }}
    >
      <Card id="network-url" title={t("network.url.title")} body={t("network.url.body")}>
        <div className="form-grid">
          <Field
            label={t("network.url.portal")}
            hint={`${t("network.url.portalHint")} ${SOURCE[s.portalUrl.source]()}: ${s.portalUrl.value ?? "—"}`}
            error={errors.portalUrl}
          >
            {(id, by) => (
              <input
                id={id}
                type="url"
                value={d.portalUrl}
                readOnly={!canManage}
                placeholder={s.portalUrl.value ?? ""}
                aria-describedby={by}
                aria-invalid={Boolean(errors.portalUrl) || undefined}
                onChange={(e) => edit({ portalUrl: e.target.value })}
              />
            )}
          </Field>
          <Field label={t("network.url.venue")} hint={t("network.url.venueHint")} error={errors.venueUrl}>
            {(id, by) => (
              <input
                id={id}
                type="url"
                value={d.venueUrl}
                readOnly={!canManage}
                aria-describedby={by}
                aria-invalid={Boolean(errors.venueUrl) || undefined}
                onChange={(e) => edit({ venueUrl: e.target.value })}
              />
            )}
          </Field>
          <SaveState error={save.error && !errors.portalUrl && !errors.venueUrl ? save.error : null} saved={saved} />
          {canManage ? (
            <button type="submit" className="btn btn-primary" aria-disabled={save.isPending || draft === null || undefined}>
              {t("network.url.save")}
            </button>
          ) : null}
        </div>
      </Card>
    </form>
  );
}

/**
 * The sign-in limits and the request sizes: the technical half of the same
 * operation, inside the screen's `Advanced` section, in seconds and KB/MB with each
 * figure's own default a click away.
 */
function LimitsForm({ canManage }: { canManage: boolean }) {
  const { settings, save } = useSettingsSave();
  const [draft, setDraft] = useState<Pick<Draft, "throttles" | "bodyCeilings"> | null>(null);
  const [saved, setSaved] = useState(false);
  const errors = networkFieldMessages(save.error);

  if (settings.error) return <ErrorNotice error={settings.error} title={t("network.limits.title")} />;
  const s = settings.data;
  if (!s) return null;
  const d = draft ?? { throttles: structuredClone(s.throttles), bodyCeilings: { ...s.bodyCeilings } };
  const edit = (next: Partial<Pick<Draft, "throttles" | "bodyCeilings">>) => {
    setSaved(false);
    setDraft({ ...d, ...next });
  };
  const setThrottle = (name: (typeof THROTTLES)[number], figure: "burst" | "intervalMs", v: number) =>
    edit({ throttles: { ...d.throttles, [name]: { ...d.throttles[name], [figure]: v } } });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!canManage || save.isPending) return;
        save.mutate(updateFromDraft({ ...draftOf(s), ...d }), {
          onSuccess: () => {
            setDraft(null);
            setSaved(true);
          },
        });
      }}
    >
      <Card id="network-limits" title={t("network.limits.title")} body={t("network.limits.body")}>
        <div className="form-grid">
          {THROTTLES.map((name) => (
            <fieldset key={name} className="field" style={{ border: 0, padding: 0 }}>
              <legend>{t(`network.limits.${name}`)}</legend>
              {name === "portalAddress" ? <p className="field-hint">{t("network.limits.natHint")}</p> : null}
              <SettingNumber
                label={t("network.limits.burst")}
                bound={throttleBound(s, name, "burst")}
                value={d.throttles[name].burst}
                readOnly={!canManage}
                error={errors[`throttles.${name}.burst`]}
                onChange={(v) => setThrottle(name, "burst", v)}
              />
              <SettingNumber
                label={t("network.limits.interval")}
                bound={throttleBound(s, name, "intervalMs")}
                unit={UNITS.msAsSeconds}
                value={d.throttles[name].intervalMs}
                readOnly={!canManage}
                error={errors[`throttles.${name}.intervalMs`]}
                onChange={(v) => setThrottle(name, "intervalMs", v)}
              />
            </fieldset>
          ))}
        </div>
      </Card>

      <Card id="network-ceilings" title={t("network.ceilings.title")} body={t("network.ceilings.body")}>
        <div className="form-grid">
          {CEILINGS.map((name) => {
            const bound = ceilingBound(s, name);
            return (
              <SettingNumber
                key={name}
                label={t(`network.ceilings.${name}`)}
                bound={bound}
                unit={bytesUnit(bound)}
                value={d.bodyCeilings[name]}
                readOnly={!canManage}
                error={errors[`bodyCeilings.${name}`]}
                onChange={(v) => edit({ bodyCeilings: { ...d.bodyCeilings, [name]: v } })}
              />
            );
          })}
          <SaveState error={save.error && Object.keys(errors).length === 0 ? save.error : null} saved={saved} />
          {canManage ? (
            <button type="submit" className="btn btn-primary" aria-disabled={save.isPending || draft === null || undefined}>
              {t("network.settings.save")}
            </button>
          ) : null}
        </div>
      </Card>
    </form>
  );
}

// ── the portal certificate ────────────────────────────────────────────────────

/** A PEM text area with a file picker beside it, for the file an operator has. */
function PemField({
  label,
  hint,
  error,
  value,
  onChange,
  secret,
}: {
  label: string;
  hint: string;
  error?: string;
  value: string;
  onChange: (v: string) => void;
  secret?: boolean;
}) {
  return (
    <Field label={label} hint={hint} error={error}>
      {(id, by) => (
        <>
          <textarea
            id={id}
            rows={4}
            value={value}
            spellCheck={false}
            autoComplete="off"
            aria-describedby={by}
            aria-invalid={Boolean(error) || undefined}
            onChange={(e) => onChange(e.target.value)}
            style={{ fontFamily: "var(--font-mono, monospace)", ...(secret ? { WebkitTextSecurity: "disc" } : {}) }}
          />
          <label className="btn" style={{ marginTop: "0.5rem" }}>
            {t("network.cert.fromFile")}
            <input
              type="file"
              accept=".pem,.crt,.key,.cer"
              className="visually-hidden"
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file) void file.text().then(onChange);
                e.target.value = "";
              }}
            />
          </label>
        </>
      )}
    </Field>
  );
}

export function CertificateFacts({ st }: { st: PortalCertificateStatus }) {
  if (st.state === "none") return <p>{t("network.cert.none")}</p>;
  const reason = st.notServedReason;
  return (
    <>
      {st.state === "served" ? (
        <Notice level="positive" title={t("network.cert.served")} />
      ) : (
        <Notice
          level="warning"
          title={`${t("network.cert.notServed")} ${reason ? t(`network.cert.reason.${reason}`) : ""}`}
        />
      )}
      {st.source === "environment" ? <p className="field-hint">{t("network.cert.fromEnvironment")}</p> : null}
      <dl className="field-hint">
        <dt>{t("network.cert.names")}</dt>
        <dd>{[...(st.dnsNames ?? []), ...(st.ipAddresses ?? [])].join(", ") || "—"}</dd>
        <dt>{t("network.cert.until")}</dt>
        <dd>{absoluteTime(st.notAfter) ?? st.notAfter ?? "—"}</dd>
        <dt>{t("network.cert.fingerprint")}</dt>
        <dd>
          <code>{st.fingerprintSha256 ?? "—"}</code>
        </dd>
        <dt>{t("network.cert.key")}</dt>
        <dd>{st.keySet ? t("network.cert.keySet") : "—"}</dd>
      </dl>
    </>
  );
}

function Certificate({ canManage }: { canManage: boolean }) {
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: [...KEY, "settings"], queryFn: net.settings });
  const [cert, setCert] = useState("");
  const [key, setKey] = useState("");
  const [url, setUrl] = useState("");
  const [saved, setSaved] = useState(false);
  const [removing, setRemoving] = useState(false);
  const refresh = () => void qc.invalidateQueries({ queryKey: [...KEY, "settings"] });
  const upload = useMutation({
    mutationFn: net.putCertificate,
    onSuccess: () => {
      /* The key leaves this screen's memory the moment it is sent. */
      setCert("");
      setKey("");
      setUrl("");
      setSaved(true);
      refresh();
    },
  });
  const remove = useMutation({
    mutationFn: net.deleteCertificate,
    onSuccess: () => {
      setRemoving(false);
      refresh();
    },
  });
  const errors = networkFieldMessages(upload.error);
  const st = settings.data?.portalCertificate;

  return (
    <Card id="network-cert" title={t("network.cert.title")} body={t("network.cert.body")}>
      {st ? <CertificateFacts st={st} /> : null}
      {canManage && st?.source === "console" ? (
        <button type="button" className="btn btn-danger" onClick={() => setRemoving(true)}>
          {t("network.cert.remove")}
        </button>
      ) : null}
      {canManage ? (
        <form
          style={{ marginTop: "1rem" }}
          onSubmit={(e) => {
            e.preventDefault();
            setSaved(false);
            if (upload.isPending) return;
            upload.mutate({
              certificatePem: cert,
              privateKeyPem: key,
              ...(url.trim() ? { portalUrl: url.trim() } : {}),
            });
          }}
        >
          <h3>{t("network.cert.upload")}</h3>
          <div className="form-grid">
            <PemField
              label={t("network.cert.certificatePem")}
              hint={t("network.cert.certificateHint")}
              error={errors.certificatePem}
              value={cert}
              onChange={setCert}
            />
            <PemField
              label={t("network.cert.privateKeyPem")}
              hint={t("network.cert.privateKeyHint")}
              error={errors.privateKeyPem}
              value={key}
              onChange={setKey}
              secret
            />
            <Field label={t("network.cert.portalUrl")} hint={t("network.cert.portalUrlHint")} error={errors.portalUrl}>
              {(id, by) => (
                <input
                  id={id}
                  type="url"
                  value={url}
                  placeholder={settings.data?.portalUrl.value ?? ""}
                  aria-describedby={by}
                  aria-invalid={Boolean(errors.portalUrl) || undefined}
                  onChange={(e) => setUrl(e.target.value)}
                />
              )}
            </Field>
            {upload.error && Object.keys(errors).length === 0 ? (
              <ErrorNotice error={upload.error} title={t("network.failed")} />
            ) : null}
            {saved ? <Notice level="positive" role="status" title={t("network.cert.saved")} /> : null}
            <button
              type="submit"
              className="btn btn-primary"
              aria-disabled={upload.isPending || !cert.trim() || !key.trim() || undefined}
            >
              {t("network.cert.save")}
            </button>
          </div>
        </form>
      ) : null}
      <ConfirmDialog
        open={removing}
        title={t("network.cert.removeTitle")}
        verb={t("network.cert.remove")}
        consequence={t("network.cert.removeConsequence")}
        onConfirm={() => remove.mutate()}
        onCancel={() => setRemoving(false)}
        busy={remove.isPending}
        error={remove.error}
      />
    </Card>
  );
}

// ── FreeRADIUS ────────────────────────────────────────────────────────────────

function SyncState({ sync }: { sync: RadiusClientSync | undefined }) {
  if (!sync) return <p className="field-hint">{t("network.sync.never")}</p>;
  const when = relativeTime(sync.at) ?? sync.at;
  return (
    <>
      <Notice
        level={sync.outcome === "synced" ? "positive" : "negative"}
        title={`${t(`network.sync.outcome.${sync.outcome}`)} · ${t("network.sync.last", when)}`}
      >
        <p>{t("network.sync.counts", sync.inserted, sync.updated, sync.removed.length)}</p>
        {sync.changeClass === "restart" ? <p>{t("network.sync.restart")}</p> : null}
        {sync.reasons.map((r) => (
          <p key={r}>{r}</p>
        ))}
      </Notice>
      {sync.skipped.length > 0 ? (
        <>
          <p>{t("network.sync.skipped", sync.skipped.length)}</p>
          <ul>
            {sync.skipped.map((s) => (
              <li key={s.nasName}>
                <code>{s.nasName}</code> — {s.reason}
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </>
  );
}

/**
 * The sentence for an apply's outcome. `applied` without a restart is the
 * configuration FreeRADIUS was already running — the control plane's start-up
 * hand-over of an unchanged tree — and saying "restarted" there would be false.
 */
export function appliedKey(a: RadiusApply) {
  return a.outcome === "applied" && a.restarted === false
    ? ("network.radius.applied.unchanged" as const)
    : (`network.radius.applied.${a.outcome}` as const);
}

/**
 * What an apply just did. The body is the supervisor's own line — what
 * `radiusd -C` refused, or what FreeRADIUS said before it did not start, and
 * whether the previous configuration is running again — sanitised server-side,
 * so an operator reads the reason here rather than in a container log.
 */
export function ApplyOutcome({ applied }: { applied: RadiusApply }) {
  return (
    <Notice
      level={applied.outcome === "applied" ? "positive" : applied.outcome === "failed" ? "negative" : "warning"}
      role="status"
      title={t(appliedKey(applied))}
    >
      {applied.detail ? <p>{applied.detail}</p> : null}
    </Notice>
  );
}

function Radius({ canManage }: { canManage: boolean }) {
  const qc = useQueryClient();
  const radius = useQuery({ queryKey: [...KEY, "radius"], queryFn: net.radius });
  const [draft, setDraft] = useState<RadiusServerSettingsUpdate | null>(null);
  const [eap, setEap] = useState({ cert: "", key: "", ca: "" });
  const [applying, setApplying] = useState(false);
  const [applied, setApplied] = useState<RadiusApply | null>(null);
  const put = (next: RadiusServerSettings) => qc.setQueryData([...KEY, "radius"], next);
  const save = useMutation({
    mutationFn: net.updateRadius,
    onSuccess: (next) => {
      put(next);
      setDraft(null);
    },
  });
  const upload = useMutation({
    mutationFn: net.putEap,
    onSuccess: (next) => {
      put(next);
      setEap({ cert: "", key: "", ca: "" });
    },
  });
  const apply = useMutation({
    mutationFn: net.apply,
    onSuccess: (rec) => {
      setApplying(false);
      setApplied(rec);
      void qc.invalidateQueries({ queryKey: [...KEY, "radius"] });
    },
  });
  const sync = useMutation({
    mutationFn: net.sync,
    onSuccess: () => void qc.invalidateQueries({ queryKey: [...KEY, "radius"] }),
  });
  const errors = networkFieldMessages(save.error);
  const eapErrors = networkFieldMessages(upload.error);

  if (radius.error) return <ErrorNotice error={radius.error} title={t("network.radius.title")} />;
  const r = radius.data;
  if (!r) return null;
  const d = draft ?? { maxServers: r.maxServers, eapTlsEnabled: r.eapTlsEnabled };
  const b = r.maxServersBound;

  return (
    <>
      <Card id="network-radius" title={t("network.radius.title")} body={t("network.radius.body")}>
        {r.pendingApply ? <Notice level="warning" title={t("network.radius.pending")} /> : null}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!canManage || save.isPending) return;
            save.mutate(d);
          }}
        >
          <div className="form-grid">
            <SettingNumber
              label={t("network.radius.maxServers")}
              hint={t("network.radius.maxServersHint")}
              error={errors.maxServers}
              bound={b}
              value={d.maxServers}
              readOnly={!canManage}
              onChange={(v) => setDraft({ ...d, maxServers: v })}
            />
            <div className="field">
              <label>
                <input
                  type="checkbox"
                  checked={d.eapTlsEnabled}
                  disabled={!canManage}
                  onChange={(e) => setDraft({ ...d, eapTlsEnabled: e.target.checked })}
                />{" "}
                {t("network.radius.eap")}
              </label>
              <p className="field-hint">{t("network.radius.eapHint")}</p>
              {errors.eapTlsEnabled ? (
                <p className="field-error" role="alert">
                  {errors.eapTlsEnabled}
                </p>
              ) : null}
            </div>
            {canManage ? (
              <button type="submit" className="btn btn-primary" aria-disabled={save.isPending || draft === null || undefined}>
                {t("network.radius.save")}
              </button>
            ) : null}
          </div>
        </form>

        <h3>{t("network.radius.eapCert")}</h3>
        {r.eapCertificate.keySet ? (
          <dl className="field-hint">
            <dt>{t("network.cert.names")}</dt>
            <dd>{r.eapCertificate.subject ?? "—"}</dd>
            <dt>{t("network.cert.until")}</dt>
            <dd>{absoluteTime(r.eapCertificate.notAfter) ?? "—"}</dd>
            <dt>{t("network.cert.key")}</dt>
            <dd>{t("network.cert.keySet")}</dd>
          </dl>
        ) : (
          <p className="field-hint">{t("network.radius.eapNone")}</p>
        )}
        {canManage ? (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (upload.isPending) return;
              upload.mutate({ certificatePem: eap.cert, privateKeyPem: eap.key, caPem: eap.ca });
            }}
          >
            <div className="form-grid">
              <PemField
                label={t("network.cert.certificatePem")}
                hint={t("network.cert.certificateHint")}
                error={eapErrors.certificatePem}
                value={eap.cert}
                onChange={(v) => setEap({ ...eap, cert: v })}
              />
              <PemField
                label={t("network.cert.privateKeyPem")}
                hint={t("network.cert.privateKeyHint")}
                error={eapErrors.privateKeyPem}
                value={eap.key}
                onChange={(v) => setEap({ ...eap, key: v })}
                secret
              />
              <PemField
                label={t("network.radius.caPem")}
                hint={t("network.radius.eapHint")}
                error={eapErrors.caPem}
                value={eap.ca}
                onChange={(v) => setEap({ ...eap, ca: v })}
              />
              <button
                type="submit"
                className="btn"
                aria-disabled={upload.isPending || !eap.cert.trim() || !eap.key.trim() || !eap.ca.trim() || undefined}
              >
                {t("network.radius.eapSave")}
              </button>
            </div>
          </form>
        ) : null}

        <h3>{t("network.radius.apply")}</h3>
        {!r.applyAvailable ? <p className="field-hint">{t("network.radius.applyUnavailable")}</p> : null}
        {applied ? (
          <ApplyOutcome applied={applied} />
        ) : r.lastApply ? (
          <p className="field-hint">
            {t("network.radius.lastApply", relativeTime(r.lastApply.at) ?? r.lastApply.at)}{" "}
            {t(appliedKey(r.lastApply))}
            {r.lastApply.outcome === "failed" && r.lastApply.detail ? ` ${r.lastApply.detail}` : null}
          </p>
        ) : null}
        {canManage ? (
          <button type="button" className="btn btn-danger" onClick={() => setApplying(true)}>
            {t("network.radius.apply")}
          </button>
        ) : null}
        <ConfirmDialog
          open={applying}
          title={t("network.radius.applyTitle")}
          verb={t("network.radius.apply")}
          consequence={t("network.radius.applyConsequence")}
          recovery={!r.applyAvailable ? t("network.radius.applyUnavailable") : undefined}
          onConfirm={() => apply.mutate()}
          onCancel={() => setApplying(false)}
          busy={apply.isPending}
          error={apply.error}
        />
      </Card>

      <Card id="network-sync" title={t("network.sync.title")} body={t("network.sync.body")}>
        <SyncState sync={r.lastSync} />
        {sync.error ? <ErrorNotice error={sync.error} title={t("network.failed")} /> : null}
        {canManage ? (
          <button type="button" className="btn" aria-disabled={sync.isPending || undefined} onClick={() => sync.mutate()}>
            {t("network.sync.now")}
          </button>
        ) : null}
      </Card>
    </>
  );
}

// ── CoA timing ────────────────────────────────────────────────────────────────

function CoaTiming({ canManage }: { canManage: boolean }) {
  const list = useQuery({ queryKey: [...KEY, "coa"], queryFn: net.coa });
  const bounds = list.data?.bounds;
  return (
    <Card id="network-coa" title={t("network.coa.title")} body={t("network.coa.body")}>
      {list.error ? <ErrorNotice error={list.error} title={t("network.coa.title")} /> : null}
      {list.data && list.data.items.length === 0 ? (
        <Empty title={t("network.coa.empty.title")} description={t("network.coa.empty.description")} />
      ) : null}
      {bounds && list.data && list.data.items.length > 0 ? (
        <>
          <table>
            <thead>
              <tr>
                <th scope="col">{t("network.coa.device")}</th>
                <th scope="col">{t("network.coa.timeout")}</th>
                <th scope="col">{t("network.coa.retries")}</th>
                <th scope="col">
                  <span className="visually-hidden">{t("network.coa.save")}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.data.items.map((row) => (
                <CoaRow key={row.nasId} row={row} bounds={bounds} canManage={canManage} />
              ))}
            </tbody>
          </table>
          <p className="field-hint">
            {t("network.coa.timeout")}: {boundHint(bounds.timeoutMs, UNITS.msAsSeconds)} {t("network.coa.retries")}:{" "}
            {boundHint(bounds.retries, UNITS.count)}
          </p>
        </>
      ) : null}
    </Card>
  );
}

/** A CoA wait as typed, in seconds, back in the milliseconds the server holds; empty is the default. */
export const coaTimeoutBody = (text: string) => (text.trim() === "" ? null : parsed(text, UNITS.msAsSeconds));

function CoaRow({ row, bounds, canManage }: { row: NasCoaTiming; bounds: CoaTimingBounds; canManage: boolean }) {
  const qc = useQueryClient();
  const [timeout, setTimeoutText] = useState(shown(row.timeoutMs, UNITS.msAsSeconds));
  const [retries, setRetries] = useState(String(row.retries));
  const save = useMutation({
    mutationFn: (body: NasCoaTimingUpdate) => net.updateCoa(row.nasId, body),
    onSuccess: (next) => {
      setTimeoutText(shown(next.timeoutMs, UNITS.msAsSeconds));
      setRetries(String(next.retries));
      void qc.invalidateQueries({ queryKey: [...KEY, "coa"] });
    },
  });
  const errors = networkFieldMessages(save.error);
  const n = (v: string) => (v.trim() === "" ? null : Number(v));
  return (
    <tr>
      <td>
        {row.name}
        {row.source === "default" ? <span className="field-hint"> · {t("network.coa.default")}</span> : null}
      </td>
      <td>
        <input
          type="number"
          inputMode="decimal"
          step="any"
          min={bounds.timeoutMs.minimum / 1000}
          max={bounds.timeoutMs.maximum / 1000}
          aria-label={`${t("network.coa.timeout")} — ${row.name}`}
          value={timeout}
          readOnly={!canManage}
          aria-invalid={Boolean(errors.timeoutMs) || undefined}
          onChange={(e) => setTimeoutText(e.target.value)}
          style={{ maxWidth: "7rem" }}
        />
        {errors.timeoutMs ? <p className="field-error" role="alert">{errors.timeoutMs}</p> : null}
      </td>
      <td>
        <input
          type="number"
          inputMode="numeric"
          min={bounds.retries.minimum}
          max={bounds.retries.maximum}
          aria-label={`${t("network.coa.retries")} — ${row.name}`}
          value={retries}
          readOnly={!canManage}
          aria-invalid={Boolean(errors.retries) || undefined}
          onChange={(e) => setRetries(e.target.value)}
          style={{ maxWidth: "5rem" }}
        />
        {errors.retries ? <p className="field-error" role="alert">{errors.retries}</p> : null}
      </td>
      <td>
        {canManage ? (
          <>
            <button
              type="button"
              className="btn"
              aria-disabled={save.isPending || undefined}
              onClick={() => save.mutate({ timeoutMs: coaTimeoutBody(timeout), retries: n(retries) })}
            >
              {t("network.coa.save")}
            </button>{" "}
            {row.source === "console" ? (
              <button
                type="button"
                className="btn"
                aria-disabled={save.isPending || undefined}
                onClick={() => save.mutate({ timeoutMs: null, retries: null })}
              >
                {t("network.coa.reset")}
              </button>
            ) : null}
          </>
        ) : null}
      </td>
    </tr>
  );
}
