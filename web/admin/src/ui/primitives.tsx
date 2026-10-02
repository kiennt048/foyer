import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import type { Capabilities, Freshness, Plan, Session } from "@foyer/contract";
import { freshnessLevel, freshnessNote, type StatusLevel } from "../format.js";
import { errorCopy } from "../problems.js";
import { t } from "../i18n/index.js";
import { useKeyboardSuspend } from "../keys/registry.js";
import { Alert, Check, Clock, Copy, Danger, Info, Inbox, Slash } from "./icons.js";

/* ── the non-hue half of a status ─────────────────────────────────────────── */

/**
 * The glyph a status level carries, derived from the level and not from the call
 * site.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **A stated contract has to be enforced, not remembered.** `Pill`'s own comment says
 * *the label carries the meaning and colour is redundant reinforcement rather than
 * the only signal*. With `icon` as an optional prop the contract would be
 * per-call-site, kept in exactly the places somebody remembered — across 143 pills on
 * nine routes that means a glyph on every `urgent` and `warning` pill, none on any
 * `negative` one, and `positive` inconsistent inside a single table. So the glyph
 * comes from the level, not from the call site.
 *
 * The eight tints sit at **deliberately equal luminance** — `tokens.css` records the
 * reason and it is not being reopened here — so hue is the only thing separating
 * them. Measured `negative` against `positive`: **1.003:1** light. In greyscale,
 * without a glyph, `/attempts` would be twenty-one identical chips.
 *
 * ── WHY THESE FIVE SHAPES ───────────────────────────────────────────────────
 *
 * Five silhouettes across eight levels, chosen so that the pairs an operator has to
 * tell apart differ by *outline* and not by what is inside a circle:
 *
 *   urgent                                Danger   triangle
 *   warning · caution-major · caution-minor  Alert  circle, mark high
 *   negative                              Slash    circle, struck through
 *   positive                              Check    a tick, no circle at all
 *   info                                  Info     circle, mark low
 *   neutral                               —        a neutral pill makes no claim
 *
 * `neutral` is deliberately glyphless rather than given a fifth circle: it is the
 * absence of a verdict, and drawing a symbol for *no opinion* is how a console ends
 * up with a status vocabulary that means nothing.
 *
 * ── WHAT IS NOT CLOSED ──────────────────────────────────────────────────────
 *
 * `warning` / `caution-major` / `caution-minor` share a shape, so within the caution
 * family the tint is still the only difference. Closing that needs either a per-level
 * luminance — the settled decision, which would make a `neutral` notice shout — or a
 * severity word in the copy, which is a change to what every notice *says* rather
 * than to how it is drawn. Recorded rather than quietly left.
 */
const LEVEL_GLYPH: Record<StatusLevel, ((p: { size?: number }) => ReactNode) | null> = {
  neutral: null,
  info: Info,
  positive: Check,
  negative: Slash,
  "caution-minor": Alert,
  "caution-major": Alert,
  warning: Alert,
  urgent: Danger,
};

/* ── status pill ──────────────────────────────────────────────────────────── */

export interface PillSegment {
  label: string;
  icon?: ReactNode;
  title?: string;
}

/**
 * A status pill, optionally with qualifier segments joined onto it.
 *
 * The qualifier is a *segment*, not a change of hue, because status and freshness
 * are independent facts: a session is Online, and separately its last sample is 20
 * minutes old. Turning the whole row amber for the second corrupts the first.
 *
 * Text is always dark-on-tint, so the label carries the meaning and colour is
 * redundant reinforcement rather than the only signal.
 *
 * **And the glyph comes from the level, not from the caller.** See `LEVEL_GLYPH`:
 * the sentence above was true of the component and false of a third of its call
 * sites, because an optional prop makes a contract into a reminder.
 */
export function Pill({
  level,
  label,
  icon,
  qualifiers = [],
  title,
}: {
  level: StatusLevel;
  label: string;
  /**
   * A glyph of this call site's own — the session heartbeat, a clock on a
   * freshness qualifier.
   *
   * **Omitted means the level's glyph, not none.** `null` is the way to say *no
   * glyph here*, and it is deliberately harder to type than forgetting: the
   * forgetting is what shipped fifteen `Rejected` chips that differed from
   * `Accepted` by 1.003:1 of background luminance and nothing else.
   */
  icon?: ReactNode;
  qualifiers?: PillSegment[];
  title?: string;
}) {
  const Glyph = LEVEL_GLYPH[level];
  const glyph = icon === undefined ? (Glyph ? <Glyph size={11} /> : null) : icon;
  return (
    <span className="pill" data-level={level} title={title}>
      <span className="pill-seg">
        {glyph}
        {label}
      </span>
      {qualifiers.map((q) => (
        <span className="pill-seg pill-seg-qualifier" key={q.label} title={q.title}>
          {q.icon}
          {q.label}
        </span>
      ))}
    </span>
  );
}

/**
 * A pulse that means "updates are arriving", and stops when they stop.
 *
 * This is the cheapest honest signal in the product: motion that is load-bearing
 * rather than decorative.
 */
export function Heartbeat({ still }: { still: boolean }) {
  return <span className="heartbeat" data-still={still} />;
}

/**
 * The freshness qualifier for any accounting-derived figure.
 *
 * **It needs the session's state, because `stale` is a claim about the future.**
 * `stale` means an Interim-Update is overdue — and nothing is overdue for a session
 * that has stopped. After `Acct-Stop` the counters are final, so `Ended | stale`
 * was meaningless at best and, read the obvious way, implied the numbers might
 * still move.
 *
 * `absent` is not suppressed the same way, and the difference is the point: "this
 * device sends no accounting" stays true after the stop record, and the figures it
 * qualifies are still blanks rather than final values.
 */
export function freshnessQualifier(f: Freshness, state?: Session["state"]): PillSegment | null {
  if (f.state === "live" || f.state === "sampled") return null;
  if (f.state === "stale" && state === "ended") return null;
  return {
    label:
      f.state === "absent" ? t("freshness.qualifier.absent") : t("freshness.qualifier.stale"),
    icon: <Clock size={11} />,
    title: freshnessNote(f),
  };
}

export { freshnessLevel };

/* ── venue-authored text ──────────────────────────────────────────────────── */

/**
 * A plan's name, marked with the language it is actually written in.
 *
 * `Plan.name` is venue text and Foyer never translates it, so the only honest thing
 * a console can do with it is say what it is. Every fixture plan name is Vietnamese
 * and the console is often read in English, which is the ordinary case at a Vietnamese
 * venue rather than an edge one: without the tag a screen reader pronounces the venue's
 * words with English phonemes, and a browser's offer to translate the page never
 * fires for them.
 *
 * `nameLang` absent means "assume the document's language", which is the right
 * default for a deployment that has only ever had one.
 */
export function PlanName({ plan }: { plan: Plan | null | undefined }) {
  if (!plan?.name) return null;
  if (!plan.nameLang) return <>{plan.name}</>;
  return <span lang={plan.nameLang}>{plan.name}</span>;
}

/* ── notice ───────────────────────────────────────────────────────────────── */

/**
 * The longest string a notice's *verdict* may be.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `Notice` is one component for four speech acts, and on two
 * surfaces **an entire paragraph was passed as `title`** — so 3–4 lines rendered at
 * weight 650, the weight that everywhere else in this console means *the one-line
 * verdict*.
 *
 * The bound is not a taste call, it is a census. Every notice the console can render,
 * over fourteen routes: **25 distinct titles. 23 of them are 53 characters or fewer,
 * and every one of those 23 carries a body.** The two that do not carry a body are
 * **184 and 214 characters** and are the two that were at fault. There is nothing
 * between 53 and 184, so the line can be drawn anywhere in that gap; 96 is chosen for
 * being a long way clear of the longest real verdict and a long way short of the
 * shortest piece of prose. The gallery labels the intended shape in its own copy:
 * *"a one-line title that names the fault"*.
 *
 * **Why the rule lives here and not in a review comment.** `title` being required and
 * `children` optional is what made this the *easy* mistake: a caller holding one
 * paragraph and no verdict puts the paragraph in the slot that is not optional. That
 * is the caller's discipline standing in for a type, on a component with thirty call
 * sites — and it failed twice. So the component refuses to render prose as a verdict:
 * an over-long title is the body, at the body's weight, and the notice renders with no
 * title rather than with a false one.
 *
 * That leaves a real and *visible* gap — a notice with no verdict — which is the
 * honest state of those two call sites and the thing that gets them fixed. Writing a
 * verdict for them needs new copy, and copy is not this file's to invent.
 */
const VERDICT_MAX = 96;

export function Notice({
  level,
  title,
  children,
  action,
  role,
}: {
  level: StatusLevel;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  /**
   * `alert` for a notice that appears **because something just failed**.
   *
   * Only for a node that mounts with its text. A present-and-empty `role="alert"`
   * that is later filled announces nothing in most screen readers, which is how a
   * console can carry a live region on every route and still be silent — so this is
   * a prop rather than a default, and it belongs on the `{error ? … : null}` sites.
   */
  role?: "alert" | "status";
}) {
  /*
   * `LEVEL_GLYPH`, and `Info` for the one level that has none of its own.
   *
   * This was `level === "urgent" || level === "warning" ? Alert : Info` — a
   * two-state glyph across eight levels, with `urgent` and `warning` in the same
   * bucket. `Notice` is where that mattered most: `Pill`'s premise is that the label
   * is the verdict ("Unreachable", "Refused"), and a notice's title is *the fault*,
   * never its severity, so the level appeared nowhere in the text and nowhere in the
   * shape. Measured `urgent` against `warning`: 1.001:1 light, 1.084:1 dark.
   *
   * A notice always carries a glyph — unlike a pill, it has no tinted chip around a
   * word, so the row would start with a hole.
   */
  const Glyph = LEVEL_GLYPH[level] ?? Info;
  /* See `VERDICT_MAX`. A paragraph is not a verdict, so it renders as one. */
  const prose = title.length > VERDICT_MAX;
  return (
    <div className="notice" data-level={level} role={role}>
      <Glyph size={16} />
      <div style={{ flex: 1, minWidth: 0 }}>
        {prose ? null : <div className="notice-title">{title}</div>}
        {prose ? <p className="notice-body">{title}</p> : null}
        {/*
         * `<div>`, and the one above stays a `<p>`, because they hold different
         * things.
         *
         * `title` is always a string, so a paragraph is the right element for it.
         * `children` is whatever the caller passes, and two call sites already pass a
         * `<ul className="notice-list">` — `ProbeWarnings`, on the registration
         * wizard's third step. A `<ul>` inside a `<p>` is invalid: the parser closes
         * the paragraph early and hoists the list out, so the rendered tree is not the
         * one this component describes, and React 19 logs *"cannot contain a nested"*
         * every time that panel mounts. It has been doing so since the panel shipped.
         *
         * The class carries the styling, so nothing moves. `primitives.test.tsx` pins
         * both halves: the prose title is still a paragraph, and a list child is not
         * inside one.
         */}
        {children ? <div className="notice-body">{children}</div> : null}
      </div>
      {action}
    </div>
  );
}

/**
 * What a mutation or a query threw, as a sentence, announced when it appears.
 *
 * The `title` override is for a surface that already names the request — *Changes
 * not saved*, *Controller not added*. The type says **why** the request was refused
 * and the screen says **which** request it was; neither can say both, and a screen
 * that drops its own half leaves an operator with a correct sentence about an
 * unidentified action.
 */
export function ErrorNotice({
  error,
  title,
  id,
}: {
  error: unknown;
  title?: string;
  /** For an input's `aria-describedby` to point at. */
  id?: string;
}) {
  const copy = errorCopy(error);
  /* `data-level="urgent"` below, so the glyph is the one an urgent notice carries.
     Read from `LEVEL_GLYPH` rather than naming `Alert`, so this cannot drift from
     `Notice`. */
  const Glyph = LEVEL_GLYPH.urgent ?? Info;
  return (
    <div className="notice" data-level="urgent" role="alert" id={id}>
      <Glyph size={16} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div className="notice-title">{title ?? copy.title}</div>
        <p className="notice-body">{copy.body}</p>
      </div>
    </div>
  );
}

/* ── empty states ─────────────────────────────────────────────────────────── */

/**
 * The never-had-data state. Active language and an action whose label mirrors the
 * title — "No access points." → "Add access point", never "Get started".
 */
export function Empty({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <Inbox size={26} />
      <div className="empty-title">{title}</div>
      {description ? <p className="empty-desc">{description}</p> : null}
      {action}
    </div>
  );
}

/* ── copy ─────────────────────────────────────────────────────────────────── */

/**
 * Copy-to-clipboard that names what it copied.
 *
 * The clipboard write is the first thing in the handler with nothing awaited
 * before it — Safari drops the permission if the call leaves the user-gesture
 * task, and a copy button that silently fails on one browser is worse than none.
 */
export function CopyButton({ value, subject }: { value: string; subject: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="id-action"
      title={t("common.copy", subject)}
      aria-label={t("common.copy", subject)}
      onClick={(e) => {
        e.stopPropagation();
        void navigator.clipboard.writeText(value);
        setDone(true);
        setTimeout(() => setDone(false), 2000);
      }}
    >
      {done ? <Check size={13} /> : <Copy size={13} />}
    </button>
  );
}

/* ── the line under a figure ──────────────────────────────────────────────── */

/**
 * What the line beneath a figure is *doing*.
 *
 * `provenance` — how old the figure is. *"Sampled 25 minutes ago, every 5 minutes."*
 * `live`       — a claim that it is current. *"Live figure."*
 * `alarm`      — a count of things that need acting on. *"3 devices need attention."*
 * `definition` — what the figure counts. *"No stop record."*
 */
export type FootAct = "provenance" | "live" | "alarm" | "definition";

/**
 * The line under a figure, marked with which of the four things it is saying.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `.metric-foot` was one style for all four, measured in one
 * render on `/`: 11px `--text-muted`, four times over. **A 25-minute-old figure and a
 * live one looked equally trustworthy**, and a count of devices needing attention
 * looked as inert as a footnote.
 *
 * `Dashboard.tsx` had already done the structural half — `PlainCard.foot` is
 * `string | null`, so the slot is honest about a card that has nothing to say there.
 * What was missing is that the slot had **one voice for four acts**, and no type said
 * which act a caller was performing. `act` is required for that reason: the
 * distinction survives the next screen that adds a card, without anybody remembering
 * this paragraph.
 *
 * The renderings are in `admin.css`. Only `alarm` gets colour, which is the same rule
 * the pills now follow: hue is for the deviation.
 */
export function Foot({
  act,
  title,
  children,
}: {
  act: FootAct;
  /** The full sentence, where the line is an abbreviation of it — a freshness note. */
  title?: string;
  children: ReactNode;
}) {
  return (
    <div className="metric-foot" data-act={act} title={title}>
      {act === "provenance" ? <Clock size={11} /> : null}
      {/* The console's existing signal for "updates are arriving", and the only
          honest way to draw a liveness claim: it is the same dot a live session
          carries, and it stops when the updates do. */}
      {act === "live" ? <Heartbeat still={false} /> : null}
      {children}
    </div>
  );
}

/**
 * The foot's loading shape.
 *
 * The metric cards skeletoned the value and rendered **no foot at all**, so all four
 * grew 84px → 111px on arrival — measured at 1440 — and the freshness line the
 * frontend rules say never travels without its figure had no loading state to be
 * omitted from. There is no act yet, because the act is a property of the data that
 * has not arrived; what there is, is a box the right size.
 */
export function FootSkeleton() {
  return (
    <div className="metric-foot" aria-hidden="true">
      <span className="skeleton" />
    </div>
  );
}

/* ── skeleton ─────────────────────────────────────────────────────────────── */

/**
 * Loading rows that are the shape of the rows they stand in for.
 *
 * `stackAt` is the index of the identity cell — the first one that is not a selection
 * checkbox. Every list in this console renders a two-line `Stack` there (the value,
 * and a sub-line naming the vendor, the SSID, the batch), which is why an arrived row
 * is 74px against a skeleton row's 44px: the skeleton drew one bar where two arrive,
 * so the table shifted under an operator already reading it.
 *
 * A second, shorter bar rather than a taller one: the sub-line is a *different, less
 * certain* value, and drawing it as its own line is the difference between a loading
 * state that resembles the table and one that merely occupies it.
 */
export function SkeletonRows({
  rows,
  cols,
  stackAt,
}: {
  rows: number;
  cols: number;
  stackAt?: number;
}) {
  return (
    <>
      {Array.from({ length: rows }, (_, r) => (
        <tr key={r}>
          {Array.from({ length: cols }, (_, c) => (
            <td key={c}>
              <span className="skeleton" style={{ width: `${45 + ((r * 7 + c * 13) % 45)}%` }} />
              {c === stackAt ? (
                <span className="skeleton skeleton-sub" style={{ width: `${30 + ((r * 5) % 20)}%` }} />
              ) : null}
            </td>
          ))}
        </tr>
      ))}
    </>
  );
}

/* ── capability gate ──────────────────────────────────────────────────────── */

/**
 * The only way a capability-dependent control reaches the screen.
 *
 * Controls the driver cannot honour **disappear**. A disabled control with a
 * tooltip still advertises a feature the hardware does not have, and an operator
 * who sees it will go looking for the setting that enables it.
 *
 * The exception is a capability that exists but is *currently* unavailable — a
 * reachable NAS that is temporarily down. That is `unavailable`, which renders the
 * child disabled with a stated reason, because the feature is real.
 */
export function IfCapable({
  capabilities,
  need,
  unavailable,
  children,
}: {
  capabilities: Capabilities;
  need: keyof Capabilities;
  /** A reason the action cannot be taken right now, despite being supported. */
  unavailable?: string | null;
  children: (opts: { disabled: boolean; reason: string | null }) => ReactNode;
}) {
  const value = capabilities[need];
  const supported = need === "walledGarden" ? value !== "none" : value === true;
  if (!supported) return null;
  return <>{children({ disabled: Boolean(unavailable), reason: unavailable ?? null })}</>;
}

/* ── dialog ───────────────────────────────────────────────────────────────── */

/**
 * Dismissal on Escape, made reliable.
 *
 * Three things go wrong here, and none is visible from reading the JSX.
 *
 * `cancel` and `close` **do not bubble**, so `onCancel`/`onClose` written as React
 * props depend on React attaching them to the element rather than delegating them
 * at the root. That has changed between React versions and is not something a
 * console should bet its only escape hatch on. Bound on the element here.
 *
 * The second can leave the shortcut sheet not closing at all: an
 * `<input type="search">` holds its own meaning for Escape, and the field can consume
 * the press before it ever becomes a close request — so the keydown reaches the
 * document and no `cancel` ever fires.
 * The keydown listener below is the backstop. It calls `preventDefault`, which stops
 * the UA raising a close request afterwards, so the two paths cannot both fire for
 * one press.
 *
 * The third is not visible from reading either file. The staged Escape on a search
 * field — clear the query, keep focus, and only close on the second press — cannot be
 * a React `onKeyDown` on the input that calls `stopPropagation`. **It cannot work.** React delegates its
 * listeners to the root container, which is an *ancestor* of this dialog, so the
 * bubbling order is field → dialog → root: the listener below has already closed the
 * sheet by the time the field's handler runs, and `stopPropagation` there stops
 * nothing, so the first Escape would close it.
 *
 * So the decision moves here, where the press actually arrives first, and the field
 * opts in with `data-escape-clears`. The division is then the honest one: this hook
 * decides whether a press is a *close request*, and the field decides what else to
 * do with one that is not. An explicit attribute rather than "any text input with a
 * value", because the typed-name confirmation also has a text input and clears
 * nothing — an implicit rule would leave Escape doing nothing at all there, on the
 * one dialog that renders no Cancel button.
 */
export const ESCAPE_CLEARS_ATTR = "data-escape-clears";

/** True when Escape belongs to the focused field rather than to the dialog. */
function escapeBelongsToField(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || !("value" in el)) return false;
  if (!el.hasAttribute(ESCAPE_CLEARS_ATTR)) return false;
  return String((el as HTMLInputElement).value) !== "";
}

function useDismissOnEscape(
  ref: { current: HTMLDialogElement | null },
  onDismiss: () => void,
): void {
  const latest = useRef(onDismiss);
  latest.current = onDismiss;

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const cancel = (event: Event) => {
      event.preventDefault();
      latest.current();
    };
    const keydown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      /* Claimed from the user agent whatever happens next: without this the UA
         raises its own close request afterwards and `cancel` above fires, so the
         staged press would close the dialog by the other path. */
      event.preventDefault();
      if (escapeBelongsToField(event.target)) return;
      latest.current();
    };
    el.addEventListener("cancel", cancel);
    el.addEventListener("keydown", keydown);
    return () => {
      el.removeEventListener("cancel", cancel);
      el.removeEventListener("keydown", keydown);
    };
  }, [ref]);
}

/**
 * A modal dialog, for the overlays that are mounted only while they are open.
 *
 * Never `<dialog open>`. The creation flows — the voucher batch, the account, both
 * steps of registering an access point — are **every creation flow in the product**,
 * and `open` is the *non-modal* form: it does not trap focus, does not inert the
 * document, does not answer Escape, and gets no `::backdrop`, so the scrim in the
 * stylesheet would style nothing. An operator tabbing through the
 * register-access-point form would walk straight out of it into the table behind,
 * with the form still on screen and still apparently focused.
 *
 * `showModal()` is what makes a dialog a dialog. It is called once, on mount, because
 * these overlays are conditionally rendered — the parent's `null` is the closed state.
 *
 * **And that is also why focus has to be handed back by hand.** A native `<dialog>`
 * returns focus to whatever opened it when `close()` is called; these dialogs are
 * never closed. The parent renders `null`, the element leaves the DOM still open, and
 * the platform's return never runs — so all six overlays in this console dropped
 * focus on `<body>`, measured, which is the top of the document and a keyboard user's
 * place in the page gone. The bug is in the closing, not the opening, which is why
 * every review of the opening passed.
 *
 * **It also owns the keyboard while it is up**, and that is the one thing the
 * platform gives no help with at all. `showModal()` inerts the document behind the
 * dialog, which stops a *click* reaching the page and does nothing whatsoever to a
 * `keydown` on `window` — so the console's registry went on dispatching plain keys
 * through every open dialog. Measured on four routes: with Create batch open and
 * focus on its submit button, `g` `d` navigated to the dashboard and took the
 * half-filled form with it. `registry.tsx` already declined to act while focus was in
 * a text input, so the hole was exactly buttons, links and the `role="group"` dialog
 * bodies — which is to say every dialog in the console at the moment its primary
 * control has focus. The suspend is here rather than in each overlay because there
 * were two mechanisms and only the two overlays that remembered to ask for one were
 * covered; now every `<dialog>` gets it by being one.
 */
export function Modal({
  onClose,
  className,
  labelledBy,
  label,
  dismissOnBackdrop,
  children,
}: {
  onClose: () => void;
  className?: string;
  /**
   * The id of the heading that names this dialog.
   *
   * A `<dialog>` with no accessible name is announced as "dialog" and nothing else,
   * which is survivable for a one-question overlay and is not for a flow with three
   * steps in it.
   */
  labelledBy?: string;
  /**
   * The name itself, for an overlay that has no heading to point at.
   *
   * `labelledBy` is the better of the two everywhere there is a visible title, since
   * the name a screen reader announces is then the same string on screen and cannot
   * drift from it. The command palette has no title — its first row is a search field
   * — and inventing a heading to hide from sighted operators is worse than naming it
   * here.
   */
  label?: string;
  /** Clicking the scrim dismisses. Opt-in: it is wrong for a destructive confirm. */
  dismissOnBackdrop?: boolean;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const openerRef = useRef<Element | null>(null);

  useKeyboardSuspend(true);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    /*
     * The opener, captured before `showModal()` moves focus inside — and captured
     * **once**, which is the part that had to be measured rather than reasoned about.
     * React runs mount → cleanup → mount in development, and on that second mount the
     * dialog is already open with focus in its own first field: an effect-local
     * capture therefore records a node *inside* the dialog, which is disconnected by
     * the time the restore runs, and the restore silently does nothing. It looked
     * exactly like the fix not being deployed.
     */
    openerRef.current ??= document.activeElement;
    if (!el.open) el.showModal();

    return () => {
      const opener = openerRef.current;
      if (!(opener instanceof HTMLElement) || opener === document.body) return;
      /* A control that has itself gone away — a row action whose row was refetched
         out from under it — is not somewhere to put focus. */
      if (!opener.isConnected) return;
      /*
       * Restore only if nothing else has taken focus. A dialog opening as this one
       * closes has already moved focus into itself, and React gives no promise about
       * which of an unmount and a mount in the same commit runs first — this guard
       * is what makes that ordering not matter. `document.body` is what the platform
       * leaves behind when a focused element is removed, so it counts as nothing.
       */
      const active = document.activeElement;
      if (active && active !== document.body && !el.contains(active)) return;
      opener.focus();
    };
  }, []);

  useDismissOnEscape(ref, onClose);

  return (
    <dialog
      ref={ref}
      className={className}
      aria-labelledby={labelledBy}
      aria-label={label}
      /* A click on the `::backdrop` is delivered to the dialog element itself, so
         the target is what tells a scrim click apart from a click on the content —
         there is no separate node to hang a handler on, and there must not be: the
         hand-written scrim this replaced was a `position: fixed` div, which is the
         same trap in a different shape. Pointer only, and paired with Escape. */
      onClick={
        dismissOnBackdrop
          ? (event) => {
              if (event.target === event.currentTarget) onClose();
            }
          : undefined
      }
    >
      {children}
    </dialog>
  );
}

export type DestructiveTier = "confirm" | "type-name";

/**
 * A destructive confirmation, at one of two escalations.
 *
 * `confirm` — a dialog that names the consequence *and the recovery path*, with a
 * confirm button repeating the verb. For anything an operator can recover from.
 *
 * `type-name` — the resource's own name must be typed. Reserved for what has no
 * recovery path. Typing the name proves you know *which* resource you are
 * destroying; typing the word "confirm" only proves deliberation.
 *
 * Reversible actions do not come here at all — they happen immediately, with undo.
 */
export function ConfirmDialog({
  open,
  tier = "confirm",
  title,
  verb,
  confirmName,
  consequence,
  recovery,
  onConfirm,
  onCancel,
  busy,
  error,
}: {
  open: boolean;
  tier?: DestructiveTier;
  title: string;
  verb: string;
  confirmName?: string;
  consequence: ReactNode;
  recovery?: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
  busy?: boolean;
  /**
   * What the confirmed action threw, announced **inside the dialog**.
   *
   * A failure in an open modal has nowhere else to go: the page behind it is inert,
   * so a notice or a toast out there is neither reachable nor announced. Without
   * this, a `500` on *Stop allowing it* leaves the dialog byte-for-byte unchanged,
   * with no error text anywhere on the page and focus on `<body>`, which reads as
   * *nothing happened* on the actions that have no undo.
   *
   * Mounted with its text rather than kept present-and-empty. An `alert` that is
   * already in the document when it gains content announces nothing in most screen
   * readers, and a console can carry one on every route and still be silent.
   */
  error?: unknown;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [typed, setTyped] = useState("");
  /*
   * The field's id was the literal `confirm-name`. One dialog at a time makes that
   * fine and two makes it a defect: `htmlFor` resolves to the first match, so the
   * second dialog's input has no label at all, and a screen reader announces it as
   * an unnamed text field on the one control standing between an operator and an
   * irreversible action. Two were mounted together for the first time on `/design`.
   */
  const fieldId = useId();
  /* Three of the console's five unnamed dialogs were this one primitive, and it is
     the one where "dialog" and nothing else is least survivable: every instance of it
     is a question about destroying something, and the title is which something. */
  const titleId = useId();

  // Plain keys belong to the confirmation while it is up. Measured: with the garden's
  // delete confirmation open and focus on *Stop allowing it*, `g` `d` navigated to
  // the dashboard and unmounted the dialog mid-question.
  useKeyboardSuspend(open);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) {
      el.showModal();
      setTyped("");
    }
    if (!open && el.open) el.close();
  }, [open]);

  useDismissOnEscape(ref, onCancel);

  const ready = tier === "confirm" || typed.trim() === (confirmName ?? "").trim();

  return (
    <dialog ref={ref} aria-labelledby={titleId}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {title}
          </h2>
        </div>
        <div className="dialog-body">
          {error ? <ErrorNotice error={error} /> : null}
          <div>{consequence}</div>
          {recovery ? <div>{recovery}</div> : null}
          {tier === "type-name" ? (
            <div className="field">
              {/* One sentence, not a fragment with the name spliced into the
                  middle: the name is a value, and a sentence assembled around a
                  value is the thing that breaks first when the copy changes. */}
              <label htmlFor={fieldId}>{t("common.typeToConfirm", confirmName ?? "")}</label>
              <input
                id={fieldId}
                type="text"
                value={typed}
                autoComplete="off"
                onChange={(e) => setTyped(e.target.value)}
              />
            </div>
          ) : null}
        </div>
        <div className="dialog-foot">
          {/* The typed-confirmation tier renders no Cancel button: there is no
              symmetric pair of buttons to mis-click. Escape and the backdrop
              dismiss it. */}
          {tier === "confirm" ? (
            <button type="button" className="btn" onClick={onCancel}>
              {t("common.cancel")}
            </button>
          ) : null}
          {/*
            `busy` is `aria-disabled`, not `disabled`, and the difference is where a
            keyboard operator is standing for the next two seconds.

            This is the control that has focus — pressing `Enter` on it is what
            started the request. Setting the DOM `disabled` attribute on the focused
            element makes the user agent blur it to `<body>` with a null
            `relatedTarget`, and nothing puts it back: instrumented against a request
            slowed to 2.5 s, focus was on `<body>` for the whole of it, and still on
            `<body>` after the request *failed* — so the operator is nowhere, with
            nothing announced, on the one screen where something has just gone wrong.

            `aria-disabled` announces the same unavailability and keeps the element
            focusable, so there is no blur to recover from. The guard on the handler
            is what makes it mean something, because an `aria-disabled` button is
            still clickable — the attribute is a claim about the control, and the
            handler has to be the thing that honours it.

            `!ready` stays a real `disabled`: the typed-name tier's button has never
            had focus in that state, because `disabled` is also what keeps it out of
            the tab order until the name matches.
          */}
          <button
            type="button"
            /*
              `btn-primary btn-danger` — and the pairing is the whole point.
              `.btn-danger` alone is a destructive *trigger*: a button, in danger
              ink, with the console's ordinary edge. The solid `--danger` fill
              belongs to the one surface where destroying something genuinely is the
              primary action, which is this dialog — in a page-header slot, where
              every route puts its single primary action, it would make that page's
              loudest control its destructive one.
            */
            className="btn btn-primary btn-danger"
            disabled={!ready}
            aria-disabled={busy || undefined}
            onClick={() => {
              if (!busy) onConfirm();
            }}
          >
            {verb}
          </button>
        </div>
      </div>
    </dialog>
  );
}

/* ── toast ────────────────────────────────────────────────────────────────── */

export interface Toast {
  id: number;
  message: string;
  /**
   * The sentence under the headline, for an outcome the headline cannot carry.
   *
   * Most toasts confirm something and one line is the whole of it. A *failed* one
   * has two things to say — what happened and what it means for the guest standing
   * in front of the operator — and squeezing both into one line is how "Revoked, but
   * the session could not be ended" becomes "Revoked".
   */
  detail?: string;
  undo?: () => void;
}

export function Toasts({ toasts, onDismiss }: { toasts: Toast[]; onDismiss: (id: number) => void }) {
  return (
    <div
      aria-live="polite"
      style={{
        position: "fixed",
        right: "1.25rem",
        bottom: "1.25rem",
        display: "flex",
        flexDirection: "column",
        gap: "0.5rem",
        zIndex: 60,
      }}
    >
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className="card"
          style={{
            boxShadow: "var(--shadow-lg)",
            display: "flex",
            alignItems: "center",
            gap: "0.75rem",
            padding: "0.625rem 0.875rem",
            minWidth: "18rem",
            /* A toast that can carry two lines is a toast that can run the width of
               a desk monitor. Bounded here rather than by keeping the copy short:
               the sentence is the point, the line length is not. */
            maxWidth: "26rem",
          }}
        >
          <span style={{ flex: 1, fontSize: "0.8125rem" }}>
            {toast.message}
            {toast.detail ? <span className="toast-detail">{toast.detail}</span> : null}
          </span>
          {/* Undo is what makes a low-friction destructive action safe. Where it is
              offered, the action does not get a confirmation dialog as well. */}
          {toast.undo ? (
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              onClick={() => {
                toast.undo?.();
                onDismiss(toast.id);
              }}
            >
              {t("common.undo")}
            </button>
          ) : null}
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            onClick={() => onDismiss(toast.id)}
            aria-label={t("common.dismiss")}
          >
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}
