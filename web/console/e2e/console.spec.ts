import { expect, test, type Page } from "@playwright/test";

async function signIn(page: Page): Promise<void> {
  const key = process.env["OHS_E2E_ADMIN_KEY"] ?? "";
  expect(key).not.toBe("");
  await page.goto("/console/sign-in");
  await page.getByLabel("Tenant API key").fill(key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/\/console\/sign-in/);
}

async function expectPage(page: Page, heading: string | RegExp): Promise<void> {
  await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
  await expect(page.getByText("This page failed")).toHaveCount(0);
  // Pages show a failed query inline, not through the boundary: wait for the loads, then look.
  await expect(page.getByText("Loading", { exact: true })).toHaveCount(0);
  await expect(page.locator(".error-panel")).toHaveCount(0);
}

test("signs in, opens the overview and signs out", async ({ page }) => {
  const key = process.env["OHS_E2E_ADMIN_KEY"] ?? "";
  expect(key).not.toBe("");

  await page.goto("/console/users");
  await expect(page).toHaveURL(/\/console\/sign-in/);

  await page.getByLabel("Tenant API key").fill(key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/console\/users$/);

  await page.getByRole("link", { name: "Overview" }).click();
  await expect(page).toHaveURL(/\/console\/overview$/);
  await expect(page.getByRole("region", { name: "Pipeline, live" })).toBeVisible();

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/console\/sign-in/);

  await page.goto("/console/overview");
  await expect(page).toHaveURL(/\/console\/sign-in/);
});

test("opens every page", async ({ page }) => {
  await signIn(page);
  const nav = page.getByRole("navigation", { name: "Console" });

  await nav.getByRole("link", { name: "Overview" }).click();
  await expectPage(page, "Overview");

  await nav.getByRole("link", { name: "Users" }).click();
  await expectPage(page, "Users");
  await page.getByRole("row", { name: /demo-user/ }).first().getByRole("link").click();
  await expect(page).toHaveURL(/\/console\/users\/[^/]+/);
  await expectPage(page, "demo-user");
  for (const tab of ["Data", "Connections", "Consents", "Sync runs", "Shares"]) {
    await page.getByRole("tab", { name: tab }).click();
    await expectPage(page, "demo-user");
  }

  await nav.getByRole("link", { name: "Providers" }).click();
  await expectPage(page, "Providers");
  await page.getByRole("table").first().getByRole("row").nth(1).getByRole("link").click();
  await expect(page).toHaveURL(/\/console\/providers\/[^/]+/);
  await expectPage(page, /.+/);

  await nav.getByRole("link", { name: "Jobs" }).click();
  await expectPage(page, "Jobs");
  await nav.getByRole("link", { name: "Webhooks" }).click();
  await expectPage(page, "Webhooks");
  await nav.getByRole("link", { name: "API keys" }).click();
  await expectPage(page, "API keys");
  await nav.getByRole("link", { name: "Audit log" }).click();
  await expectPage(page, "Audit log");
  await nav.getByRole("link", { name: "Settings" }).click();
  await expectPage(page, "Settings");
});

test("switches tenant and back", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/users");
  await expectPage(page, "Users");
  await expect(page.getByRole("row", { name: /demo-user/ }).first()).toBeVisible();

  const picker = page.getByLabel("Acting tenant");
  await picker.selectOption({ label: "e2e-other" });
  await expect(page.getByText("Acting as e2e-other")).toBeVisible();
  await page.getByRole("navigation", { name: "Console" }).getByRole("link", { name: "Users" }).click();
  await expectPage(page, "Users");
  await expect(page.getByText("No users yet")).toBeVisible();
  // /v1/admin/* authorizes on the session tenant: Settings still opens while acting as a non-admin.
  await page.getByRole("navigation", { name: "Console" }).getByRole("link", { name: "Settings" }).click();
  await expectPage(page, "Settings");
  await expect(page.getByText("Acting as e2e-other")).toBeVisible();

  await picker.selectOption({ label: "e2e-admin" });
  await expect(page.getByText("Acting as e2e-other")).toHaveCount(0);
  await page.getByRole("navigation", { name: "Console" }).getByRole("link", { name: "Users" }).click();
  await expect(page.getByRole("row", { name: /demo-user/ }).first()).toBeVisible();
});

test("an old #/ link from v0.2.0 opens the same page", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/users");
  await page.getByRole("row", { name: /demo-user/ }).first().getByRole("link").click();
  await expect(page).toHaveURL(/\/console\/users\/[^/]+$/);
  const userPath = new URL(page.url()).pathname;
  await page.goto("/console/#" + userPath.slice("/console".length));
  await expect.poll(() => new URL(page.url()).pathname + new URL(page.url()).hash).toBe(userPath);
  await expectPage(page, "demo-user");
});
