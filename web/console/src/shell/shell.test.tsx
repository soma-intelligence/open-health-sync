import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";

import type { ConsoleSession } from "../api/types";
import { fakeApi } from "../test/fake-api";
import readOnlyJson from "../test/fixtures/session-read-only.json";
import sessionJson from "../test/fixtures/session.json";
import { renderWithApp } from "../test/render";
import { SESSION_KEY } from "../session/SessionProvider";
import { AppShell } from "./AppShell";

const tenantSession = sessionJson as ConsoleSession;
const readOnlySession = readOnlyJson as ConsoleSession;
const ROOT = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const CLINIC = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const adminSession: ConsoleSession = {
  ...tenantSession,
  tenant: { id: ROOT, name: "Root", admin: true },
  acting_tenant: { id: ROOT, name: "Root", admin: true },
};
const tenants = {
  data: [
    { id: ROOT, name: "Root", admin: true, created_at: "2030-01-01T00:00:00Z", active_keys: 1 },
    { id: CLINIC, name: "Clinic B", admin: false, created_at: "2030-01-02T00:00:00Z", active_keys: 2 },
  ],
};

function adminApi() {
  const { csrf_token: _gone, ...answer } = { ...adminSession, acting_tenant: { id: CLINIC, name: "Clinic B", admin: false } };
  void _gone;
  return fakeApi({
    "GET /v1/admin/tenants": { body: tenants },
    "PUT /console/session/tenant": { body: answer },
  });
}

const nav = () => screen.getByRole("navigation", { name: "Console" });

describe("shell", () => {
  afterEach(() => {
    Reflect.deleteProperty(window, "matchMedia");
    window.localStorage.clear();
  });

  it("shows the tenant, the pages and sign out", () => {
    renderWithApp(null, { session: tenantSession });
    const n = within(nav());
    expect(n.getByText("Acme")).toBeInTheDocument();
    expect(n.getByText("Tenant")).toBeInTheDocument();
    const headings = n.getAllByRole("heading").map((h) => h.textContent);
    expect(headings).toEqual(["Workspace", "Build"]);
    ["Overview", "Users", "Providers", "Jobs", "Webhooks", "API keys", "Audit log"].forEach((name) => {
      expect(n.getByRole("link", { name })).toBeInTheDocument();
    });
    ["Light", "Dark", "Auto"].forEach((name) => {
      expect(n.getByRole("button", { name })).toBeInTheDocument();
    });
    expect(n.getByRole("button", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Search or jump to/ })).toBeInTheDocument();
  });

  it("shows the Instance group with Settings to an admin", () => {
    renderWithApp(null, { session: adminSession, api: adminApi() });
    const n = within(nav());
    expect(n.getAllByRole("heading").map((h) => h.textContent)).toEqual(["Workspace", "Build", "Instance"]);
    expect(n.getByRole("link", { name: "Settings" })).toBeInTheDocument();
  });

  it("hides Settings from a tenant that is not an admin", () => {
    renderWithApp(null, { session: tenantSession });
    expect(screen.queryByRole("link", { name: "Settings" })).not.toBeInTheDocument();
    expect(screen.queryByText("Instance")).not.toBeInTheDocument();
  });

  it("marks a read only session", () => {
    renderWithApp(null, { session: readOnlySession });
    expect(within(nav()).getByText("Read only")).toBeInTheDocument();
    // And says above the page why the write buttons are missing.
    expect(screen.getByRole("note")).toHaveTextContent("This console is read only");
  });

  it("shows no read only notice to a session that may write", () => {
    renderWithApp(null);
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });

  it("lists tenants for an admin and switches", async () => {
    const api = adminApi();
    renderWithApp(null, { session: adminSession, api });
    expect(await screen.findByRole("option", { name: "Clinic B" })).toBeInTheDocument();
    expect(api.callsTo("GET /v1/admin/tenants")).toHaveLength(1);
    await userEvent.setup().selectOptions(screen.getByLabelText("Acting tenant"), "Clinic B");
    await waitFor(() => {
      expect(api.callsTo("PUT /console/session/tenant")).toHaveLength(1);
    });
    expect(api.callsTo("PUT /console/session/tenant")[0]?.body).toEqual({ tenant_id: CLINIC });
  });

  it("switching tenant clears every cached query and opens the overview", async () => {
    const api = adminApi();
    const view = renderWithApp(null, { session: adminSession, api, route: "/console/providers" });
    view.queryClient.setQueryData(["users"], [{ id: "u1" }]);
    await screen.findByRole("option", { name: "Clinic B" });
    await userEvent.setup().selectOptions(screen.getByLabelText("Acting tenant"), "Clinic B");
    expect(await within(nav()).findByText("Acting as Clinic B")).toBeInTheDocument();
    expect(view.path()).toBe("/console/overview");
    const keys = view.queryClient
      .getQueryCache()
      .getAll()
      .map((q) => q.queryKey[0]);
    // The overview is open now and loads the new tenant's own data; the old users are gone.
    expect(keys).toContain("session");
    expect(view.queryClient.getQueryData(["users"])).toBeUndefined();
  });

  it("has no switcher for a tenant that is not an admin", () => {
    const api = fakeApi({});
    renderWithApp(null, { session: tenantSession, api });
    expect(screen.queryByLabelText("Acting tenant")).not.toBeInTheDocument();
    expect(api.callsTo("GET /v1/admin/tenants")).toHaveLength(0);
  });

  it("a read only admin session can still switch tenant", async () => {
    const api = adminApi();
    renderWithApp(null, { session: { ...adminSession, read_only: true }, api });
    await screen.findByRole("option", { name: "Clinic B" });
    await userEvent.setup().selectOptions(screen.getByLabelText("Acting tenant"), "Clinic B");
    expect(await within(nav()).findByText("Acting as Clinic B")).toBeInTheDocument();
    expect(api.callsTo("PUT /console/session/tenant")).toHaveLength(1);
  });

  it("the PUT answer keeps the current CSRF token", async () => {
    const api = adminApi();
    const view = renderWithApp(null, { session: adminSession, api });
    await screen.findByRole("option", { name: "Clinic B" });
    await userEvent.setup().selectOptions(screen.getByLabelText("Acting tenant"), "Clinic B");
    await within(nav()).findByText("Acting as Clinic B");
    const kept = view.queryClient.getQueryData<ConsoleSession>(SESSION_KEY);
    expect(kept?.acting_tenant.name).toBe("Clinic B");
    expect(kept?.csrf_token).toBe(adminSession.csrf_token);
  });

  it("an exception in a page shows an error panel for that page only", async () => {
    function Boom(): never {
      throw new Error("kaboom");
    }
    const page = (
      <Routes>
        <Route element={<AppShell />}>
          <Route path="boom" element={<Boom />} />
          <Route path="overview" element={<p>fine</p>} />
        </Route>
      </Routes>
    );
    const err = vi.spyOn(globalThis.console, "error").mockImplementation(() => undefined);
    const view = renderWithApp(page, { session: tenantSession, route: "/console/boom" });
    expect(await screen.findByText("Something went wrong on this page")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload page" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.getByText("kaboom")).toBeInTheDocument();
    expect(nav()).toBeInTheDocument();
    await userEvent.setup().click(within(nav()).getByRole("link", { name: "Overview" }));
    expect(await screen.findByText("fine")).toBeInTheDocument();
    expect(screen.queryByText("Something went wrong on this page")).not.toBeInTheDocument();
    expect(view.path()).toBe("/console/overview");
    err.mockRestore();
  });

  it("folds the navigation into a menu under 1024 px", async () => {
    window.matchMedia = ((query: string) =>
      ({
        matches: query === "(max-width: 1023px)",
        media: query,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      }) as unknown as MediaQueryList) as typeof window.matchMedia;
    renderWithApp(null, { session: tenantSession });
    const user = userEvent.setup();
    expect(screen.queryByRole("navigation", { name: "Console" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Menu" }));
    expect(nav()).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("navigation", { name: "Console" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Menu" }));
    await user.click(within(nav()).getByRole("link", { name: "Users" }));
    expect(screen.queryByRole("navigation", { name: "Console" })).not.toBeInTheDocument();
  });

  it("unknown console path shows Not found inside the shell", () => {
    renderWithApp(null, { session: tenantSession, route: "/console/nowhere" });
    expect(screen.getByRole("heading", { name: "Page not found", level: 1 })).toBeInTheDocument();
    expect(nav()).toBeInTheDocument();
  });

  it("opens the inspector on Overview, and the operator's toggle holds for the session", async () => {
    renderWithApp(null, { session: tenantSession, route: "/console/overview" });
    const user = userEvent.setup();
    expect(screen.getByRole("complementary", { name: "Inspector" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Inspector" }));
    expect(screen.queryByRole("complementary", { name: "Inspector" })).not.toBeInTheDocument();
    await user.click(within(nav()).getByRole("link", { name: "Users" }));
    expect(screen.queryByRole("complementary", { name: "Inspector" })).not.toBeInTheDocument();
    await user.click(within(nav()).getByRole("link", { name: "Overview" }));
    expect(screen.queryByRole("complementary", { name: "Inspector" }), "the toggle wins over the page default").not.toBeInTheDocument();
    expect(window.localStorage.getItem("ohs.console.inspector"), "nothing outlives the session").toBeNull();
  });

  it("keeps the inspector closed on list pages until the operator opens it", async () => {
    renderWithApp(null, { session: tenantSession, route: "/console/users" });
    const user = userEvent.setup();
    expect(screen.queryByRole("complementary", { name: "Inspector" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Inspector" }));
    expect(screen.getByRole("complementary", { name: "Inspector" })).toBeInTheDocument();
    await user.click(within(nav()).getByRole("link", { name: "Jobs" }));
    expect(screen.getByRole("complementary", { name: "Inspector" })).toBeInTheDocument();
  });
});
