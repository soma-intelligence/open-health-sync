import type { APIKey } from "../../api/types";
import { AUDIT_ACTIONS } from "./actions";
import type { AuditFilter } from "./api";

export interface AuditFiltersProps {
  filter: AuditFilter;
  keys: readonly APIKey[];
  onChange: (filter: AuditFilter) => void;
}

/** Two selects: one action, one API key. An empty value means any. */
export function AuditFilters({ filter, keys, onChange }: AuditFiltersProps) {
  const known = (AUDIT_ACTIONS as readonly string[]).includes(filter.action) || filter.action === "";
  return (
    <div className="filters">
      <div className="field">
        <label htmlFor="audit-action">Action</label>
        <select
          id="audit-action"
          className="input"
          value={filter.action}
          onChange={(e) => {
            onChange({ ...filter, action: e.target.value });
          }}
        >
          <option value="">Any action</option>
          {known ? null : <option value={filter.action}>{filter.action}</option>}
          {AUDIT_ACTIONS.map((a) => (
            <option key={a} value={a}>
              {a}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="audit-actor">Actor</label>
        <select
          id="audit-actor"
          className="input"
          value={filter.keyId}
          onChange={(e) => {
            onChange({ ...filter, keyId: e.target.value });
          }}
        >
          <option value="">Any actor</option>
          {keys.map((k) => (
            <option key={k.id} value={k.id}>
              {k.prefix}...
            </option>
          ))}
        </select>
      </div>
    </div>
  );
}
