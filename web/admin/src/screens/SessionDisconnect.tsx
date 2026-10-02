import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { createPortal } from "react-dom";
import type { Failure, Nas, Session, SessionDisconnectResult } from "@foyer/contract";
import { api } from "../api.js";
import { useCan } from "../auth.js";
import { t } from "../i18n/disconnect.js";
import { useSite } from "../site.js";
import { ConfirmDialog, Notice } from "../ui/primitives.js";

/**
 * The disconnect control in the session dock — `disconnectSession`.
 *
 * **Not an edit of the record.** The dock stays read-only: nothing here writes a
 * session, and the device's own Acct-Stop is what turns the row `ended`. This is an
 * action on the device, gated like every other one on the capability the device
 * reported (`Capabilities.canDeauthGuest`), and the server re-asks the driver
 * before it acts.
 *
 * Lazily loaded out of the dock, with its own copy namespace, so the entry chunk pays
 * for the import edge and not for the sentences.
 *
 * The confirmation is portalled to `<body>`: its two options are checkboxes, and the
 * dock is asserted in `web/e2e` to contain no data-entry control at all.
 */

/** What the dock offers for one session. Pure, so it can be proved without a DOM. */
export type DisconnectOffer =
  | { kind: "hidden" }
  | { kind: "offer" }
  | { kind: "cannot"; reason: "ruckus" | "omada" | "unprobed" };

/**
 * The gate, stated once.
 *
 * - An ended session offers nothing: there is nothing to end.
 * - No `sessions:manage`, nothing — the server would refuse, and a control the
 *   server refuses is the defect `OperatorPermission` exists to prevent.
 * - A device the list did not return offers nothing and says nothing: `null` is
 *   *not established*, and inventing a refusal for it would be a fact in the
 *   direction of alarm.
 * - `canDeauthGuest` false is a **reason**, said beside the missing control.
 */
export function disconnectOffer(
  session: Pick<Session, "state">,
  nas: Pick<Nas, "driver" | "capabilities"> | null,
  mayManage: boolean,
): DisconnectOffer {
  if (session.state === "ended" || !mayManage || !nas) return { kind: "hidden" };
  if (nas.capabilities.canDeauthGuest) return { kind: "offer" };
  if (nas.driver === "ruckus") return { kind: "cannot", reason: "ruckus" };
  if (nas.driver === "omada") return { kind: "cannot", reason: "omada" };
  return { kind: "cannot", reason: "unprobed" };
}

/** The sentence for a device that did not end the session, from the failure's params. */
export function failureSentence(f: Failure | undefined): string {
  const p = f?.params ?? {};
  switch (p.cause) {
    case "refused":
      return t("disconnect.cause.refused", typeof p.errorCause === "number" ? p.errorCause : null);
    case "no-answer":
      return t("disconnect.cause.no-answer");
    case "bad-secret":
      return t("disconnect.cause.bad-secret");
    default:
      return t("disconnect.cause.error");
  }
}

export function DisconnectPanel({
  offer,
  result,
  onOpen,
}: {
  offer: DisconnectOffer;
  result: SessionDisconnectResult | null;
  onOpen: () => void;
}) {
  const withdrawn = result ? t("disconnect.withdrawn", result.voucherRevoked, result.grantsRevoked) : "";
  return (
    <div className="detail-disconnect" data-role="disconnect">
      {result ? (
        result.disconnected ? (
          <Notice level="positive" title={t("disconnect.done.title")} role="status">
            {t("disconnect.done.body")} {withdrawn}
          </Notice>
        ) : (
          <Notice level="negative" title={t("disconnect.failed.title")} role="alert">
            {failureSentence(result.failure)} {withdrawn}
          </Notice>
        )
      ) : null}
      {offer.kind === "offer" && !result?.disconnected ? (
        <button type="button" className="btn btn-sm btn-danger" onClick={onOpen}>
          {t("disconnect.button")}
        </button>
      ) : null}
      {offer.kind === "cannot" ? (
        <Notice level="neutral" title={t("disconnect.cannot.title")}>
          {t(`disconnect.cannot.${offer.reason}`)}
        </Notice>
      ) : null}
    </div>
  );
}

export default function SessionDisconnect({ session }: { session: Session }) {
  const { siteId } = useSite();
  const mayManage = useCan("sessions:manage");
  /* The voucher option is `revokeVoucher`'s act and needs its permission as well;
     the server refuses it without one, so it is not offered without one. */
  const mayRevokeVoucher = useCan("vouchers:manage");
  const voucherId = useId();
  const grantId = useId();
  const qc = useQueryClient();
  /* The same query, key and all, the sessions screen already issued for the dock's
     capabilities — so this is a cache read, not a second request. */
  const nasQuery = useQuery({
    queryKey: ["nas", siteId, "capabilities"],
    queryFn: () => api.listNas({ siteId, limit: 200 }),
    staleTime: 60_000,
  });
  const nas = nasQuery.data?.items.find((n) => n.id === session.nasId) ?? null;
  const offer = disconnectOffer(session, nas, mayManage);

  const [confirming, setConfirming] = useState(false);
  const [revokeVoucher, setRevokeVoucher] = useState(false);
  const [revokeGrant, setRevokeGrant] = useState(false);
  const [result, setResult] = useState<{ id: string; r: SessionDisconnectResult } | null>(null);

  const run = useMutation({
    mutationFn: () =>
      api.disconnectSession(session.id, { revokeVoucher: mayRevokeVoucher && revokeVoucher, revokeGrant }),
    onSuccess: (r) => {
      setResult({ id: session.id, r });
      setConfirming(false);
      void qc.invalidateQueries({ queryKey: ["sessions"] });
      void qc.invalidateQueries({ queryKey: ["session"] });
      if (r.voucherRevoked) void qc.invalidateQueries({ queryKey: ["vouchers"] });
    },
  });

  /* A result belongs to the session it was for; `J`/`K` move the dock on. */
  const shown = result?.id === session.id ? result.r : null;
  if (offer.kind === "hidden" && !shown) return null;

  const dialog = (
    <ConfirmDialog
      open={confirming}
      tier="confirm"
      title={t("disconnect.confirm.title")}
      verb={t("disconnect.confirm.verb")}
      busy={run.isPending}
      error={run.error}
      consequence={t("disconnect.confirm.consequence", session.callingStationId)}
      recovery={
        <>
          <p>{t("disconnect.confirm.recovery")}</p>
          {mayRevokeVoucher ? (
            <div className="ack">
              <input
                id={voucherId}
                type="checkbox"
                data-role="disconnect-revoke-voucher"
                checked={revokeVoucher}
                onChange={(e) => setRevokeVoucher(e.target.checked)}
              />
              <label htmlFor={voucherId}>{t("disconnect.option.voucher")}</label>
            </div>
          ) : null}
          <div className="ack">
            <input
              id={grantId}
              type="checkbox"
              data-role="disconnect-revoke-grant"
              checked={revokeGrant}
              onChange={(e) => setRevokeGrant(e.target.checked)}
            />
            <label htmlFor={grantId}>{t("disconnect.option.grant")}</label>
          </div>
        </>
      }
      onConfirm={() => run.mutate()}
      onCancel={() => {
        setConfirming(false);
        run.reset();
      }}
    />
  );

  return (
    <>
      <DisconnectPanel offer={offer} result={shown} onOpen={() => setConfirming(true)} />
      {offer.kind === "offer" && typeof document !== "undefined" ? createPortal(dialog, document.body) : null}
    </>
  );
}
