import { KEY_SCOPES, fullAccessScopes, grantable, groupScopes, heldBy, impliedScopes, readOnlyScopes, scopeLabel } from "./scopes";

describe("scopes", () => {
  it("groups by resource with * first", () => {
    const groups = groupScopes(KEY_SCOPES);
    expect(groups[0]).toEqual({ resource: "*", scopes: ["*"] });
    expect(groups.map((g) => g.resource)).toEqual([
      "*", "tenant", "users", "tokens", "data", "connections", "webhooks", "exports", "keys", "audit", "admin",
    ]);
    expect(groups.find((g) => g.resource === "data")?.scopes).toEqual(["data:read", "data:write", "data:delete"]);
    expect(groupScopes(["users:write", "*", "data:read"]).map((g) => g.resource)).toEqual(["*", "users", "data"]);
  });

  it("a :write scope implies its :read in the picker", () => {
    expect(impliedScopes(["users:write"])).toEqual(new Set(["users:write", "users:read"]));
    expect(impliedScopes(["data:delete"])).toEqual(new Set(["data:delete"]));
    expect(impliedScopes(["*"]).size).toBe(KEY_SCOPES.length);
  });

  it("a key holds what its scopes grant", () => {
    expect(heldBy(["*"], "audit:read")).toBe(true);
    expect(heldBy(["keys:write"], "keys:read")).toBe(true);
    expect(heldBy(["keys:read"], "keys:write")).toBe(false);
  });

  it("the read only preset holds only :read scopes the session may give", () => {
    expect(readOnlyScopes(["*"], false)).not.toContain("admin:read");
    expect(readOnlyScopes(["*"], true)).toContain("admin:read");
    expect(readOnlyScopes(["*"], true).every((s) => s.endsWith(":read"))).toBe(true);
    expect(readOnlyScopes(["users:write", "data:delete"], false)).toEqual(["users:read"]);
    expect(readOnlyScopes(["tokens:write"], false)).toEqual([]);
  });

  it("the full access preset names the session's scopes, never an empty list", () => {
    expect(fullAccessScopes(["*"])).toEqual(["*"]);
    expect(fullAccessScopes(["data:read", "users:write"])).toEqual(["users:write", "data:read"]);
  });

  it("admin scopes are grantable only by an instance admin", () => {
    expect(grantable(["*"], false, "admin:read")).toBe(false);
    expect(grantable(["*"], true, "admin:write")).toBe(true);
    expect(grantable(["users:read"], true, "users:write")).toBe(false);
  });

  it("* is labelled in words", () => {
    expect(scopeLabel("*")).toBe("All scopes (*)");
    expect(scopeLabel("data:read")).toBe("data:read");
  });
});
