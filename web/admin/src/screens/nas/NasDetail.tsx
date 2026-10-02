import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { DRIVER_SHAPES, needsCoaVendor } from "@foyer/contract";
import { api } from "../../api.js";
import { PageHead } from "../../App.js";
import { useCan } from "../../auth.js";
import { t } from "../../i18n/nas.js";
import { EvidencePanel } from "./Evidence.jsx";
import { Key, Refresh } from "../../ui/icons.js";
import {
  ConfirmDialog,
  ErrorNotice,
  IfCapable,
  Modal,
  Notice,
  Pill,
} from "../../ui/primitives.js";
import { Absent, Mono, TimeCell } from "../../ui/table.js";
import { COA_VENDOR_LABEL, DRIVER_LABEL } from "../../drivers.js";
import { GardenPill, ReachabilityPill, YesNo } from "./bits.js";
import { Findings, ProbeWarnings, Verdict } from "./FindingsPanel.js";
import { attentionCount, probeFindings } from "./findings.js";
import { reachabilityClause } from "../../problems.js";
import { rotationOffer } from "./rotation.js";
import { SecretReveal } from "./SecretReveal.js";

export function NasDetail() {
  const { nasId = "" } = useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const canManage = useCan("nas:manage");
  const [rotating, setRotating] = useState(false);
  const [deleting, setDeleting] = useState(false);
  /*
   * The value a rotation just produced, and the only place in the console it
   * exists.
   *
   * **It is set here and must be rendered.** The mutation receives `sharedSecret`
   * from `rotateNasSecret` and writes it into this state. Without a `newSecret` in
   * the returned tree the dead end would be total, and total for the worst possible
   * reason: Foyer generated the secret, the API is write-only, the console would
   * throw away what the server had just handed it, and the device would be told to
   * stop authenticating — while registration reveals the identical value one screen
   * away.
   *
   * Showing it costs the write-only property nothing: the server sent the value and
   * kept nothing retrievable, so showing it once is not a read of a stored secret —
   * it is the disclosure the rotation response carries.
   */
  const [newSecret, setNewSecret] = useState<string | null>(null);
  /*
   * Leaving, by the button or by Escape. The same shape and the same reason as the
   * registration wizard's: the acknowledgement lives out here so the two routes
   * out are one refusing function rather than two that agree today.
   */
  const [revealAcknowledged, setRevealAcknowledged] = useState(false);
  const [revealPressed, setRevealPressed] = useState(false);
  const ackRef = useRef<HTMLInputElement>(null);
  const revealTitleId = useId();

  /*
   * The one route out of this page that is not ours, and the reason the value is
   * not simply left in state.
   *
   * `pagehide` fires when the document goes into the back-forward cache, and
   * bfcache preserves the DOM and the JS heap — a restored session would come
   * back with a `<dialog>` still holding a live RADIUS shared secret and the
   * acknowledgement still ticked, so nothing about the restored page would say it
   * had been through a cache. Clearing here is the whole of the defence, and it is
   * four lines: a value that is not in the heap is not in the snapshot.
   *
   * The registration wizard does not need this — it *navigates* on success, and
   * `AccessPoints.tsx` removes `/new` from the history stack so Back cannot land on
   * it. Rotation has no route change to make, so this is its equivalent.
   */
  useEffect(() => {
    if (!newSecret) return;
    const clear = () => {
      setNewSecret(null);
      setRevealAcknowledged(false);
    };
    window.addEventListener("pagehide", clear);
    return () => window.removeEventListener("pagehide", clear);
  }, [newSecret]);

  /** Leaving the reveal, by either route. Refuses, visibly, until it may not. */
  const leaveReveal = () => {
    if (revealAcknowledged) {
      setNewSecret(null);
      setRevealAcknowledged(false);
      setRevealPressed(false);
      return;
    }
    /* The refusal is stated and the cursor is put on the thing that resolves it. A
       control that does nothing when pressed reads as a broken screen. */
    setRevealPressed(true);
    ackRef.current?.focus();
  };

  const query = useQuery({
    queryKey: ["nas", nasId],
    queryFn: () => api.getNas(nasId),
  });
  /* How this device's claims are known. A plain GET rather than part of the probe:
     nothing in it is established by a probe, and it must answer for a device
     nothing can reach — which is exactly the device an operator is looking at when
     they want to know what Foyer would send it. */
  const evidence = useQuery({
    queryKey: ["nas", nasId, "evidence"],
    queryFn: () => api.getNasEvidence(nasId),
  });
  const probe = useMutation({
    mutationFn: () => api.probeNas(nasId),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["nas"] }),
  });
  const rotate = useMutation({
    mutationFn: () => api.rotateSecret(nasId),
    onSuccess: (r) => {
      setNewSecret(r.sharedSecret);
      setRotating(false);
      void qc.invalidateQueries({ queryKey: ["nas"] });
    },
  });
  const del = useMutation({
    mutationFn: () => api.deleteNas(nasId),
    onSuccess: () => navigate("/access-points"),
  });

  const nas = query.data;
  if (query.isLoading) return <div className="page">{t("common.loading")}</div>;
  /*
   * A wrong sentence is worse than a missing one, because it makes a claim about the
   * world. A bare `if (!nas)` saying *"Access point not found. It may have been
   * deleted from another session."* would say it on a `500`, on a dropped connection,
   * on anything at all. The type decides: `/problems/not-found` reads as gone, and
   * nothing else borrows that sentence.
   */
  if (!nas) {
    return (
      <div className="page">
        <ErrorNotice error={query.error} />
      </div>
    );
  }

  const unreachable = nas.reachability !== "ok";
  const unavailableReason = unreachable ? t("nas.reach.unavailable") : null;
  const c = nas.capabilities;
  /*
   * Which of the three outcomes pressing *Generate new secret* produces. Derived,
   * not branched at the call sites: if the standing note and the confirmation below
   * each asked only "can it push?" while the server asks "can it push?" *and* "has
   * it ever answered?", the note would promise a device coming off the network on
   * a rotation the server was about to refuse.
   */
  const offer = rotationOffer(nas);
  /*
   * The sentence the `409` will come back with, built here rather than written
   * again. It has to be the *same* sentence: the standing note exists to tell the
   * operator what is about to happen, and a hand-written version of a refusal is
   * how a screen ends up disagreeing with the server about why it refused.
   */
  const blockedBecause = () => reachabilityClause(nas.name, nas.reachability);

  /* The probe result and the device it describes. `nas` is refetched by the
     invalidation above, so the two are the same measurement rather than one fresh
     half and one stale one. */
  const findings = probe.data ? probeFindings(probe.data, nas) : null;

  return (
    <div className="page">
      <PageHead
        title={nas.name}
        description={DRIVER_LABEL[nas.driver]}
        actions={
          /*
           * Probe, delete and rotate all declare `nas:manage`, so all three leave a
           * staff console. **The shared-secret section below does not**, and that is
           * the narrow reading rather than an oversight: a staff member who cannot
           * see that a secret exists, and when it was last used, is a staff member
           * who cannot tell an administrator that one needs rotating. The role makes
           * these three actions somebody else's, not the knowledge.
           */
          !canManage ? null : (
            <>
              {/* Pressed by the operator, so it holds focus while the probe runs.
                `aria-disabled` — see `Accounts.tsx`'s row action. */}
              <button
                type="button"
                className="btn"
                onClick={() => {
                  if (!probe.isPending) probe.mutate();
                }}
                aria-disabled={probe.isPending || undefined}
              >
                <Refresh size={14} />
                {t("nas.detail.probe")}
              </button>
              <button
                type="button"
                className="btn btn-danger"
                onClick={() => setDeleting(true)}
              >
                {t("common.delete")}
              </button>
            </>
          )
        }
      />

      <div className="notice-stack" style={{ marginBottom: "1.25rem" }}>
        {/* The probe is a page-level action, so its failure is a page-level notice —
            and it has to be one, because the findings panel below simply does not
            render and the button returns to looking pressable. */}
        {probe.error ? (
          <ErrorNotice
            error={probe.error}
            title={t("nas.wizard.probe.failed.title")}
          />
        ) : null}
        {nas.reachability === "unreachable" ? (
          <Notice level="urgent" title={t("nas.notice.unreachable.title")}>
            {t("nas.notice.unreachable.body")}
          </Notice>
        ) : null}
        {nas.reachability === "credentials-expired" ? (
          <Notice
            level="urgent"
            title={t("nas.notice.credentialsExpired.title")}
          >
            {t("nas.notice.credentialsExpired.body")}
          </Notice>
        ) : null}
        {nas.messageAuthenticator === "refused" ? (
          <Notice
            level="warning"
            title={t("nas.notice.messageAuthenticator.title")}
          >
            {t("nas.notice.messageAuthenticator.body")}
          </Notice>
        ) : null}
        {c.walledGarden === "none" ? (
          <Notice level="warning" title={t("nas.notice.noGarden.title")}>
            {t("nas.notice.noGarden.body")}
          </Notice>
        ) : null}
        {/* `=== false`, not `!`. The third value of this field is *absent*, and a
            device nobody has probed has it — so `!nas.accountingConfigured` raised a
            fault notice about a radio nothing has spoken to, on the one screen whose
            premise is that these things are measured rather than assumed.

            Exactly the shape of the notice three lines up: `messageAuthenticator`
            has the same three states and only `refused` gets a notice, because
            `unknown` is not something to act on. `false` is this field's `refused`
            and absent is its `unknown`. The absence is said where the value is
            rendered rather than where faults are — `AccountingPill` on the list, and
            the findings panel's *not measured* row once a probe has run. */}
        {nas.accountingConfigured === false ? (
          <Notice
            level="caution-minor"
            title={t("nas.notice.noAccounting.title")}
          >
            {t("nas.notice.noAccounting.body")}
          </Notice>
        ) : null}
      </div>

      {/* `.record-grid`, not an inline `1fr 1fr`. The class carries why — a track
          minimum that collapses with the container — and, more to the point, it
          puts the decision where `admin.css` and the responsive sweep can see it. */}
      <div className="record-grid">
        <section className="section">
          <h2 className="section-title">{t("nas.device.section")}</h2>
          <div className="card">
            <dl className="kv">
              <dt>{t("nas.col.status")}</dt>
              <dd>
                <ReachabilityPill nas={nas} />
              </dd>
              <dt>{t("nas.device.model")}</dt>
              <dd>{nas.model ?? <Absent />}</dd>
              <dt>{t("nas.device.firmware")}</dt>
              <dd>{nas.firmware ?? <Absent />}</dd>
              <dt>{t("nas.device.address")}</dt>
              <dd>
                {nas.ipAddress ? <Mono>{nas.ipAddress}</Mono> : <Absent />}
              </dd>
              <dt>{t("nas.device.identifier")}</dt>
              <dd>
                {nas.nasIdentifier ? (
                  <Mono>{nas.nasIdentifier}</Mono>
                ) : (
                  <Absent />
                )}
              </dd>
              <dt>{t("nas.device.coaPort")}</dt>
              <dd>
                {/*
                  Per-NAS, not a constant. 3799 is common; Cisco frequently uses 1700.

                  **An absent port is two different facts and one sentence cannot
                  carry both.** *Does not use CoA.* is right for Ruckus and Omada,
                  whose enforcement is an HTTP API; it is false for the two drivers
                  whose enforcement is nothing but CoA, and those are exactly the two
                  the registration wizard offers — which sends no port at all, so
                  **every device registered by hand** arrives here with a null.

                  So the question is asked of `DRIVER_SHAPES`, which already answers
                  it, rather than inferred from whether a nullable column happens to
                  be filled in. `coaPort` is the *operator's override* and nothing
                  else: the default is applied one layer down at send time, and both
                  backends store the operator's value or nothing at all.

                  **This reasons about the contract, not about what a backend
                  stores.** A console comment that reasons about a backend's
                  internals is a claim about something this file cannot see — and
                  there are two backends, so it is wrong as soon as either moves.
                  `Nas.coaPort` is nullable for every driver, which is all this branch
                  needs to know and the one thing neither backend can change alone.
                */}
                {nas.coaPort ? (
                  <Mono>{nas.coaPort}</Mono>
                ) : DRIVER_SHAPES[nas.driver].revoke === "coa" ? (
                  <>
                    <Absent />
                    <span
                      style={{
                        color: "var(--text-muted)",
                        marginLeft: "0.5rem",
                      }}
                    >
                      {t("nas.device.coaPortDefault")}
                    </span>
                  </>
                ) : (
                  <Absent hint={t("nas.device.coaPortAbsent")} />
                )}
              </dd>
              {/*
                **Which CoA vendor this is**, on the one driver whose kind names a
                family rather than a vendor — and it was on the wire with nothing
                rendering it.

                Cisco and Aruba identify a session by different attributes, so this
                value decides whether a revocation reaches anything; the wrong dialect
                matches no session and a careless device **ACKs it anyway**. A device
                screen that cannot say which vendor a device was registered as cannot
                answer the first question anybody asks when a revocation does nothing.

                `needsCoaVendor`, not `driver === "coa"`: the same `DRIVER_SHAPES` row
                the registration form asks from and the server refuses on. A row on a
                driver that does not speak CoA is a value nothing reads, so there is no
                row here for it either.

                **Unset is not an absence, it is a fault**, which is why it is a pill
                and not the `–` one line above. An absent port is a value Foyer fills
                in; an absent vendor is a device Foyer refuses to build a driver for at
                all, and `updateNas` takes only `name` and `enabled`, so the recovery
                really is to register it again. That state is reachable — the contract
                says a `coa` row predating this field has no vendor and never will
                until somebody says — so the branch is driven in
                `web/e2e/tests/admin/coa-vendor.spec.ts` rather than left to reasoning.
              */}
              {needsCoaVendor(nas.driver) ? (
                <>
                  <dt>{t("nas.device.coaVendor")}</dt>
                  <dd>
                    {nas.coaVendor ? (
                      COA_VENDOR_LABEL[nas.coaVendor]
                    ) : (
                      <>
                        <Pill
                          level="caution-minor"
                          label={t("nas.device.coaVendorUnset")}
                        />
                        <span
                          style={{
                            color: "var(--text-muted)",
                            marginLeft: "0.5rem",
                          }}
                        >
                          {t("nas.device.coaVendorUnsetWhy")}
                        </span>
                      </>
                    )}
                  </dd>
                </>
              ) : null}
              <dt>{t("nas.col.lastSeen")}</dt>
              <dd>
                <TimeCell
                  iso={nas.lastSeenAt}
                  absent={t("nas.lastSeen.absent")}
                />
              </dd>
            </dl>
          </div>
        </section>

        <section className="section">
          <h2 className="section-title">{t("nas.secret.section")}</h2>
          <div className="card">
            <dl className="kv">
              <dt>{t("nas.col.status")}</dt>
              <dd>
                {nas.secretSet ? (
                  <Pill level="positive" label={t("nas.secret.set")} />
                ) : (
                  <Pill level="caution-minor" label={t("nas.secret.unset")} />
                )}
              </dd>
              <dt>{t("nas.secret.lastUsed")}</dt>
              {/* What makes a write-only secret manageable: not the value, but
                  whether anything is still using it. */}
              <dd>
                <TimeCell
                  iso={nas.secretLastUsedAt}
                  absent={t("nas.secret.neverUsed")}
                />
              </dd>
            </dl>
            <p
              style={{
                fontSize: "0.75rem",
                color: "var(--text-muted)",
                marginBottom: 0,
              }}
            >
              {t("nas.secret.note")}
            </p>
            {/*
              Not wrapped in `<IfCapable need="rotateSecret">`, deliberately. Rotation
              is not "a manual, documented procedure, not a button".

              The general rule is that **a capability describes what the driver can do
              on the operator's behalf, not what the product may offer.** When one is
              false, the question is whether the action becomes *impossible* or merely
              *manual*. Impossible means hide it. Manual means keep it and state the
              price. `rotateSecret` is *"Push a new shared secret"* — the driver's
              ability to write the value onto the device. Generating one is Foyer's own
              operation and works everywhere, so gating this button would leave a site
              whose driver cannot push with **no way to rotate a secret at all**.

              What was actually wrong is that the page contradicted itself: a row four
              hundred pixels below read "Push a new shared secret: No" while this button
              sat here with no context, and the only way to learn what that meant was to
              open a destructive dialog. So the dialog's sentence is lifted up here,
              beside the control it qualifies, and the manual procedure is named before
              the operator commits rather than after.
            */}
            {/* The note is the control's caption — it states what pressing it costs —
                so the two leave together. Everything above them stays. */}
            {canManage ? (
              <>
                {/*
                 * Three states, derived in `rotation.ts` from the same question
                 * `rotateNasSecret` asks. The note beside the button is the only
                 * place the operator reads before committing, so it is where all
                 * three of the facts belong — and `refused` is the one that is
                 * easiest to leave out: without it a real `U6 Lite` is told *"the
                 * device stops authenticating"* by a screen that is about to
                 * refuse the press with a `409`.
                 *
                 * The button stays in the refused state on purpose. Reachability
                 * changes under the operator's feet — a probe or a fixed API key
                 * turns this into a `manual` rotation — and `disabled` would strand
                 * a device that became rotatable while the page was open. The
                 * server is the authority; this is the operator being told which
                 * of the three answers they are about to get.
                 */}
                {offer.kind === "refused" ? (
                  <Notice level="warning" title={t("nas.rotate.blockedTitle")}>
                    <p style={{ margin: "0 0 0.5rem" }}>{blockedBecause()}</p>
                    <p style={{ margin: 0 }}>
                      <button
                        type="button"
                        className="btn btn-sm"
                        onClick={() => {
                          if (!probe.isPending) probe.mutate();
                        }}
                      >
                        <Refresh size={13} />
                        {t("nas.rotate.blockedProbe")}
                      </button>{" "}
                      <span style={{ color: "var(--text-muted)" }}>
                        {t("nas.rotate.blockedOr")}
                      </span>
                    </p>
                  </Notice>
                ) : (
                  <p
                    className="secret-rotate-note"
                    data-manual={offer.kind === "manual"}
                  >
                    {offer.kind === "push" ? (
                      t("nas.rotate.recoveryPush")
                    ) : (
                      <>
                        <strong>
                          {t("nas.rotate.recoveryManualEmphasis")}
                        </strong>{" "}
                        {t("nas.rotate.recoveryManual")}
                      </>
                    )}
                  </p>
                )}
                <div style={{ marginTop: "0.75rem" }}>
                  <button
                    type="button"
                    className="btn"
                    onClick={() => setRotating(true)}
                  >
                    <Key size={14} />
                    {t("nas.secret.rotate")}
                  </button>
                </div>
              </>
            ) : null}
          </div>
        </section>
      </div>

      <section className="section">
        <h2 className="section-title">{t("nas.capabilities.section")}</h2>
        <div className="card">
          <p
            style={{
              marginTop: 0,
              fontSize: "0.8125rem",
              color: "var(--text-secondary)",
            }}
          >
            <strong>{t("nas.capabilities.emphasis")}</strong>{" "}
            {t("nas.capabilities.note")}
          </p>
          <dl className="kv">
            <dt>{t("nas.col.garden")}</dt>
            <dd>
              <GardenPill capabilities={c} />
            </dd>
            <dt>{t("nas.capabilities.bandwidth")}</dt>
            <dd>
              <YesNo value={c.bandwidthLimits} />
            </dd>
            <dt>{t("nas.capabilities.hardQuota")}</dt>
            <dd>
              <YesNo value={c.hardQuota} />
              {!c.hardQuota ? (
                <span
                  style={{ color: "var(--text-muted)", marginLeft: "0.5rem" }}
                >
                  {t("nas.capabilities.hardQuotaNo")}
                </span>
              ) : null}
            </dd>
            <dt>{t("nas.capabilities.dynamicVlan")}</dt>
            <dd>
              <YesNo value={c.dynamicVlan} />
            </dd>
            <dt>{t("nas.capabilities.liveReauthorize")}</dt>
            <dd>
              <YesNo value={c.liveReauthorize} />
              {!c.liveReauthorize ? (
                <span
                  style={{ color: "var(--text-muted)", marginLeft: "0.5rem" }}
                >
                  {t("nas.capabilities.liveReauthorizeNo")}
                </span>
              ) : null}
            </dd>
            <dt>{t("nas.capabilities.rotateSecret")}</dt>
            <dd>
              <YesNo value={c.rotateSecret} />
            </dd>
          </dl>

          {/*
            The capability gate in use — and the controls now go somewhere.
            ─────────────────────────────────────────────────────────────────────
            These four were `<button>`s with no `onClick`. Not merely dead: they were
            the *only* controls `capability-gating.spec.ts` counted, so the admin
            suite's highest-value assertion was proven entirely against placeholders.

            Driving them turned up the better finding. **Three of the four were on the
            wrong screen.** A rate limit belongs to a plan, a VLAN to a policy, and
            "apply now" to a session — none of them is a property of a device record,
            and a device screen offering to set them was offering to edit something
            that does not live here. Those two are gone; the capability list above
            already says what the hardware can do, which is what a device screen is
            for.

            What is left is two links to where the setting actually is. The gate still
            hides rather than disables — a control the hardware cannot honour must not
            exist, or an operator goes hunting for the setting that enables it — and
            `unavailable` still disables-with-reason for the supported-but-unreachable
            case, which is the other half of the rule.
          */}
          <div
            style={{
              display: "flex",
              gap: "0.5rem",
              marginTop: "1rem",
              flexWrap: "wrap",
            }}
          >
            <IfCapable
              capabilities={c}
              need="bandwidthLimits"
              unavailable={unavailableReason}
            >
              {({ disabled, reason }) =>
                disabled ? (
                  <button
                    type="button"
                    className="btn"
                    disabled
                    title={reason ?? undefined}
                  >
                    {t("nas.action.bandwidth")}
                  </button>
                ) : (
                  <Link className="btn" to="/vouchers">
                    {t("nas.action.bandwidth")}
                  </Link>
                )
              }
            </IfCapable>
            <IfCapable
              capabilities={c}
              need="walledGarden"
              unavailable={unavailableReason}
            >
              {({ disabled, reason }) =>
                disabled ? (
                  <button
                    type="button"
                    className="btn"
                    disabled
                    title={reason ?? undefined}
                  >
                    {t("nas.action.garden")}
                  </button>
                ) : (
                  <Link className="btn" to="/garden">
                    {t("nas.action.garden")}
                  </Link>
                )
              }
            </IfCapable>
          </div>
        </div>
      </section>

      {/*
        The same rendering the registration wizard uses, from the same derivation.
        Two renderings of one probe would drift, and the one an operator returns to
        would be the one that drifted — they read it here weeks after the wizard.

        No button in this card, deliberately: "Probe again" belongs in the page
        header beside the other page-level actions, and this section is a direct
        child of `.page`, which is where the capability-gating suite counts gated
        controls.

        **The warnings were the half this screen did not render at all.** The comment
        above says two renderings of one probe would drift; what had actually happened
        is narrower and worse — this one was missing a part rather than disagreeing
        about it. `probeFindings` was shared and `probeWarningText` was not, so
        `access-point-unconfirmed` — which the UniFi driver appends to **every** probe —
        was visible for the length of the registration wizard and never again. An
        operator who comes back to a device weeks later is exactly the reader that
        warning is for.
      */}
      {/*
        How this device's claims are known. A separate section from the probe, because it is a separate register: the
        probe says what was measured just now, and this says how anything is known
        at all. It renders for every device, reachable or not, because the vendor
        profile answers the same either way.
      */}
      {evidence.data ? (
        <section className="section">
          <h2 className="section-title">{t("nas.evidence.section")}</h2>
          <div className="card">
            <EvidencePanel evidence={evidence.data} />
          </div>
        </section>
      ) : null}

      {findings ? (
        <section className="section">
          <h2 className="section-title">{t("nas.probe.section")}</h2>
          <div className="card">
            <Verdict
              reachable={probe.data?.reachability === "ok"}
              attention={attentionCount(findings)}
            />
            {probe.data ? (
              <ProbeWarnings warnings={probe.data.warnings} />
            ) : null}
            <Findings findings={findings} />
          </div>
        </section>
      ) : null}

      <ConfirmDialog
        open={rotating}
        title={t("nas.rotate.title")}
        verb={t("nas.rotate.verb")}
        busy={rotate.isPending}
        error={rotate.error}
        consequence={
          /*
           * The consequence operators do not expect, and the reason a rotation gets
           * a dialog at all. **Except when the press will be refused**, where the
           * consequence of pressing is *nothing*: the whole sentence above — the
           * restart, the dropped EAP conversations, the cleared TLS cache — describes
           * a rotation that is not going to happen. Naming that here rather than
           * waiting for the 409 is the difference between a screen that is honest
           * and one that has to be corrected by its own error.
           */
          offer.kind === "refused" ? (
            t("nas.rotate.blockedBecause", blockedBecause())
          ) : (
            <>
              {t("nas.rotate.consequence")}{" "}
              <strong>{t("nas.rotate.consequenceEmphasis")}</strong>
            </>
          )
        }
        recovery={
          offer.kind === "push" ? (
            t("nas.rotate.recoveryPush")
          ) : offer.kind === "refused" ? (
            t("nas.rotate.blockedProbe")
          ) : (
            <>
              <strong>{t("nas.rotate.recoveryManualEmphasis")}</strong>{" "}
              {t("nas.rotate.recoveryManual")}
            </>
          )
        }
        onConfirm={() => rotate.mutate()}
        onCancel={() => setRotating(false)}
      />

      {/*
        The reveal, and why its absence would be invisible to every suite in the
        repository.

        The `rotate` mutation above sets `newSecret`. If nothing read it, a device
        that Foyer discovered — a UniFi, Omada or Ruckus access point, every one of
        which has a driver that cannot push a secret — would be permanently unable to
        authenticate with no operator able to configure it. The type checker would
        stay green because an unused `useState` is not an error, the API tests
        because the server is right, and the E2E suite because `revealedSecret`
        would only be asserted *absent* — the refusal case, the one case where
        nothing is shown.

        `null` when closed, for the reason `AccessPointsScreen` gives: an overlay
        that has not opened is nothing on screen, and `Modal` moves focus into
        itself on mount, so rendering a closed one would move the operator into an
        empty dialog and then move them again.
        */}
      {newSecret ? (
        <Modal labelledBy={revealTitleId} onClose={leaveReveal}>
          <div className="dialog-card">
            <div className="dialog-head">
              <h2 className="dialog-title" id={revealTitleId}>
                {t("nas.newSecret.title")}
              </h2>
            </div>
            <SecretReveal
              secret={newSecret}
              bodyLabel={t("nas.newSecret.title")}
              body={
                /*
                 * Two sentences, and the difference is the driver rather than the
                 * action. Where the driver pushed the value, the device already has
                 * it and there is nothing to type — saying so stops an operator
                 * walking to an access point to key in a secret that is already
                 * there. Where it could not, this is the one screen where the value
                 * exists anywhere, and the manual procedure gets stated where the
                 * value is in their hands.
                 */
                <>
                  <div style={{ marginBottom: "0.75rem" }}>
                    <strong>{t("nas.wizard.secret.heading")}</strong>{" "}
                    {offer.kind === "push"
                      ? t("nas.rotate.revealedPush")
                      : t("nas.rotate.revealedManual")}
                  </div>
                  <p className="field-hint" style={{ marginTop: 0 }}>
                    {t("nas.newSecret.never")}
                  </p>
                </>
              }
              continueLabel={t("nas.newSecret.continue")}
              acknowledged={revealAcknowledged}
              pressed={revealPressed}
              ackRef={ackRef}
              onAcknowledge={(value) => {
                setRevealAcknowledged(value);
                if (value) setRevealPressed(false);
              }}
              onContinue={leaveReveal}
            />
          </div>
        </Modal>
      ) : null}

      <ConfirmDialog
        open={deleting}
        tier="type-name"
        title={t("nas.delete.title")}
        verb={t("nas.delete.verb")}
        confirmName={nas.name}
        busy={del.isPending}
        error={del.error}
        /*
         * **Removing a NAS is not a revocation**, and the dialog is where that has to
         * be said: `docs/rules/frontend.md` asks for the price *before* the
         * operator commits, and there is no surface after this one — the mutation
         * navigates to `/access-points`, which unmounts this screen and any toast
         * with it, onto a list where the device is simply absent.
         *
         * Same shape as the rotation above, because it is the same class of thing:
         * one sentence of ordinary consequence, one emphasised sentence carrying the
         * part nobody expects. See `nas.delete.*` in `i18n/nas.ts` for the measurement and
         * for why no number appears in any of it.
         */
        consequence={
          <>
            {t("nas.delete.consequence")}{" "}
            <strong>{t("nas.delete.consequenceEmphasis")}</strong>
          </>
        }
        recovery={
          <>
            <strong>{t("nas.delete.recoveryEmphasis")}</strong>{" "}
            {t("nas.delete.recovery")} {t("nas.delete.records")}
          </>
        }
        onConfirm={() => del.mutate()}
        onCancel={() => setDeleting(false)}
      />
    </div>
  );
}
