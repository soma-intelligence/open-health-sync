import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, type RenderResult } from "@testing-library/react";
import type { ReactNode } from "react";
import { RouterProvider, createMemoryRouter, type RouteObject } from "react-router";

import type { ConsoleSession } from "../api/types";
import { BASENAME } from "../lib/basename";
import { DraftProvider } from "../pages/settings/DraftProvider";
import { routes } from "../routes";
import { RequireSession } from "../session/RequireSession";
import { SESSION_KEY, SessionProvider } from "../session/SessionProvider";
import { SignInPage } from "../session/SignInPage";
import { fakeApi, type FakeApi } from "./fake-api";

export interface RenderOptions {
  /** A session to start with; null starts signed out; omitted loads it from GET /console/session. */
  session?: ConsoleSession | null;
  /** The full address to start at, basename included. */
  route?: string;
  api?: FakeApi;
}

export interface Rendered extends RenderResult {
  api: FakeApi;
  queryClient: QueryClient;
  /** The current address, basename included. */
  path: () => string;
  /** Moves the router to a router path (no basename), as a typed address would. */
  go: (to: string) => Promise<void>;
}

/**
 * Renders ui behind the session provider and the sign in route. Pass null to
 * render the console's own routes.
 */
export function renderWithApp(ui: ReactNode, opts: RenderOptions = {}): Rendered {
  const api = opts.api ?? fakeApi({});
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (opts.session !== undefined) queryClient.setQueryData(SESSION_KEY, opts.session);
  const tree: RouteObject[] =
    ui === null
      ? routes
      : [
          { path: "sign-in", element: <SignInPage /> },
          { path: "*", element: <RequireSession>{ui}</RequireSession> },
        ];
  const router = createMemoryRouter(tree, { basename: BASENAME, initialEntries: [opts.route ?? `${BASENAME}/overview`] });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <SessionProvider fetchImpl={api.fetch}>
        <DraftProvider>
          <RouterProvider router={router} />
        </DraftProvider>
      </SessionProvider>
    </QueryClientProvider>,
  );
  return {
    ...result,
    api,
    queryClient,
    path: () => router.state.location.pathname + router.state.location.search,
    go: (to: string) => router.navigate(to),
  };
}
