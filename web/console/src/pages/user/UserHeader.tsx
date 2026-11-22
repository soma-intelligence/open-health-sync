import { useState } from "react";

import type { ConnectSession, User } from "../../api/types";
import { classifyUser } from "../../lib/classify";
import { isDemoUser } from "../../lib/demo";
import { absTime, humanize, relTime } from "../../lib/format";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Mono } from "../../ui/Mono";
import { Pill } from "../../ui/Pill";
import { useCreateConnectSession } from "./api";
import { ConnectLinkDialog } from "./ConnectLinkDialog";

/** The name an operator knows the user by: their external id, else the id. */
export function userName(user: User): string {
  return user.external_ref ?? user.id;
}

export function UserHeader({ user }: { user: User }) {
  const canWrite = useCanWrite();
  const createLink = useCreateConnectSession(user.id);
  const [link, setLink] = useState<ConnectSession | null>(null);
  const [linking, setLinking] = useState(false);
  const name = user.external_ref ?? "No external id";

  return (
    <header className="page-head user-head">
      <div className="user-id">
        <span className="user-avatar" aria-hidden="true">
          {(user.external_ref ?? "?").slice(0, 1).toUpperCase()}
        </span>
        <div className="user-names">
          <div className="user-title">
            <h1 className="page-title">{name}</h1>
            <Pill tone={classifyUser(user)}>{humanize(user.status)}</Pill>
            {isDemoUser(user) ? <Pill tone="info">Demo</Pill> : null}
          </div>
          <p className="page-sub user-sub">
            <Mono>{user.id}</Mono>
            <span title={absTime(user.created_at)}>{`since ${relTime(user.created_at)}`}</span>
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
    </header>
  );
}
