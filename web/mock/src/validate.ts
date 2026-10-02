/**
 * Runtime enforcement of the contract.
 *
 * `docs/rules/frontend.md` says the OpenAPI document is the deliverable and that
 * mock handlers are driven from it. Generated types give us that at compile time —
 * a handler for an operation that does not exist will not compile. This gives us
 * the other half: a response whose *shape* has drifted from the schema fails here,
 * loudly, in development, rather than being discovered when the Go backend is
 * written against a contract the prototype never actually honoured.
 *
 * OpenAPI 3.1 schemas are JSON Schema 2020-12, so Ajv validates them directly with
 * no conversion step.
 *
 * ── Two things Ajv alone does not give us ───────────────────────────────────
 *
 * **1. Ajv is blind to a property the contract does not declare.** JSON Schema is
 * open-world by default, and no schema in `openapi.yaml` sets
 * `additionalProperties: false`, so a response carrying a field the contract
 * removed — or a typo'd name beside the real one — validates clean. A field such as
 * `terms.url`, removed from the contract, can go on being sent by the mock with
 * every check green.
 *
 * `additionalProperties: false` in the document was considered and rejected, for
 * three reasons:
 *
 *   - **It would be a claim about the API, not about the mock.** In the document it
 *     says "a conforming server never sends anything else", which forbids the Go
 *     backend from ever adding a field without a coordinated client change. The
 *     project has already decided it wants the opposite: the contract keeps a
 *     server-supplied fallback string in the schema precisely so *"a newer backend
 *     can still say something useful to an older console"*. The rule we actually
 *     want is narrower — **the mock must not serve a field the contract does not
 *     declare** — and that is a rule about this package.
 *   - **`allOf` and `additionalProperties: false` do not compose.** The paginated
 *     responses are `allOf: [Page, {items}]`; `additionalProperties: false` on
 *     either branch rejects the other branch's properties, because each branch is
 *     evaluated against the whole object independently. Making the keyword work
 *     would mean flattening the envelopes — changing the shape of the contract to
 *     suit a validation trick, which is the wrong layer paying for the rule.
 *   - **It would leak into the generated types**, which two applications this
 *     package does not own compile against.
 *
 * So the strictness lives here, in the mock's own validator, and it is applied to
 * **responses only**. Requests are deliberately not held to it: an unexpected field
 * in a request is a *client* mistake and its answer is `422`, not a `500` claiming
 * the mock violated its own contract. Being lenient about what you accept and
 * strict about what you send is also the behaviour the Go backend should have.
 *
 * **2. Ajv only checks the schema it is handed.** A handler that validated the
 * wrong component, or validated only a fragment of what it sent, would pass. So the
 * primary entry point is keyed on **operation and status** and looks the schema up
 * in the document — including the inline envelopes (`{items, nextCursor}`,
 * `{nas, sharedSecret}`) that no component names and nothing else would check.
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { parse } from "yaml";

type Schema = Record<string, unknown>;

const specPath = fileURLToPath(new URL("../../contract/openapi.yaml", import.meta.url));
const spec = parse(readFileSync(specPath, "utf8")) as {
  paths: Record<string, Record<string, unknown>>;
  components: { schemas: Record<string, unknown>; responses?: Record<string, unknown> };
};

const HTTP_METHODS = ["get", "put", "post", "delete", "patch"] as const;
/** Both are JSON; RFC 9457 problems just carry a more specific type. */
const JSON_MEDIA = ["application/json", "application/problem+json"] as const;

/** Resolves one `#/...` pointer against the document. Not recursive by design. */
function follow(node: unknown): Schema | undefined {
  if (!node || typeof node !== "object") return undefined;
  const ref = (node as Schema).$ref;
  if (typeof ref !== "string") return node as Schema;
  if (!ref.startsWith("#/")) throw new Error(`unsupported $ref in the contract: ${ref}`);
  let cursor: unknown = spec;
  for (const raw of ref.slice(2).split("/")) {
    const key = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    cursor = (cursor as Record<string, unknown> | undefined)?.[key];
  }
  if (!cursor) throw new Error(`dangling $ref in the contract: ${ref}`);
  return follow(cursor);
}

/**
 * `operationId:status` → the response schema the document declares.
 *
 * Collected from the document rather than written down here, so an envelope that
 * changes shape in `openapi.yaml` changes what the mock is held to, with no second
 * copy to forget. `204` and the portal's `303` have no body and appear nowhere.
 */
const operations: Record<string, unknown> = {};
for (const item of Object.values(spec.paths)) {
  for (const method of HTTP_METHODS) {
    const op = item[method] as { operationId?: string; responses?: Record<string, unknown> };
    if (!op?.operationId) continue;
    for (const [status, response] of Object.entries(op.responses ?? {})) {
      const content = follow(response)?.content as Record<string, Schema> | undefined;
      const media = JSON_MEDIA.find((m) => content?.[m]?.schema);
      if (media) operations[`${op.operationId}:${status}`] = content![media]!.schema;
    }
  }
}

const ajv = new Ajv2020({
  strict: false,
  allErrors: true,
  // The document uses internal $refs throughout; register the whole components map
  // so `#/components/schemas/X` resolves without rewriting anything. The response
  // schemas lifted out of `paths` sit alongside it under the same root, so the refs
  // *inside* them resolve against the same base URI they were written for.
  schemas: { foyer: { components: spec.components, operations } },
});
addFormats(ajv);

const cache = new Map<string, ReturnType<typeof ajv.compile>>();

function validator(pointer: string) {
  let v = cache.get(pointer);
  if (!v) {
    v = ajv.compile({ $ref: `foyer#${pointer}` });
    cache.set(pointer, v);
  }
  return v;
}

// ── undeclared properties ────────────────────────────────────────────────────

/**
 * What a schema declares at one level, with composition already folded in.
 *
 * `allOf` is why this is a merge rather than a lookup. Each branch of an `allOf` is
 * evaluated against the whole object, so a property declared by *any* branch is
 * declared — which is exactly the semantics `additionalProperties: false` cannot
 * express and the reason it is not in the document. `anyOf`/`oneOf` are folded the
 * same way: permissive, because a property accepted on one arm is not undeclared.
 */
interface Declared {
  /** Property name → the subschemas it must satisfy (more than one under `allOf`). */
  properties: Map<string, Schema[]>;
  /** Schemas for keys not named above; empty when the object declares no map. */
  additional: Schema[];
  /** `additionalProperties` present and not `false` — the object is a declared map. */
  open: boolean;
}

function declaredIn(schema: Schema | undefined, into?: Declared): Declared {
  const out: Declared = into ?? { properties: new Map(), additional: [], open: false };
  const resolved = follow(schema);
  if (!resolved) return out;

  const properties = resolved.properties as Record<string, Schema> | undefined;
  for (const [name, sub] of Object.entries(properties ?? {})) {
    const existing = out.properties.get(name);
    if (existing) existing.push(sub);
    else out.properties.set(name, [sub]);
  }

  const additional = resolved.additionalProperties;
  if (additional !== undefined && additional !== false) {
    out.open = true;
    if (typeof additional === "object" && additional !== null) out.additional.push(additional as Schema);
  }

  for (const key of ["allOf", "anyOf", "oneOf"] as const) {
    for (const branch of (resolved[key] as Schema[] | undefined) ?? []) declaredIn(branch, out);
  }
  return out;
}

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

/**
 * Walks a value against a schema and collects every property the contract does not
 * declare, as JSON pointers into the value.
 *
 * Silent about a schema that declares nothing at all (no `properties`, no
 * `additionalProperties`): that is a deliberately opaque object, and reporting its
 * contents as undeclared would be noise rather than drift.
 */
function undeclared(schema: Schema | undefined, value: unknown, path: string, out: string[]): void {
  const resolved = follow(schema);
  if (!resolved) return;

  if (Array.isArray(value)) {
    const items = resolved.items as Schema | undefined;
    if (items) value.forEach((element, i) => undeclared(items, element, `${path}/${i}`, out));
    return;
  }
  if (!isPlainObject(value)) return;

  const declared = declaredIn(resolved);
  if (declared.properties.size === 0 && !declared.open) return;

  for (const [key, member] of Object.entries(value)) {
    const subs = declared.properties.get(key);
    if (subs) {
      for (const sub of subs) undeclared(sub, member, `${path}/${key}`, out);
    } else if (declared.open) {
      for (const sub of declared.additional) undeclared(sub, member, `${path}/${key}`, out);
    } else {
      out.push(`${path}/${key}`);
    }
  }
}

// ── the door every response goes through ─────────────────────────────────────

export class ContractViolation extends Error {
  constructor(
    readonly subject: string,
    readonly detail: string,
  ) {
    super(`response does not match ${subject}: ${detail}`);
    this.name = "ContractViolation";
  }
}

/**
 * Deliberately throws rather than warning. A warning in a scrolling dev log is a
 * contract violation nobody sees.
 */
function check<T>(subject: string, pointer: string, schema: Schema, value: T): T {
  const problems: string[] = [];

  const v = validator(pointer);
  if (!v(value)) {
    for (const e of v.errors ?? []) problems.push(`${e.instancePath || "/"} ${e.message}`);
  }

  const extras: string[] = [];
  undeclared(schema, value, "", extras);
  for (const at of extras) {
    problems.push(`${at} is not declared by the contract — remove it, or declare it first`);
  }

  if (problems.length) throw new ContractViolation(subject, problems.join("; "));
  return value;
}

/**
 * Validates a response against the schema the document declares for that operation
 * and status, and returns it unchanged.
 *
 * Keyed on the operation rather than on a component name so that a handler cannot
 * quietly check the wrong thing, and so the inline envelopes — `{items, nextCursor}`
 * from the list operations, `{nas, sharedSecret}` from `createNas` — are covered by
 * the same door as everything else.
 */
export function conformResponse<T>(operationId: string, status: number, value: T): T {
  const key = `${operationId}:${status}`;
  const schema = operations[key] as Schema | undefined;
  if (!schema) {
    throw new ContractViolation(
      key,
      "the contract declares no JSON response for this operation and status. " +
        "Write the operation in openapi.yaml before the handler that answers it.",
    );
  }
  return check(key, `/operations/${key}`, schema, value);
}

/**
 * Validates a value against a named component schema.
 *
 * Used for the RFC 9457 problem bodies, which every operation that can fail
 * declares identically — `notFound()` is shared, so it has no single operation to
 * key on. `problemIsTheOnlyErrorSchema` in `validate.test.ts` is what keeps that
 * shortcut honest.
 */
export function conform<T>(schemaName: string, value: T): T {
  const schema = spec.components.schemas[schemaName] as Schema | undefined;
  if (!schema) throw new ContractViolation(schemaName, "no such schema in the contract");
  return check(schemaName, `/components/schemas/${schemaName}`, schema, value);
}

/** Names every schema in the document. */
export function schemaNames(): string[] {
  return Object.keys(spec.components.schemas);
}

/** `operationId:status` for every response the document declares a JSON body for. */
export function responseKeys(): string[] {
  return Object.keys(operations);
}

/** The document itself, for the tests that hold this file to it. */
export function contractDocument() {
  return spec;
}

/**
 * A **request** member held to its schema, answered the way the Go server's
 * request validator answers it (`internal/api/requestvalidation.go`): the first
 * violation, keyed by its dotted path, as a field fault.
 *
 * Not the response door above. A request that breaks the contract is the client's
 * mistake and its answer is a `422`, never a `ContractViolation`. Used only where a
 * member is deep enough that hand-written checks would be a second schema — the
 * page design's block list.
 */
export function requestFault(
  schemaName: string,
  value: unknown,
  prefix: string,
): {
  field: string;
  fault: "required" | "incorrect" | "too-large" | "below-minimum";
  message: string;
  /** `too-large`'s two figures — its sentence has a number in it (`x-fault-params`). */
  figures?: { bytes: number; limitBytes: number };
} | null {
  const v = validator(`/components/schemas/${schemaName}`);
  if (v(value)) return null;
  const e = v.errors?.[0];
  if (!e) return null;
  const path = [prefix, ...e.instancePath.split("/").filter(Boolean)];
  switch (e.keyword) {
    case "required": {
      const missing = (e.params as { missingProperty?: string }).missingProperty;
      return { field: [...path, missing].filter(Boolean).join("."), fault: "required", message: "Required." };
    }
    case "maxLength":
    case "maxItems":
    case "maximum": {
      const limit = (e.params as { limit: number }).limit;
      // Ajv carries the offending value only in verbose mode, which this shared
      // instance is not in; the path finds it just as well.
      const data = e.instancePath
        .split("/")
        .filter(Boolean)
        .reduce<unknown>((at, key) => (at as Record<string, unknown> | undefined)?.[key], value);
      const actual = typeof data === "string" || Array.isArray(data) ? data.length : Number(data);
      return {
        field: path.join("."),
        fault: "too-large",
        message: "Above the permitted maximum.",
        figures: { bytes: actual, limitBytes: limit },
      };
    }
    case "minLength":
    case "minItems":
    case "minimum":
      return { field: path.join("."), fault: "below-minimum", message: "Below the permitted minimum." };
    default:
      return { field: path.join("."), fault: "incorrect", message: "Not valid for this field." };
  }
}
