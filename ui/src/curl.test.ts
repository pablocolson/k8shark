import { describe, expect, it } from "vitest";
import { curlCommand } from "./curl";
import type { Entry } from "./types";

function entry(overrides: Partial<Entry> & { id: string }): Entry {
  return {
    protocol: "http",
    timestamp: "2026-01-01T00:00:00.000Z",
    elapsedMs: 10,
    node: "node-1",
    src: { ip: "10.0.0.1", port: 40000 },
    dst: { ip: "10.0.0.2", port: 80, name: "api" },
    request: { summary: "GET /", method: "GET", path: "/" },
    response: { summary: "200" },
    status: "success",
    statusCode: 200,
    ...overrides,
  };
}

describe("curlCommand", () => {
  it("renders method, host and path as http:// by default", () => {
    const e = entry({
      id: "1",
      request: { summary: "GET /widgets?limit=10", method: "GET", path: "/widgets?limit=10", host: "shop.svc" },
    });
    expect(curlCommand(e)).toBe("curl -X 'GET' 'http://shop.svc/widgets?limit=10'");
  });

  it("uses https:// when the entry carries decrypted TLS info", () => {
    const e = entry({
      id: "2",
      request: { summary: "GET /", method: "GET", path: "/", host: "shop.svc" },
      l4: { tls: { sni: "shop.svc" } },
    });
    expect(curlCommand(e)).toContain("'https://shop.svc/'");
  });

  it("falls back to dst.name then dst.ip when the request has no Host", () => {
    const e = entry({
      id: "3",
      request: { summary: "GET /", method: "GET", path: "/" },
      dst: { ip: "10.0.0.2", port: 80, name: "api" },
    });
    expect(curlCommand(e)).toContain("'http://api/'");
  });

  it("includes non-hop-by-hop headers, in order, and drops hop-by-hop ones", () => {
    const e = entry({
      id: "4",
      request: {
        summary: "GET /",
        method: "GET",
        path: "/",
        host: "shop.svc",
        headers: {
          Authorization: "Bearer abc",
          "Content-Type": "application/json",
          Connection: "keep-alive",
          Host: "shop.svc",
          "Content-Length": "0",
        },
      },
    });
    const cmd = curlCommand(e);
    expect(cmd).toContain("-H 'Authorization: Bearer abc'");
    expect(cmd).toContain("-H 'Content-Type: application/json'");
    expect(cmd).not.toContain("Connection");
    expect(cmd).not.toContain("-H 'Host");
    expect(cmd).not.toContain("Content-Length");
  });

  it("adds --data-raw for a request body", () => {
    const e = entry({
      id: "5",
      request: { summary: "POST /login", method: "POST", path: "/login", host: "auth.svc", body: '{"user":"a"}' },
    });
    expect(curlCommand(e)).toContain(`--data-raw '{"user":"a"}'`);
  });

  it("omits --data-raw when there is no body", () => {
    const e = entry({ id: "6", request: { summary: "GET /", method: "GET", path: "/", host: "h" } });
    expect(curlCommand(e)).not.toContain("--data-raw");
  });

  it("shell-escapes embedded single quotes in header values and the body", () => {
    const e = entry({
      id: "7",
      request: {
        summary: "POST /",
        method: "POST",
        path: "/",
        host: "h",
        headers: { "X-Note": "it's fine" },
        body: "it's a body",
      },
    });
    const cmd = curlCommand(e);
    expect(cmd).toContain(String.raw`-H 'X-Note: it'\''s fine'`);
    expect(cmd).toContain(String.raw`--data-raw 'it'\''s a body'`);
  });

  it("defaults to GET when the method is missing", () => {
    const e = entry({ id: "8", request: { summary: "", path: "/", host: "h" } });
    expect(curlCommand(e)).toContain("-X 'GET'");
  });
});
