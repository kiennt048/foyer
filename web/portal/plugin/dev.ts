import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { Plugin, ViteDevServer } from "vite";
import type { Language, PortalContext, PortalStatus } from "@foyer/contract";
import { DEFAULT_LANGUAGE, LANGUAGES, isLanguage, negotiateLanguage } from "../src/i18n/index.js";
import type { RenderInput } from "../src/render.js";

const CSS_PATH = fileURLToPath(new URL("../src/styles/portal.css", import.meta.url));
const MOCK = process.env.MOCK_ORIGIN ?? "http://localhost:5170";

/**
 * Serves the portal the way production will: the server decides the language,
 * resolves the client's context, renders a complete document, and sends it.
 *
 * There is no client-side router and no hydration. That is not a simplification for
 * the prototype — it is the constraint. The CNA re-checks captivity only after a
 * full URL navigation, so every step of this flow has to be one.
 */
export function portalDev(): Plugin {
  let server: ViteDevServer;

  return {
    name: "foyer:portal-dev",
    configureServer(s) {
      server = s;
      s.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url ?? "/", "http://localhost");
        const path = url.pathname;

        if (path.startsWith("/@") || path.startsWith("/src/") || path.startsWith("/node_modules/")) {
          return next();
        }

        /*
         * `/` → the default language. Production does the same from Accept-Language.
         *
         * **The query string comes with it.** A guest does not arrive at a bare
         * root: the NAS appends its own parameters — for UniFi `ap`, `id`, `t`,
         * `ssid` and `url` (`docs/protocol-reality.md`) — and this redirect is the
         * first thing that happens to them. Dropping the query threw away the MAC,
         * AP and site that `VerifyPresence` is required to check before any grant
         * exists (`docs/rules/security.md` §4), and threw away `url`, which is
         * the only source for the success page's "continue to where you were going".
         *
         * Relayed verbatim and not parsed. These parameters are unsigned and
         * attacker-controlled; forwarding them is transport, and every one of them
         * is still untrusted at the far end.
         */
        if (path === "/") {
          /*
           * Negotiated, not defaulted.
           *
           * Redirecting to `DEFAULT_LANGUAGE` unconditionally would mean **a venue
           * advertising only Vietnamese greets its guests in English** — on the one
           * screen a single-language venue exists not to have, the one with a
           * switcher. The negotiation is against `Accept-Language` *intersected with
           * the site's advertised languages*, not only the half an E2E suite might
           * assert.
           *
           * That gap matters more than a dev convenience: the portal is reviewed
           * against this middleware, so a stand-in that models the rule wrongly
           * approves the wrong behaviour. Production is the Go binary's obligation
           * and it implements the same exported function.
           *
           * A mock that cannot be reached falls back to the default rather than
           * failing the request — a guest arriving mid-outage should still get a page.
           */
          let advertised: Language[] | undefined;
          try {
            // The claim is relayed here too, and not only for the type: this is a
            // guest arriving at `/` straight off a NAS redirect, so the languages
            // being negotiated must be **their** venue's rather than the default
            // one's.
            const { context } = await fetchPortalState(req.headers.cookie ?? "", url.searchParams);
            advertised = context?.languages;
          } catch {
            /* fall through to the default */
          }
          const lang = negotiateLanguage(req.headers["accept-language"] ?? null, advertised);
          res.statusCode = 302;
          res.setHeader("Location", `/${lang}/${url.search}`);
          /* The negotiation varies on the header, so a cache that ignored it would
             serve one guest's language to the next. */
          res.setHeader("Vary", "Accept-Language");
          return res.end();
        }

        const match = new RegExp(`^/(${LANGUAGES.join("|")})/(.*)$`).exec(path);
        if (!match || !isLanguage(match[1])) return next();

        const lang: Language = match[1];
        const rest = match[2] ?? "";

        try {
          const mod = (await s.ssrLoadModule("/src/render.ts")) as typeof import("../src/render.js");
          const cookie = req.headers.cookie ?? "";
          const { context, status, setCookie, refused } = await fetchPortalState(
            cookie,
            url.searchParams,
          );
          // The mock issues the client cookie on first contact. Because the render
          // happens server-side here, it has to be relayed on to the browser or
          // every navigation would look like a brand-new client and no grant would
          // ever survive a step. Cookies ignore port, so one set for `localhost`
          // reaches the mock on its own port too.
          if (setCookie) res.setHeader("Set-Cookie", setCookie);

          const input: RenderInput = {
            lang,
            path: rest,
            context,
            status,
            // An explicit `?e=` wins: it is the reason the *previous* step
            // redirected with, and it is more specific than the claim's verdict.
            error: url.searchParams.get("e") ?? refused,
            // `?code=` — the voucher code that was just rejected, echoed back so the
            // guest does not retype twelve characters because they got one wrong.
            // Same name as the form field it came from, so the redirect is a literal
            // echo rather than a mapping somebody has to remember. Sanitised in
            // `render.ts`, not here.
            code: url.searchParams.get("code"),
            // `?sent=1` on the phone page chooses the code form; nothing else.
            phoneSent: url.searchParams.get("sent") === "1",
            // `?n=` — a success to read, allowlisted in `render.ts`.
            notice: url.searchParams.get("n"),
            // Verbatim, for the language links. Same rule as the root redirect above:
            // these parameters are relayed, never parsed, and stay untrusted.
            query: url.search,
          };
          const page = mod.renderPage(input);
          if (!page) return next();

          const doc = (await s.ssrLoadModule("/src/document.ts")) as typeof import("../src/document.js");
          let html = doc.renderDocument({
            lang,
            title: page.title,
            body: page.body,
            css: readFileSync(CSS_PATH, "utf8"),
            // In dev the script is a module URL so HMR and source maps work. The
            // build inlines it; the budget check runs against the build output.
            script: page.enhance ? { src: "/src/enhance/index.ts" } : undefined,
          });
          html = await s.transformIndexHtml(req.url ?? "/", html);

          res.statusCode = 200;
          res.setHeader("Content-Type", "text/html; charset=utf-8");
          // A cached portal document is a portal that tells a returning guest they
          // are still connected when they are not.
          res.setHeader("Cache-Control", "no-store, no-cache, must-revalidate");
          return res.end(html);
        } catch (err) {
          s.ssrFixStacktrace(err as Error);
          return next(err);
        }
      });
    },
    handleHotUpdate({ file }) {
      // Editing a page or the stylesheet has to reload the document, because the
      // document is server-rendered — there is nothing on the client to patch.
      if (file.includes("/src/")) server.ws.send({ type: "full-reload" });
    },
  };
}

// The parameters a NAS puts on the redirect, relayed to the mock verbatim.
//
// Relaying only `present` would not be a missing feature — it would mean **a
// reviewer could not see the refusal they were checking.** A URL carrying
// `ap=aa:bb:cc:dd:ee:ff` whose parameter is dropped here makes the mock answer
// as though no claim had been made, and a portal that verifies the access point
// and one that ignores it render the same page. `docs/rules/security.md` §4 is
// the rule with the most attacker in front of it in this product, and this relay
// is what lets a developer at a desk exercise it.
//
// The names are the NAS's, not ours: `id` is the client MAC and `ap` the access
// point's, spelled as UniFi spells them, matching `internal/portal`'s `claimFrom`
// and the mock's `nasClaim`. `present=0` stays because it is the switch that
// drives a *verified* device to refuse, which no real redirect can express.
const NAS_REDIRECT_PARAMS = ["present", "id", "mac", "ap", "ssid", "url", "t"] as const;

async function fetchPortalState(
  cookie: string,
  query: URLSearchParams,
): Promise<{
  context: PortalContext | null;
  status: PortalStatus | null;
  setCookie: string | null;
  /**
   * `not-present` when the server refused the claim, null otherwise.
   *
   * **A refusal and an outage are not the same fact, so they are not the same
   * value here.** If both produced `context: null`, the renderer — which treats
   * a null context as *the mock is down, still give the guest a page* — would
   * let the refusal reach the server and change nothing on the screen. Two
   * situations sharing one representation is why no assertion could tell them
   * apart.
   *
   * The distinction is not invented here. `internal/portal` already draws it:
   * `VerdictRefuse` is *the device is not here* and is "the only one
   * `error.not-present` describes"; `VerdictUnavailable` deliberately answers
   * `generic` instead, because sending somebody to toggle Wi-Fi for twenty
   * minutes over a server fault is the worse error of the two. This reads that
   * rule rather than restating it.
   */
  refused: "not-present" | null;
}> {
  const headers = cookie ? { cookie } : undefined;
  const relayed = new URLSearchParams();
  for (const name of NAS_REDIRECT_PARAMS) {
    const value = query.get(name);
    if (value !== null) relayed.set(name, value);
  }
  const suffix = relayed.size > 0 ? `?${relayed.toString()}` : "";

  const ctxRes = await fetch(`${MOCK}/api/v0/portal/context${suffix}`, { headers }).catch(
    () => null,
  );
  const setCookie = ctxRes?.headers.get("set-cookie") ?? null;

  // Carry the freshly-issued cookie into the second call, or the status lookup
  // would run as a different client than the one just created.
  const onward = setCookie ? `${cookie ? `${cookie}; ` : ""}${setCookie.split(";")[0]}` : cookie;
  const statusRes = await fetch(`${MOCK}/api/v0/portal/status`, {
    headers: onward ? { cookie: onward } : undefined,
  }).catch(() => null);

  return {
    context: ctxRes?.ok ? ((await ctxRes.json()) as PortalContext) : null,
    status: statusRes?.ok ? ((await statusRes.json()) as PortalStatus) : null,
    setCookie,
    // 403 is the server having answered and said no. A network failure leaves
    // `ctxRes` null and falls through to the outage path, which stays silent.
    refused: ctxRes?.status === 403 ? "not-present" : null,
  };
}
