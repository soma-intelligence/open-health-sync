import { useId, useState } from "react";

import type { AdminProvider } from "../../api/types";
import { providerStatusLabel } from "../../lib/classify";
import { keysPatch, type KeyField } from "../../lib/providers";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Pill } from "../../ui/Pill";
import { useToast } from "../../ui/Toast";
import { useUpdateProvider } from "./api";

export const ENV_LOCK_NOTE = "Set by the environment. Unset its OHS_ variables and restart to edit them here.";

interface Edit {
  input: string;
  clear: boolean;
}

// A secret is write only: its field always starts empty.
function initial(p: AdminProvider): Record<string, Edit> {
  const out: Record<string, Edit> = {};
  for (const k of p.keys) out[k.name] = { input: k.secret ? "" : (k.value ?? ""), clear: false };
  return out;
}

export function ProviderKeysForm({ provider }: { provider: AdminProvider }) {
  const canWrite = useCanWrite();
  const toast = useToast();
  const update = useUpdateProvider(provider.slug);
  const formId = useId();
  const [edits, setEdits] = useState(() => initial(provider));
  const [confirming, setConfirming] = useState(false);

  const locked = provider.source === "env";
  const disabled = locked || !canWrite;
  const fields: KeyField[] = provider.keys.map((k) => ({
    name: k.name,
    secret: k.secret,
    set: k.set,
    value: k.value,
    input: edits[k.name]?.input ?? "",
    clear: edits[k.name]?.clear ?? false,
  }));
  const patch = keysPatch(fields);
  const change = (name: string, next: Partial<Edit>) => {
    setEdits((all) => ({ ...all, [name]: { input: "", clear: false, ...all[name], ...next } }));
  };

  return (
    <Card title="Keys">
      {locked ? <p className="notice">{ENV_LOCK_NOTE}</p> : null}
      <form
        id={formId}
        autoComplete="off"
        onSubmit={(e) => {
          e.preventDefault();
          if (patch !== null && !disabled) setConfirming(true);
        }}
      >
        {provider.keys.map((k) => {
          const id = `${formId}-${k.name}`;
          const edit = edits[k.name];
          return (
            <div className="key-row" key={k.name}>
              <div className="key-label">
                <label htmlFor={id} className="mono">
                  {k.name}
                </label>
                <span className="muted small">
                  {k.required ? "Required" : "Optional"}
                  {k.secret ? ", secret" : ""}. Or <span className="mono">{k.env}</span>
                </span>
              </div>
              <div className="key-control">
                <input
                  id={id}
                  className="input mono"
                  type={k.secret ? "password" : "text"}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={disabled || edit?.clear === true}
                  placeholder={k.secret ? (k.set ? "Type a new value to replace it" : "") : ""}
                  value={edit?.input ?? ""}
                  onChange={(e) => {
                    change(k.name, { input: e.target.value });
                  }}
                />
                {k.secret ? <Pill tone={k.set ? "ok" : "off"}>{k.set ? "Set" : "Not set"}</Pill> : null}
                {k.set && !disabled ? (
                  <label className="check small muted">
                    <input
                      type="checkbox"
                      checked={edit?.clear ?? false}
                      onChange={(e) => {
                        change(k.name, { clear: e.target.checked });
                      }}
                    />
                    Remove {k.name}
                  </label>
                ) : null}
              </div>
            </div>
          );
        })}
        {disabled ? null : (
          <div className="form-foot">
            <span className="muted small">Checked with the provider's own rules before anything is stored. Secrets are never shown again.</span>
            <Button type="submit" kind="primary" disabled={patch === null}>
              Save keys
            </Button>
          </div>
        )}
      </form>
      {confirming && patch !== null ? (
        <ConfirmDialog
          title={`Save the keys of ${provider.name}?`}
          body={
            <>
              <p>These keys change: {Object.keys(patch).join(", ")}. They are checked with the provider's own rules, then sealed and applied by every process within 30 seconds.</p>
            </>
          }
          confirmLabel="Save keys"
          onConfirm={async () => {
            const next = await update.mutateAsync({ keys: patch });
            setEdits(initial(next));
            toast(`Keys saved. ${provider.name} is ${providerStatusLabel(next.status).toLowerCase()}.`);
          }}
          onClose={() => {
            setConfirming(false);
          }}
        />
      ) : null}
    </Card>
  );
}
