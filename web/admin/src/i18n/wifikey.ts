/**
 * Copy for the guest network's Wi-Fi password, on `/portal` beside the network
 * name. Loaded with that screen; English, like every catalogue of the console's own.
 * The slip's own words for it — in the venue's language — are in `print.ts`.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const wifiKey = {
  "wifiKey.label": "Wi-Fi password",
  "wifiKey.hint":
    "Only if the guest network asks for a password as well as the sign-in page. Voucher slips then print it, and their QR code joins with it.",
  "wifiKey.set": (when: string, who: string) => `Set ${when}${who ? ` by ${who}` : ""}. It is not shown again.`,
  "wifiKey.unset": "Not set. Slips join an open network.",
  "wifiKey.save": "Save password",
  "wifiKey.replace": "Replace password",
  "wifiKey.clear": "Remove password",
  "wifiKey.saved": "Wi-Fi password saved.",
  "wifiKey.cleared": "Wi-Fi password removed. Slips join an open network.",
  "wifiKey.failed": "The Wi-Fi password was not saved",
  "wifiKey.rotates":
    "Slips already printed keep the old password. Change it on the access points first, then here, then print new slips.",
};

void loadLocale(() => import("./vi/wifikey.js"));

export const t = makeT({ ...core, ...wifiKey });
