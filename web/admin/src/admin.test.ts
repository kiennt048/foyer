import { describe, expect, it } from "vitest";
import type { Capabilities, Nas, NasProbe, Session } from "@foyer/contract";
import { UNVERIFIED_CAPABILITIES, buildWorld, deriveAlarms } from "@foyer/fixtures";
import { bytes, duration, freshnessNote, sessionLevel, splitForTruncation } from "./format.js";
import {
  FINDING_COUNT,
  attentionCount,
  probeFindings,
  splitProbeWarnings,
  type Finding,
} from "./screens/nas/findings.js";

const world = buildWorld();

/**
 * The named admin tests. Each guards a failure that is easy
 * to introduce and expensive to find later; none may be deleted without a
 * replacement.
 */

describe("capability gating", () => {
  /** The rule the whole gate exists for. */
  const supported = (c: Capabilities, k: keyof Capabilities) =>
    k === "walledGarden" ? c[k] !== "none" : c[k] === true;

  const GATED: (keyof Capabilities)[] = [
    "bandwidthLimits",
    // The session dock's disconnect control. `nas-omada-1` must lack it too.
    "canDeauthGuest",
    "dynamicVlan",
    "liveReauthorize",
    "walledGarden",
  ];

  it("renders strictly fewer controls for a low-capability driver", () => {
    // The single easiest thing to get wrong in a mocked admin UI is rendering
    // every control for every NAS. This is the assertion that catches it.
    const high = world.nas.find((n) => n.id === "nas-mt-1")!;
    const low = world.nas.find((n) => n.id === "nas-omada-1")!;

    const count = (c: Capabilities) => GATED.filter((k) => supported(c, k)).length;

    expect(count(low.capabilities)).toBe(0);
    expect(count(high.capabilities)).toBeGreaterThan(count(low.capabilities));
  });

  it("has at least one low-capability driver in every fixture set", () => {
    // Required by `docs/rules/frontend.md`. Without one, the gate is never
    // exercised and the design is approved against hardware that does not exist.
    const minimal = world.nas.filter(
      (n) => n.capabilities.walledGarden === "none" && !n.capabilities.bandwidthLimits,
    );
    expect(minimal.length).toBeGreaterThan(0);
  });
});

describe("the registration probe tells an operator what they will hit later", () => {
  /*
   * The probe is the payoff of the registration wizard, and everything it renders is
   * derived here from the structured half of the answer — `reachability` and
   * `capabilities` — rather than from the server's sentences. These guard that
   * derivation, which is the part that decides what an operator actually reads.
   */
  const nas = (id: string): Nas => world.nas.find((n) => n.id === id)!;

  /**
   * The probe reports what the device is. Synthesised, so this stays a unit test.
   *
   * `messageAuthenticator` and `accounting` are **derived from the reachability this
   * probe is being given**, not copied off the device. That is what the two fields
   * are for: a probe that reached nothing measured nothing, so a caller may not
   * repeat what the record happens to still hold. Hardcoding a value here would make
   * the helper stop telling the truth the moment a fixture changed, and would let
   * `probeOf(device, "unreachable")` hand the findings a measurement no probe made —
   * which is the one thing the block below exists to catch.
   */
  const probeOf = (n: Nas, reachability = n.reachability): NasProbe => ({
    reachability,
    capabilities: n.capabilities,
    messageAuthenticator:
      reachability === "ok" && n.messageAuthenticator !== "unknown"
        ? n.messageAuthenticator
        : "not-measured",
    accounting:
      reachability !== "ok" ? "not-measured" : n.accountingConfigured ? "configured" : "missing",
    /*
     * Derived from the device's driver, not hardcoded.
     *
     * The shipped UniFi driver names three capabilities it cannot back with a
     * measurement and appends an access-point warning on every run; a helper that
     * named none would hand the findings a controller with no caveats, which is not
     * a controller anyone has. That is the *input* half of the shape
     * `docs/rules/testing.md` calls the thirteenth kind — a fake too generous
     * about what a driver can do produces confident output in either direction.
     *
     * The names come from `UNVERIFIED_CAPABILITIES` rather than from a literal here,
     * so this file cannot drift from the world the rest of the suite runs against.
     *
     * **The two warnings are keyed on different things, because the servers key them
     * on different things.** `access-point-unconfirmed` is `unifi` only — `unifi.go`'s
     * probe returns exactly that one and `web/mock` gates it on the same driver — while
     * `capabilities-unverified` follows `unverified`, so an Omada device carries the
     * second and not the first. Emitting the access-point warning for all three
     * controller vendors would model a driver nobody ships: too generous on the input
     * side, and the standing-versus-found split below is exactly the kind of assertion
     * that would then be proven against it.
     */
    warnings: [
      ...(n.driver === "unifi"
        ? [{ kind: "access-point-unconfirmed" as const, params: { vendor: n.driver } }]
        : []),
      ...(unverifiedFor(n).length
        ? [
            {
              kind: "capabilities-unverified" as const,
              params: { count: unverifiedFor(n).length },
            },
          ]
        : []),
    ],
    unverified: unverifiedFor(n),
    checks: [],
  });

  /** The driver's unverified set, empty for a driver that is not a controller. */
  const unverifiedFor = (n: Nas): string[] =>
    n.driver === "unifi" || n.driver === "omada" || n.driver === "ruckus"
      ? [...UNVERIFIED_CAPABILITIES[n.driver]]
      : [];

  const byKind = (findings: Finding[], kind: Finding["kind"]) =>
    findings.find((f) => f.kind === kind)!;

  it("answers every question every time, so a missing row cannot mean anything", () => {
    // A probe that could not measure something returns "not measured" for it rather
    // than dropping the row. A shorter list would make absence ambiguous — was it
    // not asked, or asked and fine? — and it is what the busy skeleton is sized on.
    const findings = probeFindings(probeOf(nas("nas-unifi-1")), nas("nas-unifi-1"));
    expect(findings).toHaveLength(FINDING_COUNT);
    expect(new Set(findings.map((f) => f.kind)).size).toBe(FINDING_COUNT);

    const unreachable = probeFindings(probeOf(nas("nas-unifi-3")), nas("nas-unifi-3"));
    expect(unreachable).toHaveLength(FINDING_COUNT);
  });

  it("presents nothing as capable that it could not measure", () => {
    /*
     * The pessimistic default, asserted from the outside. `nas-unifi-3` is a fully
     * capable UniFi that happens to be unreachable, and `nas-new-1` has never been
     * contacted at all — between them they cover "we knew once" and "we never knew".
     * Neither may produce a single `positive`, because a green row here is a promise
     * that a control will work.
     */
    for (const id of ["nas-unifi-3", "nas-new-1"]) {
      const findings = probeFindings(probeOf(nas(id)), nas(id));
      expect(
        findings.filter((f) => f.level === "positive"),
        `${id} reported a measured capability without reaching the device`,
      ).toEqual([]);
      const measured = findings.filter((f) => f.kind !== "reachability" && f.measured);
      expect(measured, `${id} claimed to have measured something`).toEqual([]);
    }
  });

  it("keeps a stale Message-Authenticator result out of a fresh probe", () => {
    // `nas-unifi-3` carries `messageAuthenticator: "enforced"` from the last time
    // anything reached it. On a panel headed "what this probe found", rendering that
    // is presenting an old measurement as a new one.
    const device = nas("nas-unifi-3");
    expect(device.messageAuthenticator).toBe("enforced");
    expect(byKind(probeFindings(probeOf(device), device), "messageAuthenticator").measured).toBe(
      false,
    );
  });

  it("tells an unreachable device apart from a rejected API key", () => {
    // Different problems with different fixes: one is the network or the address,
    // the other is a key an operator reissues on the controller in a minute. A
    // wizard that said "probe failed" for both would send them to the wrong place.
    const device = nas("nas-unifi-1");
    const down = byKind(probeFindings(probeOf(device, "unreachable"), device), "reachability");
    const expired = byKind(
      probeFindings(probeOf(device, "credentials-expired"), device),
      "reachability",
    );

    expect(down.level).toBe("urgent");
    expect(expired.level).toBe("urgent");
    expect(down.consequence).not.toBe(expired.consequence);
    expect(down.verdict).not.toBe(expired.verdict);
  });

  it("puts what needs doing above what merely works", () => {
    for (const device of world.nas) {
      const levels = probeFindings(probeOf(device), device).map((f) => f.level);
      const firstPositive = levels.indexOf("positive");
      if (firstPositive === -1) continue;
      expect(
        levels.slice(firstPositive).every((l) => l === "positive"),
        `${device.id} buried a finding under a row that was fine`,
      ).toBe(true);
    }
  });

  it("says everything in words, never in colour alone", () => {
    // Every row carries a verdict and a consequence. A row that meant something only
    // by its tint says nothing to a screen reader and nothing in a screenshot, which
    // is how these get circulated.
    for (const device of world.nas) {
      for (const f of probeFindings(probeOf(device), device)) {
        expect(f.verdict.trim(), `${device.id}/${f.kind}`).not.toBe("");
        expect(f.consequence.trim(), `${device.id}/${f.kind}`).not.toBe("");
        expect(f.title.trim(), `${device.id}/${f.kind}`).not.toBe("");
      }
    }
  });

  it("names the four consequences an operator otherwise learns the hard way", () => {
    /*
     * Accounting that will never arrive, a device exposed to BlastRADIUS, an
     * unbounded pre-authentication state, and a quota that is a sampled backstop.
     * Each is permanent or near-permanent, and each is invisible until it bites.
     */
    const capable = nas("nas-unifi-1");
    const noAccounting: Nas = { ...capable, accountingConfigured: false };
    expect(byKind(probeFindings(probeOf(noAccounting), noAccounting), "accounting").level).not.toBe(
      "positive",
    );

    // Refused is a `warning`, not a caution: it does not clear on its own and no
    // configuration Foyer generates makes it go away.
    const mt = nas("nas-mt-1");
    expect(mt.messageAuthenticator).toBe("refused");
    expect(byKind(probeFindings(probeOf(mt), mt), "messageAuthenticator").level).toBe("warning");

    /*
     * The garden is three-valued on purpose, and the middle value is not "fine".
     *
     * Measured on `nas-mt-1` rather than on the UniFi device, and the reason is the
     * assertion immediately below: the shipped UniFi driver cannot back its garden
     * value with a measurement, so on that device none of the three may read as a
     * fact. A driver that *can* is where the three levels are checked.
     */
    const gardenLevel = (kind: Capabilities["walledGarden"]) =>
      byKind(
        probeFindings(
          { ...probeOf(mt, "ok"), capabilities: { ...mt.capabilities, walledGarden: kind } },
          mt,
        ),
        "walledGarden",
      ).level;
    expect(gardenLevel("fqdn-sni")).toBe("positive");
    expect(gardenLevel("ip-range")).toBe("caution-minor");
    expect(gardenLevel("none")).toBe("warning");

    /*
     * **A value the driver believes is never a green tick**, however good the value
     * is. `NasProbe.unverified` names it, and the shipped UniFi driver names three on
     * every run — a screen ignoring them shows an operator a green tick over three
     * unmeasured claims.
     *
     * `fqdn-sni` is the strongest garden there is, which is what makes it the right
     * value to assert on: the row must not read `positive` **because the standing of
     * the value is unknown**, not because the value is bad.
     */
    const unverifiedGarden = byKind(
      probeFindings(
        {
          ...probeOf(capable, "ok"),
          capabilities: { ...capable.capabilities, walledGarden: "fqdn-sni" },
          unverified: ["walledGarden"],
        },
        capable,
      ),
      "walledGarden",
    );
    expect(unverifiedGarden.level).toBe("neutral");
    expect(unverifiedGarden.measured).toBe(false);
    /* And the reverse, so this is not satisfied by a console that gave up on the
       whole row: with nothing unverified, the same value reads as the fact it is. */
    expect(
      byKind(
        probeFindings(
          {
            ...probeOf(capable, "ok"),
            capabilities: { ...capable.capabilities, walledGarden: "fqdn-sni" },
            unverified: [],
          },
          capable,
        ),
        "walledGarden",
      ).level,
    ).toBe("positive");

    const sampled: NasProbe = {
      ...probeOf(capable, "ok"),
      capabilities: { ...capable.capabilities, hardQuota: false },
    };
    expect(byKind(probeFindings(sampled, capable), "hardQuota").level).toBe("caution-minor");
  });

  it("has more to tell an operator about a device that can do less", () => {
    // The capability-gating rule, at the moment of registration: the low-capability
    // driver every fixture set is required to contain must produce strictly more for
    // the operator to read, not a shorter and tidier list.
    const capable = nas("nas-unifi-1");
    const minimal = nas("nas-omada-1");
    /* Forced reachable: the fixture's expired key would make everything unmeasured,
       which is a different assertion. This one is about capability, not contact. */
    const capableFindings = probeFindings(probeOf(capable, "ok"), capable);
    const minimalFindings = probeFindings(probeOf(minimal, "ok"), minimal);

    expect(attentionCount(minimalFindings)).toBeGreaterThan(attentionCount(capableFindings));
  });

  /*
   * ── the warning that fires on every healthy probe ──────────────────────────
   *
   * `access-point-unconfirmed` is unconditional on UniFi: `unifi.go`'s probe returns
   * exactly `[AccessPointUnconfirmedWarning]`, always, because the Integration API has
   * no field to compare the claimed access point against. `web/mock` adds
   * `capabilities-unverified` on the same run for the same driver.
   *
   * So the notice they were rendered in was, on the commonest vendor in the product,
   * **constant**. Nothing in it ever varied, nothing in it was ever about the device
   * being looked at, and it sat under a heading and a glyph that say something is
   * wrong. The block below is what stops that coming back.
   *
   * **A missing kind is a compile error, not a case here.** `WARNING_STANDING` is a
   * `Record<ProbeWarningKind, …>` over the contract's closed enum, so a fourth kind
   * does not build until somebody has decided which of the two it is — which is a
   * stronger guarantee than any assertion this file could make, and the reason there
   * is no "every kind is classified" test underneath.
   */
  describe("a standing property of the driver is not a fault found today", () => {
    it("leaves nothing in the found bucket for a healthy UniFi probe", () => {
      /*
       * The assertion the split exists for, and the one that is red against the
       * previous rendering: every warning a healthy UniFi device produces is a
       * standing one, so the notice that says *Found on this device* must not appear
       * at all. Both kinds are listed, because the two servers disagree about how
       * many arrive — Go sends one, the mock sends two — and the console has to be
       * right either way.
       */
      const { standing, found } = splitProbeWarnings([
        { kind: "access-point-unconfirmed", params: { vendor: "unifi" } },
        { kind: "capabilities-unverified", params: { count: 3 } },
      ]);
      expect(found, "a healthy device reported a fault").toEqual([]);
      expect(standing).toHaveLength(2);
    });

    it("keeps the one warning an operator can act on out of the standing list", () => {
      // `version-below-floor` is about *this* controller and an upgrade fixes it. It
      // was the third bullet under the same heading as the two constants.
      const { standing, found } = splitProbeWarnings([
        { kind: "access-point-unconfirmed", params: {} },
        { kind: "version-below-floor", params: { version: "8.0.24", minimumVersion: "9.1.105" } },
      ]);
      expect(found).toHaveLength(1);
      expect(found[0]).toContain("9.1.105");
      expect(standing).toHaveLength(1);
    });

    it("files a kind it has never heard of as found rather than as standing", () => {
      /*
       * The default that matters. A condition this console cannot name is one nobody
       * has decided about; calling it "always true of this driver, and nothing to do"
       * is a claim, and it is the one mistake that cannot be noticed from the screen.
       */
      const { standing, found } = splitProbeWarnings([
        {
          /* A real one: `internal/nas` emits this kind and the contract does not name
             it, so it is what a console one version behind actually receives. */
          kind: "coa-acknowledged-unknown-session" as never,
          detail: "The NAS acknowledged a session id it cannot have issued.",
        },
      ]);
      expect(standing).toEqual([]);
      expect(found).toHaveLength(1);
    });

    it("reports no fault on any reachable device in the fixture world", () => {
      /*
       * Driven from the fixtures rather than from a literal, and it is the shape of
       * the defect at product scale: **no device anybody can register produces a
       * device-specific warning**, so before the split the *Before you rely on this*
       * notice was, across the whole world, a heading over two constants.
       *
       * The `found` bucket earns its place from `version-below-floor`, which arrives
       * on the controller probe rather than this one. If a device here ever starts
       * reporting one, this assertion is the thing that says the world changed.
       */
      const seen = world.nas
        .map((n) => probeOf(n, "ok").warnings)
        .flatMap((w) => splitProbeWarnings(w).found);
      expect(seen).toEqual([]);

      const standing = world.nas
        .map((n) => probeOf(n, "ok").warnings)
        .flatMap((w) => splitProbeWarnings(w).standing);
      expect(standing.length, "no device warns about anything, so nothing is split").
        toBeGreaterThan(0);
    });
  });
});

describe("the fixtures are ugly enough to be worth reviewing", () => {
  it("contains a session orphaned with no Acct-Stop", () => {
    const orphaned = world.sessions.find((s) => s.state === "orphaned");
    expect(orphaned).toBeDefined();
    expect(orphaned!.endedAt).toBeNull();
    // Forty days "online". Left alone it stays that way forever.
    expect(Date.now() - Date.parse(orphaned!.startedAt)).toBeGreaterThan(30 * 86_400_000);
  });

  it("contains a session with no accounting at all", () => {
    const unknown = world.sessions.find((s) => s.state === "unknown");
    expect(unknown).toBeDefined();
    expect(unknown!.freshness.state).toBe("absent");
    expect(unknown!.inputOctets).toBeNull();
  });

  it("contains a session whose duration is negative from clock skew", () => {
    const skewed = world.sessions.find((s) => (s.sessionSeconds ?? 0) < 0);
    expect(skewed).toBeDefined();
  });

  it("contains two live sessions sharing one MAC", () => {
    const live = world.sessions.filter((s) => s.state === "online");
    const counts = new Map<string, number>();
    for (const s of live) counts.set(s.callingStationId, (counts.get(s.callingStationId) ?? 0) + 1);
    expect([...counts.values()].some((n) => n > 1)).toBe(true);
  });

  it("contains a non-unique Acct-Session-Id", () => {
    // Observed in real deployments, not hypothetical. Never a key on its own.
    const ids = world.sessions.map((s) => s.acctSessionId).filter(Boolean);
    expect(new Set(ids).size).toBeLessThan(ids.length);
  });

  it("contains an unreachable NAS and one with expired credentials", () => {
    expect(world.nas.some((n) => n.reachability === "unreachable")).toBe(true);
    expect(world.nas.some((n) => n.reachability === "credentials-expired")).toBe(true);
  });

  it("contains a NAS that refuses require_message_authenticator", () => {
    expect(world.nas.some((n) => n.messageAuthenticator === "refused")).toBe(true);
  });

  it("contains vouchers in every state", () => {
    const states = new Set(world.vouchers.map((v) => v.state));
    for (const s of ["unused", "in-use", "exhausted", "expired", "revoked"]) {
      expect(states.has(s as never), `missing voucher state ${s}`).toBe(true);
    }
  });
});

describe("no figure is rendered without its freshness", () => {
  it("never describes absent accounting as unlimited or zero", () => {
    const note = freshnessNote({ state: "absent", sampledAt: null, intervalSeconds: null });
    /*
     * Asserted as **mechanism, then consequence** rather than as a phrase.
     *
     * Pinning literal words such as "Not measurable" and "not unlimited" would test
     * the copy rather than what the copy has to accomplish — and would make a
     * rewrite that *improves* the sentence look like a regression. A negation pair
     * with no positive statement is the failure: the reader holds two "not"s and
     * infers the true state from what it is not.
     *
     * What must survive any rewording is that the note says the device sends nothing
     * (the mechanism) and what that costs the operator (they cannot measure and
     * cannot enforce). Both halves, and no word suggesting a quantity.
     */
    expect(note, "The note does not say what the device is doing.").toMatch(/sends no accounting/i);
    expect(
      note,
      "The note states the mechanism but not what it costs the operator. Somebody " +
        "looking at a blank cell concludes 'broken', then 'unlimited', unless the " +
        "sentence says otherwise.",
    ).toMatch(/cannot/i);
    // And never a number or a word that reads as one.
    expect(note).not.toMatch(/\b(0|zero|unlimited|none)\b/i);
  });

  it("names the sampling interval whenever one exists", () => {
    const note = freshnessNote({ state: "sampled", sampledAt: null, intervalSeconds: 300 });
    expect(note).toContain("5 minutes");
  });

  it("returns null for an absent byte count rather than zero", () => {
    // `null` is not `0`. A NAS that sends no accounting has not transferred
    // nothing; it has told us nothing.
    expect(bytes(null)).toBeNull();
    expect(bytes(0)).toBe("0 B");
  });

  it("names clock skew instead of printing a negative duration", () => {
    const d = duration(-252);
    expect(d?.anomaly).toBe(true);
    expect(d?.text).toBe("clock skew");
    expect(d?.text).not.toContain("-");
  });

  it("drops a zero component from a duration", () => {
    expect(duration(40 * 86400)?.text).toBe("40 days");
    expect(duration(3 * 3600)?.text).toBe("3 hours");
  });

  it("does not print a plural unit for a count of one", () => {
    // English needs this and the Vietnamese copy did not. "1 days online" is the
    // kind of detail that makes an otherwise careful console look unfinished.
    expect(duration(86400)?.text).toBe("1 day");
    expect(duration(3600)?.text).toBe("1 hour");
  });
});

describe("session state is keyed on what the operator must do", () => {
  const cases: [Session["state"], string][] = [
    ["online", "positive"],
    ["ended", "neutral"],
    // A fault requiring correction, not an outage — and never a healthy green row.
    ["orphaned", "caution-minor"],
    ["unknown", "neutral"],
  ];
  for (const [state, level] of cases) {
    it(`maps ${state} to ${level}`, () => {
      expect(sessionLevel(state)).toBe(level);
    });
  }
});

describe("identifier truncation", () => {
  it("never truncates the tail", () => {
    // MACs and Acct-Session-Ids carry their discriminating characters at the end.
    const mac = "3c:22:fb:06:0b:e5";
    const { head, tail } = splitForTruncation(mac, 8);
    expect(head + tail).toBe(mac);
    expect(mac.endsWith(tail)).toBe(true);
    expect(tail.length).toBe(8);
  });
});

describe("alarms", () => {
  it("raises urgent for an unreachable NAS and warning for a refused Message-Authenticator", () => {
    const alarms = deriveAlarms(world, "site-hcm");
    expect(alarms.some((a) => a.level === "urgent")).toBe(true);
    expect(alarms.some((a) => a.level === "warning")).toBe(true);
  });

  it("sorts by required action, most urgent first", () => {
    const alarms = deriveAlarms(world, "site-hcm");
    const order = ["urgent", "warning", "caution-major", "caution-minor", "negative", "info"];
    const ranks = alarms.map((a) => order.indexOf(a.level));
    expect(ranks).toEqual([...ranks].sort((a, b) => a - b));
  });
});

describe("copy comes from the catalogue, in English", () => {
  it("leaves no Vietnamese string inline in a component", () => {
    // English is the source of truth. A Vietnamese string surviving anywhere in
    // `src/` means a screen was converted by eye rather than through the
    // catalogue, which is exactly how the last one gets missed.
    const sources = import.meta.glob("./**/*.{ts,tsx}", {
      query: "?raw",
      eager: true,
      import: "default",
    }) as Record<string, string>;

    // The Vietnamese-only base letters, plus the Latin Extended Additional block
    // its precomposed tone marks live in.
    const vietnamese =
      /[\u0103\u00e2\u0111\u00ea\u00f4\u01a1\u01b0\u0102\u00c2\u0110\u00ca\u00d4\u01a0\u01af\u1ea0-\u1ef9]/;
    /*
     * **One catalogue is Vietnamese on purpose: the voucher slip's.** A slip is
     * printed for a guest, in the venue's language, and the console is not its
     * reader (without this, a Vietnamese hotel's slips print in English). That
     * copy lives in `i18n/print.ts` and nowhere else; the other three files are its
     * tests and their vectors. Each exemption must still *contain* Vietnamese, so an
     * entry cannot outlive its reason and quietly widen the rule.
     */
    /*
     * **And one console catalogue: the network screen's.** The rule that every
     * operator setting is set in the console asked for each new settings screen's
     * copy in English and Vietnamese. It is still one
     * catalogue and not a component: `i18n/network.ts` types the Vietnamese as a
     * total map of the English keys, so the rule this test exists for — no screen
     * converted by eye — holds there by compilation rather than by this sweep.
     */
    /*
     * And the sessions screen's portal-authorization list, on the
     * same footing and for the same reason: `i18n/portalgrants.ts` types its
     * Vietnamese as a total map of the English keys.
     */
    /*
     * **And the console speaks Vietnamese**, from one place: `i18n/vi/`, one total translation per English catalogue,
     * each typed so a missing key does not compile and held key for key by
     * `i18n/i18n.test.ts`. The inline Vietnamese that `network.ts` and
     * `portalgrants.ts` carried moved there, so their exemptions are gone. `lang.ts`
     * names each language in itself for the switcher — "Tiếng Việt" is a name, not
     * copy.
     */
    const GUEST_COPY = [
      "./i18n/print.ts",
      "./i18n/lang.ts",
      "./print/VoucherSlips.test.tsx",
      "./print/qr.test.ts",
      "./print/qr.golden.ts",
    ];
    const TRANSLATIONS = "./i18n/vi/";
    const offenders = Object.entries(sources)
      .filter(([path]) => !path.endsWith("admin.test.ts") && !GUEST_COPY.includes(path))
      .filter(([path]) => !path.startsWith(TRANSLATIONS))
      .filter(([, body]) => vietnamese.test(body))
      .map(([path]) => path);

    // Guards the guard: a glob that matches nothing would pass silently.
    expect(Object.keys(sources).length).toBeGreaterThan(5);
    expect(offenders).toEqual([]);
    for (const path of GUEST_COPY) {
      expect(vietnamese.test(sources[path] ?? ""), `${path} no longer needs its exemption`).toBe(true);
    }
    const translations = Object.entries(sources).filter(([path]) => path.startsWith(TRANSLATIONS));
    expect(translations.length, "the translations directory is where the glob looks").toBeGreaterThan(20);
    for (const [path, body] of translations) {
      expect(vietnamese.test(body), `${path} is in the translations directory and holds no Vietnamese`).toBe(true);
    }
  });

  /*
   * ─────────────────────────────────────────────────────────────────────────────
   *
   * **The three house-rule sweeps below run over every catalogue, not over one.**
   *
   * The copy is nine modules — `core.ts` and one namespace per lazily loaded screen —
   * and a sweep that named one of them would go on passing while covering *half* the
   * sentences in the console, with nothing red to say so.
   *
   * So the set is **derived from the directory**, not listed here: every `i18n/*.ts`
   * is globbed, and every export that is a non-empty object of strings and
   * string-returning functions is a catalogue. A tenth namespace is swept the moment
   * the file exists, and nobody has to remember.
   *
   * `CATALOGUE_FILES` below is the guard on the derivation itself: a file that
   * exports no catalogue must be one of the two that are *supposed* to — the lookup
   * and the helper — or the glob has stopped seeing something and the sweeps are
   * quietly narrower than they read.
   */
  const MODULES = import.meta.glob(["./i18n/*.ts", "!./i18n/*.test.ts"], { eager: true }) as Record<
    string,
    Record<string, unknown>
  >;

  /** The three modules in `i18n/` that hold no copy. Everything else must hold some. */
  const NOT_CATALOGUES = ["./i18n/index.ts", "./i18n/catalogue.ts", "./i18n/lang.ts"];

  const isCatalogue = (v: unknown): v is Record<string, string | ((...a: never[]) => string)> =>
    typeof v === "object" &&
    v !== null &&
    !Array.isArray(v) &&
    Object.keys(v).length > 0 &&
    Object.values(v).every((e) => typeof e === "string" || typeof e === "function");

  /** Every catalogue in the console, as `[module, key, value]`. */
  const ENTRIES: [string, string, string | ((...a: never[]) => string)][] = Object.entries(MODULES)
    .flatMap(([path, mod]) => Object.values(mod).filter(isCatalogue).map((c) => [path, c] as const))
    .flatMap(([path, c]) => Object.entries(c).map(([k, v]) => [path, k, v] as [string, string, typeof v]));

  it("sweeps every catalogue module, and knows which files carry no copy", () => {
    const withCopy = new Set(ENTRIES.map(([path]) => path));
    const without = Object.keys(MODULES).filter((p) => !withCopy.has(p));
    expect(without.sort()).toEqual([...NOT_CATALOGUES].sort());
    // The split is the reason the sweeps are worth having: a single-file catalogue
    // would make the glob pointless and this assertion is what notices a collapse
    // back to one.
    expect(withCopy.size).toBeGreaterThan(5);
    // And it has to reach the copy, not merely the files.
    expect(ENTRIES.length).toBeGreaterThan(900);
  });

  it("writes empty-state titles as a phrase ending in a period", () => {
    // `No access points.` — not `No access points found`, and never `Get started!`.
    const titles = ENTRIES.filter(([, k]) => k.endsWith("empty.title"));
    expect(titles.length).toBeGreaterThan(0);
    for (const [path, key, value] of titles) {
      const where = `${path} ${key}`;
      expect(typeof value, where).toBe("string");
      expect(value as string, where).toMatch(/\.$/);
      expect((value as string).split(/\s+/).length, where).toBeLessThanOrEqual(6);
    }
  });

  it("keeps empty-state descriptions under 14 words", () => {
    const descriptions = ENTRIES.filter(([, k]) => k.endsWith("empty.description"));
    expect(descriptions.length).toBeGreaterThan(0);
    for (const [path, key, value] of descriptions) {
      expect((value as string).split(/\s+/).length, `${path} ${key}`).toBeLessThan(14);
    }
  });

  it("never apologises, never says a thing happened successfully", () => {
    // Confirm completion instead: "Account created", not "Your account was
    // successfully created".
    const banned = /\b(oops|sorry|please|successfully)\b/i;
    for (const [path, key, value] of ENTRIES) {
      const text =
        typeof value === "function" ? (value as (...a: unknown[]) => string)(1, "x") : value;
      expect(banned.test(text), `${path} ${key}: ${text}`).toBe(false);
    }
  });

  /**
   * One key is one place.
   *
   * A namespace resolves `{ ...core, ...namespace }`, so a key defined in both would
   * compile, render the namespace's copy on that one screen and core's everywhere
   * else — a divergence with no error and no visible symptom until somebody edits the
   * wrong one. Nothing in the type system refuses it, so this does.
   */
  it("defines every key exactly once across the nine catalogues", () => {
    const seen = new Map<string, string[]>();
    for (const [path, key] of ENTRIES) seen.set(key, [...(seen.get(key) ?? []), path]);
    const duplicated = [...seen.entries()].filter(([, paths]) => paths.length > 1);
    expect(duplicated).toEqual([]);
  });
});

describe("read-only regions", () => {
  it("exposes no mutating operation for sessions", async () => {
    // `radacct` belongs to FreeRADIUS. An "edit session" affordance can never be
    // built, so the API surface must not offer one to build it from.
    //
    // `disconnectSession` is the one named exception and it is not an edit: it asks
    // the *device* to end the session and writes no accounting row — the device's
    // own Acct-Stop does that. `contract/src/read-only.ts` names the same one
    // operation, so a second write here fails in both places.
    const { api } = await import("./api.js");
    const sessionOps = Object.keys(api).filter((k) => k.toLowerCase().includes("session"));
    expect(sessionOps.sort()).toEqual(["disconnectSession", "getSession", "listSessions"]);
  });
});
