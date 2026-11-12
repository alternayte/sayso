import { expect, test } from "@playwright/test";
import { pointGitHubAtFake, sql, stack } from "../stack";

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
