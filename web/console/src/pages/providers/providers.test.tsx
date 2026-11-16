import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { AdminProvider, AdminTenantProvider, ConsoleSession } from "../../api/types";
import { keysPatch } from "../../lib/providers";
import { fakeApi, type FakeApi, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import readOnlyJson from "../../test/fixtures/session-read-only.json";
import { renderWithApp } from "../../test/render";

const tenantSession = sessionJson as ConsoleSession;
const admin: ConsoleSession = { ...tenantSession, tenant: { ...tenantSession.tenant, admin: true }, acting_tenant: { ...tenantSession.acting_tenant, admin: true } };
const adminReadOnly: ConsoleSession = { ...(readOnlyJson as ConsoleSession), tenant: { ...tenantSession.tenant, admin: true } };
const TENANT = admin.acting_tenant.id;

function provider(slug: string, over: Partial<AdminProvider> = {}): AdminProvider {
  return {
    slug,
    name: slug === "oura" ? "Oura" : slug === "withings" ? "Withings" : "Apple Health",
    kinds: ["oauth", "push"],
    status: "active",
    enabled: true,
    source: "console",
    keys: [
      { name: "CLIENT_ID", env: "OHS_OURA_CLIENT_ID", required: true, secret: false, set: true, value: "abc123" },
      { name: "CLIENT_SECRET", env: "OHS_OURA_CLIENT_SECRET", required: true, secret: true, set: true, value: null },
    ],
    error: null,
    urls: { redirect: "https://ohs.example.com/v1/oauth/oura/callback", inbound: "https://ohs.example.com/v1/webhooks/oura" },
    sync: {
      pull_every_seconds: 21600,
      pull_every_without_push_seconds: 3600,
      lookback_seconds: 21600,
      max_catch_up_seconds: 604800,
      backfill_seconds: 7776000,
      backfill_chunk_seconds: 604800,
      reconcile_seconds: 172800,
    },
    updated_at: "2026-10-01T08:30:00Z",
    updated_by: "key-id-1",
    ...over,
  };
}

const oura = provider("oura");
const withings = provider("withings", { status: "not_configured", source: null, updated_at: null, updated_by: null, keys: [], urls: { redirect: null, inbound: null }, sync: null });
const apple = provider("apple_health", { status: "built_in", kinds: ["device"], source: null, keys: [], urls: { redirect: null, inbound: null }, sync: null, updated_at: null, updated_by: null });

function tenantRow(p: AdminProvider, over: Partial<AdminTenantProvider> = {}): AdminTenantProvider {
  return { slug: p.slug, name: p.name, kinds: p.kinds, instance_status: p.status, enabled: true, own_keys: false, available: true, updated_at: null, ...over };
}

const adminRoutes: Record<string, FakeHandler> = {
  "GET /v1/admin/providers": { body: { data: [oura, withings, apple], public_url: true } },
  [`GET /v1/admin/tenants/${TENANT}/providers`]: {
    body: { data: [tenantRow(oura), tenantRow(withings, { enabled: false, available: false }), tenantRow(apple)] },
  },
  "GET /v1/admin/providers/oura": { body: oura },
};

function view(routes: Record<string, FakeHandler>, route: string, s: ConsoleSession = admin) {
  const api: FakeApi = fakeApi({ ...adminRoutes, ...routes });
  const out = renderWithApp(null, { session: s, api, route });
  return { ...out, api };
}

async function confirmIn(label: string) {
  const dialog = await screen.findByRole("dialog");
  await userEvent.click(within(dialog).getByRole("button", { name: label }));
  return dialog;
}

describe("providers list", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("an admin sees every connector from GET /v1/admin/providers, grouped", async () => {
    view({}, "/console/providers");
    expect(await screen.findByRole("heading", { name: "Providers", level: 1 })).toBeInTheDocument();
    expect(await screen.findByText("2 of 3 on. Keys saved here are sealed with the instance encryption key.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Cloud providers" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "App uploads" })).toBeInTheDocument();
    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers.slice(0, 4)).toEqual(["Provider", "Status", "Keys from", "This tenant"]);
    const rows = screen.getAllByRole("row");
    const ouraRow = rows.find((r) => within(r).queryByText("Oura"));
    expect(ouraRow).toBeDefined();
    expect(within(ouraRow as HTMLElement).getByText("Console")).toBeInTheDocument();
    const appleRow = rows.find((r) => within(r).queryByText("Apple Health"));
    expect(within(appleRow as HTMLElement).getByText("Built-in")).toBeInTheDocument();
    const withingsRow = rows.find((r) => within(r).queryByText("Withings"));
    expect(within(withingsRow as HTMLElement).getByText("Not configured")).toBeInTheDocument();
    await waitFor(() => {
      expect(within(withingsRow as HTMLElement).getByText("Off")).toBeInTheDocument();
    });
  });

  it("a tenant that is not an admin sees GET /v1/providers, read only", async () => {
    const api = fakeApi({ "GET /v1/providers": { body: { data: [{ slug: "oura", name: "Oura", kinds: ["oauth"] }] } } });
    renderWithApp(null, { session: tenantSession, api, route: "/console/providers" });
    expect(await screen.findByText("Oura")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path.startsWith("/v1/admin"))).toBe(false);
    expect(within(screen.getByRole("main")).queryByRole("button")).toBeNull();
    expect(within(screen.getByRole("main")).queryByRole("link")).toBeNull();
  });
});

describe("provider page", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("turns a provider on and off after a confirmation", async () => {
    const { api } = view({ "PATCH /v1/admin/providers/oura": { body: { ...oura, enabled: false, status: "off" } } }, "/console/providers/oura");
    await userEvent.click(await screen.findByRole("button", { name: "Turn off" }));
    expect(api.callsTo("PATCH /v1/admin/providers/oura")).toHaveLength(0);
    await confirmIn("Turn off");
    await waitFor(() => {
      expect(api.callsTo("PATCH /v1/admin/providers/oura")).toHaveLength(1);
    });
    expect(api.callsTo("PATCH /v1/admin/providers/oura")[0]?.body).toEqual({ enabled: false });
  });

  it("turns a provider off for the acting tenant only", async () => {
    const path = `/v1/admin/tenants/${TENANT}/providers/oura`;
    const { api } = view({ [`PATCH ${path}`]: { body: tenantRow(oura, { enabled: false }) } }, "/console/providers/oura");
    await userEvent.click(await screen.findByRole("button", { name: "Turn off for this tenant" }));
    await confirmIn("Turn off");
    await waitFor(() => {
      expect(api.callsTo(`PATCH ${path}`)).toHaveLength(1);
    });
    expect(api.callsTo(`PATCH ${path}`)[0]?.body).toEqual({ enabled: false });
    expect(api.callsTo("PATCH /v1/admin/providers/oura")).toHaveLength(0);
    expect(api.callsTo(`GET /v1/admin/tenants/${TENANT}/providers`).length).toBeGreaterThan(0);
  });

  it("saves keys, sending only what changed, and never fills a secret", async () => {
    const { api } = view({ "PATCH /v1/admin/providers/oura": { body: oura } }, "/console/providers/oura");
    const id = await screen.findByLabelText("CLIENT_ID");
    const secret = screen.getByLabelText("CLIENT_SECRET");
    expect(id).toHaveValue("abc123");
    expect(secret).toHaveValue("");
    expect(screen.getByText("Set")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save keys" })).toBeDisabled();
    await userEvent.type(secret, "s3cret");
    const fields = [
      { name: "CLIENT_ID", secret: false, set: true, value: "abc123", input: "abc123", clear: false },
      { name: "CLIENT_SECRET", secret: true, set: true, value: null, input: "s3cret", clear: false },
    ];
    await userEvent.click(screen.getByRole("button", { name: "Save keys" }));
    await confirmIn("Save keys");
    await waitFor(() => {
      expect(api.callsTo("PATCH /v1/admin/providers/oura")).toHaveLength(1);
    });
    expect(api.callsTo("PATCH /v1/admin/providers/oura")[0]?.body).toEqual({ keys: keysPatch(fields) });
    expect(api.callsTo("PATCH /v1/admin/providers/oura")[0]?.body).toEqual({ keys: { CLIENT_SECRET: "s3cret" } });
    await waitFor(() => {
      expect(screen.getByLabelText("CLIENT_SECRET")).toHaveValue("");
    });
  });

  it("shows Not set for a secret that has no value", async () => {
    const fresh = provider("oura", {
      status: "not_configured",
      keys: [{ name: "CLIENT_SECRET", env: "OHS_OURA_CLIENT_SECRET", required: true, secret: true, set: false, value: null }],
    });
    view({ "GET /v1/admin/providers/oura": { body: fresh } }, "/console/providers/oura");
    expect(await screen.findByText("Not set")).toBeInTheDocument();
    expect(screen.getByLabelText("CLIENT_SECRET")).toHaveValue("");
  });

  it("keys set by the environment are locked with the reason", async () => {
    const env = provider("oura", { source: "env" });
    const { api } = view({ "GET /v1/admin/providers/oura": { body: env } }, "/console/providers/oura");
    expect(await screen.findByText("Set by the environment. Unset its OHS_ variables and restart to edit them here.")).toBeInTheDocument();
    expect(screen.getByLabelText("CLIENT_ID")).toBeDisabled();
    expect(screen.getByLabelText("CLIENT_SECRET")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save keys" })).toBeNull();
    expect(api.callsTo("PATCH /v1/admin/providers/oura")).toHaveLength(0);
  });

  it("shows the API message when the server answers 409", async () => {
    view(
      {
        "PATCH /v1/admin/providers/oura": { status: 409, body: { title: "Conflict", status: 409, detail: "keys come from environment variables" } },
      },
      "/console/providers/oura",
    );
    await userEvent.type(await screen.findByLabelText("CLIENT_SECRET"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Save keys" }));
    const dialog = await confirmIn("Save keys");
    expect(await within(dialog).findByText("keys come from environment variables")).toBeInTheDocument();
  });

  it("a refused Configure shows the connector's message and changes nothing", async () => {
    const { api } = view(
      {
        "PATCH /v1/admin/providers/oura": { status: 422, body: { title: "Unprocessable", status: 422, detail: "client id is not accepted by Oura" } },
      },
      "/console/providers/oura",
    );
    await userEvent.type(await screen.findByLabelText("CLIENT_SECRET"), "bad");
    await userEvent.click(screen.getByRole("button", { name: "Save keys" }));
    const dialog = await confirmIn("Save keys");
    expect(await within(dialog).findByText("client id is not accepted by Oura")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(api.callsTo("PATCH /v1/admin/providers/oura")).toHaveLength(1);
    expect(screen.getByLabelText("CLIENT_SECRET", { selector: "input" })).toHaveValue("bad");
  });

  it("forget needs the slug typed", async () => {
    const { api } = view({ "DELETE /v1/admin/providers/oura": { body: { ...oura, updated_at: null } } }, "/console/providers/oura");
    await userEvent.click(await screen.findByRole("button", { name: "Forget saved settings" }));
    const dialog = await screen.findByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Forget settings" });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText(/Type/), "oura");
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);
    await waitFor(() => {
      expect(api.callsTo("DELETE /v1/admin/providers/oura")).toHaveLength(1);
    });
  });

  it("shows the URLs to register, each with Copy, and the sync schedule", async () => {
    view({}, "/console/providers/oura");
    expect(await screen.findByText("https://ohs.example.com/v1/oauth/oura/callback")).toBeInTheDocument();
    expect(screen.getByText("https://ohs.example.com/v1/webhooks/oura")).toBeInTheDocument();
    expect(within(screen.getByRole("main")).getAllByRole("button", { name: /Copy/ })).toHaveLength(2);
    expect(screen.getByRole("heading", { name: "Sync schedule" })).toBeInTheDocument();
    expect(screen.getByText("Pulled while notifications arrive")).toBeInTheDocument();
  });

  it("a read only session sees no write button", async () => {
    view({}, "/console/providers/oura", adminReadOnly);
    await screen.findByRole("heading", { name: "Oura", level: 1 });
    await screen.findByText("Oura is on for Acme.");
    expect(screen.queryByRole("button", { name: /Turn off/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Save keys" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Forget saved settings" })).toBeNull();
  });
});
