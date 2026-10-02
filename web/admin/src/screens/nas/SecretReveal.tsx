import { useId, useState, type ReactNode, type RefObject } from "react";
import { t } from "../../i18n/nas.js";
import { Check, Copy } from "../../ui/icons.js";

/**
 * The one-time reveal of a RADIUS shared secret — **one component, two callers.**
 *
 * It is here because there are exactly two moments Foyer mints a secret an operator
 * has to carry to a device: registering one, and rotating one. Two renderings would
 * drift, and if only one of them showed the value every discovered UniFi, Omada and
 * Ruckus access point would be permanently unable to authenticate: Foyer generates
 * the secret, the API is write-only, and a console that did not display what the
 * server had just returned would leave nobody able to configure the device.
 *
 * **The write-only property is not weakened by any of this.** Nothing here reads a
 * secret back: it is handed the value the mutation just returned, which the
 * contract returns exactly once and no operation repeats. Two renderings would
 * drift apart — a registration reveal somebody later "simplifies" and a rotation
 * reveal nobody updates.
 *
 * The protection on this surface is deliberately not "don't add a Cancel button".
 * It is that the acknowledgement lives in the *caller*, so Escape, the primary
 * button and the backdrop are the same function, and that function refuses until
 * the operator says they have the value. Copying ticks it, because copying *is*
 * storing it; the checkbox is there for the operator who stored it some other way,
 * because a flow that only accepted one of those would push them into lying.
 */
export function SecretReveal({
  secret,
  bodyLabel,
  body,
  continueLabel,
  acknowledged,
  pressed,
  ackRef,
  onAcknowledge,
  onContinue,
}: {
  secret: string;
  /**
   * The accessible name for the scrolling region.
   *
   * A region that scrolls and cannot be focused cannot be scrolled from a keyboard
   * — WCAG 2.1.1, and axe reports it `serious`. Each caller passes its own name so
   * a screen reader user landing on the tab stop is told what they are about to
   * read rather than "group".
   */
  bodyLabel: string;
  /**
   * What to do with the value, which is not the same sentence twice. Registering by
   * hand means typing it into a device with a field for it; rotating a device whose
   * driver pushed the value means there is nothing to type.
   */
  body?: ReactNode;
  continueLabel: string;
  acknowledged: boolean;
  /** The acknowledgement was asked for and refused. Shows the reason. */
  pressed: boolean;
  ackRef: RefObject<HTMLInputElement | null>;
  onAcknowledge: (value: boolean) => void;
  /**
   * Leaving, by **any** route. The caller refuses until the acknowledgement holds,
   * so the dialog cannot be dismissed by the button and then by Escape.
   */
  onContinue: () => void;
}) {
  const ackId = useId();
  const [copied, setCopied] = useState(false);
  const [clipboardRefused, setClipboardRefused] = useState(false);

  const copy = () => {
    /*
     * The write is the first thing in the handler with nothing awaited before it —
     * Safari drops the permission if the call leaves the user-gesture task. The
     * promise is handled rather than voided here (unlike `CopyButton`) because the
     * acknowledgement turns on the copy having actually happened.
     */
    navigator.clipboard.writeText(secret).then(
      () => {
        setCopied(true);
        setClipboardRefused(false);
        onAcknowledge(true);
      },
      () => setClipboardRefused(true),
    );
  };

  return (
    <>
      <div className="dialog-body" tabIndex={0} role="group" aria-label={bodyLabel}>
        {body ?? (
          <div>
            <strong>{t("nas.wizard.secret.heading")}</strong> {t("nas.wizard.secret.body")}
          </div>
        )}

        <div className="secret">{secret}</div>

        <div className="secret-actions">
          <button type="button" className="btn btn-sm" onClick={copy} autoFocus>
            {copied ? <Check size={13} /> : <Copy size={13} />}
            {copied ? t("nas.wizard.secret.copied") : t("nas.wizard.secret.copy")}
          </button>
        </div>

        {clipboardRefused ? (
          <p className="field-error" role="alert">
            {t("nas.wizard.secret.copyRefused")}
          </p>
        ) : null}

        <div className="ack">
          <input
            ref={ackRef}
            id={ackId}
            type="checkbox"
            checked={acknowledged}
            onChange={(e) => onAcknowledge(e.target.checked)}
          />
          <div>
            <label htmlFor={ackId}>{t("nas.wizard.secret.ack")}</label>
            <p className="field-hint">{t("nas.wizard.secret.ackWhy")}</p>
            {pressed ? (
              <p className="field-error" role="alert">
                {t("nas.wizard.secret.ackRequired")}
              </p>
            ) : null}
          </div>
        </div>
      </div>

      {/*
        No Cancel. The only way out of a reveal is through it, and the button is the
        same function Escape runs.

        Disabled rather than removed, against the console's usual preference. The
        rule there is about *capabilities* — a control the hardware cannot honour
        must not exist, because an operator will go hunting for the setting that
        enables it. This one is the other case the design system already carves
        out: the action is real and the moment is wrong, so it is disabled with the
        reason stated beside it rather than in a tooltip.
      */}
      <div className="dialog-foot">
        <button
          type="button"
          className="btn btn-primary"
          onClick={onContinue}
          disabled={!acknowledged}
        >
          {continueLabel}
        </button>
      </div>
    </>
  );
}
