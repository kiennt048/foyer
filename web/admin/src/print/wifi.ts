/**
 * What the QR code on a voucher slip says: **join this open network**, and nothing
 * else.
 *
 * ── what the field prints, and why this prints the same thing ──────────────
 *
 * The survey behind this: `unifi-voucher-site`, the most widely deployed UniFi voucher printer,
 * encodes `WIFI:S:<ssid>;;` for an open network — a join string — and prints the code
 * beside it as text. UniFi's and Omada's own voucher prints carry no QR at all.
 * MikroTik/Mikhmon templates encode a login URL with the credential in it, which
 * works there only because the RouterOS hotspot login page *is* on the router and
 * reads the query string itself.
 *
 * The obvious alternative — a portal URL that pre-fills the code — was
 * checked against this portal and it cannot work, for a reason that is structural
 * rather than a missing feature:
 *
 *   1. **A camera opens a URL in the browser, not in the captive-network assistant.**
 *      iOS and Android both hand a scanned `http(s)` URL to Safari/Chrome. The CNA
 *      has its own ephemeral cookie store; the browser is a different client with no
 *      portal session.
 *   2. **A direct visit carries no NAS claim.** A guest is identified by the MAC and
 *      AP the NAS appends when it *redirects* them (`internal/portal/entry.go`,
 *      `claimFrom`). The portal's own FQDN is in the walled garden by requirement, so
 *      a request to it is never intercepted and never redirected — it arrives with no
 *      `id`/`ap`, `Resolve` returns `ErrNoClaim`, and the voucher POST is `403`.
 *      Invariant 1 of `internal/portal/CONTRACT.md` forbids the only fix (granting
 *      without `VerifyPresence`).
 *   3. **It puts a live credential in a URL**: browser history, the portal's access
 *      log, and a `Referer` to whatever the guest opens next.
 *
 * So the QR takes the guest to the one place a sign-in can start — associated to the
 * SSID, with the operating system about to open the CNA on the portal the NAS
 * redirects it to — and the code is typed there. Both portals already keep a code
 * that was typed wrongly (`?code=` echo), so the twelve characters are typed once.
 *
 * ── the format ──────────────────────────────────────────────────────────────
 *
 * ZXing's `WIFI:` scheme, the one iOS 11+ Camera and Android 10+ Camera/Settings
 * both read: `WIFI:S:<ssid>;T:nopass;;`. `\ ; , : "` are escaped with a backslash.
 *
 * **Open by default; WPA when the venue has set a password.** A guest SSID behind a
 * captive portal is usually open (or OWE, which a phone joins as open). Printing
 * only that kind would hand a venue running a password-protected guest network a
 * QR no phone can use. Every operator setting is set in the console,
 * so the password is a setting (`updateGuestWifiKey`, write-only;
 * the slip reads it through `getVoucherSlipWifi`), and with one the join string is
 * ZXing's `WIFI:S:<ssid>;T:WPA;P:<password>;;` — `T:WPA` covers WPA2 and WPA3
 * transition, which is what both phones' readers accept. The price is the operator's
 * to weigh: a printed password that rotates strands every slip already in a drawer.
 *
 * **Not quoted.** ZXing's own Barcode Scanner app reads an unquoted SSID made only of
 * hex digits (`CAFE1234`) as a hex SSID; quoting it fixes that app and has unmeasured
 * effect on iOS, so the SSID is written as the operator typed it. UNVERIFIED either way.
 */

/** Backslash-escape the five characters the `WIFI:` grammar reserves. */
export function escapeWifiField(value: string): string {
  return value.replace(/[\\;,:"]/g, (c) => `\\${c}`);
}

/**
 * The longest SSID 802.11 allows, in octets (IEEE 802.11-2020 §9.4.2.2).
 *
 * `networkName` is free text — neither the contract, the console nor the store
 * limits it — so this is the only place a name that cannot be an SSID is caught.
 * Before it was, a descriptive name past ~195 bytes made the encoder throw inside
 * the batch page's render and took the page down, and a shorter one still printed a
 * QR for a network no phone could find.
 */
export const SSID_MAX_BYTES = 32;

/** True where the name is longer than any SSID can be, so no QR can join it. */
export function ssidTooLong(ssid: string | null | undefined): boolean {
  if (!ssid || !ssid.trim()) return false;
  return new TextEncoder().encode(ssid).length > SSID_MAX_BYTES;
}

/**
 * The join string — for an open network, or a WPA one when `passphrase` is given —
 * or `null` where there is no SSID to join: no name, or a name longer than an SSID
 * can be (`ssidTooLong`).
 */
export function wifiJoinPayload(
  ssid: string | null | undefined,
  passphrase?: string | null,
): string | null {
  // Blank is absent, but a non-blank name is used exactly as stored: an SSID may
  // legitimately begin or end with a space, and the phone matches it byte for byte.
  if (!ssid || !ssid.trim() || ssidTooLong(ssid)) return null;
  const s = escapeWifiField(ssid);
  return passphrase ? `WIFI:S:${s};T:WPA;P:${escapeWifiField(passphrase)};;` : `WIFI:S:${s};T:nopass;;`;
}
