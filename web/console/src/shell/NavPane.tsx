import { NavLink } from "react-router";

import type { KeyList, Pipeline } from "../api/types";
import { formatCount } from "../lib/format";
import { useKeysQuery } from "../pages/keys/api";
import { usePipelineQuery } from "../pages/overview/api";
import { signOutFailure } from "../session/signOutFailure";
import { useIsAdmin, useSession } from "../session/SessionProvider";
import { Button } from "../ui/Button";
import { BrandMark, Icon, type IconName } from "../ui/Icon";
import { Pill } from "../ui/Pill";
import { useToast } from "../ui/Toast";
import { TenantSwitcher } from "./TenantSwitcher";
import { ThemeControl } from "./ThemeControl";

export interface NavCounts {
  pipeline?: Pipeline | undefined;
  keys?: KeyList | undefined;
}

/** A badge beside a page: a count, and "bad" when the count is something to fix. */
interface Badge {
  value: number;
  bad?: boolean;
}

export interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  /** Shown to an instance admin only. */
  admin?: boolean;
  /** The count beside the label, from data the console already loads; undefined hides it. */
  count?: (c: NavCounts) => Badge | undefined;
}

export interface NavGroup {
  label: string;
  items: NavItem[];
}

/** Keys that work now: not revoked and not past their expiry. */
export function activeKeys(keys: KeyList, now = Date.now()): number {
  return keys.data.filter((k) => !k.revoked_at && (k.expires_at === null || Date.parse(k.expires_at) > now)).length;
}

export const NAV_GROUPS: NavGroup[] = [
  {
    label: "Workspace",
    items: [
      { to: "/overview", label: "Overview", icon: "grid" },
      { to: "/users", label: "Users", icon: "users", count: ({ pipeline }) => (pipeline ? { value: pipeline.users.active } : undefined) },
      {
        to: "/providers",
        label: "Providers",
        icon: "plug",
        count: ({ pipeline }) => (pipeline ? { value: pipeline.sources.filter((s) => s.enabled).length } : undefined),
      },
      {
        to: "/jobs",
        label: "Jobs",
        icon: "cpu",
        count: ({ pipeline }) => {
          if (!pipeline) return undefined;
          const stuck = pipeline.sync.failed_24h + pipeline.processing.parked;
          return stuck > 0 ? { value: stuck, bad: true } : undefined;
        },
      },
    ],
  },
  {
    label: "Build",
    items: [
      { to: "/keys", label: "API keys", icon: "key", count: ({ keys }) => (keys ? { value: activeKeys(keys) } : undefined) },
      {
        to: "/webhooks",
        label: "Webhooks",
        icon: "hook",
        count: ({ pipeline }) => {
          if (!pipeline) return undefined;
          const { endpoints, endpoints_failing: failing, endpoints_disabled: disabled } = pipeline.delivery;
          return failing + disabled > 0 ? { value: failing + disabled, bad: true } : { value: endpoints };
        },
      },
      { to: "/audit", label: "Audit log", icon: "list" },
    ],
  },
  {
    label: "Instance",
    items: [{ to: "/settings", label: "Settings", icon: "sliders", admin: true }],
  },
];

export function NavPane({ id }: { id: string }) {
  const { session, signOut } = useSession();
  const toast = useToast();
  const admin = useIsAdmin();
  // The same queries as the Overview and API keys pages: one request serves both.
  const pipeline = usePipelineQuery();
  const keys = useKeysQuery({ glance: true });
  if (!session) return null;
  const { tenant, acting_tenant: acting } = session;
  const counts: NavCounts = { pipeline: pipeline.data, keys: keys.data };

  return (
    <nav id={id} className="nav" aria-label="Console">
      <span className="nav-brand">
        <BrandMark />
        <span>Open Health Sync</span>
      </span>
      <div className="tenant-card">
        <div className="tenant-row">
          <span className="avatar" aria-hidden="true">
            {tenant.name.slice(0, 1).toUpperCase()}
          </span>
          <div className="nav-tenant-text">
            <span className="nav-tenant-name">{tenant.name}</span>
            <span className="nav-tenant-role">{tenant.admin ? "Instance admin" : "Tenant"}</span>
          </div>
          {session.read_only ? <Pill tone="warn">Read only</Pill> : null}
        </div>
        {admin ? <TenantSwitcher /> : null}
        {acting.id !== tenant.id ? <p className="nav-acting">{`Acting as ${acting.name}`}</p> : null}
      </div>
      {NAV_GROUPS.map((group) => {
        const items = group.items.filter((i) => !i.admin || admin);
        if (items.length === 0) return null;
        return (
          <div key={group.label} className="nav-group">
            <h2 className="nav-heading">{group.label}</h2>
            {items.map((i) => {
              const badge = i.count?.(counts);
              return (
                <NavLink key={i.to} to={i.to} className={({ isActive }) => (isActive ? "nav-link on" : "nav-link")}>
                  <Icon name={i.icon} />
                  {i.label}
                  {badge ? (
                    // The page itself says the number: the badge is a glance, kept out of the link's name.
                    <span className={badge.bad ? "nav-count bad" : "nav-count"} aria-hidden="true">
                      {formatCount(badge.value)}
                    </span>
                  ) : null}
                </NavLink>
              );
            })}
          </div>
        );
      })}
      <div className="nav-foot">
        <ThemeControl />
        <Button
          kind="ghost"
          className="nav-signout"
          onClick={() => {
            signOut().catch((e: unknown) => {
              toast(signOutFailure(e), "bad");
            });
          }}
        >
          <Icon name="out" size="sm" />
          Sign out
        </Button>
      </div>
    </nav>
  );
}
