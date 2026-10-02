import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import type { Failure, PortalAuthorization, SessionDisconnectResult } from "@foyer/contract";
import { request } from "../api.js";
import { useCan } from "../auth.js";
import { relativeTime } from "../format.js";
import { t } from "../i18n/portalgrants.js";
import { useSite } from "../site.js";
import { ConfirmDialog, Empty, ErrorNotice, Notice } from "../ui/primitives.js";

/**
 * The portal's open authorizations, below the session list (review2:disconnect).
 *
 * A session is a `radacct` row, and on a network whose controller authorizes guests
 * with no RADIUS accounting profile there is none — the guest is online and the list
 * above is empty, so there was nothing to disconnect. UniFi's Hotspot Manager and
 * Omada's portal list put *unauthorize* on the authorization record; this is that
 * record. `canDisconnect` is the server reading the driver; a guest whose device is
 * not a controller's is a RADIUS session, and the row says where to end it instead.
 *
 * Lazily loaded out of the sessions screen with its own copy namespace, so the entry
 * pays for the import edge and not for the sentences.
 */

type Page = { items: PortalAuthorization[]; nextCursor: string | null };

const KEY = "portal-grants";

/** The sentence for a controller that did not confirm, from the failure's params. */
export function grantFailureSentence(f: Failure | undefined): string {
  switch (f?.params?.cause) {
    case "refused":
      return t("grants.cause.refused");
    case "no-answer":
      return t("grants.cause.no-answer");
    case "bad-secret":
      return t("grants.cause.bad-secret");
    default:
      return t("grants.cause.error");
  }
}

export function GrantOutcome({ result }: { result: SessionDisconnectResult }) {
  return result.disconnected ? (
    <Notice level="positive" role="status" title={t("grants.done")} />
  ) : (
    <Notice level="negative" role="alert" title={t("grants.failed")}>
      <p>{grantFailureSentence(result.failure)}</p>
    </Notice>
  );
}

export default function PortalGrants() {
  const { siteId } = useSite();
  const mayManage = useCan("sessions:manage");
  const mayRevokeVoucher = useCan("vouchers:manage");
  const qc = useQueryClient();
  const optionId = useId();
  const list = useQuery({
    queryKey: [KEY, siteId],
    queryFn: () => request<Page>(`/portal-grants?siteId=${encodeURIComponent(siteId)}&limit=200`),
  });

  const [target, setTarget] = useState<PortalAuthorization | null>(null);
  const [revokeVoucher, setRevokeVoucher] = useState(false);
  const [result, setResult] = useState<SessionDisconnectResult | null>(null);
  const run = useMutation({
    mutationFn: (g: PortalAuthorization) =>
      request<SessionDisconnectResult>(`/portal-grants/${encodeURIComponent(g.id)}/disconnect`, {
        method: "POST",
        body: JSON.stringify({ revokeVoucher: mayRevokeVoucher && revokeVoucher && Boolean(g.voucherId) }),
      }),
    onSuccess: (r) => {
      setResult(r);
      setTarget(null);
      void qc.invalidateQueries({ queryKey: [KEY] });
      if (r.voucherRevoked) void qc.invalidateQueries({ queryKey: ["vouchers"] });
    },
  });

  const items = list.data?.items ?? [];
  return (
    <section className="card" id="portal-grants" aria-labelledby="portal-grants-title">
      <h2 id="portal-grants-title">{t("grants.title")}</h2>
      <p className="field-hint">{t("grants.body")}</p>
      {result ? <GrantOutcome result={result} /> : null}
      {list.error ? <ErrorNotice error={list.error} title={t("grants.title")} /> : null}
      {list.data && items.length === 0 ? <Empty title={t("grants.empty")} /> : null}
      {items.length > 0 ? (
        /* In `.table-scroll`, as every list in the console is: at 390px this table is
           660px, and without a scroll port of its own the whole page scrolled
           sideways (admin-narrow/responsive-shell.spec.ts). */
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th scope="col">{t("grants.col.device")}</th>
                <th scope="col">{t("grants.col.method")}</th>
                <th scope="col">{t("grants.col.granted")}</th>
                <th scope="col">{t("grants.col.expires")}</th>
                <th scope="col">{t("grants.col.via")}</th>
                <th scope="col" />
              </tr>
            </thead>
            <tbody>
              {items.map((g) => (
                <tr key={g.id}>
                  <td className="mono">{g.clientMac}</td>
                  <td>{t(`grants.method.${g.method}`)}</td>
                  <td>{relativeTime(g.grantedAt) ?? g.grantedAt}</td>
                  <td>{g.expiresAt ? (relativeTime(g.expiresAt) ?? g.expiresAt) : t("grants.noExpiry")}</td>
                  <td>{g.deviceName ?? ""}</td>
                  <td>
                    {!mayManage ? null : g.canDisconnect ? (
                      <button
                        type="button"
                        className="btn btn-sm btn-danger"
                        onClick={() => {
                          setRevokeVoucher(false);
                          setResult(null);
                          setTarget(g);
                        }}
                      >
                        {t("grants.disconnect")}
                      </button>
                    ) : (
                      <span className="field-hint">{t("grants.cannot")}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <ConfirmDialog
        open={target !== null}
        tier="confirm"
        title={t("grants.confirm.title")}
        verb={t("grants.disconnect")}
        busy={run.isPending}
        error={run.error}
        consequence={t("grants.confirm.consequence", target?.clientMac ?? "")}
        recovery={
          <>
            <p>{t("grants.confirm.recovery")}</p>
            {mayRevokeVoucher && target?.voucherId ? (
              <div className="ack">
                <input
                  id={optionId}
                  type="checkbox"
                  checked={revokeVoucher}
                  onChange={(e) => setRevokeVoucher(e.target.checked)}
                />
                <label htmlFor={optionId}>{t("grants.option.voucher")}</label>
              </div>
            ) : null}
          </>
        }
        onConfirm={() => target && run.mutate(target)}
        onCancel={() => {
          setTarget(null);
          run.reset();
        }}
      />
    </section>
  );
}
