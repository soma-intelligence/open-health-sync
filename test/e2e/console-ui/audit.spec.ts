import { mkdirSync, writeFileSync } from "node:fs";

import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// UI/UX audit probes. Off in a normal run: test/e2e/console_ui.sh sets
// OHS_UI_AUDIT=1. Each probe records what it sees in out/audit/*.json and
// screenshots in out/audit/*.png; a probe never fails the run on a finding,
// only when it cannot run at all.
test.skip(process.env["OHS_UI_AUDIT"] !== "1", "OHS_UI_AUDIT is not set");
test.describe.configure({ mode: "serial" });
test.setTimeout(600_000);

const out = `${process.env["OHS_UI_OUT"] ?? "out"}/audit`;
mkdirSync(out, { recursive: true });
const adminKey = process.env["OHS_UI_ADMIN_KEY"] ?? "";
const otherKey = process.env["OHS_UI_OTHER_KEY"] ?? "";

const PAGES: [string, string][] = [
  ["overview", "/console/overview"],
  ["users", "/console/users"],
  ["providers", "/console/providers"],
  ["jobs", "/console/jobs"],
  ["webhooks", "/console/webhooks"],
  ["keys", "/console/keys"],
  ["audit", "/console/audit"],
  ["settings", "/console/settings"],
];

function save(name: string, data: unknown): void {
  writeFileSync(`${out}/${name}.json`, JSON.stringify(data, null, 2));
}
async function shot(page: Page, name: string, fullPage = true): Promise<void> {
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
}
async function signIn(page: Page, key = adminKey): Promise<void> {
  await page.goto("/console/sign-in");
  await page.getByLabel("Tenant API key").fill(key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/\/console\/sign-in/);
}
async function idle(page: Page): Promise<void> {
  await page.waitForLoadState("networkidle").catch(() => undefined);
  await page.waitForTimeout(300);
}
async function firstUserPath(page: Page, name = "demo-user"): Promise<string> {
  await page.goto("/console/users");
  await page.getByRole("row", { name: new RegExp(name) }).first().getByRole("link").click();
  await expect(page).toHaveURL(/\/console\/users\/[^/]+/);
  return new URL(page.url()).pathname;
}

test("axe accessibility scan, every page, light and dark", async ({ page }) => {
  await signIn(page);
  const user = await firstUserPath(page);
  const pages = [...PAGES, ["user", user] as [string, string], ["sign-in", "/console/sign-in"] as [string, string]];
  const result: Record<string, unknown> = {};
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto("/console/overview");
    await page.getByRole("group", { name: "Theme" }).getByRole("button", { name: scheme === "light" ? "Light" : "Dark" }).click();
    for (const [name, path] of pages) {
      if (name === "sign-in") continue;
      await page.goto(path);
      await idle(page);
      const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"]).analyze();
      result[`${scheme}:${name}`] = r.violations.map((v) => ({
        id: v.id,
        impact: v.impact,
        help: v.help,
        count: v.nodes.length,
        targets: v.nodes.slice(0, 4).map((n) => ({ target: n.target.join(" "), summary: n.failureSummary?.slice(0, 300) })),
      }));
    }
  }
  const ctx = await page.context().browser()!.newContext({ baseURL: test.info().project.use.baseURL });
  const anon = await ctx.newPage();
  await anon.goto("/console/sign-in");
  await idle(anon);
  const r = await new AxeBuilder({ page: anon }).analyze();
  result["light:sign-in"] = r.violations.map((v) => ({ id: v.id, impact: v.impact, help: v.help, count: v.nodes.length, targets: v.nodes.slice(0, 4).map((n) => n.target.join(" ")) }));
  await ctx.close();
  save("axe", result);
});

test("keyboard: tab order and visible focus from the overview", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/overview");
  await idle(page);
  const stops: unknown[] = [];
  for (let i = 0; i < 30; i++) {
    await page.keyboard.press("Tab");
    stops.push(
      await page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null;
        if (!el || el === document.body) return { el: "body" };
        const cs = getComputedStyle(el);
        const r = el.getBoundingClientRect();
        return {
          el: `${el.tagName.toLowerCase()}${el.id ? "#" + el.id : ""}`,
          name: (el.getAttribute("aria-label") ?? el.textContent ?? "").trim().slice(0, 50),
          outline: `${cs.outlineStyle} ${cs.outlineWidth} ${cs.outlineColor}`,
          boxShadow: cs.boxShadow.slice(0, 80),
          visible: r.width > 0 && r.height > 0 && r.bottom > 0 && r.top < innerHeight,
          size: `${Math.round(r.width)}x${Math.round(r.height)}`,
        };
      }),
    );
    if (i === 2 || i === 8 || i === 15) await shot(page, `keyboard-focus-${String(i)}`, false);
  }
  // Skip link?
  await page.goto("/console/overview");
  await page.keyboard.press("Tab");
  const firstStop = await page.evaluate(() => (document.activeElement?.textContent ?? "").trim().slice(0, 60));
  save("keyboard", { firstStop, stops });
});

test("empty states: a tenant with no data", async ({ page }) => {
  test.skip(otherKey === "", "no OHS_UI_OTHER_KEY");
  await signIn(page, otherKey);
  const seen: Record<string, string> = {};
  for (const [name, path] of PAGES) {
    await page.goto(path);
    await idle(page);
    await shot(page, `empty-${name}`);
    seen[name] = (await page.locator("main").innerText().catch(() => "")).slice(0, 600);
  }
  save("empty", seen);
});

test("loading states: slow API (2.5 s per call)", async ({ page }) => {
  await signIn(page);
  const user = await firstUserPath(page);
  await page.route("**/v1/**", async (route) => {
    await new Promise((r) => setTimeout(r, 2500));
    await route.continue();
  });
  const seen: Record<string, string> = {};
  for (const [name, path] of [...PAGES, ["user", user] as [string, string]]) {
    await page.goto(path);
    await page.waitForTimeout(700);
    await shot(page, `loading-${name}`, false);
    seen[name] = (await page.locator("main").innerText().catch(() => "")).slice(0, 300);
  }
  // A refresh while content is on screen: does the content stay?
  await page.unroute("**/v1/**");
  await page.goto("/console/overview");
  await idle(page);
  await page.route("**/v1/**", async (route) => {
    await new Promise((r) => setTimeout(r, 2500));
    await route.continue();
  });
  await page.getByRole("button", { name: "Refresh" }).click();
  await page.waitForTimeout(600);
  await shot(page, "loading-overview-refresh", false);
  seen["overview-refresh"] = (await page.locator("main").innerText().catch(() => "")).slice(0, 300);
  save("loading", seen);
});

test("error states: every API call answers 500", async ({ page }) => {
  await signIn(page);
  const user = await firstUserPath(page);
  await page.route("**/v1/**", (route) =>
    route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: { code: "internal", message: "internal error" } }) }),
  );
  const seen: Record<string, string> = {};
  for (const [name, path] of [...PAGES, ["user", user] as [string, string]]) {
    await page.goto(path);
    await idle(page);
    await page.waitForTimeout(1500);
    await shot(page, `error-${name}`, false);
    seen[name] = (await page.locator("main").innerText().catch(() => "")).slice(0, 400);
  }
  // Recovery: is there a retry, and does it work once the API is back?
  await page.goto("/console/users");
  await page.waitForTimeout(1500);
  const retry = page.getByRole("button", { name: /retry|try again/i });
  seen["retry-buttons"] = String(await retry.count());
  await page.unroute("**/v1/**");
  if ((await retry.count()) > 0) {
    await retry.first().click();
    await idle(page);
    seen["after-retry"] = (await page.locator("main").innerText().catch(() => "")).slice(0, 200);
    await shot(page, "error-users-after-retry", false);
  }
  save("errors", seen);
});

test("refresh failure keeps the content on screen", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/overview");
  await idle(page);
  await page.route("**/v1/**", (route) => route.fulfill({ status: 503, body: "" }));
  await page.getByRole("button", { name: "Refresh" }).click();
  await page.waitForTimeout(2000);
  await shot(page, "error-overview-refresh-failed", false);
  save("refresh-failure", { main: (await page.locator("main").innerText()).slice(0, 500) });
});

test("offline after load", async ({ page, context }) => {
  await signIn(page);
  await page.goto("/console/overview");
  await idle(page);
  await context.setOffline(true);
  const nav = page.getByRole("navigation", { name: "Console" });
  await nav.getByRole("link", { name: "Users" }).click();
  await page.waitForTimeout(2500);
  await shot(page, "offline-users", false);
  await nav.getByRole("link", { name: "Jobs" }).click();
  await page.waitForTimeout(2500);
  await shot(page, "offline-jobs", false);
  const text = (await page.locator("main").innerText().catch(() => "")).slice(0, 400);
  await context.setOffline(false);
  save("offline", { jobs: text });
});

test("session expiry in the middle of a form, and deep link after sign-in", async ({ page, context }) => {
  await signIn(page);
  const user = await firstUserPath(page);
  await page.goto("/console/users");
  await page.getByRole("button", { name: "New user" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("External id").fill("typed-before-expiry");
  await context.clearCookies();
  await dialog.getByRole("button", { name: "Create user" }).click();
  await page.waitForTimeout(2000);
  await shot(page, "session-expired-submit", false);
  const afterUrl = page.url();
  const dialogStillOpen = await page.getByRole("dialog").count();
  const valueKept = dialogStillOpen ? await page.getByRole("dialog").getByLabel("External id").inputValue().catch(() => "") : "";

  // Deep link: signed out, open a user page, sign in, where do we land?
  await context.clearCookies();
  await page.goto(user + "/connections");
  await page.waitForTimeout(1000);
  const redirectedTo = new URL(page.url()).pathname + new URL(page.url()).search;
  await page.getByLabel("Tenant API key").fill(adminKey);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.waitForTimeout(1500);
  save("session", { afterSubmitUrl: afterUrl, dialogStillOpen, valueKept, deepLink: user + "/connections", redirectedTo, landedOn: new URL(page.url()).pathname });
});

test("forms: new user, webhook endpoint, API key", async ({ page }) => {
  await signIn(page);
  const findings: Record<string, unknown> = {};
  await page.goto("/console/users");
  await idle(page);

  // Empty submit
  const opener = page.getByRole("button", { name: "New user" });
  await opener.click();
  let dialog = page.getByRole("dialog");
  await shot(page, "form-user-open", false);
  findings["user-focus-on-open"] = await page.evaluate(() => `${document.activeElement?.tagName ?? ""} ${document.activeElement?.getAttribute("aria-label") ?? document.activeElement?.id ?? ""}`);
  findings["user-create-enabled-when-empty"] = await dialog.getByRole("button", { name: "Create user" }).isEnabled();
  if (findings["user-create-enabled-when-empty"]) {
    await dialog.getByRole("button", { name: "Create user" }).click();
    await page.waitForTimeout(800);
    await shot(page, "form-user-empty-submit", false);
    findings["user-empty-submit-text"] = (await dialog.innerText().catch(() => "")).slice(0, 300);
  }
  // Escape with typed text: closes silently?
  await dialog.getByLabel("External id").fill("draft-value");
  await page.keyboard.press("Escape");
  await page.waitForTimeout(400);
  findings["user-escape-with-text-closes"] = (await page.getByRole("dialog").count()) === 0;
  findings["focus-returned-to-opener"] = await opener.evaluate((el) => el === document.activeElement);

  // Long value + emoji, double click on Create
  await opener.click();
  dialog = page.getByRole("dialog");
  const long = "patient-" + "x".repeat(180) + "-🙂-é";
  await dialog.getByLabel("External id").fill(long);
  let posts = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && /\/v1\/users$/.test(new URL(r.url()).pathname)) posts++;
  });
  await dialog.getByRole("button", { name: "Create user" }).dblclick();
  await page.waitForTimeout(1500);
  findings["user-double-click-posts"] = posts;
  await shot(page, "form-user-after-create", false);
  await page.keyboard.press("Escape");
  await page.goto("/console/users");
  await idle(page);
  await shot(page, "long-content-users");
  await page.setViewportSize({ width: 390, height: 844 });
  await shot(page, "long-content-users-mobile");
  await page.getByRole("row", { name: /patient-x/ }).first().getByRole("link").click().catch(() => undefined);
  await idle(page);
  await shot(page, "long-content-user-mobile");
  await page.setViewportSize({ width: 1440, height: 900 });
  await shot(page, "long-content-user");

  // Webhook endpoint form, invalid URL
  await page.goto("/console/webhooks");
  await idle(page);
  const add = page.getByRole("button", { name: /new endpoint|add endpoint|new webhook|add webhook|create/i }).first();
  findings["webhook-add-button"] = await add.innerText().catch(() => "none");
  await add.click().catch(() => undefined);
  await page.waitForTimeout(500);
  const url = page.getByLabel("Endpoint URL");
  if (await url.count()) {
    await url.fill("not a url");
    await url.press("Tab");
    await page.waitForTimeout(300);
    await shot(page, "form-webhook-invalid-blur", false);
    await page.getByRole("button", { name: /create|add|save/i }).last().click().catch(() => undefined);
    await page.waitForTimeout(1000);
    await shot(page, "form-webhook-invalid-submit", false);
    findings["webhook-invalid-text"] = (await page.locator("main, [role=dialog]").last().innerText()).slice(0, 400);
    await url.fill("http://insecure.example.com/hook");
    await page.getByRole("button", { name: /create|add|save/i }).last().click().catch(() => undefined);
    await page.waitForTimeout(1000);
    await shot(page, "form-webhook-http", false);
  }
  await page.keyboard.press("Escape");

  // API key dialog
  await page.goto("/console/keys");
  await idle(page);
  await page.getByRole("button", { name: /new key|create|new api key/i }).first().click().catch(() => undefined);
  await page.waitForTimeout(500);
  await shot(page, "form-key-open", false);
  save("forms", findings);
});

test("context kept: search, back button, tabs", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/users");
  await idle(page);
  await page.getByLabel("External id").fill("demo");
  await page.getByRole("button", { name: "Search" }).click();
  await idle(page);
  const searchUrl = page.url();
  await page.getByRole("row", { name: /demo-user/ }).first().getByRole("link").click();
  await idle(page);
  await page.getByRole("tab", { name: "Sync runs" }).click();
  const tabUrl = page.url();
  await page.goBack();
  await idle(page);
  const afterBack = page.url();
  await page.goBack();
  await idle(page);
  save("context", {
    searchUrl,
    tabUrl,
    afterFirstBack: afterBack,
    afterSecondBack: page.url(),
    searchValueAfterBack: await page.getByLabel("External id").inputValue().catch(() => "n/a"),
  });
});

test("settings: unsaved change then navigate away", async ({ page }) => {
  await signIn(page);
  await page.goto("/console/settings");
  await idle(page);
  let dialogSeen = false;
  page.on("dialog", async (d) => {
    dialogSeen = true;
    await d.dismiss();
  });
  const editable = page.locator("main input:not([disabled]):not([readonly]), main select:not([disabled])").first();
  const n = await editable.count();
  if (n) {
    const tag = await editable.evaluate((el) => el.tagName);
    if (tag === "SELECT") {
      const opts = await editable.locator("option").allInnerTexts();
      await editable.selectOption({ index: Math.min(1, opts.length - 1) });
    } else {
      await editable.fill("10.0.0.0/8");
    }
    await page.waitForTimeout(500);
    await shot(page, "settings-dirty", false);
    await page.getByRole("navigation", { name: "Console" }).getByRole("link", { name: "Users" }).click();
    await page.waitForTimeout(800);
  }
  save("settings-unsaved", { editableFound: n, browserDialog: dialogSeen, urlAfter: page.url(), inAppPrompt: await page.getByRole("dialog").count() });
  await shot(page, "settings-after-leave", false);
});

test("phone width: every page, overflow and menu", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(page);
  const overflow: Record<string, number> = {};
  for (const [name, path] of PAGES) {
    await page.goto(path);
    await idle(page);
    overflow[name] = await page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - innerWidth);
    await shot(page, `mobile-${name}`);
  }
  await page.getByRole("button", { name: "Menu" }).click();
  await page.waitForTimeout(400);
  await shot(page, "mobile-menu-open", false);
  // Touch target sizes under 24 and 44 px
  const small = await page.evaluate(() => {
    const els = [...document.querySelectorAll<HTMLElement>("a, button, input, select, [role=tab], [role=button]")];
    const visible = els.filter((e) => e.offsetParent !== null);
    const sizes = visible.map((e) => e.getBoundingClientRect());
    return { total: visible.length, under24: sizes.filter((r) => r.height < 24 || r.width < 24).length, under44: sizes.filter((r) => r.height < 44).length };
  });
  save("mobile", { overflow, targetsAtUsersPageWithMenu: small });
});

test("large text, forced colors, reduced motion", async ({ page }) => {
  await signIn(page);
  const user = await firstUserPath(page);
  await page.goto("/console/overview");
  await idle(page);
  const motion: Record<string, number> = {};
  motion["animations-normal"] = await page.evaluate(() => document.getAnimations().length);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.reload();
  await idle(page);
  motion["animations-reduced"] = await page.evaluate(() => document.getAnimations().length);
  motion["transition-duration-reduced-sample"] = await page.evaluate(() => {
    const b = document.querySelector("button");
    return b ? parseFloat(getComputedStyle(b).transitionDuration) : -1;
  });
  await page.emulateMedia({ reducedMotion: "no-preference" });

  for (const [name, path] of [["overview", "/console/overview"], ["user", user], ["settings", "/console/settings"]] as const) {
    await page.goto(path);
    await idle(page);
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "200%";
    });
    await page.waitForTimeout(300);
    await shot(page, `text200-${name}`, false);
  }
  await page.emulateMedia({ forcedColors: "active" });
  await page.goto("/console/overview");
  await idle(page);
  await shot(page, "forced-colors-overview", false);
  await page.goto(user);
  await idle(page);
  await shot(page, "forced-colors-user", false);
  save("motion", motion);
});
