import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { signIn, sql } from "../stack";

test("SCN-CFG-005 an admin imports exported YAML with one changed profile, sees one change and applies it", async ({
  page,
}) => {
  sql(`UPDATE profiles SET round_budget_usd = 1.5 WHERE name = 'review'`);
  await signIn(page, "octo-admin");
  await page.goto("/settings/config");
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download YAML" }).click();
  const file = await (await download).path();
  const yaml = readFileSync(file, "utf8");
  expect(yaml).toContain("version: 1");

  // Change the budget of the review profile only.
  const at = yaml.indexOf("\n    - name: review\n");
  expect(at).toBeGreaterThan(0);
  const budget = yaml.indexOf("round_budget_usd: 1.5", at);
  const changed = `${yaml.slice(0, budget)}round_budget_usd: 4.25${yaml.slice(budget + "round_budget_usd: 1.5".length)}`;

  await page.getByLabel("YAML", { exact: true }).fill(changed);
  await page.getByRole("button", { name: "Show diff" }).click();
  const diff = page.getByRole("region", { name: "Diff" });
  await expect(diff.getByRole("status")).toHaveText("Changes: 1");
  await expect(diff.getByRole("listitem")).toHaveCount(1);
  await expect(diff.getByText("profiles")).toBeVisible();
  await expect(diff.getByText("round_budget_usd")).toBeVisible();
  expect(sql(`SELECT round_budget_usd FROM profiles WHERE name = 'review'`)).toBe("1.5000");

  await page.getByRole("button", { name: "Apply changes" }).click();
  await expect(page.getByText("Applied changes: 1")).toBeVisible();
  expect(sql(`SELECT round_budget_usd FROM profiles WHERE name = 'review'`)).toBe("4.2500");
  expect(sql(`SELECT count(*) FROM audit_log WHERE action = 'config.apply' AND actor_login = 'octo-admin'`)).toBe("1");
});
