import type { KeyboardEvent } from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import { contextAt } from "../filterParse";
import { useFields } from "../useFields";
import { MAX_ENTRIES } from "../useHub";
import { downloadFile, entriesToCSV, entriesToJSON } from "../export";
import { entriesToPcap } from "../pcap";
import type { Entry } from "../types";
import { FilterSuggest, pickInsertion, useSuggestItems } from "./FilterSuggest";

const EXAMPLES = [
  'http.method == "POST"',
  "response.status >= 500",
  'protocol == "postgres"',
  'redis.command contains "SET"',
  'dst.namespace == "shop"',
  'request.path contains "checkout"',
];

interface Props {
  value: string;
  onApply: (f: string) => void;
  paused: boolean;
  pausedCount: number;
  onTogglePause: () => void;
  onClear: () => void;
  view: "list" | "map";
  onViewChange: (v: "list" | "map") => void;
  count: number;
  truncated: boolean;
  filterError: string | null;
  entries: Entry[];
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
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setDraft(value);
    setOpen(false);
  }, [value]);

  const { fields, byName, lazyValues } = useFields();
  const ctx = useMemo(() => contextAt(draft, caret), [draft, caret]);
  const { items, hint } = useSuggestItems(ctx, fields, byName, lazyValues);

  // Reset the highlight whenever the candidate list changes shape, and open
  // the dropdown only while the input is actually focused (avoids it
  // reappearing after a filter is applied and focus has moved elsewhere).
  useEffect(() => {
    setHighlightIndex(-1);
    setOpen(focused && (items.length > 0 || !!hint));
  }, [focused, items, hint]);

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
    if (!open || items.length === 0) return;
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setHighlightIndex((i) => (i + 1) % items.length);
        return;
      case "ArrowUp":
        e.preventDefault();
        setHighlightIndex((i) => (i <= 0 ? items.length - 1 : i - 1));
        return;
      case "Enter": {
        // Only intercept Enter when the user has actually navigated to a
        // suggestion (highlightIndex >= 0); otherwise let it fall through to
        // the form's onSubmit so a fully-typed, valid filter applies on
        // Enter like any other text input, instead of silently picking
        // whatever suggestion happens to be listed first.
        const item = items[highlightIndex];
        if (item) {
          e.preventDefault();
          const { text, caret: c } = pickInsertion(ctx, byName, item);
          handlePick(text, c);
        }
        return;
      }
      case "Tab": {
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
          onApply(draft.trim());
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
          aria-activedescendant={open && items[highlightIndex] ? `filter-suggest-opt-${highlightIndex}` : undefined}
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
        {open && (
          <FilterSuggest
            ctx={ctx}
            byName={byName}
            items={items}
            hint={hint}
            highlightIndex={highlightIndex}
            onPick={handlePick}
          />
        )}
        {draft && (
          <button type="button" className="icon-btn" title="clear filter" aria-label="clear filter" onClick={() => { setDraft(""); setOpen(false); onApply(""); }}>
            ✕
          </button>
        )}
        <button type="submit" className="apply-btn">
          Apply
        </button>
      </form>

      {filterError && <div className="filter-error">invalid filter: {filterError}</div>}

      <div className="filter-actions">
        <span className="count">
          {count} shown{truncated ? ` · showing latest ${MAX_ENTRIES}` : ""}
        </span>
        <button className={`toggle ${paused ? "active" : ""}`} onClick={onTogglePause}>
          {paused ? `▶ Resume${pausedCount > 0 ? ` (${pausedCount} new)` : ""}` : "⏸ Pause"}
        </button>
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
        </div>
        <ExportMenu entries={entries} />
      </div>

      <div className="examples">
        {EXAMPLES.map((ex) => (
          <button key={ex} className="chip" onClick={() => onApply(ex)}>
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
