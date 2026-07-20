import { useMemo, useState } from "react";
import type { GroupSummary } from "../types";
import { useSummary, type SummaryGroupBy } from "../useSummary";
import { groupClause } from "../iflClause";

// TopView (UI-7): the "top talkers" table. It polls GET /api/summary (the same
// per-group counts, error totals and latency percentiles the MCP already
// consumes) for the current filter and group-by key, and lets the user sort by
// any column and click a row to pivot the List view onto that group.

type SortKey = "calls" | "errRate" | "p50" | "p95";

interface Column {
  key: SortKey;
  label: string;
  value: (g: GroupSummary) => number;
}

const COLUMNS: Column[] = [
  { key: "calls", label: "calls", value: (g) => g.count },
  { key: "errRate", label: "error %", value: (g) => (g.count ? g.errors / g.count : 0) },
  { key: "p50", label: "p50 ms", value: (g) => g.p50Ms },
  { key: "p95", label: "p95 ms", value: (g) => g.p95Ms },
];

export function TopView({
  filter,
  onApply,
}: {
  filter: string;
  onApply: (clause: string) => void;
}) {
  const [groupBy, setGroupBy] = useState<SummaryGroupBy>("workload");
  const [sortKey, setSortKey] = useState<SortKey>("calls");
  const [desc, setDesc] = useState(true);
  const { groups, total } = useSummary(filter, groupBy);

  const sorted = useMemo(() => {
    const col = COLUMNS.find((c) => c.key === sortKey) ?? COLUMNS[0];
    const rows = [...groups].sort((a, b) => col.value(a) - col.value(b));
    return desc ? rows.reverse() : rows;
  }, [groups, sortKey, desc]);

  const toggleSort = (key: SortKey) => {
    if (key === sortKey) setDesc((d) => !d);
    else {
      setSortKey(key);
      setDesc(true);
    }
  };

  return (
    <div className="top-view">
      <div className="top-controls">
        <div className="view-switch" role="group" aria-label="group by">
          {(["workload", "namespace"] as SummaryGroupBy[]).map((g) => (
            <button
              key={g}
              className={groupBy === g ? "active" : ""}
              onClick={() => setGroupBy(g)}
            >
              {g}
            </button>
          ))}
        </div>
        <span className="top-total" aria-live="polite">
          {sorted.length} {groupBy}s · {total} entries
        </span>
      </div>
      <table className="top-table">
        <thead>
          <tr>
            <th scope="col">{groupBy}</th>
            {COLUMNS.map((c) => (
              <th
                key={c.key}
                scope="col"
                className="num"
                aria-sort={
                  sortKey === c.key ? (desc ? "descending" : "ascending") : "none"
                }
              >
                <button className="th-sort" onClick={() => toggleSort(c.key)}>
                  {c.label}
                  {sortKey === c.key ? (desc ? " ▾" : " ▴") : ""}
                </button>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {sorted.length === 0 ? (
            <tr>
              <td colSpan={COLUMNS.length + 1} className="top-empty">
                no traffic in the current window
              </td>
            </tr>
          ) : (
            sorted.map((g) => {
              const errPct = g.count ? (g.errors / g.count) * 100 : 0;
              return (
                <tr
                  key={g.key}
                  tabIndex={0}
                  className="top-row"
                  onClick={() => onApply(groupClause(groupBy, g.key))}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      onApply(groupClause(groupBy, g.key));
                    }
                  }}
                  title={`filter on ${g.key}`}
                >
                  <td className="top-key">{g.key}</td>
                  <td className="num">{g.count}</td>
                  <td className={"num" + (errPct > 0 ? " has-err" : "")}>
                    {errPct.toFixed(errPct > 0 && errPct < 1 ? 1 : 0)}%
                  </td>
                  <td className="num">{g.p50Ms}</td>
                  <td className="num">{g.p95Ms}</td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
}
