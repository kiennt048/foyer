import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test, type Page, type Route } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";
/* The pair rule moved to `helpers/fields.ts` when a second spec needed it — see
   that file's header, and `coa-vendor.spec.ts`, which judges the rung above it. */
import { fieldPair, fieldPairFaults } from "../helpers/fields.js";
import { MOCK_URL } from "../helpers/env.js";
import { ADMINISTRATOR, signInAs } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `error-architecture` — the refusal an operator reads, driven rather than rendered
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `admin/src/problems.test.ts` says so in its own header: it proves
 * the catalogue **compiles and interpolates**, and proves nothing about whether any
 * code path in the console produces a type. This file is the other half — every
 * sentence below is on screen because a request was really refused.
 *
 * The architecture being locked, so a reader knows what a real assertion looks like:
 *
 *   - `Problem.type` is a closed enum (`contract/openapi.yaml`, `ProblemType`).
 *   - `admin/src/problems.ts` is a `Record<ProblemType, Copy>` and owns every
 *     sentence; a type added to the contract does not compile until it has copy.
 *   - Field copy keys on `(type, field, fault)`, `fault` coming from `params.fields`.
 *   - `detail` is **fallback-only** and `ApiError` does not expose it at all.
 *   - `/problems/unreachable` is **synthesised in `api.ts`** when `fetch` rejects.
 *
 * ── THE FIVE VACUOUS ASSERTIONS THIS FILE REFUSES TO MAKE ────────────────────
 *
 * Every one of them is **green against a console with the defect**, which is
 * why they are named here rather than left to judgement:
 *
 *  1. *Render a component with a `type` prop and assert the sentence.* That is
 *     `problems.test.ts` again, in a browser. Nothing here mounts a component: every
 *     sentence arrives from a request that was refused, and the type it was refused
 *     with is read off the wire in the same test (`refusalSeen`).
 *  2. *Assert a live region exists.* `/portal` has an `aria-live` Toast host on every
 *     load whose save-failure path never writes to it. Existence is green there and
 *     says nothing. Every assertion below is on a region's **text**, after the drive.
 *  3. *Assert a dialog "stays open" on failure.* It does, byte-identically, against
 *     a console that says nothing: a `/garden` delete dialog left unchanged by a `500`
 *     passes it. So the dialog cases assert the catalogue's sentence
 *     **inside `dialog[open]`**, never the openness alone.
 *  4. *Assert `aria-invalid` alone.* Green on `/garden` and `/portal` even with the
 *     message missing. `fieldPairFaults` asserts the **pair**, and that every id in
 *     `aria-describedby` resolves to a node that exists and that one of them carries
 *     the message.
 *  5. *Text-match the server's own sentence.* Every expectation below is the
 *     **client catalogue's** string. The companion — the server's own words absent
 *     from the document — is captured from the response body in the same test and
 *     never typed in, and it is asserted where the two sides genuinely differ:
 *     `detail` everywhere, and the per-field prose on the one refusal whose wording
 *     the mock and the catalogue do not share. `refusalsSeen` explains why `title`
 *     is deliberately not in that set.
 *
 * And one that is neither of the five: **`role="alert"` must mount *with* its text.**
 * A present-and-empty alert announces nothing in most screen readers, which is how a
 * console can carry a live region on every route and still be silent. `alertsSince`
 * records `textContent` **at insertion**, from a `MutationObserver` installed before
 * the page's own scripts. The inverse holds for `role="status"`, and `ui/table.tsx`
 * gets that one right — this repository contains one of each and they point opposite
 * ways, so neither rule may be generalised into the other.
 *
 * ── HOW EACH FAILURE IS DRIVEN ───────────────────────────────────────────────
 *
 * In descending order of honesty, and the reason is worth stating: a refusal reached
 * by the console's own controls proves the console can reach it, and one reached by
 * rewriting the request proves only that the console renders it when it arrives.
 *
 *   - **The console's own controls.** `controller-in-use`, `duplicate`,
 *     `validation`, `not-found`, `rotation-refused` and `session-end-failed` are all
 *     reached by pressing what an operator would press. Nothing is faked.
 *   - **`X-Foyer-Mock-Fault: internal`.** One accepted value, by design — see the
 *     header on `INJECTABLE_FAULTS` in `mock/src/server.ts`. It is the failure no
 *     honest request can produce, and the header is added by `page.route` to a real
 *     request the console made.
 *   - **`?nas=nas-unifi-3`.** Puts a simulated guest on the permanently unreachable
 *     access point, which is how `revokeVoucher`'s failure branch in
 *     `mock/src/state.ts` is reached without spending a fixture row that a re-run
 *     cannot restore. The call site says why the fixture that also reaches it is not
 *     the one used.
 *   - **Rewriting a request the console made.** Used three times, each noted at its
 *     call site with why the console cannot get there on its own. The response is
 *     still the mock's real refusal, produced by the mock's real branch.
 *   - **`route.abort()`.** The only way to reach `/problems/unreachable`, because it
 *     is the one type no server can send.
 *
 * ── WHAT IS NOT HERE ─────────────────────────────────────────────────────────
 *
 * The partition at the bottom (`every catalogue entry is driven or named`) reads the
 * enum out of `contract/openapi.yaml` rather than listing it, so a value added to the
 * contract turns this file red until it is either driven above or written into
 * `UNREACHABLE_FROM_THE_CONSOLE` with a reason.
 *
 * Most are driven here. Of those that are not: three are the guest portal's and no
 * admin operation can produce them; the authentication routes' refusals are driven in
 * `tests/admin-auth/`; and the rest each carry their own reason — a refusal the
 * console's own controls cannot select the input for, though it has somewhere to
 * render it, or a refusal this file's fixture world cannot reach. Each is written out
 * at its entry rather than summarised here.
 *
 * The reasons in that list are claims about the current code, so they go stale
 * exactly when the code improves — which is a good problem and still a problem.
 */

/* ── the catalogue, quoted ─────────────────────────────────────────────────── */

/**
 * The client's sentences, copied from `admin/src/i18n/core.ts` with the key beside
 * each one.
 *
 * Typed in on purpose, and it is the assertion rather than a shortcut: the whole
 * claim of the error architecture is that **the console owns this copy**. A test that read the
 * string from the same module the console renders it from would pass for any two
 * strings that happen to match, including the server's. The cost is that changing
 * the copy turns this file red — which is what a lock on copy ownership is for.
 */
const COPY = {
  /** `problem.internal.body` */
  internal:
    "The request reached the server and it failed on its own. Nothing was changed. Try again, and check the server log if it keeps happening.",
  /** `problem.unreachable.body` */
  unreachable:
    "The request did not reach the server, so nothing was changed. Check the connection and try again.",
  /** `problem.not-found.body` */
  notFound:
    "It is not there any more — it may have been deleted from another session. Reload to see what is there now.",
  /** `problem.validation.body` */
  validation: "Correct the fields marked below and try again.",
  /** `fault.duplicate` — and the server's prose for the same refusal differs. */
  faultDuplicate: "Already taken. Choose another.",
  /** `fault.not-a-hostname` */
  faultNotAHostname: "Enter a hostname, for example payments.example.com.",
  /** `problem.session-end-failed.title` */
  sessionEndFailedTitle: "Revoked — the guest is still online",
} as const;

/** `problem.controller-in-use.body`, which is a sentence about a number. */
const controllerInUse = (count: number): string =>
  `${count === 1 ? "1 access point came" : `${count} access points came`} from this controller and would stop being managed. Delete them first, or adopt them elsewhere.`;

/** `problem.reach.*` — the clause `rotation-refused` and `session-end-failed` build on. */
const reachCredentialsExpired = (name: string): string =>
  `The controller rejected Foyer's credentials for ${name}.`;
const reachUnreachable = (name: string): string => `${name} could not be reached.`;
const reachUnknown = (name: string): string => `${name} has never answered a probe.`;

/** `problem.controller-unreachable.body` */
const controllerUnreachable = (reach: string): string =>
  `${reach} Nothing was read, and nothing on this screen changed.`;

/** `problem.rotation-refused.body` */
const rotationRefused = (reach: string): string =>
  `${reach} A new secret would be generated here and never reach the device, which would take it off the network. The current secret still works.`;

/** `problem.session-end-failed.body` */
const sessionEndFailed = (reach: string): string =>
  `${reach} The code cannot be used again, but the session it is being used for was not ended: the guest stays online until it ends on its own.`;

/** `problem.garden-entry-required.body` */
const gardenEntryRequired = (value: string): string =>
  `${value} is what serves the sign-in page itself. Removing it would leave guests unable to load the portal, so it was kept.`;

/** `problem.too-large.body`, whose two figures are formatted by the console. */
const TOO_LARGE_SHAPE = / against a .+ limit\. Nothing was uploaded\.$/;

/**
 * The mock, asked directly, over IPv4.
 *
 * `MOCK_URL` spells the host `localhost`, and Node resolves that to `::1` first while
 * the mock listens on `0.0.0.0` — so an out-of-band ask from the test process fails
 * `ECONNREFUSED ::1:5170` intermittently, depending on which address the resolver
 * hands back. The browser is unaffected; it goes through the admin origin's proxy.
 * Pinned here rather than in `helpers/env.ts`, which other suites depend on.
 */
const MOCK = MOCK_URL.replace("//localhost:", "//127.0.0.1:");

/**
 * The toast host, which is **not** the only `aria-live` region on a list screen.
 *
 * `admin.toasts` is `[aria-live="polite"]`, and every table also carries a
 * `role="status"` row-count region with the same attribute — so the shared selector
 * is a strict-mode violation on any screen with a table under it. Narrowed here
 * rather than in the helper, because the helper's meaning is right for the screens
 * that use it.
 */
const TOASTS = `div${admin.toasts}`;

/* ── driving, and reading back what was really refused ─────────────────────── */

interface Refusal {
  url: string;
  status: number;
  type: string | null;
  /** The server's prose. **`ApiError` does not expose it and nothing may render it.** */
  detail: string | null;
  /** The server's per-field prose. The *keys* are load-bearing; the values are not. */
  errors: Record<string, string>;
  /** Rung two of the fallback, and legitimately renderable for an unknown type. */
  title: string | null;
}

/**
 * Records every problem document the page was answered with.
 *
 * This is what stops the file's expectations being a guess. Each test asserts that
 * the refusal it drove really carried the type it claims — so a mock that stops
 * emitting it fails here by name rather than by the page quietly rendering something
 * else — and it is where the "not the server's sentence" companions get the server's
 * sentence from, rather than typing it in.
 *
 * ── `title` is deliberately not checked for absence ───────────────────────────
 *
 * Asserting that *nothing the server wrote* reaches the document, `title` included,
 * raises three false alarms — *Access points still adopted*, *Not found*, *Required
 * by the portal* — because those are the **catalogue's** titles, which happen to say the same words as
 * the server's. They were written to agree, and `title` is rung two of the fallback
 * order precisely so a console meeting a newer type has something to render.
 *
 * So the discriminating checks are the two where the two sides genuinely differ:
 * `detail`, which `ApiError` does not expose at all, and the per-field prose, where
 * the server says *"That username already exists. Choose another."* and the console
 * says *"Already taken. Choose another."*
 */
function refusalsSeen(page: Page): Refusal[] {
  const seen: Refusal[] = [];
  page.on("response", (response) => {
    const url = response.url();
    if (!url.includes("/api/v0/") || response.ok()) return;
    void response
      .json()
      .then((body: unknown) => {
        const doc = body as {
          type?: string;
          title?: string;
          detail?: string;
          errors?: Record<string, string>;
        };
        seen.push({
          url,
          status: response.status(),
          type: doc.type ?? null,
          detail: doc.detail ?? null,
          errors: doc.errors ?? {},
          title: doc.title ?? null,
        });
      })
      .catch(() => undefined);
  });
  return seen;
}

/**
 * Every type a drive in this file actually put on the wire.
 *
 * Written by `only`, read by the partition at the bottom. It is what stops `DRIVEN`
 * being a list somebody could keep up to date by hand: a name left in that list after
 * its test stopped producing the type is a claim of coverage with nothing under it,
 * which is the exact shape this file exists to refuse.
 */
const observed = new Set<string>();

/**
 * Records a type as driven — and it is a function rather than `observed.add` at the
 * call sites so that the two types which cannot come through `only` have to say so.
 *
 * `/problems/unreachable` has **no response**; that is the whole of what makes it
 * client-synthesised. `/problems/session-end-failed` travels in the body of a `200`,
 * which is what `Failure` is in the contract for — without it a session-end failure
 * goes unseen inside a success response, because guards that walk problem documents
 * never look there. Neither is a refusal `refusalsSeen` can see, and both are asserted from
 * their own evidence at the call site before this is reached.
 */
function drove(type: string): void {
  observed.add(type);
}

/** The one refusal a test drove, found by type. Fails loudly when it never arrived. */
async function only(seen: Refusal[], type: string): Promise<Refusal> {
  /* Polled, because the body is read asynchronously off the response. A synchronous
     read would make the whole check racy in exactly the direction that hides a
     failure: an empty list reads as "no such refusal" either way. */
  await expect
    .poll(() => seen.filter((r) => r.type === type).length, {
      message:
        `nothing on the wire carried ${type}. Every expectation in this test would ` +
        `then be asserting a sentence that arrived from somewhere else — or from nowhere.`,
    })
    .toBeGreaterThan(0);
  drove(type);
  const matches = seen.filter((r) => r.type === type);
  return matches[matches.length - 1]!;
}

/**
 * Adds the mock's one injectable fault to a request the console really made.
 *
 * A header on a genuine request rather than a fabricated response, which is the
 * difference between "the server failed" and "a test drew a failure". The mock
 * accepts exactly one value by design — see the header on `INJECTABLE_FAULTS` — so
 * this cannot become a way to conjure any type on demand.
 */
async function faultInternal(page: Page, glob: string) {
  await page.route(glob, async (route: Route) => {
    const request = route.request();
    await route.continue({
      headers: { ...request.headers(), "x-foyer-mock-fault": "internal" },
    });
  });
}

/** Everything the operator can read, as one string. */
async function documentText(page: Page): Promise<string> {
  return page.evaluate(() => document.body.innerText);
}

/**
 * The server's `detail` did not reach an operator — asserted in **both** directions.
 *
 * `expectDetail` is what stops this vacating itself. A conforming server omits
 * `detail` once its clients implement the type, and `mock/src/server.test.ts` asserts
 * the mock sends none — so for every refusal here except one, "absent from the DOM"
 * is trivially true and the interesting claim is that the server sent none at all.
 * The exception is the mock's crash path, which is the one place a sentence survives
 * in that server; there `expectDetail` is `true`, and the absence check has something
 * real to look for.
 *
 * Taken from the capture rather than from a constant, so a mock that changes its
 * wording cannot make this pass by accident.
 */
async function serverDetailAbsent(page: Page, refusal: Refusal, expectDetail: boolean) {
  expect(
    Boolean(refusal.detail),
    expectDetail
      ? "the server sent no `detail`, so the check that it is not rendered has nothing " +
        "to look for — see `server.test.ts`, which asserts the same thing in the " +
        "other direction"
      : "this server is asserted to send no `detail` on a product answer; one appeared",
  ).toBe(expectDetail);
  if (!refusal.detail) return;
  const text = await documentText(page);
  expect(
    text,
    `the server's own sentence reached an operator: ${refusal.detail}`,
  ).not.toContain(refusal.detail);
}

/* ── `role="alert"` mounts with its text ───────────────────────────────────── */

interface RecordedAlert {
  text: string;
  html: string;
}

declare global {
  interface Window {
    __foyerAlerts?: RecordedAlert[];
  }
}

/**
 * Installs a `MutationObserver` before any page script runs, recording every
 * `role="alert"` **and the text it carried at the moment it entered the document**.
 *
 * The distinction is the whole point and cannot be measured after the fact: a node
 * that is inserted empty and filled a tick later looks identical to a correct one by
 * the time a locator reads it, and announces nothing.
 */
async function watchAlerts(page: Page) {
  await page.addInitScript(() => {
    window.__foyerAlerts = [];
    const record = (el: Element) => {
      window.__foyerAlerts?.push({
        text: (el.textContent ?? "").trim(),
        html: el.outerHTML.slice(0, 120),
      });
    };
    const scan = (node: Node) => {
      if (!(node instanceof Element)) return;
      if (node.getAttribute("role") === "alert") record(node);
      for (const nested of node.querySelectorAll('[role="alert"]')) record(nested);
    };
    new MutationObserver((mutations) => {
      for (const mutation of mutations) for (const node of mutation.addedNodes) scan(node);
    }).observe(document, { childList: true, subtree: true });
  });
}

/** Forgets what mounted during the page load, so a test measures only its own drive. */
async function forgetAlerts(page: Page) {
  await page.evaluate(() => {
    window.__foyerAlerts = [];
  });
}

async function alertsSince(page: Page): Promise<RecordedAlert[]> {
  return page.evaluate(() => window.__foyerAlerts ?? []);
}

/* ══ the five surfaces ══════════════════════════════════════════════════════════ */

test.describe("a refusal is the console's own sentence, and it got there by failing", () => {
  test("a 500 on a query says what the console says, and none of what the server said", async ({
    page,
  }) => {
    const seen = refusalsSeen(page);

    /* The healthy render first, in the same run. Two measurements rather than a
       measurement and a memory — and it is what says the sentence below arrived from
       the drive rather than being on the page all along. */
    await page.goto("/operators");
    await tableReady(page);
    expect(
      await documentText(page),
      "the failure sentence is on a healthy screen, so its presence after the drive " +
        "would prove nothing",
    ).not.toContain(COPY.internal);

    await faultInternal(page, "**/api/v0/operators**");
    /* A filter changes the query key on a screen that is already mounted, which is
       the smallest thing that puts a fresh request in front of the injected fault.
       Navigating would measure the lazy chunk loading instead. */
    await page.locator(admin.search).fill("nguyen");

    await expect(page.locator(`${admin.tbody} .empty-row`)).toContainText(COPY.internal);

    const refusal = await only(seen, "/problems/internal");
    expect(refusal.status).toBe(500);
    /* The injected 500 stands in for a product answer and must have a product
       answer's shape — no `detail`. `server.test.ts` asserts the same thing from the
       other side; this is that contract holding where an operator can see it. */
    await serverDetailAbsent(page, refusal, false);
  });

  test("a 500 on a mutation renders the catalogue, and the server's detail reaches nobody", async ({
    page,
  }) => {
    const seen = refusalsSeen(page);
    await watchAlerts(page);

    await page.goto("/portal");
    await expect(page.locator(".page-title")).toBeVisible();

    /*
     * The mock's own crash path, which is the **one** place a `detail` survives in
     * this server — `server.test.ts` asserts that in both directions. Reaching it
     * needs a body the handler cannot parse, and the console will never send one, so
     * the request the console made is rewritten on the way out. The refusal is still
     * the mock's real `onError`, with a real sentence in it, which is the only reason
     * this test can prove anything about `detail` at all.
     */
    await page.route("**/api/v0/sites/*/portal", async (route: Route) => {
      if (route.request().method() !== "PATCH") return route.fallback();
      await route.continue({ postData: "{not json" });
    });

    const venue = page.locator("#venue-name");
    await expect(venue).toBeVisible();
    await venue.fill("Quán Cà Phê Số 7");
    await forgetAlerts(page);
    await page.getByRole("button", { name: "Save changes" }).click();

    const notice = page.locator('.notice[role="alert"]');
    await expect(notice).toContainText(COPY.internal);
    /* The screen names the request and the type names the reason; neither can say
       both, so the heading is the screen's and the body is the catalogue's. */
    await expect(notice).toContainText("Changes not saved");

    const refusal = await only(seen, "/problems/internal");
    expect(refusal.status).toBe(500);
    /*
     * `true` — the one refusal in this file that really carries a sentence, so the
     * absence check has something to look for. With the injection removed and the
     * sentence pasted into the page, it fails with *"the server's own sentence reached
     * an operator: SyntaxError: Expected property name or '}' in JSON at position 1"*.
     * Without `expectDetail` this assertion would go quiet the day the mock stopped
     * sending it, which is the shape it is written to avoid.
     */
    await serverDetailAbsent(page, refusal, true);

    const alerts = await alertsSince(page);
    expect(alerts.length, "the failure mounted no alert at all").toBeGreaterThan(0);
    for (const alert of alerts) {
      expect(alert.text, `a role="alert" entered the document empty: ${alert.html}`).not.toBe("");
    }
  });

  test("a refusal inside an open dialog is the refusal's own sentence, not the dialog surviving", async ({
    page,
    request,
  }) => {
    /*
     * How many access points the controller really has, asked of the mock rather
     * than typed in — the sentence is *about the number*, so a hardcoded one would
     * pass while the console interpolated a different figure from `params`.
     */
    const listed = await request.get(`${MOCK}/api/v0/nas?siteId=site-hcm&limit=100`);
    const nas = (await listed.json()) as { items: { controllerId: string | null }[] };
    const controllers = (await (
      await request.get(`${MOCK}/api/v0/controllers?siteId=site-hcm`)
    ).json()) as { id: string; name: string }[];

    /* Whichever controller actually has access points behind it, and how many. Both
       halves are asked rather than assumed: an id that stopped being adopted would
       make the refusal never fire, and a hardcoded count would pass while the console
       interpolated a different figure from `params`. */
    const holding = controllers
      .map((c) => ({ ...c, adopted: nas.items.filter((n) => n.controllerId === c.id).length }))
      .find((c) => c.adopted > 0);
    expect(holding, "no controller has an adopted access point, so no delete is refused").
      toBeTruthy();

    const seen = refusalsSeen(page);
    await watchAlerts(page);
    await page.goto("/controllers");
    await tableReady(page);

    const row = page.locator(admin.rows, { hasText: holding!.name }).first();
    await row.getByRole("button", { name: "Delete" }).click();

    const dialog = page.locator("dialog[open]");
    await expect(dialog).toBeVisible();
    await forgetAlerts(page);
    await dialog.getByRole("button", { name: "Forget controller" }).click();

    /*
     * The assertion is the sentence, inside the dialog. "The dialog stayed open" is
     * byte-identically true of the code this was written against — that was the
     * measurement: a `500` on `DELETE /garden/{id}` left the dialog unchanged, with
     * no error text anywhere and focus on `<body>`.
     */
    await expect(dialog.locator('.notice[role="alert"]')).toContainText(
      controllerInUse(holding!.adopted),
    );

    const refusal = await only(seen, "/problems/controller-in-use");
    expect(refusal.status).toBe(409);
    await serverDetailAbsent(page, refusal, false);

    const alerts = await alertsSince(page);
    expect(alerts.length).toBeGreaterThan(0);
    for (const alert of alerts) {
      expect(alert.text, `a role="alert" entered the document empty: ${alert.html}`).not.toBe("");
    }

    await page.keyboard.press("Escape");
  });

  test("a refused field marks the input and names the fault where the input points", async ({
    page,
    request,
  }) => {
    /* An existing username, asked of the mock. A hardcoded one that had been renamed
       would make the refusal never fire and every assertion below unreachable. */
    const listed = await request.get(`${MOCK}/api/v0/accounts?siteId=site-hcm&limit=1`);
    const accounts = (await listed.json()) as { items: { username: string }[] };
    const taken = accounts.items[0]?.username;
    expect(taken, "no account exists to collide with").toBeTruthy();

    const seen = refusalsSeen(page);
    await watchAlerts(page);
    await page.goto("/accounts");
    await tableReady(page);
    await page.getByRole("button", { name: "Add account" }).click();

    const dialog = page.locator("dialog[open]");
    await dialog.locator("#username").fill(taken!);
    await dialog.locator("#password").fill("a-long-enough-password");
    await forgetAlerts(page);
    await dialog.getByRole("button", { name: "Create account" }).click();

    await expect(dialog.locator("#username-error")).toHaveText(COPY.faultDuplicate);

    /* The pair, judged by the shared predicate — see `fieldPairFaults`. */
    const pair = await fieldPair(page, "dialog[open] #username");
    expect(fieldPairFaults(pair, COPY.faultDuplicate)).toEqual([]);

    /*
     * The same defect, put back into the page, so the predicate above is shown to be
     * capable of going red on this run. This is the shape of an unpaired field error:
     * a mark on the input, an error beside it, and nothing joining them.
     */
    await page.evaluate(() => {
      const host = document.createElement("div");
      host.innerHTML =
        '<input id="sabotage-input" aria-invalid="true">' +
        '<p class="field-error">Already taken. Choose another.</p>';
      document.body.append(host);
    });
    const sabotaged = await fieldPair(page, "#sabotage-input");
    expect(
      fieldPairFaults(sabotaged, COPY.faultDuplicate),
      "the broken shape — `aria-invalid` with no `aria-describedby` — satisfied the " +
        "predicate, which means the predicate is not checking the pair",
    ).not.toEqual([]);

    const refusal = await only(seen, "/problems/validation");
    expect(refusal.status).toBe(422);
    await serverDetailAbsent(page, refusal, false);

    /*
     * The half that makes this a copy-ownership lock rather than a spelling test.
     *
     * This refusal is the one place in the console where the two sides genuinely
     * disagree: the server's prose for the fault is *"That username already exists.
     * Choose another."* and the catalogue's is *"Already taken. Choose another."* —
     * so this is the only configuration in which "assert the client's string" can be
     * told apart from "assert any string that happens to be there". Everywhere else
     * the mock's `errors` were written to say what the catalogue says, and a blanket
     * absence check would be asserting that the console renders nothing at all.
     *
     * Both directions, and the first is what stops the second vacating: the server
     * really did send prose, and it really did differ.
     */
    const prose = refusal.errors["username"];
    expect(prose, "the server sent no prose for `username`, so nothing distinguishes the two")
      .toBeTruthy();
    expect(prose, "the server's prose and the catalogue's now agree, so this proves nothing")
      .not.toBe(COPY.faultDuplicate);

    const text = await documentText(page);
    expect(text, `the server's own sentence reached an operator: ${prose!}`).not.toContain(prose!);
    expect(text, "the page-level heading for a validation refusal is missing").toContain(
      COPY.validation,
    );

    const alerts = await alertsSince(page);
    expect(alerts.length).toBeGreaterThan(0);
    for (const alert of alerts) {
      expect(alert.text, `a role="alert" entered the document empty: ${alert.html}`).not.toBe("");
    }

    await page.keyboard.press("Escape");
  });

  test("the unreachable refusal is the client's own, and nothing answered to produce it", async ({
    page,
  }) => {
    await page.goto("/garden");
    await tableReady(page);
    expect(await documentText(page)).not.toContain(COPY.unreachable);

    /*
     * `route.abort` rather than an error status, because `/problems/unreachable` is
     * the one value marked `x-client-synthesised` in the contract: `fetch` rejects
     * with a bare `TypeError`, no response exists, and `api.ts` is the only thing
     * that can turn that into a type the catalogue has copy for.
     */
    /* The *write*, and only the write. The screen refetches the garden around this,
       and those reads answer perfectly well — counting them would make the assertion
       below red on a healthy console and tell nobody anything. */
    const answered: string[] = [];
    page.on("response", (r) => {
      if (r.url().includes("/walled-garden") && r.request().method() === "POST") {
        answered.push(`${r.request().method()} ${r.url()} → ${r.status()}`);
      }
    });
    await page.route("**/api/v0/sites/*/walled-garden", async (route: Route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await route.abort("failed");
    });

    await page.getByRole("button", { name: "Allow a destination" }).click();
    const dialog = page.locator("dialog[open]");
    await dialog.locator("#garden-value").fill("payments.example.com");
    await dialog.getByRole("button", { name: "Allow destination" }).click();

    const notice = dialog.locator('.notice[role="alert"]');
    await expect(notice).toContainText(COPY.unreachable);
    await expect(notice).toContainText("Destination not added");

    /*
     * The half that makes this about synthesis rather than about copy.
     *
     * A server *can* be made to send `/problems/unreachable` — the value is marked
     * `x-client-synthesised` in the contract by convention, not by enforcement — and
     * a lock that only read the sentence would be green for a console that had simply
     * been handed the type. With `route.fulfill` answering a `503` carrying that
     * type, the sentence appears, and this assertion is what goes red.
     */
    expect(
      answered,
      "a response came back, so whatever is on screen was not synthesised by the client",
    ).toEqual([]);
    /* Not through `only`: there is no response to read a type off, which is exactly
       what has just been asserted. The two lines above are this drive's evidence. */
    drove("/problems/unreachable");

    await page.keyboard.press("Escape");
  });
});

/* ══ the rest of the catalogue, one drive per entry ════════════════════════════ */

test.describe("every refusal the console can reach says the catalogue's sentence", () => {
  test("/problems/not-found — a record that is gone reads as gone", async ({ page }) => {
    const seen = refusalsSeen(page);
    await page.goto("/access-points/nas-does-not-exist");
    await expect(page.locator('.notice[role="alert"]')).toContainText(COPY.notFound);
    /*
     * `isNotFound` keys on the type and not on `status === 404`, which is the whole
     * reason the type exists here: a surface that keyed on the status had to answer
     * `false` for every other failure, and that is how a `500` came to render *"No
     * session selected."* with a row selected.
     */
    const refusal = await only(seen, "/problems/not-found");
    expect(refusal.status).toBe(404);
    await serverDetailAbsent(page, refusal, false);
  });

  test("/problems/duplicate — the value that is already there is named", async ({
    page,
    request,
  }) => {
    const garden = await request.get(`${MOCK}/api/v0/sites/site-hcm/walled-garden`);
    const entries = (await garden.json()) as {
      entries: { origin: string; kind: string; value: string }[];
    };
    /*
     * `kind: "fqdn"`, and it is not incidental. The dialog opens on that kind, and the
     * first operator entry in this world is an IP range — typing `104.16.0.0/13` into
     * a hostname field is refused `not-a-hostname` **before** the duplicate check runs,
     * so a test that typed one would drive a different refusal than the one it names.
     */
    const existing = entries.entries.find((e) => e.origin === "operator" && e.kind === "fqdn")
      ?.value;
    expect(existing, "no operator FQDN entry to collide with").toBeTruthy();

    const seen = refusalsSeen(page);
    await page.goto("/garden");
    await tableReady(page);
    await page.getByRole("button", { name: "Allow a destination" }).click();
    const dialog = page.locator("dialog[open]");
    await dialog.locator("#garden-value").fill(existing!);
    await dialog.getByRole("button", { name: "Allow destination" }).click();

    await expect(dialog.locator('.notice[role="alert"]')).toContainText(
      `${existing} is already there. Nothing was added.`,
    );
    const refusal = await only(seen, "/problems/duplicate");
    expect(refusal.status).toBe(409);
    await serverDetailAbsent(page, refusal, false);
    await page.keyboard.press("Escape");
  });

  test("/problems/validation — one field, three ways to fail, and the right sentence", async ({
    page,
  }) => {
    const seen = refusalsSeen(page);
    await watchAlerts(page);
    await page.goto("/garden");
    await tableReady(page);
    await page.getByRole("button", { name: "Allow a destination" }).click();
    const dialog = page.locator("dialog[open]");
    /* `value` is the field `FieldFault` was added for: `required`, `not-a-hostname`
       and `not-a-cidr` all arrive as `(/problems/validation, value)`, and a catalogue
       keyed on the pair would render one of them for all three. */
    await dialog.locator("#garden-value").fill("not a hostname at all");
    await forgetAlerts(page);
    await dialog.getByRole("button", { name: "Allow destination" }).click();

    await expect(dialog.locator("#garden-value-error")).toHaveText(COPY.faultNotAHostname);
    const pair = await fieldPair(page, "dialog[open] #garden-value");
    expect(fieldPairFaults(pair, COPY.faultNotAHostname)).toEqual([]);

    const refusal = await only(seen, "/problems/validation");
    expect(refusal.status).toBe(422);

    const alerts = await alertsSince(page);
    expect(alerts.length).toBeGreaterThan(0);
    for (const alert of alerts) {
      expect(alert.text, `a role="alert" entered the document empty: ${alert.html}`).not.toBe("");
    }
    await page.keyboard.press("Escape");
  });

  test("/problems/rotation-refused — the reachability clause comes from params", async ({
    page,
    request,
  }) => {
    /*
     * Asked of the mock, because the refusal only exists for the one combination with
     * no way back: the driver cannot push a secret **and** nothing can currently reach
     * the device to be told it by hand. `rotationRefused` in `mock/src/server.ts` is
     * exported for exactly this question.
     */
    const listed = await request.get(`${MOCK}/api/v0/nas?siteId=site-hcm&limit=100`);
    const nas = (await listed.json()) as {
      items: { id: string; name: string; reachability: string; capabilities: { rotateSecret: boolean } }[];
    };
    const refused = nas.items.find((n) => !n.capabilities.rotateSecret && n.reachability !== "ok");
    expect(refused, "no fixture satisfies the refusal, so this branch never runs").toBeTruthy();

    const seen = refusalsSeen(page);
    await page.goto(`/access-points/${refused!.id}`);
    /* Scoped to `main`: `ConfirmDialog` is mounted always with `open` toggled, so the
       confirmation's own button carries the same accessible name. A closed `<dialog>`
       is `display: none` and role queries skip it, but relying on that would make the
       two indistinguishable the day it changes. */
    await page.locator("main").getByRole("button", { name: "Generate new secret" }).click();

    const dialog = page.locator("dialog[open]");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Generate new secret" }).click();

    const clause =
      refused!.reachability === "credentials-expired"
        ? reachCredentialsExpired(refused!.name)
        : reachUnreachable(refused!.name);
    /*
     * The clause is the assertion. Three reachability values are three different
     * afternoons — a network problem, a controller problem, and a device that has
     * never answered — which is why the mock sends `nasReachability` beside the name
     * rather than a sentence containing both, and why a test that only checked the
     * tail of the sentence would pass for all three.
     */
    await expect(dialog.locator('.notice[role="alert"]')).toHaveText(
      new RegExp(escapeForRegExp(rotationRefused(clause))),
    );

    const refusal = await only(seen, "/problems/rotation-refused");
    expect(refusal.status).toBe(409);
    await serverDetailAbsent(page, refusal, false);
    await page.keyboard.press("Escape");
  });

  /**
   * ── WHY THIS REFUSAL IS REACHABLE AT ALL ──────────────────────────────────
   *
   * `ProbeDialog`'s `reachable` falls back to the controller's stored reachability
   * rather than requiring a probe run **since this dialog opened**, and the refusal's
   * `ErrorNotice` sits outside the probe-result branch. If either were otherwise, every
   * path to *Discover access points* would run through a probe that disables the
   * button first, and this type would belong in `UNREACHABLE_FROM_THE_CONSOLE`.
   *
   * So the type has a screen it can reach and a site that renders it. The first
   * assertion below is what makes the rest possible — the control is offered on a
   * dialog that has probed nothing.
   *
   * ── WHY THE REQUEST IS RE-POINTED ─────────────────────────────────────────
   *
   * Discovery against a reachable controller **succeeds**, and succeeding writes: it
   * adopts access points and mints a controller-level secret, in a mock the whole
   * suite shares. The refusal exists for the gap between the two — the probe said
   * `ok`, and by the time the operator pressed Discover the controller had gone — and
   * that gap is a race no test can hold open. So the request the console really made
   * is re-pointed at a controller the mock already knows is not reachable, which is
   * the same device used three other times in this file and noted each time. The
   * mock's branch, the mock's params, the console's rendering, nothing invented — and
   * the refusal returns before that handler writes anything at all.
   */
  test("/problems/controller-unreachable — discovery refused, on a control the dialog offers", async ({
    page,
    request,
  }) => {
    const listed = await request.get(`${MOCK}/api/v0/controllers?siteId=site-hcm`);
    const controllers = (await listed.json()) as {
      id: string;
      name: string;
      reachability: string;
      vendorSites: { boundSiteId?: string | null }[];
    }[];
    /* Both ends asked rather than assumed: a fixture where every controller answers,
       or where none does, would make this test drive nothing while still passing the
       shape of its assertions.

       **And the reachable one has to be adopted**, which is not fussiness about
       tidiness. The row's first control carries one of two labels, and a
       controller that answers and is adopted by nobody says *Attach to this venue*
       rather than *Probe* — same dialog, different sentence, because that is the
       step the operator needs. `ctl-unifi-unattached` is such a row and it is in
       this list. Without this clause the `getByRole("button", { name: "Probe" })`
       below is coupled to the order `buildControllers` happens to return, and moving
       one fixture row would fail this test with a locator timeout that says nothing
       about error architecture. */
    const reachable = controllers.find(
      (c) => c.reachability === "ok" && c.vendorSites.some((v) => v.boundSiteId),
    );
    const down = controllers.find((c) => c.reachability !== "ok");
    expect(
      reachable,
      "no controller both answers and is adopted into this venue, so no row offers a control " +
        "labelled Probe and the Discover dialog is never opened",
    ).toBeTruthy();
    expect(down, "every controller answers, so the refusal has no subject").toBeTruthy();

    const seen = refusalsSeen(page);
    await page.goto("/controllers");
    await tableReady(page);

    const row = page.locator(admin.rows, { hasText: reachable!.name }).first();
    await row.getByRole("button", { name: "Probe" }).click();

    const dialog = page.locator("dialog[open]");
    await expect(dialog).toBeVisible();

    /*
     * **Nothing has been probed in this dialog**, and the control is live anyway.
     * A `disabled` button here would make the operator re-run a probe to be allowed to
     * ask a question the table one row away had already answered — and would make
     * everything below unreachable.
     */
    const discover = dialog.getByRole("button", { name: "Discover access points" });
    await expect(
      discover,
      "the Discover control is disabled on a dialog that has probed nothing, so the " +
        "refusal below cannot be reached from the screen and this entry belongs back " +
        "in `UNREACHABLE_FROM_THE_CONSOLE`.",
    ).toBeEnabled();

    await page.route("**/api/v0/controllers/*/discover", async (route: Route) => {
      if (route.request().method() !== "POST") return route.fallback();
      const url = route.request().url().replace(/[^/]+\/discover$/, `${down!.id}/discover`);
      await route.continue({ url });
    });

    await discover.click();

    const clause =
      down!.reachability === "credentials-expired"
        ? reachCredentialsExpired(down!.name)
        : down!.reachability === "unreachable"
          ? reachUnreachable(down!.name)
          : reachUnknown(down!.name);
    /* The clause, for the same reason `rotation-refused` above asserts it: three
       reachability values are three different afternoons and three different next
       steps, and a test that read only the tail would pass for all of them. */
    await expect(dialog.locator('.notice[role="alert"]')).toHaveText(
      new RegExp(escapeForRegExp(controllerUnreachable(clause))),
    );

    const refusal = await only(seen, "/problems/controller-unreachable");
    expect(refusal.status).toBe(409);
    await serverDetailAbsent(page, refusal, false);
    await page.keyboard.press("Escape");
  });

  test("/problems/garden-entry-required — the entry that serves the portal is kept", async ({
    page,
    request,
  }) => {
    const garden = await request.get(`${MOCK}/api/v0/sites/site-hcm/walled-garden`);
    const entries = (await garden.json()) as {
      entries: { id: string; origin: string; value: string }[];
    };
    const required = entries.entries.find((e) => e.origin === "required");
    const operator = entries.entries.find((e) => e.origin === "operator");
    expect(required, "no required entry exists").toBeTruthy();
    expect(operator, "no deletable entry exists").toBeTruthy();

    const seen = refusalsSeen(page);
    await page.goto("/garden");
    await tableReady(page);

    /*
     * **The console cannot reach this refusal on its own, and that is correct.** It
     * renders no delete control for a required entry at all; the server's check is
     * the second gate, for a client that tries anyway. So the request the console
     * made is re-pointed at the required entry — the mock's branch, the mock's
     * params, the console's rendering, and nothing invented.
     */
    await page.route("**/api/v0/sites/*/walled-garden/*", async (route: Route) => {
      if (route.request().method() !== "DELETE") return route.fallback();
      const url = route.request().url().replace(/[^/]+$/, required!.id);
      await route.continue({ url });
    });

    const row = page.locator(admin.rows, { hasText: operator!.value }).first();
    await row.getByRole("button", { name: "Delete" }).click();
    const dialog = page.locator("dialog[open]");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: "Stop allowing it" }).click();

    await expect(dialog.locator('.notice[role="alert"]')).toContainText(
      gardenEntryRequired(required!.value),
    );
    const refusal = await only(seen, "/problems/garden-entry-required");
    expect(refusal.status).toBe(409);
    await serverDetailAbsent(page, refusal, false);
    await page.keyboard.press("Escape");
  });

  test("/problems/too-large — the console's two figures, never the server's sentence", async ({
    page,
  }) => {
    const seen = refusalsSeen(page);
    await page.goto("/portal");
    await expect(page.locator(".page-title")).toBeVisible();

    /*
     * **The console's own gate normally shadows this refusal**, and that is worth
     * leaving written down rather than working around silently: `ImageField`
     * compares `file.size` against the same constant the server uses and refuses
     * locally with its own copy, so the server's `413` branch cannot be reached by an
     * operator. The upload the console really made is enlarged on the way out, so the
     * mock's real branch produces a real `too-large` with real figures.
     */
    /*
     * Over the ceiling **the server reports**, not over a figure typed here. This was
     * a literal 200 kB, written when the ceiling was 100 kB; the ceiling was raised to
     * 5 MB and the "refusal" became a successful upload of 200 kB of
     * garbage — which then sat in the fixture world as the café's photograph.
     */
    const settings = await page.request.get("/api/v0/sites/site-hcm/portal");
    const ceiling = ((await settings.json()) as { budgets: { imageMaxBytes: number } }).budgets.imageMaxBytes;
    await page.route("**/api/v0/sites/*/portal/hero-image", async (route: Route) => {
      if (route.request().method() !== "PUT") return route.fallback();
      const body = JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>;
      body.dataBase64 = Buffer.alloc(ceiling + 1024, 7).toString("base64");
      await route.continue({ postData: JSON.stringify(body) });
    });

    /* A one-pixel PNG. Small enough to pass the client gate, so the request is made
       at all — the size that matters is the one the route handler substitutes. */
    const png = Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
      "base64",
    );
    const chooser = page.locator('input[type="file"]').last();
    await chooser.setInputFiles({ name: "hero.png", mimeType: "image/png", buffer: png });

    const alert = page.locator('.field-error[role="alert"]').last();
    await expect(alert).toHaveText(TOO_LARGE_SHAPE);

    const refusal = await only(seen, "/problems/too-large");
    expect(refusal.status).toBe(413);
    await serverDetailAbsent(page, refusal, false);
  });

  test("/problems/session-end-failed — revoked, and the guest is still online", async ({
    page,
    request,
  }) => {
    /*
     * The one branch in `mock/src/state.ts` that no ordinary request reaches: every
     * simulated guest lands on `nas-unifi-1`, which is reachable, so the driver call
     * that ends the session always succeeds. `?nas=nas-unifi-3` puts the guest on the
     * permanently unreachable access point instead — the review affordance the mock
     * grew for precisely this, and the reason its copy could never be looked at.
     *
     * **`sess-stranded-unreachable` reaches the same branch from a cold mock**, and it
     * is deliberately not used here. Its voucher is a single fixture row: revoking it
     * leaves the voucher `revoked`, the console renders no Revoke control for a
     * revoked row, and the second run of this suite against the same mock process
     * would find nothing to press. Redeeming a fresh code costs one voucher out of a
     * batch that has hundreds and leaves the fixture the design was reviewed against
     * intact — which is the rule the ugly fixtures are kept under.
     */
    const unused = await request.get(
      `${MOCK}/api/v0/vouchers?siteId=site-hcm&state=unused&limit=1`,
    );
    const vouchers = (await unused.json()) as { items: { code: string; batchId: string }[] };
    const voucher = vouchers.items[0];
    expect(voucher, "no unused voucher left to redeem").toBeTruthy();
    const code = voucher!.code;

    const context = await request.get(`${MOCK}/api/v0/portal/context?nas=nas-unifi-3`);
    expect(
      context.headers()["x-foyer-mock-nas"],
      "the mock fell back to the default access point, so the guest is on a reachable " +
        "one and the failure branch will not run",
    ).toBe("nas-unifi-3");

    const redeemed = await request.post(`${MOCK}/api/v0/portal/voucher`, {
      form: { code, lang: "en" },
      maxRedirects: 0,
    });
    expect(redeemed.status(), "the redemption did not take").toBe(303);
    expect(redeemed.headers()["location"]).toContain("/connected/");

    const nasName = await request
      .get(`${MOCK}/api/v0/nas/nas-unifi-3`)
      .then((r) => r.json())
      .then((n: { name: string }) => n.name);

    /*
     * The failure this test names travels in the body of a **`200`**, so
     * `refusalsSeen` cannot see it — it skips `response.ok()`, which is right for
     * every other case here. `Failure` exists as a schema for exactly this reason:
     * an earlier fix for this failure missed it because both of its guards walk
     * problem documents and this one rode inside a success response. So the wire
     * evidence for this drive is captured on its own.
     */
    const revoked: string[] = [];
    page.on("response", (r) => {
      if (!r.url().includes("/revoke")) return;
      void r
        .json()
        .then((body: unknown) => {
          const type = (body as { failure?: { type?: string } }).failure?.type;
          if (type) revoked.push(type);
        })
        .catch(() => undefined);
    });

    /* The codes live on the batch, not on `/vouchers` — that route is the batch list. */
    await page.goto(`/vouchers/${voucher!.batchId}`);
    await tableReady(page);
    await page.locator(admin.search).fill(code);
    const row = page.locator(admin.rows).first();
    await expect(row).toBeVisible();
    await row.getByRole("button", { name: "Revoke" }).click();

    /*
     * A live region's **text**, not its existence. The Toast host is `aria-live` and
     * is mounted on every load of this screen, so `toHaveCount(1)` is green before
     * anything is revoked at all.
     *
     * Three outcomes, not two, and this is the one that matters: the code is dead and
     * the guest is still on the network. Collapsing it into *"Voucher revoked."* would
     * be a confident answer about a different outcome, so both halves are asserted.
     */
    const toasts = page.locator(TOASTS);
    await expect(toasts).toContainText(COPY.sessionEndFailedTitle);
    await expect(toasts).toContainText(sessionEndFailed(reachUnreachable(nasName)));
    await expect(toasts).not.toContainText("Voucher revoked and the live session ended.");

    /* And the sentence really came from the type, not from the screen guessing. */
    expect(revoked, "the `200` carried no `failure`, so the sentence above is unexplained")
      .toContain("/problems/session-end-failed");
    drove("/problems/session-end-failed");
  });

  test("/problems/session-not-live — the session ended while the confirmation was open", async ({
    page,
    request,
  }) => {
    /*
     * The console hides *Disconnect guest* on a session that has ended, so the only
     * way an operator meets this refusal is the race it exists for: the dock was
     * opened on a live session, the guest's session ended — somebody else pressed the
     * same button, or the NAS sent its stop — and then this operator confirmed.
     *
     * Driven exactly that way, on a session this test makes for itself: a fresh
     * voucher redeemed through the portal puts a new guest on the default access
     * point, so no fixture session is spent and a second run finds another voucher.
     */
    const unused = await request.get(`${MOCK}/api/v0/vouchers?siteId=site-hcm&state=unused&limit=1`);
    const voucher = ((await unused.json()) as { items: { id: string; code: string }[] }).items[0];
    expect(voucher, "no unused voucher left to redeem").toBeTruthy();
    await request.get(`${MOCK}/api/v0/portal/context`);
    const redeemed = await request.post(`${MOCK}/api/v0/portal/voucher`, {
      form: { code: voucher!.code, lang: "en" },
      maxRedirects: 0,
    });
    expect(redeemed.status(), "the redemption did not take").toBe(303);
    const sessions = await request.get(`${MOCK}/api/v0/sessions?siteId=site-hcm&limit=200`);
    const session = ((await sessions.json()) as { items: { id: string; voucherId?: string | null }[] }).items.find(
      (x) => x.voucherId === voucher!.id,
    );
    expect(session, "the redemption made no session to disconnect").toBeTruthy();

    const seen = refusalsSeen(page);
    await page.goto(`/sessions?session=${encodeURIComponent(session!.id)}`);
    await tableReady(page);
    await page.locator(`${admin.detailDock} [data-role="disconnect"] button`).click();
    const dialog = page.locator("dialog[open]");
    await expect(dialog).toBeVisible();

    /* The race: the session ends underneath the open confirmation. */
    const ended = await request.post(`${MOCK}/api/v0/sessions/${session!.id}/disconnect`, { data: {} });
    expect(ended.status()).toBe(200);

    await dialog.locator(".dialog-foot button.btn-danger").click();
    await expect(dialog.locator('.notice[role="alert"]')).toContainText(
      "This session has already ended. Nothing was changed.",
    );
    const refusal = await only(seen, "/problems/session-not-live");
    expect(refusal.status).toBe(409);
    await serverDetailAbsent(page, refusal, false);
    await page.keyboard.press("Escape");
  });

  /*
   * **Driven, not excused.** A drive here does not hold the shared administrator's
   * bucket shut for every spec running beside it: the budget is per
   * USERNAME, split again by trusted-device cookie, and there is no address bucket on
   * console sign-in (`web/mock/src/auth.ts`, as `internal/api` spends it). So a
   * username nobody else uses, from a browser nobody signed in on, empties a bucket
   * no other spec can touch. The name carries the run's clock so a CI retry starts on
   * a full bucket rather than an empty one.
   */
  test.describe("from a browser nobody signed in on", () => {
    test.use({ storageState: { cookies: [], origins: [] } });

    test("/problems/rate-limited — a username's own budget runs out, and the wait is the server's", async ({
      page,
    }) => {
      const seen = refusalsSeen(page);
      const username = `rate-limit-probe-${Date.now().toString(36)}`;
      await page.goto("/sign-in");
      await expect(page.locator("#signin-username")).toBeVisible();

      /* Until the first 429, bounded well above any burst the console may set. */
      let limited = false;
      for (let attempt = 0; attempt < 40 && !limited; attempt++) {
        await page.locator("#signin-username").fill(username);
        await page.locator("#signin-password").fill("not-the-password");
        const answer = page.waitForResponse(
          (r) => r.url().includes("/api/v0/auth/session") && r.request().method() === "POST",
        );
        await page.locator(".signin form button[type=submit]").click();
        limited = (await answer).status() === 429;
      }
      expect(limited, "forty wrong passwords for one username and none was refused 429").toBe(true);

      const refusal = await only(seen, "/problems/rate-limited");
      expect(refusal.status).toBe(429);
      /* `problem.rate-limited.title` / `.body`, the wait built from `params`. */
      const notice = page.locator(".signin [role=alert]");
      await expect(notice).toContainText("Too many sign-in attempts");
      await expect(notice).toContainText(/Wait (a second|\d+ seconds) before trying again\./);
      await serverDetailAbsent(page, refusal, false);
    });
  });
});

/* ══ the recorder itself, and the partition ═══════════════════════════════════ */

test.describe("the mechanisms these locks rest on can go red", () => {
  test('the alert recorder rejects a role="alert" that is filled after it mounts', async ({
    page,
  }) => {
    await watchAlerts(page);
    await page.goto("/accounts");
    await tableReady(page);
    await forgetAlerts(page);

    /*
     * The defect, re-injected from the page, so the evidence that
     * `alertsSince(page)` can fail is re-derived on every run rather than recorded
     * once in a commit message.
     *
     * A present-and-empty `role="alert"` announces nothing in most screen readers,
     * and by the time any locator reads it it is indistinguishable from a correct
     * one. This is the shape the recorder exists to catch, and every assertion of it
     * above would be worthless if it could not.
     */
    await page.evaluate(async () => {
      const late = document.createElement("div");
      late.setAttribute("role", "alert");
      document.body.append(late);
      await new Promise((resolve) => setTimeout(resolve, 0));
      late.textContent = "announced to nobody";
    });

    const alerts = await alertsSince(page);
    expect(alerts.length, "the observer did not see the injected node at all").toBeGreaterThan(0);
    expect(
      alerts.some((a) => a.text === ""),
      "the recorder read the text after the fact rather than at insertion, which is " +
        "the one thing it exists to distinguish",
    ).toBe(true);
  });

  test("every catalogue entry is either driven here or named unreachable with a reason", async ({
    context,
  }) => {
    /*
     * Read out of the contract, never typed in.
     *
     * `ProblemType` is the closed enum the client catalogue is a `Record` over, so a
     * value added there is already a compile error in `admin/src/problems.ts` until
     * it has copy. This is the other half of that property: it must also be a red
     * end-to-end run until somebody has either driven it or written down why they
     * could not. A hardcoded list here would accept a new value in silence — a
     * mechanism that only looks like a check.
     */
    const declared = problemTypesFromContract();
    expect(declared.length, "the contract's ProblemType enum could not be parsed").toBeGreaterThan(
      10,
    );

    const covered = new Set<string>(DRIVEN);
    const excused = new Set(Object.keys(UNREACHABLE_FROM_THE_CONSOLE));
    const missing = declared.filter((t) => !covered.has(t) && !excused.has(t));

    expect(
      missing,
      "these values are in the contract and this file neither drives them nor says " +
        "why it cannot. A catalogue entry that has never been produced by a code " +
        "path is a sentence that can never be shown to be right.",
    ).toEqual([]);

    /*
     * **One exemption's reason, evaluated rather than read.**
     *
     * An exemption whose reason is prose can stop being true in silence. A predicate
     * per entry is the general repair; the entries below carry one, so the shape
     * exists in the file.
     *
     * The claim is *"no operator in this world is on a temporary password"*. If one
     * ever does, this refusal becomes drivable from here and the exemption is wrong,
     * so the assertion fails and asks for the drive.
     */
    const excusedForSetupClosed = "/problems/setup-complete";
    if (excused.has(excusedForSetupClosed)) {
      const setup = await context.request.get("/api/v0/auth/setup");
      expect(setup.status()).toBe(200);
      expect(
        ((await setup.json()) as { required: boolean }).required,
        `${excusedForSetupClosed} is excused because this world's setup is complete, so ` +
          "the console never shows the setup form. It is open now, so the refusal IS " +
          "reachable from this file — drive it instead of excusing it.",
      ).toBe(false);
    }

    const excusedForNoMarkedOperator = "/problems/password-change-required";
    if (excused.has(excusedForNoMarkedOperator)) {
      const session = await signInAs(context, ADMINISTRATOR);
      expect(
        session.mustChangePassword,
        `${excusedForNoMarkedOperator} is excused because no operator in this world ` +
          "is on a temporary password somebody else set. One now is, so the refusal " +
          "IS reachable from this file and the exemption has outlived its reason — " +
          "drive it instead of excusing it.",
      ).toBe(false);
    }

    /* And the exemption list cannot become a place to hide a value: everything in it
       must still be a real member of the enum. */
    expect(
      [...excused].filter((t) => !declared.includes(t)),
      "an exemption names a type the contract does not have",
    ).toEqual([]);
    expect(
      [...covered].filter((t) => !declared.includes(t)),
      "a test claims to drive a type the contract does not have",
    ).toEqual([]);

    /*
     * And `DRIVEN` is not a list somebody maintains by hand.
     *
     * `observed` is written by `only`, so it holds what the drives above really put
     * on the wire during this run. Without this, a name left in `DRIVEN` after its
     * test stopped producing the type would keep the partition green while covering
     * nothing — a hardcoded number matching a hardcoded number, which is the shape
     * `capability-gating` and `narrow-values-reachable` were both rewritten to avoid.
     *
     * Skipped when nothing was driven at all, which happens only under `--grep`
     * selecting this case on its own. That is stated rather than silent: a whole-file
     * run — the one CI does — always takes the branch below.
     */
    if (observed.size > 0) {
      expect(
        [...covered].filter((t) => !observed.has(t)),
        "listed as driven, but no drive in this run put it on the wire",
      ).toEqual([]);
      expect(
        [...observed].filter((t) => !covered.has(t)),
        "a drive produced a type the coverage list does not mention",
      ).toEqual([]);
    }
  });
});

/** The types a test above put on screen by driving a real failure. */
const DRIVEN = [
  "/problems/internal",
  "/problems/controller-in-use",
  "/problems/validation",
  "/problems/unreachable",
  "/problems/not-found",
  "/problems/duplicate",
  "/problems/rotation-refused",
  "/problems/garden-entry-required",
  "/problems/too-large",
  "/problems/session-end-failed",
  "/problems/controller-unreachable",
  "/problems/session-not-live",
  "/problems/rate-limited",
] as const;

/**
 * What no drive against the admin console can reach, and why.
 *
 * Three are the guest portal's refusals, which the admin console cannot produce at
 * all and which are in the catalogue only because `ProblemType` is one keyspace; the
 * authentication routes' own are driven in `tests/admin-auth/`; and one is a refusal
 * whose render site exists but whose only control cannot select the input that
 * produces it.
 *
 * **This list is meant to shrink.** A reason here is not an excuse, and it is not
 * durable either: it is a claim about the current code that goes stale the moment that code is fixed. Every one of them names
 * the operation that emits the value and what stands between it and a screen, so the
 * next person can tell a value that is somebody else's from a value that nothing can
 * ever show — and can tell when the sentence in front of them has stopped being true.
 *
 * ── AND NOTHING CHECKS THAT. THE TWO HALVES OF THIS FILE ARE NOT SYMMETRIC ───
 *
 * **`DRIVEN` is derived; this list is asserted.** `observed` is written by `only`, so
 * a name left in `DRIVEN` after its drive stopped firing goes red and says so. The
 * two guards over *this* half ask only that an exemption names a real member of
 * `ProblemType` and that every member is in one list or the other. **Neither can
 * evaluate whether a reason is still true**, so the paragraph above is advice to a
 * human rather than a property of the run.
 *
 * A reason can predict its own expiry and still expire silently: an exemption that
 * says *"no control produces this"* stays in the list after the product grows the
 * control, and the suite stays green. That is the expired-skip failure in its sharper
 * form — the condition is checkable and machine-shaped, and nothing checks it. The
 * repair is that an exemption carries a predicate the run evaluates, the way the other
 * half already does; `setup-complete` and `password-change-required` below have one.
 *
 * ── TWO KINDS OF REASON, WHICH SIZES THE FIX ─────────────────────────────────
 *
 *  - A reason about **the product** — no control produces the refusal. It expires when
 *    the product grows the control, which is the thing most likely to change, and it
 *    needs the general mechanism.
 *  - A reason about **this file's world** — the mock serves the ordinary fixtures,
 *    which hold three venues, so the create-venue form is never on screen here. It can
 *    only expire if the fixture world changes.
 *
 * The second kind is the cheap one, because its load-bearing clause is a single
 * question this suite can ask: does the world it is driving have any venue at all?
 * The predicate discriminates — the mock answers `GET /api/v0/sites` with three
 * (`site-hcm`, `site-hanoi`, `site-lab`) ordinarily and with zero under
 * `FOYER_MOCK_FRESH=1` — but it cannot catch a world-shaped reason that stays true
 * while the *product* grows the control that makes the drive possible.
 *
 * Wiring it here is not a one-line edit: this file has no `beforeAll` and no request
 * plumbing, and it would need to go through `MOCK` rather than `MOCK_URL` — see the
 * note there, where `localhost` resolving to `::1` against a mock listening on
 * `0.0.0.0` produces an intermittent `ECONNREFUSED` that looks like anything but a
 * host-resolution bug.
 */
const UNREACHABLE_FROM_THE_CONSOLE: Record<string, string> = {
  "/problems/disconnect-unsupported":
    "`disconnectSession` on a device whose driver cannot end a session. The console " +
    "shows no control there — it gates on `Capabilities.canDeauthGuest` and says why " +
    "instead (`session-disconnect.spec.ts`) — and `web/mock` decides from that same " +
    "column, so no drive from here can make the two disagree. The refusal is the Go " +
    "adapter reading the **driver** where the stored column says otherwise, and " +
    "it is asserted there: `internal/apistore/disconnect_test.go`, a Ruckus row whose " +
    "column claims the capability.",
  "/problems/device-not-verified":
    "The guest portal's refusal. `GET /portal/context` answers it for a client the " +
    "driver will not confirm; no admin operation can produce it.",
  "/problems/method-not-offered":
    "The guest portal's refusal, on `POST /portal/agree` and `POST /portal/account`.",
  "/problems/no-grant": "The guest portal's refusal, on `GET /portal/status`.",
  "/problems/not-signed-in":
    "Routes rather than renders — `whenSignedOut` in `api.ts` puts the operator in " +
    "front of the sign-in form. The authentication routes are driven in `tests/admin-auth/`.",
  "/problems/bad-credentials": "`/sign-in`. The authentication routes are driven in `tests/admin-auth/`.",
  "/problems/forbidden": "The role gate. The authentication routes are driven in `tests/admin-auth/`.",
  "/problems/last-administrator":
    "`DELETE /operators/{id}` on the last enabled administrator. The authentication " +
    "routes are driven in `tests/admin-auth/`.",
  "/problems/one-site-per-install":
    "`SiteGate`'s no-venue branch renders `screens/CreateVenue`, so an operator CAN " +
    "produce this refusal. What keeps it out of `DRIVEN` is not a missing control, " +
    "it is **this file's world**: the suite's mock serves " +
    "the ordinary fixtures, which hold three venues, so `SiteGate` resolves `ready` " +
    "on every route driven here and the form is never on screen. The drive that does " +
    "produce it — the venue list answered `[]` for one browser, the `POST` allowed " +
    "through to the mock, which refuses it because the world really does have three " +
    "venues — is `tests/admin/create-first-venue.spec.ts`, and `observed` is written " +
    "per file, so a drive over there cannot satisfy the partition here. Moving this " +
    "into `DRIVEN` means adding that drive to THIS file and watching it produce the " +
    "type; it is not a list edit. **This sentence is itself unchecked**, which is the " +
    "asymmetry recorded above this list. The condition it rests on is evaluable: this suite's world has three venues, so " +
    "`SiteGate` never reaches `none` here. Nothing evaluates it.",
  "/problems/setup-complete":
    "**Evaluated, like the entry below.** `completeAdminSetup` answers it wherever no " +
    "setup is open, and the console only renders `Setup` (whose `ErrorNotice` would " +
    "show it) when `GET /auth/setup` says one is. This suite's world is an install " +
    "whose setup is long complete — only `FOYER_MOCK_FRESH=1` opens one — so no drive " +
    "from here reaches the form. The partition test asks the server and goes red the " +
    "day this world opens a setup. The refusal itself is asserted in " +
    "`internal/api/setup_test.go` (a setup nobody opened, the losing half of a race, " +
    "a spent code).",
  "/problems/password-change-required":
    "**The reason is a condition this file evaluates, which is what every entry " +
    "here should become.** The server answers this to every operation while the " +
    "signed-in operator is on a temporary password somebody else set (Foyer ships " +
    "no credential; only `foyerctl create-operator " +
    "--must-change-password` sets it), and the console's answer is to " +
    "route to `/password` rather than to render a refusal — so there is no screen " +
    "here that shows this sentence, exactly as `not-signed-in` has none. What keeps " +
    "it out of `DRIVEN` is **this suite's world**: no operator in the fixtures is " +
    "marked as holding it, so no drive from here can produce the refusal at all. " +
    "That claim is not prose — the test below signs in and asserts " +
    "`mustChangePassword` is false, so the day a fixture operator IS marked this " +
    "exemption goes red and demands the drive instead of quietly outliving its " +
    "reason. Marking one is not a one-line fixture edit.",
  "/problems/needs-controller":
    "The register-access-point " +
    "wizard offers only `MANUAL_DRIVERS`, so the operator cannot select a driver whose " +
    "controller is `required` — that is correct, it is the console refusing before the " +
    "server has to, and it is why the refusal stays unreachable by driving. The wizard " +
    "does have a render site for it: `ErrorNotice`, and an `aria-invalid` driver " +
    "select with a `field-error`, so the sentence has somewhere to land. " +
    "The window it lands in is a console older than the server it is talking to — " +
    "`DRIVER_SHAPES` is compiled into the bundle and an operator\u2019s tab outlives a " +
    "deployment — which is a state no drive here can create.",
};

function escapeForRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** `ProblemType`'s enum, parsed out of the document that declares it. */
function problemTypesFromContract(): string[] {
  const path = fileURLToPath(new URL("../../../contract/openapi.yaml", import.meta.url));
  const source = readFileSync(path, "utf8");
  const start = source.indexOf("\n    ProblemType:\n");
  expect(start, "ProblemType is not declared in contract/openapi.yaml").toBeGreaterThan(-1);
  const rest = source.slice(start + 1);
  const end = rest.search(/\n {4}[A-Za-z_]/);
  const block = (end < 0 ? rest : rest.slice(0, end)).split("\n");
  const enumAt = block.indexOf("      enum:");
  expect(enumAt, "ProblemType declares no enum").toBeGreaterThan(-1);

  const values: string[] = [];
  for (const line of block.slice(enumAt + 1)) {
    if (/^\s*#/.test(line)) continue;
    const item = /^ {8}- (\S+)$/.exec(line);
    if (!item) break;
    values.push(item[1]!);
  }
  return values;
}
