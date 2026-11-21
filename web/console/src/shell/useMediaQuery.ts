import { useCallback, useSyncExternalStore } from "react";

// jsdom and old engines have no matchMedia: the query is then false.
function list(query: string): MediaQueryList | null {
  const match = (window as Partial<Window>).matchMedia;
  return match ? match.call(window, query) : null;
}

export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (notify: () => void) => {
      const media = list(query);
      media?.addEventListener("change", notify);
      return () => {
        media?.removeEventListener("change", notify);
      };
    },
    [query],
  );
  return useSyncExternalStore(
    subscribe,
    () => list(query)?.matches ?? false,
    () => false,
  );
}
