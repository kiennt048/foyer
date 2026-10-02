/**
 * The typed surface of `openapi.yaml`.
 *
 * `generated/types.ts` is produced by `pnpm generate` and is committed but never
 * hand-edited. Everything below is a convenience alias over it, so that a change to
 * the contract shows up as a type error in the mock and in both applications rather
 * than as a runtime surprise.
 *
 * This is the mechanism behind the rule in `docs/rules/frontend.md`: a handler
 * for an operation that does not exist in the contract does not compile.
 */
import type { components, operations, paths } from "./generated/types.js";

export type { components, operations, paths };

type S = components["schemas"];

export type Language = S["Language"];
export type Problem = S["Problem"];
/**
 * The closed set of failures, and the reason the three aliases below exist at all.
 *
 * A client catalogue keyed on `ProblemType` is a `Record<ProblemType, Copy>`, so a
 * missing entry is a compile error. Keyed on `string` it is a runtime hole that
 * nothing can find. `FieldFault` is the same argument one level down, for the
 * `(type, field, fault)` triple; `Failure` is the same keyspace for a failure that
 * arrives inside a `200`.
 */
export type ProblemType = S["ProblemType"];
export type FieldFault = S["FieldFault"];
export type Failure = S["Failure"];

/**
 * Every key of the map below is checked against `FieldFault`: a key that is not a
 * fault is required to be `never` and therefore cannot be written. A plain
 * `Partial<Record<FieldFault, object>>` looks like it does this and does the opposite
 * — it makes *every* fault a key, which would put `required` and `duplicate` back in
 * the set of faults that need a figure.
 */
type FaultParamsShape<T extends { [K in keyof T]: K extends FieldFault ? object : never }> = T;

/**
 * The faults whose sentence contains a figure, and the figure each one needs.
 *
 * `FieldFault` says *why* a field was refused and the client owns the wording, which
 * works while the wording is a whole sentence — *Required.*, *Already taken.* Two of
 * them are a sentence with a **number** in it, and a number is exactly what a client
 * cannot know. `below-minimum` with no minimum renders *"At least 0."*: a catalogue
 * entry that compiles, an alert that mounts with text, and a wrong sentence.
 *
 * This is the type-level half of `x-fault-params` in `openapi.yaml`. The document is
 * the authority and `web/mock` asserts the two agree, so this cannot drift into a
 * second, quieter contract. It exists because the interesting property is a compile
 * error rather than a test failure: `fieldRefusals().add()` in the mock takes its
 * params from here, so refusing a field `below-minimum` **without** a figure does not
 * build. The runtime sweep still runs, for the refusals that do not use that helper.
 *
 * Extending it is a contract edit first: add the entry to `x-fault-params`, mirror it
 * here, then find every emission — the sweep will name them.
 */
export type FaultParams = FaultParamsShape<{
  "below-minimum": { minimumCount: number };
  "above-maximum": { maximumCount: number };
  "too-large": { bytes: number; limitBytes: number };
  "range-too-long": { maxDays: number };
  "certificate-refused": { reason: string };
}>;

/** A fault that names params, or `never` for the ones whose sentence is complete. */
export type FaultWithParams = keyof FaultParams & FieldFault;

/**
 * The same map as a value, so a test can compare it with the document key by key.
 *
 * `keyof` on each entry rather than a hand-typed list of strings: a param renamed in
 * `FaultParams` and not here is a compile error, which is the half a runtime test
 * cannot see.
 */
export const FAULT_PARAMS: {
  readonly [K in FaultWithParams]: readonly (keyof FaultParams[K] & string)[];
} = {
  "below-minimum": ["minimumCount"],
  "above-maximum": ["maximumCount"],
  "too-large": ["bytes", "limitBytes"],
  "range-too-long": ["maxDays"],
  "certificate-refused": ["reason"],
};
export type ChangeClass = S["ChangeClass"];
export type Freshness = S["Freshness"];
export type FreshnessState = NonNullable<S["Freshness"]["state"]>;

export type ControllerVendor = S["ControllerVendor"];
export type ControllerAuthMode = S["ControllerAuthMode"];
export type TlsMode = S["TlsMode"];
export type Controller = S["Controller"];
export type ControllerCreate = S["ControllerCreate"];
export type ControllerUpdate = S["ControllerUpdate"];
export type ControllerProbe = S["ControllerProbe"];
export type ControllerCheckKind = NonNullable<ControllerProbe["checks"]>[number]["kind"];
export type DiscoveryResult = S["DiscoveryResult"];
/**
 * What a driver *needs*, rather than what it can do.
 *
 * **Deliberately not in `openapi.yaml`.** It is not wire data: it follows from the
 * vendor's API and is identical in every deployment, so a server sending it would be
 * telling the client something the client already knows and could disagree with.
 * `Capabilities` is the opposite — measured per device, and therefore on the wire.
 */
export interface DriverShape {
  controller: "required" | "none";
  radiusClients: "discovered" | "manual";
  accessPoints: "discovered" | "implicit-single";
  authorize: "controller-api" | "coa" | "northbound-api";
  revoke: "controller-api" | "coa" | "northbound-api";
  /**
   * Whether registering this driver's device must also say **which** CoA vendor it
   * is.
   *
   * `"required"` for exactly one driver, and it is not a quirk of that driver: `coa`
   * is the one enum member that names a *family* rather than a vendor. Cisco and
   * Aruba identify a session differently — an `audit-session-id` AV-pair against a
   * `Calling-Station-Id` in Aruba's own spelling — and RFC 5176 §2.3 makes the NAS
   * match on every session-identification attribute in the request, so the wrong one
   * matches nothing. A careful device answers `Error-Cause 503`; a careless one
   * **ignores the request and acknowledges it**, which is the failure neither the
   * lab nor a typed error can see.
   *
   * `"none"` everywhere else because those drivers name their vendor by naming
   * themselves, and `coaVendor` on one of them is a value nothing reads.
   */
  coaVendor: "required" | "none";
}
export type NasSource = S["NasSource"];

/**
 * What each driver needs, and it follows from the vendor's API rather than taste.
 *
 * Declared once, here, because three places have to agree about it: the registration
 * form asks different questions per driver, the mock refuses the combinations that
 * cannot work, and the console decides whether to offer manual registration at all.
 * Three copies of this table is three copies that drift, and the drift would present
 * as a form that cannot connect — which is the defect this table exists to prevent.
 */
export const DRIVER_SHAPES: Record<NasDriverKind, DriverShape> = {
  unifi: {
    controller: "required",
    radiusClients: "discovered",
    accessPoints: "discovered",
    authorize: "controller-api",
    /* Revocation *can* use CoA — Ubiquiti documents port 3799 — but authorization
       cannot, and that narrower claim is the one that is true.
       `docs/rules/nas-drivers.md`. */
    revoke: "controller-api",
    coaVendor: "none",
  },
  omada: {
    controller: "required",
    radiusClients: "discovered",
    accessPoints: "discovered",
    authorize: "controller-api",
    revoke: "controller-api",
    coaVendor: "none",
  },
  ruckus: {
    controller: "required",
    radiusClients: "discovered",
    accessPoints: "discovered",
    /* Not CoA. SmartZone's external-portal model is the Northbound JSON API, and
       PacketFence documents RADIUS deauth failing outright in this mode. */
    authorize: "northbound-api",
    revoke: "northbound-api",
    coaVendor: "none",
  },
  mikrotik: {
    /* The router is its own controller. Nothing to register separately. */
    controller: "none",
    radiusClients: "manual",
    accessPoints: "implicit-single",
    authorize: "coa",
    revoke: "coa",
    /* MikroTik is a vendor, so the dialect follows from the driver. */
    coaVendor: "none",
  },
  coa: {
    controller: "none",
    radiusClients: "manual",
    accessPoints: "implicit-single",
    authorize: "coa",
    revoke: "coa",
    /* The one member that names a family. Cisco and Aruba both live here and they
       do not identify a session the same way, so the operator has to say which. */
    coaVendor: "required",
  },
};

/** Whether this driver's devices are registered by hand, or read from a controller. */
export function needsController(driver: NasDriverKind): boolean {
  return DRIVER_SHAPES[driver].controller === "required";
}

/**
 * Whether registering this driver's device must also name a CoA vendor.
 *
 * Read from the table rather than compared against `"coa"` in three places, for the
 * reason the table exists: the registration form asks the question, the mock refuses
 * the body that does not answer it, and the server refuses it again. Three copies
 * drift, and the drift here presents as a device that registers cleanly and can
 * never be sent a packet.
 */
export function needsCoaVendor(driver: NasDriverKind): boolean {
  return DRIVER_SHAPES[driver].coaVendor === "required";
}

export type GardenKind = S["GardenKind"];
export type Capabilities = S["Capabilities"];
export type NasDriverKind = S["NasDriverKind"];
export type NasReachability = S["NasReachability"];
/**
 * Which of two RFC 5176 dialects a `coa` device speaks.
 *
 * Aliased here for the same reason as every other enum on this page: without it the
 * one consumer that needs the union — the console's vendor label map — would reach
 * through the wire type as `NonNullable<Nas["coaVendor"]>`, which is a different type
 * from the schema's own the moment `Nas.coaVendor` gains a nullable wrapper, an
 * `undefined`, or a second property that happens to be spelled the same.
 *
 * `CoaVendor` is a named schema in `openapi.yaml` and is generated into
 * `components["schemas"]`; this is an alias over it and adds no operation.
 */
export type CoaVendor = S["CoaVendor"];
export type Nas = S["Nas"];
export type NasCreate = S["NasCreate"];
export type NasProbe = S["NasProbe"];
/** How a device's capabilities and wire attributes are known — `getNasEvidence`. */
export type DriverEvidence = S["DriverEvidence"];
export type EvidenceFact = S["EvidenceFact"];
export type EvidenceStanding = S["EvidenceStanding"];
export type ProbeWarning = S["ProbeWarning"];
export type ProbeWarningKind = S["ProbeWarningKind"];

export type VoucherState = S["VoucherState"];
export type ExpiryMode = S["ExpiryMode"];
export type VoucherBatch = S["VoucherBatch"];
export type VoucherBatchCreate = S["VoucherBatchCreate"];
export type Voucher = S["Voucher"];
export type Plan = S["Plan"];
export type PlanRequest = S["PlanRequest"];

export type SessionState = S["SessionState"];
export type Session = S["Session"];
export type SessionDisconnectRequest = S["SessionDisconnectRequest"];
export type SessionDisconnectResult = S["SessionDisconnectResult"];
export type PortalAuthorization = S["PortalAuthorization"];
export type PortalGrantDisconnectRequest = S["PortalGrantDisconnectRequest"];

export type Account = S["Account"];
export type AccountCreate = S["AccountCreate"];

export type Operator = S["Operator"];
export type OperatorRequest = S["OperatorRequest"];
export type OperatorUpdate = S["OperatorUpdate"];
export type OperatorRole = S["OperatorRole"];
export type OperatorPermission = S["OperatorPermission"];
export type AuthSession = S["AuthSession"];
export type SignInRequest = S["SignInRequest"];
export type PasswordChange = S["PasswordChange"];
/** The first-access setup — Foyer ships no credential (`completeAdminSetup`). */
export type AdminSetupState = S["AdminSetupState"];
export type AdminSetupRequest = S["AdminSetupRequest"];

/**
 * What each role may do — the **v1 derivation**, and the server's, not the client's.
 *
 * Deliberately here rather than in `openapi.yaml`, for the same reason as
 * `DRIVER_SHAPES` above: it is not wire data. It follows from a product decision, it
 * is identical in every deployment, and it is what a *server* implements when it
 * answers `GET /auth/session`. A server sending the whole table would be telling the
 * client something the client could then disagree with.
 *
 * **What a client reads is `AuthSession.permissions`, always.** That is the same
 * split as `DriverShape` and `Capabilities`: the shape of the thing is shared
 * knowledge, the answer for *this* subject is measured and travels on the wire. A
 * console that gated on this table would keep gating on it after a deployment scoped
 * a role per site or added a third one — and the failure would present as a control
 * that renders and then `403`s, which is the defect capability gating exists to
 * prevent.
 *
 * `Record<OperatorRole, …>` over a closed enum, so adding a role is a compile error
 * here rather than a role with no permissions at runtime. `web/mock` asserts that
 * what it puts on the wire is exactly this, for every role in the enum.
 *
 * The split between `:read` and `:manage` exists only where the two roles differ —
 * see `OperatorPermission` in `openapi.yaml` for why a fuller CRUD matrix would be a
 * distinction the product does not have.
 */
export const ROLE_PERMISSIONS: Record<OperatorRole, readonly OperatorPermission[]> = {
  administrator: [
    "sessions:read",
    "sessions:manage",
    "vouchers:manage",
    "accounts:manage",
    "nas:read",
    "nas:manage",
    "garden:read",
    "garden:manage",
    "portal:read",
    "portal:manage",
    "operators:manage",
    "audit:read",
    /* Retention periods and erasing one guest's data. Administrators only: an
       erasure cannot be undone and a period set too short deletes history. */
    "privacy:manage",
    /* Administrators only — a bulk offline copy of personal data outlives the
       session and cannot be recalled. See the `reports` tag. */
    "reports:export",
  ],
  /*
   * The counter, not the network. Staff issue and revoke vouchers, look guests up
   * and end their sessions (`sessions:manage` — see `OperatorPermission` for why a
   * split both roles hold exists at all),
   * and manage guest accounts; they configure nothing.
   *
   * They **read** the access points, the walled garden and the portal settings, and
   * that is the narrow reading rather than an oversight: a console where a staff
   * member cannot see that shared secrets exist is a console where they cannot tell
   * an administrator that one needs rotating. The role makes those actions somebody
   * else's, not impossible — `docs/rules/frontend.md`.
   */
  staff: [
    "sessions:read",
    "sessions:manage",
    "vouchers:manage",
    "accounts:manage",
    "nas:read",
    "garden:read",
    "portal:read",
  ],
};

/** Whether a signed-in operator may do this. The only gate a client should use. */
export function can(
  session: Pick<AuthSession, "permissions"> | null | undefined,
  permission: OperatorPermission,
): boolean {
  return session?.permissions?.includes(permission) ?? false;
}

/**
 * The shortest password an operator account may have, defined once and served to
 * whoever is asking.
 *
 * Here rather than in `openapi.yaml` for the same reason as the byte budgets: the
 * console has to **state the rule before the operator types**, and a rule the client
 * can only discover by being refused is a form that rejects work already done. The
 * server refuses below it as well — this is the first check, not the only one.
 *
 * Twelve, and the number is a floor rather than a ceiling: NIST SP 800-63B puts the
 * minimum at 8 and explicitly prefers length over composition rules, so there is no
 * character-class requirement here and there must not be one. A console that demanded
 * a symbol would be trading a measured improvement for a memorised one.
 */
export const PASSWORD_MIN_LENGTH = 12;

/*
 * There is no shipped credential here, and there must not be one. The first
 * administrator is created by `completeAdminSetup` with a one-time code the
 * installer prints.
 */

export type AuditKind = S["AuditKind"];
export type AuditEntry = S["AuditEntry"];

/* ── privacy: retention periods and data-subject erasure ──────────────────── */
export type RetentionCategoryId = S["RetentionCategoryId"];
export type RetentionCategoryPolicy = S["RetentionCategoryPolicy"];
export type RetentionPolicy = S["RetentionPolicy"];
export type RetentionPolicyUpdate = S["RetentionPolicyUpdate"];
export type DataSubjectKind = S["DataSubjectKind"];
export type DataSubjectErasureRequest = S["DataSubjectErasureRequest"];
export type DataSubjectErasure = S["DataSubjectErasure"];
/* ── the control plane's maintenance schedule (admin-configurability rule) ── */
export type MaintenanceSettings = S["MaintenanceSettings"];
export type MaintenanceSettingsUpdate = S["MaintenanceSettingsUpdate"];
export type MaintenanceSettingLimit = S["MaintenanceSettingLimit"];
/* ── the guest network's Wi-Fi password, for the voucher slip ─────────────── */
export type GuestWifiKeyStatus = S["GuestWifiKeyStatus"];
export type GuestWifiKeyUpdate = S["GuestWifiKeyUpdate"];
export type VoucherSlipWifi = S["VoucherSlipWifi"];
export type AuditSubject = S["AuditSubject"];

/* ── guest data and marketing consent ─────────────────────────────────────── */
export type GuestDataField = S["GuestDataField"];
export type GuestDataFieldSetting = S["GuestDataFieldSetting"];
export type GuestDataSettings = S["GuestDataSettings"];
export type GuestDataSettingsUpdate = S["GuestDataSettingsUpdate"];
export type ConsentText = S["ConsentText"];
export type PortalDataCollection = S["PortalDataCollection"];
export type MarketingConsent = S["MarketingConsent"];
export type MarketingConsentState = S["MarketingConsentState"];
export type ConsentWithdrawnBy = S["ConsentWithdrawnBy"];

/* ── network: transport and RADIUS-plane settings ─────────────────────────── */
export type NetworkSettings = S["NetworkSettings"];
export type NetworkSettingsUpdate = S["NetworkSettingsUpdate"];
export type NetworkSettingSource = S["NetworkSettingSource"];
export type NetworkBound = S["NetworkBound"];
export type NetworkSettingDefaults = S["NetworkSettingDefaults"];
export type CoaTimingBounds = S["CoaTimingBounds"];
export type PhoneAbuseLimitBounds = S["PhoneAbuseLimitBounds"];
export type SignInThrottle = S["SignInThrottle"];
export type SignInThrottles = S["SignInThrottles"];
export type BodyCeilings = S["BodyCeilings"];
export type PortalCertificateStatus = S["PortalCertificateStatus"];
export type PortalCertificateUpload = S["PortalCertificateUpload"];
export type EapCertificateUpload = S["EapCertificateUpload"];
export type RadiusServerSettings = S["RadiusServerSettings"];
export type RadiusServerSettingsUpdate = S["RadiusServerSettingsUpdate"];
export type RadiusClientSync = S["RadiusClientSync"];
export type RadiusApply = S["RadiusApply"];
export type RadiusSyncTrigger = S["RadiusSyncTrigger"];
export type NasCoaTiming = S["NasCoaTiming"];
export type NasCoaTimingUpdate = S["NasCoaTimingUpdate"];
export type CoaTimingValues = S["CoaTimingValues"];

export type Site = S["Site"];
export type SiteCreate = S["SiteCreate"];
export type Metric = S["Metric"];
export type Overview = S["Overview"];
export type AlarmLevel = S["AlarmLevel"];
export type AlarmKind = S["AlarmKind"];
export type Alarm = S["Alarm"];

export type TrendGranularity = S["TrendGranularity"];
export type Trends = S["Trends"];
export type TrendBucket = S["TrendBucket"];

export type PortalContext = S["PortalContext"];
export type PortalStatus = S["PortalStatus"];

export type PortalMethod = S["PortalMethod"];
export type HeroImage = S["HeroImage"];
export type HeroImageUpload = S["HeroImageUpload"];
export type PortalSettings = S["PortalSettings"];
export type PortalSettingsUpdate = S["PortalSettingsUpdate"];
export type PhoneSignIn = S["PhoneSignIn"];
export type PhoneSignInUpdate = S["PhoneSignInUpdate"];
export type PhoneSignInProvider = S["PhoneSignInProvider"];
export type PhoneSignInLimits = S["PhoneSignInLimits"];
export type PhoneAbuseControls = S["PhoneAbuseControls"];
export type PhoneAbuseControlsUpdate = S["PhoneAbuseControlsUpdate"];
export type PhoneSpendCap = S["PhoneSpendCap"];
export type PhoneSpendCapUpdate = S["PhoneSpendCapUpdate"];
export type PhoneTestMessageResult = S["PhoneTestMessageResult"];
export type PhoneTestMessageOutcome = S["PhoneTestMessageOutcome"];
export type SiteTimeZone = S["SiteTimeZone"];
export type SiteTimeZoneUpdate = S["SiteTimeZoneUpdate"];

/**
 * The byte ceilings, defined once and served to the client rather than duplicated.
 *
 * `tools/check-budget.mjs` enforces the same numbers on the built tree, and the two
 * must agree — a console that accepts a 200 kB photograph and a CI gate that
 * rejects it produces an operator who has done nothing wrong and a build that is
 * red. The gate is the second check, not the first: an operator uploading through
 * the console never runs it.
 *
 * `IMAGE_MAX_BYTES` traces to a measurement, not a preference: a real photograph at
 * 900 px — the macOS CNA's full window width — is 89,697 B at JPEG q70
 * (`docs/protocol-reality.md`).
 *
 * **`openapi.yaml` is the authority for that one, and this is a checked copy of it.**
 * Two copies that nothing compares drift apart while both citing the same
 * measurement. The figure lives on `HeroImageUpload.dataBase64` as
 * `x-max-decoded-bytes`: Go reads it out of the embedded document, and `web/mock`'s
 * *"agrees with the byte ceiling the document declares"* holds this constant to it —
 * because a TypeScript module and a Go binary cannot read each other, and an
 * unchecked copy is a copy that drifts.
 *
 * It stays a constant here rather than becoming a document read at runtime: this
 * module is bundled into a browser, the portal build and the mock, and none of them
 * can parse YAML at load time. A copy that is *checked* is the available shape of
 * "derive the stand-in from what it stands in for" across a language boundary.
 */
export const CODE_BUDGET_BYTES = 50 * 1024;
// A ceiling, not a target: the measurement above is still the cost a venue *should*
// pay, and the ceiling leaves room for a photograph a venue has not optimised.
export const IMAGE_MAX_BYTES = 5242880;
/** A logo is flat colour and lands *inside* the document, so it is spent from code. */
// Ten times the code budget it is spent from — a deliberate tradeoff, recorded in
// openapi.yaml.
export const LOGO_MAX_BYTES = 512000;

/** Decoded byte length of a `data:` URI, or `null` if it is not one. */
export function dataUriBytes(uri: string): number | null {
  const comma = uri.indexOf(",");
  if (!uri.startsWith("data:") || comma < 0) return null;
  const meta = uri.slice(5, comma);
  const payload = uri.slice(comma + 1);
  if (!meta.includes(";base64")) return payload.length;
  // 4 base64 chars per 3 bytes, minus padding. Exact, and no Buffer/atob needed —
  // this runs in the portal build, in the mock and in the browser.
  const clean = payload.replace(/[^A-Za-z0-9+/=]/g, "");
  const pad = clean.endsWith("==") ? 2 : clean.endsWith("=") ? 1 : 0;
  return Math.max(0, (clean.length * 3) / 4 - pad) | 0;
}

export type AuthSource = S["AuthSource"];
export type AuthOutcome = S["AuthOutcome"];
export type AuthEventKind = S["AuthEventKind"];
export type AuthAttempt = S["AuthAttempt"];
export type MacCoverage = S["MacCoverage"];
export type AuthBlindSpot = S["AuthBlindSpot"];
export type AuthBlindSpotKind = AuthBlindSpot["kind"];

export type GardenEntryKind = S["GardenEntryKind"];
export type GardenEntryOrigin = S["GardenEntryOrigin"];
export type GardenEntry = S["GardenEntry"];
export type GardenEntryCreate = S["GardenEntryCreate"];
export type GardenDriverCoverage = S["GardenDriverCoverage"];
export type WalledGarden = S["WalledGarden"];

/**
 * The garden kinds, weakest first — and the one place that ordering is written.
 *
 * `WalledGarden.effectiveKind` is the **weakest** kind any device in the site can
 * enforce, because a site is only as enclosed as its most permissive device. That
 * rule needs a total order, and an order defined twice is an order that disagrees
 * with itself; the mock derives the field from this and the console renders the
 * consequence from the same array.
 */
export const GARDEN_KINDS_WEAKEST_FIRST = ["none", "ip-range", "fqdn-sni"] as const;

/** The weakest garden in a set — `none` if the set is empty, since nothing enforces one. */
export function weakestGarden(kinds: readonly GardenKind[]): GardenKind {
  let worst: GardenKind = "fqdn-sni";
  for (const k of kinds) {
    if (GARDEN_KINDS_WEAKEST_FIRST.indexOf(k) < GARDEN_KINDS_WEAKEST_FIRST.indexOf(worst)) {
      worst = k;
    }
  }
  return kinds.length === 0 ? "none" : worst;
}

/**
 * Whether an entry of this kind does anything on a device with this garden.
 *
 * An `fqdn` entry needs `fqdn-sni`. An `ip-range` entry works on either mechanism,
 * because a device that can match by name can also match by address. `none` honours
 * nothing at all, which is the case the console must never render as a working
 * garden — it is why `GardenDriverCoverage` exists per device rather than per site.
 */
export function honoursGardenEntry(driver: GardenKind, entry: GardenEntryKind): boolean {
  if (driver === "none") return false;
  if (entry === "fqdn") return driver === "fqdn-sni";
  return true;
}

/**
 * The voucher alphabet, and the one place it is defined.
 *
 * `0 O 1 I L` are excluded because a voucher is read off a thermal receipt and
 * typed by hand into a phone. This constrains the generator, so the admin UI, the
 * portal and the eventual Go implementation all read it from here rather than each
 * choosing their own.
 */
export const VOUCHER_ALPHABET = "ABCDEFGHJKMNPQRSTUVWXYZ23456789";
export const VOUCHER_LENGTH = 12;
export const VOUCHER_GROUP = 4;

/** `ABCDEFGHJKMN` → `ABCD-EFGH-JKMN`. Display only; the wire form is ungrouped. */
export function groupVoucherCode(code: string): string {
  const clean = normaliseVoucherCode(code);
  const groups: string[] = [];
  for (let i = 0; i < clean.length; i += VOUCHER_GROUP) {
    groups.push(clean.slice(i, i + VOUCHER_GROUP));
  }
  return groups.join("-");
}

/**
 * Accepts whatever the guest actually typed or pasted — any casing, any spacing,
 * dashes or none — and returns the wire form.
 */
export function normaliseVoucherCode(input: string): string {
  return input.toUpperCase().replace(/[^A-Z0-9]/g, "");
}

/** Does not prove the voucher exists. Shape only, for client-side feedback. */
export function isVoucherCodeShaped(input: string): boolean {
  const clean = normaliseVoucherCode(input);
  if (clean.length !== VOUCHER_LENGTH) return false;
  for (const ch of clean) if (!VOUCHER_ALPHABET.includes(ch)) return false;
  return true;
}

export * from "./design.js";
