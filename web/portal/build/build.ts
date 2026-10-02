/**
 * Builds the portal as a set of complete, self-contained HTML documents — one per
 * (language × page), each with its CSS and JS inlined.
 *
 * The output is static files. A captive portal that needs no server to render its
 * GET side is the most robust thing available in an environment where the client
 * has no internet, no warm cache and no persistent storage.
 */
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import * as esbuild from "esbuild";
import { DEFAULT_LANGUAGE, LANGUAGES } from "../src/i18n/index.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const out = join(root, "dist");

async function main() {
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });

  // 1. The enhancement script → one minified IIFE, inlined into the documents that
  //    ask for it. No module graph, no import map, no second request.
  const script = await esbuild.build({
    entryPoints: [join(root, "src/enhance/index.ts")],
    bundle: true,
    minify: true,
    format: "iife",
    target: ["chrome117", "safari15"],
    write: false,
  });
  const inlineScript = script.outputFiles[0]!.text.trim();

  // 2. The stylesheet → minified, inlined.
  const css = (
    await esbuild.transform(readFileSync(join(root, "src/styles/portal.css"), "utf8"), {
      loader: "css",
      minify: true,
    })
  ).code.trim();

  // 3. The renderer → bundled for Node so the pages can be rendered here.
  const ssr = await esbuild.build({
    entryPoints: [join(root, "src/render.ts"), join(root, "src/document.ts"), join(root, "src/probe.ts")],
    bundle: true,
    format: "esm",
    platform: "node",
    target: "node22",
    jsx: "automatic",
    jsxImportSource: "preact",
    outdir: join(root, ".ssr"),
    packages: "external",
  });
  if (ssr.errors.length) throw new Error("ssr build failed");

  const { renderPage, PAGES } = await import(join(root, ".ssr/render.js"));
  const { renderDocument } = await import(join(root, ".ssr/document.js"));
  const { renderProbeDocument, PROBE_LANG_PATHS } = await import(join(root, ".ssr/probe.js"));

  /*
   * The measurement probe is not a guest page, and this is where that is enforced.
   *
   * It is one page that reports what a phone's browser does, in every language
   * the portal carries. It is a diagnostic for one afternoon, not a product
   * feature, and a page every guest downloads to serve that afternoon is a
   * regression the byte budget would rightly fail.
   *
   * So it is emitted beside the portal, never inside it — `dist-probe/`, which
   * `make web-embed` does not copy and `web/tools/check-budget.mjs` does not
   * walk. `dist/` is the tree that reaches a guest, and one output directory is
   * the whole of the containment.
   *
   * **This runs before anything is written.** `PAGES` is the guest route table
   * and the loop below walks it, so a `PageDefinition` added for the probe would
   * put a diagnostic on every guest's page view silently — nothing else about
   * that change looks wrong. The probe is deliberately not a `PageDefinition`
   * (`src/probe.ts`), so finding one here is reporting a contradiction, and
   * aborting before the first write is what keeps a rejected tree from leaving
   * half a guest bundle behind.
   */
  for (const page of PAGES) {
    if (page.path.replace(/\/+$/, "").split("/").includes("probe")) {
      throw new Error(
        `the measurement probe must not be a guest page: ${page.path} is in PAGES, so it ` +
          `would be built into dist/ and served to every guest. Render it with ` +
          `src/probe.ts instead.`,
      );
    }
  }

  let total = 0;
  const report: { file: string; bytes: number }[] = [];

  for (const lang of LANGUAGES) {
    for (const page of PAGES) {
      /*
       * No `query`, and that is the same statement about production the root
       * redirect below makes. The language links carry the request's query string so
       * that switching language keeps the NAS parameters, the return `url` and a
       * rejected voucher's `?e=`/`?code=`. A pre-rendered file has no request to read
       * one from, so these copies emit bare links.
       *
       * The Go binary renders these documents per request, as `plugin/dev.ts` does,
       * and there the links carry it. Fixing it here would mean a script on all five
       * documents rewriting its own hrefs — bytes on every page, a flow that leans on
       * JavaScript, and still nothing on the no-script path.
       */
      const rendered = renderPage({ lang, path: page.path, context: null, status: null, error: null });
      if (!rendered) continue;

      const html = renderDocument({
        lang,
        title: rendered.title,
        body: rendered.body,
        css,
        script: rendered.enhance ? { inline: inlineScript } : undefined,
      });

      const rel = join(lang, page.path, "index.html");
      const file = join(out, rel);
      mkdirSync(dirname(file), { recursive: true });
      writeFileSync(file, html, "utf8");

      const bytes = Buffer.byteLength(html);
      total = Math.max(total, bytes);
      report.push({ file: rel, bytes });
    }
  }


  // `lab/measure` serves this directory at `/{lang}/probe/`, and owns the
  // `POST /report` the page sends its digest to. One language per file, exactly
  // as `dist/` does it, so the URL a person types on a phone keyboard is the same
  // shape as every other portal URL they could have typed.
  const probeOut = join(root, "dist-probe");
  rmSync(probeOut, { recursive: true, force: true });
  for (const rel of PROBE_LANG_PATHS) {
    const lang = rel.slice(0, rel.indexOf("/")) as (typeof LANGUAGES)[number];
    const file = join(probeOut, rel, "index.html");
    mkdirSync(dirname(file), { recursive: true });
    const html = renderProbeDocument({ lang, css });
    writeFileSync(file, html, "utf8");
    report.push({ file: join("dist-probe", rel, "index.html"), bytes: Buffer.byteLength(html) });
  }

  /*
   * A bare `/` redirect for anything that lands on the origin root.
   *
   * **This file cannot fully do its job, and that is a statement about production
   * rather than about the build.** A guest arrives from the NAS carrying its
   * parameters — `ap`, `id`, `t`, `ssid`, `url` for UniFi — and those are the only
   * evidence `VerifyPresence` has to work from (`docs/rules/security.md` §4), plus
   * the only source of the "continue to where you were going" link. A static file
   * cannot see the request, so `<meta http-equiv="refresh">` necessarily drops them.
   *
   * **What the Go binary must do, since it is what serves this in production:**
   * answer `/` with a `302` to `/{lang}/` carrying the query string verbatim, with
   * `lang` negotiated from `Accept-Language` intersected with the site's advertised
   * languages. That is what `plugin/dev.ts` does, and what the E2E suite asserts.
   *
   * Until then this file does the best a static artefact can. The script runs first
   * and preserves the query; the meta refresh behind it is the no-script path and
   * loses it. Ordering is load-bearing — the meta must come *after*, or it fires
   * during parsing and wins. Nothing about the portal flow depends on the script:
   * with it, the parameters survive; without it, the guest still reaches the right
   * page, only without them. The script is not permitted to be the only mechanism, which is why the
   * meta stays.
   */
  const landing = `/${DEFAULT_LANGUAGE}/`;
  writeFileSync(
    join(out, "index.html"),
    `<!doctype html><html lang="${DEFAULT_LANGUAGE}"><head><meta charset="utf-8">` +
      `<script>location.replace(${JSON.stringify(landing)}+location.search)</script>` +
      `<meta http-equiv="refresh" content="0;url=${landing}">` +
      `<title>Foyer</title></head><body></body></html>\n`,
    "utf8",
  );

  rmSync(join(root, ".ssr"), { recursive: true, force: true });

  report.sort((a, b) => b.bytes - a.bytes);
  console.log("\n  portal build\n");
  for (const r of report) {
    console.log(`    ${String(r.bytes).padStart(7)} B  ${r.file}`);
  }
  console.log(`\n    largest guest document: ${total} B uncompressed`);
  console.log(
    `    dist-probe/ is the measurement page and is not part of the guest bundle:\n` +
      `    it is served by lab/measure -probe, and lab-up does not copy it into the binary\n`,
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
