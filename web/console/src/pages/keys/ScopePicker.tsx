import { useId } from "react";

import type { KeyScope } from "../../api/types";
import { KEY_SCOPES, grantable, groupScopes, impliedScopes, scopeLabel } from "./scopes";

export interface ScopePickerProps {
  chosen: readonly KeyScope[];
  onChange: (chosen: KeyScope[]) => void;
  /** The scopes of the session's key: others cannot be given. */
  sessionScopes: readonly KeyScope[];
  /** admin:* counts only on a key of an instance admin tenant. */
  isAdmin: boolean;
}

/** One checkbox per scope, grouped by resource. A :write shows its :read as granted, and what the session lacks is disabled. */
export function ScopePicker({ chosen, onChange, sessionScopes, isAdmin }: ScopePickerProps) {
  const id = useId();
  const granted = impliedScopes(chosen);
  const allowed = (scope: KeyScope) => grantable(sessionScopes, isAdmin, scope);
  const toggle = (scope: KeyScope, on: boolean) => {
    onChange(KEY_SCOPES.filter((s) => (s === scope ? on : chosen.includes(s))));
  };
  return (
    <fieldset className="scope-picker">
      <legend>
        Scopes <span className="muted">(check at least one)</span>
      </legend>
      {groupScopes(KEY_SCOPES).map((group) => (
        <div key={group.resource} className="scope-group">
          {group.resource === "*" ? null : <span className="scope-resource mono">{group.resource}</span>}
          <div className="scope-items">
            {group.scopes.map((scope) => {
              const implied = granted.has(scope) && !chosen.includes(scope);
              return (
                <label key={scope} className="check" htmlFor={`${id}-${scope}`}>
                  <input
                    id={`${id}-${scope}`}
                    type="checkbox"
                    checked={granted.has(scope)}
                    disabled={!allowed(scope) || implied}
                    onChange={(e) => {
                      toggle(scope, e.target.checked);
                    }}
                  />
                  <span className="mono">{scopeLabel(scope)}</span>
                </label>
              );
            })}
          </div>
        </div>
      ))}
    </fieldset>
  );
}
