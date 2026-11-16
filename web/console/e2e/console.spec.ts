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
  await page.getByRole("row", { name: /demo-user/ }).first().dblclick();
  await expect(page).toHaveURL(/\/console\/users\/[^/]+/);
  await expectPage(page, "demo-user");
  for (const tab of ["Data", "Connections", "Consents", "Sync runs", "Shares"]) {
    await page.getByRole("tab", { name: tab }).click();
    await expectPage(page, "demo-user");
  }

  await nav.getByRole("link", { name: "Providers" }).click();
  await expectPage(page, "Providers");
  await page.getByRole("table").first().getByRole("row").nth(1).dblclick();
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

  await picker.selectOption({ label: "e2e-admin" });
  await expect(page.getByText("Acting as e2e-other")).toHaveCount(0);
  await page.getByRole("navigation", { name: "Console" }).getByRole("link", { name: "Users" }).click();
  await expect(page.getByRole("row", { name: /demo-user/ }).first()).toBeVisible();
});
