/**
 * Copy for the phone sign-in panel on `/portal`, loaded with that panel.
 *
 * The panel is its own lazy chunk inside the portal screen's, so an operator who
 * never opens the sign-in tab never downloads these sentences. `core.ts` has the rule.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const phone = {
  "phone.lede":
    "Guests type a Vietnamese mobile number and receive a 6-digit code by SMS or Zalo. Every code costs the venue a message, so the daily limit below is a spending limit.",
  "phone.unverified.title": "Not yet proven with a real provider account",
  "phone.unverified.body":
    "The eSMS connection is built from eSMS's published API and tested against a stand-in. Send one code to your own phone after saving, before offering this to guests.",

  "phone.provider": "Provider",
  "phone.provider.esms-sms": "SMS from a brandname (eSMS)",
  "phone.provider.esms-zalo": "Zalo notification (eSMS)",
  "phone.brandname": "Brandname",
  "phone.brandnameHint":
    "The sender name registered with eSMS, exactly as registered. Vietnamese carriers deliver a brandname SMS only from a registered name, through the provider's HTTP API — not a phone line or SIP.",
  "phone.zaloOaId": "Zalo Official Account id",
  "phone.zaloTemplateId": "Zalo template id",
  "phone.zaloTemplateHint": "An approved template with one parameter named otp.",
  "phone.apiKey": "API key",
  "phone.secretKey": "Secret key",
  "phone.keysHint.unset":
    "The API key and secret key from your eSMS account. Stored encrypted and never shown again.",
  "phone.keysHint.set": "Stored. Leave blank to keep the current key.",
  "phone.template": "Message",
  "phone.templateHint":
    "Write {code} where the code goes. For SMS this must match the brandname template eSMS approved, character for character.",
  "phone.dailyLimit": "Messages per day",
  "phone.dailyLimitHint":
    "When this is reached, phone sign-in stops sending until tomorrow and guests are asked to use another way in.",
  "phone.sessionMinutes": "Minutes online after signing in",
  "phone.sandbox": "Test mode — the provider accepts messages and delivers none",

  "phone.sentToday": (sent: number, limit: number) => `${sent} of ${limit} messages sent today.`,
  "phone.limits": (
    perHour: number,
    perDay: number,
    device: number,
    address: number,
    minutes: number,
    attempts: number,
  ) =>
    `Limits in force: ${perHour} codes an hour and ${perDay} a day per number, ${device} an hour per device, ${address} an hour per internet address; a code works for ${minutes} minutes and ${attempts} tries. Only Vietnamese mobile numbers receive codes. Change them under Phone sign-in limits below.`,

  "phone.test.title": "Send a test message",
  "phone.test.label": "Your mobile number",
  "phone.test.hint":
    "Sends the message above, with a random code that signs nobody in, through the saved provider. It is a real message: it counts against today's limits.",
  "phone.test.send": "Send test message",
  "phone.test.failed": "Test message not sent",
  "phone.test.sent": "The provider accepted the message. Check that it arrived.",
  "phone.test.phone-invalid": "Not a Vietnamese mobile number in a range that is open below.",
  "phone.test.not-configured": "Save a provider and its keys first.",
  "phone.test.ceiling-reached": "Today's message limit is reached, so nothing was sent.",
  "phone.test.provider-refused": (code: string) =>
    `The provider refused the message (result code ${code}). Check the keys, the brandname and that the message matches the registered template.`,
  "phone.test.provider-unreachable": "The provider did not answer. Try again in a minute.",
  "phone.save": "Save phone sign-in",
  "phone.saved": "Phone sign-in saved",
  "phone.saveFailed": "Phone sign-in not saved",
  "phone.notConfigured": "Not set up. Save a provider here before turning on phone sign-in above.",
};

/** The shell's copy plus this panel's. */
void loadLocale(() => import("./vi/phone.js"));

export const t = makeT({ ...core, ...phone });
