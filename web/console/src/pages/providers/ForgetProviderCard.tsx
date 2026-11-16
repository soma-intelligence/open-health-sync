import { useState } from "react";

import type { AdminProvider } from "../../api/types";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { useToast } from "../../ui/Toast";
import { useForgetProvider } from "./api";

/** Deletes the keys and the switch the console saved; shown only when something was saved. */
export function ForgetProviderCard({ provider }: { provider: AdminProvider }) {
  const canWrite = useCanWrite();
  const toast = useToast();
  const forget = useForgetProvider(provider.slug);
  const [asking, setAsking] = useState(false);

  if (!canWrite || provider.updated_at === null) return null;
  return (
    <Card title="Forget saved settings">
      <p className="muted">Delete the keys and the switch saved in this console. Environment variables are not touched.</p>
      <Button
        kind="danger"
        onClick={() => {
          setAsking(true);
        }}
      >
        Forget saved settings
      </Button>
      {asking ? (
        <ConfirmDialog
          title={`Forget the settings of ${provider.name}?`}
          body="The keys saved in this console are deleted. Without environment variables, the provider stops until its keys are saved again. Connections and data are kept."
          confirmLabel="Forget settings"
          danger
          typed={provider.slug}
          onConfirm={async () => {
            await forget.mutateAsync();
            toast(`The saved settings of ${provider.name} are gone.`);
          }}
          onClose={() => {
            setAsking(false);
          }}
        />
      ) : null}
    </Card>
  );
}
