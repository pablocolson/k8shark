import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/react";
import { TopView } from "./TopView";
import type { GroupSummary } from "../types";

function group(key: string, count: number, errors = 0, p50 = 10, p95 = 20): GroupSummary {
  return {
    key,
    count,
    errors,
    warnings: 0,
    p50Ms: p50,
    p95Ms: p95,
    maxMs: p95,
    firstSeen: "2026-01-01T00:00:00Z",
    lastSeen: "2026-01-01T00:01:00Z",
  };
}

function stubSummary(groups: GroupSummary[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(() =>
      Promise.resolve({ ok: true, json: () => Promise.resolve({ groupBy: "workload", total: 42, groups }) })
    )
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("TopView", () => {
  it("renders one row per group from /api/summary", async () => {
    stubSummary([group("shop/checkout", 30), group("shop/frontend", 12)]);
    const { container } = render(<TopView filter="" onApply={vi.fn()} />);
    await waitFor(() => expect(container.querySelectorAll(".top-row")).toHaveLength(2));
    expect(container.textContent).toContain("shop/checkout");
    expect(container.textContent).toContain("shop/frontend");
  });

  it("fetches with the active filter and group-by", async () => {
    stubSummary([group("shop/checkout", 5)]);
    render(<TopView filter='protocol == "http"' onApply={vi.fn()} />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    const url = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
    expect(url).toContain("groupBy=workload");
    expect(url).toContain("filter=protocol");
  });

  it("applies a workload group clause on row click", async () => {
    const onApply = vi.fn();
    stubSummary([group("shop/checkout", 30)]);
    const { container } = render(<TopView filter="" onApply={onApply} />);
    const row = await waitFor(() => {
      const r = container.querySelector(".top-row");
      if (!r) throw new Error("no row yet");
      return r;
    });
    fireEvent.click(row);
    expect(onApply).toHaveBeenCalledWith(
      '(src.namespace == "shop" and src.workload == "checkout") or (dst.namespace == "shop" and dst.workload == "checkout")'
    );
  });

  it("sorts by a column when its header is clicked", async () => {
    stubSummary([group("a", 5, 0, 100), group("b", 50, 0, 5)]);
    const { container, getByText } = render(<TopView filter="" onApply={vi.fn()} />);
    // default sort: calls desc -> b (50) first
    await waitFor(() => expect(container.querySelector(".top-row .top-key")?.textContent).toBe("b"));
    // sort by p50 desc -> a (100ms) first
    fireEvent.click(getByText(/p50 ms/i));
    await waitFor(() => expect(container.querySelector(".top-row .top-key")?.textContent).toBe("a"));
  });
});
