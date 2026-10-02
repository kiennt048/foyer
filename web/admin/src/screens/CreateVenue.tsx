import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, api } from "../api.js";
import { t } from "../i18n/index.js";
import { ErrorNotice } from "../ui/primitives.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE FIRST VENUE — the step that could not live on a venue-scoped screen
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `createSite` is the only way the first venue comes into being: the Go server
 * implements it, `web/mock` implements it, and `web/contract/openapi.yaml` documents
 * why it must exist at all — *a migrated database holds zero sites and nothing else
 * in the product could put the first row in*. This file is where the one client an
 * operator actually has makes that request.
 *
 * The console seeds no fixture venue, so on a fresh deployment `SiteGate` renders a
 * page that says *"No venue yet"*. Without a way to make one from that page, the
 * console would tell the operator clearly that they are stuck — a worse place to stop
 * than a console that merely confuses them.
 *
 * The release bar is *a stranger installs it and a phone gets online*, timed, and
 * `docs/rules/deployment.md` says the five minutes that matter are the ones after
 * the container starts. A first run that requires `curl` fails that bar.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  HOW THIS RELATES TO THE OTHER FIRST-RUN SCREEN, WHICH IS THE REAL QUESTION
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `FirstRun.tsx` is the other half and it sits beside this file on purpose. It is a
 * **derived checklist**: register a device, create a batch of codes, create an
 * account — a step is done when the thing it asks for exists, nothing is stored, and
 * an operator who abandons it halfway has a half-configured venue rather than a
 * half-finished wizard.
 *
 * **The venue is not a fourth step in that list.** Every step `FirstRun` measures is
 * a question with a `siteId` in it — `listNas({siteId})`, `listBatches({siteId})`,
 * `getPortalSettings(siteId)` — and before a venue exists there is no id to ask with.
 * That is not a scoping detail; it is why `SiteGate` intercepts *before* the dashboard
 * mounts, and it is why a create-venue control placed inside `FirstRun` would be
 * unreachable in exactly the world it exists for.
 *
 * So: **one first run, two render sites, and the seam is where `siteId` starts
 * existing.** This step renders in the gate because it is the only one whose
 * precondition is that no venue exists; every later step renders in the panel because
 * every later step is scoped to the venue this one makes.
 *
 * What makes that one flow rather than two screens each knowing half is that the
 * **continuity is visible and is not a navigation**. On success this invalidates
 * `["sites"]` and does nothing else: the list comes back with one venue,
 * `resolveSite` falls through to *the deployment's venue*, `status` becomes `ready`,
 * and the gate opens onto the dashboard — where `FirstRun` is already asking for an
 * access point. **The gate closes because the fact it was gating on stopped being
 * true**, which is the same discipline `FirstRun` uses for every one of its own
 * steps. `site.create.next` says so before the operator presses, so the form reads as
 * step one of a short path rather than as an isolated dead end.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  WHY IT IS A FORM ON THE PAGE AND NOT A DIALOG, AND NOT A ROUTE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Not a dialog.** When this renders it is the only thing on the screen and there is
 * exactly one thing to do, so a dialog would be a click in front of a single text
 * field — and it would add a sixth overlay to `dialog-focus-return`,
 * `dialogs-are-named` and `shortcuts-do-not-reach-through-a-modal` to buy nothing. An
 * inline form has no focus trap to get wrong and no return of focus to lose.
 *
 * **Not a route.** `createSite` can succeed exactly once per installation, so a
 * permanent destination for it is dead on every deployment past its first minute —
 * and `docs/rules/frontend.md` names the first-release screens, which are five and
 * do not include this. It is also the reason this is not lazy: `SiteGate` is in
 * `App.tsx` and therefore in the entry chunk, and `Dashboard.tsx`'s header records
 * what a `lazy()` edge out of an entry module costs — 3 115 B *worse*, because Rollup
 * hoists everything the new chunk shares with the entry and the split streams
 * compress worse than the one they replaced.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE PRICE IS STATED BEFORE THE OPERATOR COMMITS, NOT AFTER
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * One venue per install, and the second call is refused `409`. A form that lets an
 * operator find that out by being refused is the shape `docs/rules/frontend.md`
 * rejects for capability-gated controls — *state the price beside the control, before
 * the operator commits*. `site.create.nameHint` says it is the only venue they will
 * name here, at the moment they are choosing the name.
 *
 * The `409` is still handled, because it is reachable and not by pressing twice: the
 * venue list is fetched, this renders, and *then* somebody else creates the venue —
 * a second tab, another operator, `deploy/first-boot.sh`, a `curl`. `problems.ts`
 * already owns that sentence and its comment predicted this screen, so it is rendered
 * rather than restated. **And it is not a dead end either**: the refusal means a venue
 * now exists, so the notice carries a control that re-asks for the list, which opens
 * the gate. A refusal an operator can read and then act on is the whole difference
 * between this file and the state it replaced.
 */
export function CreateVenue() {
  const qc = useQueryClient();
  const [name, setName] = useState("");

  const create = useMutation({
    mutationFn: () => api.createSite({ name: name.trim() }),
    /*
     * Nothing but the invalidation, and the `await` is load-bearing.
     *
     * Returning the promise keeps `isPending` true until the venue list has actually
     * come back, so the form does not blink through an empty, enabled state between
     * the `201` and the gate opening. There is no `navigate` here on purpose: the
     * operator is already on the route they asked for — the gate was standing in
     * front of it — so pushing an address would put a page in the back stack whose
     * only content was a form that can never succeed again.
     */
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sites"] }),
  });

  const fieldErrors = create.error instanceof ApiError ? create.error.fieldErrors : {};
  /* The one refusal that means *a venue exists now*, which is a different next action
     from every other failure this form can produce. */
  const refusedAsSecond =
    create.error instanceof ApiError &&
    create.error.problem?.type === "/problems/one-site-per-install";

  return (
    <section className="section" data-create-venue="">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          /* The guard is here rather than on the button, because an `aria-disabled`
             submit still submits and `Enter` in the field submits without touching the
             button at all. See `screens.test.ts`. */
          if (create.isPending) return;
          create.mutate();
        }}
      >
        {create.error ? (
          <div style={{ marginBottom: "0.875rem" }}>
            <ErrorNotice error={create.error} title={t("site.create.failed.title")} />
            {refusedAsSecond ? (
              <p style={{ marginTop: "0.625rem" }}>
                <button
                  type="button"
                  className="btn btn-sm"
                  data-create-venue-continue=""
                  onClick={() => qc.invalidateQueries({ queryKey: ["sites"] })}
                >
                  {t("site.create.refused.continue")}
                </button>
              </p>
            ) : null}
          </div>
        ) : null}

        <div className="form-grid">
          <div className="field">
            <label htmlFor="venue-name">{t("site.create.name")}</label>
            <input
              id="venue-name"
              value={name}
              autoComplete="off"
              /* The first thing on an otherwise empty page, and the only thing to do
                 on it. Nothing else can reasonably hold focus here. */
              autoFocus
              /* Native validation runs before `submit` fires, so an empty name is
                 refused by the user agent in its own language rather than by a
                 round trip that comes back `422` naming a field the operator can see
                 is blank. */
              required
              aria-invalid={Boolean(fieldErrors.name) || undefined}
              aria-describedby={fieldErrors.name ? "venue-name-error" : "venue-name-hint"}
              onChange={(e) => setName(e.target.value)}
            />
            {fieldErrors.name ? (
              <p className="field-error" role="alert" id="venue-name-error">
                {fieldErrors.name}
              </p>
            ) : (
              <p className="field-hint" id="venue-name-hint">
                {t("site.create.nameHint")}
              </p>
            )}
          </div>

          {/* `aria-disabled`, never `disabled`: the operator pressed this, so it has
              focus, and the user agent blurs a focused element to `<body>` the moment
              `disabled` is set on it. `pending-does-not-eat-focus` covers fourteen
              controls that did exactly that. */}
          <button
            type="submit"
            className="btn btn-primary"
            data-create-venue-submit=""
            aria-disabled={create.isPending || undefined}
          >
            {t("site.create.submit")}
          </button>
        </div>

        {/* After the control, because it describes what pressing it leads to rather
            than how to fill the field in. This is the whole of the continuity between
            the two first-run surfaces — see the header. */}
        <p className="field-hint" data-create-venue-next="">
          {t("site.create.next")}
        </p>
      </form>
    </section>
  );
}

export default CreateVenue;
