import { useMemo } from "react";

import { ApiError } from "../../api/client";
import { withQuery, type Query } from "../../api/paths";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";

/** Shows a tab's own GET in the inspector: its path and query, and what it answered. */
export function useTabInspect(title: string, userId: string, path: string, query: Query | undefined, data: unknown, error: unknown): void {
  const queryKey = JSON.stringify(query ?? {});
  const entry = useMemo<InspectorEntry | null>(() => {
    const asked: Record<string, string> = {};
    for (const [k, v] of Object.entries(JSON.parse(queryKey) as Query)) if (v !== null && v !== undefined && v !== "") asked[k] = String(v);
    const request = { method: "GET" as const, path: withQuery(path), ...(Object.keys(asked).length > 0 ? { query: asked } : {}) };
    const base = { kind: "user" as const, title, id: userId, request };
    if (data !== undefined) return { ...base, response: data };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [title, userId, path, queryKey, data, error]);
  useInspect(entry);
}
