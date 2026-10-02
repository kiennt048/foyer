import type { Page } from "@playwright/test";

/**
 * A recording of everything the page did on the wire, plus everything it *tried* to
 * do from inside JavaScript.
 *
 * Two independent observers, on purpose. Playwright's `request`/`response` events
 * see the transport — they say whether a request was a document navigation and what
 * status answered it. They do **not** distinguish "the browser navigated" from "a
 * script fetched", cleanly enough to assert on: a `fetch()` and a form POST both
 * appear as a POST to the same URL. So the page is also instrumented from the
 * inside, wrapping `fetch`, `XMLHttpRequest` and `sendBeacon` before any page
 * script runs, and reporting every call back out.
 *
 * The combination is what makes `navigation-not-xhr.spec.ts` mean anything.
 */

export type ScriptedKind = "fetch" | "xhr" | "beacon";

export interface ScriptedCall {
  seq: number;
  kind: ScriptedKind;
  url: string;
  method: string;
}

export interface WireRequest {
  seq: number;
  kind: "request";
  url: string;
  method: string;
  resourceType: string;
  isNavigation: boolean;
  postData: string | null;
}

export interface WireResponse {
  seq: number;
  kind: "response";
  url: string;
  status: number;
  method: string;
  resourceType: string;
  location: string | null;
}

export interface FrameEvent {
  seq: number;
  kind: "framenavigated" | "load" | "domcontentloaded";
  url: string;
}

export type TraceEvent = WireRequest | WireResponse | FrameEvent | (ScriptedCall & { kind: ScriptedKind });

export interface NetworkTrace {
  /** Every observation in the order it happened. Order is the whole point here. */
  events: TraceEvent[];
  requests: WireRequest[];
  responses: WireResponse[];
  /** `fetch` / XHR / `sendBeacon` calls made from page JavaScript, across navigations. */
  scripted: ScriptedCall[];
  frames: FrameEvent[];
}

declare global {
  interface Window {
    __foyerScripted?: (kind: string, url: string, method: string) => void;
  }
}

/**
 * Starts recording. Call before the first navigation — the init script has to be in
 * place before any page script gets hold of the real `fetch`.
 */
export async function recordNetwork(page: Page): Promise<NetworkTrace> {
  let seq = 0;
  const trace: NetworkTrace = { events: [], requests: [], responses: [], scripted: [], frames: [] };

  await page.exposeFunction("__foyerScripted", (kind: string, url: string, method: string) => {
    const entry: ScriptedCall = {
      seq: seq++,
      kind: kind as ScriptedKind,
      url,
      method: (method || "GET").toUpperCase(),
    };
    trace.scripted.push(entry);
    trace.events.push(entry as TraceEvent);
  });

  await page.addInitScript(() => {
    const report = (kind: string, url: unknown, method: unknown) => {
      try {
        window.__foyerScripted?.(kind, String(url ?? ""), String(method ?? "GET"));
      } catch {
        /* the binding is gone mid-teardown; nothing useful to do */
      }
    };

    const nativeFetch = window.fetch;
    window.fetch = function patchedFetch(input: RequestInfo | URL, init?: RequestInit) {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.href
            : (input as Request).url;
      const method = init?.method ?? (input as Request)?.method ?? "GET";
      report("fetch", url, method);
      return nativeFetch.call(this as unknown as Window, input, init);
    };

    const nativeOpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function patchedOpen(
      this: XMLHttpRequest,
      method: string,
      url: string | URL,
      isAsync?: boolean,
      user?: string | null,
      password?: string | null,
    ) {
      report("xhr", url, method);
      return nativeOpen.call(this, method, url, isAsync ?? true, user, password);
    };

    if (typeof navigator.sendBeacon === "function") {
      const nativeBeacon = navigator.sendBeacon.bind(navigator);
      navigator.sendBeacon = (url: string | URL, data?: BodyInit | null) => {
        report("beacon", url, "POST");
        return nativeBeacon(url, data);
      };
    }
  });

  page.on("request", (req) => {
    const entry: WireRequest = {
      seq: seq++,
      kind: "request",
      url: req.url(),
      method: req.method(),
      resourceType: req.resourceType(),
      isNavigation: req.isNavigationRequest(),
      postData: req.postData(),
    };
    trace.requests.push(entry);
    trace.events.push(entry);
  });

  page.on("response", (res) => {
    const req = res.request();
    const entry: WireResponse = {
      seq: seq++,
      kind: "response",
      url: res.url(),
      status: res.status(),
      method: req.method(),
      resourceType: req.resourceType(),
      location: res.headers()["location"] ?? null,
    };
    trace.responses.push(entry);
    trace.events.push(entry);
  });

  const frame = (kind: FrameEvent["kind"]) => () => {
    const entry: FrameEvent = { seq: seq++, kind, url: page.url() };
    trace.frames.push(entry);
    trace.events.push(entry);
  };

  page.on("framenavigated", (f) => {
    if (f !== page.mainFrame()) return;
    const entry: FrameEvent = { seq: seq++, kind: "framenavigated", url: f.url() };
    trace.frames.push(entry);
    trace.events.push(entry);
  });
  page.on("domcontentloaded", frame("domcontentloaded"));
  page.on("load", frame("load"));

  return trace;
}

/** Scripted calls aimed at the portal's own API. Should always be empty. */
export function scriptedApiCalls(trace: NetworkTrace): ScriptedCall[] {
  return trace.scripted.filter((c) => c.url.includes("/api/v0/"));
}

/** A readable dump for a failure message. Worth the lines; a bare count is useless. */
export function describeTrace(trace: NetworkTrace): string {
  return trace.events
    .map((e) => {
      switch (e.kind) {
        case "request":
          return `  ${e.seq}  → ${e.method} ${e.url}  [${e.resourceType}${e.isNavigation ? ", navigation" : ""}]`;
        case "response":
          return `  ${e.seq}  ← ${e.status} ${e.method} ${e.url}${e.location ? `  → ${e.location}` : ""}`;
        case "fetch":
        case "xhr":
        case "beacon":
          return `  ${e.seq}  ⚠ page called ${e.kind}(): ${e.method} ${e.url}`;
        default:
          return `  ${e.seq}  · ${e.kind} ${e.url}`;
      }
    })
    .join("\n");
}
