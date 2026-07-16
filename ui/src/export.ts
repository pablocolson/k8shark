import type { Entry } from "./types";

export function entriesToJSON(entries: Entry[]): string {
  return JSON.stringify(entries, null, 2);
}

const CSV_COLUMNS: Array<[string, (e: Entry) => string | number]> = [
  ["id", (e) => e.id],
  ["timestamp", (e) => e.timestamp],
  ["protocol", (e) => e.protocol],
  ["status", (e) => e.status],
  ["statusCode", (e) => e.statusCode],
  ["elapsedMs", (e) => e.elapsedMs],
  ["node", (e) => e.node],
  ["src", (e) => endpointLabel(e.src)],
  ["srcIp", (e) => e.src.ip],
  ["srcPort", (e) => e.src.port],
  ["dst", (e) => endpointLabel(e.dst)],
  ["dstIp", (e) => e.dst.ip],
  ["dstPort", (e) => e.dst.port],
  ["summary", (e) => e.request.summary || ""],
];

function endpointLabel(ep: Entry["src"]): string {
  if (!ep.name) return ep.ip;
  return ep.namespace ? `${ep.name}.${ep.namespace}` : ep.name;
}

// csvEscape quotes a field only when needed (contains a comma, quote, or
// newline), doubling any embedded quotes — standard RFC 4180 escaping.
function csvEscape(v: string | number): string {
  const s = String(v);
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

export function entriesToCSV(entries: Entry[]): string {
  const header = CSV_COLUMNS.map(([name]) => name).join(",");
  const rows = entries.map((e) => CSV_COLUMNS.map(([, get]) => csvEscape(get(e))).join(","));
  return [header, ...rows].join("\n");
}

// downloadFile triggers a browser save-as for locally-generated content —
// nothing is fetched from anywhere, it's purely a Blob synthesized from data
// already loaded in the page.
export function downloadFile(content: string, filename: string, mime: string): void {
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}
