import { Navigate, createBrowserRouter, type RouteObject } from "react-router";

import { BASENAME } from "./lib/basename";
import { AuditPage } from "./pages/audit/AuditPage";
import { JobsPage } from "./pages/jobs/JobsPage";
import { KeysPage } from "./pages/keys/KeysPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { OverviewPage } from "./pages/overview/OverviewPage";
import { ProviderPage } from "./pages/providers/ProviderPage";
import { ProvidersPage } from "./pages/providers/ProvidersPage";
import { SettingsPage } from "./pages/settings/SettingsPage";
import { UserPage } from "./pages/user/UserPage";
import { UsersPage } from "./pages/users/UsersPage";
import { WebhookPage } from "./pages/webhooks/WebhookPage";
import { WebhooksPage } from "./pages/webhooks/WebhooksPage";
import { AppShell } from "./shell/AppShell";
import { RequireSession } from "./session/RequireSession";
import { SignInPage } from "./session/SignInPage";

export const routes: RouteObject[] = [
  { path: "sign-in", element: <SignInPage /> },
  {
    element: <RequireSession />,
    children: [
      {
        element: <AppShell />,
        children: [
          { index: true, element: <Navigate to="/overview" replace /> },
          { path: "overview", element: <OverviewPage /> },
          { path: "users", element: <UsersPage /> },
          { path: "users/:userId", element: <UserPage /> },
          { path: "users/:userId/:tab", element: <UserPage /> },
          { path: "providers", element: <ProvidersPage /> },
          { path: "providers/:slug", element: <ProviderPage /> },
          { path: "jobs", element: <JobsPage /> },
          { path: "webhooks", element: <WebhooksPage /> },
          { path: "webhooks/:webhookId", element: <WebhookPage /> },
          { path: "keys", element: <KeysPage /> },
          { path: "audit", element: <AuditPage /> },
          { path: "settings", element: <SettingsPage /> },
          { path: "settings/:section", element: <SettingsPage /> },
          { path: "settings/:section/:name", element: <SettingsPage /> },
          { path: "*", element: <NotFoundPage /> },
        ],
      },
    ],
  },
];

export const router = createBrowserRouter(routes, { basename: BASENAME });
