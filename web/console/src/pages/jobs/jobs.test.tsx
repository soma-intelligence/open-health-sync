import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";

import type { ConsoleSession, EtlJob, TenantSyncRun } from "../../api/types";
import { AppShell } from "../../shell/AppShell";
import { fakeApi, type FakeApi, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import readOnlyJson from "../../test/fixtures/session-read-only.json";
import { renderWithApp } from "../../test/render";
import { JobsPage } from "./JobsPage";

const session = sessionJson as ConsoleSession;
const readOnly = readOnlyJson as ConsoleSession;

const RUNS = "GET /v1/pipeline/sync-runs";
const JOBS = "GET /v1/pipeline/etl-jobs";

const run: TenantSyncRun = {
  id: 9,
  user_id: "00000000-0000-4000-8000-000000000001",
  provider: "oura",
  trigger: "scheduled",
  status: "failed",
  data_type: null,
  window_start: "2026-10-01T00:00:00Z",
  window_end: "2026-10-02T00:00:00Z",
  attempts: 5,
  objects_stored: 0,
  error: "provider said 500",
  created_at: "2026-10-02T08:00:00Z",
  started_at: "2026-10-02T08:00:01Z",
  finished_at: "2026-10-02T08:05:00Z",
  next_attempt_at: null,
};

const job: EtlJob = {
  id: 42,
  user_id: "00000000-0000-4000-8000-000000000002",
  provider: "withings",
  tier: "raw",
  status: "parked",
  attempts: 5,
  error: "disk full",
  created_at: "2026-10-02T09:00:00Z",
  next_attempt_at: null,
};

function view(routes: Record<string, FakeHandler>, s: ConsoleSession = session) {
  const api: FakeApi = fakeApi(routes);
  const ui = (
    <Routes>
      <Route element={<AppShell />}>
        <Route path="jobs" element={<JobsPage />} />
      </Route>
    </Routes>
  );
  return renderWithApp(ui, { session: s, api, route: "/console/jobs" });
}

const loaded = {
  [RUNS]: { body: { data: [run], next_cursor: null } },
  [JOBS]: { body: { data: [job], next_cursor: null } },
};

describe("jobs", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("lists failed sync runs first, then parked jobs", async () => {
    const { api } = view(loaded);
    expect(await screen.findByRole("heading", { name: "Jobs", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Work that stopped and waits for you. Everything else retries on its own.")).toBeInTheDocument();
    expect(await screen.findByText("provider said 500")).toBeInTheDocument();
    expect(await screen.findByText("disk full")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === "/v1/pipeline/sync-runs?status=failed&limit=50")).toBe(true);
    expect(api.calls.some((c) => c.path === "/v1/pipeline/etl-jobs?status=parked&limit=100")).toBe(true);
    const titles = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(titles).toEqual(["Failed sync runs", "Parked processing jobs"]);
  });

  it("retries one parked job", async () => {
    const { api } = view({ ...loaded, "POST /v1/pipeline/etl-jobs/42/retry": { body: { ...job, status: "pending" } } });
    await userEvent.click(await screen.findByRole("button", { name: "Retry" }));
    const dialog = await screen.findByRole("dialog");
    expect(api.callsTo("POST /v1/pipeline/etl-jobs/42/retry")).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole("button", { name: "Retry" }));
    await waitFor(() => {
      expect(api.callsTo("POST /v1/pipeline/etl-jobs/42/retry")).toHaveLength(1);
    });
    expect(await screen.findByText("Job #42 queued again.")).toBeInTheDocument();
  });

  it("says when a job is no longer parked", async () => {
    view({
      ...loaded,
      "POST /v1/pipeline/etl-jobs/42/retry": { status: 409, body: { title: "Conflict", status: 409, detail: "job 42 is done" } },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Retry" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Retry" }));
    expect(await within(dialog).findByText(/This job is no longer parked\./)).toBeInTheDocument();
    expect(within(dialog).getByText(/job 42 is done/)).toBeInTheDocument();
  });

  it("retries every parked job", async () => {
    const { api } = view({ ...loaded, "POST /v1/pipeline/etl-jobs/retry": { body: { retried: 3 } } });
    await userEvent.click(await screen.findByRole("button", { name: "Retry all" }));
    const dialog = await screen.findByRole("dialog", { name: "Retry every parked job?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Retry all" }));
    await waitFor(() => {
      expect(api.callsTo("POST /v1/pipeline/etl-jobs/retry")).toHaveLength(1);
    });
    expect(api.callsTo("POST /v1/pipeline/etl-jobs/retry")[0]?.body).toEqual({});
    expect(await screen.findByText("3 jobs queued again.")).toBeInTheDocument();
  });

  it("empty state says nothing is stuck", async () => {
    view({ [RUNS]: { body: { data: [], next_cursor: null } }, [JOBS]: { body: { data: [], next_cursor: null } } });
    expect(await screen.findByText("Nothing is stuck")).toBeInTheDocument();
    expect(screen.getByText("No failed sync run")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry all" })).not.toBeInTheDocument();
  });

  it("a read only session sees no Retry button", async () => {
    view(loaded, readOnly);
    expect(await screen.findByText("disk full")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry all" })).not.toBeInTheDocument();
  });
});
