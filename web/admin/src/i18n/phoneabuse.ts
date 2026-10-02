/**
 * Copy for phone sign-in's abuse limits and the deployment-wide SMS cap, on the
 * sign-in tab of `/portal`, loaded with that panel.
 *
 * Its own namespace, in the portal screen's lazy chunk: an operator who never opens
 * the sign-in tab never downloads these sentences, and the entry chunk carries none
 * of them. English, the source of truth; the Vietnamese is `vi/phoneabuse.ts`.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const phoneAbuse = {
  "abuse.title": "Phone sign-in limits",
  "abuse.lede":
    "What stops one person, one phone or one address from spending the venue's messages. Each can be raised or lowered within a fixed range; none can be switched off.",
  "abuse.defaults": "These are Foyer's defaults.",
  "abuse.perNumberPerHour": "Codes per number, per hour",
  "abuse.perNumberPerDay": "Codes per number, per day",
  "abuse.perDevicePerHour": "Codes per device, per hour",
  "abuse.perAddressPerHour": "Codes per internet address, per hour",
  "abuse.perAddressHint":
    "Raise this if every guest reaches the portal through one shared address — a NAT, or a cloud-hosted portal behind a proxy — or twenty guests an hour will use it up.",
  "abuse.codeMinutes": "Minutes a code works",
  "abuse.maxAttempts": "Tries per code",
  "abuse.range": (min: number, max: number) => `From ${min} to ${max}.`,
  "abuse.prefixes": "Mobile ranges that can receive a code",
  "abuse.prefixesHint":
    "Only Vietnamese mobile numbers ever receive a code. Untick a range to stop sending to it.",
  "abuse.save": "Save limits",
  "abuse.saved": "Limits saved",
  "abuse.saveFailed": "Limits not saved",

  "cap.title": "Messages per day, whole deployment",
  "cap.lede":
    "A second ceiling above every venue's own daily limit, counted across all of them. When it is reached, no venue sends a code until tomorrow.",
  "cap.none": "No deployment-wide cap — each venue's own daily limit applies.",
  "cap.label": "Messages per day",
  "cap.hint": "Leave empty for no deployment-wide cap.",
  "cap.save": "Save cap",
  "cap.saved": "Cap saved",
  "cap.saveFailed": "Cap not saved",
};

/** The shell's copy plus this panel's. */
void loadLocale(() => import("./vi/phoneabuse.js"));

export const t = makeT({ ...core, ...phoneAbuse });
