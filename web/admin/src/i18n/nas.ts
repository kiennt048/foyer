/**
 * Copy for the three `/access-points/…` surfaces that are loaded on demand: the
 * detail page, the registration wizard and the probe findings both of them render.
 *
 * The *list* at `/access-points` is eager, and its copy is in `core.ts` — the split
 * is by what a browser downloads, not by what a route is called.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const nas = {
  "nas.reach.unavailable": "This device cannot be reached right now.",

  "nas.detail.probe": "Probe again",

  "nas.notice.unreachable.title": "Device unreachable",
  "nas.notice.unreachable.body":
    "Sessions cannot be authorized or revoked here until the device answers again. Sessions already running continue.",
  "nas.notice.credentialsExpired.title": "Controller API key expired",
  "nas.notice.credentialsExpired.body":
    "Issue a new key on the controller, then update it here. This is not a network fault.",
  "nas.notice.messageAuthenticator.title": "Device refuses Message-Authenticator",
  "nas.notice.messageAuthenticator.body":
    "This device is exposed to BlastRADIUS (CVE-2024-3596): an on-path attacker can forge an Access-Accept out of a genuine Access-Reject. Foyer still emits the safe configuration and never downgrades silently, so this warning stays until the device is replaced or its firmware is updated.",
  "nas.notice.noGarden.title": "Driver supports no walled garden",
  "nas.notice.noGarden.body":
    "The pre-authentication state on this device is not restricted to any destination. The portal itself still works normally.",
  "nas.notice.noAccounting.title": "No accounting profile attached",
  "nas.notice.noAccounting.body":
    "No session records arrive from this device. On the controller-API path there is no Access-Accept, so Foyer has no channel in which to ask for interim updates.",

  /*
   * The rotation's reveal. A key with no surface rendering it keeps a suite green
   * because the field exists, so these two are only right while something renders
   * them.
   */
  "nas.newSecret.title": "New shared secret",
  /* Stated once, in the same words the wizard uses, because it is the same
     property: the API is write-only and there is no default to fall back on. */
  "nas.newSecret.never":
    "No operation returns it, in this console or in the API, and Foyer ships no default to fall back on.",
  "nas.newSecret.continue": "Done — I have stored it",

  "nas.device.section": "Device",
  "nas.device.model": "Model",
  "nas.device.firmware": "Firmware",
  "nas.device.address": "Address",
  "nas.device.identifier": "NAS-Identifier",
  // Per-NAS, not a constant. 3799 is common; Cisco frequently uses 1700.
  "nas.device.coaPort": "CoA port",
  // For a driver whose enforcement is *not* CoA. The registration form sends no port,
  // so the two drivers it offers reach this row with a null and this sentence would be
  // false about them — see the call site.
  "nas.device.coaPortAbsent": "Does not use CoA.",
  "nas.device.coaPortDefault": "Not set — Foyer sends to 3799. Cisco often uses 1700.",
  /* Rendered only for the driver that reads it, and it is the value that decides
     whether a revocation reaches anything at all. */
  "nas.device.coaVendor": "CoA vendor",
  "nas.device.coaVendorUnset": "Not chosen",
  "nas.device.coaVendorUnsetWhy":
    "Foyer refuses to drive this device rather than guess a dialect. Naming one means registering it again.",

  "nas.secret.section": "RADIUS shared secret",
  "nas.secret.set": "Set",
  "nas.secret.unset": "Not set",
  "nas.secret.lastUsed": "Last used",
  "nas.secret.neverUsed": "Never used.",
  "nas.secret.note":
    "The secret is generated at registration and shown once. The API never returns a stored secret.",
  "nas.secret.rotate": "Generate new secret",

  "nas.capabilities.section": "Device capabilities",
  "nas.capabilities.emphasis":
    "What this device cannot do does not appear on the other screens.",
  "nas.capabilities.note":
    "These are measured, not declared. Foyer never renders a field it would silently ignore.",
  "nas.capabilities.bandwidth": "Bandwidth limits",
  "nas.capabilities.hardQuota": "Enforces quota itself",
  "nas.capabilities.hardQuotaNo": "— quota is sampled here, and can overshoot",
  "nas.capabilities.dynamicVlan": "Dynamic VLAN",
  "nas.capabilities.liveReauthorize": "Change limits mid-session",
  "nas.capabilities.liveReauthorizeNo": "— changes take effect when the guest reconnects",
  "nas.capabilities.rotateSecret": "Push a new shared secret",

  /*
   * Placed here rather than in `core.ts`: `EvidencePanel` is imported by
   * `NasDetail.tsx` and nothing else, so it is lazy and its copy is here. Keys put in
   * `core.ts` instead would still compile and pass every test while growing the entry
   * chunk — the thing the catalogue split exists to stop — so placement is checked by
   * where the screen imports `t` from.
   */

  /* ── how a device's claims are known — `getNasEvidence` ─────────────────
   *
   * Three words rather than two, because
   * the distinction between a citation and a measurement is the one that matters
   * most: an encoding that is right buys nothing if the device ignores the value
   * inside it.
   */
  "nas.evidence.section": "How this is known",
  "nas.evidence.standing.measured": "Measured",
  "nas.evidence.standing.documented": "Documented",
  "nas.evidence.standing.unverified": "Unverified",
  "nas.evidence.lead":
    "Two separate questions, and they have different answers. What Foyer will put on the wire is mostly written down; whether this device honours it has not been observed here.",
  "nas.evidence.capabilities": "What this driver claims it can do",
  "nas.evidence.capabilitiesWhy":
    "A capability decides whether a control appears on these screens at all. Anything marked Unverified is believed rather than observed — the note says what would settle it.",
  "nas.evidence.attributes": "What Foyer will send",
  "nas.evidence.attributesWhy":
    "The RADIUS attributes this device's vendor profile can emit. The standard ones are defined by RFC. The redirect attributes are not a standard — they are read from a vendor table, and no device of this make has answered one here.",
  "nas.evidence.vendorNotChosen":
    "No vendor has been chosen for this device, so Foyer cannot say what it would send. Cisco and Aruba identify a session by different attributes, and guessing wrong is worse than refusing: the device answers success and disconnects nobody.",

  // These go *somewhere*. A control that does nothing is worse than no control, and
  // most device-shaped settings live on another screen entirely: a rate limit belongs
  // to a plan, a VLAN to a policy, "apply now" to a session. What
  // belongs on a device is a statement of what the device can do — which the
  // capability list above already is — and a way to reach where the setting lives.
  "nas.action.bandwidth": "Set rate limits on a voucher batch",
  "nas.action.garden": "Edit the walled garden",

  "nas.probe.section": "Latest probe",
  "nas.probe.pass": "Pass",
  "nas.probe.fail": "Fail",
  "nas.probe.skipped": "Skipped",

  "nas.rotate.title": "Generate a new shared secret",
  "nas.rotate.verb": "Generate new secret",
  "nas.rotate.consequence":
    "This rewrites the FreeRADIUS configuration and restarts the process.",
  // The consequence operators do not expect, and the reason this is a dialog.
  "nas.rotate.consequenceEmphasis":
    "Every in-flight EAP conversation is dropped and the in-memory TLS session cache is cleared, so every 802.1X laptop on this site re-authenticates.",
  "nas.rotate.recoveryPush":
    "This driver can push the new secret to the device, so nobody has to enter it by hand.",
  "nas.rotate.recoveryManualEmphasis": "This driver cannot push the new secret.",
  "nas.rotate.recoveryManual":
    "The device stops authenticating until someone enters the new secret on it. Have access to the device before you continue.",

  /*
   * ── the reveal ────────────────────────────────────────────────────────────
   *
   * **These two sentences are the fix, and the split between them is the driver
   * rather than the action.**
   *
   * Where the driver pushed the value, the device already has it and there is
   * nothing to type. Saying so stops an operator walking to an access point to key
   * in a secret that is already there — and on a UniFi Network Application the
   * value lives in a controller-level RADIUS profile with no per-device field at
   * all, so "go and enter it on the AP" is advice that cannot be followed.
   *
   * Where the driver could not push it, this is the only screen in the product
   * where the value exists anywhere, and the manual procedure is stated here rather
   * than in the confirmation the operator has already dismissed.
   */
  /*
   * ── the three-way answer, stated before the press ──────────────────────────
   *
   * One sentence for "the driver cannot push" would be wrong for a device that has
   * never answered a probe: it would promise a rotation the server refuses. Three
   * states, because the server asks two questions — can the driver push it, and has
   * the device ever answered — and the copy answers both.
   *
   * `refused` names the `409` and what settles it. **Probe first** is the honest
   * recovery, and it is a real one: a device that answers becomes `manual` and the
   * rotation goes through. A note that only said "no" would leave the operator
   * thinking the product had decided their device is unrotatable.
   */
  "nas.rotate.blockedTitle": "Cannot rotate this device's secret",
  "nas.rotate.blockedBecause": (reach: string) =>
    `${reach} The driver cannot write the new secret to the device either, so nobody is known to be able to carry it there. Rotating would take the device off the network with no way to put the new value back on it — the server refuses this, and nothing changes until one of the two is fixed.`,
  "nas.rotate.blockedProbe": "Probe the device first",
  "nas.rotate.blockedOr": "or the controller credentials may be wrong",

  "nas.rotate.revealedPush":
    "Foyer wrote this onto the device itself, so there is nothing to enter by hand. Keep this copy: it is how you tell whether the two still match.",
  "nas.rotate.revealedManual":
    "Foyer could not write this onto the device. It is not authenticating anything until somebody enters this value there — have access to it before you continue.",

  /*
   * ── deleting an access point ───────────────────────────────────────────────
   *
   * **It does not say the device "can no longer authenticate", because that is false.**
   *
   * Measured against FreeRADIUS 3.2.10 with `read_clients = yes`: the row is gone
   * from `nas`, the sync has run, and the running server **still answers**. The
   * definition was discovered, so the server holds it cached; it stops on a restart.
   *
   * `docs/rules/frontend.md` forbids a UI that promises what the protocol cannot
   * deliver, and the rule it states there is about accounting figures — *"any figure
   * derived from accounting travels with its freshness"*. This is the same rule
   * applied to a button: the console must not imply an immediacy that does not exist.
   *
   * **No number here, and that is the whole point.** The cache window is
   * `DynamicClientLifetimeSeconds`, 300 by Foyer's generated default — but it is a
   * setting on a server this console does not read, the expiry has never actually
   * been waited out (`docs/protocol-reality.md` marks it `UNVERIFIED`), and a client
   * the server loaded at *startup* has no expiry at all. Writing "up to 5 minutes"
   * would dress a configured default as a measurement, which is the defect one step
   * sideways rather than a fix for it. So the copy says what is bounded — a restart —
   * and says plainly that it cannot give a number and why.
   */
  "nas.delete.title": "Delete access point",
  "nas.delete.verb": "Delete access point",
  "nas.delete.consequence":
    "This device is removed from Foyer. Its shared secret is destroyed and cannot be recovered.",
  // The consequence operators do not expect, and the reason this dialog changed.
  "nas.delete.consequenceEmphasis":
    "Deleting it is not a revocation. FreeRADIUS holds this client in memory, so the running server goes on authenticating the device after the row is gone.",
  "nas.delete.recoveryEmphasis": "Restart FreeRADIUS if it has to stop now.",
  "nas.delete.recovery":
    "That is the only bounded way. Left alone, a client FreeRADIUS discovered is dropped when its cached definition expires; how long that takes is a setting on a server Foyer does not read, and nobody has timed the expiry. A client the server loaded at startup has no expiry at all and is held until it restarts.",
  "nas.delete.records":
    "Existing session records stay — accounting data belongs to FreeRADIUS and is not deleted with the device. Reusing the device means registering it again with a new secret.",

  "nas.create.title": "Add access point",
  "nas.create.name": "Name",
  "nas.create.nameHint": "The name staff will see, for example “Level 2 — window side”.",
  "nas.create.nameRequired": "Give the device a name before registering it.",
  // The wizard only offers the drivers it can actually register. The others are
  // reached through their controller, and saying so where the operator is looking is
  // better than a select that accepts a choice the server refuses.
  "nas.create.viaController":
    "UniFi, Omada and Ruckus access points are read from their controller rather than added here. Add the controller and run discovery.",
  "nas.create.goToControllers": "Go to controllers",
  "nas.create.driver": "Device type",
  /* Asked for one driver of five, because `coa` is the one kind that names a family.
     "Not chosen" is a real option and the one the field opens on: whichever vendor a
     form pre-selects is exactly the guess this question exists to prevent. */
  "nas.create.coaVendor": "CoA vendor",
  "nas.create.coaVendorUnset": "Not chosen",
  "nas.create.coaVendorHint":
    "Cisco and Aruba identify a session by different attributes, so a request in the wrong dialect matches nothing — and many devices acknowledge it anyway, reporting a revocation that revoked nobody.",
  "nas.create.ip": "IP address",
  "nas.create.ipRequired": "Required — RADIUS identifies a device by its address.",
  /* The hint says what the field is *for* as well as what happens next. Saying only
     the second would read as optional beside an unmarked input, for a value whose
     database column is `NOT NULL`. */
  "nas.create.ipHint":
    "One address or a range, for example 192.0.2.10 or 192.0.2.0/24. Capabilities are probed after registration; until then Foyer assumes the device can do nothing, rather than the reverse.",
  "nas.create.submit": "Register device",
  "nas.create.secretTitle": "RADIUS shared secret",
  "nas.create.secretBody":
    "Enter this secret on the device. It is shown once — no API returns a stored secret, and Foyer ships no default.",
  "nas.create.secretDone": "I have copied it",
  // The title only. The body is the refusal's own, resolved from its type by
  // `problems.ts` — see the `ErrorNotice` in `RegisterWizard`. A second sentence here
  // meant every refusal read "Correct the form and register again", including the one
  // whose answer is to go and add a controller instead.
  "nas.create.failed.title": "The device was not registered",

  /* ── the registration wizard ──────────────────────────────────────────────── */

  /*
   * Three steps, and the operator is told so at the start. The middle step is the
   * one moment in the product that cannot be repeated, and an operator who does not
   * know a third step is coming reads the secret step as the end of the flow — which
   * is exactly when a dialog gets dismissed.
   */
  "nas.wizard.title": "Register an access point",
  "nas.wizard.step": (index: number, total: number) => `Step ${index} of ${total}`,
  "nas.wizard.step.details": "Device",
  "nas.wizard.step.secret": "Shared secret",
  "nas.wizard.step.probe": "What it can do",

  "nas.wizard.secret.heading": "This value is shown once",
  // Not "keep it safe". The consequence, in the order it happens.
  "nas.wizard.secret.body":
    "Enter it on the device. No operation returns it, in this console or in the API, and Foyer ships no default to fall back on.",
  "nas.wizard.secret.copy": "Copy secret",
  "nas.wizard.secret.copied": "Copied",
  "nas.wizard.secret.copyRefused":
    "The browser refused the clipboard. Select the value above and copy it by hand.",
  "nas.wizard.secret.ack": "I have stored this secret",
  "nas.wizard.secret.ackWhy":
    "Leaving without it leaves a device that is registered and cannot authenticate. The only way back is to generate another secret, which on some drivers means going to the device with it.",
  "nas.wizard.secret.ackRequired": "Confirm you have stored the secret. It is not shown again.",
  "nas.wizard.secret.continue": "Continue — probe the device",

  "nas.wizard.probe.busy": "Asking the device what it supports",
  "nas.wizard.probe.busyNote":
    "Capabilities are measured, never assumed. Until this answers, Foyer treats the device as able to do nothing.",
  "nas.wizard.probe.retry": "Probe again",
  "nas.wizard.probe.failed.title": "The probe did not run",
  // Distinct from "the device did not answer": this is Foyer's own request failing.
  "nas.wizard.probe.failed.body":
    "Foyer could not complete the probe, so nothing has been measured. The device stays registered with the secret it was given.",
  "nas.wizard.probe.stands":
    "The device is registered and its secret is stored. Probing again later changes neither.",
  "nas.wizard.probe.done": "Open this access point",

  "nas.wizard.verdict.clean": "Everything Foyer checked is supported on this device.",
  "nas.wizard.verdict.findings": (n: number) =>
    n === 1
      ? "One thing to know before you rely on this device."
      : `${n} things to know before you rely on this device.`,
  "nas.wizard.verdict.unmeasured":
    "Nothing below was measured. Foyer treats an unprobed device as able to do nothing rather than assuming the opposite.",

  /* ── probe findings ───────────────────────────────────────────────────────── */

  /*
   * One title and one consequence sentence per finding, composed **here** from the
   * structured probe result — `reachability`, `capabilities`, and the device's own
   * `messageAuthenticator` and `accountingConfigured`.
   *
   * Not from `NasProbe.checks[].name` / `.detail`. Those are sentences the server
   * writes, in the server's language, and they name the mechanism ("the device
   * accepts require_message_authenticator") rather than the consequence ("a forged
   * accept cannot be built"). A contract change could turn them into kinds; until
   * then the console composes its own, which is what the client should end up doing
   * anyway.
   */
  "nas.finding.reachability.title": "Contact with the device",
  "nas.finding.reachability.ok": "Foyer reached the device and read what follows from it.",
  "nas.finding.reachability.unreachable":
    "Foyer could not reach the device, so nothing else here has been measured. Registration stands and the secret is stored — this is a network or address problem, not a registration one.",
  "nas.finding.reachability.credentialsExpired":
    "The controller answered and rejected the API key. Issue a new key on the controller and update it here; the network is fine.",
  "nas.finding.reachability.unknown": "Not probed yet.",

  "nas.finding.messageAuthenticator.title": "Message-Authenticator",
  "nas.finding.messageAuthenticator.enforced":
    "The device accepts the attribute Foyer requires, so an Access-Accept cannot be forged out of a genuine reject.",
  "nas.finding.messageAuthenticator.refused":
    "This device is exposed to BlastRADIUS (CVE-2024-3596). Foyer emits the safe configuration regardless and never downgrades quietly, so this stays until the firmware is updated or the device is replaced.",

  "nas.finding.accounting.title": "Session accounting",
  "nas.finding.accounting.configured": "Session records will arrive from this device.",
  // The one on this page that cannot be fixed from this console, ever.
  "nas.finding.accounting.missing":
    "No session records will arrive from this device, so every session figure for it stays blank. On the controller-API path there is no Access-Accept in which to ask for them — attach an accounting profile on the controller instead.",

  "nas.finding.walledGarden.title": "Walled garden",
  "nas.finding.walledGarden.fqdnSni":
    "Traffic before authentication is restricted by name, so an allowance means one destination.",
  "nas.finding.walledGarden.ipRange":
    "Only IP ranges can be allowed here. One CDN range serves many unrelated customers, so an allowance is materially wider than it looks.",
  "nas.finding.walledGarden.none":
    "Traffic before authentication is not restricted to any destination on this device. The portal still works; the unauthenticated state is unbounded.",

  "nas.finding.hardQuota.title": "Quota enforcement",
  "nas.finding.hardQuota.yes": "The device enforces the allowance itself, so a limit stops a guest at the limit.",
  "nas.finding.hardQuota.no":
    "Quota is a sampled backstop here, not a cap. Counters move only on interim updates, floored at five minutes by most vendors, so a guest overshoots an allowance before Foyer can see it.",

  "nas.finding.bandwidthLimits.title": "Bandwidth limits",
  "nas.finding.bandwidthLimits.yes": "Rate limits can be applied per plan.",
  "nas.finding.bandwidthLimits.no": "Rate-limit fields do not appear for this device anywhere in the console.",

  "nas.finding.dynamicVlan.title": "Dynamic VLAN",
  "nas.finding.dynamicVlan.yes": "A VLAN can be assigned per identity.",
  "nas.finding.dynamicVlan.no": "VLAN assignment does not appear for this device anywhere in the console.",

  "nas.finding.liveReauthorize.title": "Changes to running sessions",
  "nas.finding.liveReauthorize.yes": "A changed limit can be pushed to sessions already running.",
  "nas.finding.liveReauthorize.no":
    "A changed limit takes effect when the guest reconnects. There is no apply-now for this device.",

  "nas.finding.rotateSecret.title": "Pushing a new shared secret",
  "nas.finding.rotateSecret.yes":
    "Foyer can write a new secret onto the device, so a rotation needs nobody standing at it.",
  // Manual, not impossible — so the control stays and the price is stated.
  "nas.finding.rotateSecret.no":
    "Foyer can generate a new secret but cannot write it to the device. Rotating means entering it there by hand, so plan a rotation for a time when someone can reach it.",

  /*
   * Deliberately does not name the cause. The two ways a probe fails to measure
   * something are different — nothing answered, or the controller answered and
   * rejected the key — and the reachability row above says which. Repeating a guess
   * at it on every row would be wrong on the expired-key path, where the device
   * answers perfectly well.
   */
  "nas.finding.unmeasured": "Not measured — Foyer could not read this from the device.",

  /*
   * The third standing, and it is neither of the other two.
   *
   * *Not measured* is a probe that could not run. This is a probe that ran and gave
   * an answer its driver cannot back with a measurement or a citation — the shipped
   * UniFi driver names three such values on **every** probe. The sentence says what
   * an operator can do about it, because "unverified" on its own reads as a defect
   * rather than as an honest limit.
   */
  "nas.finding.unverified":
    "Believed, not measured — no vendor documentation or lab measurement settles this " +
    "value, so treat it as unknown rather than as a guarantee.",

  // Pill labels. Everything else reuses the word the table and the detail screen
  // already use for the same fact, so two screens cannot name it differently.
  "nas.finding.verdict.supported": "Supported",
  "nas.finding.verdict.unsupported": "Not supported",
  "nas.finding.verdict.notMeasured": "Not measured",
  "nas.finding.verdict.unverified": "Unverified",
  "nas.finding.verdict.quotaDevice": "By the device",
  "nas.finding.verdict.quotaSampled": "Sampled",

  /*
   * ── probe warnings ─────────────────────────────────────────────────────────
   *
   * One sentence per `ProbeWarningKind`. The server sends the kind and the params;
   * these are the only place the prose exists, the same rule the alarm and audit
   * catalogues carry.
   *
   * The first matters most: the UniFi probe appends it when a client's access point
   * cannot be resolved, and without it a registration screen would show a green
   * tick over a check that was not made.
   */
  "nas.probeWarning.accessPointUnconfirmed":
    "The access point a guest's redirect claims is not checked against this controller — " +
    "its client records carry no field to compare it with. MAC, site and " +
    "already-authorized are checked.",
  "nas.probeWarning.versionBelowFloor": (version: string, minimum: string) =>
    `Running ${version}; this driver needs ${minimum} or newer.`,
  "nas.probeWarning.capabilitiesUnverified": (n: number) =>
    `${plural(n, "capability", "capabilities")} below ${n === 1 ? "is" : "are"} believed rather than measured.`,
  // A kind newer than this console, and the server sent no fallback either. Same
  // shape as `alarm.unknown` and `audit.unknown`: never blank, never a raw enum value.
  "nas.probeWarning.unknown": "A condition this console does not recognise.",

  /*
   * ── two headings, because these are two kinds of thing ─────────────────────
   *
   * One notice headed *"Before you rely on this"* holding all of them would repeat
   * the same items on every UniFi probe — the capabilities nobody has measured among
   * them — so an operator registering a second controller would already have read
   * the whole notice, and by the third would not read it at all. `findings.ts`
   * carries the argument.
   *
   * *"Always true of this driver"* rather than *"Known limitations"*: the point of the
   * heading is that **nothing here is about the device on the screen**, and an operator
   * has to be able to take that from the heading alone, because the heading is all
   * anyone reads the fourth time.
   */
  "nas.probeWarning.foundTitle": "Found on this device",
  "nas.probeWarning.standingTitle": "Always true of this driver",
  // Says the two things the list itself cannot: that there is nothing to do, and why
  // it is on the screen at all rather than hidden as noise.
  "nas.probeWarning.standingLead":
    "Not faults, and not about this device — they follow from what the vendor's API offers, and every probe of this driver reports them. They are here because a screen that left them out would be telling you a check happened.",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/nas.js"));

export const t = makeT({ ...core, ...nas });
