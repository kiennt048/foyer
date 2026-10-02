/**
 * The `network` tag — the oracle's world (every operator setting is set in the
 * console).
 *
 * `internal/api/handlers_network.go` holds the rules and `internal/apistore/
 * network.go` the persistence; this is the copy `make diff-api` compares them
 * against, so its bounds, defaults, refusals and shapes are the Go server's.
 *
 * **The key is never kept where an answer could reach it.** An uploaded private
 * key is checked against its certificate and then dropped: this world has no KEK to
 * seal it under, and the Go server never returns one either, so there is nothing
 * the mock needs it for after the check.
 */
import { X509Certificate, createPrivateKey, createPublicKey } from "node:crypto";
import type {
  BodyCeilings,
  CoaTimingValues,
  NasCoaTiming,
  NasDriverKind,
  NetworkBound,
  NetworkSettingSource,
  NetworkSettings,
  PortalCertificateStatus,
  RadiusApply,
  RadiusClientSync,
  RadiusServerSettings,
  RadiusSyncTrigger,
  SignInThrottles,
} from "@foyer/contract";

// ── bounds and defaults — `internal/api/netsettings.go` ────────────────────────

const bound = (minimum: number, maximum: number, dflt: number): NetworkBound => ({
  minimum,
  maximum,
  default: dflt,
});

export const DEFAULT_THROTTLES: SignInThrottles = {
  consoleAddress: { burst: 10, intervalMs: 6000 },
  consoleAccount: { burst: 5, intervalMs: 30000 },
  portalAddress: { burst: 30, intervalMs: 1000 },
  portalAccount: { burst: 5, intervalMs: 30000 },
};

export const DEFAULT_CEILINGS: BodyCeilings = {
  portalBytes: 16 << 10,
  consoleBytes: 1 << 20,
  anonymousBytes: 16 << 10,
  headerBytes: 64 << 10,
};

export const BOUNDS = {
  throttleBurst: bound(1, 1000, 30),
  throttleIntervalMs: bound(100, 600_000, 1000),
  portalBytes: bound(8 << 10, 128 << 10, 16 << 10),
  /* 7 MiB is not a restated constant but the Go server's memory plan for the shipped
     container (`internal/api/memplan.go`): the most a console body may be
     that its budget can still decode. `TestTheShippedPlanHoldsTheContractsLargestBody`
     fails when the Go figure moves, naming this file. */
  consoleBytes: bound(1 << 20, 7 << 20, 1 << 20),
  anonymousBytes: bound(8 << 10, 128 << 10, 16 << 10),
  headerBytes: bound(16 << 10, 128 << 10, 64 << 10),
};
// 44, not 256: every worker is an rlm_sql connection, and PostgreSQL is sized at
// max_connections = 80 (internal/api RadiusMaxServersCeiling).
export const MAX_SERVERS = bound(4, 44, 30);
export const COA_DEFAULTS: CoaTimingValues = { timeoutMs: 2000, retries: 0 };
export const COA_BOUNDS = { timeoutMs: [200, 10_000], retries: [0, 5] } as const;

/** The deployment's own address, derived from the listener, as a fresh install has. */
const BOOTSTRAP_PORTAL_URL = "http://10.20.0.2:8081/en/";

// ── state ──────────────────────────────────────────────────────────────────────

interface StoredCert {
  pem: string;
  leaf: X509Certificate;
  chain: number;
  caPem?: string;
  updatedAt: string;
  updatedBy: string;
}

const state = {
  portalUrl: null as string | null,
  venueUrl: null as string | null,
  throttles: null as SignInThrottles | null,
  ceilings: null as BodyCeilings | null,
  updatedAt: null as string | null,
  updatedBy: null as string | null,
  portalCert: null as StoredCert | null,
  maxServers: null as number | null,
  eapTls: false,
  radiusChangedAt: null as string | null,
  eap: null as StoredCert | null,
  lastSync: null as RadiusClientSync | null,
  lastApply: null as RadiusApply | null,
  coa: new Map<string, CoaTimingValues>(),
};

// ── certificates — `internal/config/portalcert.go` ─────────────────────────────

export type CertReason =
  | "not-pem"
  | "key-mismatch"
  | "expired"
  | "not-yet-valid"
  | "chain-broken"
  | "name-not-covered"
  | "portal-url-not-https";

export type CertRefusal = { input: "certificate" | "key" | "url" | "ca"; reason: CertReason };

function certificates(pem: string): X509Certificate[] | null {
  const blocks = pem.match(/-----BEGIN CERTIFICATE-----[\s\S]+?-----END CERTIFICATE-----/g);
  if (!blocks) return null;
  try {
    return blocks.map((b) => new X509Certificate(b));
  } catch {
    return null;
  }
}

/** Whether a PEM text holds at least one certificate and nothing unreadable. */
export function isCertificatePem(pem: string): boolean {
  return certificates(pem) !== null;
}

/** The pair's checks, first failure wins, in the Go server's order. */
export function checkPair(
  certPem: string,
  keyPem: string,
  now = Date.now(),
): { ok: true; chain: X509Certificate[] } | ({ ok: false } & CertRefusal) {
  const chain = certificates(certPem);
  if (!chain) return { ok: false, input: "certificate", reason: "not-pem" };
  if (!/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(keyPem)) return { ok: false, input: "key", reason: "not-pem" };
  let key;
  try {
    key = createPrivateKey(keyPem);
  } catch {
    return { ok: false, input: "key", reason: "not-pem" };
  }
  const leafKey = chain[0]!.publicKey.export({ format: "der", type: "spki" }).toString("hex");
  const pairKey = createPublicKey(key).export({ format: "der", type: "spki" }).toString("hex");
  if (leafKey !== pairKey) return { ok: false, input: "key", reason: "key-mismatch" };
  for (let i = 0; i + 1 < chain.length; i++) {
    if (!chain[i]!.verify(chain[i + 1]!.publicKey)) return { ok: false, input: "certificate", reason: "chain-broken" };
  }
  for (const c of chain) {
    if (now < Date.parse(c.validFrom)) return { ok: false, input: "certificate", reason: "not-yet-valid" };
    if (now > Date.parse(c.validTo)) return { ok: false, input: "certificate", reason: "expired" };
  }
  return { ok: true, chain };
}

/** An absolute http(s) URL with no userinfo — `config.CheckWebAddress`. */
export function isWebAddress(raw: string): boolean {
  try {
    const u = new URL(raw);
    return (u.protocol === "http:" || u.protocol === "https:") && !!u.host && !u.username && !u.password;
  } catch {
    return false;
  }
}

/** `config.CheckPortalPair`: https, and a name the leaf covers. */
export function portalPairFault(leaf: X509Certificate, portalUrl: string): "portal-url-not-https" | "name-not-covered" | null {
  if (!isWebAddress(portalUrl)) return "portal-url-not-https";
  const u = new URL(portalUrl);
  if (u.protocol !== "https:") return "portal-url-not-https";
  const host = u.hostname.replace(/^\[|\]$/g, "");
  const covered = /^[\d.:]+$/.test(host) ? leaf.checkIP(host) : leaf.checkHost(host);
  return covered ? null : "name-not-covered";
}

// ── the settings ───────────────────────────────────────────────────────────────

const urlSetting = (v: string | null, source: NetworkSettingSource) =>
  v ? { value: v, source } : { value: null, source: "unset" as const };

function effectivePortalUrl() {
  return state.portalUrl ?? BOOTSTRAP_PORTAL_URL;
}

function describe(c: StoredCert): Omit<PortalCertificateStatus, "state" | "source"> {
  const leaf = c.leaf;
  const names = (leaf.subjectAltName ?? "").split(/,\s*/).filter(Boolean);
  return {
    keySet: true,
    subject: leaf.subject.split("\n").join(", "),
    issuer: leaf.issuer.split("\n").join(", "),
    dnsNames: names.filter((n) => n.startsWith("DNS:")).map((n) => n.slice(4)),
    ipAddresses: names.filter((n) => n.startsWith("IP Address:")).map((n) => n.slice(11)),
    notBefore: new Date(leaf.validFrom).toISOString(),
    notAfter: new Date(leaf.validTo).toISOString(),
    fingerprintSha256: leaf.fingerprint256.replaceAll(":", "").toLowerCase(),
    chainLength: c.chain,
    updatedAt: c.updatedAt,
    updatedBy: c.updatedBy,
  };
}

export function portalCertificateStatus(): PortalCertificateStatus {
  const c = state.portalCert;
  if (!c) return { state: "none", source: "none", keySet: false };
  const st: PortalCertificateStatus = { state: "served", source: "console", ...describe(c) };
  if (Date.now() < Date.parse(c.leaf.validFrom)) return { ...st, state: "not-served", notServedReason: "not-yet-valid" };
  if (Date.now() > Date.parse(c.leaf.validTo)) return { ...st, state: "not-served", notServedReason: "expired" };
  const fault = portalPairFault(c.leaf, effectivePortalUrl());
  return fault ? { ...st, state: "not-served", notServedReason: fault } : st;
}

export function networkSettings(): NetworkSettings {
  return {
    portalUrl: state.portalUrl ? urlSetting(state.portalUrl, "console") : urlSetting(BOOTSTRAP_PORTAL_URL, "derived"),
    venueUrl: state.venueUrl ? urlSetting(state.venueUrl, "console") : urlSetting(null, "unset"),
    throttles: structuredClone(state.throttles ?? DEFAULT_THROTTLES),
    bodyCeilings: { ...(state.ceilings ?? DEFAULT_CEILINGS) },
    bounds: structuredClone(BOUNDS),
    defaults: { throttles: structuredClone(DEFAULT_THROTTLES), bodyCeilings: { ...DEFAULT_CEILINGS } },
    portalCertificate: portalCertificateStatus(),
    updatedAt: state.updatedAt,
    updatedBy: state.updatedBy,
  };
}

export function currentPortalLeaf(): X509Certificate | null {
  return state.portalCert?.leaf ?? null;
}

export function bootstrapPortalUrl(): string {
  return BOOTSTRAP_PORTAL_URL;
}

export function saveTransport(
  body: { portalUrl: string | null; venueUrl: string | null; throttles: SignInThrottles; bodyCeilings: BodyCeilings },
  actor: string,
): void {
  state.portalUrl = body.portalUrl;
  state.venueUrl = body.venueUrl;
  state.throttles = structuredClone(body.throttles);
  state.ceilings = { ...body.bodyCeilings };
  state.updatedAt = new Date().toISOString();
  state.updatedBy = actor;
}

export function putPortalCertificate(certPem: string, chain: X509Certificate[], portalUrl: string | null, actor: string) {
  state.portalCert = { pem: certPem, leaf: chain[0]!, chain: chain.length, updatedAt: new Date().toISOString(), updatedBy: actor };
  if (portalUrl !== null) state.portalUrl = portalUrl;
}

export function deletePortalCertificate() {
  state.portalCert = null;
}

// ── FreeRADIUS ─────────────────────────────────────────────────────────────────

export function radiusSettings(): RadiusServerSettings {
  const out: RadiusServerSettings = {
    maxServers: state.maxServers ?? MAX_SERVERS.default,
    maxServersBound: { ...MAX_SERVERS },
    eapTlsEnabled: state.eapTls,
    eapCertificate: { keySet: false },
    /* The shape `deploy/` and `lab/` ship: the control plane
       writes the tree into the handoff directory and the supervisor in the
       FreeRADIUS image has announced itself (internal/raddb/handoff.go). A Go
       process started with no FOYER_RADDB_TARGET still answers `false`, and says
       so; that is a developer's `go run`, not an installation. */
    applyAvailable: true,
    pendingApply: false,
  };
  if (state.eap) {
    const d = describe(state.eap);
    const ca = state.eap.caPem ? certificates(state.eap.caPem)?.[0] : undefined;
    out.eapCertificate = {
      keySet: true,
      subject: d.subject,
      issuer: d.issuer,
      notAfter: d.notAfter,
      fingerprintSha256: d.fingerprintSha256,
      updatedAt: d.updatedAt,
      ...(ca ? { caSubject: ca.subject.split("\n").join(", ") } : {}),
    };
  }
  if (state.lastSync) out.lastSync = structuredClone(state.lastSync);
  if (state.lastApply) out.lastApply = structuredClone(state.lastApply);
  if (state.radiusChangedAt) {
    out.pendingApply =
      !state.lastApply || state.lastApply.outcome !== "applied" || state.lastApply.at < state.radiusChangedAt;
  }
  return out;
}

export function hasEapCertificate(): boolean {
  return state.eap !== null;
}

export function saveRadius(maxServers: number, eapTls: boolean) {
  state.maxServers = maxServers;
  state.eapTls = eapTls;
  state.radiusChangedAt = new Date().toISOString();
}

export function putEapCertificate(certPem: string, chain: X509Certificate[], caPem: string, actor: string) {
  state.eap = { pem: certPem, leaf: chain[0]!, chain: chain.length, caPem, updatedAt: new Date().toISOString(), updatedBy: actor };
  state.radiusChangedAt = new Date().toISOString();
}

/**
 * One sync of the client table. The oracle has no startup snapshot, so no class.
 *
 * `skipped` is every enabled device with no shared secret, with the sentence the Go
 * store writes (`internal/store/nassync.go`): FreeRADIUS discards its packets until
 * one is assigned, and a sync that said nothing about it would read as complete.
 */
export function syncClients(
  trigger: RadiusSyncTrigger,
  projected: number,
  actor: string | null,
  unkeyed: readonly string[] = [],
): RadiusClientSync {
  const rec: RadiusClientSync = {
    at: new Date().toISOString(),
    trigger,
    outcome: "synced",
    inserted: 0,
    updated: 0,
    unchanged: projected,
    removed: [],
    skipped: unkeyed.map((nasName) => ({
      nasName,
      reason: "no shared secret assigned; FreeRADIUS discards every packet from this address until one is",
    })),
    reasons: [
      "This deployment records no startup snapshot, so whether FreeRADIUS needs a restart to see this is not known.",
    ],
    ...(actor ? { actorUsername: actor } : {}),
  };
  state.lastSync = rec;
  return structuredClone(rec);
}

/**
 * The supervisor's answer, as a deployment gives it. An operator's apply always
 * restarts (it may be asking FreeRADIUS to re-read its client table), so
 * `restarted` is true; the mock has no FreeRADIUS to refuse the tree, so the
 * refused branch — `failed` with the supervisor's `detail` — is the Go side's
 * and `web/admin`'s tests' to drive.
 */
export function applyRadius(actor: string | null): RadiusApply {
  const rec: RadiusApply = {
    at: new Date().toISOString(),
    outcome: "applied",
    restarted: true,
    ...(actor ? { actorUsername: actor } : {}),
  };
  state.lastApply = rec;
  return structuredClone(rec);
}

// ── CoA timing ─────────────────────────────────────────────────────────────────

export const speaksCoA = (driver: NasDriverKind) => driver === "mikrotik" || driver === "coa";

export function coaTiming(n: { id: string; siteId: string; name: string; driver: NasDriverKind }): NasCoaTiming {
  const stored = state.coa.get(n.id);
  return {
    nasId: n.id,
    siteId: n.siteId,
    name: n.name,
    driver: n.driver,
    timeoutMs: stored?.timeoutMs ?? COA_DEFAULTS.timeoutMs,
    retries: stored?.retries ?? COA_DEFAULTS.retries,
    source: stored ? "console" : "default",
  };
}

export function setCoaTiming(nasId: string, v: CoaTimingValues | null) {
  if (v) state.coa.set(nasId, { ...v });
  else state.coa.delete(nasId);
}

/** The sign-in buckets in force — what `internal/api` reshapes its limiters to. */
export function throttlesInForce(): SignInThrottles {
  return state.throttles ?? DEFAULT_THROTTLES;
}

/** The body ceilings in force, read per request as the Go listeners read them. */
export function ceilingsInForce(): BodyCeilings {
  return state.ceilings ?? DEFAULT_CEILINGS;
}
