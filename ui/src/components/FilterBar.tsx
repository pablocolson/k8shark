import type { KeyboardEvent } from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import { contextAt } from "../filterParse";
import { useFields } from "../useFields";
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
  onTogglePause: () => void;
  onClear: () => void;
  view: "list" | "map";
  onViewChange: (v: "list" | "map") => void;
  count: number;
}

export function FilterBar({
  value,
  onApply,
  paused,
  onTogglePause,
  onClear,
  view,
  onViewChange,
  count,
}: Props) {
  const [draft, setDraft] = useState(value);
  const [caret, setCaret] = useState(value.length);
  const [focused, setFocused] = useState(false);
  const [open, setOpen] = useState(false);
  const [highlightIndex, setHighlightIndex] = useState(0);
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
    setHighlightIndex(0);
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
        setHighlightIndex((i) => (i - 1 + items.length) % items.length);
        return;
      case "Enter": {
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
          ref={inputRef}
          className="filter-input"
          placeholder='filter, e.g.  response.status >= 500 and dst.name == "checkout"'
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

      <div className="filter-actions">
        <span className="count">{count} shown</span>
        <button className={`toggle ${paused ? "active" : ""}`} onClick={onTogglePause}>
          {paused ? "▶ Resume" : "⏸ Pause"}
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
