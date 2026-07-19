import { useEffect, useMemo, useState } from "react";
import { useHub } from "./useHub";
import { StatsHeader } from "./components/StatsHeader";
import { FilterBar } from "./components/FilterBar";
import { TrafficTable } from "./components/TrafficTable";
import { EntryDetail } from "./components/EntryDetail";
import { ServiceMap } from "./components/ServiceMap";
import { CompareView } from "./components/CompareView";
import { isTypingTarget } from "./dom";
import type { Entry } from "./types";

type View = "list" | "map";

const PROTO_CLAUSE_RE = /\bprotocol\s*==\s*"?([\w.-]+)"?/i;

// Extracts the protocol a "protocol == x" clause pins the filter to, wherever
// it appears in the expression (not just when it's the whole filter).
function activeProtocol(filter: string): string | null {
  const m = filter.match(PROTO_CLAUSE_RE);
  return m ? m[1] : null;
}

// Toggles a "protocol == x" clause: adds it (joined with "and") if absent,
// swaps the value if a different protocol is already pinned, or removes it —
// plus one adjacent connective — if it's already the active one. Preserves
// the rest of a compound filter instead of clobbering it.
function toggleProtoFilter(filter: string, proto: string): string {
  const m = PROTO_CLAUSE_RE.exec(filter);
  if (!m) {
    const trimmed = filter.trim();
    return trimmed ? `${trimmed} and protocol == ${proto}` : `protocol == ${proto}`;
  }
  if (m[1].toLowerCase() !== proto.toLowerCase()) {
    return filter.slice(0, m.index) + `protocol == ${proto}` + filter.slice(m.index + m[0].length);
  }
  const before = filter.slice(0, m.index).replace(/\s*\b(and|or)\s*$/i, "").trimEnd();
  const after = filter.slice(m.index + m[0].length).replace(/^\s*\b(and|or)\s*/i, "").trimStart();
  return before && after ? `${before} and ${after}` : before || after;
}

export function App() {
  const [filter, setFilter] = useState(() => new URLSearchParams(location.search).get("filter") ?? "");
  const hub = useHub(filter);
  const { paused, setPaused } = hub;
  const [selected, setSelected] = useState<Entry | null>(null);
  const [view, setView] = useState<View>(() =>
    new URLSearchParams(location.search).get("view") === "map" ? "map" : "list"
  );
  const [pinned, setPinned] = useState<Entry[]>([]);
  const [showCompare, setShowCompare] = useState(false);

  // Pin up to two entries for the compare view; pinning a third drops the
  // oldest pin. Pinning holds a snapshot of the entry, not just its id, so a
  // pin survives the entry aging out of the live buffer.
  const togglePin = (e: Entry) => {
    setPinned((prev) => {
      if (prev.some((p) => p.id === e.id)) return prev.filter((p) => p.id !== e.id);
      if (prev.length >= 2) return [prev[1], e];
      return [...prev, e];
    });
  };

  const onApply = (f: string) => {
    setFilter(f);
    hub.applyFilter(f);
    setSelected(null);
  };

  // Resolve a ?entry=<id> permalink on first load. Not necessarily in the
  // live/replayed buffer yet (or ever, if it aged out) — fetch it directly.
  useEffect(() => {
    const id = new URLSearchParams(location.search).get("entry");
    if (!id) return;
    fetch(`/api/entry/${encodeURIComponent(id)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((e) => e && setSelected(e))
      .catch(() => {});
  }, []);

  // Keep the URL in sync with filter/view/selection so the current view is
  // bookmarkable and shareable, without piling up history entries.
  useEffect(() => {
    const params = new URLSearchParams();
    if (filter) params.set("filter", filter);
    if (view !== "list") params.set("view", view);
    if (selected) params.set("entry", selected.id);
    const qs = params.toString();
    history.replaceState(null, "", qs ? `${location.pathname}?${qs}` : location.pathname);
  }, [filter, view, selected]);

  const activeProto = useMemo(() => activeProtocol(filter), [filter]);

  // Click a protocol pill to add/swap/remove its clause in the filter.
  const onProtoClick = (proto: string) => {
    onApply(toggleProtoFilter(filter, proto));
  };

  // Global shortcuts: "/" focuses the filter (unless already typing
  // somewhere), space toggles pause (only when nothing specific has focus,
  // so it doesn't fight a focused row/button's own Space handling), Escape
  // closes the detail panel.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "/" && !isTypingTarget(e.target)) {
        e.preventDefault();
        document.getElementById("filter-input")?.focus();
      } else if (e.key === " " && document.activeElement === document.body) {
        e.preventDefault();
        setPaused(!paused);
      } else if (e.key === "Escape") {
        setSelected(null);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [paused, setPaused]);

  // Keep the selected entry object in sync with the freshest list reference.
  const selectedLive = useMemo(
    () => (selected ? hub.entries.find((e) => e.id === selected.id) ?? selected : null),
    [selected, hub.entries]
  );

  const pinnedIds = useMemo(() => new Set(pinned.map((p) => p.id)), [pinned]);

  return (
    <div className="app">
      <StatsHeader
        stats={hub.stats}
        statsHistory={hub.statsHistory}
        connected={hub.connected}
        onProtoClick={onProtoClick}
        activeProto={activeProto}
      />
      <FilterBar
        value={filter}
        onApply={onApply}
        paused={hub.paused}
        pausedCount={hub.pausedCount}
        onTogglePause={() => hub.setPaused(!hub.paused)}
        onClear={hub.clear}
        view={view}
        onViewChange={setView}
        count={hub.entries.length}
        truncated={hub.truncated}
        filterError={hub.filterError}
        entries={hub.entries}
      />
      {view === "list" ? (
        <div className="main split">
          <TrafficTable
            entries={hub.entries}
            selectedId={selectedLive?.id ?? null}
            onSelect={setSelected}
            onLoadOlder={hub.loadOlder}
            loadingOlder={hub.loadingOlder}
            noMoreHistory={hub.noMoreHistory}
            pinnedIds={pinnedIds}
            onTogglePin={togglePin}
            onCompare={() => setShowCompare(true)}
          />
          {selectedLive && (
            <EntryDetail entry={selectedLive} onClose={() => setSelected(null)} />
          )}
          {showCompare && pinned.length === 2 && (
            <CompareView a={pinned[0]} b={pinned[1]} onClose={() => setShowCompare(false)} />
          )}
        </div>
      ) : (
        <div className="main">
          <ServiceMap
            entries={hub.entries}
            onNodeClick={(clause) => {
              onApply(clause);
              setView("list");
            }}
          />
        </div>
      )}
    </div>
  );
}
