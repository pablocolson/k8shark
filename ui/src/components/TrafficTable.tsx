import type { ReactNode } from "react";
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import type { Entry } from "../types";
import { PROTO_COLORS } from "../constants";

// Matches the row height content-visibility used to assume before real
// virtualization replaced it (styles.css .row).
const ROW_HEIGHT = 29;

type SortKey = "proto" | "status" | "latency" | "time" | "node" | "bytes" | "packets";
type SortDir = "asc" | "desc";
interface SortState {
  key: SortKey;
  dir: SortDir;
}

interface ColumnDef {
  key: string;
  label: string;
  className: string;
  sortKey?: SortKey;
  mono?: boolean;
}

// node/bytes/packets are present on every entry but weren't previously shown
// anywhere in the table — optional columns, hidden by default.
const ALL_COLUMNS: ColumnDef[] = [
  { key: "proto", label: "proto", className: "col-proto", sortKey: "proto" },
  { key: "status", label: "status", className: "col-status", sortKey: "status" },
  { key: "summary", label: "summary", className: "col-summary", mono: true },
  { key: "source", label: "source", className: "col-src", mono: true },
  { key: "destination", label: "destination", className: "col-dst", mono: true },
  { key: "latency", label: "latency", className: "col-lat", sortKey: "latency", mono: true },
  { key: "time", label: "time", className: "col-time", sortKey: "time", mono: true },
  { key: "node", label: "node", className: "col-node", sortKey: "node", mono: true },
  { key: "bytes", label: "bytes", className: "col-bytes", sortKey: "bytes", mono: true },
  { key: "packets", label: "packets", className: "col-packets", sortKey: "packets", mono: true },
];
const ALWAYS_ON = new Set(["proto", "status"]);
const DEFAULT_VISIBLE = ["proto", "status", "summary", "source", "destination", "latency", "time"];
const VISIBLE_COLUMNS_KEY = "k8shark.columns";

function loadVisibleColumns(): Set<string> {
  try {
    const raw = localStorage.getItem(VISIBLE_COLUMNS_KEY);
    if (raw) return new Set(JSON.parse(raw));
  } catch {
    // corrupt/inaccessible storage — fall through to defaults
  }
  return new Set(DEFAULT_VISIBLE);
}

function sortValue(e: Entry, key: SortKey): number | string {
  switch (key) {
    case "proto":
      return e.protocol;
    case "status":
      return e.status || "";
    case "latency":
      return e.elapsedMs;
    case "time":
      return new Date(e.timestamp).getTime();
    case "node":
      return e.node || "";
    case "bytes":
      return e.request.bytes ?? 0;
    case "packets":
      return e.request.packets ?? 0;
  }
}

function compareEntries(a: Entry, b: Entry, key: SortKey): number {
  const va = sortValue(a, key);
  const vb = sortValue(b, key);
  if (typeof va === "number" && typeof vb === "number") return va - vb;
  return String(va).localeCompare(String(vb));
}

function cellContent(key: string, e: Entry): ReactNode {
  switch (key) {
    case "proto": {
      const color = PROTO_COLORS[e.protocol] ?? "#888";
      return (
        <span className="proto-badge" style={{ background: color }}>
          {e.protocol}
        </span>
      );
    }
    case "status":
      return <StatusBadge entry={e} />;
    case "summary":
      return e.request.summary || "—";
    case "source":
      return endpoint(e.src);
    case "destination":
      return endpoint(e.dst);
    case "latency":
      return `${e.elapsedMs}ms`;
    case "time":
      return time(e.timestamp);
    case "node":
      return e.node || "—";
    case "bytes":
      return e.request.bytes ? String(e.request.bytes) : "—";
    case "packets":
      return e.request.packets ? String(e.request.packets) : "—";
    default:
      return null;
  }
}

interface Props {
  entries: Entry[];
  selectedId: string | null;
  onSelect: (e: Entry) => void;
  onLoadOlder: () => void;
  loadingOlder: boolean;
  noMoreHistory: boolean;
  pinnedIds: Set<string>;
  onTogglePin: (e: Entry) => void;
  onCompare: () => void;
}

export const TrafficTable = memo(function TrafficTable({
  entries,
  selectedId,
  onSelect,
  onLoadOlder,
  loadingOlder,
  noMoreHistory,
  pinnedIds,
  onTogglePin,
  onCompare,
}: Props) {
  const [visible, setVisible] = useState<Set<string>>(loadVisibleColumns);
  const [sort, setSort] = useState<SortState | null>(null);

  useEffect(() => {
    localStorage.setItem(VISIBLE_COLUMNS_KEY, JSON.stringify([...visible]));
  }, [visible]);

  const toggleColumn = (key: string) => {
    if (ALWAYS_ON.has(key)) return;
    setVisible((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const onHeaderClick = (key: SortKey) => {
    setSort((prev) => {
      if (!prev || prev.key !== key) return { key, dir: "asc" };
      if (prev.dir === "asc") return { key, dir: "desc" };
      return null;
    });
  };

  const columns = useMemo(() => ALL_COLUMNS.filter((c) => visible.has(c.key)), [visible]);

  const displayEntries = useMemo(() => {
    if (!sort) return entries;
    const copy = entries.slice();
    copy.sort((a, b) => compareEntries(a, b, sort.key) * (sort.dir === "asc" ? 1 : -1));
    return copy;
  }, [entries, sort]);

  // Real DOM windowing: only rows actually in (or near) the viewport get
  // mounted, unlike the old content-visibility:auto trick which still kept
  // every row in the DOM for React to reconcile. Table semantics (sticky
  // thead, real <tr>/<td>) are preserved via the padding-row technique
  // instead of absolutely-positioned rows, which table layout doesn't
  // support well.
  const scrollRef = useRef<HTMLDivElement>(null);
  const rowVirtualizer = useVirtualizer({
    count: displayEntries.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  });
  const virtualRows = rowVirtualizer.getVirtualItems();
  const paddingTop = virtualRows.length > 0 ? virtualRows[0].start : 0;
  const paddingBottom =
    virtualRows.length > 0 ? rowVirtualizer.getTotalSize() - virtualRows[virtualRows.length - 1].end : 0;
  const colSpan = columns.length + 1;

  return (
    <div className="table-wrap-outer">
      <div className="table-toolbar">
        <ColumnPicker visible={visible} onToggle={toggleColumn} />
        {pinnedIds.size > 0 && (
          <button type="button" className="chip" onClick={onCompare} disabled={pinnedIds.size !== 2}>
            {pinnedIds.size === 2 ? "compare pinned (2)" : `pinned ${pinnedIds.size}/2 — pick one more`}
          </button>
        )}
        {sort && (
          <span className="table-toolbar-hint">
            sorted by {sort.key} ({sort.dir}) — click the header again to change, a third click resets
          </span>
        )}
      </div>
      <div className="table-wrap" ref={scrollRef}>
        <table className="traffic">
          <thead>
            <tr>
              <th className="col-pin" title="pin up to two entries to compare"></th>
              {columns.map((c) => {
                const active = !!sort && !!c.sortKey && sort.key === c.sortKey;
                return (
                  <th
                    key={c.key}
                    className={c.className}
                    onClick={c.sortKey ? () => onHeaderClick(c.sortKey!) : undefined}
                    aria-sort={active ? (sort!.dir === "asc" ? "ascending" : "descending") : undefined}
                    style={c.sortKey ? { cursor: "pointer" } : undefined}
                  >
                    {c.label}
                    {active && <span className="sort-arrow">{sort!.dir === "asc" ? " ▲" : " ▼"}</span>}
                  </th>
                );
              })}
            </tr>
          </thead>
          <tbody>
            {entries.length === 0 && (
              <tr className="empty">
                <td colSpan={colSpan}>Waiting for traffic… (workers stream matching entries here in real time)</td>
              </tr>
            )}
            {paddingTop > 0 && (
              <tr aria-hidden="true">
                <td colSpan={colSpan} style={{ height: paddingTop, padding: 0, border: "none" }} />
              </tr>
            )}
            {virtualRows.map((vRow) => {
              const e = displayEntries[vRow.index];
              return (
                <Row
                  key={e.id}
                  e={e}
                  columns={columns}
                  selected={e.id === selectedId}
                  onSelect={onSelect}
                  pinned={pinnedIds.has(e.id)}
                  onTogglePin={onTogglePin}
                />
              );
            })}
            {paddingBottom > 0 && (
              <tr aria-hidden="true">
                <td colSpan={colSpan} style={{ height: paddingBottom, padding: 0, border: "none" }} />
              </tr>
            )}
            {entries.length > 0 && (
              <tr className="load-older">
                <td colSpan={colSpan}>
                  {noMoreHistory ? (
                    <span className="load-older-note">no more history</span>
                  ) : (
                    <button type="button" className="chip" onClick={onLoadOlder} disabled={loadingOlder}>
                      {loadingOlder ? "loading…" : "load older"}
                    </button>
                  )}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
});

function ColumnPicker({ visible, onToggle }: { visible: Set<string>; onToggle: (key: string) => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDocMouseDown = (ev: MouseEvent) => {
      if (ref.current && !ref.current.contains(ev.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDocMouseDown);
    return () => document.removeEventListener("mousedown", onDocMouseDown);
  }, [open]);

  return (
    <div className="col-picker" ref={ref}>
      <button type="button" className="chip" onClick={() => setOpen((o) => !o)} aria-expanded={open} aria-haspopup="true">
        columns ▾
      </button>
      {open && (
        <div className="col-picker-menu" role="menu">
          {ALL_COLUMNS.filter((c) => !ALWAYS_ON.has(c.key)).map((c) => (
            <label key={c.key} className="col-picker-item">
              <input type="checkbox" checked={visible.has(c.key)} onChange={() => onToggle(c.key)} />
              {c.label}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}

const Row = memo(function Row({
  e,
  columns,
  selected,
  onSelect,
  pinned,
  onTogglePin,
}: {
  e: Entry;
  columns: ColumnDef[];
  selected: boolean;
  onSelect: (e: Entry) => void;
  pinned: boolean;
  onTogglePin: (e: Entry) => void;
}) {
  return (
    <tr
      className={`row ${selected ? "sel" : ""} st-${e.status || "na"}`}
      role="button"
      tabIndex={0}
      onClick={() => onSelect(e)}
      onKeyDown={(ev) => {
        if (ev.key === "Enter" || ev.key === " ") {
          ev.preventDefault();
          onSelect(e);
        }
      }}
    >
      <td className="col-pin" onClick={(ev) => ev.stopPropagation()}>
        <input
          type="checkbox"
          checked={pinned}
          onChange={() => onTogglePin(e)}
          title="pin to compare"
          aria-label={`pin ${e.request.summary || e.id} to compare`}
        />
      </td>
      {columns.map((c) => (
        <td key={c.key} className={`${c.className}${c.mono ? " mono" : ""}`}>
          {cellContent(c.key, e)}
        </td>
      ))}
    </tr>
  );
});

function StatusBadge({ entry }: { entry: Entry }) {
  if (entry.protocol === "http") {
    return <span className={`code st-${entry.status}`}>{entry.statusCode || "—"}</span>;
  }
  return <span className={`code st-${entry.status}`}>{entry.status || "ok"}</span>;
}

function endpoint(ep: { name?: string; ip: string; port: number; namespace?: string }): string {
  if (ep.name) return ep.namespace ? `${ep.name}.${ep.namespace}` : ep.name;
  return `${ep.ip}:${ep.port}`;
}

function time(ts: string): string {
  const d = new Date(ts);
  return d.toLocaleTimeString([], { hour12: false }) + "." + String(d.getMilliseconds()).padStart(3, "0");
}
