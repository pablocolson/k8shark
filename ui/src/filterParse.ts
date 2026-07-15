// filterParse.ts — pure, side-effect-free helpers for IFL autocomplete.
//
// This mirrors the lexical rules of internal/hub/filter.go's lex() (same
// operators, quoting/escaping, and/or/not keywords, identifier charset) but
// is deliberately *lenient*: unlike the Go lexer it must never throw, because
// it runs on every keystroke against input that is, by definition, usually
// incomplete (a stray quote, a half-typed operator, a trailing "and").
//
// contextAt() is the only thing the rest of the app calls. It figures out
// what kind of thing the caret is sitting in the middle of — a field name, an
// operator, a value, or a boolean connective — so the UI can offer the right
// suggestions. It is a heuristic aid, not a parser: on ambiguous or malformed
// input it degrades to a reasonable guess rather than failing.

export type CaretKind = "field" | "operator" | "value" | "boolean";

export interface CaretContext {
  kind: CaretKind;
  prefix: string; // the partial text already typed for the current token
  quoted: boolean; // true if we're inside an open, unterminated quote
  fieldName?: string; // resolved field name when kind is "operator" or "value"
  tokenStart: number; // index into the input where the current token begins
  tokenEnd: number; // index where it ends (usually == caret, unless mid-token)
}

type TokKind = "ident" | "string" | "number" | "op" | "lparen" | "rparen" | "and" | "or" | "not";

interface Tok {
  kind: TokKind;
  val: string; // decoded value (for strings: contents with escapes resolved, no quotes)
  start: number; // index of the first char of the token (for strings: the opening quote)
  end: number; // index just past the last char consumed (for strings: just past the
  // closing quote, or input.length if the quote was never closed)
  quoteOpen?: boolean; // true for a string token with no closing quote found
}

// Same stop-char set as filter.go's lex(): whitespace, parens, the operator
// chars, and both quote characters all terminate an identifier/number run.
const STOP_CHARS = " \t\n()=!<>\"'";

function isStop(ch: string): boolean {
  return STOP_CHARS.includes(ch);
}

// tokenize splits the *entire* input into tokens using the same lexical
// rules as the Go lexer, but never errors: an unterminated quote just
// consumes to the end of the string (flagged via quoteOpen), and any other
// oddity is skipped rather than raising.
function tokenize(s: string): Tok[] {
  const toks: Tok[] = [];
  const n = s.length;
  let i = 0;
  while (i < n) {
    const c = s[i];
    if (c === " " || c === "\t" || c === "\n") {
      i++;
      continue;
    }
    if (c === "(") {
      toks.push({ kind: "lparen", val: "(", start: i, end: i + 1 });
      i++;
      continue;
    }
    if (c === ")") {
      toks.push({ kind: "rparen", val: ")", start: i, end: i + 1 });
      i++;
      continue;
    }
    if (c === '"' || c === "'") {
      const quote = c;
      const start = i;
      let j = i + 1;
      let val = "";
      let closed = false;
      while (j < n) {
        if (s[j] === quote) {
          closed = true;
          break;
        }
        if (s[j] === "\\" && j + 1 < n) j++;
        val += s[j];
        j++;
      }
      if (closed) {
        toks.push({ kind: "string", val, start, end: j + 1 });
        i = j + 1;
      } else {
        // Unterminated (still being typed): consume to end of input.
        toks.push({ kind: "string", val, start, end: n, quoteOpen: true });
        i = n;
      }
      continue;
    }
    const two = s.slice(i, i + 2);
    if (two === "==" || two === "!=" || two === ">=" || two === "<=") {
      toks.push({ kind: "op", val: two, start: i, end: i + 2 });
      i += 2;
      continue;
    }
    if (c === ">" || c === "<") {
      toks.push({ kind: "op", val: c, start: i, end: i + 1 });
      i++;
      continue;
    }
    if (c === "=" || c === "!") {
      // A lone '=' / '!' isn't valid standalone IFL (the grammar only knows
      // ==/!=), but mid-keystroke we still want a token to anchor operator
      // suggestions on ("proto =" while typing "=="), so treat it leniently
      // as a partial operator rather than an error.
      toks.push({ kind: "op", val: c, start: i, end: i + 1 });
      i++;
      continue;
    }
    // identifier / number / keyword run
    let j = i;
    while (j < n && !isStop(s[j])) j++;
    if (j === i) {
      // Shouldn't happen given the cases above, but never throw.
      i++;
      continue;
    }
    const word = s.slice(i, j);
    const lower = word.toLowerCase();
    if (lower === "and") toks.push({ kind: "and", val: word, start: i, end: j });
    else if (lower === "or") toks.push({ kind: "or", val: word, start: i, end: j });
    else if (lower === "not") toks.push({ kind: "not", val: word, start: i, end: j });
    else if (lower === "contains") toks.push({ kind: "op", val: word, start: i, end: j });
    else if (word.trim() !== "" && !Number.isNaN(Number(word))) toks.push({ kind: "number", val: word, start: i, end: j });
    else toks.push({ kind: "ident", val: word, start: i, end: j });
    i = j;
  }
  return toks;
}

// decodeStringPrefix re-applies the same backslash-escape handling as the
// lexer, but only up to `caret`, so a value typed mid-string (or with the
// caret inside an escape) still yields a sane partial value.
function decodeStringPrefix(input: string, quoteStart: number, caret: number): string {
  const quote = input[quoteStart];
  const stop = Math.min(caret, input.length);
  let out = "";
  let j = quoteStart + 1;
  while (j < stop) {
    if (input[j] === quote) break;
    if (input[j] === "\\" && j + 1 < input.length) {
      j++;
      if (j >= stop) break;
    }
    out += input[j];
    j++;
  }
  return out;
}

// contextAt figures out what the caret is "inside of" for autocomplete
// purposes. Algorithm:
//
//  1. Tokenize the whole input (lenient — never throws).
//  2. Find the token the caret touches: strictly after its start and at or
//     before its end. Caret == token.start does NOT count (that's the empty
//     slot just *before* the token) — this is what lets "protocol " (field
//     word complete, caret now sitting in the trailing whitespace) resolve
//     differently from "protocol" (still mid-word).
//  3. Everything strictly before that token (or, if the caret sits in
//     whitespace/at the very start, everything with end <= caret) is
//     "before". Walk `before` backward to the nearest and/or/not/'(' — that's
//     the start of the current clause. (')' is deliberately NOT a start-of-
//     clause boundary: it *closes* one.)
//  4. Classify the clause-so-far tokens:
//       0 tokens                       -> "field"      (right after a
//                                          boundary/paren/start of input)
//       1 ident token                  -> "operator"    (field name typed,
//                                          e.g. "protocol ")
//       1 string/number token          -> "boolean"     (a bare literal is
//                                          already a complete full-text
//                                          clause on its own)
//       ident, op                      -> "value"
//       ends in ')'                    -> "boolean"     (parenthesized
//                                          sub-expression just closed)
//       anything else (3+ tokens, or
//       malformed)                     -> "boolean"     (clause looks
//                                          complete; next expected thing is
//                                          a connective)
//
// Design note on the "field-recognition boundary": this module has no
// access to the live field catalog (it's pure/DOM-and-fetch-free), so it
// can't check whether a typed word is actually a *known* field name. Instead
// it uses word-completion as the signal: as soon as an identifier token is
// finished (the caret has moved past it, e.g. into trailing whitespace) it
// is assumed to be "the field" and the next expected thing is an operator.
// While the word is still being typed (caret inside/at its end with nothing
// after), it's still kind "field". FilterSuggest is expected to double-check
// against the real field list and degrade further if the word doesn't
// actually match anything.
export function contextAt(input: string, caret: number): CaretContext {
  const toks = tokenize(input);

  let curIdx = -1;
  for (let i = 0; i < toks.length; i++) {
    const t = toks[i];
    if (caret > t.start && caret <= t.end) {
      curIdx = i;
      break;
    }
  }
  const cur = curIdx === -1 ? null : toks[curIdx];

  const before = curIdx === -1 ? toks.filter((t) => t.end <= caret) : toks.slice(0, curIdx);

  let boundary = -1;
  for (let i = before.length - 1; i >= 0; i--) {
    const k = before[i].kind;
    if (k === "and" || k === "or" || k === "not" || k === "lparen") {
      boundary = i;
      break;
    }
  }
  const clause = before.slice(boundary + 1);

  let kind: CaretKind;
  let fieldName: string | undefined;

  if (clause.length === 0) {
    kind = "field";
  } else if (clause[clause.length - 1].kind === "rparen") {
    kind = "boolean";
  } else if (clause.length === 1) {
    if (clause[0].kind === "ident") {
      kind = "operator";
      fieldName = clause[0].val;
    } else {
      kind = "boolean";
    }
  } else if (clause.length === 2 && clause[0].kind === "ident" && clause[1].kind === "op") {
    kind = "value";
    fieldName = clause[0].val;
  } else {
    kind = "boolean";
  }

  if (!cur) {
    return { kind, prefix: "", quoted: false, fieldName, tokenStart: caret, tokenEnd: caret };
  }

  if (cur.kind === "string") {
    const prefix = decodeStringPrefix(input, cur.start, caret);
    const closeQuoteIdx = cur.quoteOpen ? -1 : cur.end - 1;
    const quoted = cur.quoteOpen === true || caret <= closeQuoteIdx;
    return { kind, prefix, quoted, fieldName, tokenStart: cur.start, tokenEnd: cur.end };
  }

  const prefix = input.slice(cur.start, caret);
  return { kind, prefix, quoted: false, fieldName, tokenStart: cur.start, tokenEnd: cur.end };
}
