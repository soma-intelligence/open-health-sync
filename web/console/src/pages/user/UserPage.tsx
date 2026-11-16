import { Link, useParams } from "react-router";

import { ApiError } from "../../api/client";
import { EmptyState } from "../../ui/EmptyState";
import { ErrorPanel } from "../../ui/ErrorPanel";
import { TabPanel, Tabs, type TabItem } from "../../ui/Tabs";
import { useUserQuery } from "./api";
import { ConnectionsTab } from "./ConnectionsTab";
import { ConsentsTab } from "./ConsentsTab";
import { DataTab } from "./DataTab";
import { SharesTab } from "./SharesTab";
import { SyncRunsTab } from "./SyncRunsTab";
import { UserHeader } from "./UserHeader";

export type UserTab = "data" | "connections" | "consents" | "sync-runs" | "shares";

const TABS: readonly [UserTab, string][] = [
  ["data", "Data"],
  ["connections", "Connections"],
  ["consents", "Consents"],
  ["sync-runs", "Sync runs"],
  ["shares", "Shares"],
];

/** The tab named in the address; anything else is the default, Data. */
export function parseTab(value: string | undefined): UserTab {
  return TABS.find(([id]) => id === value)?.[0] ?? "data";
}

/** Another user is a fresh page: nothing local (a Preview result) carries over. A tab change keeps it. */
export function UserPage() {
  const { userId = "" } = useParams();
  return <UserView key={userId} />;
}

function UserView() {
  const { userId = "", tab: tabParam } = useParams();
  const tab = parseTab(tabParam);
  const user = useUserQuery(userId);

  if (user.isPending) {
    return (
      <div className="page">
        <p role="status">Loading</p>
      </div>
    );
  }
  if (user.isError) {
    if (user.error instanceof ApiError && user.error.status === 404) {
      return (
        <div className="page">
          <EmptyState title="Not found">
            There is no such user in this tenant. Back to <Link to="/users">Users</Link>.
          </EmptyState>
        </div>
      );
    }
    return (
      <div className="page">
        <ErrorPanel error={user.error} />
      </div>
    );
  }

  const items: TabItem<UserTab>[] = TABS.map(([id, label]) => ({
    id,
    label,
    to: id === "data" ? `/users/${userId}` : `/users/${userId}/${id}`,
  }));

  return (
    <div className="page">
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/users">Users</Link>
      </nav>
      <UserHeader user={user.data} />
      <Tabs tabs={items} current={tab} label="User" />
      <TabPanel current={tab}>
        {tab === "data" ? <DataTab userId={userId} /> : null}
        {tab === "connections" ? <ConnectionsTab userId={userId} /> : null}
        {tab === "consents" ? <ConsentsTab userId={userId} /> : null}
        {tab === "sync-runs" ? <SyncRunsTab userId={userId} /> : null}
        {tab === "shares" ? <SharesTab userId={userId} /> : null}
      </TabPanel>
    </div>
  );
}
