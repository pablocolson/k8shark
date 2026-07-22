// Regression coverage for the "pause capture does nothing" bug: each worker
// only self-reports its capturePaused field on its own ~10s stats heartbeat
// (internal/worker/sink.go's sinkStatsInterval), independent of this hook's
// 5s poll cadence and of when the user clicks. That means the poll landing
// shortly after a click almost always still carries pre-command data — before
// the fix below, that poll blindly overwrote the just-flipped optimistic
// state, so the toggle visibly snapped back to "not paused" within seconds
// of every click, worse (and more likely) the more workers there are to
// catch up. See useWorkers.ts's pendingRef comment for the fix.
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useWorkers } from "./useWorkers";
import type { WorkerInfo } from "./types";

function worker(overrides: Partial<WorkerInfo> = {}): WorkerInfo {
  return {
    node: "node-1",
    connected: true,
    connectedAt: "2026-01-01T00:00:00Z",
    lastSeen: "2026-01-01T00:00:00Z",
    entries: 100,
    dropped: 0,
    captureLive: true,
    captureTls: false,
    capturePaused: false,
    ringPackets: 0,
    ringDrops: 0,
    flowsEvicted: 0,
    ...overrides,
  };
}

let mockWorkers: WorkerInfo[] = [];

function mockFetch() {
  return vi.fn((url: string, init?: RequestInit) => {
    if (url === "/api/workers") {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(mockWorkers) } as Response);
    }
    if (url === "/api/workers/capture") {
      const { paused } = JSON.parse(init!.body as string);
      mockWorkers = mockWorkers.map((w) => ({ ...w, capturePaused: paused }));
      return Promise.resolve({ ok: true } as Response);
    }
    return Promise.reject(new Error(`unexpected fetch ${url}`));
  });
}

// Flushes pending microtasks (fetch().then() chains) without relying on
// testing-library's waitFor, which polls via real setTimeout internally and
// so never resolves once fake timers are installed.
async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("useWorkers pause/resume", () => {
  it("keeps showing the requested paused state across a poll that still reports stale (pre-command) data", async () => {
    mockWorkers = [worker({ node: "a" }), worker({ node: "b" })];
    globalThis.fetch = mockFetch() as typeof fetch;

    const { result } = renderHook(() => useWorkers());
    await flush();
    expect(result.current.workers).toHaveLength(2);

    act(() => result.current.setCapturePaused(true));
    // Optimistic flip lands immediately, before any poll.
    expect(result.current.workers.every((w) => w.capturePaused)).toBe(true);

    // The workers haven't self-reported the change yet (their own ~10s
    // heartbeat hasn't landed) — simulate the hub still handing back stale
    // data on the next poll, without applying setCapturePaused's own mutation
    // to mockWorkers this time.
    mockWorkers = [worker({ node: "a" }), worker({ node: "b" })]; // capturePaused: false
    act(() => {
      vi.advanceTimersByTime(5000); // next poll tick
    });
    await flush();

    // Before the fix, this poll would have overwritten the optimistic flip —
    // the toggle would read as if pause had never been clicked.
    expect(result.current.workers.every((w) => w.capturePaused)).toBe(true);
  });

  it("resumes trusting polls once the workers actually confirm the change", async () => {
    mockWorkers = [worker({ node: "a" })];
    globalThis.fetch = mockFetch() as typeof fetch;

    const { result } = renderHook(() => useWorkers());
    await flush();
    expect(result.current.workers).toHaveLength(1);

    act(() => result.current.setCapturePaused(true));

    // This time the worker's heartbeat *has* caught up by the next poll.
    mockWorkers = [worker({ node: "a", capturePaused: true })];
    act(() => vi.advanceTimersByTime(5000));
    await flush();
    expect(result.current.workers[0].capturePaused).toBe(true);

    // Pending intent should be cleared now — a poll reporting the worker
    // resumed on its own (e.g. another client toggled it) must be trusted,
    // not overridden back to "paused".
    mockWorkers = [worker({ node: "a", capturePaused: false })];
    act(() => vi.advanceTimersByTime(5000));
    await flush();
    expect(result.current.workers[0].capturePaused).toBe(false);
  });

  it("gives up on the pending intent after the confirm timeout and trusts polls again", async () => {
    mockWorkers = [worker({ node: "a" })];
    globalThis.fetch = mockFetch() as typeof fetch;

    const { result } = renderHook(() => useWorkers());
    await flush();
    expect(result.current.workers).toHaveLength(1);

    act(() => result.current.setCapturePaused(true));

    // This worker never confirms (e.g. its command was dropped) — every poll
    // keeps reporting capturePaused: false.
    mockWorkers = [worker({ node: "a", capturePaused: false })];
    act(() => vi.advanceTimersByTime(5000));
    await flush();
    expect(result.current.workers[0].capturePaused).toBe(true); // still shown paused, well within the grace window

    act(() => vi.advanceTimersByTime(20000)); // past PENDING_CONFIRM_TIMEOUT_MS
    await flush();
    // Past the timeout, an unconfirmed pending intent must not lie forever —
    // reflect what the hub is actually reporting.
    expect(result.current.workers[0].capturePaused).toBe(false);
  });

  it("does not treat a disconnected worker as blocking confirmation", async () => {
    mockWorkers = [worker({ node: "a" }), worker({ node: "b", connected: false, capturePaused: false })];
    globalThis.fetch = mockFetch() as typeof fetch;

    const { result } = renderHook(() => useWorkers());
    await flush();
    expect(result.current.workers).toHaveLength(2);

    act(() => result.current.setCapturePaused(true));

    // "a" (the only connected worker) confirms; "b" is disconnected and
    // never will, but must not hold the toggle hostage.
    mockWorkers = [
      worker({ node: "a", capturePaused: true }),
      worker({ node: "b", connected: false, capturePaused: false }),
    ];
    act(() => vi.advanceTimersByTime(5000));
    await flush();

    // Now simulate another client resuming "a" — since the pending intent
    // should already be cleared (confirmed on the previous poll), this must
    // be trusted immediately rather than held at the stale "paused" value.
    mockWorkers = [
      worker({ node: "a", capturePaused: false }),
      worker({ node: "b", connected: false, capturePaused: false }),
    ];
    act(() => vi.advanceTimersByTime(5000));
    await flush();
    expect(result.current.workers.find((w) => w.node === "a")?.capturePaused).toBe(false);
  });
});
