import type { ReactNode } from "react";
import { Navigate, Outlet, useLocation } from "react-router";

import { BASENAME } from "../lib/basename";
import { useSession } from "./SessionProvider";

/** Renders its children (or the matched child route) for a signed in visit, else goes to sign in. */
export function RequireSession({ children }: { children?: ReactNode }) {
  const { status } = useSession();
  const location = useLocation();
  if (status === "loading") return <p role="status">Loading</p>;
  if (status === "signed-out") {
    // useLocation drops the router basename; the return path keeps it.
    const from = `${BASENAME}${location.pathname}${location.search}`;
    return <Navigate to="/sign-in" replace state={{ from }} />;
  }
  return children ?? <Outlet />;
}
