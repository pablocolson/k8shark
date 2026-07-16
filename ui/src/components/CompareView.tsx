import { useMemo } from "react";
import type { Entry } from "../types";

// Above this many lines the O(n*m) LCS diff below gets expensive; just show
// the two bodies side by side without highlighting instead.
const MAX_DIFF_LINES = 400;

interface DiffOp {
  type: "same" | "add" | "del";
  line: string;
}

// diffLines is a small LCS-based line diff (dependency-free, matching the
// app's existing "no charting/diff lib" approach). Not Myers' algorithm —
// just the textbook O(n*m) LCS table — fine at the line counts entry bodies
// realistically have.
function diffLines(a: string[], b: string[]): DiffOp[] {
  const n = a.length;
  const m = b.length;
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const ops: DiffOp[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      ops.push({ type: "same", line: a[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      ops.push({ type: "del", line: a[i] });
      i++;
    } else {
      ops.push({ type: "add", line: b[j] });
      j++;
    }
  }
  while (i < n) ops.push({ type: "del", line: a[i++] });
  while (j < m) ops.push({ type: "add", line: b[j++] });
  return ops;
}

function BodyDiff({ a, b }: { a: string; b: string }) {
  const linesA = a.split("\n");
  const linesB = b.split("\n");

  if (a === b) {
    return <div className="empty-note">identical</div>;
  }
  if (linesA.length > MAX_DIFF_LINES || linesB.length > MAX_DIFF_LINES) {
    return (
      <div className="compare-body-plain">
        <pre className="body mono">{a}</pre>
        <pre className="body mono">{b}</pre>
      </div>
    );
  }
  const ops = diffLines(linesA, linesB);
  return (
    <pre className="body mono compare-diff">
      {ops.map((op, i) => (
        <div key={i} className={`diff-line diff-${op.type}`}>
          {op.type === "add" ? "+ " : op.type === "del" ? "- " : "  "}
          {op.line}
        </div>
      ))}
    </pre>
  );
}

const META_ROWS: Array<[string, (e: Entry) => string]> = [
  ["protocol", (e) => e.protocol],
  ["status", (e) => String(e.statusCode || e.status || "—")],
  ["latency", (e) => `${e.elapsedMs} ms`],
  ["time", (e) => new Date(e.timestamp).toLocaleString([], { hour12: false })],
  ["source", (e) => e.src.name || e.src.ip],
  ["destination", (e) => e.dst.name || e.dst.ip],
  ["summary", (e) => e.request.summary || "—"],
];

export function CompareView({ a, b, onClose }: { a: Entry; b: Entry; onClose: () => void }) {
  const bodyA = a.response.body ?? "";
  const bodyB = b.response.body ?? "";
  const hasBodies = !!(a.response.body || b.response.body);
  const rows = useMemo(() => META_ROWS.map(([label, get]) => [label, get(a), get(b)] as const), [a, b]);

  return (
    <div className="compare-overlay">
      <div className="compare-head">
        <span className="compare-title">Compare entries</span>
        <button type="button" className="icon-btn" onClick={onClose} title="close" aria-label="close compare">
          ✕
        </button>
      </div>
      <div className="compare-body">
        <table className="kv compare-meta">
          <thead>
            <tr>
              <th></th>
              <th>A · {a.id}</th>
              <th>B · {b.id}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(([label, va, vb]) => (
              <tr key={label} className={va !== vb ? "diff-row" : ""}>
                <td className="kv-k">{label}</td>
                <td className="kv-v mono">{va}</td>
                <td className="kv-v mono">{vb}</td>
              </tr>
            ))}
          </tbody>
        </table>

        {hasBodies && (
          <>
            <div className="subhead">response body diff</div>
            <BodyDiff a={bodyA} b={bodyB} />
          </>
        )}
      </div>
    </div>
  );
}
