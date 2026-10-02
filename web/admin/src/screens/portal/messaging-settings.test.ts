import { describe, expect, it } from "vitest";
import type { PhoneAbuseControls, PhoneTestMessageOutcome } from "@foyer/contract";
import { abuseBody, capBody, toAbuseDraft } from "./PhoneAbuse.js";
import { testOutcomeCopy } from "./PhoneSignIn.js";
import { nowIn, zoneOptions } from "./TimeZone.js";

const stored: PhoneAbuseControls = {
  siteId: "site-hcm",
  limits: {
    perNumberPerHour: 3,
    perNumberPerDay: 5,
    perDevicePerHour: 5,
    perAddressPerHour: 20,
    codeMinutes: 5,
    maxAttempts: 5,
  },
  bounds: {
    perNumberPerHour: { minimum: 1, maximum: 10, default: 3 },
    perNumberPerDay: { minimum: 1, maximum: 20, default: 5 },
    perDevicePerHour: { minimum: 1, maximum: 20, default: 5 },
    perAddressPerHour: { minimum: 1, maximum: 2000, default: 60 },
    codeMinutes: { minimum: 1, maximum: 10, default: 5 },
    maxAttempts: { minimum: 1, maximum: 10, default: 5 },
  },
  mobilePrefixes: ["03", "05", "07", "08", "09"],
  country: "VN",
  customised: false,
  updatedAt: null,
};

describe("phone sign-in's limits panel", () => {
  it("round-trips every limit as a number and keeps the ranges in the plan's order", () => {
    const d = toAbuseDraft(stored);
    d.limits.perAddressPerHour = 400;
    d.prefixes = ["09", "03"];
    const body = abuseBody(d);
    expect(body.limits.perAddressPerHour).toBe(400);
    expect(body.limits.codeMinutes).toBe(5);
    expect(body.mobilePrefixes).toEqual(["03", "09"]);
  });

  it("sends an empty cap as no cap, never as zero", () => {
    expect(capBody("")).toEqual({ dailyLimit: null });
    expect(capBody(" 250 ")).toEqual({ dailyLimit: 250 });
  });
});

describe("the test message", () => {
  it("has a sentence for every outcome, and the refusal names the provider's code", () => {
    const outcomes: PhoneTestMessageOutcome[] = [
      "sent",
      "phone-invalid",
      "not-configured",
      "ceiling-reached",
      "provider-refused",
      "provider-unreachable",
    ];
    const seen = new Set(outcomes.map((outcome) => testOutcomeCopy({ outcome, sentToday: 0, providerCode: "99" })));
    expect(seen.size).toBe(outcomes.length);
    expect(testOutcomeCopy({ outcome: "provider-refused", sentToday: 0, providerCode: "99" })).toContain("99");
  });
});

describe("the site's time zone", () => {
  it("offers the browser's zones and never drops the stored one", () => {
    expect(zoneOptions("Asia/Ho_Chi_Minh")).toContain("Asia/Ho_Chi_Minh");
    expect(zoneOptions("Etc/Nowhere")[0]).toBe("Etc/Nowhere");
    expect(nowIn("Asia/Ho_Chi_Minh", new Date("2026-09-23T00:00:00Z"))).toContain("07:00");
  });
});
