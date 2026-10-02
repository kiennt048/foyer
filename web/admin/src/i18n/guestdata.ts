/**
 * Copy for `/guest-data`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so an operator
 * who never opens the screen never downloads these sentences. `core.ts` has the rule.
 *
 * The console's own words are English, like the rest of the console. **The consent
 * sentence is not the console's**: it is the venue's, written for its guests in each
 * language the portal serves. The suggested sentence the screen offers is therefore
 * not here either: it is guest-facing text in both languages, and it lives with the
 * portal's templates (`@foyer/portal/src/consent.ts`).
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const guestdata = {
  /* ── what the forms ask for ─────────────────────────────────────────────── */
  "guestData.fields.title": "What guests are asked for",
  "guestData.fields.body":
    "Every sign-in form on the portal carries the fields you choose here — voucher, account, click-through and phone. A field is optional unless you require it — and a guest who leaves a required one blank is not let on. Ask for as little as you need: each one is personal data you have to look after. A guest who signs in with their phone is never asked for their number again: the number they just proved is the one kept.",
  "guestData.field.name": "Name",
  "guestData.field.phone": "Phone number",
  "guestData.field.email": "E-mail address",
  "guestData.field.ask": "Ask for it",
  "guestData.field.require": "Required to get online",

  /* ── marketing consent ──────────────────────────────────────────────────── */
  "guestData.consent.title": "Marketing consent",
  "guestData.consent.body":
    "A separate checkbox, never ticked in advance and never required to get online. Accepting your terms is not consent. Foyer records a consent only when a guest ticks the box — when, and the exact sentence they read — and a guest can withdraw it from the portal at any time.",
  "guestData.consent.enabled": "Ask guests whether they want to hear from you",
  "guestData.consent.text.vi": "Sentence beside the checkbox — Vietnamese",
  "guestData.consent.text.en": "Sentence beside the checkbox — English",
  "guestData.consent.textHint":
    "Say who will contact them, how, and about what. Changing it later does not change what earlier guests agreed to: each version is kept.",
  "guestData.consent.suggest": "Use a suggested sentence",
  "guestData.consent.version": (lang: string, v: string) => `Current ${lang} version: ${v.slice(0, 12)}…`,
  "guestData.save": "Save",
  "guestData.saved": "Saved. The portal asks for this from the next page a guest opens.",
  "guestData.failed": "The setting was not saved",

  /* ── the consents ───────────────────────────────────────────────────────── */
  "guestData.list.title": "Consents given",
  "guestData.list.body":
    "Only guests who ticked the box are here. A withdrawn consent keeps when it was given and withdrawn, and loses the guest's contact details.",
  "guestData.list.empty": "No guest has consented yet.",
  "guestData.list.filter": "Show",
  "guestData.list.all": "All",
  "guestData.list.active": "Active",
  "guestData.list.withdrawn": "Withdrawn",
  "guestData.col.given": "Given",
  "guestData.col.contact": "Contact",
  "guestData.col.text": "Sentence agreed to",
  "guestData.col.state": "State",
  "guestData.state.active": "Active",
  "guestData.state.withdrawn": (when: string, by: string) => `Withdrawn ${when} by ${by}`,
  "guestData.by.guest": "the guest",
  "guestData.by.operator": "an administrator",
  "guestData.cleared": "Cleared on withdrawal",
  "guestData.more": "Show more",
  "guestData.withdraw": "Withdraw…",
  "guestData.withdraw.title": "Withdraw this consent?",
  "guestData.withdraw.verb": "Withdraw",
  "guestData.withdraw.consequence":
    "The guest's name, phone number and e-mail address are removed from the record, and the next export lists it as withdrawn.",
  "guestData.withdraw.recovery":
    "It cannot be given back from here: only the guest can consent again, by ticking the box. The audit log records that you did it.",
  "guestData.withdrawn": "Consent withdrawn.",
  "guestData.list.failed": "The consents could not be loaded",

  "guestData.export.title": "Export consents",
  "guestData.export.body": (days: number) =>
    `A CSV of every consent given or withdrawn in the dates you choose, at most ${plural(days, "day", "days")}. Load its withdrawn rows into your mailing tool as unsubscribes. Every export is recorded in the audit log.`,
  "guestData.export.from": "From",
  "guestData.export.to": "To",
  "guestData.export.go": "Export consents",
  "guestData.export.failed": "The export could not be made",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/guestdata.js"));

export const t = makeT({ ...core, ...guestdata });
