/**
 * Copy for `/operators`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const operators = {
  "operators.add": "Add operator",
  "operators.search": "Search usernames…",
  "operators.searchLabel": "Search operators",

  "operators.empty.title": "No operators.",
  "operators.empty.description": "Nobody can sign in to this console.",

  "operators.col.username": "Username",
  "operators.col.role": "Role",
  "operators.col.status": "Status",
  "operators.col.created": "Created",
  "operators.col.lastLogin": "Last sign-in",
  "operators.noDisplayName": "no display name",
  "operators.enabled": "Enabled",
  "operators.disabled": "Disabled",
  "operators.neverSignedIn": "Never signed in.",

  "operators.action.disable": "Disable",
  "operators.action.enable": "Enable",
  "operators.action.disableWhy":
    "A disabled operator cannot sign in, and any console they have open is signed out at once.",
  "operators.action.enableWhy": "This operator can sign in again from their next attempt.",
  "operators.action.makeAdministrator": "Make administrator",
  "operators.action.makeStaff": "Make staff",
  "operators.action.roleWhy":
    "Staff issue vouchers and look guests up. An administrator also configures the network — access points, shared secrets, the walled garden and the portal.",
  "operators.toast.enabled": (username: string) => `${username} enabled.`,
  "operators.toast.disabled": (username: string) => `${username} disabled.`,
  "operators.toast.role": (username: string, role: string) => `${username} is now ${role}.`,

  // Counted on *enabled* administrators. A disabled one cannot sign in, so a
  // deployment whose only administrator is disabled is already locked out.
  "operators.lastAdmin.title": (username: string) => `${username} is the only enabled administrator`,
  "operators.lastAdmin.body":
    "Disabling, demoting or deleting this account would leave a deployment nobody can administer, and there is no password reset to recover through. Give another operator the administrator role first — these three controls come back the moment one has it.",
  "operators.lastAdmin.why": "This is the only enabled administrator.",

  // Where an administrator looks for the control, said before they look for it. The
  // reasoning is recorded on `OperatorUpdate` in the contract.
  "operators.noReset.title": "There is no password reset",
  "operators.noReset.body":
    "Nobody can set another operator's password. A reset needs a delivery channel — an email sender or a one-time link — and this product has neither, and reading a new password out over whatever channel is to hand is worse than the alternative. If somebody is locked out, disable their account and create another.",

  "operators.delete.title": "Delete operator",
  "operators.delete.verb": "Delete operator",
  "operators.delete.consequence": (username: string) =>
    `${username} can no longer sign in, and any console they have open is signed out at once.`,
  // The one thing that survives, and the reason it does.
  "operators.delete.recovery":
    "Their entries in the audit log stay, and keep their username on them. Disabling is the reversible half of this: an operator who might come back should be disabled instead.",

  "operators.create.title": "Add operator",
  "operators.create.failed.title": "Operator not created",
  "operators.create.username": "Username",
  "operators.create.usernameHint":
    "Cannot be changed later. The audit log records it at the moment of each change, so a rename would leave one person under two names.",
  "operators.create.displayName": "Display name",
  "operators.create.role": "Role",
  "operators.create.roleHint":
    "Staff issue vouchers, look guests up and manage guest accounts. They read the network configuration and change none of it.",
  "operators.create.password": "Initial password",
  "operators.create.passwordHint": (min: number) =>
    `At least ${min} characters. Hand it over in person — that is the only delivery channel there is, and nobody can reset it afterwards.`,
  "operators.create.submit": "Create operator",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/operators.js"));

export const t = makeT({ ...core, ...operators });
