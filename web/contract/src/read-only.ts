/**
 * Sessions are read-only, forever — proved against the contract, not against the UI.
 *
 * `radacct` and `radpostauth` belong to FreeRADIUS. Go never writes them (the one
 * exception is the stale-session reaper), so an "edit session" affordance is
 * a control that can never be built, and every layer above has to be told exactly
 * once. `docs/rules/radius.md` states the ownership; `docs/rules/frontend.md`
 * says the admin UI must not even *design* the affordance.
 *
 * The end-to-end suite can only observe the **absence** of a write: it browses the
 * sessions screen, records every `POST`/`PUT`/`PATCH`/`DELETE` aimed at the API, and
 * asserts the list stayed empty. That is a good test and it stays, but it is the
 * wrong shape for this rule — it notices a mutating affordance only once somebody has
 * built one and put it on screen, and it says nothing at all about a write operation
 * that exists in the contract and has no button yet.
 *
 * This assertion forbids the capability instead. It fails when the *operation* is
 * added to `openapi.yaml`, which is the moment the decision is actually being made,
 * and it fails during `pnpm -r typecheck` — a command every change to `web/`
 * already has to pass — rather than needing a browser and a running stack.
 *
 * It is checked, not documentation: `openapi-typescript` emits `post?: never` for a
 * verb a path does not declare and `post: operations["…"]` for one it does, so the
 * two are distinguishable in the type system and the check below is reading the
 * generated document rather than a comment about it.
 *
 * **If this file stops compiling, the fix is almost never here.** Either a write
 * operation was added under `/sessions` — in which case the question is who is
 * writing `radacct`, and the answer has to be nobody — or the generated types are
 * stale, in which case run `pnpm -F @foyer/contract generate`.
 */
import type { paths } from "./generated/types.js";

/** The methods that change something. `get`, `head` and `options` do not. */
type WriteVerb = "put" | "post" | "delete" | "patch";

/**
 * Every path in the contract whose URL names a session, matched by pattern rather
 * than listed. A `/sessions/{sessionId}/notes` added tomorrow is covered without
 * anybody remembering to extend a list — which is the failure mode of listing them.
 * (`/disconnect` is the one named exemption below, and why it is not a write to a
 * session is written there.)
 */
type SessionPath = Extract<keyof paths, `/sessions${string}`>;

/**
 * The write verbs those paths actually declare.
 *
 * `never` — the empty union — is the only acceptable value. An undeclared verb is
 * typed `undefined` by the generator, so it collapses to `never` here; a declared one
 * contributes its own name, which is what makes the failure message say *which* verb
 * on *which* path.
 */
type DeclaredSessionWrites = {
  [P in SessionPath]: {
    [V in WriteVerb]: paths[P][V] extends undefined ? never : `${P} ${V}`;
  }[WriteVerb];
}[SessionPath];

/**
 * The one write under `/sessions` that is not a write to a session, named exactly.
 *
 * `disconnectSession` asks the **device** to end a live session — an RFC 5176
 * Disconnect-Request, or a controller's unauthorize — and touches no accounting
 * row: the device's own Acct-Stop is what closes it. This file's rule is *nobody
 * writes `radacct`*, and that is still true; "sessions are read-only" means the
 * accounting record, not the live connection.
 *
 * Named rather than matched: a second write under `/sessions` still fails below, and
 * `DISCONNECT_IS_DECLARED` fails if this name ever stops naming a real operation — an
 * exemption for a path that no longer exists is how a guard quietly widens.
 */
type DeviceActionOnASession = "/sessions/{sessionId}/disconnect post";

/**
 * Resolves to `true` when the union is empty, and otherwise to a *sentence*.
 *
 * A template literal rather than an object or a bare `never`, because the compiler
 * prints the expected type verbatim and distributes over the union — so the error a
 * developer actually reads names the rule, the path and the verb, once per offending
 * operation, instead of "type X is not assignable to type 'never'".
 */
type NoWritesDeclared<Found extends string> = [Found] extends [never]
  ? true
  : `radacct and radpostauth belong to FreeRADIUS; Go never writes them, so this can never be implemented. Remove it from openapi.yaml: ${Found}`;

/**
 * The assertion. Assigning `true` fails to compile the moment the union above is
 * non-empty, and the error text carries both the rule and the operation that broke it.
 */
export const SESSIONS_ARE_READ_ONLY: NoWritesDeclared<
  Exclude<DeclaredSessionWrites, DeviceActionOnASession>
> = true;

/** The exemption names an operation that exists. See `DeviceActionOnASession`. */
export const DISCONNECT_IS_DECLARED: DeviceActionOnASession extends DeclaredSessionWrites
  ? true
  : never = true;

/**
 * The audit log is read-only, forever — for a different reason from `radacct`.
 *
 * Sessions above are read-only because FreeRADIUS owns the table. This one is
 * read-only because **a log an administrator can edit is not a log**: the only reader
 * it ever has is somebody reconstructing what happened, usually after something went
 * wrong, and the entry most worth removing is the one that would be removed.
 *
 * Same mechanism as above and for the same reason: the end-to-end suite can only
 * notice a mutating affordance once somebody has built one and put it on screen,
 * whereas this fails when the *operation* is added to `openapi.yaml`, which is the
 * moment the decision is being made.
 *
 * There is no "correct" way to fix a wrong entry. An entry that misdescribes what
 * happened is corrected by a later entry describing the correction, which is what an
 * append-only log is.
 */
type AuditPath = Extract<keyof paths, `/audit${string}`>;

type DeclaredAuditWrites = {
  [P in AuditPath]: {
    [V in WriteVerb]: paths[P][V] extends undefined ? never : `${P} ${V}`;
  }[WriteVerb];
}[AuditPath];

type NoAuditWritesDeclared<Found extends string> = [Found] extends [never]
  ? true
  : `an audit log an administrator can edit is not a log; it is append-only and there is no correct way to amend an entry. Remove it from openapi.yaml: ${Found}`;

export const AUDIT_IS_READ_ONLY: NoAuditWritesDeclared<DeclaredAuditWrites> = true;

/**
 * And it is not vacuous: `/audit` exists and declares a read.
 *
 * The assertion above is `true` for a path set that is *empty*, so it would pass
 * unchanged if the operation were deleted — a guard that is vacuously true checks
 * nothing. This one fails in that case.
 */
export const AUDIT_EXISTS: paths["/audit"]["get"] extends undefined ? never : true = true;
