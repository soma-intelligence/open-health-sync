import { expect, test, type Page } from "@playwright/test";

// Run by test/e2e/console_ui.sh against the compose stack: OHS_UI_URL is the
// server, OHS_UI_ADMIN_KEY the key of the first (instance admin) tenant, which
// holds the seed --demo data and an Apple Health user.

const out = process.env["OHS_UI_OUT"] ?? "out";
const key = process.env["OHS_UI_ADMIN_KEY"] ?? "";

/** Errors the page reports while a test runs: uncaught exceptions, console errors, 5xx answers. */
function watch(page: Page): string[] {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error") problems.push(`console: ${m.text()}`);
  });
  page.on("response", (r) => {
    if (r.status() >= 500) problems.push(`${String(r.status())} ${r.request().method()} ${r.url()}`);
  });
  return problems;
}

async function signIn(page: Page): Promise<void> {
  expect(key, "OHS_UI_ADMIN_KEY").not.toBe("");
  await page.goto("/console/sign-in");
  await page.getByLabel("Tenant API key").fill(key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/\/console\/sign-in/);
}

async function settled(page: Page, heading: string | RegExp): Promise<void> {
  await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
  await expect(page.getByText("Loading", { exact: true })).toHaveCount(0);
  await expect(page.getByText("This page failed")).toHaveCount(0);
  await expect(page.locator(".error-panel")).toHaveCount(0);
  await page.waitForLoadState("networkidle");
}

async function shot(page: Page, name: string): Promise<void> {
  await page.screenshot({ path: `${out}/screens/${name}.png`, fullPage: true });
}

test("sign-in page and a bad key", async ({ page }) => {
  const problems = watch(page);
  await page.goto("/console/overview");
  await expect(page).toHaveURL(/\/console\/sign-in/);
  await shot(page, "00-sign-in");
  await page.getByLabel("Tenant API key").fill("ohs_sk_not_a_real_key");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/console\/sign-in/);
  await shot(page, "01-sign-in-bad-key");
  // A refused key is a 401, which the browser logs; nothing else may fail.
  expect(problems.filter((p) => !/401|Unauthorized/i.test(p))).toEqual([]);
});

for (const scheme of ["light", "dark"] as const) {
  test(`every page, ${scheme}`, async ({ page }) => {
    const problems = watch(page);
    await page.emulateMedia({ colorScheme: scheme });
    await signIn(page);
    await page.goto("/console/overview");
    await page.getByRole("group", { name: "Theme" }).getByRole("button", { name: scheme === "light" ? "Light" : "Dark" }).click();

    const nav = page.getByRole("navigation", { name: "Console" });
    const go = async (link: string, heading: string | RegExp, name: string): Promise<void> => {
      await nav.getByRole("link", { name: link }).click();
      await settled(page, heading);
      await shot(page, `${scheme}-${name}`);
    };

    await go("Overview", "Overview", "10-overview");
    await expect(page.getByRole("region", { name: "Pipeline, live" })).toBeVisible();

    await go("Users", "Users", "20-users");
    await page.getByRole("row", { name: /demo-user/ }).first().getByRole("link").click();
    await settled(page, "demo-user");
    await shot(page, `${scheme}-21-user`);
    for (const [i, tab] of ["Data", "Connections", "Consents", "Sync runs", "Shares"].entries()) {
      await page.getByRole("tab", { name: tab }).click();
      await settled(page, "demo-user");
      await shot(page, `${scheme}-22-user-${String(i)}-${tab.toLowerCase().replace(/ /g, "-")}`);
    }

    await nav.getByRole("link", { name: "Users" }).click();
    await settled(page, "Users");
    await page.getByRole("row", { name: /apple-health-user/ }).first().getByRole("link").click();
    await settled(page, "apple-health-user");
    await page.getByRole("tab", { name: "Data" }).click();
    await settled(page, "apple-health-user");
    await shot(page, `${scheme}-23-apple-health-user-data`);

    await go("Providers", "Providers", "30-providers");
    await page.getByRole("table").first().getByRole("row").nth(1).getByRole("link").click();
    await settled(page, /.+/);
    await shot(page, `${scheme}-31-provider`);

    await go("Jobs", "Jobs", "40-jobs");
    await go("Webhooks", "Webhooks", "50-webhooks");
    await go("API keys", "API keys", "60-api-keys");
    await go("Audit log", "Audit log", "70-audit");
    await go("Settings", "Settings", "80-settings");

    // Command palette, from the top bar button and from the keyboard.
    await page.getByRole("button", { name: /Search or jump to/ }).click();
    await expect(page.getByRole("dialog", { name: "Command palette" })).toBeVisible();
    await shot(page, `${scheme}-90-palette`);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Command palette" })).toHaveCount(0);
    await page.keyboard.press("Control+k");
    await expect(page.getByRole("dialog", { name: "Command palette" })).toBeVisible();
    await page.keyboard.press("Escape");

    // An unknown path inside the console.
    await page.goto("/console/no-such-page");
    await page.waitForLoadState("networkidle");
    await shot(page, `${scheme}-95-not-found`);

    expect(problems, problems.join("\n")).toEqual([]);
  });
}

test("narrow window (phone width)", async ({ page }) => {
  const problems = watch(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  await page.goto("/console/overview");
  await settled(page, "Overview");
  await shot(page, "mobile-overview");
  await page.goto("/console/users");
  await settled(page, "Users");
  await shot(page, "mobile-users");
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  test.info().annotations.push({ type: "horizontal overflow (px)", description: String(overflow) });
  expect(problems, problems.join("\n")).toEqual([]);
});

test("sign out ends the session", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/overview");
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/console\/sign-in/);
  await page.goto("/console/users");
  await expect(page).toHaveURL(/\/console\/sign-in/);
});
