import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EntryDetail } from "./EntryDetail";
import type { Entry } from "../types";

const httpEntry: Entry = {
  id: "e1",
  protocol: "http",
  timestamp: "2026-01-01T00:00:00.000Z",
  elapsedMs: 42,
  node: "node-1",
  src: { ip: "10.0.0.1", port: 1234, name: "frontend" },
  dst: { ip: "10.0.0.2", port: 8080, name: "backend" },
  request: {
    summary: "POST /api/cart",
    method: "POST",
    path: "/api/cart",
    host: "backend",
    headers: { "content-type": "application/json" },
  },
  response: {
    summary: "200 OK",
    statusCode: 200,
    contentType: "application/json",
    body: '{"ok":true,"count":3}',
    headers: { "content-type": "application/json" },
    http: { version: "HTTP/1.1", ttfbMs: 12 },
  },
  status: "success",
  statusCode: 200,
};

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
});

describe("EntryDetail", () => {
  it("renders the overview tab by default with protocol-specific fields", () => {
    render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    expect(screen.getByRole("tab", { name: "overview", selected: true })).toBeInTheDocument();
    expect(screen.getByText("POST")).toBeInTheDocument();
    expect(screen.getByText("/api/cart")).toBeInTheDocument();
  });

  it("calls onClose from the close button", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<EntryDetail entry={httpEntry} onClose={onClose} />);
    await user.click(screen.getByRole("button", { name: /close/i }));
    expect(onClose).toHaveBeenCalled();
  });

  it("filters on the source/destination endpoint and follows the conversation, when onApply is provided", async () => {
    const user = userEvent.setup();
    const onApply = vi.fn();
    render(<EntryDetail entry={httpEntry} onClose={vi.fn()} onApply={onApply} />);

    await user.click(screen.getByRole("button", { name: /filter on this source/i }));
    expect(onApply).toHaveBeenLastCalledWith('dst.name == "frontend" or src.name == "frontend"');

    await user.click(screen.getByRole("button", { name: /filter on this destination/i }));
    expect(onApply).toHaveBeenLastCalledWith('dst.name == "backend" or src.name == "backend"');

    await user.click(screen.getByRole("button", { name: /follow this conversation/i }));
    expect(onApply).toHaveBeenLastCalledWith('src.name == "frontend" and dst.name == "backend"');
  });

  it("has no endpoint filter buttons and a disabled follow-conversation arrow without onApply", () => {
    render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    expect(screen.queryByRole("button", { name: /filter on this source/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /filter on this destination/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /follow this conversation/i })).toBeDisabled();
  });

  it("switches tabs on click and via ArrowRight keyboard navigation", async () => {
    const user = userEvent.setup();
    render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);

    await user.click(screen.getByRole("tab", { name: "response" }));
    expect(screen.getByRole("tab", { name: "response" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("ttfb")).toBeInTheDocument();

    screen.getByRole("tab", { name: "response" }).focus();
    await user.keyboard("{ArrowRight}");
    const headersTab = screen.getByRole("tab", { name: "headers" });
    expect(headersTab).toHaveAttribute("aria-selected", "true");
    expect(headersTab).toHaveFocus();
  });

  it("resets to the overview tab when the entry changes", () => {
    const { rerender } = render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    rerender(<EntryDetail entry={{ ...httpEntry, id: "e2" }} onClose={vi.fn()} />);
    expect(screen.getByRole("tab", { name: "overview", selected: true })).toBeInTheDocument();
  });

  it("pretty-prints and highlights a JSON body", async () => {
    const user = userEvent.setup();
    render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    await user.click(screen.getByRole("tab", { name: "body" }));

    const body = document.querySelector("pre.body");
    expect(body).not.toBeNull();
    expect(body!.querySelector(".jk")).toBeTruthy(); // a highlighted key
    expect(body!.textContent).toContain('"ok"');
  });

  it("copies the displayed body via the copy button", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    await user.click(screen.getByRole("tab", { name: "body" }));
    await user.click(screen.getByRole("button", { name: /copy response body/i }));

    expect(writeText).toHaveBeenCalledTimes(1);
    expect(JSON.parse(writeText.mock.calls[0][0])).toEqual({ ok: true, count: 3 });
    // Screen readers get an explicit confirmation (UI-10): the button's own
    // aria-label is static and wouldn't otherwise announce the state change.
    expect(await screen.findByText(/copied to clipboard/i)).toBeInTheDocument();
  });

  it("copies the request as a curl command from the header button", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: /copy this request as a curl command/i }));

    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText.mock.calls[0][0]).toBe("curl -X 'POST' 'http://backend/api/cart' -H 'content-type: application/json'");
    expect(await screen.findByText(/copied to clipboard/i)).toBeInTheDocument();
  });

  it("does not show a curl button for a non-HTTP entry", () => {
    const dnsEntry: Entry = { ...httpEntry, protocol: "dns" };
    render(<EntryDetail entry={dnsEntry} onClose={vi.fn()} />);
    expect(screen.queryByRole("button", { name: /copy this request as a curl command/i })).not.toBeInTheDocument();
  });

  it("does not render a body tab for an entry with no body", () => {
    const noBody: Entry = { ...httpEntry, request: { ...httpEntry.request, }, response: { ...httpEntry.response, body: undefined } };
    render(<EntryDetail entry={noBody} onClose={vi.fn()} />);
    expect(screen.queryByRole("tab", { name: "body" })).not.toBeInTheDocument();
  });

  // UI-11: resizable panel.
  it("restores the persisted width and resets it (with localStorage) on handle double-click", async () => {
    localStorage.setItem("k8shark.detailWidth", "600");
    const { container, unmount } = render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    const panel = container.querySelector(".detail") as HTMLElement;
    expect(panel.style.width).toBe("600px");

    const handle = container.querySelector(".detail-resize") as HTMLElement;
    await userEvent.dblClick(handle);
    expect(panel.style.width).toBe("440px");
    expect(localStorage.getItem("k8shark.detailWidth")).toBe("440");
    unmount();
  });

  it("clamps a persisted width below the minimum back up to 320", () => {
    localStorage.setItem("k8shark.detailWidth", "50");
    const { container } = render(<EntryDetail entry={httpEntry} onClose={vi.fn()} />);
    const panel = container.querySelector(".detail") as HTMLElement;
    expect(panel.style.width).toBe("320px");
  });
});
