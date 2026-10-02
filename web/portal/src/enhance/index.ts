/**
 * Progressive enhancement. Deliberately tiny, and deliberately incapable of
 * carrying the flow.
 *
 * Everything here is decoration over a form that already works with JavaScript
 * disabled. Nothing calls `preventDefault`, nothing submits by `fetch`, and nothing
 * stores state — because the CNA only re-checks captivity after a full navigation,
 * and its data store is destroyed when the window closes.
 *
 * If this file failed to load entirely, the portal would still work. That is the
 * test it has to keep passing.
 */
import { groupVoucherCode, normaliseVoucherCode, VOUCHER_LENGTH } from "@foyer/contract";

/**
 * Grouping comes from the contract, not from a second copy of the rule here.
 *
 * The length, the alphabet and the group size are contract facts — the admin UI
 * prints codes in this grouping, the portal's placeholder is drawn from it and the
 * server normalises with the same function. A local reimplementation with a
 * hardcoded `4` and `12` was one edit away from the two disagreeing, on the one
 * control that most guests only ever use once. esbuild tree-shakes the rest.
 */
function groupCode(raw: string): string {
  return groupVoucherCode(normaliseVoucherCode(raw).slice(0, VOUCHER_LENGTH));
}

function enhanceCodeInput(input: HTMLInputElement): void {
  const reformat = (event?: Event) => {
    /*
     * Never rewrite the value mid-composition. Gboard and every IME build the word
     * in the field and read it back; assigning to `value` underneath them drops or
     * duplicates characters. There is nothing to group until the composition ends
     * anyway, and `compositionend` is followed by an `input` event that lands here.
     */
    if ((event as InputEvent | undefined)?.isComposing) return;

    const before = input.value;
    // Count real characters before the caret, so grouping does not move it.
    const caret = input.selectionStart ?? before.length;
    const realBefore = before.slice(0, caret).replace(/[^A-Za-z0-9]/g, "").length;

    const formatted = groupCode(before);
    if (formatted === before) return;
    input.value = formatted;

    /*
     * Only touch the selection when this field is the one being typed into.
     *
     * `reformat()` also runs once at load, to group a value the server re-rendered
     * after a rejected code. Calling `setSelectionRange` on an unfocused input is
     * not harmless there: it scrolls the field and, on a page that autofocuses,
     * fights whatever the engine has already decided about the caret.
     */
    if (document.activeElement !== input) return;

    let pos = 0;
    let seen = 0;
    while (pos < formatted.length && seen < realBefore) {
      if (/[A-Z0-9]/.test(formatted[pos]!)) seen++;
      pos++;
    }
    input.setSelectionRange(pos, pos);
  };

  input.addEventListener("input", reformat);
  reformat();

  // The separators are display only. The contract carries the ungrouped form, and
  // stripping here means the server never has to guess which convention was used.
  input.form?.addEventListener("submit", () => {
    input.value = normaliseVoucherCode(input.value);
  });
}

/**
 * A one-time code is digits, and nothing here groups it.
 *
 * The OTP field must not share the voucher's `code-input` class. When it did,
 * `enhanceCodeInput` regrouped six digits as a voucher — `231086` became
 * `2310-86` — and the field's `pattern` refused the result: the browser blocked
 * the submit before the `submit` handler could strip anything, and phone sign-in
 * worked only with JavaScript off.
 *
 * So the code field has its own class and its own, smaller, handling: the
 * separators a guest pastes or an SMS app autofills (`231 086`, `231-086`, a
 * no-break space) are removed as they arrive, and nothing else is touched —
 * a letter stays visible, where the pattern and then the server refuse it, rather
 * than vanishing silently from what the guest typed.
 */
function stripCodeSeparators(raw: string): string {
  return raw.replace(/[\s\-]/g, "");
}

function enhanceOtpInput(input: HTMLInputElement): void {
  const tidy = (event?: Event) => {
    if ((event as InputEvent | undefined)?.isComposing) return;
    const tidied = stripCodeSeparators(input.value);
    if (tidied !== input.value) input.value = tidied;
  };
  input.addEventListener("input", tidy);
  tidy();
  input.form?.addEventListener("submit", () => {
    input.value = stripCodeSeparators(input.value);
  });
}

function enhanceSubmit(form: HTMLFormElement): void {
  form.addEventListener("submit", () => {
    const button = form.querySelector<HTMLButtonElement>("button[type=submit]");
    if (!button) return;
    // Marked busy, never disabled: a disabled submit button is excluded from the
    // form submission on some engines, and this button is the whole flow.
    button.setAttribute("aria-busy", "true");
    const spinner = document.createElement("span");
    spinner.className = "spinner";
    button.prepend(spinner);
  });
}

for (const form of document.querySelectorAll<HTMLFormElement>("form[data-portal-form]")) {
  enhanceSubmit(form);
  const code = form.querySelector<HTMLInputElement>("input.code-input");
  if (code) enhanceCodeInput(code);
  const otp = form.querySelector<HTMLInputElement>("input.otp-input");
  if (otp) enhanceOtpInput(otp);
}
