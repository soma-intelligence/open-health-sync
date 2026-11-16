import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useMemo, useReducer, useRef, type ReactNode } from "react";

import { ApiError, SESSION_PATH, browserFetch, createClient, type ApiClient } from "../api/client";
import type { ConsoleSession } from "../api/types";

export const SESSION_KEY = ["session"] as const;

export type SessionStatus = "loading" | "signed-in" | "signed-out";

export interface SessionValue {
  session: ConsoleSession | null;
  status: SessionStatus;
  signIn: (key: string) => Promise<void>;
  signOut: () => Promise<void>;
  switchTenant: (tenantId: string) => Promise<void>;
  /** Calls fn after each explicit sign out (not on a 401); returns the unsubscribe. */
  onSignOut: (fn: () => void) => () => void;
}

interface Context extends SessionValue {
  api: ApiClient;
}

const SessionContext = createContext<Context | null>(null);

function useContextValue(): Context {
  const value = useContext(SessionContext);
  if (!value) throw new Error("SessionProvider is missing");
  return value;
}

const isSessionKey = (key: readonly unknown[]): boolean => key[0] === SESSION_KEY[0];

function current(queryClient: QueryClient): ConsoleSession | null {
  return queryClient.getQueryData<ConsoleSession | null>(SESSION_KEY) ?? null;
}

export function SessionProvider({ children, fetchImpl = browserFetch }: { children: ReactNode; fetchImpl?: typeof fetch }) {
  const queryClient = useQueryClient();
  // clear() drops the session query the observer holds; a render makes it pick up the new one.
  const [, rebind] = useReducer((n: number) => n + 1, 0);
  const signOutListeners = useRef(new Set<() => void>());
  const onSignOut = useCallback((fn: () => void) => {
    const listeners = signOutListeners.current;
    listeners.add(fn);
    return () => {
      listeners.delete(fn);
    };
  }, []);

  const api = useMemo<ApiClient>(() => {
    // Declared first so the client can fetch the session to refresh the token.
    const client: ApiClient = createClient({
      fetch: fetchImpl,
      csrf: () => current(queryClient)?.csrf_token ?? null,
      refreshCsrf: async () => {
        const fresh = await queryClient.fetchQuery({ queryKey: SESSION_KEY, queryFn: () => loadSession(client), staleTime: 0 });
        return fresh?.csrf_token ?? null;
      },
      readOnly: () => current(queryClient)?.read_only ?? false,
      onUnauthorized: () => {
        // The next sign in may be another tenant: none of this one's data stays.
        queryClient.removeQueries({ predicate: (q) => !isSessionKey(q.queryKey) });
        queryClient.setQueryData(SESSION_KEY, null);
      },
    });
    return client;
  }, [fetchImpl, queryClient]);

  // A 401 here means signed out, not an error.
  const query = useQuery({
    queryKey: SESSION_KEY,
    queryFn: () => loadSession(api),
    staleTime: Infinity,
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const session = query.data ?? null;
  const status: SessionStatus = query.isPending ? "loading" : session ? "signed-in" : "signed-out";

  const value = useMemo<Context>(
    () => ({
      api,
      session,
      status,
      signIn: async (key) => {
        const next = await api.send<ConsoleSession>("POST", SESSION_PATH, { key });
        queryClient.setQueryData(SESSION_KEY, next);
      },
      signOut: async () => {
        await api.send("DELETE", SESSION_PATH);
        queryClient.clear();
        queryClient.setQueryData(SESSION_KEY, null);
        for (const fn of signOutListeners.current) fn();
        rebind();
      },
      onSignOut,
      switchTenant: async (tenantId) => {
        const next = await api.send<ConsoleSession>("PUT", `${SESSION_PATH}/tenant`, { tenant_id: tenantId });
        // The answer carries no token: the session keeps the one it has.
        const csrf = current(queryClient)?.csrf_token;
        queryClient.setQueryData(SESSION_KEY, csrf ? { ...next, csrf_token: csrf } : next);
        queryClient.removeQueries({ predicate: (q) => !isSessionKey(q.queryKey) });
      },
    }),
    [api, onSignOut, queryClient, rebind, session, status],
  );

  return <SessionContext value={value}>{children}</SessionContext>;
}

async function loadSession(api: ApiClient): Promise<ConsoleSession | null> {
  try {
    return await api.get<ConsoleSession>(SESSION_PATH);
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return null;
    throw e;
  }
}

export function useSession(): SessionValue {
  const { api: _api, ...rest } = useContextValue();
  void _api;
  return rest;
}

export function useApi(): ApiClient {
  return useContextValue().api;
}

/** Signed in and not read only. */
export function useCanWrite(): boolean {
  const { session } = useContextValue();
  return session !== null && !session.read_only;
}

/** The session tenant is the instance admin. */
export function useIsAdmin(): boolean {
  return useContextValue().session?.tenant.admin ?? false;
}
