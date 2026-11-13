import { expect, test } from "@playwright/test";
import { open, waitRound } from "../rounds";
import { fake, pointGitHubAtFake, signIn, sql, stack } from "../stack";

test("SCN-AUTH-001 setup token opens only the App step, and setup closes after the App exists", async ({ page, request }) => {
  pointGitHubAtFake();
  await page.goto("/setup");
  await page.getByLabel("Setup token").fill("not-the-token");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("The setup token is wrong.")).toBeVisible();

  await page.getByLabel("Setup token").fill(stack.setupToken);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("heading", { name: "Setup checklist" })).toBeVisible();
  const steps = page.locator("ol > li");
  await expect(steps).toHaveCount(1);
  await expect(steps.first()).toContainText("Create the GitHub App");

  // The setup cookie opens the setup operations only, not the admin API.
  expect((await page.request.get("/api/v1/settings")).status()).toBe(401);

  // SCN-GH-001 through the UI: the manifest flow creates the App and shows the install link.
  await page.getByLabel("GitHub organization").fill("acme");
  await page.getByRole("button", { name: "Create the GitHub App" }).click();
  await page.waitForURL((u) => u.pathname === "/signin");
  await expect(page.getByText("The GitHub App exists.")).toBeVisible();
  await expect(page.getByRole("link", { name: "Install the App" })).toHaveAttribute("href", /\/apps\/sayso-acme\/installations\/new$/);
  expect(sql(`SELECT count(*) FROM secrets WHERE name LIKE 'github.%' AND kind = 'stored'`)).toBe("3");

  expect((await request.get("/setup")).status()).toBe(404);
  expect((await request.get("/api/v1/setup/state")).status()).toBe(200);
  expect((await request.post("/api/v1/setup/session", { data: { token: stack.setupToken }, headers: { Origin: stack.baseURL } })).status()).toBe(404);
});

test("SCN-CFG-007 each checklist step turns done in order, then the home page is the reviews list", async ({ page }) => {
  test.setTimeout(300_000);
  sql(`UPDATE settings SET value = '1'::jsonb, revision = revision + 1 WHERE key = 'debounce_seconds'`);
  await signIn(page, "octo-admin");
  const list = page.getByRole("list", { name: "Setup checklist" }).getByRole("listitem");
  // done checks that the first n steps are done and the others are not.
  const done = async (n: number) => {
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Set up Sayso" })).toBeVisible();
    await expect(list).toHaveCount(8);
    for (let i = 0; i < 8; i++) await expect(list.nth(i)).toHaveAttribute("data-state", i < n ? "done" : i === n ? "current" : "todo");
  };
  await done(1);

  // The installation webhook is the first delivery, so steps 2 and 3 turn done together.
  await fake("/_fake/install?repos=acme/payments,acme/web");
  await expect.poll(() => sql(`SELECT count(*) FROM webhook_deliveries`)).not.toBe("0");
  await done(3);

  sql(`INSERT INTO providers (name, api_type, base_url, auth_style, models) VALUES ('fake', 'openai-completions', 'http://fake:9002/v1', 'bearer', '{fake-large}') ON CONFLICT (name) DO NOTHING`);
  await list.nth(3).getByRole("link").click();
  const provider = page.getByRole("listitem").filter({ hasText: "fake" }).first();
  await provider.getByRole("button", { name: "Test" }).click();
  await expect(provider.getByText("Passed")).toBeVisible();
  await done(4);

  sql(`UPDATE profiles SET targets = '[{"provider":"fake","model":"fake-large","price_in":0,"price_out":0,"price_cache_read":0}]'`);
  await done(5);

  await list.nth(5).getByRole("link").click();
  const store = page.getByRole("listitem").filter({ hasText: "postgres" });
  await store.getByRole("button", { name: "Test" }).click();
  await expect(store.getByText("Test passed")).toBeVisible();
  await done(6);

  sql(`UPDATE repositories SET mode = 'shadow', execution = false WHERE full_name = 'acme/web'`);
  await done(7);

  const pr = await open("acme/web", "e2e-caller", "shadow");
  await waitRound("acme/web", pr.number, "done");
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Reviews" })).toBeVisible();
  await expect(page.getByRole("link", { name: `acme/web#${pr.number}` })).toBeVisible();

  // The home page stays the reviews list after a step is not done any more.
  sql(`UPDATE repositories SET mode = 'off' WHERE full_name = 'acme/web'`);
  await page.reload();
  await expect(page.getByRole("heading", { name: "Reviews" })).toBeVisible();
});
