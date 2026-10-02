/** @jsxImportSource preact */
import { groupVoucherCode, VOUCHER_LENGTH } from "@foyer/contract";
import type { Language, PortalStatus } from "@foyer/contract";
import { Shell } from "../ui/Shell.js";
import { Blocks, headline } from "./blocks.js";
import { GuestFields, Notice, WithdrawConsent } from "./details.js";
import { Alert, ArrowLeft, Check, Chevron, Info, Person, Phone, Ticket } from "../ui/icons.js";
import type { MessageKey } from "../i18n/index.js";
import type { PageContext, PageDefinition } from "./types.js";

/**
 * Renders a failure reason carried through the 303 as human copy.
 *
 * `id` is given on the form pages so the field can point `aria-describedby` at it:
 * the message is adjacent visually, and a screen reader that lands on the input
 * needs the same adjacency.
 */
function ErrorNote({ ctx, id }: { ctx: PageContext; id?: string }) {
  if (!ctx.error) return null;
  const key = `error.${ctx.error}` as MessageKey;
  const message = ctx.t(key) ?? ctx.t("error.generic");
  return (
    <p class="error" role="alert" id={id}>
      <Alert />
      <span>{message}</span>
    </p>
  );
}

// ── welcome ──────────────────────────────────────────────────────────────────

const welcome: PageDefinition = {
  path: "",
  title: (ctx) => `${ctx.t("welcome.title")} · ${ctx.venueName}`,
  render: (ctx) => (
    <Shell ctx={ctx} path="">
      {/*
        The venue's photograph, and it is a letterbox strip rather than a hero.
        Height is on the `--v` multiplier like every other vertical gap, so it
        compresses with the page and reaches zero at the CNA step — the 900 × 512
        box already only just fits the welcome page with the consent line on it, and
        a photograph is decoration where that line is not. Measured, both languages.

        Linked, not inlined: at 900 px a real photograph is 89,697 B against a
        51,200 B document budget, and as a data URI it costs *more than the raw
        file*. Same-origin, so it is reachable before the guest has internet — the
        portal's own FQDN is already required to be in the walled garden.

        `alt=""`: the venue is named in the masthead directly above, and decoration
        that announces itself makes a screen reader say the venue twice. Intrinsic
        dimensions are set so the image cannot reflow the consent text below it
        while it loads.
      */}
      {ctx.heroImageUrl ? (
        <img
          class="venue-hero"
          src={ctx.heroImageUrl}
          alt=""
          width="900"
          height="200"
          decoding="async"
        />
      ) : null}

      <h1 lang={headline(ctx).lang}>{headline(ctx).text}</h1>
      {ctx.networkName ? (
        <p class="lede">
          {ctx.t("welcome.network")} · {ctx.networkName}
        </p>
      ) : null}

      <ErrorNote ctx={ctx} />

      {/* A notice is the one block above the ways in: one short line, the kind a
          venue puts on the door. Everything longer waits below them, because a
          guest who cannot find how to get online reads nothing else. */}
      <Blocks ctx={ctx} placement="welcome" only="notice" class="blocks blocks-notice" />

      <p class="section-label">{ctx.t("welcome.choose")}</p>
      <div class="methods">
        {ctx.methods.includes("voucher") ? (
          <a class="method" href={`/${ctx.lang}/voucher/`}>
            <Ticket class="method-icon" />
            <span class="method-body">
              <span class="method-title">{ctx.t("welcome.voucher")}</span>
              <span class="method-hint">{ctx.t("welcome.voucherHint")}</span>
            </span>
            <Chevron class="method-chevron" />
          </a>
        ) : null}
        {ctx.methods.includes("account") ? (
          <a class="method" href={`/${ctx.lang}/account/`}>
            <Person class="method-icon" />
            <span class="method-body">
              <span class="method-title">{ctx.t("welcome.account")}</span>
              <span class="method-hint">{ctx.t("welcome.accountHint")}</span>
            </span>
            <Chevron class="method-chevron" />
          </a>
        ) : null}
        {ctx.methods.includes("phone") ? (
          <a class="method" href={`/${ctx.lang}/phone/`}>
            <Phone class="method-icon" />
            <span class="method-body">
              <span class="method-title">{ctx.t("welcome.phone")}</span>
              <span class="method-hint">{ctx.t("welcome.phoneHint")}</span>
            </span>
            <Chevron class="method-chevron" />
          </a>
        ) : null}
      </div>

      <Blocks ctx={ctx} placement="welcome" except="notice" />

      {/*
        Terms live here, in the flow, before authentication — never in a modal and
        never after. A modal is not a navigation, and Android destroys the login
        window the moment the network validates.
      */}
      <section class="terms">
        <h2>{ctx.t("welcome.termsHeading")}</h2>
        {/*
          `lang` only when the terms are not in the language of the document. The
          venue may have supplied its terms in one language only, and the portal
          shows them rather than nothing — but it never lets the markup claim they
          are in a language they are not, which is what a screen reader's voice and
          a browser's translation offer both key on.
        */}
        <p lang={ctx.terms.lang === ctx.lang ? undefined : ctx.terms.lang}>{ctx.terms.summary}</p>
        {/*
          Foyer's sentence, not the venue's.

          `docs/rules/security.md` §6 requires the product to say that the captive
          portal is a billing and consent boundary rather than a security one. As the
          last sentence of the default terms, any venue that wrote its own terms would
          delete it, silently, by doing the ordinary thing the terms field exists for.

          So it renders here instead: outside the venue's block, unconditionally, in
          the language of the *document* rather than of the terms — because unlike
          the terms above it, this sentence is the portal's own and the portal has it
          in every language it ships. Visually subordinate, next to an information
          icon, in the same register as the qualifier on the success page: a note
          from the software running the network, not a second paragraph of legalese
          competing with the first.
        */}
        <p class="boundary">
          <Info />
          <span>{ctx.t("welcome.boundary")}</span>
        </p>

        {/*
          Click-through, and it lives **here** rather than in the list of ways to
          sign in.

          Every other method is a route to a credential; this one has no credential
          at all — the consent *is* the authentication. So the button belongs
          immediately beneath the text being consented to, where a guest cannot press
          it without the terms in the same glance. A row in the method list would put
          one tap and a page turn between the words and the agreement, and a separate
          agree page would either render the terms twice or move them away from the
          action. Both are worse, and rendering the terms twice is the easier of the
          two defects to make.

          A real form POST answered `303`, like everything else here: the CNA
          re-checks captivity only after a full navigation, and a `fetch` would
          authenticate the guest and then hand them a button that disassociates them.
        */}
        {ctx.methods.includes("click-through") ? (
          <form class="agree" method="post" action="/api/v0/portal/agree" data-portal-form>
            <input type="hidden" name="lang" value={ctx.lang} />
            <GuestFields ctx={ctx} />
            {/* `btn`, the same class the voucher and account submits use. The portal
                has one button style on purpose — a second primary would put two
                competing calls to action on the page a guest is trying to leave. */}
            <button type="submit" class="btn">
              {ctx.t("welcome.agree")}
            </button>
          </form>
        ) : null}
      </section>
    </Shell>
  ),
};

// ── voucher ──────────────────────────────────────────────────────────────────

const voucher: PageDefinition = {
  path: "voucher/",
  enhance: true,
  title: (ctx) => `${ctx.t("voucher.title")} · ${ctx.venueName}`,
  render: (ctx) => (
    <Shell ctx={ctx} path="voucher/">
      <h1>{ctx.t("voucher.title")}</h1>

      {/*
        A real form POST answered with 303. Not a fetch: the CNA only re-checks
        captivity after a full navigation, and without one the guest is authorized
        and then offered a "Cancel" button that disassociates them from the SSID.
      */}
      <form method="post" action="/api/v0/portal/voucher" data-portal-form>
        <input type="hidden" name="lang" value={ctx.lang} />
        <div class="field">
          <label for="code">{ctx.t("voucher.label")}</label>
          {/*
            The rejected code comes back in the field. The design's rule: "server-side
            validation errors re-render the same document with the value preserved
            and the message adjacent to the field — because there is no client state
            to preserve it in." Without this, one wrong character out of twelve,
            typed one-handed from a thermal receipt, costs the guest all twelve.

            Rendered grouped, because that is what the guest is looking at and what
            the enhancement script would produce anyway; the server normalises, so
            the separators survive a no-script resubmit.
          */}
          <input
            id="code"
            name="code"
            type="text"
            class="code-input"
            required
            maxLength={VOUCHER_LENGTH + 2}
            autocomplete="off"
            autocapitalize="characters"
            autocorrect="off"
            spellcheck={false}
            inputMode="text"
            enterkeyhint="go"
            autofocus
            value={ctx.submittedCode ? groupVoucherCode(ctx.submittedCode) : undefined}
            placeholder={groupVoucherCode("XXXXXXXXXXXX")}
            aria-invalid={ctx.error ? "true" : undefined}
            aria-describedby={ctx.error ? "code-help code-error" : "code-help"}
          />
          <p class="help" id="code-help">
            {ctx.t("voucher.help")}
          </p>
          <ErrorNote ctx={ctx} id="code-error" />
        </div>
        <GuestFields ctx={ctx} />
        <button class="btn" type="submit">
          {ctx.t("voucher.submit")}
        </button>
      </form>

      <a class="back" href={`/${ctx.lang}/`}>
        <ArrowLeft width="16" height="16" />
        {ctx.t("voucher.back")}
      </a>
    </Shell>
  ),
};

// ── account ──────────────────────────────────────────────────────────────────

const account: PageDefinition = {
  path: "account/",
  enhance: true,
  title: (ctx) => `${ctx.t("account.title")} · ${ctx.venueName}`,
  render: (ctx) => (
    <Shell ctx={ctx} path="account/">
      <h1>{ctx.t("account.title")}</h1>

      <form method="post" action="/api/v0/portal/account" data-portal-form>
        <input type="hidden" name="lang" value={ctx.lang} />
        <div class="field">
          <label for="username">{ctx.t("account.username")}</label>
          {/*
            Standard autocomplete tokens. iOS 26 reportedly syncs captive-portal
            sign-in details across a user's devices; whether or not that holds,
            correct markup is what any autofill feeds on — and the CNA is already
            known to scrape and fill form inputs with injected JavaScript.
          */}
          {/*
            The username is *not* echoed back after a failure, unlike the voucher
            code. It is half of a credential pair, carrying it would put it in the
            query string and therefore in every access log on the way, and the guest
            has to retype the password regardless — so it would buy half a form for a
            permanent logging liability. See `resolveSubmittedCode` in `render.ts`.
          */}
          <input
            id="username"
            name="username"
            type="text"
            required
            autocomplete="username"
            autocapitalize="none"
            autocorrect="off"
            spellcheck={false}
            enterkeyhint="next"
            autofocus
            aria-invalid={ctx.error ? "true" : undefined}
            aria-describedby={ctx.error ? "account-error" : undefined}
          />
        </div>
        <div class="field">
          <label for="password">{ctx.t("account.password")}</label>
          <input
            id="password"
            name="password"
            type="password"
            required
            autocomplete="current-password"
            enterkeyhint="go"
            aria-invalid={ctx.error ? "true" : undefined}
            aria-describedby={ctx.error ? "account-error" : undefined}
          />
          {/* The failure belongs to the pair, not to either field, so both point here. */}
          <ErrorNote ctx={ctx} id="account-error" />
        </div>
        <GuestFields ctx={ctx} />
        <button class="btn" type="submit">
          {ctx.t("account.submit")}
        </button>
      </form>

      <a class="back" href={`/${ctx.lang}/`}>
        <ArrowLeft width="16" height="16" />
        {ctx.t("account.back")}
      </a>
    </Shell>
  ),
};

// ── connected ────────────────────────────────────────────────────────────────

function duration(ctx: PageContext, minutes: number | null | undefined): string | null {
  if (minutes == null) return ctx.t("unit.unlimited");
  if (minutes < 60) return `${minutes} ${ctx.t("unit.minutes")}`;
  const h = minutes / 60;
  // Separate singular and plural keys rather than one "hours" string. A portal that
  // reads "1 hours" is one nobody proofread, and it is the cheapest possible tell.
  return `${Number.isInteger(h) ? h : h.toFixed(1)} ${ctx.t(h === 1 ? "unit.hour" : "unit.hours")}`;
}

function allowance(mb: number | null | undefined): string | null {
  if (mb == null) return null;
  return mb >= 1000 ? `${(mb / 1000).toFixed(mb % 1000 === 0 ? 0 : 1)} GB` : `${mb} MB`;
}

/**
 * Venue-authored text, marked with the language it is actually written in.
 *
 * The same rule the terms block follows, for the same reason: the portal renders the
 * venue's words verbatim and never translates them, but it must not let the document
 * claim they are in a language they are not. A `lang` attribute is what a screen
 * reader's voice and a browser's translation offer both key on, and without it an
 * English page announces `Nhân viên` in English.
 *
 * A plan name is venue-authored in exactly the way the terms are. It arrives as one
 * string with `nameLang` beside it rather than as a per-language map, because a plan
 * has one name — the venue is not expected to translate its own price list, only to
 * say what language it wrote it in.
 */
function VenueText({ ctx, text, lang }: { ctx: PageContext; text: string; lang?: Language | null }) {
  if (!lang || lang === ctx.lang) return <>{text}</>;
  return <span lang={lang}>{text}</span>;
}

/**
 * The state of a `/connected/` document that is not a connected guest.
 *
 * Rendered in the problem page's shape rather than the success page's, because the
 * two answer different questions: this one has to say what happened and what to do,
 * and it must not carry a green tick, a plan, or "you can close this window" — the
 * last of which would send a guest away from the one screen that could still help
 * them. The recovery action goes back to sign-in rather than reloading, because
 * reloading is what they have already tried.
 */
function NotConnected({ ctx, message }: { ctx: PageContext; message: MessageKey }) {
  return (
    <Shell ctx={ctx} path="connected/">
      <h1>{ctx.t("problem.title")}</h1>
      <Notice ctx={ctx} />
      <p class="error" role="alert">
        <Alert />
        <span>{ctx.t(message)}</span>
      </p>
      <a class="back" href={`/${ctx.lang}/`}>
        <ArrowLeft width="16" height="16" />
        {ctx.t("connected.signIn")}
      </a>
    </Shell>
  );
}

/**
 * Which sentence a non-connected `/connected/` gets.
 *
 * `PortalStatus.state` is `connected | expired | revoked | not-on-network`, and the
 * absent case is a fifth outcome the contract cannot express because it is the
 * *absence* of a status: no grant answers to this browser. It is the common one —
 * the CNA's cookie jar is destroyed when the window closes, so a reopened portal has
 * no grant to find — and it is emphatically not a connected guest.
 *
 * `not-on-network` is the newest and the only one where **the guest did nothing
 * wrong and their code is still fine**: the venue granted, and the access point
 * never let the device on. The other three all end in "sign in again"; this one
 * cannot, because signing in again runs through the controller that just refused.
 * The fallback is `connected.unknown` rather than a throw — a state a future backend
 * adds must not blank the page — and it is the reason the copy for each one is
 * asserted rather than assumed.
 */
function notConnectedMessage(state: PortalStatus["state"] | undefined): MessageKey {
  if (state === "expired") return "connected.expired";
  if (state === "revoked") return "connected.revoked";
  if (state === "not-on-network") return "connected.notOnNetwork";
  return "connected.unknown";
}

const connected: PageDefinition = {
  path: "connected/",
  title: (ctx) =>
    `${ctx.t(ctx.status?.state === "connected" ? "connected.title" : "problem.title")} · ${ctx.venueName}`,
  render: (ctx) => {
    /*
     * The branch this page did not have.
     *
     * It read `ctx.status?.plan` and never `ctx.status.state`, so every one of the
     * four outcomes rendered the green tick and "You're online." Measured: a guest
     * whose voucher was revoked mid-session — a fixture somebody wrote on purpose,
     * and the one screen that reports its outcome to them — reloaded the page and
     * was told they were connected. So did a browser with no cookie at all.
     */
    if (ctx.status?.state !== "connected") {
      return <NotConnected ctx={ctx} message={notConnectedMessage(ctx.status?.state)} />;
    }

    const plan = ctx.status.plan;
    const data = allowance(plan?.dataAllowanceMb);
    // `enforcedBy` decides which sentence is true. A limit nothing enforces is not
    // a cap, and a sampled one overshoots — neither may be drawn as a live balance.
    const enforcement = plan?.enforcedBy ?? "unenforced";

    /*
     * The qualifier is assembled from whichever facts are on screen, and it renders
     * for a bare time limit as well as for a data figure. The success page may state
     * a time limit as a cap only *if the NAS is enforcing it*, so a duration shown
     * under `unenforced` needs the same sentence a byte figure does. Attaching the
     * qualifier only to the data figure would leave that case silent.
     */
    const notes: string[] = [];
    if (data) notes.push(ctx.t("connected.allowanceNote"));
    if (plan && enforcement === "unenforced") notes.push(ctx.t("connected.unenforcedNote"));
    else if (plan && enforcement === "foyer-sampled") notes.push(ctx.t("connected.sampledNote"));

    // Attacker-controlled: `originalUrl` originates in an unsigned NAS query
    // parameter. The server allowlists the scheme before it reaches the contract and
    // the schema constrains it, and it is checked again here because this is the
    // line that actually writes an `href` — the only one of the three where getting
    // it wrong is an open redirect on a page the guest has just typed into.
    const returnTo = safeHref(ctx.status.originalUrl);

    return (
      <Shell ctx={ctx} path="connected/">
        <p class="badge-ok">
          <Check width="15" height="15" />
          {ctx.t("connected.title")}
        </p>
        <h1>{ctx.t("connected.lede")}</h1>
        <Notice ctx={ctx} />

        {plan ? (
          <div class="facts">
            {plan.name ? (
              <div class="fact">
                <span class="fact-label">{ctx.t("connected.plan")}</span>
                <span class="fact-value">
                  <VenueText ctx={ctx} text={plan.name} lang={plan.nameLang} />
                </span>
              </div>
            ) : null}
            <div class="fact">
              <span class="fact-label">{ctx.t("connected.duration")}</span>
              <span class="fact-value">{duration(ctx, plan.durationMinutes)}</span>
            </div>
            {data ? (
              <div class="fact">
                <span class="fact-label">{ctx.t("connected.allowance")}</span>
                <span class="fact-value">{data}</span>
              </div>
            ) : null}
          </div>
        ) : null}

        {/*
          The qualifier travels with the number. `docs/protocol-reality.md`: byte
          counters advance only on Interim-Update or Stop, with a 300-second floor,
          and on the controller-API path there may be no accounting at all.
        */}
        {notes.length ? (
          <p class="qualifier">
            <Info />
            <span>{notes.join(" ")}</span>
          </p>
        ) : null}

        {/* The venue's page after sign-in — the one place a link is a link, because
            the guest has internet now. Before the way back to where they were going,
            which is the guest's own intent and stays last. */}
        <Blocks ctx={ctx} placement="connected" />

        {/*
          Rendered as a link the guest chooses to follow, never an automatic bounce.
          The victim of an open redirect here is on an untrusted network, expects to
          be redirected, and has just typed credentials.
        */}
        {returnTo ? (
          <a class="continue" href={returnTo} rel="noopener noreferrer">
            <span>
              <span class="continue-label">{ctx.t("connected.continue")}</span>
              <br />
              <span class="continue-url">{hostOf(returnTo)}</span>
            </span>
            <Chevron width="18" height="18" />
          </a>
        ) : null}

        <p class="close-hint">{ctx.t("connected.closeHint")}</p>
        <WithdrawConsent ctx={ctx} />
      </Shell>
    );
  },
};

/**
 * The last gate before a NAS-supplied string becomes an `href`.
 *
 * `docs/rules/frontend.md`: never render a NAS-supplied value as a link target
 * without validating the scheme — the portal is a prime open-redirect target
 * *because* the guest expects to be redirected. There are three gates on this value:
 * the mock/server allowlist that produces it, the `pattern` the schema now carries
 * so a violation fails contract validation instead of reaching a browser, and this
 * one. Only this one is adjacent to the DOM, so only this one is a defence rather
 * than an assumption about somebody else's code.
 *
 * `URL` parsing rather than a string test: it is the same parser the browser will
 * use, so `java\nscript:`, `HTTP://`, `//host` and `http://u:p@host` cannot mean one
 * thing here and another in the address bar.
 */
function safeHref(raw: string | null | undefined): string | null {
  if (!raw) return null;
  try {
    const u = new URL(raw);
    if (u.protocol !== "http:" && u.protocol !== "https:") return null;
    if (u.username || u.password) return null;
    return u.toString();
  } catch {
    return null;
  }
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

// ── problem ──────────────────────────────────────────────────────────────────

const problem: PageDefinition = {
  path: "problem/",
  title: (ctx) => `${ctx.t("problem.title")} · ${ctx.venueName}`,
  render: (ctx) => (
    <Shell ctx={ctx} path="problem/">
      <h1>{ctx.t("problem.title")}</h1>
      <p class="error" role="alert">
        <Alert />
        <span>{ctx.t((`error.${ctx.error ?? "generic"}`) as MessageKey) ?? ctx.t("error.generic")}</span>
      </p>
      <a class="back" href={`/${ctx.lang}/`}>
        <ArrowLeft width="16" height="16" />
        {ctx.t("problem.back")}
      </a>
    </Shell>
  ),
};

// ── phone ────────────────────────────────────────────────────────────────────

/**
 * Phone sign-in: the number, then — with `?sent=1` — the code.
 *
 * Two forms on one document, and the flag only chooses which is shown: the code is
 * checked against this browser's session, never against the URL. Both are real form
 * POSTs answered `303`, for the reason every other form here is.
 *
 * **Nothing is echoed.** The number is not put back in its field after a refusal —
 * it is personal data and would have to travel in the query string — and the code is
 * a credential. The Go twin is `internal/portal/render_phone.go`.
 */
/**
 * The code field's `pattern`: digits and the separators a guest may paste around
 * them — everything `otp.NormaliseCode` accepts. The Go twin is
 * `portal.OTPPattern`, and the two must be the same string.
 */
export const OTP_PATTERN = "[0-9\\s\\-]*";

const phone: PageDefinition = {
  path: "phone/",
  enhance: true,
  title: (ctx) => `${ctx.t("phone.title")} · ${ctx.venueName}`,
  render: (ctx) =>
    ctx.phoneSent ? (
      <Shell ctx={ctx} path="phone/">
        <h1>{ctx.t("phone.codeTitle")}</h1>
        <p class="lede">{ctx.t("phone.sentLede").replaceAll("{minutes}", String(ctx.phoneCodeMinutes))}</p>
        <form method="post" action="/api/v0/portal/phone/code" data-portal-form>
          <input type="hidden" name="lang" value={ctx.lang} />
          <div class="field">
            <label for="otp">{ctx.t("phone.codeLabel")}</label>
            {/*
              `one-time-code` is what lets iOS offer the code from the message above the
              keyboard. Whether the captive-network sheet honours it is UNVERIFIED.

              `otp-input`, never `code-input`: the voucher
              class is what the enhancement script groups into `XXXX-XXXX-XXXX`, which
              turned a six-digit code into `2310-86` and had the browser refuse the
              submit. The pattern admits what `otp.NormaliseCode` admits, so a code
              pasted as `231 086` submits with JavaScript off as well.
            */}
            <input
              id="otp"
              name="code"
              type="text"
              class="otp-input"
              required
              maxLength={12}
              inputMode="numeric"
              pattern={OTP_PATTERN}
              autocomplete="one-time-code"
              enterkeyhint="go"
              autofocus
              aria-invalid={ctx.error ? "true" : undefined}
              aria-describedby={ctx.error ? "otp-error" : undefined}
            />
            <ErrorNote ctx={ctx} id="otp-error" />
          </div>
          {/* The venue's details and its box, less the number the code proved. */}
          <GuestFields ctx={ctx} omit="phone" />
          <button class="btn" type="submit">
            {ctx.t("phone.verify")}
          </button>
        </form>
        <a class="back" href={`/${ctx.lang}/phone/`}>
          <ArrowLeft width="16" height="16" />
          {ctx.t("phone.again")}
        </a>
      </Shell>
    ) : (
      <Shell ctx={ctx} path="phone/">
        <h1>{ctx.t("phone.title")}</h1>
        <form method="post" action="/api/v0/portal/phone" data-portal-form>
          <input type="hidden" name="lang" value={ctx.lang} />
          <div class="field">
            <label for="phone">{ctx.t("phone.label")}</label>
            <input
              id="phone"
              name="phone"
              type="tel"
              required
              maxLength={32}
              autocomplete="tel-national"
              inputMode="tel"
              enterkeyhint="send"
              autofocus
              aria-invalid={ctx.error ? "true" : undefined}
              aria-describedby={ctx.error ? "phone-help phone-error" : "phone-help"}
            />
            <p class="help" id="phone-help">
              {ctx.t("phone.help")}
            </p>
            <ErrorNote ctx={ctx} id="phone-error" />
          </div>
          <button class="btn" type="submit">
            {ctx.t("phone.send")}
          </button>
        </form>
        <a class="back" href={`/${ctx.lang}/`}>
          <ArrowLeft width="16" height="16" />
          {ctx.t("phone.back")}
        </a>
      </Shell>
    ),
};

export const PAGES: PageDefinition[] = [welcome, voucher, account, connected, problem, phone];

export function findPage(path: string): PageDefinition | undefined {
  const normalised = path.replace(/^\/+/, "").replace(/\/*$/, path.endsWith("/") ? "/" : "");
  return PAGES.find((p) => p.path === normalised || p.path === `${normalised}/`);
}
