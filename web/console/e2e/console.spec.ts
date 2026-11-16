import { expect, test } from "@playwright/test";

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
