import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkerInfo } from "./types";

const POLL_MS = 5_000;

// How long to keep showing the just-requested pause/resume state over what
// polls report, before giving up and trusting the poll regardless. Generous:
// each worker only self-reports its capturePaused field on its own ~10s
// stats heartbeat (sinkStatsInterval, independent of this poll's cadence and
// of when the user clicked), so the first poll or two after a click almost
// always still carries pre-command data.
const PENDING_CONFIRM_TIMEOUT_MS = 20_000;

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

  // Tracks a just-issued pause/resume intent that the next poll(s) haven't
  // confirmed yet. Without this, a poll landing shortly after a click almost
  // always overwrites the optimistic toggle with the workers' pre-command
  // capturePaused values (see PENDING_CONFIRM_TIMEOUT_MS above) — the click
  // visibly "does nothing," worse the more workers there are to catch up.
  const pendingRef = useRef<{ paused: boolean; since: number } | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      fetch("/api/workers")
        .then((r) => (r.ok ? r.json() : []))
        .then((data: WorkerInfo[]) => {
          if (cancelled) return;
          const pending = pendingRef.current;
          if (pending) {
            const connected = data.filter((w) => w.connected);
            const confirmed = connected.length > 0 && connected.every((w) => w.capturePaused === pending.paused);
            if (confirmed || Date.now() - pending.since > PENDING_CONFIRM_TIMEOUT_MS) {
              pendingRef.current = null;
            } else {
              data = data.map((w) => ({ ...w, capturePaused: pending.paused }));
            }
          }
          setWorkers(data);
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
    // next poll. pendingRef keeps it flipped across the next several polls
    // too, until the workers themselves confirm it (or the timeout above
    // gives up and defers to whatever polls are actually reporting).
    pendingRef.current = { paused, since: Date.now() };
    setWorkers((prev) => prev.map((w) => ({ ...w, capturePaused: paused })));
    fetch("/api/workers/capture", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ paused }),
    }).catch(() => {});
  }, []);

  return { workers, setCapturePaused };
}
