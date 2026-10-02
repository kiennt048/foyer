import { useSyncExternalStore } from "react";

/**
 * Where the console stops being a two-column application.
 *
 * **Declared once, here.** `admin.css` carries this exact string after its `@media`,
 * `matchMedia` is handed the same constant, and `layout.test.ts` asserts the
 * stylesheet contains it and that no *other* screen width query exists in the file.
 * The two languages cannot disagree about what "narrow" means.
 *
 * That matters more than a shared constant usually does, because the two halves fail
 * in opposite directions and neither failure shows up where anybody looks. A CSS
 * breakpoint one pixel wider than the JS one is a drawer that opens onto a sidebar
 * that is still there; one pixel narrower is a header with a menu button whose panel
 * is `display: none`. Both are correct at every width except the handful between the
 * two numbers, which is the range nobody resizes to on purpose.
 *
 * 900px is not a phone width and is not chosen here — it is the value the responsive
 * block in `admin.css` was already written against, and the width at which nine
 * navigation items and a nine-column table stop fitting side by side.
 */
export const NARROW = "(max-width: 900px)";

/* One `MediaQueryList` for the process. `matchMedia` is cheap but not free, and
   `useSyncExternalStore` calls the snapshot on every render of every subscriber. */
let list: MediaQueryList | null = null;
const media = () => (list ??= window.matchMedia(NARROW));

/*
 * Both events, and what could not be proven about either.
 *
 * `change` on a `MediaQueryList` is the correct notification. `resize` is the wider
 * net for the same fact, costs a listener, and is what a device rotation raises on
 * hardware whatever the media query does.
 *
 * Whether an event arrives depends on the harness. Some drivers update `innerWidth`
 * and re-evaluate every `@media` rule while delivering no event — at 1280 → 420 with
 * both listeners attached, `matches` flips and both counts stay at zero — so one
 * harness is not evidence about all of them.
 *
 * `page.setViewportSize` on Playwright 1.62.1's Chromium delivers **both** — one
 * `resize` and one `MediaQueryList` `change`, in both directions, including at that
 * same 1280 → 420. `drawer-focus` asserts the drawer really closes on widen because
 * of it. **The event arrives a turn after the call resolves**, so a synchronous read
 * sees zero and fails looking exactly like a harness that sends nothing; poll for it.
 *
 * Keep both listeners regardless. `change` is the correct notification, `resize` is
 * the wider net for the same fact, and a device rotation on hardware raises the
 * second whatever the media query does — which is the case none of these drivers
 * reproduces at all. A drawer that stays open under a harness that sends nothing is
 * the harness, not the console; the symptom is a stray "Menu" button in a full-width
 * sidebar, because every rule that makes this a drawer lives inside the media query
 * and has already stopped applying.
 */
const subscribe = (onChange: () => void) => {
  const m = media();
  m.addEventListener("change", onChange);
  window.addEventListener("resize", onChange);
  return () => {
    m.removeEventListener("change", onChange);
    window.removeEventListener("resize", onChange);
  };
};

/**
 * True while the viewport is narrow enough that the navigation lives in the drawer.
 *
 * A subscription rather than a width read once at mount, because a rotation is a
 * resize: without it, turning a phone to landscape leaves an open drawer holding the
 * focus trap over a layout that no longer renders the button that would close it.
 * The drawer is the only thing in the console that cannot survive its own viewport.
 */
export function useNarrow(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => media().matches,
    // Rendered on a server there is no viewport, and the wide layout is the one that
    // needs no JavaScript to be correct. The admin does not server-render today; this
    // is what keeps the hook honest if a test ever renders the shell without a DOM.
    () => false,
  );
}
