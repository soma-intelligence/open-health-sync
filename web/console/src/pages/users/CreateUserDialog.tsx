import { useId, useState } from "react";
import { useNavigate } from "react-router";

import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { useCreateUser } from "./api";

/** The confirmation for POST /v1/users. A blank external id makes a user with none. */
export function CreateUserDialog({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const create = useCreateUser();
  const inputId = useId();
  const [externalRef, setExternalRef] = useState("");

  return (
    <ConfirmDialog
      title="New user"
      confirmLabel="Create user"
      onClose={onClose}
      dirty={externalRef.trim() !== ""}
      onConfirm={async () => {
        const user = await create.mutateAsync(externalRef.trim());
        void navigate(`/users/${user.id}`);
      }}
      body={
        <div className="field">
          <label htmlFor={inputId}>External id</label>
          <input
            id={inputId}
            className="input mono"
            autoComplete="off"
            spellCheck={false}
            value={externalRef}
            onChange={(e) => {
              setExternalRef(e.target.value);
            }}
          />
          <p className="muted">Your own id for this person. Leave it empty to create a user without one.</p>
        </div>
      }
    />
  );
}
