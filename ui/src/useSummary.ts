import { useEffect, useState } from "react";
import type { GroupSummary } from "./types";

const POLL_MS = 5_000;

// The Top view's group-by selector: the hub's two endpoint-union
// pseudo-fields (see summary.go groupKeys). /api/summary also accepts any
// IFL field, but these are the two that answer "who are the top talkers".
export type SummaryGroupBy = "workload" | "namespace";

interface SummaryResponse {
  groupBy: string;
  total: number;
  groups: GroupSummary[] | null;
}

// useSummary polls GET /api/summary (per-group counts, error totals and
// latency percentiles over the hub's buffered entries) so the Top view
// tracks the current filter and group-by key without managing fetches
// itself — same polling shape as useWorkers/useTimeline.
export function useSummary(
  filter: string,
  groupBy: SummaryGroupBy
): { groups: GroupSummary[]; total: number } {
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [total, setTotal] = useState(0);

  useEffect(() => {
    let cancelled = false;

    const load = () => {
      const q = new URLSearchParams({ groupBy });
      if (filter) q.set("filter", filter);
      fetch(`/api/summary?${q.toString()}`)
        .then((r) => (r.ok ? r.json() : null))
        .then((data: SummaryResponse | null) => {
          if (cancelled || !data) return;
          setGroups(data.groups ?? []);
          setTotal(data.total ?? 0);
        })
        .catch(() => {
          // fail-soft: keep the last-known groups on a transient error
        });
    };

    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [filter, groupBy]);

  return { groups, total };
}
