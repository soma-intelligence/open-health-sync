import type { Setting } from "../api/types";
import { paletteItems, searchPalette, type PaletteItem } from "./search";

function s(name: string, label: string, section: string): Setting {
  return {
    name,
    label,
    section,
    help: "",
    type: "string",
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
  };
}

const SETTINGS = [
  s("OHS_LOG_LEVEL", "Log level", "Server"),
  s("OHS_WEBHOOK_PAUSE_AFTER", "Pause after failures", "Webhooks"),
];

const ctx = { admin: true, canWrite: true, settings: SETTINGS };
const titles = (items: readonly PaletteItem[]) => items.map((i) => i.title);

function item(group: PaletteItem["group"], title: string, hint?: string): PaletteItem {
  return hint === undefined ? { group, title } : { group, title, hint };
}

describe("paletteItems", () => {
  it("empty query lists the pages, then the actions", () => {
    const all = paletteItems(ctx);
    const found = searchPalette(all, "");
    expect(titles(found)).toEqual([
      "Overview",
      "Users",
      "Providers",
      "Jobs",
      "Webhooks",
      "API keys",
      "Audit log",
      "Settings",
      "Create user",
      "Create API key",
      "Add webhook endpoint",
      "Switch theme",
      "Sign out",
    ]);
    expect(found.slice(0, 8).every((i) => i.group === "Go to")).toBe(true);
    expect(found.slice(8).every((i) => i.group === "Actions")).toBe(true);
    expect(all.find((i) => i.title === "Create user")).toMatchObject({ to: "/console/users", open: "create" });
    expect(all.find((i) => i.title === "Create API key")).toMatchObject({ to: "/console/keys", open: "create" });
    expect(all.find((i) => i.title === "Add webhook endpoint")).toMatchObject({ to: "/console/webhooks", open: "create" });
    expect(all.find((i) => i.title === "Switch theme")).toMatchObject({ run: "theme" });
    expect(all.find((i) => i.title === "Sign out")).toMatchObject({ run: "sign-out" });
  });

  it("ranks a prefix of the title or hint before a word prefix before a substring, case insensitive, keeping order on ties", () => {
    const items = [
      item("Go to", "xxlogxx"),
      item("Go to", "my log"),
      item("Go to", "Log b"),
      item("Go to", "other", "Log hint"),
      item("Go to", "Log a"),
    ];
    expect(titles(searchPalette(items, "LOG"))).toEqual(["Log b", "other", "Log a", "my log", "xxlogxx"]);
  });

  it("finds a setting by name or by label", () => {
    const all = paletteItems(ctx);
    expect(titles(searchPalette(all, "OHS_LOG"))).toContain("OHS_LOG_LEVEL");
    const pause = searchPalette(all, "pause after");
    expect(pause[0]).toMatchObject({
      group: "Settings",
      title: "OHS_WEBHOOK_PAUSE_AFTER",
      to: "/console/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER",
    });
    expect(titles(searchPalette(all, "log level"))).toContain("OHS_LOG_LEVEL");
  });

  it("webhook lists the Webhooks page first", () => {
    const found = searchPalette(paletteItems(ctx), "webhook");
    expect(found[0]).toMatchObject({ group: "Go to", title: "Webhooks" });
  });

  it("no Settings items for a tenant that is not an admin; no write actions when read only", () => {
    const tenant = paletteItems({ admin: false, canWrite: true, settings: SETTINGS });
    expect(tenant.some((i) => i.group === "Settings")).toBe(false);
    expect(titles(tenant)).not.toContain("Settings");
    const ro = paletteItems({ admin: true, canWrite: false, settings: SETTINGS });
    expect(ro.some((i) => i.open === "create")).toBe(false);
    expect(titles(ro)).toContain("Switch theme");
    expect(titles(ro)).toContain("Sign out");
  });

  it("limits the answer", () => {
    expect(searchPalette(paletteItems(ctx), "", 3)).toHaveLength(3);
    expect(searchPalette(paletteItems(ctx), "o").length).toBeLessThanOrEqual(14);
  });
});
