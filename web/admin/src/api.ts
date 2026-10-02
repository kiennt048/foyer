import type {
  Account,
  Alarm,
  AuditEntry,
  AuthAttempt,
  AuthBlindSpot,
  AuthSession,
  Controller,
  ControllerCreate,
  ControllerProbe,
  ControllerUpdate,
  DataSubjectErasure,
  DataSubjectErasureRequest,
  GuestDataSettings,
  GuestDataSettingsUpdate,
  MarketingConsent,
  MarketingConsentState,
  DiscoveryResult,
  DriverEvidence,
  Failure,
  FieldFault,
  GardenEntry,
  GardenEntryCreate,
  HeroImage,
  HeroImageUpload,
  MacCoverage,
  Nas,
  NasProbe,
  Operator,
  OperatorRequest,
  OperatorUpdate,
  Overview,
  TrendGranularity,
  Trends,
  PasswordChange,
  PhoneSignIn,
  PhoneSignInUpdate,
  PortalSettings,
  PortalSettingsUpdate,
  Problem,
  RetentionPolicy,
  RetentionPolicyUpdate,
  MaintenanceSettings,
  MaintenanceSettingsUpdate,
  GuestWifiKeyStatus,
  VoucherSlipWifi,
  Session,
  SessionDisconnectRequest,
  SessionDisconnectResult,
  SignInRequest,
  Site,
  SiteCreate,
  Voucher,
  VoucherBatch,
  WalledGarden,
} from "@foyer/contract";

const BASE = "/api/v0";

/**
 * An API error carrying an RFC 9457 problem document.
 *
 * **It carries no copy.** `problem.type` is the discriminator and `admin/src/problems.ts`
 * owns every sentence; this layer's whole job is to make sure a `type` is always
 * there to key on. `detail` is deliberately not exposed: it is the server's prose,
 * a conforming server omits it, and a component reaching for it is a component that
 * renders the server's language on the failures an operator most needs to read.
 */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly problem: Problem | null,
  ) {
    super(problem?.title ?? `HTTP ${status}`);
    this.name = "ApiError";
  }

  /** Field name to the server's prose. **The keys are the load-bearing half.** */
  get fieldErrors(): Record<string, string> {
    return (this.problem?.errors ?? {}) as Record<string, string>;
  }

  /** Field name to *why*, which is what a client with its own copy renders from. */
  get fieldFaults(): Record<string, FieldFault> {
    const fields = (this.problem?.params as { fields?: unknown } | undefined)?.fields;
    return (fields ?? {}) as Record<string, FieldFault>;
  }
}

/**
 * The refusal no server can send, and the reason this layer classifies at all.
 *
 * `fetch` rejects with a bare `TypeError` when the request never arrived — DNS
 * failed, the radio dropped, the server is not running. That is not an `ApiError`,
 * so without this every `instanceof ApiError` in the console would fall through to
 * `String(error)` and put *"TypeError: Failed to fetch"* on an operator's screen.
 * The contract has a value for it — `/problems/unreachable`, marked
 * `x-client-synthesised` because no server can produce it — so the classification
 * happens once, here, and the surfaces above only ever see a type they have copy for.
 */
const UNREACHABLE: Problem = {
  type: "/problems/unreachable",
  /* The one place a `title` is written by this client rather than read from a
     server. It is never rendered — `problems.ts` has a catalogue entry for this
     type — and exists so `Problem` is a whole document rather than a partial one. */
  title: "unreachable",
  status: 0,
};

/**
 * The CSRF token, held here because this is the layer that writes headers.
 *
 * The session cookie is `httpOnly`, so the client cannot read it and cannot echo it;
 * the token arrives in `AuthSession.csrfToken` instead and goes back in
 * `X-Foyer-CSRF` on every request (the server checks it on writes and exports). `auth.tsx` is what puts it here,
 * because it is what knows when a session begins and ends.
 *
 * **`web/mock` issues this and does not check it**, which is deliberate on both
 * sides: the check is the Go binary's, like argon2id and the refuse-to-start-on-
 * default behaviour. The console sends it anyway — a client that only starts sending
 * a header once a server starts requiring it is a client nobody has ever run against
 * a server that requires it.
 */
let csrfToken: string | null = null;
export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

/**
 * What the console does when the server says *you are not signed in*.
 *
 * **Routing, not rendering** — `getAuthSession`'s own description says the `401` is a
 * normal answer rather than a fault, and `problems.ts` already classifies the type.
 * This is the other half: the answer to a refusal of work the operator asked for is
 * to put them in front of a sign-in form, and every query and mutation in the console
 * can now produce one. Registered once, by `auth.tsx`, because a route change needs
 * the router and this module is not a component.
 *
 * The boot probe passes `quiet` and is deliberately exempt. *Nobody is signed in* is
 * the state every console starts in, and treating it as a refusal would send an
 * operator to the sign-in screen from a question they never asked.
 */
let signedOut: (() => void) | null = null;
export function whenSignedOut(handler: () => void): () => void {
  signedOut = handler;
  return () => {
    if (signedOut === handler) signedOut = null;
  };
}

/* Exported so a lazily loaded panel can make its own calls without adding a method
   to `api`, which is in the entry chunk every operator downloads first. */
export async function request<T>(path: string, init?: RequestInit, quiet?: boolean): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, {
      ...init,
      headers: {
        ...(init?.body ? { "content-type": "application/json" } : {}),
        /* On every request once signed in. The scheme requires it on each write and on
           each export (a GET that hands a document out); sending it on a read too
           costs nothing and keeps the rule out of this client. */
        ...(csrfToken ? { "X-Foyer-CSRF": csrfToken } : {}),
        ...init?.headers,
      },
    });
  } catch {
    /* No response at all. `status` is 0 because there is no status, which is also
       what tells a caller apart from a `4xx` it might want to branch on. */
    throw new ApiError(0, UNREACHABLE);
  }

  if (!res.ok) {
    let problem: Problem | null = null;
    try {
      problem = (await res.json()) as Problem;
    } catch {
      /* A body that is not a problem document is still an error, and it still needs
         a type — an unparseable 500 and a crashed server read the same to an
         operator, and both are "the server failed on its own". */
    }
    const error = new ApiError(
      res.status,
      problem?.type
        ? problem
        : {
            /* A bodyless `404` is still *gone*, and a surface that treats gone
               differently from failed must be able to tell. Everything else with no
               readable body is the server having failed on its own. */
            type: res.status === 404 ? "/problems/not-found" : "/problems/internal",
            title: res.statusText,
            status: res.status,
          },
    );
    if (!quiet && error.problem?.type === "/problems/not-signed-in") signedOut?.();
    throw error;
  }

  if (res.status === 204) return undefined as T;
  /* An export is a document, not JSON. */
  if (res.headers.get("content-type")?.startsWith("text/csv")) return (await res.blob()) as T;
  return (await res.json()) as T;
}

/** Cursor pagination. Deliberately no total — see `listSessions` in the contract. */
export interface Page<T> {
  items: T[];
  nextCursor: string | null;
}

const qs = (params: Record<string, string | number | undefined | null | string[]>) => {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue;
    sp.set(k, Array.isArray(v) ? v.join(",") : String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
};

export const api = {
  sites: () => request<Site[]>("/sites"),

  /**
   * The first venue, and the only call in this client that can succeed exactly once.
   *
   * `createSite` refuses the second venue `409 /problems/one-site-per-install`
   * (`web/contract/openapi.yaml`), so on a shipped deployment this
   * is dead the moment it has been used. That is why its only caller is the gate that
   * renders when there is no venue — see `screens/CreateVenue.tsx`.
   *
   * `SiteCreate` accepts `name` and nothing else on purpose: an operator-chosen `id`
   * would be a permanent identifier typed once into a first-run form, and
   * `environment` has no screen that sets it. Typed as `SiteCreate` rather than
   * `Record<string, unknown>` so that stays true by compilation.
   */
  createSite: (body: SiteCreate) =>
    request<Site>("/sites", { method: "POST", body: JSON.stringify(body) }),

  overview: (siteId: string) => request<Overview>(`/overview${qs({ siteId })}`),
  alarms: (siteId: string) => request<Alarm[]>(`/alarms${qs({ siteId })}`),
  trends: (p: { siteId: string; from: string; to: string; granularity: TrendGranularity }) =>
    request<Trends>(`/trends${qs(p)}`),
  /* The document, as a Blob: `request` answers `text/csv` with one. */
  exportCsv: (dataset: string, p: { siteId: string; from: string; to: string }) =>
    request<Blob>(`/exports/${dataset}${qs(p)}`),

  listControllers: (siteId: string) => request<Controller[]>(`/controllers${qs({ siteId })}`),
  getController: (id: string) => request<Controller>(`/controllers/${id}`),
  createController: (body: ControllerCreate) =>
    request<Controller>("/controllers", { method: "POST", body: JSON.stringify(body) }),
  updateController: (id: string, body: ControllerUpdate) =>
    request<Controller>(`/controllers/${id}`, { method: "PATCH", body: JSON.stringify(body) }),
  deleteController: (id: string) => request<void>(`/controllers/${id}`, { method: "DELETE" }),
  probeController: (id: string) =>
    request<ControllerProbe>(`/controllers/${id}/probe`, { method: "POST" }),
  discoverFromController: (id: string, body: { siteId?: string; vendorSiteId?: string }) =>
    request<DiscoveryResult>(`/controllers/${id}/discover`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  listNas: (p: { siteId: string; q?: string; cursor?: string; limit?: number }) =>
    request<Page<Nas>>(`/nas${qs(p)}`),
  getNas: (id: string) => request<Nas>(`/nas/${id}`),
  createNas: (body: Record<string, unknown>) =>
    request<{ nas: Nas; sharedSecret: string }>("/nas", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  updateNas: (id: string, body: { name?: string; enabled?: boolean }) =>
    request<Nas>(`/nas/${id}`, { method: "PATCH", body: JSON.stringify(body) }),
  deleteNas: (id: string) => request<void>(`/nas/${id}`, { method: "DELETE" }),
  probeNas: (id: string) => request<NasProbe>(`/nas/${id}/probe`, { method: "POST" }),
  /* How this device's claims are known.
     A GET, and separate from `probeNas`, because nothing in it is established by a
     probe: the facts are properties of the driver and are identical before and
     after any packet is sent. */
  getNasEvidence: (id: string) => request<DriverEvidence>(`/nas/${id}/evidence`),
  rotateSecret: (id: string) =>
    request<{ sharedSecret: string; changeClass: string }>(`/nas/${id}/secret`, { method: "POST" }),

  listBatches: (p: { siteId: string; cursor?: string; limit?: number }) =>
    request<Page<VoucherBatch>>(`/voucher-batches${qs(p)}`),
  getBatch: (id: string) => request<VoucherBatch>(`/voucher-batches/${id}`),
  createBatch: (body: Record<string, unknown>) =>
    request<VoucherBatch>("/voucher-batches", { method: "POST", body: JSON.stringify(body) }),

  listVouchers: (p: {
    siteId: string;
    batchId?: string;
    state?: string[];
    q?: string;
    cursor?: string;
    limit?: number;
  }) => request<Page<Voucher>>(`/vouchers${qs(p)}`),
  /**
   * `failure`, not `driverError`.
   *
   * Revoking succeeds; ending the session the code was being used for is a separate
   * driver call that can fail on its own. A sentence the server composed would be in
   * the server's language, not the reader's — server-written copy inside the body of a
   * `200`, where neither of the catalogue's guards walks. `Failure` is the same `ProblemType` keyspace as a refusal,
   * so `problems.ts` is the one catalogue rather than two that drift.
   */
  revokeVoucher: (id: string) =>
    request<{ voucher: Voucher; sessionEnded: boolean; failure?: Failure }>(
      `/vouchers/${id}/revoke`,
      { method: "POST" },
    ),

  listSessions: (p: {
    siteId: string;
    state?: string[];
    nasId?: string;
    mac?: string;
    q?: string;
    cursor?: string;
    limit?: number;
  }) => request<Page<Session>>(`/sessions${qs(p)}`),
  getSession: (id: string) => request<Session>(`/sessions/${id}`),
  /* Not an edit of the record: the device ends the session, and its own Acct-Stop
     is what turns the row `ended`. Loaded only by the lazy disconnect control. */
  disconnectSession: (id: string, body: SessionDisconnectRequest) =>
    request<SessionDisconnectResult>(`/sessions/${id}/disconnect`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  /**
   * The envelope, not just the rows.
   *
   * `macCoverage` and `blindSpots` are what let an empty result say *no record*
   * rather than *nothing went wrong*, so the page type carries them and the screen
   * cannot render the table without having them in hand.
   */
  listAuthAttempts: (p: {
    siteId: string;
    q?: string;
    outcome?: string;
    source?: string;
    cursor?: string;
    limit?: number;
  }) =>
    request<Page<AuthAttempt> & { macCoverage: MacCoverage; blindSpots: AuthBlindSpot[] }>(
      `/auth-attempts${qs(p)}`,
    ),

  getPortalSettings: (siteId: string) =>
    request<PortalSettings>(`/sites/${siteId}/portal`),
  updatePortalSettings: (siteId: string, body: PortalSettingsUpdate) =>
    request<PortalSettings>(`/sites/${siteId}/portal`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),
  getPhoneSignIn: (siteId: string) => request<PhoneSignIn>(`/sites/${siteId}/portal/phone`),
  updatePhoneSignIn: (siteId: string, body: PhoneSignInUpdate) =>
    request<PhoneSignIn>(`/sites/${siteId}/portal/phone`, { method: "PUT", body: JSON.stringify(body) }),
  putHeroImage: (siteId: string, body: HeroImageUpload) =>
    request<HeroImage>(`/sites/${siteId}/portal/hero-image`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteHeroImage: (siteId: string) =>
    request<void>(`/sites/${siteId}/portal/hero-image`, { method: "DELETE" }),

  getWalledGarden: (siteId: string) =>
    request<WalledGarden>(`/sites/${siteId}/walled-garden`),
  createGardenEntry: (siteId: string, body: GardenEntryCreate) =>
    request<GardenEntry>(`/sites/${siteId}/walled-garden`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  deleteGardenEntry: (siteId: string, entryId: string) =>
    request<void>(`/sites/${siteId}/walled-garden/${entryId}`, { method: "DELETE" }),

  listAccounts: (p: { siteId: string; q?: string; cursor?: string; limit?: number }) =>
    request<Page<Account>>(`/accounts${qs(p)}`),
  createAccount: (body: Record<string, unknown>) =>
    request<Account>("/accounts", { method: "POST", body: JSON.stringify(body) }),
  updateAccount: (
    id: string,
    body: { displayName?: string; enabled?: boolean; password?: string; eapTtlsPap?: boolean },
  ) => request<Account>(`/accounts/${id}`, { method: "PATCH", body: JSON.stringify(body) }),
  deleteAccount: (id: string) => request<void>(`/accounts/${id}`, { method: "DELETE" }),

  /*
   * ── the console's own identity ─────────────────────────────────────────────
   *
   * **None of these has `session` in its name, and that is not a style choice.**
   * In this product *session* is the RADIUS noun — a guest attached to an access
   * point, a row of `radacct` — and `admin.test.ts` asserts that the only members of
   * this object matching it are `getSession` and `listSessions`, which is how the
   * read-only rule for FreeRADIUS's tables is held at the API layer. An operator's
   * sign-in is a different thing that English happens to give the same word, so it
   * is named for what it does: `signIn`, `signOut`, `me`.
   */

  /**
   * Who am I, and **`null` rather than a thrown error when the answer is nobody**.
   *
   * The `401` here is a normal answer — see `getAuthSession` — so the classification
   * happens once, at the only call site that can be sure of it, and every surface
   * above reads a session or the absence of one. A query that *rejected* on the
   * ordinary case would put a failed query in the cache on every cold boot, and each
   * screen would then have to know that one particular failure means nothing.
   *
   * `quiet`, because this is also the one `401` that must not route: it is the
   * question "is anybody signed in", not a refusal of work somebody asked for.
   */
  me: async (): Promise<AuthSession | null> => {
    try {
      return await request<AuthSession>("/auth/session", undefined, true);
    } catch (error) {
      if (error instanceof ApiError && error.problem?.type === "/problems/not-signed-in") {
        return null;
      }
      throw error;
    }
  },
  signIn: (body: SignInRequest) =>
    request<AuthSession>("/auth/session", { method: "POST", body: JSON.stringify(body) }),
  signOut: () => request<void>("/auth/session", { method: "DELETE" }),
  /** Your own, and there is no operation for anybody else's — see `OperatorUpdate`. */
  changePassword: (body: PasswordChange) =>
    request<void>("/auth/password", { method: "POST", body: JSON.stringify(body) }),

  /* No `siteId`: an operator is deployment-wide, and filtering them by the site
     switcher would invent a scope the object does not have. */
  listOperators: (p: { q?: string; cursor?: string; limit?: number }) =>
    request<Page<Operator>>(`/operators${qs(p)}`),
  createOperator: (body: OperatorRequest) =>
    request<Operator>("/operators", { method: "POST", body: JSON.stringify(body) }),
  updateOperator: (id: string, body: OperatorUpdate) =>
    request<Operator>(`/operators/${id}`, { method: "PATCH", body: JSON.stringify(body) }),
  deleteOperator: (id: string) => request<void>(`/operators/${id}`, { method: "DELETE" }),

  maintenanceSettings: () => request<MaintenanceSettings>("/privacy/maintenance"),
  updateMaintenanceSettings: (body: MaintenanceSettingsUpdate) =>
    request<MaintenanceSettings>("/privacy/maintenance", { method: "PUT", body: JSON.stringify(body) }),
  guestWifiKey: (siteId: string) => request<GuestWifiKeyStatus>(`/sites/${siteId}/guest-wifi-key`),
  updateGuestWifiKey: (siteId: string, passphrase: string | null) =>
    request<GuestWifiKeyStatus>(`/sites/${siteId}/guest-wifi-key`, {
      method: "PUT",
      body: JSON.stringify({ passphrase }),
    }),
  voucherSlipWifi: (siteId: string) => request<VoucherSlipWifi>(`/sites/${siteId}/voucher-slip-wifi`),
  retentionPolicy: () => request<RetentionPolicy>("/privacy/retention"),
  updateRetentionPolicy: (body: RetentionPolicyUpdate) =>
    request<RetentionPolicy>("/privacy/retention", { method: "PUT", body: JSON.stringify(body) }),
  eraseDataSubject: (body: DataSubjectErasureRequest) =>
    request<DataSubjectErasure>("/privacy/erasures", { method: "POST", body: JSON.stringify(body) }),

  /* Guest data and marketing consent. */
  guestData: (siteId: string) => request<GuestDataSettings>(`/sites/${siteId}/guest-data`),
  updateGuestData: (siteId: string, body: GuestDataSettingsUpdate) =>
    request<GuestDataSettings>(`/sites/${siteId}/guest-data`, { method: "PUT", body: JSON.stringify(body) }),
  listMarketingConsents: (siteId: string, p: { state?: MarketingConsentState; cursor?: string; limit?: number }) =>
    request<Page<MarketingConsent>>(`/sites/${siteId}/marketing-consents${qs(p)}`),
  withdrawMarketingConsent: (siteId: string, id: string) =>
    request<MarketingConsent>(`/sites/${siteId}/marketing-consents/${id}/withdraw`, { method: "POST" }),

  /* Read-only, forever, and there is nothing else here to write. The contract has no
     operation that could be added to this object — `contract/src/read-only.ts`. */
  listAudit: (p: { actorId?: string; from?: string; to?: string; cursor?: string; limit?: number }) =>
    request<Page<AuditEntry>>(`/audit${qs(p)}`),
};
