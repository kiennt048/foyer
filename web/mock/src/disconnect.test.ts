import { afterEach, describe, expect, it } from "vitest";
import type { Session, SessionDisconnectResult, Voucher } from "@foyer/contract";
import { app } from "./server.js";
import { DEV_PASSWORD, world } from "./state.js";

/**
 * `disconnectSession` in the oracle, driven through the real route.
 *
 * The Go handler keeps an order — refusals first, then the withdrawals, then the
 * device — and this mock is the oracle `make diff-api` holds it to. So the order is
 * asserted here too, from the only side a mock can see it: a refusal leaves the
 * voucher exactly as it was, and a device that did not answer still leaves the
 * voucher revoked and the guest online.
 *
 * Every drive puts the fixture rows back afterwards. Ending a fixture session would
 * change what every other file in this process counts as online.
 */

async function cookie(): Promise<string> {
  const res = await app.request("/api/v0/auth/session", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ username: "dang.quoc.viet", password: DEV_PASSWORD }),
  });
  expect(res.status).toBe(200);
  return res.headers.get("set-cookie")?.split(";")[0] ?? "";
}

async function disconnect(sessionId: string, body: Record<string, unknown>) {
  return app.request(`/api/v0/sessions/${sessionId}/disconnect`, {
    method: "POST",
    headers: { "content-type": "application/json", cookie: await cookie() },
    body: JSON.stringify(body),
  });
}

const nasOf = (s: Session) => world.nas.find((n) => n.id === s.nasId);

/** An online session on a device that can end one, whose voucher can be found. */
function sessionWithVoucher(): { session: Session; voucher: Voucher } {
  for (const session of world.sessions) {
    const nas = nasOf(session);
    if (session.state !== "online" || !nas?.capabilities.canDeauthGuest || nas.reachability !== "ok") continue;
    const voucher = world.vouchers.find(
      (v) => v.state !== "revoked" && (v.id === session.voucherId || v.currentSessionId === session.id),
    );
    if (voucher) return { session, voucher };
  }
  throw new Error("no online session with a voucher on a reachable, disconnect-capable device");
}

const restore: (() => void)[] = [];
afterEach(() => {
  while (restore.length) restore.pop()!();
});
function keep<T extends object>(row: T) {
  const copy = { ...row };
  restore.push(() => Object.assign(row, copy));
}

describe("disconnectSession", () => {
  it("withdraws the voucher, ends the session, and records both", async () => {
    const { session, voucher } = sessionWithVoucher();
    keep(session);
    keep(voucher);
    const res = await disconnect(session.id, { revokeVoucher: true });
    expect(res.status).toBe(200);
    const body = (await res.json()) as SessionDisconnectResult;
    expect(body.disconnected).toBe(true);
    expect(body.voucherRevoked).toBe(true);
    expect(body.voucher?.id).toBe(voucher.id);
    expect(voucher.state).toBe("revoked");
    expect(session.state).toBe("ended");

    const entry = world.audit.find((e) => e.kind === "session-disconnected" && e.subject?.id === session.id);
    expect(entry?.subject).toEqual({ kind: "session", id: session.id, name: session.callingStationId });
    expect(entry?.params).toMatchObject({ disconnected: true, voucherRevoked: true, voucherId: voucher.id });
  });

  it("a plain disconnect leaves the voucher alone", async () => {
    const { session, voucher } = sessionWithVoucher();
    keep(session);
    keep(voucher);
    const body = (await (await disconnect(session.id, {})).json()) as SessionDisconnectResult;
    expect(body.disconnected).toBe(true);
    expect(body.voucherRevoked).toBe(false);
    expect(voucher.state).not.toBe("revoked");
  });

  it("a device that does not answer: the voucher is gone and the guest is still online", async () => {
    const { session, voucher } = sessionWithVoucher();
    const nas = nasOf(session)!;
    keep(session);
    keep(voucher);
    keep(nas);
    nas.reachability = "unreachable";

    const body = (await (await disconnect(session.id, { revokeVoucher: true })).json()) as SessionDisconnectResult;
    expect(body.disconnected).toBe(false);
    expect(body.failure?.type).toBe("/problems/session-end-failed");
    expect(body.failure?.params).toMatchObject({ cause: "no-answer", nasName: nas.name });
    expect(body.failure?.detail, "a server-composed sentence").toBeUndefined();
    expect(voucher.state, "revoked before the device was asked").toBe("revoked");
    expect(session.state).toBe("online");
  });

  it("a device whose driver cannot end a session changes nothing", async () => {
    const { session, voucher } = sessionWithVoucher();
    const nas = nasOf(session)!;
    keep(session);
    keep(voucher);
    keep(nas);
    nas.capabilities = { ...nas.capabilities, canDeauthGuest: false };
    const before = world.audit.length;

    const res = await disconnect(session.id, { revokeVoucher: true, revokeGrant: true });
    expect(res.status).toBe(409);
    expect(((await res.json()) as { type: string }).type).toBe("/problems/disconnect-unsupported");
    expect(voucher.state).not.toBe("revoked");
    expect(session.state).toBe("online");
    expect(world.audit.length, "a refusal is not a change").toBe(before);
  });
});
