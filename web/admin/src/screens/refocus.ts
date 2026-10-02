import { type RefObject, useEffect, useRef } from "react";

/**
 * Where focus goes when the control it was standing on has just been destroyed.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Baseline without this module**, five destructive surfaces driven from the
 * keyboard, `document.activeElement` read after each one settled:
 *
 * ```
 * delete on /accounts     button "Delete"  →  <body>
 * delete on /garden       button "Delete"  →  <body>
 * delete on /controllers  button "Delete"  →  <body>
 * revoke on /vouchers/…   button "Revoke"  →  <body>
 * disable on /accounts    button "Disable" →  <body>
 * ```
 *
 * One number five times, so a structural cause rather than five screen bugs. The last of the five is a different cause and is
 * fixed differently: the control is merely *disabled* while its request runs and the
 * user agent blurs it, which is what `aria-disabled` on those thirteen controls
 * answers. The other four are genuine removals, and no attribute can help: the
 * element the platform would hand focus back to no longer exists.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The rule, and it is one rule rather than four answers.**
 *
 * > Focus goes to the first action on the row that took the removed row's place;
 * > failing that, the nearest row before it; failing that, the control that adds to
 * > the list.
 *
 * Three things make it the rule rather than a rule.
 *
 * *It keeps the operator's place.* A destructive action in a list is not a change of
 * screen — the operator is triaging thirty rows and has removed one of them. Sending
 * focus to the page heading, or to the toolbar, or to the "add" button as a first
 * choice, all treat one deletion as though it ended the task. Landing on the row that
 * moved up is where a sighted operator's eye already is.
 *
 * *It is expressible where the defect is.* `ui/table.tsx` renders these rows and does
 * not know which of them is being destroyed; the screens know exactly, because they
 * own the button that starts it. Rows on these four tables are deliberately **not**
 * focusable by design, and making them focusable to give focus
 * somewhere to land would add four tab stops per row to a 21-row table. So the
 * landing target is the row's own first action control, which is already a tab stop.
 *
 * *It degrades in the right direction.* Deleting the last row lands on the new last
 * row; emptying the list entirely lands on the empty state's own action, because that
 * is the only control left inside the table; and a refetch that never arrives leaves
 * focus exactly where the platform left it, which is no worse than today.
 *
 * *It has one sibling and it is stated where it applies.* A settings form has no
 * rows, so the same question on `/portal`'s two asset fields is answered by the same
 * rule with the row read as the *action group*: the first remaining action in the
 * group the removed control belonged to. Written out at `Portal.tsx`'s `LogoField`
 * rather than generalised into a second hook — there the landing target already
 * exists and outlives the removal, so it needs no anchor and no waiting.
 *
 * The first control in the row, not the one in the same column: on `/accounts` that
 * is *Disable* rather than *Delete*, and landing a keyboard operator on a Delete
 * button one keystroke after they confirmed a deletion is a mis-press waiting to
 * happen. On `/vouchers` the revoked row keeps its place and loses its only control,
 * so the scan simply walks on to the next row — the same rule, no special case.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why it waits for the rows rather than firing on success.** The mutation's
 * `onSuccess` runs before the list has refetched, so the row that is about to
 * disappear is still on screen and still focusable — focusing anything at that moment
 * puts the operator on a doomed element and loses them a second time. The anchor is
 * therefore armed at the press and spent on the render where `rows` changes identity,
 * which is the render where the removal is actually on screen.
 */

interface Anchor {
  /** The `<tbody>` the removed row lived in. React keeps this node across updates. */
  body: HTMLTableSectionElement;
  /** Its index *within that body* — so the row that takes its place has the same one. */
  index: number;
}

/**
 * Arms a focus target from a destructive action's trigger, and spends it once the
 * list has actually changed.
 *
 * `rows` is the array the screen renders. Identity is the signal: react-query hands
 * back the same array while a refetch is in flight and a new one when it lands.
 */
export function useRefocusAfterRemoval(rows: readonly unknown[]): (trigger: Element | null) => void {
  const anchor = useRef<Anchor | null>(null);

  useEffect(() => {
    const armed = anchor.current;
    if (!armed) return;
    anchor.current = null;
    focusNearestRowAction(armed);
  }, [rows]);

  return (trigger) => {
    const row = trigger?.closest("tr");
    const body = row?.parentElement;
    anchor.current =
      row instanceof HTMLTableRowElement && body instanceof HTMLTableSectionElement
        ? { body, index: row.sectionRowIndex }
        : null;
  };
}

function focusNearestRowAction({ body, index }: Anchor): void {
  /* The table itself can go — a delete that empties a filtered view, a route change
     under a slow request. Nothing to return to, and nothing to force. */
  if (!body.isConnected) return;

  const rows = [...body.rows];
  /* Forward from where the row was, then backward from just before it. The removed
     row's index is the first candidate on purpose: whatever is there now is what took
     its place. */
  const search = [...rows.slice(index), ...rows.slice(0, index).reverse()];

  for (const row of search) {
    /* `a[href]` as well as `button`: two row actions in this console are links to
       where the setting actually lives, and `docs/rules/testing.md` records the
       backstop that counted only `<button>` and silently measured nothing. */
    const control = row.querySelector<HTMLElement>("button:not([disabled]), a[href]");
    if (control) {
      control.focus();
      return;
    }
  }

  /* Nothing left in the table at all — not even the empty state's own action. The
     control that adds to the list is the last thing on the page that is still about
     this list. */
  body
    .closest(".page")
    ?.querySelector<HTMLElement>(".page-actions button:not([disabled]), .page-actions a[href]")
    ?.focus();
}

/* ─────────────────────────────────────────────────────────────────────────────
   The same question one layer down: where focus goes when the *page* changes.
   ───────────────────────────────────────────────────────────────────────────── */

/**
 * Focus after a route change — one rule, applied to every link in the product.
 *
 * **Baseline without this hook**, keyboard only, `document.activeElement` read
 * once each navigation had settled:
 *
 * ```
 * Enter on a /vouchers row       → /vouchers/<batch>    →  <body>
 * Enter on an /access-points row → /access-points/<id>  →  <body>
 * Enter on the sidebar "Sessions" link → /sessions      →  a.nav-item "Sessions"
 * g then v                       → /vouchers            →  a.nav-item "Sessions"
 * ```
 *
 * Two shapes and one defect. A row hands focus to nothing. A link keeps it, which
 * sounds like the better of the two and is not: the operator is still in the
 * sidebar, their next Tab walks the remaining eight destinations rather than
 * entering the page they just asked for, and nothing anywhere has said the page
 * changed. In a document the user agent does this and does it correctly; a
 * client-side route change is a DOM edit, so it is ours.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The rule.**
 *
 * > On a route change, focus the new page's `<h1 class="page-title">`. Never on the
 * > first page of the session, where the browser has already placed focus correctly
 * > and nothing has changed yet.
 *
 * The heading is the right target for the same reason `useRefocusAfterRemoval`
 * lands inside the table: it is where the operator's attention has *already* gone.
 * It is also the top of the page's own tab order, so the next Tab enters the page
 * rather than resuming the navigation the operator has finished with.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **A route change is a `PageHead` that mounts under a different pathname, and
 * nothing else.** That sentence is the whole of this hook. The console puts
 * selection, filters and the dock in the URL, so a rule that compared pathnames
 * would fire on arrow keys, on opening the session dock, and on a dialog that has a
 * route of its own. Driven rather than reasoned — the h1 node was tagged, the
 * transition performed, and the tag looked for afterwards:
 *
 * ```
 * /sessions        ArrowDown → /sessions?session=…    h1 node kept     not a navigation
 * /access-points   Add       → /access-points/new     h1 node kept     not a navigation
 * /access-points/new Escape  → /access-points         h1 node kept     not a navigation
 * /vouchers        Enter row → /vouchers/<batch>      h1 node replaced a navigation
 * ```
 *
 * The middle two are what make this a rule rather than a comparison.
 * `/access-points/new` is a *dialog with a URL* — `AccessPoints.tsx` says why it has
 * one — and both its open and its close move the pathname while the page behind it
 * never moves. Watching the heading instead of the location is what lets the wizard
 * keep the focus it takes, at no cost: the heading is the page.
 *
 * **That distinction is measured**, because a guard that has never been red only
 * looks like a check. With the dependency list below as `[pathname]` — the naive rule,
 * and the one anybody would write first — and the wizard driven from the keyboard:
 *
 * ```
 * deps [pathname]   Escape from the register wizard  →  h1.page-title "Access points"
 * deps []           Escape from the register wizard  →  button "Add access point"
 * ```
 *
 * So the naive rule does not merely over-fire: it takes focus back off the control
 * the operator opened the dialog with, half a frame after `Modal` returned it, and
 * turns the `dialog-focus-return` lock red.
 *
 * The `previous === pathname` arm is the other half of the same idea. A screen that
 * swaps its whole body for a loading state and back has not navigated. Measured on
 * `/portal`, the one screen with two `PageHead` call sites: with the settings
 * request held for 900 ms, "Loading…" rendered and **one** heading node served both
 * branches, because they share a `<div class="page">` prefix and React reconciles
 * them. So the arm is unexercised today and is kept anyway — it is the arm that
 * decides what happens if that prefix ever stops matching, and the answer it gives
 * (do nothing) is the safe one.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The heading is not also announced, and that is a decision rather than an
 * omission.** `PageHead` is on every screen, so a live region here would be a live
 * region everywhere. Focus landing on an element with `tabIndex={-1}` is already an
 * announcement — a screen reader reads "Vouchers, heading level 1" — so a region
 * beside it says the same words twice. Worse, it would say them when no navigation
 * happened at all: `/access-points/<id>` titles itself with the device's name, which
 * arrives from a fetch, and `/vouchers/<batch>` with the batch's. One mechanism, and
 * it is the one that also puts the keyboard where the words are.
 */

/** The pathname the most recent *page* mounted under; `null` until one has. */
let mountedUnder: string | null = null;

export function useFocusOnNavigation(pathname: string): RefObject<HTMLHeadingElement | null> {
  const heading = useRef<HTMLHeadingElement>(null);

  /* Mount is the signal and the empty dependency list is load-bearing: a `PageHead`
     that stays mounted while the URL moves under it is the same page, whatever the
     location says. `pathname` is read from the render that mounted it. */
  useEffect(() => {
    const previous = mountedUnder;
    mountedUnder = pathname;
    if (previous === null || previous === pathname) return;
    heading.current?.focus();
  }, []);

  return heading;
}

/**
 * A page that has no `PageHead`, saying so.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Baseline without this hook**, keyboard only, `document.activeElement` read once
 * the transition had settled:
 *
 * ```
 * cold sign-in           /sign-in → /            →  <body>
 * signed out on /operators, then back in         →  <body>
 * ```
 *
 * Without it, the sign-in transition is **the one route change in this console that
 * drops focus**, and both arms of the exemption above produce it. `SignInScreen`
 * renders outside `Shell` and has no `PageHead`, so nothing updates `mountedUnder`
 * while `/sign-in` is on screen: a cold sign-in meets `previous === null`, and signing
 * back in to where you were meets `previous === pathname`, because the last page to
 * mount a header is the page the operator is being returned to.
 *
 * **The exemption is right and its premise is what fails here.** It is written for
 * *"the browser has already placed focus correctly and nothing has changed"* — and
 * here focus is on a submit button that the navigation destroys, which is the one
 * shape where the platform has nowhere to put it.
 *
 * **So this is not a second rule, it is the first rule being told the truth.** A route
 * change is a page mounting under a different pathname; the sign-in screen is a page,
 * and without this it is invisible to the only thing that records them. Recording it repairs both
 * arms at once, which is the property that matters: a fix that seeds `mountedUnder`
 * at boot repairs the cold case and leaves the return case — the common one under
 * session enforcement, and the entire reason `location.state.from` exists — exactly
 * as broken, with a green test beside it.
 *
 * It records and does **not** focus, deliberately. On the sign-in screen the browser
 * really has placed focus correctly: the username field carries `autoFocus`, it is the
 * operator's next action, and there is nothing above it to read first.
 */
export function useRouteWithoutPageHead(pathname: string): void {
  useEffect(() => {
    mountedUnder = pathname;
  }, []);
}
