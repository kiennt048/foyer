/**
 * The ugly world.
 *
 * `docs/rules/frontend.md` requires these fixtures to be deliberately ugly: a design
 * checked against tidy fixtures has been validated against a world that does not
 * exist.
 *
 * So everything below is a failure mode someone has actually hit, not a variation
 * for visual interest. Each one is annotated with what it is there to break.
 */
import type {
  Account,
  Alarm,
  AuditEntry,
  Controller,
  AuthAttempt,
  AuthBlindSpot,
  Capabilities,
  GardenEntry,
  MacCoverage,
  Nas,
  Operator,
  Plan,
  PortalContext,
  PortalSettings,
  Session,
  Site,
  Voucher,
  VoucherBatch,
} from "@foyer/contract";
import {
  CODE_BUDGET_BYTES,
  IMAGE_MAX_BYTES,
  LOGO_MAX_BYTES,
  VOUCHER_ALPHABET,
  VOUCHER_LENGTH,
} from "@foyer/contract";
import { int, mac, macWithOui, mulberry32, pick, REAL_OUIS } from "./rand.js";

/**
 * Re-exported because the mock's clock needs randomness that is a function of
 * `(key, tick)` rather than of call order. See `rand.ts`.
 */
export { noise, noiseInt } from "./rand.js";

/**
 * `internal/nas`'s evidence tables, GENERATED — never hand-edited.
 *
 * The mock answers `getNasEvidence` from these, so its answer and the Go server's
 * come from one authority. `make generate-check` fails if the file has drifted.
 * See `tools/codegen/vendorevidence` for why it is generated rather than written.
 */
export {
  VENDOR_BY_DRIVER,
  VENDOR_EVIDENCE,
  type VendorEvidenceEntry,
  type VendorEvidenceFact,
  type VendorEvidenceStanding,
} from "./generated/vendor-evidence.js";

/**
 * Re-exported so the fixture guard can measure the built world rather than trust the
 * generator that made it. `docs/rules/security.md` §6 is about *detecting* the
 * locally-administered bit, and a generator that claims to set it and sets a
 * different one is caught only by a test reading the finished address.
 */
export { macIsLocallyAdministered, macIsMulticast } from "./rand.js";

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const iso = (t: number) => new Date(t).toISOString();

// ── plans ────────────────────────────────────────────────────────────────────

/**
 * `enforcedBy` is the field that keeps the UI honest. A plan whose limits nothing
 * enforces is not a cap, and the sessions screen must not draw it as one.
 */
export const PLANS: Record<string, Plan> = {
  /*
   * Every name here is Vietnamese, because a café in District 1 writes its price
   * list in Vietnamese. `nameLang` is what stops the English portal claiming they
   * are English: the guest portal renders venue text verbatim and never translates
   * it, so the only honest thing it can do is mark it — the same rule the terms
   * block follows, applied to the other piece of venue-authored text on the page.
   * Without the tag a screen reader pronounces `Nhân viên` in English.
   */
  cafe1h: {
    name: "Café — 1 giờ",
    nameLang: "vi",
    durationMinutes: 60,
    dataAllowanceMb: 500,
    downKbps: 20_000,
    upKbps: 5_000,
    enforcedBy: "nas",
  },
  cafe24h: {
    name: "Café — 24 giờ",
    nameLang: "vi",
    durationMinutes: 1440,
    dataAllowanceMb: 2_000,
    downKbps: 20_000,
    upKbps: 5_000,
    enforcedBy: "nas",
  },
  event: {
    name: "Sự kiện — trọn ngày",
    nameLang: "vi",
    durationMinutes: 720,
    dataAllowanceMb: null,
    downKbps: 10_000,
    upKbps: 10_000,
    enforcedBy: "nas",
  },
  /**
   * Click-through: terms only, no credential.
   *
   * A duration and nothing else, because there is no identity to hang a quota on —
   * the guest gave nothing, so a byte cap here would be enforced against a MAC that
   * iOS and Android both rotate. Time is the one limit that means anything in this
   * mode, which is why `clickThroughMinutes` is the single setting a venue must give
   * before the method can be offered at all.
   */
  clickThrough: {
    name: "Khách vãng lai",
    nameLang: "vi",
    durationMinutes: 60,
    dataAllowanceMb: null,
    downKbps: null,
    upKbps: null,
    enforcedBy: "nas",
  },
  /** Sampled: the NAS cannot enforce, so `radacct` is the backstop and overshoots. */
  hotelRoom: {
    name: "Phòng khách sạn",
    nameLang: "vi",
    durationMinutes: 1440,
    dataAllowanceMb: 5_000,
    downKbps: null,
    upKbps: null,
    enforcedBy: "foyer-sampled",
  },
  /**
   * The dangerous one. A plan that *states* a limit nothing is enforcing, because
   * the NAS has no hard quota and sends no accounting. Any UI that renders this as
   * a cap is lying, and this fixture exists to catch that.
   */
  staffUnmetered: {
    name: "Nhân viên",
    nameLang: "vi",
    durationMinutes: null,
    dataAllowanceMb: 10_000,
    downKbps: null,
    upKbps: null,
    enforcedBy: "unenforced",
  },
  /**
   * The other dangerous one, one field along, and nothing catches it yet.
   *
   * `staffUnmetered` above is aimed at a UI that draws an unenforced *quota* as a
   * cap, and `enforcedBy` is the field that lets the UI know. **There is no such
   * field for a rate limit.** `enforcedBy` describes the time and data limits; it
   * says nothing about `downKbps`/`upKbps`, and no other property of `Plan` does
   * either. Whether "5 Mbps down" is a fact or a wish is answerable only from
   * `Capabilities.bandwidthLimits` on the access point the session is on — a join
   * the session payload does not carry.
   *
   * So this plan states a rate, and it is attached to `sess-unenforceable-rate` on
   * `nas-omada-1`, whose driver reports `bandwidthLimits: false`. It sits beside
   * `sess-no-accounting` on the *same* device carrying `staffUnmetered`: two
   * sessions, one unenforcing NAS, differing only in whether the plan names a rate.
   * Anything the console renders differently between those two rows is attributable
   * to that one difference and to nothing else.
   *
   * Every capability-gated control in the admin *disappears* when the driver cannot
   * honour it (`docs/rules/frontend.md` — "hide the rate-limit fields entirely").
   * A rate limit rendered from the plan alone is that rule with the gate left off.
   */
  warehouseShift: {
    name: "Kho — ca 8 tiếng",
    nameLang: "vi",
    durationMinutes: 480,
    dataAllowanceMb: null,
    downKbps: 5_000,
    upKbps: 2_000,
    enforcedBy: "unenforced",
  },
};

// ── capabilities ─────────────────────────────────────────────────────────────

const CAP_UNIFI: Capabilities = {
  liveReauthorize: true, // MEASURED against a real controller's Integration API
  walledGarden: "fqdn-sni",
  hardQuota: true,
  bandwidthLimits: true,
  dynamicVlan: false,
  rotateSecret: true,
  canDeauthGuest: true, // the controller's unauthorize action; DOCUMENTED, not measured
};

const CAP_MIKROTIK: Capabilities = {
  liveReauthorize: true,
  walledGarden: "ip-range", // materially weaker; the UI must say so
  hardQuota: false,
  bandwidthLimits: true,
  dynamicVlan: true,
  rotateSecret: false,
  canDeauthGuest: true, // RFC 5176 Disconnect-Request
};

/**
 * The low-capability driver the rules require in every fixture set. Everything the
 * admin UI could offer, it cannot do. If any control renders for this NAS that does
 * not render as *disabled with a reason*, the capability gating is broken.
 */
const CAP_MINIMAL: Capabilities = {
  liveReauthorize: false,
  walledGarden: "none",
  hardQuota: false,
  bandwidthLimits: false,
  dynamicVlan: false,
  rotateSecret: false,
  canDeauthGuest: false,
};

const CAP_COA: Capabilities = {
  liveReauthorize: true,
  walledGarden: "fqdn-sni",
  hardQuota: false,
  bandwidthLimits: true,
  dynamicVlan: true,
  rotateSecret: false,
  canDeauthGuest: true, // RFC 5176 Disconnect-Request
};

// ── the world ────────────────────────────────────────────────────────────────

export interface World {
  now: number;
  sites: Site[];
  nas: Nas[];
  batches: VoucherBatch[];
  vouchers: Voucher[];
  sessions: Session[];
  accounts: Account[];
  /** Deployment-wide. Not per site, and not in `accounts` — see `listOperators`. */
  operators: Operator[];
  /** Append-only, newest first. Nothing in this repository may write it twice. */
  audit: AuditEntry[];
  authAttempts: AuthAttempt[];
  controllers: Controller[];
  gardenEntries: GardenEntry[];
  portalSettings: PortalSettings[];
  /** Set by the mock when a portal client "associates". Gates VerifyPresence. */
  presentMacs: Set<string>;
}

export function buildWorld(now = Date.now()): World {
  const rng = mulberry32(0xf0e12a);
  const sites = buildSites();
  const nas = buildNas(now);
  const controllers = buildControllers(now);
  const { batches, vouchers } = buildVouchers(now, rng);
  const accounts = buildAccounts(now);
  const operators = buildOperators(now);
  const sessions = buildSessions(now, rng, nas, vouchers);
  return {
    now,
    sites,
    nas,
    batches,
    vouchers,
    sessions,
    accounts,
    operators,
    audit: buildAudit(now),
    authAttempts: buildAuthAttempts(now, rng, sessions, vouchers),
    controllers,
    gardenEntries: buildGardenEntries(now),
    portalSettings: buildPortalSettings(sites),
    presentMacs: new Set(sessions.filter((s) => s.state === "online").map((s) => s.callingStationId)),
  };
}

// ── sites ────────────────────────────────────────────────────────────────────

/**
 * Three sites, and every one of them carries rows.
 *
 * This is the ugly-fixture rule applied to a dimension that is easy to forget. **If
 * every session, account, device and voucher batch lived in `site-hcm`, a dropped
 * site scope would change no answer** — `listSessions` would return the same rows
 * whether or not the query filtered by site, and so would every other scoped list.
 * No schema-validating mock, end-to-end test or differential comparison can see that
 * in a world with one populated site.
 *
 * Site scoping is a **security property** — a `listAuthAttempts` that answers with
 * every site's usernames and MACs is exactly this class of defect — so the three
 * sites divide the work between them rather than one carrying all of it:
 *
 * - **`site-hcm`** keeps every pathological row it has ever had. Nothing below moves
 *   it, because a fixture that heals itself is worse than a static one.
 * - **`site-hanoi`** is the **clean** site, and its cleanliness is the assertion. It
 *   has devices, guests, accounts and codes of its own and **no alarm of any kind**,
 *   so a scope dropped anywhere makes the hotel look like the café's worst day —
 *   an unreachable access point, an expired controller credential and a forty-day
 *   orphan, all in a building where none of them exist.
 * - **`site-lab`** stays **empty of devices**, which is not laziness: it is the only
 *   site that produces `no-radius-configured`, the one `AuthBlindSpot` whose whole
 *   meaning is *this site has no RADIUS at all*. Give it a NAS and that enum value
 *   becomes unreachable again. See `authBlindSpotsFor`.
 */
function buildSites(): Site[] {
  return [
    // Long enough to force truncation in the header switcher.
    { id: "site-hcm", name: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1", environment: "live" },
    { id: "site-hanoi", name: "Khách sạn Hồ Gươm", environment: "live" },
    { id: "site-lab", name: "Lab", environment: "lab" },
  ];
}

/** The site every pathological row belongs to. Named so the rest reads as a choice. */
const HCM = "site-hcm";
/** The clean site. Populated, and deliberately raising nothing. */
const HANOI = "site-hanoi";

// ── access points ────────────────────────────────────────────────────────────

/**
 * An access point this deployment has never registered — the refusal side of
 * `VerifyPresence`, as an address rather than as a row.
 *
 * Without it, a mock that read **none** of the NAS redirect parameters would answer
 * an access point nobody had ever registered with `200`, a venue's name, its
 * languages and its terms — and nothing could notice, because the world would have
 * *no way to represent `VerifyPresence` failing at all*. A world that cannot express
 * a property makes every check of that property vacuous (`docs/rules/testing.md`).
 *
 * The refusal needs an address, and an address needs to be one that cannot
 * accidentally become real. This is IANA's documentation range (RFC 7042 §2.1.2,
 * `00-00-5E-00-53-00` … `-FF`) — the MAC equivalent of RFC 5737's addresses, which
 * `UNREACHABLE_PREFIX` already uses for the same reason. No vendor ships it, so no
 * fixture and no real radio can collide with it.
 *
 * **It is an input the tests cannot be wrong about, so its absence is asserted
 * rather than assumed.** `web/mock`'s portal tests derive it: no row in `buildNas`
 * may carry this address, and the day one does, the test that uses it as *the AP
 * nothing registered* goes red instead of quietly passing against a resolvable
 * device. That is the difference between a fixture constant and a fixture fact.
 */
export const UNREGISTERED_AP_MAC = "00:00:5e:00:53:ff";

function buildNas(now: number): Nas[] {
  /*
   * The three UniFi access points are **one controller's**, share **one** shared
   * secret, and each speaks RADIUS from its own address. That is not a tidy-up: it
   * is how the hardware works. Three devices, three secrets and no controller would
   * be a model error — a controller, a RADIUS client and an access point are three
   * different nouns.
   */
  const base = {
    siteId: HCM,
    enabled: true,
    secretSet: true,
    source: "discovered",
    radiusVia: "self",
  } as const;

  const unifi = { ...base, controllerId: "ctl-unifi", sharedSecretId: "sec-unifi" } as const;
  /* No controller. The router is its own, and every field is the operator's. */
  const standalone = { ...base, source: "manual", controllerId: null } as const;
  /* The hotel's own controller and its own secret. A second site is not a second
     copy of the first: nothing here is shared with `site-hcm`, which is what makes
     "these rows must not appear under that site" a statement with content. */
  const hanoi = {
    ...base,
    siteId: HANOI,
    controllerId: "ctl-hanoi",
    sharedSecretId: "sec-hanoi",
  } as const;

  return [
    {
      ...unifi,
      id: "nas-unifi-1",
      mac: "94:2a:6f:d0:30:57",
      name: "UniFi U6 Pro — Tầng trệt",
      description: "Khu vực quầy và bàn ngoài trời",
      driver: "unifi",
      model: "U6-Pro",
      firmware: "7.0.25",
      ipAddress: "10.20.0.11",
      nasIdentifier: "unifi-tang-tret",
      coaPort: 3799,
      capabilities: CAP_UNIFI,
      reachability: "ok",
      messageAuthenticator: "enforced",
      accountingConfigured: true,
      secretLastUsedAt: iso(now - 3 * MIN),
      lastSeenAt: iso(now - 40_000),
      createdAt: iso(now - 120 * DAY),
    },
    {
      ...unifi,
      id: "nas-unifi-2",
      mac: "94:2a:6f:d0:31:a2",
      name: "UniFi U6 Lite — Tầng 2",
      driver: "unifi",
      model: "U6-Lite",
      firmware: "6.6.55", // deliberately behind nas-unifi-1
      ipAddress: "10.20.0.12",
      nasIdentifier: "unifi-tang-2",
      coaPort: 3799,
      capabilities: CAP_UNIFI,
      reachability: "ok",
      messageAuthenticator: "enforced",
      accountingConfigured: true,
      secretLastUsedAt: iso(now - 11 * MIN),
      lastSeenAt: iso(now - 90_000),
      createdAt: iso(now - 118 * DAY),
    },
    {
      /**
       * Unreachable. Every action that needs the controller must be disabled with a
       * reason — not hidden, because the capability is real, only currently
       * unavailable. That distinction is the one the admin UI has to get right.
       */
      ...unifi,
      id: "nas-unifi-3",
      mac: "94:2a:6f:d0:32:1b",
      name: "UniFi U6 Mesh — Sân sau",
      driver: "unifi",
      model: "U6-Mesh",
      firmware: "7.0.25",
      ipAddress: "10.20.0.13",
      nasIdentifier: "unifi-san-sau",
      coaPort: 3799,
      capabilities: CAP_UNIFI,
      reachability: "unreachable",
      messageAuthenticator: "enforced",
      accountingConfigured: true,
      secretLastUsedAt: iso(now - 9 * DAY),
      lastSeenAt: iso(now - 9 * DAY),
      createdAt: iso(now - 60 * DAY),
    },
    {
      /**
       * Expired controller API key. Distinct from unreachable, and fixed differently.
       *
       * **This row is the world's *unmeasured* device.** `accountingConfigured` is
       * absent, not `false`: `false` would be a claim that somebody looked at this
       * controller and found no accounting profile attached. Nobody has. The API key
       * expired 34 days ago, and the row's own `reachability: "credentials-expired"`
       * is the reason nothing can be measured here at all — a probe against these
       * credentials fails before it reaches the accounting configuration, so
       * *absence* is what the product's own Probe button would leave behind, and
       * what `createNas` leaves on every freshly registered device.
       *
       * It is on this row, of all nine, because the two values are not the same
       * sentence: **measured silent** says no session record will ever arrive from
       * here, and *never measured* says nobody has asked. The console renders them
       * on two different pills, the dashboard alarms on only the first
       * (`nas-no-accounting` fires on `=== false && secretSet`), and the difference
       * is an operator being sent to the controller instead of to the Probe button.
       *
       * If every device set the property explicitly, **no screen test, no E2E route
       * and no differential scenario would ever run in a world where "not measured"
       * occurs**. A world that cannot express a property makes every check of that
       * property vacuous (`docs/rules/testing.md`), and this is exactly the place to
       * look for one. Every check of the third value would pass against a world
       * that could not produce it.
       *
       * **The trap this row has to avoid, and does:** it is populated in every other
       * field — MAC, model, firmware, address, site, secret. An unmeasured device
       * that is also empty would differ for the wrong reason, and every alarm
       * derived from it would be a finding about the emptiness rather than about
       * accounting that was never measured.
       */
      ...unifi,
      id: "nas-omada-1",
      controllerId: "ctl-omada",
      sharedSecretId: "sec-omada",
      mac: "b0:a7:b9:44:12:0e",
      name: "Omada EAP660 — Kho",
      driver: "omada",
      model: "EAP660 HD",
      firmware: "1.2.3",
      ipAddress: "10.20.0.21",
      nasIdentifier: "omada-kho",
      coaPort: null,
      capabilities: CAP_MINIMAL,
      reachability: "credentials-expired",
      messageAuthenticator: "unknown",
      secretLastUsedAt: iso(now - 34 * DAY),
      lastSeenAt: iso(now - 34 * DAY),
      createdAt: iso(now - 200 * DAY),
    },
    {
      /**
       * Refuses `require_message_authenticator`. Exposed to BlastRADIUS
       * (CVE-2024-3596). This must be a loud, permanent, per-NAS warning — the whole
       * point of generating the config is that we do not silently downgrade.
       */
      ...standalone,
      id: "nas-mt-1",
      sharedSecretId: "sec-mt",
      mac: null,
      name: "MikroTik hAP ax² — Chi nhánh Q3",
      driver: "mikrotik",
      model: "hAP ax²",
      firmware: "7.14.2",
      ipAddress: "10.30.0.1",
      nasIdentifier: "mt-q3",
      coaPort: 3799,
      capabilities: CAP_MIKROTIK,
      reachability: "ok",
      messageAuthenticator: "refused",
      accountingConfigured: true,
      secretLastUsedAt: iso(now - 2 * MIN),
      lastSeenAt: iso(now - 25_000),
      createdAt: iso(now - 45 * DAY),
    },
    {
      /** Cisco on the non-default CoA port. 1700, not 3799. The field is per-NAS. */
      ...standalone,
      id: "nas-coa-1",
      sharedSecretId: "sec-coa",
      mac: null,
      name: "Cisco C9120AXI — Văn phòng",
      driver: "coa",
      /* `coa` is one driver kind for Cisco and Aruba, and this device is a Cisco —
         which is also why its CoA port is 1700. They identify a session differently,
         so a device that named neither could not be driven at all: nothing would
         know whether to send an `audit-session-id` AV-pair or a MAC in Aruba's
         spelling, and the wrong one is answered by a careless NAS with an ACK and no
         effect. The fixture names it, because a real one has to. */
      coaVendor: "cisco",
      model: "C9120AXI-S",
      firmware: "17.9.4",
      ipAddress: "10.40.0.5",
      nasIdentifier: "c9120-vp",
      coaPort: 1700,
      capabilities: CAP_COA,
      reachability: "ok",
      messageAuthenticator: "enforced",
      accountingConfigured: true,
      secretLastUsedAt: iso(now - 47 * MIN),
      lastSeenAt: iso(now - 5 * MIN),
      createdAt: iso(now - 15 * DAY),
    },
    {
      /**
       * Registered but never seen. No secret yet, no accounting, no capabilities
       * measured. Every "last seen" and every counter for it is null, which is the
       * case a table full of `-` has to look deliberate rather than broken.
       */
      ...unifi,
      id: "nas-new-1",
      controllerId: "ctl-ruckus",
      sharedSecretId: null,
      mac: null,
      name: "Ruckus R650 — chưa lắp",
      driver: "ruckus",
      model: null,
      firmware: null,
      ipAddress: null,
      nasIdentifier: null,
      coaPort: null,
      secretSet: false,
      capabilities: CAP_MINIMAL,
      reachability: "unknown",
      messageAuthenticator: "unknown",
      accountingConfigured: false,
      secretLastUsedAt: null,
      lastSeenAt: null,
      createdAt: iso(now - 2 * HOUR),
    },

    /*
     * ── the hotel, and it is healthy on purpose ─────────────────────────────
     *
     * Two access points in `site-hanoi`, one controller, one shared secret, and
     * **not one condition that raises an alarm**: reachable, accounting attached,
     * `require_message_authenticator` enforced, an FQDN garden, a credential that
     * has not expired.
     *
     * That is the assertion, not an oversight. `deriveAlarms(world, "site-hanoi")`
     * is empty, so the moment any surface drops its site scope the hotel inherits
     * the café's unreachable mesh AP, its expired Omada credential, its BlastRADIUS
     * refusal and its end-of-life controller — four urgent-looking claims about a
     * building where none of them is true. Without a second site to be wrong
     * about, no screen and no test could tell a scoped answer from an unscoped one.
     *
     * They are `radiusVia: "self"` like the café's: each AP speaks RADIUS from its
     * own address, which is what makes twelve APs twelve `clients.conf` rows and one
     * controller record.
     */
    {
      ...hanoi,
      id: "nas-hanoi-1",
      mac: "70:a7:41:c2:08:19",
      name: "UniFi U6 Pro — Sảnh",
      description: "Quầy lễ tân và khu chờ",
      driver: "unifi",
      model: "U6-Pro",
      firmware: "7.0.25",
      ipAddress: "10.50.0.11",
      nasIdentifier: "hg-sanh",
      coaPort: 3799,
      capabilities: CAP_UNIFI,
      reachability: "ok",
      messageAuthenticator: "enforced",
      accountingConfigured: true,
      secretLastUsedAt: iso(now - 6 * MIN),
      lastSeenAt: iso(now - 50_000),
      createdAt: iso(now - 80 * DAY),
    },
    {
      ...hanoi,
      id: "nas-hanoi-2",
      mac: "70:a7:41:c2:09:44",
      name: "UniFi U6 In-Wall — Tầng 4",
      description: "Hành lang phòng 401–412",
      driver: "unifi",
      model: "U6-IW",
      firmware: "7.0.25",
      ipAddress: "10.50.0.12",
      nasIdentifier: "hg-tang-4",
      coaPort: 3799,
      capabilities: CAP_UNIFI,
      reachability: "ok",
      messageAuthenticator: "enforced",
      accountingConfigured: true,
      secretLastUsedAt: iso(now - 14 * MIN),
      lastSeenAt: iso(now - 70_000),
      createdAt: iso(now - 80 * DAY),
    },
  ];
}

// ── vouchers ─────────────────────────────────────────────────────────────────

function code(rng: () => number): string {
  let out = "";
  for (let i = 0; i < VOUCHER_LENGTH; i++) {
    out += VOUCHER_ALPHABET[Math.floor(rng() * VOUCHER_ALPHABET.length)]!;
  }
  return out;
}

function buildVouchers(now: number, rng: () => number) {
  const batches: VoucherBatch[] = [];
  const vouchers: Voucher[] = [];

  /* `siteId` is optional and defaults to `site-hcm`; only the hotel's batch names
     its site. */
  const specs: {
    id: string;
    siteId?: string;
    label: string;
    count: number;
    plan: Plan;
    expiryMode: VoucherBatch["expiryMode"];
    age: number;
  }[] = [
    {
      id: "batch-daily",
      label: "In hằng ngày — quầy",
      count: 40,
      plan: PLANS.cafe1h!,
      expiryMode: "from-first-use" as const,
      age: 2 * HOUR,
    },
    {
      id: "batch-event",
      label: "Hội thảo Fintech 2026",
      // 500 — pagination and the print sheet must survive it.
      count: 500,
      plan: PLANS.event!,
      expiryMode: "from-creation" as const,
      age: 6 * DAY,
    },
    {
      id: "batch-hotel",
      label: "Khách sạn — tháng 7",
      count: 60,
      plan: PLANS.hotelRoom!,
      expiryMode: "from-first-use" as const,
      age: 40 * DAY,
    },
    {
      /** Entirely spent. The batch list needs a genuinely finished row. */
      id: "batch-old",
      label: "Tết 2026",
      count: 25,
      plan: PLANS.cafe24h!,
      expiryMode: "from-creation" as const,
      age: 190 * DAY,
    },
    {
      /**
       * **The hotel's own codes, and the reason they are last in this array.**
       *
       * Every voucher state is drawn off the one shared `rng` stream in spec order,
       * so a batch inserted anywhere but the end re-rolls every batch after it and
       * moves fixtures that have nothing to do with it. Appended, the four batches
       * above keep exactly the world every existing screenshot and test was taken
       * against.
       *
       * What it is here to break: `listVouchers` and `listVoucherBatches` are both
       * scoped by `siteId` in the contract, and without this batch there would be
       * nothing in either list that a dropped scope could add. A code handed over at
       * the café's counter and searched there must not resolve to a room key at the
       * hotel — the two are separate businesses to the operator, and the redeem and
       * revoke controls sit one column away from the search result.
       */
      id: "batch-hanoi-rooms",
      siteId: HANOI,
      label: "Phòng 401–460 — tháng 8",
      count: 60,
      plan: PLANS.hotelRoom!,
      expiryMode: "from-first-use" as const,
      age: 9 * DAY,
    },
  ];

  for (const spec of specs) {
    const createdAt = now - spec.age;
    const expiresAt =
      spec.expiryMode === "from-creation" ? createdAt + 30 * DAY : null;
    const counts = { unused: 0, inUse: 0, exhausted: 0, expired: 0, revoked: 0 };

    for (let i = 0; i < spec.count; i++) {
      const roll = rng();
      let state: Voucher["state"];
      if (spec.id === "batch-old") state = roll < 0.85 ? "exhausted" : "expired";
      else if (expiresAt && expiresAt < now) state = "expired";
      else if (roll < 0.6) state = "unused";
      else if (roll < 0.72) state = "in-use";
      else if (roll < 0.95) state = "exhausted";
      else state = "revoked";

      const usedOffset = int(rng, 1, Math.max(2, Math.floor(spec.age / MIN)));
      const firstUsedAt = state === "unused" ? null : createdAt + usedOffset * MIN;

      vouchers.push({
        id: `v-${spec.id}-${i}`,
        batchId: spec.id,
        code: code(rng),
        state,
        createdAt: iso(createdAt),
        firstUsedAt: firstUsedAt ? iso(firstUsedAt) : null,
        expiresAt: expiresAt
          ? iso(expiresAt)
          : firstUsedAt
            ? iso(firstUsedAt + (spec.plan.durationMinutes ?? 60) * MIN)
            : null,
        revokedAt: state === "revoked" ? iso(now - int(rng, 5, 400) * MIN) : null,
        currentSessionId: null,
      });

      const key =
        state === "in-use" ? "inUse" : (state as "unused" | "exhausted" | "expired" | "revoked");
      counts[key] += 1;
    }

    batches.push({
      id: spec.id,
      siteId: spec.siteId ?? HCM,
      label: spec.label,
      count: spec.count,
      createdAt: iso(createdAt),
      createdBy: pick(rng, ["Linh", "Minh", "quản trị"]),
      plan: spec.plan,
      expiryMode: spec.expiryMode,
      expiresAt: expiresAt ? iso(expiresAt) : null,
      counts,
    });
  }

  return { batches, vouchers };
}

// ── accounts ─────────────────────────────────────────────────────────────────

function buildAccounts(now: number): Account[] {
  return [
    {
      id: "acc-1",
      siteId: "site-hcm",
      username: "nguyen.thi.mai",
      displayName: "Nguyễn Thị Mai",
      enabled: true,
      createdAt: iso(now - 300 * DAY),
      lastLoginAt: iso(now - 3 * HOUR),
      credentialKind: "argon2id",
      plan: PLANS.staffUnmetered!,
    },
    {
      id: "acc-2",
      siteId: "site-hcm",
      username: "tran.van.hung",
      displayName: "Trần Văn Hùng",
      enabled: true,
      createdAt: iso(now - 210 * DAY),
      lastLoginAt: iso(now - 26 * DAY),
      credentialKind: "argon2id",
      plan: PLANS.staffUnmetered!,
    },
    {
      /**
       * The one account opted in to PEAP-MSCHAPv2, which means an NT hash exists for
       * it in a separate envelope-encrypted table. An NT hash is password-equivalent.
       * The UI must make this visibly different from every other row — it is the most
       * consequential per-identity decision an operator can make.
       */
      id: "acc-3",
      siteId: "site-hcm",
      username: "may.in.kho",
      displayName: "Máy in kho (802.1X)",
      enabled: true,
      createdAt: iso(now - 90 * DAY),
      lastLoginAt: iso(now - 55 * MIN),
      credentialKind: "argon2id+nt",
      plan: PLANS.staffUnmetered!,
    },
    {
      /**
       * Opted in to 802.1X and to nothing else — `argon2id+bcrypt`.
       *
       * It exists because a credential renderer written as an equality test against
       * `argon2id`, with an `else` that says *"argon2id + NT hash"*, is
       * indistinguishable from a correct one in a world holding only `argon2id` and
       * `argon2id+nt`: **every row still gets the right label**, and the screen goes
       * on lying only about identities the fixtures cannot express. A fixture input
       * that cannot be wrong checks nothing, and this row is what makes the
       * assertion mean something.
       *
       * The display name deliberately does NOT say 802.1X. `acc-3` says it and is
       * MSCHAPv2; a fixture whose label agrees with its credential kind would let a
       * test pass by reading the name.
       */
      id: "acc-5",
      siteId: "site-hcm",
      username: "may.quet.ma.vach",
      displayName: "Máy quét mã vạch",
      enabled: true,
      createdAt: iso(now - 45 * DAY),
      lastLoginAt: iso(now - 12 * MIN),
      credentialKind: "argon2id+bcrypt",
      plan: PLANS.staffUnmetered!,
    },
    {
      /**
       * Both opt-ins on one identity — `argon2id+nt+bcrypt`.
       *
       * Representable in the schema, not reachable through the API today, and here
       * because a renderer that picks ONE pill looks correct until a row has two
       * facts to state. An NT notice above the table whose filter is an equality
       * test counts this row wrongly.
       */
      id: "acc-6",
      siteId: "site-hcm",
      username: "cam.hanh.lang",
      displayName: "Camera hành lang",
      enabled: true,
      createdAt: iso(now - 120 * DAY),
      lastLoginAt: iso(now - 4 * HOUR),
      credentialKind: "argon2id+nt+bcrypt",
      plan: PLANS.staffUnmetered!,
    },
    {
      id: "acc-4",
      siteId: "site-hcm",
      username: "le.hoang.long",
      displayName: null, // no display name — the cell needs its own muted treatment
      enabled: false,
      createdAt: iso(now - 400 * DAY),
      lastLoginAt: null, // never logged in
      credentialKind: "argon2id",
      plan: PLANS.staffUnmetered!,
    },

    /*
     * ── the hotel's staff, and one of them shares a surname ──────────────────
     *
     * `nguyen.van.tuan` is here because `site-hcm` already has `nguyen.thi.mai`, and
     * *Nguyễn* is the surname of roughly two Vietnamese people in five. So
     * `listAccounts?siteId=site-hcm&q=nguyen` is a query an operator really types,
     * and the site scope is the only thing that stops it answering with a person who
     * works at a different business — whose account they can then disable.
     *
     * A cross-site fixture that added `smith` and `dupont` would have proved a list
     * got longer. This one is wrong in a way somebody would act on.
     */
    {
      id: "acc-hanoi-1",
      siteId: HANOI,
      username: "nguyen.van.tuan",
      displayName: "Nguyễn Văn Tuấn",
      enabled: true,
      createdAt: iso(now - 150 * DAY),
      lastLoginAt: iso(now - 5 * HOUR),
      credentialKind: "argon2id",
      plan: PLANS.staffUnmetered!,
    },
    {
      id: "acc-hanoi-2",
      siteId: HANOI,
      username: "le.thi.hoa",
      displayName: "Lê Thị Hoa",
      enabled: true,
      createdAt: iso(now - 60 * DAY),
      lastLoginAt: iso(now - 2 * DAY),
      credentialKind: "argon2id",
      plan: PLANS.staffUnmetered!,
    },
  ];
}

// ── operators ────────────────────────────────────────────────────────────────

/**
 * The people who sign in to the console. **No credential of any kind lives here**,
 * and that is a rule rather than an omission: `docs/rules/security.md` §7 says no
 * static credential anywhere in the repository, and a fixture password is a static
 * credential that ships. The first administrator's password is generated at first
 * boot by the Go binary, which refuses to start on a known default; the mock verifies
 * nothing at all and says so where it does it.
 *
 * Ugly on purpose, and each row is a case the console has to render:
 *
 *   - **Exactly one *enabled* administrator, and a second administrator who is
 *     disabled.** "There are two administrators" is true and useless. This is what
 *     makes `/problems/last-administrator` a rule about `enabled` rather than about
 *     `role`, and a console that counts rows rather than enabled rows will offer a
 *     delete control that the server refuses.
 *   - An operator with **no display name**, so the cell needs its own treatment.
 *   - An operator who has **never signed in** — `lastLoginAt: null` is not "a long
 *     time ago", and a screen that formats it as a date will render the epoch.
 *   - A **shared front-desk account**, because that is what a hotel actually does
 *     with a staff login and the audit log will attribute a night shift to it.
 */
export const ADMINISTRATOR_ID = "op-viet";

function buildOperators(now: number): Operator[] {
  return [
    {
      id: ADMINISTRATOR_ID,
      username: "dang.quoc.viet",
      displayName: "Đặng Quốc Việt",
      role: "administrator",
      enabled: true,
      createdAt: iso(now - 420 * DAY),
      lastLoginAt: iso(now - 41 * MIN),
    },
    {
      /* Left in March. Disabled rather than deleted, because the audit entries she
         wrote are the reason anybody can reconstruct the walled-garden change that
         broke the portal — see `buildAudit`. Deleting her would be legitimate and the
         log would survive it; keeping her disabled is what an operator usually does,
         and it is the row that makes "count the administrators" wrong. */
      id: "op-ha",
      username: "pham.thu.ha",
      displayName: "Phạm Thu Hà",
      role: "administrator",
      enabled: false,
      createdAt: iso(now - 400 * DAY),
      lastLoginAt: iso(now - 163 * DAY),
    },
    {
      id: "op-tuan",
      username: "le.minh.tuan",
      displayName: null, // no display name — the cell needs its own muted treatment
      role: "staff",
      enabled: true,
      createdAt: iso(now - 96 * DAY),
      lastLoginAt: iso(now - 12 * MIN),
    },
    {
      id: "op-letan",
      username: "quay.le.tan",
      displayName: "Quầy lễ tân — ca đêm (dùng chung)",
      role: "staff",
      enabled: true,
      createdAt: iso(now - 31 * DAY),
      lastLoginAt: null, // created and never used
    },
    {
      id: "op-an",
      username: "nguyen.van.an",
      displayName: "Nguyễn Văn An",
      role: "staff",
      enabled: false,
      createdAt: iso(now - 520 * DAY),
      lastLoginAt: iso(now - 380 * DAY),
    },
  ];
}

// ── the audit log ────────────────────────────────────────────────────────────

/**
 * History, newest first, and every `AuditKind` appears at least once — asserted in
 * `web/mock`, from the enum rather than from a number, so a kind added to the
 * contract leaves the audit screen with a row someone can actually look at.
 *
 * Three rows are here to break a rendering rather than to fill a table:
 *
 *   - **`op-gone` no longer exists.** No operator has that id, and the entry still
 *     names `tran.quoc.bao`. That is the whole argument for denormalising
 *     `actorUsername`: an implementation that joins to `operators` at read time
 *     renders "deleted operator" on the one row somebody is reading the log to
 *     understand. If the audit screen ever shows a blank actor, this is the row.
 *   - **A subject that no longer exists.** `nas-tang-2` was registered and deleted;
 *     the entries about it still have to say which device it was, from
 *     `subject.name`, because there is nothing left to look up.
 *   - **One entry timestamped in the future.** The clock skew already in the session
 *     fixtures, in the one table that is read strictly in time order. A screen that
 *     sorts by `at` puts a "3 minutes from now" row at the top, and the honest
 *     rendering is the skew rather than a hidden row.
 */
function buildAudit(now: number): AuditEntry[] {
  const viet = { actorId: ADMINISTRATOR_ID, actorUsername: "dang.quoc.viet" };
  const ha = { actorId: "op-ha", actorUsername: "pham.thu.ha" };
  const tuan = { actorId: "op-tuan", actorUsername: "le.minh.tuan" };
  /* No such operator. Deleted, and the log outlives them. */
  const gone = { actorId: "op-gone", actorUsername: "tran.quoc.bao" };
  const desk = { actorId: "op-letan", actorUsername: "quay.le.tan" };
  const hcm = "site-hcm";

  const rows: AuditEntry[] = [
    {
      /* Ahead of the clock. Not a mistake in this file — see the note above. */
      id: "aud-skew",
      at: iso(now + 3 * MIN),
      ...tuan,
      kind: "voucher-revoked",
      siteId: hcm,
      params: { code: "K7MTQ4WX9RJ2" },
      subject: { kind: "voucher", id: "vch-skew", name: "K7MTQ4WX9RJ2" },
    },
    {
      /* An administrator took last month's sessions home. The one read in the log,
         and its params are the table and the days — never a row. */
      id: "aud-export",
      at: iso(now - 9 * MIN),
      ...viet,
      kind: "data-exported",
      siteId: hcm,
      params: {
        dataset: "sessions",
        from: iso(now - 31 * DAY).replace(/\.\d{3}Z$/, "Z"),
        to: iso(now).replace(/\.\d{3}Z$/, "Z"),
      },
      subject: { kind: "site", id: hcm, name: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1" },
    },
    {
      id: "aud-1",
      at: iso(now - 26 * MIN),
      ...desk,
      kind: "voucher-batch-created",
      siteId: hcm,
      params: { label: "Quầy lễ tân — tháng 8", count: 200 },
      subject: { kind: "voucher-batch", id: "batch-desk-aug", name: "Quầy lễ tân — tháng 8" },
    },
    {
      id: "aud-2",
      at: iso(now - 4 * HOUR),
      ...viet,
      kind: "nas-secret-rotated",
      siteId: hcm,
      params: { nasName: "UniFi U6 Pro — Tầng trệt", changeClass: "restart" },
      subject: { kind: "nas", id: "nas-unifi-1", name: "UniFi U6 Pro — Tầng trệt" },
    },
    {
      id: "aud-3",
      at: iso(now - 9 * HOUR),
      ...tuan,
      kind: "operator-password-changed",
      siteId: null,
      params: { username: "le.minh.tuan" },
      subject: { kind: "operator", id: "op-tuan", name: "le.minh.tuan" },
    },
    {
      /*
       * A guest asked to be forgotten. **The subject is
       * masked**: an audit log that kept the MAC it records erasing would not have
       * erased it. The counts are per table, zeros included, because an erasure that
       * reported only what it found could not be told from one that never looked.
       */
      id: "aud-18",
      at: iso(now - 20 * HOUR),
      ...viet,
      kind: "data-subject-erased",
      siteId: null,
      params: {
        subjectKind: "mac",
        removed: {
          authorization_events: 1,
          portal_grants: 2,
          portal_sessions: 1,
          radacct: 7,
          radpostauth: 9,
        },
        total: 20,
      },
      subject: { kind: "data-subject", id: "era-fixture1", name: "••:••:••:••:4e:21" },
    },
    {
      id: "aud-19",
      at: iso(now - 30 * HOUR),
      ...viet,
      kind: "retention-policy-changed",
      siteId: null,
      params: { changed: { accounting: { from: 90, to: 180 } } },
      subject: { kind: "retention-policy", id: "retention-policy" },
    },
    {
      /* The café started asking for an e-mail address and for marketing consent.
         The params say which fields and whether consent is asked,
         never the consent text itself — that is kept under its version elsewhere. */
      id: "aud-20",
      at: iso(now - 31 * HOUR),
      ...viet,
      kind: "guest-data-settings-changed",
      siteId: hcm,
      params: { fields: ["name", "email (required)"], marketingConsent: true },
      subject: { kind: "site", id: hcm, name: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1" },
    },
    {
      /* A guest rang and asked to stop receiving offers. **Named by the consent's id
         only**: the withdrawal cleared the contact, and a log that kept the e-mail
         address would have kept what the guest asked to be let go of. */
      id: "aud-21",
      at: iso(now - 32 * HOUR),
      ...viet,
      kind: "marketing-consent-withdrawn",
      siteId: hcm,
      subject: { kind: "marketing-consent", id: "mc-fixture01" },
    },
    /* The network tag: the four kinds a deployment
       writes when an administrator changes how the portal and FreeRADIUS are
       reached. All four are the deployment's, never a venue's. */
    {
      /* A café behind one NAT address raised the portal's per-address bucket. */
      id: "aud-net-1",
      at: iso(now - 33 * HOUR),
      ...viet,
      kind: "network-settings-changed",
      siteId: null,
      params: { changed: { "throttles.portalAddress.burst": { from: 30, to: 300 } } },
      subject: { kind: "network-settings", id: "network-settings" },
    },
    {
      /* The fingerprint and the expiry — never the key, which no answer carries. */
      id: "aud-net-2",
      at: iso(now - 34 * HOUR),
      ...viet,
      kind: "portal-certificate-changed",
      siteId: null,
      params: {
        fingerprintSha256: "3f9a1c0e5b7d2a4f6c8e0b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a",
        notAfter: iso(now + 80 * DAY),
        portalUrl: "https://wifi.hoasua.example/en/",
      },
      subject: { kind: "network-settings", id: "network-settings" },
    },
    {
      id: "aud-net-3",
      at: iso(now - 35 * HOUR),
      ...viet,
      kind: "radius-clients-synced",
      siteId: null,
      params: { trigger: "nas-created", outcome: "synced", inserted: 1, updated: 0, removed: 0, skipped: 0 },
      subject: { kind: "radius-server", id: "radius-server" },
    },
    {
      id: "aud-net-4",
      at: iso(now - 36 * HOUR),
      ...viet,
      kind: "radius-configuration-applied",
      siteId: null,
      params: { outcome: "restart-unavailable", restarted: false },
      subject: { kind: "radius-server", id: "radius-server" },
    },
    {
      /* The venue's calendar was UTC because nobody had set one; every daily
         figure moved seven hours when it was put right. */
      id: "aud-22",
      at: iso(now - 33 * HOUR),
      ...viet,
      kind: "site-time-zone-changed",
      siteId: hcm,
      params: { from: "UTC", to: "Asia/Ho_Chi_Minh" },
      subject: { kind: "site", id: hcm, name: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1" },
    },
    {
      /* The deployment-wide SMS cap. The deployment's entry, so no venue. */
      id: "aud-23",
      at: iso(now - 34 * HOUR),
      ...viet,
      kind: "phone-spend-cap-changed",
      siteId: null,
      params: { from: null, to: 500 },
      subject: { kind: "phone-spend-cap", id: "phone-spend-cap" },
    },
    {
      /* The maintenance interval lengthened to fifteen minutes from the console.
         The deployment's, like a retention change. */
      id: "aud-maint-1",
      at: iso(now - 33 * HOUR),
      ...viet,
      kind: "maintenance-settings-changed",
      siteId: null,
      params: { changed: { intervalSeconds: { from: 300, to: 900 } } },
      subject: { kind: "maintenance-settings", id: "maintenance-settings" },
    },
    {
      /* A guest account opted in to 802.1X with a new password. Whether each
         changed, never a value. */
      id: "aud-dot1x-1",
      at: iso(now - 34 * HOUR),
      ...viet,
      kind: "account-credentials-changed",
      siteId: hcm,
      params: { passwordChanged: true, eapTtlsPap: true },
      subject: { kind: "account", id: "acc-5", name: "may.quet.ma.vach" },
    },
    {
      id: "aud-4",
      at: iso(now - 2 * DAY),
      ...viet,
      kind: "operator-created",
      siteId: null,
      params: { username: "quay.le.tan", role: "staff" },
      subject: { kind: "operator", id: "op-letan", name: "quay.le.tan" },
    },
    {
      id: "aud-5",
      at: iso(now - 3 * DAY - 2 * HOUR),
      ...viet,
      kind: "portal-terms-changed",
      siteId: hcm,
      params: {
        changed: {
          "terms.summary.vi": { from: "Sử dụng Wi-Fi công cộng miễn phí.", to: "Sử dụng Wi-Fi miễn phí, không dùng cho mục đích thương mại." },
          "terms.summary.en": { from: "Free public Wi-Fi.", to: "Free public Wi-Fi, not for commercial use." },
        },
      },
      subject: { kind: "site", id: hcm, name: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1" },
    },
    {
      id: "aud-6",
      at: iso(now - 3 * DAY - 3 * HOUR),
      ...viet,
      kind: "portal-settings-changed",
      siteId: hcm,
      params: {
        changed: {
          heroImage: { from: null, to: { contentType: "image/jpeg", bytes: 89697 } },
          venueName: { from: "Cà phê Hoa Sữa", to: "Cà phê Hoa Sữa — Nguyễn Huệ" },
        },
      },
      subject: { kind: "site", id: hcm, name: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1" },
    },
    {
      /* The change the log exists for. Removing this entry's actor from `operators`
         is what makes `actorUsername` load-bearing rather than convenient. */
      id: "aud-7",
      at: iso(now - 11 * DAY),
      ...gone,
      kind: "garden-entry-removed",
      siteId: hcm,
      params: { value: "ocsp.digicert.com", kind: "fqdn" },
      subject: { kind: "garden-entry", id: "gar-ocsp-old", name: "ocsp.digicert.com" },
    },
    {
      id: "aud-8",
      at: iso(now - 11 * DAY - 6 * MIN),
      ...gone,
      kind: "garden-entry-added",
      siteId: hcm,
      params: { value: "payments.example.vn", kind: "fqdn" },
      subject: { kind: "garden-entry", id: "gar-payments", name: "payments.example.vn" },
    },
    {
      id: "aud-9",
      at: iso(now - 34 * DAY),
      ...ha,
      kind: "operator-updated",
      siteId: null,
      params: { username: "nguyen.van.an", changed: ["enabled"], enabled: false },
      subject: { kind: "operator", id: "op-an", name: "nguyen.van.an" },
    },
    {
      id: "aud-10",
      at: iso(now - 58 * DAY),
      ...ha,
      kind: "operator-deleted",
      siteId: null,
      params: { username: "vo.thi.lan" },
      subject: { kind: "operator", id: "op-lan", name: "vo.thi.lan" },
    },
    {
      /* Registered, then deleted eleven days later. Neither entry can be rendered by
         looking the device up — there is nothing to look up. */
      id: "aud-11",
      at: iso(now - 74 * DAY),
      ...ha,
      kind: "nas-deleted",
      siteId: hcm,
      params: { nasName: "MikroTik hAP ax² — Tầng 2" },
      subject: { kind: "nas", id: "nas-tang-2", name: "MikroTik hAP ax² — Tầng 2" },
    },
    {
      id: "aud-12",
      at: iso(now - 85 * DAY),
      ...ha,
      kind: "nas-registered",
      siteId: hcm,
      params: { nasName: "MikroTik hAP ax² — Tầng 2", driver: "mikrotik" },
      subject: { kind: "nas", id: "nas-tang-2", name: "MikroTik hAP ax² — Tầng 2" },
    },
    {
      /*
       * **A disconnect the device did not confirm**, the row this kind is read for:
       * the voucher went, the guest did not. `disconnected: false` beside
       * `voucherRevoked: true` is the whole fact — somebody tried, and the guest was
       * still online afterwards.
       */
      id: "aud-disconnect",
      at: iso(now - 50 * MIN),
      ...desk,
      kind: "session-disconnected",
      siteId: hcm,
      params: {
        nasId: "nas-unifi-1",
        disconnected: false,
        grantsRevoked: 1,
        voucherRevoked: true,
        voucherId: "vch-skew",
      },
      subject: { kind: "session", id: "sess-disconnect-attempt", name: "9e:0d:41:55:aa:1c" },
    },
    {
      id: "aud-13",
      at: iso(now - 190 * DAY),
      ...ha,
      kind: "controller-deleted",
      siteId: hcm,
      params: { controllerName: "UniFi cũ (Cloud Key Gen1)", orphanedNas: 0 },
      subject: { kind: "controller", id: "ctl-unifi-old", name: "UniFi cũ (Cloud Key Gen1)" },
    },
    {
      id: "aud-14",
      at: iso(now - 300 * DAY),
      ...ha,
      kind: "controller-registered",
      siteId: hcm,
      params: { controllerName: "UniFi — Cloud Key Gen2+", vendor: "unifi" },
      subject: { kind: "controller", id: "ctl-unifi", name: "UniFi — Cloud Key Gen2+" },
    },
    {
      /*
       * **A replaced API key**, which is the change `controller-updated` exists in
       * `AuditKind` for: without it, the one operation on this surface that swaps a
       * stored credential would leave no trace.
       *
       * `changed` names the field and **never the value**. `apiKey` is write-only in
       * this contract, and an audit log read months later by somebody who was not
       * there is the last place a credential should reappear.
       */
      id: "aud-15",
      at: iso(now - 3 * DAY),
      ...ha,
      kind: "controller-updated",
      siteId: null,
      params: { controllerName: "Omada — OC200", changed: ["apiKey"] },
      subject: { kind: "controller", id: "ctl-omada", name: "Omada — OC200" },
    },
    {
      /*
       * **A discovery run that adopted nothing**, which is the row worth having.
       *
       * `adopted: 0, updated: 3, missing: 1` is the afternoon an operator has to read
       * carefully: the access point that stopped being reported is marked, not
       * deleted, so nothing on any other screen changed and this line is the only
       * record that the controller's answer got shorter. A fixture set carrying only
       * the successful run would leave the console's copy for `missing` unrendered.
       */
      id: "aud-16",
      at: iso(now - 9 * DAY),
      ...ha,
      kind: "controller-discovery-run",
      siteId: hcm,
      params: {
        controllerName: "UniFi — Cloud Key Gen2+",
        adopted: 0,
        updated: 3,
        missing: 1,
      },
      subject: { kind: "controller", id: "ctl-unifi", name: "UniFi — Cloud Key Gen2+" },
    },
    {
      /*
       * **The oldest row, and the only row that could be.**
       *
       * Nothing above it was possible until this one happened: `createNas` requires
       * a `siteId`, a voucher batch requires a `siteId`, the walled garden hangs off
       * a site. So in a real deployment the log begins here — without a `site-created`
       * member in `AuditKind`, the first act of every installation would go
       * unrecorded, and the audit screen's oldest entry would be whatever somebody
       * happened to do second.
       *
       * `params` is absent on purpose. The change plus its subject is the whole
       * fact, exactly as it is for `nas-deleted` and `operator-deleted`, and the
       * venue's name is in `subject.name` where every kind's is — a `params.name`
       * beside it would be a second copy of the one member the log is denormalised
       * for. `environment` is not here either: `SiteCreate` accepts none and this
       * operation always makes a `live` one, so recording it would be a fixture
       * asserting a fact the product cannot vary.
       */
      id: "aud-17",
      at: iso(now - 411 * DAY),
      ...viet,
      kind: "site-created",
      siteId: hcm,
      subject: { kind: "site", id: hcm, name: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1" },
    },
  ];
  return rows;
}

// ── sessions ─────────────────────────────────────────────────────────────────

/**
 * The named rows.
 *
 * The pathological sessions have stable ids, because a row identified only by its
 * position in the array is impossible to talk about ("look at row 4") and impossible
 * for the mock's clock to protect. The roles below say which row is which.
 *
 * `2 * intervalSeconds` is the stale threshold, so one "gap" of 300 s is still
 * `sampled` and three is unambiguously `stale`. Every schedule in the mock is
 * expressed in those units.
 */
export const SESSION_IDS = {
  /** 40 days "online" because the NAS rebooted without Acct-Stop. */
  orphaned: "sess-orphaned-no-stop",
  /** Interim updates stopped 20 minutes ago. Recovers on the mock's schedule. */
  staleMikrotik: "sess-stale-mikrotik",
  /** Controller-API path, no accounting profile: there is nothing and never will be. */
  noAccounting: "sess-no-accounting",
  /** NAS clock ahead of the server's, so the duration is negative. */
  clockSkew: "sess-clock-skew",
  /** Two live sessions, one MAC, one shared Acct-Session-Id. */
  duplicateA: "sess-duplicate-a",
  duplicateB: "sess-duplicate-b",
  endedClean: "sess-ended-clean",
  endedRevoked: "sess-ended-revoked",
  /** Live, but its accounting goes quiet on the mock's schedule. */
  interimGap: "sess-interim-gap",
  /** Its 32-bit octet counter wraps, so the total goes *down*. */
  counterWrap: "sess-counter-wrap",
  /** A plan stating a rate limit, on a driver that cannot apply one. */
  unenforceableRate: "sess-unenforceable-rate",
  /**
   * The guest on the access point nothing can reach, holding a voucher that is still
   * `in-use`. Revoking it frees the code and **cannot** end the session — the one
   * fixture from which `/vouchers` can show a failed revoke without driving the portal
   * first.
   */
  strandedUnreachable: "sess-stranded-unreachable",
  /**
   * **The same MAC, in the other site.** A guest at the hotel whose laptop carries
   * the address two live café sessions already carry. Nothing but the site scope
   * separates them, which is the whole case for site scoping in one row.
   */
  crossSiteMac: "sess-hanoi-shared-mac",
  /** A healthy hotel guest, on a voucher from the hotel's own batch. */
  hanoiVoucher: "sess-hanoi-voucher",
  /** Ended cleanly at the hotel, so a state filter there is not a filter over nothing. */
  hanoiEnded: "sess-hanoi-ended",
} as const;

/**
 * The rows a moving world may never move.
 *
 * A living mock that quietly heals itself is worse than a static one: it removes
 * the exact cases the design was built against. These seven keep their authored
 * shape forever — the orphan stays orphaned, the skewed row keeps its negative
 * duration, neither row on the device with no accounting profile ever acquires a
 * counter, the duplicate MAC stays duplicated, and the guest stranded on the
 * unreachable access point stays nine days behind rather than drifting into a
 * different number every time the mock is restarted. `web/mock/src/sim.test.ts`
 * asserts it after N ticks.
 */
export const FROZEN_SESSION_IDS: readonly string[] = [
  SESSION_IDS.orphaned,
  SESSION_IDS.noAccounting,
  SESSION_IDS.clockSkew,
  SESSION_IDS.duplicateA,
  SESSION_IDS.duplicateB,
  SESSION_IDS.unenforceableRate,
  SESSION_IDS.strandedUnreachable,
];

/**
 * The access point the mock takes down and brings back.
 *
 * Deliberately *not* `nas-unifi-3` (permanently unreachable) or `nas-omada-1`
 * (credentials expired) — those two are load-bearing fixtures and must stay
 * broken. This one is healthy at rest, which is what makes a transition visible.
 */
export const FLAPPING_NAS_ID = "nas-unifi-2";

/**
 * What a freshness *is*, right now — as opposed to what it was when it was written.
 *
 * `stale` is a function of the current clock, not of when the row was recorded, and
 * the world is now allowed to move underneath it. One definition, used by the
 * fixture builder, by the mock's read path and by the alarm derivation, so a row
 * and the alarm about that row can never disagree.
 */
export function freshnessStateAt(
  f: Session["freshness"],
  now: number,
): NonNullable<Session["freshness"]["state"]> {
  if (f.state === "absent" || f.sampledAt === null || f.sampledAt === undefined) return "absent";
  if (f.state === "live") return "live";
  if (f.intervalSeconds == null) return "sampled";
  const ageSeconds = (now - Date.parse(f.sampledAt)) / 1000;
  return ageSeconds > 2 * f.intervalSeconds ? "stale" : "sampled";
}

const fresh = (
  now: number,
  sampledAt: number | null,
  interval: number | null,
): Session["freshness"] => {
  if (sampledAt === null) return { state: "absent", sampledAt: null, intervalSeconds: interval };
  const age = (now - sampledAt) / 1000;
  const state: NonNullable<Session["freshness"]["state"]> =
    interval !== null && age > 2 * interval ? "stale" : "sampled";
  return { state, sampledAt: iso(sampledAt), intervalSeconds: interval };
};

/**
 * The five counter fields, written once so a session cannot declare a byte total
 * without also declaring whether it can be believed.
 *
 * `countersWrapped` is `false` on every authored row, and that is the honest value:
 * a wrap is something Foyer *observes* between two accounting records, so at t = 0
 * it has observed nothing. `sess-counter-wrap` is authored just under 2^32 and the
 * mock's simulation carries it over — which means the tripwire arms during a review
 * session rather than being baked in, and a component that ignores the flag is wrong
 * on screen rather than wrong in a fixture file.
 *
 * The totals equal the reported counters while nothing has wrapped. `sim.ts` is what
 * makes them diverge, and it is the only thing that may.
 */
function counters(
  input: number | null,
  output: number | null,
): Pick<
  Session,
  "inputOctets" | "outputOctets" | "countersWrapped" | "inputOctetsTotal" | "outputOctetsTotal"
> {
  return {
    inputOctets: input,
    outputOctets: output,
    countersWrapped: false,
    inputOctetsTotal: input,
    outputOctetsTotal: output,
  };
}

function buildSessions(now: number, rng: () => number, nas: Nas[], vouchers: Voucher[]): Session[] {
  const out: Session[] = [];
  /* Reachable devices **in `site-hcm`**. The site clause is load-bearing rather than
     decorative: the baseline below draws a voucher for each row, every voucher it
     can reach belongs to a café batch, and without the clause about half of these
     twenty-two would land on a hotel access point holding a code sold at the café. A
     cross-site world makes that kind of incoherence possible, so it is also
     asserted — see the fixture guard in `web/mock/src/validate.test.ts`.

     It also keeps the baseline where it was written to be. These twenty-two exist so
     the pathological rows stand out instead of being the only thing on screen, and
     that only works in the site the pathological rows are in. */
  const online = nas.filter((n) => n.reachability === "ok" && n.siteId === HCM);
  const inUse = vouchers.filter((v) => v.state === "in-use");
  let seq = 0;
  const id = () => `sess-${String(++seq).padStart(4, "0")}`;

  // A healthy baseline, so the pathological rows below stand out rather than being
  // the only thing on screen.
  for (let i = 0; i < 22; i++) {
    const n = pick(rng, online);
    const randomised = rng() < 0.7; // iOS 14+/Android 10+ default
    const started = now - int(rng, 2, 300) * MIN;
    const sampled = now - int(rng, 10, 280) * 1000;
    const v = inUse[i % Math.max(1, inUse.length)];
    out.push({
      id: id(),
      acctSessionId: `8${int(rng, 100000, 999999)}`,
      siteId: n.siteId,
      nasId: n.id,
      nasName: n.name,
      state: "online",
      callingStationId: randomised ? mac(rng, true) : macWithOui(rng, pick(rng, REAL_OUIS)),
      macIsRandomised: randomised,
      username: v ? null : pick(rng, ["nguyen.thi.mai", null]),
      identityKind: v ? "voucher" : "account",
      voucherId: v?.id ?? null,
      ipAddress: `10.20.${int(rng, 1, 4)}.${int(rng, 10, 250)}`,
      apName: n.name,
      ssid: "Foyer Guest",
      startedAt: iso(started),
      endedAt: null,
      lastUpdateAt: iso(sampled),
      terminateCause: null,
      freshness: fresh(now, sampled, 300),
      ...counters(int(rng, 1e6, 9e8), int(rng, 1e6, 3e9)),
      sessionSeconds: Math.floor((now - started) / 1000),
      plan: pick(rng, [PLANS.cafe1h!, PLANS.cafe24h!, PLANS.event!]),
    });
  }

  const unifi1 = nas[0]!;
  const mt = nas.find((n) => n.id === "nas-mt-1")!;
  const omada = nas.find((n) => n.id === "nas-omada-1")!;
  const duplicateMac = macWithOui(rng, "3c:22:fb");

  const specials: Session[] = [
    {
      /**
       * The classic RADIUS accounting failure: the NAS rebooted without sending
       * Acct-Stop, so this has been "online" for 40 days. Nothing closes it but the
       * reaper. A UI that draws this as a healthy green row is the bug.
       */
      id: SESSION_IDS.orphaned,
      acctSessionId: "80000042",
      siteId: unifi1.siteId,
      nasId: unifi1.id,
      nasName: unifi1.name,
      state: "orphaned",
      callingStationId: mac(rng, true),
      macIsRandomised: true,
      username: null,
      identityKind: "voucher",
      voucherId: inUse[0]?.id ?? null,
      ipAddress: "10.20.1.77",
      apName: unifi1.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 40 * DAY),
      endedAt: null,
      lastUpdateAt: iso(now - 40 * DAY + 4 * MIN),
      terminateCause: null,
      freshness: fresh(now, now - 40 * DAY + 4 * MIN, 300),
      ...counters(44_120_233, 91_882_004),
      sessionSeconds: Math.floor((40 * DAY) / 1000),
      plan: PLANS.cafe24h!,
    },
    {
      /**
       * 20 minutes stale against a 300 s interval — `caution-minor`, not a failure.
       *
       * 20 minutes is four missed intervals, which is also this session's resting
       * position in the mock's cycle: it starts degraded and **recovers** while the
       * operator watches, so the transition back to `sampled` is reviewable and not
       * merely asserted. See `web/mock/src/sim.ts`.
       */
      id: SESSION_IDS.staleMikrotik,
      acctSessionId: "80000043",
      siteId: mt.siteId,
      nasId: mt.id,
      nasName: mt.name,
      state: "online",
      callingStationId: mac(rng, true),
      macIsRandomised: true,
      username: null,
      identityKind: "voucher",
      voucherId: inUse[1]?.id ?? null,
      ipAddress: "10.30.0.44",
      apName: mt.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 3 * HOUR),
      endedAt: null,
      lastUpdateAt: iso(now - 20 * MIN),
      terminateCause: null,
      freshness: fresh(now, now - 20 * MIN, 300),
      ...counters(812_004_211, 2_904_881_003),
      sessionSeconds: Math.floor((3 * HOUR) / 1000),
      plan: PLANS.hotelRoom!,
    },
    {
      /**
       * No accounting at all — the controller-API path with no accounting profile
       * attached. There is no Access-Accept in which to request interim updates, so
       * there is nothing and there never will be. This must read as *unknown*, never
       * as unlimited and never as zero.
       */
      id: SESSION_IDS.noAccounting,
      acctSessionId: "",
      siteId: omada.siteId,
      nasId: omada.id,
      nasName: omada.name,
      state: "unknown",
      callingStationId: mac(rng, true),
      macIsRandomised: true,
      username: null,
      identityKind: "voucher",
      voucherId: null,
      ipAddress: null,
      apName: omada.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 55 * MIN),
      endedAt: null,
      lastUpdateAt: null,
      terminateCause: null,
      freshness: fresh(now, null, null),
      ...counters(null, null),
      sessionSeconds: null,
      plan: PLANS.staffUnmetered!,
    },
    {
      /**
       * Clock skew: the NAS believes the session started after the server's now, so
       * the duration is negative. Observed in real pfSense/FreeRADIUS deployments.
       * The UI renders the anomaly rather than "-00:04:12".
       */
      id: SESSION_IDS.clockSkew,
      acctSessionId: "80000044",
      siteId: unifi1.siteId,
      nasId: unifi1.id,
      nasName: unifi1.name,
      state: "online",
      callingStationId: macWithOui(rng, "a4:83:e7"),
      macIsRandomised: false,
      username: null,
      identityKind: "voucher",
      voucherId: null,
      ipAddress: "10.20.2.31",
      apName: unifi1.name,
      ssid: "Foyer Guest",
      startedAt: iso(now + 4 * MIN),
      endedAt: null,
      lastUpdateAt: iso(now - 30_000),
      terminateCause: null,
      freshness: fresh(now, now - 30_000, 300),
      ...counters(1_204, 8_991),
      sessionSeconds: -252,
      plan: PLANS.cafe1h!,
    },
    // Two overlapping live sessions for one MAC — a roam that never closed, or a
    // spoof. Either way the operator needs to see both, so neither may be hidden by
    // a de-duplicating list.
    {
      id: SESSION_IDS.duplicateA,
      acctSessionId: "80000045",
      siteId: unifi1.siteId,
      nasId: unifi1.id,
      nasName: unifi1.name,
      state: "online",
      callingStationId: duplicateMac,
      macIsRandomised: false,
      username: null,
      identityKind: "voucher",
      voucherId: null,
      ipAddress: "10.20.1.90",
      apName: unifi1.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 90 * MIN),
      endedAt: null,
      lastUpdateAt: iso(now - 120_000),
      terminateCause: null,
      freshness: fresh(now, now - 120_000, 300),
      ...counters(220_004_112, 640_112_884),
      sessionSeconds: Math.floor((90 * MIN) / 1000),
      plan: PLANS.cafe24h!,
    },
    {
      // Same MAC, different AP, same non-unique Acct-Session-Id as above. Both
      // conditions are observed in the field; neither may be used alone as a key.
      id: SESSION_IDS.duplicateB,
      acctSessionId: "80000045",
      siteId: nas[1]!.siteId,
      nasId: nas[1]!.id,
      nasName: nas[1]!.name,
      state: "online",
      callingStationId: duplicateMac,
      macIsRandomised: false,
      username: null,
      identityKind: "voucher",
      voucherId: null,
      ipAddress: "10.20.2.90",
      apName: nas[1]!.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 12 * MIN),
      endedAt: null,
      lastUpdateAt: iso(now - 60_000),
      terminateCause: null,
      freshness: fresh(now, now - 60_000, 300),
      ...counters(4_112, 9_004),
      sessionSeconds: Math.floor((12 * MIN) / 1000),
      plan: PLANS.cafe24h!,
    },
    {
      /** Ended cleanly. The list needs rows where "last seen" is meaningful. */
      id: SESSION_IDS.endedClean,
      acctSessionId: "80000046",
      siteId: unifi1.siteId,
      nasId: unifi1.id,
      nasName: unifi1.name,
      state: "ended",
      callingStationId: macWithOui(rng, "50:eb:f6"),
      macIsRandomised: false,
      username: "tran.van.hung",
      identityKind: "dot1x",
      voucherId: null,
      ipAddress: "10.20.1.14",
      apName: unifi1.name,
      ssid: "Foyer Corp",
      startedAt: iso(now - 6 * HOUR),
      endedAt: iso(now - 2 * HOUR),
      lastUpdateAt: iso(now - 2 * HOUR),
      terminateCause: "User-Request",
      freshness: fresh(now, now - 2 * HOUR, 300),
      ...counters(3_112_004_882, 812_004_112),
      sessionSeconds: 4 * 60 * 60,
      plan: PLANS.staffUnmetered!,
    },
    {
      /** Ended because the voucher was revoked mid-session. */
      id: SESSION_IDS.endedRevoked,
      acctSessionId: "80000047",
      siteId: mt.siteId,
      nasId: mt.id,
      nasName: mt.name,
      state: "ended",
      callingStationId: mac(rng, true),
      macIsRandomised: true,
      username: null,
      identityKind: "voucher",
      voucherId: vouchers.find((v) => v.state === "revoked")?.id ?? null,
      ipAddress: "10.30.0.51",
      apName: mt.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 80 * MIN),
      endedAt: iso(now - 14 * MIN),
      lastUpdateAt: iso(now - 14 * MIN),
      terminateCause: "Admin-Reset",
      freshness: fresh(now, now - 14 * MIN, 300),
      ...counters(91_004_112, 204_881_004),
      sessionSeconds: 66 * 60,
      plan: PLANS.cafe1h!,
    },
    {
      /**
       * **Live, and its accounting has gone quiet.**
       *
       * Distinct from the orphan above, and the distinction is the whole point. The
       * orphan is a session that *ended* and nobody said so. This one is a session
       * that is still running while its Interim-Updates stopped arriving — a NAS
       * whose accounting queue overflowed, or whose accounting server went away, or
       * whose interim interval was silently dropped by a config push.
       *
       * What it is there to break: any UI that reads a frozen counter as "the guest
       * stopped using data". The plan here is `foyer-sampled`, so the quota reading
       * is the sample — and the sample has stopped, while the guest has not. The
       * remaining allowance shown beside this row is not just imprecise, it is
       * *frozen*, and the row has to say so.
       *
       * This is the session the mock takes silent and brings back while the operator
       * watches: the qualifier segment appears, the heartbeat stops, and the status
       * does not change. See `web/mock/src/sim.ts`.
       */
      id: SESSION_IDS.interimGap,
      acctSessionId: "80000048",
      siteId: unifi1.siteId,
      nasId: unifi1.id,
      nasName: unifi1.name,
      state: "online",
      callingStationId: macWithOui(rng, "dc:a6:32"),
      macIsRandomised: false,
      username: null,
      identityKind: "voucher",
      voucherId: inUse[2]?.id ?? null,
      ipAddress: "10.20.3.18",
      apName: unifi1.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 70 * MIN),
      endedAt: null,
      lastUpdateAt: iso(now - 45_000),
      terminateCause: null,
      freshness: fresh(now, now - 45_000, 300),
      ...counters(188_004_112, 1_402_881_004),
      sessionSeconds: 70 * 60,
      plan: PLANS.hotelRoom!,
    },
    {
      /**
       * **A counter that goes backwards.**
       *
       * `Acct-Input-Octets` and `Acct-Output-Octets` are 32-bit (RFC 2866 §5.3–5.4).
       * RFC 2869 adds `Acct-Input-Gigawords`/`Acct-Output-Gigawords` for exactly one
       * reason — the 32-bit counters wrap at 4 GiB — and a NAS that does not send
       * them reports a total that silently drops by 4 294 967 296 mid-session.
       *
       * What it is there to break: every arithmetic the admin does on a byte total.
       * "Used" computed as a difference goes negative; "remaining" computed as
       * allowance-minus-used jumps back up and hands the guest 4 GiB of free quota;
       * a delta-based rate estimate reads as a wildly negative throughput. A UI that
       * treats accounting counters as monotonic is wrong, and this row is where it
       * finds out.
       *
       * The mock advances this one fast enough that the wrap happens roughly once a
       * cycle, so it is watchable rather than theoretical.
       */
      id: SESSION_IDS.counterWrap,
      acctSessionId: "80000049",
      siteId: unifi1.siteId,
      nasId: unifi1.id,
      nasName: unifi1.name,
      state: "online",
      callingStationId: macWithOui(rng, "f0:9f:c2"),
      macIsRandomised: false,
      username: null,
      identityKind: "voucher",
      voucherId: null,
      ipAddress: "10.20.1.61",
      apName: unifi1.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 58 * MIN),
      endedAt: null,
      lastUpdateAt: iso(now - 70_000),
      terminateCause: null,
      freshness: fresh(now, now - 70_000, 300),
      // Just under 2^32 on the downlink. The next few intervals take it over — and
      // `countersWrapped` is false *here*, because at t=0 nothing has wrapped yet.
      // The tripwire arms while someone is watching rather than at boot.
      ...counters(210_884_003, 4_012_004_881),
      sessionSeconds: 58 * 60,
      plan: PLANS.cafe24h!,
    },
    {
      /**
       * **A rate limit nothing can apply, and nothing can even observe.**
       *
       * The same access point as `sess-no-accounting`: the Omada whose driver
       * reports `bandwidthLimits: false`, `hardQuota: false` and
       * `walledGarden: "none"`, and whose controller-API path has no accounting
       * profile attached. The only difference between the two rows is that this
       * one's plan states 5 Mbps down and 2 Mbps up. See `PLANS.warehouseShift`.
       *
       * What it is there to break: a screen that renders `plan.downKbps` because
       * the plan has one. `Capabilities.bandwidthLimits` is the only thing that can
       * say whether the figure is in force, and without this row there would be no
       * fixture where the plan names a rate and the driver cannot apply it — so a
       * rate rendered with no capability in the query would look correct everywhere.
       *
       * Its accounting is absent for the same reason `sess-no-accounting`'s is, and
       * it has to be: `accountingConfigured` is **absent** on this device (see
       * `nas-omada-1` in `buildNas`), so a byte counter here would be a fixture inventing evidence
       * that cannot exist. A byte counter would need a measurement behind it, and
       * this device has none.
       * This is also what makes the row worth reading twice — with no NAS enforcement and
       * no sampled backstop, there is nothing behind the number at either end.
       *
       * Appended rather than slotted in beside its twin: the specials are rendered
       * in array order and the suites that reach for "the first row" would otherwise
       * silently be reaching for a different session.
       */
      id: SESSION_IDS.unenforceableRate,
      acctSessionId: "",
      siteId: omada.siteId,
      nasId: omada.id,
      nasName: omada.name,
      state: "unknown",
      callingStationId: macWithOui(rng, "b8:27:eb"),
      macIsRandomised: false,
      username: null,
      identityKind: "voucher",
      voucherId: null,
      ipAddress: null,
      apName: omada.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 3 * HOUR),
      endedAt: null,
      lastUpdateAt: null,
      terminateCause: null,
      freshness: fresh(now, null, null),
      ...counters(null, null),
      sessionSeconds: null,
      plan: PLANS.warehouseShift!,
    },
  ];

  /*
   * **A guest stranded on an access point nothing can reach.**
   *
   * The ugly-fixture rule in its strongest form — not a fixture that heals itself
   * but a case nobody would otherwise write. `revokeVoucher` in
   * `web/mock/src/state.ts` has a branch for *the voucher was revoked and the driver
   * call that would end the session could not be made*, which answers
   * `/problems/session-end-failed` with the device's name and reachability. Without
   * this row nothing in the world reaches it: every other voucher with a live session
   * sits on a device whose `reachability` is `ok`, so the only way to see that copy
   * would be to drive the portal onto `nas-unifi-3` first, and someone opening
   * `/vouchers` could not.
   *
   * **`orphaned`, and the cause is the one the other orphan does not carry.**
   * `sess-orphaned-no-stop` is a NAS that rebooted and never sent `Acct-Stop` — the
   * device is fine, the record is wrong. This one is the device itself gone: it stopped
   * answering nine days ago, which is `nas-unifi-3.lastSeenAt` exactly, and the
   * accounting stopped in the same breath. Same state, different afternoon, and the
   * difference is the whole of why revoking here fails and revoking there does not.
   *
   * **The voucher is still `in-use`, because nothing closed it.** That is the shape an
   * operator meets: a code that reads as in use, and a Revoke that frees the code and
   * cannot free the network — which the confirmation has to say, rather than only
   * *"Revoked"*.
   *
   * No `rng` call anywhere in here. Every random value in this file comes off one
   * shared stream, so drawing from it would silently re-roll every MAC built after
   * this point and move fixtures nothing about this row should touch.
   */
  const spoken = new Set([...out, ...specials].map((s) => s.voucherId).filter(Boolean));
  /* A voucher no other session already claims: `revokeVoucher` takes the *first*
     session matching the voucher, so sharing one with a session on a healthy device
     would revoke through that one instead and this row would never be consulted. */
  const stranded = inUse.find((v) => !spoken.has(v.id));
  const gone = nas.find((n) => n.reachability === "unreachable");
  if (stranded && gone) {
    stranded.currentSessionId = SESSION_IDS.strandedUnreachable;
    specials.push({
      id: SESSION_IDS.strandedUnreachable,
      acctSessionId: "80000051",
      siteId: gone.siteId,
      nasId: gone.id,
      nasName: gone.name,
      state: "orphaned",
      callingStationId: "b2:41:7d:96:0c:58",
      // Locally administered, and the bit is set: iOS 14+/Android 10+ per-SSID MAC.
      macIsRandomised: true,
      username: null,
      identityKind: "voucher",
      voucherId: stranded.id,
      ipAddress: "10.20.0.132",
      apName: gone.name,
      ssid: "Foyer Guest",
      startedAt: iso(now - 9 * DAY - 2 * HOUR),
      endedAt: null,
      lastUpdateAt: iso(now - 9 * DAY),
      terminateCause: null,
      freshness: fresh(now, now - 9 * DAY, 300),
      ...counters(1_204_887_003, 3_881_002_744),
      sessionSeconds: Math.floor((9 * DAY + 2 * HOUR) / 1000),
      plan: PLANS.hotelRoom!,
    });
  }

  /*
   * ── the hotel's guests ──────────────────────────────────────────────────────
   *
   * Three rows in `site-hanoi`, and the first of them is the one that matters.
   *
   * **`crossSiteMac` carries the address two live café sessions already carry.**
   * A cross-site fixture that only adds *different* rows to a second site proves
   * that a list can get longer. This one proves the scope: an operator searching
   * that MAC at the café must be shown two sessions, and searching it at the hotel
   * must be shown one, and the only thing standing between those two answers is the
   * `siteId` on the query. Drop it and the café's operator is looking at a guest in
   * another city — with *End session* one column away.
   *
   * The address is universally administered (`3c:22:fb`, Apple, and the
   * locally-administered bit is clear), which is what makes it legitimate in two
   * places at once: iOS and Android present a **per-SSID** private address, so a
   * randomised MAC on two networks would be a fixture inventing something the
   * platforms do not do. A laptop with private Wi-Fi address turned off keeps one
   * MAC everywhere, and that is this row.
   *
   * It also gives the café's duplicate-MAC pair a second lesson without weakening
   * the first: *two live sessions on one address in one site are both real*, and
   * *a third on the same address in another site is a different device entirely*.
   *
   * None of these three is pathological, and that is deliberate — see `buildSites`.
   * The hotel raises no alarm, so a dropped scope is what makes it look broken.
   * Appended after everything above and drawing nothing from `rng`, for the reason
   * the stranded row states.
   */
  const hanoiLobby = nas.find((n) => n.id === "nas-hanoi-1");
  const hanoiFloor = nas.find((n) => n.id === "nas-hanoi-2");
  const hanoiVoucher = inUse.find((v) => v.batchId === "batch-hanoi-rooms");
  if (hanoiLobby && hanoiFloor) {
    specials.push(
      {
        id: SESSION_IDS.crossSiteMac,
        acctSessionId: "90000011",
        siteId: hanoiFloor.siteId,
        nasId: hanoiFloor.id,
        nasName: hanoiFloor.name,
        state: "online",
        callingStationId: duplicateMac,
        macIsRandomised: false,
        username: null,
        identityKind: "voucher",
        voucherId: null,
        ipAddress: "10.50.4.22",
        apName: hanoiFloor.name,
        ssid: "HoGuom-Guest",
        startedAt: iso(now - 35 * MIN),
        endedAt: null,
        lastUpdateAt: iso(now - 100_000),
        terminateCause: null,
        freshness: fresh(now, now - 100_000, 300),
        ...counters(52_118_004, 331_004_772),
        sessionSeconds: 35 * 60,
        plan: PLANS.hotelRoom!,
      },
      {
        id: SESSION_IDS.hanoiVoucher,
        acctSessionId: "90000012",
        siteId: hanoiLobby.siteId,
        nasId: hanoiLobby.id,
        nasName: hanoiLobby.name,
        state: "online",
        /* Locally administered and the bit is set — the ordinary iOS/Android case,
           which the hotel needs one of so its rows are not all universal OUIs. */
        callingStationId: "9e:0d:41:77:2c:b3",
        macIsRandomised: true,
        username: null,
        identityKind: "voucher",
        voucherId: hanoiVoucher?.id ?? null,
        ipAddress: "10.50.1.31",
        apName: hanoiLobby.name,
        ssid: "HoGuom-Guest",
        startedAt: iso(now - 2 * HOUR),
        endedAt: null,
        lastUpdateAt: iso(now - 80_000),
        terminateCause: null,
        freshness: fresh(now, now - 80_000, 300),
        ...counters(410_882_119, 1_204_003_881),
        sessionSeconds: 2 * 60 * 60,
        plan: PLANS.hotelRoom!,
      },
      {
        /* An ended row, so `listSessions?siteId=site-hanoi&state=…` narrows something
           rather than filtering an all-online list down to itself. */
        id: SESSION_IDS.hanoiEnded,
        acctSessionId: "90000013",
        siteId: hanoiLobby.siteId,
        nasId: hanoiLobby.id,
        nasName: hanoiLobby.name,
        state: "ended",
        callingStationId: "9e:0d:41:55:aa:1c",
        macIsRandomised: true,
        username: "le.thi.hoa",
        identityKind: "account",
        voucherId: null,
        ipAddress: "10.50.1.18",
        apName: hanoiLobby.name,
        ssid: "HoGuom-Staff",
        startedAt: iso(now - 7 * HOUR),
        endedAt: iso(now - 90 * MIN),
        lastUpdateAt: iso(now - 90 * MIN),
        terminateCause: "User-Request",
        freshness: fresh(now, now - 90 * MIN, 300),
        ...counters(88_004_112, 640_881_003),
        sessionSeconds: Math.floor((7 * HOUR - 90 * MIN) / 1000),
        plan: PLANS.staffUnmetered!,
      },
    );
    if (hanoiVoucher) hanoiVoucher.currentSessionId = SESSION_IDS.hanoiVoucher;
  }

  return [...specials, ...out];
}

// ── controllers ──────────────────────────────────────────────────────────────

/**
 * The controller version floor, per vendor — what the driver needs, not what is new.
 *
 * It lives here rather than in `web/mock` because two things read it and they must
 * not be able to disagree: the probe, which decides `versionSupported`, and the
 * fixture guard, which refuses a controller row whose `versionSupported` contradicts
 * its own `version`. An alarm that says *upgrade* about a controller the probe
 * dialog calls fine is worse than no alarm.
 *
 * **UniFi's floor is sourced and Omada's is weaker.** The UniFi Integration API
 * requires Network Application >= 9.1.105 (`docs/protocol-reality.md`, DOCUMENTED);
 * Omada's 5.0.15 comes from the KB that documents the redirect parameters and is
 * recorded there as *not* separately sourced. Ruckus has **no** floor established,
 * and `null` is that fact rather than a permissive default: nothing may report a
 * Ruckus version as unsupported until somebody measures one.
 */
export const CONTROLLER_MINIMUM_VERSION: Record<Controller["vendor"], string | null> = {
  unifi: "9.1.105",
  omada: "5.0.15",
  ruckus: null,
};

/**
 * The capability values a driver believes rather than measures, by vendor —
 * `NasProbe.unverified` / `ControllerProbe.unverified`.
 *
 * **Empty is a claim about the driver, not about the hardware**, and it is the
 * shipped reading rather than a permissive one: `nas.UnverifiedCapabilities` returns
 * nothing for a driver that declares no `CapabilityEvidence` at all, and only the
 * UniFi driver declares any. So a Ruckus probe naming nothing here means *this
 * driver cannot say how it knows*, which is exactly what an operator should be able
 * to see is different from *this driver measured everything*.
 *
 * The two UniFi names are `internal/nas/unifi.go`'s for a driver holding an API key:
 * `walledGarden` (no documented Integration API endpoint programs the
 * pre-authorization allowance) and `rotateSecret` (no documented operation writes the
 * RADIUS profile's shared secret). `liveReauthorize` is not on the list because it was
 * measured true on the Integration API; a driver on a local admin account still
 * reports it unverified, and this world has none. Sorted, because the Go side sorts and
 * a list whose order differs between two servers is a diff the differential comparison
 * has to explain away.
 *
 * This is a cross-language copy of a Go fact and there is no way for it not to be.
 * What keeps it from drifting silently is the guard in `web/mock`: every name here
 * must be a property of the contract's own `Capabilities` schema, read from the
 * document rather than typed out a second time.
 */
export const UNVERIFIED_CAPABILITIES: Record<Controller["vendor"], readonly string[]> = {
  unifi: ["rotateSecret", "walledGarden"],
  omada: [],
  ruckus: [],
};

/**
 * Whether `version` is at or above `minimum`, comparing each dotted field as a
 * **number**.
 *
 * The string comparison anyone reaches for first is wrong on the real values in this
 * repository: `9.4.19` is newer than the floor `9.1.105`, and `"9.4.19" < "9.1.105"`
 * is false only by luck of the second field — on `9.1.19` against `9.1.105` the
 * lexicographic answer is *supported* and the numeric answer is *too old*. There is
 * a test on exactly that pair.
 *
 * `null` for either side means **not established**, which is neither pass nor fail —
 * the caller decides, and the only correct decision is to leave `versionSupported`
 * null rather than to guess.
 */
export function versionMeetsMinimum(
  version: string | null | undefined,
  minimum: string | null | undefined,
): boolean | null {
  if (!version || !minimum) return null;
  const fields = (v: string) => v.split(".").map((part) => Number.parseInt(part, 10));
  const a = fields(version);
  const b = fields(minimum);
  if (a.some(Number.isNaN) || b.some(Number.isNaN)) return null;
  for (let i = 0; i < Math.max(a.length, b.length); i += 1) {
    const left = a[i] ?? 0;
    const right = b[i] ?? 0;
    if (left !== right) return left > right;
  }
  return true;
}

/**
 * Whether a controller belongs on the screen of an operator working in `siteId`.
 *
 * **One predicate, and it is exported so that there is only one of it.** The same
 * rule is applied in `internal/apistore/controllers.go`, in
 * `internal/apistore/dashboard.go`, in `web/mock`'s `GET /controllers` and in
 * `deriveAlarms` below, and separate copies drift into different rules that answer
 * the same question differently. `docs/rules/database.md` has the section: a
 * stand-in for a rule whose authority is sitting right there drifts silently, still
 * passing, still printing a reason.
 *
 * Two ways in, and they are different facts about the same object: the controller is
 * **bound to this site**, or it is **bound to nothing at all** — registered, perhaps
 * probed, and not yet attached to a venue. The second is a state with a next step in
 * it rather than an absence, and hiding it removes the only screen carrying the step.
 *
 * Each half has a plausible wrong reading, and each wrong reading fails in a
 * different direction:
 *
 *   * **"Bound to nothing" is not `vendorSites.length === 0`.** That reading holds
 *     only for as long as nothing records a controller's own sites. The UniFi probe
 *     reports them, so under it probing a controller would remove it from every site
 *     view, including the one carrying `discoverFromController` — the call that
 *     would bind it. That hides a controller.
 *   * **"Bound to this site" is not "some vendor site is unadopted".** `ctl-unifi` is
 *     adopted into the café and its `q7-branch` site is free, so that question would
 *     list it on the hotel's controllers screen, beside *Delete* and *Discover access
 *     points*. That shows a management object to a venue that does not own it.
 *
 * The price of the narrow reading is that a second vendor site is adoptable only
 * from the Foyer site that already holds the controller. That costs nothing while
 * `createSite` refuses the second venue — one venue per install — and it is the
 * safer half of the trade on the day it does not.
 */
export function controllerServesSite(c: Pick<Controller, "vendorSites">, siteId: string): boolean {
  if (!siteId) return true;
  let bound = false;
  for (const v of c.vendorSites) {
    if (v.boundSiteId === siteId) return true;
    if (v.boundSiteId) bound = true;
  }
  return !bound;
}

/**
 * The controllers, and they are ugly in the ways that matter.
 *
 * One healthy UniFi console with three access points under it and one shared secret;
 * one Omada whose hotspot-operator credential has expired; one Ruckus SmartZone
 * that has **never been probed**, so its version is unknown and its site list is
 * empty — which is the state every controller is in for the first thirty seconds and
 * the state a console built only against healthy fixtures renders as a blank card;
 * and one UniFi that answers, whose credential is accepted, and whose **version is
 * below the floor this driver needs**.
 *
 * That fourth row is the fixtures rule in its strongest form. `Controllers.tsx`
 * draws an *urgent* pill on `versionSupported === false`, and without this row **the
 * condition would never render once**: the three controllers above are `true`,
 * `true`, `null`, and both probe paths in the mock produce `true` or `null`. Not a
 * fixture that heals itself — a case nobody wrote, which is harder to notice
 * because there is nothing there to look wrong.
 *
 * `ctl-unifi` is deliberately **`trust-on-first-use` with a pinned fingerprint**,
 * because that is what a real deployment looks like: controllers ship self-signed
 * certificates and the alternative every shipped integration reaches for is turning
 * verification off. `ctl-omada` carries a `controllerId`, the 32-character string
 * from its own URL, which cannot be guessed and so has to be asked for.
 *
 * **Two further rows, and neither is decoration.** `ctl-hanoi` is the hotel's, so
 * that a site scope can be dropped in either direction rather than one;
 * `ctl-unifi-unattached` is a controller that answered a probe and is adopted by
 * nobody, the state the four rows above cannot express between them. Each row's own
 * comment carries the argument.
 *
 * **What this set still cannot say.** Every controller here is `ok`,
 * `credentials-expired` or `unknown`: **no controller is `unreachable`**, so the
 * status this screen draws for a console that stopped answering has never rendered
 * from a fixture — the same shape as the `versionSupported === false` case above,
 * one enum value along. And `tlsMode: "verify"` occurs only on `ctl-ruckus`, which
 * has no credential and has never been probed, so *verify* and *never contacted* are
 * perfectly correlated and nothing can tell a rendering that keys on one from a
 * rendering that keys on the other. Both are known gaps rather than fixed here:
 * adding rows for them changes what every suite that counts controllers is counting.
 */
function buildControllers(now: number): Controller[] {
  return [
    {
      id: "ctl-unifi",
      name: "UniFi — Cloud Key Gen2",
      vendor: "unifi",
      baseUrl: "https://10.20.0.2",
      controllerId: null,
      authMode: "api-key",
      tlsMode: "trust-on-first-use",
      credentialSet: true,
      certificateFingerprint: "9F:22:3A:0C:7E:41:B8:D5:6A:12:EE:03:47:9B:CC:10",
      reachability: "ok",
      version: "9.4.19",
      versionSupported: true,
      vendorSites: [
        { id: "default", name: "Default", boundSiteId: "site-hcm" },
        // A second site the operator has *not* adopted. A controller manages many,
        // and a console that assumed one would authorize guests into the wrong one.
        { id: "q7-branch", name: "Chi nhánh Quận 7", boundSiteId: null },
      ],
      lastContactAt: iso(now - 40_000),
      createdAt: iso(now - 120 * DAY),
    },
    {
      id: "ctl-omada",
      name: "Omada — OC200",
      vendor: "omada",
      baseUrl: "https://10.20.0.3:443",
      controllerId: "a1b2c3d4e5f60718293a4b5c6d7e8f90",
      // The account TP-Link is explicit is *not* the controller admin, and the one
      // everybody types wrong the first time.
      authMode: "hotspot-operator",
      tlsMode: "trust-on-first-use",
      credentialSet: true,
      certificateFingerprint: "1B:88:D4:52:0F:9C:3E:77:A0:64:12:BB:35:9E:D1:2A",
      reachability: "credentials-expired",
      version: "5.14.26",
      versionSupported: true,
      vendorSites: [{ id: "Default", name: "Default", boundSiteId: "site-hcm" }],
      lastContactAt: iso(now - 34 * DAY),
      createdAt: iso(now - 200 * DAY),
    },
    {
      id: "ctl-ruckus",
      name: "Ruckus SmartZone — chưa cấu hình",
      vendor: "ruckus",
      baseUrl: "https://10.40.0.9:8443",
      controllerId: null,
      authMode: "local-admin",
      tlsMode: "verify",
      credentialSet: false,
      certificateFingerprint: null,
      reachability: "unknown",
      // Never probed. `null` and not `false`: nothing has established it, which is a
      // different fact from established-and-too-old, and the console must not render
      // the first as the second.
      version: null,
      versionSupported: null,
      vendorSites: [],
      lastContactAt: null,
      createdAt: iso(now - 2 * DAY),
    },
    {
      id: "ctl-unifi-legacy",
      // A Gen1 Cloud Key, and the choice is not decoration: the hardware is
      // end-of-life and cannot run a Network Application that would satisfy the
      // floor. So the instruction the alarm gives — upgrade to 9.1.105 — is one this
      // operator cannot follow without buying a box. An alarm whose remedy is
      // impossible is the case worth having on screen, and a fixture running one
      // point release behind would have hidden it.
      name: "UniFi — Cloud Key Gen1",
      vendor: "unifi",
      baseUrl: "https://10.30.0.4:8443",
      controllerId: null,
      // Not `api-key`: the Integration API that issues one arrived *with* the floor,
      // so a controller below it has no key to give. The operator reached for the
      // only credential this version has, and it works — which is exactly why
      // nothing else on the dashboard says a word about this controller.
      authMode: "local-admin",
      tlsMode: "trust-on-first-use",
      credentialSet: true,
      certificateFingerprint: "5E:19:C0:73:2B:84:AF:6D:31:97:52:0E:DA:48:B1:66",
      // Reachable, and the credential is accepted. Every other health signal this
      // console has is green.
      reachability: "ok",
      version: "8.6.9",
      // Established, and false. The third value is not in play here — a probe read
      // this version off the controller and compared it with the floor.
      versionSupported: false,
      vendorSites: [{ id: "default", name: "Default", boundSiteId: "site-hcm" }],
      lastContactAt: iso(now - 6 * MIN),
      createdAt: iso(now - 400 * DAY),
    },
    {
      /**
       * **The hotel's controller, and every signal on it is green.**
       *
       * Bound to `site-hanoi`, reachable, credential set, above the version floor.
       * It is here so that `listControllers?siteId=` differs in *both* directions
       * rather than one: without this row the hotel's list would be a subset of the
       * café's, because the only controllers the hotel saw would be ones every site
       * sees, and a dropped scope could therefore only ever add. With it, each site
       * holds a controller the other must not show, and the difference is visible
       * from either end.
       *
       * `ctl-unifi` does **not** appear under every site, even though its
       * `q7-branch` vendor site is unadopted: a controller adopted into the café is
       * the café's, whatever else it manages. `ctl-ruckus` appears everywhere,
       * because it is bound to nothing.
       *
       * Its binding collides with nothing, and neither do the three on `site-hcm`.
       * Three controllers binding one site is legal in both the contract and the
       * schema: migration `00011` makes the index `UNIQUE (controller_id,
       * bound_site_id)`, because every resolution path goes through the **device**
       * and the only real ambiguity is two vendor sites *of one controller* claiming
       * one Foyer site.
       */
      id: "ctl-hanoi",
      name: "UniFi — UDM Pro (Hồ Gươm)",
      vendor: "unifi",
      baseUrl: "https://10.50.0.2",
      controllerId: null,
      authMode: "api-key",
      tlsMode: "trust-on-first-use",
      credentialSet: true,
      certificateFingerprint: "3D:7A:16:C4:88:2F:B0:59:E1:47:6C:22:9A:D3:04:F8",
      reachability: "ok",
      version: "9.3.45",
      versionSupported: true,
      vendorSites: [{ id: "default", name: "Default", boundSiteId: HANOI }],
      lastContactAt: iso(now - 55_000),
      createdAt: iso(now - 80 * DAY),
    },
    {
      /**
       * **Probed, answering, and attached to no venue at all.**
       *
       * ───────────────────────────────────────────────────────────────────────
       *
       * Without this row nothing in this world could hold this state. Every other
       * controller here is either *never probed with no site list at all*
       * (`ctl-ruckus`) or *probed and adopted* (the other four), and the middle
       * state — **read, and adopted by nobody** — would be reachable only by driving
       * `POST /controllers` and then `POST /controllers/:id/probe` through the mock:
       * invisible on the screen and to every static assertion about controller scope.
       *
       * That matters more than a missing case, because of *which* assertion it would
       * make vacuous. `web/mock`'s scope test asks that a controller bound to nothing
       * is listed under every site, and without this row the only authored row
       * satisfying it is `ctl-ruckus`, whose vendor-site list is **empty**. The wrong
       * predicate, `vendorSites.length === 0`, answers `ctl-ruckus` *correctly*: the
       * wrong rule and the right rule would agree about every other controller in
       * this file.
       *
       * **Measured, by putting `vendorSites.length === 0` back into
       * `controllerServesSite` and running that test both ways:**
       *
       *     without this row, alone          green — the defect is invisible
       *     without this row, whole file     red, about `ctl-ddpys1sk`
       *     with this row                    red, about `ctl-unifi-unattached`
       *
       * The middle line is the one worth reading. A drive several hundred lines
       * earlier registers a Ruckus and probes it, and the mock's probe writes a
       * default vendor site with no binding — so the world the test read had the
       * state in it **as residue from an unrelated test**, under an id that changes
       * every run and belongs to no fixture. The suite could fail and could not say
       * what about. That is why the assertion is made over `buildWorld()` and why
       * the subject is an authored, named row.
       *
       * So the shape is chosen against the two rules and not against a screenshot:
       *
       * - **`vendorSites` is non-empty and nothing in it is bound.** That is what
       *   separates it from `ctl-ruckus` and what `vendorSites.length === 0` gets wrong.
       * - **Two vendor sites, both free** — `0 of 2` against `ctl-unifi`'s `1 of 2`.
       *   Same denominator, different numerator, so a rendering that reads the
       *   *length* of the list cannot tell an unattached controller from a partly
       *   adopted one; only the bound count can. It also means *Attach to this
       *   venue* has a real choice to offer rather than one option it could have
       *   picked silently.
       * - **Every other signal is green.** Reachable, credential accepted, above the
       *   version floor, contacted a moment ago. That is the whole point: the row
       *   looks finished, and the one step that makes the controller do anything is
       *   named nowhere but the Sites column and the action label. A row that was
       *   also unreachable would let a reader take the fault for connectivity.
       * - **`reachability: "ok"` is load-bearing, not decoration.**
       *   `discoverFromController` — the call that performs the binding — is refused
       *   for anything else, so an unattached controller that could not answer would
       *   render an attach action the server would refuse.
       *
       * It is listed under every site, which is the rule for a controller bound to
       * nothing (`controllerServesSite`), and it stays that way: a probe of this row
       * re-reads its site list and **does not bind it**, so unlike a fixture that
       * heals itself this one survives the mock's own ticks unchanged. The only
       * thing that clears it is the operator doing the thing the screen asks.
       */
      id: "ctl-unifi-unattached",
      name: "UniFi — UDM SE (Thảo Điền, chưa gắn)",
      vendor: "unifi",
      baseUrl: "https://10.60.0.2",
      controllerId: null,
      authMode: "api-key",
      tlsMode: "trust-on-first-use",
      credentialSet: true,
      certificateFingerprint: "A2:65:0E:D9:3C:71:84:FB:27:50:9A:16:C3:E8:4D:BB",
      reachability: "ok",
      version: "9.4.19",
      versionSupported: true,
      // Both free. `boundSiteId: null` twice is the whole fixture: it is not an
      // empty list, and it is not a binding.
      vendorSites: [
        { id: "default", name: "Default", boundSiteId: null },
        { id: "thao-dien", name: "Thảo Điền", boundSiteId: null },
      ],
      lastContactAt: iso(now - 4 * MIN),
      // Registered this morning. The state is a first-run state and the timestamp
      // should not make anyone read it as one that has been ignored for months.
      createdAt: iso(now - 3 * HOUR),
    },
  ];
}

// ── authentication attempts ──────────────────────────────────────────────────

/**
 * Whether this deployment records the client MAC on an authentication attempt.
 *
 * Fixed at `recorded` here because Foyer's generated `post-auth` query populates
 * `CallingStationId` — but note what the stock FreeRADIUS 3.2.x query does, because
 * it is why the field exists at all: it inserts only `username`, `pass`, `reply` and
 * `authdate`, leaving the MAC columns empty even though the schema has them. A
 * deployment on stock config answers `not-recorded`, and every MAC search in the
 * console then returns nothing for a reason that has nothing to do with the guest.
 *
 * `ATT_IDS.noMac` is the row that exercises the consequence inside a `recorded`
 * deployment, which is the harder case: coverage is not all-or-nothing per row.
 */
export const MAC_COVERAGE: MacCoverage = "recorded";

/**
 * What this stream cannot see, ever.
 *
 * **Required to be non-empty**, per the contract. An empty result for a MAC means
 * *no record*, and the console has to be able to say what produces no record —
 * otherwise the most common real fault in the product (a wrong shared secret, which
 * FreeRADIUS answers by discarding the packet) renders as a blank table and reads as
 * "this guest never tried".
 */
export const AUTH_BLIND_SPOTS: readonly AuthBlindSpot[] = [
  { kind: "shared-secret-mismatch" },
  { kind: "unknown-radius-client" },
  { kind: "never-associated" },
  { kind: "before-retention", params: { retentionDays: 30 } },
];

/**
 * The same list, answered for the site actually being read.
 *
 * One constant served for every site would make `no-radius-configured`
 * unreachable — the one value in the enum whose whole meaning
 * is *this site has no RADIUS at all*, on a stream whose fixtures all belong to the
 * one site that does. `site-hanoi` and `site-lab` have no `Nas` row between them, so
 * their attempt stream is empty **by design**, and the four entries above answer a
 * question they are not being asked: there is no shared secret to mismatch and no
 * `clients.conf` for a client to be unknown to.
 *
 * `never-associated` survives both branches because it is a fact about the radio
 * rather than about RADIUS — a guest who never reached the access point produces
 * nothing anywhere, whatever is configured behind it.
 */
export function authBlindSpotsFor(w: World, siteId: string): readonly AuthBlindSpot[] {
  const here = w.nas.filter((n) => n.siteId === siteId);
  const speaksRadius = here.some((n) => n.secretSet);
  if (speaksRadius) return AUTH_BLIND_SPOTS;
  /*
   * **`deviceCount` travels, because one kind is being asked to carry two
   * situations and it cannot say which without it.**
   *
   * The predicate is `no device here has a shared secret`, and that is true of a
   * site with **no devices at all** — which is `site-lab`, the only site that
   * reaches this branch. A console sentence for the kind that said *"This deployment
   * authorizes through a controller API and has no RADIUS accounting attached, so
   * this stream is empty by design"* would be a claim about an architecture, made
   * because a list was empty. Nothing establishes that anybody chose a controller
   * API, or indeed chose anything.
   *
   * The two are different afternoons. *No access point is registered here* is the
   * first-run state and the answer is to register one. *Devices are registered and
   * none has a RADIUS secret* is a deployment that either meant it or is the single
   * most common real fault in the product wearing a different coat — and the console
   * cannot tell those apart either, so its sentence says both rather than picking.
   *
   * The **kind** stays `no-radius-configured`, which is the honest one for both:
   * `AuthBlindSpot.kind` is a closed enum in the contract with no member for *no
   * devices*, and the count is what `params` is for — `before-retention` already
   * carries `retentionDays` there. Sending the count rather than a name for the case
   * keeps the sentence derived from the world instead of from a second opinion about
   * it, and a client that ignores `params` still gets a true kind.
   */
  return [
    { kind: "no-radius-configured", params: { deviceCount: here.length } },
    { kind: "never-associated" },
  ];
}

/** The named attempts, so the mock's clock and the tests can talk about them. */
export const ATT_IDS = {
  /** Six rejects in ninety seconds — the guest standing at the counter, right now. */
  repeatedVoucher: "att-repeat-1",
  /** Rejected, and **no MAC on the row**. Invisible to the search an operator will use. */
  noMac: "att-no-mac",
  /** A RADIUS reject. There is no reason and there never will be one. */
  radiusReject: "att-radius-reject",
  /** Everything Foyer owns said yes; the controller said no. */
  authorizeFailed: "att-authorize-failed",
  /** A MAC that claimed to be present and was not. `docs/rules/security.md` §4. */
  presenceUnverified: "att-presence-unverified",
  /** Accepted by RADIUS, and no session ever followed. */
  acceptedNoSession: "att-accepted-orphan",
  /** From a source address matching no registered client. `nasId` is null. */
  unknownClient: "att-unknown-client",
  /** Timestamped in the future. The clock-skew case, in a list sorted by time. */
  futureDated: "att-clock-skew",
} as const;

/**
 * The attempts.
 *
 * Every row is a failure mode someone has hit, in the register the rest of this file
 * uses. Three of them exist specifically to break a console that assumes the happy
 * shape of this data:
 *
 * - **`noMac`** has `callingStationId: null` in a deployment whose `macCoverage` is
 *   `recorded`. A console that renders coverage once at the top of the page and
 *   stops is wrong per row.
 * - **`unknownClient`** has `nasId: null`. The temptation is to drop a row that
 *   cannot be joined to a device; that row is the symptom of the single most common
 *   real fault, so dropping it deletes the answer.
 * - **`radiusReject`** carries no reason, because `radpostauth` has no reason
 *   column. A console that renders "rejected — reason unknown" as an error state has
 *   misread it: the reason is not missing, it was never observable.
 */
function buildAuthAttempts(
  now: number,
  rng: () => number,
  sessions: Session[],
  vouchers: Voucher[],
): AuthAttempt[] {
  const revoked = vouchers.find((v) => v.state === "revoked");
  const exhausted = vouchers.find((v) => v.state === "exhausted");
  const online = sessions.filter((s) => s.state === "online");

  const out: AuthAttempt[] = [];

  /* The guest at the counter: one code, mistyped, six times in ninety seconds. The
     list has to survive a burst without the operator losing the one row that
     differs — the sixth attempt is a *different* code, which is what tells you they
     were handed the wrong slip rather than fat-fingering one. */
  const burstMac = mac(rng, true);
  for (let i = 0; i < 6; i += 1) {
    out.push({
      id: i === 0 ? ATT_IDS.repeatedVoucher : `att-repeat-${i + 1}`,
      at: iso(now - 90_000 + i * 15_000),
      source: "portal",
      kind: i === 5 ? "voucher-expired" : "voucher-unknown",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: burstMac,
      calledStationId: "F4-92-BF-00-11-22:Foyer Guest",
      username: null,
      nasId: "nas-unifi-1",
      voucherId: null,
      accountId: null,
      params: { attempt: i + 1 },
    });
  }

  out.push(
    /* No MAC on the row, in a deployment that records MACs. A per-row absence, not a
       deployment-wide one — the two are different and the console must say which. */
    {
      id: ATT_IDS.noMac,
      at: iso(now - 8 * MIN),
      source: "portal",
      kind: "voucher-unknown",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: null,
      calledStationId: null,
      username: null,
      nasId: null,
      voucherId: null,
      accountId: null,
    },

    /* A RADIUS reject. `reply` said Access-Reject and that is the whole of what the
       row contains. Nothing downstream may invent a cause for it. */
    {
      id: ATT_IDS.radiusReject,
      at: iso(now - 22 * MIN),
      source: "radius",
      kind: "radius-reject",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: macWithOui(rng, pick(rng, REAL_OUIS)),
      calledStationId: "F4-92-BF-00-11-33:Foyer Corp",
      username: "may.in.kho",
      nasId: "nas-coa-1",
      voucherId: null,
      accountId: null,
    },

    /* The one that is invisible from every other screen: a valid voucher, a verified
       device, and a controller that refused. The guest is not online and nothing in
       sessions, vouchers or access points says why. */
    {
      id: ATT_IDS.authorizeFailed,
      at: iso(now - 3 * MIN),
      source: "driver",
      kind: "authorize-failed",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: macWithOui(rng, pick(rng, REAL_OUIS)),
      calledStationId: null,
      username: null,
      nasId: "nas-omada-1",
      voucherId: null,
      accountId: null,
      params: { status: 401 },
      detail: "The controller rejected the authorization call.",
    },

    /* `VerifyPresence` refused: the portal was called with a MAC that was not
       associated. This is an attack shape, not a fault, and it must not render in
       the same register as a mistyped voucher. */
    {
      id: ATT_IDS.presenceUnverified,
      at: iso(now - 41 * MIN),
      source: "driver",
      kind: "presence-unverified",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: "02:00:5e:10:00:01",
      calledStationId: null,
      username: null,
      nasId: "nas-unifi-1",
      voucherId: null,
      accountId: null,
    },

    /* Accepted, and no session followed. Accounting never started, or the device
       left immediately. An "accepted" row is not proof that anybody got online. */
    {
      id: ATT_IDS.acceptedNoSession,
      at: iso(now - 2 * HOUR),
      source: "radius",
      kind: "radius-accept",
      outcome: "accepted",
      siteId: "site-hcm",
      callingStationId: "6e:11:9a:03:cc:70",
      calledStationId: "F4-92-BF-00-11-22:Foyer Guest",
      username: "6e-11-9a-03-cc-70",
      nasId: "nas-unifi-2",
      voucherId: null,
      accountId: null,
    },

    /* A request from an address in no `clients.conf` entry. On a controller
       deployment this is what a newly adopted access point looks like. `nasId` is
       null and the row must survive it. */
    {
      id: ATT_IDS.unknownClient,
      at: iso(now - 6 * HOUR),
      source: "radius",
      kind: "radius-reject",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: null,
      calledStationId: null,
      username: "aa-bb-cc-dd-ee-ff",
      nasId: null,
      voucherId: null,
      accountId: null,
      params: { sourceAddress: "10.20.0.47" },
    },

    /* Ninety seconds in the future. A list sorted by time must not put this first
       and call it "just now"; the sessions screen already carries the same anomaly
       and the two should agree about how skew reads. */
    {
      id: ATT_IDS.futureDated,
      at: iso(now + 90_000),
      source: "portal",
      kind: "account-bad-password",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: macWithOui(rng, pick(rng, REAL_OUIS)),
      calledStationId: null,
      username: "nguyen.thi.mai",
      nasId: "nas-unifi-1",
      voucherId: null,
      accountId: "acc-1",
    },

    /* A disabled account still trying. *Disable* is the reversible action beside
       delete on the account screen; this is what that looks like from the guest's
       side, and it is a support call the operator can answer in one look. */
    {
      id: "att-account-disabled",
      at: iso(now - 3 * HOUR - 12 * MIN),
      source: "portal",
      kind: "account-disabled",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: macWithOui(rng, pick(rng, REAL_OUIS)),
      calledStationId: null,
      username: "le.hoang.long",
      nasId: "nas-unifi-1",
      voucherId: null,
      accountId: "acc-4",
    },

    /* Rate limited. Not a credential problem, and rendering it as one sends the
       operator hunting for a voucher that is fine. */
    {
      id: "att-rate-limited",
      at: iso(now - 4 * HOUR),
      source: "portal",
      kind: "rate-limited",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: burstMac,
      calledStationId: null,
      username: null,
      nasId: "nas-unifi-1",
      voucherId: null,
      accountId: null,
      params: { retryAfterSeconds: 300 },
    },

    /*
     * A username nothing matches.
     *
     * The pair below is the whole reason `account-unknown` and `account-bad-password`
     * are two kinds rather than one `account-rejected`: the operator's next move is
     * different. This one is *create the account, or check the spelling*; the other is
     * *reset the password*. A stream that collapsed them would send the operator to
     * the accounts screen looking for a row that was never there.
     *
     * `accountId` is null and must stay null — there is no account to point at, which
     * is the fact the row carries.
     */
    {
      id: "att-account-unknown",
      at: iso(now - 26 * MIN),
      source: "portal",
      kind: "account-unknown",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: macWithOui(rng, pick(rng, REAL_OUIS)),
      calledStationId: null,
      username: "nguyen.thi.mai@example.vn",
      nasId: "nas-unifi-2",
      voucherId: null,
      accountId: null,
    },
  );

  /*
   * A voucher with nothing left on it, tried again.
   *
   * Distinct from `voucher-expired` and the distinction decides what the operator
   * hands over: an expired code was never fully used and its *time* ran out, an
   * exhausted one was used up. Both read as "this code does not work" from the
   * counter, and the two answers are *wait for tomorrow's batch* and *here is a new
   * code*.
   *
   * Tied to a voucher that really is exhausted rather than to an invented id, so the
   * console's link from this row lands on a batch whose counts agree with it.
   */
  if (exhausted) {
    out.push({
      id: "att-voucher-exhausted",
      at: iso(now - 38 * MIN),
      source: "portal",
      kind: "voucher-exhausted",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: macWithOui(rng, pick(rng, REAL_OUIS)),
      calledStationId: "F4-92-BF-00-11-22:Foyer Guest",
      username: null,
      nasId: "nas-unifi-1",
      voucherId: exhausted.id,
      accountId: null,
    });
  }

  /* A voucher revoked mid-session, retried by the guest who was using it. Ties the
     stream to the voucher fixtures rather than inventing a parallel world. */
  if (revoked) {
    out.push({
      id: "att-voucher-revoked",
      at: iso(now - 55 * MIN),
      source: "portal",
      kind: "voucher-revoked",
      outcome: "rejected",
      siteId: "site-hcm",
      callingStationId: macWithOui(rng, pick(rng, REAL_OUIS)),
      calledStationId: null,
      username: null,
      nasId: "nas-unifi-1",
      voucherId: revoked.id,
      accountId: null,
    });
  }

  /* Successful grants for the sessions that exist, so the stream is not exclusively
     failure — an operator scanning it has to be able to see the normal shape to
     recognise the abnormal one. Spread backwards from each session's start.

     `siteId` comes off the session rather than being written as a literal: the row
     already borrows that session's MAC and its device, and a site that disagreed
     with both would be this file seeding the very defect the guards look for. */
  for (const [i, s] of online.slice(0, 5).entries()) {
    out.push({
      id: `att-grant-${i + 1}`,
      at: s.startedAt ?? iso(now - (i + 1) * 37 * MIN),
      source: "portal",
      kind: "grant-created",
      outcome: "accepted",
      siteId: s.siteId,
      callingStationId: s.callingStationId,
      calledStationId: null,
      username: null,
      nasId: s.nasId ?? null,
      voucherId: null,
      accountId: null,
    });
  }

  /*
   * ── the hotel's stream, and the row site scoping is about ───────────────────
   *
   * `listAuthAttempts` is where site scope is easiest to lose: `radpostauth`
   * carries no NAS address and no tenant, so a naive adapter answers a site-scoped
   * request with **every** site's usernames and MACs. A world in which every
   * attempt belonged to the same site could not tell a correct answer from that one.
   *
   * The middle row is the sharp one. It carries the address `site-hcm`'s two
   * duplicate sessions carry — the laptop that visits both venues — so a MAC search
   * at the café, which is the search an operator runs while a guest is standing in
   * front of them, must return the café's rows and not this one. What leaks
   * otherwise is another business's guest identifiers, and the leak is one query
   * parameter wide.
   */
  const crossSite = sessions.find((s) => s.id === SESSION_IDS.crossSiteMac);
  if (crossSite) {
    out.push(
      {
        id: "att-hanoi-grant",
        at: iso(now - 2 * HOUR),
        source: "portal",
        kind: "grant-created",
        outcome: "accepted",
        siteId: HANOI,
        callingStationId: "9e:0d:41:77:2c:b3",
        calledStationId: null,
        username: null,
        nasId: "nas-hanoi-1",
        voucherId: null,
        accountId: null,
      },
      {
        id: "att-hanoi-cross-site-mac",
        at: iso(now - 41 * MIN),
        source: "portal",
        kind: "voucher-unknown",
        outcome: "rejected",
        siteId: HANOI,
        callingStationId: crossSite.callingStationId,
        calledStationId: "8C-3B-AD-04-9E-11:HoGuom-Guest",
        username: null,
        nasId: "nas-hanoi-2",
        voucherId: null,
        accountId: null,
      },
      {
        /* A RADIUS accept at the hotel. `source: "radius"` is the one stream
           `radpostauth` really holds, so the hotel needs one or the site scope is
           exercised only over the two streams that live somewhere else. This row
           is what makes that asymmetry measurable. */
        id: "att-hanoi-radius",
        at: iso(now - 6 * HOUR),
        source: "radius",
        kind: "radius-accept",
        outcome: "accepted",
        siteId: HANOI,
        callingStationId: "9e:0d:41:55:aa:1c",
        calledStationId: "8C-3B-AD-04-9E-10:HoGuom-Staff",
        username: "le.thi.hoa",
        nasId: "nas-hanoi-1",
        voucherId: null,
        accountId: "acc-hanoi-2",
      },
    );
  }

  return out.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
}

// ── walled garden ────────────────────────────────────────────────────────────

/**
 * The garden for `site-hcm`, and it is a bad one on purpose.
 *
 * Two of the site's devices report `GardenKind: "none"` — the Omada and the
 * unracked Ruckus — so the site's **effective** garden is `none`: a guest behind
 * either of them reaches everything, and every entry below is decoration for those
 * two. That is the single most important thing this screen has to say, and it is
 * only visible because the fixture set contains a low-capability driver.
 *
 * The `required` entries are Foyer's own. `docs/rules/security.md` §5: without the
 * portal's FQDN, its OCSP/CRL endpoints and the RFC 8908 endpoint inside the garden
 * the portal cannot serve itself, and the failure reaches the guest as a network
 * that is simply broken rather than as a setting somebody got wrong.
 *
 * `garden-cdn-range` is the anti-pattern the rules name, present so the console has
 * to render the warning rather than being trusted to: an operator wanted to allow one
 * payment provider and allowed a CDN edge that hosts arbitrary customers. Its note is
 * written the way a real one would be — reasonable, and wrong.
 */
function buildGardenEntries(now: number): GardenEntry[] {
  const req = (
    siteId: string,
    id: string,
    kind: GardenEntry["kind"],
    value: string,
    note: string,
  ): GardenEntry => ({
    id: `${id}-${siteId}`,
    siteId,
    kind,
    value,
    note,
    origin: "required",
    createdAt: iso(now - 400 * DAY),
  });

  /*
   * **Every site gets these**, because the portal has to serve itself in every site
   * — a site missing them is a site whose guests meet a network that simply does not
   * work. Seeding them only for the site that also has operator entries would make
   * the other two look like a design where they are optional, and the console would
   * have been built against that.
   */
  const required = ["site-hcm", "site-hanoi", "site-lab"].flatMap((siteId) => [
    req(siteId, "garden-portal", "fqdn", "portal.foyer.local", "The portal itself."),
    req(siteId, "garden-ocsp", "fqdn", "ocsp.example-ca.net", "Certificate status for the portal's chain."),
    req(siteId, "garden-crl", "fqdn", "crl.example-ca.net", "Revocation list for the same chain."),
    req(siteId, "garden-capport", "fqdn", "capport.foyer.local", "The RFC 8908 endpoint."),
  ]);

  return [
    ...required,
    {
      id: "garden-cdn-range",
      siteId: "site-hcm",
      kind: "ip-range",
      value: "104.16.0.0/13",
      /* Deliberately plausible. The fixture is worthless if the note reads as a
         mistake — the point is that this is what a competent operator writes. */
      note: "Cổng thanh toán — added so guests can pay before signing in",
      origin: "operator",
      createdAt: iso(now - 61 * DAY),
    },
    {
      id: "garden-maps",
      siteId: "site-hcm",
      kind: "fqdn",
      value: "maps.example.com",
      note: "Venue map on the welcome page",
      origin: "operator",
      createdAt: iso(now - 12 * DAY),
    },
    {
      /* Added and never removed. No device in the site has done anything with it for
         a year, and nothing in the product has ever said so. */
      id: "garden-stale",
      siteId: "site-hcm",
      kind: "fqdn",
      value: "promo-2025.example.vn",
      note: "Tết campaign landing page",
      origin: "operator",
      createdAt: iso(now - 380 * DAY),
    },
  ];
}


// ── portal settings ──────────────────────────────────────────────────────────

/**
 * The editable portal record, one per site.
 *
 * **Seeded from the `cafe` venue profile on purpose**, so that a deployment nobody
 * has configured behaves exactly as the default venue profile does — which keeps
 * every venue-profile fixture and every terms end-to-end lock meaning what it
 * means. The profiles stay the portal's review mechanism; this is what an operator
 * edits, and the mock prefers a profile whenever one is explicitly
 * selected.
 *
 * The other two sites are deliberately **not** copies. `site-hanoi` is a hotel that
 * advertises one language and wrote no terms at all, which is the deployment that
 * falls all the way through the resolution order to Foyer's own catalogue default —
 * the branch a tidy fixture set would never exercise. `site-lab` has an empty venue
 * name, because a required string can still be empty and the screen that edits it
 * has to say so.
 */
/**
 * The café's page, as its owner would build it — and deliberately not tidy.
 *
 * A brand colour pale enough that the renderer has to darken it, a headline in
 * Vietnamese only (so an English reader sees the fallback marked `lang="vi"`), an
 * offer with a code, a price list, and links that exist only after sign-in. Two
 * blocks a guest must never see: one switched off, and one whose campaign ended.
 */
const CAFE_DESIGN: PortalSettings["design"] = {
  accent: "#c8873a",
  headline: { vi: "Chào mừng đến Cà phê Hoa Sữa" },
  blocks: [
    {
      id: "notice-hours",
      kind: "notice",
      placement: "welcome",
      visible: true,
      content: {
        vi: { title: "Wi-Fi miễn phí cho khách tại quán" },
        en: { title: "Free Wi-Fi for our guests" },
      },
    },
    {
      id: "offer-second-cup",
      kind: "offer",
      placement: "welcome",
      visible: true,
      code: "LY2GIAM20",
      content: {
        vi: { title: "Ly thứ hai giảm 20%", body: "Đưa mã này cho thu ngân.\nÁp dụng đến hết tháng." },
        en: { title: "20% off your second cup", body: "Show this code at the counter." },
      },
    },
    {
      id: "menu",
      kind: "list",
      placement: "welcome",
      visible: true,
      content: {
        vi: {
          title: "Món nổi bật",
          items: [
            { label: "Cà phê sữa đá", value: "29.000đ" },
            { label: "Bạc xỉu", value: "32.000đ" },
            { label: "Trà đào cam sả", value: "45.000đ" },
          ],
        },
      },
    },
    {
      id: "draft-offer",
      kind: "offer",
      placement: "welcome",
      visible: false,
      content: { vi: { title: "Bản nháp — chưa công bố" } },
    },
    {
      id: "tet-campaign",
      kind: "text",
      placement: "welcome",
      visible: true,
      startsOn: "2026-01-20",
      endsOn: "2026-02-10",
      content: { vi: { title: "Chúc mừng năm mới", body: "Quán nghỉ Tết từ 28 đến mùng 4." } },
    },
    {
      id: "review",
      kind: "button",
      placement: "connected",
      visible: true,
      url: "https://g.page/r/example-review",
      content: {
        vi: { label: "Đánh giá 5★ cho quán" },
        en: { label: "Leave us a review" },
      },
    },
    {
      id: "follow",
      kind: "social",
      placement: "connected",
      visible: true,
      links: [
        { network: "facebook", url: "https://facebook.com/example.cafe" },
        { network: "zalo", url: "https://zalo.me/0900000000" },
        { network: "phone", url: "tel:+84900000000" },
      ],
      content: { vi: { title: "Theo dõi quán" }, en: { title: "Follow us" } },
    },
  ],
};

function buildPortalSettings(sites: Site[]): PortalSettings[] {
  const budgets = {
    logoMaxBytes: LOGO_MAX_BYTES,
    imageMaxBytes: IMAGE_MAX_BYTES,
    codeBudgetBytes: CODE_BUDGET_BYTES,
  };
  const cafe = defaultVenue().context;

  const perSite: Record<string, Omit<PortalSettings, "siteId" | "budgets">> = {
    "site-hcm": {
      venueName: cafe.venueName,
      networkName: cafe.networkName ?? null,
      logoDataUri: null,
      heroImage: null,
      languages: [...cafe.languages],
      /*
       * The café offers all three, including click-through — the mode most small
       * Vietnamese venues actually want.
       *
       * On by default in the fixture rather than behind a switch someone has to
       * find: a branch nobody can see is a branch nobody reviews, which is the whole
       * reason the venue profiles exist. It is still a *setting*, and
       * the admin's portal screen turns it off.
       */
      methods: [...cafe.methods, "click-through"],
      clickThroughMinutes: 30,
      terms: { summary: { ...(cafe.terms.summary ?? {}) } },
      design: CAFE_DESIGN,
    },
    "site-hanoi": {
      venueName: "Khách sạn Hồ Gươm",
      networkName: "HoGuom-Guest",
      logoDataUri: null,
      heroImage: null,
      languages: ["vi"],
      // No terms in any language. The portal falls through to its own catalogue
      // default, and the editor has to say that is what will happen.
      //
      // And it offers click-through, so the mode is reachable from the running
      // product — a branch nobody can see is a branch nobody reviews, which is the
      // lesson the venue profiles were built out of.
      methods: ["voucher", "account", "click-through"],
      clickThroughMinutes: 45,
      terms: { summary: {} },
      // Never designed. The console's empty state — templates first — is reviewed
      // against this venue.
      design: { accent: null, headline: {}, blocks: [] },
    },
    "site-lab": {
      venueName: "",
      networkName: null,
      logoDataUri: null,
      heroImage: null,
      languages: ["en"],
      // One method. The portal renders no chooser, the same way a single-language
      // venue renders no switcher.
      methods: ["voucher"],
      clickThroughMinutes: null,
      terms: { summary: { en: "Lab network. Nothing here is a production service." } },
      design: { accent: null, headline: {}, blocks: [] },
    },
  };

  return sites.map((site) => ({
    siteId: site.id,
    budgets,
    ...(perSite[site.id] ?? {
      venueName: site.name,
      networkName: null,
      logoDataUri: null,
      heroImage: null,
      languages: ["en"],
      methods: ["voucher"],
      clickThroughMinutes: null,
      terms: { summary: {} },
      design: { accent: null, headline: {}, blocks: [] },
    }),
  }));
}

// ── portal venues ────────────────────────────────────────────────────────────

/**
 * The venues the portal can be reviewed against.
 *
 * Everything the portal decides on the welcome page — whether a language switcher
 * exists at all, which language the terms come out in, whether the venue's terms or
 * the catalogue's are on screen — is a function of one object the venue supplies.
 * With a single hardcoded venue, three of the four branches of that decision would
 * be unreachable from the running product: they would have unit coverage and no way
 * for a person, or an end-to-end test, to *see* them.
 *
 * So the profiles below are not variations for visual interest. Each one is a
 * branch of the terms-resolution order in the `PortalContext.terms` description, or
 * of the switcher rule, that nothing else reaches:
 *
 * | Profile | Advertises | Venue terms | The branch it makes visible |
 * |---|---|---|---|
 * | `cafe` | en, vi | en, vi | Rule 1 — the venue's own terms, in the language being read. **The default.** |
 * | `solo` | vi | vi | **No switcher at all** on `/vi/` — not a disabled control, none. |
 * | `foreign-terms` | en, vi | vi | Rule 2 — Vietnamese terms on an English page, carrying `lang="vi"`. |
 * | `no-terms` | en, vi | — | Rule 3 — the portal's catalogue default, for a venue that configured none. |
 *
 * **`cafe` stays the default** because it is what every other test, screenshot and
 * bundle measurement is taken against; a review surface that moves under everyone
 * is worse than one branch being harder to reach.
 *
 * How to select one is in `web/mock/src/server.ts` — `?venue=` on the API, a
 * `foyer_mock_venue` cookie for a rendered page, and `/venues` for a person with
 * a mouse.
 */
export interface Venue {
  id: string;
  /** Shown on the mock's chooser page. */
  label: string;
  /** What this profile makes reachable that the default does not. */
  proves: string;
  context: PortalContext;
}

/**
 * The consent text is deliberately *not* a copy of the portal's catalogue default,
 * so anyone can tell at a glance which of the two is on screen.
 *
 * **No venue's terms may contain the billing-and-consent-boundary sentence.** That
 * sentence is Foyer's, it renders unconditionally beneath whatever terms resolve,
 * and these fixtures exist to prove it survives a venue that wrote its own. A venue
 * paragraph ending with a near-verbatim copy of it makes the welcome page show the
 * same claim twice, stacked — so instead of demonstrating that Foyer's line
 * survives, the fixture would demonstrate duplication and push the real line further
 * below the fold. A venue that repeats it is not the case worth reviewing; a venue
 * that never mentions it is.
 */
export const VENUES: readonly Venue[] = [
  {
    id: "cafe",
    label: "Cà phê Hoa Sữa — bilingual café",
    proves: "The realistic default: a venue with foreign guests, terms in both languages.",
    context: {
      venueName: "Cà phê Hoa Sữa",
      networkName: "Foyer Guest",
      logoDataUri: null,
      methods: ["voucher", "account"],
      languages: ["en", "vi"],
      terms: {
        summary: {
          en: "Internet access at Cà phê Hoa Sữa is provided as is, to customers, during opening hours. We log when you connect and for how long, as required by law.",
          vi: "Truy cập Internet tại Cà phê Hoa Sữa được cung cấp nguyên trạng, dành cho khách hàng trong giờ mở cửa. Chúng tôi ghi lại thời điểm và thời lượng kết nối của bạn theo quy định pháp luật.",
        },
      },
    },
  },
  {
    id: "solo",
    label: "Quán Cơm Tấm Bà Sáu — Vietnamese only",
    proves:
      "A venue advertising one language renders no switcher at all on /vi/. " +
      "The neighbourhood shop, which is most Vietnamese-language venues.",
    context: {
      venueName: "Quán Cơm Tấm Bà Sáu",
      networkName: "ComTam_Free",
      logoDataUri: null,
      methods: ["voucher", "account"],
      // One language. The portal must render *no control*, not a disabled one —
      // the same rule the admin follows for a capability a driver does not have.
      languages: ["vi"],
      terms: {
        summary: {
          vi: "Wi-Fi miễn phí cho khách ăn tại quán. Mỗi mã dùng cho một thiết bị. Quán ghi lại thời điểm và thời lượng kết nối theo quy định.",
        },
      },
    },
  },
  {
    id: "foreign-terms",
    label: "Khách sạn Sen Vàng — terms in Vietnamese only",
    proves:
      "Terms the guest cannot read, rendered anyway with lang=\"vi\" — never translated, " +
      "never dropped. The bilingual venue whose lawyer wrote in one language.",
    context: {
      venueName: "Khách sạn Sen Vàng Sài Gòn",
      networkName: "SenVang-Guest",
      logoDataUri: null,
      methods: ["voucher", "account"],
      languages: ["en", "vi"],
      terms: {
        // No `en`. An English reader gets this, tagged as Vietnamese: showing terms
        // in the wrong language is bad, showing none is worse, and the `lang`
        // attribute is what lets a screen reader and a translation offer do the
        // right thing with text the portal itself must leave alone.
        summary: {
          vi: "Khách lưu trú được dùng Internet miễn phí trong thời gian lưu trú. Mỗi phòng được cấp một mã. Khách sạn ghi nhật ký truy cập theo quy định của pháp luật Việt Nam và không chịu trách nhiệm về nội dung khách truy cập.",
        },
      },
    },
  },
  {
    id: "no-terms",
    label: "Bún Chả Hương Liên — no terms configured",
    proves:
      "The deployment that never configured terms still shows terms: the portal's " +
      "catalogue default, which is what keeps the consent sentence on screen.",
    context: {
      venueName: "Bún Chả Hương Liên",
      networkName: "BunCha Wifi",
      logoDataUri: null,
      methods: ["voucher", "account"],
      languages: ["en", "vi"],
      // Empty, not absent — both mean "the venue configured none", and the empty
      // map is the one a half-configured deployment actually produces.
      terms: { summary: {} },
    },
  },
];

export const DEFAULT_VENUE_ID = "cafe";

export function venueById(id: string | undefined | null): Venue | undefined {
  return VENUES.find((v) => v.id === id);
}

export function defaultVenue(): Venue {
  return venueById(DEFAULT_VENUE_ID)!;
}

// ── alarms ───────────────────────────────────────────────────────────────────

/**
 * Derived from the world rather than authored, so an alarm cannot drift out of
 * agreement with the row it is about.
 *
 * Levels are keyed on what the operator must do, not on how bad it sounds.
 */
/**
 * The alarms, as kinds rather than sentences.
 *
 * Authored `title` and `detail` text here would go straight onto the dashboard in
 * whatever language it was written in, whatever the console's language — the first
 * screen anyone opens. The fallback fields are deliberately **not** populated here: a server that always sends text is a server
 * whose clients never have to implement the kind, and the fallback then becomes the
 * real path. It exists for a kind a client does not recognise, and every kind here
 * is one the console recognises.
 */
export function deriveAlarms(w: World, siteId: string): Alarm[] {
  const alarms: Alarm[] = [];
  const now = Date.now();

  for (const n of w.nas.filter((x) => x.siteId === siteId)) {
    if (n.reachability === "unreachable") {
      alarms.push({
        id: `alarm-nas-unreachable-${n.id}`,
        level: "urgent",
        kind: "nas-unreachable",
        subject: { kind: "nas", id: n.id, name: n.name },
        raisedAt: n.lastSeenAt ?? iso(now),
      });
    }
    if (n.reachability === "credentials-expired") {
      alarms.push({
        id: `alarm-nas-creds-${n.id}`,
        level: "urgent",
        kind: "nas-credentials-expired",
        subject: { kind: "nas", id: n.id, name: n.name },
        raisedAt: n.lastSeenAt ?? iso(now),
      });
    }
    if (n.messageAuthenticator === "refused") {
      alarms.push({
        id: `alarm-nas-blast-${n.id}`,
        level: "warning",
        kind: "nas-message-authenticator-refused",
        subject: { kind: "nas", id: n.id, name: n.name },
        raisedAt: iso(now - 3 * DAY),
      });
    }
    if (n.capabilities.walledGarden === "none" && n.enabled) {
      alarms.push({
        id: `alarm-nas-garden-${n.id}`,
        level: "warning",
        kind: "nas-no-walled-garden",
        subject: { kind: "nas", id: n.id, name: n.name },
        raisedAt: iso(now - 12 * HOUR),
      });
    }
    /*
     * **`=== false`, and the authority is the four predicates above it.**
     *
     * Every other alarm in this loop compares against a value — `=== "unreachable"`,
     * `=== "credentials-expired"`, `=== "refused"`, `=== "none"`. This one must not
     * test a three-valued field for its truthiness, and `messageAuthenticator ===
     * "refused"` four lines up is the same field shape answered correctly: `unknown`
     * raises nothing, because *nothing has established this* is not a fault.
     *
     * `createNas` leaves `accountingConfigured` absent on every device it registers,
     * so absence is a real third state. `!undefined` is `true`, so a truthiness test
     * would raise **"No accounting profile attached — no session records will arrive
     * from this device"** about a radio nobody has spoken to, in the future tense, on
     * the dashboard. Widening a value exposes every two-valued reader of it; this is
     * one.
     *
     * **A `params` discriminator is wrong here**, which is worth saying because it is
     * right for `AuthBlindSpot`. There, `no-radius-configured` is *true* of both
     * situations it carries, so a count in `params` narrows a kind that is already
     * honest. `nas-no-accounting`
     * is **false** of an unmeasured device, and the `kind` is what the console keys
     * its copy on — `Alarm.title` is an explicit fallback a client with copy for the
     * kind must ignore. `params` cannot negate a kind's own sentence, and a kind
     * that means its opposite half the time is worse than the silence.
     *
     * So: no alarm, and no new kind for it either. `AlarmKind` is closed and has no
     * *never probed* member; the state is already rendered where the value is —
     * `AccountingPill`'s third branch on the list, the findings panel's *not
     * measured* row — and an alarm nobody can clear except by pressing Probe on
     * every freshly registered device is a dashboard people learn to scroll past.
     */
    if (n.accountingConfigured === false && n.secretSet) {
      alarms.push({
        id: `alarm-nas-acct-${n.id}`,
        level: "caution-minor",
        kind: "nas-no-accounting",
        subject: { kind: "nas", id: n.id, name: n.name },
        raisedAt: iso(now - 5 * DAY),
      });
    }
  }

  /*
   * Controllers.
   *
   * The scope predicate is **the same one `GET /controllers` filters on**, and it is
   * literally the same function rather than a second copy of the sentence —
   * `controllerServesSite`, above, which says why. An alarm the operator cannot find
   * a row for on the controllers screen is an alarm they cannot act on, so the two
   * must give one answer, right or wrong, rather than a second opinion. A
   * transcription of the rule is how they come to give two.
   */
  for (const c of w.controllers.filter((x) => controllerServesSite(x, siteId))) {
    /*
     * `=== false`, never falsy. `null` is *not established* — the never-probed
     * controller — and treating it as unsupported sends an operator to upgrade
     * software nothing has looked at, which is the mistake the probe path already
     * refuses to make in the other direction.
     */
    if (c.versionSupported === false) {
      alarms.push({
        id: `alarm-ctl-version-${c.id}`,
        /*
         * `urgent`, at the same level as an unreachable NAS, and the level set is
         * keyed on what the operator must do rather than on how bad it sounds.
         * On a controller-API vendor the controller *is* the authorization path —
         * UniFi's captive-portal authorization has no RADIUS equivalent — so a
         * driver that cannot drive this version means no guest is let on under any
         * access point beneath it, while RADIUS keeps answering and every other
         * signal on this dashboard stays green. It is also the level the two
         * surfaces that already draw this state chose: the version pill and the
         * probe notice.
         */
        level: "urgent",
        kind: "controller-version-unsupported",
        /*
         * Everything the sentence needs, because only `params` reaches the copy
         * catalogue — the subject is rendered separately. `minimumVersion` is the
         * field that turns a verdict into an instruction; without it the operator is
         * told their controller is unsupported and has to go and find out what
         * would not be.
         */
        params: {
          controllerName: c.name,
          vendor: c.vendor,
          version: c.version,
          minimumVersion: CONTROLLER_MINIMUM_VERSION[c.vendor],
        },
        subject: { kind: "controller", id: c.id, name: c.name },
        // The last time anything reached it, which is when the version was read.
        raisedAt: c.lastContactAt ?? iso(now),
      });
    }
  }

  const orphaned = w.sessions.filter((s) => s.siteId === siteId && s.state === "orphaned");
  if (orphaned.length > 0) {
    alarms.push({
      id: "alarm-orphaned",
      level: "caution-minor",
      kind: "sessions-orphaned",
      params: { count: orphaned.length },
      raisedAt: orphaned[0]!.lastUpdateAt ?? iso(now),
    });
  }

  // Against the current clock, not the state baked in at build time. The world
  // moves; an alarm that reads a stale field is itself stale.
  const stale = w.sessions.filter(
    (s) =>
      s.siteId === siteId && s.state === "online" && freshnessStateAt(s.freshness, now) === "stale",
  );
  if (stale.length > 0) {
    // The oldest sample behind the alarm, so "raised at" is a fact rather than a
    // decoration. Reporting the freshest would flatter it.
    const oldest = stale
      .map((s) => (s.freshness.sampledAt ? Date.parse(s.freshness.sampledAt) : now))
      .sort((a, b) => a - b)[0]!;
    alarms.push({
      id: "alarm-stale",
      level: "caution-minor",
      kind: "sessions-stale",
      params: { count: stale.length },
      raisedAt: iso(oldest),
    });
  }

  const order: Record<Alarm["level"], number> = {
    urgent: 0,
    warning: 1,
    "caution-major": 2,
    "caution-minor": 3,
    negative: 4,
    info: 5,
  };
  return alarms.sort((a, b) => order[a.level] - order[b.level]);
}
