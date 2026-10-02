/**
 * The portal byte budget, as a hard CI gate rather than a warning.
 *
 * A warning is a budget nobody enforces. This fails the build, because the cost of
 * a bloated portal is not paid by the developer who added the dependency — it is
 * paid by a guest on an open SSID with no default route, on a device that has just
 * associated, over a link that is the worst they will use that day.
 *
 * Every number here comes from measurement rather than taste; see
 * `docs/protocol-reality.md`. Android calls `clearCache(true)` on every portal
 * launch and the iOS CNA has no persistent store, so **every byte is paid on every
 * single open**. There is no warm second load to amortise anything against, which is
 * why the unit below is *one page view* and not *one file*.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **This gate weighs what a document references, not only the document.**
 *
 * Gzipping each `.html` in `dist` and grepping for `(?:src|href)="https?://…"` is not
 * enough: a same-origin `<img src="/en/hero.jpg">` matches no such pattern and would
 * never be weighed — so a branding feature could take an arbitrarily large photograph
 * straight through a green gate. A planted 400 kB image must make this script fail.
 *
 * *No external origin* and *the portal's own weight* are two different rules, and
 * they have two different checks.
 */
import { gzipSync } from "node:zlib";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, posix, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const DIST = fileURLToPath(new URL("../portal/dist", import.meta.url));

/**
 * The portal's own bytes for one document: HTML, plus any CSS or JS it references.
 *
 * 50 KiB with the system font stack. Everything is inlined today, so a linked code
 * asset would be a deliberate change and it should be weighed against the same
 * number rather than escaping it by being a separate file.
 */
const CODE_BUDGET = 50 * 1024;

/**
 * One venue image: 5 MiB, raised from an original 100 KiB. The 89,697 B measurement below is kept because it is still the cost a venue
 * *should* pay for a 900 px photograph.
 *
 * A real photograph downscaled to 900 px — the macOS CNA's full window width — at
 * JPEG quality 70 is **89,697 B** (`docs/protocol-reality.md`). 100 KiB admits that
 * with a little headroom and forces anything larger to be re-encoded or downscaled
 * rather than shipped. WebP at the same width clears it comfortably.
 *
 * Deliberately a *separate* budget rather than a larger combined one. Operator
 * content and portal code fail for different reasons and are fixed by different
 * people: a blown code budget is a dependency somebody added, and a blown image
 * budget is a photograph somebody uploaded. One number would let each hide inside
 * the other's headroom.
 */
const IMAGE_BUDGET = 5 * 1024 * 1024;

/** Formats gzip cannot usefully compress — the browser pays the raw bytes. */
const PRECOMPRESSED = new Set([".jpg", ".jpeg", ".png", ".webp", ".avif", ".gif", ".woff", ".woff2"]);

const IMAGE_EXT = new Set([".jpg", ".jpeg", ".png", ".webp", ".avif", ".gif", ".svg"]);

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry);
    if (statSync(p).isDirectory()) out.push(...walk(p));
    else if (p.endsWith(".html")) out.push(p);
  }
  return out;
}

/** What the browser actually transfers for this file. */
function wireBytes(path) {
  const buf = readFileSync(path);
  const ext = path.slice(path.lastIndexOf(".")).toLowerCase();
  return PRECOMPRESSED.has(ext) ? buf.length : gzipSync(buf, { level: 9 }).length;
}

/**
 * Every URL the document tells the browser to fetch.
 *
 * `src`, `href` and `srcset`, because a responsive image escapes a `src`-only scan
 * and a responsive image is exactly what a branding feature reaches for. `data:`
 * URIs are skipped: they are already inside the document and already weighed, and
 * counting them twice would punish the one case that is correct.
 */
function referencesIn(html) {
  const refs = [];
  for (const m of html.matchAll(/\b(?:src|href)\s*=\s*"([^"]+)"/g)) refs.push(m[1]);
  for (const m of html.matchAll(/\bsrcset\s*=\s*"([^"]+)"/g)) {
    for (const candidate of m[1].split(",")) {
      const url = candidate.trim().split(/\s+/)[0];
      if (url) refs.push(url);
    }
  }
  return refs.filter((r) => r && !r.startsWith("data:") && !r.startsWith("#"));
}

let failed = false;
const fail = (msg) => {
  failed = true;
  console.log(`      ✗ ${msg}`);
};

const files = walk(DIST);

console.log("\n  portal budget\n");

for (const file of files.sort()) {
  const html = readFileSync(file);
  const text = html.toString("utf8");
  const rel = relative(DIST, file);
  const docBytes = gzipSync(html, { level: 9 }).length;

  let codeBytes = docBytes;
  let imageBytes = 0;
  const seen = [];

  for (const ref of referencesIn(text)) {
    // An external origin is a separate rule and a harder failure: the portal has to
    // render before the client has internet, and anything off-origin would also have
    // to be added to the walled garden on every driver — which is the reason not to
    // have one rather than a reason to maintain a list.
    if (/^https?:\/\//i.test(ref) || ref.startsWith("//")) {
      fail(`external request in ${rel}: ${ref}`);
      continue;
    }

    // Resolve same-origin references against the built tree. Root-relative against
    // `dist`, otherwise relative to the document.
    const target = ref.startsWith("/")
      ? join(DIST, ref.replace(/^\/+/, "").split("?")[0].split("#")[0])
      : resolve(file, "..", ref.split("?")[0].split("#")[0]);

    // Outside `dist` is not a thing a static portal can serve.
    if (!resolve(target).startsWith(resolve(DIST))) {
      fail(`${rel} references a path outside dist: ${ref}`);
      continue;
    }
    // A broken reference is fatal rather than free. The portal is served to a client
    // with no internet: a 404 here is a missing logo with no fallback anywhere.
    if (!existsSync(target) || statSync(target).isDirectory()) {
      // Documents link to each other by directory (`/en/voucher/`); that is a route,
      // not an asset, and it resolves to a directory in the built tree.
      if (existsSync(join(target, "index.html"))) continue;
      fail(`${rel} references a file that does not exist: ${ref}`);
      continue;
    }

    const bytes = wireBytes(target);
    const ext = target.slice(target.lastIndexOf(".")).toLowerCase();
    const isImage = IMAGE_EXT.has(ext);
    if (isImage) imageBytes += bytes;
    else codeBytes += bytes;
    seen.push({ ref: posix.normalize(ref), bytes, isImage });
  }

  const overCode = codeBytes > CODE_BUDGET;
  const overImage = imageBytes > IMAGE_BUDGET;
  if (overCode || overImage) failed = true;

  const total = codeBytes + imageBytes;
  const flag = overCode || overImage ? "✗" : "·";
  const suffix = imageBytes > 0 ? `  (code ${codeBytes} + image ${imageBytes})` : "";
  console.log(`    ${flag} ${String(total).padStart(6)} B  ${rel}${suffix}`);

  for (const s of seen) {
    console.log(`        ${String(s.bytes).padStart(6)} B  ${s.isImage ? "image" : " code"}  ${s.ref}`);
  }

  if (overCode) console.log(`      ✗ code over budget: ${codeBytes} > ${CODE_BUDGET}`);
  if (overImage) console.log(`      ✗ image over budget: ${imageBytes} > ${IMAGE_BUDGET}`);
}

console.log(
  `\n    budget: ${CODE_BUDGET} B code per page view, ${IMAGE_BUDGET} B images per page view`,
);
console.log("    one page view, not one file — Android clears the cache on every launch\n");

if (failed) {
  console.error("  portal budget exceeded\n");
  process.exit(1);
}
