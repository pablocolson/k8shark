import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FieldMeta, FieldsResponse, FieldValue } from "./types";

const POLL_MS = 15_000;

export interface FieldsState {
  fields: FieldMeta[];
  byName: Map<string, FieldMeta>;
  lazyValues: (field: string, prefix: string) => Promise<FieldValue[]>;
}

// useFields fetches the IFL field catalog (GET /api/fields) on mount and
// polls every 15s since observed values drift as traffic flows. Fetch
// failures are fail-soft: the last-known `fields` array is kept so the
// autocomplete dropdown doesn't flicker empty on a transient network blip.
export function useFields(): FieldsState {
  const [fields, setFields] = useState<FieldMeta[]>([]);
  const fieldsRef = useRef<FieldMeta[]>(fields);
  fieldsRef.current = fields;

  useEffect(() => {
    let cancelled = false;

    const load = async () => {
      try {
        const res = await fetch("/api/fields");
        if (!res.ok) return;
        const data: FieldsResponse = await res.json();
        if (!cancelled && Array.isArray(data.fields)) {
          setFields(data.fields);
        }
      } catch {
        // fail-soft: keep last-known fields, don't clear on transient errors.
      }
    };

    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  const byName = useMemo(() => {
    const m = new Map<string, FieldMeta>();
    for (const f of fields) m.set(f.name, f);
    return m;
  }, [fields]);

  const lazyValues = useCallback(async (field: string, prefix: string): Promise<FieldValue[]> => {
    try {
      const q = new URLSearchParams({ prefix, limit: "50" });
      const res = await fetch(`/api/fields/${encodeURIComponent(field)}/values?${q.toString()}`);
      if (!res.ok) return [];
      const data: { field: string; type: string; values: FieldValue[] } = await res.json();
      return data.values ?? [];
    } catch {
      return [];
    }
  }, []);

  return { fields, byName, lazyValues };
}
