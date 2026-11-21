import { onlineManager } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";

import { Icon } from "../ui/Icon";

const subscribe = (fn: () => void) => onlineManager.subscribe(fn);
const isOnline = () => onlineManager.isOnline();

/**
 * Without a connection the queries wait instead of failing, so a page would
 * show Loading with no end. This says why, and that the console picks up by itself.
 */
export function OfflineNotice() {
  const online = useSyncExternalStore(subscribe, isOnline, () => true);
  if (online) return null;
  return (
    <div className="notice-bar offline-notice" role="status">
      <Icon name="alert" size="sm" />
      <p>You are offline. What is on screen may be out of date, and pages wait to load. The console carries on when the connection is back.</p>
    </div>
  );
}
