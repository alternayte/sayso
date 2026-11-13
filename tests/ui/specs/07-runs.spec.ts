import { spawn } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";
import { open, waitRound } from "../rounds";
import { signIn, sql, stack } from "../stack";

// Each test may run a real round with the agent image.
test.describe.configure({ timeout: 300_000 });

function runOf(round: string): string {
  return sql(
    `SELECT id FROM agent_runs WHERE round_id = '${round}' ORDER BY created_at DESC LIMIT 1`,
  );
}

/** anyRun returns a finished agent run of the earlier specs. */
function anyRun(): string {
  return sql(
    `SELECT id FROM agent_runs WHERE status = 'done' ORDER BY created_at DESC LIMIT 1`,
  );
}

// Slow rounds of the earlier specs can still hold the admission slots.
test.beforeAll(async () => {
  await expect
    .poll(() => sql(`SELECT count(*) FROM agent_runs WHERE status IN ('queued', 'running')`), { timeout: 240_000, intervals: [2000] })
    .toBe("0");
});

test("SCN-OBS-005 the run page shows new events without reload", async ({
  page,
}) => {
  const pr = await open("acme/payments", "e2e-slow-start", "gate");
  const round = await waitRound("acme/payments", pr.number, "running");
  await expect.poll(() => runOf(round), { timeout: 60_000 }).not.toBe("");
  const run = runOf(round);
  await signIn(page, "dev-alice");
  let loads = 0;
  page.on("load", () => loads++);
  await page.goto(`/runs/${run}`);
  const list = page.getByRole("list", { name: "Events" });
  await expect(list).toBeVisible();
  const before = await list.getByRole("listitem").count();
  loads = 0;
  await expect
    .poll(async () => list.getByRole("listitem").count(), { timeout: 120_000 })
    .toBeGreaterThan(before);
  expect(loads).toBe(0);
  // SCN-UI-005: a live event gets the arrival highlight; the status change pulses once.
  await expect(list.locator(".arrive").first()).toBeAttached();
  await expect(page.locator(".ring-pulse")).toHaveCount(1, {
    timeout: 120_000,
  });
});

test("SCN-OBS-010 a run with 5,000 events shows in 2 s, virtualized", async ({
  page,
}) => {
  const run = anyRun();
  sql(`INSERT INTO run_events (run_id, seq, at, type, name, status, preview)
       SELECT '${run}', 500000 + g, now() - interval '1 hour' + g * interval '1 ms', 'tool_call', 'read', 'ok', 'read file ' || g FROM generate_series(1, 5000) g
       ON CONFLICT DO NOTHING`);
  await signIn(page, "octo-admin");
  await page.goto("/");
  const start = Date.now();
  await page.goto(`/runs/${run}`);
  const list = page.getByRole("list", { name: "Events" });
  await expect(list.getByRole("listitem").first()).toBeVisible();
  expect(Date.now() - start).toBeLessThanOrEqual(2000);
  await expect(list).toHaveAttribute("data-virtualized", "");
  expect(await list.getByRole("listitem").count()).toBeLessThan(200);
  // Scrolling to the end shows the last event.
  await list.evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect(list.getByText("read file 5000")).toBeVisible();
});

test("SCN-OBS-007 saving a steering review takes the round out of the queue; filters narrow it", async ({
  page,
}) => {
  const rounds = sql(
    `SELECT id FROM rounds WHERE status = 'done' ORDER BY created_at DESC LIMIT 2`,
  ).split("\n");
  expect(rounds).toHaveLength(2);
  const [a, b] = rounds as [string, string];
  sql(`DELETE FROM steering_reviews`);
  sql(`DELETE FROM steering_signals`);
  sql(
    `INSERT INTO steering_signals (id, review_id, round_id, type, actor_login) SELECT gen_random_uuid(), review_id, id, 'dismissed', 'dev-alice' FROM rounds WHERE id = '${a}'`,
  );
  sql(
    `INSERT INTO steering_signals (id, review_id, round_id, type, actor_login) SELECT gen_random_uuid(), review_id, id, 'overridden', 'octo-admin' FROM rounds WHERE id = '${b}'`,
  );
  await signIn(page, "dev-alice");
  await page.goto("/steering");
  const rows = page.getByRole("row");
  await expect(rows).toHaveCount(3);
  await page.getByLabel("Signal type").selectOption("overridden");
  await expect(rows).toHaveCount(2);
  await expect(page.getByRole("cell").getByText("overridden")).toBeVisible();
  await page.getByLabel("Signal type").selectOption("");
  await page.goto(`/rounds/${a}`);
  await page.getByLabel("Tags").fill("false-positive");
  await page.getByLabel("Note").fill("The API bounds the amount.");
  await page.getByRole("button", { name: "Save steering review" }).click();
  await expect(page.getByText("Steering review by dev-alice.")).toBeVisible();
  await page.goto("/steering");
  await expect(rows).toHaveCount(2);
  await expect(
    page.getByRole("link", {
      name: new RegExp(
        sql(`SELECT left(head_sha, 7) FROM rounds WHERE id = '${b}'`),
      ),
    }),
  ).toBeVisible();
});

test("SCN-OBS-009 the cost view equals the sum of model_usage", async ({
  page,
}) => {
  for (const repo of ["acme/payments", "acme/web"]) {
    sql(`INSERT INTO model_usage (run_id, provider, model, tokens_in, tokens_out, cost_usd, status)
         SELECT ar.id, 'fake', 'fake-large', 1000, 200, 0.125, 200 FROM agent_runs ar JOIN repositories r ON r.id = ar.repository_id
         WHERE r.full_name = '${repo}' ORDER BY ar.created_at LIMIT 1`);
  }
  await signIn(page, "dev-alice");
  await page.goto("/usage");
  const table = page.getByRole("table", { name: "Usage" });
  for (const [group, sel] of [
    ["Repository", "r.full_name"],
    ["Month", "to_char(date_trunc('month', u.at), 'YYYY-MM')"],
  ] as const) {
    await page.getByRole("radio", { name: group }).click();
    const want =
      sql(`SELECT ${sel} AS k, to_char(sum(u.cost_usd), 'FM999990.000000') FROM model_usage u JOIN agent_runs ar ON ar.id = u.run_id
      LEFT JOIN repositories r ON r.id = ar.repository_id GROUP BY k ORDER BY k`).split(
        "\n",
      );
    for (const line of want) {
      const [key, cost] = line.split("|") as [string, string];
      await expect(
        table.locator(`tr[data-key="${key}"] td[data-cost]`),
      ).toHaveAttribute("data-cost", Number(cost).toFixed(6));
    }
  }
});

test("SCN-UI-005 indicators slide, numbers tween and the secret reveal clears the blur", async ({
  page,
}) => {
  await signIn(page, "octo-admin");
  await page.goto("/");
  const nav = page.getByRole("navigation", { name: "Main" });
  await expect(nav.locator("[data-indicator]")).toHaveCount(1);
  const where = () =>
    nav
      .locator("[data-indicator]")
      .evaluate((el) => (el as HTMLElement).style.transform);
  const home = await where();
  await nav.getByRole("link", { name: "Repositories" }).click();
  await expect.poll(where).not.toBe(home);
  await page.goto("/usage");
  const seg = page.getByRole("radiogroup", { name: "Group by" });
  await expect(seg.locator("[data-indicator]")).toHaveCount(1);
  const segAt = await seg
    .locator("[data-indicator]")
    .evaluate((el) => (el as HTMLElement).style.transform);
  await seg.getByRole("radio", { name: "Profile" }).click();
  await expect
    .poll(() =>
      seg
        .locator("[data-indicator]")
        .evaluate((el) => (el as HTMLElement).style.transform),
    )
    .not.toBe(segAt);
  // A number tweens: the shown total passes values between the old and the new one.
  await page.getByRole("radio", { name: "Month" }).click();
  const seen = new Set<string>();
  sql(
    `INSERT INTO model_usage (run_id, provider, model, tokens_in, tokens_out, cost_usd, status) SELECT id, 'fake', 'fake-large', 1, 1, 9.5, 200 FROM agent_runs LIMIT 1`,
  );
  await page.getByRole("radio", { name: "Repository" }).click();
  const total = page.getByText("Total").locator(".tabular");
  for (let i = 0; i < 40; i++) {
    seen.add(await total.innerText());
    await page.waitForTimeout(15);
  }
  expect(seen.size).toBeGreaterThan(2);
  // Secret reveal: the value goes from blur to clear.
  await page.goto("/settings/models");
  await page.getByRole("button", { name: "Add provider" }).click();
  await page.getByLabel("API key", { exact: true }).fill("sk-reveal-0001");
  await page.getByRole("button", { name: "Show value" }).click();
  await expect(page.getByLabel("API key", { exact: true })).toHaveClass(
    /reveal/,
  );
  await expect
    .poll(() =>
      page
        .getByLabel("API key", { exact: true })
        .evaluate((el) => getComputedStyle(el).filter),
    )
    .toBe("none");
});

test("SCN-UI-006 press starts on pointerdown; a failed dismiss rolls back with a shake", async ({
  page,
}) => {
  await signIn(page, "dev-alice");
  const n =
    sql(`SELECT rv.pr_number FROM findings f JOIN reviews rv ON rv.id = f.review_id JOIN repositories r ON r.id = rv.repository_id
    WHERE f.status = 'open' AND r.full_name = 'acme/payments' ORDER BY f.created_at DESC LIMIT 1`);
  await page.goto(`/reviews/acme/payments/${n}`);
  const dismiss = page.getByRole("button", { name: "Dismiss" }).first();
  await dismiss.hover();
  await page.mouse.down();
  await expect(dismiss).toHaveAttribute("data-pressed", "");
  await page.mouse.up();
  await page.route("**/api/v1/findings/*/dismiss", (r) =>
    r.fulfill({
      status: 500,
      contentType: "application/problem+json",
      body: '{"title":"Internal Server Error","status":500}',
    }),
  );
  await page.getByLabel("Reason").fill("the caller checks it");
  await page.getByRole("button", { name: "Dismiss finding" }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "The finding is open again" }),
  ).toBeVisible();
  await expect(page.locator("[data-shake]")).toBeAttached();
  await expect(
    page.getByRole("row").filter({ hasText: "Open" }).first(),
  ).toBeVisible();
});

test("SCN-UI-007 hover preloads route data; a filter change keeps the old rows with a progress line", async ({
  page,
}) => {
  await signIn(page, "octo-admin");
  await page.goto("/");
  const link = page.getByRole("link", { name: /acme\/payments#\d+/ }).first();
  const href = (await link.getAttribute("href")) ?? "";
  const [, owner, name, num] =
    href.match(/\/reviews\/([^/]+)\/([^/]+)\/(\d+)/) ?? [];
  let preloaded = false;
  page.on("request", (r) => {
    if (r.url().includes(`/api/v1/reviews/${owner}/${name}/${num}`))
      preloaded = true;
  });
  await link.hover();
  await page.waitForTimeout(300);
  expect(preloaded).toBe(true);
  await page.goto("/");
  await expect(
    page.getByRole("link", { name: /acme\/payments#\d+/ }).first(),
  ).toBeVisible();
  let release!: () => void;
  const held = new Promise<void>((r) => (release = r));
  await page.route("**/api/v1/reviews?*", async (r) => {
    await held;
    await r.continue();
  });
  await page.getByRole("radio", { name: "Open" }).click();
  await expect(
    page.getByRole("progressbar", { name: "Loading" }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: /acme\/payments#\d+/ }).first(),
  ).toBeVisible();
  release();
  await expect(page.getByRole("progressbar", { name: "Loading" })).toHaveCount(
    0,
  );
});

async function inp(page: Page) {
  return page.evaluate(
    () => (window as unknown as { __inp: number[] }).__inp ?? [],
  );
}

test("SCN-UI-010 INP p75 is 100 ms or less at 4x CPU slowdown; the run page keeps 55 fps at 20 events/s", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const w = window as unknown as { __inp: number[] };
    w.__inp = [];
    const byId = new Map<number, number>();
    new PerformanceObserver((list) => {
      for (const e of list.getEntries() as (PerformanceEventTiming & {
        interactionId?: number;
      })[]) {
        if (!e.interactionId) continue;
        byId.set(
          e.interactionId,
          Math.max(byId.get(e.interactionId) ?? 0, e.duration),
        );
      }
      w.__inp = [...byId.values()];
    }).observe({
      type: "event",
      durationThreshold: 16,
      buffered: true,
    } as PerformanceObserverInit);
  });
  await signIn(page, "octo-admin");
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
  await page.goto("/");
  const nav = page.getByRole("navigation", { name: "Main" });
  for (const name of [
    "Repositories",
    "Steering",
    "Usage",
    "Home",
    "Repositories",
    "Usage",
  ]) {
    await nav.getByRole("link", { name }).click();
    await page.waitForTimeout(400);
  }
  for (const g of ["Repository", "Profile", "Round", "Month"]) {
    await page.getByRole("radio", { name: g }).click();
    await page.waitForTimeout(300);
  }
  await nav.getByRole("link", { name: "Home" }).click();
  for (const s of ["Open", "Merged", "All"]) {
    await page.getByRole("radio", { name: s }).click();
    await page.waitForTimeout(300);
  }
  const durations = (await inp(page)).sort((a, b) => a - b);
  expect(durations.length).toBeGreaterThan(5);
  const p75 = durations[Math.floor(durations.length * 0.75)] ?? 0;
  expect(p75).toBeLessThanOrEqual(100);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 1 });

  // 20 events per second arrive on a running run.
  const run = anyRun();
  sql(`UPDATE agent_runs SET status = 'running' WHERE id = '${run}'`);
  await page.goto(`/runs/${run}`);
  await expect(page.getByRole("list", { name: "Events" })).toBeVisible();
  const feed = spawn("docker", [
    "exec",
    `${stack.project}-postgres-1`,
    "psql",
    "-U",
    "sayso",
    "-d",
    "sayso",
    "-c",
    `DO $$ BEGIN FOR i IN 1..120 LOOP INSERT INTO run_events (run_id, seq, at, type, name, preview) VALUES ('${run}', 900000 + i, now(), 'tool_call', 'read', 'live ' || i); PERFORM pg_sleep(0.05); COMMIT; END LOOP; END $$;`,
  ]);
  await page.waitForTimeout(1500);
  const fps = await page.evaluate(
    () =>
      new Promise<number>((resolve) => {
        let frames = 0;
        const start = performance.now();
        const tick = (t: number) => {
          frames++;
          if (t - start < 3000) requestAnimationFrame(tick);
          else resolve((frames * 1000) / (t - start));
        };
        requestAnimationFrame(tick);
      }),
  );
  await new Promise((r) => feed.on("exit", r));
  sql(`UPDATE agent_runs SET status = 'done' WHERE id = '${run}'`);
  expect(fps).toBeGreaterThanOrEqual(55);
});
