import { useQuery } from "@tanstack/react-query";

import type { SettingsList } from "../../api/types";
import { useApi, useIsAdmin } from "../../session/SessionProvider";

export const SETTINGS_KEY = ["settings"] as const;

/** GET /v1/admin/settings. Runs only for an instance admin session. */
export function useSettingsQuery() {
  const api = useApi();
  const admin = useIsAdmin();
  return useQuery({
    queryKey: SETTINGS_KEY,
    queryFn: () => api.get<SettingsList>("/v1/admin/settings"),
    enabled: admin,
  });
}
