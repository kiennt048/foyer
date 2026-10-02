/**
 * Copy for `/password`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const password = {
  "password.current": "Current password",
  // Why it is asked for at all when the browser is already signed in.
  "password.currentHint":
    "The session proves this browser was signed in. It does not prove you are the one at the keyboard.",
  "password.new": "New password",
  // Stated before anything is typed. A length rule an operator can only discover by
  // being refused is a form that rejects work already done.
  "password.rule": (min: number) =>
    `At least ${min} characters. Length beats punctuation — a passphrase of ordinary words is stronger than a short password with symbols in it.`,
  "password.submit": "Change password",
  "password.done": "Password changed.",
  "password.failed": "Password not changed",
  "password.noReset":
    "Nobody can reset this for you. There is no email sender and no one-time link, so an administrator who wanted to help you back in would have to disable this account and create another.",
  "password.signedOut.title": "Not signed in.",
  "password.signedOut.body": "There is no password to change until you are.",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/password.js"));

export const t = makeT({ ...core, ...password });
