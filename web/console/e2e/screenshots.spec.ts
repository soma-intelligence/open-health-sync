import { resolve } from "node:path";

import { expect, test } from "@playwright/test";

// Writes the pictures docs/console.md shows. Off in a normal run: set OHS_E2E_SHOTS=1.
test.skip(process.env["OHS_E2E_SHOTS"] !== "1", "OHS_E2E_SHOTS is not set");

const out = resolve(import.meta.dirname, "..", "..", "..", "docs", "assets", "console");

for (const scheme of ["light", "dark"] as const) {
  test(`takes the ${scheme} screenshots`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.emulateMedia({ colorScheme: scheme });

    const key = process.env["OHS_E2E_ADMIN_KEY"] ?? "";
    await page.goto("/console/sign-in");
    await page.getByLabel("Tenant API key").fill(key);
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page).not.toHaveURL(/\/console\/sign-in/);

    await page.goto("/console/overview");
    await page.getByRole("group", { name: "Theme" }).getByRole("button", { name: scheme === "light" ? "Light" : "Dark" }).click();
    await expect(page.getByRole("region", { name: "Pipeline, live" })).toBeVisible();
    await page.waitForLoadState("networkidle");
    await page.screenshot({ path: resolve(out, `overview-${scheme}.png`) });

    await page.goto("/console/users");
    await page.getByRole("row", { name: /demo-user/ }).first().getByRole("link").click();
    await expect(page.getByRole("heading", { name: "demo-user", level: 1 })).toBeVisible();
    await page.waitForLoadState("networkidle");
    await page.screenshot({ path: resolve(out, `user-${scheme}.png`) });
  });
}
