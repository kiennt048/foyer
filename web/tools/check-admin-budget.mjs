/**
 * The admin console's byte budget.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why this exists when the portal already has one.** The portal's gate has caught
 * nothing, because the portal is at 8% of its budget — it is a gate on a thing that
 * is not growing. The admin is the application where a budget actually binds: React
 * 19 plus a router plus a query client is 127 kB gzip before a screen is written,
 * and routes are added to it all the time.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The numbers are not the portal's, and copying them would have been wrong.**
 *
 * The portal's 50 kB is a *measured* constraint: an SSID with no default route, a
 * device that has just associated, and a cache Android guarantees is cold on every
 * single launch. None of that is true here. The admin is opened by a member of
 * staff, on a network that works, from a browser that caches — so the honest budget
 * is not "as small as the portal" but "does not grow without somebody deciding to".
 *
 * So these are **ratchets set just above today's measurement**, not aspirations.
 * A budget nobody is near is a budget nobody notices breaking; a budget 5% above the
 * current figure turns any real addition into a conversation. Raising one is a
 * one-line change and entirely allowed — the point is that it is *deliberate* and
 * shows up in a diff, which a new route otherwise does not have to be.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Entry, not total.** What is gated is what a browser must fetch before the first
 * screen is usable: the HTML, the CSS and the entry chunk. Lazily-loaded routes are
 * reported and deliberately not counted — `/design` is 6.6 kB of gallery that no
 * operator ever loads, and counting it would push work *away* from code-splitting,
 * which is the one lever that actually helps here.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **What the number gates.** The copy is `i18n/core.ts` plus one namespace per lazy
 * feature, because Rollup drops unused modules, not unused properties: a single
 * catalogue object would make every lazy screen's copy part of the entry. Split, the
 * entry measures **131 843 B**, and 135 KiB is a ratchet a shade under 5% above that
 * measurement — no slack that nobody decided to spend.
 *
 * The tight number is affordable because of the split. Without it the only answer to
 * a full budget would be "make another screen lazy", and `App.tsx` has spent that
 * lever down to one candidate worth 1 127 B. With it, the answer to a screen's copy
 * growing is "put the key in that screen's namespace", where it costs the entry
 * nothing — so what this number actually gates is growth in the **shell's**
 * copy and the shell's code, which is the growth that deserves a conversation.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Re-ratcheted downwards again, for the same reason.**
 *
 * The entry had 621 B left of 138 240 B, and changes were being rationed to about
 * 150 B each — a budget so tight it had stopped being a conversation and become a
 * queue. The shell had not grown much; `/sessions` and `/vouchers`, the two "daily"
 * screens `App.tsx` kept eager, had. They are lazy routes now, with their copy in
 * `i18n/sessions.ts` and `i18n/vouchers.ts` — the field's shape (Grafana, authentik
 * and Keycloak all route-split every page; the entry is the shell and the landing
 * screen). Measured with `pnpm -F @foyer/admin build`, gzip, entry total:
 *
 *     before                              137 619 B   one stream
 *     session dock lazy on its own        136 010 B   hoisted into index + primitives
 *     /sessions and /vouchers lazy        125 138 B   one stream, and fetched ← shipped
 *                                                     once the shell paints
 *
 * The middle row is the trap `screens/Dashboard.tsx` records, measured again: a lazy
 * *panel* that reaches only part of the shell splits the entry into two streams and
 * saves a third of what it moved. A lazy *route* reaches `PageHead`, and so the whole
 * shell, and does not.
 *
 * 128 KiB is the rule this file has always applied — a shade under 5% above the
 * measurement (4.7%) — and leaves 5 934 B, which is headroom a change can spend
 * without asking and small enough that a screen rejoining the entry breaks it.
 */
import { gzipSync } from "node:zlib";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const DIST = fileURLToPath(new URL("../admin/dist", import.meta.url));
const I18N = fileURLToPath(new URL("../admin/src/i18n", import.meta.url));

/** Everything the browser needs before the first screen renders. */
const ENTRY_BUDGET = 128 * 1024;
/** The stylesheet alone, so a token file that doubles is visible on its own. */
const CSS_BUDGET = 9 * 1024;

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry);
    if (statSync(p).isDirectory()) out.push(...walk(p));
    else out.push(p);
  }
  return out;
}

const files = walk(DIST);
const index = files.find((f) => f.endsWith("index.html"));
if (!index) {
  console.error("\n  admin budget: no dist/index.html — run `pnpm -F @foyer/admin build` first\n");
  process.exit(1);
}

const html = readFileSync(index, "utf8");
/* The entry set is what the document references directly. Anything else in `dist`
   is a lazy chunk the router fetches later, and is reported rather than gated. */
const referenced = new Set(
  [...html.matchAll(/(?:src|href)="\/([^"]+)"/g)].map((m) => m[1]),
);

let entryBytes = gzipSync(Buffer.from(html), { level: 9 }).length;
let cssBytes = 0;
const entry = [];
const lazy = [];

for (const file of files.sort()) {
  const rel = relative(DIST, file).split("\\").join("/");
  if (rel.endsWith(".html")) continue;
  const bytes = gzipSync(readFileSync(file), { level: 9 }).length;
  if (referenced.has(rel)) {
    entryBytes += bytes;
    if (rel.endsWith(".css")) cssBytes += bytes;
    entry.push({ rel, bytes });
  } else {
    lazy.push({ rel, bytes });
  }
}

console.log("\n  admin budget\n");
for (const f of entry) console.log(`    entry ${String(f.bytes).padStart(7)} B gzip  ${f.rel}`);
for (const f of lazy) console.log(`    lazy  ${String(f.bytes).padStart(7)} B gzip  ${f.rel}`);

const overEntry = entryBytes > ENTRY_BUDGET;
const overCss = cssBytes > CSS_BUDGET;

console.log(
  `\n    entry total ${entryBytes} B of ${ENTRY_BUDGET} B${overEntry ? "  OVER" : ""}` +
    `\n    stylesheet  ${cssBytes} B of ${CSS_BUDGET} B${overCss ? "  OVER" : ""}\n`,
);

/*
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The number above cannot see a namespace rejoining the entry chunk.**
 *
 * The copy split works because seven screens are `lazy()`, so the namespace each one
 * imports lands in that screen's chunk. Turn one of them back into a static import —
 * a one-line change, and the obvious thing to write when a screen needs a value from
 * another screen — and its whole catalogue moves into the entry.
 *
 * **Measured, by doing it.** Making `/portal` a static import takes the entry from
 * 131 843 B to 136 674 B, which is under the 138 240 B ratchet: the byte gate prints
 * no `OVER` and exits 0, while every operator has started downloading a screen most of
 * them never open. A budget reports on the outcome and cannot name the cause.
 *
 * So this asserts the cause directly: for every non-core catalogue, its longest
 * sentence must be absent from the entry chunk and present in a lazy one.
 *
 * **The sentence is read out of the catalogue, never written here.** A copy of the
 * copy is a second source of truth that goes stale the day somebody edits a string —
 * still green, still printing a reason, and now checking a sentence the console no
 * longer says. `docs/rules/database.md` has the general form of that mistake.
 */
const entryText = entry.filter((f) => f.rel.endsWith(".js")).map((f) => readFileSync(join(DIST, f.rel), "utf8")).join("");
const lazyText = lazy.map((f) => readFileSync(join(DIST, f.rel), "utf8")).join("");

/* The longest plain string literal a catalogue declares — long enough to be unique,
   and minifiers do not touch string data. Interpolating entries are skipped: their
   text is assembled at runtime and no whole sentence survives into the bundle. */
function longestSentence(source) {
  const literals = [...source.matchAll(/^\s*(?:"[^"]+": *)?"((?:[^"\\]|\\.){24,})",?$/gm)].map((m) => m[1]);
  return literals.sort((a, b) => b.length - a.length)[0] ?? null;
}

const misplaced = [];
const namespaces = readdirSync(I18N)
  .filter((f) => f.endsWith(".ts") && !f.endsWith(".test.ts"))
  .filter((f) => !["index.ts", "catalogue.ts", "core.ts", "lang.ts"].includes(f));
/* Every Vietnamese catalogue, core's included, must be in a lazy chunk and never in
   the entry: an English reader downloads no Vietnamese, and a Vietnamese reader
   downloads each screen's with that screen. The same
   probe as the English namespaces, over `i18n/vi/`. */
const translations = readdirSync(join(I18N, "vi"))
  .filter((f) => f.endsWith(".ts"))
  .map((f) => join("vi", f));
if (translations.length < namespaces.length + 1) {
  misplaced.push(`vi/: ${translations.length} translations for ${namespaces.length + 1} catalogues — this check cannot see them all`);
}
for (const file of [...namespaces, ...translations].sort()) {
  const sentence = longestSentence(readFileSync(join(I18N, file), "utf8"));
  if (!sentence) {
    misplaced.push(`${file}: no plain sentence long enough to look for — this check cannot see it`);
    continue;
  }
  const probe = JSON.parse(`"${sentence}"`);
  if (entryText.includes(probe)) misplaced.push(`${file}: its copy is in the ENTRY chunk`);
  else if (!lazyText.includes(probe)) misplaced.push(`${file}: its copy is in no chunk at all`);
}

console.log(
  `    ${namespaces.length} lazy copy namespaces and ${translations.length} lazy translations` +
    (misplaced.length ? "" : ", none of them in the entry chunk") +
    "\n",
);

if (misplaced.length) {
  console.error(
    "  a copy namespace is not where the split puts it:\n\n" +
      misplaced.map((m) => `    ${m}`).join("\n") +
      "\n\n  A namespace in the entry chunk means the screen that imports it is no longer\n" +
      "  lazily loaded, or something in the entry imports the namespace directly. Either\n" +
      "  way every operator now downloads that screen's copy. See `admin/src/i18n/core.ts`.\n",
  );
  process.exit(1);
}

if (overEntry || overCss) {
  console.error(
    "  admin budget exceeded.\n\n" +
      "  These are ratchets a few percent above the last measurement, not aspirations.\n" +
      "  Raising one is a one-line change and entirely allowed — the point is that it\n" +
      "  is deliberate and shows up in a diff. Before raising it, check the two cheaper\n" +
      "  answers, in this order:\n\n" +
      "    1. Is the new copy for a screen that is lazily loaded? Then its key belongs\n" +
      "       in that screen's namespace in `admin/src/i18n/`, not in `core.ts`, and it\n" +
      "       costs the entry nothing. `core.ts` has the rule for where a key goes.\n" +
      "    2. Can the addition be a lazy route? Those are reported here and not gated.\n",
  );
  process.exit(1);
}
