import { Navigate, createBrowserRouter, useLocation, type RouteObject } from "react-router";

import { BASENAME } from "./lib/basename";
import { NotFoundPage } from "./pages/NotFoundPage";
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

const PAGE_PATHS = ["overview", "users", "providers", "jobs", "webhooks", "keys", "audit", "settings"];

export const routes: RouteObject[] = [
  { path: "sign-in", element: <SignInPage /> },
  {
    element: <RequireSession />,
    children: [
      {
        element: <AppShell />,
        children: [
          { index: true, element: <Navigate to="/overview" replace /> },
          ...PAGE_PATHS.map((path) => ({ path: `${path}/*`, element: <Placeholder /> })),
          { path: "*", element: <NotFoundPage /> },
        ],
      },
    ],
  },
];

export const router = createBrowserRouter(routes, { basename: BASENAME });
