import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { APIKey, AuditEvent, ConsoleSession } from "../../api/types";
import { fakeApi, type FakeApi, type FakeCall, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import { renderWithApp } from "../../test/render";
import { routeLabel } from "./AuditTable";

const session = sessionJson as ConsoleSession;
const adminSession: ConsoleSession = { ...session, tenant: { ...session.tenant, admin: true } };

const key: APIKey = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  name: null,
  prefix: "ohs_sk_AbCdE",
  created_at: "2026-09-01T08:00:00Z",
  last_used_at: null,
  revoked_at: null,
  expires_at: null,
  scopes: ["*"],
  current: true,
};

function event(n: number, over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 1000 - n,
    at: "2026-10-01T08:30:00Z",
    actor: { type: "api_key", id: key.id },
    action: "data.read",
    user_id: null,
    actor_tenant_id: null,
    method: "GET",
    // As the server records it: the Go route pattern, method included.
    route: `GET /v1/route-${String(n)}`,
    status: 200,
    detail: {},
    ...over,
  };
}

const page1 = Array.from({ length: 50 }, (_, i) => event(i + 1));
const page2 = [event(51), event(52)];

function view(routes: Record<string, FakeHandler>, s: ConsoleSession = session) {
  const api: FakeApi = fakeApi({ "GET /v1/keys": { body: { data: [key] } }, ...routes });
  return renderWithApp(null, { session: s, api, route: "/console/audit" });
}

const list = (c: FakeCall) =>
  c.path.includes("cursor=cur-2") ? { body: { data: page2, next_cursor: null } } : { body: { data: page1, next_cursor: "cur-2" } };

describe("audit", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("lists events newest first, 50 per page", async () => {
    const { api } = view({ "GET /v1/audit": list });
    expect(await screen.findByRole("heading", { name: "Audit log", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Who read, exported, changed or deleted what, with which key.")).toBeInTheDocument();
    expect(await screen.findByText("GET /v1/route-1")).toBeInTheDocument();
    expect(api.calls.find((c) => c.path.startsWith("/v1/audit"))?.path).toBe("/v1/audit?limit=50");
    for (const h of ["Time", "Action", "Route", "Actor", "Status"]) {
      expect(screen.getByRole("columnheader", { name: h })).toBeInTheDocument();
    }
    expect(screen.getAllByRole("row")).toHaveLength(51);
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("GET /v1/route-51")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === "/v1/audit?cursor=cur-2&limit=50")).toBe(true);
  });

  it("filters by action", async () => {
    const { api } = view({ "GET /v1/audit": list });
    await screen.findByText("GET /v1/route-1");
    await userEvent.selectOptions(screen.getByLabelText("Action"), "key.create");
    await screen.findByText("GET /v1/route-1");
    expect(api.calls.some((c) => c.path === "/v1/audit?action=key.create&limit=50")).toBe(true);
  });

  it("filters by actor", async () => {
    const { api } = view({ "GET /v1/audit": list });
    await screen.findByText("GET /v1/route-1");
    await userEvent.selectOptions(screen.getByLabelText("Actor"), "ohs_sk_AbCdE...");
    await screen.findByText("GET /v1/route-1");
    expect(api.calls.some((c) => c.path === `/v1/audit?actor_type=api_key&actor_id=${key.id}&limit=50`)).toBe(true);
    const before = api.calls.length;
    await userEvent.selectOptions(screen.getByLabelText("Actor"), "Any actor");
    await screen.findByText("GET /v1/route-1");
    expect(api.calls.slice(before).some((c) => c.path === "/v1/audit?limit=50")).toBe(true);
  });

  it("shows the method once, whether the pattern holds it or not", async () => {
    const rows = [event(1), event(2, { method: "POST", route: "/v1/legacy" })];
    view({ "GET /v1/audit": { body: { data: rows, next_cursor: null } } });
    expect(await screen.findByText("GET /v1/route-1")).toBeInTheDocument();
    expect(screen.getByText("POST /v1/legacy")).toBeInTheDocument();
    expect(screen.queryByText(/GET GET/)).not.toBeInTheDocument();
  });

  it("routeLabel adds the method only when the route lacks it", () => {
    expect(routeLabel("GET", "GET /v1/keys")).toBe("GET /v1/keys");
    expect(routeLabel("DELETE", "/v1/keys/{key_id}")).toBe("DELETE /v1/keys/{key_id}");
    expect(routeLabel(undefined, "/v1/keys")).toBe("/v1/keys");
  });

  it("selecting an event inspects it", async () => {
    view({ "GET /v1/audit": list });
    await userEvent.click(await screen.findByText("GET /v1/route-3"));
    // List pages start with the inspector closed: the operator opens it.
    await userEvent.click(screen.getByRole("button", { name: "Inspector" }));
    const pane = await screen.findByRole("complementary", { name: "Inspector" });
    expect(await within(pane).findByText(/"id": 997/)).toBeInTheDocument();
  });

  it("names the console session behind an event", async () => {
    const other = "22222222-2222-4222-8222-222222222222";
    const rows = [event(1, { actor_tenant_id: other }), event(2, { actor_tenant_id: session.acting_tenant.id }), event(3)];
    view({
      "GET /v1/audit": { body: { data: rows, next_cursor: null } },
      "GET /v1/admin/tenants": { body: { data: [{ id: other, name: "Globex", admin: false, created_at: "2026-01-01T00:00:00Z", active_keys: 1 }] } },
      "GET /v1/admin/settings": {
        body: { revision: "1", data: [{ name: "OHS_AUDIT_RETENTION_DAYS", label: "Audit retention", section: "Security", help: "", type: "int", min: 1, max: null, values: null, default: "365", default_note: null, secret: false, apply: "live", source: "default", value: "365", stored_value: null, invalid: null }] },
      },
    }, adminSession);
    expect(await screen.findByText("via console session of Globex")).toBeInTheDocument();
    expect(screen.getAllByText(/via console session of/)).toHaveLength(1);
    expect(await screen.findByText(/Kept 365 days\./)).toBeInTheDocument();
  });

  it("shows the tenant id to a tenant that cannot list tenants", async () => {
    const other = "22222222-2222-4222-8222-222222222222";
    view({ "GET /v1/audit": { body: { data: [event(1, { actor_tenant_id: other })], next_cursor: null } } });
    expect(await screen.findByText(`via console session of ${other}`)).toBeInTheDocument();
    expect(screen.queryByText(/Kept/)).not.toBeInTheDocument();
  });

  it("has no action button", async () => {
    view({ "GET /v1/audit": list });
    await screen.findByText("GET /v1/route-1");
    const main = screen.getByRole("main");
    expect(within(main).queryAllByRole("button").map((b) => b.textContent)).toEqual(["Previous", "Next"]);
  });
});
