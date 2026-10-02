/**
 * Copy for `/portal`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const portal = {
  "portal.save": "Save changes",
  "portal.saved": "Changes saved",
  "portal.saveFailed.title": "Changes not saved",

  // Staff read the portal settings and change none of them. The values stay
  // legible — a staff member who cannot see which terms a guest is agreeing to
  // cannot answer the guest standing in front of them.
  "portal.readOnly.title": "Read-only for your role",
  "portal.readOnly.body":
    "Staff can read these settings. An administrator changes the venue's branding, the sign-in methods and the terms.",
  "portal.discard": "Discard",

  "portal.section.identity": "Venue",
  "portal.section.network": "Network",
  "portal.section.image": "Photograph",
  "portal.section.methods": "How guests sign in",
  "portal.section.phone": "Phone sign-in",
  "portal.section.languages": "Languages",
  "portal.section.terms": "Terms of use",
  "portal.section.weight": "What this costs a guest",

  "portal.venueName": "Venue name",
  "portal.venueNameHint": "Shown in the masthead and in the page title.",
  "portal.venueNameRequired": "The welcome page names the venue. It cannot be blank.",
  "portal.networkName": "Network name",
  "portal.networkNameHint":
    "The SSID as a guest reads it on their phone. Voucher slips print it with a QR code that joins it. Leave blank to omit both.",

  "portal.logo": "Logo",
  "portal.logoHint": (max: string) =>
    `Inlined into every page, so it grows the first paint — ${max} at most. Foyer's mark is used if there is none.`,
  "portal.logoChoose": "Choose a logo",
  "portal.logoRemove": "Remove logo",
  "portal.logoNone": "No logo. Foyer's mark is shown instead.",
  "portal.logoByUrl": "Use an image link instead",
  "portal.logoByUpload": "Upload a file instead",
  "portal.logoUrl": "Image link",
  "portal.logoUrlHint": "Same address the guest's phone reaches — the portal must render before it has internet, so no external origin. Paste the link after uploading the file below.",
  "portal.logoUrlPlaceholder": "https://portal.example/logo.png",
  "portal.logoUrlPreview": "What this link loads, right now",
  "portal.logoUrlEmpty": "Paste a link to preview it here.",
  "portal.logoUrlNotImage": "This address did not answer an image — check the link and that the file is served.",
  "portal.logoUrlTooLarge": (size: string, max: string) =>
    `That file is ${size}. The limit is ${max} — it is saved as a link, so the server accepts it either way, but the guest still pays every byte on every launch.`,
  "portal.image.none": "No photograph.",
  "portal.image.choose": "Choose a photograph",
  "portal.image.hint": (max: string) =>
    `A guest downloads this on every portal launch, so keep it small — ${max} at most, and under 100 kB if the welcome page should open fast. WebP at 900 px clears that comfortably.`,
  "portal.image.replace": "Replace photograph",
  "portal.image.remove": "Remove photograph",
  "portal.image.failed.title": "Photograph not uploaded",
  "portal.image.tooLarge": (size: string, max: string) =>
    `${size}. The limit is ${max} — re-encode it smaller before uploading.`,
  "portal.image.wrongType": "Use JPEG, PNG or WebP. SVG is refused: it can carry script, and this renders before a guest has signed in.",
  // The measurement, stated where the decision is made rather than in a doc nobody
  // opens. This is the sentence that stops the next person inlining it.
  "portal.image.whyLinked":
    "Served from the portal's own address rather than embedded in the page. Embedded, a 900 px photograph costs more than the file itself and delays the first word a guest reads.",
  "portal.image.hiddenOnShort":
    "Hidden in the iOS and macOS sign-in window, which is 900 × 512 and cannot be resized. The consent sentence has to fit there and a photograph does not.",

  "portal.methods.voucher": "Voucher",
  "portal.methods.account": "Local account",
  "portal.methods.click-through": "Terms only",
  "portal.methods.phone": "Phone number",
  "portal.methods.clickThroughHint":
    "One button, no code and no account. The cheapest way to let people on, and the guest gives nothing — which is also why there is nothing to bill or trace it to.",
  "portal.clickThroughMinutes": "Minutes per guest",
  "portal.clickThroughMinutesHint":
    "How long a guest gets before they have to agree again. Required — access with no code and no time limit is public internet handed out on one tap.",
  "portal.clickThroughMinutesRequired": "Set a limit, or turn terms-only access off.",
  // Measured, and unknowable from this screen: the iOS and macOS sign-in window is
  // 900 x 512 and does not resize. Three ways in plus the terms plus the agree
  // button comes to 570px there.
  // A verdict and the sentence under it, because `Notice` refuses to render a
  // paragraph as a title (`VERDICT_MAX`). The fault is that a guest cannot see the
  // third way in; the window's dimensions are why, and they belong under it.
  "portal.methods.tooManyForCna.title": "The third way in falls below the fold",
  "portal.methods.tooManyForCna.body":
    "The iOS and macOS sign-in window is 900 × 512 and cannot be resized, so a guest offered three or four ways in has to scroll to reach the last. Offering terms-only on its own is the usual choice.",
  "portal.methods.hint": "At least one. A portal with no way in renders normally and cannot be used.",
  "portal.methods.required": "Choose at least one.",

  // Names in the console's language, not endonyms. The portal shows a guest their own
  // language in their own script; the console names it in the operator's. Writing the endonym here — even
  // inside a comment — is the one thing the Vietnamese-string guard scans for.
  "portal.language.en": "English",
  "portal.language.vi": "Vietnamese",

  "portal.languages.hint":
    "A venue advertising one language shows no switcher at all — the same rule the console follows for hardware it cannot use.",
  "portal.languages.required": "Choose at least one.",

  "portal.terms.hint":
    "Legal text. Foyer never paraphrases it, never machine-translates it, and never substitutes one language for another.",
  "portal.terms.for": (language: string) => `Terms in ${language}`,
  "portal.terms.placeholder": "Leave blank if you have none in this language.",
  // The resolution order, said as a consequence rather than as a rule. An operator
  // should be able to predict what a guest sees without reading the schema.
  "portal.terms.fallback": (reader: string, written: string) =>
    `A ${reader} reader sees your ${written} terms, marked as ${written}.`,
  "portal.terms.default": (reader: string) =>
    `A ${reader} reader sees Foyer's own wording, because you have supplied none.`,
  // Same split, same reason — 184 characters of prose in the title slot. `info`, so
  // the verdict is a fact rather than a fault: the operator's terms are not the whole
  // of what a guest agrees to, and that is the thing to know before writing any. What
  // the sentence *says*, and that it is fixed, are the detail under it.
  "portal.terms.consent.title": "Foyer adds its own sentence beneath yours",
  "portal.terms.consent.body":
    "It tells the guest this network is a billing and consent boundary, not a security one. It cannot be edited or removed.",
  "portal.terms.noTranslate":
    "There is no translate button, on purpose. A machine translation of a legal text is a different legal text.",

  "portal.weight.code": (used: string, budget: string) => `Page ${used} of ${budget}`,
  "portal.weight.image": (used: string, budget: string) => `Photograph ${used} of ${budget}`,
  "portal.weight.why":
    "Android clears its cache every time the sign-in page opens and the iOS window keeps nothing, so a guest pays this on every single launch — usually on the worst connection of their day.",
  "portal.weight.over": "Over budget. The build will refuse this.",

  // ── the page builder ───────────────────────────────────────────────────────
  //
  // Written for the person who runs the café, not for whoever installed the
  // network: no byte counts in the sentences they read first, no protocol names,
  // and every constraint stated as what the guest will see.
  "portal.tab.page": "Your page",
  "portal.tab.signin": "Sign-in",
  "portal.tab.terms": "Terms",
  "portal.tabs.label": "Portal settings",

  "portal.templates.title": "Start from a template",
  "portal.templates.lede":
    "Pick the one closest to your business. Everything in it can be changed, and nothing reaches guests until you save.",
  "portal.templates.open": "Templates",
  "portal.templates.replace.title": "Replace your page with this template?",
  "portal.templates.replace.body":
    "Your headline, colour and blocks are replaced. Your logo, photograph, sign-in and terms stay as they are. Nothing is saved until you press Save changes.",
  "portal.templates.replace.confirm": "Use this template",
  "portal.templates.cancel": "Cancel",
  "portal.template.cafe": "Café",
  "portal.template.cafe.hint": "A welcome offer, your best-sellers with prices, a review link.",
  "portal.template.restaurant": "Restaurant",
  "portal.template.restaurant.hint": "Today's specials, a table offer, a booking link.",
  "portal.template.hotel": "Hotel",
  "portal.template.hotel.hint": "Breakfast and check-out times, a spa offer, your services.",
  "portal.template.shop": "Shop",
  "portal.template.shop.hint": "A sale banner, a discount code, your opening hours.",
  "portal.template.event": "Event",
  "portal.template.event.hint": "The programme, a notice for the room, a download link.",
  "portal.template.blank": "Blank page",
  "portal.template.blank.hint": "Only your name and the sign-in. Add blocks yourself.",

  "portal.brand.title": "Look",
  "portal.brand.colour": "Brand colour",
  "portal.brand.colourHint":
    "Used for buttons, codes and highlights. Foyer darkens or lightens it a little where needed so text on it stays readable, in daylight and at night.",
  "portal.brand.custom": "Custom colour",
  "portal.brand.adjusted": (shown: string) => `Shown as ${shown} so it stays readable.`,
  "portal.accent.foyer": "Foyer teal",
  "portal.accent.coffee": "Coffee",
  "portal.accent.amber": "Amber",
  "portal.accent.red": "Red",
  "portal.accent.rose": "Rose",
  "portal.accent.purple": "Purple",
  "portal.accent.blue": "Blue",
  "portal.accent.green": "Green",
  "portal.accent.slate": "Slate",

  "portal.headline": "Headline",
  "portal.headlineFor": (language: string) => `Headline in ${language}`,
  "portal.headlineHint": (fallback: string) => `The big line at the top. Left blank, guests see "${fallback}".`,

  "portal.writingIn": "Writing in",
  "portal.writingIn.hint":
    "Guests see the language their phone asks for. A block you have written in only one language is shown in that language to everyone.",

  "portal.content.title": "What guests read",
  "portal.place.welcome": "Before sign-in",
  "portal.place.connected": "After sign-in",
  "portal.place.welcome.hint":
    "Every guest sees this page. Put your offer, your menu and anything that matters here.",
  "portal.place.connected.title": "Not every guest sees this page",
  "portal.place.connected.body":
    "Many Android phones close the sign-in window the moment the guest is online. iPhones and laptops show it. Use it for extras — a review link, your Facebook page — and keep anything important before sign-in.",
  "portal.place.connected.hint":
    "Guests are online here, so links work. Before sign-in they would not: the phone cannot reach any other website yet.",
  "portal.blocks.empty.title": "No blocks on this page.",
  "portal.blocks.empty.body": "Add a block, or start from a template.",
  "portal.blocks.add": "Add a block",
  "portal.blocks.full": (max: number) => `A page holds ${max} blocks at most.`,
  "portal.blocks.linksOnlyAfter":
    "Buttons and social links are only offered after sign-in, where they work.",

  "portal.kind.notice": "Notice",
  "portal.kind.notice.hint": "One short highlighted line, above the sign-in buttons.",
  "portal.kind.text": "Text",
  "portal.kind.text.hint": "A heading and a paragraph — a welcome, a story, house rules.",
  "portal.kind.offer": "Offer",
  "portal.kind.offer.hint": "A promotion, with a code to show at the counter if you want one.",
  "portal.kind.list": "List",
  "portal.kind.list.hint": "Rows with a price or a time — a menu, opening hours, a programme.",
  "portal.kind.button": "Button",
  "portal.kind.button.hint": "One link — a review, a booking, your website, a phone number.",
  "portal.kind.social": "Social links",
  "portal.kind.social.hint": "Your Facebook, Zalo, Instagram, TikTok pages.",

  "portal.block.untitled": "Nothing written yet",
  "portal.block.edit": "Edit",
  "portal.block.done": "Done",
  "portal.block.up": "Move up",
  "portal.block.down": "Move down",
  "portal.block.remove": "Remove",
  "portal.block.removed": "Block removed",
  "portal.block.visible": "Show to guests",
  "portal.block.hidden": "Hidden",
  "portal.block.scheduled": (from: string, to: string) => `${from} – ${to}`,
  "portal.block.notYet": "Not started yet",
  "portal.block.ended": "Ended",
  "portal.block.otherLanguage": (written: string) =>
    `Not written in this language yet. Guests reading it see your ${written} text.`,
  "portal.block.copyFrom": (written: string) => `Start from the ${written} text`,

  "portal.field.title": "Heading",
  "portal.field.notice": "Text",
  "portal.field.body": "Text",
  "portal.field.details": "Details",
  "portal.field.code": "Code",
  "portal.field.codeHint": "Shown large. Leave blank for an offer with no code.",
  "portal.field.label": "Button text",
  "portal.field.url": "Link",
  "portal.field.urlHint": "A web address starting https://, or a phone number as tel:+84…",
  "portal.field.urlInvalid": "Use a web address starting https:// or a phone number starting tel:",
  "portal.field.urlRequired": "Add a link, or hide this block.",
  "portal.field.items": "Rows",
  "portal.field.itemLabel": "Item",
  "portal.field.itemValue": "Price or time",
  "portal.field.addItem": "Add a row",
  "portal.field.removeItem": "Remove row",
  "portal.field.network": "Network",
  "portal.field.addLink": "Add a link",
  "portal.field.removeLink": "Remove link",
  "portal.field.optionalHeading": "Heading (optional)",
  "portal.schedule": "Schedule",
  "portal.schedule.hint": "Show this block only between these dates. Leave both blank to show it always.",
  "portal.schedule.from": "From",
  "portal.schedule.to": "Until",
  "portal.schedule.invalid": "The end date is before the start date.",
  "portal.social.facebook": "Facebook",
  "portal.social.instagram": "Instagram",
  "portal.social.tiktok": "TikTok",
  "portal.social.zalo": "Zalo",
  "portal.social.youtube": "YouTube",
  "portal.social.google-review": "Google review",
  "portal.social.website": "Website",
  "portal.social.phone": "Phone",
  "portal.design.duplicate": "Two blocks share an id. Remove one and add it again.",
  "portal.design.linkBeforeSignIn": "Links only work after sign-in.",
  "portal.design.fixFirst": "One block needs fixing before this can be saved.",

  "portal.previewing": "Preview",
  "portal.previewing.draft": "What guests will see once you save. Updates as you type.",
  "portal.previewing.phone": "Phone",
  "portal.previewing.window": "Laptop sign-in window",
  "portal.previewing.language": "Guest language",
  "portal.previewing.page": "Page",
  "portal.previewing.live": "Open the live portal",
  "portal.previewing.unsaved": "Unsaved changes",
  "portal.leave": "You have unsaved changes on this page.",
  "portal.weight.page": (used: string, budget: string) => `This page ${used} of ${budget}`,

  "portal.preview.title": "Preview",
  "portal.preview.hint":
    "The live portal, at the two sizes that matter. Save first — this shows what a guest gets right now, not what is typed above.",
  "portal.preview.unknown":
    "The portal address is not configured, so there is nothing to preview from here.",
  "portal.preview.cna": "iOS / macOS sign-in window · 900 × 512",
  "portal.preview.phone": "Phone · 390 × 844",
  "portal.preview.label": "Guest portal preview",
  "portal.preview.open": "Open the portal in a new tab",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/portal.js"));

export const t = makeT({ ...core, ...portal });
