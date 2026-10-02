import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  chordOf,
  findExact,
  hasPending,
  type BindingSpec,
  type Chord,
} from "./bindings.js";

/**
 * The live keyboard registry.
 *
 * One `keydown` listener for the whole console. Screens declare what they support by
 * calling `useShortcut`; the listener dispatches, and the reference sheet and the
 * command palette read the same list back out. Nothing else may add a global key
 * handler — a listener added in a screen is invisible to the sheet, which is the
 * failure this replaces.
 */

export interface Registration {
  spec: BindingSpec;
  run: () => void;
  /** Registered but not currently actionable. Still listed — it exists here. */
  enabled: boolean;
}

interface KeyboardApi {
  registrations: readonly Registration[];
  register: (registration: Registration) => () => void;
  pushSuspend: () => () => void;
}

const Ctx = createContext<KeyboardApi | null>(null);

/** A multi-key sequence is abandoned if the second key does not arrive. */
const SEQUENCE_TIMEOUT_MS = 1500;

function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName;
  return (
    tag === "INPUT" ||
    tag === "TEXTAREA" ||
    tag === "SELECT" ||
    target.isContentEditable ||
    target.closest("[contenteditable='true']") !== null
  );
}

/** `Enter`/`Space` belong to a focused button or link, not to the list beneath it. */
function isActivationTarget(target: EventTarget | null, chord: Chord): boolean {
  if (chord.key !== "Enter" && chord.key !== " ") return false;
  if (chord.mod || chord.shift) return false;
  if (!(target instanceof HTMLElement)) return false;
  return target.closest("button, a[href], summary") !== null;
}

export function KeyboardProvider({ children }: { children: ReactNode }) {
  const [registrations, setRegistrations] = useState<readonly Registration[]>([]);
  const [suspendCount, setSuspendCount] = useState(0);

  const register = useCallback((registration: Registration) => {
    setRegistrations((prev) => [...prev, registration]);
    return () => setRegistrations((prev) => prev.filter((r) => r !== registration));
  }, []);

  const pushSuspend = useCallback(() => {
    setSuspendCount((n) => n + 1);
    return () => setSuspendCount((n) => n - 1);
  }, []);

  const pending = useRef<Chord[]>([]);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const reset = () => {
      pending.current = [];
      if (timer.current) clearTimeout(timer.current);
      timer.current = null;
    };

    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.repeat) return;
      /*
       * A press something else has already claimed is not the registry's.
       *
       * This listener is on `window`, in the bubble phase, so it is the *last* thing
       * to see any key — which makes `defaultPrevented` an accurate record of whether
       * anything nearer the press dealt with it. Without the check the registry acts
       * on a key a dialog has already answered: `Modal`'s Escape handler calls
       * `preventDefault()` and not `stopPropagation()`, so the press dismissed the
       * dialog *and* ran whatever `Escape` is bound to on the screen behind it. That
       * is latent today only because no screen both registers *Close detail* and
       * renders a `Modal`, which is a fact about the current nine screens rather than
       * a property of anything, and it stops being true the first time a session gets
       * a confirmation dialog.
       *
       * Kept as well as the suspend `Modal` now performs, because the two cover
       * different things and only look like the same fix. Suspension is a claim on
       * *every* plain key for as long as an overlay is up, and it works only for code
       * that knows the registry exists. This is a claim on exactly one press, and it
       * works for code that does not — a `<details>` element, a future component, a
       * library. Neither subsumes the other; the alternative to the pair is choosing
       * which half of the hole to leave.
       */
      if (event.defaultPrevented) {
        reset();
        return;
      }
      const chord = chordOf(event);

      /*
       * Where a plain key may not act. A modifier chord is safe everywhere — it
       * cannot be something the operator meant to type — so ⌘K opens the palette
       * from inside a search box and from underneath an open overlay. Everything
       * else defers to what has focus.
       */
      const plain = !chord.mod;
      if (plain && (suspendCount > 0 || isTypingTarget(event.target))) {
        reset();
        return;
      }
      if (isActivationTarget(event.target, chord)) {
        reset();
        return;
      }

      const active = registrations.filter((r) => r.enabled);
      const pressed = [...pending.current, chord];

      const exact = findExact(active, pressed);
      if (exact) {
        event.preventDefault();
        reset();
        exact.run();
        return;
      }

      if (hasPending(active, pressed)) {
        // A prefix such as `G` or `F`: hold it, and let go if nothing follows.
        event.preventDefault();
        pending.current = pressed;
        if (timer.current) clearTimeout(timer.current);
        timer.current = setTimeout(reset, SEQUENCE_TIMEOUT_MS);
        return;
      }

      reset();
    };

    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      reset();
    };
  }, [registrations, suspendCount]);

  const value = useMemo<KeyboardApi>(
    () => ({ registrations, register, pushSuspend }),
    [registrations, register, pushSuspend],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

function useKeyboard(): KeyboardApi {
  const api = useContext(Ctx);
  if (!api) throw new Error("useKeyboard outside KeyboardProvider");
  return api;
}

/**
 * Declare that this component handles a binding, for as long as it is mounted.
 *
 * `enabled: false` keeps the binding listed in the reference sheet while stopping it
 * from firing — "Close detail" is a real shortcut on this screen even at the moment
 * nothing is open, and hiding it would make the sheet change under the operator.
 */
export function useShortcut(spec: BindingSpec, run: () => void, enabled = true): void {
  const { register } = useKeyboard();
  const runRef = useRef(run);
  runRef.current = run;

  useEffect(
    () => register({ spec, enabled, run: () => runRef.current() }),
    [register, spec, enabled],
  );
}

/** Everything currently registered, in declaration order. Read by the sheet. */
export function useShortcuts(): readonly Registration[] {
  return useKeyboard().registrations;
}

/**
 * Run a binding from a button.
 *
 * The other half of "every shortcut is discoverable from visible UI": a visible
 * control and its shortcut must be the *same* action, not two implementations that
 * drift. A button that needs to do what `⌘/` does asks the registry to do it.
 */
export function useRunShortcut(): (spec: BindingSpec) => void {
  const { registrations } = useKeyboard();
  return useCallback(
    (spec: BindingSpec) => {
      registrations.find((r) => r.spec.id === spec.id && r.enabled)?.run();
    },
    [registrations],
  );
}

/**
 * Silence plain-key shortcuts while an overlay owns the keyboard.
 *
 * Without this, `J` typed into the command palette's filter would also walk the list
 * behind it. Modifier chords deliberately survive, so ⌘K still closes the palette
 * it opened.
 *
 * **Tolerant of there being no provider, which `useShortcut` deliberately is not.**
 * A registration that goes nowhere is a shortcut the reference sheet cannot list and
 * nothing runs — worth throwing over. Suspending a registry that does not exist is
 * not: with no listener there is nothing to silence, so the no-op and the correct
 * behaviour are the same thing. It became a live distinction the moment `Modal`
 * started calling this on behalf of every dialog, because `renderToStaticMarkup` of
 * a screen that has one is a legitimate caller with no provider above it.
 *
 * The dependency is `pushSuspend` and not the context value: the value is rebuilt on
 * every registration, and an effect keyed on it would drop and retake the suspension
 * each time a screen registered a shortcut.
 */
export function useKeyboardSuspend(active: boolean): void {
  const pushSuspend = useContext(Ctx)?.pushSuspend;
  useEffect(() => {
    if (!active || !pushSuspend) return;
    return pushSuspend();
  }, [active, pushSuspend]);
}
