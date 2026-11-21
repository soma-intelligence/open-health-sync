import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";

import type { ConsoleSession, Pipeline } from "../../api/types";
import { AppShell } from "../../shell/AppShell";
import { fakeApi, type FakeApi, type FakeReply } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import { renderWithApp } from "../../test/render";
import { OverviewPage } from "./OverviewPage";

const session = sessionJson as ConsoleSession;

const quiet: Pipeline = {
  generated_at: "2026-10-13T12:00:00Z",
  users: { active: 2, deleting: 0 },
  sources: [{ slug: "oura", name: "Oura", kinds: ["oauth"], enabled: true, connections: { active: 2 }, broken: 0, records: 0, paused_until: null }],
  sync: { queued: 1, running: 0, retrying: 0, rate_limited: 0, failed_24h: 0, succeeded_24h: 40 },
  processing: { pending: 0, running: 0, parked: 0, rejected_records: 0 },
  storage: { raw_objects_24h: 12, raw_bytes_24h: 2_500_000 },
  delivery: { endpoints: 1, endpoints_failing: 0, endpoints_disabled: 0, pending: 0, parked: 0 },
  data: { records: 0, users: 0, sources: 0, last_synced_at: null },
};

const fresh: Pipeline = {
  ...quiet,
  users: { active: 0, deleting: 0 },
  sources: [],
  sync: { ...quiet.sync, queued: 0, succeeded_24h: 0 },
  storage: { raw_objects_24h: 0, raw_bytes_24h: 0 },
  delivery: { ...quiet.delivery, endpoints: 0 },
};

const trouble: Pipeline = {
  ...quiet,
  sources: [{ ...(quiet.sources[0] as Pipeline["sources"][number]), connections: { active: 1, needs_reauth: 1 } }],
  processing: { ...quiet.processing, parked: 2 },
};

const up: FakeReply = { status: 200, body: "ok" };

function view(pipeline: FakeReply, extra: Record<string, FakeReply> = {}): ReturnType<typeof renderWithApp> & { api: FakeApi } {
  const api = fakeApi({ "GET /v1/pipeline": pipeline, "GET /healthz": up, "GET /readyz": up, ...extra });
  const ui = (
    <Routes>
      <Route element={<AppShell />}>
        <Route path="overview" element={<OverviewPage />} />
        <Route path="jobs" element={<p>jobs page</p>} />
      </Route>
    </Routes>
  );
  return renderWithApp(ui, { session, api, route: "/console/overview" });
}

describe("overview", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("opens on the Pipeline, live card with five stages", async () => {
    view({ body: quiet });
    expect(await screen.findByRole("heading", { name: "Overview", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Everything flowing into Acme, right now.")).toBeInTheDocument();
    const band = await screen.findByRole("region", { name: "Pipeline, live" });
    expect(band).toHaveClass("pipeline");
    const headline: Record<string, string> = { Sources: "2", Sync: "40", Processing: "0", Storage: "2.5", Delivery: "1" };
    for (const [title, figure] of Object.entries(headline)) {
      const card = within(band).getByRole("article", { name: title });
      expect(card.querySelector(".stage-num")).toHaveTextContent(new RegExp(`^${figure}$`));
    }
    expect(within(band).getAllByRole("article")).toHaveLength(5);
    expect(within(within(band).getByRole("article", { name: "Storage" })).getByText("MB")).toBeInTheDocument();
    expect(within(within(band).getByRole("article", { name: "Sync" })).getByText("OK")).toBeInTheDocument();
  });

  it("lists what needs you, each with one button to the page that fixes it", async () => {
    view({ body: trouble });
    const card = (await screen.findByRole("heading", { name: "Needs you" })).closest("section");
    expect(card).not.toBeNull();
    const list = within(card as HTMLElement);
    expect(list.getByRole("link", { name: /Oura token expired for 1 user/ })).toHaveAttribute("href", "/console/users");
    expect(list.getByRole("link", { name: /2 processing jobs are parked/ })).toHaveAttribute("href", "/console/jobs");
  });

  it("says so when nothing needs attention", async () => {
    view({ body: quiet });
    expect(await screen.findByText("Nothing needs you.")).toBeInTheDocument();
  });

  it("shows the getting started checklist until every item is done", async () => {
    const first = view({ body: fresh });
    const heading = await screen.findByRole("heading", { name: "Get started" });
    expect(screen.getByText("0 of 5 done")).toBeInTheDocument();
    const card = within(heading.closest("section") as HTMLElement);
    expect(card.getAllByRole("link")).toHaveLength(5);
    expect(card.getByRole("link", { name: "Turn on a cloud provider" })).toHaveAttribute("href", "/console/providers");
    expect(card.getByRole("link", { name: "Add a webhook endpoint" })).toHaveAttribute("href", "/console/webhooks");
    first.unmount();

    view({ body: quiet });
    await screen.findByRole("region", { name: "Pipeline, live" });
    expect(screen.queryByRole("heading", { name: "Get started" })).not.toBeInTheDocument();
  });

  it("counts the steps that are done", async () => {
    view({ body: { ...fresh, users: { active: 1, deleting: 0 } } });
    expect(await screen.findByText("1 of 5 done")).toBeInTheDocument();
  });

  it("shows healthz and readyz", async () => {
    view({ body: quiet });
    expect(await screen.findByText("API live")).toBeInTheDocument();
    expect(await screen.findByText("Ready")).toBeInTheDocument();
  });

  it("shows a probe that fails", async () => {
    view({ body: quiet }, { "GET /readyz": { status: 503, body: { title: "Service Unavailable", status: 503, detail: "database not ready" } } });
    expect(await screen.findByText("Not ready")).toBeInTheDocument();
    expect(screen.getByText("API live")).toBeInTheDocument();
  });

  it("puts the pipeline call in the inspector", async () => {
    view({ body: quiet });
    await screen.findByRole("region", { name: "Pipeline, live" });
    const pane = within(screen.getByRole("complementary", { name: "Inspector" }));
    expect(await pane.findByText("pipeline")).toBeInTheDocument();
    expect(pane.getByText("GET")).toBeInTheDocument();
    expect(pane.getByText("/v1/pipeline")).toBeInTheDocument();
    expect(pane.getByText(/"succeeded_24h": 40/)).toBeInTheDocument();
  });

  it("refetches when Refresh is pressed", async () => {
    const { api } = view({ body: quiet });
    await screen.findByRole("region", { name: "Pipeline, live" });
    expect(api.callsTo("GET /v1/pipeline")).toHaveLength(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "Refresh" }));
    await vi.waitFor(() => {
      expect(api.callsTo("GET /v1/pipeline")).toHaveLength(2);
    });
  });

  it("says what failed in words, with the API detail and a way to try again", async () => {
    const { api } = view({ status: 503, body: { title: "Service Unavailable", status: 503, detail: "the database is not reachable" } });
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("The server is not ready yet. Try again in a moment.")).toBeInTheDocument();
    expect(within(alert).getByText("the database is not reachable")).toBeInTheDocument();
    // Said once: no raw status line beside the panel.
    expect(screen.queryByText(/HTTP 503/)).not.toBeInTheDocument();
    const calls = api.callsTo("GET /v1/pipeline").length;
    await userEvent.setup().click(within(alert).getByRole("button", { name: "Try again" }));
    await vi.waitFor(() => {
      expect(api.callsTo("GET /v1/pipeline").length).toBeGreaterThan(calls);
    });
    expect(screen.queryByRole("region", { name: "Pipeline, live" })).not.toBeInTheDocument();
    const pane = within(screen.getByRole("complementary", { name: "Inspector" }));
    expect(await pane.findByText("503")).toBeInTheDocument();
  });
});
