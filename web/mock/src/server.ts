/**
 * The mock API.
 *
 * Two things make this worth having rather than a folder of JSON files:
 * it is stateful, so the flows that matter can actually be walked; and every
 * response is validated against `openapi.yaml` before it leaves, so the prototype
 * cannot quietly diverge from the contract the backend will be built to.
 */
import { serve } from "@hono/node-server";
import type {
  Account,
  Alarm,
  AuditEntry,
  MaintenanceSettingsUpdate,
  AuthAttempt,
  Controller,
  ControllerCreate,
  DataSubjectErasureRequest,
  GuestDataSettingsUpdate,
  EapCertificateUpload,
  NasCoaTimingUpdate,
  NetworkSettingsUpdate,
  PortalCertificateUpload,
  RadiusServerSettingsUpdate,
  RadiusSyncTrigger,
  ControllerProbe,
  ControllerUpdate,
  DiscoveryResult,
  FaultParams,
  FaultWithParams,
  FieldFault,
  GardenDriverCoverage,
  GardenEntry,
  GardenEntryCreate,
  HeroImage,
  HeroImageUpload,
  Nas,
  Operator,
  OperatorRequest,
  OperatorUpdate,
  Overview,
  PasswordChange,
  Plan,
  PlanRequest,
  PortalContext,
  PortalSettings,
  PhoneAbuseControlsUpdate,
  PhoneSignInUpdate,
  PortalSettingsUpdate,
  Problem,
  ProblemType,
  ProbeWarning,
  RetentionPolicyUpdate,
  Session,
  Site,
  SiteCreate,
  Trends,
  Voucher,
  VoucherBatch,
} from "@foyer/contract";
import {
  DRIVER_SHAPES,
  IMAGE_MAX_BYTES,
  LOGO_MAX_BYTES,
  PASSWORD_MIN_LENGTH,
  dataUriBytes,
  designFault,
  localDate,
  narrowDesign,
  needsCoaVendor,
  normaliseVoucherCode,
  weakestGarden,
} from "@foyer/contract";
import {
  CONTROLLER_MINIMUM_VERSION,
  MAC_COVERAGE,
  UNVERIFIED_CAPABILITIES,
  VENDOR_BY_DRIVER,
  VENDOR_EVIDENCE,
  authBlindSpotsFor,
  controllerServesSite,
  defaultVenue,
  deriveAlarms,
  PLANS,
  VENUES,
  venueById,
  versionMeetsMinimum,
} from "@foyer/fixtures";
import type { Venue } from "@foyer/fixtures";
import { Hono } from "hono";
import type { Context } from "hono";
import { deleteCookie, getCookie, setCookie } from "hono/cookie";
import {
  DEVICE_COOKIE,
  bodyCeilingFor,
  issueDeviceCookie,
  setupOpen,
  throttleSetup,
  throttleSignIn,
} from "./auth.js";
import { hasOptionRequirements, optionRefusal, requirementFor } from "./permissions.js";
import {
  consentsBetween,
  guestDataFault,
  guestDataSettings,
  keepGuestInput,
  withVerifiedPhone,
  marketingConsents,
  portalDataCollection,
  putGuestDataSettings,
  readGuestForm,
  withdrawConsent,
  withdrawDevice,
} from "./guestdata.js";
import {
  BOUNDS,
  COA_BOUNDS,
  COA_DEFAULTS,
  MAX_SERVERS,
  applyRadius,
  bootstrapPortalUrl,
  checkPair,
  coaTiming,
  currentPortalLeaf,
  deletePortalCertificate,
  hasEapCertificate,
  isCertificatePem,
  isWebAddress,
  networkSettings,
  portalCertificateStatus,
  portalPairFault,
  putEapCertificate,
  putPortalCertificate,
  radiusSettings,
  saveRadius,
  saveTransport,
  setCoaTiming,
  speaksCoA,
  syncClients,
} from "./network.js";
import {
  DATASETS,
  countBucket,
  csvDocument,
  exportFilename,
  maxRangeDays,
  time as csvTime,
  trendBuckets,
} from "./reports.js";
import {
  checkPhoneCode,
  phoneAbuseControlsFor,
  phoneCodeMinutes,
  phoneOffered,
  phoneOutbox,
  phoneSignInFor,
  phoneSpendCap,
  sendPhoneCode,
  sendTestMessage,
  setPhoneAbuseControls,
  setPhoneSignIn,
  setPhoneSpendCap,
} from "./phone.js";
import { setSiteTimeZone, siteDay, siteTimeZone, timeZoneOf, validTimeZone } from "./sitetime.js";
import {
  guestWifiKeyStatus,
  maintenanceFaults,
  maintenanceSettings,
  setGuestWifiKey,
  setMaintenanceSettings,
  slipPassphrase,
  validWifiPassphrase,
} from "./settings.js";
import { LIVE, Simulation } from "./sim.js";
import { ContractViolation, conform, conformResponse, requestFault } from "./validate.js";
import {
  CREDENTIALS_EXPIRED_PREFIX,
  DEV_PASSWORD,
  DEV_PASSWORD_PINNED,
  LIVE_SESSION_WINDOW,
  UNREACHABLE_PREFIX,
  acceptTerms,
  beginAdminSession,
  createSetupAdministrator,
  SETUP_CODE,
  grantPhone,
  createNas,
  disconnectSession,
  disconnectPortalGrant,
  openPortalGrants,
  enabledAdministrators,
  endAdminSession,
  endSessionsFor,
  erasableMac,
  erasableEmail,
  erasablePhone,
  eraseSubject,
  forgetPortalClient,
  generateSecret,
  getAdminSession,
  getGrant,
  getPortalClient,
  grantsInSite,
  loginWithAccount,
  newPortalClient,
  normaliseMac,
  operatorById,
  mustChangePassword,
  operatorPassword,
  permissionsFor,
  probeNas,
  recordAudit,
  redeemVoucher,
  retentionPolicy,
  revokeVoucher,
  setAccountPassword,
  setOperatorPassword,
  setRetention,
  site,
  withCurrentFreshness,
  world,
} from "./state.js";
import type { NasClaim, PortalClient } from "./state.js";

const PORT = Number(process.env.PORT ?? 5170);
const PORTAL_ORIGIN = process.env.PORTAL_ORIGIN ?? "http://localhost:5171";
/**
 * The portal's session cookie, and the name is the contract's — see the `portal`
 * tag, which says that this cookie is the only carrier by which a client MAC
 * reaches any of the five portal operations.
 *
 * The name comes from the tag rather than from either server. Two servers
 * implementing a carrier the document does not describe pick two different names,
 * which is the same shape as every other second copy in this repository: nothing is
 * red, and nothing would be.
 */
const CLIENT_COOKIE = "foyer_portal_session";
/**
 * The admin console's session cookie — the contract's name, minus its `__Host-`
 * prefix on a transport that cannot carry one.
 *
 * `components.securitySchemes.sessionCookie.name` is `__Host-foyer_admin_session`
 * The prefix is not decoration and it is not an attribute: a
 * user agent refuses a cookie so named unless it is `Secure`, `Path=/` and carries
 * no `Domain`. So it **means** `Secure`, and this server already declares exactly
 * one deviation about `Secure` — it serves plain HTTP on localhost, where a
 * `secure` cookie is one the browser discards and a dev server that cannot hold a
 * session teaches nothing.
 *
 * **One condition, both consequences, computed in one place.** Two independent
 * expressions of the same fact is how a server ends up sending a `__Host-` name it
 * has just stripped the `Secure` attribute off — a cookie every browser drops, from
 * a mock that looks like it is doing the right thing. `adminCookie` is that place;
 * nothing else in this file names the cookie.
 */
const ADMIN_COOKIE = "foyer_admin_session";

/**
 * The name to set and read on this request, and the `secure` attribute that goes
 * with it. See `ADMIN_COOKIE`.
 *
 * The Go server has no branch here and must not grow one: it sets
 * `__Host-foyer_admin_session`, `Secure`, always. Browsers treat
 * `http://localhost` as a secure context, so that binary still works against a
 * development listener without a second, weaker code path — and a second code path
 * is how a deployment ships the weaker one. This mock is not that binary; it is the
 * console's dev server, reached over `http://localhost:5170` from a Vite proxy on
 * another port, and it says so here rather than in a deployment note.
 */
function adminCookie(c: Context): { name: string; secure: boolean } {
  const secure = new URL(c.req.url).protocol === "https:";
  return { name: secure ? `__Host-${ADMIN_COOKIE}` : ADMIN_COOKIE, secure };
}

const VENUE_COOKIE = "foyer_mock_venue";

const app = new Hono();
const api = new Hono();

/**
 * The clock that makes the world move. See `sim.ts` for the schedule and for why
 * the compressed cadence is announced rather than hidden.
 */
const sim = new Simulation(world);

// ── plumbing ─────────────────────────────────────────────────────────────────

app.use("*", async (c, next) => {
  // The admin dev server runs on its own origin; the portal is proxied same-origin.
  const origin = c.req.header("Origin");
  if (origin) {
    c.header("Access-Control-Allow-Origin", origin);
    c.header("Access-Control-Allow-Credentials", "true");
    c.header("Access-Control-Allow-Headers", "content-type, x-foyer-mock-fault");
    c.header("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS");
  }
  // The console log is only seen by whoever started the mock. Anyone reading the
  // running app — or a HAR from it — is told the cadence is compressed too, because
  // mistaking the demo rate for the 300-second floor is the one way this clock can
  // lie. Not part of the contract; a header, not a body field.
  c.header("X-Foyer-Mock-Clock", LIVE ? sim.header() : "frozen (FOYER_MOCK_LIVE=0)");
  if (c.req.method === "OPTIONS") return c.body(null, 204);
  await next();
});

/**
 * The mock has broken, which is a different kind of thing from the operator having
 * asked for something impossible.
 *
 * **This is the one place the mock composes a sentence**, and the exception is
 * narrow on purpose: `detail` here is addressed to whoever is running the mock, not
 * to an operator. It says which invariant of *this file* failed, in the same
 * terminal that started it. Nothing a product client renders comes through here —
 * a console meeting `/problems/internal` shows its own copy for "something went
 * wrong", which is why the injected fault below carries no `detail` at all.
 *
 * Built by hand rather than through `problem()`: `conform()` throws, and a throw
 * inside `onError` is a crash with no answer at all. The shape is asserted from
 * outside instead — see `server.test.ts`.
 */
function internalProblem(diagnostic: string): Problem {
  return {
    type: "/problems/internal",
    title: "The mock failed",
    status: 500,
    detail: diagnostic,
  };
}

app.onError((err, c) => {
  const body =
    err instanceof ContractViolation
      ? internalProblem(err.message)
      : internalProblem(String(err));
  if (err instanceof ContractViolation) {
    // Loud on purpose. A contract violation that only warns is one nobody sees.
    console.error(`\n  ✗ CONTRACT VIOLATION  ${c.req.method} ${c.req.path}\n    ${err.message}\n`);
  } else {
    console.error(err);
  }
  return c.body(JSON.stringify(body), 500, { "content-type": "application/problem+json" });
});

/**
 * The statuses this mock answers with an RFC 9457 problem document.
 *
 * `500` is here because `/problems/internal` is in `ProblemType`, and a value in
 * that enum that nothing can emit is a catalogue entry no client can ever be shown
 * to render correctly — the vacuous-check shape. See `X-Foyer-Mock-Fault` below for what emits it.
 */
type ProblemStatus = 401 | 403 | 404 | 409 | 413 | 422 | 429 | 500;

/**
 * An error answer: the body checked against `Problem`, sent under the media type
 * the contract declares for it.
 *
 * `c.json()` sends `application/json`. Every 4xx in `openapi.yaml` is declared
 * under `application/problem+json`, and the runtime validator compares *bodies* to
 * schemas — it cannot see a media type. So every error the mock has ever sent went
 * out under the wrong content type with nothing in the stack able to notice, which
 * is the same class of blind spot `terms.url` was. RFC 9457 exists so a client can
 * tell a problem document from a payload without guessing, and the Go backend is
 * meant to implement this contract rather than to copy this file.
 */
function problem(c: Context, status: ProblemStatus, body: Problem) {
  return c.body(JSON.stringify(conform("Problem", body)), status, {
    "content-type": "application/problem+json",
  });
}

function notFound(c: Context) {
  return problem(c, 404, {
    type: "/problems/not-found",
    title: "Not found",
    status: 404,
  });
}

/**
 * **A normal answer, not a fault.** The console's response to it is to route to the
 * sign-in screen, and nothing logs it — see `getAuthSession` in `openapi.yaml`.
 *
 * One call site, deliberately: the middleware below will answer with it once the
 * enforcement switch is thrown, and a second literal would be a second title for one
 * refusal. `server.test.ts` reads the titles out of this file to compute what must be
 * driven, so a refusal that exists twice would have to be driven twice to describe
 * the same thing.
 */
function notSignedIn(c: Context) {
  return problem(c, 401, {
    type: "/problems/not-signed-in",
    title: "Not signed in",
    status: 401,
  });
}

/** A session, and a role that may not do this. Signing in again does not help. */
function forbidden(c: Context, required: string) {
  return problem(c, 403, {
    type: "/problems/forbidden",
    title: "Not permitted",
    status: 403,
    /* The permission, not a sentence about it. A console that knows the operator's
       permission set can say something specific; one that does not renders its
       generic copy. Either way the server does not compose the sentence. */
    params: { required },
  });
}

/**
 * The two halves of a field refusal, collected together so they cannot drift apart.
 *
 * `params.fields` carries the **kind** — what a client with copy renders from — and
 * `errors` carries the **prose**, which is the fallback for a `(type, field, fault)`
 * triple that client has never heard of. They must have the same keys: a fault with
 * no `errors` entry is a field the client cannot describe at all, and an `errors`
 * entry with no fault is a field it can only describe in the server's language.
 *
 * Nothing but discipline was keeping two separate object literals in step, on the
 * two handlers that refuse more than one field at a time. There is a guard on the
 * key equality in `server.test.ts` as well, because this helper is only used where
 * somebody remembered to use it.
 *
 * ── the third half: the figure in the sentence ───────────────────────────────
 *
 * `params.fieldParams` carries the **facts** a client interpolates into its own copy
 * for that one field. It was missing, and the way that showed up is the reason the
 * argument type below is conditional rather than optional: a five-character password
 * was refused and the console rendered *"At least 0."* — the copy read
 * `params.minimumCount`, this helper had nowhere to put one, and nothing anywhere
 * failed. Making it optional would leave the next fault with a figure in exactly that
 * position.
 *
 * So `add` takes the figure as a **rest parameter typed from the contract**:
 * `FAULT_PARAMS` says `below-minimum` needs `minimumCount`, and `Figures<F>` turns
 * that into a required argument for that fault and into *no argument at all* for the
 * eleven faults whose sentence is complete on its own. Refusing a field
 * `below-minimum` without the number does not compile.
 */

/**
 * The figure argument for one fault: required where the contract names params for it,
 * absent where it does not.
 *
 * A tuple type rather than an optional parameter, because `arg?: T` still accepts the
 * call with the argument left off — which is the exact defect. `[FaultParams[F]]`
 * makes it mandatory; `[]` makes passing one an error, so a fault that takes no
 * figure cannot quietly acquire one that no client reads.
 */
type Figures<F extends FieldFault> = F extends FaultWithParams ? [FaultParams[F]] : [];

function fieldRefusals() {
  const fields: Record<string, FieldFault> = {};
  const errors: Record<string, string> = {};
  const fieldParams: Record<string, Record<string, unknown>> = {};
  return {
    add<F extends FieldFault>(field: string, fault: F, message: string, ...figures: Figures<F>) {
      fields[field] = fault;
      errors[field] = message;
      if (figures.length) fieldParams[field] = figures[0] as Record<string, unknown>;
    },
    any: () => Object.keys(errors).length > 0,
    /** Spread into a `Problem`, so a caller cannot take one half and forget the other. */
    problem: () => ({
      params: {
        fields,
        /* Omitted rather than sent empty. Every refusal this helper builds would
           otherwise carry `fieldParams: {}`, which reads as "asked and answered
           nothing" on the fourteen refusals where no fault names a figure. */
        ...(Object.keys(fieldParams).length ? { fieldParams } : {}),
      },
      errors,
    }),
  };
}

// ── fault injection ──────────────────────────────────────────────────────────

/**
 * `X-Foyer-Mock-Fault: internal` — the failure no honest request can produce.
 *
 * The third affordance of its kind, and deliberately the same shape as the two that
 * already exist: `FOYER_MOCK_LIVE=0` freezes the clock, `?present=0` produces a
 * client the driver will not confirm, and this makes the server fail. Each one
 * exists so a path is **reviewable in the running product** rather than only
 * asserted in a test — the console's own 500 handling is otherwise a branch nobody
 * has ever seen render.
 *
 * ── why it accepts exactly one value ─────────────────────────────────────────
 *
 * A header that could produce any `ProblemType` would make the drivability sweep in
 * `server.test.ts` vacuous: every value would be "reachable" through the same
 * injector, which proves nothing about whether the server has a branch that emits
 * it. So this accepts the one value the server has no genuine branch for.
 * `/problems/session-end-failed` is *not* here even though it is equally hard to
 * reach, because it does have a real branch — `revokeVoucher` in `state.ts` — and
 * faking it would leave that branch never executed, which is the failure this file's
 * header comment was written about.
 *
 * The injected problem carries **no `detail`**. The crash path in `onError` does,
 * because that one is the mock talking to its own developer; this one is standing in
 * for a product answer and must have the shape a product answer has.
 */
const INJECTABLE_FAULTS: Record<string, ProblemType> = {
  internal: "/problems/internal",
};

api.use("*", async (c, next) => {
  const requested = c.req.header("X-Foyer-Mock-Fault");
  if (!requested) return next();

  const type = INJECTABLE_FAULTS[requested];
  if (!type) {
    /* No `errors`, because `errors` is keyed by *field* and this is a header — and
       no `params.fields` for the same reason. The refusal names what it will accept
       in `params` so a reader who typed the wrong word is told which words exist. */
    return problem(c, 422, {
      type: "/problems/validation",
      title: "No such injectable fault",
      status: 422,
      params: { header: "X-Foyer-Mock-Fault", injectable: Object.keys(INJECTABLE_FAULTS) },
    });
  }
  return problem(c, 500, { type, title: "Something went wrong", status: 500 });
});


// ── the console's own session, and the switch that is not thrown yet ──────────

/**
 * **Authentication is not enforced, and that is a decision with a date on it.**
 *
 * The admin console implements none of this yet: it has no sign-in screen, no session
 * and no `401` handling in its data layer. A mock that started demanding a cookie
 * would turn every screen and every end-to-end test red for a reason that has nothing
 * to do with what they assert. So every operation stays reachable with no session, as
 * it is today.
 *
 * ── what the switch is ───────────────────────────────────────────────────────
 *
 * Exactly this constant. Set it to `true` and the middleware below answers
 * `/problems/not-signed-in` for every operation whose `x-permission` is not `public`.
 * Nothing else in this file changes: the requirement lookup, the role gate, the
 * session store, the cookie and the CSRF token are all live already.
 *
 * It is safe to throw once the console (a) calls `GET /auth/session` on boot,
 * (b) routes to a sign-in screen on `401` rather than rendering an error, and
 * (c) the end-to-end suite signs in before it browses. Until all three are true,
 * throwing it converts one missing feature into two hundred failures.
 *
 * ── what is *already* enforced, and why that is not the same thing ───────────
 *
 * **The role gate is live whenever a session exists.** Sign in as staff and
 * `GET /operators` answers `403`. That is not authentication enforcement — nothing is
 * required to have a session — and it costs nothing today because nothing signs in.
 * It is here because `/problems/forbidden` has to be *drivable*: a refusal no request
 * can reach is a catalogue entry whose copy has never been looked at. It also means the console's role
 * gating can be built against a server that really refuses, rather than against one
 * that will start refusing later.
 *
 * **CSRF is issued and not checked.** `AuthSession.csrfToken` is real and rotates per
 * session; the mock does not require the `X-Foyer-CSRF` header, because requiring it
 * would break the same clients this switch protects, and because the check itself is
 * the Go binary's — like argon2id and the refuse-to-start-on-default behaviour, it is
 * a contract obligation this prototype states rather than performs.
 *
 * **The sign-in password IS checked.** A mock that documented its CSRF gap while
 * `beginAdminSession` matched on username and `enabled`, ignored the password
 * entirely, and said so **nowhere** would be a defect rather than a limitation: any
 * non-empty string would sign anybody in — through every review and every run of the
 * differential harness, which would have to file it as a declared divergence against
 * `signIn`'s `401`.
 *
 * What is checked and what is still not, stated narrowly so neither is read wide:
 *
 *   * The submitted password is compared, in constant time, against the one this
 *     operator has, and a mismatch is the same `401` an unknown username gets.
 *   * The comparison is **against the string**, not against argon2id. Verification
 *     *shape* is the Go binary's and stays in the paragraph above; what the mock owes
 *     is the *outcome* — right password in, wrong password out — because that is what
 *     the contract declares and what the console and the differ are built against.
 *   * There is still **no static credential**: the password is generated per process,
 *     as first boot generates the real one, and `FOYER_MOCK_ADMIN_PASSWORD` pins it
 *     for scripted drivers. See `DEV_PASSWORD` in `state.ts`.
 *   * It remains true that *nothing requires a session* — this switch is still
 *     `false`. Verifying a password and requiring a cookie are separate claims, and
 *     conflating them is how the first one would get read as the second.
 */
const REQUIRE_SESSION = false;

/** `/api/v0/nas/x` → `/nas/x`, which is what the contract's paths are written in. */
function contractPath(c: Context): string {
  return c.req.path.replace(/^\/api\/v0/, "");
}

function sessionOf(c: Context) {
  const session = getAdminSession(getCookie(c, adminCookie(c).name));
  if (!session) return undefined;
  const operator = operatorById(session.operatorId);
  /* The operator was deleted while the session lived. `endSessionsFor` normally
     prevents this; treating it as "no session" rather than crashing is the behaviour
     a real server needs anyway, because deletion and a request in flight can race. */
  return operator ? { session, operator } : undefined;
}

/**
 * **The body ceilings, before the gate** — `internal/api`'s `limitBody`, which
 * answers `413` before `401` because it discloses only that the listener reads no
 * more than that, true of every operation on it. Without them a console change to
 * the ceilings would reach Go and not here. Read per request from the settings in
 * force (`network.ts`), as the Go listeners read them.
 *
 * A declared `Content-Length` is judged without reading; a streamed body is read
 * whole and measured, which a mock can afford and a server may not.
 */
api.use("*", async (c, next) => {
  const method = c.req.method;
  if (method === "GET" || method === "HEAD" || method === "DELETE") return next();
  const path = contractPath(c);
  const limit = bodyCeilingFor({
    portal: path.startsWith("/portal/"),
    isPublic: requirementFor(method, path) === "public",
    /* While `REQUIRE_SESSION` is off this mock serves a caller with no session as
       an operator everywhere else, so it does here too; the anonymous rule then binds
       where Go binds it regardless of who asks — public operations and the portal. */
    signedIn: sessionOf(c) !== undefined || !REQUIRE_SESSION,
    heroImage: /^\/sites\/[^/]+\/portal\/hero-image$/.test(path),
    imageMaxBytes: IMAGE_MAX_BYTES,
  });
  const declared = c.req.header("content-length");
  const bytes =
    declared !== undefined ? Number(declared) : (await c.req.arrayBuffer()).byteLength;
  if (bytes > limit) {
    c.header("Connection", "close");
    return problem(c, 413, {
      type: "/problems/too-large",
      title: "Request body too large",
      status: 413,
      params: {
        fields: { body: "too-large" },
        fieldParams: { body: { bytes, limitBytes: limit } },
        bytes,
        limitBytes: limit,
      },
      errors: { body: "Too large." },
    });
  }
  return next();
});

/**
 * The gate, keyed on what **`openapi.yaml` says** each operation requires rather than
 * on a list kept here. See `permissions.ts` for why that distinction is the point.
 */
api.use("*", async (c, next) => {
  const required = requirementFor(c.req.method, contractPath(c));
  /* `undefined` is a path the contract does not declare: let the router answer 404
     rather than inventing a permission for a thing that does not exist. */
  if (required === undefined || required === "public") return next();

  const current = sessionOf(c);
  if (!current) {
    if (!REQUIRE_SESSION) return next(); // ← the switch, and the whole of it
    return notSignedIn(c);
  }
  if (required !== "authenticated" && !permissionsFor(current.operator).includes(required)) {
    return forbidden(c, required);
  }
  /* A body option with a permission of its own (`x-permission` on the property):
     `disconnectSession`'s `revokeVoucher` needs `vouchers:manage` as well. Refused
     here, before the handler reads anything, as the Go handler refuses it first.
     Hono caches the parsed body, so the handler's own read is unaffected. */
  if (hasOptionRequirements(c.req.method, contractPath(c))) {
    const body = await c.req.json().catch(() => null);
    const missing = optionRefusal(c.req.method, contractPath(c), body, permissionsFor(current.operator));
    if (missing) return forbidden(c, missing);
  }

  /*
   * **The forced password change, and it is in the gate rather than in a client.**
   *
   * An operator on a temporary password somebody else set (`foyerctl create-operator
   * --must-change-password`) chooses their own before anything else. A console that
   * only *routes* to that screen has forced nothing — a second browser, a stale tab or `curl` with the session
   * cookie walks straight past it — so the refusal is here, where every operation
   * passes, and `internal/api`'s gate carries the identical rule.
   *
   * Three exemptions and each is load-bearing: `changeOwnPassword` is the way out,
   * `getAuthSession` is how the console learns it must go there, and `signOut` is how
   * somebody who cannot change it leaves. Reads are refused too — a read-only console
   * on a password somebody else knows is still a console somebody else may be reading.
   */
  if (mustChangePassword(current.operator.id) && !passwordChangeExempt(c)) {
    return problem(c, 403, {
      type: "/problems/password-change-required",
      title: "Choose your own password",
      status: 403,
    });
  }
  return next();
});

/**
 * What an operator on a temporary password may still reach.
 *
 * Matched on the contract path and method, the same pair `requirementFor` keys on, so
 * the exemption cannot address an operation the gate above addresses differently.
 */
function passwordChangeExempt(c: Context): boolean {
  const path = contractPath(c);
  const method = c.req.method;
  if (path === "/auth/password" && method === "POST") return true;
  if (path === "/auth/session" && (method === "GET" || method === "DELETE")) return true;
  return false;
}

/**
 * Record one change, **only when somebody is signed in**.
 *
 * Not a shortcut around the switch above: an audit entry is a claim about *who*, and
 * with no session there is no who. `actorId` and `actorUsername` are required members
 * and there is no honest value for either — attributing an unauthenticated write to
 * the fixture administrator would put a name in a log next to something they did not
 * do, which is worse than the absence.
 *
 * When the switch is thrown this branch becomes unreachable rather than wrong: an
 * unauthenticated mutation will not have got this far.
 */
function audit(
  c: Context,
  entry: Omit<AuditEntry, "id" | "at" | "actorId" | "actorUsername">,
): void {
  const current = sessionOf(c);
  if (!current) return;
  recordAudit({
    actorId: current.operator.id,
    /* Denormalised at write time, which is the whole point of the member — see
       `AuditEntry.actorUsername`. Reading it back through `operators` later is the
       implementation that renders "deleted operator" on the row somebody needs. */
    actorUsername: current.operator.username,
    ...entry,
  });
}

/** Cursor pagination. No total — `COUNT(*)` on accounting data is not affordable. */
function page<T>(items: T[], cursor: string | undefined, limit: number) {
  const start = cursor ? Number(cursor) || 0 : 0;
  const slice = items.slice(start, start + limit);
  const next = start + limit < items.length ? String(start + limit) : null;
  return { items: slice, nextCursor: next };
}

const q = (c: { req: { query: (k: string) => string | undefined } }, k: string) => c.req.query(k);
/**
 * What a client that names no `limit` gets.
 *
 * Exported because it is half of a relationship rather than a local convenience:
 * `LIVE_SESSION_WINDOW` in `state.ts` is bounded so that the fixtures' own rows plus
 * a site's worth of portal arrivals fit inside one of these. `state.test.ts` holds
 * the two together, so raising either alone fails there rather than quietly pushing
 * the 40-day orphan onto page two.
 */
export const DEFAULT_PAGE_LIMIT = 50;

const limitOf = (c: { req: { query: (k: string) => string | undefined } }) =>
  Math.min(200, Math.max(1, Number(c.req.query("limit") ?? DEFAULT_PAGE_LIMIT) || DEFAULT_PAGE_LIMIT));

// ── portal ───────────────────────────────────────────────────────────────────

/**
 * The NAS redirect, read off the request.
 *
 * `internal/portal`'s `claimFrom` is the authority for the names and for what is
 * done to each value before it is stored, and this is the same four:
 *
 *   - `id`, or `mac` — the client address, normalised;
 *   - `ap` — the access point's, normalised;
 *   - `ssid` — venue text that reaches a rendered page, so it is length-capped here
 *     and escaped there. Neither alone is enough;
 *   - `url` — allowlisted **on the way in**, so a value that never enters this
 *     server's state in a dangerous shape cannot be rendered in one by a future
 *     reader who forgets to check.
 */
function nasClaim(c: Context): NasClaim {
  const q = (k: string) => c.req.query(k) ?? "";
  return {
    mac: normaliseMac(q("id") || q("mac")),
    apMac: normaliseMac(q("ap")),
    ssid: q("ssid").slice(0, 64),
    originalUrl: safeReturnUrl(q("url")),
  };
}

/**
 * Resolves the simulated client, creating one on first contact — or refusing.
 *
 * **`null` is the refusal `security.md` §4 exists to require.** A version that read
 * none of the NAS redirect parameters — `present` from its own `?present=0` knob and
 * an invented MAC — would answer an access point no deployment had ever registered
 * with `200` and a venue's name, its languages and its terms. That is not the oracle
 * merely being *wrong* — it would have no way to express `VerifyPresence` failing, so
 * no review and no E2E test could see it.
 *
 * The claim is honoured the way the product honours it: `newPortalClient` resolves
 * `ap` against `world.nas` and refuses an access point that is not there. What the
 * caller must do with `null` is answer the refusal **and set no cookie** — see
 * `newPortalClient` for why.
 *
 * ── the review affordances, which are still here and still knobs ─────────────
 *
 * `?present=0` produces a client the driver will not confirm, which is what a
 * portal called directly with forged NAS query parameters looks like. It exists so
 * the rejection path is reviewable rather than merely asserted. It is a knob, and
 * not the *only* way this server can refuse.
 *
 * `?nas=<id>` puts the guest on a chosen access point instead of the healthy
 * default, and exists for the same reason. Without it every simulated guest is on
 * `nas-unifi-1`, which is reachable — so *revoking a voucher whose session cannot
 * be ended* would be a branch in `state.ts` that no request could reach, and the
 * console copy for it could never be looked at. `?nas=nas-unifi-3` reaches it.
 *
 * An unknown id falls back to the default and **says so in a header**, the same way
 * `X-Foyer-Mock-Venue` answers "which venue did I actually get". A review affordance
 * that silently ignores a typo is a review affordance that shows you the healthy
 * path while you believe you are looking at the broken one.
 *
 * A real NAS claim outranks `?nas=`: a request that carries `id` and `ap` is
 * answered on the device those name, or not at all.
 */
function portalClient(c: Context): PortalClient | null {
  const claim = nasClaim(c);
  const existing = getPortalClient(getCookie(c, CLIENT_COOKIE));
  if (existing) {
    // A session, and no claim or the same address: this is the browser we bound.
    if (claim.mac === "" || claim.mac === existing.mac) return existing;
    // A different address. Forget rather than re-point — `security.md` §4, and
    // `forgetPortalClient` for how narrow that rule actually is.
    forgetPortalClient(existing.id);
  }

  const created = newPortalClient({
    present: c.req.query("present") !== "0",
    // `?authorize=0` is the second failure, and it is a different one from
    // `?present=0`. That refuses **before** anything is granted and the guest keeps
    // their code; this one refuses **after** the `303` is on the wire, which is the
    // only ordering the CNA permits — so the grant is real, the voucher is spent,
    // and the guest is already on the connected page when it happens.
    //
    // It exists because `PortalStatus.state`'s `not-on-network` has to be reachable
    // by driving this world. A member of a closed enum that nothing here can produce
    // is copy that can never be shown to render correctly, which is the standing
    // `AuditKind`, `ProblemType` and `AuthEventKind` already carry.
    authorizes: c.req.query("authorize") !== "0",
    nasId: c.req.query("nas"),
    claim,
  });
  if (!created) {
    // No cookie. The product mints no session row for an access point it cannot
    // place, and a cookie written here would bind this browser to a refusal that a
    // second, well-formed redirect could then never get out of.
    return null;
  }
  if (c.req.query("nas")) c.header("X-Foyer-Mock-Nas", created.nasId);
  setCookie(c, CLIENT_COOKIE, created.id, {
    httpOnly: true,
    sameSite: "Lax",
    path: "/",
    // No Max-Age. The CNA destroys its cookie jar on close regardless, and binding
    // the portal session to the browser session is the behaviour we want anyway.
  });
  return created;
}

/**
 * The `403` for a device this server will not vouch for.
 *
 * One function because there are two ways to reach it — the claim named an
 * access point nothing registered, and the driver did not confirm the MAC — and the
 * body must be **identical** for both. A response that distinguished them would be
 * an oracle for what the portal believes about an attacker-supplied address, which
 * is the thing `getPortalContext`'s own description forbids.
 */
function deviceNotVerified(c: Context) {
  return problem(c, 403, {
    type: "/problems/device-not-verified",
    title: "Could not confirm this device",
    status: 403,
    // No `detail`, and that is structural rather than editorial: a sentence here
    // must not say which check failed, because that would make this an oracle. With
    // no sentence there is nothing to review — the client owns the copy, and this
    // body cannot leak what it does not carry.
  });
}

/**
 * Which venue the portal is standing in.
 *
 * Three ways in, because two different readers need it and they cannot use the same
 * one:
 *
 *  - **`?venue=<id>` on this operation** — a per-request override, the same shape as
 *    `?present=0`. This is what a test asserting on the API itself uses.
 *  - **a `foyer_mock_venue` cookie** — what makes it reach a *rendered page*. The
 *    portal is server-rendered and its dev middleware relays the browser's cookies
 *    to this server but not the query string, so a cookie is the only carrier that
 *    survives the hop. Cookies ignore port, so one set here reaches the portal's
 *    origin too.
 *  - **`/venues`** — a chooser page, because a portal review is a human being
 *    clicking through the portal and a switch they cannot reach is a switch that
 *    does not get reviewed.
 *
 * An unrecognised id falls back to the default rather than failing: this is a
 * review affordance, not an API, and a 400 here would have to be declared in the
 * contract to be answerable. `X-Foyer-Mock-Venue` says which profile actually
 * answered, so a typo is visible on the response rather than silently ignored.
 */
function venueFor(c: Context): Venue {
  const requested = c.req.query("venue") ?? getCookie(c, VENUE_COOKIE);
  const venue = venueById(requested) ?? defaultVenue();
  c.header(
    "X-Foyer-Mock-Venue",
    requested && requested !== venue.id ? `${venue.id} (no profile "${requested}")` : venue.id,
  );
  return venue;
}

/**
 * The context a guest actually gets.
 *
 * **An explicitly selected venue profile wins; otherwise the operator's edited
 * settings answer.** Both halves matter. The profiles are the portal's review
 * mechanism and several end-to-end locks select one, so they cannot become a
 * fallback nobody reaches. And the settings record is seeded byte-identical to the
 * default profile, so a deployment nobody has configured behaves exactly as it did
 * before the record existed — which is what keeps the locks that rely on the
 * *default* meaning what they meant.
 *
 * `heroImage` is folded in as a same-origin path rather than a data URI, which is
 * the whole point of the field. See `putPortalHeroImage` in the contract.
 *
 * **The site comes from the guest, not from a literal.** Reading
 * `settingsFor("site-hcm")` would be a hardcoded default site, and would make a leak
 * invisible: with one site's settings answering every guest, a portal that resolved
 * the venue correctly and one that ignored the access point entirely emit identical
 * bytes.
 */
function portalContextFor(c: Context, siteId: string): PortalContext {
  const requested = c.req.query("venue") ?? getCookie(c, VENUE_COOKIE);
  if (requested) return venueFor(c).context;

  const s = settingsFor(siteId);
  c.header("X-Foyer-Mock-Venue", "site-settings");
  return {
    venueName: s.venueName,
    networkName: s.networkName ?? null,
    logoDataUri: s.logoDataUri ?? null,
    heroImageUrl: s.heroImage?.url ?? null,
    languages: s.languages,
    methods: s.methods,
    terms: { summary: { ...(s.terms.summary ?? {}) } },
    // Narrowed as `internal/api` narrows it: a block the venue switched off, or
    // whose dates have passed, is not readable pre-authentication by anyone.
    design: narrowDesign(s.design, localDate()),
    // What the sign-in forms ask for, narrowed as `portalDataCollection` is.
    dataCollection: portalDataCollection(siteId),
    // The code form's "it works for N min" — the venue's own lifetime.
    phoneCodeMinutes: s.methods.includes("phone") ? phoneCodeMinutes(siteId) : null,
  };
}

/**
 * The venue name the connected page welcomes the guest to.
 *
 * Resolved the same way `portalContextFor` resolves it, and that is the point: the
 * success page must name the business the guest consented to. A profile chosen for
 * review wins, and otherwise it is **this guest's** venue rather than the default
 * one — which is what `internal/api` does, reading the settings of the grant's own
 * site.
 */
function venueNameFor(c: Context, siteId: string): string {
  const requested = c.req.query("venue") ?? getCookie(c, VENUE_COOKIE);
  if (requested) return venueFor(c).context.venueName;
  return settingsFor(siteId).venueName;
}

/**
 * Every portal `303`, and the single decision they share: **the `Location` is
 * root-relative.**
 *
 * Building these seven redirects from `PORTAL_ORIGIN` would make the mock choose a
 * scheme and a host for the browser instead of letting it inherit them from the
 * request — and with `PortalRedirect.Location` typed a bare `string`, that and the
 * Go server's root-relative path would both conform and nothing could say which was
 * right. The contract carries a `pattern`, and `portal-redirect.test.ts` reads it
 * out of the document rather than restating it.
 *
 * The cost of choosing is measured, not just reasoned about: with `PORTAL_ORIGIN`
 * left at its default, a phone that redeems a voucher is redirected to **its own
 * loopback**, so the flow
 * appears to work right up to the last step and then dies with no diagnosis. That is
 * not a configuration mistake to warn about — it is the shape of the header. A guest
 * is on plain HTTP, on an SSID with no route off it, on the one screen where they
 * have just typed a credential; inheriting the origin is the only answer that is
 * right behind a TLS terminator, on a LAN address, through the portal's dev proxy and
 * when the Go binary serves both.
 *
 * The parameter type is `` `/${string}` `` on purpose: an absolute URL here is a
 * compile error rather than something a test has to catch after the fact.
 *
 * **`PORTAL_ORIGIN` still exists and still means something** — `/venues/:venueId` is
 * a mock-only convenience that deliberately crosses from this server's origin to the
 * portal's, and `PortalContext.portalUrl` names the portal. Neither is a
 * `PortalRedirect`.
 */
function portalRedirect(c: Context, path: `/${string}`) {
  return c.redirect(path, 303);
}

api.get("/portal/context", (c) => {
  const client = portalClient(c);
  if (!client || !client.verified) return deviceNotVerified(c);
  return c.json(conformResponse("getPortalContext", 200, portalContextFor(c, client.siteId)));
});

/** `303` on every outcome. See the note on `redeemVoucher` in the contract. */
api.post("/portal/voucher", async (c) => {
  const client = portalClient(c);
  const form = await c.req.parseBody();
  // The portal always submits `lang` as a hidden field, so this default is never
  // reached from the portal itself. It matches `Language`'s default all the same —
  // a stale `vi` here is a trap for whoever reads it next.
  const lang = String(form.lang ?? "en") === "vi" ? "vi" : "en";
  const submitted = String(form.code ?? "");
  // Before the claim: a voucher is not spent on a form about to be refused.
  const guest = client ? readGuestForm(client.siteId, form) : null;
  // A claim this server cannot place is `not-present`, and it is a `303` like every
  // other outcome — never a problem document. A form navigation answered
  // `application/problem+json` renders raw JSON inside a 900 × 550 pt mini-browser
  // with no client left to turn it into a sentence, and it is not a navigation the
  // CNA treats as a captivity re-check, so its button stays *Cancel*.
  const result = !client
    ? ({ ok: false, reason: "not-present" } as const)
    : guest && !guest.ok
      ? ({ ok: false, reason: guest.reason } as const)
      : redeemVoucher(client, submitted);
  if (result.ok && client && guest?.ok) keepGuestInput(client.siteId, client.mac, result.grant.id, guest.input);

  c.header("Cache-Control", "no-store");
  if (!result.ok) {
    /*
     * The submitted code travels back with the failure.
     *
     * The portal's success path is a real document navigation (form POST → 303 →
     * new document), which is the only thing the Captive Network Assistant treats
     * as a re-check. That is not negotiable — but it also means the failure path is
     * a new document too, with **no client state to hold the field**. The CNA's data
     * store is ephemeral and the flow must not depend on JavaScript, so there is
     * nowhere else this value can live: the server has to carry it.
     *
     * Without this a guest who mistypes one of twelve characters, one-handed, on a
     * phone, in a mini-browser, retypes all twelve.
     *
     * It comes back **normalised**, not raw. `normaliseVoucherCode` upper-cases and
     * drops everything outside the voucher alphabet, so the value the portal echoes
     * into an input is constrained to `[A-Z0-9]` by construction — a hand-written
     * link cannot use this parameter to put arbitrary text on the portal page. The
     * length cap bounds the URL and still lets an over-long entry read as over-long.
     */
    const params = new URLSearchParams({ e: result.reason });
    const echo = normaliseVoucherCode(submitted).slice(0, 24);
    if (echo) params.set("code", echo);
    return portalRedirect(c, `/${lang}/voucher/?${params}`);
  }
  return portalRedirect(c, `/${lang}/connected/`);
});

/**
 * Click-through. `303` on every outcome, like every other portal operation.
 *
 * `404` where the venue has not enabled the method, rather than granting anyway: an
 * endpoint that works while its button is hidden is an endpoint somebody finds, and
 * this is the one with no credential in front of it.
 *
 * **The venue is this guest's.** Reading `settingsFor("site-hcm")` — a literal —
 * would answer whether click-through is offered at all, and for how many minutes,
 * from a site the guest may not be standing in. `tools/codegen/differ` cannot catch
 * that, because its guest stands in `site-hcm` and the two servers would agree; a
 * declaration that never fires is one that harness rightly refuses. The literal
 * would still be wrong, and the fixtures' second venue offers 45 minutes rather
 * than 30.
 */
api.post("/portal/agree", async (c) => {
  const client = portalClient(c);
  const form = await c.req.parseBody();
  const lang = String(form.lang ?? "en") === "vi" ? "vi" : "en";

  c.header("Cache-Control", "no-store");

  // No placement, so there is no venue whose settings could say whether this is
  // offered. `not-present`, as a navigation.
  if (!client) {
    return portalRedirect(c, `/${lang}/problem/?e=not-present`);
  }
  const settings = settingsFor(client.siteId);

  if (!settings.methods.includes("click-through") || !settings.clickThroughMinutes) {
    return problem(c, 404, {
      type: "/problems/method-not-offered",
      title: "Not offered here",
      status: 404,
    });
  }

  // Accepting the terms is not consenting to marketing: the checkbox is read by
  // `readGuestForm` exactly as on the other two forms, and only there.
  const guest = readGuestForm(client.siteId, form);
  if (!guest.ok) return portalRedirect(c, `/${lang}/?e=${guest.reason}`);
  const result = acceptTerms(client, settings.clickThroughMinutes);
  if (!result.ok) {
    return portalRedirect(c, `/${lang}/problem/?e=${result.reason}`);
  }
  keepGuestInput(client.siteId, client.mac, result.grant.id, guest.input);
  return portalRedirect(c, `/${lang}/connected/`);
});

api.post("/portal/account", async (c) => {
  const client = portalClient(c);
  const form = await c.req.parseBody();
  const lang = String(form.lang ?? "en") === "vi" ? "vi" : "en";
  const guest = client ? readGuestForm(client.siteId, form) : null;
  const result = !client
    ? ({ ok: false, reason: "not-present" } as const)
    : guest && !guest.ok
      ? ({ ok: false, reason: guest.reason } as const)
      : loginWithAccount(client, String(form.username ?? ""), String(form.password ?? ""));
  if (result.ok && client && guest?.ok) keepGuestInput(client.siteId, client.mac, result.grant.id, guest.input);

  c.header("Cache-Control", "no-store");
  if (!result.ok) {
    return portalRedirect(c, `/${lang}/account/?e=${result.reason}`);
  }
  return portalRedirect(c, `/${lang}/connected/`);
});

/**
 * Phone sign-in, the first half: send a code. `303` on every outcome, `404` where
 * the venue does not show the button — the same two answers `internal/api` gives, in
 * the same order: offered, then presence, then the number, the limits, the ceiling.
 * The number is never in the redirect.
 */
api.post("/portal/phone", async (c) => {
  const client = portalClient(c);
  const form = await c.req.parseBody();
  const lang = String(form.lang ?? "en") === "vi" ? "vi" : "en";
  c.header("Cache-Control", "no-store");
  if (!client) return portalRedirect(c, `/${lang}/phone/?e=not-present`);
  if (!settingsFor(client.siteId).methods.includes("phone") || phoneOffered(client.siteId) === null) {
    return problem(c, 404, { type: "/problems/method-not-offered", title: "Not offered here", status: 404 });
  }
  // Presence before anything is paid for — see `internal/portal/phone.go`.
  if (!client.verified || !world.presentMacs.has(client.mac)) {
    return portalRedirect(c, `/${lang}/phone/?e=not-present`);
  }
  // The socket peer, as `internal/api`'s `clientIP` reads it — never a header.
  const source = (c.env as { incoming?: { socket?: { remoteAddress?: string } } } | undefined)?.incoming?.socket
    ?.remoteAddress;
  const result = sendPhoneCode(client, String(form.phone ?? ""), source ?? "");
  if (!result.ok) return portalRedirect(c, `/${lang}/phone/?e=${result.reason}`);
  return portalRedirect(c, `/${lang}/phone/?sent=1`);
});

/** Phone sign-in, the second half: the code, then the grant. */
api.post("/portal/phone/code", async (c) => {
  const client = portalClient(c);
  const form = await c.req.parseBody();
  const lang = String(form.lang ?? "en") === "vi" ? "vi" : "en";
  c.header("Cache-Control", "no-store");
  if (!client) return portalRedirect(c, `/${lang}/phone/?e=not-present`);
  const minutes = phoneOffered(client.siteId);
  if (!settingsFor(client.siteId).methods.includes("phone") || minutes === null) {
    return problem(c, 404, { type: "/problems/method-not-offered", title: "Not offered here", status: 404 });
  }
  if (!client.verified || !world.presentMacs.has(client.mac)) {
    return portalRedirect(c, `/${lang}/phone/?e=not-present`);
  }
  /* The venue's guest-data setting, before the code is spent. */
  const guest = readGuestForm(client.siteId, form, { omitPhone: true });
  if (!guest.ok) return portalRedirect(c, `/${lang}/phone/?e=${guest.reason}&sent=1`);
  const checked = checkPhoneCode(client.id, String(form.code ?? ""));
  if (!checked.ok) {
    // A wrong code keeps the code form open; an expired one sends the guest back
    // to ask for a new one. `url.Values.Encode` sorts, so `e` comes first.
    return portalRedirect(
      c,
      checked.reason === "code-wrong" ? `/${lang}/phone/?e=code-wrong&sent=1` : `/${lang}/phone/?e=${checked.reason}`,
    );
  }
  const result = grantPhone(client, checked.phone, minutes);
  if (!result.ok) return portalRedirect(c, `/${lang}/phone/?e=${result.reason}`);
  keepGuestInput(client.siteId, client.mac, result.grant.id, withVerifiedPhone(client.siteId, form, guest.input, checked.phone));
  return portalRedirect(c, `/${lang}/connected/`);
});

api.get("/portal/status", (c) => {
  const client = portalClient(c);
  const grant = getGrant(client?.grantId ?? null);
  /*
   * `404` means **no grant answers to this browser**, and nothing else.
   *
   * Making it also mean "the grant exists and is no longer connected" would leave
   * `PortalStatus.state`'s `expired` and `revoked` members unreachable on the wire —
   * the contract declares three states and the mock could only ever produce one. A
   * portal that read the plan and never the state would then show a green tick to a
   * guest whose voucher had just been revoked, and no test could catch it because
   * the mock could not produce the case.
   *
   * A grant that has ended is a fact the guest is owed, so it is answered with `200`
   * and its real state. The portal decides what to say about it.
   */
  if (!client || !grant) {
    return problem(c, 404, {
      type: "/problems/no-grant",
      title: "Not connected",
      status: 404,
    });
  }
  return c.json(
    conformResponse("getPortalStatus", 200, {
      state: grant.state,
      plan: grant.plan ?? PLANS[grant.planKey]!,
      grantedAt: new Date(grant.grantedAt).toISOString(),
      expiresAt: grant.expiresAt ? new Date(grant.expiresAt).toISOString() : null,
      // The same venue that answered `/portal/context`, or the success page would
      // welcome the guest to a different business than the one they consented to.
      // Resolved from this guest's own site, not the default profile.
      venueName: venueNameFor(c, client.siteId),
      // Allowlisted when the NAS sent it and allowlisted again here. Rendered as a
      // link the guest chooses to follow — never an automatic bounce.
      originalUrl: safeReturnUrl(client.originalUrl),
    }),
  );
});

/**
 * The open-redirect defence. `internal/portal.SafeReturnURL` is the authority and
 * this is the same allowlist, clause for clause.
 *
 * It matters more than it reads, and `security.md` §4 says why: the victim is on an
 * untrusted network, *expects* to be redirected, and has just typed a credential. On
 * any other site a redirect to a look-alike host is suspicious; here it is the
 * advertised behaviour of the product.
 *
 * **It must never be applied only to a fixture constant**, which is a defect of
 * shape rather than a missing clause: a helper that only ever sees
 * `http://vnexpress.net/` cannot refuse anything, so this server could not disagree
 * with an implementation that failed to validate. It sees whatever the NAS sent, on
 * the way in and again on the way out — and `?url=` is how a hostile one is put to
 * it.
 *
 * Empty string rather than a partial answer, and the caller renders no link at all:
 * a guest who does not get their original page back is mildly inconvenienced, and a
 * guest who gets somebody else's has been phished on a network the venue vouched for.
 */
function safeReturnUrl(raw: string | null): string | null {
  if (!raw) return null;
  const value = raw.trim();
  // A control character anywhere is a request to split a header or break a
  // renderer, never a URL. Refused before parsing, because `URL` tolerates some.
  if (/[\u0000-\u001f\u007f]/.test(value)) return null;
  // The contract caps it at 2048. A longer value is not a destination anybody typed.
  if (value.length > 2048) return null;
  try {
    const u = new URL(value);
    if (u.protocol !== "http:" && u.protocol !== "https:") return null;
    // Userinfo. `http://portal.example@evil.example` — the half a guest reads is the
    // half that is not the host. `//evil.example` has no scheme and is caught above.
    if (u.username || u.password) return null;
    // No host is not a destination; an `@` or whitespace surviving the parse is a
    // shape the contract's own `pattern` rejects and is not one a NAS produces.
    if (!u.host || /[@\s]/.test(u.host)) return null;
    return u.toString();
  } catch {
    return null;
  }
}

// ── sites and overview ───────────────────────────────────────────────────────

api.get("/sites", (c) => c.json(conformResponse("listSites", 200, world.sites)));

/*
 * `createSite` — the operation without which a deployment goes no further, and the
 * one whose absence no world in this repository could see.
 *
 * Measured on the running lab: `createNas` requires a `siteId` and refuses `422`
 * without one, and a migrated database holds **zero** sites. Every fixture world
 * here has three sites, so a missing `createSite` would be invisible from inside —
 * the *world cannot express the property* shape at deployment scale, one layer out
 * from the single-site fixture world that would hide a dropped site scope.
 *
 * **The blank-name refusal is hand-written and has to be.** `docs/rules/
 * frontend.md`: this mock validates *responses* against the schema and not requests,
 * so `required: [name]` in `SiteCreate` falls out of nothing at runtime. `ipAddress`
 * and `coaVendor` carry the same hand-written shape for the same reason.
 *
 * **It writes `site-created`.** `internal/api`'s invariant 10 forbids filing an
 * action under a near-enough member — `nas-registered` would name a device as the
 * subject of a change that made a venue — so `AuditKind` has a member of its own
 * for creating a venue rather than a relaxed rule, and the first act of every
 * installation is recorded.
 */
api.post("/sites", async (c) => {
  const body = (await c.req.json()) as SiteCreate;
  const refused = fieldRefusals();

  /* Blank is refused rather than stored. `sites.name` is `text NOT NULL` and `""`
     satisfies that, so the database is not the gate: an unnamed venue would be a gap
     in the site switcher that an operator cannot click. Whitespace counts as blank
     for the same reason the name is trimmed on the way in — a venue's name does not
     start with a space. */
  if (!body?.name?.trim()) refused.add("name", "required", "Required.");

  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Site not created",
      status: 422,
      ...refused.problem(),
    });
  }

  /*
   * ── one venue per install ──────────────────────────────────────────────────
   *
   * Refused AFTER the field check, because a blank name is still a blank name and
   * an operator who typed nothing should be told that rather than told the venue
   * limit. Ordering a refusal ahead of validation is how a form starts reporting
   * the wrong reason for the right rejection.
   *
   * NOT `/problems/duplicate`: nothing collided, and *choose another name* is
   * advice that cannot work here. The reasoning ends at the protocol —
   * `nas.nasname` is globally unique because FreeRADIUS's client list is one flat
   * namespace, so two venues sharing `192.168.1.1` can never both be projected,
   * and on private-range networks that is the common case.
   *
   * `world.sites.length` and not a count of what this route created: an install
   * that already has a venue has one however it got there. `FOYER_MOCK_FRESH=1` is
   * the world where this is empty, and it is the only world in which the `201`
   * below can be reached — see `state.ts`.
   */
  if (world.sites.length > 0) {
    return problem(c, 409, {
      type: "/problems/one-site-per-install",
      title: "This deployment already has a site",
      status: 409,
    });
  }

  const site: Site = {
    id: `site-${Math.random().toString(36).slice(2, 10)}`,
    name: body.name.trim(),
    /* `live`, and `SiteCreate` accepts no environment. `lab` is the badge that says
       *you may point this at fake gear*; a real venue must not wear it because a
       form defaulted. */
    environment: "live",
  };
  /* Inserted in name order, not appended. `SiteRepo.List` is `ORDER BY name` and
     `listSites` is unpaginated, so a mock that appended would disagree with the Go
     adapter about the whole response body — and this server is that adapter's
     behavioural oracle, so the disagreement would be reported as the adapter's. */
  world.sites.push(site);
  world.sites.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  /* The venue's own id in `siteId`, not `null`. `siteId` is null for a change that
     is not a site's — an operator was created — and this one is a site's from the
     instant it exists. No `params`: the name is in `subject.name`, where every
     kind's subject name is, and `SiteCreate` carries nothing else. */
  audit(c, {
    kind: "site-created",
    siteId: site.id,
    subject: { kind: "site", id: site.id, name: site.name },
  });
  return c.json(conformResponse("createSite", 201, site), 201);
});

/**
 * The freshness of a counted figure, over exactly the rows it counts.
 *
 * A `CountSessions` that took one timestamp over `state IN ('online','orphaned')`
 * while answering `online` and `orphaned` as two separate figures would report, for
 * a site with a fresh online session and one forty-day orphan, *the online count* as
 * forty days stale. This route counts over `online` alone, which is the rule: **a
 * freshness is derived over exactly the rows its figure counts.** The orphan's own
 * age is real and belongs to `orphanedSessions`, which carries it — see
 * {@link orphanFreshness}.
 *
 * The three clauses are stated in the contract under `Overview.clientsOnline` and
 * implemented twice, here and in `internal/apistore`'s `onlineFreshness`, because
 * there is no runtime the two servers share. Two restatements drift, so the contract
 * is the authority and neither of these is:
 *
 *  1. Nothing counted → `live`. There is no sample behind a figure with no rows in
 *     it, and zero is what this server knows at this instant. (The adapter answers
 *     `live` here too; `sampled` with a null `sampledAt` would be a state that names
 *     a sample and then declines to date it.)
 *  2. Otherwise the sample time is the **oldest** of the counted rows, never the
 *     newest. Reporting the freshest would flatter the figure into looking live,
 *     which is the exact lie this whole design is trying not to tell.
 *  3. `stale` when that sample is older than twice the declared interval.
 *
 * Clause 3 is measured against the declared 300-second floor rather than against
 * each row's own `freshness.state`. A NAS that asked for less than five minutes and
 * is late against its own promise is a per-session fault, it travels on that
 * session's own `Freshness` — where there is deliberately no floor — and reading it
 * here would produce a verdict no reader could reproduce from the two numbers
 * rendered beside it. It would also be a verdict that is very nearly unreachable: every fixture interval is 300 or null, and at
 * 300 a row's freshness and its `orphaned` threshold are the same 600 seconds, so it
 * stops being counted at the instant it would have gone stale. `sess-clock-skew` --
 * whose `startedAt` is four minutes in the future -- is the one row that can reach
 * it, and only during the few minutes of uptime when its sample has aged out and its
 * skewed start has not. A predicate nothing exercises is not one anybody has seen be
 * wrong.
 */
const INTERIM_FLOOR_SECONDS = 300;

function countedFreshness(rows: Session[], now: number): Overview["clientsOnline"]["freshness"] {
  /* A row with no accounting at all is `unknown` on both servers and never
     `online`, so a counted row always carries a sample: this is empty exactly when
     `rows` is. */
  const samples = rows
    .map((s) => (s.freshness.sampledAt ? Date.parse(s.freshness.sampledAt) : null))
    .filter((t): t is number => t !== null);
  if (samples.length === 0) {
    return { state: "live", sampledAt: new Date(now).toISOString(), intervalSeconds: null };
  }
  const oldest = Math.min(...samples);
  const stale = (now - oldest) / 1000 > 2 * INTERIM_FLOOR_SECONDS;
  return {
    state: stale ? "stale" : "sampled",
    sampledAt: new Date(oldest).toISOString(),
    intervalSeconds: INTERIM_FLOOR_SECONDS,
  };
}

/**
 * The same rule over the orphaned rows — and it does not mean the same thing.
 *
 * **`orphanedSessions` is a `Metric`, not a bare `integer`**, so the one figure
 * whose entire meaning is *these have not been heard from* can say when any of them
 * last was.
 *
 * `Metric`'s rule is one rule and it applies here unchanged: the sample is the
 * oldest of exactly the rows the value counts. What differs is what an old sample
 * *is*. On `clientsOnline` it is an artefact of the instrument, and the warning is
 * that the number may be wrong. Here the age is the subject — nothing about forty
 * days makes the count of them less true — so *sampled forty days ago* is the true
 * and useful sentence about this figure where the same words about the online count
 * would be a lie.
 *
 * Two departures from {@link countedFreshness}, and neither is a copy:
 *
 *  - **No `intervalSeconds`.** A row joins this set after
 *    `2 × max(its own interval, 300s)` of silence — `withDerivedState` in
 *    `state.ts` — so the set mixes rows judged at ten minutes with rows judged at
 *    two hours, and no one cadence is true of all of them. The online figure
 *    publishes the floor because the floor is what turns its age into a verdict;
 *    here membership decided the verdict before the figure existed, and printing
 *    *every 5 minutes* beside rows defined by nothing arriving would be a cadence
 *    this server invented.
 *  - **`stale` whenever the count is non-zero.** True — every counted row has had
 *    no update within twice its own interval, which is what put it in the set — and
 *    **redundant with the value**, since it is `stale` exactly when the count is
 *    non-zero. The contract says that out loud rather than dressing it up: on this
 *    figure the state is not the information, the timestamp is, and the console
 *    renders the timestamp. `live` was the alternative and it is worse in both
 *    directions: it is false about rows that are plainly sampled, and it makes a
 *    generic client drop the one field worth carrying.
 *
 * Clause 1 is unchanged and shared: nothing counted → `live`, dated now.
 */
function orphanFreshness(rows: Session[], now: number): Overview["clientsOnline"]["freshness"] {
  const samples = rows
    .map((s) => (s.freshness.sampledAt ? Date.parse(s.freshness.sampledAt) : null))
    .filter((t): t is number => t !== null);
  if (samples.length === 0) {
    return { state: "live", sampledAt: new Date(now).toISOString(), intervalSeconds: null };
  }
  return {
    state: "stale",
    sampledAt: new Date(Math.min(...samples)).toISOString(),
    intervalSeconds: null,
  };
}

api.get("/overview", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const mine = world.sessions.filter((s) => s.siteId === siteId).map(withCurrentFreshness);
  const online = mine.filter((s) => s.state === "online");
  const orphaned = mine.filter((s) => s.state === "orphaned");

  const overview: Overview = {
    clientsOnline: {
      value: online.length,
      freshness: countedFreshness(online, Date.now()),
    },
    /*
     * **Scoped through the batch.**
     *
     * Reading `world.vouchers` entire would make the hotel's dashboard report the
     * café's redemptions as its own. It is easy to miss by reading this function:
     * every other figure here is site-filtered, so an unscoped line looks like its
     * neighbours, and with one occupied site "all vouchers" and "this site's
     * vouchers" are the same set. Opening the second site's dashboard shows it.
     *
     * A voucher carries no `siteId` — it belongs to a batch, and the batch belongs
     * to a site. Resolved through the batch, the same way `listVouchers` does.
     */
    /* "Today" is the site's calendar day (`getSiteTimeZone`), as the Go server
       counts it — not the last 24 hours, which is no operator's "today". */
    vouchersRedeemedToday: {
      value: world.vouchers.filter(
        (v) =>
          v.firstUsedAt &&
          siteDay(siteId, Date.parse(v.firstUsedAt)) === siteDay(siteId, Date.now()) &&
          Date.parse(v.firstUsedAt) <= Date.now() &&
          world.batches.find((b) => b.id === v.batchId)?.siteId === siteId,
      ).length,
      freshness: { state: "live", sampledAt: new Date().toISOString(), intervalSeconds: null },
    },
    nasTotal: world.nas.filter((n) => n.siteId === siteId).length,
    nasUnhealthy: world.nas.filter((n) => n.siteId === siteId && n.reachability !== "ok").length,
    /*
     * **A second figure over a second set of rows, and it dates itself now.**
     *
     * See {@link orphanFreshness}. The rows are the open ones `clientsOnline` does
     * not count, the sample is the oldest of them, and the sentence it makes
     * possible — *the oldest of these was last heard from forty days ago* — is what
     * separates one access point that rebooted ten minutes ago from rows nothing
     * has reaped since July. Those are the same integer and different faults, and
     * for as long as this was an `integer` the console could not tell them apart.
     */
    orphanedSessions: {
      value: orphaned.length,
      freshness: orphanFreshness(orphaned, Date.now()),
    },
    alarmCount: deriveAlarms(world, siteId).length,
  };
  return c.json(conformResponse("getOverview", 200, overview));
});

api.get("/alarms", (c) => {
  const alarms: Alarm[] = deriveAlarms(world, q(c, "siteId") ?? "site-hcm");
  return c.json(conformResponse("listAlarms", 200, alarms));
});

// ── reports ──────────────────────────────────────────────────────────────────
//
// The CSV documents and the trends. The rules are `internal/api`'s and the bytes
// are compared by `make diff-api`; see `reports.ts` for what is read from the
// contract rather than restated.

/**
 * A bounded range, or the refusal Go answers: an unreadable or missing bound is
 * the generated binder's `query` fault, an inverted one `range-inverted`, and one
 * longer than the operation allows `range-too-long` with the figure.
 */
function readRange(
  c: Context,
  limitDays: number,
): { from: Date; to: Date } | Response {
  const from = new Date(q(c, "from") ?? "");
  const to = new Date(q(c, "to") ?? "");
  if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime())) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      params: { fields: { query: "required" } },
      errors: { query: "Missing or unreadable." },
    });
  }
  if (to.getTime() <= from.getTime()) {
    const refused = fieldRefusals();
    refused.add("to", "range-inverted", "Must be after the start of the range.");
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      ...refused.problem(),
    });
  }
  if (limitDays > 0 && to.getTime() - from.getTime() > limitDays * 86_400_000) {
    return rangeTooLong(c, limitDays);
  }
  return { from, to };
}

function rangeTooLong(c: Context, maxDays: number) {
  const refused = fieldRefusals();
  refused.add("to", "range-too-long", `At most ${maxDays} days.`, { maxDays });
  const body = refused.problem();
  return problem(c, 422, {
    type: "/problems/validation",
    title: "That cannot be saved",
    status: 422,
    errors: body.errors,
    params: { maxDays, ...body.params },
  });
}

/**
 * One export: the site, the audit entry **before** the document, then the document.
 * Go refuses an export it cannot record; the mock's log cannot fail, so the order
 * is what is kept.
 */
function csvExport<T>(
  c: Context,
  operationId: "exportSessions" | "exportVouchers" | "exportGuests" | "exportMarketingConsents",
  dataset: { columns: string[]; row: (v: T) => string[] },
  name: string,
  rows: (siteId: string, from: number, to: number) => T[],
) {
  const range = readRange(c, maxRangeDays(operationId));
  if (range instanceof Response) return range;
  const siteId = q(c, "siteId") ?? "";
  const venue = site(siteId);
  if (!venue) return notFound(c);
  audit(c, {
    kind: "data-exported",
    siteId,
    params: { dataset: name, from: csvTime(range.from.getTime()), to: csvTime(range.to.getTime()) },
    subject: { kind: "site", id: venue.id, name: venue.name },
  });
  const body = csvDocument(
    dataset.columns,
    rows(siteId, range.from.getTime(), range.to.getTime()).map(dataset.row),
  );
  return c.body(body, 200, {
    "content-type": "text/csv; charset=utf-8",
    "content-disposition": `attachment; filename="${exportFilename(name, siteId, range.from, range.to)}"`,
    "cache-control": "no-store",
  });
}

const byTimeThenId = <T extends { id: string }>(at: (v: T) => number) => (a: T, b: T) =>
  at(a) - at(b) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);

api.get("/exports/sessions", (c) =>
  csvExport(c, "exportSessions", DATASETS.sessions, "sessions", (siteId, from, to) =>
    world.sessions
      .filter((s) => s.siteId === siteId)
      .filter((s) => Date.parse(s.startedAt) >= from && Date.parse(s.startedAt) < to)
      .map(withCurrentFreshness)
      .sort(byTimeThenId((s) => Date.parse(s.startedAt))),
  ),
);

api.get("/exports/vouchers", (c) =>
  csvExport(c, "exportVouchers", DATASETS.vouchers, "vouchers", (siteId, from, to) => {
    const inSite = new Set(world.batches.filter((b) => b.siteId === siteId).map((b) => b.id));
    const within = (iso: string | null | undefined) =>
      iso != null && Date.parse(iso) >= from && Date.parse(iso) < to;
    return world.vouchers
      .filter((v) => inSite.has(v.batchId) && (within(v.createdAt) || within(v.firstUsedAt)))
      .sort(byTimeThenId((v) => (v.createdAt ? Date.parse(v.createdAt) : 0)));
  }),
);

api.get("/exports/guests", (c) =>
  csvExport(c, "exportGuests", DATASETS.guests, "guests", (siteId, from, to) =>
    grantsInSite(siteId).filter((g) => g.grantedAt >= from && g.grantedAt < to),
  ),
);

api.get("/exports/marketing-consents", (c) =>
  csvExport(c, "exportMarketingConsents", DATASETS.consents, "marketing-consents", consentsBetween),
);

api.get("/trends", (c) => {
  const raw = q(c, "granularity") ?? "day";
  if (raw !== "day" && raw !== "week") {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      params: { fields: { query: "required" } },
      errors: { query: "Missing or unreadable." },
    });
  }
  const granularity = raw;
  const range = readRange(c, 0);
  if (range instanceof Response) return range;
  const buckets = trendBuckets(range.from, range.to, granularity, timeZoneOf(q(c, "siteId") ?? ""));
  const limit = maxRangeDays("getTrends", granularity);
  if (buckets.length * (granularity === "week" ? 7 : 1) > limit) return rangeTooLong(c, limit);
  const siteId = q(c, "siteId") ?? "";
  if (!site(siteId)) return notFound(c);

  const sessions = world.sessions.filter((s) => s.siteId === siteId);
  const grants = grantsInSite(siteId);
  const inSite = new Set(world.batches.filter((b) => b.siteId === siteId).map((b) => b.id));
  const redeemedAt = world.vouchers
    .filter((v) => inSite.has(v.batchId) && v.firstUsedAt)
    .map((v) => Date.parse(v.firstUsedAt!));
  const now = Date.now();
  const trends: Trends = {
    granularity,
    buckets: buckets.map((b) => ({
      start: new Date(b.from).toISOString(),
      end: new Date(b.to).toISOString(),
      ...countBucket(b, sessions, grants, redeemedAt, now),
    })),
  };
  return c.json(conformResponse("getTrends", 200, trends));
});

// ── diagnostics ──────────────────────────────────────────────────────────────

/**
 * Authentication attempts, including the ones that failed.
 *
 * The free-text match deliberately includes rows whose `callingStationId` is null —
 * they simply cannot match a MAC query, which is the honest outcome and the reason
 * `macCoverage` and `blindSpots` ride on the envelope. Filtering them out entirely
 * would make the one row that proves per-row absence invisible to the one screen
 * built to show it.
 */
api.get("/auth-attempts", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const query = (q(c, "q") ?? "").trim().toLowerCase();
  const outcome = q(c, "outcome");
  const source = q(c, "source");

  let items: AuthAttempt[] = world.authAttempts.filter((a) => a.siteId === siteId);
  if (outcome) items = items.filter((a) => a.outcome === outcome);
  if (source) items = items.filter((a) => a.source === source);
  if (query) {
    /* MACs are searched with separators stripped, because an operator copies them
       from a phone's settings screen in whatever form that screen used. */
    const bare = query.replace(/[^a-z0-9]/g, "");
    items = items.filter((a) =>
      [a.callingStationId, a.calledStationId, a.username, a.nasId]
        .filter(Boolean)
        .some((f) => {
          const v = String(f).toLowerCase();
          return v.includes(query) || v.replace(/[^a-z0-9]/g, "").includes(bare);
        }),
    );
  }

  const p = page(items, q(c, "cursor"), limitOf(c));
  return c.json(
    conformResponse("listAuthAttempts", 200, {
      ...p,
      macCoverage: MAC_COVERAGE,
      blindSpots: authBlindSpotsFor(world, siteId),
    }),
  );
});


// ── portal settings ──────────────────────────────────────────────────────────

const settingsFor = (siteId: string): PortalSettings => {
  const found = world.portalSettings.find((p) => p.siteId === siteId);
  if (!found) throw new Error(`no portal settings for ${siteId}`);
  return found;
};

/** The record plus where the portal answers, which is the server's to know. */
const settingsResponse = (siteId: string): PortalSettings => ({
  ...settingsFor(siteId),
  portalUrl: PORTAL_ORIGIN,
});

api.get("/sites/:siteId/portal", (c) => {
  const siteId = c.req.param("siteId");
  if (!world.portalSettings.some((p) => p.siteId === siteId)) return notFound(c);
  return c.json(conformResponse("getPortalSettings", 200, settingsResponse(siteId)));
});

/* Two settings values, compared as the log compares them: a field that is absent
   and a field that is `null` are the same page. `audit_portal.go` compares the
   same way on the Go side. */
const sameValue = (a: unknown, b: unknown): boolean => canonical(a) === canonical(b);

/** `JSON.stringify` with every object's keys in sorted order, so two values that
    read alike compare alike. The console rebuilds the whole settings object on
    every save, and a page it did not touch comes back with its keys in the
    request's order rather than the stored one — a raw `JSON.stringify`
    comparison calls that a design change and writes an entry for a page nobody
    edited, which is exactly the defect the difference is supposed to remove.
    `encoding/json` sorts a map's keys too, so the two implementations agree. */
function canonical(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(canonical).join(",")}]`;
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    return `{${Object.keys(o)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canonical(o[k])}`)
      .join(",")}}`;
  }
  return JSON.stringify(v ?? null);
}

api.patch("/sites/:siteId/portal", async (c) => {
  const siteId = c.req.param("siteId");
  if (!world.portalSettings.some((p) => p.siteId === siteId)) return notFound(c);
  const s = settingsFor(siteId);
  const body = (await c.req.json()) as PortalSettingsUpdate;

  if (body.venueName !== undefined && !body.venueName.trim()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Missing venue name",
      status: 422,
      params: { fields: { venueName: "required" } },
      errors: { venueName: "Required." },
    });
  }
  /* A portal advertising no way in is a portal that renders and cannot be used —
     the guest reads the page and there is nothing to press. Refused rather than
     saved, because it fails silently on the one screen nobody is watching. */
  if (body.methods !== undefined && body.methods.length === 0) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "No way to sign in",
      status: 422,
      params: { fields: { methods: "required" } },
      errors: { methods: "Choose at least one." },
    });
  }
  if (body.languages !== undefined && body.languages.length === 0) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "No language",
      status: 422,
      params: { fields: { languages: "required" } },
      errors: { languages: "Choose at least one." },
    });
  }
  if (body.logoDataUri) {
    /* A data URI is inlined and gated; a same-origin path is served by the portal
       itself and needs no gate — the mirror of `internal/api`'s shape check. An
       external origin is refused here rather than debugged on the device. */
    if (body.logoDataUri.startsWith("/") && !body.logoDataUri.startsWith("//") &&
        !/[\s"'<>]/.test(body.logoDataUri)) {
      /* A link: stored as-is, weighed never. */
    } else {
      const bytes = dataUriBytes(body.logoDataUri);
      if (bytes === null) {
        return problem(c, 422, {
          type: "/problems/validation",
          title: "Not an inline image",
          status: 422,
          params: { fields: { logoDataUri: "not-a-data-uri" } },
          errors: { logoDataUri: "Expected a data URI or a portal path." },
        });
      }
    if (bytes > LOGO_MAX_BYTES) {
      return problem(c, 422, {
        type: "/problems/too-large",
        title: "Logo too large",
        status: 422,
        /* Both figures, not the sentence that contained them. A client that has to
           parse "140 kB against a 100 kB limit" out of prose is a client that will
           render the prose instead, in whatever language the server chose. */
        params: {
          fields: { logoDataUri: "too-large" },
          /* The page's figures and the field's figures are the same two numbers here
             because exactly one field was refused. They are sent twice on purpose:
             `params` is what the page-level sentence quotes, `fieldParams` is what the
             sentence under the input quotes, and a second refusal of a second file
             field would give them different values. See `fieldParams` in the
             contract for why the top level cannot be the field's home. */
          fieldParams: { logoDataUri: { bytes, limitBytes: LOGO_MAX_BYTES } },
          bytes,
          limitBytes: LOGO_MAX_BYTES,
        },
        errors: { logoDataUri: `${bytes} B is over the ${LOGO_MAX_BYTES} B limit.` },
      });
    }
    }
  }
  /*
   * The page design: the schema first, then the rules the schema cannot say —
   * both answered in the order and shape `internal/api` answers them.
   */
  if (body.design !== undefined) {
    const shape = requestFault("PortalDesign", body.design, "design");
    if (shape) {
      return problem(c, 422, {
        type: "/problems/validation",
        title: "Page design not valid",
        status: 422,
        params: {
          fields: { [shape.field]: shape.fault },
          ...(shape.figures ? { fieldParams: { [shape.field]: shape.figures } } : {}),
        },
        errors: { [shape.field]: shape.message },
      });
    }
    const rule = designFault(body.design);
    if (rule) {
      const prose: Record<typeof rule.fault, string> = {
        duplicate: "Two blocks share an id.",
        "link-before-sign-in": "Links only work after sign-in.",
        required: "A button needs a link.",
        "not-a-link": "Not a web address, phone number or email address.",
        "ends-before-start": "Ends before it starts.",
      };
      return problem(c, 422, {
        type: "/problems/validation",
        title: "Page design not valid",
        status: 422,
        params: { fields: { design: rule.fault } },
        errors: { design: `Block ${rule.block + 1}: ${prose[rule.fault]}` },
      });
    }
  }

  /* **The difference, not the request.** A PATCH names every field the console holds,
     so deciding from what the body carried recorded a change to each of them on every
     save — including the save an operator makes after reading a form and pressing the
     button. `internal/api/audit_portal.go` is the same rule on the other side of the
     seam, and an empty difference writes no entry there at all. Taken here, before
     anything below writes to `s`, and compared against the value that write leaves. */
  const was = { ...s };
  const wasTerms = { ...(s.terms?.summary ?? {}) };

  if (body.venueName !== undefined) s.venueName = body.venueName.trim();
  if (body.networkName !== undefined) s.networkName = body.networkName;
  if (body.logoDataUri !== undefined) s.logoDataUri = body.logoDataUri;
  /* Terms-only access with no duration is public internet handed out on one tap.
     Checked against the *resulting* state, not the patch, so turning the method on
     in one request and the minutes off in another cannot slip between them. */
  const nextMethods = body.methods ?? s.methods;
  const nextMinutes =
    body.clickThroughMinutes !== undefined ? body.clickThroughMinutes : s.clickThroughMinutes;
  /* Phone sign-in only where a provider can send — `internal/api` refuses the same
     way. A button that cannot send a code is a control that lies. */
  if (body.methods?.includes("phone") && !phoneSignInFor(siteId).credentialsSet) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Phone sign-in is not set up",
      status: 422,
      params: { fields: { methods: "needs-phone-provider" } },
      errors: { methods: "Configure phone sign-in before offering it." },
    });
  }
  if (nextMethods.includes("click-through") && !nextMinutes) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "No limit on terms-only access",
      status: 422,
      /* `required-with-click-through` and not `required`: the field is only
         mandatory because another field is set, and a client whose copy said
         "Required." would be telling an operator to fill in a box that is optional
         the moment they turn terms-only access back off. */
      params: { fields: { clickThroughMinutes: "required-with-click-through" } },
      errors: { clickThroughMinutes: "Required when terms-only access is offered." },
    });
  }

  if (body.languages !== undefined) s.languages = body.languages;
  if (body.methods !== undefined) s.methods = body.methods;
  if (body.clickThroughMinutes !== undefined) s.clickThroughMinutes = body.clickThroughMinutes;
  if (body.design !== undefined) s.design = body.design;

  const settingsChanges: Record<string, { from: unknown; to: unknown }> = {};
  for (const f of ["venueName", "networkName", "logoDataUri", "languages", "methods", "clickThroughMinutes", "design"] as const) {
    if (body[f] !== undefined && !sameValue(was[f], s[f])) {
      settingsChanges[f] = { from: was[f] ?? null, to: s[f] ?? null };
    }
  }
  const termsChanges: Record<string, { from: unknown; to: unknown }> = {};
  if (body.terms?.summary !== undefined) {
    /* Empty strings are removals, not empty terms. A language whose box an operator
       cleared has no terms, and the resolution order has to see an absent key rather
       than a present empty one — otherwise it resolves to "" and renders nothing
       where it should have fallen through. */
    const next: Record<string, string> = {};
    for (const [lang, text] of Object.entries(body.terms.summary)) {
      if (typeof text === "string" && text.trim()) next[lang] = text;
    }
    for (const lang of new Set([...Object.keys(wasTerms), ...Object.keys(next)])) {
      if ((wasTerms[lang] ?? "") !== (next[lang] ?? "")) {
        termsChanges[`terms.summary.${lang}`] = { from: wasTerms[lang] ?? "", to: next[lang] ?? "" };
      }
    }
    s.terms = { summary: next };
  }

  /*
   * Two kinds from one operation, and that is not a split for tidiness: the terms are
   * the venue's legal text and the consent boundary a guest agrees to, and "somebody
   * changed the terms in March" is a different question from "somebody renamed the
   * venue". A single `portal-settings-changed` would bury the first inside the
   * second, on a screen that exists to answer exactly that kind of question.
   *
   * Both carry `changed: {field: {from, to}}`, the shape `network-settings-changed`
   * and `maintenance-settings-changed` already have, because a row that cannot say
   * what moved cannot be corrected by a later row.
   */
  if (Object.keys(termsChanges).length > 0) {
    audit(c, {
      kind: "portal-terms-changed",
      siteId,
      params: { changed: termsChanges },
      subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
    });
  }
  if (Object.keys(settingsChanges).length > 0) {
    audit(c, {
      kind: "portal-settings-changed",
      siteId,
      params: { changed: settingsChanges },
      subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
    });
  }

  return c.json(conformResponse("updatePortalSettings", 200, settingsResponse(siteId)));
});

api.put("/sites/:siteId/portal/hero-image", async (c) => {
  const siteId = c.req.param("siteId");
  if (!world.portalSettings.some((p) => p.siteId === siteId)) return notFound(c);
  const s = settingsFor(siteId);
  const body = (await c.req.json()) as HeroImageUpload;

  if (!body?.dataBase64) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "No image",
      status: 422,
      params: { fields: { dataBase64: "required" } },
      errors: { dataBase64: "Required." },
    });
  }
  if (!["image/jpeg", "image/png", "image/webp"].includes(body.contentType)) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Unsupported format",
      status: 422,
      /* No SVG. It is a document that can carry script and external references, and
         this one renders on a pre-authentication page. */
      params: { fields: { contentType: "unsupported-image-format" } },
      errors: { contentType: "JPEG, PNG or WebP." },
    });
  }

  const bytes = Buffer.from(body.dataBase64, "base64").length;
  if (bytes > IMAGE_MAX_BYTES) {
    /* The server is the first gate; `tools/check-budget.mjs` is the second and only
       ever sees the built tree, which an operator uploading here never touches. */
    return problem(c, 413, {
      type: "/problems/too-large",
      title: "Image too large",
      status: 413,
      params: {
        fields: { dataBase64: "too-large" },
        fieldParams: { dataBase64: { bytes, limitBytes: IMAGE_MAX_BYTES } },
        bytes,
        limitBytes: IMAGE_MAX_BYTES,
      },
      errors: { dataBase64: `${bytes} B is over the ${IMAGE_MAX_BYTES} B limit.` },
    });
  }

  const image: HeroImage = {
    url: `/venue/${siteId}/hero-${Math.random().toString(36).slice(2, 8)}`,
    bytes,
    contentType: body.contentType,
    uploadedAt: new Date().toISOString(),
  };
  s.heroImage = image;
  heroBlobs.set(image.url, {
    body: Uint8Array.from(Buffer.from(body.dataBase64, "base64")),
    type: body.contentType,
  });
  /* The hero image is a portal setting; it has its own operation only because it
     carries bytes. A separate `AuditKind` for it would be a kind the contract's
     list does not have. */
  audit(c, {
    kind: "portal-settings-changed",
    siteId,
    params: { fields: ["heroImage"], bytes },
    subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
  });
  return c.json(conformResponse("putPortalHeroImage", 200, image));
});

api.get("/sites/:siteId/portal/phone", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  return c.json(conformResponse("getPhoneSignIn", 200, phoneSignInFor(siteId)));
});

api.put("/sites/:siteId/portal/phone", async (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  const body = (await c.req.json()) as PhoneSignInUpdate;
  const shape = requestFault("PhoneSignInUpdate", body, "");
  if (shape) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Phone sign-in not valid",
      status: 422,
      params: { fields: { [shape.field]: shape.fault } },
      errors: { [shape.field]: shape.message },
    });
  }
  const result = setPhoneSignIn(siteId, body);
  if (!result.ok) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Phone sign-in not valid",
      status: 422,
      params: { fields: result.faults },
      errors: result.errors,
    });
  }
  /* Whether the keys changed, never what they are. */
  audit(c, {
    kind: "portal-settings-changed",
    siteId,
    params: { section: "phone-sign-in", credentialsChanged: result.credentialsChanged },
    subject: { kind: "site", id: siteId },
  });
  return c.json(conformResponse("updatePhoneSignIn", 200, result.settings));
});

/**
 * A schema refusal for the messaging settings, with the figure its sentence needs.
 *
 * Every minimum in those four schemas is one (`PhoneAbuseLimitsUpdate`,
 * `PhoneAbuseControlsUpdate.mobilePrefixes`, `PhoneSpendCapUpdate`,
 * `SiteTimeZoneUpdate`), so `below-minimum` carries `minimumCount: 1`; `too-large`
 * carries what `requestFault` measured. The field is named without the leading dot
 * an empty prefix leaves.
 */
function settingsShape(shape: NonNullable<ReturnType<typeof requestFault>>) {
  const field = shape.field.replace(/^\./, "");
  const figures = shape.fault === "below-minimum" ? { minimumCount: 1 } : shape.figures;
  return {
    params: { fields: { [field]: shape.fault }, ...(figures ? { fieldParams: { [field]: figures } } : {}) },
    errors: { [field]: shape.message },
  };
}

/* The admin-configurability rule: the abuse limits, the test message
   and §9's deployment-wide cap — `internal/api/handlers_phone.go`'s twins. */
api.get("/sites/:siteId/portal/phone/abuse-controls", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  return c.json(conformResponse("getPhoneAbuseControls", 200, phoneAbuseControlsFor(siteId)));
});

api.put("/sites/:siteId/portal/phone/abuse-controls", async (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  const body = (await c.req.json()) as PhoneAbuseControlsUpdate;
  const shape = requestFault("PhoneAbuseControlsUpdate", body, "");
  if (shape) {
    return problem(c, 422, { type: "/problems/validation", title: "Limits not valid", status: 422, ...settingsShape(shape) });
  }
  const result = setPhoneAbuseControls(siteId, body);
  if (!result.ok) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Limits not valid",
      status: 422,
      params: { fields: result.faults, ...(result.fieldParams ? { fieldParams: result.fieldParams } : {}) },
      errors: result.errors,
    });
  }
  audit(c, {
    kind: "portal-settings-changed",
    siteId,
    params: { section: "phone-abuse-controls", fields: ["limits", "mobilePrefixes"] },
    subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
  });
  return c.json(conformResponse("updatePhoneAbuseControls", 200, result.controls));
});

api.post("/sites/:siteId/portal/phone/test-message", async (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  const body = (await c.req.json()) as { phone?: unknown };
  const shape = requestFault("PhoneTestMessageRequest", body, "");
  if (shape) {
    return problem(c, 422, { type: "/problems/validation", title: "Number not valid", status: 422, ...settingsShape(shape) });
  }
  const result = sendTestMessage(siteId, String(body.phone));
  /* The outcome, never the number. */
  audit(c, {
    kind: "portal-settings-changed",
    siteId,
    params: { section: "phone-test-message", fields: ["testMessage"], outcome: result.outcome },
    subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
  });
  return c.json(conformResponse("sendPhoneTestMessage", 200, result));
});

api.get("/phone-sign-in/spend-cap", (c) => c.json(conformResponse("getPhoneSpendCap", 200, phoneSpendCap())));

api.put("/phone-sign-in/spend-cap", async (c) => {
  const body = (await c.req.json()) as { dailyLimit?: number | null };
  const shape = requestFault("PhoneSpendCapUpdate", body, "");
  if (shape) {
    return problem(c, 422, { type: "/problems/validation", title: "Cap not valid", status: 422, ...settingsShape(shape) });
  }
  const before = phoneSpendCap().dailyLimit;
  const after = setPhoneSpendCap(body.dailyLimit ?? null);
  audit(c, {
    kind: "phone-spend-cap-changed",
    siteId: null,
    params: { from: before, to: after.dailyLimit },
    subject: { kind: "phone-spend-cap", id: "phone-spend-cap" },
  });
  return c.json(conformResponse("updatePhoneSpendCap", 200, after));
});

/* A site's calendar — `handlers_sitetime.go`'s twin. */
api.get("/sites/:siteId/time-zone", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  return c.json(conformResponse("getSiteTimeZone", 200, siteTimeZone(siteId)));
});

api.put("/sites/:siteId/time-zone", async (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  const body = (await c.req.json()) as { timeZone?: unknown };
  if (typeof body.timeZone !== "string" || !validTimeZone(body.timeZone)) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Time zone not valid",
      status: 422,
      params: { fields: { timeZone: "not-a-time-zone" } },
      errors: { timeZone: "Choose a time zone from the list." },
    });
  }
  const before = siteTimeZone(siteId).timeZone;
  const after = setSiteTimeZone(siteId, body.timeZone);
  audit(c, {
    kind: "site-time-zone-changed",
    siteId,
    params: { from: before, to: after.timeZone },
    subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
  });
  return c.json(conformResponse("updateSiteTimeZone", 200, after));
});

/* The guest network's Wi-Fi password. Write-only on these two; the voucher slip's
   read below is the one that carries it. */
api.get("/sites/:siteId/guest-wifi-key", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  return c.json(conformResponse("getGuestWifiKey", 200, guestWifiKeyStatus(siteId)));
});

api.put("/sites/:siteId/guest-wifi-key", async (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  const body = (await c.req.json()) as { passphrase?: string | null };
  const passphrase = body?.passphrase ?? null;
  if (passphrase !== null && !validWifiPassphrase(passphrase)) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      params: { fields: { passphrase: "not-a-wifi-passphrase" } },
      errors: { passphrase: "Not a Wi-Fi password." },
    });
  }
  setGuestWifiKey(siteId, passphrase, sessionOf(c)?.operator.username ?? null);
  audit(c, {
    kind: "portal-settings-changed",
    siteId,
    params: { fields: ["guestWifiKey"], cleared: passphrase === null },
    subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
  });
  return c.json(conformResponse("updateGuestWifiKey", 200, guestWifiKeyStatus(siteId)));
});

api.get("/sites/:siteId/voucher-slip-wifi", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  return c.json(conformResponse("getVoucherSlipWifi", 200, { passphrase: slipPassphrase(siteId) }));
});

api.delete("/sites/:siteId/portal/hero-image", (c) => {
  const siteId = c.req.param("siteId");
  if (!world.portalSettings.some((p) => p.siteId === siteId)) return notFound(c);
  const s = settingsFor(siteId);
  if (!s.heroImage) return notFound(c);
  heroBlobs.delete(s.heroImage.url);
  s.heroImage = null;
  audit(c, {
    kind: "portal-settings-changed",
    siteId,
    params: { fields: ["heroImage"], removed: true },
    subject: { kind: "site", id: siteId, name: site(siteId)?.name ?? siteId },
  });
  return c.body(null, 204);
});

/**
 * The uploaded bytes, served from the same origin the portal is on.
 *
 * Outside `/api/v0` because it is not an API operation — it is the asset itself, at
 * the path `HeroImage.url` names, which is what makes the "linked same-origin" claim
 * true rather than aspirational. In production this is the Go binary's to serve.
 */
const heroBlobs = new Map<string, { body: Uint8Array; type: string }>();

app.get("/venue/:siteId/:name", (c) => {
  const blob = heroBlobs.get(`/venue/${c.req.param("siteId")}/${c.req.param("name")}`);
  if (!blob) return c.notFound();
  return c.body(blob.body as unknown as ArrayBuffer, 200, {
    "content-type": blob.type,
    "cache-control": "no-store",
  });
});

// ── walled garden ────────────────────────────────────────────────────────────

/**
 * The garden, and what actually honours it.
 *
 * `effectiveKind` is derived here rather than stored, from the **weakest** garden
 * any enabled device in the site can enforce. Storing it would let it drift away
 * from the devices it describes on the first registration, and the whole value of
 * the field is that it cannot.
 */
function gardenFor(siteId: string) {
  const drivers: GardenDriverCoverage[] = world.nas
    .filter((n) => n.siteId === siteId && n.enabled)
    .map((n) => ({
      nasId: n.id,
      name: n.name,
      walledGarden: n.capabilities.walledGarden,
    }));

  return {
    siteId,
    entries: world.gardenEntries.filter((e) => e.siteId === siteId),
    drivers,
    effectiveKind: weakestGarden(drivers.map((d) => d.walledGarden)),
  };
}

api.get("/sites/:siteId/walled-garden", (c) => {
  const siteId = c.req.param("siteId");
  if (!world.sites.some((s) => s.id === siteId)) return notFound(c);
  return c.json(conformResponse("getWalledGarden", 200, gardenFor(siteId)));
});

api.post("/sites/:siteId/walled-garden", async (c) => {
  const siteId = c.req.param("siteId");
  if (!world.sites.some((s) => s.id === siteId)) return notFound(c);

  const body = (await c.req.json()) as GardenEntryCreate;
  const value = body?.value?.trim() ?? "";

  if (!value) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Missing value",
      status: 422,
      params: { fields: { value: "required" } },
      errors: { value: "Required." },
    });
  }

  const kind = body.kind === "ip-range" ? "ip-range" : "fqdn";

  if (kind === "fqdn" && !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i.test(value)) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Not a hostname",
      status: 422,
      /* One of the three faults on this one field that made `FieldFault` necessary:
         `required`, `not-a-hostname` and `not-a-cidr` all arrive as
         `(/problems/validation, value)`. */
      params: { fields: { value: "not-a-hostname" } },
      errors: { value: "Enter a hostname, for example payments.example.com." },
    });
  }
  if (kind === "ip-range" && !/^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/.test(value)) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Not a CIDR range",
      status: 422,
      params: { fields: { value: "not-a-cidr" } },
      errors: { value: "Enter a range, for example 192.0.2.0/24." },
    });
  }

  if (world.gardenEntries.some((e) => e.siteId === siteId && e.value.toLowerCase() === value.toLowerCase())) {
    return problem(c, 409, {
      type: "/problems/duplicate",
      title: "Already allowed",
      status: 409,
      params: { value },
    });
  }

  const entry: GardenEntry = {
    id: `garden-${Math.random().toString(36).slice(2, 10)}`,
    siteId,
    kind,
    value,
    ...(body.note?.trim() ? { note: body.note.trim() } : {}),
    origin: "operator",
    createdAt: new Date().toISOString(),
  };
  world.gardenEntries.push(entry);
  audit(c, {
    kind: "garden-entry-added",
    siteId,
    params: { value: entry.value, kind: entry.kind },
    subject: { kind: "garden-entry", id: entry.id, name: entry.value },
  });
  return c.json(conformResponse("createGardenEntry", 201, entry), 201);
});

api.delete("/sites/:siteId/walled-garden/:entryId", (c) => {
  const siteId = c.req.param("siteId");
  const i = world.gardenEntries.findIndex(
    (e) => e.siteId === siteId && e.id === c.req.param("entryId"),
  );
  if (i < 0) return notFound(c);

  /* The second gate. The console does not render a delete control for a required
     entry at all; this is what answers a client that tries anyway. */
  if (world.gardenEntries[i]!.origin === "required") {
    return problem(c, 409, {
      type: "/problems/garden-entry-required",
      title: "Required by the portal",
      status: 409,
      params: { value: world.gardenEntries[i]!.value },
    });
  }

  const removed = world.gardenEntries.splice(i, 1)[0]!;
  /* The walled garden is unauthenticated egress, so every change to it is a change
     to what a pre-authentication client can reach — `docs/rules/security.md` §5.
     A removal that broke the portal in March is the entry somebody is reading this
     log to find. */
  audit(c, {
    kind: "garden-entry-removed",
    siteId,
    params: { value: removed.value, kind: removed.kind },
    subject: { kind: "garden-entry", id: removed.id, name: removed.value },
  });
  return c.body(null, 204);
});


// ── controllers ──────────────────────────────────────────────────────────────

/**
 * The minimum controller version each driver's integration needs.
 *
 * Sourced, not invented: Ubiquiti's official integration API is documented as
 * requiring Network Application 9.1.105 (March 2025), and below it the API-key
 * screen does not exist — so the credential an operator was told to create cannot be
 * created, and the failure presents as "the key is wrong". Omada's floor is the
 * external-portal API revision, 5.0.15. Ruckus has no sourced floor yet, which is
 * why it is null rather than a guess (`docs/protocol-reality.md`).
 */
const controllerById = (id: string) => world.controllers.find((x) => x.id === id);

api.get("/controllers", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  /* A controller is not scoped to one Foyer site — it manages many, and the binding
     is per *vendor* site. So the filter is "does this controller serve this site",
     not "was it created in it", and a controller nothing has adopted yet still shows
     or it disappears between registration and discovery.

     `controllerServesSite` is `@foyer/fixtures`', and imported rather than written
     here for the reason that function's own comment gives: four copies of the rule
     drift into two different rules, and a drifted copy here lists a controller
     adopted into the café on the hotel's screen because one of its vendor sites is
     free. */
  const items = world.controllers.filter((x) => controllerServesSite(x, siteId));
  return c.json(conformResponse("listControllers", 200, items));
});

api.post("/controllers", async (c) => {
  const body = (await c.req.json()) as ControllerCreate;
  const refused = fieldRefusals();

  if (!body?.name?.trim()) refused.add("name", "required", "Required.");
  if (!body?.baseUrl?.trim()) refused.add("baseUrl", "required", "Required.");
  else if (!/^https?:\/\/[^\s/]+$/i.test(body.baseUrl.trim().replace(/\/+$/, ""))) {
    /* `not-an-origin`, not `not-a-hostname`. The garden's hostname refusal already
       owns that fault and its copy says "enter a hostname, for example
       payments.example.com", which is the wrong instruction for a field that wants
       a scheme and a port. */
    refused.add(
      "baseUrl",
      "not-an-origin",
      "Scheme and host only, for example https://192.0.2.10:8443.",
    );
  }
  /* Omada cannot be reached without it and it cannot be derived. Asking for it late
     is what makes the first attempt fail with a message about credentials. */
  if (body?.vendor === "omada" && !body.controllerId?.trim()) {
    refused.add(
      "controllerId",
      "required",
      "Required for Omada — the 32 characters from your controller's URL.",
    );
  }
  if (body?.authMode === "api-key" && !body.apiKey?.trim()) {
    refused.add("apiKey", "required", "Required.");
  }
  if (body?.authMode !== "api-key" && !body?.username?.trim()) {
    refused.add("username", "required", "Required.");
  }
  if (body?.authMode !== "api-key" && !body?.password) {
    refused.add("password", "required", "Required.");
  }

  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Controller not registered",
      status: 422,
      ...refused.problem(),
    });
  }

  const controller: Controller = {
    id: `ctl-${Math.random().toString(36).slice(2, 10)}`,
    name: body.name.trim(),
    vendor: body.vendor,
    baseUrl: body.baseUrl.trim().replace(/\/+$/, ""),
    controllerId: body.controllerId?.trim() || null,
    authMode: body.authMode,
    tlsMode: body.tlsMode,
    /* Stored, never returned. The same write-only rule as a RADIUS shared secret,
       and for a stronger reason: this one is a password to somebody's network. */
    credentialSet: true,
    certificateFingerprint: null,
    /* Not `unknown` by accident. Nothing has reached it, and a console that showed a
       freshly registered controller as reachable would be reporting a measurement
       nobody took. */
    reachability: "unknown",
    version: null,
    versionSupported: null,
    vendorSites: [],
    lastContactAt: null,
    createdAt: new Date().toISOString(),
  };
  world.controllers.push(controller);
  audit(c, {
    kind: "controller-registered",
    siteId: null,
    params: { controllerName: controller.name, vendor: controller.vendor },
    subject: { kind: "controller", id: controller.id, name: controller.name },
  });
  return c.json(conformResponse("createController", 201, controller), 201);
});

api.get("/controllers/:controllerId", (c) => {
  const ctl = controllerById(c.req.param("controllerId"));
  return ctl ? c.json(conformResponse("getController", 200, ctl)) : notFound(c);
});

api.patch("/controllers/:controllerId", async (c) => {
  const ctl = controllerById(c.req.param("controllerId"));
  if (!ctl) return notFound(c);
  const body = (await c.req.json()) as ControllerUpdate;
  if (typeof body.name === "string" && body.name.trim()) ctl.name = body.name.trim();
  if (typeof body.baseUrl === "string" && body.baseUrl.trim()) {
    ctl.baseUrl = body.baseUrl.trim().replace(/\/+$/, "");
    /* The address moved, so nothing measured about the old one still holds. Keeping
       `reachability: ok` here would report a live controller at an address nothing
       has ever answered on. */
    ctl.reachability = "unknown";
    ctl.version = null;
    ctl.versionSupported = null;
  }
  if (body.controllerId !== undefined) ctl.controllerId = body.controllerId;
  if (body.tlsMode) ctl.tlsMode = body.tlsMode;
  if (body.apiKey || body.password) {
    ctl.credentialSet = true;
    ctl.reachability = "unknown";
  }
  /*
   * `controller-updated` is in `AuditKind` because this operation can replace the
   * stored credential, and without a kind of its own nothing could legally record
   * that.
   *
   * `changed` is read off the request rather than diffed against the stored object,
   * so it names what the operator sent. **No value travels** — `apiKey` and
   * `password` are write-only in this contract and a log read months later is the
   * last place either should reappear. The order is fixed rather than the body's key
   * order, because two identical changes must produce two identical rows.
   */
  const changed = (["name", "baseUrl", "controllerId", "tlsMode", "apiKey", "password"] as const)
    .filter((k) => body[k] !== undefined)
    .map(String);
  audit(c, {
    kind: "controller-updated",
    siteId: null,
    params: { controllerName: ctl.name, changed },
    subject: { kind: "controller", id: ctl.id, name: ctl.name },
  });
  return c.json(conformResponse("updateController", 200, ctl));
});

api.delete("/controllers/:controllerId", (c) => {
  const id = c.req.param("controllerId");
  const i = world.controllers.findIndex((x) => x.id === id);
  if (i < 0) return notFound(c);

  const adopted = world.nas.filter((n) => n.controllerId === id);
  if (adopted.length > 0) {
    /* Orphaned access points are worse than a refusal. They stay in `clients.conf`,
       so RADIUS still answers for them, while the console can no longer authorize a
       guest on any of them and no longer knows where they came from. */
    return problem(c, 409, {
      type: "/problems/controller-in-use",
      title: "Access points still adopted",
      status: 409,
      /* The count *is* the message, and this refusal is why `params` had to exist
         rather than being a nicety: without it the number can only live in `detail`,
         which is the field the client must not render — so the client would reach
         for the fallback on exactly the refusals an operator most needs to act on,
         and the fallback would quietly become the real path. */
      params: { count: adopted.length },
    });
  }
  const removed = world.controllers.splice(i, 1)[0]!;
  audit(c, {
    kind: "controller-deleted",
    siteId: null,
    params: { controllerName: removed.name, orphanedNas: 0 },
    subject: { kind: "controller", id: removed.id, name: removed.name },
  });
  return c.body(null, 204);
});

/**
 * The warnings one probe found, derived from the same values the answer already
 * carries — never a list typed beside the route.
 *
 * `access-point-unconfirmed` is **unconditional on UniFi**, and that is the whole
 * reason `NasProbe.warnings` exists: the shipped driver appends it on every run
 * because the Integration API's client object carries no field to compare the
 * claimed access point against, so three of `docs/rules/security.md` §4's four
 * checks are made and the fourth is not. A registration screen that renders a green
 * tick over it is telling an operator a check happened.
 *
 * The other two are computed from `versionSupported` and from `unverified`, so a
 * controller that stops being below the floor stops warning about it without
 * anybody editing this — the shape `tools/codegen/differ` learned the hard way when
 * a hardcoded divergence outlived its defect.
 */
function probeWarnings(opts: {
  vendor: Controller["vendor"];
  versionSupported: boolean | null;
  version: string | null;
  minimumVersion: string | null;
  unverified: readonly string[];
}): ProbeWarning[] {
  const warnings: ProbeWarning[] = [];
  if (opts.vendor === "unifi") {
    warnings.push({
      kind: "access-point-unconfirmed",
      params: { vendor: opts.vendor },
    });
  }
  if (opts.versionSupported === false) {
    warnings.push({
      kind: "version-below-floor",
      params: { version: opts.version, minimumVersion: opts.minimumVersion },
    });
  }
  if (opts.unverified.length > 0) {
    warnings.push({
      kind: "capabilities-unverified",
      params: { count: opts.unverified.length },
    });
  }
  return warnings;
}

api.post("/controllers/:controllerId/probe", (c) => {
  const ctl = controllerById(c.req.param("controllerId"));
  if (!ctl) return notFound(c);

  const reachable = !ctl.name.startsWith(UNREACHABLE_PREFIX) && ctl.credentialSet;
  const credentialOk = reachable && ctl.reachability !== "credentials-expired";
  /* The floor is `@foyer/fixtures`' to state, not this server's. `deriveAlarms` and
     the fixture guard read it too, and a second copy is how the probe dialog and the
     dashboard end up disagreeing about the same controller. */
  const minimum = CONTROLLER_MINIMUM_VERSION[ctl.vendor];

  if (!reachable) {
    ctl.reachability = "unreachable";
    const probe: ControllerProbe = {
      reachability: "unreachable",
      version: ctl.version,
      /* Not `false`. Nothing was measured, and reporting "unsupported" for a box
         nobody reached sends the operator to upgrade software that may be fine. */
      versionSupported: null,
      minimumVersion: minimum,
      flavour: null,
      vendorSites: [],
      certificateFingerprint: ctl.certificateFingerprint ?? null,
      /* Nothing was reached, so nothing was warned about. **Empty, not omitted** —
         the field is required precisely so that *we did not look* cannot arrive as
         `undefined` and be read as *there was nothing wrong*. The capability caveats
         are still named: they are a property of the driver rather than of the run,
         and they are as true of a controller nobody reached as of one that answered. */
      warnings: [],
      unverified: [...UNVERIFIED_CAPABILITIES[ctl.vendor]],
      checks: [
        { kind: "reachable", result: "fail" },
        { kind: "tls", result: "skip" },
        { kind: "credential", result: "skip" },
        { kind: "version", result: "skip" },
        { kind: "sites", result: "skip" },
        { kind: "guest-authorization", result: "skip" },
        { kind: "presence-check", result: "skip" },
      ],
    };
    return c.json(conformResponse("probeController", 200, probe));
  }

  ctl.reachability = credentialOk ? "ok" : "credentials-expired";
  ctl.lastContactAt = new Date().toISOString();
  /* Read off the box, so it is spelled the way that vendor spells it. A Ruckus that
     fell through to the Omada string would report a SmartZone running `5.14.26` — a
     version number that vendor does not issue, in the dialog whose whole subject is
     which version this is. */
  if (credentialOk && !ctl.version) {
    ctl.version =
      ctl.vendor === "unifi" ? "9.4.19" : ctl.vendor === "omada" ? "5.14.26" : "6.1.2.0.769";
  }
  if (credentialOk) {
    /*
     * Read off the controller and compared with the floor — **not** asserted because
     * the credential worked.
     *
     * The form `credentialOk ? true : …` would make the state unreachable from every
     * direction at once: no fixture would carry `false` for long, because the one
     * path that could produce it would overwrite `false` with `true` the moment
     * anybody pressed Probe. A fixture that heals itself is worse than a static one,
     * and that would be a probe that healed somebody else's.
     */
    /*
     * `null` propagates. It is *no floor established for this vendor* — Ruckus, where
     * `docs/protocol-reality.md` records the version floor as an open question. Reading
     * `met === null ? true : met` here would make the probe answer **supported** on a
     * comparison it had not been able to make.
     *
     * That is the same error as the one the unreachable branch above refuses, pointing
     * the other way. Reporting *unsupported* for a box nobody reached sends the
     * operator to upgrade software that may be fine; reporting *supported* against a
     * floor nobody has measured tells them the one thing they opened this dialog to
     * check is fine when nothing checked it. `null` already means *not established* in
     * this model and the console already renders it as such, so the third value costs
     * nothing and is the only true answer.
     */
    ctl.versionSupported = versionMeetsMinimum(ctl.version, minimum);
  }
  if (credentialOk && ctl.vendorSites.length === 0) {
    ctl.vendorSites = [{ id: "default", name: "Default", boundSiteId: null }];
  }
  if (ctl.tlsMode === "trust-on-first-use" && !ctl.certificateFingerprint) {
    ctl.certificateFingerprint = "3C:7E:00:A1:45:B2:9D:68:1F:04:CE:22:80:5A:F7:31";
  }

  const probe: ControllerProbe = {
    reachability: ctl.reachability,
    version: ctl.version,
    versionSupported: credentialOk ? ctl.versionSupported : null,
    minimumVersion: minimum,
    flavour:
      ctl.vendor === "unifi" ? "UniFi OS console (/proxy/network)" : ctl.vendor === "omada" ? "Omada SDN controller" : null,
    vendorSites: ctl.vendorSites.map((v) => ({ id: v.id, name: v.name })),
    certificateFingerprint: ctl.certificateFingerprint ?? null,
    warnings: probeWarnings({
      vendor: ctl.vendor,
      versionSupported: credentialOk ? (ctl.versionSupported ?? null) : null,
      version: ctl.version ?? null,
      minimumVersion: minimum,
      unverified: UNVERIFIED_CAPABILITIES[ctl.vendor],
    }),
    unverified: [...UNVERIFIED_CAPABILITIES[ctl.vendor]],
    checks: [
      { kind: "reachable", result: "pass" },
      { kind: "tls", result: "pass" },
      { kind: "credential", result: credentialOk ? "pass" : "fail" },
      /* The check the pill on the list is drawn from, so the two cannot disagree:
         a controller the dashboard calls unsupported must not show a green tick in
         the dialog the operator opens to find out why. `skip` is the third answer and
         it belongs to the same claim — a controller the dashboard calls *not
         established* must not show a green tick here either, and `skip` is the word
         this list already has for a check that was not made. Reachable on Ruckus,
         where the floor is an open question in `docs/protocol-reality.md`. */
      {
        kind: "version",
        result: !credentialOk
          ? "skip"
          : ctl.versionSupported === false
            ? "fail"
            : ctl.versionSupported === null
              ? "skip"
              : "pass",
      },
      { kind: "sites", result: credentialOk ? "pass" : "skip" },
      /* The check the contract calls *what Foyer is here for*, and it cannot pass on
         a version whose driver API is not there. `fail` rather than `skip`, on the
         same argument the Omada presence-check below is written on: a capability
         that is absent is a finding, and a green tick here would tell an operator
         the one thing they came to this dialog to check is fine. Reachable only on a
         controller below the floor, which the fixtures carry for exactly this. */
      {
        kind: "guest-authorization",
        result: !credentialOk ? "skip" : ctl.versionSupported === false ? "fail" : "pass",
      },
      /*
       * The check that decides whether a driver may ship at all.
       *
       * `security.md` §4 makes `VerifyPresence` mandatory before any grant, and the
       * Omada external-portal API has **no documented way to ask** whether a MAC is
       * associated. Reporting that as a fail on the screen is the honest thing: it is
       * a product finding, and hiding it behind a green tick is how a driver ships
       * that authorizes MACs which never associated.
       */
      {
        kind: "presence-check",
        result: !credentialOk ? "skip" : ctl.vendor === "omada" ? "fail" : "pass",
      },
    ],
  };
  return c.json(conformResponse("probeController", 200, probe));
});

api.post("/controllers/:controllerId/discover", async (c) => {
  const ctl = controllerById(c.req.param("controllerId"));
  if (!ctl) return notFound(c);
  if (ctl.reachability !== "ok") {
    return problem(c, 409, {
      type: "/problems/controller-unreachable",
      title: "Cannot read from this controller",
      status: 409,
      /* `reachability` and not just the name: never-probed, unreachable and
         expired-credentials are three different afternoons, and the console's copy
         for each names a different next step. */
      params: { name: ctl.name, reachability: ctl.reachability },
    });
  }

  const body = (await c.req.json().catch(() => ({}))) as {
    siteId?: string;
    vendorSiteId?: string;
  };
  const siteId = body.siteId ?? "site-hcm";
  const vendorSiteId = body.vendorSiteId ?? ctl.vendorSites[0]?.id ?? "default";
  const secretId = `sec-${ctl.id}`;

  /* One secret for every device under this controller. See `discoverFromController`
     in the contract: the secret lives in a controller-level RADIUS profile. */
  const existing = world.nas.filter((n) => n.controllerId === ctl.id);
  const reported = existing.length > 0 ? existing.length : 2;

  let adopted = 0;
  const accessPointIds: string[] = [];
  for (let i = 0; i < reported; i += 1) {
    const mac = `94:2a:6f:${ctl.id.slice(-2)}:${String(i).padStart(2, "0")}:0${i}`;
    let device = world.nas.find((n) => n.controllerId === ctl.id && n.mac === mac);
    if (!device && i >= existing.length) {
      device = createNas({ siteId, name: `${ctl.name} · AP ${i + 1}`, driver: ctl.vendor });
      device.controllerId = ctl.id;
      device.source = "discovered";
      device.mac = mac;
      device.sharedSecretId = secretId;
      device.ipAddress = `10.20.0.${100 + world.nas.length}`;
      adopted += 1;
      world.nas.push(device);
    }
    if (device) accessPointIds.push(device.id);
  }

  for (const n of existing) {
    n.source = "discovered";
    n.sharedSecretId = secretId;
    n.lastSeenAt = new Date().toISOString();
    if (!accessPointIds.includes(n.id)) accessPointIds.push(n.id);
  }

  const site = ctl.vendorSites.find((v) => v.id === vendorSiteId);
  if (site) site.boundSiteId = siteId;

  const result: DiscoveryResult = {
    adopted,
    updated: existing.length,
    /* Never deleted on absence — an access point that dropped off for ten minutes is
       not the same fact as one an operator removed. */
    missing: 0,
    sharedSecretId: secretId,
    accessPointIds,
  };
  /*
   * One entry for one run, subject the **controller**.
   *
   * Not `nas-registered`, which would be wrong twice over: one entry claiming what may
   * be nine adoptions, and a device named as the subject of a change made to a
   * controller.
   * The counts are what make the row worth reading — *adopted nine* and *adopted
   * none* are different afternoons, and only the first needs anybody to go and look
   * at a shared secret.
   */
  audit(c, {
    kind: "controller-discovery-run",
    siteId,
    params: {
      controllerName: ctl.name,
      adopted: result.adopted,
      updated: result.updated,
      missing: result.missing,
    },
    subject: { kind: "controller", id: ctl.id, name: ctl.name },
  });
  syncAfterClientWrite(c, "controller-discovery");
  return c.json(conformResponse("discoverFromController", 200, result));
});

// ── nas ──────────────────────────────────────────────────────────────────────


api.get("/nas", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const query = (q(c, "q") ?? "").toLowerCase();
  let items = world.nas.filter((n) => n.siteId === siteId);
  if (query) {
    items = items.filter((n) =>
      [n.name, n.ipAddress, n.nasIdentifier, n.model, n.driver]
        .filter(Boolean)
        .some((f) => String(f).toLowerCase().includes(query)),
    );
  }
  const p = page(items, q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listNas", 200, p));
});

/**
 * One address or one range, v4 or v6 — what `radius_clients.address` accepts.
 *
 * Hand-written rather than pulled in, because this package has no IP parser and
 * `go.mod`'s frozen twin here is the dependency list. It is deliberately *strict
 * enough to refuse what PostgreSQL's `inet` refuses* and no stricter: the point is
 * that the console and the server agree about which values are acceptable, and a
 * validator that admits something the writer then rejects has moved the 500 rather
 * than removed it.
 */
function isClientAddress(raw: string): boolean {
  const [value, prefix, ...rest] = raw.split("/");
  if (rest.length > 0 || value === undefined) return false;
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(value);
  const isV4 = v4 !== null && v4.slice(1).every((o) => Number(o) <= 255 && String(Number(o)) === o);
  /* Not a full v6 grammar: one `::` at most, hex groups, and at least one colon.
     A v6 address an operator types is either right or obviously not. */
  const isV6 =
    !isV4 &&
    value.includes(":") &&
    (value.match(/::/g) ?? []).length <= 1 &&
    value.split(/:+/).every((g) => g === "" || /^[0-9A-Fa-f]{1,4}$/.test(g));
  if (!isV4 && !isV6) return false;
  if (prefix === undefined) return true;
  if (!/^\d{1,3}$/.test(prefix)) return false;
  return Number(prefix) <= (isV4 ? 32 : 128);
}

api.post("/nas", async (c) => {
  const body = (await c.req.json()) as Parameters<typeof createNas>[0];
  if (!body?.name?.trim()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Missing information",
      status: 422,
      params: { fields: { name: "required" } },
      errors: { name: "Required." },
    });
  }
  /*
   * **The address, refused here rather than left to the database.**
   *
   * `NasCreate.ipAddress` is required, matching `radius_clients.address`
   * (`inet NOT NULL`): an omitted one must not reach the column and come back as a 500
   * for a field the operator simply had not filled in. FreeRADIUS matches a packet to
   * a client by its source address and nothing else, so the address is genuinely
   * required, and both refusals are field-level so the console marks the field rather
   * than the form.
   */
  {
    const address = String(body?.ipAddress ?? "").trim();
    const faults = fieldRefusals();
    if (!address) {
      faults.add("ipAddress", "required", "Required.");
    } else if (!isClientAddress(address)) {
      /* Not `not-a-cidr`: that fault's copy asks for a *range*, which would tell an
         operator who typed `10.20.0.two` that a single address is unacceptable. */
      faults.add("ipAddress", "not-an-ip-address", "Not an address or a range.");
    }
    /*
     * And which member of the CoA family this is, for the one driver whose enum
     * value names a family rather than a vendor.
     *
     * Cisco and Aruba identify a session differently — an `audit-session-id`
     * AV-pair against a `Calling-Station-Id` in Aruba's own spelling — and RFC 5176
     * §2.3 makes the NAS match on every session-identification attribute in the
     * request, so the wrong one matches nothing. A careful device answers
     * `Error-Cause 503`; a careless one **ignores the request and acknowledges
     * it**, which is a revocation that reports success and revoked nobody.
     *
     * Hand-written here rather than falling out of the schema, because this mock
     * validates **responses** and not requests: `required` for one of five drivers
     * is not expressible as `required` on the body, and even if it were, nothing
     * would check it. Same reason `ipAddress` above is hand-written.
     */
    if (needsCoaVendor(body.driver) && !body.coaVendor) {
      faults.add("coaVendor", "required", "Required.");
    }
    if (faults.any()) {
      return problem(c, 422, {
        type: "/problems/validation",
        title: "Missing information",
        status: 422,
        ...faults.problem(),
      });
    }
  }
  /*
   * Refused for a driver that has a controller.
   *
   * Accepting this registration would be broken: a UniFi typed in
   * by hand is a device Foyer holds no credential for, cannot authorize a guest on,
   * and has minted a secret for that the hardware has no field to accept. Answering
   * 422 with the reason is better than accepting it and failing later on a screen
   * that cannot explain itself.
   */
  if (DRIVER_SHAPES[body.driver].controller === "required") {
    return problem(c, 422, {
      type: "/problems/needs-controller",
      title: "Register the controller instead",
      status: 422,
      /* The driver name travels, because the console's copy for this ends in a link
         to the controller screen for *that vendor* and cannot compose it otherwise. */
      params: { fields: { driver: "needs-controller" }, driver: body.driver },
      errors: { driver: "Registered through a controller." },
    });
  }

  const nas = createNas(body);
  audit(c, {
    kind: "nas-registered",
    siteId: nas.siteId,
    params: { nasName: nas.name, driver: nas.driver },
    subject: { kind: "nas", id: nas.id, name: nas.name },
  });
  syncAfterClientWrite(c, "nas-created");
  return c.json(conformResponse("createNas", 201, { nas, sharedSecret: generateSecret() }), 201);
});

api.get("/nas/:nasId", (c) => {
  const nas = world.nas.find((n) => n.id === c.req.param("nasId"));
  return nas ? c.json(conformResponse("getNas", 200, nas)) : notFound(c);
});

api.patch("/nas/:nasId", async (c) => {
  const nas = world.nas.find((n) => n.id === c.req.param("nasId"));
  if (!nas) return notFound(c);
  const body = (await c.req.json()) as Partial<Pick<Nas, "name" | "enabled">>;
  if (typeof body.name === "string") nas.name = body.name;
  if (typeof body.enabled === "boolean") nas.enabled = body.enabled;
  syncAfterClientWrite(c, "nas-updated");
  return c.json(conformResponse("updateNas", 200, nas));
});

api.delete("/nas/:nasId", (c) => {
  const i = world.nas.findIndex((n) => n.id === c.req.param("nasId"));
  if (i < 0) return notFound(c);
  const removed = world.nas.splice(i, 1)[0]!;
  /* `subject.name` is the only thing that will ever say which device this was: the
     row is gone, and the log outlives it. Same argument as `actorUsername`. */
  audit(c, {
    kind: "nas-deleted",
    siteId: removed.siteId,
    params: { nasName: removed.name },
    subject: { kind: "nas", id: removed.id, name: removed.name },
  });
  syncAfterClientWrite(c, "nas-deleted");
  return c.body(null, 204);
});

api.post("/nas/:nasId/probe", (c) => {
  const nas = world.nas.find((n) => n.id === c.req.param("nasId"));
  if (!nas) return notFound(c);
  return c.json(conformResponse("probeNas", 200, probeNas(nas)));
});

/*
 * `getNasEvidence` — how this device's claims are known.
 *
 * The CoA family ships with every capability UNVERIFIED, and the label has to reach
 * an operator rather than living in the code.
 *
 * **The tables are GENERATED from `internal/nas`, not written here.** A hand-typed
 * copy would be a fake asserting that a Cisco encoding is documented, inside a suite
 * whose whole premise is that no Cisco encoding here is measured against a device —
 * the *stale second copy* defect `docs/rules/testing.md` records. `make
 * generate-check` fails when it drifts.
 *
 * The two lists disagree and a client must not merge them. Measured against the
 * generated tables rather than claimed: the STANDARD attributes are `documented`
 * (RFCs), the vendor redirect attributes are `unverified` (a reading of a vendor
 * table no device has answered), and every capability of the CoA family is
 * `unverified`. The honest sentence is *we know what we will send and we do not
 * know what it will do*.
 */
api.get("/nas/:nasId/evidence", (c) => {
  const nas = world.nas.find((n) => n.id === c.req.param("nasId"));
  if (!nas) return notFound(c);

  /* `coaVendor` participates in the key only for the CoA family, exactly as
     `VendorForDriver` does in Go — the table is generated from that function, so
     this is a lookup rather than a second copy of the rule. */
  const key = nas.driver === "coa" ? `coa:${nas.coaVendor ?? ""}` : nas.driver;
  const vendor = VENDOR_BY_DRIVER[key];
  const entry = vendor ? VENDOR_EVIDENCE[vendor] : undefined;

  if (!entry) {
    /* A `coa` row with no vendor. Answered, not refused, and the emptiness has a
       state beside it: Cisco and Aruba identify a session differently and a wrong
       guess is an ACK with no effect, so "nobody said which vendor" has to be
       representable. Empty lists with nothing beside them would read as *this
       driver sends nothing*. */
    return c.json(
      conformResponse("getNasEvidence", 200, {
        profile: "vendor-not-chosen" as const,
        capabilities: [],
        attributes: [],
      }),
    );
  }

  return c.json(
    conformResponse("getNasEvidence", 200, {
      profile: "resolved" as const,
      capabilities: entry.capabilities,
      attributes: entry.attributes,
    }),
  );
});

/**
 * Whether `rotateNasSecret` must answer 409 for this access point.
 *
 * Exported so a test can ask the fixtures the question directly — "is there a NAS
 * that reaches this at all?" — rather than hoping one exists. A refusal condition
 * no fixture satisfies is a branch that never runs.
 */
export function rotationRefused(nas: Nas): boolean {
  return !nas.capabilities.rotateSecret && nas.reachability !== "ok";
}

api.post("/nas/:nasId/secret", (c) => {
  const nas = world.nas.find((n) => n.id === c.req.param("nasId"));
  if (!nas) return notFound(c);
  /*
   * The one combination with no route back, and the only case rotation is refused:
   * the driver cannot push the new value **and** nothing can currently reach the
   * device to be told it by hand. Neither condition refuses alone — a reachable
   * device whose driver cannot push is a *manual* rotation, not an impossible one,
   * which is exactly why the console keeps offering the button. See the
   * `rotateNasSecret` description in `openapi.yaml`.
   */
  if (rotationRefused(nas)) {
    return problem(c, 409, {
      type: "/problems/rotation-refused",
      title: "Cannot rotate the secret",
      status: 409,
      params: { nasName: nas.name, nasReachability: nas.reachability },
    });
  }
  nas.secretSet = true;
  nas.secretLastUsedAt = null;
  /* The secret itself never reaches the log, and there is a test on it. An audit
     entry that recorded what was rotated *to* would be a credential store with a
     read operation and no expiry. */
  audit(c, {
    kind: "nas-secret-rotated",
    siteId: nas.siteId,
    params: { nasName: nas.name, changeClass: "restart" },
    subject: { kind: "nas", id: nas.id, name: nas.name },
  });
  syncAfterClientWrite(c, "nas-secret-rotated");
  return c.json(
    conformResponse("rotateNasSecret", 200, {
      sharedSecret: generateSecret(),
      // Restart, not sql-only: applying a new secret rewrites config, and a restart
      // drops in-flight EAP and the in-memory TLS session cache.
      changeClass: "restart" as const,
    }),
  );
});

// ── vouchers ─────────────────────────────────────────────────────────────────

api.get("/voucher-batches", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const items = world.batches.filter((b) => b.siteId === siteId);
  const p = page(items, q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listVoucherBatches", 200, p));
});

api.post("/voucher-batches", async (c) => {
  const body = (await c.req.json()) as {
    siteId: string;
    label: string;
    count: number;
    plan: VoucherBatch["plan"];
    expiryMode: VoucherBatch["expiryMode"];
    validForHours?: number;
  };
  if (!body?.label?.trim() || !body.count || body.count < 1) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Batch not created",
      status: 422,
      /* `below-minimum` rather than `required`: zero was supplied and refused, which
         is a different sentence from nothing having been supplied at all. */
      params: {
        fields: {
          ...(body?.label?.trim() ? {} : { label: "required" as const }),
          ...(body?.count >= 1 ? {} : { count: "below-minimum" as const }),
        },
        ...(body?.count >= 1 ? {} : { fieldParams: { count: { minimumCount: 1 } } }),
        /* Kept at the top level as well, and only because a client reading it is
           already deployed. It is the workaround `fieldParams` replaces: a figure
           belonging to `count`, sent where a second refused field could overwrite
           it. Removable the day every client reads `fieldParams` — the admin
           console is the only one. */
        minimumCount: 1,
      },
      errors: {
        ...(body?.label?.trim() ? {} : { label: "Required." }),
        ...(body?.count >= 1 ? {} : { count: "At least 1." }),
      },
    });
  }

  const id = `batch-${Date.now().toString(36)}`;
  const now = Date.now();
  const batch: VoucherBatch = {
    id,
    siteId: body.siteId,
    label: body.label,
    count: body.count,
    createdAt: new Date(now).toISOString(),
    createdBy: "quản trị",
    plan: { ...body.plan, enforcedBy: enforcementFor(body.siteId, body.plan) },
    expiryMode: body.expiryMode,
    expiresAt:
      body.expiryMode === "from-creation"
        ? new Date(now + (body.validForHours ?? 720) * 3_600_000).toISOString()
        : null,
    counts: { unused: body.count, inUse: 0, exhausted: 0, expired: 0, revoked: 0 },
  };
  world.batches.unshift(batch);

  for (let i = 0; i < body.count; i++) {
    world.vouchers.push({
      id: `v-${id}-${i}`,
      batchId: id,
      code: generateVoucherCode(),
      state: "unused",
      createdAt: batch.createdAt,
      firstUsedAt: null,
      expiresAt: batch.expiresAt,
      revokedAt: null,
      currentSessionId: null,
    });
  }
  audit(c, {
    kind: "voucher-batch-created",
    siteId: batch.siteId,
    params: { label: batch.label, count: batch.count },
    subject: { kind: "voucher-batch", id: batch.id, name: batch.label },
  });
  return c.json(conformResponse("createVoucherBatch", 201, batch), 201);
});

/**
 * What will actually enforce this batch's limits, derived from the site.
 *
 * **The weakest enabled device decides**, because a voucher batch is redeemable on
 * any access point in the site: a guest who lands on the one that cannot apply a
 * rate has an unenforced rate, whatever the other eleven do. Reporting the best case
 * is how an operator concludes a cap exists when it does not. Same rule as
 * `WalledGarden.effectiveKind`.
 *
 * The server derives it, because a console that sent `enforcedBy: "nas"` as a
 * literal on every batch would be the client asserting a fact about hardware it had
 * never looked at.
 */
function enforcementFor(siteId: string, plan: PlanRequest): NonNullable<Plan["enforcedBy"]> {
  const devices = world.nas.filter((n) => n.siteId === siteId && n.enabled);
  const statesRate = Boolean(plan.downKbps || plan.upKbps);
  const statesData = Boolean(plan.dataAllowanceMb);

  /* Nothing to enforce. Not `unenforced` — that word means *a limit nothing is
     enforcing*, and rendering a caveat for a plan with no limits trains an operator
     to ignore the ones that matter. A duration is honoured on every path: by
     Session-Timeout on RADIUS, by `timeLimitMinutes` on the controller API. */
  if (!statesRate && !statesData) return "nas";

  if (devices.length === 0) return "unenforced";

  const everyDeviceApplies =
    (!statesRate || devices.every((n) => n.capabilities.bandwidthLimits)) &&
    (!statesData || devices.every((n) => n.capabilities.hardQuota));
  if (everyDeviceApplies) return "nas";

  /* A data allowance survives as a sampled backstop wherever accounting arrives —
     the narrow reading: not impossible, merely sampled, and the UI states the
     overshoot. A **rate** cannot be sampled at all: there is nothing to enforce it
     with after the fact, so a rate no device applies is simply unenforced. */
  if (statesRate) return "unenforced";

  /*
   * **`unenforced` needs a measurement; `foyer-sampled` does not.**
   *
   * Asking `devices.some((n) => n.accountingConfigured)` would be a truthiness test
   * on a three-valued field. `store.RadiusClient.AccountingConfigured` is a `*bool`
   * whose comment says *"Nil is not measured"*, the property is optional on `Nas`,
   * and `createNas` leaves it **absent** on a device nothing has probed. So
   * `undefined` would fall through to `unenforced` and *nobody has looked* would be
   * answered as *measured silent*.
   *
   * Those two are not interchangeable, because the two values are not symmetric.
   * `foyer-sampled` is a hedge and carries its own caveat — sampled, with overshoot,
   * interval stated. `unenforced` is a **claim about the network**: the contract says
   * *"the plan states a limit nothing is enforcing. The UI must not present this as a
   * cap."* Saying that about radios nobody has spoken to manufactures a measurement
   * out of the absence of one — the mistake of writing `false` at registration, one
   * layer up — and this one reaches the guest rather than a pill on an admin screen.
   *
   * Go decides it the same way, and this is the same answer on all three inputs:
   * `internal/portal/service.go`'s `enforcement()` returns `EnforcedByNothing` for
   * `AccountingAbsent` **alone** and `EnforcedBySampling` otherwise, and
   * `internal/portal/placement_test.go` names the third case — *"nobody has measured
   * it" → EnforcedBySampling* — because *"reporting `unenforced` for a device nobody
   * has probed would tell an operator that a working quota is not being applied."*
   * The guess is closed at NAS registration, where `docs/rules/radius.md` asks for
   * it, not papered over in a voucher batch.
   *
   * Written as `every(... === false)` rather than a negated `some`, so the sentence
   * is the one being claimed: **only devices measured silent argue for `unenforced`.**
   */
  return devices.every((n) => n.accountingConfigured === false) ? "unenforced" : "foyer-sampled";
}

function generateVoucherCode(): string {
  const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789";
  const bytes = new Uint8Array(12);
  globalThis.crypto.getRandomValues(bytes);
  return [...bytes].map((b) => alphabet[b % alphabet.length]!).join("");
}

api.get("/voucher-batches/:batchId", (c) => {
  const batch = world.batches.find((b) => b.id === c.req.param("batchId"));
  return batch ? c.json(conformResponse("getVoucherBatch", 200, batch)) : notFound(c);
});

/**
 * Individual vouchers, **scoped to the site**.
 *
 * `siteId` is a required parameter on `listVouchers` in the contract. A handler
 * that ignored it, filtering only on batch, state and code, would be invisible for
 * exactly as long as every batch in the fixture world lived in one site: with
 * nothing to leak, a dropped scope returns the same rows either way. With
 * `batch-hanoi-rooms` in the world, `GET /vouchers?siteId=site-hcm` would answer
 * with the hotel's room codes — and a code is a bearer credential with *Revoke* one
 * column away.
 *
 * A voucher carries no `siteId` of its own — it belongs to a batch, and the batch
 * belongs to a site. So the scope is resolved through the batch rather than
 * duplicated onto the voucher, which is also what the schema does.
 */
api.get("/vouchers", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const batchId = q(c, "batchId");
  const states = (q(c, "state") ?? "").split(",").filter(Boolean) as Voucher["state"][];
  const query = (q(c, "q") ?? "").toUpperCase().replace(/[^A-Z0-9]/g, "");
  const inSite = new Set(world.batches.filter((b) => b.siteId === siteId).map((b) => b.id));
  let items = world.vouchers.filter((v) => inSite.has(v.batchId));
  if (batchId) items = items.filter((v) => v.batchId === batchId);
  if (states.length) items = items.filter((v) => states.includes(v.state));
  if (query) items = items.filter((v) => v.code.includes(query));
  const p = page(items, q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listVouchers", 200, p));
});

api.post("/vouchers/:voucherId/revoke", (c) => {
  const result = revokeVoucher(c.req.param("voucherId"));
  if (!result) return notFound(c);
  /* Recorded whether or not the session could be ended. The revocation happened; the
     driver call is a separate thing that may have failed, which is what `failure`
     in the response body is for and is not what this entry claims. */
  audit(c, {
    kind: "voucher-revoked",
    siteId: world.batches.find((b) => b.id === result.voucher.batchId)?.siteId ?? null,
    params: { code: result.voucher.code, sessionEnded: result.sessionEnded },
    subject: { kind: "voucher", id: result.voucher.id, name: result.voucher.code },
  });
  return c.json(conformResponse("revokeVoucher", 200, result));
});

// ── sessions ─────────────────────────────────────────────────────────────────

api.get("/sessions", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const states = (q(c, "state") ?? "").split(",").filter(Boolean) as Session["state"][];
  const nasId = q(c, "nasId");
  const macQuery = (q(c, "mac") ?? "").toLowerCase();
  const query = (q(c, "q") ?? "").toLowerCase();

  let items = world.sessions.filter((s) => s.siteId === siteId);
  if (states.length) items = items.filter((s) => states.includes(s.state));
  if (nasId) items = items.filter((s) => s.nasId === nasId);
  if (macQuery) items = items.filter((s) => s.callingStationId.toLowerCase().includes(macQuery));
  if (query) {
    items = items.filter((s) =>
      // No hostname: it left the contract. `docs/protocol-reality.md` — "Client
      // hostname / OS — Not available." Searching a field the backend will never
      // populate is a search box that quietly does less than it looks like it does.
      [s.callingStationId, s.username, s.ipAddress, s.nasName, s.acctSessionId]
        .filter(Boolean)
        .some((f) => String(f).toLowerCase().includes(query)),
    );
  }
  const p = page(items.map(withCurrentFreshness), q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listSessions", 200, p));
});

api.get("/sessions/:sessionId", (c) => {
  const s = world.sessions.find((x) => x.id === c.req.param("sessionId"));
  return s ? c.json(conformResponse("getSession", 200, withCurrentFreshness(s))) : notFound(c);
});

/*
 * `disconnectSession` — the one action on a session, and not an edit of one.
 *
 * Both refusals come before anything changes, whatever was asked for: an operator
 * told "no" must find the voucher and the grant exactly as they were. Recorded in
 * the log whether or not the device confirmed, because an attempt that revoked a
 * voucher is a change — the same rule `revokeVoucher` follows above.
 */
api.post("/sessions/:sessionId/disconnect", async (c) => {
  const body = ((await c.req.json().catch(() => ({}))) ?? {}) as {
    revokeGrant?: boolean;
    revokeVoucher?: boolean;
  };
  const outcome = disconnectSession(c.req.param("sessionId"), {
    revokeGrant: body.revokeGrant === true,
    revokeVoucher: body.revokeVoucher === true,
  });
  switch (outcome.kind) {
    case "not-found":
      return notFound(c);
    case "not-live":
      return problem(c, 409, {
        type: "/problems/session-not-live",
        title: "This session has already ended",
        status: 409,
      });
    case "unsupported":
      return problem(c, 409, {
        type: "/problems/disconnect-unsupported",
        title: "This device cannot end a session from Foyer",
        status: 409,
        ...(outcome.nas ? { params: { driver: outcome.nas.driver, nasName: outcome.nas.name } } : {}),
      });
  }
  const { session, result } = outcome;
  audit(c, {
    kind: "session-disconnected",
    siteId: session.siteId,
    params: {
      nasId: session.nasId,
      disconnected: result.disconnected,
      grantsRevoked: result.grantsRevoked,
      voucherRevoked: result.voucherRevoked,
      ...(result.voucher ? { voucherId: result.voucher.id } : {}),
    },
    subject: { kind: "session", id: session.id, name: session.callingStationId },
  });
  return c.json(conformResponse("disconnectSession", 200, result));
});

/*
 * The portal's open authorizations: a guest on a controller-
 * authorized network with no RADIUS accounting has no session row, so the grant is
 * what the console lists and ends — UniFi's Hotspot Manager and Omada's portal list
 * do the same. `revokeVoucher`'s own permission is the gate's, from the contract.
 */
api.get("/portal-grants", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const p = page(openPortalGrants(siteId), q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listPortalGrants", 200, p));
});

api.post("/portal-grants/:grantId/disconnect", async (c) => {
  const body = ((await c.req.json().catch(() => ({}))) ?? {}) as { revokeVoucher?: boolean };
  const outcome = disconnectPortalGrant(c.req.param("grantId"), { revokeVoucher: body.revokeVoucher === true });
  switch (outcome.kind) {
    case "not-found":
      return notFound(c);
    case "not-live":
      return problem(c, 409, {
        type: "/problems/session-not-live",
        title: "This session has already ended",
        status: 409,
      });
    case "unsupported":
      return problem(c, 409, {
        type: "/problems/disconnect-unsupported",
        title: "This device cannot end a session from Foyer",
        status: 409,
        ...(outcome.nas ? { params: { driver: outcome.nas.driver, nasName: outcome.nas.name } } : {}),
      });
  }
  const { grant, result } = outcome;
  audit(c, {
    kind: "session-disconnected",
    siteId: grant.siteId,
    params: {
      grantId: grant.id,
      disconnected: result.disconnected,
      grantsRevoked: result.grantsRevoked,
      voucherRevoked: result.voucherRevoked,
      ...(result.voucher ? { voucherId: result.voucher.id } : {}),
    },
    subject: { kind: "portal-grant", id: grant.id, name: grant.clientMac },
  });
  return c.json(conformResponse("disconnectPortalGrant", 200, result));
});

// ── accounts ─────────────────────────────────────────────────────────────────

api.get("/accounts", (c) => {
  const siteId = q(c, "siteId") ?? "site-hcm";
  const query = (q(c, "q") ?? "").toLowerCase();
  let items = world.accounts.filter((a) => a.siteId === siteId);
  if (query) {
    items = items.filter((a) =>
      [a.username, a.displayName].filter(Boolean).some((f) => String(f).toLowerCase().includes(query)),
    );
  }
  const p = page(items, q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listAccounts", 200, p));
});

api.post("/accounts", async (c) => {
  const body = (await c.req.json()) as {
    siteId: string;
    username: string;
    displayName?: string;
    password: string;
    eapTtlsPap?: boolean;
  };
  const username = (body?.username ?? "").trim().toLowerCase();
  const refused = fieldRefusals();
  /* The other half of the pair `FieldFault` was written for: `username` is refused
     two ways here and `value` is refused three ways on the walled garden, so
     `(type, field)` cannot key a client's copy. */
  if (!username) refused.add("username", "required", "Required.");
  else if (world.accounts.some((a) => a.username === username && a.siteId === body.siteId)) {
    /* Scoped to the venue, because that is what the database enforces: `accounts` is
       `UNIQUE (tenant_id, site_id, username)` in migration `00005`, and
       `internal/api/memory.CreateAccount` refuses on exactly that pair. Unscoped, this
       would refuse the hotel a `nguyen.van.tuan` because the café already had one —
       the same species as site-blindness in `loginWithAccount`, and reachable from
       the same screen. */
    refused.add("username", "duplicate", "That username already exists. Choose another.");
  }
  if (!body?.password) refused.add("password", "required", "Required.");
  else if (body.password.length < PASSWORD_MIN_LENGTH) {
    /* The same floor `createOperator` and `changeOwnPassword` apply, and the same one
       `internal/api` applies to *this* operation — `passwordMinimum` in
       `handlers_auth.go`, twelve. Without it here nothing would be red: `make
       diff-api` creates its account with a 35-character constant, so the harness
       cannot be wrong about the input and both servers answer 201. That is the quiet
       half of `docs/rules/testing.md`'s *input the harness cannot be wrong about* —
       it produces confident output in either direction, and this direction is the
       one that says nothing at all.

       A guest account is a network credential. An oracle that accepts a four-character
       one is an oracle a console can be built against and then refused by the server
       the day it is pointed at Go. */
    refused.add("password", "below-minimum", `At least ${PASSWORD_MIN_LENGTH} characters.`, {
      minimumCount: PASSWORD_MIN_LENGTH,
    });
  } else if (body.eapTtlsPap && !papPasswordFits(body.password)) {
    refused.add("password", "above-maximum", "Too long for 802.1X.", { maximumCount: PAP_MAX_BYTES });
  }
  if (!refused.any() && body.eapTtlsPap && papHolder(username, null)) {
    refused.add("eapTtlsPap", "duplicate", "Already in use.");
  }

  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Account not created",
      status: 422,
      ...refused.problem(),
    });
  }

  const account = {
    id: `acc-${Date.now().toString(36)}`,
    siteId: body.siteId,
    username,
    displayName: body.displayName?.trim() || null,
    enabled: true,
    createdAt: new Date().toISOString(),
    lastLoginAt: null,
    // Always argon2id. An account only becomes `argon2id+nt` through an explicit,
    // separate opt-in that states what NT-hash storage means.
    credentialKind: (body.eapTtlsPap ? "argon2id+bcrypt" : "argon2id") as Account["credentialKind"],
    plan: PLANS.staffUnmetered!,
  };
  world.accounts.unshift(account);
  if (body.eapTtlsPap) auditCredentials(c, account, false);
  /* The password the operator typed goes somewhere. Otherwise an account created
     here could not sign in on the portal — invisible while any password worked, and
     the same obligation `createOperator` has. */
  setAccountPassword(account.id, body.password);
  return c.json(conformResponse("createAccount", 201, account), 201);
});

api.patch("/accounts/:accountId", async (c) => {
  const a = world.accounts.find((x) => x.id === c.req.param("accountId"));
  if (!a) return notFound(c);
  const body = (await c.req.json()) as {
    displayName?: string;
    enabled?: boolean;
    password?: string;
    eapTtlsPap?: boolean;
  };
  /* The 802.1X opt-in travels with the password it is made from — `updateAccount`
     in the contract, `apistore.AmendAccount` in Go. Every refusal changes nothing. */
  if (typeof body.password === "string" || typeof body.eapTtlsPap === "boolean") {
    const has = a.credentialKind.includes("+bcrypt");
    const want = typeof body.eapTtlsPap === "boolean" ? body.eapTtlsPap : has;
    const refused = fieldRefusals();
    if (typeof body.password === "string" && body.password.length < PASSWORD_MIN_LENGTH) {
      refused.add("password", "below-minimum", `At least ${PASSWORD_MIN_LENGTH} characters.`, {
        minimumCount: PASSWORD_MIN_LENGTH,
      });
    } else if (want && !has && typeof body.password !== "string") {
      refused.add("password", "required", "Required to enable 802.1X.");
    } else if (want && typeof body.password === "string" && !papPasswordFits(body.password)) {
      refused.add("password", "above-maximum", "Too long for 802.1X.", { maximumCount: PAP_MAX_BYTES });
    } else if (want && !has && papHolder(a.username, a.id)) {
      refused.add("eapTtlsPap", "duplicate", "Already in use.");
    }
    if (refused.any()) {
      return problem(c, 422, {
        type: "/problems/validation",
        title: "That cannot be saved",
        status: 422,
        ...refused.problem(),
      });
    }
    const base = a.credentialKind.replace("+bcrypt", "");
    a.credentialKind = (want ? `${base}+bcrypt` : base) as Account["credentialKind"];
    if (typeof body.password === "string") setAccountPassword(a.id, body.password);
  }
  if (typeof body.displayName === "string") a.displayName = body.displayName;
  if (typeof body.enabled === "boolean") a.enabled = body.enabled;
  if (typeof body.password === "string" || typeof body.eapTtlsPap === "boolean") {
    auditCredentials(c, a, typeof body.password === "string");
  }
  return c.json(conformResponse("updateAccount", 200, a));
});

/** bcrypt reads 72 bytes — `identity.ErrPasswordTooLongForPAP`. */
const PAP_MAX_BYTES = 72;
const papPasswordFits = (p: string) => new TextEncoder().encode(p).length <= PAP_MAX_BYTES;

/** Another account, at any venue, opted in under this username: the store's
    `account_pap_verifiers_username_is_global`. */
const papHolder = (username: string, exceptId: string | null) =>
  world.accounts.some((x) => x.id !== exceptId && x.username === username && x.credentialKind.includes("+bcrypt"));

function auditCredentials(c: Context, a: Account, passwordChanged: boolean) {
  audit(c, {
    kind: "account-credentials-changed",
    siteId: a.siteId,
    params: { passwordChanged, eapTtlsPap: a.credentialKind.includes("+bcrypt") },
    subject: { kind: "account", id: a.id, name: a.username },
  });
}

api.delete("/accounts/:accountId", (c) => {
  const i = world.accounts.findIndex((x) => x.id === c.req.param("accountId"));
  if (i < 0) return notFound(c);
  world.accounts.splice(i, 1);
  return c.body(null, 204);
});

// ── the console's own authentication ─────────────────────────────────────────

const authSessionBody = (operator: Operator, csrfToken: string) => ({
  operator,
  /* Spread, because the contract's table is `readonly` and this is a response body.
     The array is the authority a client gates on — see `OperatorPermission`. */
  permissions: [...permissionsFor(operator)],
  csrfToken,
  /* Read on every call rather than captured at sign-in. The console learns from this
     that it must route to the change-password screen, and it has to stop being true
     the moment the change lands — a value remembered from sign-in would leave the
     console routing back to a screen it has already completed. */
  mustChangePassword: mustChangePassword(operator.id),
});

/**
 * Hand a browser its session and its trusted-device cookie — `signIn` and
 * `completeAdminSetup` alike, as Go's `startSession` does.
 */
function startSession(c: Context, session: { id: string }, username: string) {
  /* The name and `secure` together, from one condition. `adminCookie` carries the
     whole of the deviation and why it is one deviation and not two: the contract's
     name is `__Host-`-prefixed, the prefix *is* `Secure` as far as a user agent is
     concerned, and this mock serves plain HTTP on localhost where a `secure` cookie
     is one the browser discards. The Go binary sets both unconditionally. */
  const admin = adminCookie(c);
  setCookie(c, admin.name, session.id, {
    httpOnly: true,
    sameSite: "Lax",
    path: "/",
    secure: admin.secure,
  });
  setCookie(c, admin.secure ? `__Host-${DEVICE_COOKIE}` : DEVICE_COOKIE, issueDeviceCookie(username), {
    httpOnly: true,
    sameSite: "Strict",
    path: "/",
    secure: admin.secure,
    maxAge: 180 * 24 * 3600,
  });
}

api.get("/auth/session", (c) => {
  const current = sessionOf(c);
  if (!current) return notSignedIn(c);
  return c.json(
    conformResponse(
      "getAuthSession",
      200,
      authSessionBody(current.operator, current.session.csrfToken),
    ),
  );
});

api.post("/auth/session", async (c) => {
  const body = (await c.req.json()) as { username?: string; password?: string };
  const refused = fieldRefusals();
  if (!body?.username?.trim()) refused.add("username", "required", "Required.");
  if (!body?.password) refused.add("password", "required", "Required.");
  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Missing credentials",
      status: 422,
      ...refused.problem(),
    });
  }

  /*
   * **The account's sign-in budget, checked before the password is**, following the
   * console's `consoleAccount` setting and split by trusted device exactly as
   * `internal/api` splits it (`auth.ts`): a browser that signed in as this username
   * before spends from its own bucket, so a stranger emptying the shared one cannot
   * keep the account's holder out. No address bucket here — see `auth.ts` for why. Same answer
   * whether or not the username exists.
   */
  const secure = adminCookie(c).secure;
  const budget = throttleSignIn(
    body.username!,
    getCookie(c, secure ? `__Host-${DEVICE_COOKIE}` : DEVICE_COOKIE),
  );
  if (budget.wait) {
    c.header("Retry-After", String(budget.wait));
    return problem(c, 429, {
      type: "/problems/rate-limited",
      title: "Too many attempts",
      status: 429,
      params: { retryAfterSeconds: budget.wait },
    });
  }

  const session = beginAdminSession(body.username!, body.password!);
  if (!session) {
    /*
     * **Never says which**, and a disabled operator lands here too. An answer that
     * distinguished an unknown username from a wrong password would make this form a
     * place to ask whether somebody has an account, and would turn disabling an
     * account into a way of publishing that it exists.
     *
     * No `params` either: there is nothing structured to say that is not the same
     * disclosure in another field.
     */
    return problem(c, 401, {
      type: "/problems/bad-credentials",
      title: "Wrong username or password",
      status: 401,
    });
  }
  /* Reset-on-success, of the bucket this attempt spent and no other. */
  budget.spent();
  startSession(c, session, body.username!);
  const operator = operatorById(session.operatorId)!;
  return c.json(conformResponse("signIn", 200, authSessionBody(operator, session.csrfToken)));
});

api.delete("/auth/session", (c) => {
  endAdminSession(getCookie(c, adminCookie(c).name));
  deleteCookie(c, adminCookie(c).name, { path: "/" });
  /* `204` whether or not there was a session. "Sign me out" has one correct outcome,
     and a client that has just been told `401` by something else must still be able
     to reach it. */
  return c.body(null, 204);
});

api.post("/auth/password", async (c) => {
  const current = sessionOf(c);
  /* Not the middleware's `401`: there is no "your own password" without a you, so
     this refusal survives the enforcement switch either way. */
  if (!current) return notSignedIn(c);
  const body = (await c.req.json()) as PasswordChange;

  const refused = fieldRefusals();
  if (!body?.currentPassword) refused.add("currentPassword", "required", "Required.");
  else if (body.currentPassword !== operatorPassword(current.operator.id)) {
    /* `incorrect`, not `/problems/bad-credentials`: that one refuses to say which of
       two things was wrong because it is answering a stranger. This is talking to
       somebody already signed in, about the field they just typed. */
    refused.add("currentPassword", "incorrect", "That is not your current password.");
  }
  if (!body?.newPassword) refused.add("newPassword", "required", "Required.");
  else if (body.newPassword.length < PASSWORD_MIN_LENGTH) {
    refused.add("newPassword", "below-minimum", `At least ${PASSWORD_MIN_LENGTH} characters.`, {
      minimumCount: PASSWORD_MIN_LENGTH,
    });
  } else if (body.newPassword === body.currentPassword) {
    /* A change that changed nothing reads as success, and the operator walks away
       believing they rotated a credential they did not. */
    refused.add("newPassword", "unchanged", "Choose a password you have not used here.");
  }
  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Password not changed",
      status: 422,
      ...refused.problem(),
    });
  }

  /* The credential, not the session: `AdminSession` carries no password, because
     sign-in verifies against the credential. A change that only reached the session would
     leave the operator able to sign in again with the old one — which is the same
     "changed nothing" the `unchanged` refusal above exists to stop, one layer down. */
  setOperatorPassword(current.operator.id, body.newPassword);
  audit(c, {
    kind: "operator-password-changed",
    siteId: null,
    params: { username: current.operator.username },
    subject: { kind: "operator", id: current.operator.id, name: current.operator.username },
  });
  return c.body(null, 204);
});

// ── the first-access setup — `internal/api/handlers_setup.go` ─────────────────
//
// Foyer ships no credential. `FOYER_MOCK_FRESH=1` is a first-booted install with
// the setup open and its code in the banner (`SETUP_CODE`); every other world is an
// install whose setup is long complete, so `completeAdminSetup` answers 409 there.

api.get("/auth/setup", (c) =>
  c.json(
    conformResponse("getAdminSetup", 200, {
      required: setupOpen(),
      /* As the Go handler answers it: a closed setup on an install with
         no administrator is a bootstrap that never ran, not a console with accounts
         to sign in to. */
      noAdministrator: !setupOpen() && enabledAdministrators().length === 0,
    }),
  ),
);

api.post("/auth/setup", async (c) => {
  /* Closed answers the same whatever the body says. */
  if (!setupOpen()) {
    return problem(c, 409, {
      type: "/problems/setup-complete",
      title: "Setup is complete",
      status: 409,
    });
  }
  const body = (await c.req.json()) as { setupCode?: string; username?: string; password?: string };
  /* Per address only — no account exists yet, and one shared bucket would let one
     device keep the installer out of their own install. Charged only when a code is named. */
  if (body?.setupCode?.trim()) {
    /* One key: every client of this mock is localhost, and Go keys the socket peer,
       never a header a client writes. */
    const wait = throttleSetup("local");
    if (wait) {
      c.header("Retry-After", String(wait));
      return problem(c, 429, {
        type: "/problems/rate-limited",
        title: "Too many attempts",
        status: 429,
        params: { retryAfterSeconds: wait },
      });
    }
  }
  const refused = fieldRefusals();
  if (!body?.setupCode?.trim()) refused.add("setupCode", "required", "Required.");
  if (!body?.username?.trim()) refused.add("username", "required", "Required.");
  if (!body?.password) refused.add("password", "required", "Required.");
  else if (body.password.length < PASSWORD_MIN_LENGTH) {
    refused.add("password", "below-minimum", "Too short.", { minimumCount: PASSWORD_MIN_LENGTH });
  }
  if (!refused.any()) {
    const made = createSetupAdministrator(body.setupCode!, body.username!, body.password!);
    if (made === "closed") {
      return problem(c, 409, {
        type: "/problems/setup-complete",
        title: "Setup is complete",
        status: 409,
      });
    }
    if (made === "wrong") refused.add("setupCode", "incorrect", "Not the code the installer printed.");
    else if (made === "taken") refused.add("username", "duplicate", "Already in use.");
    else {
      const session = beginAdminSession(made.username, body.password!)!;
      recordAudit({
        actorId: made.id,
        actorUsername: made.username,
        kind: "operator-created",
        siteId: null,
        params: { role: made.role, setup: true },
        subject: { kind: "operator", id: made.id, name: made.username },
      });
      startSession(c, session, made.username);
      return c.json(conformResponse("completeAdminSetup", 200, authSessionBody(made, session.csrfToken)));
    }
  }
  return problem(c, 422, {
    type: "/problems/validation",
    title: "Setup not completed",
    status: 422,
    ...refused.problem(),
  });
});

// ── operators ────────────────────────────────────────────────────────────────

api.get("/operators", (c) => {
  const query = (q(c, "q") ?? "").toLowerCase();
  /* No `siteId`. An operator is deployment-wide, and filtering them by the site
     switcher would be inventing a scope the object does not have. */
  let items = world.operators;
  if (query) {
    items = items.filter((o) =>
      [o.username, o.displayName].filter(Boolean).some((f) => String(f).toLowerCase().includes(query)),
    );
  }
  const p = page(items, q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listOperators", 200, p));
});

api.post("/operators", async (c) => {
  const body = (await c.req.json()) as OperatorRequest;
  const username = (body?.username ?? "").trim().toLowerCase();

  const refused = fieldRefusals();
  if (!username) refused.add("username", "required", "Required.");
  else if (world.operators.some((o) => o.username === username)) {
    refused.add("username", "duplicate", "That username already exists. Choose another.");
  }
  if (!body?.role) refused.add("role", "required", "Required.");
  if (!body?.password) refused.add("password", "required", "Required.");
  else if (body.password.length < PASSWORD_MIN_LENGTH) {
    refused.add("password", "below-minimum", `At least ${PASSWORD_MIN_LENGTH} characters.`, {
      minimumCount: PASSWORD_MIN_LENGTH,
    });
  }
  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "Operator not created",
      status: 422,
      ...refused.problem(),
    });
  }

  const operator: Operator = {
    id: `op-${Date.now().toString(36)}-${world.operators.length}`,
    username,
    displayName: body.displayName?.trim() || null,
    role: body.role,
    enabled: body.enabled ?? true,
    createdAt: new Date().toISOString(),
    lastLoginAt: null,
  };
  world.operators.unshift(operator);
  /* And it is the operator's password from here on. Otherwise an operator created
     through this console could not sign in — invisible while sign-in accepted
     anything, and a `201` for an account that does not work. */
  setOperatorPassword(operator.id, body.password!);
  audit(c, {
    kind: "operator-created",
    siteId: null,
    params: { username: operator.username, role: operator.role },
    subject: { kind: "operator", id: operator.id, name: operator.username },
  });
  return c.json(conformResponse("createOperator", 201, operator), 201);
});

/**
 * Would this change leave the deployment with nobody who can administer it?
 *
 * Counts **enabled** administrators, not administrators: a disabled one cannot sign
 * in, so a deployment whose only administrator is disabled is already locked out, and
 * there is no password-reset channel to recover through. The fixture set has one of
 * each, so a rule that counted rows would pass the fixtures and fail a real site.
 */
function wouldOrphanTheDeployment(target: Operator, next: { role?: string; enabled?: boolean }) {
  const stillAdministrator =
    (next.role ?? target.role) === "administrator" && (next.enabled ?? target.enabled);
  if (stillAdministrator) return false;
  return enabledAdministrators().every((o) => o.id === target.id);
}

api.patch("/operators/:operatorId", async (c) => {
  const operator = world.operators.find((o) => o.id === c.req.param("operatorId"));
  if (!operator) return notFound(c);
  const body = (await c.req.json()) as OperatorUpdate;

  if (wouldOrphanTheDeployment(operator, body)) {
    return problem(c, 409, {
      type: "/problems/last-administrator",
      title: "Would leave no administrator",
      status: 409,
      params: { username: operator.username },
    });
  }

  const changed: string[] = [];
  if (body.displayName !== undefined) {
    operator.displayName = body.displayName?.trim() || null;
    changed.push("displayName");
  }
  if (body.role !== undefined && body.role !== operator.role) {
    operator.role = body.role;
    changed.push("role");
  }
  if (body.enabled !== undefined && body.enabled !== operator.enabled) {
    operator.enabled = body.enabled;
    changed.push("enabled");
    /* Disabling an operator whose browser stays signed in has disabled nothing. */
    if (!operator.enabled) endSessionsFor(operator.id);
  }
  audit(c, {
    kind: "operator-updated",
    siteId: null,
    params: { username: operator.username, changed, role: operator.role, enabled: operator.enabled },
    subject: { kind: "operator", id: operator.id, name: operator.username },
  });
  return c.json(conformResponse("updateOperator", 200, operator));
});

api.delete("/operators/:operatorId", (c) => {
  const i = world.operators.findIndex((o) => o.id === c.req.param("operatorId"));
  if (i < 0) return notFound(c);
  const operator = world.operators[i]!;

  if (wouldOrphanTheDeployment(operator, { enabled: false })) {
    return problem(c, 409, {
      type: "/problems/last-administrator",
      title: "Would delete the last administrator",
      status: 409,
      params: { username: operator.username },
    });
  }

  world.operators.splice(i, 1);
  endSessionsFor(operator.id);
  /* The entry is written *after* the deletion and still names them, which is the
     member `actorUsername` exists for read from the other end: this operator's own
     entries stay in the log with their username on them. */
  audit(c, {
    kind: "operator-deleted",
    siteId: null,
    params: { username: operator.username },
    subject: { kind: "operator", id: operator.id, name: operator.username },
  });
  return c.body(null, 204);
});

// ── the audit log ────────────────────────────────────────────────────────────

api.get("/audit", (c) => {
  const actorId = q(c, "actorId");
  const from = q(c, "from");
  const to = q(c, "to");
  let items = world.audit;
  if (actorId) items = items.filter((e) => e.actorId === actorId);
  /* String comparison, and it is exact rather than lucky: every `at` in this world is
     `Date.prototype.toISOString()`, which is fixed-width UTC, so lexicographic order
     is chronological order. A world that mixed offsets would need parsing. */
  if (from) items = items.filter((e) => e.at >= from);
  if (to) items = items.filter((e) => e.at < to);
  const p = page(items, q(c, "cursor"), limitOf(c));
  return c.json(conformResponse("listAudit", 200, p));
});

// ── privacy: retention periods and data-subject erasure ──────────────────────
//
// `internal/store/privacy.go` is the real thing; this is the
// oracle the Go server is compared against, so its defaults, bounds, refusals and
// receipts are the contract's shapes exactly.

api.get("/privacy/retention", (c) => c.json(conformResponse("getRetentionPolicy", 200, retentionPolicy())));

api.put("/privacy/retention", async (c) => {
  const body = (await c.req.json()) as RetentionPolicyUpdate;
  const changes = body?.changes ?? [];
  const refused = fieldRefusals();
  if (changes.length === 0) refused.add("changes", "required", "Required.");
  const before = retentionPolicy();
  for (const ch of changes) {
    const cat = before.categories.find((x) => x.category === ch.category);
    if (!cat || ch.days === null) continue;
    const field = `changes.${ch.category}`;
    if (ch.days < cat.minimumDays)
      refused.add(field, "below-minimum", "Too short.", { minimumCount: cat.minimumDays });
    else if (ch.days > cat.maximumDays)
      refused.add(field, "above-maximum", "Too long.", { maximumCount: cat.maximumDays });
  }
  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      ...refused.problem(),
    });
  }
  setRetention(changes);
  const after = retentionPolicy();
  const changed: Record<string, { from: number; to: number }> = {};
  for (const cat of after.categories) {
    const prev = before.categories.find((x) => x.category === cat.category)!;
    if (prev.days !== cat.days) changed[cat.category] = { from: prev.days, to: cat.days };
  }
  audit(c, {
    kind: "retention-policy-changed",
    siteId: null,
    params: { changed },
    subject: { kind: "retention-policy", id: "retention-policy" },
  });
  return c.json(conformResponse("updateRetentionPolicy", 200, after));
});

api.post("/privacy/erasures", async (c) => {
  const body = (await c.req.json()) as DataSubjectErasureRequest;
  const subject = body?.subject;
  const refused = fieldRefusals();
  let mac = "";
  if (subject?.kind === "mac") {
    if (!subject.mac?.trim()) refused.add("subject.mac", "required", "Required.");
    else if (!(mac = erasableMac(subject.mac.trim())))
      refused.add("subject.mac", "not-a-mac-address", "Not a MAC address.");
  } else if (subject?.kind === "account" && !subject.accountId?.trim()) {
    refused.add("subject.accountId", "required", "Required.");
  }
  let phone: ReturnType<typeof erasablePhone> = null;
  if (subject?.kind === "phone") {
    if (!subject.phone?.trim()) refused.add("subject.phone", "required", "Required.");
    else if (!(phone = erasablePhone(subject.phone)))
      refused.add("subject.phone", "not-a-phone-number", "Not a phone number.");
  }
  let email = "";
  if (subject?.kind === "email") {
    if (!subject.email?.trim()) refused.add("subject.email", "required", "Required.");
    else if (!(email = erasableEmail(subject.email)))
      refused.add("subject.email", "not-an-email-address", "Not an e-mail address.");
  }
  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be erased",
      status: 422,
      ...refused.problem(),
    });
  }
  const result = eraseSubject(subject.kind, mac, subject.accountId?.trim() ?? "", phone ?? undefined, email || undefined);
  if (!result) return notFound(c);

  const id = `era-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
  /* Masked in the log: an audit entry that kept the MAC it records erasing would
     not have erased it. */
  audit(c, {
    kind: "data-subject-erased",
    siteId: null,
    params: {
      subjectKind: subject.kind,
      removed: Object.fromEntries(result.removed.map((r) => [r.table, r.rows])),
      pseudonymised: Object.fromEntries(result.pseudonymised.map((r) => [r.table, r.rows])),
      total: result.total,
    },
    subject: { kind: "data-subject", id, name: result.masked },
  });
  return c.json(
    conformResponse("eraseDataSubject", 200, {
      id,
      at: new Date().toISOString(),
      subject: { kind: subject.kind, masked: result.masked },
      removed: result.removed,
      pseudonymised: result.pseudonymised,
      total: result.total,
    }),
  );
});

// ── network: transport and RADIUS-plane settings ──────────────────────────────
//
// Every operator setting is set in the console. `internal/api/handlers_network.go` is the real
// thing; see `network.ts` for the state, the bounds and the certificate checks, and
// for why no private key is kept here.

const actorOf = (c: Context) => sessionOf(c)?.operator.username ?? null;

/** The automatic sync after a RADIUS-client write — `syncAfterClientWrite`. */
function syncAfterClientWrite(c: Context, trigger: RadiusSyncTrigger) {
  const rec = syncClients(
    trigger,
    world.nas.filter((n) => n.enabled && n.secretSet).length,
    actorOf(c),
    world.nas.filter((n) => n.enabled && !n.secretSet).map((n) => n.ipAddress ?? n.name),
  );
  audit(c, {
    kind: "radius-clients-synced",
    siteId: null,
    params: {
      trigger: rec.trigger,
      outcome: rec.outcome,
      inserted: rec.inserted,
      updated: rec.updated,
      removed: rec.removed.length,
      skipped: rec.skipped.length,
    },
    subject: { kind: "radius-server", id: "radius-server" },
  });
  return rec;
}

function refuseBound(
  refused: ReturnType<typeof fieldRefusals>,
  field: string,
  v: number,
  b: { minimum: number; maximum: number },
) {
  if (v < b.minimum) refused.add(field, "below-minimum", "Too small.", { minimumCount: b.minimum });
  else if (v > b.maximum) refused.add(field, "above-maximum", "Too large.", { maximumCount: b.maximum });
}

api.get("/network/settings", (c) => c.json(conformResponse("getNetworkSettings", 200, networkSettings())));

api.put("/network/settings", async (c) => {
  const body = (await c.req.json()) as NetworkSettingsUpdate;
  const portalUrl = body.portalUrl === null ? null : body.portalUrl.trim();
  const venueUrl = body.venueUrl === null ? null : body.venueUrl.trim();
  const refused = fieldRefusals();
  if (portalUrl !== null && !isWebAddress(portalUrl))
    refused.add("portalUrl", "not-a-web-address", "Not an http:// or https:// address.");
  if (venueUrl !== null && !isWebAddress(venueUrl))
    refused.add("venueUrl", "not-a-web-address", "Not an http:// or https:// address.");
  const leaf = currentPortalLeaf();
  if (leaf && (portalUrl === null || isWebAddress(portalUrl))) {
    const reason = portalPairFault(leaf, portalUrl ?? bootstrapPortalUrl());
    if (reason) refused.add("portalUrl", "certificate-refused", `Refused: ${reason}.`, { reason });
  }
  for (const name of ["consoleAddress", "consoleAccount", "portalAddress", "portalAccount"] as const) {
    refuseBound(refused, `throttles.${name}.burst`, body.throttles[name].burst, BOUNDS.throttleBurst);
    refuseBound(refused, `throttles.${name}.intervalMs`, body.throttles[name].intervalMs, BOUNDS.throttleIntervalMs);
  }
  for (const name of ["portalBytes", "consoleBytes", "anonymousBytes", "headerBytes"] as const) {
    refuseBound(refused, `bodyCeilings.${name}`, body.bodyCeilings[name], BOUNDS[name]);
  }
  if (refused.any()) {
    return problem(c, 422, { type: "/problems/validation", title: "That cannot be saved", status: 422, ...refused.problem() });
  }
  const before = networkSettings();
  saveTransport({ portalUrl, venueUrl, throttles: body.throttles, bodyCeilings: body.bodyCeilings }, actorOf(c) ?? "");
  const after = networkSettings();
  const changed: Record<string, { from: unknown; to: unknown }> = {};
  const diff = (k: string, a: unknown, b: unknown) => {
    if (a !== b) changed[k] = { from: a, to: b };
  };
  diff("portalUrl", before.portalUrl.value, after.portalUrl.value);
  diff("venueUrl", before.venueUrl.value, after.venueUrl.value);
  for (const name of ["consoleAddress", "consoleAccount", "portalAddress", "portalAccount"] as const) {
    diff(`throttles.${name}.burst`, before.throttles[name].burst, after.throttles[name].burst);
    diff(`throttles.${name}.intervalMs`, before.throttles[name].intervalMs, after.throttles[name].intervalMs);
  }
  for (const name of ["portalBytes", "consoleBytes", "anonymousBytes", "headerBytes"] as const) {
    diff(`bodyCeilings.${name}`, before.bodyCeilings[name], after.bodyCeilings[name]);
  }
  audit(c, {
    kind: "network-settings-changed",
    siteId: null,
    params: { changed },
    subject: { kind: "network-settings", id: "network-settings" },
  });
  return c.json(conformResponse("updateNetworkSettings", 200, after));
});

api.put("/network/portal-certificate", async (c) => {
  const body = (await c.req.json()) as PortalCertificateUpload;
  const refused = fieldRefusals();
  if (!body.certificatePem?.trim()) refused.add("certificatePem", "required", "Required.");
  if (!body.privateKeyPem?.trim()) refused.add("privateKeyPem", "required", "Required.");
  const portalUrl = body.portalUrl === undefined ? null : body.portalUrl.trim();
  if (portalUrl !== null && !isWebAddress(portalUrl))
    refused.add("portalUrl", "not-a-web-address", "Not an http:// or https:// address.");
  if (!refused.any()) {
    const pair = checkPair(body.certificatePem, body.privateKeyPem!);
    if (!pair.ok) {
      const field = pair.input === "key" ? "privateKeyPem" : "certificatePem";
      refused.add(field, "certificate-refused", `Refused: ${pair.reason}.`, { reason: pair.reason });
    } else {
      const target = portalUrl ?? networkSettings().portalUrl.value ?? "";
      const reason = portalPairFault(pair.chain[0]!, target);
      if (reason) refused.add("portalUrl", "certificate-refused", `Refused: ${reason}.`, { reason });
      else putPortalCertificate(body.certificatePem, pair.chain, portalUrl, actorOf(c) ?? "");
    }
  }
  if (refused.any()) {
    return problem(c, 422, { type: "/problems/validation", title: "That cannot be saved", status: 422, ...refused.problem() });
  }
  const status = portalCertificateStatus();
  audit(c, {
    kind: "portal-certificate-changed",
    siteId: null,
    params: {
      fingerprintSha256: status.fingerprintSha256,
      notAfter: status.notAfter,
      ...(portalUrl !== null ? { portalUrl } : {}),
    },
    subject: { kind: "network-settings", id: "network-settings" },
  });
  return c.json(conformResponse("putPortalCertificate", 200, status));
});

api.delete("/network/portal-certificate", (c) => {
  deletePortalCertificate();
  audit(c, {
    kind: "portal-certificate-changed",
    siteId: null,
    params: { removed: true },
    subject: { kind: "network-settings", id: "network-settings" },
  });
  return c.json(conformResponse("deletePortalCertificate", 200, portalCertificateStatus()));
});

api.get("/network/radius", (c) => c.json(conformResponse("getRadiusServerSettings", 200, radiusSettings())));

api.put("/network/radius", async (c) => {
  const body = (await c.req.json()) as RadiusServerSettingsUpdate;
  const refused = fieldRefusals();
  refuseBound(refused, "maxServers", body.maxServers, MAX_SERVERS);
  if (body.eapTlsEnabled && !hasEapCertificate())
    refused.add("eapTlsEnabled", "required", "Upload an EAP-TLS certificate first.");
  if (refused.any()) {
    return problem(c, 422, { type: "/problems/validation", title: "That cannot be saved", status: 422, ...refused.problem() });
  }
  const before = radiusSettings();
  saveRadius(body.maxServers, body.eapTlsEnabled);
  const changed: Record<string, { from: unknown; to: unknown }> = {};
  if (before.maxServers !== body.maxServers) changed.maxServers = { from: before.maxServers, to: body.maxServers };
  if (before.eapTlsEnabled !== body.eapTlsEnabled)
    changed.eapTlsEnabled = { from: before.eapTlsEnabled, to: body.eapTlsEnabled };
  audit(c, {
    kind: "network-settings-changed",
    siteId: null,
    params: { changed },
    subject: { kind: "radius-server", id: "radius-server" },
  });
  return c.json(conformResponse("updateRadiusServerSettings", 200, radiusSettings()));
});

api.put("/network/radius/eap-certificate", async (c) => {
  const body = (await c.req.json()) as EapCertificateUpload;
  const refused = fieldRefusals();
  if (!body.privateKeyPem?.trim()) {
    refused.add("privateKeyPem", "required", "Required.");
  } else {
    const pair = checkPair(body.certificatePem, body.privateKeyPem);
    if (!pair.ok) {
      const field = pair.input === "key" ? "privateKeyPem" : "certificatePem";
      refused.add(field, "certificate-refused", `Refused: ${pair.reason}.`, { reason: pair.reason });
    } else if (!isCertificatePem(body.caPem)) {
      refused.add("caPem", "certificate-refused", "Refused: not-pem.", { reason: "not-pem" });
    } else {
      putEapCertificate(body.certificatePem, pair.chain, body.caPem, actorOf(c) ?? "");
    }
  }
  if (refused.any()) {
    return problem(c, 422, { type: "/problems/validation", title: "That cannot be saved", status: 422, ...refused.problem() });
  }
  const after = radiusSettings();
  audit(c, {
    kind: "network-settings-changed",
    siteId: null,
    params: {
      eapCertificate: { fingerprintSha256: after.eapCertificate.fingerprintSha256, notAfter: after.eapCertificate.notAfter },
    },
    subject: { kind: "radius-server", id: "radius-server" },
  });
  return c.json(conformResponse("putEapCertificate", 200, after));
});

api.post("/network/radius/sync", (c) =>
  c.json(conformResponse("syncRadiusClients", 200, syncAfterClientWrite(c, "operator"))),
);

api.post("/network/radius/apply", (c) => {
  const rec = applyRadius(actorOf(c));
  audit(c, {
    kind: "radius-configuration-applied",
    siteId: null,
    params: { outcome: rec.outcome },
    subject: { kind: "radius-server", id: "radius-server" },
  });
  return c.json(conformResponse("applyRadiusConfiguration", 200, rec));
});

api.get("/network/coa-timing", (c) =>
  c.json(
    conformResponse("listCoaTiming", 200, {
      items: world.nas.filter((n) => speaksCoA(n.driver)).map(coaTiming),
      defaults: { ...COA_DEFAULTS },
      bounds: {
        timeoutMs: { minimum: COA_BOUNDS.timeoutMs[0], maximum: COA_BOUNDS.timeoutMs[1], default: COA_DEFAULTS.timeoutMs },
        retries: { minimum: COA_BOUNDS.retries[0], maximum: COA_BOUNDS.retries[1], default: COA_DEFAULTS.retries },
      },
    }),
  ),
);

api.put("/nas/:nasId/coa-timing", async (c) => {
  const nas = world.nas.find((n) => n.id === c.req.param("nasId"));
  if (!nas) return notFound(c);
  const body = (await c.req.json()) as NasCoaTimingUpdate;
  const refused = fieldRefusals();
  if (!speaksCoA(nas.driver)) {
    refused.add("nasId", "incorrect", "This device's driver does not send CoA itself.");
  }
  let next: { timeoutMs: number; retries: number } | null = null;
  if (body.timeoutMs !== null || body.retries !== null) {
    next = { timeoutMs: body.timeoutMs ?? COA_DEFAULTS.timeoutMs, retries: body.retries ?? COA_DEFAULTS.retries };
    const [tMin, tMax] = COA_BOUNDS.timeoutMs;
    const [rMin, rMax] = COA_BOUNDS.retries;
    refuseBound(refused, "timeoutMs", next.timeoutMs, { minimum: tMin, maximum: tMax });
    refuseBound(refused, "retries", next.retries, { minimum: rMin, maximum: rMax });
  }
  if (refused.any()) {
    return problem(c, 422, { type: "/problems/validation", title: "That cannot be saved", status: 422, ...refused.problem() });
  }
  setCoaTiming(nas.id, next);
  const view = coaTiming(nas);
  audit(c, {
    kind: "network-settings-changed",
    siteId: null,
    params: { timeoutMs: view.timeoutMs, retries: view.retries, source: view.source },
    subject: { kind: "nas", id: nas.id, name: nas.name },
  });
  return c.json(conformResponse("updateNasCoaTiming", 200, view));
});

// ── maintenance: the control plane's own schedule ─────────────────────────────
//
// Configurable from the console, like every setting. `internal/api/handlers_settings.go`
// is the real thing; `settings.ts` holds the limits.

api.get("/privacy/maintenance", (c) =>
  c.json(conformResponse("getMaintenanceSettings", 200, maintenanceSettings())),
);

api.put("/privacy/maintenance", async (c) => {
  const body = (await c.req.json()) as MaintenanceSettingsUpdate;
  const shape = requestFault("MaintenanceSettingsUpdate", body, "");
  if (shape) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      params: { fields: { [shape.field]: shape.fault } },
      errors: { [shape.field]: shape.message },
    });
  }
  const refused = fieldRefusals();
  for (const f of maintenanceFaults(body)) {
    if (f.fault === "below-minimum") refused.add(f.field, f.fault, "Too small.", { minimumCount: f.figure });
    else refused.add(f.field, f.fault, "Too large.", { maximumCount: f.figure });
  }
  if (refused.any()) {
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      ...refused.problem(),
    });
  }
  const changed = setMaintenanceSettings(body, sessionOf(c)?.operator.username ?? null);
  audit(c, {
    kind: "maintenance-settings-changed",
    siteId: null,
    params: { changed },
    subject: { kind: "maintenance-settings", id: "maintenance-settings" },
  });
  return c.json(conformResponse("updateMaintenanceSettings", 200, maintenanceSettings()));
});

// ── guest data and marketing consent ──────────────────────────────────────────
//
// `internal/api/handlers_guestdata.go` is the real thing; see `guestdata.ts` for the
// rule both copies keep.

api.get("/sites/:siteId/guest-data", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  return c.json(conformResponse("getGuestDataSettings", 200, guestDataSettings(siteId)));
});

api.put("/sites/:siteId/guest-data", async (c) => {
  const siteId = c.req.param("siteId");
  const venue = site(siteId);
  if (!venue) return notFound(c);
  const body = (await c.req.json()) as GuestDataSettingsUpdate;
  const fault = guestDataFault(body);
  if (fault) {
    const refused = fieldRefusals();
    refused.add(fault.field, fault.fault, fault.message);
    return problem(c, 422, {
      type: "/problems/validation",
      title: "That cannot be saved",
      status: 422,
      ...refused.problem(),
    });
  }
  const after = putGuestDataSettings(siteId, body);
  audit(c, {
    kind: "guest-data-settings-changed",
    siteId,
    params: {
      fields: after.fields.map((f) => (f.required ? `${f.field} (required)` : f.field)),
      marketingConsent: after.marketingConsent.enabled,
    },
    subject: { kind: "site", id: venue.id, name: venue.name },
  });
  return c.json(conformResponse("updateGuestDataSettings", 200, after));
});

api.get("/sites/:siteId/marketing-consents", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  const items = marketingConsents(siteId, q(c, "state"));
  return c.json(conformResponse("listMarketingConsents", 200, page(items, q(c, "cursor"), limitOf(c))));
});

api.post("/sites/:siteId/marketing-consents/:consentId/withdraw", (c) => {
  const siteId = c.req.param("siteId");
  if (!site(siteId)) return notFound(c);
  const consent = withdrawConsent(siteId, c.req.param("consentId"));
  if (!consent) return notFound(c);
  audit(c, {
    kind: "marketing-consent-withdrawn",
    siteId,
    subject: { kind: "marketing-consent", id: consent.id },
  });
  return c.json(conformResponse("withdrawMarketingConsent", 200, consent));
});

/**
 * The guest's own way out. The same `303` whether or not anything was withdrawn:
 * whether a device ever consented is not something a stranger holding its MAC
 * should learn by pressing a button.
 */
api.post("/portal/marketing-consent/withdraw", async (c) => {
  const client = portalClient(c);
  const form = await c.req.parseBody();
  const lang = String(form.lang ?? "en") === "vi" ? "vi" : "en";
  c.header("Cache-Control", "no-store");
  // Only the session that holds a grant — `WithdrawOwnMarketingConsent`.
  if (client?.grantId) withdrawDevice(client.siteId, client.mac);
  return portalRedirect(c, `/${lang}/connected/?n=consent-withdrawn`);
});

// ── the venue chooser ────────────────────────────────────────────────────────

/*
 * Outside `/api/v0` on purpose. This is not an operation and must never look like
 * one: the contract is the deliverable, and a mock-only endpoint
 * inside the API surface is exactly how a viewer comes to believe the backend
 * will implement it. It sits beside `/health`, which is the same kind of thing.
 */

const escapeHtml = (s: string) =>
  s.replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[ch]!);

app.get("/venues", (c) => {
  const current = venueById(getCookie(c, VENUE_COOKIE)) ?? defaultVenue();
  const rows = VENUES.map(
    (v) => `<li${v.id === current.id ? ' aria-current="true"' : ""}>
      <a href="/venues/${v.id}"><strong>${escapeHtml(v.label)}</strong></a>
      <p>${escapeHtml(v.proves)}</p>
      <p class="meta">advertises ${v.context.languages.join(", ")} ·
        terms in ${Object.keys(v.context.terms.summary ?? {}).join(", ") || "no language"}</p>
    </li>`,
  ).join("\n");

  return c.html(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Foyer mock — venue profiles</title>
<style>
  body { font: 15px/1.55 ui-sans-serif, system-ui, sans-serif; max-width: 46rem; margin: 3rem auto; padding: 0 1.25rem; }
  ul { list-style: none; padding: 0; }
  li { border: 1px solid #8884; border-radius: 8px; padding: .9rem 1rem; margin: .6rem 0; }
  li[aria-current] { border-color: #2a7; border-width: 2px; }
  p { margin: .35rem 0 0; }
  .meta { opacity: .65; font-size: .85em; }
  code { background: #8882; padding: .1em .35em; border-radius: 4px; }
</style></head><body>
<h1>Venue profiles</h1>
<p>Pick one and you land back on the portal rendered as that venue. Currently
serving <strong>${escapeHtml(current.label)}</strong>.</p>
<ul>${rows}</ul>
<p class="meta">The choice is a <code>foyer_mock_venue</code> cookie, so it follows you
across the portal until you change it. For a single API call without setting anything,
use <code>/api/v0/portal/context?venue=&lt;id&gt;</code>. Every response says which profile
answered in <code>X-Foyer-Mock-Venue</code>.</p>
</body></html>`);
});

app.get("/venues/:venueId", (c) => {
  const venue = venueById(c.req.param("venueId"));
  if (!venue) return c.redirect("/venues", 303);
  setCookie(c, VENUE_COOKIE, venue.id, { sameSite: "Lax", path: "/" });
  /*
   * Back to the portal, not to a JSON body: the point of this route is to put the
   * viewer in front of the screen the profile changes.
   *
   * Straight to a language the venue actually advertises, rather than to `/` — and
   * the difference is not cosmetic. `/` redirects to the portal's *default*
   * language, so choosing the Vietnamese-only venue and landing on `/en/` shows a
   * switcher (the one language the venue does speak) instead of the no-switcher
   * screen the profile exists to demonstrate. The backend negotiates
   * `Accept-Language` **intersected with the site's advertised languages**; the dev
   * middleware does the redirect but not the intersection, which is a gap in the
   * portal's dev middleware, not something this route should paper over silently.
   */
  const landing = venue.context.languages[0] ?? "en";
  return c.redirect(`${PORTAL_ORIGIN}/${landing}/`, 303);
});

// ── teardown, which is a property of the harness and not of the product ───────

/**
 * The batches this world was born with, captured before anything can create one.
 *
 * The teardown below may remove **only** what a test made. `web/e2e` runs its
 * workers in parallel against one mock, so a teardown that could reach a seeded
 * batch is a teardown that can empty the ugly world out from under every other
 * worker — and the failure would land in a file that did nothing wrong.
 */
const SEEDED_BATCH_IDS = new Set(world.batches.map((b) => b.id));

/**
 * The voucher the fixtures strand on the unreachable access point, found the way the
 * fixture guard finds it — by the relationship, never by an id. The boot banner names
 * its code so a viewer can go straight to the failed revoke.
 */
function strandedVoucherCode(): string | null {
  const unreachable = new Set(world.nas.filter((n) => n.reachability !== "ok").map((n) => n.id));
  const found = world.vouchers.find((v) => {
    if (v.state !== "in-use") return false;
    const s = world.sessions.find(
      (x) => x.voucherId === v.id && (x.state === "online" || x.state === "orphaned"),
    );
    return Boolean(s && unreachable.has(String(s.nasId)));
  });
  return found?.code ?? null;
}

/**
 * `DELETE /mock/voucher-batches/:batchId` — the e2e suite's teardown.
 *
 * **The contract deliberately does not gain a delete, and this is the argument.**
 *
 * The leak is the e2e suite's: `revoke` cases create a batch per run, and they
 * accumulate across runs against one mock. Nothing an operator does creates a batch
 * per run. A fix belongs where the cause is, and the cause is a harness.
 *
 * Adding `DELETE /voucher-batches/{id}` to the contract would be a product claim, and
 * not a small one. It is a change that cannot be undone and is security-relevant, so
 * it earns an `AuditKind` — which is a `Record` in the console and therefore a compile
 * error in the console's tree as well. It needs a refusal for a batch whose codes are in use, which is `/problems/controller-in-use`'s
 * twin and another catalogue entry. And the product argument runs the other way twice
 * over: `DiscoveryResult.missing` is *marked, never deleted*, `/problems/
 * controller-in-use` refuses rather than orphan the rows underneath, and a voucher
 * batch is referenced by `Voucher`, by `Session.voucherId` and by
 * `AuthAttempt.voucherId`. Deleting one strands the answer to *which code did this
 * guest use*, which is the question `/attempts` exists to answer. Not accounting, so
 * `docs/rules/frontend.md`'s read-only rule does not literally reach it — the same
 * class of record, though, and the same reason.
 *
 * So it is the fourth affordance of the shape this file already has:
 * `FOYER_MOCK_LIVE=0`, `?present=0`, `X-Foyer-Mock-Fault`. Outside `/api/v0` on
 * purpose — no `operationId`, nothing in `permissions.ts`, nothing in the drivability
 * sweeps, and no chance of being read as product surface. It answers plainly rather
 * than with a problem document for the same reason: an RFC 9457 body here would put a
 * refusal in the product's keyspace that no product route composes.
 */
app.delete("/mock/voucher-batches/:batchId", (c) => {
  const batchId = c.req.param("batchId");

  if (SEEDED_BATCH_IDS.has(batchId)) {
    return c.json(
      {
        error: "seeded",
        batchId,
        message:
          "This batch is part of the fixture world. Teardown removes only what a test " +
          "created — the ugly fixtures are what every other worker is asserting against.",
      },
      409,
    );
  }

  const i = world.batches.findIndex((b) => b.id === batchId);
  if (i < 0) return c.json({ error: "not-found", batchId }, 404);

  /* The vouchers go with it. A batch removed on its own leaves rows pointing at a
     batch that is not there, which is a defect the mock would be inventing rather
     than modelling — the same reason eviction in `state.ts` clears
     `currentSessionId`. Sessions and attempts are left alone: they are the record of
     what happened, and a teardown that rewrote history would be the one thing this
     affordance exists to avoid doing to the product. */
  const [removed] = world.batches.splice(i, 1);
  /* Spliced in place rather than reassigned: `world.vouchers` is the array every
     handler in this file closes over by property access, and swapping it for a new
     one is the kind of aliasing bug that shows up three screens away. */
  let vouchersRemoved = 0;
  for (let n = world.vouchers.length - 1; n >= 0; n -= 1) {
    if (world.vouchers[n]!.batchId === batchId) {
      world.vouchers.splice(n, 1);
      vouchersRemoved += 1;
    }
  }

  return c.json({ batchId, label: removed!.label, vouchersRemoved });
});

// ── boot ─────────────────────────────────────────────────────────────────────

/**
 * The fake provider's outbox — what phone sign-in would have sent. A review
 * affordance, outside `/api/v0` and outside the contract, so a person driving
 * `pnpm dev` can finish the flow. The Go binary has no such route.
 */
app.get("/mock/phone-outbox", (c) => c.json(phoneOutbox()));

app.route("/api/v0", api);
app.get("/health", (c) => c.json({ ok: true }));

/**
 * The router, so a test can drive a handler without a socket.
 *
 * `app.request()` runs the real route, the real branch and the real `conform()`
 * call — which for an error branch is the only way it ever runs at all. A 4xx
 * that is written and never executed proves nothing; `server.test.ts` executes them.
 */
export { app };

/*
 * Listen only when this module *is* the process.
 *
 * Vitest imports it for `app.request()`, and an import that binds :5170 would take
 * the port from whichever dev server is already running — and start the simulation
 * clock inside a unit-test worker on the way. `process.env.VITEST` is set by the
 * runner in every worker; `server.test.ts` asserts that it is, so this guard cannot
 * rot into a permanently-false condition that silently stops the mock booting.
 */
if (!process.env.VITEST) {
  serve({ fetch: app.fetch, port: PORT }, (info) => {
    const counts = {
      nas: world.nas.length,
      sessions: world.sessions.length,
      vouchers: world.vouchers.length,
      accounts: world.accounts.length,
      operators: world.operators.length,
      audit: world.audit.length,
    };
    console.log(`  mock api   http://localhost:${info.port}/api/v0`);
    console.log(
      `  fixtures   ${counts.nas} nas · ${counts.sessions} sessions · ${counts.vouchers} vouchers · ${counts.accounts} accounts`,
    );
    console.log(
      `             ${counts.operators} operators · ${counts.audit} audit entries`,
    );
    /*
     * Announced for the same reason as the probe prefixes below: a viewer cannot
     * approve a screen whose states they cannot reach from the interface — and the
     * password is one of those states. Sign-in **verifies** the password.
     *
     * There is still no static credential: §7 forbids one, so the password is
     * generated per process exactly as `deploy/first-boot.sh` generates the first
     * administrator's, and printed here exactly once. `FOYER_MOCK_ADMIN_PASSWORD`
     * pins it for scripted drivers. See `DEV_PASSWORD` in `state.ts`.
     *
     * Nothing requires a session yet: see `REQUIRE_SESSION`. That limitation and this
     * one are documented in the same voice, so neither is hidden behind the other.
     */
    console.log(
      `  sign in    password ${DEV_PASSWORD}${DEV_PASSWORD_PINNED ? " (pinned by FOYER_MOCK_ADMIN_PASSWORD)" : " — generated for this process, as first boot does"}`,
    );
    console.log(
      `             ${world.operators.filter((o) => o.enabled).map((o) => `${o.username} (${o.role})`).join(", ")}`,
    );
    /* A fresh deployment has nobody to sign in as: Foyer ships no credential, and
       the installer prints a one-time setup code instead. So does this. */
    if (SETUP_CODE) {
      console.log(
        `  setup      code ${SETUP_CODE}${process.env.FOYER_MOCK_SETUP_CODE ? " (pinned by FOYER_MOCK_SETUP_CODE)" : " — generated for this process, as first boot does"}`,
      );
    }
    /* And the guest half, announced for the same reason. The portal's account login
       verifies the password and the site, so a viewer needs telling that the
       fixtures' accounts take this same generated password. */
    console.log(
      "             the same password signs the fixtures' guest accounts in on the portal",
    );
    console.log("  responses validated against contract/openapi.yaml on every request");
    console.log(
      `  venues     http://localhost:${info.port}/venues — ${VENUES.length} profiles, serving ${defaultVenue().id}`,
    );
    /*
     * The failure path of the registration flow, addressed to whoever is reviewing
     * it. A branch a viewer cannot reach from the interface is a branch they
     * cannot approve, and without this line the only way to see an unreachable
     * probe would be to open a fixture file. See `probeReachability`.
     */
    console.log(
      `  probe      register an access point on ${UNREACHABLE_PREFIX}x to probe unreachable,`,
    );
    console.log(
      `             ${CREDENTIALS_EXPIRED_PREFIX}x for expired credentials (RFC 5737). Anything else answers ok.`,
    );
    /*
     * And the below-floor controller state — announced rather than left to be
     * found, because it is the one an operator is least likely to go looking for:
     * the controller answers, the credential is accepted, and nothing else on the
     * dashboard says a word about it. Read off the world so the line cannot drift
     * from the row it describes.
     */
    for (const c of world.controllers.filter((x) => x.versionSupported === false)) {
      console.log(
        `             ${c.name} runs ${c.version}, below the ${CONTROLLER_MINIMUM_VERSION[c.vendor]} this driver needs — probe it.`,
      );
    }
    /*
     * The other two branches a viewer cannot otherwise reach from the interface.
     * Announced for the same reason as the probe prefixes above: the console's copy
     * for a 500, and for a revocation whose session could not be ended, are both
     * screens nobody has ever seen render.
     */
    console.log(
      `  faults     X-Foyer-Mock-Fault: ${Object.keys(INJECTABLE_FAULTS).join("|")} on any /api/v0 request answers 500,`,
    );
    console.log(
      "             and /api/v0/portal/context?nas=nas-unifi-3 puts the guest on an",
    );
    console.log(
      "             access point nothing can reach, so revoking their voucher cannot end the session.",
    );
    /* And that state is in the world rather than only at the end of a drive, which
       is the difference between a branch a viewer can be shown and one
       they have to be walked to. */
    console.log(
      `             The same failure is already on /vouchers: ${
        strandedVoucherCode() ?? "—"
      } is in use on that access point.`,
    );
    /* The teardown, announced for the same reason as everything else here: an
       affordance nobody can find is one the next harness reinvents. */
    console.log(
      "  teardown   DELETE /mock/voucher-batches/:id removes a batch a test created, and its",
    );
    console.log(
      "             vouchers with it. Refuses a seeded batch — the ugly world is not a test's to drop.",
    );
    /* The one thing this server deletes, said out loud for the same reason the
       compressed clock is: a viewer must not have to discover a mock affordance by
       noticing that something they made has gone. */
    console.log(
      `  sessions   the last ${LIVE_SESSION_WINDOW} portal arrivals per site are kept; older ones are dropped so the`,
    );
    console.log(
      "             authored rows — the 40-day orphan, the wrapping counter, the duplicate MAC —",
    );
    console.log("             stay on the first page. Nothing the fixtures wrote is ever dropped.");

    if (LIVE) {
      for (const line of sim.describe()) console.log(line);
      sim.start();
    } else {
      console.log("  clock      frozen — FOYER_MOCK_LIVE=0. The world is a snapshot.");
    }
  });
}
