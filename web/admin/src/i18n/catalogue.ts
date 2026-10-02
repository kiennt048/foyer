/**
 * What a catalogue is, and the one function that reads one.
 *
 * This file exists because the console's copy is not a single object. It is
 * `core.ts` plus one module per feature that is loaded lazily, and every one of them
 * needs the same lookup with a different set of keys behind it. `makeT` is that
 * lookup, and it is generic over the catalogue so each namespace's `t` refuses a key
 * that namespace does not have — see `index.ts` for why that refusal is the point.
 */

/** A catalogue entry: a finished sentence, or one that needs values first. */
import { LANG } from "./lang.js";

export type Entry = string | ((...args: never[]) => string);

/**
 * Any catalogue. Used only as a *constraint* — never as the type of a catalogue,
 * because an index signature would make every key valid and there would be nothing
 * left to refuse.
 */
export type Catalogue = { readonly [key: string]: Entry };

/**
 * The arguments an entry takes. A plain string takes none; an interpolating entry
 * takes exactly the parameters it declares, in order, with their types.
 */
type Params<C extends Catalogue, K extends keyof C> = C[K] extends (...args: infer A) => string
  ? A
  : [];

/**
 * A translation of one catalogue: every key it has, with the same parameters. The
 * type is the completeness check — a Vietnamese catalogue missing a key, or taking
 * an interpolating entry's arguments differently, does not compile — and
 * `i18n.test.ts` repeats it at runtime over every module, for the file the
 * compiler was never shown.
 */
export type Translation<C extends Catalogue> = {
  readonly [K in keyof C]: C[K] extends (...args: infer A) => string ? (...args: A) => string : string;
};

/**
 * The entries of the language in force, installed over the English ones.
 *
 * One flat map for every namespace is sound because a key is defined exactly once
 * across the console (`admin.test.ts`, "defines every key exactly once"). Empty
 * while the console speaks English, which is the source of truth and needs nothing
 * installed.
 */
const installed: Record<string, Entry> = {};

/** Every translation still downloading. */
const pending = new Set<Promise<void>>();

/**
 * Fetch and install one namespace's translation, when the console is not speaking
 * English. Each namespace module calls this as it loads; `withCopy` and
 * `localesSettled` are how nothing renders before it has finished.
 *
 * **Not a top-level `await`, and that is measured.** Awaiting this at the top of
 * each namespace makes Rolldown split the entry into several chunks so no lazy chunk
 * statically imports an async module — kilobytes more entry for an English reader
 * who downloads no Vietnamese at all. With the promise registered here instead, the
 * entry is one chunk and the cost of this whole mechanism is a few hundred bytes.
 *
 * **A translation that fails to download leaves the English in place** rather than
 * failing the screen: an operator on a venue LAN whose uplink is down — the moment
 * this console exists for — must still get a working screen. The catch is the only
 * fallback, and it is to a complete catalogue, never to a key.
 */
export function loadLocale(load: () => Promise<{ vi: Record<string, Entry> }>): Promise<void> {
  if (LANG === "en") return Promise.resolve();
  const p: Promise<void> = load()
    .then((m) => void Object.assign(installed, m.vi))
    .catch(() => {
      /* English stays. */
    })
    .finally(() => pending.delete(p));
  pending.add(p);
  return p;
}

/** Resolves once every translation asked for so far is installed (or given up on). */
export async function localesSettled(): Promise<void> {
  while (pending.size > 0) await Promise.all([...pending]);
}

/**
 * A module load that resolves only once the translations its module asked for
 * are installed. **Every dynamic `import()` of a screen goes through this** — a
 * `lazy()` route, a nested dialog, the print kit — because the namespace a module
 * imports starts its Vietnamese downloading while the module loads, and the first
 * render must not beat it. `i18n.test.ts` refuses a dynamic import that does not.
 */
export function withCopy<M>(load: () => Promise<M>): Promise<M> {
  return load().then(async (m) => {
    await localesSettled();
    return m;
  });
}

/**
 * Copy a module wants to hold as a constant — a label per enum member, a list of
 * options — computed on first use rather than when the module loads.
 *
 * A module's top level runs before its translation has arrived, so a label map
 * built there would be English for a Vietnamese reader for the life of the page.
 * This defers the build to the first property read, which is a render and so after
 * `withCopy`; `i18n.test.ts` refuses a `t()` at any module's top level. Objects
 * only: a list is a function that returns one.
 */
export function lazyCopy<T extends object>(build: () => T): T {
  let value: T | undefined;
  const get = () => (value ??= build());
  return new Proxy({} as T, {
    get: (_, k) => Reflect.get(get(), k),
    has: (_, k) => Reflect.has(get(), k),
    ownKeys: () => Reflect.ownKeys(get()),
    getOwnPropertyDescriptor: (_, k) => {
      const d = Reflect.getOwnPropertyDescriptor(get(), k);
      return d ? { ...d, configurable: true } : undefined;
    },
  });
}

/**
 * Build the copy lookup for one catalogue. Total by construction.
 *
 * `K extends keyof C` means a key that is not in *this* catalogue does not compile,
 * and `Params<C, K>` means an interpolating entry cannot be called with the wrong
 * arguments. There is no fallback path and no runtime miss — the guarantee a single
 * catalogue object would give, narrowed, deliberately, to the keys the calling
 * module is entitled to.
 */
export function makeT<C extends Catalogue>(catalogue: C) {
  return function t<K extends keyof C>(key: K, ...params: Params<C, K>): string {
    const entry: unknown = Object.hasOwn(installed, key) ? installed[key as string] : catalogue[key];
    if (typeof entry === "function") {
      return (entry as (...args: unknown[]) => string)(...params);
    }
    return entry as string;
  };
}

/**
 * English needs this and Vietnamese does not. Kept trivial on purpose.
 *
 * It lives here rather than beside the strings because the strings are in many
 * files and most of them count something.
 */
export const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
