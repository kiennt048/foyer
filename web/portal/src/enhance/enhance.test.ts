import { describe, expect, it } from "vitest";
import { groupVoucherCode, isVoucherCodeShaped, normaliseVoucherCode } from "@foyer/contract";

/**
 * The voucher field is the single most-used control in the product. A guest reads
 * the code off a thermal receipt and types it into a phone, one-handed, on a bad
 * link. Everything it accepts is worth a test.
 */
describe("voucher code handling", () => {
  it("accepts whatever the guest actually typed", () => {
    // Every one of these is a real thing someone does.
    for (const input of [
      "ABCDEFGHJKMN",
      "abcdefghjkmn",
      "ABCD-EFGH-JKMN",
      "abcd efgh jkmn",
      "  ABCD-EFGH-JKMN  ",
      "ABCD—EFGH—JKMN", // em dashes, from a word processor
    ]) {
      expect(normaliseVoucherCode(input)).toBe("ABCDEFGHJKMN");
    }
  });

  it("groups for display without changing the wire form", () => {
    expect(groupVoucherCode("ABCDEFGHJKMN")).toBe("ABCD-EFGH-JKMN");
    expect(normaliseVoucherCode(groupVoucherCode("ABCDEFGHJKMN"))).toBe("ABCDEFGHJKMN");
  });

  it("rejects the characters the alphabet deliberately excludes", () => {
    // 0/O, 1/I/L are indistinguishable in most receipt fonts, so the generator
    // never emits them and the validator must not silently accept them either.
    for (const bad of ["0BCDEFGHJKMN", "OBCDEFGH1KMN", "IBCDEFGHLKMN"]) {
      expect(isVoucherCodeShaped(bad)).toBe(false);
    }
  });

  it("rejects the wrong length", () => {
    expect(isVoucherCodeShaped("ABCDEFGHJKM")).toBe(false);
    expect(isVoucherCodeShaped("ABCDEFGHJKMNP")).toBe(false);
    expect(isVoucherCodeShaped("ABCDEFGHJKMN")).toBe(true);
  });
});
