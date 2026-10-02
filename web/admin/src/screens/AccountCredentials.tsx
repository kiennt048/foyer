import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import type { Account } from "@foyer/contract";
import { api } from "../api.js";
import { t } from "../i18n/credentials.js";
import { fieldMessages } from "../problems.js";
import { useSite } from "../site.js";
import { ErrorNotice, Modal } from "../ui/primitives.js";

/**
 * The two account dialogs, as one lazy chunk of `/accounts`.
 *
 * `/accounts` itself stays eager (`App.tsx` says why: it is where a leaver is
 * disabled), and its list is what an operator opens it for. These two are opened
 * for a reason and then closed, so they load on the first press. That keeps room in
 * the entry for the 802.1X opt-in: without it the per-account opt-in
 * (`store.AccountRepo.OptInTo8021X`) has no surface at all, and no guest account can
 * sign in over RADIUS.
 *
 * **The opt-in is made from the password typed in the same dialog**, because it is a
 * bcrypt verifier and Foyer keeps no password to make it from later —
 * `updateAccount` refuses the opt-in without one, on `password`.
 */
export function CreateAccountDialog({ onClose }: { onClose: () => void }) {
  const { siteId } = useSite();
  const qc = useQueryClient();
  /* The name a screen reader announces for this dialog. `labelledBy` rather than a
     `label` string, so the announced name is the same string that is on screen. */
  const titleId = useId();
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [dot1x, setDot1x] = useState(false);

  const create = useMutation({
    mutationFn: () => api.createAccount({ siteId, username, displayName, password, eapTtlsPap: dot1x }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["accounts"] });
      onClose();
    },
  });

  const fieldErrors = fieldMessages(create.error);

  return (
    <Modal className="no-print" labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {t("accounts.create.title")}
          </h2>
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            /* The guard belongs here rather than on the button: an `aria-disabled`
               submit still submits, and Enter in any field submits without it. */
            if (create.isPending) return;
            create.mutate();
          }}
        >
          <div className="dialog-body">
            {/* A `409` or a `500` names no field, and would otherwise render nothing. */}
            {create.error ? (
              <ErrorNotice error={create.error} title={t("accounts.create.failed.title")} />
            ) : null}
            <div className="form-grid">
              <div className="field">
                <label htmlFor="username">{t("accounts.create.username")}</label>
                <input
                  id="username"
                  value={username}
                  autoComplete="off"
                  aria-invalid={Boolean(fieldErrors.username) || undefined}
                  aria-describedby={fieldErrors.username ? "username-error" : undefined}
                  onChange={(e) => setUsername(e.target.value)}
                />
                {fieldErrors.username ? (
                  <p className="field-error" role="alert" id="username-error">
                    {fieldErrors.username}
                  </p>
                ) : null}
              </div>
              <div className="field">
                <label htmlFor="displayName">{t("accounts.create.displayName")}</label>
                <input id="displayName" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
              </div>
              <PasswordField
                id="password"
                label={t("accounts.create.password")}
                hint={t("accounts.create.passwordHint")}
                value={password}
                error={fieldErrors.password}
                onChange={setPassword}
              />
              <Dot1xField checked={dot1x} error={fieldErrors.eapTtlsPap} onChange={setDot1x} />
            </div>
          </div>
          <div className="dialog-foot">
            <button type="button" className="btn" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button type="submit" className="btn btn-primary" aria-disabled={create.isPending || undefined}>
              {t("accounts.create.submit")}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}

/**
 * A new password and the 802.1X opt-in, for one account.
 *
 * The checkbox starts at the account's state, read from `credentialKind` — the
 * `LEFT JOIN`, not a flag. Only what changed is sent: an unchanged box and an empty
 * password send nothing, so saving cannot turn 802.1X off by accident.
 */
export function AccountCredentialsDialog({
  account,
  onClose,
  onSaved,
}: {
  account: Account;
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const qc = useQueryClient();
  const titleId = useId();
  const had = account.credentialKind.includes("+bcrypt");
  const [password, setPassword] = useState("");
  const [dot1x, setDot1x] = useState(had);

  const save = useMutation({
    mutationFn: () =>
      api.updateAccount(account.id, {
        ...(password ? { password } : {}),
        ...(dot1x !== had ? { eapTtlsPap: dot1x } : {}),
      }),
    onSuccess: (a) => {
      void qc.invalidateQueries({ queryKey: ["accounts"] });
      onSaved(t("credentials.saved", a.username));
      onClose();
    },
  });
  const fieldErrors = fieldMessages(save.error);
  const nothing = !password && dot1x === had;

  return (
    <Modal className="no-print" labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="dialog-head">
          <h2 className="dialog-title" id={titleId}>
            {t("credentials.title", account.username)}
          </h2>
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (save.isPending || nothing) return;
            save.mutate();
          }}
        >
          <div className="dialog-body">
            {save.error && !fieldErrors.password && !fieldErrors.eapTtlsPap ? (
              <ErrorNotice error={save.error} title={t("credentials.failed")} />
            ) : null}
            <div className="form-grid">
              <PasswordField
                id="credentials-password"
                label={t("credentials.password")}
                hint={t("credentials.passwordHint")}
                value={password}
                error={fieldErrors.password}
                onChange={setPassword}
              />
              <Dot1xField checked={dot1x} error={fieldErrors.eapTtlsPap} onChange={setDot1x} />
            </div>
          </div>
          <div className="dialog-foot">
            <button type="button" className="btn" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button
              type="submit"
              className="btn btn-primary"
              aria-disabled={save.isPending || nothing || undefined}
            >
              {t("credentials.submit")}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}

function PasswordField({
  id,
  label,
  hint,
  value,
  error,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  value: string;
  error?: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <input
        id={id}
        type="password"
        value={value}
        autoComplete="new-password"
        aria-invalid={Boolean(error) || undefined}
        aria-describedby={error ? `${id}-hint ${id}-error` : `${id}-hint`}
        onChange={(e) => onChange(e.target.value)}
      />
      <p className="field-hint" id={`${id}-hint`}>
        {hint}
      </p>
      {error ? (
        <p className="field-error" role="alert" id={`${id}-error`}>
          {error}
        </p>
      ) : null}
    </div>
  );
}

function Dot1xField({
  checked,
  error,
  onChange,
}: {
  checked: boolean;
  error?: string;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="field">
      <label>
        <input
          type="checkbox"
          checked={checked}
          aria-describedby="dot1x-hint"
          aria-invalid={Boolean(error) || undefined}
          onChange={(e) => onChange(e.target.checked)}
        />{" "}
        {t("credentials.dot1x")}
      </label>
      <p className="field-hint" id="dot1x-hint">
        {t("credentials.dot1xHint")}
      </p>
      {error ? (
        <p className="field-error" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  );
}
