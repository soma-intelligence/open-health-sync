import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import type { ApiError } from "../api/client";
import type { InspectorKind, InspectorRequest } from "./snippets";

export type { InspectorKind, InspectorRequest, SnippetLang } from "./snippets";

export interface InspectorEntry {
  kind: InspectorKind;
  title: string;
  id?: string;
  request: InspectorRequest;
  response: unknown;
  error?: ApiError;
}

type Setter = (entry: InspectorEntry | null) => void;

// Two contexts: a page that publishes an entry holds only the stable setter, so
// the entry it sets never re-renders it.
const EntryContext = createContext<InspectorEntry | null>(null);
const SetterContext = createContext<Setter | null>(null);

export function InspectorProvider({ children }: { children: ReactNode }) {
  const [entry, setEntry] = useState<InspectorEntry | null>(null);
  const setter = useMemo<Setter>(() => setEntry, []);
  return (
    <SetterContext value={setter}>
      <EntryContext value={entry}>{children}</EntryContext>
    </SetterContext>
  );
}

/** What the inspector pane shows now. */
export function useInspectorEntry(): InspectorEntry | null {
  return useContext(EntryContext);
}

/** Shows entry in the inspector while the page is mounted; the pane is emptied on unmount. */
export function useInspect(entry: InspectorEntry | null): void {
  const set = useContext(SetterContext);
  if (!set) throw new Error("InspectorProvider is missing");
  useEffect(() => {
    set(entry);
    return () => {
      set(null);
    };
  }, [set, entry]);
}
