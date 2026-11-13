import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { signIn, sql } from "../stack";

const adminRoutes = ["/", "/repositories", "/settings/github", "/settings/models", "/settings/mcp", "/settings/skills", "/settings/extensions", "/settings/feeds", "/settings/images", "/settings/general", "/settings/access", "/audit", "/diagnostics"];
const settingsRoutes = ["/settings/github", "/settings/models", "/settings/mcp", "/settings/skills", "/settings/extensions", "/settings/feeds", "/settings/images", "/settings/general", "/settings/access", "/audit", "/diagnostics"];

function collectCSP(page: Page) {
  const violations: string[] = [];
  page.on("console", (m) => {
    if (/Content Security Policy|Content-Security-Policy/i.test(m.text())) violations.push(m.text());
  });
  return violations;
}

test("SCN-UI-001 a viewer reaches no settings route, an admin loads every route", async ({ page, browser }) => {
  await signIn(page, "dev-bob");
  await expect(page.getByRole("heading", { name: "Reviews" })).toBeVisible();
  const nav = page.getByRole("navigation", { name: "Main" });
  for (const name of ["GitHub", "Models", "MCP servers", "Skills", "Extensions", "Package feeds", "Agent images", "General", "Access", "Audit log", "Diagnostics"]) await expect(nav.getByRole("link", { name })).toHaveCount(0);
  for (const r of settingsRoutes) {
    await page.goto(r);
    await expect(page.getByRole("heading", { name: "Page not found" })).toBeVisible();
  }

  const admin = await (await browser.newContext()).newPage();
  await signIn(admin, "octo-admin");
  for (const r of adminRoutes) {
    await admin.goto(r);
    await expect(admin.locator("main h1")).toBeVisible();
    await expect(admin.getByRole("heading", { name: "Page not found" }), r).toHaveCount(0);
  }
});

test("SCN-CFG-010 admins filter the audit log, and the API cannot change it", async ({ page }) => {
  await signIn(page, "octo-admin");
  await page.goto("/settings/general");
  const field = page.getByRole("textbox", { name: "debounce_seconds" });
  await field.fill("75");
  await field.press("Enter");
  await expect(page.getByRole("button", { name: /^History of debounce_seconds, revision 2$/ })).toBeVisible();

  await page.goto("/audit");
  await page.getByLabel("Action").selectOption("settings.update");
  const rows = page.locator("tbody tr");
  await expect(rows.first()).toBeVisible();
  for (const action of await rows.evaluateAll((trs) => trs.map((tr) => tr.getAttribute("data-action")))) {
    expect(action).toBe("settings.update");
  }

  const doc = await (await page.request.get("/api/v1/openapi.json")).json();
  for (const [path, ops] of Object.entries<Record<string, unknown>>(doc.paths)) {
    if (!path.startsWith("/api/v1/audit")) continue;
    expect(Object.keys(ops)).toEqual(["get"]);
  }
  expect(Number(sql("SELECT count(*) FROM audit_log WHERE action = 'settings.update'"))).toBeGreaterThan(0);
});

test("SCN-UI-002 the main routes have no serious axe violations and no horizontal scroll", async ({ page }) => {
  await signIn(page, "octo-admin");
  for (const width of [360, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    for (const r of [...adminRoutes, "/signin"]) {
      await page.goto(r);
      await page.locator("main").first().waitFor();
      const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"]).analyze();
      const bad = res.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
      expect(bad.map((v) => `${r}@${width}: ${v.id} ${v.nodes[0]?.target}`)).toEqual([]);
      const scroll = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(scroll, `${r}@${width} scrolls sideways`).toBeLessThanOrEqual(0);
    }
  }
});

test("SCN-UI-008 reduced motion leaves no running movement, scale, blur or shimmer", async ({ browser }) => {
  const ctx = await browser.newContext({ reducedMotion: "reduce" });
  const page = await ctx.newPage();
  await signIn(page, "octo-admin");
  for (const r of adminRoutes) {
    await page.goto(r);
    await page.locator("main h1").waitFor();
    const moving = await page.evaluate(() =>
      document.getAnimations().flatMap((a) => {
        const effect = a.effect as KeyframeEffect | null;
        if (!effect || a.playState !== "running") return [];
        const props = effect.getKeyframes().flatMap((k) => Object.keys(k));
        return props.filter((p) => ["transform", "scale", "translate", "filter"].includes(p)).map((p) => `${(effect.target as Element)?.className}: ${p}`);
      }),
    );
    expect(moving, r).toEqual([]);
  }
});

test("SCN-UI-009 every UI response has the CSP, and the browser reports no violation", async ({ page }) => {
  const violations = collectCSP(page);
  await signIn(page, "octo-admin");
  for (const r of [...adminRoutes, "/signin"]) {
    const res = await page.goto(r);
    expect(res?.headers()["content-security-policy"], r).toMatch(/^default-src 'self'; style-src 'self' 'nonce-[A-Za-z0-9+/=]+'$/);
    await page.locator("main").first().waitFor();
  }
  const api = await page.request.get("/api/v1/me");
  expect(api.headers()["content-security-policy"]).toBe("default-src 'self'");
  expect(violations).toEqual([]);
});

test("SCN-CORE-012 only admins reach the Temporal UI", async ({ page, browser }) => {
  await signIn(page, "octo-admin");
  const res = await page.goto("/temporal/namespaces/default/workflows");
  expect(res?.status()).toBe(200);

  sql(`INSERT INTO role_grants (login, role, granted_by) VALUES ('dev-alice', 'maintainer', 'e2e') ON CONFLICT (login) DO UPDATE SET role = 'maintainer'`);
  for (const login of ["dev-alice", "dev-bob"]) {
    const other = await (await browser.newContext()).newPage();
    await signIn(other, login);
    const r = await other.request.get("/temporal/namespaces/default/workflows");
    expect(r.status(), login).toBe(404);
  }
});
