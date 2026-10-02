/**
 * The console's shell copy: everything a browser needs before a route resolves, and
 * everything the screens that are *not* lazily loaded say.
 *
 * English, and the source of truth. The Vietnamese is `vi/core.ts`, a total
 * translation fetched only when the console speaks Vietnamese (`lang.ts`), and
 * installed under this catalogue by `loadLocale` before the first render.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why this is nine files and not one.**
 *
 * As one object, every sentence would ship in the entry chunk — including the copy
 * for the screens that are `lazy()` precisely so an operator does not download them.
 * About half of the console's copy belongs to screens most operators never open, and
 * carrying it in the entry would put the entry at the edge of the budget in
 * `tools/check-admin-budget.mjs`, arriving before the dashboard renders.
 *
 * A single object cannot be split by a bundler: Rollup drops unused *modules*, not
 * unused *properties*, and `t()` was typed as `keyof` the whole thing. So the split
 * is a source split, into the shape the field already uses — **i18next namespaces,
 * Lingui per-route catalogues, react-intl's "split translations by route"**: the copy
 * a screen needs is a module that screen imports, and it travels in that screen's
 * chunk.
 *
 * Where this diverges from all three: they load a namespace *asynchronously at
 * runtime* through a backend, and pay for it with a missing-key fallback, a loading
 * state and a registry. None of that is here. A namespace is a plain ES module that
 * the screen imports statically, so the bundler co-locates it in the chunk the router
 * is already fetching — the copy cannot arrive late because it arrives *as* the
 * screen. That is available to Foyer and not to them because the console's language
 * is fixed for a page's life (`lang.ts`): there is nothing to choose at runtime.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The rule for where a key goes**, and it is about bytes, not about topic:
 *
 * - Used by anything in the entry chunk — the shell, `format.ts`, `problems.ts`, or
 *   an eagerly loaded screen — it belongs **here**, whatever it is called. The
 *   access-point *list* is eager, so `nas.col.*` is here while `nas.rotate.*` is in
 *   `nas.ts`.
 * - Used by exactly one lazy feature, it belongs in that feature's namespace.
 * - Used by two different lazy features, it belongs **here**: a module imported from
 *   two chunks becomes a third chunk, and a third request is worse than the bytes.
 *
 * A key in the wrong place is not a silent cost. Put a screen's key here and nothing
 * breaks but the entry grows; put a shell key in a namespace and **the shell stops
 * compiling**, because `index.ts`'s `t` cannot see it.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * House rules, from a survey of Stripe, Cloudscape, PatternFly and GitLab. They apply
 * to every namespace, and `admin.test.ts` enforces three of them across all nine:
 *
 * - An empty-state **title** states what is missing, is a short phrase, and ends
 *   with a period — `No access points.`
 * - An empty-state **description** says when or how the data appears, under 14
 *   words, active voice.
 * - **Action text mirrors the title**: `No access points.` → `Add access point`.
 * - **Errors name the next action.** Never `An error occurred`.
 * - Never `Oops`, `Sorry`, `Please`, or `successfully`. Confirm completion instead:
 *   `Account created`.
 * - **Tooltips explain why, not what**, in one sentence.
 * - Page descriptions are declarative fragments with **no trailing period**.
 * - Title case for main navigation; sentence case for in-page headings.
 * - A destructive dialog's confirm button repeats the verb — `Delete access point`,
 *   never `Yes` or `OK`.
 *
 * The technical sentences here are load-bearing and were checked against
 * `docs/rules/`. They are not decoration and must not be softened: an operator
 * who does not know that rotating a shared secret drops every in-flight EAP
 * conversation will rotate one at 09:00 on a Monday.
 */

import { loadLocale, plural } from "./catalogue.js";

export const core = {
  /* ── shell ────────────────────────────────────────────────────────────────── */

  // Title case: this is main navigation. In-page headings are sentence case, which
  // is why "Access Points" here and "Access points" on the page itself.
  "nav.dashboard": "Dashboard",
  "nav.sessions": "Sessions",
  "nav.accessPoints": "Access Points",
  "nav.vouchers": "Vouchers",
  "nav.accounts": "Accounts",
  "nav.controllers": "Controllers",
  "nav.portal": "Portal",
  "nav.attempts": "Sign-in Attempts",
  "nav.garden": "Walled Garden",
  "nav.guestData": "Guest Data",
  // The two deployment-wide destinations. Every other row is this site's.
  "nav.operators": "Operators",
  "nav.audit": "Audit Log",
  "nav.privacy": "Privacy",
  "nav.network": "Network",

  "shell.density.toCompact": "Compact rows",
  "shell.density.toComfortable": "Comfortable rows",
  "shell.commands": "Commands",
  "shell.language": "Language",
  "shell.language.unsaved": "This browser cannot keep the choice — a private window, or site data is blocked.",

  /* ── the console's own identity ───────────────────────────────────────────
   *
   * A role is a *label* in this console and gates nothing — every control is gated
   * on `AuthSession.permissions`. These two exist because an administrator assigns
   * a role and has to read the word they are assigning.
   */
  "operator.role.administrator": "Administrator",
  "operator.role.staff": "Staff",

  "signOut": "Sign out",

  "signIn.title": "Sign in",
  "signIn.username": "Username",
  "signIn.password": "Password",
  "signIn.submit": "Sign in",
  // Where somebody looks for "forgot your password", said before they look. The
  // reasoning is on `OperatorUpdate` in the contract: a reset needs a delivery
  // channel and this product has none.
  "signIn.noReset":
    "There is no password reset. If you cannot sign in, an administrator disables this account and creates another one for you.",

  "password.title": "Change password",
  "password.description": "The password for the account you are signed in to",

  /*
   * ── operators · header only; the screen's copy is in `operators.ts` ────────
   *
   * The five sections that look like stubs from here are not truncated. Each names a
   * lazily loaded screen, and `App.tsx` renders the page header *outside* the
   * `Suspense` boundary — so the title and the description are on screen while the
   * chunk is still arriving, and they are the two keys that therefore cannot travel
   * with it. Everything else the screen says is in its own namespace.
   */

  "operators.title": "Operators",
  "operators.description": "The people who can sign in to this console",

  /* ── the audit log · header only; the screen's copy is in `audit.ts` ──────── */

  "audit.title": "Audit log",
  "audit.description": "What was changed on this deployment, by whom, and when",

  /* ── privacy · header only; the screen's copy is in `privacy.ts` ─────────── */

  "privacy.title": "Privacy",
  "privacy.description": "How long guests' personal data is kept, and erasing one person's",

  /* ── guest data · header only; the screen's copy is in `guestdata.ts` ── */
  "guestData.title": "Guest data",
  "guestData.description": "What this venue asks its guests for at sign-in, and the marketing consents they gave",
  /* ── network · header only; the screen's copy is in `network.ts` ─────────── */
  "network.title": "Network",
  "network.description": "The portal's address and certificate, sign-in limits, and FreeRADIUS",

  // The narrow header's navigation control, and the drawer's own way out. A word
  // rather than three lines and no text: this console carries no hamburger glyph,
  // and the button is also the drawer's accessible name, where "Menu" is the noun a
  // screen reader should announce. `aria-expanded` carries the state, so the label
  // does not — a button that renames itself under the finger is a button an operator
  // has to re-read to press twice.
  "shell.menu": "Menu",
  "shell.menuClose": "Close menu",

  // The dialog's name and the listbox's, for an overlay with no visible heading —
  // the noun, not the instruction, because it is announced as "Commands, dialog".
  "palette.title": "Commands",
  "palette.placeholder": "Search commands…",
  "palette.none": "No commands match.",
  "palette.goTo": (screen: string) => `Go to ${screen}`,
  "palette.navigate": "Navigate",
  "palette.action": "Action",
  "palette.switchSite": (site: string) => `Switch to ${site}`,

  /* ── keyboard ─────────────────────────────────────────────────────────────── */

  // Every one of these is drawn somewhere visible. A shortcut that exists only in a
  // keydown handler is a shortcut nobody uses, so the catalogue and the hints under
  // the table are generated from one declaration — see `keys/bindings.ts`.
  "keys.group.global": "Anywhere",
  "keys.group.navigation": "Go to",
  "keys.group.list": "In a list",
  "keys.group.detail": "Detail panel",
  "keys.group.filter": "Filters",

  "keys.listNext": "Next row",
  "keys.listPrevious": "Previous row",
  "keys.listOpen": "Open detail",
  "keys.listToggle": "Select row",
  "keys.listExtendDown": "Extend selection down",
  "keys.listExtendUp": "Extend selection up",
  "keys.detailClose": "Close detail",
  "keys.focusSearch": "Search this list",

  "keys.cap.space": "Space",
  "keys.cap.enter": "Enter",
  "keys.cap.esc": "Esc",
  "keys.cap.then": "then",
  "keys.cap.or": "or",

  "keys.sheet.title": "Keyboard shortcuts",
  "keys.sheet.search": "Search shortcuts…",
  "keys.sheet.none": "No shortcuts match.",
  // Explains why the sheet is shorter on some screens than others, which is
  // otherwise read as a bug.
  "keys.sheet.note": "This list is what the current screen registers, not a fixed table.",
  "keys.shortcuts": "Shortcuts",

  /* ── site switcher ────────────────────────────────────────────────────────── */

  "site.label": "Site",
  "site.failed": "Cannot load sites",
  "site.choose": "Choose a different site",
  "site.env.live": "Live",
  "site.env.lab": "Lab",
  /* Two states the switcher must be able to say. `site.none` is a request that
     succeeded and was answered *none*; rendering it as `site.failed` would describe a
     different event entirely. */
  "site.none": "No venue yet",
  "site.unknown": "Unknown venue",

  /* ── what a venue-scoped screen says when there is no venue to scope it to ─── */

  "site.gate.noneTitle": "No venue yet",
  /* Two sentences: the state, and the next act, which is the form below it. A
     description that names the state without naming the next act tells an operator
     on a fresh deployment, clearly, that they are stuck. */
  "site.gate.noneBody":
    "Every access point, voucher, account and session belongs to a venue, and this deployment has none yet. Name it to begin.",
  "site.gate.unknownTitle": "Venue not in this deployment",
  "site.gate.unknownBody": "Choose a venue below, or correct the address.",
  // The id is rendered, not hinted at: an operator correcting a bookmark needs to see
  // the string that is wrong. A `title` attribute would be a fact nothing can read.
  "site.gate.unknownNamed": (id: string) =>
    `The address asks for “${id}”, and no venue here has that id. It may have been removed, or the link may be older than this deployment.`,

  /* ── naming the first venue ───────────────────────────────────────────────────
   *
   * In `core.ts` and not in a namespace, and that is the byte rule rather than the
   * naming one: `docs/rules/frontend.md` puts a key where the chunk that reads it
   * is, and `CreateVenue` is rendered by `SiteGate`, which is in `App.tsx` and
   * therefore in the entry. A `site.ts` namespace for these would be a module
   * imported from the entry chunk, which is a third request for six strings.
   *
   * **The price is stated before the operator commits.** One venue per install, so a
   * form that lets them discover the limit by being refused is the shape the same
   * rules file rejects for capability-gated controls. `nameHint` says it while they
   * are choosing the name; `problem.one-site-per-install.*` — already in this
   * catalogue, and written for this screen before it existed — says it afterwards on
   * the one path where somebody else got there first.
   */
  "site.create.name": "Venue name",
  "site.create.nameHint":
    "The name of the place these guests are connecting from — it labels every access point, voucher and session in this console. Foyer holds one venue per installation, so this is the only one you will name here.",
  "site.create.submit": "Create venue",
  // What pressing it leads to. The two first-run surfaces are one flow and this is
  // the sentence that says so, before the operator has committed to the first step.
  "site.create.next":
    "Next, the console will ask you to register the access point that serves it.",
  "site.create.failed.title": "Venue not created",
  // Only rendered for the `409`, which means a venue now exists — so this is not an
  // apology, it is the way out. Without it a refused form is a dead end on the one
  // screen that exists to lead out of one.
  "site.create.refused.continue": "Open the venue this deployment has",

  /* ── shared vocabulary ────────────────────────────────────────────────────── */

  "common.cancel": "Cancel",
  "common.close": "Close",
  "common.delete": "Delete",
  "common.undo": "Undo",
  "common.dismiss": "Dismiss",
  "common.view": "View",
  "common.yes": "Yes",
  "common.no": "No",
  "common.unknown": "unknown",
  "common.loading": "Loading…",
  "common.copy": (subject: string) => `Copy ${subject}`,
  "common.filterBy": (subject: string) => `Filter by this ${subject}`,
  "common.typeToConfirm": (name: string) => `Type ${name} to confirm`,

  /* ── table ────────────────────────────────────────────────────────────────── */

  // Names an action the operator can actually take. A retry control belongs here
  // and does not exist yet — until it does, the copy must not pretend otherwise.
  //
  // **The fallback, not the headline.** `ui/table.tsx` renders the refused type's own
  // label where `problems.ts` recognises it, because *reload and try again* is wrong
  // advice for a permanent refusal — a staff session's `403` on `/operators` reloads
  // into the same `403`. This is what a type newer than this console
  // gets, which is the one case where reloading is still worth suggesting.
  /*
   * **One voice for a failure, and the dashboard's is the model.**
   *
   * This read *"Could not load this data. Reload the page to try again."* — past
   * tense, two sentences, and an instruction inside a **title**, sitting directly
   * above a body carrying a *different* instruction from the type's own catalogue
   * entry. Beside it, `dashboard.alarmsFailed` and `garden.unknown` both name what is
   * unknown in the present tense and leave the instruction to the body.
   *
   * So: name what is unknown, and nothing else. The sentence under it belongs to the
   * refusal, and there is exactly one of it.
   */
  "table.error": "Cannot load these rows",
  "table.noMatches": "No rows match these filters.",
  "table.clearFilters": "Clear filters",
  "table.empty.title": "No data.",
  "table.rows": (n: number) => plural(n, "row", "rows"),
  // Not "0 rows". A count that has not arrived is not a count of zero, and zero is
  // the number an operator would act on.
  "table.rowsUnavailable": "Row count not loaded",
  // Spoken, not read: the table's live region, after a filter, a clear, or a refetch
  // that changed how many rows there are. A count and nothing else — it interrupts
  // whatever is being read, so it says the one thing that changed and stops.
  "table.rowsShown": (n: number) => `${plural(n, "row", "rows")} shown`,
  /*
   * What is past the right-hand edge of a table too wide for its port.
   *
   * `.table-scroll` absorbs the overflow so the *page* never scrolls, and macOS
   * overlay scrollbars are invisible until a gesture — so a card's right border can
   * read as the end of the row while columns are simply not there. The count and the
   * names are both here because the names are the point: on `/sessions` the column
   * that falls off can be the one dating every byte figure on the row.
   */
  "table.hiddenColumns": (n: number) =>
    `${plural(n, "column", "columns")} further right — scroll the table to read:`,
  // The other half of "refresh never destroys the view": rows may stay on screen
  // only because something says how old they are.
  "table.lastUpdated": (absolute: string) => `Last updated ${absolute}`,
  "table.refreshFailed": "The most recent refresh failed. These rows are older than the time above.",
  // The narrow layout's disclosure control. It names the row rather than saying
  // "Show more", because fifteen buttons with one name is a list a screen-reader
  // user cannot navigate — and it stays the same phrase when open, since
  // `aria-expanded` already carries the state and a name that changes under the
  // cursor is announced as a different control.
  "table.moreFields": (n: number, row: string) => `${plural(n, "more field", "more fields")} for ${row}`,

  /* ── units and time ───────────────────────────────────────────────────────── */

  "unit.days": (n: number) => plural(n, "day", "days"),
  "unit.hours": (n: number) => plural(n, "hour", "hours"),
  "unit.minutesShort": (n: number) => `${n} min`,
  "unit.minutes": (n: number) => plural(n, "minute", "minutes"),

  "time.justNow": "just now",
  "time.soon": "in a moment",
  "time.minutesAgo": (n: number) => `${plural(n, "minute", "minutes")} ago`,
  "time.inMinutes": (n: number) => `in ${plural(n, "minute", "minutes")}`,
  "time.hoursAgo": (n: number) => `${plural(n, "hour", "hours")} ago`,
  "time.inHours": (n: number) => `in ${plural(n, "hour", "hours")}`,
  "time.daysAgo": (n: number) => `${plural(n, "day", "days")} ago`,
  "time.inDays": (n: number) => `in ${plural(n, "day", "days")}`,

  /* ── freshness ────────────────────────────────────────────────────────────── */

  // The sentence that travels with every accounting-derived number. `radacct`
  // advances only on Interim-Update or Stop, `Acct-Interim-Interval` has a
  // 300-second floor, and on the controller-API path there is no Access-Accept in
  // which to ask for updates at all.
  "freshness.live": "Enforced by the network device itself, not sampled.",
  "freshness.sampled": (every: string) =>
    `Sampled every ${every}. Figures can be up to ${every} behind.`,
  "freshness.sampledUnknown": "Sampled periodically, not measured live.",
  "freshness.stale": (every: string) =>
    `No update received for two ${every} intervals. These figures are old.`,
  "freshness.staleUnknown": "No update received for two intervals. These figures are old.",
  /*
   * The distinction the whole design turns on, stated as a mechanism and then a
   * consequence.
   *
   * It read "Not measurable — not unlimited" for a long time, and that is a negation
   * pair with no positive statement in it: the reader holds two "not"s and has to
   * infer the true state from what it is not. It also answers a question nobody
   * asked — somebody looking at a blank cell thinks *broken*, not *unlimited* — and
   * "accounting" is a term of art wearing an everyday word's clothes, which a
   * Vietnamese reader reasonably parses as bookkeeping.
   *
   * The rule for anything else on this screen: **say the mechanism, then say what it
   * means operationally.**
   */
  "freshness.absent":
    "This device sends no accounting. Foyer cannot tell how much this client has used — and cannot cut them off.",
  "freshness.qualifier.absent": "no data",
  "freshness.qualifier.stale": "stale",

  "freshness.noAccounting": "This device sends no accounting.",
  "freshness.clockSkew": "clock skew",

  /* ── the 32-bit counter wrap ──────────────────────────────────────────────── */

  // `Acct-Input-Octets`/`Acct-Output-Octets` are 32-bit (RFC 2866 §5.3–5.4) and a
  // NAS that omits Gigawords (RFC 2869 §5.1–5.2) reports a total that drops by
  // 4 GiB every 4 GiB. Foyer reconstructs a floor and says it is one — two wraps
  // between two accounting records are indistinguishable from one, and 4 GiB inside
  // the 300-second interim floor is 114 Mbps, which a hotel uplink reaches.
  "bytes.atLeast": (value: string) => `at least ${value}`,

  /* ── dashboard ────────────────────────────────────────────────────────────── */

  "dashboard.title": "Dashboard",
  "dashboard.clientsOnline": "Clients online",
  "dashboard.vouchersToday": "Vouchers redeemed today",
  "dashboard.accessPoints": "Access points",
  "dashboard.nasHealthy": "All reachable",
  // The third branch, and the one an operator meets first. `0/0` under "All
  // reachable" would be this console's own thesis failing on its landing screen: an
  // estate of nothing is not a healthy estate, and a brand-new deployment must not be
  // told its access points are fine. Says what the figure means, not what to do about
  // it — the first-run panel above it is what says that.
  "dashboard.nasNone": "Nothing is serving guests yet",
  "dashboard.nasUnhealthy": (n: number) =>
    n === 1 ? "1 device needs attention" : `${n} devices need attention`,
  "dashboard.orphaned": "Orphaned sessions",
  "dashboard.orphanedFoot": "No stop record",
  "dashboard.orphanedUnknown": "This server did not report the figure",
  /*
   * The orphan count dates itself.
   *
   * `Sampled 40 days ago` — `metricFoot`'s sentence — reads on this dashboard as
   * *this count is forty days behind*, which is what it means one card to the left
   * and is false here: the count is exact, and what is old is the sessions. So the
   * line names the subject rather than the sample, and it is the figure worth
   * having, because ten minutes and forty days are the same integer and different
   * faults.
   */
  "dashboard.orphanedOldest": (relative: string) => `Oldest last heard ${relative}`,
  // Mechanism, then what it means operationally — the rule `freshness.absent`
  // carries. The second sentence is the one that must not be `freshness.stale`'s:
  // "These figures are old" is a claim about the number, and the number is current.
  "dashboard.orphanedNote":
    "These sessions have no stop record and no recent update. The count is current — what is old is the sessions themselves.",
  "dashboard.needsAction": "Needs action",
  "dashboard.nothingToDo": "Nothing needs action.",
  // The fourth branch of the alarm section. An empty list and an unanswered request
  // are opposite facts, and this console rendered a green tick for both.
  "dashboard.alarmsFailed.title": "Cannot load what needs action",
  "dashboard.alarmsFailed.body":
    "Foyer cannot reach its own backend, so this list is not current and nothing is being watched on your behalf. Read the empty space as unknown — never as nothing to do.",
  /*
   * One finding, one card, and the devices inside it.
   *
   * Grafana's alerting guidance says it plainly: *"Paging separately for each
   * symptom quickly turns into notification spam, even though there is a single
   * root cause. Notification grouping consolidates related alerts into a single
   * notification."* A venue with six access points that were all registered by
   * hand and none of which supports a walled garden is ONE fault, and rendered
   * per device it would be six cards carrying six near-identical sentences.
   *
   * **The alarms stay per-device.** The server's alarm carries a subject with an
   * id, and that link to the one access point that is at fault is the reason the
   * model is per-object at all; grouping must not cost it. So the names are named
   * *inside* the card, each one still a link to its own page. Nothing is merged
   * in the data, and nothing an operator needs to act on is lost.
   */
  "dashboard.alarmGroupCount": (count: number) =>
    count === 1 ? "1 access point" : `${count} access points`,
  "dashboard.alarmGroupOf": "All of:",
  "dashboard.alarmMore": (shown: number, total: number) => `and ${total - shown} more`,

  /* ── first run ────────────────────────────────────────────────────────────── */

  /*
   * The design's first-run sentence, in the console: *an operator with one access point
   * creates a voucher, and a phone gets online.*
   *
   * The steps are short because the screens they point at already carry the detail —
   * `nas.wizard.controllerOnly` names which vendors are discovered rather than typed,
   * `controllers.create.apiKeyHint` names the menu path to a UniFi API key. Repeating
   * either here would be a second copy that drifts.
   *
   * The `outside` block is the part the design says the five minutes actually go to, and the
   * part a first-run flow usually pretends does not exist. None of it is automatable
   * and all of it is invisible from inside the console, so it is stated rather than
   * offered — and each line is shown only where it applies.
   */
  "firstRun.title": "Get this site online",

  "firstRun.device.title": "No access point serves this site.",
  "firstRun.device.body":
    "Nothing sends a guest to the portal yet. UniFi, Omada and Ruckus devices are read from their controller; MikroTik, Cisco and Aruba are registered directly.",
  // Mirrors the title, as every action label in this catalogue does. It leads to the
  // register dialog, which is where the console already says which vendors are
  // discovered from a controller rather than typed in here.
  "firstRun.device.action": "Add access point",
  "firstRun.device.done": (n: number) =>
    n === 1 ? "1 access point registered." : `${n} access points registered.`,

  "firstRun.codes.title": "No codes to hand out.",
  "firstRun.codes.body":
    "The portal asks guests for a voucher, so there has to be a batch before anybody can sign in.",
  "firstRun.codes.action": "Create batch",
  "firstRun.codes.done": "Codes are ready to hand out.",

  // Only for a portal that offers accounts and neither vouchers nor terms-only entry.
  "firstRun.account.title": "No account a guest can sign in with.",
  "firstRun.account.body":
    "The portal asks guests for a username and password, and this site has neither vouchers nor terms-only entry.",
  "firstRun.account.action": "Add account",

  "firstRun.outside.title": "What Foyer cannot do for you",
  "firstRun.outside.ssid":
    "The access point has to be told to send its guests here. That is done in the device's or the controller's own interface, never from Foyer.",
  "firstRun.outside.apiKey":
    "A controller needs an API key, and it is issued in the controller's own interface. Foyer cannot create one.",
  "firstRun.outside.dns":
    "A guest must be able to resolve the portal's address and trust its certificate before they sign in. Foyer cannot check either from here.",
  "firstRun.outside.dnsAt": (url: string) =>
    `A guest must be able to resolve ${url} and trust its certificate before they sign in. Foyer cannot check either from here.`,
  // Capability-derived, and only where every device in the site reports
  // `walledGarden: none`. Saying "add it to the walled garden" to that operator is
  // advice that does nothing.
  "firstRun.outside.noGarden":
    "No device here can hold a guest to a list of destinations before they sign in, so whatever lets them reach the portal is configured on the device itself.",
  // `url-redirect-acl` names an ACL that must already exist on the NAS. Shown for a
  // CoA device that is Cisco and for one whose vendor nobody has stated.
  "firstRun.outside.ciscoAcl":
    "A Cisco redirect names an access list that must already exist on the device. Foyer sends the name, never the list.",

  "metric.live": "Live figure",
  "metric.absent": "No data",
  // A figure that did not arrive says so, rather than holding a skeleton for ever
  // under a caption describing a number nobody has.
  "metric.unavailable": "Not available — the backend did not answer",
  "metric.sampled": (relative: string) => `Sampled ${relative}`,
  "metric.sampledEvery": (relative: string, every: string) =>
    `Sampled ${relative}, every ${every}`,

  /* ── access points ────────────────────────────────────────────────────────── */

  "nas.title": "Access points",
  "nas.description":
    "Devices are read from the database, not from a config file. Adding one is a write and interrupts no session",
  "nas.add": "Add access point",
  "nas.search": "Search name, address, identifier…",
  "nas.searchLabel": "Search access points",

  "nas.empty.title": "No access points.",
  "nas.empty.description": "Register the first device to start serving Wi-Fi to guests.",

  "nas.col.name": "Name",
  "nas.col.status": "Status",
  "nas.col.messageAuthenticator": "Message-Authenticator",
  "nas.col.garden": "Walled garden",
  "nas.col.accounting": "Accounting",
  "nas.col.address": "Address",
  "nas.col.lastSeen": "Last contact time",

  "nas.driver.unifi": "UniFi (controller API)",
  "nas.driver.mikrotik": "MikroTik (CoA)",
  "nas.driver.coa": "Cisco / Aruba (CoA)",
  "nas.driver.ruckus": "Ruckus (Northbound API)",
  "nas.driver.omada": "Omada (controller API)",

  /* The vendor without the mechanism, for prose. `A UniFi access point…` reads as
     English where `A UniFi (controller API) access point…` does not — see
     `drivers.ts`, which is the one place both forms are declared. */
  "nas.vendor.unifi": "UniFi",
  "nas.vendor.mikrotik": "MikroTik",
  "nas.vendor.coa": "Cisco or Aruba",
  "nas.vendor.ruckus": "Ruckus",
  "nas.vendor.omada": "Omada",

  /* The two vendors behind the one driver kind that names a family. Bare names: the
     mechanism is already in `nas.driver.coa`, and this pair is read inside a field
     that has just said CoA. */
  "nas.coaVendor.cisco": "Cisco",
  "nas.coaVendor.aruba": "Aruba",

  "nas.garden.none": "Not supported",
  "nas.garden.ipRange": "By IP range",
  "nas.garden.fqdnSni": "By FQDN/SNI",
  // Not a stylistic preference: CDNs host arbitrary customers, so an IP-range
  // allowance degrades into a general-purpose HTTPS tunnel.
  "nas.garden.ipRangeWhy":
    "Materially weaker than FQDN/SNI: one CDN IP range serves many unrelated customers.",

  "nas.reach.ok": "Reachable",
  "nas.reach.unreachable": "Unreachable",
  "nas.reach.credentialsExpired": "API key expired",
  "nas.reach.unknown": "Not probed",

  "nas.msgauth.enforced": "Enforced",
  "nas.msgauth.refused": "Refused",
  "nas.msgauth.unknown": "Unknown",
  "nas.msgauth.refusedWhy": "This device is exposed to BlastRADIUS (CVE-2024-3596).",

  "nas.accounting.configured": "Configured",
  "nas.accounting.missing": "Not attached",
  "nas.accounting.missingWhy":
    "No session records arrive from this device. Missing accounting means not measurable, not unlimited.",
  /* The third value, and it is not a softer way of saying the second. `Not attached`
     is a measurement — every session figure for this device is absent forever — and
     it may only be rendered where something measured it. A registered device nobody
     has probed has an absent `accountingConfigured`, and this is what that reads as.
     The two send an operator to different afternoons: one to a controller's
     accounting profile, the other to the Probe button. */
  "nas.accounting.notMeasured": "Not measured",
  "nas.accounting.notMeasuredWhy":
    "Nothing has probed this device, so whether accounting is attached has never been established. It is not a report that accounting is missing.",

  "nas.address.absent": "Not configured.",
  "nas.lastSeen.absent": "Never reached.",

  /* ── alarms ───────────────────────────────────────────────────────────────── */

  /*
   * One title and one body per `AlarmKind`. This catalogue is the only place these
   * sentences exist — the server sends the kind and the params, never the prose.
   *
   * They are consequences, not mechanisms. "The device refuses Message-Authenticator"
   * is what happened; "an Access-Accept can be forged out of a genuine reject" is
   * what the operator is being asked to care about, and it is the half that decides
   * whether they act today or next quarter.
   */
  "alarm.nas-unreachable.title": "Cannot reach the access point",
  "alarm.nas-unreachable.body":
    "Guests on this device cannot be signed in or signed out until contact is restored. Sessions already running are unaffected.",

  "alarm.nas-credentials-expired.title": "The controller's API key has expired",
  "alarm.nas-credentials-expired.body":
    "Not a network fault — the device is reachable and the key is not accepted. Issue a new one on the controller and update it here.",

  "alarm.nas-message-authenticator-refused.title": "The device refuses Message-Authenticator",
  "alarm.nas-message-authenticator-refused.body":
    "An Access-Accept can be forged out of a genuine reject on this device (BlastRADIUS, CVE-2024-3596). Foyer will not quietly downgrade to keep it working.",

  "alarm.nas-no-walled-garden.title": "No walled garden on this device",
  "alarm.nas-no-walled-garden.body":
    "This driver cannot restrict traffic before sign-in, so a guest on it reaches the whole internet without signing in.",

  "alarm.nas-no-accounting.title": "No accounting profile attached",
  "alarm.nas-no-accounting.body":
    "No session records will arrive from this device. Missing accounting means unmeasured, never unlimited.",

  "alarm.sessions-orphaned.title": (count: number) =>
    `${plural(count, "session has", "sessions have")} no stop record`,
  "alarm.sessions-orphaned.body":
    "The access point most likely restarted without sending Acct-Stop. Nothing closes these until the reaper does.",

  "alarm.sessions-stale.title": (count: number) =>
    `${plural(count, "session is", "sessions are")} overdue for an update`,
  "alarm.sessions-stale.body":
    "No Interim-Update received for two intervals. The figures for these are old.",

  // Only reached when the server raises a kind this console predates. Deliberately
  // says so rather than pretending to understand it.
  /*
   * Not a reachability alarm, and it must not be worded as one. The controller
   * answers and the credential is accepted; what is missing is Foyer's confidence
   * that this driver can drive *that version*. On a controller-API vendor that is
   * the whole authorization path — UniFi's captive-portal authorization has no
   * RADIUS equivalent — so RADIUS keeps answering, every access point under it stays
   * reachable, and no guest is let on.
   */
  "alarm.controller-version-unsupported.title": "Foyer is not tested against this controller's version",
  "alarm.controller-version-unsupported.body": (version: string, minimum: string) =>
    `It reports ${version}; Foyer is tested against ${minimum} and newer. The controller answers and its credential works — what is missing is any assurance a guest can be authorized through this version, which on this vendor is the whole path onto the network.`,

  "alarm.unknown.title": "An alarm this console does not recognise",
  "alarm.unknown.body":
    "The server raised something newer than this console. Update the console to see what it is.",

  /* ── controllers · header only; the screen's copy is in `controllers.ts` ──── */

  "controllers.title": "Controllers",
  "controllers.description":
    "The thing you actually own. Access points are read from it, never typed in here",

  /* ── portal settings · header only; the screen's copy is in `portal.ts` ───── */

  "portal.title": "Portal",
  "portal.description": "What a guest sees before they sign in, and what it costs them to load",

  /* ── sign-in attempts · header only; the screen's copy is in `attempts.ts` ─── */

  "attempts.title": "Sign-in attempts",
  "attempts.description":
    "Every attempt to get online, including the ones that failed — and what this cannot see",

  /* ── walled garden · header only; the screen's copy is in `garden.ts` ─────── */

  "garden.title": "Walled garden",
  "garden.description":
    "What a guest may reach before they sign in — and which devices actually enforce it",

  /* ── sessions · header; the screen's and the dock's copy is in `sessions.ts` ── */

  "sessions.title": "Sessions",
  "sessions.description":
    "Accounting data belongs to FreeRADIUS and is read-only. Figures are sampled, never live",

  // Here because the byte cell in `ui/table.tsx`, which the dashboard also renders,
  // says it.
  "sessions.wrapped.why": (reported: string) =>
    `The reported counter is ${reported}, and it has gone down at least once — a 32-bit wrap with no Gigawords. Foyer adds 4 GiB per wrap it saw, but two wraps between two updates look like one, so this is a floor.`,

  /* ── bulk selection ───────────────────────────────────────────────────────── */

  "bulk.selected": (n: number) => (n === 1 ? "1 selected" : `${n} selected`),
  "bulk.selectAll": "Select every row on screen",
  "bulk.clear": "Clear selection",
  // Stated where the selection is, because the reset looks like a bug otherwise.
  "bulk.resetNote": "Changing a filter clears the selection, so it can never hold a row you cannot see.",
  "bulk.selectAccount": (username: string) => `Select account ${username}`,
  "bulk.selectEntry": (value: string) => `Select destination ${value}`,
  /* Progress, on the control that started it. A batch is one request per row, so
     "working" with no number is indistinguishable from a hang at row four of forty. */
  "bulk.running": (done: number, total: number) => `${done} of ${total}…`,

  "bulk.accounts.disable": (n: number) => (n === 1 ? "Disable 1" : `Disable ${n}`),
  "bulk.accounts.enable": (n: number) => (n === 1 ? "Enable 1" : `Enable ${n}`),
  "bulk.accounts.disabled": (n: number) => (n === 1 ? "1 account disabled" : `${n} accounts disabled`),
  "bulk.accounts.enabled": (n: number) => (n === 1 ? "1 account enabled" : `${n} accounts enabled`),
  /* Said beside the pair, because the pair is the undo. There is no batch delete and
     this is where an operator finds out why they are not missing one. */
  "bulk.accounts.note": "Disabling is reversible — Enable puts the same rows back.",

  "bulk.garden.remove": (n: number) => (n === 1 ? "Remove 1" : `Remove ${n}`),
  "bulk.garden.confirm.title": "Stop allowing these destinations?",
  "bulk.garden.confirm.verb": "Stop allowing them",
  "bulk.garden.confirm.consequence": (n: number) =>
    n === 1
      ? "1 destination stops being reachable before a guest signs in."
      : `${n} destinations stop being reachable before a guest signs in.`,
  "bulk.garden.confirm.recovery": "Each can be added again, and takes effect on the next change.",

  /* ── what happened to the ones that did not go through ────────────────────── */

  "bulk.failed.title": (failed: number, total: number) => `${failed} of ${total} did not go through`,
  "bulk.failed.body": "These are still selected, so the same control retries exactly them.",
  "bulk.noted.body": (n: number) =>
    n === 1
      ? "The code is dead. The guest is still online — retrying does not change that."
      : "The codes are dead. The guests are still online — retrying does not change that.",

  /* ── vouchers · header; the screen's copy is in `vouchers.ts`, the slip's in `print.ts` */

  "vouchers.title": "Vouchers",
  "vouchers.description":
    "Vouchers are created and managed in batches — the unit staff actually work in",

  /* ── accounts ─────────────────────────────────────────────────────────────── */

  "accounts.title": "Accounts",
  "accounts.description": "Local accounts for the captive portal and for 802.1X",
  "accounts.add": "Add account",
  "accounts.search": "Search usernames…",
  "accounts.searchLabel": "Search accounts",

  "accounts.empty.title": "No accounts.",
  "accounts.empty.description": "Local accounts serve long-stay guests and staff.",

  "accounts.col.username": "Username",
  "accounts.col.status": "Status",
  "accounts.col.credential": "Password storage",
  "accounts.col.created": "Created",
  "accounts.col.lastLogin": "Last sign-in",
  "accounts.noDisplayName": "no display name",
  "accounts.enabled": "Enabled",
  "accounts.disabled": "Disabled",
  "accounts.neverSignedIn": "Never signed in.",

  "accounts.credential.argon2id": "argon2id",
  "accounts.credential.nt": "argon2id + NT hash",
  // Password-equivalent, not "a hash". It is the sole input to the MSCHAPv2
  // response, so a stolen hash authenticates directly.
  "accounts.credential.ntWhy":
    "PEAP-MSCHAPv2 is enabled here, so the server must keep an NT hash. An NT hash is password-equivalent: a stolen one authenticates directly, without being cracked.",
  /*
   * The scheme is named, and naming it IS the point.
   *
   * The price must be written where it cannot be missed: an identity opted in to 802.1X authenticates against a
   * **bcrypt** verifier, not argon2id. Measured — `rlm_pap` in FreeRADIUS 3.2.10
   * has no argon2 scheme and libxcrypt answers its failure token for `$argon2id$`
   * — so a console that says "argon2" here is describing something the data plane
   * cannot do.
   */
  "accounts.credential.bcrypt": "+ bcrypt for 802.1X",
  "accounts.credential.bcryptWhy":
    "802.1X is enabled here, so this identity has a second verifier the network can check: bcrypt, not argon2id. FreeRADIUS's PAP module cannot read an argon2 hash, so EAP-TTLS/PAP needs one it can. Unlike an NT hash it is one-way — reading it does not authenticate — which is why it may live where the RADIUS server can reach it. The portal and this console still verify against argon2id.",
  "accounts.dot1x.title": (n: number) =>
    n === 1
      ? "1 identity can authenticate over 802.1X"
      : `${n} identities can authenticate over 802.1X`,
  "accounts.dot1x.body":
    "These identities have a second, bcrypt verifier so EAP-TTLS/PAP can check them. It is one-way and lives in its own table, readable by the RADIUS server and by nothing else. The 802.1X username is unique across the whole install, because an 802.1X supplicant sends a username with no site in it.",

  "accounts.nt.title": (n: number) =>
    n === 1 ? "1 identity stores an NT hash" : `${n} identities store an NT hash`,
  "accounts.nt.body":
    "These identities have PEAP-MSCHAPv2 enabled. An NT hash is password-equivalent and lives in a separate table, encrypted under a key that is not in the database. Prefer EAP-TLS or EAP-TTLS/PAP unless PEAP-MSCHAPv2 is genuinely required.",

  /*
   * Disabling is the reversible action, so it is the easy one.
   *
   * A screen offering deletion and nothing else would leave an operator facing a
   * compromised staff account to destroy the record or do nothing.
   */
  "accounts.action.disable": "Disable",
  "accounts.action.enable": "Enable",
  "accounts.action.disableWhy":
    "A disabled account cannot sign in. Sessions already running do not end on their own — that needs a command sent to the access point.",
  "accounts.action.enableWhy": "The account can sign in again from its next attempt.",
  "accounts.action.credentials": "Password…",
  "accounts.action.credentialsWhy": "A new password, and 802.1X on or off",
  "accounts.toast.disabled": (username: string) => `${username} disabled.`,
  "accounts.toast.enabled": (username: string) => `${username} enabled.`,

  "accounts.delete.title": "Delete account",
  "accounts.delete.verb": "Delete account",
  "accounts.delete.consequence": (username: string) =>
    `Account ${username} is deleted and can no longer sign in.`,
  "accounts.delete.recovery":
    "Sessions already running do not end on their own — that needs a command sent to the access point. An account with the same name can be created later, but its sign-in history does not come back.",

  // The create-account dialog's copy is `i18n/credentials.ts`, loaded with it.

  /* ── refusals ─────────────────────────────────────────────────────────────
   *
   * The client's half of `ProblemType`. The server sends a type and structured
   * `params`; every sentence an operator reads about a refusal is written here and
   * composed in `problems.ts`.
   *
   * `title` is a short label — a toast on its own, or a heading where the surface
   * has none. `body` is the sentence that says what happened and what to do next.
   * Surfaces that already own a heading ("Changes not saved", "Controller not
   * added") keep it and render only the body: the type says why the request was
   * refused, the screen says which request it was, and neither can say both.
   */

  // Reachability, as a clause other sentences build on. Three values, three
  // different afternoons: a network problem, a controller problem, and a device
  // that has never answered at all.
  "problem.reach.unreachable": (name: string) => `${name} could not be reached.`,
  "problem.reach.credentials-expired": (name: string) =>
    `The controller rejected Foyer's credentials for ${name}.`,
  "problem.reach.unknown": (name: string) => `${name} has never answered a probe.`,
  "problem.reach.device": "The access point",

  "problem.validation.title": "Not accepted",
  "problem.validation.body": "Correct the fields marked below and try again.",

  "problem.not-found.title": "Not found",
  "problem.not-found.body":
    "It is not there any more — it may have been deleted from another session. Reload to see what is there now.",

  /*
   * Says why, and the why is not a product preference. Two venues on private-range
   * networks both using 192.168.1.1 is the common case, and FreeRADIUS matches a
   * client on its address in one flat namespace — so they could never both work.
   * An operator told only "not allowed" would reasonably assume it is a licence
   * limit, and this project has none.
   */
  "problem.one-site-per-install.title": "This deployment already has a venue",
  "problem.one-site-per-install.body":
    "Foyer holds one venue per installation. It is not a licence limit — the RADIUS server matches an access point by its IP address in a single flat list, so two venues that both use an address like 192.168.1.1 could never both work. A second venue is a second installation.",

  "problem.duplicate.title": "Already on the list",
  "problem.duplicate.body": (value: string | null) =>
    value ? `${value} is already there. Nothing was added.` : "That is already there. Nothing was added.",

  "problem.too-large.title": "Too large",
  "problem.too-large.body": (used: string, limit: string) =>
    `${used} against a ${limit} limit. Nothing was uploaded.`,

  "problem.garden-entry-required.title": "Required by the portal",
  "problem.garden-entry-required.body": (value: string | null) =>
    `${value ?? "This destination"} is what serves the sign-in page itself. Removing it would leave guests unable to load the portal, so it was kept.`,

  "problem.controller-in-use.title": "Access points still adopted",
  "problem.controller-in-use.body": (count: number) =>
    `${count === 1 ? "1 access point came" : `${count} access points came`} from this controller and would stop being managed. Delete them first, or adopt them elsewhere.`,

  "problem.controller-unreachable.title": "Cannot read from this controller",
  "problem.controller-unreachable.body": (reach: string) =>
    `${reach} Nothing was read, and nothing on this screen changed.`,

  "problem.needs-controller.title": "Register the controller instead",
  "problem.needs-controller.body": (driver: string | null) =>
    `${driver ? `A ${driver} access point` : "This access point"} is registered through its controller, not on its own. Add the controller, then discover the access points behind it.`,

  "problem.rotation-refused.title": "Secret not rotated",
  "problem.rotation-refused.body": (reach: string) =>
    `${reach} A new secret would be generated here and never reach the device, which would take it off the network. The current secret still works.`,

  // Portal-side refusals. The admin console cannot produce them today; they are
  // here because `ProblemType` is one keyspace and a gap in it is a compile error.
  "problem.device-not-verified.title": "Device not confirmed",
  "problem.device-not-verified.body":
    "The access point did not confirm that this device is connected to it.",
  "problem.method-not-offered.title": "Not offered here",
  "problem.method-not-offered.body": "This site does not offer that way of signing in.",
  "problem.no-grant.title": "Not connected",
  "problem.no-grant.body": "There is no session for this device.",

  "problem.internal.title": "The server failed",
  "problem.internal.body":
    "The request reached the server and it failed on its own. Nothing was changed. Try again, and check the server log if it keeps happening.",

  // Not a `4xx`. The voucher was revoked; ending the session it was being used for
  // is a separate driver call, and it is the one an operator standing in front of a
  // guest actually needs to know about.
  "problem.session-end-failed.title": "Revoked — the guest is still online",
  "problem.session-end-failed.body": (reach: string) =>
    `${reach} The code cannot be used again, but the session it is being used for was not ended: the guest stays online until it ends on its own.`,
  // `disconnectSession` refusals. Nothing was changed by either.
  "problem.session-not-live.title": "Already ended",
  "problem.session-not-live.body": "This session has already ended. Nothing was changed.",
  "problem.disconnect-unsupported.title": "Cannot disconnect from Foyer",
  "problem.disconnect-unsupported.body": (vendor: string | null) =>
    `Foyer cannot end a session on this ${vendor ? `${vendor} ` : ""}device. Nothing was changed; disconnect the guest on the device itself.`,

  /*
   * Sign-in and permission. `ProblemType` is a closed enum, so the catalogue does not
   * compile until every member has copy.
   */
  // Not a failure. The client's answer is to route to the sign-in screen, so this
  // copy is what an operator sees only where routing is not the answer.
  "problem.not-signed-in.title": "Signed out",
  "problem.not-signed-in.body": "This session has ended. Sign in again to continue.",
  // Never says which of the two was wrong, and a disabled operator is refused the
  // same way. Saying more would answer "does this username exist" for a stranger.
  "problem.bad-credentials.title": "Wrong username or password",
  "problem.bad-credentials.body": "Check both and try again.",
  "problem.rate-limited.title": "Too many sign-in attempts",
  "problem.rate-limited.body": (seconds: number) =>
    `Wait ${seconds < 2 ? "a second" : `${seconds} seconds`} before trying again.`,
  "problem.forbidden.title": "Not permitted for this role",
  /* A temporary password. In core rather than a namespace because the shell's
     problem catalogue is eager and this is one of its keys — see frontend.md: the
     rule is about what a browser downloads, not about which screen the words are
     for. */
  "problem.password-change-required.title": "Choose your own password",
  "problem.password-change-required.body":
    "This account has a temporary password somebody else set. Nothing else will answer until you change it.",
  // The first-access setup was completed by somebody else first; sign in instead.
  "problem.setup-complete.title": "Already set up",
  "problem.setup-complete.body": "An administrator exists. Sign in.",
  "problem.forbidden.body":
    "This account's role does not allow that. Signing in again will not change it — an administrator has to grant the permission.",
  "problem.last-administrator.title": "The last administrator",
  "problem.last-administrator.body":
    "This is the only enabled administrator left, and there is no password-reset channel: a deployment with none cannot be administered again. Give another operator the administrator role first.",

  // The one type no server can send. Synthesised in `api.ts` when `fetch` rejects.
  "problem.unreachable.title": "No answer from the server",
  "problem.unreachable.body":
    "The request did not reach the server, so nothing was changed. Check the connection and try again.",

  // A type this console predates. Same shape as `alarm.unknown` and for the same
  // reason: never blank, and never a raw enum value.
  "problem.unknown.title": "Refused",
  "problem.unknown.body":
    "The server refused this and named a reason this console does not recognise. Nothing was changed.",

  /* ── refused fields ───────────────────────────────────────────────────────
   *
   * Keyed on the fault, which is the closed half of `(type, field, fault)`. The
   * three below the fault table are the triples where the fault's own sentence
   * would answer a different question.
   */
  "fault.required": "Required.",
  "fault.duplicate": "Already taken. Choose another.",
  "fault.too-large": (used: string, limit: string) => `${used} against a ${limit} limit.`,
  "fault.below-minimum": (min: number) => `At least ${min}.`,
  "fault.above-maximum": (max: number) => `At most ${max}.`,
  "fault.not-a-mac-address": "Enter a MAC address, for example AA-BB-CC-00-11-22.",
  "fault.not-a-phone-number": "Enter a phone number, for example 0912 345 678.",
  "fault.not-an-email-address": "Enter an e-mail address, for example lan@example.com.",
  "fault.not-a-wifi-passphrase": "8 to 63 characters, without accents.",
  "fault.not-a-data-uri": "Choose an image file.",
  "fault.not-a-hostname": "Enter a hostname, for example payments.example.com.",
  "fault.not-a-cidr": "Enter a range, for example 192.0.2.0/24.",
  // Not `not-a-cidr`: that one refuses a field that must be a *range*, and its
  // sentence would tell an operator who typed `10.20.0.two` that a single address is
  // unacceptable — the opposite of true. A RADIUS client is one address or a fleet.
  "fault.not-an-ip-address": "Enter an address or a range, for example 192.0.2.10 or 192.0.2.0/24.",
  "fault.not-an-origin": "Scheme and host only, for example https://192.0.2.10:8443.",
  "fault.unsupported-image-format": "JPEG, PNG or WebP.",
  "fault.needs-controller": "Registered through a controller, not on its own.",
  "fault.required-with-click-through": "Required when terms-only access is offered.",
  "fault.needs-phone-provider": "Set up phone sign-in before offering it.",
  "fault.missing-code-placeholder": "Put {code} in the message once.",
  // The portal page design's three rules the schema cannot state.
  "fault.not-a-link": "Use a web address starting https:// or a phone number starting tel:",
  "fault.link-before-sign-in": "Links only work after sign-in.",
  "fault.ends-before-start": "The end date is before the start date.",
  // Not `bad-credentials`: that one refuses to say which of two things was wrong
  // because it answers a stranger. This is talking to somebody already signed in
  // about the field they just typed.
  "fault.incorrect": "Not correct.",
  "fault.unchanged": "Same as the current value. Choose a different one.",
  "fault.range-inverted": "Must end after it starts.",
  "fault.range-too-long": (days: number) => `At most ${days} days.`,
  "fault.needs-contact-field": "Ask for a phone number or an e-mail address, or there is nobody to contact.",
  "fault.not-a-web-address": "Enter an address starting http:// or https://.",
  "fault.certificate-refused": "This certificate cannot be used.",
  "fault.not-a-time-zone": "Choose a time zone from the list.",
  "fault.chooseOne": "Choose at least one.",
  // The same number the form stated before anything was typed, read from the same
  // constant. `fault.below-minimum` cannot serve here: it takes the figure from the
  // refusal, and a refusal that omits it renders "At least 0."
  "fault.passwordLength": (min: number) => `At least ${min} characters.`,
  "fault.omadaControllerId": "Required for Omada — the 32 characters from your controller's URL.",
  // Not the bare `fault.required`. This field is refused because Foyer *will not
  // guess*, and the sentence has to say so — the operator is looking at a select whose
  // current value is a real one, so "Required." reads as a form bug.
  "fault.coaVendor": "Required — Foyer will not guess which of the two this is.",
  "fault.unknown": "This value was refused.",
};

/* The Vietnamese, when the console speaks it (`lang.ts`). `main.tsx` renders
   nothing until `localesSettled()`, so the first frame is already in it. */
void loadLocale(() => import("./vi/core.js"));
