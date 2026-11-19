import { useId, useState } from "react";

import type { CreateKeyRequest, KeyScope } from "../../api/types";
import { useIsAdmin, useSession } from "../../session/SessionProvider";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { useCreateKey } from "./api";
import { ScopePicker } from "./ScopePicker";
import { fullAccessScopes, knownScopes, readOnlyScopes } from "./scopes";

/** The date field's value as the instant the key stops working: the start of that day, UTC. */
function expiryOf(day: string): string {
  return `${day}T00:00:00Z`;
}

/** How the scopes of the new key are chosen. */
export type Access = "read" | "full" | "custom";

/**
 * The scopes the dialog sends for a choice. Never empty: the server reads an
 * empty list as "what the calling key holds", which is too easy to give by
 * accident, so the console always names the scopes.
 */
export function scopesFor(access: Access, chosen: readonly KeyScope[], sessionScopes: readonly KeyScope[], isAdmin: boolean): KeyScope[] {
  if (access === "read") return readOnlyScopes(sessionScopes, isAdmin);
  if (access === "full") return fullAccessScopes(sessionScopes);
  return [...chosen];
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
  const accessName = useId();
  const sessionScopes = knownScopes(session?.key_scopes ?? []);
  const readScopes = readOnlyScopes(sessionScopes, isAdmin);
  const [access, setAccess] = useState<Access>(readScopes.length > 0 ? "read" : "custom");
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
          <fieldset className="scope-picker">
            <legend>Access</legend>
            {readScopes.length > 0 ? (
              <label className="check">
                <input
                  type="radio"
                  name={accessName}
                  checked={access === "read"}
                  onChange={() => {
                    setAccess("read");
                  }}
                />
                <span>
                  Read only <span className="muted">(every :read scope this session holds)</span>
                </span>
              </label>
            ) : null}
            <label className="check">
              <input
                type="radio"
                name={accessName}
                checked={access === "full"}
                onChange={() => {
                  setAccess("full");
                }}
              />
              <span>
                Full access <span className="muted">(everything this session&apos;s key can do)</span>
              </span>
            </label>
            <label className="check">
              <input
                type="radio"
                name={accessName}
                checked={access === "custom"}
                onChange={() => {
                  setAccess("custom");
                }}
              />
              <span>Pick scopes</span>
            </label>
          </fieldset>
          {access === "custom" ? <ScopePicker chosen={chosen} onChange={setChosen} sessionScopes={sessionScopes} isAdmin={isAdmin} /> : null}
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
        const scopes = scopesFor(access, chosen, sessionScopes, isAdmin);
        if (scopes.length === 0) throw new Error("Pick at least one scope.");
        const body: CreateKeyRequest = { scopes };
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
