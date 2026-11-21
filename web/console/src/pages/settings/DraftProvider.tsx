import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { useSession } from "../../session/SessionProvider";
import type { Pending } from "./model";

export interface Draft {
  pending: Pending;
  /** Puts a value in the draft; null reverts to the default. */
  set: (name: string, v: string | null) => void;
  /** Takes names out of the draft. */
  drop: (names: readonly string[]) => void;
  discard: () => void;
  /** Counts discards, so a field can forget what it holds locally. */
  epoch: number;
}

interface State {
  owner: string | null;
  pending: Pending;
  epoch: number;
}

const EMPTY: Pending = {};
const DraftContext = createContext<Draft | null>(null);

/**
 * The unsaved settings. Sits above the router so a 401 and the sign in that
 * follows leave it in place; never written to storage. An explicit sign out
 * drops it. A draft belongs to the session tenant and key that wrote it:
 * another key signing in sees an empty one.
 */
export function DraftProvider({ children }: { children: ReactNode }) {
  const { session, onSignOut } = useSession();
  const owner = session ? `${session.tenant.id} ${session.key_prefix}` : null;
  const [state, setState] = useState<State>({ owner: null, pending: EMPTY, epoch: 0 });
  const mine = state.owner === null || owner === null || state.owner === owner;
  const pending = mine ? state.pending : EMPTY;

  const set = useCallback(
    (name: string, v: string | null) => {
      setState((prev) => {
        const base = prev.owner === null || prev.owner === owner ? prev.pending : EMPTY;
        return { ...prev, owner, pending: { ...base, [name]: v } };
      });
    },
    [owner],
  );
  const drop = useCallback((names: readonly string[]) => {
    if (names.length === 0) return;
    setState((prev) => ({ ...prev, pending: Object.fromEntries(Object.entries(prev.pending).filter(([n]) => !names.includes(n))) }));
  }, []);
  // A typed secret must not outlive the session that typed it.
  useEffect(
    () =>
      onSignOut(() => {
        setState((prev) => ({ owner: null, pending: EMPTY, epoch: prev.epoch + 1 }));
      }),
    [onSignOut],
  );
  const discard = useCallback(() => {
    setState((prev) => ({ owner: prev.owner, pending: EMPTY, epoch: prev.epoch + 1 }));
  }, []);

  const value = useMemo<Draft>(() => ({ pending, set, drop, discard, epoch: state.epoch }), [pending, set, drop, discard, state.epoch]);
  return <DraftContext value={value}>{children}</DraftContext>;
}

export function useDraft(): Draft {
  const value = useContext(DraftContext);
  if (!value) throw new Error("DraftProvider is missing");
  return value;
}
