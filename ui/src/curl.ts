import type { Entry } from "./types";

// hopByHopHeaders are excluded from the generated command: they describe
// this specific captured TCP connection (or get recomputed by curl itself),
// not something a replay should carry over.
const hopByHopHeaders = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
  "host", // already folded into the URL
  "content-length", // curl recomputes this from --data-raw
]);

// curlCommand renders an HTTP entry's request as a copy-pasteable curl
// command: method, scheme://host+path (path already carries the query
// string — see redactedRequestURI on the worker), non-hop-by-hop headers,
// and a --data-raw body when present. Purely client-side, so it works even
// against a read-only/historical entry with no live connection to replay
// against.
export function curlCommand(entry: Entry): string {
  const rq = entry.request;
  const scheme = entry.l4?.tls ? "https" : "http";
  const host = rq.host || entry.dst.name || entry.dst.ip;
  const path = rq.path || "/";
  const url = `${scheme}://${host}${path}`;

  const parts = ["curl", "-X", shQuote(rq.method || "GET"), shQuote(url)];
  if (rq.headers) {
    for (const [k, v] of Object.entries(rq.headers)) {
      if (hopByHopHeaders.has(k.toLowerCase())) continue;
      parts.push("-H", shQuote(`${k}: ${v}`));
    }
  }
  if (rq.body) {
    parts.push("--data-raw", shQuote(rq.body));
  }
  return parts.join(" ");
}

// shQuote wraps s in single quotes, safe for a POSIX shell: each embedded
// single quote is closed out of the quoted string, escaped, then reopened
// (the standard '\'' idiom — backslash-escaping only works outside quotes).
function shQuote(s: string): string {
  return "'" + s.replace(/'/g, "'\\''") + "'";
}
