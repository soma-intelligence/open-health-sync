import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { CreateUserRequest, User, UserList } from "../../api/types";
import { useApi } from "../../session/SessionProvider";

export const PAGE_SIZE = 50;

/**
 * GET /v1/users, 50 a page; externalRef asks for the one user with exactly that external id.
 * enabled: false waits (the overview reads the first page only when it may hold every user).
 */
export function useUsersQuery(cursor: string | undefined, externalRef: string, { enabled = true }: { enabled?: boolean } = {}) {
  const api = useApi();
  return useQuery({
    queryKey: ["users", { cursor, externalRef }],
    queryFn: () => api.get<UserList>("/v1/users", { external_ref: externalRef, cursor, limit: PAGE_SIZE }),
    placeholderData: keepPreviousData,
    enabled,
  });
}

/** POST /v1/users; a blank external id is left out of the body. */
export function useCreateUser() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (externalRef: string) => {
      const body: CreateUserRequest = externalRef === "" ? {} : { external_ref: externalRef };
      return api.send<User>("POST", "/v1/users", body);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["users"] });
    },
  });
}
