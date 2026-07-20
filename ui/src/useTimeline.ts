import { useEffect, useState } from "react";
import type { TimelineBucket } from "./types";

const POLL_MS = 10_000;

interface TimelineResponse {
  bucketSeconds: number;
  buckets: TimelineBucket[];
}

// useTimeline polls GET /api/timeline (the hub's own default window and
// bucket width — currently the last 15 minutes in 60s buckets) so the
// histogram tracks the current filter without the caller managing a time
// range itself.
export function useTimeline(filter: string): { buckets: TimelineBucket[]; bucketSeconds: number } {
  const [buckets, setBuckets] = useState<TimelineBucket[]>([]);
  const [bucketSeconds, setBucketSeconds] = useState(60);

  useEffect(() => {
    let cancelled = false;

    const load = () => {
      const q = new URLSearchParams();
      if (filter) q.set("filter", filter);
      const qs = q.toString();
      fetch(`/api/timeline${qs ? `?${qs}` : ""}`)
        .then((r) => (r.ok ? r.json() : null))
        .then((data: TimelineResponse | null) => {
          if (cancelled || !data) return;
          setBuckets(data.buckets ?? []);
          setBucketSeconds(data.bucketSeconds ?? 60);
        })
        .catch(() => {
          // fail-soft: keep the last-known buckets on a transient error
        });
    };

    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [filter]);

  return { buckets, bucketSeconds };
}
