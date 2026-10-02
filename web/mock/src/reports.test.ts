/**
 * The mock's `reports` documents, held to the Go writer's bytes and to the contract.
 *
 * The differential harness compares the two servers' exports byte for byte, but only
 * over the rows the fixture world happens to hold. These tests pin the cases that
 * world does not carry: every OWASP trigger, every quoting rule `encoding/csv`
 * applies, and a hostile value travelling through a real route.
 */
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { app } from "./server.js";
import { DATASETS, codeHint, csvColumns, csvDocument, neutralise, trendBuckets } from "./reports.js";
import { world } from "./state.js";
import { contractDocument } from "./validate.js";

const testdata = (name: string) =>
  readFileSync(new URL(`../../../internal/export/testdata/${name}`, import.meta.url), "utf8");

describe("the CSV writer is Go's, byte for byte", () => {
  it("writes the golden document from the shared vectors", () => {
    const v = JSON.parse(testdata("vectors.json")) as { header: string[]; rows: string[][] };
    expect(csvDocument(v.header, v.rows)).toBe(testdata("golden.csv"));
  });
});

describe("formula injection is neutralised in every cell", () => {
  it("defuses every trigger OWASP names, and the full-width forms", () => {
    for (const hostile of [
      "=1+1",
      '=HYPERLINK("https://attacker.example/","x")',
      "=cmd|' /C calc'!A0",
      "+1",
      "-1+1",
      "@SUM(A1)",
      "\t=1",
      "\r=1",
      "\n=1",
      "＝1",
      "＋1",
      "－1",
      "＠1",
      // Past leading blanks and format characters, and the small-form forms.
      " =1",
      " \t=1",
      "\u00a0=1",
      "\u3000+1",
      "\ufeff=1",
      "\u200b@SUM(A1)",
      "\ufe661",
      "\ufe621",
      "\ufe631",
      "\ufe6bSUM(A1)",
      "\u22121",
      " -12",
    ]) {
      expect(neutralise(hostile), hostile).toBe(`'${hostile}`);
    }
  });

  it("leaves a plain decimal alone, so a negative duration still sorts", () => {
    for (const plain of ["-12", "12", "3.14", "-0.5", "", "a=b", "Cà Phê Sữa"]) {
      expect(neutralise(plain), plain).toBe(plain);
    }
  });

  it("defuses a hostile username on its way out of a real route", async () => {
    const session = world.sessions.find((s) => s.siteId === "site-hcm" && s.identityKind !== "voucher")!;
    const before = session.username;
    session.username = '=HYPERLINK("https://attacker.example/","x")';
    try {
      const from = new Date(Date.parse(session.startedAt) - 60_000).toISOString();
      const to = new Date(Date.parse(session.startedAt) + 60_000).toISOString();
      const res = await app.request(`/api/v0/exports/sessions?siteId=site-hcm&from=${from}&to=${to}`);
      expect(res.status).toBe(200);
      expect(res.headers.get("content-type")).toMatch(/^text\/csv/);
      expect(res.headers.get("content-disposition")).toMatch(/^attachment; filename="foyer-sessions-site-hcm-/);
      /* Bytes, not `text()`: a WHATWG decoder strips a leading byte-order mark, so a
         `text()` assertion could not tell a document with one from one without. */
      const bytes = new Uint8Array(await res.arrayBuffer());
      expect([...bytes.slice(0, 3)], "no byte-order mark").toEqual([0xef, 0xbb, 0xbf]);
      const body = new TextDecoder().decode(bytes);
      expect(body).toContain(`"'=HYPERLINK(""https://attacker.example/"",""x"")"`);
      expect(body).not.toMatch(/(^|,)=HYPERLINK/m);
    } finally {
      session.username = before;
    }
  });
});

describe("the columns are the contract's", () => {
  it("writes each export's header exactly as x-csv-columns declares it", () => {
    expect(DATASETS.sessions.columns).toEqual(csvColumns("exportSessions"));
    expect(DATASETS.vouchers.columns).toEqual(csvColumns("exportVouchers"));
    expect(DATASETS.guests.columns).toEqual(csvColumns("exportGuests"));
  });

  it("never carries a voucher code", async () => {
    const res = await app.request(
      `/api/v0/exports/vouchers?siteId=site-hcm&from=${new Date(Date.now() - 90 * 86_400_000).toISOString()}` +
        `&to=${new Date().toISOString()}`,
    );
    const body = await res.text();
    const codes = world.vouchers.map((v) => v.code);
    expect(codes.filter((c) => body.includes(c)), "a code left in full").toEqual([]);
    expect(codeHint("ABCDEFGHJKMN")).toBe("…JKMN");
  });

  it("declares a bound on every export and on both trend granularities", () => {
    for (const path of ["/exports/sessions", "/exports/vouchers", "/exports/guests"]) {
      const op = contractDocument().paths[path]!.get as Record<string, unknown>;
      expect(typeof op["x-max-range-days"], path).toBe("number");
    }
  });
});

describe("trends", () => {
  it("floors and ceils to the site's days and Monday weeks", () => {
    // 15:30 on the 23rd in Ho Chi Minh City is 08:30 UTC; 01:00 on the 25th is 18:00 UTC the 24th.
    const from = new Date(Date.UTC(2026, 8, 23, 8, 30));
    const days = trendBuckets(from, new Date(Date.UTC(2026, 8, 24, 18, 0)), "day", "Asia/Ho_Chi_Minh");
    // Each bucket starts at 17:00 UTC the day before: midnight at UTC+7, not the process's midnight.
    expect(days.map((b) => new Date(b.from).toISOString())).toEqual([
      "2026-09-22T17:00:00.000Z",
      "2026-09-23T17:00:00.000Z",
      "2026-09-24T17:00:00.000Z",
    ]);
    const weeks = trendBuckets(from, new Date(Date.UTC(2026, 9, 1)), "week", "Asia/Ho_Chi_Minh");
    for (const b of weeks) expect(new Date(b.from + 7 * 3_600_000).getUTCDay()).toBe(1);
    // A day is not 24 hours where a clock moves: New York's 2026-11-01 is 25.
    const fall = trendBuckets(
      new Date(Date.UTC(2026, 10, 1, 12)),
      new Date(Date.UTC(2026, 10, 1, 13)),
      "day",
      "America/New_York",
    );
    expect(fall[0]!.to - fall[0]!.from).toBe(25 * 3_600_000);
  });

  it("renders the seeded world with every bucket present and its freshness", async () => {
    const res = await app.request(
      `/api/v0/trends?siteId=site-hcm&from=${new Date(Date.now() - 13 * 86_400_000).toISOString()}` +
        `&to=${new Date().toISOString()}`,
    );
    expect(res.status).toBe(200);
    const body = (await res.json()) as {
      buckets: { bytes: number; guestDevices: number; bytesFreshness: { state: string } }[];
    };
    expect(body.buckets.length).toBe(14);
    expect(body.buckets.some((b) => b.bytes > 0 && b.guestDevices > 0), "the seeded world draws nothing").toBe(
      true,
    );
    expect(body.buckets.every((b) => b.bytesFreshness.state)).toBe(true);
  });
});
