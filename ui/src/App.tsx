import { useMemo, useState } from "react";
import { useHub } from "./useHub";
import { StatsHeader } from "./components/StatsHeader";
import { FilterBar } from "./components/FilterBar";
import { TrafficTable } from "./components/TrafficTable";
import { EntryDetail } from "./components/EntryDetail";
import { ServiceMap } from "./components/ServiceMap";
import type { Entry } from "./types";

type View = "list" | "map";

export function App() {
  const [filter, setFilter] = useState("");
  const hub = useHub("");
  const [selected, setSelected] = useState<Entry | null>(null);
  const [view, setView] = useState<View>("list");

  const onApply = (f: string) => {
    setFilter(f);
    hub.applyFilter(f);
    setSelected(null);
  };

  // Which protocol the current filter pins to (for the active-pill highlight),
  // or null. Only recognises the exact "protocol == X" shape the pills emit.
  const activeProto = useMemo(() => {
    const m = filter.trim().match(/^protocol\s*==\s*(\S+)$/);
    return m ? m[1] : null;
  }, [filter]);

  // Click a protocol pill to filter to it; click the active one again to clear.
  const onProtoClick = (proto: string) => {
    onApply(activeProto === proto ? "" : `protocol == ${proto}`);
  };

  // Keep the selected entry object in sync with the freshest list reference.
  const selectedLive = useMemo(
    () => (selected ? hub.entries.find((e) => e.id === selected.id) ?? selected : null),
    [selected, hub.entries]
  );

  return (
    <div className="app">
      <StatsHeader
        stats={hub.stats}
        connected={hub.connected}
        onProtoClick={onProtoClick}
        activeProto={activeProto}
      />
      <FilterBar
        value={filter}
        onApply={onApply}
        paused={hub.paused}
        onTogglePause={() => hub.setPaused(!hub.paused)}
        onClear={hub.clear}
        view={view}
        onViewChange={setView}
        count={hub.entries.length}
      />
      {view === "list" ? (
        <div className="main split">
          <TrafficTable
            entries={hub.entries}
            selectedId={selectedLive?.id ?? null}
            onSelect={setSelected}
          />
          {selectedLive && (
            <EntryDetail entry={selectedLive} onClose={() => setSelected(null)} />
          )}
        </div>
      ) : (
        <div className="main">
          <ServiceMap entries={hub.entries} />
        </div>
      )}
    </div>
  );
}
