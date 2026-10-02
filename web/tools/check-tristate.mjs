/**
 * The census of three-valued booleans, and the expiry condition of a decision.
 *
 * `web/` has no linter. That is a decision, taken against a measurement, and the
 * measurement's load-bearing number is *how many optional booleans the contract
 * declares*. Two of them are read models where absence means **not measured**, and
 * `!value` reads that as **false**, a shape that has caused real defects here.
 * Three are request bodies where absence means **leave it alone**, which is correct.
 *
 * Five fields is a class small enough to hold locks around. Fifteen is not, and at
 * fifteen the decision should be re-taken. **Nothing in this repository could see
 * that number change**, which makes "we measured it at five" the same kind of
 * sentence as a skip that names a phase: true when written, unable to stop being
 * true out loud. `docs/rules/testing.md` has a section about exactly that.
 *
 * So this is the decision's own expiry condition, made evaluable. It fails when:
 *
 *   - the contract grows an optional boolean nobody has classified;
 *   - a classified one disappears (the census is checked in both directions,
 *     because a stale census that names a field the contract dropped is a second
 *     source of truth for a fact the contract owns);
 *   - a read-model field's named lock file is gone, or has stopped naming it.
 *
 * The last one is the point of the exercise rather than a flourish. A census that
 * only counted would tell you the class is still small while the coverage that
 * makes a small class safe had been deleted underneath it.
 *
 * **What this does not do.** It is not a linter and it does not read call sites.
 * The measurement behind the decision found that no line-level or type-level
 * instrument can separate a guarded read of one of these fields from an unguarded
 * one — `@typescript-eslint/strict-boolean-expressions` flags all six existing
 * reads, including the four that are correct, because the guard is a *different*
 * variable carrying the same fact. What catches an unguarded read is a behavioural
 * lock driven through the path where absence arises. This gate guards the locks.
 *
 * Zero dependencies beyond `typescript`, which the workspace already builds with.
 */
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const WEB = fileURLToPath(new URL("..", import.meta.url));
const TYPES = fileURLToPath(new URL("../contract/src/generated/types.ts", import.meta.url));

/**
 * The reviewed census. One entry per optional boolean the contract declares,
 * keyed by its dotted path in the generated types.
 *
 * `kind` is **derived** below, not typed in here — a request body and a read model
 * are told apart by where the field sits, so the classification cannot drift from
 * the contract the way a copied verdict would. What a human owes each entry is the
 * `why`, and for a read model the `locks`: the tests that drive the absent value.
 */
const CENSUS = {
  "components.schemas.Nas.accountingConfigured": {
    why: "Absent = nothing has probed this device. False = probed and silent. Reading the two as one is a real defect.",
    locks: [
      "admin/src/screens/nas/accounting-pill.test.tsx",
      "mock/src/nas-registration.test.ts",
    ],
  },
  "components.schemas.Session.macIsRandomised": {
    why: "Absent = not reported. Unlike accounting it is derivable — the locally-administered bit of `callingStationId` answers it — so a reader may compute rather than guess.",
    locks: ["mock/src/validate.test.ts"],
  },
  "components.schemas.OperatorUpdate.enabled": {
    why: "PATCH body. Absent = leave the operator's enabled state alone. Two-valued when present, and absence is not a third value.",
  },
  "operations.updateNas.requestBody.content.application/json.enabled": {
    why: "PATCH body. Absent = leave the device's enabled state alone.",
  },
  "operations.updateAccount.requestBody.content.application/json.enabled": {
    why: "PATCH body. Absent = leave the account's enabled state alone.",
  },
};

/** A field is a request body if it sits in one, or on a `*Create` / `*Update` schema. */
function isRequestBody(path) {
  if (path.includes(".requestBody.")) return true;
  const schema = /^components\.schemas\.([A-Za-z0-9]+)\./.exec(path)?.[1];
  return schema !== undefined && /(Create|Update|Patch)$/.test(schema);
}

/**
 * Every optional `boolean` property in a generated-types source, as a dotted path.
 *
 * Reads the AST rather than the text. A regex over `?: boolean;` finds the same
 * five lines today and cannot say which schema any of them is on, which is the
 * half of the answer that decides whether a field matters.
 */
export function census(text, fileName = "types.ts") {
  const src = ts.createSourceFile(fileName, text, ts.ScriptTarget.ES2023, true);
  const found = [];

  const name = (node) =>
    ts.isIdentifier(node) || ts.isStringLiteral(node) ? node.text : node.getText(src);

  function walk(node, path) {
    ts.forEachChild(node, (child) => {
      if (ts.isPropertySignature(child) && child.name) {
        const here = [...path, name(child.name)];
        if (child.questionToken && child.type?.kind === ts.SyntaxKind.BooleanKeyword) {
          found.push(here.join("."));
        }
        if (child.type) walk(child.type, here);
        return;
      }
      if (ts.isInterfaceDeclaration(child)) {
        walk(child, [...path, child.name.text]);
        return;
      }
      walk(child, path);
    });
  }

  walk(src, []);
  return found.sort();
}

/**
 * The whole verdict for one census, so the self-test can drive it without a process.
 *
 * Problems carry the path they are about. The self-test below asks *which* field a
 * problem names rather than counting them, because a count is satisfied by the
 * wrong problem arriving in the right quantity.
 */
export function judge(paths, { lockExists }) {
  const problems = [];
  const known = new Set(Object.keys(CENSUS));
  const say = (path, why, text) => problems.push({ path, why, text: `${path}\n      ${text}` });

  for (const path of paths) {
    const entry = CENSUS[path];
    if (!entry) {
      say(
        path,
        "unclassified",
        `is an optional boolean the census does not classify.\n` +
          `      Absence is a third value or it is not — say which, in web/tools/check-tristate.mjs,\n` +
          `      and if it is, re-read the reasoning at the top of that file before adding\n` +
          `      the sixth: the "no linter" decision was taken against a class of five.`,
      );
      continue;
    }
    known.delete(path);
    if (isRequestBody(path)) continue;
    if (!entry.locks || entry.locks.length === 0) {
      say(path, "no-lock", "is a read model with no lock named. Absence reaches a screen with nothing driving it.");
      continue;
    }
    for (const lock of entry.locks) {
      if (!lockExists(lock, path)) {
        say(
          path,
          "lock-gone",
          `names ${lock} as its lock, and that file does not exist or no longer mentions the field.\n` +
            `      The census still says the class is small enough to hold locks around. Restore the lock or amend the census.`,
        );
      }
    }
  }

  for (const gone of known) {
    say(
      gone,
      "vanished",
      `is in the census and not in the contract. A census entry for a field that no longer\n` +
        `      exists is a second source of truth for a fact the contract owns. Remove it.`,
    );
  }

  return problems;
}

/**
 * Does the named lock still exist and still name the field?
 *
 * **It proves the file mentions the name, and no more than that** — a lock reduced
 * to a comment would pass. Stated rather than glossed, because a check that claims
 * more derivation than it performs is `docs/rules/database.md`'s restated
 * constraint in prose. What it does catch is the failure that actually happens: a
 * lock deleted, renamed, or emptied while the census goes on asserting the class is
 * small enough to hold locks around.
 */
const lockExists = (lock, path) => {
  const file = WEB + lock;
  if (!existsSync(file)) return false;
  return readFileSync(file, "utf8").includes(path.split(".").pop());
};

/*
 * The self-test, run on every invocation rather than watched once in a commit
 * message. Three ways this gate could go quiet, and each is re-derived here:
 * a new field it fails to notice, a vanished field it keeps asserting, and a
 * deleted lock it keeps believing in.
 */
function selfTest() {
  const failures = [];
  const check = (label, ok) => {
    if (!ok) failures.push(label);
  };

  /*
   * There is deliberately no assertion here that the contract declares five fields,
   * and no assertion that the shipped census passes. Both were written, and the
   * first sabotage — adding a sixth optional boolean — showed what they cost: the
   * run reported *"the tri-state census cannot fail any more"* about a harness that
   * was working perfectly, and buried the actual sentence the author needed to read.
   *
   * That is `docs/rules/testing.md`'s harness "too rigid on the output side": a
   * declaration written against today's tree, going red the day somebody changes the
   * tree legitimately, with the same exit code as a broken instrument. The real
   * check below already compares the contract against the census in both directions
   * and says so in words. A self-test tests the mechanism; it does not restate the
   * world.
   */

  const synthetic = `
export interface components {
  schemas: {
    Widget: { spinning?: boolean; name: string };
    WidgetUpdate: { spinning?: boolean };
  };
}`;
  const found = census(synthetic);
  check("a new optional boolean is seen at all", found.includes("components.schemas.Widget.spinning"));
  check(
    "the nesting is read, not the line — the two `spinning` fields are told apart",
    found.includes("components.schemas.WidgetUpdate.spinning") && found.length === 2,
  );

  /* Every judgement below is driven over the *whole* census plus the change under
     test, so a verdict is what it is in the run that ships rather than in a world
     of one field. `about` then asks which field a problem names. */
  const about = (problems, path, why) =>
    problems.filter((p) => p.path === path && p.why === why).length;
  const all = Object.keys(CENSUS);

  check(
    "a new optional boolean on a read model is refused",
    about(judge([...all, "components.schemas.Widget.spinning"], { lockExists: () => true }),
      "components.schemas.Widget.spinning", "unclassified") === 1,
  );
  check(
    "a new optional boolean on a request body is refused too — classification is owed either way",
    about(judge([...all, "components.schemas.WidgetUpdate.spinning"], { lockExists: () => true }),
      "components.schemas.WidgetUpdate.spinning", "unclassified") === 1,
  );
  check(
    "a classified request body is not refused for having no lock",
    judge(all, { lockExists: (l) => !l.includes("nas-registration") && !l.includes("validate.test") && !l.includes("accounting-pill") })
      .every((p) => p.path !== "components.schemas.OperatorUpdate.enabled"),
  );
  check(
    "a vanished field is refused",
    about(judge(all.filter((p) => p !== "components.schemas.Session.macIsRandomised"), { lockExists: () => true }),
      "components.schemas.Session.macIsRandomised", "vanished") === 1,
  );
  check(
    "a deleted lock is refused, once per lock",
    about(judge(all, { lockExists: (l) => !l.includes("accounting-pill") && !l.includes("nas-registration") }),
      "components.schemas.Nas.accountingConfigured", "lock-gone") === 2,
  );
  /* The absent name is assembled at run time rather than written out, because a
     literal would appear in this very file and satisfy the check it is testing. */
  const absent = ["components", "schemas", "Nas", ["not", "In", "That", "File"].join("")].join(".");
  check(
    "a lock file that exists but has stopped naming the field is refused",
    lockExists("tools/check-tristate.mjs", absent) === false,
  );

  if (failures.length > 0) {
    console.error("\n  the tri-state census cannot fail any more:");
    for (const f of failures) console.error(`  ✗ ${f}`);
    console.error("");
    process.exit(1);
  }
}

selfTest();

const problems = judge(census(readFileSync(TYPES, "utf8"), TYPES), { lockExists });

if (problems.length > 0) {
  console.error("");
  for (const p of problems) console.error(`  ✗ ${p.text}\n`);
  console.error(
    `  ${problems.length} problem${problems.length === 1 ? "" : "s"} in the tri-state census\n`,
  );
  process.exit(1);
}

console.log(`  tri-state census: ${Object.keys(CENSUS).length} optional booleans, all classified, locks present`);
