import type { Capabilities, Nas } from "@foyer/contract";
import { t } from "../../i18n/index.js";
import { Alert, Check, Slash } from "../../ui/icons.js";
import { Pill } from "../../ui/primitives.js";

/*
 * The small pieces every access-point surface shares.
 *
 * They live here rather than in `ui/` because they are about *this noun*: a garden
 * pill knows that an IP-range garden is materially weaker than an FQDN one, and
 * that is domain knowledge, not a design primitive. The list, the detail screen and
 * the registration wizard all render them, and the value of one home for them is
 * that the three cannot start disagreeing about how bad a finding is.
 *
 * `DRIVER_LABEL` lives in `admin/src/drivers.ts`, not here. It is the
 * one piece of this vocabulary a **refusal** also needs, and a refusal is rendered
 * from `problems.ts`, which importing this file would put in a cycle through
 * `ui/primitives.tsx`. See that file for why the split exists.
 */

export const GARDEN_LABEL: Record<NonNullable<Capabilities["walledGarden"]>, string> = {
  get none() {
    return t("nas.garden.none");
  },
  get "ip-range"() {
    return t("nas.garden.ipRange");
  },
  get "fqdn-sni"() {
    return t("nas.garden.fqdnSni");
  },
};

export function ReachabilityPill({ nas }: { nas: Pick<Nas, "reachability"> }) {
  switch (nas.reachability) {
    case "ok":
      return <Pill level="positive" label={t("nas.reach.ok")} />;
    case "unreachable":
      return <Pill level="urgent" label={t("nas.reach.unreachable")} icon={<Alert size={11} />} />;
    case "credentials-expired":
      return (
        <Pill level="urgent" label={t("nas.reach.credentialsExpired")} icon={<Alert size={11} />} />
      );
    default:
      return <Pill level="neutral" label={t("nas.reach.unknown")} />;
  }
}

export function MessageAuthPill({ nas }: { nas: Pick<Nas, "messageAuthenticator"> }) {
  switch (nas.messageAuthenticator) {
    case "enforced":
      return <Pill level="positive" label={t("nas.msgauth.enforced")} icon={<Check size={11} />} />;
    case "refused":
      return (
        <Pill
          level="warning"
          label={t("nas.msgauth.refused")}
          icon={<Alert size={11} />}
          title={t("nas.msgauth.refusedWhy")}
        />
      );
    default:
      return <Pill level="neutral" label={t("nas.msgauth.unknown")} />;
  }
}

/**
 * Accounting, in three values — because the record has three.
 *
 * `Nas.accountingConfigured` is optional and `store.RadiusClient.AccountingConfigured`
 * is a `*bool` whose comment says *"Nil is not measured"*. This rendered `!value`,
 * so a device nothing had probed got the pill that means **every session figure for
 * this device is absent forever** — a measurement, drawn from the absence of one.
 * The sibling above has had the three branches all along; this one is the same shape
 * for the same reason, and `neutral` rather than `caution-minor` because there is
 * nothing here to act on yet except the probe.
 */
export function AccountingPill({ nas }: { nas: Pick<Nas, "accountingConfigured"> }) {
  if (nas.accountingConfigured === undefined) {
    return (
      <Pill
        level="neutral"
        label={t("nas.accounting.notMeasured")}
        title={t("nas.accounting.notMeasuredWhy")}
      />
    );
  }
  return nas.accountingConfigured ? (
    <Pill level="positive" label={t("nas.accounting.configured")} />
  ) : (
    <Pill
      level="caution-minor"
      label={t("nas.accounting.missing")}
      title={t("nas.accounting.missingWhy")}
    />
  );
}

export function GardenPill({ capabilities }: { capabilities: Capabilities }) {
  const kind = capabilities.walledGarden ?? "none";
  if (kind === "none") {
    return <Pill level="warning" label={GARDEN_LABEL.none} icon={<Slash size={11} />} />;
  }
  if (kind === "ip-range") {
    return (
      <Pill
        level="caution-minor"
        label={GARDEN_LABEL["ip-range"]}
        // Not a stylistic preference: CDNs host arbitrary customers, so an
        // IP-range allowance degrades into a general-purpose HTTPS tunnel.
        title={t("nas.garden.ipRangeWhy")}
      />
    );
  }
  return <Pill level="positive" label={GARDEN_LABEL["fqdn-sni"]} />;
}

export function YesNo({ value }: { value: boolean | undefined }) {
  return value ? (
    <Pill level="positive" label={t("common.yes")} />
  ) : (
    <Pill level="neutral" label={t("common.no")} />
  );
}
