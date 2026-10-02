import { useId, useState } from "react";
import { t } from "../i18n/index.js";
import { LANG, LANGS, chooseLang, languageName, type Lang } from "../i18n/lang.js";

/**
 * The console's language, chosen by the person reading it and kept by this browser
 * (`i18n/lang.ts` has the survey and the precedence).
 *
 * A native `<select>` with a visible label, each option named in its own language
 * and marked with it (`lang`), so a screen reader pronounces the Vietnamese name
 * as Vietnamese and a reader who cannot read the current language still finds theirs.
 * On the sign-in screen and in the sidebar foot: the first is where somebody who
 * cannot read English arrives, the second where they look afterwards.
 */
export function LanguageSwitch({ className }: { className?: string }) {
  const id = useId();
  const [unsaved, setUnsaved] = useState(false);
  return (
    <div className={className ?? "field language-switch"}>
      <label htmlFor={id}>{t("shell.language")}</label>
      <select
        id={id}
        value={LANG}
        onChange={(e) => {
          if (!chooseLang(e.target.value as Lang)) setUnsaved(true);
        }}
      >
        {LANGS.map((l) => (
          <option key={l} value={l} lang={l}>
            {languageName(l)}
          </option>
        ))}
      </select>
      {unsaved ? (
        <p className="field-hint" role="status">
          {t("shell.language.unsaved")}
        </p>
      ) : null}
    </div>
  );
}
