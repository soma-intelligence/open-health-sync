import { screen, waitFor, within } from "@testing-library/react";
import type { QueryClient } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";

import type { ConsoleSession, Delivery, Webhook } from "../../api/types";
import { fakeApi, type FakeApi, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import readOnlyJson from "../../test/fixtures/session-read-only.json";
import { renderWithApp } from "../../test/render";
import { EVENT_TYPES } from "./events";

const session = sessionJson as ConsoleSession;
const readOnly = readOnlyJson as ConsoleSession;

const ID = "11111111-1111-4111-8111-111111111111";
const URL1 = "https://app.example.com/hooks/health";
const BASE = `/v1/webhooks/${ID}`;

const hook: Webhook = {
  id: ID,
  url: URL1,
  description: "Research warehouse",
  event_types: ["data.written", "user.deleted"],
  status: "active",
  disabled_reason: null,
  failing_since: null,
  last_success_at: "2026-10-02T08:00:00Z",
  created_at: "2026-09-01T08:00:00Z",
  updated_at: "2026-10-01T08:00:00Z",
  consecutive_failures: 0,
  paused_until: null,
  previous_secret_expires_at: null,
};

const other: Webhook = { ...hook, id: "22222222-2222-4222-8222-222222222222", url: "https://b.example.com/x", description: "", event_types: [], status: "disabled", disabled_reason: "no answer for 5 days" };

function delivery(id: number, over: Partial<Delivery> = {}): Delivery {
  return {
    id,
    event_id: "33333333-3333-4333-8333-333333333333",
    event_type: "data.written",
    status: "done",
    attempts: 1,
    last_status_code: 200,
    last_error: null,
    next_attempt_at: null,
    created_at: "2026-10-02T08:00:00Z",
    delivered_at: "2026-10-02T08:00:02Z",
    replay_of: null,
    ...over,
  };
}

const pending = delivery(8, { status: "pending", delivered_at: null, last_status_code: null, attempts: 0 });
const done = delivery(7);
const parked = delivery(6, { status: "parked", attempts: 8, last_status_code: 500, last_error: "receiver said 500", delivered_at: null });

const loaded: Record<string, FakeHandler> = {
  "GET /v1/webhooks": { body: { data: [hook, other], next_cursor: null } },
  [`GET ${BASE}`]: { body: hook },
  [`GET ${BASE}/deliveries`]: { body: { data: [pending, done, parked], next_cursor: null } },
};

function view(routes: Record<string, FakeHandler>, route: string, s: ConsoleSession = session) {
  const api: FakeApi = fakeApi({ ...loaded, ...routes });
  return renderWithApp(null, { session: s, api, route });
}


function mutationsHold(queryClient: QueryClient, secret: string): boolean {
  return queryClient
    .getMutationCache()
    .getAll()
    .some((m) => JSON.stringify(m.state.data ?? null).includes(secret));
}

const LIST = "/console/webhooks";
const DETAIL = `/console/webhooks/${ID}`;

describe("webhooks", () => {
  afterEach(() => {
    window.localStorage.clear();
  });

  it("lists endpoints with events and status", async () => {
    const { api } = view({}, LIST);
    expect(await screen.findByRole("heading", { name: "Webhooks", level: 1 })).toBeInTheDocument();
    const row = await screen.findByRole("row", { name: /Research warehouse/ });
    expect(within(row).getByText(URL1)).toBeInTheDocument();
    expect(within(row).getByText("2 types")).toBeInTheDocument();
    expect(within(row).getByText("Active")).toBeInTheDocument();
    const off = screen.getByRole("row", { name: /b\.example\.com/ });
    expect(within(off).getByText("Every type")).toBeInTheDocument();
    expect(within(off).getByText("Turned off")).toBeInTheDocument();
    expect(api.callsTo("GET /v1/webhooks")).toHaveLength(1);
  });

  it("creates an endpoint and shows its signing secret once", async () => {
    const secret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw";
    const { api, queryClient } = view({ "POST /v1/webhooks": { status: 201, body: { ...hook, secret } } }, LIST);
    await userEvent.click(await screen.findByRole("button", { name: "New endpoint" }));
    const ask = screen.getByRole("dialog", { name: "New endpoint" });
    await userEvent.type(within(ask).getByLabelText("Endpoint URL"), URL1);
    await userEvent.type(within(ask).getByLabelText("Description (optional)"), "Research warehouse");
    await userEvent.click(within(ask).getByRole("checkbox", { name: "data.written" }));
    expect(api.callsTo("POST /v1/webhooks")).toHaveLength(0);
    await userEvent.click(within(ask).getByRole("button", { name: "Create endpoint" }));
    const dialog = await screen.findByRole("dialog", { name: "Signing secret" });
    expect(within(dialog).getByDisplayValue(secret)).toBeInTheDocument();
    await waitFor(() => {
      expect(mutationsHold(queryClient, secret)).toBe(false);
    });
    expect(api.callsTo("POST /v1/webhooks")[0]?.body).toEqual({
      url: URL1,
      description: "Research warehouse",
      event_types: ["data.written"],
    });
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "I have stored it somewhere safe" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => {
      expect(screen.queryByDisplayValue(secret)).not.toBeInTheDocument();
    });
    expect(document.body.textContent).not.toContain(secret);
  });

  it("asks for the URL before creating", async () => {
    const { api } = view({}, LIST);
    await userEvent.click(await screen.findByRole("button", { name: "New endpoint" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Create endpoint" }));
    expect(await screen.findByText("Enter the endpoint URL.")).toBeInTheDocument();
    expect(api.callsTo("POST /v1/webhooks")).toHaveLength(0);
  });

  it("offers every event type of the schema", async () => {
    view({}, LIST);
    await userEvent.click(await screen.findByRole("button", { name: "New endpoint" }));
    const ask = screen.getByRole("dialog");
    expect(within(ask).getAllByRole("checkbox")).toHaveLength(EVENT_TYPES.length);
  });

  it("edits URL and events, sending only the changed fields", async () => {
    const saved = { ...hook, url: "https://new.example.com/h", event_types: ["data.written"] };
    const { api } = view({ [`PATCH ${BASE}`]: { body: saved } }, DETAIL);
    const url = await screen.findByLabelText("Endpoint URL");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
    await userEvent.clear(url);
    await userEvent.type(url, "https://new.example.com/h");
    await userEvent.click(screen.getByRole("checkbox", { name: "user.deleted" }));
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    const ask = screen.getByRole("dialog", { name: "Save these changes?" });
    expect(api.callsTo(`PATCH ${BASE}`)).toHaveLength(0);
    await userEvent.click(within(ask).getByRole("button", { name: "Save" }));
    await waitFor(() => {
      expect(api.callsTo(`PATCH ${BASE}`)).toHaveLength(1);
    });
    expect(api.callsTo(`PATCH ${BASE}`)[0]?.body).toEqual({ url: "https://new.example.com/h", event_types: ["data.written"] });
  });

  it("pauses and resumes", async () => {
    let current: Webhook = hook;
    const { api } = view(
      {
        [`GET ${BASE}`]: () => ({ body: current }),
        [`PATCH ${BASE}`]: (call) => {
          const enabled = (call.body as { enabled: boolean }).enabled;
          current = { ...current, status: enabled ? "active" : "disabled" };
          return { body: current };
        },
      },
      DETAIL,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Pause" }));
    expect(api.callsTo(`PATCH ${BASE}`)).toHaveLength(0);
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Pause" }));
    await waitFor(() => {
      expect(api.callsTo(`PATCH ${BASE}`)[0]?.body).toEqual({ enabled: false });
    });
    await userEvent.click(await screen.findByRole("button", { name: "Resume" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Resume" }));
    await waitFor(() => {
      expect(api.callsTo(`PATCH ${BASE}`)[1]?.body).toEqual({ enabled: true });
    });
  });

  it("keeps a typed URL in the settings form when the endpoint is paused meanwhile", async () => {
    let current: Webhook = hook;
    view(
      {
        [`GET ${BASE}`]: () => ({ body: current }),
        [`PATCH ${BASE}`]: () => {
          current = { ...current, status: "disabled", updated_at: "2026-10-02T08:00:00Z" };
          return { body: current };
        },
      },
      DETAIL,
    );
    const url = await screen.findByLabelText("Endpoint URL");
    await userEvent.clear(url);
    await userEvent.type(url, "https://typed.example.com/h");
    await userEvent.click(screen.getByRole("button", { name: "Pause" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Pause" }));
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.getByLabelText("Endpoint URL")).toHaveValue("https://typed.example.com/h");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
  });

  it("sends a test", async () => {
    const { api } = view({ [`POST ${BASE}/test`]: { status: 202, body: delivery(9) } }, DETAIL);
    await userEvent.click(await screen.findByRole("button", { name: "Send test" }));
    expect(api.callsTo(`POST ${BASE}/test`)).toHaveLength(0);
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Send test" }));
    await waitFor(() => {
      expect(api.callsTo(`POST ${BASE}/test`)).toHaveLength(1);
    });
    expect(api.callsTo(`POST ${BASE}/test`)[0]?.body).toEqual({});
  });

  it("rotates the secret after a confirmation and shows the new one once", async () => {
    const secret = "whsec_NEWNEWNEWNEWNEWNEWNEWNEWNEWNEW12";
    const { api, queryClient } = view({ [`POST ${BASE}/rotate-secret`]: { body: { secret, previous_secret_expires_at: "2026-10-03T08:00:00Z" } } }, DETAIL);
    await userEvent.click(await screen.findByRole("button", { name: "Rotate secret" }));
    const ask = screen.getByRole("dialog", { name: "Rotate the signing secret?" });
    expect(api.callsTo(`POST ${BASE}/rotate-secret`)).toHaveLength(0);
    await userEvent.click(within(ask).getByRole("button", { name: "Rotate" }));
    const dialog = await screen.findByRole("dialog", { name: "New signing secret" });
    expect(within(dialog).getByDisplayValue(secret)).toBeInTheDocument();
    expect(within(dialog).getByText(/also signs until 2026-10-03 08:00 UTC/)).toBeInTheDocument();
    await waitFor(() => {
      expect(mutationsHold(queryClient, secret)).toBe(false);
    });
    expect(api.callsTo(`POST ${BASE}/rotate-secret`)[0]?.body).toEqual({});
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "I have stored it somewhere safe" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => {
      expect(screen.queryByDisplayValue(secret)).not.toBeInTheDocument();
    });
    expect(document.body.textContent).not.toContain(secret);
    expect(mutationsHold(queryClient, secret)).toBe(false);
  });

  it("can end the old secret at once", async () => {
    const { api } = view({ [`POST ${BASE}/rotate-secret`]: { body: { secret: "whsec_x", previous_secret_expires_at: null } } }, DETAIL);
    await userEvent.click(await screen.findByRole("button", { name: "Rotate secret" }));
    const ask = screen.getByRole("dialog");
    await userEvent.click(within(ask).getByRole("checkbox", { name: /End the old secret now/ }));
    await userEvent.click(within(ask).getByRole("button", { name: "Rotate" }));
    await screen.findByRole("dialog", { name: "New signing secret" });
    expect(api.callsTo(`POST ${BASE}/rotate-secret`)[0]?.body).toEqual({ expire_previous: true });
  });

  it("delete needs the URL typed", async () => {
    const { api, path } = view({ [`DELETE ${BASE}`]: { status: 204 } }, DETAIL);
    await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const ask = screen.getByRole("dialog", { name: "Delete this endpoint?" });
    const confirm = within(ask).getByRole("button", { name: "Delete endpoint" });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(ask).getByRole("textbox"), URL1);
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);
    await waitFor(() => {
      expect(api.callsTo(`DELETE ${BASE}`)).toHaveLength(1);
    });
    await waitFor(() => {
      expect(path()).toBe("/console/webhooks");
    });
  });

  it("lists deliveries with response, duration, attempt and time", async () => {
    const { api } = view({}, DETAIL);
    const row = await screen.findByRole("row", { name: /#7/ });
    expect(within(row).getByText("200")).toBeInTheDocument();
    expect(within(row).getByText("2 s")).toBeInTheDocument();
    expect(screen.getByText("receiver said 500")).toBeInTheDocument();
    expect(api.calls.some((c) => c.path === `${BASE}/deliveries?limit=50`)).toBe(true);
    for (const h of ["Event", "Response", "Duration", "Attempt", "Time"]) {
      expect(screen.getByRole("columnheader", { name: h })).toBeInTheDocument();
    }
  });

  it("retries and replays a delivery", async () => {
    const { api } = view(
      {
        [`POST ${BASE}/deliveries/6/retry`]: { status: 202, body: parked },
        [`POST ${BASE}/deliveries/7/replay`]: { status: 202, body: delivery(10, { replay_of: 7 }) },
      },
      DETAIL,
    );
    const pendingRow = await screen.findByRole("row", { name: /#8/ });
    expect(within(pendingRow).queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
    await userEvent.click(within(await screen.findByRole("row", { name: /#6/ })).getByRole("button", { name: "Retry" }));
    expect(api.callsTo(`POST ${BASE}/deliveries/6/retry`)).toHaveLength(0);
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Retry" }));
    await waitFor(() => {
      expect(api.callsTo(`POST ${BASE}/deliveries/6/retry`)).toHaveLength(1);
    });
    await userEvent.click(within(await screen.findByRole("row", { name: /#7/ })).getByRole("button", { name: "Replay" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Replay" }));
    await waitFor(() => {
      expect(api.callsTo(`POST ${BASE}/deliveries/7/replay`)).toHaveLength(1);
    });
  });

  it("a read only session sees no action", async () => {
    view({}, LIST, readOnly);
    await screen.findByRole("row", { name: /Research warehouse/ });
    expect(screen.queryByRole("button", { name: "New endpoint" })).not.toBeInTheDocument();
  });

  it("a read only session sees no action on the endpoint", async () => {
    view({}, DETAIL, readOnly);
    await screen.findByRole("row", { name: /#7/ });
    for (const name of ["Send test", "Rotate secret", "Pause", "Delete", "Retry", "Replay", "Save changes"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByLabelText("Endpoint URL")).not.toBeInTheDocument();
  });

  it("shows a missing endpoint as not found", async () => {
    view({ [`GET ${BASE}`]: { status: 404, body: { title: "Not Found", status: 404, detail: "no such webhook" } } }, DETAIL);
    expect(await screen.findByText("Not found")).toBeInTheDocument();
  });
});
