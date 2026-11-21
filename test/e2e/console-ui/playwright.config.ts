import { defineConfig, devices } from "@playwright/test";

const out = process.env["OHS_UI_OUT"] ?? "out";

export default defineConfig({
  testDir: ".",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 30_000 },
  outputDir: `${out}/results`,
  reporter: [["list"], ["html", { open: "never", outputFolder: `${out}/report` }]],
  use: {
    baseURL: process.env["OHS_UI_URL"],
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    viewport: { width: 1440, height: 900 },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 }, headless: true } }],
});
