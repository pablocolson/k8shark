import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Entry, Envelope, Stats } from "./types";

const MAX_ENTRIES = 2000;

// Reconnect backoff: exponential with jitter, capped, reset on a healthy open.
const BACKOFF_BASE = 1000;
const BACKOFF_FACTOR = 2;
const BACKOFF_MAX = 15000;

// Flush cadence used in place of requestAnimationFrame while the document is
// hidden. Browsers throttle background setTimeout (typically to ~1/s) but,
// unlike rAF, never fully suspend it — see the comment on scheduleFlush.
const HIDDEN_FLUSH_INTERVAL_MS = 250;

// wsURL builds the hub WebSocket URL from the current origin. Works behind
// nginx (in-cluster) and behind the vite dev proxy (local).
function wsURL(filter: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const q = filter ? `?filter=${encodeURIComponent(filter)}` : "";
  return `${proto}//${location.host}/ws${q}`;
}

export interface HubState {
  entries: Entry[];
  stats: Stats | null;
  connected: boolean;
  paused: boolean;
  setPaused: (p: boolean) => void;
  clear: () => void;
  applyFilter: (f: string) => void;
}

// useHub owns the live connection: it streams entries, keeps a bounded rolling
// buffer, tracks stats, and pushes filter changes to the server so filtering
// happens hub-side (matching Kubeshark's model).
export function useHub(initialFilter: string): HubState {
  const [entries, setEntries] = useState<Entry[]>([]);
  const [stats, setStats] = useState<Stats | null>(null);
  const [connected, setConnected] = useState(false);
  const [paused, setPaused] = useState(false);

  // Counter baseline captured on clear(): the header then shows counts "since
  // clear" (server totals minus this) instead of "since hub start". statsRef
  // mirrors stats so clear() can snapshot without re-creating on every update.
  const [baseline, setBaseline] = useState<StatsBaseline | null>(null);
  const statsRef = useRef<Stats | null>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const pausedRef = useRef(paused);
  pausedRef.current = paused;
  const filterRef = useRef(initialFilter);
  const backoffRef = useRef(BACKOFF_BASE);

  // Coalesce arriving entries: buffer them and flush to state once per animation
  // frame so a burst of messages triggers a single re-render, not one per entry.
  //
  // Chromium-family browsers fully suspend requestAnimationFrame callbacks for a
  // hidden document (backgrounded tab/window) rather than merely throttling them.
  // If a flush were scheduled via rAF right as the tab goes hidden, it would never
  // fire: frameRef.current stays non-null, scheduleFlush's guard keeps early-
  // returning, and entries pile up in bufRef unflushed for as long as the tab
  // stays hidden. setTimeout is throttled but not suspended in the background
  // (browsers cap it to ~1/s), so we fall back to it whenever the document isn't
  // visible and switch back to rAF once it is.
  const bufRef = useRef<Entry[]>([]);
  const frameRef = useRef<number | null>(null);
  const frameKindRef = useRef<"raf" | "timeout">("raf");

  const cancelScheduledFlush = useCallback(() => {
    if (frameRef.current === null) return;
    if (frameKindRef.current === "raf") cancelAnimationFrame(frameRef.current);
    else clearTimeout(frameRef.current);
    frameRef.current = null;
  }, []);

  const scheduleFlush = useCallback(() => {
    if (frameRef.current !== null) return;
    const flush = () => {
      frameRef.current = null;
      const buf = bufRef.current;
      if (buf.length === 0) return;
      bufRef.current = [];
      setEntries((prev) => {
        // buf holds this frame's entries oldest-first; newest goes to the front.
        const next = buf.reverse().concat(prev);
        if (next.length > MAX_ENTRIES) next.length = MAX_ENTRIES;
        return next;
      });
    };
    if (document.visibilityState === "hidden") {
      frameKindRef.current = "timeout";
      frameRef.current = setTimeout(flush, HIDDEN_FLUSH_INTERVAL_MS) as unknown as number;
    } else {
      frameKindRef.current = "raf";
      frameRef.current = requestAnimationFrame(flush);
    }
  }, []);

  const connect = useCallback((filter: string) => {
    wsRef.current?.close();
    const ws = new WebSocket(wsURL(filter));
    wsRef.current = ws;
    ws.onopen = () => {
      setConnected(true);
      backoffRef.current = BACKOFF_BASE; // healthy connection resets the backoff
    };
    ws.onerror = (ev) => {
      console.error("hub websocket error", ev);
    };
    ws.onclose = () => {
      setConnected(false);
      // exponential backoff + jitter, capped, unless this socket was replaced
      const delay = backoffRef.current + Math.random() * BACKOFF_BASE;
      backoffRef.current = Math.min(backoffRef.current * BACKOFF_FACTOR, BACKOFF_MAX);
      setTimeout(() => {
        if (wsRef.current === ws) connect(filterRef.current);
      }, delay);
    };
    ws.onmessage = (ev) => {
      let msg: Envelope;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return; // ignore a malformed frame rather than throwing in onmessage
      }
      if (msg.type === "stats" && msg.stats) {
        statsRef.current = msg.stats;
        setStats(msg.stats);
      } else if (msg.type === "entry" && msg.entry) {
        if (pausedRef.current) return;
        bufRef.current.push(msg.entry);
        scheduleFlush();
      }
    };
  }, [scheduleFlush]);

  useEffect(() => {
    connect(filterRef.current);
    return () => {
      const ws = wsRef.current;
      wsRef.current = null;
      ws?.close();
      cancelScheduledFlush();
    };
  }, [connect, cancelScheduledFlush]);

  // Keep the scheduled flush mechanism matched to the current visibility
  // state, in both directions: a timeout fallback still pending after the tab
  // comes back to the foreground shouldn't make the user wait out the rest of
  // its (throttled, up to ~1s) interval, and — symmetrically — a rAF still
  // pending right as the tab goes to the background must be migrated to the
  // timeout fallback, since that rAF may never fire once hidden (the whole
  // reason scheduleFlush avoids rAF while hidden in the first place). Either
  // way, cancel and reschedule immediately rather than leaving a stale/stuck
  // timer in place.
  useEffect(() => {
    const onVisibilityChange = () => {
      if (frameRef.current === null) return;
      const wantKind = document.visibilityState === "hidden" ? "timeout" : "raf";
      if (frameKindRef.current === wantKind) return;
      cancelScheduledFlush();
      scheduleFlush();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [cancelScheduledFlush, scheduleFlush]);

  const applyFilter = useCallback((f: string) => {
    filterRef.current = f;
    // The hub does not replay history on a live filter swap, so clear what's shown.
    bufRef.current = [];
    setEntries([]);
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      // Update the server-side filter in place over the existing socket.
      ws.send(JSON.stringify({ type: "filter", filter: f }));
    } else {
      // Socket is closed/connecting — (re)connect with the filter in the URL.
      connect(f);
    }
  }, [connect]);

  const clear = useCallback(() => {
    bufRef.current = [];
    setEntries([]);
    // Reset the header counters too: snapshot the current totals as the new
    // baseline (workers/rate are live and stay untouched).
    const s = statsRef.current;
    setBaseline(s ? { total: s.totalEntries, byProto: { ...s.byProtocol } } : null);
  }, []);

  const displayStats = useMemo(() => applyBaseline(stats, baseline), [stats, baseline]);

  return { entries, stats: displayStats, connected, paused, setPaused, clear, applyFilter };
}

interface StatsBaseline {
  total: number;
  byProto: Record<string, number>;
}

// applyBaseline subtracts the counts captured at the last clear() so the header
// reads "since clear". Protocols with no traffic since clear drop out; rate and
// workers are live values and pass through unchanged.
function applyBaseline(s: Stats | null, base: StatsBaseline | null): Stats | null {
  if (!s || !base) return s;
  const byProtocol: Record<string, number> = {};
  for (const [p, n] of Object.entries(s.byProtocol)) {
    const d = n - (base.byProto[p] ?? 0);
    if (d > 0) byProtocol[p] = d;
  }
  return { ...s, totalEntries: Math.max(0, s.totalEntries - base.total), byProtocol };
}
