import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api.js";
import { relativeTime } from "../format.js";
import { t } from "../i18n/wifikey.js";
import { fieldMessages } from "../problems.js";
import { ErrorNotice, Notice } from "../ui/primitives.js";

/**
 * The guest network's WPA password, for the voucher slip, set in the console like
 * every other operator setting. Rendered by `/portal` (a lazy chunk)
 * beside the network name, because the two together are what a slip joins.
 *
 * **Write-only.** This reads `getGuestWifiKey`, which answers whether a password is
 * set and when, and never the password; the field starts empty every time and is
 * cleared on save. The slip reads it through `getVoucherSlipWifi`, the one operation
 * that returns it. No `<form>`: this sits inside the portal settings' own form, and a
 * nested form is not one — the same arrangement as `PhoneSignInPanel`.
 */
export function GuestWifiKey({ siteId, canManage }: { siteId: string; canManage: boolean }) {
  const qc = useQueryClient();
  const id = useId();
  const status = useQuery({ queryKey: ["guest-wifi-key", siteId], queryFn: () => api.guestWifiKey(siteId) });
  const [value, setValue] = useState("");
  const [done, setDone] = useState<"saved" | "cleared" | null>(null);

  const write = useMutation({
    mutationFn: (passphrase: string | null) => api.updateGuestWifiKey(siteId, passphrase),
    onSuccess: (next, passphrase) => {
      qc.setQueryData(["guest-wifi-key", siteId], next);
      // The slip's read, so a batch page already open prints the new password.
      void qc.invalidateQueries({ queryKey: ["slip-wifi", siteId] });
      setValue("");
      setDone(passphrase === null ? "cleared" : "saved");
    },
  });
  const error = fieldMessages(write.error).passphrase;

  if (status.error) return <ErrorNotice error={status.error} title={t("wifiKey.label")} />;
  const s = status.data;
  const when = s?.updatedAt ? (relativeTime(s.updatedAt) ?? s.updatedAt) : "";

  return (
    <div className="field">
      <label htmlFor={`${id}-key`}>{t("wifiKey.label")}</label>
      <p className="field-hint">{t("wifiKey.hint")}</p>
      {write.error && !error ? <ErrorNotice error={write.error} title={t("wifiKey.failed")} /> : null}
      {done ? (
        <Notice level="positive" role="status" title={t(done === "saved" ? "wifiKey.saved" : "wifiKey.cleared")} />
      ) : null}
      <p className="field-hint" id={`${id}-status`}>
        {s?.set ? t("wifiKey.set", when, s.updatedBy ?? "") : t("wifiKey.unset")}
      </p>
      {canManage ? (
        <>
          <input
            id={`${id}-key`}
            type="password"
            autoComplete="new-password"
            value={value}
            aria-invalid={Boolean(error) || undefined}
            aria-describedby={`${id}-status${error ? ` ${id}-error` : ""}`}
            onChange={(e) => {
              setDone(null);
              setValue(e.target.value);
            }}
          />
          {error ? (
            <p className="field-error" role="alert" id={`${id}-error`}>
              {error}
            </p>
          ) : null}
          {s?.set ? <p className="field-hint">{t("wifiKey.rotates")}</p> : null}
          <div className="row-actions">
            <button
              type="button"
              className="btn"
              aria-disabled={write.isPending || value === "" || undefined}
              onClick={() => {
                if (write.isPending || value === "") return;
                write.mutate(value);
              }}
            >
              {s?.set ? t("wifiKey.replace") : t("wifiKey.save")}
            </button>
            {s?.set ? (
              <button
                type="button"
                className="btn btn-ghost btn-danger"
                aria-disabled={write.isPending || undefined}
                onClick={() => {
                  if (write.isPending) return;
                  write.mutate(null);
                }}
              >
                {t("wifiKey.clear")}
              </button>
            ) : null}
          </div>
        </>
      ) : null}
    </div>
  );
}
