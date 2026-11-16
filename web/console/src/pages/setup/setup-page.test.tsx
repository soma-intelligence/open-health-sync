import { screen, waitFor } from "@testing-library/react";
import type { QueryClient } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";

import type { ConsoleSession } from "../../api/types";
import { fakeApi, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import { renderWithApp } from "../../test/render";

const session = sessionJson as ConsoleSession;
const KEY = "ohs_sk_" + "Zq9".repeat(15);
const TENANT = "0b6c1d52-3a41-4c11-9f0e-6f2a8f7d1111";

function view(routes: Record<string, FakeHandler>) {
  const api = fakeApi(routes);
  return renderWithApp(null, { session: null, api, route: "/console/setup" });
}

function mutationsHold(queryClient: QueryClient, secret: string): boolean {
  return queryClient
    .getMutationCache()
    .getAll()
    .some((m) => JSON.stringify(m.state.data ?? null).includes(secret));
}

function storageHolds(secret: string): boolean {
  for (const store of [window.localStorage, window.sessionStorage]) {
    for (let i = 0; i < store.length; i += 1) {
      const k = store.key(i) ?? "";
      if (k.includes(secret) || (store.getItem(k) ?? "").includes(secret)) return true;
    }
  }
  return document.cookie.includes(secret);
}

describe("setup page", () => {
  afterEach(() => {
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  it("setup calls are anonymous", async () => {
    const api = fakeApi({
      "GET /v1/setup": { body: { needed: true } },
      "POST /v1/setup": { status: 201, body: { tenant_id: TENANT, api_key: KEY } },
    });
    // A signed-in session holds a CSRF token, so only `anonymous` keeps it off the wire.
    renderWithApp(null, { session, api, route: "/console/setup" });
    await userEvent.type(await screen.findByLabelText("Setup token"), "  tok ");
    await userEvent.click(screen.getByRole("button", { name: "Create the first tenant" }));
    await screen.findByText(TENANT);
    expect(api.callsTo("GET /v1/setup").length).toBeGreaterThan(0);
    expect(api.callsTo("POST /v1/setup")).toHaveLength(1);
    for (const call of [...api.callsTo("GET /v1/setup"), ...api.callsTo("POST /v1/setup")]) {
      expect(call.credentials).toBe("omit");
      expect(call.headers["x-ohs-csrf"]).toBeUndefined();
      expect(call.headers.authorization).toBeUndefined();
    }
    expect(api.callsTo("POST /v1/setup")[0]?.body).toEqual({ setup_token: "tok" });
  });

  it("asks for the token and an optional name, at most 100", async () => {
    const api = fakeApi({
      "GET /v1/setup": { body: { needed: true } },
      "POST /v1/setup": { status: 201, body: { tenant_id: TENANT, api_key: KEY } },
    });
    renderWithApp(null, { session: null, api, route: "/console/setup" });
    const token = await screen.findByLabelText("Setup token");
    expect(token).toHaveAttribute("type", "password");
    const name = screen.getByLabelText(/Tenant name/);
    expect(name).toHaveAttribute("maxlength", "100");
    await userEvent.type(token, "tok");
    await userEvent.type(name, "Home");
    await userEvent.click(screen.getByRole("button", { name: "Create the first tenant" }));
    await screen.findByText(TENANT);
    expect(api.callsTo("POST /v1/setup")[0]?.body).toEqual({ setup_token: "tok", tenant_name: "Home" });
  });

  it("shows the key once and offers to sign in with it", async () => {
    const api = fakeApi({
      "GET /v1/setup": { body: { needed: true } },
      "POST /v1/setup": { status: 201, body: { tenant_id: TENANT, api_key: KEY } },
      "POST /console/session": { body: session },
      "GET /console/overview": { body: {} },
    });
    const { queryClient, path } = renderWithApp(null, { session: null, api, route: "/console/setup" });
    await userEvent.type(await screen.findByLabelText("Setup token"), "tok");
    await userEvent.click(screen.getByRole("button", { name: "Create the first tenant" }));
    expect(await screen.findByText(TENANT)).toBeInTheDocument();
    expect(screen.getByDisplayValue(KEY)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Copy/ })).toBeInTheDocument();
    expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(mutationsHold(queryClient, KEY)).toBe(false);
    });
    await userEvent.click(screen.getByRole("button", { name: "Sign in with this key" }));
    await waitFor(() => {
      expect(path()).toBe("/console/overview");
    });
    expect(api.callsTo("POST /console/session")[0]?.body).toEqual({ key: KEY });
    expect(screen.queryByDisplayValue(KEY)).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain(KEY);
    expect(storageHolds(KEY)).toBe(false);
    expect(mutationsHold(queryClient, KEY)).toBe(false);
  });

  it("a refused token says so and keeps the form", async () => {
    view({
      "GET /v1/setup": { body: { needed: true } },
      "POST /v1/setup": { status: 401, body: { title: "Unauthorized", detail: "wrong" } },
    });
    await userEvent.type(await screen.findByLabelText("Setup token"), "nope");
    await userEvent.click(screen.getByRole("button", { name: "Create the first tenant" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/setup token was refused/);
    expect(screen.getByLabelText("Setup token")).toBeInTheDocument();
  });

  it("an empty token is refused before any request", async () => {
    const { api } = view({ "GET /v1/setup": { body: { needed: true } } });
    await userEvent.click(await screen.findByRole("button", { name: "Create the first tenant" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Enter the setup token.");
    expect(api.callsTo("POST /v1/setup")).toHaveLength(0);
  });

  it("a 409 turns into the closed state", async () => {
    view({
      "GET /v1/setup": { body: { needed: true } },
      "POST /v1/setup": { status: 409, body: { title: "Conflict", detail: "set up" } },
    });
    await userEvent.type(await screen.findByLabelText("Setup token"), "tok");
    await userEvent.click(screen.getByRole("button", { name: "Create the first tenant" }));
    expect(await screen.findByText("This instance is already set up.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument();
  });

  it("closed setup says so and links to sign in", async () => {
    view({ "GET /v1/setup": { body: { needed: false } } });
    expect(await screen.findByText("This instance is already set up.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/console/sign-in");
  });

  it("an instance with the setup off says so", async () => {
    view({ "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } } });
    expect(await screen.findByText("First-run setup is off on this instance.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument();
  });
});
