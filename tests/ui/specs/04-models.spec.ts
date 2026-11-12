import { expect, test } from "@playwright/test";
import { signIn, sql } from "../stack";

const key = "sk-e2e-provider-key-9137";

test("SCN-CFG-003 the key field shows the typed value, then only the last 4 characters after save", async ({ page }) => {
  await signIn(page, "octo-admin");
  await page.goto("/settings/models");
  await page.getByRole("button", { name: "Add provider" }).click();
  await page.getByLabel("Preset").selectOption({ label: "OpenAI-compatible gateway" });
  await page.getByLabel("Name", { exact: true }).fill("fake-gateway");
  await page.getByLabel("Base URL", { exact: true }).fill("http://github.fake.localhost:9002/v1");
  const field = page.getByLabel("API key", { exact: true });
  await field.fill(key);
  await expect(field).toHaveAttribute("type", "password");
  await page.getByRole("button", { name: "Show value" }).click();
  await expect(field).toHaveAttribute("type", "text");
  await expect(field).toHaveValue(key);
  await page.getByRole("button", { name: "Hide value" }).click();
  await expect(field).toHaveAttribute("type", "password");
  await page.getByLabel("Models", { exact: true }).fill("fake-large");
  await page.getByRole("button", { name: "Save provider" }).click();

  await page.reload();
  const row = page.getByRole("listitem").filter({ hasText: "fake-gateway" });
  await expect(row.getByText("•••• 9137")).toBeVisible();
  await row.getByRole("button", { name: "Edit" }).click();
  await expect(page.getByTestId("secret-last4")).toHaveText("•••• 9137");
  await expect(page.getByRole("button", { name: "Replace" })).toBeVisible();
  expect(await page.content()).not.toContain(key);
  const api = await page.request.get("/api/v1/providers");
  expect(await api.text()).not.toContain(key);
  expect(sql(`SELECT count(*) FROM audit_log WHERE action = 'secrets.put' AND target = 'provider.fake-gateway.key' AND details::text NOT LIKE '%${key}%'`)).toBe("1");

  // The provider test runs against the fake model endpoint.
  await page.getByRole("button", { name: "Cancel" }).click();
  await row.getByRole("button", { name: "Test" }).click();
  await expect(row.getByText("Passed")).toBeVisible();
});
