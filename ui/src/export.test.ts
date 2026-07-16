import { describe, expect, it } from "vitest";
import { entriesToCSV, entriesToJSON } from "./export";
import type { Entry } from "./types";

function entry(overrides: Partial<Entry> & { id: string }): Entry {
  return {
    protocol: "http",
    timestamp: "2026-01-01T00:00:00.000Z",
    elapsedMs: 10,
    node: "node-1",
    src: { ip: "10.0.0.1", port: 1234, name: "frontend", namespace: "shop" },
    dst: { ip: "10.0.0.2", port: 80 }, // no name -> falls back to ip in CSV
    request: { summary: "GET /" },
    response: {},
    status: "success",
    statusCode: 200,
    ...overrides,
  };
}

describe("entriesToJSON", () => {
  it("round-trips the given entries verbatim", () => {
    const entries = [entry({ id: "a" }), entry({ id: "b" })];
    expect(JSON.parse(entriesToJSON(entries))).toEqual(entries);
  });
});

describe("entriesToCSV", () => {
  it("emits a header row plus one row per entry, name.namespace or ip fallback for endpoints", () => {
    const csv = entriesToCSV([entry({ id: "a" })]);
    const [header, row] = csv.split("\n");
    expect(header).toBe("id,timestamp,protocol,status,statusCode,elapsedMs,node,src,srcIp,srcPort,dst,dstIp,dstPort,summary");
    expect(row).toBe("a,2026-01-01T00:00:00.000Z,http,success,200,10,node-1,frontend.shop,10.0.0.1,1234,10.0.0.2,10.0.0.2,80,GET /");
  });

  it("quotes fields containing a comma, quote, or newline (RFC 4180)", () => {
    const csv = entriesToCSV([entry({ id: "a", request: { summary: 'GET /x?a=1,2 say "hi"\nline2' } })]);
    const row = csv.split("\n").slice(1).join("\n"); // summary itself may contain \n
    expect(row).toContain('"GET /x?a=1,2 say ""hi""\nline2"');
  });

  it("produces just the header for an empty entry list", () => {
    expect(entriesToCSV([]).split("\n")).toHaveLength(1);
  });
});
