import { legacyHashPath } from "./legacyHash";

describe("legacyHashPath", () => {
  it("maps the v0.2.0 hash routes to the path routes", () => {
    expect(legacyHashPath("#/users/u1")).toBe("/console/users/u1");
    expect(legacyHashPath("#/providers/oura")).toBe("/console/providers/oura");
    expect(legacyHashPath("#/webhooks/w1")).toBe("/console/webhooks/w1");
    expect(legacyHashPath("#/audit")).toBe("/console/audit");
  });

  it("leaves every other hash alone", () => {
    expect(legacyHashPath("")).toBeNull();
    expect(legacyHashPath("#")).toBeNull();
    expect(legacyHashPath("#section")).toBeNull();
    expect(legacyHashPath("#//evil.example/x")).toBeNull();
    expect(legacyHashPath("#/")).toBeNull();
  });
});
