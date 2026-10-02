import {
  PASSWORD_MIN_LENGTH,
  type FieldFault,
  type ProblemType,
} from "@foyer/contract";
import { ApiError } from "./api.js";
import { driverVendor } from "./drivers.js";
import { budgetBytes } from "./format.js";
import { t } from "./i18n/index.js";

/**
 * Refusal copy, resolved from the type the server sent.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * This is `alarms.ts` one level down, for the other half of the product's copy: an
 * alarm is a standing condition, a problem is an answer to a request the operator
 * just made. The argument is the same one `alarms.ts` makes — the server cannot know
 * the reader's language when it refuses a request, and a sentence that lives in two
 * places drifts — so `Problem.type` became a closed enum and the client owns every
 * sentence about it.
 *
 * **The compile-error property is the whole point.** `CATALOGUE` is a
 * `Record<ProblemType, …>`, so a value added to the contract's enum does not build
 * here until it has copy. A `Record<string, …>` would have accepted the same
 * catalogue with a hole in it and found the hole in front of an operator.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The fallback order, and why the server's prose is not first.**
 *
 * catalogue → `title` → an explicit "this console does not recognise it". Never a
 * blank notice, never a bare `/problems/…`. `detail` is not in that order at all:
 * a conforming server omits it once its clients implement the type, `web/mock` is
 * asserted to send none, and reaching for it first is exactly how the console ended
 * up rendering the server's language. `title` survives one rung above nothing
 * because it is the only thing a console meeting a newer type can render.
 */

type Params = Record<string, unknown>;

export interface ProblemCopy {
  /** A short label. A toast on its own, or a heading where the surface has none. */
  title: string;
  /** What happened and what to do next. Empty only for a type with no sentence. */
  body: string;
  /** True when the catalogue had nothing and the server's own label is showing. */
  fromServer: boolean;
}

/** A `Problem` or a `Failure`. The members this file reads are the ones they share. */
export interface ProblemLike {
  type: ProblemType;
  params?: Params;
  title?: string;
}

const text = (v: unknown): string | null =>
  typeof v === "string" && v ? v : null;
const count = (v: unknown): number =>
  typeof v === "number" && Number.isFinite(v) ? v : 0;
/*
 * The ceiling and the counter beside it, in one unit — `budgetBytes`, not `bytes`.
 *
 * Both said "kB" and disagreed: `/portal`'s gate divided by 1024 and this divided by
 * 1000, so **one limit read 100 kB on the field and 102 kB in the refusal**, one
 * press apart. `too-large` is only ever raised about the portal's
 * own budgets, which are the numbers that field is showing.
 */
const size = (v: unknown): string => budgetBytes(count(v));

/**
 * The reachability clause several refusals are built on.
 *
 * Three values and three different next steps — a network problem, a controller
 * problem, and a device that has never answered — which is why the mock sends
 * `nasReachability` beside the name rather than a sentence containing both.
 */
function reachClause(params: Params): string {
  return reachabilityClause(
    text(params.nasName) ?? text(params.name) ?? undefined,
    text(params.nasReachability) ?? text(params.reachability) ?? undefined,
  );
}

/**
 * The same sentence, for a caller that already holds the device and its state —
 * the device screen's standing note, which builds the *same* refusal the `409`
 * will return and must not say something different from it.
 *
 * **Exported because a second copy of this sentence is how a screen starts
 * disagreeing with the server about why it refused.** A standing note that
 * reaches for a hand-written "has never answered a probe" instead is true of
 * `unknown` and **false of `credentials-expired`**, a device that answered
 * perfectly well and was rejected by the controller. Two sentences for one
 * refusal, one of them wrong.
 */
export function reachabilityClause(
  name: string | undefined,
  reach: string | undefined,
): string {
  const who = name ?? t("problem.reach.device");
  if (reach === "unreachable") return t("problem.reach.unreachable", who);
  if (reach === "credentials-expired")
    return t("problem.reach.credentials-expired", who);
  return t("problem.reach.unknown", who);
}

const CATALOGUE: Record<
  ProblemType,
  (p: Params) => Omit<ProblemCopy, "fromServer">
> = {
  "/problems/validation": () => ({
    title: t("problem.validation.title"),
    body: t("problem.validation.body"),
  }),
  "/problems/not-found": () => ({
    title: t("problem.not-found.title"),
    body: t("problem.not-found.body"),
  }),
  "/problems/duplicate": (p) => ({
    title: t("problem.duplicate.title"),
    body: t("problem.duplicate.body", text(p.value)),
  }),
  /*
   * One venue per install. Separate from `duplicate` because what the operator
   * does next is different: nothing collided, so *choose another name* cannot
   * help. The sentence has to say that a second venue is a second installation,
   * or an operator will sit on this form retyping.
   */
  "/problems/one-site-per-install": () => ({
    title: t("problem.one-site-per-install.title"),
    body: t("problem.one-site-per-install.body"),
  }),
  "/problems/too-large": (p) => ({
    title: t("problem.too-large.title"),
    body: t("problem.too-large.body", size(p.bytes), size(p.limitBytes)),
  }),
  "/problems/garden-entry-required": (p) => ({
    title: t("problem.garden-entry-required.title"),
    body: t("problem.garden-entry-required.body", text(p.value)),
  }),
  "/problems/controller-in-use": (p) => ({
    title: t("problem.controller-in-use.title"),
    body: t("problem.controller-in-use.body", count(p.count)),
  }),
  "/problems/controller-unreachable": (p) => ({
    title: t("problem.controller-unreachable.title"),
    body: t("problem.controller-unreachable.body", reachClause(p)),
  }),
  /* The vendor, never `p.driver` — that is the enum, and it reached the screen as
     "A **unifi** access point…". See `drivers.ts`. */
  "/problems/needs-controller": (p) => ({
    title: t("problem.needs-controller.title"),
    body: t("problem.needs-controller.body", driverVendor(p.driver)),
  }),
  "/problems/rotation-refused": (p) => ({
    title: t("problem.rotation-refused.title"),
    body: t("problem.rotation-refused.body", reachClause(p)),
  }),
  "/problems/device-not-verified": () => ({
    title: t("problem.device-not-verified.title"),
    body: t("problem.device-not-verified.body"),
  }),
  "/problems/method-not-offered": () => ({
    title: t("problem.method-not-offered.title"),
    body: t("problem.method-not-offered.body"),
  }),
  "/problems/no-grant": () => ({
    title: t("problem.no-grant.title"),
    body: t("problem.no-grant.body"),
  }),
  "/problems/internal": () => ({
    title: t("problem.internal.title"),
    body: t("problem.internal.body"),
  }),
  "/problems/session-end-failed": (p) => ({
    title: t("problem.session-end-failed.title"),
    body: t("problem.session-end-failed.body", reachClause(p)),
  }),
  "/problems/session-not-live": () => ({
    title: t("problem.session-not-live.title"),
    body: t("problem.session-not-live.body"),
  }),
  "/problems/disconnect-unsupported": (p) => ({
    title: t("problem.disconnect-unsupported.title"),
    body: t("problem.disconnect-unsupported.body", driverVendor(p.driver)),
  }),
  "/problems/not-signed-in": () => ({
    title: t("problem.not-signed-in.title"),
    body: t("problem.not-signed-in.body"),
  }),
  "/problems/bad-credentials": () => ({
    title: t("problem.bad-credentials.title"),
    body: t("problem.bad-credentials.body"),
  }),
  "/problems/rate-limited": (p) => ({
    title: t("problem.rate-limited.title"),
    body: t("problem.rate-limited.body", count(p.retryAfterSeconds)),
  }),
  "/problems/forbidden": () => ({
    title: t("problem.forbidden.title"),
    body: t("problem.forbidden.body"),
  }),
  /*
   * The operator is signed in, is who they say they are, and holds a temporary
   * password somebody else set for them. Separate from `forbidden` because the console's answer
   * is different: `forbidden` means *this account may not do this* and renders a
   * refusal, while this one means *no account may do anything yet* and routes to the
   * one screen that clears it — see `auth.tsx`. An operator should rarely read this
   * copy at all; it is what a request in flight when the routing happens comes back
   * with.
   */
  "/problems/password-change-required": () => ({
    title: t("problem.password-change-required.title"),
    body: t("problem.password-change-required.body"),
  }),
  "/problems/setup-complete": () => ({
    title: t("problem.setup-complete.title"),
    body: t("problem.setup-complete.body"),
  }),
  "/problems/last-administrator": () => ({
    title: t("problem.last-administrator.title"),
    body: t("problem.last-administrator.body"),
  }),
  "/problems/unreachable": () => ({
    title: t("problem.unreachable.title"),
    body: t("problem.unreachable.body"),
  }),
};

/** Copy for a refusal the server described, or for one this console synthesised. */
export function problemCopy(problem: ProblemLike): ProblemCopy {
  const render = CATALOGUE[problem.type];
  if (render) return { ...render(problem.params ?? {}), fromServer: false };
  /* A type newer than this console. `title` is a short label rather than a sentence,
     so it goes in the title and the body says what the console can honestly say. */
  if (problem.title) {
    return {
      title: problem.title,
      body: t("problem.unknown.body"),
      fromServer: true,
    };
  }
  return {
    title: t("problem.unknown.title"),
    body: t("problem.unknown.body"),
    fromServer: true,
  };
}

/**
 * Copy for anything a mutation or a query threw.
 *
 * The classification happens in `api.ts` — a rejected `fetch` is already an
 * `ApiError` carrying `/problems/unreachable` by the time it arrives here — so this
 * has one job left: anything that is *not* an `ApiError` is a bug in this console
 * rather than an answer from the server, and it says so in those words instead of
 * putting a `TypeError` on screen.
 */
export function errorCopy(error: unknown): ProblemCopy {
  if (error instanceof ApiError && error.problem)
    return problemCopy(error.problem);
  return {
    title: t("problem.unknown.title"),
    body: t("problem.unknown.body"),
    fromServer: true,
  };
}

/**
 * *Gone*, as opposed to *the server failed*.
 *
 * The distinction is not cosmetic and it is why this is a type rather than a status:
 * a record that has aged out is an ordinary thing to walk into from a bookmark and
 * gets a calm, neutral treatment, while every other failure is urgent. A surface
 * that keys on `status === 404` has to answer `false` for all of them, which is how
 * a `500` came to render *"No session selected."* with a row selected.
 */
export function isNotFound(error: unknown): boolean {
  return (
    error instanceof ApiError && error.problem?.type === "/problems/not-found"
  );
}

/* ── refused fields ───────────────────────────────────────────────────────── */

/**
 * Why one field was refused, keyed on the fault.
 *
 * `Record<FieldFault, …>`: the contract's fault list is closed, so a fault added
 * there fails to build here, the same property `CATALOGUE` has for the type.
 */
const FAULT: Record<FieldFault, (p: Params) => string> = {
  required: () => t("fault.required"),
  duplicate: () => t("fault.duplicate"),
  "too-large": (p) => t("fault.too-large", size(p.bytes), size(p.limitBytes)),
  "below-minimum": (p) => t("fault.below-minimum", count(p.minimumCount)),
  "above-maximum": (p) => t("fault.above-maximum", count(p.maximumCount)),
  "not-a-mac-address": () => t("fault.not-a-mac-address"),
  "not-a-phone-number": () => t("fault.not-a-phone-number"),
  "not-an-email-address": () => t("fault.not-an-email-address"),
  "not-a-data-uri": () => t("fault.not-a-data-uri"),
  "not-a-hostname": () => t("fault.not-a-hostname"),
  "not-a-cidr": () => t("fault.not-a-cidr"),
  "not-an-ip-address": () => t("fault.not-an-ip-address"),
  "not-an-origin": () => t("fault.not-an-origin"),
  "unsupported-image-format": () => t("fault.unsupported-image-format"),
  "needs-controller": () => t("fault.needs-controller"),
  "required-with-click-through": () => t("fault.required-with-click-through"),
  "needs-phone-provider": () => t("fault.needs-phone-provider"),
  "missing-code-placeholder": () => t("fault.missing-code-placeholder"),
  "not-a-link": () => t("fault.not-a-link"),
  "link-before-sign-in": () => t("fault.link-before-sign-in"),
  "ends-before-start": () => t("fault.ends-before-start"),
  incorrect: () => t("fault.incorrect"),
  unchanged: () => t("fault.unchanged"),
  "range-inverted": () => t("fault.range-inverted"),
  "range-too-long": (p) => t("fault.range-too-long", count(p.maxDays)),
  "needs-contact-field": () => t("fault.needs-contact-field"),
  "not-a-web-address": () => t("fault.not-a-web-address"),
  /* The reason is `params.reason`; the Network screen renders the sentence for each. */
  "certificate-refused": () => t("fault.certificate-refused"),
  "not-a-time-zone": () => t("fault.not-a-time-zone"),
  "not-a-wifi-passphrase": () => t("fault.not-a-wifi-passphrase"),
};

/**
 * The triples where the fault's own sentence answers a different question.
 *
 * `(type, field)` is not unique — that is what `FieldFault` was added for — but
 * neither is `fault` alone: *Required.* is right for a text box and wrong for a set
 * of chips, where the operator has to be told that choosing **one** is enough.
 *
 * This map cannot be total, because `field` is an open string and always will be:
 * only a server knows what it calls its inputs. What is closed is the other two
 * thirds of the key, and `tripleKey` takes them as `ProblemType` and `FieldFault`
 * rather than as strings — so a typo in either, here or at the lookup, is a compile
 * error, and a fault that stops existing takes its overrides with it.
 */
const tripleKey = (type: ProblemType, field: string, fault: FieldFault) =>
  `${type} ${field} ${fault}`;

const SPECIFIC = new Map<string, () => string>([
  [
    tripleKey("/problems/validation", "methods", "required"),
    () => t("fault.chooseOne"),
  ],
  [
    tripleKey("/problems/validation", "languages", "required"),
    () => t("fault.chooseOne"),
  ],
  [
    tripleKey("/problems/validation", "controllerId", "required"),
    () => t("fault.omadaControllerId"),
  ],
  /*
   * The one field in the product refused because the server **declines to guess**.
   *
   * `coa` is the one `NasDriverKind` naming a family, and Cisco and Aruba identify a
   * session by different attributes — so the wrong dialect matches nothing and a
   * careless device ACKs the request anyway. A bare *Required.* beside a select whose
   * current value is *Not chosen* reads as a form that cannot see its own field; the
   * sentence has to name the refusal's reason, which is the whole point of this map.
   *
   * Safe to key on the field name: `coaVendor` is `createNas`'s alone.
   */
  [
    tripleKey("/problems/validation", "coaVendor", "required"),
    () => t("fault.coaVendor"),
  ],
  /*
   * A password refused for length, and the number comes from the **contract**, not
   * from the refusal.
   *
   * Driven rather than assumed: submitting a five-character new password rendered
   * *"At least 0."* `fault.below-minimum` reads `params.minimumCount`, the mock's
   * `fieldRefusals()` has nowhere to put one, and `count()` answers 0 for a value
   * that is not there. A catalogue entry that compiles, an alert that mounts with
   * text, and a sentence that is wrong — a check that looks satisfied
   * and is not.
   *
   * `PASSWORD_MIN_LENGTH` is exported by `@foyer/contract` precisely so the console
   * can state the rule *before* the operator types. Reading it here as well means the
   * refusal repeats the hint word for word, and the two cannot disagree — which no
   * server-supplied figure can guarantee.
   *
   * `password` is `createOperator`'s field and `newPassword` is `changeOwnPassword`'s.
   * A guest `Account` has no length rule in the contract and never produces this
   * fault, which is why the triple is safe to key on the field name.
   */
  [
    tripleKey("/problems/validation", "newPassword", "below-minimum"),
    () => t("fault.passwordLength", PASSWORD_MIN_LENGTH),
  ],
  [
    tripleKey("/problems/validation", "password", "below-minimum"),
    () => t("fault.passwordLength", PASSWORD_MIN_LENGTH),
  ],
]);

/**
 * One sentence per refused field, keyed by field name.
 *
 * Order: the triple, then the fault, then the server's prose for a triple this
 * console has no copy for, then a last line that is at least a sentence. `errors`
 * is the fallback and not the source — its *keys* are the load-bearing half, since
 * they are what says which inputs to mark.
 */
export function fieldMessages(error: unknown): Record<string, string> {
  if (!(error instanceof ApiError)) return {};
  const problem = error.problem;
  if (!problem) return {};
  const faults = error.fieldFaults;
  const prose = error.fieldErrors;
  const params = (problem.params ?? {}) as Params;
  const out: Record<string, string> = {};
  for (const field of new Set([
    ...Object.keys(faults),
    ...Object.keys(prose),
  ])) {
    const fault = faults[field];
    const specific = fault
      ? SPECIFIC.get(tripleKey(problem.type, field, fault))?.()
      : undefined;
    /* The figure for this field, from `params.fieldParams[field]` where the contract
       puts it, over the top level where older refusals left it. */
    const own = {
      ...params,
      ...((params.fieldParams as Record<string, Params> | undefined)?.[field] ??
        {}),
    };
    out[field] =
      specific ??
      (fault ? FAULT[fault](own) : undefined) ??
      prose[field] ??
      t("fault.unknown");
  }
  return out;
}
