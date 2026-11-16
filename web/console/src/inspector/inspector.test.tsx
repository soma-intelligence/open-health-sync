import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";

import { ApiError } from "../api/client";
import type { ConsoleSession } from "../api/types";
import { AppShell } from "../shell/AppShell";
import { fakeApi } from "../test/fake-api";
import sessionJson from "../test/fixtures/session.json";
import { renderWithApp } from "../test/render";
import { useInspect, type InspectorEntry } from "./InspectorProvider";
import { relatedSettings } from "./snippets";

const tenantSession = sessionJson as ConsoleSession;
const adminSession: ConsoleSession = {
  ...tenantSession,
  tenant: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", name: "Root", admin: true },
  acting_tenant: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", name: "Root", admin: true },
};

const entry: InspectorEntry = {
  kind: "webhooks",
  title: "Webhooks",
  request: { method: "GET", path: "/v1/webhooks" },
  response: { data: [{ id: "wh_1", url: "https://x.test" }] },
};

function Page({ value }: { value: InspectorEntry }) {
  useInspect(value);
  return <p>page</p>;
}

function view(value: InspectorEntry, session: ConsoleSession, api = fakeApi({})) {
  const ui = (
    <Routes>
      <Route element={<AppShell />}>
        <Route path="overview" element={<Page value={value} />} />
      </Route>
    </Routes>
  );
  return renderWithApp(ui, { session, api, route: "/console/overview" });
}

const settingsBody = {
  revision: 1,
  data: relatedSettings("webhooks").map((name, i) => ({
    name,
    label: name,
    section: "Webhooks",
    help: "",
    type: "int",
    min: 1,
    max: null,
    values: null,
    default: "5",
    default_note: null,
    secret: false,
    apply: "live",
    source: i === 0 ? "console" : "default",
    value: i === 0 ? "10" : "5",
    stored_value: null,
    invalid: false,
  })),
};

function narrow(width: number) {
  window.matchMedia = ((query: string) =>
    ({
      matches: query === "(max-width: 1023px)" || (width < 768 && query === "(max-width: 767px)"),
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }) as unknown as MediaQueryList) as typeof window.matchMedia;
}

describe("inspector", () => {
  afterEach(() => {
    Reflect.deleteProperty(window, "matchMedia");
    window.localStorage.clear();
  });

  it("shows the call, the JSON and the related settings", async () => {
    const api = fakeApi({ "GET /v1/admin/settings": { body: settingsBody } });
    view(entry, adminSession, api);
    const pane = screen.getByRole("complementary", { name: "Inspector" });
    const p = within(pane);
    expect(p.getByText("GET")).toBeInTheDocument();
    expect(p.getByText("/v1/webhooks")).toBeInTheDocument();
    for (const t of ["cURL", "TypeScript", "Python"]) expect(p.getByRole("tab", { name: t })).toBeInTheDocument();
    expect(p.getByText(/"id": "wh_1"/)).toBeInTheDocument();
    expect(p.getByText("Configuration")).toBeInTheDocument();
    const first = relatedSettings("webhooks")[0] ?? "";
    const link = await p.findByRole("link", { name: first });
    expect(link).toHaveAttribute("href", `/console/settings/webhooks/${first}`);
    expect(await p.findByText("10")).toBeInTheDocument();
    expect(p.getByText("console")).toBeInTheDocument();
    for (const name of relatedSettings("webhooks")) expect(p.getByRole("link", { name })).toBeInTheDocument();
  });

  it("names related settings without values for a tenant that is not an admin", () => {
    const api = fakeApi({});
    view(entry, tenantSession, api);
    const p = within(screen.getByRole("complementary", { name: "Inspector" }));
    for (const name of relatedSettings("webhooks")) expect(p.getByText(name)).toBeInTheDocument();
    expect(p.queryByRole("link", { name: relatedSettings("webhooks")[0] ?? "" })).not.toBeInTheDocument();
    expect(p.getByText("Settings need an instance admin session.")).toBeInTheDocument();
    expect(api.callsTo("GET /v1/admin/settings")).toHaveLength(0);
  });

  it("shows only the state of a secret as set", async () => {
    const body = {
      ...settingsBody,
      data: settingsBody.data.map((s, i) => (i === 0 ? { ...s, secret: true, value: "set", source: "console" } : s)),
    };
    view(entry, adminSession, fakeApi({ "GET /v1/admin/settings": { body } }));
    expect(await screen.findByText("set")).toBeInTheDocument();
  });

  it("shows the raw answer of a failed call", () => {
    const error = new ApiError(404, "no such webhook", "req_1", { title: "Not Found", status: 404, detail: "no such webhook" });
    view({ ...entry, response: undefined, error }, tenantSession);
    const p = within(screen.getByRole("complementary", { name: "Inspector" }));
    expect(p.getByText("404")).toBeInTheDocument();
    expect(p.getByText(/"title": "Not Found"/)).toBeInTheDocument();
  });

  it("remembers open or closed and the language per browser", async () => {
    view(entry, tenantSession);
    const user = userEvent.setup();
    await user.click(screen.getByRole("tab", { name: "Python" }));
    expect(window.localStorage.getItem("ohs.console.inspector-lang")).toBe("python");
    expect(screen.getByText(/import os, requests/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Inspector" }));
    expect(window.localStorage.getItem("ohs.console.inspector")).toBe("closed");
  });

  it("starts on the stored language", () => {
    window.localStorage.setItem("ohs.console.inspector-lang", "ts");
    view(entry, tenantSession);
    expect(screen.getByRole("tab", { name: "TypeScript" })).toHaveAttribute("aria-selected", "true");
  });

  it("starts closed on a narrow screen and keeps the wide preference", async () => {
    window.localStorage.setItem("ohs.console.inspector", "open");
    narrow(1000);
    view(entry, tenantSession);
    expect(screen.queryByRole("complementary", { name: "Inspector" })).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Inspector" }));
    expect(screen.getByRole("complementary", { name: "Inspector" })).toBeInTheDocument();
    expect(window.localStorage.getItem("ohs.console.inspector")).toBe("open");
  });

  it("opens as a bottom sheet under 768 px", async () => {
    narrow(600);
    view(entry, tenantSession);
    const user = userEvent.setup();
    expect(screen.queryByRole("complementary", { name: "Inspector" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Inspector" }));
    expect(screen.getByRole("complementary", { name: "Inspector" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("complementary", { name: "Inspector" })).not.toBeInTheDocument();
  });
});
