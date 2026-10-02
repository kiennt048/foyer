import { describe, expect, it } from "vitest";
import type { PhoneSignIn } from "@foyer/contract";
import { toDraft, updateBody } from "./PhoneSignIn.js";

const stored: PhoneSignIn = {
  siteId: "site-hcm",
  provider: "esms-sms",
  brandname: "FOYER",
  credentialsSet: true,
  sandbox: false,
  dailyLimit: 200,
  sentToday: 3,
  sessionMinutes: 60,
  messageTemplate: "{code} la ma Wi-Fi",
  country: "VN",
  limits: {
    perNumberPerHour: 3,
    perNumberPerDay: 5,
    perDevicePerHour: 5,
    perAddressPerHour: 20,
    codeMinutes: 5,
    maxAttempts: 5,
  },
};

describe("phone sign-in panel", () => {
  it("starts with empty key boxes whatever is stored, and an empty box keeps the stored key", () => {
    const d = toDraft(stored);
    expect(d.apiKey).toBe("");
    expect(d.secretKey).toBe("");
    const body = updateBody(d);
    expect("apiKey" in body).toBe(false);
    expect("secretKey" in body).toBe(false);
  });

  it("sends a key only when one was typed, and only the chosen provider's fields", () => {
    const body = updateBody({
      ...toDraft(stored),
      apiKey: "new-key",
      provider: "esms-zalo",
      zaloOaId: "oa",
      zaloTemplateId: "t",
    });
    expect(body.apiKey).toBe("new-key");
    expect("secretKey" in body).toBe(false);
    expect(body.brandname).toBeUndefined();
    expect(body.zaloOaId).toBe("oa");
  });
});
