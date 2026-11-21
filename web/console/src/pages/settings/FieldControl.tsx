import { useState } from "react";

import type { Setting } from "../../api/types";
import { Button } from "../../ui/Button";

export interface FieldControlProps {
  setting: Setting;
  /** The id the field's label points at. */
  id: string;
  /** What the control shows: the draft value when there is one, else the effective value. */
  value: string;
  /** False for a locked field or a read only session. */
  enabled: boolean;
  /** A secret field: whether the draft holds a new value. */
  replacing: boolean;
  onChange: (v: string) => void;
  /** A secret field: the new value is given up. */
  onCancel: () => void;
}

function isTrue(v: string): boolean {
  return ["1", "t", "true"].includes(v.trim().toLowerCase());
}

function splitRate(v: string): [string, string] {
  const at = v.indexOf("/");
  return at < 0 ? [v, ""] : [v.slice(0, at), v.slice(at + 1)];
}

/** "rate/burst", or the rate alone ("0" is no limit), or "" when both are empty. */
function joinRate(rate: string, burst: string): string {
  if (burst === "") return rate;
  return `${rate}/${burst}`;
}

function SecretControl({ setting, id, value, enabled, replacing, onChange, onCancel }: FieldControlProps) {
  // Local until the first character is typed, so an open, empty input is not a change.
  const [open, setOpen] = useState(false);
  const shown = open || replacing;
  return (
    <div className="set-secret">
      <span className="set-secret-state">{setting.value === null ? "not set" : "set"}</span>
      {shown ? (
        <>
          <input
            id={id}
            className="input mono"
            type="password"
            autoComplete="new-password"
            spellCheck={false}
            value={value}
            disabled={!enabled}
            onChange={(e) => {
              onChange(e.target.value);
            }}
          />
          <Button
            size="sm"
            kind="ghost"
            onClick={() => {
              setOpen(false);
              onCancel();
            }}
          >
            Cancel
          </Button>
        </>
      ) : enabled ? (
        <Button
          size="sm"
          onClick={() => {
            setOpen(true);
          }}
        >
          Replace
        </Button>
      ) : null}
    </div>
  );
}

/** The input of one setting, by type. A secret is write only and never prefilled. */
export function FieldControl(props: FieldControlProps) {
  const { setting: s, id, value, enabled, onChange } = props;
  if (s.secret) return <SecretControl {...props} />;
  switch (s.type) {
    case "bool":
      return (
        <input
          id={id}
          className="set-toggle"
          type="checkbox"
          role="switch"
          checked={isTrue(value)}
          disabled={!enabled}
          onChange={(e) => {
            onChange(e.target.checked ? "true" : "false");
          }}
        />
      );
    case "int":
      return (
        <input
          id={id}
          className="input mono set-num"
          type="number"
          inputMode="numeric"
          step={1}
          {...(s.min !== null ? { min: s.min } : {})}
          {...(s.max !== null ? { max: s.max } : {})}
          value={value}
          disabled={!enabled}
          onChange={(e) => {
            onChange(e.target.value);
          }}
        />
      );
    case "enum": {
      const values = s.values ?? [];
      const options = value !== "" && !values.includes(value) ? [value, ...values] : values;
      return (
        <select
          id={id}
          className="input mono"
          value={value}
          disabled={!enabled}
          onChange={(e) => {
            onChange(e.target.value);
          }}
        >
          {value === "" ? <option value="">Choose</option> : null}
          {options.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      );
    }
    case "rate": {
      const [rate, burst] = splitRate(value);
      return (
        <div className="set-rate">
          <input
            id={id}
            className="input mono set-num"
            type="number"
            min={0}
            step="any"
            aria-label={`${s.label} req/s`}
            value={rate}
            disabled={!enabled}
            onChange={(e) => {
              onChange(joinRate(e.target.value, burst));
            }}
          />
          <span className="muted small">req/s</span>
          <input
            className="input mono set-num"
            type="number"
            min={0}
            step={1}
            aria-label={`${s.label} burst`}
            value={burst}
            disabled={!enabled}
            onChange={(e) => {
              onChange(joinRate(rate, e.target.value));
            }}
          />
          <span className="muted small">burst</span>
        </div>
      );
    }
    default:
      return (
        <input
          id={id}
          className="input mono"
          type="text"
          autoComplete="off"
          spellCheck={false}
          {...(s.type === "list" ? { placeholder: "comma separated" } : {})}
          value={value}
          disabled={!enabled}
          onChange={(e) => {
            onChange(e.target.value);
          }}
        />
      );
  }
}
