import { describe, expect, it } from "vitest";
import { ROLE_PERMISSIONS, type OperatorPermission } from "@foyer/contract";
import { optionRefusal, optionRequirementsByOperation } from "./permissions.js";

/**
 * Body options that carry a permission of their own (`x-permission` on a request
 * property), enforced by the mock from the document.
 *
 * `disconnectSession`'s `revokeVoucher` is the act `revokeVoucher` performs, so it
 * needs `vouchers:manage` beside the operation's `sessions:manage`. The Go handler
 * refuses it, and because every shipped role holds both, no flow through the shipped
 * roles can tell whether the mock does too. The refusal is therefore driven here against a
 * permission set no shipped role has — the role a later change might add.
 */
describe("a body option's own permission", () => {
  it("is read from the contract, not written here", () => {
    expect(optionRequirementsByOperation().disconnectSession).toEqual([
      { property: "revokeVoucher", requires: "vouchers:manage" },
    ]);
  });

  const path = "/sessions/sess-1/disconnect";
  const withoutVouchers = ROLE_PERMISSIONS.administrator.filter((p) => p !== "vouchers:manage") as OperatorPermission[];

  it("refuses the option to a caller without it, before anything is done", () => {
    expect(optionRefusal("POST", path, { revokeVoucher: true }, withoutVouchers)).toBe("vouchers:manage");
  });

  it("lets the same caller disconnect without the option", () => {
    expect(optionRefusal("POST", path, { revokeGrant: true }, withoutVouchers)).toBeUndefined();
    expect(optionRefusal("POST", path, { revokeVoucher: false }, withoutVouchers)).toBeUndefined();
  });

  it("grants it to every shipped role, which holds both", () => {
    for (const role of ["administrator", "staff"] as const) {
      expect(optionRefusal("POST", path, { revokeVoucher: true }, ROLE_PERMISSIONS[role])).toBeUndefined();
    }
  });
});
