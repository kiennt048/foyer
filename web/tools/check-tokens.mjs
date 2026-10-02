/**
 * Colour restraint, enforced rather than documented.
 *
 * Every colour must reach a component through a role-named token. The reason is
 * not tidiness: a role name says *why* a colour is there, so a dark theme can be a
 * real theme rather than an inversion, and so a raw hex nobody can trace does not
 * accumulate in one screen and quietly disagree with the next.
 *
 * The token files are the only place a literal colour may appear.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOTS = [
  fileURLToPath(new URL("../admin/src", import.meta.url)),
  fileURLToPath(new URL("../portal/src", import.meta.url)),
];

/** Where literal colours are allowed to live. */
const ALLOWED = [/tokens\.css$/, /portal\.css$/];

const COLOUR = /#[0-9a-fA-F]{3,8}\b|\b(?:rgb|rgba|hsl|hsla|oklch|lab)\(/g;

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry);
    if (statSync(p).isDirectory()) out.push(...walk(p));
    else if (/\.(css|tsx?)$/.test(p)) out.push(p);
  }
  return out;
}

let failures = 0;

for (const root of ROOTS) {
  for (const file of walk(root)) {
    if (ALLOWED.some((re) => re.test(file))) continue;
    const text = readFileSync(file, "utf8");
    text.split("\n").forEach((line, i) => {
      // A colour inside a comment is prose about a colour, not a colour.
      if (/^\s*(\*|\/\/|<!--)/.test(line)) return;
      const hits = line.match(COLOUR);
      if (!hits) return;
      failures++;
      console.error(`  ✗ ${file.split("/web/")[1]}:${i + 1}  literal colour ${hits.join(", ")}`);
      console.error(`    use a role-named token from tokens.css instead`);
    });
  }
}

if (failures > 0) {
  console.error(`\n  ${failures} literal colour${failures === 1 ? "" : "s"} outside the token files\n`);
  process.exit(1);
}

console.log("  colour tokens: clean");
