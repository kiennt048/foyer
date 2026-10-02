import ts from "typescript";
import { describe, expect, it } from "vitest";

/**
 * The four effects in this package whose dependency list is narrow **on purpose**
 * (`web/` does not adopt ESLint). Each carries prose that says why the list is narrow,
 * rather than a one-line suppression comment addressed to a linter nobody runs.
 *
 * **This is the only thing standing behind those four lists.** `@foyer/admin` has no
 * jsdom and no DOM of any kind, and its unit tests are `renderToStaticMarkup`, where
 * an effect never runs at all (`docs/rules/testing.md` names this). So widening a
 * dependency list — adding `query.data` and re-seeding the draft on every refetch, or
 * adding `text` and letting the settings box clear itself mid-keystroke — is a
 * behaviour change no test in this package can see. A tier that drives the surface is
 * what should hold it; until then this does, and the comment is what tells the next
 * person that the list is a decision rather than an oversight.
 *
 * Two locks, and they are not the same thing:
 *
 *   1. **No suppression for a linter this workspace does not run survives anywhere in
 *      `web/admin/src`.** Such a comment is a fossil: to a reader who cannot tell it
 *      from one doing work, it reads as a live warning, and nothing in the build fails
 *      when it is wrong. This is a guard on a recorded decision, in the shape of
 *      `i18n.test.ts`'s scan and `tools/check-tristate.mjs` — and the one file that
 *      cannot spell the string it forbids is this one, so it quotes the rule as
 *      `eslint-<disable>` rather than the fossil it is looking for.
 *   2. **The four lists keep the dependencies they have, and each carries prose that
 *      names them.** Not a restatement of the code: the assertion is that whoever
 *      narrowed the list said which value it is about, so changing the list means
 *      reading a sentence rather than guessing.
 *
 * The sources come from `import.meta.glob` with `?raw`, the same mechanism
 * `screens.test.ts` and `admin.test.ts` use — the admin has no `node:fs`, and Vite
 * hands the source over as text.
 */

interface Source {
  /** As the glob keys it: `./ui/settings.tsx`. */
  path: string;
  text: string;
  sf: ts.SourceFile;
}

const SOURCES: Source[] = Object.entries(
  import.meta.glob("./**/*.{ts,tsx}", { query: "?raw", eager: true, import: "default" }) as Record<string, string>,
).map(([path, text]) => ({
  path,
  text,
  sf: ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX),
}));

const read = (path: string) => SOURCES.find((s) => s.path === path)!;

/** The `useEffect` calls in a file whose dependency array reads exactly this. */
function effectsWithDeps({ sf }: Source, deps: string) {
  const out: ts.CallExpression[] = [];
  const visit = (n: ts.Node) => {
    if (
      ts.isCallExpression(n) &&
      ts.isIdentifier(n.expression) &&
      n.expression.text === "useEffect" &&
      n.arguments.length === 2
    ) {
      const dependencyList = n.arguments[1];
      if (dependencyList !== undefined && ts.isArrayLiteralExpression(dependencyList)) {
        const written = dependencyList
          .getText(sf)
          .replace(/\s+/g, " ")
          .trim();
        if (written === deps.replace(/\s+/g, " ")) out.push(n);
      }
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out;
}

/** The statements of the enclosing body above `node`, nearest first. */
function statementsAbove(node: ts.Node): ts.Node[] {
  let statement: ts.Node = node;
  while (statement.parent && !ts.isBlock(statement.parent) && !ts.isSourceFile(statement.parent)) {
    statement = statement.parent;
  }
  const out: ts.Node[] = [];
  for (let i = 0; i < 3; i++) {
    const parent: ts.Node | undefined = statement.parent;
    if (!parent || (!ts.isBlock(parent) && !ts.isSourceFile(parent))) return out;
    const siblings = (parent as ts.Block | ts.SourceFile).statements;
    const before = siblings.indexOf(statement as ts.Statement) > 0 ? siblings[siblings.indexOf(statement as ts.Statement) - 1] : undefined;
    if (!before) return out;
    out.push(before);
    statement = before;
  }
  return out;
}

/**
 * The comment that defends the effect: whatever leads the effect, plus whatever leads
 * the statement immediately above it. Both halves are needed — of the four sites, two
 * put the prose above the effect and two above a local the effect also reads.
 * `getFullStart`, not `getStart`: the latter is already past the comment.
 */
function proseBefore({ text, sf }: Source, effect: ts.CallExpression): string {
  const parts: string[] = [];
  for (const node of [effect, ...statementsAbove(effect)]) {
    for (const range of ts.getLeadingCommentRanges(text, node.getFullStart()) ?? []) parts.push(text.slice(range.pos, range.end));
  }
  return parts.join("\n");
}

/** The four sites, each with the dependency list it is allowed to keep. */
const NARROW: readonly { file: string; deps: string[]; why: string }[] = [
  {
    file: "./ui/settings.tsx",
    deps: ["value"],
    why: "the box keeps the operator's characters until the figure they parse to moves",
  },
  {
    file: "./screens/portal/PhoneSignIn.tsx",
    deps: ["siteId", "query.data?.siteId"],
    why: "the draft is seeded per site, and a refetch must not discard a half-typed key",
  },
  {
    file: "./screens/portal/PageBuilder.tsx",
    deps: ["faulted?.id", "fault?.fault"],
    why: "the fault's identity, not the design it was found in, which changes on every keystroke",
  },
  {
    file: "./screens/Portal.tsx",
    deps: ["siteId", "settings?.siteId"],
    why: "a new object for the same site is a refetch, and reseeding on it is what loses a hero photograph",
  },
];

describe("the suppression fossils", () => {
  it("finds the tree it is meant to be checking", () => {
    // Guards the guard: a glob that matched nothing would pass every assertion below.
    expect(SOURCES.length).toBeGreaterThan(50);
    for (const { file } of NARROW) expect(SOURCES.map((s) => s.path)).toContain(file);
  });

  it("leaves no eslint suppression in web/admin/src", () => {
    const fossils = SOURCES.flatMap(({ path, text }) =>
      text
        .split("\n")
        .flatMap((line, i) => (/eslint-(disable|enable)/.test(line) ? [`${path}:${i + 1}: ${line.trim()}`] : [])),
    );
    expect(
      fossils,
      `web/ does not run ESLint. A suppression comment here is a fossil, not a warning:\n${fossils.join("\n")}`,
    ).toEqual([]);
  });
});

describe("the four deliberately narrow dependency lists", () => {
  for (const { file, deps, why } of NARROW) {
    const source = read(file);
    const written = `[${deps.join(", ")}]`;

    it(`${file}: ${written} — one effect has this list, and a sentence says why`, () => {
      const found = effectsWithDeps(source, written);
      expect(
        found.length,
        `${file} has no useEffect with the dependency list ${written} — the list changed, so the comment above it is now about something else`,
      ).toBe(1);
      const prose = proseBefore(source, found[0]!);
      // Every dependency named, so the comment is about *this* list.
      for (const name of deps) expect(prose, `${file}: the comment does not name \`${name}\``).toContain(name);
      // Longer than the fossil it replaced, and longer than a shrug.
      expect(prose.length, `${file} needs prose that says why the list is narrow, not a line about a linter`).toBeGreaterThan(200);
      expect(prose.split("\n").filter((l) => l.trim()).length).toBeGreaterThanOrEqual(5);
      expect(prose, `${file}: the comment should say what the list is for — ${why}`).toMatch(
        /\b(text|site|payload|design|figure|characters|identity|value)\b/,
      );
    });
  }
});
