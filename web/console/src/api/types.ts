import type { components, paths } from "./schema";

/** The application/json body of the answer with status S to method M of path P. */
export type JsonOf<P extends keyof paths, M extends keyof paths[P], S extends number> = paths[P][M] extends {
  responses: infer R;
}
  ? S extends keyof R
    ? R[S] extends { content: { "application/json": infer J } }
      ? J
      : never
    : never
  : never;

type Schemas = components["schemas"];

export type ConsoleSession = Schemas["ConsoleSession"];
export type ConsoleTenant = Schemas["ConsoleTenant"];
export type Problem = Schemas["Problem"];
export type Tenant = Schemas["Tenant"];
export type AdminTenant = Schemas["AdminTenant"];
export type User = Schemas["User"];
export type CreateUserRequest = Schemas["CreateUserRequest"];
/** One page of GET /v1/users, as the schema says it. */
export type UserList = JsonOf<"/v1/users", "get", 200>;
export type Pipeline = Schemas["Pipeline"];
export type Provider = Schemas["Provider"];
export type AdminProvider = Schemas["AdminProvider"];
export type Webhook = Schemas["Webhook"];
export type Delivery = Schemas["WebhookDelivery"];
export type APIKey = Schemas["APIKey"];
export type CreatedAPIKey = Schemas["CreatedAPIKey"];
export type KeyScope = Schemas["KeyScope"];
export type CreateKeyRequest = Schemas["CreateKeyRequest"];
export type KeyList = JsonOf<"/v1/keys", "get", 200>;
export type AuditEvent = Schemas["AuditEvent"];

export type Setting = Schemas["AdminSetting"];
export type SettingsList = Schemas["AdminSettings"];
export type SettingsProblem = Schemas["AdminSettingsProblem"];
export type ProcessHeartbeat = Schemas["AdminProcess"];
export type ProcessList = Schemas["AdminProcesses"];

export type UserConnections = JsonOf<"/v1/users/{id}/connections", "get", 200>;
export type UserConnection = Schemas["Connection"];
export type UserFreshness = JsonOf<"/v1/users/{id}/freshness", "get", 200>;
export type SourceFreshness = Schemas["SourceFreshness"];
export type UserCoverage = Schemas["Coverage"];
export type ConnectSession = Schemas["ConnectSession"];
export type UserConsents = JsonOf<"/v1/users/{id}/consents", "get", 200>;
export type ConsentHistory = JsonOf<"/v1/users/{id}/consents/history", "get", 200>;
export type ConsentEvent = Schemas["ConsentEvent"];
export type UserSyncRuns = JsonOf<"/v1/users/{id}/sync-runs", "get", 200>;
export type SyncRun = Schemas["SyncRun"];
export type UserShares = JsonOf<"/v1/users/{id}/shares", "get", 200>;
export type Share = Schemas["Share"];
export type UserDaily = JsonOf<"/v1/users/{id}/daily", "get", 200>;
export type UserSleep = JsonOf<"/v1/users/{id}/sleep", "get", 200>;
export type UserWorkouts = JsonOf<"/v1/users/{id}/workouts", "get", 200>;
/** The answer of any previewed dataset. */
export type PreviewAnswer = UserDaily | UserSleep | UserWorkouts;

export type AdminProviderList = JsonOf<"/v1/admin/providers", "get", 200>;
export type AdminProviderKey = Schemas["AdminProviderKey"];
export type AdminProviderPatch = Schemas["AdminProviderPatch"];
export type ProviderList = JsonOf<"/v1/providers", "get", 200>;
export type ProviderListItem = ProviderList["data"][number];
export type AdminTenantProvider = Schemas["AdminTenantProvider"];
export type AdminTenantProviderList = JsonOf<"/v1/admin/tenants/{tenant_id}/providers", "get", 200>;
export type AdminTenantProviderPatch = Schemas["AdminTenantProviderPatch"];

export type TenantSyncRunList = JsonOf<"/v1/pipeline/sync-runs", "get", 200>;
export type TenantSyncRun = Schemas["TenantSyncRun"];
export type EtlJobList = JsonOf<"/v1/pipeline/etl-jobs", "get", 200>;
export type EtlJob = Schemas["ETLJob"];
export type RetryParkedJobs = JsonOf<"/v1/pipeline/etl-jobs/retry", "post", 200>;

export type WebhookEventType = Schemas["WebhookEventType"];
export type WebhookList = JsonOf<"/v1/webhooks", "get", 200>;
export type WebhookWithSecret = Schemas["WebhookWithSecret"];
export type CreateWebhookRequest = Schemas["CreateWebhookRequest"];
export type UpdateWebhookRequest = Schemas["UpdateWebhookRequest"];
export type RotatedWebhookSecret = JsonOf<"/v1/webhooks/{webhook_id}/rotate-secret", "post", 200>;
export type WebhookDeliveryList = JsonOf<"/v1/webhooks/{webhook_id}/deliveries", "get", 200>;
