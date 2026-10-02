/** @jsxImportSource preact */
import type { PageContext } from "./types.js";

/*
 * What a venue asks for at sign-in. `internal/portal/
 * render_guestdata.go` is the Go twin and renders the same structure.
 *
 *   - **Only the fields the venue asked for**, in its order, each marked optional
 *     unless the venue made it required — and a required one carries `required`, so
 *     the browser stops an empty submit before the server has to.
 *   - **The marketing checkbox is never pre-ticked**, and nothing here can make it
 *     so. Its sentence is the venue's own, resolved like the terms; with no sentence
 *     in any language there is no checkbox. The hidden `consentVersion` is the
 *     SHA-256 of exactly the sentence rendered, so the record names what the guest
 *     read.
 *   - **Nothing a guest typed is rendered back.** A refused form comes back empty
 *     with a reason: the value would have had to travel in the address.
 */

const ATTRS = {
  name: { type: "text", autocomplete: "name", maxLength: 120 },
  phone: { type: "tel", autocomplete: "tel", inputMode: "tel", maxLength: 32 },
  email: {
    type: "email",
    autocomplete: "email",
    inputMode: "email",
    maxLength: 254,
    autocapitalize: "none",
    spellcheck: false,
  },
} as const;

/**
 * `omit` is the one field a form already has: phone sign-in's code form, whose
 * number the code just proved — `guestFieldsExcept` in the Go twin.
 */
export function GuestFields({ ctx, omit }: { ctx: PageContext; omit?: "phone" }) {
  const d = ctx.dataCollection;
  if (!d) return null;
  const consent = d.consent;
  return (
    <>
      {d.fields.filter((f) => f.field !== omit).map((f) => (
        <div class="field" key={f.field}>
          <label for={`g-${f.field}`}>
            {ctx.t(`details.${f.field}`)}
            {f.required ? null : (
              <>
                {" "}
                <span class="optional">({ctx.t("details.optional")})</span>
              </>
            )}
          </label>
          <input id={`g-${f.field}`} name={f.field} {...ATTRS[f.field]} required={f.required} />
        </div>
      ))}
      {consent ? (
        <div class="consent">
          <input type="checkbox" id="g-consent" name="marketingConsent" value="yes" />
          <label for="g-consent" lang={consent.lang === ctx.lang ? undefined : consent.lang}>
            {consent.text}
          </label>
          <input type="hidden" name="consentVersion" value={consent.version} />
        </div>
      ) : null}
    </>
  );
}

/** A success carried through a `303`, as `?n=`. */
export function Notice({ ctx }: { ctx: PageContext }) {
  if (!ctx.notice) return null;
  return (
    <p class="notice" role="status">
      <span>{ctx.t(`notice.${ctx.notice}`)}</span>
    </p>
  );
}

/**
 * The guest's own way out of a marketing consent, wherever the venue asks for one.
 * Shown whether or not this device consented: the page does not know, and must not
 * be the thing that tells a stranger.
 */
export function WithdrawConsent({ ctx }: { ctx: PageContext }) {
  if (!ctx.dataCollection?.consentAsked) return null;
  return (
    <form class="withdraw" method="post" action="/api/v0/portal/marketing-consent/withdraw" data-portal-form>
      <input type="hidden" name="lang" value={ctx.lang} />
      <p class="help">{ctx.t("connected.withdrawHint")}</p>
      <button type="submit" class="btn-quiet">
        {ctx.t("connected.withdraw")}
      </button>
    </form>
  );
}
