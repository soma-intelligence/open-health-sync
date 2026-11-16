import { KEY_SCOPES, groupScopes, heldBy, impliedScopes } from "./scopes";

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
});
