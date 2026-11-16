import { confirmMatches } from "./confirm";

test("confirmMatches", () => {
  expect(confirmMatches("oura", "oura")).toBe(true);
  expect(confirmMatches(" oura ", "oura")).toBe(true);
  expect(confirmMatches("Oura", "oura")).toBe(false);
  expect(confirmMatches("", "")).toBe(false);
  expect(confirmMatches(undefined, "x")).toBe(false);
});
