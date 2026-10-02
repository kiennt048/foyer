import { useId, useMemo, useState } from "react";
import { t } from "../i18n/index.js";
import { Modal } from "../ui/primitives.js";
import { filterSections, shortcutSections } from "./bindings.js";
import { BindingKeys } from "./Keys.jsx";
import { useShortcuts } from "./registry.js";

/**
 * The shortcut reference, generated from the registry.
 *
 * It lists what the screen you are on has registered — not a hand-written table that
 * drifts. That is also why it is searchable by key as well as by label: an operator
 * who half-remembers pressing something can type `esc` and find out what it did.
 */
export function ShortcutSheet({ onClose }: { onClose: () => void }) {
  const registrations = useShortcuts();
  const [query, setQuery] = useState("");
  const titleId = useId();

  /* No `useKeyboardSuspend(true)` here: `Modal` owns it. A suspend each overlay has
     to remember is one most of them forget, and a forgotten one dispatches `g` `d`
     through the dialog — so the mechanism lives in the thing every dialog already
     is. */

  const sections = useMemo(
    () => filterSections(shortcutSections(registrations.map((r) => r.spec)), query),
    [registrations, query],
  );

  return (
    <Modal className="sheet" labelledBy={titleId} onClose={onClose}>
      <div className="dialog-card">
        <div className="sheet-head">
          <h2 className="dialog-title" id={titleId}>
            {t("keys.sheet.title")}
          </h2>
          {/* `type="text"`, not `type="search"`. A search input carries the user
              agent's own meaning for Escape and can swallow the press before it
              becomes a close request, so the sheet would not close on Escape at all.

              `data-escape-clears` is how the staged behaviour is declared.
              `stopPropagation` in the handler below would do nothing:
              React delegates to the root container, an ancestor of the dialog, so
              the dialog's own listener has already closed the sheet by the time
              this runs. `Modal` reads the attribute and declines to treat the press
              as a close request; the handler below still does the clearing, because
              only this component owns the query. */}
          <input
            type="text"
            autoFocus
            className="sheet-search"
            data-escape-clears=""
            placeholder={t("keys.sheet.search")}
            value={query}
            aria-label={t("keys.sheet.search")}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              // Focus and escape-to-blur are a pair: clear first, keep focus, and
              // let the second press — on an empty field — close the sheet.
              if (e.key === "Escape" && e.currentTarget.value) setQuery("");
            }}
          />
        </div>

        <div className="sheet-body">
          {sections.length === 0 ? (
            <p className="sheet-none">{t("keys.sheet.none")}</p>
          ) : (
            sections.map((section) => (
              <section key={section.group}>
                <h3 className="section-title">{section.label}</h3>
                <dl className="sheet-list">
                  {section.items.map((spec) => (
                    <div key={spec.id} className="sheet-row">
                      <dt>{spec.label}</dt>
                      <dd>
                        <BindingKeys spec={spec} />
                      </dd>
                    </div>
                  ))}
                </dl>
              </section>
            ))
          )}
        </div>

        <div className="dialog-foot">
          <p className="sheet-note">{t("keys.sheet.note")}</p>
          <button type="button" className="btn" onClick={onClose}>
            {t("common.close")}
          </button>
        </div>
      </div>
    </Modal>
  );
}
