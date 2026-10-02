/**
 * What the contract check is actually able to catch.
 *
 * JSON Schema permits unknown properties by default, so a field removed from
 * `openapi.yaml` that the mock keeps sending — `terms.url`, for instance — leaves
 * every test green unless the validator rejects undeclared properties. A validator
 * that sees a *missing* required field and is blind to an *extra* one is the wrong
 * way round for a project whose whole premise is that the Go backend implements this
 * contract rather than negotiating a new one.
 *
 * So the first block below is the one that matters: every case is a response that
 * must be rejected. `docs/rules/testing.md` — "a test that has never failed is
 * not necessarily proving anything". A validator that has never rejected anything
 * is a validator nobody has tested.
 */
import { describe, expect, it } from "vitest";
import {
  CONTROLLER_MINIMUM_VERSION,
  PLANS,
  UNVERIFIED_CAPABILITIES,
  VENUES,
  buildWorld,
  defaultVenue,
  deriveAlarms,
  macIsLocallyAdministered,
  macIsMulticast,
  versionMeetsMinimum,
} from "@foyer/fixtures";
import { IMAGE_MAX_BYTES } from "@foyer/contract";
import {
  ContractViolation,
  conform,
  conformResponse,
  contractDocument,
  responseKeys,
  schemaNames,
} from "./validate.js";

/** A `PortalContext` that is exactly what the contract declares, and nothing more. */
const context = () => structuredClone(defaultVenue().context) as Record<string, unknown>;

/** A minimal page envelope for `listNas`, the `allOf` case. */
const nasPage = () => ({
  nextCursor: null,
  items: [
    {
      id: "nas-1",
      siteId: "site-hcm",
      /* Discovered from a controller, which is how every UniFi device arrives. Both
         fields are required, so a fixture missing them
         is a fixture the schema rejects, which is what this file is for. */
      source: "discovered",
      radiusVia: "self",
      name: "AP",
      driver: "unifi",
      capabilities: {
        liveReauthorize: false,
        walledGarden: "none",
        hardQuota: false,
        bandwidthLimits: false,
        dynamicVlan: false,
        rotateSecret: false,
        canDeauthGuest: false,
      },
      reachability: "ok",
      enabled: true,
      secretSet: true,
      messageAuthenticator: "enforced",
    },
  ],
});

function violation(fn: () => unknown): ContractViolation {
  try {
    fn();
  } catch (err) {
    if (err instanceof ContractViolation) return err;
    throw err;
  }
  throw new Error("expected a ContractViolation and the value was accepted");
}

describe("a property the contract does not declare is rejected", () => {
  /**
   * The case this whole file is about, constructed exactly.
   *
   * `terms.url` is not a member of `PortalContext.terms`; a mock sending `url: {}`
   * inside `terms` must fail. If this
   * test ever passes for the wrong reason — because `url` came back into the schema —
   * read the paragraph in the `terms` description that says what evidence would
   * reopen that decision. It is not "somebody missed the field".
   */
  it("rejects an undeclared terms.url", () => {
    const drifted = context();
    (drifted.terms as Record<string, unknown>).url = {};

    const err = violation(() => conformResponse("getPortalContext", 200, drifted));
    expect(err.detail).toContain("/terms/url");
    expect(err.detail).toContain("not declared by the contract");
  });

  it("catches a typo beside the real property", () => {
    const drifted = context();
    drifted.venueNmae = "Cà phê Hoa Sữa";
    expect(violation(() => conformResponse("getPortalContext", 200, drifted)).detail).toContain(
      "/venueNmae",
    );
  });

  it("catches one nested inside an array element", () => {
    const page = nasPage();
    (page.items[0] as Record<string, unknown>).sharedSecret = "hunter2";
    const err = violation(() => conformResponse("listNas", 200, page));
    expect(err.detail).toContain("/items/0/sharedSecret");
  });

  it("catches one nested two objects deep", () => {
    const page = nasPage();
    (page.items[0]!.capabilities as Record<string, unknown>).macFiltering = true;
    expect(violation(() => conformResponse("listNas", 200, page)).detail).toContain(
      "/items/0/capabilities/macFiltering",
    );
  });

  /* The envelope itself, which nothing checked at all before: `items` and
     `nextCursor` are declared across two `allOf` branches, and a third key is not. */
  it("catches one on a paginated envelope", () => {
    expect(violation(() => conformResponse("listNas", 200, { ...nasPage(), total: 812 })).detail)
      .toContain("/total");
  });

  it("still catches a missing required property", () => {
    const drifted = context();
    delete drifted.languages;
    expect(violation(() => conformResponse("getPortalContext", 200, drifted)).detail).toContain(
      "languages",
    );
  });

  it("refuses to validate a response the contract does not declare", () => {
    expect(violation(() => conformResponse("getPortalContext", 418, {})).detail).toContain(
      "declares no JSON response",
    );
  });
});

describe("and it is not so strict that it is useless", () => {
  it("accepts every venue profile the mock can serve", () => {
    for (const venue of VENUES) {
      expect(() => conformResponse("getPortalContext", 200, venue.context)).not.toThrow();
    }
  });

  it("accepts a page whose properties come from both allOf branches", () => {
    // The reason `additionalProperties: false` is not in the document: on
    // `allOf: [Page, {items}]` each branch sees the whole object, so the keyword
    // would make `Page` reject `items` and the `items` branch reject `nextCursor`.
    expect(() => conformResponse("listNas", 200, nasPage())).not.toThrow();
  });

  it("accepts arbitrary keys in a map the contract declares as a map", () => {
    // `terms.summary` is `additionalProperties: {type: string}` — language code to
    // text. Keys there are data, not schema drift. Same for `Problem.errors`.
    const venue = context();
    (venue.terms as { summary: Record<string, string> }).summary.fr = "Accès Internet fourni tel quel.";
    expect(() => conformResponse("getPortalContext", 200, venue)).not.toThrow();

    /*
     * `type` is required now and is a closed enum, so this body carries one. The
     * *values* under `errors` are still deliberately arbitrary — and deliberately
     * Vietnamese, because the schema's job is to accept any string there. Whether
     * the server should ever compose one is a different question with a different
     * guard: "the server does not talk to the operator in its own language" in
     * `server.test.ts`. A schema that policed language would be a schema forbidding
     * the Vietnamese-language deployment this product is first shipping into.
     */
    expect(() =>
      conform("Problem", {
        type: "/problems/validation",
        title: "Không tạo được tài khoản",
        status: 422,
        errors: { username: "Bắt buộc.", password: "Bắt buộc." },
      }),
    ).not.toThrow();
  });

  it("says nothing about a value the contract declares as opaque", () => {
    // A schema with neither `properties` nor `additionalProperties` declares
    // nothing to compare against; calling its contents drift would be noise.
    expect(() =>
      conform("Session", {
        id: "s1",
        siteId: "site-hcm",
        nasId: "nas-1",
        state: "unknown",
        callingStationId: "02:11:22:33:44:55",
        startedAt: new Date().toISOString(),
        freshness: { state: "absent" },
        // Required, even on the minimum session the contract will accept. A server
        // that cannot say whether a byte figure has wrapped cannot be trusted with
        // the byte figure — see `Session.countersWrapped`.
        countersWrapped: false,
      }),
    ).not.toThrow();
  });
});

/**
 * The two shortcuts this file's design rests on, asserted rather than assumed.
 */
describe("the document still supports how the mock validates against it", () => {
  it("declares Problem as the only error-response schema", () => {
    // `notFound()` and the 422 handlers are shared across operations, so they
    // validate against the `Problem` component rather than against an operation.
    // That is only equivalent while every declared 4xx/5xx body *is* `Problem`.
    const spec = contractDocument();

    /* Most error responses are `$ref`s into `components/responses`, so the check has
       to follow one hop before it can see the schema. */
    const resolve = (node: unknown): Record<string, unknown> | undefined => {
      if (!node || typeof node !== "object") return undefined;
      const ref = (node as Record<string, unknown>).$ref;
      if (typeof ref !== "string") return node as Record<string, unknown>;
      const name = ref.replace("#/components/responses/", "");
      return resolve((spec.components.responses ?? {})[name]);
    };

    const offenders: string[] = [];
    for (const [path, item] of Object.entries(spec.paths)) {
      for (const [method, op] of Object.entries(item)) {
        const responses = (op as { responses?: Record<string, unknown> })?.responses;
        for (const [status, response] of Object.entries(responses ?? {})) {
          if (Number(status) < 400) continue;
          const content = resolve(response)?.content as
            | Record<string, { schema?: { $ref?: string } }>
            | undefined;
          const schemas = Object.values(content ?? {}).map((c) => c.schema?.$ref);
          if (!schemas.length) continue; // a declared error with no body is fine
          if (schemas.some((ref) => ref !== "#/components/schemas/Problem")) {
            offenders.push(`${method.toUpperCase()} ${path} ${status}`);
          }
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("names a response schema for every operation the mock answers with a body", () => {
    // Guards the lookup itself: a response schema that stops being reachable by
    // `operationId:status` would make `conformResponse` throw at runtime instead of
    // validating, which is loud but late.
    expect(responseKeys()).toContain("getPortalContext:200");
    expect(responseKeys()).toContain("listSessions:200");
    expect(responseKeys()).toContain("createNas:201");
    expect(responseKeys()).toContain("rotateNasSecret:200");
    expect(responseKeys()).toContain("revokeVoucher:200");
  });

  it("has no orphan in the components map", () => {
    // Every named schema is either referenced somewhere or is a top-level response
    // shape. An unreferenced one is usually the residue of a removed feature.
    const spec = contractDocument();
    const document = JSON.stringify(spec);
    const unreferenced = schemaNames().filter(
      (name) => !document.includes(`#/components/schemas/${name}"`),
    );
    expect(unreferenced).toEqual([]);
  });

  /*
   * The inverse of the orphan guard, and the half nothing was checking.
   *
   * The guard above catches a schema nothing references. A `$ref` to a schema that
   * does not exist is caught earlier still and by three separate things — Ajv here,
   * `kin-openapi`, and `oapi-codegen`, which refuses to emit Go for an unresolvable
   * reference. So a **dangling `$ref` cannot reach a commit** in this repository.
   *
   * A broken reference can still hide in prose. `ControllerVendor`'s description ended
   * *"See `DriverShape`"* — a cross-reference in **prose**, which every resolver in
   * the chain reads as an opaque string. Nothing tolerated a broken reference,
   * because at no layer was there a reference to break; the sentence was simply
   * copied into `src/generated/types.ts` and into `internal/api/gen/server.gen.go`,
   * so one unresolvable name became three, each further from anything that could
   * answer it.
   *
   * The rule is narrow on purpose. Descriptions in this document are full of
   * backticked names — `radacct`, `rlm_sql`, `Nas.accountingConfigured` — and almost
   * none of them are schema references. *"See `Name`"* with a bare CamelCase word is
   * the one shape that reads as one, and there are eight of them: seven resolve, and
   * the eighth is what this exists for.
   *
   * It goes red on a description that names a schema with nothing beside it — and
   * equally on a comment that quotes such a sentence verbatim while explaining it,
   * because nothing about the guard knows which occurrence is the defect.
   */
  it("resolves every prose cross-reference that reads as a schema reference", () => {
    const named = new Set(schemaNames());
    /* Every `description` in the document, wherever it is nested. Walking the parsed
       value rather than the file, because a reference is a reference whether it sits
       on a schema, a parameter, an operation or a response. */
    const prose: string[] = [];
    const walk = (node: unknown) => {
      if (Array.isArray(node)) return node.forEach(walk);
      if (!node || typeof node !== "object") return;
      for (const [key, value] of Object.entries(node)) {
        if (key === "description" && typeof value === "string") prose.push(value);
        else walk(value);
      }
    };
    walk(contractDocument());

    const dangling = prose
      .flatMap((text) => [...text.matchAll(/[Ss]ee\s+`([A-Z][A-Za-z0-9]*)`/g)])
      .map((m) => m[1]!)
      .filter((name) => !named.has(name));
    /* Non-empty, or the walk found nothing and this test proves nothing about
       anything. Seven of the eight resolve; a document with none has stopped being
       the document this reads. */
    expect(prose.length).toBeGreaterThan(100);
    expect([...new Set(dangling)]).toEqual([]);
  });
});

describe("the open-redirect allowlist is a schema constraint, not a paragraph", () => {
  /*
   * `PortalStatus.originalUrl` was `type: [string, "null"]` with the allowlist
   * described in prose, so runtime contract validation could not catch a violation
   * at all — and the portal renders the value straight into an `href`. In a codebase
   * whose stated principle is that rules are enforced mechanically rather than by
   * discipline, the open-redirect defence was the one that was not.
   */
  const status = (originalUrl: string | null) => ({
    state: "connected" as const,
    plan: { name: "T", durationMinutes: 60, enforcedBy: "nas" as const },
    venueName: "V",
    grantedAt: new Date().toISOString(),
    expiresAt: null,
    originalUrl,
  });

  it("rejects every shape the prose only asked for", () => {
    for (const hostile of [
      "javascript:alert(1)",
      "data:text/html,<script>alert(1)</script>",
      // Protocol-relative: the browser reads this as another origin.
      "//evil.example/",
      // Userinfo: the visible prefix is the part a guest reads as the destination.
      "http://portal.example@evil.example/",
      "https://user:pw@evil.example/",
      "ftp://evil.example/",
      "not a url",
    ]) {
      expect(
        violation(() => conformResponse("getPortalStatus", 200, status(hostile))).detail,
        hostile,
      ).toContain("originalUrl");
    }
  });

  it("still accepts the ordinary case, including one with a query and a fragment", () => {
    for (const fine of [
      "http://vnexpress.net/",
      "https://vnexpress.net/tin-tuc?page=2#top",
      "https://example.test:8443/a/b",
      null,
    ]) {
      expect(() => conformResponse("getPortalStatus", 200, status(fine)), String(fine)).not.toThrow();
    }
  });
});

describe("the fixtures prove what they were written to prove", () => {
  it("lets no venue restate Foyer's consent sentence in its own terms", () => {
    /*
     * The `cafe` profile exists to show that Foyer's billing-and-consent-boundary
     * line survives a venue that wrote its own terms. Both of its paragraphs ended
     * with a near-verbatim copy of that line, so the welcome page showed the same
     * claim twice, stacked — the fixture demonstrated duplication instead, and the
     * duplicate pushed the real line further below the fold of a window that cannot
     * be resized. A venue that repeats it is not the case worth reviewing.
     */
    for (const venue of VENUES) {
      for (const [lang, text] of Object.entries(venue.context.terms?.summary ?? {})) {
        const where = `${venue.id}.${lang}`;
        expect(text, where).not.toMatch(/billing and consent boundary/i);
        expect(text, where).not.toMatch(/ranh giới thanh toán và đồng ý/i);
        expect(text, where).not.toMatch(/not a security boundary/i);
        expect(text, where).not.toMatch(/không phải ranh giới bảo mật/i);
      }
    }
  });

  it("says what language a venue wrote its plan names in", () => {
    // Every fixture plan name is Vietnamese, because a café in District 1 writes its
    // price list in Vietnamese. The English portal renders them verbatim, so the
    // only honest thing it can do is mark them — and it can only do that if the
    // venue said. See `Plan.nameLang`.
    for (const [key, plan] of Object.entries(PLANS)) {
      expect(plan.nameLang, `${key} does not say what language its name is in`).toBe("vi");
    }
  });

  /*
   * `Controllers.tsx` draws an *urgent* pill on `versionSupported === false`, and a
   * world of three controllers at `true`, `true`, `null`, with both probe paths
   * producing `true` or `null`, would never render it. The fixture below is that
   * case, and these two guards are what stop it going quiet.
   */
  it("carries a controller whose version is below the floor, otherwise healthy", () => {
    const world = buildWorld();
    const unsupported = world.controllers.filter((c) => c.versionSupported === false);
    expect(
      unsupported.length,
      "no controller fixture is `versionSupported: false`, so the urgent version pill, " +
        "the probe notice and `controller-version-unsupported` are all unreachable again",
    ).toBeGreaterThan(0);

    for (const c of unsupported) {
      /* Otherwise healthy is the whole point: an unreachable controller would raise
         `nas`-style alarms of its own and the operator would already be looking. The
         case worth reviewing is the one where every other signal is green. */
      expect(c.reachability, c.id).toBe("ok");
      expect(c.credentialSet, c.id).toBe(true);
      expect(c.version, `${c.id} is unsupported with no version, which is \`null\`'s job`).toBeTruthy();
      expect(c.lastContactAt, `${c.id} has never been contacted`).toBeTruthy();
    }
  });

  /*
   * The same shape one screen over.
   *
   * `revokeVoucher` answers `/problems/session-end-failed` when the voucher is
   * revoked and the driver call that would end the guest's session cannot be made.
   * If every voucher with a live session sat on a device whose `reachability` is
   * `ok`, the branch would exist and **nothing in the fixture world would reach it**:
   * seeing that copy would mean driving a guest through the portal onto the
   * unreachable access point first, which is not something a viewer opening
   * `/vouchers` can do.
   *
   * The guard is on the *relationship* rather than on an id, because an id is
   * satisfied by a row that still exists and no longer carries the case — the row
   * could keep its name and move to a healthy device, or lose its voucher, or have
   * its voucher revoked in the fixtures, and each of those puts the branch back out
   * of reach with nothing red.
   */
  it("strands one in-use voucher on an access point nothing can reach", () => {
    const world = buildWorld();
    const unreachable = new Set(
      world.nas.filter((n) => n.reachability !== "ok").map((n) => n.id),
    );
    /* The predicate `revokeVoucher` itself uses, not an approximation of it: the
       session it looks for is the *first* one on the voucher in either live state. */
    const stranded = world.vouchers.filter((v) => {
      if (v.state !== "in-use") return false;
      const session = world.sessions.find(
        (s) => s.voucherId === v.id && (s.state === "online" || s.state === "orphaned"),
      );
      return Boolean(session && unreachable.has(String(session.nasId)));
    });
    expect(
      stranded.length,
      "No in-use voucher has a live session on a device Foyer cannot reach, so " +
        "`/problems/session-end-failed` is unreachable from `/vouchers` again and its " +
        "copy — the device's name and its reachability — can only be seen by driving " +
        "the portal onto the unreachable access point first.",
    ).toBeGreaterThan(0);

    for (const v of stranded) {
      /* And the voucher points at it. The console links a voucher in use to the
         session it is in use *on*, and a revoke that fails is the moment an operator
         follows that link. */
      expect(
        world.sessions.some((s) => s.id === v.currentSessionId),
        `${v.id} is stranded and points at no session`,
      ).toBe(true);
    }
  });

  it("lets no controller's versionSupported contradict its own version", () => {
    /* Authored fields drift. `versionSupported` and `version` are two statements
       about one controller, and the alarm quotes both — so a row that said "8.6.9"
       and "supported" would put the console's own dashboard at odds with the probe
       dialog behind it, which is why `web/mock` keeps no copy of this floor. */
    for (const c of buildWorld().controllers) {
      const expected = versionMeetsMinimum(c.version, CONTROLLER_MINIMUM_VERSION[c.vendor]);
      if (expected === null) continue; // no floor, or never probed: not established
      expect(
        c.versionSupported,
        `${c.id} reports ${c.version} against a floor of ${CONTROLLER_MINIMUM_VERSION[c.vendor]}`,
      ).toBe(expected);
    }
  });

  it("compares version fields as numbers, which is the half that goes wrong", () => {
    /* The pair that decides it. `9.1.19` sorts *after* `9.1.105` as a string and is
       three years older as a version, so a lexicographic comparison reports an
       out-of-date controller as supported — silently, and only on the deployments
       old enough to matter. */
    expect("9.1.19" > "9.1.105").toBe(true);
    expect(versionMeetsMinimum("9.1.19", "9.1.105")).toBe(false);

    expect(versionMeetsMinimum("9.4.19", "9.1.105")).toBe(true); // newer, despite the digits
    expect(versionMeetsMinimum("9.1.105", "9.1.105")).toBe(true); // the floor itself passes
    expect(versionMeetsMinimum("8.6.9", "9.1.105")).toBe(false);
    expect(versionMeetsMinimum("5.14.26", "5.0.15")).toBe(true);
    expect(versionMeetsMinimum("9.2", "9.1.105")).toBe(true); // short forms pad with zero

    /* Not established is neither pass nor fail, and the caller must not be handed a
       boolean it would render as a verdict. */
    expect(versionMeetsMinimum(null, "9.1.105")).toBe(null);
    expect(versionMeetsMinimum("9.4.19", null)).toBe(null);
    expect(versionMeetsMinimum("UDM-Pro", "9.1.105")).toBe(null);
  });

  /**
   * ── the world has to be able to be wrong about a site ──────────────────────
   *
   * `server.test.ts` proves the *handlers* honour the scope. This proves the
   * *world* can tell whether they do — the half nothing in this project would
   * notice on its own: were every session, account, device and voucher batch in
   * `site-hcm`, a dropped scope would change no answer and a leaking handler would
   * be byte-identical to a correct one.
   *
   * `tools/codegen/differ`'s `-sabotage site-scope` **refuses to run** against such
   * a world rather than report a false pass, which is the right refusal and also the
   * measurement: the harness cannot tell *I did not notice* from *there was nothing
   * to notice*. This guard is what keeps the answer to that "there was something to
   * notice".
   */
  it("puts rows in more than one site, in every collection the contract scopes", () => {
    const w = buildWorld();
    const collections: Record<string, { siteId: string }[]> = {
      sessions: w.sessions,
      accounts: w.accounts,
      nas: w.nas,
      "voucher batches": w.batches,
      "auth attempts": w.authAttempts,
    };
    for (const [name, rows] of Object.entries(collections)) {
      const sites = new Set(rows.map((r) => r.siteId));
      expect(
        [...sites].sort(),
        `every ${name} row is in one site, so a dropped site scope over this ` +
          `collection changes no answer and nothing — not this suite, not the E2E ` +
          `suite, not the differential harness, not a review of the console — can see ` +
          `the difference. Site scope is a security property.`,
      ).toHaveLength(2);
    }
  });

  it("puts one identifier in two sites at once", () => {
    /* Two sites full of unrelated rows prove that a list can get longer. The scope
       is only visible where the *same* identifier exists on both sides of it, which
       is what an operator searching a MAC actually meets. */
    const w = buildWorld();
    const sitesPerMac = new Map<string, Set<string>>();
    for (const s of w.sessions) {
      const seen = sitesPerMac.get(s.callingStationId) ?? new Set<string>();
      seen.add(s.siteId);
      sitesPerMac.set(s.callingStationId, seen);
    }
    const shared = [...sitesPerMac].filter(([, sites]) => sites.size > 1);
    expect(
      shared.map(([mac]) => mac),
      "no address exists in two sites, so a MAC search cannot show a scope being " +
        "honoured or dropped — only a longer or shorter list",
    ).not.toHaveLength(0);

    /* And it must be an address that legitimately can be in two places. iOS and
       Android present a *per-SSID* private address, so a randomised MAC on two
       networks would be this world inventing something the platforms do not do. */
    for (const [mac] of shared) {
      expect(
        macIsLocallyAdministered(mac),
        `${mac} is locally administered and appears in two sites, which no per-SSID ` +
          `private address does`,
      ).toBe(false);
    }
  });

  it("keeps one site clean, so a dropped scope is what makes it look broken", () => {
    const w = buildWorld();
    const raising = w.sites.filter((s) => deriveAlarms(w, s.id).length > 0);
    const populated = w.sites.filter((s) => w.nas.some((n) => n.siteId === s.id));
    const quiet = populated.filter((s) => deriveAlarms(w, s.id).length === 0);

    expect(raising.length, "no site raises an alarm, so the dashboard has nothing to show").toBeGreaterThan(0);
    expect(
      quiet.length,
      "every populated site raises alarms, so an alarm list that ignored its site " +
        "scope would look plausible everywhere — the case this fixture exists for is " +
        "a healthy venue inheriting another venue's unreachable access point",
    ).toBeGreaterThan(0);
  });

  it("never puts a session on a voucher belonging to another site", () => {
    /* Newly possible, and newly worth asserting: with one site there was no such
       thing as the wrong site's voucher. It happened immediately — the healthy
       baseline picks a device at random and eleven of its twenty-two rows landed on
       a hotel access point holding a code sold at the café. */
    const w = buildWorld();
    const siteOfBatch = new Map(w.batches.map((b) => [b.id, b.siteId]));
    for (const s of w.sessions) {
      if (!s.voucherId) continue;
      const v = w.vouchers.find((x) => x.id === s.voucherId);
      expect(v, `${s.id} points at a voucher that does not exist`).toBeTruthy();
      expect(
        siteOfBatch.get(v!.batchId),
        `${s.id} is in ${s.siteId} and holds ${v!.id}, sold in another site`,
      ).toBe(s.siteId);
    }
  });

  /**
   * ── the flag and the address have to agree ─────────────────────────────────
   *
   * `Session.macIsRandomised` is defined by the contract as *"the
   * locally-administered bit is set"*, and `docs/rules/security.md` §6 requires
   * that bit to be detected rather than assumed. A generator can claim to set it and
   * set a different one: masking the first octet's *high* nibble makes `| 0b0010`
   * touch bit 5 and `& 0b1110` clear bit 4, leaving both bits that mean anything to
   * chance. **In a world built that way, 12 of 34 sessions claimed
   * `macIsRandomised: true` over an address whose bit was clear, and 10 of 34 carried
   * a multicast address** — which no NAS can ever report as `Calling-Station-Id`.
   *
   * It is asserted here, over the finished world, rather than in a unit test of the
   * generator: the failure is the generator's *intent* and its *output* disagreeing,
   * and only a test reading the output can see that.
   *
   * Here the Go side is the reference and the oracle is the one that can be wrong,
   * so the guard belongs here and cannot be declared away in the harness.
   */
  it("never claims an address is randomised when its own bit says otherwise", () => {
    const w = buildWorld();
    const wrong = w.sessions.filter(
      (s) => Boolean(s.macIsRandomised) !== macIsLocallyAdministered(s.callingStationId),
    );
    expect(
      wrong.map((s) => `${s.id} ${s.callingStationId} macIsRandomised=${s.macIsRandomised}`),
      "a session's `macIsRandomised` contradicts the locally-administered bit of its " +
        "own address. The console tells the operator a real vendor OUI is a pseudonym, " +
        "or a rotating pseudonym is a device identity — the opposite of the truth in " +
        "both directions.",
    ).toEqual([]);
  });

  it("never gives a station a group address", () => {
    const w = buildWorld();
    const addresses = [
      ...w.sessions.map((s) => [s.id, s.callingStationId] as const),
      ...w.authAttempts
        .filter((a) => a.callingStationId)
        .map((a) => [a.id, a.callingStationId!] as const),
    ];
    expect(
      addresses.filter(([, mac]) => macIsMulticast(mac)).map(([id, mac]) => `${id} ${mac}`),
      "bit 0 of the first octet is the I/G bit: a group address is multicast and can " +
        "never be a station address, so no `Calling-Station-Id` may carry one",
    ).toEqual([]);
  });
});

/**
 * Figures that exist in two places, held to the one that is the authority.
 *
 * `docs/rules/database.md`: **derive the stand-in from what it stands in for.**
 * A copy goes stale while its authority sits right there, and goes on printing a
 * confident reason for a rule that has changed. Where a second copy genuinely cannot be avoided — a TypeScript constant
 * and a Go server cannot read each other — the copy has to be *checked* against the
 * authority rather than trusted, and this is where that check lives, because this
 * package is the one that already parses the document.
 */
describe("a second copy of a contract figure is checked, never trusted", () => {
  it("agrees with the byte ceiling the document declares", () => {
    /* Two servers can hold two numbers here while both cite the same measurement —
       200 kB in one, 100 kB in the other. The contract's figure is the authority,
       and `HeroImageUpload.dataBase64` carries it machine-readably so that
       neither implementation has to restate it — Go reads it out of the embedded
       specification, and this holds the TypeScript constant to it. */
    const declared = (
      contractDocument().components.schemas.HeroImageUpload as {
        properties: { dataBase64: { "x-max-decoded-bytes"?: number } };
      }
    ).properties.dataBase64["x-max-decoded-bytes"];
    expect(
      declared,
      "`HeroImageUpload.dataBase64` declares no `x-max-decoded-bytes`, so nothing is " +
        "the authority for the venue image ceiling and every implementation is free to " +
        "pick its own — which is how a console that accepts a photograph and a CI gate " +
        "that rejects it come to exist",
    ).toBeTypeOf("number");
    expect(IMAGE_MAX_BYTES, "`IMAGE_MAX_BYTES` has drifted from the document").toBe(declared);
  });

  it("names only capabilities the contract has, in the unverified list", () => {
    /* `unverified` is an array of strings on the wire, and it has to be: a driver may
       name a capability this document does not model, and refusing to serialise that
       would lose the warning rather than the value. What must not happen is a console
       receiving a name it can never mark — so the fixture world's list is held to the
       schema's own property names, read from the document rather than typed here. */
    const capabilities = Object.keys(
      (contractDocument().components.schemas.Capabilities as { properties: Record<string, unknown> })
        .properties,
    );
    expect(capabilities.length).toBeGreaterThan(0);
    for (const [vendor, names] of Object.entries(UNVERIFIED_CAPABILITIES)) {
      expect(
        names.filter((n) => !capabilities.includes(n)),
        `${vendor} names an unverified capability that is not a property of ` +
          "`Capabilities`, so no screen can mark the value it refers to",
      ).toEqual([]);
    }
    /* Non-vacuity: a world where every vendor named nothing would pass the loop above
       without asserting anything. The shipped UniFi driver names three. */
    expect(
      UNVERIFIED_CAPABILITIES.unifi.length,
      "no vendor names an unverified capability, so the check above is vacuous",
    ).toBeGreaterThan(0);
  });
});
