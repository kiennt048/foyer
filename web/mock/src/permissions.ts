/**
 * The mock's role gate, **computed from `openapi.yaml` rather than written here**.
 *
 * Every operation in the contract declares `x-permission`. This file turns those
 * annotations into a matcher, so the mock refuses a staff session exactly where the
 * document says it should — and so an annotation that is wrong is a thing a drive can
 * find, rather than a comment nobody executes.
 *
 * Writing the table here instead would have been the shorter route and the wrong one:
 * it would be a second copy of the permission model, in the one process whose whole
 * job is to hold the first copy to account. `web/mock` already computes its expected
 * sets from `ProblemType`, `FieldFault` and `AlarmKind` for the same reason.
 *
 * `public` and `authenticated` are the two values that are not permissions:
 * `public` is the guest portal and the two ways in; `authenticated` is an operation
 * every signed-in operator may reach whatever their role — the site switcher, the
 * dashboard, signing out, changing your own password.
 */
import type { OperatorPermission } from "@foyer/contract";
import { contractDocument } from "./validate.js";

export type Requirement = "public" | "authenticated" | OperatorPermission;

interface Route {
  method: string;
  match: RegExp;
  operationId: string;
  requires: Requirement;
  /** Body options that carry a permission of their own; see `optionRefusal`. */
  options: OptionRequirement[];
}

/**
 * A request-body property annotated with `x-permission`: setting it is a second act
 * the operation's own permission does not cover. `disconnectSession`'s
 * `revokeVoucher` is the first — it is the act `revokeVoucher` performs, gated on
 * `vouchers:manage`, and an option must not be the way round another operation's
 * permission. Read from the document, like every other
 * requirement here, so a new annotated option is enforced by the mock the day it is
 * written.
 */
export interface OptionRequirement {
  property: string;
  requires: OperatorPermission;
}

type SchemaLike = { $ref?: string; properties?: Record<string, { "x-permission"?: OperatorPermission }> };

function resolveSchema(s: SchemaLike | undefined): SchemaLike | undefined {
  const ref = s?.$ref;
  if (!ref) return s;
  const name = ref.replace("#/components/schemas/", "");
  const doc = contractDocument() as unknown as { components?: { schemas?: Record<string, SchemaLike> } };
  return doc.components?.schemas?.[name];
}

function optionsOf(op: { requestBody?: { content?: Record<string, { schema?: SchemaLike }> } }): OptionRequirement[] {
  const schema = resolveSchema(op.requestBody?.content?.["application/json"]?.schema);
  return Object.entries(schema?.properties ?? {}).flatMap(([property, p]) =>
    p["x-permission"] ? [{ property, requires: p["x-permission"] }] : [],
  );
}

const HTTP_METHODS = ["get", "put", "post", "delete", "patch"] as const;

/**
 * `/nas/{nasId}` → `^/nas/[^/]+$`.
 *
 * Anchored at both ends: an unanchored pattern would let `/nas/{nasId}/secret` be
 * matched by `/nas/{nasId}`, and the two do not carry the same requirement — reading
 * a device is `nas:read` and rotating its shared secret is `nas:manage`.
 */
function templateToRegExp(template: string): RegExp {
  const source = template
    .split("/")
    .map((segment) =>
      segment.startsWith("{") && segment.endsWith("}")
        ? "[^/]+"
        : segment.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
    )
    .join("/");
  return new RegExp(`^${source}$`);
}

const routes: Route[] = [];
for (const [template, item] of Object.entries(contractDocument().paths)) {
  for (const method of HTTP_METHODS) {
    const op = (item as Record<string, unknown>)[method] as
      | { operationId?: string; "x-permission"?: Requirement }
      | undefined;
    if (!op?.operationId) continue;
    const requires = op["x-permission"];
    if (!requires) {
      /* Loud, at import time. An operation with no annotation is an operation with no
         gate, and a gate that silently defaults to open is how a staff member ends up
         able to rotate a shared secret. There is a test on the same property, in the
         contract document, so this throw is the second gate rather than the only one. */
      throw new Error(
        `${method.toUpperCase()} ${template} (${op.operationId}) declares no x-permission in openapi.yaml`,
      );
    }
    routes.push({
      method: method.toUpperCase(),
      match: templateToRegExp(template),
      operationId: op.operationId,
      requires,
      options: optionsOf(op as Parameters<typeof optionsOf>[0]),
    });
  }
}

/**
 * The permission a request body asks for beyond the operation's own and the caller
 * does not hold, or `undefined`. An option counts as set unless it is absent, null or
 * `false` — the Go handler's reading of a boolean flag, and the safe one for any
 * other type: a value the caller sent is a value the caller asked for.
 */
export function optionRefusal(
  method: string,
  path: string,
  body: unknown,
  held: readonly OperatorPermission[],
): OperatorPermission | undefined {
  const route = routes.find((r) => r.method === method.toUpperCase() && r.match.test(path));
  if (!route || !body || typeof body !== "object") return undefined;
  for (const o of route.options) {
    const v = (body as Record<string, unknown>)[o.property];
    if (v !== undefined && v !== null && v !== false && !held.includes(o.requires)) return o.requires;
  }
  return undefined;
}

/** Whether an operation has any permission-bearing body option (so the gate reads the body only then). */
export function hasOptionRequirements(method: string, path: string): boolean {
  return (routes.find((r) => r.method === method.toUpperCase() && r.match.test(path))?.options.length ?? 0) > 0;
}

/** `operationId` → its permission-bearing body options, for the tests. */
export function optionRequirementsByOperation(): Record<string, OptionRequirement[]> {
  return Object.fromEntries(routes.filter((r) => r.options.length).map((r) => [r.operationId, r.options]));
}

/**
 * What this request requires, or `undefined` for a path the contract does not
 * declare at all.
 *
 * `undefined` is deliberately not `public`: an unknown path is a 404 the router will
 * produce on its own, and treating it as public here would be answering a question
 * about a thing that does not exist.
 */
export function requirementFor(method: string, path: string): Requirement | undefined {
  return routes.find((r) => r.method === method.toUpperCase() && r.match.test(path))?.requires;
}

/** Every requirement the contract declares. The expected set for the sweeps. */
export function declaredRequirements(): Requirement[] {
  return [...new Set(routes.map((r) => r.requires))].sort();
}

/** `operationId` → requirement, for the tests that hold the annotations to the model. */
export function requirementsByOperation(): Record<string, Requirement> {
  return Object.fromEntries(routes.map((r) => [r.operationId, r.requires]));
}
