import { useId } from "react";

import type { Setting } from "../../api/types";
import { Button } from "../../ui/Button";
import { FieldControl } from "./FieldControl";
import { FieldMeta } from "./FieldMeta";
import { fieldState, type Pending } from "./model";

export interface SettingFieldProps {
  setting: Setting;
  pending: Pending;
  canWrite: boolean;
  /** A message of the last refused PATCH for this setting. */
  serverError: string | null;
  focused: boolean;
  /** A value to keep in the draft, null to revert to the default, undefined to leave the draft. */
  onChange: (name: string, v: string | null | undefined) => void;
}

export function controlId(name: string): string {
  return `set-${name}`;
}

export function fieldId(name: string): string {
  return `setting-${name}`;
}

/** One setting: what it is, its control, and why it cannot be edited when it cannot. */
export function SettingField({ setting: s, pending, canWrite, serverError, focused, onChange }: SettingFieldProps) {
  const labelId = useId();
  const st = fieldState(s, pending);
  const enabled = st.editable && canWrite;
  const draft = Object.hasOwn(pending, s.name) ? pending[s.name] : undefined;
  const reverted = draft === null;
  const shown = draft ?? (reverted ? s.default : s.secret ? "" : (s.value ?? ""));

  const className = ["set-field", focused ? "focus" : "", st.changed ? "changed" : ""].filter(Boolean).join(" ");
  return (
    <div id={fieldId(s.name)} className={className} role="group" aria-labelledby={labelId}>
      <div className="set-l">
        <label id={labelId} className="set-label" htmlFor={controlId(s.name)}>
          {s.label}
        </label>
        <FieldMeta setting={s} changed={st.changed} />
      </div>
      <div className="set-c">
        <FieldControl
          setting={s}
          id={controlId(s.name)}
          value={shown}
          enabled={enabled}
          replacing={s.secret && typeof draft === "string"}
          onChange={(v) => {
            onChange(s.name, v);
          }}
          onCancel={() => {
            onChange(s.name, undefined);
          }}
        />
        {st.locked === "env" ? (
          <p className="set-lock">{`Set by ${s.name} in the environment. Unset it and restart to edit it here.`}</p>
        ) : null}
        {st.locked === "bootstrap" ? (
          <p className="set-lock">{`Bootstrap setting. Set ${s.name} in the environment.`}</p>
        ) : null}
        {reverted ? <p className="muted small">Goes back to the default on Apply.</p> : null}
        {st.inputError ? <p className="set-error">{st.inputError}</p> : null}
        {serverError ? <p className="set-error">{serverError}</p> : null}
        {st.invalidStored ? (
          <div className="set-invalid">
            <span className="small">The saved value is skipped:</span> <span>{st.invalidStored}</span>
          </div>
        ) : null}
        {enabled ? (
          <div className="set-actions">
            {s.source === "console" && !reverted ? (
              <Button
                size="sm"
                kind="ghost"
                onClick={() => {
                  onChange(s.name, null);
                }}
              >
                Revert to default
              </Button>
            ) : null}
            {st.changed ? (
              <Button
                size="sm"
                kind="ghost"
                onClick={() => {
                  onChange(s.name, undefined);
                }}
              >
                Undo change
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  );
}
