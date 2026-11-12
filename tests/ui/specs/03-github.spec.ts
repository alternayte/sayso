import { expect, test } from "@playwright/test";
import { fake, signIn, sql } from "../stack";

test("SCN-GH-009 a maintainer sets the mode of a repository", async ({ page }) => {
  await fake("/_fake/install?repos=acme/payments,acme/web");
  await expect.poll(() => sql(`SELECT count(*) FROM repositories WHERE removed_at IS NULL`)).toBe("2");
  sql(`INSERT INTO role_grants (login, role, granted_by) VALUES ('dev-alice', 'maintainer', 'e2e') ON CONFLICT (login) DO UPDATE SET role = 'maintainer'`);
  sql(`UPDATE users SET role = 'maintainer', role_source = 'grant' WHERE login = 'dev-alice'`);

  await signIn(page, "dev-alice");
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Repositories" }).click();
  await page.getByRole("link", { name: "acme/payments" }).click();
  await expect(page.getByRole("heading", { name: "acme/payments" })).toBeVisible();
  const mode = page.getByRole("radiogroup", { name: "Mode" });
  await expect(mode.getByRole("radio", { name: "Off" })).toHaveAttribute("aria-checked", "true");
  await mode.getByRole("radio", { name: "Shadow" }).click();
  await expect(mode.getByRole("radio", { name: "Shadow" })).toHaveAttribute("aria-checked", "true");
  await expect(page.getByRole("status").filter({ hasText: "Saved" })).toBeVisible();

  expect(sql(`SELECT mode FROM repositories WHERE full_name = 'acme/payments'`)).toBe("shadow");
  expect(sql(`SELECT actor_login || ' ' || details->>'new' FROM audit_log WHERE action = 'repository.mode' AND target = 'acme/payments'`)).toBe("dev-alice shadow");

  // A viewer sees the mode and cannot change it.
  const viewer = await (await page.context().browser()!.newContext()).newPage();
  await signIn(viewer, "dev-bob");
  await viewer.goto("/repositories/acme/payments");
  await expect(viewer.getByRole("radio", { name: "Shadow" })).toBeDisabled();
  expect((await viewer.request.put("/api/v1/repositories/acme/payments/mode", { data: { mode: "gate" } })).status()).toBe(403);
});

test("SCN-GH-002 admins see the installation and the install link", async ({ page }) => {
  await signIn(page, "octo-admin");
  await page.goto("/settings/github");
  await expect(page.getByRole("link", { name: "Install the App" })).toHaveAttribute("href", /installations\/new$/);
  await expect(page.getByRole("cell", { name: "acme", exact: true })).toBeVisible();
  await expect(page.getByRole("cell", { name: "installation.created" })).toBeVisible();
});
