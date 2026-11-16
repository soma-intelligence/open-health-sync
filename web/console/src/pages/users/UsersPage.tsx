import { useMemo, useState, type ReactNode } from "react";

import { ApiError } from "../../api/client";
import type { User } from "../../api/types";
import { useInspect, type InspectorEntry } from "../../inspector/InspectorProvider";
import { useCanWrite } from "../../session/SessionProvider";
import { Button } from "../../ui/Button";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { useOpenCreate } from "../../palette/useOpenCreate";
import { Pager, usePager } from "../../ui/Pager";
import { PAGE_SIZE, useUsersQuery } from "./api";
import { CreateUserDialog } from "./CreateUserDialog";
import { UserSearch } from "./UserSearch";
import { UsersTable } from "./UsersTable";

export function UsersPage() {
  const canWrite = useCanWrite();
  const pager = usePager();
  const [externalRef, setExternalRef] = useState("");
  const [creating, setCreating] = useState(false);
  const [selected, setSelected] = useState<User | null>(null);
  useOpenCreate(() => {
    setCreating(true);
  });
  const users = useUsersQuery(pager.cursor, externalRef);
  const { data, error } = users;

  const entry = useMemo<InspectorEntry | null>(() => {
    if (selected) {
      return {
        kind: "user",
        title: selected.external_ref ?? selected.id,
        id: selected.id,
        request: { method: "GET", path: `/v1/users/${selected.id}` },
        response: selected,
      };
    }
    const query: Record<string, string> = { limit: String(PAGE_SIZE) };
    if (externalRef !== "") query["external_ref"] = externalRef;
    if (pager.cursor !== undefined) query["cursor"] = pager.cursor;
    const base = { kind: "users" as const, title: "Users", request: { method: "GET" as const, path: "/v1/users", query } };
    if (data !== undefined) return { ...base, response: data };
    return error instanceof ApiError ? { ...base, response: undefined, error } : null;
  }, [selected, data, error, externalRef, pager.cursor]);
  useInspect(entry);

  const search = (value: string) => {
    pager.reset();
    setSelected(null);
    setExternalRef(value);
  };

  let empty: ReactNode;
  if (externalRef !== "") {
    empty = <EmptyState title={`No user with external id ${externalRef}`}>Ids match exactly, capitals included.</EmptyState>;
  } else {
    empty = <EmptyState title="No users yet">Create one here, or let your app do it with POST /v1/users.</EmptyState>;
  }

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">Users</h1>
          <p className="page-sub">People whose devices and accounts feed this tenant.</p>
        </div>
        <div className="page-actions">
          <UserSearch key={externalRef} value={externalRef} onSearch={search} />
          {canWrite ? (
            <Button
              kind="primary"
              onClick={() => {
                setCreating(true);
              }}
            >
              New user
            </Button>
          ) : null}
        </div>
      </header>
      {users.isPending ? <p role="status">Loading</p> : null}
      {error !== null && data === undefined ? <ErrorPanel error={error} /> : null}
      {data ? (
        <>
          <UsersTable
            rows={data.data}
            selectedId={selected?.id}
            onSelect={setSelected}
            empty={empty}
          />
          <Pager pager={pager} nextCursor={data.next_cursor}
            loading={users.isPlaceholderData} />
        </>
      ) : null}
      {creating ? (
        <CreateUserDialog
          onClose={() => {
            setCreating(false);
          }}
        />
      ) : null}
    </div>
  );
}
