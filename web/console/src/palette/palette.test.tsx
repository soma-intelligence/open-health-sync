import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router";

import type { ConsoleSession } from "../api/types";
import { UsersPage } from "../pages/users/UsersPage";
import { AppShell } from "../shell/AppShell";
import { fakeApi } from "../test/fake-api";
import sessionJson from "../test/fixtures/session.json";
import readOnlyJson from "../test/fixtures/session-read-only.json";
import { renderWithApp } from "../test/render";

const session = sessionJson as ConsoleSession;
const readOnly = readOnlyJson as ConsoleSession;

function Where() {
  const loc = useLocation();
  return <p data-testid="where">{`${loc.pathname}|${loc.state === null ? "none" : JSON.stringify(loc.state)}`}</p>;
}

function view(s: ConsoleSession = session, route = "/console/jobs") {
  const api = fakeApi({ "GET /v1/users": { body: { data: [], next_cursor: null } } });
  return renderWithApp(
    <Routes>
      <Route element={<AppShell />}>
        <Route path="jobs" element={<><h1>Jobs page</h1><Where /></>} />
        <Route path="keys" element={<><h1>Keys page</h1><Where /></>} />
        <Route path="users" element={<UsersPage />} />
      </Route>
    </Routes>,
    { session: s, api, route },
  );
}

const dialog = () => screen.queryByRole("dialog", { name: "Command palette" });

describe("command palette", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("opens with Ctrl+K and with Meta+K, closes with Escape", async () => {
    const user = userEvent.setup();
    view();
    expect(dialog()).toBeNull();
    await user.keyboard("{Control>}k{/Control}");
    expect(dialog()).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Search pages, settings and actions")).toHaveFocus();
    expect(screen.getByText("↑ ↓ move")).toBeInTheDocument();
    expect(screen.getByText("↵ open")).toBeInTheDocument();
    expect(screen.getByText("esc close")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(dialog()).toBeNull();
    await user.keyboard("{Meta>}k{/Meta}");
    expect(dialog()).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(dialog()).toBeNull();
  });

  it("the top bar button opens it", async () => {
    const user = userEvent.setup();
    view();
    await user.click(screen.getByRole("button", { name: /Search or jump to/ }));
    expect(dialog()).toBeInTheDocument();
  });

  it("arrows move, Enter opens the address", async () => {
    const user = userEvent.setup();
    const { path } = view();
    await user.keyboard("{Control>}k{/Control}");
    await user.keyboard("api");
    expect(screen.getByRole("option", { name: /API keys/ })).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("heading", { name: "Keys page" })).toBeInTheDocument();
    expect(path()).toBe("/console/keys");
    expect(dialog()).toBeNull();

    await user.keyboard("{Control>}k{/Control}");
    const options = screen.getAllByRole("option");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowDown}");
    expect(options[1]).toHaveAttribute("aria-selected", "true");
    expect(options[0]).toHaveAttribute("aria-selected", "false");
    await user.keyboard("{ArrowUp}{ArrowUp}");
    expect(screen.getAllByRole("option").at(-1)).toHaveAttribute("aria-selected", "true");
  });

  it("Create user opens the New user dialog", async () => {
    const user = userEvent.setup();
    const { path } = view();
    await user.keyboard("{Control>}k{/Control}");
    await user.type(screen.getByPlaceholderText("Search pages, settings and actions"), "create user");
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: "New user" })).toBeInTheDocument();
    expect(path()).toBe("/console/users");
  });

  it("a read only session gets no create action, and the state never opens a dialog", async () => {
    const user = userEvent.setup();
    view(readOnly);
    await user.keyboard("{Control>}k{/Control}");
    await user.type(screen.getByPlaceholderText("Search pages, settings and actions"), "create");
    expect(screen.queryByRole("option", { name: /Create user/ })).toBeNull();
  });

  it("an admin finds a setting and opens its address", async () => {
    const user = userEvent.setup();
    const admin: ConsoleSession = { ...session, tenant: { ...session.tenant, admin: true } };
    const api = fakeApi({
      "GET /v1/admin/settings": {
        body: {
          revision: 1,
          data: [
            {
              name: "OHS_LOG_LEVEL",
              label: "Log level",
              section: "Server",
              help: "",
              type: "string",
              min: null,
              max: null,
              values: null,
              default: "info",
              default_note: null,
              secret: false,
              apply: "live",
              source: "default",
              value: "info",
              stored_value: null,
              invalid: null,
            },
          ],
        },
      },
    });
    const { path } = renderWithApp(
      <Routes>
        <Route element={<AppShell />}>
          <Route path="jobs" element={<h1>Jobs page</h1>} />
          <Route path="settings/:section/:name" element={<h1>Setting page</h1>} />
        </Route>
      </Routes>,
      { session: admin, api, route: "/console/jobs" },
    );
    await user.keyboard("{Control>}k{/Control}");
    await waitFor(() => {
      expect(api.callsTo("GET /v1/admin/settings")).toHaveLength(1);
    });
    await user.type(screen.getByPlaceholderText("Search pages, settings and actions"), "OHS_LOG");
    await screen.findByRole("option", { name: /OHS_LOG_LEVEL/ });
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("heading", { name: "Setting page" })).toBeInTheDocument();
    expect(path()).toBe("/console/settings/server/OHS_LOG_LEVEL");
  });

  it("focus returns to where it was on close", async () => {
    const user = userEvent.setup();
    view();
    const button = screen.getByRole("button", { name: /Search or jump to/ });
    button.focus();
    await user.keyboard("{Control>}k{/Control}");
    expect(button).not.toHaveFocus();
    await user.keyboard("{Escape}");
    await waitFor(() => {
      expect(button).toHaveFocus();
    });
  });
});
