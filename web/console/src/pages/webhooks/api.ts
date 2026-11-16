import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiPath } from "../../api/paths";
import type {
  CreateWebhookRequest,
  Delivery,
  RotatedWebhookSecret,
  UpdateWebhookRequest,
  Webhook,
  WebhookDeliveryList,
  WebhookList,
  WebhookWithSecret,
} from "../../api/types";
import { useApi } from "../../session/SessionProvider";

export const WEBHOOKS_PATH = "/v1/webhooks";
export const DELIVERIES_QUERY = { limit: 50 } as const;

/** /v1/webhooks/{id} and what lies under it. */
export function webhookPath(id: string, ...rest: string[]): string {
  return apiPath(["v1", "webhooks", id, ...rest]);
}

const WEBHOOKS_KEY = ["webhooks"] as const;

/** GET /v1/webhooks, one page. */
export function useWebhooksQuery() {
  const api = useApi();
  return useQuery({
    queryKey: [...WEBHOOKS_KEY, "list"],
    queryFn: () => api.get<WebhookList>(WEBHOOKS_PATH),
  });
}

/** GET /v1/webhooks/{id}. */
export function useWebhookQuery(id: string) {
  const api = useApi();
  return useQuery({
    queryKey: [...WEBHOOKS_KEY, id],
    queryFn: () => api.get<Webhook>(webhookPath(id)),
  });
}

/** GET /v1/webhooks/{id}/deliveries?limit=50, newest first. */
export function useDeliveriesQuery(id: string) {
  const api = useApi();
  return useQuery({
    queryKey: [...WEBHOOKS_KEY, id, "deliveries"],
    queryFn: () => api.get<WebhookDeliveryList>(webhookPath(id, "deliveries"), DELIVERIES_QUERY),
  });
}

function useRefresh() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: WEBHOOKS_KEY });
}

/** POST /v1/webhooks. The signing secret is returned to the caller and not kept by the cache. */
export function useCreateWebhook() {
  const api = useApi();
  const refresh = useRefresh();
  return useMutation({
    gcTime: 0,
    mutationFn: (body: CreateWebhookRequest) => api.send<WebhookWithSecret>("POST", WEBHOOKS_PATH, body),
    onSettled: refresh,
  });
}

/** PATCH /v1/webhooks/{id}: the fields given only. */
export function useUpdateWebhook(id: string) {
  const api = useApi();
  const refresh = useRefresh();
  return useMutation({
    mutationFn: (body: UpdateWebhookRequest) => api.send<Webhook>("PATCH", webhookPath(id), body),
    onSettled: refresh,
  });
}

/** DELETE /v1/webhooks/{id}. */
export function useDeleteWebhook(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api.send<undefined>("DELETE", webhookPath(id)),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: [...WEBHOOKS_KEY, id] });
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: [...WEBHOOKS_KEY, "list"] });
    },
  });
}

/** POST /v1/webhooks/{id}/test. */
export function useTestWebhook(id: string) {
  const api = useApi();
  const refresh = useRefresh();
  return useMutation({
    mutationFn: () => api.send<Delivery>("POST", webhookPath(id, "test"), {}),
    onSettled: refresh,
  });
}

/** POST /v1/webhooks/{id}/rotate-secret. The new secret is returned to the caller and not kept by the cache. */
export function useRotateWebhookSecret(id: string) {
  const api = useApi();
  const refresh = useRefresh();
  return useMutation({
    gcTime: 0,
    mutationFn: (expirePrevious: boolean) =>
      api.send<RotatedWebhookSecret>("POST", webhookPath(id, "rotate-secret"), expirePrevious ? { expire_previous: true } : {}),
    onSettled: refresh,
  });
}

/** POST .../deliveries/{delivery}/retry or /replay. */
export function useRedeliver(id: string, how: "retry" | "replay") {
  const api = useApi();
  const refresh = useRefresh();
  return useMutation({
    mutationFn: (delivery: number) => api.send<Delivery>("POST", webhookPath(id, "deliveries", String(delivery), how)),
    onSettled: refresh,
  });
}
