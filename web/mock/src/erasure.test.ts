/**
 * The oracle's erasure, for two things beyond erasure by MAC: erasure by e-mail
 * address, and the audit log's earlier entries kept with the
 * identifier pseudonymised (migration 00052) rather than left naming the guest.
 */
import { describe, expect, it } from "vitest";
import { erasableEmail, eraseSubject, maskEmail, recordAudit, world } from "./state.js";

describe("erasure reaches the audit log", () => {
  it("pseudonymises every entry naming an erased MAC, in any spelling, and keeps the entries", () => {
    const mac = "5e:1f:a3:77:c0:4d";
    for (const name of ["5E-1F-A3-77-C0-4D", "5e1f.a377.c04d"])
      recordAudit({
        actorId: "op-1",
        actorUsername: "linh",
        kind: "session-disconnected",
        siteId: null,
        subject: { kind: "session", id: `sess-${name.length}`, name },
      });
    const before = world.audit.length;
    const r = eraseSubject("mac", mac, "")!;
    expect(world.audit.length).toBe(before);
    expect(r.pseudonymised).toEqual([{ table: "audit_log", rows: 2 }]);
    const names = world.audit.map((e) => e.subject?.name ?? "").filter((n) => n.startsWith("pseudonym:"));
    expect(new Set(names).size).toBe(1);
    expect(JSON.stringify(world.audit).toLowerCase()).not.toContain("5e-1f-a3");
  });

  it("erases by e-mail address, without regard to case, and masks it", () => {
    expect(erasableEmail("0912 345 678")).toBe("");
    expect(erasableEmail(" Lan@Example.vn ")).toBe("Lan@Example.vn");
    expect(maskEmail("Lan@Example.vn")).toBe("l•••@•••.vn");
    const r = eraseSubject("email", "", "", undefined, "lan@example.vn")!;
    expect(r.removed.map((x) => x.table)).toEqual(["guest_details", "marketing_consents"]);
    expect(r.masked).toBe("l•••@•••.vn");
  });
});
