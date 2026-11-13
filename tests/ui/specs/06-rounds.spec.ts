import { execFileSync } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";
import { cli, fake, signIn, sql, stack } from "../stack";
import { fakeJSON, open, roundOf, state, waitRound } from "../rounds";

// Each test runs real rounds with the agent image.
test.describe.configure({ timeout: 300_000 });

test.beforeAll(async () => {
  // The fake may still be starting.
  for (let i = 0; ; i++) {
    try {
      await fake("/_fake/install?repos=acme/payments,acme/web");
      break;
    } catch (e) {
      if (i >= 30) throw e;
      await new Promise((r) => setTimeout(r, 1000));
    }
  }
  sql(
    `INSERT INTO providers (name, api_type, base_url, auth_style) VALUES ('fake', 'openai-completions', 'http://fake:9002/v1', 'bearer') ON CONFLICT (name) DO NOTHING`,
  );
  sql(
    `UPDATE profiles SET targets = '[{"provider":"fake","model":"fake-large","price_in":0,"price_out":0,"price_cache_read":0}]'`,
  );
  sql(
    `UPDATE settings SET value = '1'::jsonb, revision = revision + 1 WHERE key = 'debounce_seconds'`,
  );
});

test("SCN-PUB-001 each mode posts what §10.1 says, and the shadow round page shows the output", async ({
  page,
}) => {
  const repo = "acme/payments";
  const off = await open(repo, "e2e-caller", "off");
  await page.waitForTimeout(5000);
  expect(roundOf(repo, off.number)).toBe("");

  const shadow = await open(repo, "e2e-caller", "shadow");
  const id = await waitRound(repo, shadow.number, "done");
  const s = await state(repo, shadow.number);
  expect(s.checks.filter((c) => c.HeadSHA === shadow.head_sha)).toHaveLength(0);
  expect(s.issue_comments).toHaveLength(0);
  expect(
    s.review_comments.filter((c) => c.Number === shadow.number),
  ).toHaveLength(0);
  await signIn(page, "octo-admin");
  await page.goto(`/rounds/${id}`);
  await expect(
    page.getByText("Shadow mode: Sayso posts nothing."),
  ).toBeVisible();
  const summary = page.getByRole("region", { name: "Summary comment" });
  await expect(summary.getByText("Verdict:")).toBeVisible();
  await expect(
    summary.getByText("Post no longer rejects negative amounts"),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "ledger/ledger.go:18" }),
  ).toBeVisible();

  for (const [mode, conclusion] of [
    ["comment", "neutral"],
    ["gate", "failure"],
  ] as const) {
    const pr = await open(repo, "e2e-caller", mode);
    await waitRound(repo, pr.number, "done");
    await expect
      .poll(
        async () =>
          (await state(repo, pr.number)).checks.find(
            (c) => c.HeadSHA === pr.head_sha,
          )?.Conclusion,
        { timeout: 30_000 },
      )
      .toBe(conclusion);
    expect((await state(repo, pr.number)).issue_comments).toHaveLength(1);
  }
});

test("SCN-PUB-008 the UI renders agent text with no script and no active link", async ({
  page,
}) => {
  const pr = await open("acme/payments", "e2e-unsafe-text", "shadow");
  const id = await waitRound("acme/payments", pr.number, "done");
  const errors: string[] = [];
  page.on("dialog", (d) => {
    errors.push(d.message());
    void d.dismiss();
  });
  await signIn(page, "octo-admin");
  await page.goto(`/rounds/${id}`);
  const out = page.getByRole("region", { name: "ledger/ledger.go:18" });
  await expect(out.getByText(/about the check/).first()).toBeVisible();
  expect(await page.locator("main script").count()).toBe(0);
  expect(await page.locator('main a[href^="javascript"]').count()).toBe(0);
  await expect(
    out.locator("code", { hasText: /@\u2060?octo-admin/ }).first(),
  ).toBeVisible();
  await expect(out.getByRole("link", { name: "the guide" })).toHaveCount(0);
  expect(errors).toEqual([]);
});

async function waitRunning(repo: string, n: number) {
  const id = await waitRound(repo, n, "running");
  await expect
    .poll(
      () =>
        sql(
          `SELECT count(*) FROM agent_runs WHERE round_id = '${id}' AND status = 'running'`,
        ),
      { timeout: 60_000 },
    )
    .toBe("1");
  return id;
}

async function checkTitle(repo: string, n: number, head: string) {
  return (await state(repo, n)).checks.find(
    (c) => c.HeadSHA === head && c.Status === "completed",
  );
}

test("SCN-LIFE-013 a maintainer cancels a round in the UI, and the Temporal CLI cancel gets the same cleanup", async ({
  page,
}) => {
  const repo = "acme/payments";
  const pr = await open(repo, "review-slow", "gate");
  const id = await waitRunning(repo, pr.number);
  await signIn(page, "dev-alice");
  await page.goto(`/rounds/${id}`);
  await page.getByRole("button", { name: "Cancel round" }).click();
  await expect
    .poll(() => roundOf(repo, pr.number, "cancelled"), { timeout: 60_000 })
    .toBe(id);
  await expect
    .poll(async () => (await checkTitle(repo, pr.number, pr.head_sha))?.Title, {
      timeout: 30_000,
    })
    .toBe("Cancelled by @dev-alice");
  expect((await checkTitle(repo, pr.number, pr.head_sha))?.Conclusion).toBe(
    "neutral",
  );
  expect(sql(`SELECT status FROM agent_runs WHERE round_id = '${id}'`)).toBe(
    "cancelled",
  );
  const summary = (await state(repo, pr.number)).issue_comments[0]?.Body ?? "";
  expect(summary).toContain("Cancelled by `@dev-alice`");

  const next = await open(repo, "review-slow", "gate");
  const other = await waitRunning(repo, next.number);
  execFileSync("docker", [
    "exec",
    `${stack.project}-temporal-1`,
    "temporal",
    "workflow",
    "cancel",
    "--address",
    "temporal:7233",
    "--workflow-id",
    `round/${other}`,
  ]);
  await expect
    .poll(() => roundOf(repo, next.number, "cancelled"), { timeout: 60_000 })
    .toBe(other);
  await expect
    .poll(
      async () => (await checkTitle(repo, next.number, next.head_sha))?.Title,
      { timeout: 30_000 },
    )
    .toBe("Cancelled by operator");
});

async function temporalStatus(page: Page) {
  const res = await page.request.get("/temporal/", { maxRedirects: 0 });
  return res.status();
}

test("SCN-CORE-012 only admins reach /temporal and see the workflow links", async ({
  page,
  browser,
}) => {
  const pr = await open("acme/payments", "e2e-caller", "shadow");
  const id = await waitRound("acme/payments", pr.number, "done");
  await signIn(page, "octo-admin");
  await page.goto(`/rounds/${id}`);
  const link = page.getByRole("link", { name: "Open in Temporal" });
  await expect(link).toHaveAttribute(
    "href",
    `/temporal/namespaces/default/workflows/round%2F${id}`,
  );
  await link.click();
  await expect(page).toHaveURL(
    new RegExp(`/temporal/namespaces/default/workflows/round(%2F|/)${id}`),
  );
  await expect(page.getByText(`round/${id}`).first()).toBeVisible({
    timeout: 30_000,
  });
  for (const login of ["dev-alice", "dev-bob"]) {
    const other = await (await browser.newContext()).newPage();
    await signIn(other, login);
    expect(await temporalStatus(other)).toBe(404);
    await other.goto(`/rounds/${id}`);
    await expect(other.getByText("Rendered output")).toBeVisible();
    await expect(
      other.getByRole("link", { name: "Open in Temporal" }),
    ).toHaveCount(0);
  }
});

test("SCN-UI-011 a maintainer turns execution off and sets the threshold, and the next round uses both", async ({
  page,
}) => {
  sql(
    `UPDATE repositories SET execution = true, challenge_min_severity = 'minor', mode = 'shadow' WHERE full_name = 'acme/web'`,
  );
  await signIn(page, "dev-alice");
  await page.goto("/repositories/acme/web");
  await page.getByLabel("Code execution").click();
  await page.getByRole("radio", { name: "Major" }).click();
  await page.getByRole("button", { name: "Save review settings" }).click();
  await expect
    .poll(() =>
      sql(
        `SELECT execution || ' ' || challenge_min_severity FROM repositories WHERE full_name = 'acme/web'`,
      ),
    )
    .toBe("false major");
  expect(
    sql(
      `SELECT count(*) FROM audit_log WHERE target = 'acme/web' AND action IN ('repository.execution', 'repository.challenge_min_severity') AND actor_login = 'dev-alice'`,
    ),
  ).toBe("2");
  await fakeJSON(`/_fake/main?repo=acme/web`, {
    files: { ".sayso.yml": `instructions: "[script:review-assess-only]"\n` },
  });
  const pr = await fakeJSON<{ number: number }>(`/_fake/pulls?repo=acme/web`, {
    files: { "src/money.ts": "export const fee = 1;\n" },
  });
  const id = await waitRound("acme/web", pr.number, "done");
  expect(
    sql(
      `SELECT (config_snapshot->'spec'->>'execution') || ' ' || (config_snapshot->'spec'->'limits'->>'challenge_min_severity') FROM rounds WHERE id = '${id}'`,
    ),
  ).toBe("false major");
  expect(
    sql(`SELECT prompt_ref->>'execution' FROM rounds WHERE id = '${id}'`),
  ).toBe("false");
  const exported = cli("config", "export");
  const web = exported.slice(exported.indexOf("- full_name: acme/web"));
  expect(web.slice(0, web.indexOf("challenge_min_severity:") + 40)).toMatch(
    /execution: false\n\s+challenge_min_severity: major/,
  );
});
