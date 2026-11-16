import { Navigate, createBrowserRouter, useLocation, type RouteObject } from "react-router";

import { RequireSession } from "./session/RequireSession";
import { SignInPage } from "./session/SignInPage";

// Replaced page by page by the tasks that follow.
function Placeholder() {
  const { pathname } = useLocation();
  return (
    <main>
      <h1>Console</h1>
      <p>{pathname}</p>
    </main>
  );
}

export const routes: RouteObject[] = [
  { path: "sign-in", element: <SignInPage /> },
  {
    element: <RequireSession />,
    children: [
      { index: true, element: <Navigate to="/overview" replace /> },
      { path: "*", element: <Placeholder /> },
    ],
  },
];

export const router = createBrowserRouter(routes, { basename: "/console" });
