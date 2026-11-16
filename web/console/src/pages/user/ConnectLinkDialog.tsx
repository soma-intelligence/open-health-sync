import type { ConnectSession } from "../../api/types";
import { absTime, relTime } from "../../lib/format";
import { Button } from "../../ui/Button";
import { CopyButton } from "../../ui/CopyButton";
import { Dialog } from "../../ui/Dialog";

/** Shows a created link once. The caller holds it in state and drops it when this closes. */
export function ConnectLinkDialog({ session, onClose }: { session: ConnectSession; onClose: () => void }) {
  return (
    <Dialog
      title="Connection link"
      open
      onClose={onClose}
      footer={
        <Button kind="primary" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="field">
        <p>Send this link to the user. They pick their provider and consent with their own account. It is shown once.</p>
        <label htmlFor="connect-link">Link</label>
        <div className="secret-row">
          <input
            id="connect-link"
            className="input mono"
            readOnly
            value={session.url}
            onFocus={(e) => {
              e.currentTarget.select();
            }}
          />
          <CopyButton text={session.url} />
        </div>
        <p className="muted">
          It expires {relTime(session.expires_at)} ({absTime(session.expires_at)}). Anyone holding it can connect accounts to this user until then.
        </p>
      </div>
    </Dialog>
  );
}
