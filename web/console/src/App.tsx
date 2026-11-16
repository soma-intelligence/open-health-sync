import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { RouterProvider } from "react-router";

import { ApiError } from "./api/client";
import { DraftProvider } from "./pages/settings/DraftProvider";
import { router } from "./routes";
import { SessionProvider } from "./session/SessionProvider";

function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: (count, error) => count < 1 && !(error instanceof ApiError && error.status >= 400 && error.status < 500),
      },
    },
  });
}

export function App() {
  const [queryClient] = useState(newQueryClient);
  return (
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        {/* Above the router: unsaved settings survive the sign in a 401 leads to. */}
        <DraftProvider>
          <RouterProvider router={router} />
        </DraftProvider>
      </SessionProvider>
    </QueryClientProvider>
  );
}
