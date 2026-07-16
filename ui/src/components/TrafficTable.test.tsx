import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TrafficTable } from "./TrafficTable";
import type { Entry } from "../types";

function entry(overrides: Partial<Entry> & { id: string }): Entry {
  return {
    protocol: "http",
    timestamp: "2026-01-01T00:00:00.000Z",
    elapsedMs: 10,
    node: "node-1",
    src: { ip: "10.0.0.1", port: 1234, name: "frontend" },
    dst: { ip: "10.0.0.2", port: 80, name: "backend" },
    request: { summary: "GET /" },
    response: {},
    status: "success",
    statusCode: 200,
    ...overrides,
  };
}

const baseProps = {
  selectedId: null,
  onSelect: vi.fn(),
  onLoadOlder: vi.fn(),
  loadingOlder: false,
  noMoreHistory: false,
  pinnedIds: new Set<string>(),
  onTogglePin: vi.fn(),
  onCompare: vi.fn(),
};

beforeEach(() => {
  localStorage.clear();
  vi.clearAllMocks();
});

describe("TrafficTable", () => {
  it("shows the empty state when there are no entries", () => {
    render(<TrafficTable {...baseProps} entries={[]} />);
    expect(screen.getByText(/waiting for traffic/i)).toBeInTheDocument();
  });

  it("renders a row per entry and calls onSelect on click", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const entries = [entry({ id: "a", request: { summary: "GET /a" } }), entry({ id: "b", request: { summary: "GET /b" } })];
    render(<TrafficTable {...baseProps} entries={entries} onSelect={onSelect} />);

    expect(screen.getByText("GET /a")).toBeInTheDocument();
    expect(screen.getByText("GET /b")).toBeInTheDocument();

    await user.click(screen.getByText("GET /a"));
    expect(onSelect).toHaveBeenCalledWith(entries[0]);
  });

  it("sorts by latency ascending, then descending, then resets on a third click", async () => {
    const user = userEvent.setup();
    const entries = [
      entry({ id: "slow", elapsedMs: 300, request: { summary: "slow" } }),
      entry({ id: "fast", elapsedMs: 10, request: { summary: "fast" } }),
      entry({ id: "mid", elapsedMs: 100, request: { summary: "mid" } }),
    ];
    render(<TrafficTable {...baseProps} entries={entries} />);

    const summaries = () => Array.from(document.querySelectorAll("td.col-summary")).map((td) => td.textContent);

    const latencyHeader = screen.getByText("latency");
    await user.click(latencyHeader);
    expect(summaries()).toEqual(["fast", "mid", "slow"]);

    await user.click(latencyHeader);
    expect(summaries()).toEqual(["slow", "mid", "fast"]);

    await user.click(latencyHeader);
    // Reset to arrival order (the order passed in via `entries`).
    expect(summaries()).toEqual(["slow", "fast", "mid"]);
  });

  it("toggles optional columns via the column picker and persists the choice", async () => {
    const user = userEvent.setup();
    render(<TrafficTable {...baseProps} entries={[entry({ id: "a" })]} />);

    expect(screen.queryByRole("columnheader", { name: "node" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /columns/i }));
    await user.click(screen.getByRole("checkbox", { name: "node" }));
    expect(screen.getByRole("columnheader", { name: "node" })).toBeInTheDocument();

    const stored = JSON.parse(localStorage.getItem("k8shark.columns") || "[]");
    expect(stored).toContain("node");
  });

  it("shows a compare trigger once two entries are pinned, and it fires onCompare", async () => {
    const user = userEvent.setup();
    const onCompare = vi.fn();
    const entries = [entry({ id: "a" }), entry({ id: "b" })];
    render(<TrafficTable {...baseProps} entries={entries} pinnedIds={new Set(["a", "b"])} onCompare={onCompare} />);

    const trigger = screen.getByRole("button", { name: /compare pinned \(2\)/i });
    expect(trigger).toBeEnabled();
    await user.click(trigger);
    expect(onCompare).toHaveBeenCalled();
  });

  it("disables the compare trigger with only one entry pinned", () => {
    const entries = [entry({ id: "a" }), entry({ id: "b" })];
    render(<TrafficTable {...baseProps} entries={entries} pinnedIds={new Set(["a"])} />);
    expect(screen.getByRole("button", { name: /pinned 1\/2/i })).toBeDisabled();
  });
});
