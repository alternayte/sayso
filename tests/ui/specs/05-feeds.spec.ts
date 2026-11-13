import { expect, test } from "@playwright/test";
import { signIn, sql } from "../stack";

test("SCN-PKG-004 inject mode needs a confirmation, shows the warning on the feed and repository pages, and writes an audit row", async ({
  page,
}) => {
  await signIn(page, "octo-admin");
  await page.goto("/settings/feeds");
  await page.getByRole("button", { name: "Add feed" }).click();
  await page.getByLabel("Name", { exact: true }).fill("acme-npm");
  await page
    .getByLabel("Upstream URL", { exact: true })
    .fill("http://github.fake.localhost:9003/npm/");
  await page.getByLabel("User name", { exact: true }).fill("feed");
  await page.getByLabel("Secret", { exact: true }).fill("s3cr3t-ui-feed-token");
  await page
    .getByLabel("Match URLs", { exact: true })
    .fill("http://github.fake.localhost:9003/npm/");
  await page.getByRole("button", { name: "Save feed" }).click();
  await expect(page.getByRole("button", { name: "Save feed" })).toBeHidden();
  const row = page.getByRole("listitem").filter({ hasText: "acme-npm" });
  await expect(row.getByText("Proxy", { exact: true })).toBeVisible();

  // Switch to inject mode: the warning shows and save waits for the confirmation.
  await row.getByRole("button", { name: "Edit" }).click();
  await page.getByRole("radio", { name: "Inject" }).click();
  await expect(
    page.getByText("Inject mode gives the feed credential to agent runs."),
  ).toBeVisible();
  const save = page.getByRole("button", { name: "Save feed" });
  await expect(save).toBeDisabled();
  await page
    .getByLabel("I understand that agent runs get the credential of this feed.")
    .click();
  await save.click();
  await expect(save).toBeHidden();
  await expect(row.getByText("Inject", { exact: true })).toBeVisible();
  await expect(
    row.getByText("Inject mode gives the feed credential to agent runs."),
  ).toBeVisible();
  await expect(row.getByText("Confirmed by octo-admin.")).toBeVisible();
  expect(
    sql(
      `SELECT count(*) FROM audit_log WHERE action = 'feed.inject_confirmed' AND target = 'acme-npm' AND actor_login = 'octo-admin'`,
    ),
  ).toBe("1");
  expect(
    sql(
      `SELECT mode || ' ' || inject_confirmed_by FROM package_feeds WHERE name = 'acme-npm'`,
    ),
  ).toBe("inject octo-admin");

  // The repository that uses the feed shows the warning.
  await page.goto("/repositories/acme/payments");
  const card = page
    .locator("[data-slot=card]")
    .filter({ hasText: "Package feeds" });
  await card.getByRole("checkbox", { name: /acme-npm/ }).check();
  await card.getByRole("button", { name: "Save feeds" }).click();
  await expect(
    card.getByText(
      "Agent runs of this repository get the credential of these feeds: acme-npm.",
    ),
  ).toBeVisible();
  expect(
    sql(
      `SELECT array_to_string(feeds, ',') FROM repositories WHERE full_name = 'acme/payments'`,
    ),
  ).toBe("acme-npm");
  expect(await page.content()).not.toContain("s3cr3t-ui-feed-token");
});
