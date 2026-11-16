import { useState } from "react";
import { useNavigate } from "react-router";

import type { ConnectSession, User } from "../../api/types";
import { classifyUser } from "../../lib/classify";
import { absTime, humanize, initials, relTime } from "../../lib/format";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";
import { useToast } from "../../ui/Toast";
import { useCreateConnectSession, useDeleteUser } from "./api";
import { ConnectLinkDialog } from "./ConnectLinkDialog";

/** The name an operator knows the user by: their external id, else the id. */
export function userName(user: User): string {
  return user.external_ref ?? user.id;
}

export function UserHeader({ user }: { user: User }) {
  const canWrite = useCanWrite();
  const navigate = useNavigate();
  const toast = useToast();
  const remove = useDeleteUser(user.id);
  const createLink = useCreateConnectSession(user.id);
  const [link, setLink] = useState<ConnectSession | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [linking, setLinking] = useState(false);

  return (
    <header className="page-head user-head">
      <div className="user-id">
        <span className="user-avatar" aria-hidden="true">
          {initials(user.external_ref ?? "?")}
        </span>
        <div>
          <div className="user-title">
            <h1 className="page-title">{user.external_ref ?? "No external id"}</h1>
            <Pill tone={classifyUser(user)}>{humanize(user.status)}</Pill>
          </div>
          <p className="page-sub">
            <Mono>{user.id}</Mono> <span className="muted" title={absTime(user.created_at)}>created {relTime(user.created_at)}</span>
          </p>
        </div>
      </div>
      {canWrite ? (
        <div className="page-actions">
          <Button
            kind="primary"
            onClick={() => {
              setLinking(true);
            }}
          >
            Connection link
          </Button>
          <Button
            kind="danger"
            onClick={() => {
              setDeleting(true);
            }}
          >
            Delete user
          </Button>
        </div>
      ) : null}
      {linking ? (
        <ConfirmDialog
          title="Create a connection link?"
          body="The link lets the user connect a source to this account. It works once."
          confirmLabel="Create link"
          onConfirm={async () => {
            const made = await createLink.mutateAsync();
            // Keep the one-time link out of the mutation state: only this component holds it.
            createLink.reset();
            setLink(made);
          }}
          onClose={() => {
            setLinking(false);
          }}
        />
      ) : null}
      {link ? (
        <ConnectLinkDialog
          session={link}
          onClose={() => {
            setLink(null);
          }}
        />
      ) : null}
      {deleting ? (
        <ConfirmDialog
          title="Delete this user and all their data?"
          body="Every record, raw file, connection and token of this user is erased. Providers are told to revoke access. This cannot be undone."
          confirmLabel="Delete user"
          danger
          typed={userName(user)}
          onConfirm={async () => {
            await remove.mutateAsync();
            toast("Deletion started. The user disappears once every record is erased.");
            void navigate("/users");
          }}
          onClose={() => {
            setDeleting(false);
          }}
        />
      ) : null}
    </header>
  );
}
