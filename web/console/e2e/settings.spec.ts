import { expect, test, type Page } from "@playwright/test";

interface Entry {
  name: string;
  type: string;
  apply: string;
  source: string;
  default: string;
  min: number | null;
  max: number | null;
}

async function signIn(page: Page): Promise<void> {
  const key = process.env["OHS_E2E_ADMIN_KEY"] ?? "";
  expect(key).not.toBe("");
  await page.goto("/console/sign-in");
  await page.getByLabel("Tenant API key").fill(key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/\/console\/sign-in/);
}

// The default plus one, or minus one at the upper bound.
function nextValue(e: Entry): string {
  const d = Number(e.default);
  return String(e.max !== null && d + 1 > e.max ? d - 1 : d + 1);
}

// An int with a plain default the e2e servers leave to the console.
function pick(list: Entry[], apply: string): Entry {
  const e = list.find((s) => s.type === "int" && s.apply === apply && s.source === "default" && /^-?\d+$/.test(s.default));
  if (!e) throw new Error(`no int setting with apply ${apply} and source default`);
  return e;
}

test("changes a live and a restart setting through Review and Apply", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/settings");
  await expect(page.getByRole("heading", { name: "Settings", level: 1 })).toBeVisible();

  const list = await page.evaluate(async () => {
    const res = await fetch("/v1/admin/settings", { headers: { Accept: "application/json" } });
    return ((await res.json()) as { data: Entry[] }).data;
  });
  const live = pick(list, "live");
  const restart = pick(list, "restart");

  await page.locator(`#set-${live.name}`).fill(nextValue(live));
  await page.locator(`#set-${restart.name}`).fill(nextValue(restart));
  const bar = page.getByRole("region", { name: "Unsaved changes" });
  await expect(bar.getByText("2 unsaved changes")).toBeVisible();
  await expect(bar.getByText("1 needs a restart")).toBeVisible();

  await bar.getByRole("button", { name: "Review" }).click();
  const dialog = page.getByRole("dialog", { name: "Review changes" });
  await expect(dialog.locator("tbody tr")).toHaveCount(2);
  await expect(dialog.getByRole("row", { name: new RegExp(live.name) })).toContainText(nextValue(live));
  await expect(dialog.getByRole("row", { name: new RegExp(restart.name) })).toContainText("Restart");
  await dialog.getByRole("button", { name: "Apply" }).click();
  await expect(dialog).toBeHidden();

  await expect(page.locator(`#setting-${live.name} .set-source`)).toHaveText("Console");
  await expect(page.locator(`#set-${live.name}`)).toHaveValue(nextValue(live));
  const notice = page.getByRole("status", { name: "Restart needed" });
  await expect(notice).toContainText(restart.name);
  await expect(notice).not.toContainText(live.name);
  await expect(page.getByRole("button", { name: /restart/i })).toHaveCount(0);

  // Both e2e servers started before the change to a restart setting: behind.
  const banner = page.getByRole("region", { name: "Processes not on the current settings" });
  await expect(banner.locator("tbody tr")).toHaveCount(2);
  await expect(banner.locator("tbody tr").nth(0)).toContainText("behind");
  await expect(banner.locator("tbody tr").nth(1)).toContainText("behind");
});

test("OHS_LISTEN reads as a locked bootstrap field", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/settings/server/OHS_LISTEN");
  const field = page.locator("#setting-OHS_LISTEN");
  await expect(field).toContainText("Bootstrap setting. Set OHS_LISTEN in the environment.");
  await expect(field.locator(".set-apply")).toHaveText("Bootstrap");
  await expect(page.locator("#set-OHS_LISTEN")).toBeDisabled();
});
