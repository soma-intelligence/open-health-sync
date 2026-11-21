import { screen, waitFor, within } from "@testing-library/react";
import type { QueryClient } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";

import type { APIKey, ConsoleSession } from "../../api/types";
import { fakeApi, type FakeApi, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import readOnlyJson from "../../test/fixtures/session-read-only.json";
import { renderWithApp } from "../../test/render";

const session = sessionJson as ConsoleSession;
const readOnly = readOnlyJson as ConsoleSession;

const own: APIKey = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  prefix: "ohs_sk_AbCdE",
  created_at: "2026-09-01T08:00:00Z",
  last_used_at: "2026-10-02T08:00:00Z",
  revoked_at: null,
  expires_at: null,
  scopes: ["*"],
  current: true,
};
const ci: APIKey = {
  ...own,
  id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  prefix: "ohs_sk_Zz9Yy",
  last_used_at: null,
  expires_at: "2030-02-01T00:00:00Z",
  scopes: ["users:read", "data:read"],
  current: false,
};
const old: APIKey = { ...ci, id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", prefix: "ohs_sk_Old11", revoked_at: "2026-09-20T08:00:00Z", expires_at: null, scopes: ["*"] };

const loaded: Record<string, FakeHandler> = { "GET /v1/keys": { body: { data: [own, ci, old] } } };

function view(routes: Record<string, FakeHandler>, s: ConsoleSession = session) {
  const api: FakeApi = fakeApi({ ...loaded, ...routes });
  return renderWithApp(null, { session: s, api, route: "/console/keys" });
}

function mutationsHold(queryClient: QueryClient, secret: string): boolean {
  return queryClient
    .getMutationCache()
    .getAll()
    .some((m) => JSON.stringify(m.state.data ?? null).includes(secret));
}

describe("keys", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("lists keys with prefix, scopes, created, last used, expires, revoked", async () => {
    view({});
    expect(await screen.findByRole("heading", { name: "API keys", level: 1 })).toBeInTheDocument();
    expect(screen.getByText(/Keys of Acme\./)).toBeInTheDocument();
    const mine = await screen.findByRole("row", { name: /ohs_sk_AbCdE/ });
    expect(within(mine).getByText("This session")).toBeInTheDocument();
    expect(within(mine).getByText("Everything")).toBeInTheDocument();
    expect(within(mine).queryByRole("button", { name: /Revoke/ })).not.toBeInTheDocument();
    const row = screen.getByRole("row", { name: /ohs_sk_Zz9Yy/ });
    expect(within(row).getByText("users:read, data:read")).toBeInTheDocument();
    expect(within(row).getByText("Never")).toBeInTheDocument();
    expect(within(row).getByText("Active")).toBeInTheDocument();
    expect(within(row).getByText(/2030-02-01/)).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: "Revoke" })).toBeInTheDocument();
    const gone = screen.getByRole("row", { name: /ohs_sk_Old11/ });
    expect(within(gone).getByText("Revoked")).toBeInTheDocument();
    expect(within(gone).queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
  });

  it("names the acting tenant, whose keys /v1/keys lists", async () => {
    const acting: ConsoleSession = { ...session, acting_tenant: { id: "22222222-2222-4222-8222-222222222222", name: "Beta", admin: false } };
    view({}, acting);
    expect(await screen.findByText(/Keys of Beta\./)).toBeInTheDocument();
    expect(screen.queryByText(/Keys of Acme\./)).not.toBeInTheDocument();
  });

  it("creates a key with chosen scopes and an optional expiry, shown once", async () => {
    const key = "ohs_sk_Zz9YyQ1w2e3r4t5y6u7i8o9p0aSdFgHj";
    const { api, queryClient } = view({ "POST /v1/keys": { status: 201, body: { ...ci, key } } });
    await userEvent.click(await screen.findByRole("button", { name: "New API key" }));
    const ask = screen.getByRole("dialog", { name: "New API key" });
    await userEvent.click(within(ask).getByRole("radio", { name: /Pick scopes/ }));
    await userEvent.click(within(ask).getByRole("checkbox", { name: "users:read" }));
    await userEvent.click(within(ask).getByRole("checkbox", { name: "data:read" }));
    await userEvent.type(within(ask).getByLabelText("Expires on (optional)"), "2027-01-01");
    expect(api.callsTo("POST /v1/keys")).toHaveLength(0);
    await userEvent.click(within(ask).getByRole("button", { name: "Create key" }));
    const dialog = await screen.findByRole("dialog", { name: "Your new API key" });
    expect(within(dialog).getByDisplayValue(key)).toBeInTheDocument();
    await waitFor(() => {
      expect(mutationsHold(queryClient, key)).toBe(false);
    });
    expect(api.callsTo("POST /v1/keys")[0]?.body).toEqual({ scopes: ["users:read", "data:read"], expires_at: "2027-01-01T00:00:00Z" });
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "I have stored it somewhere safe" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => {
      expect(screen.queryByDisplayValue(key)).not.toBeInTheDocument();
    });
    expect(document.body.textContent).not.toContain(key);
    expect(mutationsHold(queryClient, key)).toBe(false);
  });

  it("defaults to read only scopes and never sends an empty list", async () => {
    const { api } = view({ "POST /v1/keys": { status: 201, body: { ...ci, key: "ohs_sk_x" } } });
    await userEvent.click(await screen.findByRole("button", { name: "New API key" }));
    const ask = screen.getByRole("dialog");
    expect(within(ask).getByRole("radio", { name: /Read only/ })).toBeChecked();
    await userEvent.click(within(ask).getByRole("button", { name: "Create key" }));
    await screen.findByRole("dialog", { name: "Your new API key" });
    expect(api.callsTo("POST /v1/keys")[0]?.body).toEqual({
      scopes: ["tenant:read", "users:read", "data:read", "connections:read", "webhooks:read", "exports:read", "keys:read", "audit:read"],
    });
  });

  it("full access is an explicit choice that sends *", async () => {
    const { api } = view({ "POST /v1/keys": { status: 201, body: { ...own, current: false, key: "ohs_sk_x" } } });
    await userEvent.click(await screen.findByRole("button", { name: "New API key" }));
    const ask = screen.getByRole("dialog");
    await userEvent.click(within(ask).getByRole("radio", { name: /Full access/ }));
    await userEvent.click(within(ask).getByRole("button", { name: "Create key" }));
    await screen.findByRole("dialog", { name: "Your new API key" });
    expect(api.callsTo("POST /v1/keys")[0]?.body).toEqual({ scopes: ["*"] });
  });

  it("picking scopes with none checked is refused before any call", async () => {
    const { api } = view({ "POST /v1/keys": { status: 201, body: { ...ci, key: "ohs_sk_x" } } });
    await userEvent.click(await screen.findByRole("button", { name: "New API key" }));
    const ask = screen.getByRole("dialog");
    await userEvent.click(within(ask).getByRole("radio", { name: /Pick scopes/ }));
    await userEvent.click(within(ask).getByRole("button", { name: "Create key" }));
    expect(await within(ask).findByText("Pick at least one scope.")).toBeInTheDocument();
    expect(api.callsTo("POST /v1/keys")).toHaveLength(0);
  });

  it("shows a write scope's read as implied, and disables scopes the session lacks", async () => {
    const narrow: ConsoleSession = { ...session, key_scopes: ["users:write", "data:read"] };
    view({}, narrow);
    await userEvent.click(await screen.findByRole("button", { name: "New API key" }));
    const ask = screen.getByRole("dialog");
    await userEvent.click(within(ask).getByRole("radio", { name: /Pick scopes/ }));
    expect(within(ask).getByRole("checkbox", { name: "audit:read" })).toBeDisabled();
    expect(within(ask).getByRole("checkbox", { name: "All scopes (*)" })).toBeDisabled();
    expect(within(ask).getByRole("checkbox", { name: "users:read" })).toBeEnabled();
    await userEvent.click(within(ask).getByRole("checkbox", { name: "users:write" }));
    expect(within(ask).getByRole("checkbox", { name: "users:read" })).toBeChecked();
    expect(within(ask).getByRole("checkbox", { name: "users:read" })).toBeDisabled();
  });

  it("a 403 naming a missing scope is shown", async () => {
    view({ "POST /v1/keys": { status: 403, body: { title: "Forbidden", status: 403, detail: "this key lacks the scope keys:write" } } });
    await userEvent.click(await screen.findByRole("button", { name: "New API key" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Create key" }));
    expect(await screen.findByText("this key lacks the scope keys:write")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "New API key" })).toBeInTheDocument();
  });

  it("revoke needs the prefix typed", async () => {
    const { api } = view({ [`DELETE /v1/keys/${ci.id}`]: { status: 204 } });
    const row = await screen.findByRole("row", { name: /ohs_sk_Zz9Yy/ });
    await userEvent.click(within(row).getByRole("button", { name: "Revoke" }));
    const ask = screen.getByRole("dialog", { name: "Revoke this key?" });
    const go = within(ask).getByRole("button", { name: "Revoke key" });
    expect(go).toBeDisabled();
    await userEvent.type(within(ask).getByLabelText(/Type/), "ohs_sk_Zz9Yy");
    expect(go).toBeEnabled();
    await userEvent.click(go);
    await waitFor(() => {
      expect(api.callsTo(`DELETE /v1/keys/${ci.id}`)).toHaveLength(1);
    });
  });

  it("a read only session sees no action", async () => {
    view({}, readOnly);
    await screen.findByRole("row", { name: /ohs_sk_Zz9Yy/ });
    expect(screen.queryByRole("button", { name: "Create key" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
  });
});
