import { useState } from "react";

import type { AdminProvider } from "../../api/types";
import { providerStatusLabel } from "../../lib/classify";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { useToast } from "../../ui/Toast";
import { useUpdateProvider } from "./api";

/** Whether the provider runs on the instance. */
export function ProviderSwitch({ provider }: { provider: AdminProvider }) {
  const canWrite = useCanWrite();
  const toast = useToast();
  const update = useUpdateProvider(provider.slug);
  const [asking, setAsking] = useState<boolean | null>(null);

  const on = provider.status === "active";
  let text: string;
  let target: boolean | null = null;
  if (on) {
    text = `${provider.name} is on. Users can pick it on their connection link, and pulls run on schedule.`;
    target = false;
  } else if (!provider.enabled) {
    text = `${provider.name} is off. Its connections are kept and resume when it is on.`;
    target = true;
  } else if (provider.status === "error") {
    text = "Fix the error, then save the keys again: it turns on as soon as they are accepted.";
  } else {
    text = `Save the required keys first: ${provider.name} turns on as soon as they are accepted.`;
  }

  return (
    <Card title="Instance switch">
      <div className="switch-row">
        <p className="switch-text">{text}</p>
        {canWrite && target !== null ? (
          <Button
            kind={target ? "primary" : "danger"}
            onClick={() => {
              setAsking(target);
            }}
          >
            {target ? "Turn on" : "Turn off"}
          </Button>
        ) : null}
      </div>
      {asking !== null ? (
        <ConfirmDialog
          title={asking ? `Turn ${provider.name} on?` : `Turn ${provider.name} off?`}
          body={
            asking
              ? "New connections, pulls and notifications resume on every process within 30 seconds."
              : "New connections, pulls and notifications stop on every process within 30 seconds. Connections and the data already collected are kept: turning it on again resumes them."
          }
          confirmLabel={asking ? "Turn on" : "Turn off"}
          danger={!asking}
          onConfirm={async () => {
            const next = await update.mutateAsync({ enabled: asking });
            toast(`${provider.name} is ${providerStatusLabel(next.status).toLowerCase()}.`);
          }}
          onClose={() => {
            setAsking(null);
          }}
        />
      ) : null}
    </Card>
  );
}
