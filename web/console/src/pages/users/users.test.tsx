import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";

import type { ConsoleSession, User } from "../../api/types";
import { AppShell } from "../../shell/AppShell";
import { fakeApi, type FakeApi, type FakeCall, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import readOnlyJson from "../../test/fixtures/session-read-only.json";
import { renderWithApp } from "../../test/render";
import { UsersPage } from "./UsersPage";

const session = sessionJson as ConsoleSession;
const readOnly = readOnlyJson as ConsoleSession;

function user(n: number, over: Partial<User> = {}): User {
  return {
    id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
    external_ref: `person-${String(n)}`,
    status: "active",
    created_at: "2026-10-01T08:30:00Z",
    timezone: null,
    timezone_source: null,
    ...over,
  };
}

const page1 = Array.from({ length: 50 }, (_, i) => user(i + 1));
const page2 = [user(51), user(52)];

function view(routes: Record<string, FakeHandler>, s: ConsoleSession = session) {
  const api: FakeApi = fakeApi(routes);
  const ui = (
    <Routes>
      <Route element={<AppShell />}>
        <Route path="users" element={<UsersPage />} />
        <Route path="users/:id" element={<p>user page</p>} />
      </Route>
    </Routes>
  );
  return renderWithApp(ui, { session: s, api, route: "/console/users" });
}

describe("users", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("lists 50 users per page with a pager", async () => {
    const { api } = view({
      "GET /v1/users": (c: FakeCall) =>
        c.path.includes("cursor=cur-2") ? { body: { data: page2, next_cursor: null } } : { body: { data: page1, next_cursor: "cur-2" } },
    });
    expect(await screen.findByRole("heading", { name: "Users", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("People whose devices and accounts feed this tenant.")).toBeInTheDocument();
    expect(await screen.findByText("person-1")).toBeInTheDocument();
    expect(api.calls.find((c) => c.path.startsWith("/v1/users"))?.path).toBe("/v1/users?limit=50");
    expect(screen.getAllByRole("row")).toHaveLength(51);
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("person-51")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === "/v1/users?cursor=cur-2&limit=50")).toBe(true);
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();

    await userEvent.click(screen.getByRole("button", { name: "Previous" }));
    expect(await screen.findByText("person-1")).toBeInTheDocument();
  });

  it("searches by external id", async () => {
    const { api } = view({
      "GET /v1/users": (c: FakeCall) => {
        if (c.path.includes("external_ref=demo-user")) return { body: { data: [user(7, { external_ref: "demo-user" })], next_cursor: null } };
        if (c.path.includes("external_ref=nobody")) return { body: { data: [], next_cursor: null } };
        return { body: { data: page1, next_cursor: null } };
      },
    });
    await screen.findByText("person-1");
    await userEvent.type(screen.getByRole("searchbox", { name: "External id" }), "demo-user{Enter}");
    expect(await screen.findByText("demo-user")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === "/v1/users?external_ref=demo-user&limit=50")).toBe(true);
    expect(screen.getAllByRole("row")).toHaveLength(2);

    await userEvent.clear(screen.getByRole("searchbox", { name: "External id" }));
    await userEvent.type(screen.getByRole("searchbox", { name: "External id" }), "nobody{Enter}");
    expect(await screen.findByText("No user with external id nobody")).toBeInTheDocument();
  });

  it("shows No user with external id demo-user when nothing matches", async () => {
    view({
      "GET /v1/users": (c: FakeCall) =>
        c.path.includes("external_ref=") ? { body: { data: [], next_cursor: null } } : { body: { data: page2, next_cursor: null } },
    });
    await screen.findByText("person-51");
    await userEvent.type(screen.getByRole("searchbox", { name: "External id" }), "demo-user{Enter}");
    expect(await screen.findByText("No user with external id demo-user")).toBeInTheDocument();
  });

  it("creates a user after a confirmation", async () => {
    const created = user(99, { external_ref: "p-17" });
    const { api, path } = view({
      "GET /v1/users": { body: { data: page2, next_cursor: null } },
      "POST /v1/users": { status: 201, body: created },
    });
    await screen.findByText("person-51");
    await userEvent.click(screen.getByRole("button", { name: "New user" }));
    const dialog = screen.getByRole("dialog", { name: "New user" });
    await userEvent.type(within(dialog).getByLabelText("External id"), "p-17");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create user" }));
    await waitFor(() => {
      expect(path()).toBe(`/console/users/${created.id}`);
    });
    expect(api.callsTo("POST /v1/users")[0]?.body).toEqual({ external_ref: "p-17" });
  });

  it("sends an empty body when the external id is empty", async () => {
    const created = user(98, { external_ref: null });
    const { api } = view({
      "GET /v1/users": { body: { data: page2, next_cursor: null } },
      "POST /v1/users": { status: 201, body: created },
    });
    await screen.findByText("person-51");
    await userEvent.click(screen.getByRole("button", { name: "New user" }));
    await userEvent.click(within(screen.getByRole("dialog", { name: "New user" })).getByRole("button", { name: "Create user" }));
    await waitFor(() => {
      expect(api.callsTo("POST /v1/users")).toHaveLength(1);
    });
    expect(api.callsTo("POST /v1/users")[0]?.body).toEqual({});
  });

  it("a read only session sees no New user button", async () => {
    view({ "GET /v1/users": { body: { data: page2, next_cursor: null } } }, readOnly);
    await screen.findByText("person-51");
    expect(screen.queryByRole("button", { name: "New user" })).not.toBeInTheDocument();
  });

  it("the first cell of a row is a link to the user", async () => {
    view({ "GET /v1/users": { body: { data: page2, next_cursor: null } } });
    const link = (await screen.findByText("person-52")).closest("a");
    expect(link).toHaveAttribute("href", `/console/users/${(page2[1] as User).id}`);
  });

  it("selecting a row inspects GET /v1/users/{id}", async () => {
    view({ "GET /v1/users": { body: { data: page2, next_cursor: null } } });
    // The first cell is the link that opens the user: another cell only selects the row.
    const row = (await screen.findByText("person-52")).closest("tr") as HTMLTableRowElement;
    const cells = within(row).getAllByRole("cell");
    await userEvent.click(cells[cells.length - 1] as HTMLElement);
    expect(row).toHaveAttribute("aria-current", "true");
    const id = (page2[1] as User).id;
    const pane = await screen.findByRole("complementary", { name: "Inspector" });
    expect(await within(pane).findByText(`/v1/users/${id}`)).toBeInTheDocument();
  });
});
