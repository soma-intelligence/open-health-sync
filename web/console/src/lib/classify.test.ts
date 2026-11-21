import {
  classifyConnection,
  classifyDelivery,
  classifyFreshness,
  classifyProvider,
  classifyRun,
  classifyUser,
  classifyWebhook,
  isCloud,
  providerStatusLabel,
  webhookLabel,
} from "./classify";

test("classifyFreshness: stale, failing and needs_reauth are red", () => {
  expect(classifyFreshness({ state: "fresh", consecutive_failures: 0 })).toBe("ok");
  expect(classifyFreshness({ state: "fresh", consecutive_failures: 1 })).toBe("warn");
  expect(classifyFreshness({ state: "syncing" })).toBe("warn");
  expect(classifyFreshness({ state: "stale" })).toBe("bad");
  expect(classifyFreshness({ state: "failing" })).toBe("bad");
  expect(classifyFreshness({ state: "needs_reauth" })).toBe("bad");
  expect(classifyFreshness({ state: "disconnected" })).toBe("off");
  expect(classifyFreshness({ state: "something new" })).toBe("warn");
});

test("webhookLabel agrees with the endpoint's color", () => {
  expect(webhookLabel({ status: "active", failing_since: null })).toBe("Active");
  expect(webhookLabel({ status: "active", failing_since: "2026-10-13T09:00:00Z" })).toBe("Failing");
  expect(webhookLabel({ status: "disabled", failing_since: "2026-10-13T09:00:00Z" })).toBe("Turned off");
});

test("classifyConnection, classifyWebhook, classifyDelivery", () => {
  expect(classifyConnection({ status: "active" })).toBe("ok");
  expect(classifyConnection({ status: "needs_reauth" })).toBe("bad");
  expect(classifyConnection({ status: "disconnected" })).toBe("off");

  expect(classifyWebhook({ status: "active", failing_since: null })).toBe("ok");
  expect(classifyWebhook({ status: "active", failing_since: "2026-10-13T00:00:00Z" })).toBe("bad");
  expect(classifyWebhook({ status: "disabled", failing_since: null })).toBe("bad");

  expect(classifyDelivery({ status: "done", attempts: 1 })).toBe("ok");
  expect(classifyDelivery({ status: "pending", attempts: 0 })).toBe("ok");
  expect(classifyDelivery({ status: "pending", attempts: 2, last_error: "HTTP 500" })).toBe("warn");
  expect(classifyDelivery({ status: "parked", attempts: 8 })).toBe("bad");
});

test("classifyProvider, providerStatusLabel and classifyRun", () => {
  expect(classifyProvider({ status: "active" })).toBe("ok");
  expect(classifyProvider({ status: "built_in" })).toBe("ok");
  expect(classifyProvider({ status: "error" })).toBe("bad");
  expect(classifyProvider({ status: "not_configured" })).toBe("off");
  expect(classifyProvider({ status: "off" })).toBe("off");
  expect(providerStatusLabel("not_configured")).toBe("Not configured");
  expect(providerStatusLabel("new_one")).toBe("new_one");
  expect(classifyRun({ status: "failed" })).toBe("bad");
  expect(classifyRun({ status: "rate_limited" })).toBe("warn");
  expect(classifyRun({ status: "succeeded" })).toBe("ok");
  expect(classifyRun({ status: "cancelled" })).toBe("off");
});

test("classifyUser and isCloud", () => {
  expect(classifyUser({ status: "active" })).toBe("ok");
  expect(classifyUser({ status: "deleting" })).toBe("off");
  expect(isCloud({ kinds: ["oauth"] })).toBe(true);
  expect(isCloud({ kinds: ["push", "device"] })).toBe(true);
  expect(isCloud({ kinds: ["device"] })).toBe(false);
  expect(isCloud(null)).toBe(false);
});
