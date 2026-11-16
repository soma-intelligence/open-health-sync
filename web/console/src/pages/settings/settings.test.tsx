import { screen, waitFor, within } from "@testing-library/react";
import type { QueryClient } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";

import type { ConsoleSession, ProcessHeartbeat, Setting, SettingsList } from "../../api/types";
import { fakeApi, type FakeApi, type FakeHandler } from "../../test/fake-api";
import sessionJson from "../../test/fixtures/session.json";
import readOnlyJson from "../../test/fixtures/session-read-only.json";
import { renderWithApp } from "../../test/render";

const tenant = (sessionJson as ConsoleSession).tenant;
const admin: ConsoleSession = { ...(sessionJson as ConsoleSession), tenant: { ...tenant, admin: true } };
const notAdmin = sessionJson as ConsoleSession;
const readOnly: ConsoleSession = { ...(readOnlyJson as ConsoleSession), tenant: { ...tenant, admin: true } };

const SECRET = "s3cr3t-value";
const KEY = "ohs_sk_0123456789abcdefABCD";

function s(over: Partial<Setting> & Pick<Setting, "name" | "label" | "section">): Setting {
  return {
    help: "",
    type: "int",
    min: null,
    max: null,
    values: null,
    default: "",
    default_note: null,
    secret: false,
    apply: "live",
    source: "default",
    value: null,
    stored_value: null,
    invalid: null,
    ...over,
  };
}

const SETTINGS: Setting[] = [
  s({ name: "OHS_PUBLIC_URL", label: "Public URL", section: "Server", type: "url", apply: "restart", default: "http://localhost:8080", value: "http://localhost:8080" }),
  s({ name: "OHS_LISTEN", label: "Listen address", section: "Server", type: "string", apply: "bootstrap", source: "env", default: ":8080", value: "127.0.0.1:9000" }),
  s({ name: "OHS_LOG_LEVEL", label: "Log level", section: "Server", type: "enum", values: ["debug", "info", "warn", "error"], source: "env", default: "info", value: "debug" }),
  s({ name: "OHS_METRICS_TOKEN", label: "Metrics token", section: "Metrics", type: "string", secret: true, apply: "restart" }),
  s({ name: "OHS_HASHI_TOKEN", label: "Vault token", section: "Metrics", type: "string", secret: true, apply: "restart", source: "env", value: "set" }),
  s({
    name: "OHS_RETENTION_DAYS",
    label: "Retention days",
    section: "Retention",
    min: 1,
    default: "365",
    value: "365",
    invalid: 'OHS_RETENTION_DAYS: "0" is not a positive integer',
  }),
  s({ name: "OHS_CONSENT_REQUIRED", label: "Consent required", section: "Consent", type: "bool", default: "false", value: "false" }),
  s({ name: "OHS_WEBHOOK_PAUSE_AFTER", label: "Pause after", section: "Webhooks", min: 1, max: 100, default: "5", source: "console", value: "8", help: "Failures before an endpoint is paused." }),
  s({ name: "OHS_WEBHOOK_TIMEOUT", label: "Webhook timeout", section: "Webhooks", type: "duration", apply: "restart", default: "10s", value: "10s" }),
  s({ name: "OHS_EVENT_PUBLIC_HOST", label: "Event host", section: "Event stream", type: "string", default_note: "the host of OHS_PUBLIC_URL", value: "localhost" }),
  s({ name: "OHS_RATE_LIMIT_API", label: "API rate", section: "Rate limits", type: "rate", default: "10/20", value: "10/20" }),
];

const list = (revision = 41, data: Setting[] = SETTINGS): SettingsList => ({ revision, data });

function proc(over: Partial<ProcessHeartbeat>): ProcessHeartbeat {
  return {
    id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    host: "web-1",
    roles: ["api"],
    version: "1.4.0",
    started_at: "2026-11-15T08:00:00Z",
    seen_at: "2026-11-15T09:00:00Z",
    start_revision: 40,
    live_revision: 41,
    skipped: [],
    reload_error: null,
    behind: false,
    stale: false,
    ...over,
  };
}

const base: Record<string, FakeHandler> = {
  "GET /v1/admin/settings": { body: list(), headers: { ETag: '"41"' } },
  "GET /v1/admin/processes": { body: { revision: 41, data: [] } },
  "GET /v1/setup": { status: 404, body: { title: "Not Found", detail: "x" } },
  "GET /v1/admin/tenants": { body: { data: [], next_cursor: null } },
};

const ROUTE = "/console/settings";

function view(routes: Record<string, FakeHandler> = {}, route = ROUTE, session: ConsoleSession = admin) {
  const api: FakeApi = fakeApi({ ...base, ...routes });
  return renderWithApp(null, { session, api, route });
}

function field(label: string): HTMLElement {
  return screen.getByRole("group", { name: label });
}

async function loaded() {
  return screen.findByRole("group", { name: "Pause after" });
}

function mutationsHold(queryClient: QueryClient, secret: string): boolean {
  return queryClient
    .getMutationCache()
    .getAll()
    .some((m) => JSON.stringify([m.state.variables ?? null, m.state.data ?? null]).includes(secret));
}

async function editTwo(user: ReturnType<typeof userEvent.setup>) {
  const pause = within(await loaded()).getByLabelText("Pause after");
  await user.clear(pause);
  await user.type(pause, "9");
  const timeout = within(field("Webhook timeout")).getByLabelText("Webhook timeout");
  await user.clear(timeout);
  await user.type(timeout, "20s");
}

describe("settings", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("lists every section with a sub-navigation", async () => {
    const scrolled: Element[] = [];
    Object.defineProperty(Element.prototype, "scrollIntoView", {
      configurable: true,
      value(this: Element) {
        scrolled.push(this);
      },
    });
    view({}, "/console/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER");
    const nav = await screen.findByRole("navigation", { name: "Settings sections" });
    const names = within(nav)
      .getAllByRole("link")
      .map((a) => a.textContent);
    expect(names).toEqual(["Server", "Metrics", "Retention", "Consent", "Webhooks", "Event stream", "Rate limits"]);
    expect(within(nav).getByRole("link", { name: "Event stream" })).toHaveAttribute("href", "/console/settings/event-stream");
    const target = await loaded();
    await waitFor(() => {
      expect(within(target).getByLabelText("Pause after")).toHaveFocus();
    });
    expect(scrolled).toContain(target);
    expect(screen.getByRole("heading", { name: "Settings", level: 1 })).toBeInTheDocument();
  });

  it("each field shows label, OHS_ name, source, apply mode and default", async () => {
    view();
    await loaded();
    const f = field("Webhook timeout");
    expect(within(f).getByText("OHS_WEBHOOK_TIMEOUT")).toBeInTheDocument();
    expect(within(f).getByText("Default", { selector: ".set-source" })).toBeInTheDocument();
    expect(within(f).getByText("Restart", { selector: ".set-apply" })).toBeInTheDocument();
    expect(within(f).getByText("10s", { selector: ".set-default code" })).toBeInTheDocument();
    const p = field("Pause after");
    expect(within(p).getByText("Console", { selector: ".set-source" })).toBeInTheDocument();
    expect(within(p).getByText("Live", { selector: ".set-apply" })).toBeInTheDocument();
    expect(within(p).getByText("Failures before an endpoint is paused.")).toBeInTheDocument();
    expect(within(p).getByText("1 to 100")).toBeInTheDocument();
    expect(within(field("Log level")).getByText("Environment", { selector: ".set-source" })).toBeInTheDocument();
    expect(screen.getByText("Environment", { selector: ".set-prec .set-source" })).toBeInTheDocument();
  });

  it("a derived default shows its note", async () => {
    view();
    await loaded();
    expect(within(field("Event host")).getByText("the host of OHS_PUBLIC_URL")).toBeInTheDocument();
  });

  it("locked fields say why", async () => {
    view();
    await loaded();
    const env = field("Log level");
    expect(within(env).getByText("Set by OHS_LOG_LEVEL in the environment. Unset it and restart to edit it here.")).toBeInTheDocument();
    expect(within(env).getByLabelText("Log level")).toBeDisabled();
    const boot = field("Listen address");
    expect(within(boot).getByText("Bootstrap setting. Set OHS_LISTEN in the environment.")).toBeInTheDocument();
    expect(within(boot).getByLabelText("Listen address")).toBeDisabled();
    const secret = field("Vault token");
    expect(within(secret).getByText("set", { selector: ".set-secret-state" })).toBeInTheDocument();
    expect(within(secret).queryByRole("button", { name: "Replace" })).not.toBeInTheDocument();
  });

  it("a field skipped as invalid shows its message", async () => {
    view();
    await loaded();
    expect(within(field("Retention days")).getByText('OHS_RETENTION_DAYS: "0" is not a positive integer')).toBeInTheDocument();
  });

  it("the save bar counts pending changes and restarts", async () => {
    const user = userEvent.setup();
    view();
    await editTwo(user);
    const bar = screen.getByRole("region", { name: "Unsaved changes" });
    expect(within(bar).getByText("2 unsaved changes")).toBeInTheDocument();
    expect(within(bar).getByText("1 needs a restart")).toBeInTheDocument();
    await user.click(within(bar).getByRole("button", { name: "Discard" }));
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).not.toBeInTheDocument();
    expect(within(field("Pause after")).getByLabelText("Pause after")).toHaveValue(8);
  });

  it("an emptied typed field blocks Review and is never sent", async () => {
    const user = userEvent.setup();
    const { api } = view();
    const pause = within(await loaded()).getByLabelText("Pause after");
    await user.clear(pause);
    expect(within(field("Pause after")).getByText("Enter a value, or revert to default.")).toBeInTheDocument();
    const bar = screen.getByRole("region", { name: "Unsaved changes" });
    expect(within(bar).getByRole("button", { name: "Review" })).toBeDisabled();
    const rate = within(field("API rate")).getByLabelText("API rate req/s");
    await user.clear(rate);
    await user.clear(within(field("API rate")).getByLabelText("API rate burst"));
    expect(within(field("API rate")).getByText("Enter a value, or revert to default.")).toBeInTheDocument();
    await user.click(within(field("Pause after")).getByRole("button", { name: "Revert to default" }));
    expect(within(bar).getByRole("button", { name: "Review" })).toBeDisabled();
    await user.click(within(field("API rate")).getByRole("button", { name: "Undo change" }));
    expect(within(bar).getByRole("button", { name: "Review" })).toBeEnabled();
    expect(api.callsTo("PATCH /v1/admin/settings")).toHaveLength(0);
  });

  it("Review opens the diff, Apply sends one PATCH with If-Match", async () => {
    const user = userEvent.setup();
    const { api } = view({
      "PATCH /v1/admin/settings": {
        body: { revision: 42, changed: ["OHS_WEBHOOK_PAUSE_AFTER", "OHS_WEBHOOK_TIMEOUT"], restart_required: ["OHS_WEBHOOK_TIMEOUT"] },
      },
    });
    await editTwo(user);
    await user.click(screen.getByRole("button", { name: "Review" }));
    const dialog = screen.getByRole("dialog", { name: "Review changes" });
    const pause = within(dialog).getByRole("row", { name: /OHS_WEBHOOK_PAUSE_AFTER/ });
    expect(within(pause).getByText("8")).toBeInTheDocument();
    expect(within(pause).getByText("9")).toBeInTheDocument();
    expect(within(pause).getByText("Live")).toBeInTheDocument();
    const timeout = within(dialog).getByRole("row", { name: /OHS_WEBHOOK_TIMEOUT/ });
    expect(within(timeout).getByText("10s")).toBeInTheDocument();
    expect(within(timeout).getByText("20s")).toBeInTheDocument();
    expect(within(timeout).getByText("Restart")).toBeInTheDocument();
    expect(api.callsTo("PATCH /v1/admin/settings")).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    const calls = api.callsTo("PATCH /v1/admin/settings");
    expect(calls).toHaveLength(1);
    expect(calls[0]?.headers["if-match"]).toBe('"41"');
    expect(calls[0]?.body).toEqual({ OHS_WEBHOOK_PAUSE_AFTER: "9", OHS_WEBHOOK_TIMEOUT: "20s" });
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).not.toBeInTheDocument();
    const notice = screen.getByRole("status", { name: "Restart needed" });
    expect(within(notice).getByText("OHS_WEBHOOK_TIMEOUT")).toBeInTheDocument();
    expect(notice).toHaveTextContent(
      "They apply when each process next starts. Restart with your own tooling (Docker, systemd, Kubernetes).",
    );
    expect(screen.queryByRole("button", { name: /restart/i })).not.toBeInTheDocument();
  });

  it("revert to default sends null", async () => {
    const user = userEvent.setup();
    const { api } = view({ "PATCH /v1/admin/settings": { body: { revision: 42, changed: ["OHS_WEBHOOK_PAUSE_AFTER"], restart_required: [] } } });
    await user.click(within(await loaded()).getByRole("button", { name: "Revert to default" }));
    expect(within(field("Webhook timeout")).queryByRole("button", { name: "Revert to default" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Review" }));
    const dialog = screen.getByRole("dialog", { name: "Review changes" });
    expect(within(dialog).getByText("default (5)")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    await waitFor(() => {
      expect(api.callsTo("PATCH /v1/admin/settings")).toHaveLength(1);
    });
    expect(api.callsTo("PATCH /v1/admin/settings")[0]?.body).toEqual({ OHS_WEBHOOK_PAUSE_AFTER: null });
  });

  it("412 reloads and reopens the diff against the new values", async () => {
    const user = userEvent.setup();
    let patches = 0;
    const moved = SETTINGS.map((x) => (x.name === "OHS_WEBHOOK_PAUSE_AFTER" ? { ...x, value: "7" } : x));
    const { api } = view({
      // The revision moves under the first PATCH.
      "GET /v1/admin/settings": () => (patches === 0 ? { body: list(41) } : { body: list(42, moved) }),
      "PATCH /v1/admin/settings": () => {
        patches += 1;
        return patches === 1
          ? { status: 412, body: { title: "Precondition Failed", detail: "the settings changed since you read them: read them again, then retry" }, headers: { ETag: '"42"' } }
          : { body: { revision: 43, changed: ["OHS_WEBHOOK_PAUSE_AFTER"], restart_required: [] } };
      },
    });
    const pause = within(await loaded()).getByLabelText("Pause after");
    await user.clear(pause);
    await user.type(pause, "9");
    await user.click(screen.getByRole("button", { name: "Review" }));
    await user.click(within(screen.getByRole("dialog", { name: "Review changes" })).getByRole("button", { name: "Apply" }));
    const dialog = await screen.findByRole("dialog", { name: "Review changes" });
    expect(await within(dialog).findByText("The settings changed since you opened this review. Here are the new values.")).toBeInTheDocument();
    const row = within(dialog).getByRole("row", { name: /OHS_WEBHOOK_PAUSE_AFTER/ });
    expect(within(row).getByText("7")).toBeInTheDocument();
    expect(within(row).getByText("9")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    await waitFor(() => {
      expect(api.callsTo("PATCH /v1/admin/settings")).toHaveLength(2);
    });
    expect(api.callsTo("PATCH /v1/admin/settings")[0]?.headers["if-match"]).toBe('"41"');
    expect(api.callsTo("PATCH /v1/admin/settings")[1]?.headers["if-match"]).toBe('"42"');
  });

  it("422 marks each invalid field and saves nothing", async () => {
    const user = userEvent.setup();
    view({
      "PATCH /v1/admin/settings": {
        status: 422,
        body: {
          title: "Unprocessable Entity",
          detail: "these settings were refused",
          settings: [
            { name: "OHS_WEBHOOK_TIMEOUT", message: 'OHS_WEBHOOK_TIMEOUT: "20s" is longer than 15s' },
            { name: "OHS_WEBHOOK_PAUSE_AFTER", message: "OHS_WEBHOOK_PAUSE_AFTER must stay below the retry count" },
          ],
        },
      },
    });
    await editTwo(user);
    await user.click(screen.getByRole("button", { name: "Review" }));
    await user.click(within(screen.getByRole("dialog", { name: "Review changes" })).getByRole("button", { name: "Apply" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(within(field("Webhook timeout")).getByText('OHS_WEBHOOK_TIMEOUT: "20s" is longer than 15s')).toBeInTheDocument();
    expect(within(field("Pause after")).getByText("OHS_WEBHOOK_PAUSE_AFTER must stay below the retry count")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Unsaved changes" })).getByText("2 unsaved changes")).toBeInTheDocument();
    expect(within(field("Webhook timeout")).getByLabelText("Webhook timeout")).toHaveValue("20s");
  });

  it("409 names the locked settings", async () => {
    const user = userEvent.setup();
    view({
      "PATCH /v1/admin/settings": {
        status: 409,
        body: {
          title: "Conflict",
          detail: "these settings are not the console's to change",
          settings: [{ name: "OHS_WEBHOOK_TIMEOUT", message: "env: OHS_WEBHOOK_TIMEOUT is set in the environment" }],
        },
      },
    });
    await editTwo(user);
    await user.click(screen.getByRole("button", { name: "Review" }));
    const dialog = screen.getByRole("dialog", { name: "Review changes" });
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "These settings are not the console's to change: OHS_WEBHOOK_TIMEOUT. Nothing was saved.",
    );
  });

  it("a typed secret is never echoed", async () => {
    const user = userEvent.setup();
    let patches = 0;
    const { api, queryClient } = view({
      "GET /v1/admin/settings/export": { body: "# Metrics\nOHS_METRICS_TOKEN=\n", headers: { "Content-Disposition": 'attachment; filename="ohs.env"' } },
      "PATCH /v1/admin/settings": () => {
        patches += 1;
        return patches === 1
          ? { status: 422, body: { title: "Unprocessable Entity", detail: "x", settings: [{ name: "OHS_METRICS_TOKEN", message: "OHS_METRICS_TOKEN: too short" }] } }
          : { body: { revision: 42, changed: ["OHS_METRICS_TOKEN"], restart_required: ["OHS_METRICS_TOKEN"] } };
      },
    });
    await loaded();
    const f = field("Metrics token");
    expect(within(f).getByText("not set", { selector: ".set-secret-state" })).toBeInTheDocument();
    await user.click(within(f).getByRole("button", { name: "Replace" }));
    const input = within(f).getByLabelText("Metrics token");
    expect(input).toHaveValue("");
    expect(input).toHaveAttribute("type", "password");
    await user.type(input, SECRET);

    await user.click(screen.getByRole("button", { name: "Review" }));
    const dialog = screen.getByRole("dialog", { name: "Review changes" });
    expect(within(dialog).getByText("new value")).toBeInTheDocument();
    expect(dialog.textContent).not.toContain(SECRET);
    const inspector = screen.getByRole("complementary", { name: "Inspector" });
    expect(inspector.textContent).not.toContain(SECRET);

    await user.click(within(dialog).getByRole("button", { name: "Apply" }));
    expect(await within(field("Metrics token")).findByText("OHS_METRICS_TOKEN: too short")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(SECRET);
    await waitFor(() => {
      expect(mutationsHold(queryClient, SECRET)).toBe(false);
    });

    await user.click(screen.getByRole("button", { name: "Copy as .env" }));
    await waitFor(async () => {
      expect(await navigator.clipboard.readText()).toBe("# Metrics\nOHS_METRICS_TOKEN=\n");
    });
    expect(await navigator.clipboard.readText()).not.toContain(SECRET);

    await user.click(screen.getByRole("button", { name: "Review" }));
    await user.click(within(screen.getByRole("dialog", { name: "Review changes" })).getByRole("button", { name: "Apply" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(api.callsTo("PATCH /v1/admin/settings")[1]?.body).toEqual({ OHS_METRICS_TOKEN: SECRET });
    expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument();
    expect(within(field("Metrics token")).queryByLabelText("Metrics token")).toBeNull();
    expect(document.body.textContent).not.toContain(SECRET);
    await waitFor(() => {
      expect(mutationsHold(queryClient, SECRET)).toBe(false);
    });
  });

  it("pending changes survive a 401 round trip through sign in", async () => {
    const user = userEvent.setup();
    let expired = false;
    const { queryClient, path } = view({
      "GET /v1/admin/settings": () => (expired ? { status: 401, body: { title: "Unauthorized", detail: "session ended" } } : { body: list() }),
      "POST /console/session": () => {
        expired = false;
        return { status: 201, body: admin };
      },
    });
    const pause = within(await loaded()).getByLabelText("Pause after");
    await user.clear(pause);
    await user.type(pause, "9");
    expired = true;
    await queryClient.invalidateQueries({ queryKey: ["settings"] });
    await waitFor(() => {
      expect(path()).toBe("/console/sign-in");
    });
    await user.type(await screen.findByLabelText("Tenant API key"), KEY);
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => {
      expect(path()).toBe("/console/settings");
    });
    expect(await within(await loaded()).findByDisplayValue("9")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Unsaved changes" })).getByText("1 unsaved change")).toBeInTheDocument();
  });

  it("the banner lists processes that are behind or stale", async () => {
    view({
      "GET /v1/admin/processes": {
        body: {
          revision: 41,
          data: [
            proc({ host: "web-1", roles: ["api", "collect"], behind: true }),
            proc({ id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", host: "worker-2", roles: ["etl"], version: "1.3.9", stale: true, skipped: ["OHS_RETENTION_DAYS"], reload_error: "settings: read failed" }),
            proc({ id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", host: "fine-3" }),
          ],
        },
      },
    });
    const banner = await screen.findByRole("region", { name: "Processes not on the current settings" });
    const rows = within(banner).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0] as HTMLElement).getByText("web-1")).toBeInTheDocument();
    expect(within(rows[0] as HTMLElement).getByText("api, collect")).toBeInTheDocument();
    expect(within(rows[0] as HTMLElement).getByText("behind")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("worker-2")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("1.3.9")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("stale")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("OHS_RETENTION_DAYS")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("settings: read failed")).toBeInTheDocument();
    expect(within(banner).queryByText("fine-3")).not.toBeInTheDocument();
  });

  it("no banner when every process is current", async () => {
    view({ "GET /v1/admin/processes": { body: { revision: 41, data: [proc({})] } } });
    await loaded();
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: "Processes not on the current settings" })).not.toBeInTheDocument();
    });
  });

  it("Copy as .env copies GET /v1/admin/settings/export; Export downloads it as ohs.env", async () => {
    const user = userEvent.setup();
    const text = "# Server\nOHS_PUBLIC_URL=http://localhost:8080\n";
    const saved: string[] = [];
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: () => "blob:fake" });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: () => undefined });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      saved.push(this.download);
    });
    const { api } = view({
      "GET /v1/admin/settings/export": { body: text, headers: { "Content-Disposition": 'attachment; filename="ohs.env"' } },
    });
    await loaded();
    await user.click(screen.getByRole("button", { name: "Copy as .env" }));
    await waitFor(async () => {
      expect(await navigator.clipboard.readText()).toBe(text);
    });
    await user.click(screen.getByRole("button", { name: "Export" }));
    await waitFor(() => {
      expect(saved).toEqual(["ohs.env"]);
    });
    expect(api.callsTo("GET /v1/admin/settings/export")).toHaveLength(2);
  });

  it("a tenant that is not an admin gets an explanation, not an empty page", async () => {
    const { api } = view({}, ROUTE, notAdmin);
    expect(await screen.findByRole("heading", { name: "Settings", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("Settings belong to the instance. Sign in with a key of an instance admin tenant.")).toBeInTheDocument();
    expect(api.calls.filter((c) => c.path.startsWith("/v1/admin/settings"))).toHaveLength(0);
    expect(api.callsTo("GET /v1/admin/processes")).toHaveLength(0);
  });

  it("a read only session can read but not edit", async () => {
    view({}, ROUTE, readOnly);
    const p = await loaded();
    expect(within(p).getByLabelText("Pause after")).toBeDisabled();
    expect(within(p).queryByRole("button", { name: "Revert to default" })).not.toBeInTheDocument();
    expect(within(field("Metrics token")).queryByRole("button", { name: "Replace" })).not.toBeInTheDocument();
    expect(within(field("Consent required")).getByLabelText("Consent required")).toBeDisabled();
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Review" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy as .env" })).toBeEnabled();
  });
});
