import { useQuery } from "@tanstack/react-query";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { useApi, useCanWrite, useIsAdmin, useSession } from "./SessionProvider";
import type { ConsoleSession } from "../api/types";
import { fakeApi } from "../test/fake-api";
import readOnlyJson from "../test/fixtures/session-read-only.json";
import sessionJson from "../test/fixtures/session.json";
import { renderWithApp } from "../test/render";

const session = sessionJson as ConsoleSession;
const readOnlySession = readOnlyJson as ConsoleSession;
const KEY = "ohs_sk_0123456789abcdefABCD";

const signedOutApi = () => fakeApi({ "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } } });

async function typeKey(key: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText("Tenant API key"), key);
  await user.click(screen.getByRole("button", { name: "Sign in" }));
}

function Where() {
  const { session: s, status } = useSession();
  return <p>{`${status}:${s?.tenant.name ?? "-"}:${String(useCanWrite())}:${String(useIsAdmin())}`}</p>;
}

describe("session", () => {
  it("signs in, holds no key, and lands where it was sent from", async () => {
    const api = fakeApi({
      "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
      "POST /console/session": { status: 201, body: session },
    });
    const view = renderWithApp(null, { session: null, route: "/console/users", api });
    expect(view.path()).toBe("/console/sign-in");
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    await typeKey(KEY);
    await waitFor(() => {
      expect(view.path()).toBe("/console/users");
    });
    expect(api.callsTo("POST /console/session")[0]?.body).toEqual({ key: KEY });
    expect(JSON.stringify(window.localStorage)).not.toContain(KEY);
    expect(JSON.stringify(window.sessionStorage)).not.toContain(KEY);
    expect(document.body.textContent).not.toContain(KEY);
  });

  it("refuses a malformed key before any request", async () => {
    const api = signedOutApi();
    renderWithApp(null, { session: null, route: "/console/sign-in", api });
    await typeKey("abc");
    expect(await screen.findByText("That is not a tenant API key.")).toBeInTheDocument();
    expect(api.callsTo("POST /console/session")).toHaveLength(0);
  });

  it("shows the API message on a refused key", async () => {
    const api = fakeApi({
      "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
      "POST /console/session": { status: 401, body: { title: "Unauthorized", detail: "invalid API key" } },
    });
    renderWithApp(null, { session: null, route: "/console/sign-in", api });
    await typeKey(KEY);
    expect(await screen.findByText("invalid API key")).toBeInTheDocument();
    expect(screen.getByLabelText("Tenant API key")).toHaveValue("");
  });

  it("shows why sign in over plain http is refused", async () => {
    const detail = "the console signs in over https, or on localhost; set OHS_PUBLIC_URL to the https address";
    const api = fakeApi({
      "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
      "POST /console/session": { status: 403, body: { title: "Forbidden", detail } },
    });
    const view = renderWithApp(null, { session: null, route: "/console/sign-in", api });
    await typeKey(KEY);
    expect(await screen.findByText(detail)).toBeInTheDocument();
    expect(api.callsTo("POST /console/session")).toHaveLength(1);
    expect(view.path()).toBe("/console/sign-in");
    expect(view.queryClient.getQueryData(["session"])).toBeNull();
  });

  it("links to setup when the instance has no tenant", async () => {
    const api = fakeApi({ "GET /v1/setup": { body: { needed: true } } });
    renderWithApp(null, { session: null, route: "/console/sign-in", api });
    const link = await screen.findByRole("link", { name: "First run: create the first tenant" });
    expect(link).toHaveAttribute("href", "/console/setup");
  });

  it("sign in and setup share one setup state, with no wrong-state flash", async () => {
    const api = fakeApi({ "GET /v1/setup": { body: { needed: true } } });
    const view = renderWithApp(null, { session: null, route: "/console/sign-in", api });
    await screen.findByRole("link", { name: "First run: create the first tenant" });
    expect(view.queryClient.getQueryData(["setup"])).toBe("needed");
    await act(() => view.go("/setup"));
    expect(screen.getByLabelText("Setup token")).toBeInTheDocument();
    await act(() => view.go("/sign-in"));
    expect(screen.getByRole("link", { name: "First run: create the first tenant" })).toBeInTheDocument();
  });

  it("an instance without setup: sign in then setup shows off, never an error", async () => {
    const api = signedOutApi();
    const view = renderWithApp(null, { session: null, route: "/console/sign-in", api });
    await waitFor(() => {
      expect(view.queryClient.getQueryData(["setup"])).toBe("off");
    });
    expect(screen.queryByRole("link", { name: "First run: create the first tenant" })).not.toBeInTheDocument();
    await act(() => view.go("/setup"));
    expect(screen.queryByText("Cannot reach this instance.")).not.toBeInTheDocument();
    expect(screen.getByText("First-run setup is off on this instance.")).toBeInTheDocument();
  });

  it("a reload restores the session from the cookie", async () => {
    const api = fakeApi({ "GET /console/session": { body: session } });
    const view = renderWithApp(null, { route: "/console/users", api });
    await waitFor(() => {
      expect(api.callsTo("GET /console/session")).toHaveLength(1);
    });
    await waitFor(() => {
      expect(screen.queryByRole("heading", { name: "Sign in" })).not.toBeInTheDocument();
    });
    expect(view.path()).toBe("/console/users");
    expect(screen.queryByLabelText("Tenant API key")).not.toBeInTheDocument();
  });

  it("a lost cookie on reload shows sign in", async () => {
    const api = fakeApi({
      "GET /console/session": { status: 401, body: { title: "Unauthorized", detail: "no session" } },
      "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
    });
    const view = renderWithApp(null, { route: "/console/users", api });
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(view.path()).toBe("/console/sign-in");
  });

  it("sign out deletes the session and returns to sign in", async () => {
    const api = fakeApi({
      "DELETE /console/session": { status: 204 },
      "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
    });
    function Out() {
      const { signOut } = useSession();
      return (
        <button
          type="button"
          onClick={() => {
            void signOut();
          }}
        >
          Out
        </button>
      );
    }
    const view = renderWithApp(<Out />, { session, route: "/console/users", api });
    view.queryClient.setQueryData(["users"], [1]);
    await userEvent.setup().click(screen.getByRole("button", { name: "Out" }));
    await waitFor(() => {
      expect(view.path()).toBe("/console/sign-in");
    });
    expect(api.callsTo("DELETE /console/session")).toHaveLength(1);
    expect(api.callsTo("DELETE /console/session")[0]?.headers["x-ohs-csrf"]).toBe(session.csrf_token);
    expect(view.queryClient.getQueryData(["users"])).toBeUndefined();
    expect(view.queryClient.getQueryData(["session"])).toBeNull();
  });

  it("a 401 mid-visit sends to sign in and back", async () => {
    const api = fakeApi({
      "GET /v1/users/u1": { status: 401, body: { title: "Unauthorized", detail: "session ended" } },
      "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
      "POST /console/session": { status: 201, body: session },
    });
    function Probe() {
      const client = useApi();
      useQuery({ queryKey: ["user", "u1"], queryFn: () => client.get("/v1/users/u1") });
      return <p>probe</p>;
    }
    const view = renderWithApp(<Probe />, { session, route: "/console/users/u1", api });
    await waitFor(() => {
      expect(view.path()).toBe("/console/sign-in");
    });
    await typeKey(KEY);
    await waitFor(() => {
      expect(view.path()).toBe("/console/users/u1");
    });
  });

  it("a 401 drops every cached query but the session", async () => {
    const api = fakeApi({
      "GET /v1/users/u1": { status: 401, body: { title: "Unauthorized", detail: "session ended" } },
      "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
    });
    function Probe() {
      const client = useApi();
      useQuery({ queryKey: ["user", "u1"], queryFn: () => client.get("/v1/users/u1") });
      return <p>probe</p>;
    }
    const view = renderWithApp(<Probe />, { session, route: "/console/users/u1", api });
    view.queryClient.setQueryData(["users"], [1]);
    await waitFor(() => {
      expect(view.path()).toBe("/console/sign-in");
    });
    expect(view.queryClient.getQueryData(["users"])).toBeUndefined();
    expect(view.queryClient.getQueryData(["user", "u1"])).toBeUndefined();
    const keys = view.queryClient
      .getQueryCache()
      .getAll()
      .map((q) => q.queryKey[0])
      .filter((k) => k !== "setup");
    expect(keys).toEqual(["session"]);
  });

  it("switches tenant, keeps the CSRF token and drops the old tenant's data", async () => {
    const other = { ...session, acting_tenant: { id: "22222222-2222-4222-8222-222222222222", name: "Beta", admin: false } };
    const { csrf_token: _dropped, ...answer } = other;
    void _dropped;
    const api = fakeApi({ "PUT /console/session/tenant": { body: answer } });
    function Switch() {
      const { switchTenant, session: s } = useSession();
      return (
        <button
          type="button"
          onClick={() => {
            void switchTenant("22222222-2222-4222-8222-222222222222");
          }}
        >
          {`Switch from ${s?.acting_tenant.name ?? ""}`}
        </button>
      );
    }
    const view = renderWithApp(<Switch />, { session, route: "/console/overview", api });
    view.queryClient.setQueryData(["users"], [1]);
    await userEvent.setup().click(screen.getByRole("button"));
    expect(await screen.findByRole("button", { name: "Switch from Beta" })).toBeInTheDocument();
    expect(api.callsTo("PUT /console/session/tenant")[0]?.body).toEqual({ tenant_id: "22222222-2222-4222-8222-222222222222" });
    const kept = view.queryClient.getQueryData<ConsoleSession>(["session"]);
    expect(kept?.csrf_token).toBe(session.csrf_token);
    expect(view.queryClient.getQueryState(["users"])?.data).toBeUndefined();
  });

  it("exposes write and admin rights", async () => {
    const admin = { ...session, tenant: { ...session.tenant, admin: true } };
    const first = renderWithApp(<Where />, { session: admin, route: "/console/overview" });
    expect(await screen.findByText("signed-in:Acme:true:true")).toBeInTheDocument();
    first.unmount();
    renderWithApp(<Where />, { session: readOnlySession, route: "/console/overview" });
    expect(await screen.findByText("signed-in:Acme:false:false")).toBeInTheDocument();
  });
});
