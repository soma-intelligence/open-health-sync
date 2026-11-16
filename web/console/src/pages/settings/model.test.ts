import { ApiError, type ApiResponse } from "../../api/client";
import type { Setting } from "../../api/types";
import {
  EMPTY_TYPED,
  applyOutcome,
  diffRows,
  fieldState,
  prunePending,
  restartCount,
  retentionWarnings,
  sectionSlug,
  settingPath,
  toPatch,
} from "./model";

function setting(over: Partial<Setting> & Pick<Setting, "name">): Setting {
  return {
    label: over.name,
    section: "Server",
    help: "",
    type: "int",
    min: null,
    max: null,
    values: null,
    default: "10",
    default_note: null,
    secret: false,
    apply: "live",
    source: "default",
    value: "10",
    stored_value: null,
    invalid: null,
    ...over,
  };
}

function ok(body: unknown): ApiResponse<unknown> {
  return { data: body, status: 200, headers: new Headers() };
}

describe("sectionSlug", () => {
  it("lower cases and hyphenates", () => {
    expect(sectionSlug("Event stream")).toBe("event-stream");
    expect(sectionSlug("Rate limits")).toBe("rate-limits");
    expect(sectionSlug("Server")).toBe("server");
  });

  it("settingPath joins section and name", () => {
    expect(settingPath(setting({ name: "OHS_WEBHOOK_PAUSE_AFTER", section: "Webhooks" }))).toBe(
      "/settings/webhooks/OHS_WEBHOOK_PAUSE_AFTER",
    );
  });
});

describe("fieldState", () => {
  it("locks a field set by the environment", () => {
    const st = fieldState(setting({ name: "OHS_X", source: "env" }), {});
    expect(st.locked).toBe("env");
    expect(st.editable).toBe(false);
  });

  it("locks a bootstrap field, even when the environment sets it", () => {
    expect(fieldState(setting({ name: "OHS_X", apply: "bootstrap" }), {}).locked).toBe("bootstrap");
    const both = fieldState(setting({ name: "OHS_X", apply: "bootstrap", source: "env" }), {});
    expect(both.locked).toBe("bootstrap");
    expect(both.editable).toBe(false);
  });

  it("an editable field is changed when the draft names it", () => {
    const s = setting({ name: "OHS_X" });
    expect(fieldState(s, {})).toEqual({ editable: true, locked: null, changed: false, invalidStored: null, inputError: null });
    expect(fieldState(s, { OHS_X: "11" }).changed).toBe(true);
    expect(fieldState(s, { OHS_X: null }).changed).toBe(true);
  });

  it("an invalid message gives invalidStored", () => {
    expect(fieldState(setting({ name: "OHS_X", invalid: "OHS_X: not a number" }), {}).invalidStored).toBe("OHS_X: not a number");
  });

  it("an int outside its bounds is an input error", () => {
    const s = setting({ name: "OHS_X", min: 1, max: 5 });
    expect(fieldState(s, { OHS_X: "9" }).inputError).toBe("Enter a whole number from 1 to 5.");
    expect(fieldState(s, { OHS_X: "2.5" }).inputError).toBe("Enter a whole number from 1 to 5.");
    expect(fieldState(s, { OHS_X: "3" }).inputError).toBeNull();
  });
});

describe("an emptied typed field blocks Review and is never sent", () => {
  const types = ["int", "duration", "rate", "enum", "bool", "string", "list", "url"] as const;
  it.each(types)("%s: empty is an input error and toPatch throws", (type) => {
    const s = setting({ name: "OHS_X", type });
    expect(fieldState(s, { OHS_X: "" }).inputError).toBe(EMPTY_TYPED);
    expect(EMPTY_TYPED).toBe("Enter a value, or revert to default.");
    expect(() => toPatch({ OHS_X: "" })).toThrow();
  });

  it("an empty secret is blocked too (the page drops it instead of keeping it)", () => {
    const s = setting({ name: "OHS_X", type: "string", secret: true });
    expect(fieldState(s, { OHS_X: "" }).inputError).toBe(EMPTY_TYPED);
  });

  it("toPatch keeps strings and nulls", () => {
    expect(toPatch({ OHS_A: "5", OHS_B: null })).toEqual({ OHS_A: "5", OHS_B: null });
  });
});

describe("retentionWarnings", () => {
  const list: Setting[] = [
    setting({ name: "OHS_RETENTION_DAYS", section: "Retention", default: "0", value: "0" }),
    setting({ name: "OHS_AUDIT_RETENTION_DAYS", section: "Retention", default: "365", value: "0", source: "console" }),
    setting({ name: "OHS_TRACKING_RETENTION_DAYS", section: "Retention", default: "30", value: "30" }),
    setting({ name: "OHS_WEBHOOK_PAUSE_AFTER", section: "Webhooks", default: "0", value: "0" }),
  ];

  it("warns when a retention goes from 0 to more than 0", () => {
    expect(retentionWarnings(list, { OHS_RETENTION_DAYS: "30" })).toEqual([
      "OHS_RETENTION_DAYS will delete data older than 30 days at the next daily cleanup. This cannot be undone.",
    ]);
  });

  it("a revert to a default above 0 warns with the default", () => {
    expect(retentionWarnings(list, { OHS_AUDIT_RETENTION_DAYS: null })).toEqual([
      "OHS_AUDIT_RETENTION_DAYS will delete data older than 365 days at the next daily cleanup. This cannot be undone.",
    ]);
  });

  it("stays quiet otherwise", () => {
    expect(retentionWarnings(list, { OHS_RETENTION_DAYS: "0", OHS_TRACKING_RETENTION_DAYS: "20", OHS_WEBHOOK_PAUSE_AFTER: "3" })).toEqual([]);
    expect(retentionWarnings(list, { OHS_RETENTION_DAYS: "x" })).toEqual([]);
    expect(retentionWarnings(list, {})).toEqual([]);
  });
});

describe("diffRows", () => {
  const list: Setting[] = [
    setting({ name: "OHS_Z", section: "Server", value: "3", source: "console" }),
    setting({ name: "OHS_A", section: "Server", value: null, default: "" }),
    setting({ name: "OHS_W", section: "Webhooks", type: "duration", value: "10m", default: "10m", apply: "restart" }),
    setting({ name: "OHS_D", section: "Server", value: "x", default: "", default_note: "the public URL host" }),
    setting({ name: "OHS_S", section: "Security", type: "string", secret: true, value: "set", default: "" }),
    setting({ name: "OHS_T", section: "Security", type: "string", secret: true, value: null, default: "" }),
  ];

  it("reads before from value and after from the draft, sorted by section then name", () => {
    const rows = diffRows(list, { OHS_W: "20m", OHS_Z: "4", OHS_A: "7" });
    expect(rows.map((r) => r.name)).toEqual(["OHS_A", "OHS_Z", "OHS_W"]);
    expect(rows[0]).toEqual({ name: "OHS_A", label: "OHS_A", apply: "live", before: "not set", after: "7", secret: false });
    expect(rows[1]).toMatchObject({ before: "3", after: "4" });
    expect(rows[2]).toMatchObject({ apply: "restart", before: "10m", after: "20m" });
  });

  it("null after reads the default, or its note when the default is derived", () => {
    const rows = diffRows(list, { OHS_Z: null, OHS_D: null });
    expect(rows.find((r) => r.name === "OHS_Z")?.after).toBe("default (10)");
    expect(rows.find((r) => r.name === "OHS_D")?.after).toBe("default (the public URL host)");
  });

  it("a secret reads set or not set before and new value after, never the typed text", () => {
    const rows = diffRows(list, { OHS_S: "s3cr3t-value", OHS_T: "other-s3cr3t" });
    expect(rows.find((r) => r.name === "OHS_S")).toMatchObject({ before: "set", after: "new value", secret: true });
    expect(rows.find((r) => r.name === "OHS_T")).toMatchObject({ before: "not set", after: "new value", secret: true });
    expect(JSON.stringify(rows)).not.toContain("s3cr3t");
  });

  it("restartCount counts restart rows", () => {
    expect(restartCount(diffRows(list, { OHS_W: "20m", OHS_Z: "4" }))).toBe(1);
    expect(restartCount([])).toBe(0);
  });
});

describe("prunePending after a reload", () => {
  it("drops names now locked and values equal to the new stored value", () => {
    const list: Setting[] = [
      setting({ name: "OHS_ENV", source: "env", value: "1" }),
      setting({ name: "OHS_BOOT", apply: "bootstrap" }),
      setting({ name: "OHS_SAME", source: "console", value: "12" }),
      setting({ name: "OHS_KEEP", source: "console", value: "12" }),
      setting({ name: "OHS_RESET", source: "default", value: "10" }),
    ];
    const out = prunePending(list, { OHS_ENV: "2", OHS_BOOT: "x", OHS_SAME: "12", OHS_KEEP: "13", OHS_RESET: null, OHS_GONE: "1" });
    expect(out.pending).toEqual({ OHS_KEEP: "13" });
    expect([...out.dropped].sort()).toEqual(["OHS_BOOT", "OHS_ENV", "OHS_GONE", "OHS_RESET", "OHS_SAME"]);
  });

  it("never compares a typed secret with the served marker", () => {
    const list = [setting({ name: "OHS_S", type: "string", secret: true, source: "console", value: "set" })];
    expect(prunePending(list, { OHS_S: "set" }).pending).toEqual({ OHS_S: "set" });
  });
});

describe("applyOutcome", () => {
  it("200 is ok with the revision and the restart names", () => {
    expect(applyOutcome(ok({ revision: 6, changed: ["OHS_X", "OHS_Y"], restart_required: ["OHS_X"] }))).toEqual({
      kind: "ok",
      revision: 6,
      restart: ["OHS_X"],
    });
  });

  it("412 is moved", () => {
    expect(applyOutcome(new ApiError(412, "moved", null, { title: "Precondition Failed" }))).toEqual({ kind: "moved" });
  });

  it("409 is a conflict naming the settings", () => {
    const body = { title: "Conflict", detail: "x", settings: [{ name: "OHS_A", message: "env: set" }, { name: "OHS_B", message: "bootstrap: x" }] };
    expect(applyOutcome(new ApiError(409, "x", null, body))).toEqual({ kind: "conflict", names: ["OHS_A", "OHS_B"] });
  });

  it("422 folds the settings into name to message, a cross-setting one included", () => {
    const body = {
      title: "Unprocessable",
      settings: [
        { name: "OHS_A", message: "OHS_A: not an integer" },
        { name: "OHS_B", message: "OHS_B must be below OHS_C" },
      ],
    };
    expect(applyOutcome(new ApiError(422, "x", null, body))).toEqual({
      kind: "invalid",
      errors: { OHS_A: "OHS_A: not an integer", OHS_B: "OHS_B must be below OHS_C" },
    });
  });

  it("any other error is thrown", () => {
    expect(() => applyOutcome(new ApiError(500, "boom", null, null))).toThrow("boom");
  });
});
