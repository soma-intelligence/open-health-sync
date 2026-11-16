import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { User } from "../../api/types";
import { useApi } from "../../session/SessionProvider";

export const PAGE_SIZE = 50;

export interface UserPage {
  data: User[];
  next_cursor: string | null;
}

/** GET /v1/users, 50 a page; externalRef asks for the one user with exactly that external id. */
export function useUsersQuery(cursor: string | undefined, externalRef: string) {
  const api = useApi();
  return useQuery({
    queryKey: ["users", { cursor, externalRef }],
    queryFn: () => api.get<UserPage>("/v1/users", { external_ref: externalRef, cursor, limit: PAGE_SIZE }),
    placeholderData: keepPreviousData,
  });
}

/** POST /v1/users; a blank external id is left out of the body. */
export function useCreateUser() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (externalRef: string) => api.send<User>("POST", "/v1/users", externalRef === "" ? {} : { external_ref: externalRef }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["users"] });
    },
  });
}
