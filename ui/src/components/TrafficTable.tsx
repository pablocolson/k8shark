import { memo } from "react";
import type { Entry } from "../types";

const PROTO_COLORS: Record<string, string> = {
  http: "#4aa8ff",
  dns: "#b07cff",
  redis: "#ff6b6b",
  valkey: "#5a9e6f",
  postgres: "#22c3dd",
  amqp: "#ff8c42",
  tcp: "#7a8699",
  udp: "#7f9c6c",
  icmp: "#c98b6a",
};

interface Props {
  entries: Entry[];
  selectedId: string | null;
  onSelect: (e: Entry) => void;
}

export const TrafficTable = memo(function TrafficTable({ entries, selectedId, onSelect }: Props) {
  return (
    <div className="table-wrap">
      <table className="traffic">
        <thead>
          <tr>
            <th className="col-proto">proto</th>
            <th className="col-status">status</th>
            <th className="col-summary">summary</th>
            <th className="col-src">source</th>
            <th className="col-dst">destination</th>
            <th className="col-lat">latency</th>
            <th className="col-time">time</th>
          </tr>
        </thead>
        <tbody>
          {entries.length === 0 && (
            <tr className="empty">
              <td colSpan={7}>Waiting for traffic… (workers stream matching entries here in real time)</td>
            </tr>
          )}
          {entries.map((e) => (
            <Row key={e.id} e={e} selected={e.id === selectedId} onSelect={onSelect} />
          ))}
        </tbody>
      </table>
    </div>
  );
});

const Row = memo(function Row({
  e,
  selected,
  onSelect,
}: {
  e: Entry;
  selected: boolean;
  onSelect: (e: Entry) => void;
}) {
  const color = PROTO_COLORS[e.protocol] ?? "#888";
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
      <td className="col-proto">
        <span className="proto-badge" style={{ background: color }}>
          {e.protocol}
        </span>
      </td>
      <td className="col-status">
        <StatusBadge entry={e} />
      </td>
      <td className="col-summary mono">{e.request.summary || "—"}</td>
      <td className="col-src mono">{endpoint(e.src)}</td>
      <td className="col-dst mono">{endpoint(e.dst)}</td>
      <td className="col-lat mono">{e.elapsedMs}ms</td>
      <td className="col-time mono">{time(e.timestamp)}</td>
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
