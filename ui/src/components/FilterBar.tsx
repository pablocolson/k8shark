import type { KeyboardEvent } from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import { contextAt } from "../filterParse";
import { useFields } from "../useFields";
import { MAX_ENTRIES } from "../useHub";
import { downloadFile, entriesToCSV, entriesToJSON } from "../export";
import { entriesToPcap } from "../pcap";
import type { HistoricalRange } from "../useHub";
import type { Entry } from "../types";
import { FilterSuggest, pickInsertion, useSuggestItems } from "./FilterSuggest";

// rangeLabel renders a historical selection compactly for the "back to
// live" button; rangeTitle spells out the full dates in a hover tooltip
// (the button text alone drops the date, which matters across midnight).
function rangeLabel(r: HistoricalRange): string {
  const fmt = (iso: string) => new Date(iso).toLocaleTimeString([], { hour12: false });
  return `${fmt(r.since)}–${fmt(r.until)}`;
}
function rangeTitle(r: HistoricalRange): string {
  return `viewing ${new Date(r.since).toLocaleString([], { hour12: false })} to ${new Date(r.until).toLocaleString([], { hour12: false })}`;
}

const EXAMPLES = [
  'http.method == "POST"',
  "response.status >= 500",
  'protocol == "postgres"',
  'redis.command contains "SET"',
  'dst.namespace == "shop"',
  'request.path contains "checkout"',
];

// Recently-applied filters, persisted client-side so a returning session (or
// a typo-prone IFL expression) doesn't have to be retyped from scratch.
const RECENT_FILTERS_KEY = "k8shark.recentFilters";
const RECENT_FILTERS_CAP = 10;

function loadRecentFilters(): string[] {
  try {
    const raw = localStorage.getItem(RECENT_FILTERS_KEY);
    if (raw) return JSON.parse(raw);
  } catch {
    // corrupt/inaccessible storage — start fresh
  }
  return [];
}

// Records f as the most recent filter (deduplicated, capped), persisting the
// result. A blank filter (cleared, not "applied") is a no-op.
function saveRecentFilter(filters: string[], f: string): string[] {
  if (!f) return filters;
  const next = [f, ...filters.filter((x) => x !== f)].slice(0, RECENT_FILTERS_CAP);
  try {
    localStorage.setItem(RECENT_FILTERS_KEY, JSON.stringify(next));
  } catch {
    // storage full/disabled — history just won't survive a reload
  }
  return next;
}

interface Props {
  value: string;
  onApply: (f: string) => void;
  paused: boolean;
  pausedCount: number;
  onTogglePause: () => void;
  onClear: () => void;
  view: "list" | "map" | "top";
  onViewChange: (v: "list" | "map" | "top") => void;
  count: number;
  truncated: boolean;
  filterError: string | null;
  entries: Entry[];
  historicalRange: HistoricalRange | null;
  onReturnToLive: () => void;
}

export function FilterBar({
  value,
  onApply,
  paused,
  pausedCount,
  onTogglePause,
  onClear,
  view,
  onViewChange,
  count,
  truncated,
  filterError,
  entries,
  historicalRange,
  onReturnToLive,
}: Props) {
  const [draft, setDraft] = useState(value);
  const [caret, setCaret] = useState(value.length);
  const [focused, setFocused] = useState(false);
  const [open, setOpen] = useState(false);
  // -1 means no suggestion is highlighted: Enter submits the filter as
  // typed instead of picking one (see the reset effect and handleKeyDown's
  // Enter case below — auto-highlighting index 0 made Enter silently pick a
  // suggestion instead of applying an already-complete, valid filter).
  const [highlightIndex, setHighlightIndex] = useState(-1);
  const [recentFilters, setRecentFilters] = useState<string[]>(() => loadRecentFilters());
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setDraft(value);
    setOpen(false);
  }, [value]);

  const { fields, byName, lazyValues } = useFields();
  const ctx = useMemo(() => contextAt(draft, caret), [draft, caret]);
  const { items, hint } = useSuggestItems(ctx, fields, byName, lazyValues);

  // An empty, focused input offers recent filter history instead of the full
  // (and, empty, unfiltered) field-name list — much more likely to be what's
  // wanted at that exact moment. Falls back to the normal suggestions once
  // there's no history yet (first run) or the user starts typing.
  const showingRecent = draft === "" && recentFilters.length > 0;
  const activeCount = showingRecent ? recentFilters.length : items.length;

  // Applies f (a full IFL expression, not a token) and records it as the most
  // recently used filter — shared by the recent-history list, Enter/Tab on a
  // highlighted history item, and the static EXAMPLES chips below.
  const applyAndRecord = (f: string) => {
    setRecentFilters((prev) => saveRecentFilter(prev, f));
    onApply(f);
  };

  // Reset the highlight whenever the candidate list changes shape, and open
  // the dropdown only while the input is actually focused (avoids it
  // reappearing after a filter is applied and focus has moved elsewhere).
  useEffect(() => {
    setHighlightIndex(-1);
    setOpen(focused && (activeCount > 0 || !!hint));
  }, [focused, activeCount, hint]);

  const updateCaretFrom = (el: HTMLInputElement) => setCaret(el.selectionStart ?? el.value.length);

  // Shared accept path for both mouse (via FilterSuggest's onPick) and
  // keyboard (Enter/Tab below) selection: splice the formatted insertion
  // into `draft` at the current token's span and move the caret to the end
  // of what was inserted.
  const handlePick = (insertText: string, newCaret: number) => {
    setDraft((prev) => prev.slice(0, ctx.tokenStart) + insertText + prev.slice(ctx.tokenEnd));
    setCaret(newCaret);
    setOpen(false);
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(newCaret, newCaret);
      }
    });
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (!open || activeCount === 0) return;
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setHighlightIndex((i) => (i + 1) % activeCount);
        return;
      case "ArrowUp":
        e.preventDefault();
        setHighlightIndex((i) => (i <= 0 ? activeCount - 1 : i - 1));
        return;
      case "Enter": {
        // Only intercept Enter when the user has actually navigated to a
        // suggestion (highlightIndex >= 0); otherwise let it fall through to
        // the form's onSubmit so a fully-typed, valid filter applies on
        // Enter like any other text input, instead of silently picking
        // whatever suggestion happens to be listed first.
        if (showingRecent) {
          const f = recentFilters[highlightIndex];
          if (f) {
            e.preventDefault();
            setOpen(false);
            applyAndRecord(f);
          }
          return;
        }
        const item = items[highlightIndex];
        if (item) {
          e.preventDefault();
          const { text, caret: c } = pickInsertion(ctx, byName, item);
          handlePick(text, c);
        }
        return;
      }
      case "Tab": {
        if (showingRecent) {
          const f = recentFilters[highlightIndex];
          if (f) {
            setOpen(false);
            applyAndRecord(f);
          }
          return;
        }
        const item = items[highlightIndex];
        if (item) {
          // Don't preventDefault: let focus move on as Tab normally would,
          // after applying the pick synchronously.
          const { text, caret: c } = pickInsertion(ctx, byName, item);
          handlePick(text, c);
        }
        return;
      }
      case "Escape":
        e.preventDefault();
        setOpen(false);
        return;
      default:
        return;
    }
  };

  return (
    <div className="filterbar">
      <form
        className="filter-form"
        onSubmit={(e) => {
          e.preventDefault();
          setOpen(false);
          applyAndRecord(draft.trim());
        }}
      >
        <span className="filter-prompt">ifl›</span>
        <input
          id="filter-input"
          ref={inputRef}
          className="filter-input"
          placeholder='filter, e.g.  response.status >= 500 and dst.name == "checkout"'
          aria-label="IFL filter expression"
          role="combobox"
          aria-autocomplete="list"
          aria-expanded={open}
          aria-controls="filter-suggest-listbox"
          aria-activedescendant={
            open && (showingRecent ? recentFilters[highlightIndex] : items[highlightIndex])
              ? `filter-suggest-opt-${highlightIndex}`
              : undefined
          }
          value={draft}
          spellCheck={false}
          onChange={(e) => {
            setDraft(e.target.value);
            updateCaretFrom(e.target);
          }}
          onKeyUp={(e) => updateCaretFrom(e.currentTarget)}
          onClick={(e) => updateCaretFrom(e.currentTarget)}
          onSelect={(e) => updateCaretFrom(e.currentTarget)}
          onKeyDown={handleKeyDown}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
        />
        {open &&
          (showingRecent ? (
            <div className="suggest" role="listbox" id="filter-suggest-listbox" aria-label="recent filters">
              {recentFilters.map((f, i) => (
                <div
                  key={f}
                  id={`filter-suggest-opt-${i}`}
                  role="option"
                  aria-selected={i === highlightIndex}
                  className={`suggest-item${i === highlightIndex ? " active" : ""}`}
                  onMouseDown={(e) => {
                    // mousedown (not click) fires before the input's blur handler.
                    e.preventDefault();
                    setOpen(false);
                    applyAndRecord(f);
                  }}
                >
                  <span className="suggest-label mono">{f}</span>
                </div>
              ))}
            </div>
          ) : (
            <FilterSuggest
              ctx={ctx}
              byName={byName}
              items={items}
              hint={hint}
              highlightIndex={highlightIndex}
              onPick={handlePick}
            />
          ))}
        {draft && (
          <button type="button" className="icon-btn" title="clear filter" aria-label="clear filter" onClick={() => { setDraft(""); setOpen(false); onApply(""); }}>
            ✕
          </button>
        )}
        <button type="submit" className="apply-btn">
          Apply
        </button>
      </form>

      {filterError && (
        <div className="filter-error" role="alert">
          invalid filter: {filterError}
        </div>
      )}

      <div className="filter-actions">
        <span className="count" aria-live="polite">
          {count} shown{truncated ? ` · showing latest ${MAX_ENTRIES}` : ""}
        </span>
        {historicalRange ? (
          <button className="toggle active" onClick={onReturnToLive} title={rangeTitle(historicalRange)}>
            ◀ back to live ({rangeLabel(historicalRange)})
          </button>
        ) : (
          <button className={`toggle ${paused ? "active" : ""}`} onClick={onTogglePause}>
            {paused ? `▶ Resume${pausedCount > 0 ? ` (${pausedCount} new)` : ""}` : "⏸ Pause"}
          </button>
        )}
        <button className="toggle" onClick={onClear}>
          Clear
        </button>
        <div className="view-switch">
          <button className={view === "list" ? "active" : ""} onClick={() => onViewChange("list")}>
            List
          </button>
          <button className={view === "map" ? "active" : ""} onClick={() => onViewChange("map")}>
            Map
          </button>
          <button className={view === "top" ? "active" : ""} onClick={() => onViewChange("top")}>
            Top
          </button>
        </div>
        <ExportMenu entries={entries} />
      </div>

      <div className="examples">
        {EXAMPLES.map((ex) => (
          <button key={ex} className="chip" onClick={() => applyAndRecord(ex)}>
            {ex}
          </button>
        ))}
      </div>
    </div>
  );
}

// ExportMenu downloads the entries currently loaded client-side (i.e.
// whatever the live/filtered buffer holds right now) as JSON, CSV, or a
// synthesized PCAP. Purely local — a Blob built from data already in the
// page, no server round trip.
function ExportMenu({ entries }: { entries: Entry[] }) {
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

  const doExport = (format: "json" | "csv" | "pcap") => {
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    if (format === "json") {
      downloadFile(entriesToJSON(entries), `k8shark-entries-${stamp}.json`, "application/json");
    } else if (format === "csv") {
      downloadFile(entriesToCSV(entries), `k8shark-entries-${stamp}.csv`, "text/csv");
    } else {
      downloadFile(entriesToPcap(entries), `k8shark-entries-${stamp}.pcap`, "application/vnd.tcpdump.pcap");
    }
    setOpen(false);
  };

  return (
    <div className="col-picker" ref={ref}>
      <button
        type="button"
        className="toggle"
        onClick={() => setOpen((o) => !o)}
        disabled={entries.length === 0}
        aria-expanded={open}
        aria-haspopup="true"
        title={entries.length === 0 ? "no entries to export yet" : `export ${entries.length} shown entries`}
      >
        export ▾
      </button>
      {open && (
        <div className="col-picker-menu" role="menu">
          <button type="button" className="col-picker-item" onClick={() => doExport("json")}>
            as JSON
          </button>
          <button type="button" className="col-picker-item" onClick={() => doExport("csv")}>
            as CSV
          </button>
          <button
            type="button"
            className="col-picker-item"
            onClick={() => doExport("pcap")}
            title="Synthesized from already-captured payload bytes and L4 metadata — not a live packet capture"
          >
            as PCAP
          </button>
        </div>
      )}
    </div>
  );
}
