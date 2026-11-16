import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ConsoleSession, User } from "../../api/types";
import { fakeApi, type FakeApi, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import readOnlyJson from "../../test/fixtures/session-read-only.json";
import { renderWithApp } from "../../test/render";

const session = sessionJson as ConsoleSession;
const readOnly = readOnlyJson as ConsoleSession;

const ID = "00000000-0000-4000-8000-000000000007";
const BASE = `/v1/users/${ID}`;

const person: User = {
  id: ID,
  external_ref: "demo-user",
  status: "active",
  created_at: "2026-10-01T08:30:00Z",
  timezone: null,
  timezone_source: null,
};

const coverage = {
  pending_jobs: 2,
  parked_jobs: 0,
  rejected_records: 0,
  truncated: false,
  days: [
    { provider: "apple_health", date: "2026-10-01", records: 40, metrics: {}, last_synced_at: "2026-10-02T06:00:00Z" },
    { provider: "apple_health", date: "2026-10-02", records: 60, metrics: {}, last_synced_at: "2026-10-03T06:00:00Z" },
  ],
};

const connections = {
  data: [
    { provider: "apple_health", status: "active", created_at: "2026-10-01T08:30:00Z", last_sync_at: "2026-10-03T06:00:00Z" },
    { provider: "oura", status: "needs_reauth", created_at: "2026-10-01T08:30:00Z", last_sync_at: null },
    { provider: "withings", status: "disconnected", created_at: "2026-10-01T08:30:00Z", last_sync_at: null },
  ],
};

const freshness = {
  as_of: "2026-10-03T07:00:00Z",
  data: [
    {
      provider: "apple_health",
      kind: "oauth",
      status: "active",
      state: "fresh",
      synced_through: "2026-10-03T06:00:00Z",
      lag_seconds: 60,
      push_healthy: true,
      pull_every_seconds: 3600,
      consecutive_failures: 0,
    },
  ],
};

const base: Record<string, FakeHandler> = {
  [`GET ${BASE}`]: { body: person },
  [`GET ${BASE}/coverage`]: { body: coverage },
  [`GET ${BASE}/connections`]: { body: connections },
  [`GET ${BASE}/freshness`]: { body: freshness },
};

function view(routes: Record<string, FakeHandler>, tab = "", s: ConsoleSession = session) {
  const api: FakeApi = fakeApi({ ...base, ...routes });
  const out = renderWithApp(null, { session: s, api, route: `/console/users/${ID}${tab}` });
  return { ...out, api };
}

function files(): { urls: Blob[]; saved: string[] } {
  const urls: Blob[] = [];
  const saved: string[] = [];
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: (blob: Blob) => {
      urls.push(blob);
      return "blob:fake";
    },
  });
  Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: () => undefined });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    saved.push(this.download);
  });
  return { urls, saved };
}

describe("user page", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("shows the tabs Data, Connections, Consents, Sync runs, Shares and keeps the tab in the address", async () => {
    const { path } = view({
      [`GET ${BASE}/consents`]: { body: { data: [], categories: [] } },
      [`GET ${BASE}/consents/history`]: { body: { data: [], next_cursor: null } },
    });
    expect(await screen.findByRole("heading", { name: "demo-user", level: 1 })).toBeInTheDocument();
    const tabs = within(screen.getByRole("tablist", { name: "User" }));
    expect(tabs.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Data", "Connections", "Consents", "Sync runs", "Shares"]);
    expect(tabs.getByRole("tab", { name: "Data" })).toHaveAttribute("aria-selected", "true");
    await userEvent.click(tabs.getByRole("tab", { name: "Consents" }));
    expect(path()).toBe(`/console/users/${ID}/consents`);
    // The shell remounts the page on every address, so ask for the tab list again.
    await waitFor(() => {
      expect(within(screen.getByRole("tablist", { name: "User" })).getByRole("tab", { name: "Consents" })).toHaveAttribute("aria-selected", "true");
    });
  });

  it("opens on the tab named in the address", async () => {
    view({}, "/connections");
    expect(await screen.findByText("Apple health")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Connections" })).toHaveAttribute("aria-selected", "true");
  });

  it("Data loads coverage only; values wait for Preview", async () => {
    const { api } = view({
      [`GET ${BASE}/daily`]: { body: { data: [{ date: "2026-10-02", steps: 4321 }], next_cursor: null } },
      [`GET ${BASE}/sleep`]: { body: { data: [{ id: "s1", total_minutes: 431 }], next_cursor: null } },
      [`GET ${BASE}/workouts`]: { body: { data: [], next_cursor: null } },
    });
    expect((await screen.findAllByText("100")).length).toBeGreaterThan(0);
    const gets = api.calls.filter((c) => c.method === "GET").map((c) => c.path);
    expect(gets).toContain(`${BASE}/coverage`);
    expect(gets.some((p) => /\/(daily|sleep|workouts|samples|fhir|omh|exports|archive)/.test(p))).toBe(false);

    const preview = within(screen.getByRole("group", { name: "Preview" }));
    await userEvent.click(preview.getByRole("button", { name: "Daily summaries" }));
    expect(await screen.findByText(/4321/)).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === `${BASE}/daily?limit=10`)).toBe(true);

    await userEvent.click(preview.getByRole("button", { name: "Sleep" }));
    expect(await screen.findByText(/431/)).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === `${BASE}/sleep?limit=10`)).toBe(true);

    await userEvent.click(preview.getByRole("button", { name: "Workouts" }));
    expect(await screen.findByText("Nothing yet.")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === `${BASE}/workouts?limit=10`)).toBe(true);
  });

  it("downloads build the file in the page", async () => {
    const { urls, saved } = files();
    const { api } = view({
      [`GET ${BASE}/fhir/Patient/${ID}/$everything`]: { body: "{}" },
      [`GET ${BASE}/omh/data-points`]: { body: "{}" },
      [`GET ${BASE}/archive`]: { body: "zip" },
      ...Object.fromEntries(
        ["samples", "category-samples", "sleep", "workouts", "nutrition", "daily", "ecg"].map((d) => [`GET ${BASE}/exports/${d}`, { body: "a,b" }]),
      ),
    });
    await screen.findByRole("button", { name: "FHIR R4 Bundle" });
    await userEvent.click(screen.getByRole("button", { name: "FHIR R4 Bundle" }));
    await waitFor(() => {
      expect(saved).toContain(`fhir-${ID}.json`);
    });
    expect(api.calls.some((c) => c.path === `${BASE}/fhir/Patient/${ID}/$everything?limit=1000`)).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "Open mHealth" }));
    await waitFor(() => {
      expect(saved).toContain(`omh-${ID}.json`);
    });
    await userEvent.click(screen.getByRole("button", { name: "Full archive (zip)" }));
    await waitFor(() => {
      expect(saved).toContain(`archive-${ID}.zip`);
    });
    const datasets = ["samples", "category-samples", "sleep", "workouts", "nutrition", "daily", "ecg"];
    for (const d of datasets) {
      const label = { samples: "Samples", "category-samples": "Category samples", sleep: "Sleep", workouts: "Workouts", nutrition: "Nutrition", daily: "Daily summaries", ecg: "ECG" }[d] ?? d;
      const group = screen.getByRole("group", { name: "CSV" });
      await userEvent.click(within(group).getByRole("button", { name: label }));
      await waitFor(() => {
        expect(saved).toContain(`${d}-${ID}.csv`);
      });
      expect(api.calls.some((c) => c.path === `${BASE}/exports/${d}?format=csv`)).toBe(true);
    }
    expect(urls.length).toBe(3 + datasets.length);
    for (const c of api.calls) expect(c.path).not.toMatch(/token|key=|api_key/i);
  });

  it("Connection link creates a connect session and shows the link once with Copy", async () => {
    const link = "https://ohs.example.com/connect/one-time-value";
    const { api } = view({ [`POST ${BASE}/connect-sessions`]: { status: 201, body: { url: link, expires_at: "2030-01-01T00:00:00Z" } } });
    await userEvent.click(await screen.findByRole("button", { name: "Connection link" }));
    const dialog = await screen.findByRole("dialog", { name: "Connection link" });
    expect(within(dialog).getByDisplayValue(link)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Copy/ })).toBeInTheDocument();
    expect(api.callsTo(`POST ${BASE}/connect-sessions`)[0]?.body).toEqual({});
    await userEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() => {
      expect(screen.queryByDisplayValue(link)).not.toBeInTheDocument();
    });
    expect(document.body.textContent).not.toContain(link);
  });

  it("Sync now on a connection", async () => {
    const { api } = view({ [`POST ${BASE}/connections/apple_health/sync`]: { status: 202, body: {} } }, "/connections");
    const row = within(await screen.findByRole("row", { name: /Apple health/ }));
    expect(screen.queryByRole("button", { name: "Sync now" })).toBeInTheDocument();
    await userEvent.click(row.getByRole("button", { name: "Sync now" }));
    expect(api.callsTo(`POST ${BASE}/connections/apple_health/sync`)).toHaveLength(0);
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Sync now" }));
    await waitFor(() => {
      expect(api.callsTo(`POST ${BASE}/connections/apple_health/sync`)).toHaveLength(1);
    });
  });

  it("Connections shows status and freshness", async () => {
    view({}, "/connections");
    const row = within(await screen.findByRole("row", { name: /Apple health/ }));
    expect(row.getByText("Active")).toBeInTheDocument();
    expect(row.getByText("Fresh")).toBeInTheDocument();
    expect(within(screen.getByRole("row", { name: /Oura/ })).getByText("Needs reconnecting")).toBeInTheDocument();
  });

  it("Disconnect asks, and can delete the data", async () => {
    const { api } = view(
      {
        [`DELETE ${BASE}/connections/oura`]: { status: 204 },
        [`DELETE ${BASE}/connections/apple_health`]: { status: 202, body: { job_id: "j1" } },
      },
      "/connections",
    );
    await userEvent.click(within(await screen.findByRole("row", { name: /Oura/ })).getByRole("button", { name: "Disconnect" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => {
      expect(api.callsTo(`DELETE ${BASE}/connections/oura`)).toHaveLength(1);
    });
    expect(api.callsTo(`DELETE ${BASE}/connections/oura`)[0]?.path).toBe(`${BASE}/connections/oura`);

    await userEvent.click(within(screen.getByRole("row", { name: /Apple health/ })).getByRole("button", { name: "Disconnect" }));
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "Also delete its data" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => {
      expect(api.callsTo(`DELETE ${BASE}/connections/apple_health`)).toHaveLength(1);
    });
    expect(api.callsTo(`DELETE ${BASE}/connections/apple_health`)[0]?.path).toBe(`${BASE}/connections/apple_health?delete_data=true`);
  });

  it("Delete user needs the external id typed", async () => {
    const { api, path } = view({ [`DELETE ${BASE}`]: { status: 202 }, "GET /v1/users": { body: { data: [], next_cursor: null } } });
    await userEvent.click(await screen.findByRole("button", { name: "Delete user" }));
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Delete user" });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(dialog).getByRole("textbox"), "demo-us");
    expect(confirm).toBeDisabled();
    expect(api.callsTo(`DELETE ${BASE}`)).toHaveLength(0);
    await userEvent.type(within(dialog).getByRole("textbox"), "er");
    await userEvent.click(confirm);
    await waitFor(() => {
      expect(api.callsTo(`DELETE ${BASE}`)).toHaveLength(1);
    });
    await waitFor(() => {
      expect(path()).toBe("/console/users");
    });
  });

  it("Delete user types the id when there is no external id", async () => {
    const { api } = view({ [`GET ${BASE}`]: { body: { ...person, external_ref: null } }, [`DELETE ${BASE}`]: { status: 202 }, "GET /v1/users": { body: { data: [], next_cursor: null } } });
    await userEvent.click(await screen.findByRole("button", { name: "Delete user" }));
    const dialog = screen.getByRole("dialog");
    await userEvent.type(within(dialog).getByRole("textbox"), ID);
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete user" }));
    await waitFor(() => {
      expect(api.callsTo(`DELETE ${BASE}`)).toHaveLength(1);
    });
  });

  it("Consents shows current consents and the history, read only", async () => {
    const { api } = view(
      {
        [`GET ${BASE}/consents`]: {
          body: {
            data: [
              {
                provider: "apple_health",
                updated_at: "2026-10-02T06:00:00Z",
                categories: [{ category: "sleep", granted: true, granted_at: "2026-10-02T06:00:00Z", revoked_at: null }],
              },
            ],
            categories: ["sleep"],
          },
        },
        [`GET ${BASE}/consents/history`]: {
          body: {
            data: [{ id: 1, at: "2026-10-02T06:00:00Z", provider: "apple_health", category: "sleep", action: "granted", actor_id: "u1", evidence: "link" }],
            next_cursor: null,
          },
        },
      },
      "/consents",
    );
    expect(await screen.findByText("Granted")).toBeInTheDocument();
    expect(await screen.findByText("link")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === `${BASE}/consents`)).toBe(true);
    expect(api.calls.some((c) => c.path.startsWith(`${BASE}/consents/history`))).toBe(true);
    expect(within(screen.getByRole("tabpanel", { name: "Consents" })).queryByRole("button")).not.toBeInTheDocument();
  });

  it("Sync runs lists the user's runs", async () => {
    const { api } = view(
      {
        [`GET ${BASE}/sync-runs`]: {
          body: {
            data: [
              {
                id: 5,
                provider: "oura",
                trigger: "manual",
                status: "failed",
                data_type: null,
                window_start: "2026-10-02T01:00:00Z",
                window_end: "2026-10-02T02:00:00Z",
                attempts: 3,
                objects_stored: 12,
                error: "token expired",
                created_at: "2026-10-02T02:00:00Z",
                started_at: null,
                finished_at: null,
                next_attempt_at: null,
              },
            ],
            next_cursor: null,
          },
        },
      },
      "/sync-runs",
    );
    expect(await screen.findByText("token expired")).toBeInTheDocument();
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === `${BASE}/sync-runs?limit=50`)).toBe(true);
  });

  it("Shares lists the share links, read only", async () => {
    const { api } = view({ [`GET ${BASE}/shares`]: { body: { data: [], next_cursor: null } } }, "/shares");
    expect(await screen.findByText("No shares yet")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path.startsWith(`${BASE}/shares`))).toBe(true);
  });

  it("Shares shows a link with its status", async () => {
    view(
      {
        [`GET ${BASE}/shares`]: {
          body: {
            data: [
              {
                id: "sh1",
                label: "For my doctor",
                categories: ["sleep"],
                start: "2026-09-01",
                end: "2026-10-01",
                expires_at: "2030-01-01T00:00:00Z",
                passcode: true,
                passcode_failures: 0,
                status: "active",
                revoked_at: null,
                revoked_reason: null,
                observations: 10,
                file_bytes: 2048,
                accesses: 3,
                last_accessed_at: null,
                created_at: "2026-10-01T00:00:00Z",
              },
            ],
            next_cursor: null,
          },
        },
      },
      "/shares",
    );
    expect(await screen.findByText("For my doctor")).toBeInTheDocument();
    expect(within(screen.getByRole("tabpanel", { name: "Shares" })).getByText("Active")).toBeInTheDocument();
  });

  it("a read only session sees no action buttons", async () => {
    view({ [`POST ${BASE}/connect-sessions`]: { status: 201, body: {} } }, "", readOnly);
    expect(await screen.findByRole("heading", { name: "demo-user", level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Connection link" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete user" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Connections" }));
    expect(await screen.findByText("Apple health")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sync now" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disconnect" })).not.toBeInTheDocument();
  });

  it("404 shows Not found with a link back to Users", async () => {
    view({ [`GET ${BASE}`]: { status: 404, body: { title: "Not Found", status: 404, detail: "no such user" } } });
    expect(await screen.findByText("Not found")).toBeInTheDocument();
    expect(within(screen.getByRole("main")).getByRole("link", { name: "Users" })).toHaveAttribute("href", "/console/users");
  });
});
