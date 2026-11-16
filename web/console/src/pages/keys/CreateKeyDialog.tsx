import { useId, useState } from "react";

import type { CreateKeyRequest, KeyScope } from "../../api/types";
import { useIsAdmin, useSession } from "../../session/SessionProvider";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { useCreateKey } from "./api";
import { ScopePicker } from "./ScopePicker";
import { knownScopes } from "./scopes";

/** The date field's value as the instant the key stops working: the start of that day, UTC. */
function expiryOf(day: string): string {
  return `${day}T00:00:00Z`;
}

export interface CreateKeyDialogProps {
  onCreated: (key: string) => void;
  onClose: () => void;
}

/** The confirmation for POST /v1/keys. The key goes to the parent in state and not to the mutation cache. */
export function CreateKeyDialog({ onCreated, onClose }: CreateKeyDialogProps) {
  const { session } = useSession();
  const isAdmin = useIsAdmin();
  const create = useCreateKey();
  const expiresId = useId();
  const [chosen, setChosen] = useState<KeyScope[]>([]);
  const [day, setDay] = useState("");

  return (
    <ConfirmDialog
      title="Create an API key"
      confirmLabel="Create key"
      onClose={() => {
        create.reset();
        onClose();
      }}
      body={
        <div className="key-form">
          <p>A new key of this tenant. It can do at most what this session&apos;s key does. Treat it as a password.</p>
          <ScopePicker chosen={chosen} onChange={setChosen} sessionScopes={knownScopes(session?.key_scopes ?? [])} isAdmin={isAdmin} />
          <div className="field">
            <label htmlFor={expiresId}>Expires on (optional)</label>
            <input
              id={expiresId}
              type="date"
              className="input mono"
              value={day}
              onChange={(e) => {
                setDay(e.target.value);
              }}
            />
            <p className="muted">Empty: the key lives until it is revoked.</p>
          </div>
        </div>
      }
      onConfirm={async () => {
        const body: CreateKeyRequest = {};
        if (chosen.length > 0) body.scopes = [...chosen];
        if (day !== "") {
          const at = expiryOf(day);
          if (Number.isNaN(Date.parse(at)) || Date.parse(at) <= Date.now()) throw new Error("Pick an expiry date in the future.");
          body.expires_at = at;
        }
        try {
          const made = await create.mutateAsync(body);
          const value = made.key;
          onCreated(value);
        } finally {
          // Keep the key out of the mutation state: only the parent holds it.
          create.reset();
        }
      }}
    />
  );
}
