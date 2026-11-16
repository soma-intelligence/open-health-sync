import { useEffect, useState } from "react";
import { useNavigate } from "react-router";

import { ApiError } from "../api/client";
import type { AdminTenant, AdminTenantList } from "../api/types";
import { useApi, useSession } from "../session/SessionProvider";
import { useToast } from "../ui/Toast";

/** Instance admin only: acts as another tenant. The list is not cached, so a switch leaves the cache with the session alone. */
export function TenantSwitcher() {
  const api = useApi();
  const { session, switchTenant } = useSession();
  const navigate = useNavigate();
  const toast = useToast();
  const [tenants, setTenants] = useState<AdminTenant[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let live = true;
    api
      .get<AdminTenantList>("/v1/admin/tenants")
      .then((page) => {
        if (live) setTenants(page.data);
      })
      .catch((e: unknown) => {
        if (live) toast(e instanceof ApiError ? e.detail || e.message : "The tenants could not be loaded.", "bad");
      });
    return () => {
      live = false;
    };
  }, [api, toast]);

  if (!session) return null;
  const acting = session.acting_tenant;
  const options = tenants.some((t) => t.id === acting.id) ? tenants : [{ id: acting.id, name: acting.name }, ...tenants];

  const choose = async (id: string) => {
    if (id === acting.id || busy) return;
    setBusy(true);
    try {
      await switchTenant(id);
      void navigate("/overview");
    } catch (e) {
      toast(e instanceof ApiError ? e.detail || e.message : "The tenant could not be switched.", "bad");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="tenant-switch">
      <label htmlFor="acting-tenant">Acting tenant</label>
      <select
        id="acting-tenant"
        value={acting.id}
        disabled={busy}
        onChange={(e) => {
          void choose(e.target.value);
        }}
      >
        {options.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </select>
    </div>
  );
}
