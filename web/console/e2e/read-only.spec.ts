import { expect, test } from "@playwright/test";

test("a read only session cannot write", async ({ page }) => {
  const base = process.env["OHS_E2E_RO_URL"] ?? "";
  const key = process.env["OHS_E2E_ADMIN_KEY"] ?? "";
  expect(base).not.toBe("");
  expect(key).not.toBe("");

  await page.goto(`${base}/console/sign-in`);
  await page.getByLabel("Tenant API key").fill(key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/\/console\/sign-in/);

  await page.goto(`${base}/console/users`);
  await expect(page.getByRole("heading", { name: "Users", level: 1 })).toBeVisible();
  await expect(page.getByRole("row").nth(1)).toBeVisible();
  await expect(page.getByRole("button", { name: "New user" })).toHaveCount(0);

  // The page never offers the write, so ask the server directly with a valid token.
  const status = await page.evaluate(async () => {
    const session = (await (await fetch("/console/session")).json()) as { csrf_token: string };
    const res = await fetch("/v1/users", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-OHS-CSRF": session.csrf_token },
      body: JSON.stringify({ external_ref: "e2e-read-only" }),
    });
    return res.status;
  });
  expect(status).toBe(403);
});
