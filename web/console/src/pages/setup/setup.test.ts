import { MAX_TENANT_NAME, SETUP_PATH, SetupError, buildSetupRequest, isApiKey, setupMessage, setupState } from "./setup";

const KEY = "ohs_sk_" + "A".repeat(43);

describe("setup helpers", () => {
  it("pins the path and the name limit", () => {
    expect(SETUP_PATH).toBe("/v1/setup");
    expect(MAX_TENANT_NAME).toBe(100);
  });

  it("buildSetupRequest trims, leaves an empty name out and refuses bad values", () => {
    expect(buildSetupRequest("  tok  ", "")).toEqual({ setup_token: "tok" });
    expect(buildSetupRequest("tok", "  Home ")).toEqual({ setup_token: "tok", tenant_name: "Home" });
    expect(() => buildSetupRequest("   ", "x")).toThrow(SetupError);
    expect(() => buildSetupRequest("tok", "x".repeat(101))).toThrow(SetupError);
    expect(() => buildSetupRequest("tok", "x".repeat(100))).not.toThrow();
    // The limit is in bytes.
    expect(() => buildSetupRequest("tok", "é".repeat(51))).toThrow(SetupError);
    expect(() => buildSetupRequest("tok", "a\nb")).toThrow(SetupError);
    expect(() => buildSetupRequest("tok", "a\u0085b")).toThrow(SetupError);
  });

  it("isApiKey accepts a tenant key only", () => {
    expect(isApiKey(KEY)).toBe(true);
    for (const v of ["", "ohs_sk_", "ohs_ut_" + "A".repeat(43), KEY + "<b>", " " + KEY, null, 42, {}]) {
      expect(isApiKey(v)).toBe(false);
    }
  });

  it("setupState", () => {
    expect(setupState(200, { needed: true })).toBe("needed");
    expect(setupState(200, { needed: false })).toBe("done");
    expect(setupState(404, null)).toBe("off");
    expect(() => setupState(200, { needed: "yes" })).toThrow(SetupError);
    expect(() => setupState(500, null)).toThrow(SetupError);
  });

  it("setupMessage names the cause", () => {
    expect(setupMessage(401, "")).toMatch(/token was refused/);
    expect(setupMessage(409, "")).toMatch(/already set up/);
    expect(setupMessage(404, "")).toMatch(/off/);
    expect(setupMessage(400, "tenant_name must not contain control characters")).toMatch(/control characters/);
    expect(setupMessage(503, "")).toMatch(/HTTP 503/);
  });
});
