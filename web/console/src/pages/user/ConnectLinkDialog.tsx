import { useEffect, useRef, useState } from "react";

import type { ConnectSession } from "../../api/types";
import { ApiError } from "../../api/client";
import { absTime, relTime } from "../../lib/format";
import { Button } from "../../ui/Button";
import { CopyButton } from "../../ui/CopyButton";
import { Dialog } from "../../ui/Dialog";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { useCreateConnectSession } from "./api";

/**
 * Creates a connect session when it opens and shows its link once. The link is
 * kept in this component's state only: it is gone when the dialog closes.
 */
export function ConnectLinkDialog({ userId, onClose }: { userId: string; onClose: () => void }) {
  const create = useCreateConnectSession(userId);
  const [session, setSession] = useState<ConnectSession | null>(null);
  const [error, setError] = useState<unknown>(null);
  const started = useRef(false);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    create.mutateAsync().then(
      (s) => {
        setSession(s);
      },
      (e: unknown) => {
        setError(e instanceof ApiError || e instanceof Error ? e : new Error("Something went wrong."));
      },
    );
  }, [create]);

  const close = () => {
    setSession(null);
    create.reset();
    onClose();
  };

  return (
    <Dialog
      title="Connection link"
      open
      onClose={close}
      footer={
        <Button kind="primary" onClick={close}>
          Close
        </Button>
      }
    >
      {error !== null ? <ErrorPanel error={error} /> : null}
      {error === null && session === null ? <p role="status">Creating the link</p> : null}
      {session ? (
        <div className="field">
          <p>Send this link to the user. They pick their provider and consent with their own account. It is shown once.</p>
          <label htmlFor="connect-link">Link</label>
          <div className="secret-row">
            <input id="connect-link" className="input mono" readOnly value={session.url} onFocus={(e) => { e.currentTarget.select(); }} />
            <CopyButton text={session.url} />
          </div>
          <p className="muted">
            It expires {relTime(session.expires_at)} ({absTime(session.expires_at)}). Anyone holding it can connect accounts to this user until then.
          </p>
        </div>
      ) : null}
    </Dialog>
  );
}
