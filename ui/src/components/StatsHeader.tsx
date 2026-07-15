import type { Stats } from "../types";

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

export function StatsHeader({
  stats,
  connected,
  onProtoClick,
  activeProto,
}: {
  stats: Stats | null;
  connected: boolean;
  onProtoClick?: (proto: string) => void;
  activeProto?: string | null;
}) {
  return (
    <header className="header">
      <div className="brand">
        <span className="logo">🦈</span>
        <span className="brand-name">k8shark</span>
      </div>

      <div className="stat-group">
        <Stat label="entries" value={stats ? fmt(stats.totalEntries) : "—"} />
        <Stat label="entries/s" value={stats ? stats.entriesPerSec.toFixed(1) : "—"} />
        <Stat label="workers" value={stats ? String(stats.workers) : "—"} />
      </div>

      <div className="proto-pills">
        {stats &&
          Object.entries(stats.byProtocol)
            .sort((a, b) => b[1] - a[1])
            .map(([p, n]) => (
              <button
                key={p}
                type="button"
                className={`pill${activeProto === p ? " active" : ""}`}
                style={{ borderColor: PROTO_COLORS[p] ?? "#888" }}
                onClick={() => onProtoClick?.(p)}
                title={`Filter: protocol == ${p}`}
                aria-pressed={activeProto === p}
              >
                <span className="dot" style={{ background: PROTO_COLORS[p] ?? "#888" }} />
                {p} <b>{fmt(n)}</b>
              </button>
            ))}
      </div>

      <div className={`conn ${connected ? "on" : "off"}`}>
        <span className="conn-dot" />
        {connected ? "live" : "reconnecting…"}
      </div>
    </header>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="stat">
      <span className="stat-value">{value}</span>
      <span className="stat-label">{label}</span>
    </div>
  );
}

function fmt(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(1) + "k";
  return String(n);
}
