import type { Nas } from "@foyer/contract";
import { t } from "./i18n/index.js";

/**
 * The words the console uses for a driver key. **A leaf, deliberately.**
 *
 * `driver` is an internal identifier — `unifi`, `coa` — and rendered verbatim it
 * reaches an operator as *"A **unifi** access point is registered through its
 * controller"* in `/problems/needs-controller`. That is the enum leaking into the
 * product's voice.
 *
 * Having `problems.ts` import the label from `screens/nas/bits.tsx` instead is a
 * **module cycle**: `problems.ts` →
 * `screens/nas/bits.tsx` → `ui/primitives.tsx` → `problems.ts`. So this file exists
 * to be imported by both sides and to import neither: the catalogue and a type, and
 * nothing else. Anything that grows an import of `ui/` or `screens/` here puts the
 * cycle straight back.
 *
 * **Two maps, because the two readers need different sentences.** A `<select>` and a
 * detail heading want the mechanism — an operator choosing between *MikroTik (CoA)*
 * and *Omada (controller API)* is choosing between mechanisms. A refusal wants the
 * vendor alone: *"A UniFi access point is registered through its controller"* reads
 * as English, and *"A UniFi (controller API) access point"* does not.
 */
export const DRIVER_LABEL: Record<Nas["driver"], string> = {
  get unifi() {
    return t("nas.driver.unifi");
  },
  get mikrotik() {
    return t("nas.driver.mikrotik");
  },
  get coa() {
    return t("nas.driver.coa");
  },
  get ruckus() {
    return t("nas.driver.ruckus");
  },
  get omada() {
    return t("nas.driver.omada");
  },
};

/** The vendor alone, for prose. See above for why this is not `DRIVER_LABEL`. */
export const DRIVER_VENDOR: Record<Nas["driver"], string> = {
  get unifi() {
    return t("nas.vendor.unifi");
  },
  get mikrotik() {
    return t("nas.vendor.mikrotik");
  },
  get coa() {
    return t("nas.vendor.coa");
  },
  get ruckus() {
    return t("nas.vendor.ruckus");
  },
  get omada() {
    return t("nas.vendor.omada");
  },
};

/**
 * The vendor name for a value that arrived over the wire, or `null`.
 *
 * `null` rather than the raw string on purpose: a driver this console does not know
 * is a console older than its server, and echoing the unknown key would reproduce
 * exactly the defect this file exists to fix — one release later and with nobody
 * looking. The refusal already has a sentence for the case where it cannot name one.
 */
export function driverVendor(value: unknown): string | null {
  return typeof value === "string" && value in DRIVER_VENDOR
    ? DRIVER_VENDOR[value as Nas["driver"]]
    : null;
}

/**
 * Which member of the CoA family a `driver: coa` device is.
 *
 * **`coa` is the one `NasDriverKind` that names a family rather than a vendor**, and
 * Cisco and Aruba are two implementations behind it: Cisco IOS-XE identifies a
 * session by a `Cisco-AVPair audit-session-id` VSA, Aruba by `Calling-Station-Id` in
 * its own spelling. RFC 5176 §2.3 makes the NAS match on every session-identification
 * attribute in the request, so the wrong one matches nothing — and a careless device
 * **ACKs a request it could not match**. That is the failure no lab and no typed error
 * can see, which is why the operator is asked instead of guessed at.
 *
 * `Record<CoaVendor, string>` over the contract's own enum, the same mechanism
 * `DRIVER_LABEL` above uses: a vendor added to `CoaVendor` is a compile error here
 * rather than a `<select>` that quietly cannot offer it.
 */
export type CoaVendor = NonNullable<Nas["coaVendor"]>;

export const COA_VENDOR_LABEL: Record<CoaVendor, string> = {
  get cisco() {
    return t("nas.coaVendor.cisco");
  },
  get aruba() {
    return t("nas.coaVendor.aruba");
  },
};

/** The choices, read off the map rather than typed a second time beside it. */
export const COA_VENDORS = Object.keys(COA_VENDOR_LABEL) as CoaVendor[];
