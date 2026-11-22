import { isDemoUser } from "./demo";

test("isDemoUser: the default external ids of the demo personas, nothing else", () => {
  for (const ref of ["demo-user", "demo-runner", "demo-sleep", "demo-weight"]) {
    expect(isDemoUser({ external_ref: ref }), ref).toBe(true);
  }
  for (const ref of ["demo", "demo-user-2", "customer-42", "Demo-user"]) {
    expect(isDemoUser({ external_ref: ref }), ref).toBe(false);
  }
  expect(isDemoUser({ external_ref: null })).toBe(false);
});
