import ts from "typescript";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveLang } from "./lang.js";

/**
 * The console in Vietnamese, held four ways,
 * each of which has a way to be quietly false:
 *
 *   1. **Every English key has a Vietnamese one**, of the same kind, in every
 *      catalogue — derived from the directory, so a new namespace without a
 *      translation fails here the moment it exists. The type (`Translation`) says
 *      the same thing at compile time; this is for the file the compiler was never
 *      shown and for a key added with `// @ts-expect-error`.
 *   2. **The language is chosen as the survey in `lang.ts` says**: stored choice,
 *      then browser, then English.
 *   3. **A screen renders in the language it loaded in**: its namespace's
 *      Vietnamese is installed before `withCopy` resolves, and English installs
 *      nothing.
 *   4. **No copy is computed where the translation cannot have arrived yet**: no
 *      `t()` at a module's top level, and no dynamic `import()` of a screen that
 *      skips `withCopy`. Either one renders English to a Vietnamese reader, with no
 *      error anywhere, and on the screens nobody tests in Vietnamese.
 */

type Entry = string | ((...a: unknown[]) => string);
type Cat = Record<string, Entry>;

const EN = import.meta.glob("./*.ts", { eager: true }) as Record<string, Record<string, unknown>>;
const VI = import.meta.glob("./vi/*.ts", { eager: true }) as Record<string, { vi?: Cat }>;

/** The modules in `i18n/` that hold no copy, as `admin.test.ts` names them. */
const NOT_CATALOGUES = ["./index.ts", "./catalogue.ts", "./lang.ts", "./i18n.test.ts"];

/** The English operator catalogue of each module. `print.ts` also exports the slip's
    own two languages, which are the guest's and are not the console's to translate. */
function englishOf(path: string, mod: Record<string, unknown>): Cat {
  const name = path === "./print.ts" ? "print" : Object.keys(mod).find((k) => k !== "t" && typeof mod[k] === "object");
  return (mod[name ?? ""] ?? {}) as Cat;
}

const call = (e: Entry) => (typeof e === "function" ? e(1, "x", "y", 2, 3, 4) : e);

describe("every English key has a Vietnamese one", () => {
  const catalogues = Object.entries(EN).filter(([p]) => !NOT_CATALOGUES.includes(p) && !p.endsWith(".test.ts"));

  it("finds the catalogues it is meant to be checking", () => {
    // Guards the guard: a glob that matched nothing would pass everything below.
    expect(catalogues.length).toBeGreaterThan(20);
    expect(Object.keys(VI).length).toBe(catalogues.length);
  });

  for (const [path, mod] of catalogues) {
    it(`${path.slice(2)}: same keys, same kinds, translated`, () => {
      const en = englishOf(path, mod);
      const translation = VI[`./vi/${path.slice(2)}`]?.vi;
      expect(translation, `${path} has no ./vi/ translation exporting \`vi\``).toBeTruthy();
      const viCat = translation as Cat;
      expect(Object.keys(viCat).sort()).toEqual(Object.keys(en).sort());
      for (const [key, value] of Object.entries(en)) {
        const v = viCat[key];
        expect(typeof v, key).toBe(typeof value);
        expect(call(v as Entry).trim().length, `${key} is empty`).toBeGreaterThan(0);
      }
    });
  }

  it("is a translation rather than a copy of the English", () => {
    let strings = 0;
    let same = 0;
    for (const [path, mod] of catalogues) {
      const en = englishOf(path, mod);
      const viCat = VI[`./vi/${path.slice(2)}`]?.vi ?? {};
      for (const [key, value] of Object.entries(en)) {
        if (typeof value !== "string") continue;
        strings += 1;
        if (viCat[key] === value) same += 1;
      }
    }
    /* Protocol names, vendor names and units are the same in both — FreeRADIUS,
       Message-Authenticator, SHA-256, KB. Anything like a sentence is not. */
    expect(strings).toBeGreaterThan(900);
    expect(same / strings).toBeLessThan(0.08);
  });
});

describe("which language the console speaks", () => {
  it("is the stored choice first, then the browser's, then English", () => {
    expect(resolveLang("vi", ["en-US"])).toBe("vi");
    expect(resolveLang("en", ["vi-VN"])).toBe("en");
    expect(resolveLang(null, ["vi-VN", "en"])).toBe("vi");
    expect(resolveLang(null, ["fr-FR", "en-GB", "vi"])).toBe("en");
    expect(resolveLang(null, ["fr-FR"])).toBe("en");
    expect(resolveLang("klingon", ["VI"])).toBe("vi");
    expect(resolveLang(undefined, [])).toBe("en");
  });
});

describe("a screen's copy, in the language the page loaded in", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  const loadAs = async (stored: string | null) => {
    vi.resetModules();
    vi.stubGlobal("localStorage", { getItem: () => stored, setItem: () => undefined });
    vi.stubGlobal("navigator", { languages: ["en-GB"], language: "en-GB" });
    const catalogue = await import("./catalogue.js");
    const network = await catalogue.withCopy(() => import("./network.js"));
    const index = await import("./index.js");
    return { network, index };
  };

  it("is Vietnamese, core and namespace, once withCopy has resolved", async () => {
    const { network, index } = await loadAs("vi");
    const { vi: viNetwork } = await import("./vi/network.js");
    const { vi: viCore } = await import("./vi/core.js");
    expect(network.t("network.cert.title")).toBe(viNetwork["network.cert.title"]);
    expect(network.t("network.title")).toBe(viCore["network.title"]);
    expect(index.t("signOut")).toBe(viCore.signOut);
    expect(index.LOCALE).toBe("vi-VN");
  });

  it("resolves withCopy only after a slow translation has been installed", async () => {
    // Deterministic, where the test above races a real module load: a translation
    // that takes 30 ms to arrive, asked for while the module loads — exactly what a
    // namespace does — and a screen that must not be handed over before it lands.
    vi.resetModules();
    vi.stubGlobal("localStorage", { getItem: () => "vi", setItem: () => undefined });
    const { loadLocale, makeT, withCopy } = await import("./catalogue.js");
    const t = makeT({ "probe.key": "English" });
    const screen = withCopy(async () => {
      void loadLocale(() => new Promise((ok) => setTimeout(() => ok({ vi: { "probe.key": "Viet" } }), 30)));
      return { t };
    });
    expect((await screen).t("probe.key")).toBe("Viet");
  });

  it("is English, with nothing installed, for an English reader", async () => {
    const { network, index } = await loadAs("en");
    expect(network.t("network.cert.title")).toBe(network.network["network.cert.title"]);
    expect(index.t("signOut")).toBe("Sign out");
    expect(index.LOCALE).toBe("en-GB");
  });
});

describe("no copy is computed before its translation can have arrived", () => {
  const SOURCES = import.meta.glob("../**/*.{ts,tsx}", { query: "?raw", eager: true, import: "default" }) as Record<
    string,
    string
  >;
  const product = Object.entries(SOURCES).filter(
    // `./` is this directory: the catalogues themselves, which are the copy.
    ([p]) => !/\.test\.tsx?$/.test(p) && !p.startsWith("./") && !p.endsWith(".golden.ts"),
  );

  const parse = (path: string, text: string) =>
    ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, path.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);

  /** Every `t(…)` / `printText(…)` call that runs when its module loads. */
  function topLevelCopy(path: string, text: string): string[] {
    const src = parse(path, text);
    const out: string[] = [];
    const visit = (node: ts.Node, deferred: boolean) => {
      const inFn = deferred || ts.isFunctionLike(node) || ts.isClassDeclaration(node);
      if (
        !inFn &&
        ts.isCallExpression(node) &&
        ts.isIdentifier(node.expression) &&
        (node.expression.text === "t" || node.expression.text === "printText")
      ) {
        out.push(`${path}:${src.getLineAndCharacterOfPosition(node.getStart()).line + 1} ${node.getText().slice(0, 60)}`);
      }
      ts.forEachChild(node, (c) => visit(c, inFn));
    };
    visit(src, false);
    return out;
  }

  /** Every dynamic `import("./…")` that is not the argument of a `withCopy(() => …)`. */
  function unwrappedImports(path: string, text: string): string[] {
    const src = parse(path, text);
    const out: string[] = [];
    const visit = (node: ts.Node) => {
      if (ts.isCallExpression(node) && node.expression.kind === ts.SyntaxKind.ImportKeyword) {
        let p: ts.Node | undefined = node.parent;
        let wrapped = false;
        while (p && !ts.isSourceFile(p)) {
          if (ts.isCallExpression(p) && ts.isIdentifier(p.expression) && p.expression.text === "withCopy") {
            wrapped = true;
            break;
          }
          p = p.parent;
        }
        if (!wrapped) out.push(`${path}:${src.getLineAndCharacterOfPosition(node.getStart()).line + 1}`);
      }
      ts.forEachChild(node, visit);
    };
    visit(src);
    return out;
  }

  it("reads the product source, not nothing", () => {
    expect(product.length).toBeGreaterThan(60);
  });

  it("never calls t() at a module's top level", () => {
    // A label map built when its module loads is English for a Vietnamese reader for
    // the life of the page. Use a getter, `lazyCopy`, or compute it in the render.
    expect(product.flatMap(([p, text]) => topLevelCopy(p, text))).toEqual([]);
  });

  it("never loads a screen without waiting for its copy", () => {
    expect(product.flatMap(([p, text]) => unwrappedImports(p, text))).toEqual([]);
  });

  it("is a check that can fail", () => {
    // Both scans against a module that breaks each rule once.
    const bad = `import { t } from "./i18n/x.js";\nconst L = { a: t("k") };\nconst S = lazy(() => import("./S.js"));\n`;
    expect(topLevelCopy("bad.tsx", bad)).toHaveLength(1);
    expect(unwrappedImports("bad.tsx", bad)).toHaveLength(1);
  });
});
