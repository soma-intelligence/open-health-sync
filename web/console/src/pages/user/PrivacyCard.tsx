import { useState } from "react";
import { Link, useNavigate } from "react-router";

import type { Share, User } from "../../api/types";
import { formatCount, relTime } from "../../lib/format";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Pill } from "../../ui/Pill";
import { useToast } from "../../ui/Toast";
import { useConsentsQuery, useDeleteUser, useSharesQuery } from "./api";
import { DownloadsPanel } from "./DownloadsPanel";

/** What the user agreed to, in a chip: how many sources hold a granted category. */
function ConsentSummary({ userId }: { userId: string }) {
  const consents = useConsentsQuery(userId, true);
  if (consents.isPending) return <span className="muted">Loading</span>;
  if (consents.data === undefined) return <span className="muted">Could not be read</span>;
  const given = consents.data.data.filter((s) => s.categories.some((c) => c.granted)).length;
  if (given === 0) return <Pill tone="off">None recorded</Pill>;
  return <Pill tone="ok">{given === 1 ? "Given for 1 source" : `Given for ${formatCount(given)} sources`}</Pill>;
}

/** The share links the user made: how many work now, and the next one to expire. */
function SharesSummary({ userId }: { userId: string }) {
  const shares = useSharesQuery(userId, undefined, true);
  if (shares.isPending) return <span className="muted">Loading</span>;
  if (shares.data === undefined) return <span className="muted">Could not be read</span>;
  const list: readonly Share[] = shares.data.data;
  const active = list.filter((s) => s.status === "active");
  if (active.length === 0) return <span>None active</span>;
  const next = active.map((s) => s.expires_at).sort()[0];
  return <span>{`${formatCount(active.length)} active${next ? `, the next expires ${relTime(next)}` : ""}`}</span>;
}

/** Consent and privacy: consent, share links, export and erase, in one place. Erasing asks for the user's id. */
export function PrivacyCard({ user }: { user: User }) {
  const canWrite = useCanWrite();
  const navigate = useNavigate();
  const toast = useToast();
  const remove = useDeleteUser(user.id);
  const [erasing, setErasing] = useState(false);

  return (
    <Card title="Consent and privacy" className="privacy-card">
      <dl className="kv">
        <dt>Consent</dt>
        <dd>
          <ConsentSummary userId={user.id} />
        </dd>
        <dt>Share links</dt>
        <dd>
          <SharesSummary userId={user.id} />
        </dd>
        <dt>Retention</dt>
        <dd>Kept until erased</dd>
      </dl>
      <p className="card-hint">
        Details in <Link to={`/users/${user.id}/consents`}>Consents</Link> and <Link to={`/users/${user.id}/shares`}>Shares</Link>.
      </p>
      <DownloadsPanel userId={user.id} bare />
      {canWrite ? (
        <div className="danger-zone">
          <Button
            kind="danger"
            onClick={() => {
              setErasing(true);
            }}
          >
            Erase user
          </Button>
          <p className="card-hint">Erasing asks you to type the user&apos;s id. It cannot be undone.</p>
        </div>
      ) : null}
      {erasing ? (
        <ConfirmDialog
          title="Erase this user and all their data?"
          body="Every record, raw file, connection and token of this user is erased. Providers are told to revoke access. This cannot be undone."
          confirmLabel="Erase user"
          danger
          typed={user.id}
          onConfirm={async () => {
            await remove.mutateAsync();
            toast("Erasure started. The user disappears once every record is erased.");
            void navigate("/users");
          }}
          onClose={() => {
            setErasing(false);
          }}
        />
      ) : null}
    </Card>
  );
}
