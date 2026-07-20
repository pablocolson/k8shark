import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/react";
import { Timeline } from "./Timeline";
import type { TimelineBucket } from "../types";

function bucket(minuteOffset: number, entries: number, errors = 0, warnings = 0): TimelineBucket {
  return {
    start: new Date(2026, 0, 1, 12, minuteOffset, 0).toISOString(),
    entries,
    errors,
    warnings,
  };
}

function stubTimeline(buckets: TimelineBucket[], bucketSeconds = 60) {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ bucketSeconds, buckets }) }))
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("Timeline", () => {
  it("renders nothing while there is no bucket data", () => {
    stubTimeline([]);
    render(<Timeline filter="" onRangeSelect={vi.fn()} />);
    expect(document.querySelector(".timeline-svg")).not.toBeInTheDocument();
  });

  it("renders once buckets load", async () => {
    stubTimeline([bucket(0, 10), bucket(1, 5), bucket(2, 20)]);
    render(<Timeline filter="" onRangeSelect={vi.fn()} />);
    await waitFor(() => expect(document.querySelector(".timeline-svg")).toBeInTheDocument());
  });

  it("requests /api/timeline with the current filter", async () => {
    stubTimeline([bucket(0, 1)]);
    render(<Timeline filter='protocol == "http"' onRangeSelect={vi.fn()} />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    const url = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
    expect(url).toContain("/api/timeline?");
    expect(new URL(url, "http://localhost").searchParams.get("filter")).toBe('protocol == "http"');
  });

  it("reports the drag-selected range's since/until on mouseup", async () => {
    // Stubbed getBoundingClientRect (test-setup.ts) is {left:0, width:800};
    // 4 buckets -> 200px each.
    const buckets = [bucket(0, 10), bucket(1, 5), bucket(2, 20), bucket(3, 8)];
    stubTimeline(buckets, 60);
    const onRangeSelect = vi.fn();
    render(<Timeline filter="" onRangeSelect={onRangeSelect} />);
    const svg = await waitFor(() => {
      const el = document.querySelector(".timeline-svg");
      expect(el).toBeInTheDocument();
      return el as unknown as SVGSVGElement;
    });

    fireEvent.mouseDown(svg, { clientX: 50 }); // bucket 0
    fireEvent.mouseMove(svg, { clientX: 450 }); // bucket 2
    fireEvent.mouseUp(svg, { clientX: 450 });

    expect(onRangeSelect).toHaveBeenCalledTimes(1);
    const [since, until] = onRangeSelect.mock.calls[0];
    expect(since).toBe(buckets[0].start);
    expect(until).toBe(new Date(new Date(buckets[2].start).getTime() + 60_000).toISOString());
  });

  it("reports a single-bucket range on a plain click (no drag)", async () => {
    const buckets = [bucket(0, 10), bucket(1, 5)]; // 400px each
    stubTimeline(buckets, 60);
    const onRangeSelect = vi.fn();
    render(<Timeline filter="" onRangeSelect={onRangeSelect} />);
    const svg = await waitFor(() => {
      const el = document.querySelector(".timeline-svg");
      expect(el).toBeInTheDocument();
      return el as unknown as SVGSVGElement;
    });

    fireEvent.mouseDown(svg, { clientX: 700 }); // bucket 1
    fireEvent.mouseUp(svg, { clientX: 700 });

    expect(onRangeSelect).toHaveBeenCalledWith(
      buckets[1].start,
      new Date(new Date(buckets[1].start).getTime() + 60_000).toISOString()
    );
  });

  it("handles a drag released in reverse order (end before start)", async () => {
    const buckets = [bucket(0, 10), bucket(1, 5), bucket(2, 20), bucket(3, 8)];
    stubTimeline(buckets, 60);
    const onRangeSelect = vi.fn();
    render(<Timeline filter="" onRangeSelect={onRangeSelect} />);
    const svg = await waitFor(() => {
      const el = document.querySelector(".timeline-svg");
      expect(el).toBeInTheDocument();
      return el as unknown as SVGSVGElement;
    });

    fireEvent.mouseDown(svg, { clientX: 450 }); // bucket 2
    fireEvent.mouseMove(svg, { clientX: 50 }); // dragged back to bucket 0
    fireEvent.mouseUp(svg, { clientX: 50 });

    const [since, until] = onRangeSelect.mock.calls[0];
    expect(since).toBe(buckets[0].start); // lower bound regardless of drag direction
    expect(until).toBe(new Date(new Date(buckets[2].start).getTime() + 60_000).toISOString());
  });

  it("does not call onRangeSelect for a mouse-leave with no prior mousedown", async () => {
    stubTimeline([bucket(0, 10), bucket(1, 5)]);
    const onRangeSelect = vi.fn();
    render(<Timeline filter="" onRangeSelect={onRangeSelect} />);
    const svg = await waitFor(() => {
      const el = document.querySelector(".timeline-svg");
      expect(el).toBeInTheDocument();
      return el as unknown as SVGSVGElement;
    });

    fireEvent.mouseLeave(svg);
    expect(onRangeSelect).not.toHaveBeenCalled();
  });
});
