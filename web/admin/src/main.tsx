import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import { ApiError } from "./api.js";
import { App } from "./App.js";
import { localesSettled } from "./i18n/catalogue.js";
import "./styles/admin.css";

/**
 * Whether asking the same question again could get a different answer.
 *
 * **`retry: 1` with no predicate was a defect**, and a staff session made it
 * visible: `/operators` and `/audit` were each requested **twice** for one visit, and
 * both requests were answered `403`. A refusal on the grounds of who this operator is
 * cannot become a `200` a tick later — the second ask buys a second line in the
 * server's log, a slower refusal on screen, and nothing else.
 *
 * The line is drawn where the answer stops being about the request:
 *
 *  - **`4xx` — never.** `400`, `401`, `403`, `404`, `409`, `413`, `422` are all
 *    decisions about *this request* or *this requester*, and neither changes between
 *    two attempts one tick apart. `401` in particular is already routed to the sign-in
 *    screen by `api.ts`, so retrying it delays the only thing that helps.
 *  - **`5xx` — once.** The server failed on its own, which is the one class of answer
 *    that can differ on a second attempt: a restarting process, a connection pool that
 *    was momentarily exhausted, an upstream that recovered.
 *  - **`status === 0` — once.** `api.ts` synthesises `/problems/unreachable` when
 *    `fetch` rejects and there is no response at all. That is the ordinary shape on a
 *    venue LAN, which is exactly the network an operator has opened this console to
 *    fix, and it is the reason `networkMode: "always"` is set below.
 *  - **Anything that is not an `ApiError` — never.** By the time an error reaches
 *    here, `api.ts` has classified everything the server or the transport can produce,
 *    so what is left is a bug in this console. Repeating it repeats the bug.
 */
function worthAskingAgain(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  return error.status === 0 || error.status >= 500;
}

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `networkMode: "always"`, and it is not a preference
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The default is `"online"`, which gates every request on `onlineManager` — a flag
 * driven purely by the browser's `online`/`offline` events. **That flag is about the
 * internet, and this console never talks to the internet.** `api.ts` builds every
 * request from `BASE = "/api/v0"`, so the server it asks is the origin that just
 * served the document: if this code is running at all, that server was reachable.
 * Asking a browser's opinion about upstream connectivity before calling back to your
 * own origin is asking the wrong question.
 *
 * **It matters here more than most.** Foyer is captive-portal software, so the
 * operator's laptop is routinely on a venue LAN whose uplink is exactly what they
 * have opened this console to fix. The console being unusable in that moment is the
 * one failure mode this product cannot have.
 *
 * And a gated request does not fail. It **pauses** — `status: "pending"`,
 * `fetchStatus: "paused"`, `error: null`, indefinitely. Nothing is retried and
 * nothing is reported. Driven with a synthetic
 * `window.dispatchEvent(new Event("offline"))` — which is what `onlineManager`
 * listens to, and which leaves the transport completely alone — `/sessions` and
 * `/operators` each send **zero requests to `/api/v0/`** against a mock that is
 * answering the whole time.
 *
 * ── TWO INDEPENDENT GUARANTEES, DELIBERATELY ────────────────────────────────
 *
 * This is the *cause* of a symptom `ui/table.tsx` also fixes: a paused query reached
 * `Table` as "not loading, no error, no rows" and every list screen rendered its
 * never-had-data state — *"No operators. Nobody can sign in to this console."*
 *
 * Either change removes that sentence on its own, and that is the point rather than
 * redundancy to tidy up. **This one stops the console declining to ask; that one
 * stops it concluding *empty* from silence.** A query can still be genuinely
 * unanswered — first paint, a slow backend — and `Table` must not call that nothing;
 * a request can still be worth sending when a browser believes it is offline, and
 * this must not be the thing deciding. Either alone hides the bug. Both together
 * mean neither can bring it back.
 *
 * Mutations carry it for the same reason and a sharper one: a paused mutation is a
 * write an operator pressed a button for and that never left the page.
 */
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Operational data goes stale quickly, but a refetch must never blank the
      // view — the table keeps its rows and moves the indicator into the header.
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      /* One retry, and only for an answer a second attempt could change. See
         `worthAskingAgain` — a bare `retry: 1` asked every `403` twice. */
      retry: (failureCount, error) => failureCount < 1 && worthAskingAgain(error),
      networkMode: "always",
    },
    mutations: {
      networkMode: "always",
    },
  },
});

/* Nothing renders until the core translation is in, when the console speaks
   anything but English (`i18n/lang.ts`): a first frame in English that then turns
   Vietnamese is the flash every product surveyed avoids. For an English reader this
   resolves in the same tick. */
void localesSettled().then(() =>
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </QueryClientProvider>
    </StrictMode>,
  ),
);
