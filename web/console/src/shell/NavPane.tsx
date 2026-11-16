import { NavLink } from "react-router";

import { signOutFailure } from "../session/signOutFailure";
import { useIsAdmin, useSession } from "../session/SessionProvider";
import { Button } from "../ui/Button";
import { Icon, type IconName } from "../ui/Icon";
import { Pill } from "../ui/Pill";
import { useToast } from "../ui/Toast";
import { TenantSwitcher } from "./TenantSwitcher";
import { ThemeControl } from "./ThemeControl";

export interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  /** Shown to an instance admin only. */
  admin?: boolean;
}

export interface NavGroup {
  label: string;
  items: NavItem[];
}

export const NAV_GROUPS: NavGroup[] = [
  {
    label: "Workspace",
    items: [
      { to: "/overview", label: "Overview", icon: "home" },
      { to: "/users", label: "Users", icon: "users" },
      { to: "/providers", label: "Providers", icon: "plug" },
      { to: "/jobs", label: "Jobs", icon: "cpu" },
    ],
  },
  {
    label: "Developers",
    items: [
      { to: "/webhooks", label: "Webhooks", icon: "hook" },
      { to: "/keys", label: "API keys", icon: "key" },
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
  if (!session) return null;
  const { tenant, acting_tenant: acting } = session;

  return (
    <nav id={id} className="nav" aria-label="Console">
      <div className="nav-tenant">
        <span className="avatar" aria-hidden="true">
          {tenant.name.slice(0, 1).toLowerCase()}
        </span>
        <div className="nav-tenant-text">
          <span className="nav-tenant-name">{tenant.name}</span>
          <span className="nav-tenant-role">{tenant.admin ? "admin" : "tenant"}</span>
        </div>
        {session.read_only ? <Pill tone="warn">Read only</Pill> : null}
      </div>
      {acting.id !== tenant.id ? <p className="nav-acting">{`Acting as ${acting.name}`}</p> : null}
      {admin ? <TenantSwitcher /> : null}
      {NAV_GROUPS.map((group) => {
        const items = group.items.filter((i) => !i.admin || admin);
        if (items.length === 0) return null;
        return (
          <div key={group.label} className="nav-group">
            <h2 className="nav-heading">{group.label}</h2>
            {items.map((i) => (
              <NavLink key={i.to} to={i.to} className={({ isActive }) => (isActive ? "nav-link on" : "nav-link")}>
                <Icon name={i.icon} />
                {i.label}
              </NavLink>
            ))}
          </div>
        );
      })}
      <div className="nav-foot">
        <ThemeControl />
        <Button
          kind="ghost"
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
