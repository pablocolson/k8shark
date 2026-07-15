import { useEffect, useMemo, useState } from "react";
import type { CaretContext } from "../filterParse";
import type { FieldMeta, FieldType, FieldValue } from "../types";

// --- candidate model ---------------------------------------------------

export interface SuggestItem {
  insertText: string; // raw value fed into renderInsertion (unquoted, unescaped)
  label: string; // primary text shown in the dropdown
  badge?: string; // small type badge (used for "field" kind items)
  count?: number; // observed-count badge (used for "value" kind items)
}

const VALUE_DEBOUNCE_MS = 150;

// useSuggestItems builds the candidate list for the current caret context.
// It's a hook (rather than a plain function) because "value" suggestions for
// tracked fields need a debounced live fetch (via lazyValues) once the user
// has typed something, layered on top of the field's static snapshot values.
//
// This is called once, in FilterBar, so there is a single source of truth
// for "how many candidates are there right now" — both the keyboard
// (ArrowUp/Down wraparound, Enter/Tab accept) and the rendered dropdown
// (FilterSuggest below) must agree on the same list, so we don't compute it
// twice (which would also mean firing the debounced fetch twice).
export function useSuggestItems(
  ctx: CaretContext,
  fields: FieldMeta[],
  byName: Map<string, FieldMeta>,
  lazyValues: (field: string, prefix: string) => Promise<FieldValue[]>
): { items: SuggestItem[]; hint?: string } {
  const [asyncValues, setAsyncValues] = useState<FieldValue[] | null>(null);

  useEffect(() => {
    setAsyncValues(null);
    if (ctx.kind !== "value" || !ctx.fieldName || !ctx.prefix) return;
    const field = byName.get(ctx.fieldName);
    if (!field || !field.values) return; // freetext/untracked: nothing to fetch

    let cancelled = false;
    const id = setTimeout(() => {
      lazyValues(ctx.fieldName as string, ctx.prefix).then((vals) => {
        if (!cancelled) setAsyncValues(vals);
      });
    }, VALUE_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(id);
    };
  }, [ctx.kind, ctx.fieldName, ctx.prefix, byName, lazyValues]);

  return useMemo(() => buildItems(ctx, fields, byName, asyncValues), [ctx, fields, byName, asyncValues]);
}

function buildItems(
  ctx: CaretContext,
  fields: FieldMeta[],
  byName: Map<string, FieldMeta>,
  asyncValues: FieldValue[] | null
): { items: SuggestItem[]; hint?: string } {
  const prefixLower = ctx.prefix.toLowerCase();

  switch (ctx.kind) {
    case "field": {
      const items = fields
        .filter((f) => f.name.toLowerCase().startsWith(prefixLower))
        .map((f) => ({ insertText: f.name, label: f.name, badge: f.type }));
      return { items };
    }
    case "operator": {
      const field = ctx.fieldName ? byName.get(ctx.fieldName) : undefined;
      const items = (field?.operators ?? [])
        .filter((op) => op.toLowerCase().startsWith(prefixLower))
        .map((op) => ({ insertText: op, label: op }));
      return { items };
    }
    case "boolean": {
      const items = ["and", "or", "not"]
        .filter((kw) => kw.startsWith(prefixLower))
        .map((kw) => ({ insertText: kw, label: kw }));
      return { items };
    }
    case "value": {
      const field = ctx.fieldName ? byName.get(ctx.fieldName) : undefined;
      if (!field) return { items: [] };
      if (!field.values) return { items: [], hint: "free text — no suggestions" };
      // Prefer the freshly-fetched (debounced) values once the user has
      // typed something; fall back to the bulk snapshot otherwise (initial
      // list, or while the fetch is in flight).
      const source = ctx.prefix && asyncValues ? asyncValues : field.values;
      const items = source
        .filter((v) => v.value.toLowerCase().startsWith(prefixLower))
        .map((v) => ({ insertText: v.value, label: v.value, count: v.count }));
      return { items };
    }
    default:
      return { items: [] };
  }
}

// --- insertion formatting -----------------------------------------------

// renderInsertion is the load-bearing formatting function: it decides how a
// picked candidate's raw text gets spliced into the filter string.
//  - "value" on a number-typed field: inserted raw, unquoted.
//  - "value" on enum/string/freetext: double-quoted, with `"` and `\` escaped.
//  - "field" / "operator" / "boolean": raw token plus a trailing space.
export function renderInsertion(kind: CaretContext["kind"], fieldType: FieldType | undefined, text: string): string {
  if (kind === "value") {
    if (fieldType === "number") return text;
    return `"${text.replace(/(["\\])/g, "\\$1")}"`;
  }
  return `${text} `;
}

// pickInsertion computes the formatted insertion text and the resulting
// caret position for a chosen candidate. Caret lands at the end of the
// inserted text, before any trailing space renderInsertion added — shared
// by both the mouse path (FilterSuggest's onMouseDown below) and the
// keyboard path (FilterBar's Enter/Tab handling) so there's one definition
// of "what happens when you pick suggestion N".
export function pickInsertion(
  ctx: CaretContext,
  byName: Map<string, FieldMeta>,
  item: SuggestItem
): { text: string; caret: number } {
  const fieldType = ctx.kind === "value" && ctx.fieldName ? byName.get(ctx.fieldName)?.type : undefined;
  const text = renderInsertion(ctx.kind, fieldType, item.insertText);
  const trailingSpace = text.endsWith(" ") ? 1 : 0;
  const caret = ctx.tokenStart + text.length - trailingSpace;
  return { text, caret };
}

// --- presentational dropdown --------------------------------------------

interface Props {
  ctx: CaretContext;
  byName: Map<string, FieldMeta>;
  items: SuggestItem[];
  hint?: string;
  highlightIndex: number;
  onPick: (insertText: string, newCaret: number) => void;
}

export function FilterSuggest({ ctx, byName, items, hint, highlightIndex, onPick }: Props) {
  if (items.length === 0 && !hint) return null;

  const accept = (item: SuggestItem) => {
    const { text, caret } = pickInsertion(ctx, byName, item);
    onPick(text, caret);
  };

  return (
    <div className="suggest">
      {items.length === 0 && hint ? (
        <div className="suggest-item suggest-hint">{hint}</div>
      ) : (
        items.map((item, i) => (
          <div
            key={`${item.insertText}-${i}`}
            className={`suggest-item${i === highlightIndex ? " active" : ""}`}
            onMouseDown={(e) => {
              // mousedown (not click) fires before the input's blur handler,
              // so we can accept the suggestion before the dropdown closes.
              e.preventDefault();
              accept(item);
            }}
          >
            <span className="suggest-label">{item.label}</span>
            {item.badge && <span className="suggest-type">{item.badge}</span>}
            {item.count !== undefined && <span className="suggest-count">{item.count}</span>}
          </div>
        ))
      )}
    </div>
  );
}
