import { useState } from "react";

import type { AdminProvider } from "../../api/types";
import { useCanWrite, useSession } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { useToast } from "../../ui/Toast";
import { useTenantProvidersQuery, useUpdateTenantProvider } from "./api";

/** The switch of the acting tenant alone: the instance and the other tenants are untouched. */
export function TenantProviderSwitch({ provider }: { provider: AdminProvider }) {
  const { session } = useSession();
  const tenant = session?.acting_tenant;
  const tenantId = tenant?.id ?? "";
  const canWrite = useCanWrite();
  const toast = useToast();
  const list = useTenantProvidersQuery(tenantId, tenantId !== "");
  const update = useUpdateTenantProvider(tenantId, provider.slug);
  const [asking, setAsking] = useState<boolean | null>(null);

  const row = list.data?.data.find((t) => t.slug === provider.slug);
  const name = tenant?.name ?? "this tenant";

  let text = "Loading";
  if (list.isError) text = "";
  else if (row) {
    text = row.enabled
      ? `${provider.name} is on for ${name}.`
      : `${provider.name} is off for ${name}. Its connections and data are kept.`;
  }

  return (
    <Card title="This tenant">
      {list.isError ? <ErrorPanel error={list.error} /> : null}
      <div className="switch-row">
        <p className="switch-text">{text}</p>
        {canWrite && row ? (
          <Button
            kind={row.enabled ? "danger" : "primary"}
            onClick={() => {
              setAsking(!row.enabled);
            }}
          >
            {row.enabled ? "Turn off for this tenant" : "Turn on for this tenant"}
          </Button>
        ) : null}
      </div>
      {asking !== null ? (
        <ConfirmDialog
          title={asking ? `Turn ${provider.name} on for ${name}?` : `Turn ${provider.name} off for ${name}?`}
          body={
            asking
              ? "The tenant's users can connect it again and pulls resume."
              : "Starting a connection, uploads and pulls stop for this tenant only. Connections and data are kept."
          }
          confirmLabel={asking ? "Turn on" : "Turn off"}
          danger={!asking}
          onConfirm={async () => {
            await update.mutateAsync({ enabled: asking });
            toast(`${provider.name} is ${asking ? "on" : "off"} for ${name}.`);
          }}
          onClose={() => {
            setAsking(null);
          }}
        />
      ) : null}
    </Card>
  );
}
