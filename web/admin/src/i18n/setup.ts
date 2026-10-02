/**
 * Copy for the first-access setup, loaded with it.
 *
 * Only a fresh install ever shows this screen, once, so none of it belongs in the
 * entry chunk; `core.ts` has the rule. English, like every console namespace — the
 * console is monolingual by a standing test in `admin.test.ts`.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const setup = {
  "setup.title": "Create the administrator",
  // Why there is no password to type: Foyer ships none, and the code is what proves this is the person who installed it.
  "setup.lede":
    "Foyer ships with no password. Enter the setup code the installer printed, then choose the administrator's username and password.",
  "setup.code": "Setup code",
  "setup.codeHint": "Printed by the installer. Lost it? Run the installer again for a new one.",
  "setup.username": "Username",
  "setup.password": "Password",
  "setup.passwordRule": (min: number) => `At least ${min} characters.`,
  "setup.submit": "Create administrator",
  "setup.failed": "Administrator not created",
  // The one-time rule, said before it is used up.
  "setup.once": "This works once. Afterwards the console only offers sign-in.",
  // The state that is not a form: no administrator, no code. Say what is wrong and
  // what to do, because a sign-in form here cannot succeed and promises a
  // recovery that needs an administrator this install does not have.
  "setup.unready.title": "This installation is not finished",
  "setup.unready.lede":
    "Foyer has no administrator account yet, and no setup code is open, so there is nothing to sign in to.",
  "setup.unready.fix":
    "The installer's first run did not get as far as creating one. Run the installer again on the server: it opens the setup and prints a one-time code. Come back with that code and this screen becomes the form that creates the administrator.",
  "setup.unready.noCode":
    "There is no way to create an administrator from this page. Foyer ships no password and will not make one up for whoever opens it first.",
  "setup.unready.guard": "No way in from this page",
};

/** The shell's copy plus this screen's. */
void loadLocale(() => import("./vi/setup.js"));

export const t = makeT({ ...core, ...setup });
