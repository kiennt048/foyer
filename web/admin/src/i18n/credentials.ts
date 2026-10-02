/**
 * Copy for the two account dialogs — creating one, and changing its password and its
 * 802.1X opt-in — loaded with them. They are a lazy chunk of `/accounts` (see
 * `screens/AccountCredentials.tsx`), so an operator who opens the list to disable
 * somebody never downloads these sentences. English, like every catalogue of the
 * console's own.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const credentials = {
  "accounts.create.failed.title": "Account not created",

  "accounts.create.title": "Add account",
  "accounts.create.username": "Username",
  "accounts.create.displayName": "Display name",
  "accounts.create.password": "Password",
  // Stated where the decision is actually made, not in a settings page.
  "accounts.create.passwordHint":
    "Stored with argon2id. This account cannot be used with PEAP-MSCHAPv2 — that method forces the server to keep an NT hash, and is enabled separately.",
  "accounts.create.submit": "Create account",

  /*
   * The 802.1X opt-in, and its price stated at the point it is paid: the network
   * then verifies against bcrypt, not argon2id.
   * It is made from the password typed in the same dialog, because Foyer keeps no
   * password to make it from later.
   */
  "credentials.dot1x": "Also allow 802.1X (EAP-TTLS/PAP)",
  "credentials.dot1xHint":
    "Stores a second, bcrypt verifier the RADIUS server can check, made from the password above. It is one-way, but it is not argon2id. At most 72 bytes of password; the 802.1X username must be unique across every venue.",

  "credentials.title": (username: string) => `Credentials for ${username}`,
  "credentials.password": "New password",
  "credentials.passwordHint": "Leave empty to keep the current one. Turning 802.1X on needs it.",
  "credentials.submit": "Save credentials",
  "credentials.failed": "The credentials were not changed",
  "credentials.saved": (username: string) => `Credentials for ${username} saved.`,
};

void loadLocale(() => import("./vi/credentials.js"));

export const t = makeT({ ...core, ...credentials });
