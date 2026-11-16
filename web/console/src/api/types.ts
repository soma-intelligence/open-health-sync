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
export type AuditEvent = Schemas["AuditEvent"];

export type Setting = Schemas["AdminSetting"];
export type SettingsList = Schemas["AdminSettings"];
export type SettingsProblem = Schemas["AdminSettingsProblem"];
export type ProcessHeartbeat = Schemas["AdminProcess"];
export type ProcessList = Schemas["AdminProcesses"];
