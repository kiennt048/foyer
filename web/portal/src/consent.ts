import type { Language } from "@foyer/contract";

/**
 * A marketing-consent sentence a venue can start from, in both
 * languages the portal serves.
 *
 * Here beside `templates.ts` rather than in the console, for the reason the page
 * templates are: it is **words a guest reads**, in the guest's language, and the
 * console's own copy is English by rule (`web/admin/src/admin.test.ts`). The console
 * offers it as a starting point; the venue edits it and the version a guest agrees to
 * is whatever the venue saved.
 *
 * It says who will contact the guest, by what channel, about what, and that they can
 * withdraw — the four things Decree 356/2025/ND-CP's "specific, clear" consent needs
 * the guest to be told, and what Purple's own default opt-in sentence says.
 */
export const SUGGESTED_CONSENT: Readonly<Record<Language, string>> = {
  vi: "Tôi đồng ý nhận tin ưu đãi và sự kiện của quán qua email hoặc tin nhắn. Tôi có thể rút lại bất cứ lúc nào.",
  en: "I agree to receive offers and news from this venue by e-mail or text message. I can withdraw at any time.",
};
