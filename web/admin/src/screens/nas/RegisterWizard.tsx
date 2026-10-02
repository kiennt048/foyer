import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import type { Nas, NasProbe } from "@foyer/contract";
import { DRIVER_SHAPES, needsCoaVendor, needsController } from "@foyer/contract";
import { api } from "../../api.js";
import { t } from "../../i18n/nas.js";
import { fieldMessages } from "../../problems.js";
import { useSite } from "../../site.js";
import { Refresh } from "../../ui/icons.js";
import { ErrorNotice, Modal, Notice } from "../../ui/primitives.js";
import { COA_VENDORS, COA_VENDOR_LABEL, DRIVER_LABEL, type CoaVendor } from "../../drivers.js";
import { SecretReveal } from "./SecretReveal.js";
import { Findings, FindingsSkeleton, ProbeWarnings, Verdict } from "./FindingsPanel.js";
import { FINDING_COUNT, attentionCount, probeFindings } from "./findings.js";

/**
 * The drivers a device can be registered under by hand.
 *
 * Derived from `DRIVER_SHAPES` rather than listed, so a driver that gains or loses a
 * controller cannot leave this select stale — the table is the one place that
 * decision lives, and three copies of it is three copies that drift.
 */
const MANUAL_DRIVERS = (Object.keys(DRIVER_SHAPES) as Nas["driver"][]).filter(
  (d) => !needsController(d),
);

/**
 * Registering an access point: three steps, in one modal.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why the probe is a step and not a link.**
 *
 * A flow that was a form and then the secret, ending on the list, would leave
 * everything the probe knows — that this device will never send accounting, that it
 * refuses Message-Authenticator, that its quota is a sampled backstop — discoverable
 * only by opening the detail screen afterwards, which nobody does immediately after
 * a thing appears to have succeeded. Every one of those facts is a surprise the
 * operator meets later as "the rate-limit field is missing" or "sessions never show
 * up", at which point it costs a support conversation instead of a sentence.
 *
 * So the probe runs inside the flow, and the flow does not end until the operator
 * has been shown what the device can and cannot do.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why this is a dialog and not a route**, which is the real decision here.
 *
 * A route is the better container for the third step: it has room, it is linkable,
 * and — the part that actually costs something — the accessibility sweep walks a
 * list of URLs, so a route is swept and a dialog is not.
 *
 * It is the *worse* container for the second step, and the second step is the one
 * that cannot be repeated. A modal `<dialog>` inerts the document behind it: the
 * sidebar cannot be clicked, the table cannot be clicked, tab does not leave, and
 * Escape is ours. On a route, every one of those is a way to lose a value that no
 * operation returns, and blocking navigation needs a data router this application
 * does not use. `showModal()` gives that protection structurally rather than by
 * catching each exit.
 *
 * Both are had by adding **`/access-points/new`**, which renders the list with this
 * dialog open on step one. The route is linkable and sweepable; the moment the
 * device is created the URL is *replaced* with `/access-points`, so the entry is
 * gone from history before the secret is on screen and Back cannot reach past it.
 * See `AccessPointsScreen`.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The secret step has no way out that is not forward.** No Cancel, no backdrop
 * dismiss, and Escape runs the same handler as the primary button — which refuses
 * until the operator has said they stored it. An operator who lands on a registered
 * device with no secret has a device that cannot authenticate and no recovery but
 * rotation, which on most drivers means going to the device with the new value.
 */

type Step = "details" | "secret" | "probe";

const STEPS: readonly Step[] = ["details", "secret", "probe"] as const;

const STEP_LABEL: Record<Step, string> = {
  get details() {
    return t("nas.wizard.step.details");
  },
  get secret() {
    return t("nas.wizard.step.secret");
  },
  get probe() {
    return t("nas.wizard.step.probe");
  },
};

export function RegisterNasWizard({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  /** Fired the instant the device exists, before the secret reaches the screen. */
  onCreated: () => void;
}) {
  const { siteId } = useSite();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const titleId = useId();

  const [step, setStep] = useState<Step>("details");
  const [created, setCreated] = useState<Nas | null>(null);
  const [secret, setSecret] = useState<string | null>(null);

  /*
   * The acknowledgement lives up here rather than in the step that renders it, so
   * that Escape and the primary button can be **the same function** rather than two
   * functions that agree today. That equivalence is the whole protection on this
   * step: the last time they were separate, Escape dismissed the dialog and left the
   * operator on the list with a registered device and no secret for it.
   */
  const [acknowledged, setAcknowledged] = useState(false);
  const [ackPressed, setAckPressed] = useState(false);
  const ackRef = useRef<HTMLInputElement>(null);

  const create = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.createNas(body),
    onSuccess: (r) => {
      setCreated(r.nas);
      setSecret(r.sharedSecret);
      setStep("secret");
      onCreated();
      void qc.invalidateQueries({ queryKey: ["nas"] });
    },
  });

  /*
   * Probe, then re-read the device.
   *
   * `NasProbe` carries `reachability` and `capabilities` and **not**
   * `messageAuthenticator` or `accountingConfigured`, which are on `Nas` — and those
   * two are half of what this step exists to say. So the probe is not complete until
   * the device has been read back. Recorded as a contract finding rather than hidden
   * behind a convenience: the operation that measures a device should return
   * everything it measured.
   */
  const probe = useMutation({
    mutationFn: async (nasId: string): Promise<{ result: NasProbe; device: Nas }> => {
      const result = await api.probeNas(nasId);
      const device = await api.getNas(nasId);
      return { result, device };
    },
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["nas"] }),
  });

  const startProbe = () => {
    if (!created) return;
    setStep("probe");
    probe.mutate(created.id);
  };

  /** Leaving the secret step, by either route. Refuses, visibly, until it may not. */
  const leaveSecretStep = () => {
    if (acknowledged) {
      startProbe();
      return;
    }
    /* The refusal is stated and the cursor is put on the thing that resolves it.
       A control that does nothing when pressed reads as a broken screen. */
    setAckPressed(true);
    ackRef.current?.focus();
  };

  /** The end of the flow: hand off to the screen that keeps this permanently. */
  const finish = () => {
    onClose();
    if (created) navigate(`/access-points/${created.id}`);
  };

  return (
    <Modal
      className="wizard"
      labelledBy={titleId}
      onClose={step === "probe" ? finish : step === "details" ? onClose : leaveSecretStep}
    >
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {t("nas.wizard.title")}
          </h2>
          <StepTrail current={step} />
        </div>

        {step === "details" ? (
          <DetailsStep
            siteId={siteId}
            busy={create.isPending}
            error={create.error}
            onCancel={onClose}
            onSubmit={(body) => create.mutate(body)}
          />
        ) : null}

        {step === "secret" && secret ? (
          <SecretStep
            secret={secret}
            acknowledged={acknowledged}
            pressed={ackPressed}
            ackRef={ackRef}
            onAcknowledge={(value) => {
              setAcknowledged(value);
              if (value) setAckPressed(false);
            }}
            onContinue={leaveSecretStep}
          />
        ) : null}

        {step === "probe" && created ? (
          <ProbeStep
            device={probe.data?.device ?? created}
            result={probe.data?.result ?? null}
            busy={probe.isPending}
            failed={probe.isError}
            onRetry={() => probe.mutate(created.id)}
            onFinish={finish}
          />
        ) : null}
      </div>
    </Modal>
  );
}

/* ── the body ─────────────────────────────────────────────────────────────── */

/**
 * The scrolling part of the dialog, and the reason it is focusable.
 *
 * A nine-row probe result does not fit in a dialog on a laptop, so this scrolls.
 * **A region that scrolls and cannot be focused cannot be scrolled from a
 * keyboard** — WCAG 2.1.1, and axe reports it `serious`. The accessibility sweep
 * reaches this dialog through its `/access-points/new` steps
 * (`web/e2e/tests/a11y/routes.spec.ts`; see also the note in `AccessPointsScreen`).
 *
 * Named after the step, so a screen reader user landing on the tab stop is told
 * what they are about to scroll rather than "group".
 */
function WizardBody({ step, children }: { step: Step; children: ReactNode }) {
  return (
    <div className="dialog-body" tabIndex={0} role="group" aria-label={STEP_LABEL[step]}>
      {children}
    </div>
  );
}

/* ── the trail ────────────────────────────────────────────────────────────── */

/**
 * Where the operator is, and — the load-bearing half — that a third step exists.
 *
 * Without it the secret reads as the end of the flow, which is exactly the moment a
 * dialog gets dismissed. Named steps rather than "2 of 3" alone, because "what it
 * can do" is also the sentence that says the probe is coming.
 */
function StepTrail({ current }: { current: Step }) {
  const index = STEPS.indexOf(current);
  return (
    <ol className="wizard-trail">
      {STEPS.map((s, i) => (
        <li
          key={s}
          className="wizard-trail-step"
          data-state={i < index ? "done" : i === index ? "current" : "todo"}
          aria-current={i === index ? "step" : undefined}
        >
          <span className="visually-hidden">{t("nas.wizard.step", i + 1, STEPS.length)}</span>
          <span aria-hidden="true" className="wizard-trail-index">
            {i + 1}
          </span>
          {STEP_LABEL[s]}
        </li>
      ))}
    </ol>
  );
}

/* ── step 1 — the device ──────────────────────────────────────────────────── */

function DetailsStep({
  siteId,
  busy,
  error,
  onCancel,
  onSubmit,
}: {
  siteId: string;
  busy: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (body: Record<string, unknown>) => void;
}) {
  const [name, setName] = useState("");
  /*
   * Only the drivers this endpoint can actually register.
   *
   * A UniFi typed in here is a device Foyer holds no credential for, cannot
   * authorize a guest on, and has minted a secret for that the hardware has no field
   * to accept. The server refuses it; offering it in the select anyway would be a control
   * that fails on submit, which is the same lie one screen later.
   */
  const [driver, setDriver] = useState<Nas["driver"]>(MANUAL_DRIVERS[0]!);
  /*
   * **Nobody said**, and it is the initial state rather than a vendor.
   *
   * The empty string is the form's spelling of `radius_clients.coa_vendor IS NULL`,
   * and keeping it expressible here is the whole point of the field: whichever vendor
   * a form pre-selects is precisely the guess this question exists to prevent, and a
   * wrong guess is the one CoA failure that answers **success** — the device ACKs a
   * request it could not match. See `CoaVendor` in `openapi.yaml`, which gives the
   * same reason for the field being nullable rather than two more `NasDriverKind`
   * members.
   */
  const [coaVendor, setCoaVendor] = useState<CoaVendor | "">("");
  const [ipAddress, setIpAddress] = useState("");
  const [nameMissing, setNameMissing] = useState(false);
  const [addressMissing, setAddressMissing] = useState(false);

  /*
   * Whether *this* driver is asked which vendor it is — read from `DRIVER_SHAPES`,
   * never compared against `"coa"` here.
   *
   * The same table the mock refuses the body against and the server refuses it again
   * against, so the form asks exactly the question the two of them require. A fourth
   * copy of the rule is a copy that drifts, and the drift presents as a device that
   * registers cleanly and can never be sent a packet — `docs/rules/database.md`,
   * *derive the stand-in from what it stands in for*.
   */
  const asksVendor = needsCoaVendor(driver);

  const fieldErrors = fieldMessages(error);

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        /* An `aria-disabled` submit still submits, and Enter in a field submits
           without touching the button at all — so the in-flight guard lives here. */
        if (busy) return;
        /* Checked here as well as on the server. The round trip is not the point —
           a required field that only the server knows about is a field the operator
           learns about after committing. */
        const missingName = !name.trim();
        /*
         * **The address is required.**
         *
         * This registers a `clients.conf` row, and FreeRADIUS matches a packet to a
         * client by its source address and nothing else — so a row without one is a
         * client every Access-Request is discarded for. The column is `NOT NULL`, so
         * a form that left it optional would meet the server where they always meet:
         * a `500` for a field the form never marked required.
         *
         * Both checks run before either returns, so an operator who left two fields
         * blank is told about both at once rather than one per attempt.
         */
        const missingAddress = !ipAddress.trim();
        setNameMissing(missingName);
        setAddressMissing(missingAddress);
        if (missingName || missingAddress) return;
        /*
         * **The vendor is deliberately *not* pre-checked here, unlike the two above.**
         *
         * Those two are text an operator can leave blank by inattention, and a local
         * check saves them a round trip. This one is a question with a closed set of
         * answers, sitting on screen with *Not chosen* selected and its price stated
         * beside it — so nothing is learned after committing that was not visible
         * before it.
         *
         * What a local refusal would cost is the thing this field exists for. The
         * defect was a `422` marking `coaVendor` on a form with no such control, and
         * refusing here would make the server's refusal unreachable from the console's
         * own controls — the console would be covering the gap rather than closing it,
         * and no test could drive the case without rewriting a request. Sending the
         * body the operator actually filled in is what puts the refusal on the field
         * and keeps it drivable. See `web/e2e/tests/admin/coa-vendor.spec.ts`.
         */
        onSubmit({
          siteId,
          name: name.trim(),
          driver,
          ipAddress: ipAddress.trim(),
          /* Sent only where it is read. `coaVendor` on a driver that does not speak
             CoA is a value nothing reads, and the server drops one anyway — see
             `coaVendorIn` in `internal/apistore`. */
          ...(asksVendor ? { coaVendor } : {}),
        });
      }}
    >
      <WizardBody step="details">
        {/*
         * `ErrorNotice`, not a `Notice` with one sentence typed into it.
         *
         * A hardcoded `Notice` carrying `nas.create.failed.body` for **any** refusal
         * would leave a problem type with no render site, seen from the client side: the
         * contract has a `/problems/needs-controller` type, `problems.ts` has copy for
         * it, the mock emits it — and it would have nowhere to land, so a test that
         * forced the 422 would assert the absence of the sentence it exists to lock.
         *
         * The title override is what the primitive documents it for: the type says
         * **why** the request was refused and the screen says **which** request it was.
         * Naming both is what a single typed sentence cannot do: it names only the
         * second and then guesses at the first.
         *
         * It also carries `role="alert"`; without it the refusal is announced to
         * nobody at all.
         */}
        {error ? <ErrorNotice error={error} title={t("nas.create.failed.title")} /> : null}

        {/* Said where the operator is looking, with somewhere to go. */}
        <Notice level="info" title={t("nas.create.viaController")}>
          <Link className="btn btn-sm" to="/controllers">
            {t("nas.create.goToControllers")}
          </Link>
        </Notice>

        <div className="form-grid">
          <div className="field">
            <label htmlFor="nas-name">{t("nas.create.name")}</label>
            <input
              id="nas-name"
              value={name}
              autoComplete="off"
              aria-invalid={nameMissing || Boolean(fieldErrors.name) || undefined}
              aria-describedby={
                nameMissing || fieldErrors.name ? "nas-name-error" : "nas-name-hint"
              }
              onChange={(e) => {
                setName(e.target.value);
                if (nameMissing) setNameMissing(false);
              }}
            />
            {nameMissing ? (
              <p className="field-error" role="alert" id="nas-name-error">
                {t("nas.create.nameRequired")}
              </p>
            ) : fieldErrors.name ? (
              <p className="field-error" role="alert" id="nas-name-error">
                {fieldErrors.name}
              </p>
            ) : (
              <p className="field-hint" id="nas-name-hint">
                {t("nas.create.nameHint")}
              </p>
            )}
          </div>

          <div className="field">
            <label htmlFor="nas-driver">{t("nas.create.driver")}</label>
            {/*
             * Marked when the server refuses the driver, even though this select cannot
             * normally produce such a refusal — `MANUAL_DRIVERS` filters out every
             * driver whose controller is `required`, and that is correct.
             *
             * The window it is for is a console older than the server it is talking to:
             * `DRIVER_SHAPES` is compiled into this bundle, an operator's tab outlives
             * a deployment, and a driver that gains a controller is exactly the change
             * that moves one entry from this list to the other side. When that happens
             * the refusal names `driver`, and an unmarked select would leave the
             * operator reading a correct sentence with no idea which control it is
             * about.
             */}
            <select
              id="nas-driver"
              value={driver}
              aria-invalid={Boolean(fieldErrors.driver) || undefined}
              aria-describedby={fieldErrors.driver ? "nas-driver-error" : undefined}
              onChange={(e) => setDriver(e.target.value as Nas["driver"])}
            >
              {MANUAL_DRIVERS.map((k) => (
                <option key={k} value={k}>
                  {DRIVER_LABEL[k]}
                </option>
              ))}
            </select>
            {fieldErrors.driver ? (
              <p className="field-error" role="alert" id="nas-driver-error">
                {fieldErrors.driver}
              </p>
            ) : null}
          </div>

          {/*
           * **Which of the two CoA vendors**, and only where the driver names a family.
           *
           * Conditional rather than always-present-and-disabled: a control an operator
           * cannot use is a control they go looking for the setting that enables
           * (`docs/rules/frontend.md`). `asksVendor` comes from `DRIVER_SHAPES`, so
           * this appears for exactly the drivers the server refuses without it — and
           * would appear for a second one the day the table gains it, with no edit
           * here.
           *
           * It sits immediately under the select that summons it, because that select
           * is the only thing that explains why it appeared.
           *
           * The refusal path is `ipAddress`'s: `fieldErrors` keyed on the field name,
           * a `.field-error` carrying `role="alert"`, and `aria-describedby` naming it
           * — so the sentence the server sent about `coaVendor` lands *on* the control
           * that answers it. That is the half this whole field is for: without it, a
           * `422` marking `coaVendor` lands on a form with no such control, and an
           * operator reads a correct sentence about a field that is not on screen.
           */}
          {asksVendor ? (
            <div className="field">
              <label htmlFor="nas-coa-vendor">{t("nas.create.coaVendor")}</label>
              <select
                id="nas-coa-vendor"
                value={coaVendor}
                aria-invalid={Boolean(fieldErrors.coaVendor) || undefined}
                aria-describedby={
                  fieldErrors.coaVendor ? "nas-coa-vendor-error" : "nas-coa-vendor-hint"
                }
                onChange={(e) => setCoaVendor(e.target.value as CoaVendor | "")}
              >
                {/* Nobody-said, kept selectable rather than removed once a choice is
                    made: a form that cannot be put back into the state the database
                    can hold is a form that has already guessed. */}
                <option value="">{t("nas.create.coaVendorUnset")}</option>
                {COA_VENDORS.map((v) => (
                  <option key={v} value={v}>
                    {COA_VENDOR_LABEL[v]}
                  </option>
                ))}
              </select>
              {fieldErrors.coaVendor ? (
                <p className="field-error" role="alert" id="nas-coa-vendor-error">
                  {fieldErrors.coaVendor}
                </p>
              ) : (
                <p className="field-hint" id="nas-coa-vendor-hint">
                  {t("nas.create.coaVendorHint")}
                </p>
              )}
            </div>
          ) : null}

          <div className="field">
            <label htmlFor="nas-ip">{t("nas.create.ip")}</label>
            <input
              id="nas-ip"
              value={ipAddress}
              autoComplete="off"
              aria-invalid={addressMissing || Boolean(fieldErrors.ipAddress) || undefined}
              aria-describedby={
                addressMissing || fieldErrors.ipAddress ? "nas-ip-error" : "nas-ip-hint"
              }
              onChange={(e) => {
                setIpAddress(e.target.value);
                if (addressMissing) setAddressMissing(false);
              }}
            />
            {addressMissing ? (
              <p className="field-error" role="alert" id="nas-ip-error">
                {t("nas.create.ipRequired")}
              </p>
            ) : fieldErrors.ipAddress ? (
              <p className="field-error" role="alert" id="nas-ip-error">
                {fieldErrors.ipAddress}
              </p>
            ) : (
              <p className="field-hint" id="nas-ip-hint">
                {t("nas.create.ipHint")}
              </p>
            )}
          </div>
        </div>
      </WizardBody>

      <div className="dialog-foot">
        <button type="button" className="btn" onClick={onCancel}>
          {t("common.cancel")}
        </button>
        {/* `aria-disabled` rather than `disabled`: the operator pressed this, so it
            has focus, and `disabled` on a focused element blurs it to `<body>` for the
            whole request. Measured and written out at `Accounts.tsx`'s row action. */}
        <button type="submit" className="btn btn-primary" aria-disabled={busy || undefined}>
          {t("nas.create.submit")}
        </button>
      </div>
    </form>
  );
}

/* ── step 2 — the secret ──────────────────────────────────────────────────── */

/**
 * The only value in the product that exists once.
 *
 * There is no Cancel here and the backdrop does not dismiss — `Modal`'s `onClose`
 * is `leaveSecretStep`, so Escape, the scrim and the primary button are all the
 * same refusing function. The acknowledgement is a checkbox rather than an
 * inference from the copy button, because copying is not the only honest way to
 * store a secret and a flow that only accepted one of them would push an operator
 * into lying to it. Copying ticks it, since copying *is* storing it.
 *
 * **The reveal itself is `SecretReveal`, shared with the rotation on the device
 * screen.** Two renderings of one thing drift, and the drift that matters is one
 * showing the value and the other not. The step trail and the probe that follow
 * are what is specific to registration.
 */
function SecretStep({
  secret,
  acknowledged,
  pressed,
  ackRef,
  onAcknowledge,
  onContinue,
}: {
  secret: string;
  acknowledged: boolean;
  pressed: boolean;
  ackRef: React.RefObject<HTMLInputElement | null>;
  onAcknowledge: (value: boolean) => void;
  onContinue: () => void;
}) {
  return (
    <SecretReveal
      secret={secret}
      bodyLabel={STEP_LABEL.secret}
      continueLabel={t("nas.wizard.secret.continue")}
      acknowledged={acknowledged}
      pressed={pressed}
      ackRef={ackRef}
      onAcknowledge={onAcknowledge}
      onContinue={onContinue}
    />
  );
}

/* ── step 3 — what the device can do ──────────────────────────────────────── */

function ProbeStep({
  device,
  result,
  busy,
  failed,
  onRetry,
  onFinish,
}: {
  device: Nas;
  result: NasProbe | null;
  busy: boolean;
  failed: boolean;
  onRetry: () => void;
  onFinish: () => void;
}) {
  const findings = result ? probeFindings(result, device) : null;
  const reachable = result?.reachability === "ok";

  return (
    <>
      <WizardBody step="probe">
        {/*
         * A probe that fails is a product finding, not an error to hide, and the two
         * ways it fails have different fixes. `unreachable` is the network or the
         * address; `credentials-expired` is a key the operator can reissue in a
         * minute and is explicitly *not* a network fault. The device stays
         * registered either way, which is the sentence that stops an operator
         * deleting and re-registering it.
         */}
        {failed ? (
          <Notice level="urgent" title={t("nas.wizard.probe.failed.title")}>
            {t("nas.wizard.probe.failed.body")}
          </Notice>
        ) : null}
        {result?.reachability === "unreachable" ? (
          <Notice level="urgent" title={t("nas.notice.unreachable.title")}>
            {t("nas.notice.unreachable.body")}
          </Notice>
        ) : null}
        {result?.reachability === "credentials-expired" ? (
          <Notice level="urgent" title={t("nas.notice.credentialsExpired.title")}>
            {t("nas.notice.credentialsExpired.body")}
          </Notice>
        ) : null}

        {busy ? (
          <>
            <p className="findings-verdict" aria-live="polite">
              {t("nas.wizard.probe.busy")}
            </p>
            <p className="findings-note">{t("nas.wizard.probe.busyNote")}</p>
            <FindingsSkeleton count={FINDING_COUNT} />
          </>
        ) : null}

        {!busy && findings ? (
          <>
            <Verdict reachable={reachable} attention={attentionCount(findings)} />
            {/*
             * **The warnings the probe produced.**
             *
             * A registration screen ignoring them shows an operator a green tick over
             * unmeasured claims. The UniFi driver appends at least one on every run, so
             * this is the ordinary path rather than an edge.
             *
             * Above the findings on purpose. These are conditions an operator is being
             * asked to *accept* before pressing Done; a row in a table below the fold
             * is not where that decision gets made.
             *
             * **Two notices, not one.** A single list would show the same two items on
             * every UniFi probe and read as a fault report that never changes — see
             * `ProbeWarnings`. The rendering lives in `FindingsPanel`, shared with
             * `NasDetail`, because two copies of this would drift, which is what the
             * panel's own header says about the findings list beside it.
             */}
            {result ? <ProbeWarnings warnings={result.warnings} /> : null}
            <Findings findings={findings} />
          </>
        ) : null}

        {!busy ? <p className="findings-note">{t("nas.wizard.probe.stands")}</p> : null}
      </WizardBody>

      <div className="dialog-foot">
        {/* Retry is the control an operator presses on the step where the probe has
            just told them something went wrong — the worst place in the console to
            drop focus. `aria-disabled`, guarded handler. */}
        <button
          type="button"
          className="btn"
          onClick={() => {
            if (!busy) onRetry();
          }}
          aria-disabled={busy || undefined}
        >
          <Refresh size={14} />
          {t("nas.wizard.probe.retry")}
        </button>
        <button type="button" className="btn btn-primary" onClick={onFinish}>
          {t("nas.wizard.probe.done")}
        </button>
      </div>
    </>
  );
}
