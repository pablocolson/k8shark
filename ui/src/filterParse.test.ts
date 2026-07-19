import { describe, expect, it } from "vitest";
import { contextAt } from "./filterParse";

// Table-driven cases for contextAt(). See the big comment above contextAt in
// filterParse.ts for the exact rule being asserted at each boundary — in
// particular the "field-recognition boundary" note: a field name is only
// considered "typed" (moving the context on to kind "operator") once the
// caret has moved *past* the identifier (e.g. into trailing whitespace),
// not merely because the identifier happens to match a known field name
// (this module has no access to the field catalog at all).
describe("contextAt", () => {
  const cases: Array<{
    name: string;
    input: string;
    caret: number;
    expect: Partial<{
      kind: string;
      prefix: string;
      quoted: boolean;
      fieldName: string | undefined;
    }>;
  }> = [
    {
      name: "empty input at caret 0",
      input: "",
      caret: 0,
      expect: { kind: "field", prefix: "" },
    },
    {
      name: "field name still being typed (no trailing space yet)",
      input: "protoc",
      caret: 6,
      expect: { kind: "field", prefix: "protoc" },
    },
    {
      name: "field name complete + trailing space -> operator",
      input: "protocol ",
      caret: 9,
      expect: { kind: "operator", prefix: "", fieldName: "protocol" },
    },
    {
      name: "field + operator + trailing space -> value",
      input: "protocol == ",
      caret: 12,
      expect: { kind: "value", prefix: "", fieldName: "protocol", quoted: false },
    },
    {
      name: "value mid-quote (unterminated) -> quoted value",
      input: 'protocol == "htt',
      caret: 16,
      expect: { kind: "value", prefix: "htt", fieldName: "protocol", quoted: true },
    },
    {
      name: "numeric value in progress",
      input: "response.status >= 5",
      caret: 20,
      expect: { kind: "value", prefix: "5", fieldName: "response.status", quoted: false },
    },
    {
      name: "typing a boolean connective after a complete clause",
      input: 'protocol == "http" an',
      caret: 21,
      expect: { kind: "boolean", prefix: "an" },
    },
    {
      name: "new clause after 'and' -> field",
      input: 'protocol == "http" and dst.name',
      caret: 31,
      expect: { kind: "field", prefix: "dst.name" },
    },
    {
      name: "'matches' recognized as a complete operator -> value",
      input: "request.path matches ",
      caret: 22,
      expect: { kind: "value", prefix: "", fieldName: "request.path" },
    },
    {
      name: "'startswith' recognized as a complete operator -> value",
      input: "request.host startswith ",
      caret: 25,
      expect: { kind: "value", prefix: "", fieldName: "request.host" },
    },
    {
      name: "'in' recognized as a complete operator -> value",
      input: "dst.namespace in ",
      caret: 18,
      expect: { kind: "value", prefix: "", fieldName: "dst.namespace" },
    },
    {
      name: "'matches' still being typed -> still operator (suggests the completion)",
      input: "request.path match",
      caret: 18,
      expect: { kind: "operator", prefix: "match", fieldName: "request.path" },
    },
  ];

  for (const c of cases) {
    it(`${c.name}: ${JSON.stringify(c.input)} @${c.caret}`, () => {
      const ctx = contextAt(c.input, c.caret);
      if (c.expect.kind !== undefined) expect(ctx.kind).toBe(c.expect.kind);
      if (c.expect.prefix !== undefined) expect(ctx.prefix).toBe(c.expect.prefix);
      if (c.expect.quoted !== undefined) expect(ctx.quoted).toBe(c.expect.quoted);
      if ("fieldName" in c.expect) expect(ctx.fieldName).toBe(c.expect.fieldName);
    });
  }

  it("never throws on malformed/partial input", () => {
    const inputs = [
      "(",
      ")",
      'and',
      'not',
      '"unterminated',
      "'unterminated",
      "field ==",
      "field == )",
      "( ( (",
      'a b c "d e f" and or not (',
    ];
    for (const input of inputs) {
      for (let caret = 0; caret <= input.length; caret++) {
        expect(() => contextAt(input, caret)).not.toThrow();
      }
    }
  });

  it("resolves a parenthesized clause as complete, expecting a boolean next", () => {
    const ctx = contextAt('(protocol == "dns") a', 21);
    expect(ctx.kind).toBe("boolean");
    expect(ctx.prefix).toBe("a");
  });

  it("resolves a bare literal clause as complete", () => {
    const ctx = contextAt('"checkout" a', 12);
    expect(ctx.kind).toBe("boolean");
    expect(ctx.prefix).toBe("a");
  });
});
