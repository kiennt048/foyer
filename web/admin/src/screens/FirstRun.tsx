import { useQuery } from "@tanstack/react-query";
import { useEffect, type ReactNode } from "react";
import { Link } from "react-router";
import type { Nas, PortalSettings } from "@foyer/contract";
import { needsController } from "@foyer/contract";
import { api } from "../api.js";
import { t } from "../i18n/index.js";
import { useSite } from "../site.js";
import { Notice } from "../ui/primitives.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE FIRST-RUN PATH — and why it is a derived checklist rather than a wizard
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The design makes *"a stranger installs it and a phone gets online"* a timed
 * release bar, and names the first moment of user value: **an operator with one
 * access point creates a voucher, and a phone gets online.** `deploy/first-boot.sh`
 * already generates the admin password and creates the operator, so the host side
 * of that sentence exists. This is the console's side of it.
 *
 * **It is not a wizard, because the empty console does not need one.** Signed in
 * against the empty site, every screen already carries the operator:
 * `/access-points` says *"Register the first device to start serving Wi-Fi to
 * guests"*, its register dialog refuses to pretend and says *"UniFi, Omada and Ruckus
 * access points are read from their controller rather than added here"* with a link
 * to `/controllers`, `/garden` says *"Register an access point first"*, and the
 * controller form already names where a UniFi API key comes from, down to the menu
 * path. A five-step wizard over that would be a second, worse copy of five forms.
 *
 * Two things are genuinely missing from those screens, and they are what this file is:
 *
 *  1. **Nothing else says what order.** Each screen names its own next action; none of
 *     them says which comes first, so the operator picks a route from the sidebar
 *     and finds out.
 *  2. **Nothing else says what is not Foyer's to do** — which is where the five
 *     minutes actually go. Naming those honestly is worth more than automating one.
 *
 * And without it the dashboard would report **`0/0` access points, "All reachable"**
 * and **"Nothing needs action"** on a deployment that cannot put a single guest
 * online. See `DashboardScreen`.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Every step is measured, never stored.** There is no wizard state, no cursor and
 * no "completed" flag anywhere — a step is done when the thing it asks for exists.
 * That is not a shortcut, it is the whole of the skippable/re-enterable requirement:
 * an operator who abandons this halfway has a half-configured site rather than a
 * half-finished wizard, every route stays reachable throughout because there is
 * nothing modal to escape, and coming back re-derives the same answer from the same
 * rows. Nothing can be lost because nothing is held, and nothing can be duplicated
 * because the steps link to the screens that already own the writes.
 *
 * The queries are `staleTime: 0`, so opening the dashboard re-derives every step from
 * the server rather than from whatever this browser last heard. The note at `useQuery`
 * below is why that is the mechanism rather than cache invalidation, and why these are
 * not the same cache entries the screens they link to use.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **It renders `null` once the site can serve a guest.** It is a static import all
 * the same — `DashboardScreen`'s header carries the measurement, and the short
 * version is that making this lazy cost 3 115 B rather than saving 985.
 */

/** What still has to happen before a guest on this site can get online. */
interface Steps {
  /** No device in this site sends its guests to Foyer. */
  needsDevice: boolean;
  /** The portal offers vouchers and there is no batch to hand out. */
  needsBatch: boolean;
  /** The portal offers accounts and no voucher, and there is no account. */
  needsAccount: boolean;
}

const nothingLeft = (s: Steps) => !s.needsDevice && !s.needsBatch && !s.needsAccount;

export function FirstRun({ onPending }: { onPending: (pending: boolean) => void }) {
  const { siteId } = useSite();

  /*
   * ───────────────────────────────────────────────────────────────────────────
   *  SAME PREFIX, DIFFERENT KEY
   * ───────────────────────────────────────────────────────────────────────────
   *
   * Not `["nas", siteId, ""]` and `["batches", siteId]` — `AccessPointsScreen`'s and
   * `VouchersScreen`'s own keys, verbatim — even though sharing one cache entry looks
   * better than keeping a second copy of the same rows.
   *
   * **Sharing would make the dashboard decide what two other screens show.**
   * `staleTime` is 10 s (`main.tsx`), so within ten seconds of landing on `/` the
   * access-points and voucher lists would render the dashboard's rows with **no
   * request of their own** — and `absence-is-not-zero.spec.ts` goes red on both,
   * because its premise is a screen whose code has arrived and whose data has not.
   * Offline, those two screens could not reach *"still asking"* or *"asked and
   * failed"* at all. The panel would quietly become a prefetcher for lists it does
   * not own.
   *
   * The last element makes the key distinct. The prefix stays, so
   * `invalidateQueries({ queryKey: ["batches"] })` still reaches this panel — but
   * **`staleTime: 0` is what the correctness rests on**, and the difference between
   * those two matters.
   *
   * With the console's default 10 s staleness, a step is only as current as the last
   * fetch, so an operator who creates a batch and presses Dashboard inside ten seconds
   * can be told to create one — the exact harm this panel exists to prevent, arriving
   * through the cache rather than through a missing check. Relying on invalidation to
   * close that leaves a guarantee that is *timing*: it holds because a write happened
   * to fire an invalidation, and a lock on it goes green by waiting long enough for the
   * staleness to expire instead. Measured: the assertion passes with the invalidation
   * prefix deliberately broken, because `expect` polls for seven seconds and
   * ten-second staleness expires under it.
   *
   * `staleTime: 0` makes it structural instead: mounting the dashboard re-derives every
   * step from the server, every time. Three small GETs on a screen that already polls
   * `getOverview` every twenty seconds, in exchange for a panel that cannot be stale.
   */
  const fresh = { staleTime: 0 } as const;
  const nas = useQuery({
    queryKey: ["nas", siteId, "", "first-run"],
    queryFn: () => api.listNas({ siteId }),
    ...fresh,
  });
  const batches = useQuery({
    queryKey: ["batches", siteId, "first-run"],
    queryFn: () => api.listBatches({ siteId }),
    ...fresh,
  });
  const portal = useQuery({
    queryKey: ["portal-settings", siteId, "first-run"],
    queryFn: () => api.getPortalSettings(siteId),
    ...fresh,
  });

  const methods = portal.data?.methods;
  /*
   * Which credential the guest needs is the portal's decision, not this file's.
   *
   * `PortalMethod` is `voucher | account | click-through`, and `click-through` is
   * terms-only: the consent *is* the authentication, so a venue offering it needs
   * nothing handed out at all. Telling that operator to create a batch of codes
   * would be this component inventing a step the product does not have — the same
   * shape as showing a rate-limit field for a driver that cannot apply one.
   */
  const offersClickThrough = methods?.includes("click-through") ?? false;
  const offersVoucher = methods?.includes("voucher") ?? false;
  const offersAccount = methods?.includes("account") ?? false;

  const wantsAccount = !offersClickThrough && !offersVoucher && offersAccount;
  const accounts = useQuery({
    queryKey: ["accounts", siteId, "", "first-run"],
    queryFn: () => api.listAccounts({ siteId }),
    enabled: wantsAccount,
    ...fresh,
  });

  /*
   * A step is *unfinished* only on evidence. `nas.data?.items.length === 0` reads
   * `undefined === 0` as false while the query is in flight, which is the branch we
   * want: an unanswered request is not the same fact as an empty list, and this panel
   * appearing for a blink on every dashboard load would be the console asserting a
   * site is unconfigured because it has not finished asking.
   */
  const steps: Steps = {
    needsDevice: nas.data?.items.length === 0,
    needsBatch: offersVoucher && batches.data?.items.length === 0,
    needsAccount: wantsAccount && accounts.data?.items.length === 0,
  };
  const pending = !nothingLeft(steps);

  /* The dashboard's "Nothing needs action." is a claim about alarms and it is true —
     but printed directly under a list of things to do it reads as a contradiction.
     The parent suppresses it while this panel has steps. */
  useEffect(() => {
    onPending(pending);
    return () => onPending(false);
  }, [onPending, pending]);

  if (!pending) return null;

  const devices = nas.data?.items ?? [];

  return (
    <section className="section" data-first-run="">
      <h2 className="section-title">{t("firstRun.title")}</h2>
      <div className="notice-stack">
        {/*
          * `data-step` and `data-state` on a wrapper, so a test can say *which* step is
          * outstanding without reading a word of the copy — the same discipline
          * `helpers/admin.ts` states for `data-level` on a status pill. Not derivable
          * from the notice's own level: `info` and `positive` say todo and done, and a
          * panel of three notices in one of those two states says nothing about which
          * of the three moved.
          */}
        <Step name="device" done={!steps.needsDevice}>
          {steps.needsDevice ? (
            <Notice
              level="info"
              title={t("firstRun.device.title")}
              /*
             * **One action, not two.**
             *
             * There are two paths — a controller-driven family that is discovered and a
             * standalone family that is typed in — and a button for each would be wrong
             * twice, and the second is the one that matters.
             *
             * At 390px a `.notice` is a flex row whose action slot does not wrap, so two
             * buttons take the whole width and the body wraps to **one word per line**
             * for eleven lines. That is fixable with `flex-wrap`.
             *
             * The other is not. `RegisterNasWizard` already owns this decision: its
             * device-type select is derived from `needsController()`, and a UniFi
             * operator who opens it is told *"UniFi, Omada and Ruckus access points are
             * read from their controller rather than added here"* with a link to
             * `/controllers`, at the exact moment they are looking for their vendor and
             * not finding it. A second copy of that routing here is a second copy that
             * drifts, and it would be the copy with no device to derive an answer from.
             *
             * So: one destination, and the body says which family goes which way.
             */
              action={
                <Link className="btn btn-sm" to="/access-points" data-first-run-act="device">
                  {t("firstRun.device.action")}
                </Link>
              }
            >
              {t("firstRun.device.body")}
            </Notice>
          ) : (
            <Notice level="positive" title={t("firstRun.device.done", devices.length)} />
          )}
        </Step>

        {offersVoucher ? (
          <Step name="codes" done={!steps.needsBatch}>
            {steps.needsBatch ? (
              <Notice
                level="info"
                title={t("firstRun.codes.title")}
                action={
                  <Link className="btn btn-sm" to="/vouchers" data-first-run-act="codes">
                    {t("firstRun.codes.action")}
                  </Link>
                }
              >
                {t("firstRun.codes.body")}
              </Notice>
            ) : (
              <Notice level="positive" title={t("firstRun.codes.done")} />
            )}
          </Step>
        ) : null}

        {steps.needsAccount ? (
          <Step name="account" done={false}>
            <Notice
              level="info"
              title={t("firstRun.account.title")}
              action={
                <Link className="btn btn-sm" to="/accounts" data-first-run-act="account">
                  {t("firstRun.account.action")}
                </Link>
              }
            >
              {t("firstRun.account.body")}
            </Notice>
          </Step>
        ) : null}

        <Outside devices={devices} settings={portal.data} />
      </div>
    </section>
  );
}

/** One step, carrying which it is and whether it is done. See the note at the stack. */
function Step({
  name,
  done,
  children,
}: {
  name: string;
  done: boolean;
  children: ReactNode;
}) {
  return (
    <div data-step={name} data-state={done ? "done" : "todo"}>
      {children}
    </div>
  );
}

/**
 * The part a wizard usually pretends does not exist.
 *
 * *"`docker compose up` was never the hard part … the five minutes that matter
 * are after the container starts: configuring the AP, obtaining a certificate the
 * guest's phone trusts, creating a controller API key, making DNS resolve
 * pre-authentication."* None of those four is a button Foyer can grow, and three of
 * them are invisible from inside the console — an operator whose phone will not get
 * online has no way to tell from here which of them is the reason.
 *
 * So they are named, and only where they apply: the Cisco ACL is not shown to a
 * MikroTik site, and the API key is not shown to a site whose only devices are
 * standalone. A list of everything every vendor might need is a list nobody reads.
 */
function Outside({
  devices,
  settings,
}: {
  devices: readonly Nas[];
  settings: PortalSettings | undefined;
}) {
  /* Before any device exists, both paths are still open, so both are named. After one
     exists, only what is actually here is. */
  const empty = devices.length === 0;
  const controllerDriven = empty || devices.some((d) => needsController(d.driver));
  /*
   * `url-redirect-acl` names an ACL that must already exist on the device, and Foyer
   * cannot create it (`docs/rules/nas-drivers.md`). Shown for a `coa` device whose
   * vendor is Cisco **and for one whose vendor nobody has said**, because unset is a
   * representable value here on purpose and the un-named half is exactly the case an
   * operator has not thought about yet.
   */
  const cisco = devices.some((d) => d.driver === "coa" && d.coaVendor !== "aruba");
  /*
   * **Capability honesty, on the one claim this panel would otherwise make.**
   *
   * The line above tells the operator a guest must be able to reach the portal
   * *before* signing in. On a site where every registered device reports
   * `walledGarden: "none"`, Foyer's own garden list is a list nothing enforces — so
   * whatever lets that traffic through is configured on the device, and saying
   * "add it to the walled garden" here would be advice that does nothing.
   *
   * `every`, not `some`: one device that can garden makes the list real for that
   * device, and `/garden` already names per entry which devices honour it. This is
   * the case where **no** device does.
   */
  const noGarden = !empty && devices.every((d) => d.capabilities.walledGarden === "none");

  return (
    <Notice level="neutral" title={t("firstRun.outside.title")}>
      <ul className="notice-list" data-first-run-outside="">
        <li>{t("firstRun.outside.ssid")}</li>
        {controllerDriven ? <li>{t("firstRun.outside.apiKey")}</li> : null}
        <li>
          {settings?.portalUrl
            ? t("firstRun.outside.dnsAt", settings.portalUrl)
            : t("firstRun.outside.dns")}
        </li>
        {noGarden ? <li>{t("firstRun.outside.noGarden")}</li> : null}
        {cisco ? <li>{t("firstRun.outside.ciscoAcl")}</li> : null}
      </ul>
    </Notice>
  );
}

export default FirstRun;
