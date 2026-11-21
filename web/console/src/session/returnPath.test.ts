import { safeReturnPath } from "./returnPath";

describe("safeReturnPath", () => {
  it("keeps a console path", () => {
    expect(safeReturnPath("/console/users/u1")).toBe("/console/users/u1");
    expect(safeReturnPath("/console/users?limit=5")).toBe("/console/users?limit=5");
  });

  it.each(["/console/sign-in", "/v1/users", "//evil.example", "https://evil.example/console/x", 42, undefined, "/console/../v1/users", "/\\evil.example", ""])(
    "sends %j to the overview",
    (from) => {
      expect(safeReturnPath(from)).toBe("/console/overview");
    },
  );
});
