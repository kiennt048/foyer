/**
 * English — the source of truth for every string in the portal.
 *
 * `MessageKey` is derived from this object, so a key added here and forgotten in
 * `vi.ts` is a compile error. The dependency points this way deliberately: English
 * is written first and Vietnamese is a complete map of it, never the reverse.
 *
 * Copy rules, from the frontend design (`docs/architecture.md`): no idiom, no
 * humour, no "please", short sentences, and every error names a next action. The budget is about eleven seconds, on a mid-range Android phone, in
 * daylight, one-handed, held by somebody who wants the internet and has no interest
 * in the product. A word stays only if removing it would cost that reader something.
 */
export const en = {
  /*
   * The endonym — the language's name in itself, which is the only label that is
   * correct for a reader who cannot read the current page. It is also the whole
   * switcher label: a label that said "the other one" would be a two-language
   * assumption sitting in a translated string, wrong the moment a venue advertises
   * a set rather than a pair.
   */
  "lang.name": "English",

  // The guest is already associated to the SSID; what they do not have is the
  // internet. "Get online" names the thing they came for in two words.
  "welcome.title": "Get online",
  "welcome.network": "Network",
  "welcome.choose": "How to sign in",
  "welcome.voucher": "I have a voucher",
  "welcome.voucherHint": "Printed on your receipt, or given to you by staff",
  "welcome.account": "I have an account",
  "welcome.accountHint": "For staff and long-stay guests",
  "welcome.phone": "Use my phone number",
  "welcome.phoneHint": "We text you a 6-digit code",
  "welcome.termsHeading": "Terms of use",
  /*
   * The default terms, used when the venue supplies none. These are the *venue's*
   * claims — access is as-is, and connection times are logged — written on behalf of
   * a venue that has not written its own.
   *
   * The billing-and-consent-boundary sentence is not part of this string: a venue
   * that supplies its own terms replaces this wholesale, so writing two paragraphs
   * of house rules would silently delete the one sentence `docs/rules/security.md`
   * §6 requires. It lives in `welcome.boundary` and is rendered separately and
   * unconditionally.
   */
  "welcome.termsSummary":
    "Internet access is provided as is, and we log when you connect and for how long, as required by law.",
  /*
   * Foyer's own sentence, not the venue's, and therefore not the venue's to remove.
   * It renders beneath the terms on every configuration of the portal, at a lighter
   * tier and next to an information icon, so it reads as a note from the software
   * running the network rather than as a second paragraph of the venue's legal text.
   *
   * One sentence. The guest has about eleven seconds and this is the honest thing
   * every other captive portal leaves out; a paragraph would guarantee it goes
   * unread, which is the same outcome as deleting it.
   */
  // Names the consequence, not the mechanism. "Accept" asks what is being accepted;
  // "Agree and get online" says what happens when you press it, which is the only
  // question a guest standing in a café actually has.
  "welcome.agree": "Agree and get online",
  "welcome.boundary":
    "Signing in here is a billing and consent boundary, not a security boundary — this network cannot protect your traffic, so use HTTPS.",

  "voucher.title": "Enter your code",
  "voucher.label": "Voucher code",
  // Everything a guest needs to type it correctly, and nothing else. The server
  // strips separators and case, so saying so removes the two hesitations people
  // actually have in front of a code printed on a thermal receipt.
  "voucher.help": "12 characters. Capitals, spaces and dashes do not matter.",
  "voucher.submit": "Connect",
  "voucher.back": "Back",

  "account.title": "Sign in",
  "account.username": "Username",
  "account.password": "Password",
  "account.submit": "Connect",
  "account.back": "Back",
  "phone.title": "Sign in with your phone",
  "phone.label": "Mobile number",
  "phone.help": "A Vietnamese mobile number, for example 0912 345 678. We text you a 6-digit code.",
  "phone.send": "Send code",
  "phone.back": "Back",
  "phone.codeTitle": "Enter your code",
  "phone.sentLede": "We sent a 6-digit code to the number you entered. It works for {minutes} min.",
  "phone.codeLabel": "Code",
  "phone.verify": "Connect",
  "phone.again": "Use another number or send a new code",

  "connected.title": "Connected",
  "connected.lede": "You're online.",
  "connected.plan": "Plan",
  "connected.duration": "Time limit",
  "connected.allowance": "Data",
  // An allowance, never a balance. radacct byte counters advance only on
  // Interim-Update or Stop, with a 300-second floor, and on the controller-API path
  // they may never arrive at all — so there is no freshness at which a
  // remaining-bytes figure would be true.
  "connected.allowanceNote": "This is your total allowance, not the amount left.",
  "connected.sampledNote":
    "Usage is sampled periodically, not measured live. It can be several minutes behind.",
  "connected.unenforcedNote":
    "The equipment here does not enforce this limit, so treat it as a guide.",
  "connected.continue": "Continue to",
  "connected.closeHint": "You can close this window.",

  /*
   * `/connected/` is a document, not a proof. It can be reloaded, bookmarked,
   * reopened by the CNA long after the grant ended, or reached by typing the URL —
   * and `PortalStatus.state` is `connected | expired | revoked` precisely because
   * the server knows which of those is true and the page did not read it. A green
   * tick shown to a guest whose access was cancelled a minute ago is the one failure
   * this whole design exists to prevent.
   *
   * Each of these leads with the mechanism and then says what it means for the
   * person reading, which is the pattern the qualifier on this page already uses.
   * None of them says "not connected" twice or asks the guest to infer a state from
   * what it is not.
   */
  "connected.expired": "Your time ran out, so this device is offline now. Ask staff for a new code.",
  "connected.revoked":
    "Your access was cancelled, so this device is offline now. Ask staff for a new code.",
  // What the portal actually knows is narrow: no grant answers to this browser. It
  // does not know why, so it says the one true thing and names the way forward.
  "connected.unknown": "This device is not signed in. Sign in again to get back online.",
  /*
   * The one where the guest did nothing wrong and their code is fine.
   *
   * `not-on-network` means the venue granted access and the access point never let
   * the device on — the controller was restarting, or its API key had expired. The
   * grant is real and the voucher is spent, so **"ask staff for a new code" would be
   * wrong here** in a way the other two are not: a new code goes through the same
   * controller that just refused, and the guest pays twice for one outage.
   *
   * It says whose problem it is out loud, for the reason `internal/portal`'s
   * CONTRACT gives about `unavailable`: a guest who reads this as a fault in their
   * phone spends twenty minutes toggling Wi-Fi, and a guest who takes it to the
   * counter reaches somebody who can see the controller is down.
   */
  "connected.notOnNetwork":
    "Your code was accepted, but the network did not let this device on. That is a fault here at the venue, not on your device. Show this screen to staff.",
  "connected.signIn": "Back to sign in",

  "problem.title": "Not connected",
  "problem.back": "Try again",

  // Every one of these names the next action. An error that only reports a state
  // leaves the guest holding a receipt in front of a staff member who cannot help.
  "error.not-found": "That code was not found. Check each character and try again.",
  "error.already-used": "That code has already been used. Ask staff for another one.",
  "error.expired": "That code has expired. Ask staff for another one.",
  "error.revoked": "That code has been cancelled. Ask staff for another one.",
  "error.not-present":
    "Your device could not be confirmed on this network. Turn Wi-Fi off and on again to start over.",
  /*
   * The guest under an access point the venue will not vouch for, and the reason
   * this is not `error.not-present`: **they are on the network.** The controller
   * returned a client for that MAC and then refused the access point the redirect
   * named, so telling them the device is missing is a true sentence about a
   * different situation — the same error `connected.notOnNetwork` and
   * `error.generic` were separated to avoid.
   *
   * Two actions, cheap one first, because two situations arrive here and one of
   * them is the venue's. A device that roamed or a page opened before the guest
   * moved is fixed by reconnecting; a venue whose controller cannot resolve an
   * access point at all never is, and sending every guest there to toggle Wi-Fi
   * is the twenty minutes `internal/portal/reason.go` refuses to spend.
   */
  "error.ap-unconfirmed":
    "Your device is on this network, but the access point it is using could not be confirmed. Reconnect to the Wi-Fi and try again. If that does not help, show this screen to staff.",
  "error.bad-credentials": "That username or password is not right. Check both and try again.",
  "error.disabled": "This account is disabled. Ask staff to turn it back on.",
  "error.shape": "A voucher code is 12 characters. Check yours and try again.",
  "error.generic": "Something went wrong. Try again.",
  "error.phone-invalid": "Codes can only be sent to a Vietnamese mobile number. Check the number and try again.",
  "error.code-wrong": "That code is not right. Check the message and try again.",
  "error.code-expired": "That code has expired or was already used. Send a new one.",
  "error.code-limit": "Too many codes were requested. Wait a while and try again, or ask staff.",
  "error.code-unavailable": "Codes cannot be sent right now. Choose another way to sign in, or ask staff.",

  /*
   * What a venue asks for at sign-in. A detail is optional unless
   * the venue says otherwise, and the label says so; the two errors never repeat the
   * value, because the value would have to travel in the address to be repeated.
   * The marketing checkbox's sentence is the venue's own and is not in this file:
   * there is no default promise the portal could make on a venue's behalf.
   */
  "error.details-required": "A detail this venue needs is blank. Fill it in and try again.",
  "error.details-invalid": "One of your details does not look right. Check it and try again.",
  "details.name": "Your name",
  "details.phone": "Phone number",
  "details.email": "Email address",
  "details.optional": "optional",
  "notice.consent-withdrawn": "Done. This venue will not send you marketing messages.",
  "connected.withdrawHint": "Agreed to hear about offers from this venue? You can take that back at any time.",
  "connected.withdraw": "Stop marketing messages",

  // Abbreviated so the fact block stays scannable, and separated by number so the
  // portal never renders "1 hours".
  // The venue's own blocks. The words are the venue's; these are only the labels
  // the portal puts around them — a code's caption and a social network's name.
  "block.code": "Code",
  "social.facebook": "Facebook",
  "social.instagram": "Instagram",
  "social.tiktok": "TikTok",
  "social.zalo": "Zalo",
  "social.youtube": "YouTube",
  "social.google-review": "Review us on Google",
  "social.website": "Website",
  "social.phone": "Call us",

  "unit.minutes": "min",
  "unit.hour": "hour",
  "unit.hours": "hours",
  "unit.unlimited": "No limit",

  /*
   * The measurement probe.
   *
   * **These strings are in the guest catalogue on purpose and ship to no guest.**
   * The portal build inlines the copy each document *uses*, not the catalogue, so
   * a key nothing renders costs no bytes on a guest's page view; and the probe
   * document is written to `dist-probe/`, which `make web-embed` does not copy and
   * `web/tools/check-budget.mjs` does not walk. The labels have to be in every
   * language the portal carries because a Vietnamese guest-facing venue running a
   * measurement session is the ordinary case, and a measurement read off an
   * English-only page is a photograph again.
   *
   * **The values these label are machine tokens, not prose** — `ok`, `threw:*`,
   * `dark`, `applied` — because the same line is POSTed verbatim to `/report` and
   * is meant to be grepped, not read aloud. The two places where a person genuinely
   * needs a sentence (`probe.fontnote`, `probe.lede`) get one.
   */
  "probe.title": "Environment probe",
  "probe.heading": "Device environment probe",
  "probe.lede":
    "This is not a guest page. It records what this browser does, shows the values, and sends them to the measurement host.",
  "probe.viewport": "Viewport",
  "probe.orientation": "Orientation",
  "probe.ladder": "Rhythm",
  "probe.dark": "Dark mode",
  "probe.insets": "Safe area",
  "probe.localstorage": "localStorage",
  "probe.sessionstorage": "sessionStorage",
  "probe.persist": "After reload",
  "probe.cookie": "Cookies",
  "probe.font": "Webfont",
  "probe.fontnote": "Twice: triangles = loaded, letters = not.",
  "probe.ua": "User agent",
  "probe.fits": "Fits on screen",
  "probe.report": "Report",
  "probe.pending": "not measured yet",
  "probe.sent": "Sent to the measurement host",
  "probe.refused": "The host refused the report. The values above are still on this screen.",
  "probe.blocked": "This browser refused to send the report. The values above are still on this screen.",
  "probe.unknown": "unknown",
} as const;

export type MessageKey = keyof typeof en;
