import { Navigate, createBrowserRouter, useLocation, type RouteObject } from "react-router";

import { BASENAME } from "./lib/basename";
import { JobsPage } from "./pages/jobs/JobsPage";
import { KeysPage } from "./pages/keys/KeysPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { OverviewPage } from "./pages/overview/OverviewPage";
import { ProviderPage } from "./pages/providers/ProviderPage";
import { ProvidersPage } from "./pages/providers/ProvidersPage";
import { UserPage } from "./pages/user/UserPage";
import { UsersPage } from "./pages/users/UsersPage";
import { WebhookPage } from "./pages/webhooks/WebhookPage";
import { WebhooksPage } from "./pages/webhooks/WebhooksPage";
import { AppShell } from "./shell/AppShell";
import { RequireSession } from "./session/RequireSession";
import { SignInPage } from "./session/SignInPage";

// Replaced page by page by the tasks that follow.
function Placeholder() {
  const { pathname } = useLocation();
  return (
    <div className="page">
      <h1 className="page-title">Console</h1>
      <p>{pathname}</p>
    </div>
  );
}

const PAGE_PATHS = ["audit", "settings"];

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
          ...PAGE_PATHS.map((path) => ({ path: `${path}/*`, element: <Placeholder /> })),
          { path: "*", element: <NotFoundPage /> },
        ],
      },
    ],
  },
];

export const router = createBrowserRouter(routes, { basename: BASENAME });
