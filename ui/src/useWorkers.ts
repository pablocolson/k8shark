import { useCallback, useEffect, useState } from "react";
import type { WorkerInfo } from "./types";

const POLL_MS = 5_000;

export interface WorkersState {
  workers: WorkerInfo[];
  setCapturePaused: (paused: boolean) => void;
}

// useWorkers polls GET /api/workers so the header's capture toggle reflects
// real registry state (including another client's toggle), and posts to
// POST /api/workers/capture to flip it. The hub relays that to every
// connected worker (see server.go's sendWorkerCommand) — pausing/resuming
// is a display-layer decision (route()/consumeTLS/runDemo stop turning
// capture into entries) rather than a process start/stop, so it's instant.
export function useWorkers(): WorkersState {
  const [workers, setWorkers] = useState<WorkerInfo[]>([]);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      fetch("/api/workers")
        .then((r) => (r.ok ? r.json() : []))
        .then((data: WorkerInfo[]) => {
          if (!cancelled) setWorkers(data);
        })
        .catch(() => {});
    };
    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  const setCapturePaused = useCallback((paused: boolean) => {
    // Optimistic: flips the button immediately rather than waiting out the
    // next poll; a failed/partial delivery just self-corrects on that poll.
    setWorkers((prev) => prev.map((w) => ({ ...w, capturePaused: paused })));
    fetch("/api/workers/capture", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ paused }),
    }).catch(() => {});
  }, []);

  return { workers, setCapturePaused };
}
