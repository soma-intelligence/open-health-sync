import { apiPath, filenameFrom } from "./paths";

describe("apiPath", () => {
  it("escapes ids and drops empty query values", () => {
    expect(apiPath(["v1", "users", "a/b?c"])).toBe("/v1/users/a%2Fb%3Fc");
    expect(apiPath(["v1", "users", "u1", "connections"])).toBe("/v1/users/u1/connections");
    expect(apiPath(["v1", "users"], { limit: 50, cursor: null })).toBe("/v1/users?limit=50");
    expect(apiPath(["v1", "users"], { cursor: "a&b" })).toBe("/v1/users?cursor=a%26b");
  });
});

describe("filenameFrom", () => {
  it("keeps only characters safe in a file name", () => {
    expect(filenameFrom('attachment; filename="a b/../c.zip"')).toBe("a_b_.._c.zip");
    expect(filenameFrom(null)).toBe("download");
    expect(filenameFrom('attachment; filename=".."')).toBe("download");
    expect(filenameFrom("attachment; filename=x.json")).toBe("x.json");
  });
});
