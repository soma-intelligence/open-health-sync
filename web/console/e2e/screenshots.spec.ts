import { resolve } from "node:path";

import { expect, test, type Page } from "@playwright/test";

// Writes the pictures docs/console.md shows. Off in a normal run: set OHS_E2E_SHOTS=1.
// OHS_E2E_SHOTS_DIR sends them elsewhere, with the review set: sign in, the new key dialog and Overview at 390 px.
test.skip(process.env["OHS_E2E_SHOTS"] !== "1", "OHS_E2E_SHOTS is not set");

const dir = process.env["OHS_E2E_SHOTS_DIR"];
const out = dir ? resolve(dir) : resolve(import.meta.dirname, "..", "..", "..", "docs", "assets", "console");
const review = dir !== undefined;

/** The page column scrolls inside the shell; for a whole-page picture, let the document scroll instead. */
async function fullShot(page: Page, path: string) {
  // CSSOM writes, not a style tag: the console's CSP refuses inline styles.
  await page.evaluate(() => {
    for (const sel of ["html", "body", "#root", ".app", ".shell"]) {
      document.querySelector<HTMLElement>(sel)?.style.setProperty("height", "auto", "important");
    }
    for (const sel of [".shell-col", ".shell-main"]) {
      document.querySelector<HTMLElement>(sel)?.style.setProperty("overflow", "visible", "important");
    }
  });
  await page.screenshot({ path, fullPage: true });
}

for (const scheme of ["light", "dark"] as const) {
  test(`takes the ${scheme} screenshots`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.emulateMedia({ colorScheme: scheme });

    const key = process.env["OHS_E2E_ADMIN_KEY"] ?? "";
    await page.goto("/console/sign-in");
    await expect(page.getByRole("heading", { name: "Sign in", level: 1 })).toBeVisible();
    if (review) await page.screenshot({ path: resolve(out, `sign-in-${scheme}.png`) });
    await page.getByLabel("Tenant API key").fill(key);
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page).not.toHaveURL(/\/console\/sign-in/);

    await page.goto("/console/overview");
    await page.getByRole("group", { name: "Theme" }).getByRole("button", { name: scheme === "light" ? "Light" : "Dark" }).click();
    await expect(page.getByRole("region", { name: "Pipeline, live" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Needs you" })).toBeVisible();
    await page.waitForLoadState("networkidle");
    await page.screenshot({ path: resolve(out, `overview-${scheme}.png`) });
    if (review) await fullShot(page, resolve(out, `overview-full-${scheme}.png`));

    await page.goto("/console/users");
    await page.getByRole("row", { name: /demo-user/ }).first().getByRole("link").click();
    await expect(page.getByRole("heading", { name: "demo-user", level: 1 })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Consent and privacy" })).toBeVisible();
    await page.waitForLoadState("networkidle");
    await page.screenshot({ path: resolve(out, `user-${scheme}.png`) });
    if (review) await fullShot(page, resolve(out, `user-full-${scheme}.png`));

    if (!review) return;

    await page.goto("/console/keys");
    await page.getByRole("button", { name: "New API key" }).click();
    await expect(page.getByRole("dialog", { name: "New API key" })).toBeVisible();
    await page.screenshot({ path: resolve(out, `key-dialog-${scheme}.png`) });
    await page.keyboard.press("Escape");

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/console/overview");
    await expect(page.getByRole("region", { name: "Pipeline, live" })).toBeVisible();
    await page.waitForLoadState("networkidle");
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, "no sideways page scroll at 390 px").toBeLessThanOrEqual(0);
    await fullShot(page, resolve(out, `overview-390-${scheme}.png`));
  });
}
